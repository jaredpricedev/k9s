// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"k8s.io/client-go/dynamic"
)

const (
	providersCommand       = "providers"
	providerVersionCommand = "version"
	providerClientFlag     = "--client"
	providerFluxCommand    = "flux"
)

func (c *Command) providerCommand(line string) {
	words := strings.Fields(line)
	if len(words) == 1 {
		text := "Provider checks\n\nChoose only the tools or APIs needed for this task:\n" +
			"  :providers git helm kustomize\n  :providers kubectl\n  :providers api:metrics api:flux\n" +
			"  :providers api:cert-manager api:resource\n\n" +
			"Optional CLI checks: flux, cilium, hubble. Each named tool runs only its bounded client version command. " +
			"API checks inspect read permission in the captured context and namespace. Availability does not establish workload health.\n\n" +
			"No provider checks run while browsing. Existing plugins and custom jumps retain their own bindings. " +
			"r repeats this explicit check; Esc cancels and returns. Checks do not run install, login or context-switch commands."
		if err := c.app.inject(NewDetails(c.app, "Provider checks", "choose providers", contentInspection, true).Update(text), false); err != nil {
			c.app.Flash().Err(err)
		}
		return
	}
	if len(words) > 9 {
		c.app.Flash().Warn("Choose at most eight providers per check")
		return
	}
	scope := provider.Scope{Context: c.app.Config.ActiveContextName(), Namespace: c.app.Config.CachedNamespace(), Revision: c.app.Config.DestinationRevision()}
	var cfg *client.Config
	if conn := c.app.Conn(); conn != nil && conn.Config() != nil {
		cfg = conn.Config().Snapshot(scope.Context)
	}
	var target SelectedResourceTarget
	if owner, ok := c.app.Content.Top().(actionOwner); ok {
		target = actionTarget(owner, scope.Context)
		if target.GVR != nil {
			scope.GVR = target.GVR.String()
		}
		scope.Name, scope.UID, scope.TargetNamespace = target.Name, string(target.UID), target.Namespace
	}
	specs, err := explicitProviderSpecs(words[1:], cfg, capabilityRequest{Context: scope.Context, Namespace: scope.Namespace, Target: target})
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	d := &providerDetails{
		Details: NewDetails(c.app, "Provider checks", scope.Context, contentInspection, true).Update("Checking only the requested providers..."),
		scope:   scope, specs: specs, discover: provider.Discover,
	}
	if err := c.app.inject(d, false); err != nil {
		c.app.Flash().Err(err)
	}
}

//nolint:gocritic // Each adapter retains its own immutable selected-resource request.
func explicitProviderSpecs(names []string, cfg *client.Config, request capabilityRequest) ([]provider.Spec, error) {
	versions := map[string][]string{
		"git": {"--version"}, "helm": {providerVersionCommand, "--short"}, "kustomize": {providerVersionCommand},
		"kubectl": {providerVersionCommand, providerClientFlag, "-o", "json"}, providerFluxCommand: {providerVersionCommand, providerClientFlag},
		"cilium": {providerVersionCommand, providerClientFlag}, "hubble": {providerVersionCommand},
	}
	seen := map[string]bool{}
	var specs []provider.Spec
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		limits := provider.Limits{Timeout: 3 * time.Second, StdoutBytes: 4096, StderrBytes: 4096}
		if args, ok := versions[name]; ok {
			specs = append(specs, provider.Spec{ID: name, Executable: name, VersionArgs: args, Limits: limits})
			continue
		}
		task := strings.TrimPrefix(name, "api:")
		if !strings.HasPrefix(name, "api:") || !validCapabilityTask(task) || task == capabilityTaskHubble {
			return nil, fmt.Errorf("unknown provider %q; use :providers to see supported checks", name)
		}
		captured := request
		captured.Task = task
		specs = append(specs, provider.Spec{ID: name, Limits: limits, Probe: providerAPIProbe(cfg, captured)})
	}
	return specs, nil
}

