// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const (
	graphCommandToken = "graph"
	graphTitle        = "Graph"
	graphRevisionKey  = "deployment.kubernetes.io/revision"
)

// graphView draws charts for one object from the declared Prometheus
// source. It reads when opened, when the window changes and on r; it never
// polls, and a reply that arrives after the view closed is discarded.
type graphView struct {
	*Details
	target        graphTarget
	revision      uint64
	window        time.Duration
	snapshot      *graphSnapshot
	load          func(context.Context, time.Duration) *graphSnapshot
	cancel        context.CancelFunc
	generation    uint64
	active        bool
	busy          string
	cursor, first int
	atEnd, report bool
	// focus names the chart to scroll to when the first read arrives.
	focus         string
	width, height int
}

func parseGraphWindow(text string) (time.Duration, bool) {
	for _, window := range graphWindows {
		if text == graphSpan(window) {
			return window, true
		}
	}
	return 0, false
}

func (c *Command) graphCommand(line string) {
	target := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	if owner, ok := c.app.Content.Top().(actionOwner); ok {
		target = actionTarget(owner, c.app.Config.ActiveContextName())
	}
	window := graphDefaultWindow
	if fields := strings.Fields(line); len(fields) > 1 {
		parsed, ok := parseGraphWindow(fields[1])
		if !ok {
			c.app.Flash().Warn("A graph window is one of 15m, 1h, 6h or 24h, as in :graph 6h")
			return
		}
		window = parsed
	}
	c.app.openGraph(target, window)
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openGraph(target SelectedResourceTarget, window time.Duration) {
	a.openGraphAt(target, window, "")
}

// openGraphAt opens the graph with the named chart scrolled into view, for
// a caller that arrives about one signal. An unknown name changes nothing.
//
//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openGraphAt(target SelectedResourceTarget, window time.Duration, focus string) {
	target.Context = a.Config.ActiveContextName()
	t, err := graphTargetFor(target)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	// The declaration and the connection are captured here, on the UI
	// thread; the reads below run off it and touch neither.
	sources, restConfig := declaredSources(a), kubeRESTConfig(a)
	objects, objectNote := graphObjectReader(a, t)
	v := newGraphView(a, t, window)
	v.focus = focus
	v.load = func(ctx context.Context, window time.Duration) *graphSnapshot {
		now := time.Now()
		source, st := observability.Resolve(sources, observability.KindPrometheus, restConfig)
		if source == nil {
			start, end, step := graphFrameTimes(now, window)
			return &graphSnapshot{Target: *t, Window: window, Step: step, Start: start, End: end, ReadAt: now.UTC(), Source: "no source", Status: &st}
		}
		defer source.Close()
		snapshot := loadGraph(ctx, source, t, window, now)
		snapshot.MarkNote = objectNote
		if objects != nil {
			marks, cancel := context.WithTimeout(ctx, observability.DefaultTimeout)
			defer cancel()
			snapshot.Marks, snapshot.MarkNote = graphMarksFrom(marks, objects, t)
		}
		return snapshot
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func newGraphView(app *App, t *graphTarget, window time.Duration) *graphView {
	if window <= 0 {
		window = graphDefaultWindow
	}
	return &graphView{
		Details: NewDetails(app, graphTitle, t.Label(), contentTXT, false),
		target:  *t, revision: app.Config.DestinationRevision(), window: window, atEnd: true,
	}
}

func graphAvailability(app *App, target SelectedResourceTarget) string { //nolint:gocritic // Immutable selection.
	if _, err := graphTargetFor(target); err != nil {
		return err.Error()
	}
	sources := declaredSources(app)
	if sources == nil || (sources.Prometheus == nil && (sources.Grafana == nil || sources.Grafana.Datasource == "")) {
		return "No Prometheus source is declared for this context; :sources shows what to add"
	}
	return ""
}

func graphActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	return []ui.ActionDescriptor{{
		ID: "resource.graph", Label: "Graph: usage vs limits over time", Category: ui.ActionInspect,
		Shortcut: ":" + graphCommandToken, Discoverable: true, UnavailableReason: graphAvailability(app, target),
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openGraph(target, 0); return nil },
	}}
}

