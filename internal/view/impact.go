// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/client-go/dynamic"
)

const (
	impactCommandToken = "impact"
	impactTitle        = "Impact"
	// impactGraphCommand is opened for a signal only when this build
	// registers it; :impact does not depend on it.
	impactGraphCommand = "graph"
	// impactDeadline covers the three bounded Kubernetes reads and the
	// range reads, which run a few at a time.
	impactDeadline = 45 * time.Second
)

var impactKinds = map[string]string{
	client.DpGVR.String(): impact.KindDeployment, client.StsGVR.String(): impact.KindStatefulSet,
	client.DsGVR.String(): impact.KindDaemonSet, client.PodGVR.String(): impact.KindPod,
}

// impactRead is the outcome of one explicit read.
type impactRead struct {
	workload impact.Workload
	rollout  *impact.Rollout
	report   *impact.Report
	notes    []string
	// incomplete says the revision list was cut by its read limit.
	incomplete string
	err        error
}

// impactView is an on-demand, retained comparison: it reads when opened and
// when the user asks again, never on a refresh tick.
type impactView struct {
	*Details
	contextName, kind, namespace, name string
	loader                             func(ctx context.Context, back int, width time.Duration) impactRead
	read                               *impactRead
	back                               int
	asked                              time.Duration
	cancel                             context.CancelFunc
	generation                         uint64
	active, loading                    bool
	header, detail, footer             *tview.TextView
	table                              *tview.Table
	focused                            string
	width, height                      int
	headerHeight, detailHeight         int
}

// impactTarget says whether a selection can be compared, and as what kind.
//
//nolint:gocritic // The immutable selection is captured by value.
func impactTarget(target SelectedResourceTarget) (kind string, err error) {
	if target.Err() != nil || target.GVR == nil {
		return "", errors.New("Select a Deployment, StatefulSet, DaemonSet or one of their Pods first")
	}
	kind, ok := impactKinds[target.GVR.String()]
	if !ok || target.Namespace == "" || target.Name == "" {
		return "", errors.New("Impact compares a rollout: select a Deployment, StatefulSet, DaemonSet or one of their Pods")
	}
	return kind, nil
}

