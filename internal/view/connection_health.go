// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const connectionHealthDeadline = 8 * time.Second

type connectionHealthState string

const (
	connectionReadable    connectionHealthState = "READABLE"
	connectionCredentials connectionHealthState = "CREDENTIALS"
	connectionDenied      connectionHealthState = "DENIED"
	connectionTLS         connectionHealthState = "TLS FAILED"
	connectionUnreachable connectionHealthState = "UNREACHABLE"
	connectionDiscovery   connectionHealthState = "API UNAVAILABLE"
	connectionTimeout     connectionHealthState = "TIMED OUT"
	connectionCanceled    connectionHealthState = "CANCELED"
	connectionUnknown     connectionHealthState = "UNKNOWN"
	connectionSkipped     connectionHealthState = "NOT CHECKED"
)

type connectionHealthRequest struct {
	Context, Namespace string
	Revision           uint64
}

type connectionHealthCheck struct {
	Name, Source string
	State        connectionHealthState
	ObservedAt   time.Time
}

type connectionHealthSnapshot struct {
	Request   connectionHealthRequest
	CheckedAt time.Time
	Checks    []connectionHealthCheck
}

type connectionHealthProbe func(context.Context, connectionHealthRequest) connectionHealthSnapshot

// connectionHealthCommand always opens a retained read-only view, including
// when the connection is down or cannot supply configuration.
func (c *Command) connectionHealthCommand(line string) {
	if len(strings.Fields(line)) != 1 {
		c.app.Flash().Warn("Use :connection or :connection-health")
		return
	}
	request := connectionHealthRequest{
		Context: c.app.Config.ActiveContextName(), Namespace: c.app.Config.ActiveNamespace(),
		Revision: c.app.Config.DestinationRevision(),
	}
	d := &connectionHealthDetails{
		Details: NewDetails(c.app, "Connection and session", request.Context, contentInspection, true).Update("Checking this destination..."),
		request: request, snapshot: connectionHealthSnapshot{Request: request},
	}
	if err := c.app.inject(d, false); err != nil {
		c.app.Flash().Err(err)
	}
}

type connectionHealthDetails struct {
	*Details
	request        connectionHealthRequest
	probe          connectionHealthProbe // Test seam; production reacquires config for every explicit retry.
	cancel         context.CancelFunc
	generation     uint64
	snapshot       connectionHealthSnapshot
	previous       connectionHealthSnapshot
	started        bool
	stale          bool
	prepareSession sessionRefreshPrepare
	sessionNotice  string
	sessionState   sessionReconnectState
}

func (*connectionHealthDetails) CompactWorkspace() bool { return true }

func (d *connectionHealthDetails) Init(ctx context.Context) error {
	if err := d.Details.Init(ctx); err != nil {
		return err
	}
	d.actions.Add(ui.KeyR, ui.NewKeyAction("Retry connection checks", func(*tcell.EventKey) *tcell.EventKey {
		d.refreshConnectionHealth()
		return nil
	}, true))
	d.actions.Add(ui.KeyShiftR, ui.NewKeyAction("Reconnect running session", func(*tcell.EventKey) *tcell.EventKey {
		d.refreshSession()
		return nil
	}, true))
	return nil
}

func (d *connectionHealthDetails) Start() {
	d.started = true
	d.app.Prompt().SetModel(d.cmdBuff)
	d.app.Styles.RemoveListener(d.Details)
	d.app.Styles.AddListener(d.Details)
	if d.snapshot.CheckedAt.IsZero() {
		d.refreshConnectionHealth()
	} else {
		d.stale = true
		d.Update(d.renderHealth(true))
	}
}

func (d *connectionHealthDetails) Stop() {
	d.started = false
	d.stale = true
	d.generation++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.Details.Stop()
}

func (d *connectionHealthDetails) current(generation uint64) bool {
	return d.started && d.generation == generation && d.app.Content.Top() == d &&
		d.request.Context == d.app.Config.ActiveContextName() && d.request.Namespace == d.app.Config.ActiveNamespace() &&
		d.request.Revision == d.app.Config.DestinationRevision()
}

