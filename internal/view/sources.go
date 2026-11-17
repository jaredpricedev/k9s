// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const (
	sourcesCommandToken = "sources"
	// sourcesSuggestLimit bounds the one Service list behind a suggestion.
	sourcesSuggestLimit = 500
)

// declaredSources returns the observability declaration of the active
// context. It reads configuration only; nothing is contacted.
func declaredSources(app *App) *observability.Sources {
	if app == nil || app.Config == nil {
		return nil
	}
	ct, err := app.Config.CurrentContext()
	if err != nil || ct == nil {
		return nil
	}
	sources := ct.Observability
	return &sources
}

// observabilitySource resolves the declared source of one kind for the
// active context without contacting it. A nil client comes with a Status
// whose State is not-configured or unavailable and whose Reason can be shown.
// Views call the client's typed reads from a goroutine, never the UI thread.
func observabilitySource(app *App, kind observability.Kind) (*observability.Client, observability.Status) {
	return observability.Resolve(declaredSources(app), kind, kubeRESTConfig(app))
}

// kubeRESTConfig hands the app's captured connection to a service source.
func kubeRESTConfig(app *App) observability.RESTConfigFunc {
	return func() (*rest.Config, error) {
		if app == nil || app.Conn() == nil || app.Conn().Config() == nil {
			return nil, errors.New("no Kubernetes connection")
		}
		return app.Conn().Config().RESTConfig()
	}
}

type sourceSuggestions struct {
	state     observability.State
	reason    string
	at        time.Time
	items     []observability.Suggestion
	truncated bool
}

type sourcesView struct {
	*Details
	contextName string
	revision    uint64
	sources     *observability.Sources
	probes      map[observability.Kind]observability.Status
	suggestions *sourceSuggestions
	probe       func(context.Context, observability.Kind) observability.Status
	suggest     func(context.Context) sourceSuggestions
	cancel      context.CancelFunc
	generation  uint64
	active      bool
	busy        string
}

func (c *Command) sourcesCommand() {
	v := newSourcesView(c.app)
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
	}
}

func newSourcesView(app *App) *sourcesView {
	v := &sourcesView{
		Details:     NewDetails(app, "Observability sources", app.Config.ActiveContextName(), contentInspection, true),
		contextName: app.Config.ActiveContextName(), revision: app.Config.DestinationRevision(),
		sources: declaredSources(app), probes: make(map[observability.Kind]observability.Status),
	}
	v.probe = func(ctx context.Context, kind observability.Kind) observability.Status {
		client, st := observability.Resolve(v.sources, kind, kubeRESTConfig(app))
		if client == nil {
			return st
		}
		defer client.Close()
		return client.Probe(ctx)
	}
	v.suggest = func(ctx context.Context) sourceSuggestions { return suggestSources(ctx, app) }
	return v
}

// suggestSources makes one bounded Service list and matches well-known names.
func suggestSources(ctx context.Context, app *App) sourceSuggestions {
	out := sourceSuggestions{state: observability.StateUnavailable, at: time.Now().UTC()}
	if app.Conn() == nil {
		out.reason = "no Kubernetes connection"
		return out
	}
	dial, err := app.Conn().Dial()
	if err != nil {
		out.reason = "no Kubernetes connection"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, observability.DefaultTimeout)
	defer cancel()
	list, err := dial.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{Limit: sourcesSuggestLimit})
	out.at = time.Now().UTC()
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		out.state, out.reason = observability.StateTimeout, "Service list did not answer within the deadline"
		return out
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		out.state, out.reason = observability.StateDenied, "listing Services in all namespaces is not permitted"
		return out
	default:
		out.reason = "Service list failed"
		return out
	}
	services := make([]observability.ServicePort, 0, len(list.Items))
	for i := range list.Items {
		svc := &list.Items[i]
		sp := observability.ServicePort{Namespace: svc.Namespace, Name: svc.Name}
		for _, p := range svc.Spec.Ports {
			sp.Ports = append(sp.Ports, p.Port)
		}
		services = append(services, sp)
	}
	out.state, out.items, out.truncated = observability.StateComplete, observability.Suggest(services), list.Continue != ""
	if out.truncated {
		out.state = observability.StateIncomplete
		out.reason = fmt.Sprintf("only the first %d Services were read", sourcesSuggestLimit)
	}
	return out
}