func (c *Command) impactCommand() {
	target := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	if owner, ok := c.app.Content.Top().(actionOwner); ok {
		target = actionTarget(owner, c.app.Config.ActiveContextName())
	}
	c.app.openImpact(target)
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openImpact(target SelectedResourceTarget) {
	kind, err := impactTarget(target)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if a.Conn() == nil || a.Conn().Config() == nil {
		a.Flash().Warn("Select a configured context first")
		return
	}
	contextName := a.Config.ActiveContextName()
	cfg, err := a.Conn().Config().Snapshot(contextName).RESTConfig()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	objects, err := dynamic.NewForConfig(cfg)
	if err != nil {
		a.Flash().Err(errors.New("captured resource transport unavailable"))
		return
	}
	// The declaration is read here, on the UI thread; resolving a source
	// contacts nothing. Each read opens and closes its own client.
	sources := declaredSources(a)
	restConfig := kubeRESTConfig(a)
	v := newImpactView(a, contextName, kind, target.Namespace, target.Name)
	v.loader = func(ctx context.Context, back int, width time.Duration) impactRead {
		return readImpact(ctx, objects, sources, restConfig, kind, target.Namespace, target.Name, back, width)
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

// readImpact makes one comparison: three bounded Kubernetes reads for the
// rollout history, then the range reads against the declared Prometheus.
func readImpact(ctx context.Context, objects dynamic.Interface, sources *observability.Sources, restConfig observability.RESTConfigFunc,
	kind, namespace, name string, back int, width time.Duration,
) impactRead {
	var read impactRead
	if read.workload, read.err = impact.Resolve(ctx, objects, kind, namespace, name); read.err != nil {
		return read
	}
	history, err := impact.ReadHistory(ctx, objects, read.workload)
	if err != nil {
		read.err = err
		return read
	}
	read.notes, read.incomplete = history.Notes, history.Incomplete
	rollout, err := history.Rollout(back)
	if rollout.Total > 0 {
		read.rollout = &rollout
	}
	if err != nil {
		read.err = err
		return read
	}
	windows, err := impact.PlanWindows(rollout.Revision.Created, rollout.PreviousAt(), rollout.NextAt, time.Now(), width)
	if err != nil {
		read.err = err
		return read
	}
	request := &impact.Request{Workload: read.workload, Rollout: rollout, Windows: windows}
	if sources != nil {
		request.RED = sources.RED
	}
	if err := request.RED.Validate(); err != nil {
		read.report = impact.Unread(request, "the k9s.observability.red mapping is not usable: "+err.Error())
		return read
	}
	source, status := observability.Resolve(sources, observability.KindPrometheus, restConfig)
	if source == nil {
		why := string(status.State)
		if status.State == observability.StateNotConfigured {
			why = "no Prometheus source is declared for this context; :sources shows how to declare one"
		} else if status.Reason != "" {
			why += ": " + status.Reason
		}
		read.report = impact.Unread(request, why)
		return read
	}
	defer source.Close()
	request.Source = source.Display()
	read.report = impact.Collect(ctx, source, request)
	return read
}

func newImpactView(app *App, contextName, kind, namespace, name string) *impactView {
	return &impactView{
		Details:     NewDetails(app, impactTitle, kind+" "+client.FQN(namespace, name), contentTXT, false),
		contextName: contextName, kind: kind, namespace: namespace, name: name, asked: impact.DefaultWidth,
	}
}

func impactActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := ""
	if _, err := impactTarget(target); err != nil {
		reason = err.Error()
	} else if sources := declaredSources(app); sources.Source(observability.KindPrometheus) == nil && (sources == nil || sources.Grafana == nil || sources.Grafana.Datasource == "") {
		reason = "No Prometheus source is declared for this context; :sources shows how to declare one"
	}
	return []ui.ActionDescriptor{{
		ID: "resource.impact", Label: "Impact: before and after the last rollout", Category: ui.ActionInspect,
		Shortcut: ":" + impactCommandToken, Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openImpact(target); return nil },
	}}
}

func (*impactView) CompactWorkspace() bool { return true }

// workload is the resolved workload, or the selection while unresolved.
func (v *impactView) workload() impact.Workload {
	if v.read != nil && v.read.workload.Name != "" {
		return v.read.workload
	}
	return impact.Workload{Kind: v.kind, Namespace: v.namespace, Name: v.name}
}

// SelectedResource exposes the compared workload to shared actions, so that
// :timeline and a graph command opened from here follow the same workload.
func (v *impactView) SelectedResource() SelectedResourceTarget {
	target := SelectedResourceTarget{Context: v.contextName}
	w := v.workload()
	if w.Kind == impact.KindPod {
		target.GVR = client.PodGVR
	} else {
		target.GVR = client.NewGVR(w.Resource())
	}
	target.Namespace, target.Name = w.Namespace, w.Name
	return target
}

func (v *impactView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.table.SetSelectionChangedFunc(func(row, _ int) {
		if row > 0 && row <= len(impact.Signals) {
			v.focused = impact.Signals[row-1].ID
			v.renderDetail()
		}
	})
	v.headerHeight, v.detailHeight = 6, 1
	v.Flex.Clear().SetDirection(tview.FlexRow).
		AddItem(v.header, v.headerHeight, 0, false).
		AddItem(v.table, 0, 1, true).
		AddItem(v.detail, v.detailHeight, 0, false).
		AddItem(v.footer, 1, 0, false)
	v.bindImpactKeys()
	v.render()
	return nil
}

func (v *impactView) bindImpactKeys() {
	v.actions.Delete(ui.KeyC, ui.KeyF, ui.KeyN, ui.KeyShiftN, ui.KeySlash, tcell.KeyCtrlS, tcell.KeyDelete)
	do := func(run func()) ui.ActionHandler {
		return func(*tcell.EventKey) *tcell.EventKey {
			run()
			return nil
		}
	}
	for index, width := range impact.Widths {
		v.actions.Add(ui.Key1+tcell.Key(index), ui.NewKeyAction("Window "+impact.Dur(width), do(func() { v.setWidth(width) }), true))
	}
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Re-read comparison", do(v.refresh), true))
	v.actions.Add(ui.KeyP, ui.NewKeyAction("Previous (earlier) rollout", do(func() { v.step(1) }), true))
	v.actions.Add(ui.KeyN, ui.NewKeyAction("Next (later) rollout", do(func() { v.step(-1) }), true))
	v.actions.Add(ui.KeyT, ui.NewKeyAction("Timeline of this workload", do(v.openTimeline), true))
	// Enter is bound only when this build has a graph command; otherwise
	// the key is left without any action or hint.
	v.actions.Delete(tcell.KeyEnter)
	if v.graphAvailable() {
		v.actions.Add(tcell.KeyEnter, ui.NewKeyAction("Graph this workload", do(v.openGraph), true))
	}
	back := func(event *tcell.EventKey) *tcell.EventKey { return v.app.PrevCmd(event) }
	v.actions.Add(tcell.KeyEscape, ui.NewKeyAction("Back", back, false))
	v.actions.Add(ui.KeyQ, ui.NewKeyAction("Back", back, false))
}

// graphAvailable reports whether this build's command table knows a graph
// command. It is asked each time, so the key does nothing rather than fail
// when the command is absent.
func (v *impactView) graphAvailable() bool {
	return v.app.command != nil && slices.Contains(v.app.command.suggestionAliases(), impactGraphCommand)
}

func (v *impactView) openGraph() {
	if !v.graphAvailable() || !v.contextCurrent() {
		return
	}
	v.app.gotoResource(impactGraphCommand, "", false, true)
}

func (v *impactView) openTimeline() {
	if !v.contextCurrent() {
		v.app.Flash().Warn("Context changed; reopen :impact")
		return
	}
	v.app.openTimeline(v.SelectedResource(), "")
}

func (v *impactView) setWidth(width time.Duration) {
	if width == v.asked && v.read != nil && !v.loading {
		return
	}
	v.asked = width
	v.refresh()
}

// step moves to an earlier (positive) or later (negative) rollout.
func (v *impactView) step(delta int) {
	total := 0
	if v.read != nil && v.read.rollout != nil {
		total = v.read.rollout.Total
	}
	next := v.back + delta
	switch {
	case total == 0:
		v.app.Flash().Warn("No rollout history has been read yet")
	case next < 0:
		v.app.Flash().Warn("This is the latest rollout")
	case next >= total:
		v.app.Flash().Warn("This is the earliest retained revision")
	default:
		v.back = next
		v.refresh()
	}
}

func (v *impactView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.read == nil && !v.loading && v.loader != nil {
		v.refresh()
	}
}

func (v *impactView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *impactView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.table != nil {
		p := styles.Semantic()
		v.table.SetBackgroundColor(p.Canvas.Color())
		v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(p.Text.Color()).Bold(true))
		for _, item := range []*tview.TextView{v.header, v.detail, v.footer} {
			item.SetBackgroundColor(p.Panel.Color())
			item.SetTextColor(p.Text.Color())
		}
	}
	v.render()
}