func (d *connectionHealthDetails) refreshConnectionHealth() {
	d.sessionState, d.sessionNotice = "", ""
	if d.request.Context != d.app.Config.ActiveContextName() || d.request.Namespace != d.app.Config.ActiveNamespace() ||
		d.request.Revision != d.app.Config.DestinationRevision() {
		d.Update(renderConnectionHealth(d.snapshot, d.previous, time.Now(), true) +
			"\n\nDestination changed. Reopen :connection to check the current context and namespace.")
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	d.stale = true
	generation, request := d.generation, d.request
	ctx, cancel := context.WithTimeout(d.app.sessionContext(), connectionHealthDeadline)
	d.cancel = cancel
	probe := d.probe
	if probe == nil {
		var config *client.Config
		var err error
		if conn := d.app.Conn(); conn != nil {
			config, err = conn.Config().PinnedDiagnosticConfig(request.Context)
		} else {
			err = errors.New("no configured connection")
		}
		probe = func(ctx context.Context, request connectionHealthRequest) connectionHealthSnapshot {
			return collectConnectionHealth(ctx, request, config, err)
		}
	}
	if !d.snapshot.CheckedAt.IsZero() {
		d.Update(renderConnectionHealth(d.snapshot, d.previous, time.Now(), true) + "\n\nRetrying with newly loaded configuration...")
	}
	go func() {
		defer cancel()
		snapshot := boundedConnectionHealth(ctx, request, probe)
		if errors.Is(ctx.Err(), context.Canceled) || !d.app.IsRunning() {
			return
		}
		d.app.QueueUpdateDraw(func() {
			if d.current(generation) {
				d.acceptSnapshot(snapshot)
				d.stale = false
				d.Update(renderConnectionHealth(d.snapshot, d.previous, time.Now(), false))
			}
		})
	}()
}

// Retain the last readable observation at its original time if a subsequent
// request fails. A retry failure never makes yesterday's success current.
//
//nolint:gocritic // Keep captured observation values independent across worker and UI boundaries.
func (d *connectionHealthDetails) acceptSnapshot(snapshot connectionHealthSnapshot) {
	if connectionHealthComplete(d.snapshot) {
		d.previous = d.snapshot
	}
	d.snapshot = snapshot
	if connectionHealthComplete(snapshot) {
		d.previous = connectionHealthSnapshot{}
	}
}

//nolint:gocritic // Read an immutable observation value without exposing retained UI state by pointer.
func connectionHealthComplete(snapshot connectionHealthSnapshot) bool {
	if len(snapshot.Checks) == 0 {
		return false
	}
	for _, check := range snapshot.Checks {
		if check.State != connectionReadable && check.State != connectionSkipped {
			return false
		}
	}
	return true
}

// Configured credential helpers may fail to honor request cancellation. This
// boundary keeps the view bounded too; late results remain isolated and are
// discarded. No helper is invoked except the one configured for this actor.
func boundedConnectionHealth(ctx context.Context, request connectionHealthRequest, probe connectionHealthProbe) connectionHealthSnapshot {
	result := make(chan connectionHealthSnapshot, 1)
	go func() { result <- probe(ctx, request) }()
	select {
	case snapshot := <-result:
		return snapshot
	case <-ctx.Done():
		return connectionHealthFailure(request, classifyConnectionHealthError(ctx.Err()), time.Now())
	}
}

func collectConnectionHealth(ctx context.Context, request connectionHealthRequest, config *client.Config, pinErr error) connectionHealthSnapshot {
	if pinErr != nil || config == nil {
		return connectionHealthFailure(request, connectionUnknown, time.Now())
	}
	cfg, err := config.RESTConfig()
	if err != nil {
		return connectionHealthFailure(request, classifyConnectionHealthError(err), time.Now())
	}
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = connectionHealthDeadline
	if cfg.ExecProvider != nil {
		// Explicit diagnostics must not present an interactive login prompt.
		cfg.ExecProvider = cfg.ExecProvider.DeepCopy()
		cfg.ExecProvider.InteractiveMode = "Never"
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return connectionHealthFailure(request, classifyConnectionHealthError(err), time.Now())
	}
	return collectConnectionHealthClients(ctx, request, typed)
}

func collectConnectionHealthClients(ctx context.Context, request connectionHealthRequest, typed kubernetes.Interface) connectionHealthSnapshot {
	snapshot := connectionHealthSnapshot{Request: request, CheckedAt: time.Now()}
	versionCheck := connectionHealthCheck{Name: "API reachability", Source: "GET /version"}
	var info version.Info
	err := ctx.Err()
	if err == nil {
		restClient := typed.Discovery().RESTClient()
		if restClient == nil {
			err = errors.New("no discovery client")
		} else {
			var body []byte
			body, err = restClient.Get().AbsPath("/version").Do(ctx).Raw()
			if err == nil {
				err = json.Unmarshal(body, &info)
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	versionCheck.State = classifyConnectionHealthError(err)
	if err == nil && info.GitVersion == "" {
		versionCheck.State = connectionDiscovery
	}
	versionCheck.ObservedAt = time.Now()
	snapshot.Checks = append(snapshot.Checks, versionCheck)
	namespaceCheck := connectionHealthCheck{Name: "Selected namespace", Source: "LIST pods limit=1", State: connectionSkipped, ObservedAt: time.Now()}
	if !client.IsClusterWide(request.Namespace) && request.Namespace != client.NotNamespaced {
		err = ctx.Err()
		if err == nil {
			_, err = typed.CoreV1().Pods(request.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
		}
		if err == nil {
			err = ctx.Err()
		}
		namespaceCheck.State = classifyConnectionHealthError(err)
		namespaceCheck.ObservedAt = time.Now()
	}
	snapshot.Checks = append(snapshot.Checks, namespaceCheck)
	return snapshot
}

func connectionHealthFailure(request connectionHealthRequest, state connectionHealthState, at time.Time) connectionHealthSnapshot {
	return connectionHealthSnapshot{Request: request, CheckedAt: at, Checks: []connectionHealthCheck{
		{Name: "Client configuration", Source: "configured context", State: state, ObservedAt: at},
	}}
}

// Only categories and authored recovery guidance reach the view. Raw errors can
// contain bearer tokens, credential-helper stderr, kubeconfig values and URLs.
func classifyConnectionHealthError(err error) connectionHealthState {
	if err == nil {
		return connectionReadable
	}
	if errors.Is(err, context.Canceled) {
		return connectionCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return connectionTimeout
	}
	if apierrors.IsUnauthorized(err) {
		return connectionCredentials
	}
	if apierrors.IsForbidden(err) {
		return connectionDenied
	}
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	if errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &certificate) ||
		errors.As(err, &verification) || errors.As(err, &record) {
		return connectionTLS
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "getting credentials") || strings.Contains(lower, "exec plugin") ||
		strings.Contains(lower, "exec: executable") || strings.Contains(lower, "credential") || strings.Contains(lower, "auth-provider") {
		return connectionCredentials
	}
	if strings.Contains(lower, "x509:") || strings.Contains(lower, "tls:") || strings.Contains(lower, "certificate") ||
		strings.Contains(lower, "unable to load root certificates") || strings.Contains(lower, "private key") {
		return connectionTLS
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return connectionTimeout
		}
		return connectionUnreachable
	}
	var discoveryErr *discovery.ErrGroupDiscoveryFailed
	var syntaxErr *json.SyntaxError
	var valueErr *json.UnmarshalTypeError
	if errors.As(err, &discoveryErr) || apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) ||
		apierrors.IsServiceUnavailable(err) || strings.Contains(lower, "no discovery client") || errors.As(err, &syntaxErr) || errors.As(err, &valueErr) {
		return connectionDiscovery
	}
	return connectionUnknown
}

func connectionHealthRecovery(state connectionHealthState) string {
	switch state {
	case connectionReadable:
		return "This bounded read succeeded; workload health and other permissions were not checked."
	case connectionCredentials:
		return "The configured credentials were rejected or could not be obtained. Renew them through your usual provider, then press r. No login is started here."
	case connectionDenied:
		return "The API denied this read. Check permission for the named source and scope; renewing credentials alone may not grant access."
	case connectionTLS:
		return "Check the API certificate, trusted CA, server name and configured client certificate/key. " +
			"Keep certificate verification enabled; press r after correcting configuration."
	case connectionUnreachable:
		return "Check the configured API destination, DNS, network/VPN and proxy. Press r when it is reachable."
	case connectionDiscovery:
		return "The requested Kubernetes API or discovery response was unavailable. Check API serving and version compatibility; this does not establish workload health."
	case connectionTimeout:
		return "The eight-second check did not finish. Check network/API latency or the configured credential helper, then press r."
	case connectionCanceled:
		return "The check was canceled; no current result was established. Press r for a new observation."
	case connectionSkipped:
		return "Choose a single named namespace to check its pod-list permission. No all-namespace list was issued."
	default:
		return "The configured client could not establish this observation. Select a configured context or correct its kubeconfig/credential setup, then reopen :connection."
	}
}

//nolint:gocritic // Rendering reads copied observations and must not mutate retained snapshot headers.
func renderConnectionHealth(snapshot, previous connectionHealthSnapshot, now time.Time, stale bool) string {
	return renderConnectionHealthHeadline(snapshot, previous, now, stale, "")
}

//nolint:gocritic // Rendering reads immutable observation copies.
func renderConnectionHealthHeadline(snapshot, previous connectionHealthSnapshot, now time.Time, stale bool, headline string) string {
	var out strings.Builder
	out.WriteString("CONNECTION AND SESSION\n")
	fmt.Fprintf(&out, "Context %s · namespace %s\n", connectionHealthSafeLabel(snapshot.Request.Context), connectionHealthSafeLabel(snapshot.Request.Namespace))
	out.WriteString("r retry checks · R reconnect session · Esc back\n")
	if headline != "" {
		out.WriteString(headline + "\n")
	}
	if stale {
		out.WriteString("RETAINED observation · press r to retry\n")
	}
	if snapshot.CheckedAt.IsZero() {
		out.WriteString("No connection observation yet.\n")
	} else {
		fmt.Fprintf(&out, "Observed %s · %s ago\n", snapshot.CheckedAt.UTC().Format(time.RFC3339), connectionHealthAge(now, snapshot.CheckedAt))
	}
	out.WriteString("\n")
	for _, check := range snapshot.Checks {
		fmt.Fprintf(&out, "%s  %s · %s ago\n", check.State, check.Name, connectionHealthAge(now, check.ObservedAt))
	}
	if !previous.CheckedAt.IsZero() {
		fmt.Fprintf(&out, "\nPREVIOUS READABLE OBSERVATION · %s ago · retained, not current\n", connectionHealthAge(now, previous.CheckedAt))
		for _, check := range previous.Checks {
			fmt.Fprintf(&out, "%s: %s (%s ago)\n", check.Name, check.State, connectionHealthAge(now, check.ObservedAt))
		}
	}
	out.WriteString("\nNEXT CHECKS\n")
	for _, check := range snapshot.Checks {
		if check.State != connectionReadable {
			fmt.Fprintf(&out, "%s: %s\n", check.Name, connectionHealthRecovery(check.State))
		}
	}
	if connectionHealthComplete(snapshot) {
		out.WriteString("These bounded reads succeeded. Workload health and other permissions were not checked.\n")
	}
	out.WriteString("\nSOURCES\n")
	for _, check := range snapshot.Checks {
		fmt.Fprintf(&out, "%s: %s\n", check.Name, check.Source)
	}
	out.WriteString("\nRetry reloads configuration for this named context without changing the running session. " +
		"R checks fresh configuration, then reconnects this same named context. Workspace and navigation stay available on Back; failed setup keeps the existing session.\n" +
		"Global kubeconfig current-context changes do not redirect these checks. " +
		"Retry checks API version and one selected-namespace pod-list read; reconnect also checks API discovery. " +
		"A readable version endpoint alone does not prove credentials or permissions are valid.")
	return out.String()
}

func connectionHealthAge(now, at time.Time) string {
	if at.IsZero() {
		return workspaceUnknown
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

func connectionHealthSafeLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, label)
	if label == "" {
		return "(none)"
	}
	return label
}