// graphKeyAction is the list key on Pods, workloads and Nodes.
func graphKeyAction(app *App, owner actionOwner) ui.KeyAction {
	return ui.NewKeyActionWithOpts("Graph", func(*tcell.EventKey) *tcell.EventKey {
		app.openGraph(actionTarget(owner, app.Config.ActiveContextName()), 0)
		return nil
	}, ui.ActionOpts{Visible: true, RequiresSelection: true})
}

func (*graphView) CompactWorkspace() bool { return true }

// SelectedResource lets shared actions (timeline, describe) follow the graph.
func (v *graphView) SelectedResource() SelectedResourceTarget {
	return SelectedResourceTarget{Context: v.target.Context, GVR: client.NewGVR(v.target.Resource), Namespace: v.target.Namespace, Name: v.target.Name}
}

// GrafanaWindow hands the graph's own window to a Grafana link, so the
// dashboard opens on the range the charts show.
func (v *graphView) GrafanaWindow() (grafanaWindow, bool) {
	now := time.Now()
	return grafanaWindow{From: now.Add(-v.window), To: now, Live: true, Reason: "the graph's window, last " + graphSpan(v.window)}, true
}

func (v *graphView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.text.SetWrap(false).SetWordWrap(false).SetRegions(false)
	v.actions.Delete(ui.KeyC, ui.KeyF, tcell.KeyCtrlS, ui.KeySlash, tcell.KeyEnter, tcell.KeyDelete)
	do := func(run func()) ui.ActionHandler {
		return func(*tcell.EventKey) *tcell.EventKey {
			run()
			return nil
		}
	}
	for index, window := range graphWindows {
		v.actions.Add(ui.Key1+tcell.Key(index), ui.NewKeyAction("Window "+graphSpan(window), do(func() { v.setWindow(window) }), true))
	}
	v.actions.Bulk(ui.KeyMap{
		ui.KeyR:         ui.NewKeyAction("Re-read", do(v.refresh), true),
		ui.KeyS:         ui.NewKeyAction("Queries and gaps", do(v.toggleReport), true),
		tcell.KeyLeft:   ui.NewKeyAction("Cursor earlier", v.moveCmd(-1), true),
		tcell.KeyRight:  ui.NewKeyAction("Cursor later", v.moveCmd(1), true),
		tcell.KeyHome:   ui.NewKeyAction("Cursor to window start", v.moveCmd(-1<<20), false),
		tcell.KeyEnd:    ui.NewKeyAction("Cursor to window end", v.moveCmd(1<<20), false),
		tcell.KeyUp:     ui.NewKeyAction("Previous chart", v.scrollCmd(-1), true),
		tcell.KeyDown:   ui.NewKeyAction("Next chart", v.scrollCmd(1), true),
		tcell.KeyEscape: ui.NewKeyAction("Back", v.escape, false),
		ui.KeyQ:         ui.NewKeyAction("Back", v.escape, false),
		grafanaKey:      grafanaKeyAction(v.app, func() actionOwner { return v }, nil),
	})
	v.render()
	return nil
}

func (v *graphView) escape(event *tcell.EventKey) *tcell.EventKey {
	if v.report {
		v.toggleReport()
		return nil
	}
	return v.app.PrevCmd(event)
}

// moveCmd moves the cursor along the sample grid; with Shift it moves ten
// points. While the report is open the keys scroll the text instead.
func (v *graphView) moveCmd(delta int) ui.ActionHandler {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if v.report || v.snapshot == nil {
			return event
		}
		if event != nil && event.Modifiers()&tcell.ModShift != 0 {
			delta *= 10
		}
		last := int(v.snapshot.Window / v.snapshot.Step)
		v.cursor = min(max(v.cursor+delta, 0), last)
		v.atEnd = v.cursor == last
		v.render()
		return nil
	}
}

func (v *graphView) scrollCmd(delta int) ui.ActionHandler {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if v.report || v.snapshot == nil {
			return event
		}
		v.first = min(max(v.first+delta, 0), max(len(v.snapshot.Plan.Panels)-1, 0))
		v.render()
		return nil
	}
}