func (v *impactView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	v.Flex.Draw(screen)
}

func (v *impactView) contextCurrent() bool {
	return v.contextName == v.app.Config.ActiveContextName()
}

// refresh starts one bounded read. A newer request or leaving the view
// cancels it, and a late reply never replaces a newer comparison.
func (v *impactView) refresh() {
	if !v.contextCurrent() || v.loader == nil {
		v.render()
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, back, asked := v.generation, v.back, v.asked
	ctx, cancel := context.WithTimeout(context.Background(), impactDeadline)
	v.cancel, v.loading = cancel, true
	v.render()
	go func() {
		defer cancel()
		read := v.loader(ctx, back, asked)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && v.generation == generation && v.contextCurrent() && v.app.Content.Top() == v {
				v.accept(&read)
			}
		})
	}()
}

func (v *impactView) accept(read *impactRead) {
	v.loading = false
	v.read = read
	if read.rollout != nil {
		v.back = read.rollout.Back
	}
	if v.focused == "" || v.focusedOutcome() == nil {
		v.focused = v.firstInteresting()
	}
	v.render()
}

// firstInteresting selects the first signal that is worse, so the detail
// line starts on evidence rather than on the first row.
func (v *impactView) firstInteresting() string {
	if v.read != nil && v.read.report != nil {
		for index := range v.read.report.Outcomes {
			if outcome := &v.read.report.Outcomes[index]; outcome.State == impact.Compared && outcome.Verdict == impact.Worse {
				return outcome.Signal.ID
			}
		}
	}
	return impact.Signals[0].ID
}

func (v *impactView) focusedOutcome() *impact.Outcome {
	if v.read == nil || v.read.report == nil {
		return nil
	}
	return v.read.report.Outcome(v.focused)
}

func (v *impactView) viewWidth() int {
	if v.width <= 0 {
		return 80
	}
	return v.width
}

// impactFit returns the first variant that fits the width, or the last one
// truncated: lines shorten their wording before they lose their end.
func impactFit(width int, variants ...string) string {
	for _, text := range variants {
		if tview.TaggedStringWidth(tview.Escape(text)) <= width {
			return text
		}
	}
	return ui.Truncate(variants[len(variants)-1], width)
}

// impactClock renders a time of day, with the date when it is not the day
// of the read.
func impactClock(t, read time.Time, seconds bool) string {
	t, read = t.UTC(), read.UTC()
	layout := "15:04"
	if seconds {
		layout = "15:04:05"
	}
	if t.Year() != read.Year() || t.YearDay() != read.YearDay() {
		layout = "Jan02 " + layout
	}
	return t.Format(layout)
}

func impactJoin(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " · ")
}