func (*sourcesView) CompactWorkspace() bool { return true }

func (v *sourcesView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.actions.Add(ui.KeyP, ui.NewKeyAction("Probe each declared source once", v.guard(v.probeAll), true))
	v.actions.Add(ui.KeyS, ui.NewKeyAction("Suggest in-cluster services", v.guard(v.suggestAll), true))
	v.render()
	return nil
}

func (v *sourcesView) guard(fn func()) func(*tcell.EventKey) *tcell.EventKey {
	return func(e *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return e
		}
		fn()
		return nil
	}
}

func (v *sourcesView) Start() {
	v.active = true
	v.Details.Start()
	v.app.Prompt().SetModel(v.cmdBuff)
	v.render()
}

func (v *sourcesView) Stop() {
	v.active = false
	v.generation++
	if v.cancel != nil {
		v.cancel()
	}
	v.Details.Stop()
}

func (v *sourcesView) current() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}

// run starts one explicit, cancellable read off the UI thread. A reply that
// arrives after the view closed or the destination changed is discarded.
func (v *sourcesView) run(label string, work func(context.Context) func()) {
	if !v.current() {
		v.busy = ""
		v.app.Flash().Warn("Destination changed; reopen :sources")
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), 3*observability.DefaultTimeout)
	v.cancel = cancel
	v.busy = label
	v.render()
	go func() {
		defer cancel()
		apply := work(ctx)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && generation == v.generation && v.current() {
				v.busy = ""
				apply()
				v.render()
			}
		})
	}()
}

func (v *sourcesView) probeAll() {
	if !v.sources.Declared() {
		v.app.Flash().Warn("No source is declared for this context; s suggests in-cluster services")
		return
	}
	probe := v.probe
	v.run("Probing declared sources once", func(ctx context.Context) func() {
		results := make(map[observability.Kind]observability.Status)
		for _, kind := range observability.Kinds {
			if v.sources.Source(kind) != nil {
				results[kind] = probe(ctx, kind)
			}
		}
		return func() { v.probes = results }
	})
}

func (v *sourcesView) suggestAll() {
	suggest := v.suggest
	v.run("Listing Services once for suggestions", func(ctx context.Context) func() {
		result := suggest(ctx)
		return func() { v.suggestions = &result }
	})
}

// probeWord states a probe outcome in words; "reachable" is about the
// source answering, not about anything it monitors.
func probeWord(st observability.Status) string {
	word := string(st.State)
	if st.State == observability.StateComplete {
		word = "reachable"
	}
	if st.Reason != "" {
		word += " (" + st.Reason + ")"
	}
	if !st.ObservedAt.IsZero() {
		word += " at " + st.ObservedAt.UTC().Format("15:04:05Z")
	}
	return word
}

func (v *sourcesView) render() {
	var b strings.Builder
	fmt.Fprintf(&b, "Observability sources · READ ONLY · %s\n", safeProviderText(v.contextName))
	b.WriteString("Nothing is contacted until p or s is pressed here, or a feature is opened.\n")
	if v.busy != "" {
		b.WriteString(v.busy + "; prior results retained.\n")
	}
	// The outcome of every kind comes first so it stays on screen at 80x24.
	b.WriteString("\n")
	for _, kind := range observability.Kinds {
		state := "NOT PROBED (p probes once)"
		if st, ok := v.probes[kind]; ok {
			state = probeWord(st)
		} else if v.sources.Source(kind) == nil {
			state = "not declared"
		}
		fmt.Fprintf(&b, "%-13s %s\n", kind, safeProviderText(state))
	}
	for _, kind := range observability.Kinds {
		fmt.Fprintf(&b, "\n%s\n", strings.ToUpper(string(kind)))
		src := v.sources.Source(kind)
		if src == nil {
			b.WriteString("  not declared\n")
			if kind == observability.KindPrometheus && v.sources != nil && v.sources.Grafana != nil && v.sources.Grafana.Datasource != "" {
				fmt.Fprintf(&b, "  Queries go through Grafana datasource %s.\n", safeProviderText(v.sources.Grafana.Datasource))
			}
			continue
		}
		fmt.Fprintf(&b, "  Reached by: %s\n", safeProviderText(src.Display()))
		if src.Service != "" {
			b.WriteString("  Credentials: Kubernetes API service proxy, your kube credentials\n")
		} else if src.TokenEnv != "" {
			fmt.Fprintf(&b, "  Credentials: bearer token from environment variable %s (value never shown)\n", safeProviderText(src.TokenEnv))
		} else {
			b.WriteString("  Credentials: none\n")
		}
		if err := v.sources.Validate(kind); err != nil {
			fmt.Fprintf(&b, "  Declaration problem: %s\n", safeProviderText(err.Error()))
		}
		if kind == observability.KindGrafana {
			v.renderGrafana(&b)
		}
	}
	b.WriteString("\nDeclared in the context configuration under k9s.observability.\n")
	b.WriteString("\nSUGGESTIONS\n")
	v.renderSuggestions(&b)
	b.WriteString("\np probe once · s suggest services · / search · Esc back")
	row, col := v.text.GetScrollOffset()
	v.Update(b.String())
	v.text.ScrollTo(row, col)
}