func (v *graphView) toggleReport() {
	v.report = !v.report
	v.render()
	v.text.ScrollToBeginning()
}

func (v *graphView) setWindow(window time.Duration) {
	if window == v.window && v.snapshot != nil {
		return
	}
	v.window, v.atEnd = window, true
	v.refresh()
}

func (v *graphView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.StylesChanged(v.app.Styles)
	if v.snapshot == nil && v.busy == "" {
		v.refresh()
	}
}

func (v *graphView) Stop() {
	v.active, v.busy = false, ""
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *graphView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	v.render()
}

func (v *graphView) current() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}

// refresh starts one explicit, bounded read off the UI thread. A newer
// request or leaving the view cancels it.
func (v *graphView) refresh() {
	if !v.current() {
		v.app.Flash().Warn("Destination changed; reopen :graph")
		return
	}
	if v.load == nil {
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, window, load := v.generation, v.window, v.load
	ctx, cancel := context.WithTimeout(context.Background(), graphDeadline)
	v.cancel = cancel
	v.busy = "Reading last " + graphSpan(window) + " once…"
	v.render()
	go func() {
		defer cancel()
		snapshot := load(ctx, window)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && generation == v.generation && v.current() {
				v.accept(snapshot)
			}
		})
	}()
}

func (v *graphView) accept(snapshot *graphSnapshot) {
	v.busy = ""
	if snapshot == nil {
		return
	}
	last := int(snapshot.Window / snapshot.Step)
	if v.atEnd || v.snapshot == nil || v.snapshot.Window != snapshot.Window {
		v.cursor, v.atEnd = last, true
	}
	v.cursor = min(v.cursor, last)
	v.first = min(v.first, max(len(snapshot.Plan.Panels)-1, 0))
	if v.focus != "" {
		for index := range snapshot.Plan.Panels {
			if snapshot.Plan.Panels[index].ID == v.focus {
				v.first = index
			}
		}
		v.focus = ""
	}
	v.snapshot = snapshot
	v.render()
}

func (v *graphView) Draw(screen tcell.Screen) {
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

func (v *graphView) render() {
	if v.text == nil {
		return
	}
	if v.report {
		// Wrapped here, on plain text: the queries hold brackets, which the
		// text view's own wrapping would take for markup.
		width := v.width
		if width <= 0 {
			width = 80
		}
		text := graphReport(v.snapshot) + "\ns or Esc returns to the charts · r re-read"
		if v.app.Config.K9s.UI.NoIcons {
			text = graphWords(text, true)
		}
		v.text.SetText(tview.Escape(strings.Join(graphWrap(text, width, "  "), "\n")))
		return
	}
	width, height := v.width, v.height
	if width <= 0 || height <= 0 {
		width, height = 80, 20
	}
	page := &graphPage{
		Width: width, Height: height, ASCII: v.app.Config.K9s.UI.NoIcons, Colors: graphPalette(v.app.Styles.Semantic()),
		Cursor: v.cursor, First: v.first, Busy: v.busy,
	}
	var lines []string
	if v.snapshot == nil || v.snapshot.Window != v.window {
		// Charts of another window are not shown under the new window's name.
		lines = []string{
			graphBold(page.Colors.Title, graphFit("Graph · "+v.target.Label()+" · last "+graphSpan(v.window), width)),
			graphPaint(page.Colors.Muted, graphFit("Reading the declared Prometheus source once… Esc cancels and goes back.", width)),
		}
	} else {
		lines = renderGraph(v.snapshot, page)
		// The page settles the scroll position: a chart that fits is shown.
		v.first = page.First
	}
	v.text.SetText(strings.Join(lines, "\n"))
	v.text.ScrollToBeginning()
}

// graphObjectReader captures a client for the bounded object reads behind
// the axis marks. Without one the page is drawn without marks and says why.
func graphObjectReader(app *App, t *graphTarget) (objects dynamic.Interface, note string) {
	if app.Conn() == nil || app.Conn().Config() == nil {
		return nil, "No Kubernetes connection: object times were not read."
	}
	cfg, err := app.Conn().Config().Snapshot(t.Context).RESTConfig()
	if err != nil {
		return nil, "Object times were not read: captured connection unavailable."
	}
	dial, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, "Object times were not read: captured connection unavailable."
	}
	return dial, ""
}

