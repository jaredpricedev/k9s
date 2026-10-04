// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	capabilityDeadline        = 8 * time.Second
	capabilityTaskCertManager = "cert-manager"
	capabilityTaskResource    = "resource"
	capabilityTaskHubble      = "hubble"
	capabilityTaskMetrics     = "metrics"
)

type capabilityState string

const (
	capabilityAvailable         capabilityState = "available"
	capabilityAbsent            capabilityState = "API absent"
	capabilityDenied            capabilityState = "permission denied"
	capabilityNotConfigured     capabilityState = "not configured"
	capabilityTLSFailure        capabilityState = "TLS failure"
	capabilityConnectionFailure capabilityState = "connection failure"
	capabilityUnavailable       capabilityState = "unavailable"
	capabilityStale             capabilityState = "stale"
	capabilityCanceled          capabilityState = "canceled"
)

type capabilityCheck struct {
	Name, Source, Detail, Recovery string
	State                          capabilityState
	ObservedAt                     time.Time
}
type capabilityRequest struct {
	Task, Context, Namespace string
	Target                   SelectedResourceTarget
	Hubble                   hubble.Config
}
type capabilitySnapshot struct {
	Request   capabilityRequest
	CheckedAt time.Time
	Checks    []capabilityCheck
}
type relayReadinessProbe func(context.Context, hubble.Config) (string, error)

// capabilityCommand runs only after an explicit diagnostics command. With no
// integration selected it shows choices without issuing discovery or list reads.
func (c *Command) capabilityCommand(line string) {
	args := strings.Fields(line)
	task := ""
	if len(args) > 1 {
		task = strings.ToLower(args[1])
	}
	if task == "tls" {
		task = capabilityTaskCertManager
	}
	if task == "" {
		text := "Capability diagnostics\n\nChoose only the integration needed for your task:\n  :diagnostics metrics\n  " +
			":diagnostics flux\n  :diagnostics cert-manager\n  :diagnostics hubble\n  :diagnostics resource (selected API " +
			"object)\n\nEach check makes bounded read-only requests for the current context. No integrations are scanned during " +
			"ordinary browsing. r repeats an observation; Esc cancels it. Results establish readable prerequisites, not " +
			"workload or traffic health.\n\nHubble checks the configured Relay address and transport before observation. It " +
			"never starts a port-forward."
		help := NewDetails(c.app, "Capability diagnostics", "choose a task", contentInspection, true).Update(text)
		if err := c.app.inject(help, false); err != nil {
			c.app.Flash().Err(err)
		}
		return
	}
	if len(args) > 2 || !validCapabilityTask(task) {
		c.app.Flash().Err(fmt.Errorf("use :diagnostics metrics, flux, cert-manager, hubble, or resource"))
		return
	}
	request := capabilityRequest{Task: task, Context: c.app.Config.ActiveContextName(), Namespace: c.app.Config.ActiveNamespace()}
	if task == capabilityTaskResource {
		viewer, ok := c.app.Content.Top().(actionOwner)
		if !ok {
			c.app.Flash().Err(fmt.Errorf("open a resource list and select an API object first"))
			return
		}
		request.Target = actionTarget(viewer, request.Context)
		if err := request.Target.Err(); err != nil {
			c.app.Flash().Err(err)
			return
		}
	}
	if task == capabilityTaskHubble {
		cfg, err := c.app.Config.CurrentContext()
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
		request.Hubble = cfg.Hubble
	}
	var reader dynamic.Interface
	if task != capabilityTaskHubble {
		if c.app.Conn() == nil {
			c.app.Flash().Err(fmt.Errorf("select a configured Kubernetes context first"))
			return
		}
		pinned, err := pinInspectionConnection(c.app.Conn())
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
		reader, err = pinned.DynDial()
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
	}
	view := &capabilityDetails{
		Details: NewDetails(c.app, "Capability diagnostics", task, contentInspection, true).Update("Checking prerequisites for " + task + "..."),
		request: request, reader: reader, probe: hubble.ProbeReadiness,
	}
	if err := c.app.inject(view, false); err != nil {
		c.app.Flash().Err(err)
	}
}

func validCapabilityTask(task string) bool {
	switch task {
	case capabilityTaskMetrics, "flux", capabilityTaskCertManager, capabilityTaskHubble, capabilityTaskResource:
		return true
	}
	return false
}

type capabilityDetails struct {
	*Details
	request    capabilityRequest
	reader     dynamic.Interface
	probe      relayReadinessProbe
	cancel     context.CancelFunc
	generation uint64
	snapshot   capabilitySnapshot
	fresh      bool
}