//nolint:gocritic // Capture by value before any asynchronous API read.
func providerAPIProbe(cfg *client.Config, request capabilityRequest) provider.Probe {
	return func(ctx context.Context, scope provider.Scope) (provider.Observation, error) {
		observation := provider.Observation{Source: "captured Kubernetes API"}
		if scope.Context != request.Context || scope.Namespace != request.Namespace {
			return observation, errors.New("provider destination changed")
		}
		if request.Task == capabilityTaskResource && request.Target.GVR != nil && request.Target.GVR.GVR() == client.SecGVR.GVR() {
			return observation, errors.New("Secret content excluded from provider checks")
		}
		if cfg == nil {
			return observation, errors.New("captured connection unavailable")
		}
		config, err := cfg.RESTConfig()
		if err != nil {
			return observation, err
		}
		if wrap := cfg.Flags().WrapConfigFn; wrap != nil {
			config = wrap(config)
		}
		if deadline, ok := ctx.Deadline(); ok {
			config.Timeout = time.Until(deadline)
		}
		reader, err := dynamic.NewForConfig(config)
		if err != nil {
			return observation, err
		}
		snapshot := collectCapabilities(ctx, reader, request, nil)
		var details, sources []string
		for _, check := range snapshot.Checks {
			details = append(details, check.Name+": "+string(check.State))
			if check.Source != "" {
				sources = append(sources, check.Source)
			}
			switch check.State {
			case capabilityDenied:
				err = provider.ErrDenied
			case capabilityAbsent:
				if err == nil {
					err = provider.ErrAbsent
				}
			case capabilityAvailable:
			default:
				if err == nil {
					err = errors.New("API prerequisite unavailable")
				}
			}
		}
		if len(snapshot.Checks) == 0 {
			err = errors.New("no scoped API prerequisites observed")
		}
		observation.Detail = strings.Join(details, "; ")
		if len(sources) > 0 {
			observation.Source = strings.Join(sources, "; ")
		}
		return observation, err
	}
}

type providerDetails struct {
	*Details
	scope      provider.Scope
	specs      []provider.Spec
	discover   func(context.Context, provider.Scope, ...provider.Spec) []provider.Capability
	checks     []provider.Capability
	cancel     context.CancelFunc
	generation uint64
	started    bool
}

func (*providerDetails) CompactWorkspace() bool { return true }
func (d *providerDetails) Init(ctx context.Context) error {
	if err := d.Details.Init(ctx); err != nil {
		return err
	}
	d.actions.Add(ui.KeyR, ui.NewKeyAction("Check providers again", func(*tcell.EventKey) *tcell.EventKey { d.refresh(); return nil }, true))
	return nil
}
func (d *providerDetails) Start() {
	d.started = true
	d.Details.Start()
	d.app.Prompt().SetModel(d.cmdBuff)
	if d.checks == nil {
		d.refresh()
	} else {
		d.Update(renderProviderChecks(d.scope, d.checks, true))
	}
}
func (d *providerDetails) Stop() {
	d.started = false
	d.generation++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.Details.Stop()
}
func (d *providerDetails) current(generation uint64) bool {
	return d.started && generation == d.generation && d.app.Content.Top() == d &&
		d.scope.Context == d.app.Config.ActiveContextName() && d.scope.Namespace == d.app.Config.CachedNamespace() &&
		d.scope.Revision == d.app.Config.DestinationRevision()
}
func (d *providerDetails) refresh() {
	if d.scope.Context != d.app.Config.ActiveContextName() || d.scope.Namespace != d.app.Config.CachedNamespace() || d.scope.Revision != d.app.Config.DestinationRevision() {
		d.Update(renderProviderChecks(d.scope, d.checks, true) + "\n\nDestination changed; reopen :providers with the intended scope.")
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	generation, scope := d.generation, d.scope
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	d.cancel = cancel
	d.Update(renderProviderChecks(scope, d.checks, len(d.checks) > 0) + "\n\nChecking only requested providers...")
	go func() {
		defer cancel()
		checks := d.discover(ctx, scope, d.specs...)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		d.app.QueueUpdateDraw(func() {
			if d.current(generation) {
				d.checks = checks
				d.Update(renderProviderChecks(scope, checks, false))
			}
		})
	}()
}

//nolint:gocritic // Rendering reads a retained destination snapshot.
func renderProviderChecks(scope provider.Scope, checks []provider.Capability, retained bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Provider checks\nContext: %s\nNamespace: %s\n", scope.Context, client.PrintNamespace(scope.Namespace))
	if retained {
		b.WriteString("Retained observation; check again before using these capabilities.\n")
	}
	for i := range checks {
		check := &checks[i]
		fmt.Fprintf(&b, "\n%s: %s\n", safeProviderText(check.ID), check.State)
		if check.Version != "" {
			fmt.Fprintf(&b, "Version: %s\n", safeProviderText(check.Version))
		}
		if check.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", safeProviderText(check.Source))
		}
		if check.Detail != "" {
			fmt.Fprintf(&b, "Evidence: %s\n", safeProviderText(check.Detail))
		}
		fmt.Fprintf(&b, "Observed: %s · deadline %s\n", check.ObservedAt.UTC().Format("15:04:05Z"), check.Limits.Timeout)
	}
	b.WriteString("\nr check again · Esc return\nAvailability is scoped evidence; denied, absent and incompatible are separate states.")
	return b.String()
}

func safeProviderText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if text := []rune(s); len(text) > 512 {
		s = string(text[:512]) + "..."
	}
	return s
}