// graphMarksFrom reads the few moments the object itself records: when the
// owning ReplicaSet of the newest revision was created, and when a container
// last exited. They are bounded reads of metadata and status.
func graphMarksFrom(ctx context.Context, dial dynamic.Interface, t *graphTarget) (marks []graphMark, note string) {
	replicaSets := dial.Resource(client.RsGVR.GVR()).Namespace(t.Namespace)
	rollout := func(rs *unstructured.Unstructured) {
		revision := rs.GetAnnotations()[graphRevisionKey]
		created := rs.GetCreationTimestamp()
		if _, err := strconv.Atoi(revision); err != nil || created.IsZero() {
			return
		}
		marks = append(marks, graphMark{Key: 'R', T: created.UTC(), Label: "rollout rev " + revision,
			Origin: "creation time of ReplicaSet " + rs.GetName() + " (revision " + revision + ")"})
	}
	switch t.Kind {
	case "Pod":
		pod, err := dial.Resource(client.PodGVR.GVR()).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return nil, "The Pod was not read, so no object times are marked."
		}
		for _, owner := range pod.GetOwnerReferences() {
			if owner.Kind != "ReplicaSet" {
				continue
			}
			if rs, err := replicaSets.Get(ctx, owner.Name, metav1.GetOptions{}); err == nil {
				rollout(rs)
			}
		}
		if exit, ok := graphLastExit(pod); ok {
			marks = append(marks, exit)
		}
	case impact.KindDeployment, impact.KindStatefulSet, impact.KindDaemonSet:
		// The rollout time comes from the same bounded revision read
		// :impact uses, so both views name the same moment.
		history, err := impact.ReadHistory(ctx, dial, impact.Workload{Kind: t.Kind, Namespace: t.Namespace, Name: t.Name})
		if err != nil {
			return nil, "The revision history was not read, so no rollout time is marked."
		}
		if n := len(history.Revisions); n > 0 {
			latest := &history.Revisions[n-1]
			revision, what := strconv.FormatInt(latest.Number, 10), "ControllerRevision"
			if t.Kind == impact.KindDeployment {
				what = "ReplicaSet"
			}
			if !latest.Created.IsZero() {
				marks = append(marks, graphMark{Key: 'R', T: latest.Created.UTC(), Label: "rollout rev " + revision,
					Origin: "creation time of " + what + " " + latest.Name + " (revision " + revision + ")"})
			}
		}
		note = history.Incomplete
	case "ReplicaSet":
		if rs, err := replicaSets.Get(ctx, t.Name, metav1.GetOptions{}); err == nil {
			rollout(rs)
		}
	}
	return marks, note
}

// graphLastExit finds the most recent recorded container exit of a Pod.
func graphLastExit(pod *unstructured.Unstructured) (graphMark, bool) {
	statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	var mark graphMark
	for _, raw := range statuses {
		status, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		finished, _, _ := unstructured.NestedString(status, "lastState", "terminated", "finishedAt")
		at, err := time.Parse(time.RFC3339, finished)
		if err != nil || !at.After(mark.T) {
			continue
		}
		reason, _, _ := unstructured.NestedString(status, "lastState", "terminated", "reason")
		code, _, _ := unstructured.NestedInt64(status, "lastState", "terminated", "exitCode")
		name, _, _ := unstructured.NestedString(status, "name")
		if reason == "" {
			reason = "exit"
		}
		label := fmt.Sprintf("last exit %s (%d)", observability.Clean(reason, 24), code)
		mark = graphMark{Key: 'X', T: at.UTC(), Label: label, Origin: "lastState.terminated.finishedAt of container " + observability.Clean(name, 63)}
	}
	return mark, !mark.T.IsZero()
}