func (d *capabilityDetails) Init(ctx context.Context) error {
	if err := d.Details.Init(ctx); err != nil {
		return err
	}
	d.actions.Add(ui.KeyR, ui.NewKeyAction("Check again", func(*tcell.EventKey) *tcell.EventKey { d.refreshCapabilities(); return nil }, true))
	if d.request.Task == capabilityTaskHubble {
		d.actions.Add(tcell.KeyEnter, ui.NewKeyAction("Open Relay status", func(*tcell.EventKey) *tcell.EventKey {
			if d.request.Context != d.app.Config.ActiveContextName() {
				d.app.Flash().Warn("Context changed; reopen diagnostics")
				return nil
			}
			contextConfig, err := d.app.Config.CurrentContext()
			if err != nil || contextConfig.Hubble != d.request.Hubble {
				d.app.Flash().Warn("Relay configuration changed; check again before opening Hubble")
				return nil
			}
			if !d.relayReady(time.Now()) {
				d.app.Flash().Warn("Run a fresh successful Relay check before opening Hubble")
				return nil
			}
			if err := d.app.inject(newHubbleView(hubble.Scope{Title: "Relay readiness checked"}, true), false); err != nil {
				d.app.Flash().Err(err)
			}
			return nil
		}, true))
	}
	return nil
}
func (d *capabilityDetails) Start() {
	d.app.Prompt().SetModel(d.cmdBuff)
	d.app.Styles.RemoveListener(d.Details)
	d.app.Styles.AddListener(d.Details)
	if d.snapshot.CheckedAt.IsZero() {
		d.refreshCapabilities()
	} else {
		d.fresh = false
		d.Update(renderCapabilities(d.snapshot, true))
	}
}
func (d *capabilityDetails) Stop() {
	d.fresh = false
	d.generation++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.Details.Stop()
}
func (d *capabilityDetails) current(generation uint64) bool {
	return d.generation == generation && d.request.Context == d.app.Config.ActiveContextName() && d.app.Content.Top() == d
}
func (d *capabilityDetails) relayReady(now time.Time) bool {
	return d.fresh && len(d.snapshot.Checks) > 0 &&
		d.snapshot.Checks[0].State == capabilityAvailable &&
		!d.snapshot.CheckedAt.IsZero() && now.Sub(d.snapshot.CheckedAt) <= client.MetricMaxAge
}