func (v *sourcesView) renderGrafana(b *strings.Builder) {
	g := v.sources.Grafana
	if g.PublicURL != "" {
		fmt.Fprintf(b, "  Links open at: %s\n", safeProviderText(observability.Clean(g.PublicURL, 200)))
	} else {
		b.WriteString("  Links: no publicURL declared, so no browser links\n")
	}
	if g.Datasource != "" {
		fmt.Fprintf(b, "  Datasource UID: %s\n", safeProviderText(g.Datasource))
	}
	var kinds []string
	for _, k := range observability.DashboardKinds {
		if d, ok := g.Dashboards[k]; ok {
			kinds = append(kinds, k+"="+d.UID)
		}
	}
	if len(kinds) == 0 {
		b.WriteString("  Dashboards: none declared\n")
		return
	}
	fmt.Fprintf(b, "  Dashboards: %s\n", safeProviderText(strings.Join(kinds, ", ")))
}

func (v *sourcesView) renderSuggestions(b *strings.Builder) {
	s := v.suggestions
	if s == nil {
		b.WriteString("  NOT REQUESTED (s lists Services once and matches well-known names)\n")
		return
	}
	at := s.at.UTC().Format("15:04:05Z")
	if s.state != observability.StateComplete && s.state != observability.StateIncomplete {
		fmt.Fprintf(b, "  Service list %s (%s) at %s. No suggestion can be made.\n", s.state, safeProviderText(s.reason), at)
		return
	}
	if s.truncated {
		fmt.Fprintf(b, "  Incomplete: %s.\n", safeProviderText(s.reason))
	}
	if len(s.items) == 0 {
		fmt.Fprintf(b, "  No Service with a well-known name among those read at %s.\n  That does not show that none is installed.\n", at)
		return
	}
	fmt.Fprintf(b, "  Name matches from one Service list at %s. A match is a guess:\n  it is not verified and nothing is written. To use one, add it to\n  %s\n\n", at,
		safeProviderText(v.configPath()))
	b.WriteString("  k9s:\n    observability:\n")
	seen := make(map[observability.Kind]int)
	for _, item := range s.items {
		seen[item.Kind]++
		note := ""
		if v.sources.Source(item.Kind) != nil {
			note = " # already declared"
		} else if seen[item.Kind] > 1 {
			note = " # alternative; keep one"
		}
		for i, line := range strings.Split(item.Snippet, "\n") {
			if i == 0 {
				line += note
			}
			body := strings.TrimLeft(line, " ")
			fmt.Fprintf(b, "      %s%s\n", strings.Repeat(" ", len(line)-len(body)), safeProviderText(body))
		}
	}
}

func (v *sourcesView) configPath() string {
	cluster, err := v.app.Config.ActiveClusterName(v.contextName)
	if err != nil {
		return "the context config.yaml"
	}
	return config.AppContextConfig(cluster, v.contextName)
}

func sourcesActions(app *App) []ui.ActionDescriptor {
	reason := ""
	if app.Config == nil || app.Config.ActiveContextName() == "" {
		reason = "No active context"
	}
	return []ui.ActionDescriptor{{ID: "command.sources", Label: "Observability sources (declared, probe on request)", Category: ui.ActionInspect,
		Shortcut: ":" + sourcesCommandToken, Discoverable: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { NewCommand(app).sourcesCommand(); return nil }}}
}