func (d *capabilityDetails) refreshCapabilities() {
	if d.request.Context != d.app.Config.ActiveContextName() {
		d.app.Flash().Warn("Context changed; reopen diagnostics")
		return
	}
	if d.request.Task == capabilityTaskHubble {
		contextConfig, err := d.app.Config.CurrentContext()
		if err != nil {
			d.app.Flash().Err(err)
			return
		}
		d.request.Hubble = contextConfig.Hubble
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	d.fresh = false
	generation := d.generation
	ctx, cancel := context.WithTimeout(context.Background(), capabilityDeadline)
	d.cancel = cancel
	request := d.request
	if !d.snapshot.CheckedAt.IsZero() {
		d.Update(renderCapabilities(d.snapshot, true) + "\n\nChecking again...")
	}
	go func() {
		defer cancel()
		snapshot := collectCapabilities(ctx, d.reader, request, d.probe)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		d.app.QueueUpdateDraw(func() {
			if d.current(generation) {
				d.snapshot = snapshot
				d.fresh = true
				d.Update(renderCapabilities(snapshot, false))
			}
		})
	}()
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func collectCapabilities(ctx context.Context, reader dynamic.Interface, request capabilityRequest, probe relayReadinessProbe) capabilitySnapshot {
	snapshot := capabilitySnapshot{Request: request, CheckedAt: time.Now()}
	if request.Task == capabilityTaskHubble {
		snapshot.Checks = []capabilityCheck{checkRelayReadiness(ctx, request.Hubble, probe)}
		return snapshot
	}
	namespace := request.Namespace
	if client.IsAllNamespaces(namespace) {
		namespace = ""
	}
	switch request.Task {
	case capabilityTaskMetrics:
		snapshot.Checks = append(snapshot.Checks, checkMetricsPrerequisite(ctx, reader))
	case "flux":
		for _, gvr := range []schema.GroupVersionResource{
			{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"},
			{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
			{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
		} {
			snapshot.Checks = append(snapshot.Checks, checkAPIPrerequisite(ctx, reader, gvr, namespace))
		}
	case capabilityTaskCertManager:
		for _, gvr := range []schema.GroupVersionResource{
			{Group: "cert-manager.io", Version: "v1", Resource: "certificates"},
			{Group: "cert-manager.io", Version: "v1", Resource: "issuers"},
		} {
			snapshot.Checks = append(snapshot.Checks, checkAPIPrerequisite(ctx, reader, gvr, namespace))
		}
	case capabilityTaskResource:
		target := request.Target
		if err := target.Err(); err != nil {
			snapshot.Checks = append(snapshot.Checks, capabilityCheck{
				Name: "Selected API object", State: capabilityUnavailable, Detail: err.Error(),
				Recovery: "Open a resource list and select a current API object", ObservedAt: time.Now(),
			})
			return snapshot
		}
		check := capabilityCheck{Name: "Selected API object", Source: "GET " + target.GVR.String() + " " + target.Path(), ObservedAt: time.Now()}
		if reader == nil {
			check.State = capabilityUnavailable
			check.Detail = "Kubernetes client unavailable"
		} else {
			var object *unstructured.Unstructured
			err := ctx.Err()
			if err == nil {
				object, err = reader.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
			}
			if err == nil {
				err = ctx.Err()
			}
			if err == nil {
				err = verifySelectedIdentity(target, object)
			}
			check.State = classifyCapabilityError(err)
			if err != nil {
				check.Detail = err.Error()
				if apierrors.IsNotFound(err) {
					check.State = capabilityUnavailable
					check.Detail = "Selected object no longer exists; refresh the resource list and select its current identity"
				}
			} else {
				check.Detail = "Selected object is readable; identity checked"
				if target.UID == "" {
					check.Detail = "Object is readable; selected UID is unknown, replacement identity cannot be verified"
				}
			}
		}
		check.Recovery = capabilityRecovery(check.State, target.GVR.String(), target.Namespace)
		snapshot.Checks = append(snapshot.Checks, check)
	}
	return snapshot
}

func checkAPIPrerequisite(ctx context.Context, reader dynamic.Interface, gvr schema.GroupVersionResource, namespace string) capabilityCheck {
	check := capabilityCheck{Name: gvr.Resource, Source: "LIST " + gvr.String() + " namespace=" + namespace + " limit=1", ObservedAt: time.Now()}
	var err error
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if reader == nil {
		err = errors.New("Kubernetes client unavailable")
	} else {
		_, err = reader.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
	}
	if err == nil {
		err = ctx.Err()
	}
	check.State = classifyCapabilityError(err)
	if err != nil {
		check.Detail = err.Error()
	} else {
		check.Detail = "API is served and the selected scope permits list reads; object readiness was not checked"
	}
	check.Recovery = capabilityRecovery(check.State, gvr.Resource+"."+gvr.Group, namespace)
	return check
}

func checkMetricsPrerequisite(ctx context.Context, reader dynamic.Interface) capabilityCheck {
	gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	check := capabilityCheck{Name: "Node metrics", Source: "LIST " + client.NodeMetricsSource + " limit=1", ObservedAt: time.Now()}
	var samples *unstructured.UnstructuredList
	var err error
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if reader == nil {
		err = errors.New("Kubernetes client unavailable")
	} else {
		samples, err = reader.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	}
	if err == nil {
		err = ctx.Err()
	}
	check.State = classifyCapabilityError(err)
	if err != nil {
		check.Detail = err.Error()
	} else if samples == nil || len(samples.Items) == 0 {
		check.State = capabilityUnavailable
		check.Detail = "API responded without a node observation"
	} else {
		sample := samples.Items[0]
		timestamp, _, _ := unstructured.NestedString(sample.Object, "timestamp")
		observed, parseErr := time.Parse(time.RFC3339Nano, timestamp)
		_, cpu, _ := unstructured.NestedString(sample.Object, "usage", "cpu")
		_, memory, _ := unstructured.NestedString(sample.Object, "usage", "memory")
		if parseErr != nil || !cpu || !memory {
			check.State = capabilityUnavailable
			check.Detail = "Observation lacks timestamp or CPU/memory usage"
		} else {
			check.ObservedAt = observed
			if time.Since(observed) > client.MetricMaxAge {
				check.State = capabilityStale
				check.Detail = "Observation is older than two minutes"
			} else {
				check.Detail = "A timestamped node usage sample is readable; measured zero is valid. This bounded check does not establish cluster health."
			}
		}
	}
	check.Recovery = capabilityRecovery(check.State, "nodes.metrics.k8s.io", "")
	return check
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func checkRelayReadiness(ctx context.Context, cfg hubble.Config, probe relayReadinessProbe) capabilityCheck {
	check := capabilityCheck{Name: "Hubble Relay", Source: "Observer.ServerStatus " + cfg.Address, ObservedAt: time.Now()}
	if cfg.Address == "" {
		check.State = capabilityNotConfigured
		check.Detail = "This context has no Hubble Relay address"
		check.Recovery = "Set k9s.hubble.address in this context's config.yaml. For TLS set caFile and serverName; set certFile and keyFile " +
			"together when Relay requires mTLS. Then press r. No port-forward is started."
		return check
	}
	if _, err := cfg.Credentials(); err != nil {
		check.State = capabilityTLSFailure
		check.Detail = err.Error()
		check.Recovery = "Correct the CA PEM, certificate/key pair, serverName and TLS settings in this context's config.yaml; press r after " +
			"reopening diagnostics to capture the changed configuration."
		return check
	}
	if probe == nil {
		probe = hubble.ProbeReadiness
	}
	version, err := probe(ctx, cfg)
	if err == nil {
		err = ctx.Err()
	}
	check.State = classifyCapabilityError(err)
	if err != nil {
		check.Detail = err.Error()
		switch check.State {
		case capabilityDenied:
			check.Recovery = "Relay denied ServerStatus. Verify Relay authentication/authorization and the configured mTLS client certificate " +
				"identity; then reopen diagnostics and press r."
		case capabilityTLSFailure:
			check.Recovery = "Verify the Relay certificate matches serverName and the configured trusted CA; configure a matching " +
				"certificate/key if Relay requires mTLS. Reopen diagnostics after changing context settings, then press r."
		default:
			check.Recovery = "Confirm the exact Relay address is reachable and accepting this configured transport. Reopen diagnostics after " +
				"changing context settings, then press r."
		}
	} else {
		check.Detail = "Relay answered ServerStatus (version " + version + "); endpoint readiness does not imply complete node coverage or healthy traffic."
		if cfg.Plaintext {
			check.Detail += " Explicit plaintext transport: TLS identity was not verified."
		} else {
			check.Detail += " TLS certificate verification succeeded."
		}
		check.Recovery = "Enter opens Relay status; select a workload or pod before flow observation."
	}
	return check
}

func classifyCapabilityError(err error) capabilityState {
	if err == nil {
		return capabilityAvailable
	}
	if errors.Is(err, context.Canceled) {
		return capabilityCanceled
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.Unauthenticated {
		return capabilityDenied
	}
	if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
		return capabilityAbsent
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	lowered := strings.ToLower(err.Error())
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		strings.Contains(lowered, "tls") || strings.Contains(lowered, "x509") || strings.Contains(lowered, "certificate") {
		return capabilityTLSFailure
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.Unavailable || status.Code(err) == codes.DeadlineExceeded {
		return capabilityConnectionFailure
	}
	return capabilityUnavailable
}

func capabilityRecovery(state capabilityState, resource, namespace string) string {
	scope := " --all-namespaces"
	if namespace != "" {
		scope = " -n " + namespace
	}
	switch state {
	case capabilityAbsent:
		return "Verify the API/CRD is installed and serves this version: kubectl api-resources. Install or upgrade the integration, then press r."
	case capabilityDenied:
		return "Ask for the exact read permission; verify with kubectl auth can-i list " + resource + scope + ". Reopen the selected context after credentials change; press r."
	case capabilityStale:
		return "Check metrics-server scrape timestamps and its connection to kubelets; press r for a fresh observation."
	case capabilityUnavailable:
		return "The API is configured but returned no usable response. Check its APIService/controller availability and reported error; press r."
	case capabilityTLSFailure:
		return "Verify this context's API server certificate, trusted CA, hostname and client credentials, then reopen diagnostics."
	case capabilityConnectionFailure:
		return "Verify the selected context's API endpoint, network access and timeout; reopen diagnostics or press r."
	case capabilityCanceled:
		return "The check was canceled; press r to retry."
	default:
		return "Read prerequisite verified; this is an observation, not a health verdict. r makes a new uncached check."
	}
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func renderCapabilities(snapshot capabilitySnapshot, retained bool) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Capability diagnostics: %s\nContext: %s\nNamespace: %s\nChecked: %s\n",
		snapshot.Request.Task, snapshot.Request.Context, snapshot.Request.Namespace, snapshot.CheckedAt.UTC().Format(time.RFC3339))
	if retained {
		fmt.Fprintln(&text, "STALE retained observation; r makes a new check. Results below have not been revalidated.")
	}
	fmt.Fprintln(&text, "Read-only, on demand, no application discovery cache. Each API list requests limit=1; the complete check has an eight-second deadline.")
	for _, check := range snapshot.Checks {
		fmt.Fprintf(&text, "\n%s: %s\n  Source: %s\n  Observed: %s\n  %s\n  Next: %s\n",
			check.Name, check.State, check.Source, check.ObservedAt.UTC().Format(time.RFC3339), check.Detail, check.Recovery)
	}
	if snapshot.Request.Task == capabilityTaskHubble {
		fmt.Fprintln(&text, "\nPort-forward is separate: explicitly select its context, namespace and Relay target, own its process and local "+
			"port, stop it on exit, report collisions and loss of connection. Diagnostics never creates one.")
	}
	return text.String()
}
