// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const (
	graphCommandToken = "graph"
	graphTitle        = "Graph"
	graphRevisionKey  = "deployment.kubernetes.io/revision"
	// graphPodsWord opens the per-Pod comparison: ":graph pods", and the
	// focus "pods" or "pods:<signal>" of openGraphAt.
	graphPodsWord = "pods"
	graphNoPeers  = "Nothing to compare: :graph on a workload or a Service compares its Pods"
	// graphPodsLabel names the per-Pod comparison in the palette and help,
	// and on the key that opens it from :slow.
	graphPodsLabel = graphPodsShort + ": which Pod differs"
	graphPodsShort = "Per-Pod graphs"
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

	// The per-Pod comparison: whether it is shown, the selected signal and
	// grouping, the first item of the page shown and the page size the
	// last layout had. compare is its retained read; it belongs to one
	// snapshot and is read again with it.
	comparing       bool
	signal          string
	grouping        graphGrouping
	page, pageSize  int
	compare         *graphCompare
	compareAsked    *graphSnapshot
	containersAsked *graphCompare
	// loading is true while a read of the charts is in flight: the
	// comparison waits for it instead of cancelling it. comparePending is
	// true while a read of the comparison is in flight.
	loading, comparePending bool
	// loadCompare reads the comparison for a snapshot; given an earlier
	// comparison it only adds the per-container series to it.
	loadCompare func(context.Context, *graphSnapshot, *graphCompare) *graphCompare

	// The calm layout is the default page; detail is the page with every
	// chart, which a toggles. main names the chart promoted to full size
	// (empty: the rule's choice) and shown the one the last layout drew.
	// episodes are the episodes of the view the comparison was opened
	// from. peers is the page of rows of the calm comparison, peerPages
	// how many there are, and open the Pod that Enter opens there.
	// dropped is what the last calm layout gave up for the size.
	detail           bool
	main, shown      string
	episodes         *graphEpisodes
	peers, peerPages int
	open             string
	dropped          []string
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
	window, focus := graphDefaultWindow, ""
	fields := strings.Fields(line)
	if len(fields) > 3 {
		c.app.Flash().Warn("Use :graph [pods] [15m|1h|6h|24h], as in :graph pods 6h")
		return
	}
	for _, field := range fields[min(1, len(fields)):] {
		if field == graphPodsWord && focus == "" {
			focus = graphPodsWord
			continue
		}
		parsed, ok := parseGraphWindow(field)
		if !ok {
			c.app.Flash().Warn("A graph window is one of 15m, 1h, 6h or 24h, as in :graph 6h; :graph pods compares the Pods of a workload")
			return
		}
		window = parsed
	}
	c.app.openGraphAt(target, window, focus)
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
	if from, ok := a.Content.Top().(graphEpisodeSource); ok {
		// The view the graph is opened from knows when its episodes were:
		// the comparison holds its rows against them.
		v.episodes = from.graphEpisodes()
	}
	if word, signal, _ := strings.Cut(focus, ":"); word == graphPodsWord {
		// The caller arrives to compare Pods, optionally about one signal.
		if !t.workload() {
			a.Flash().Warn(graphNoPeers)
			return
		}
		v.comparing, v.signal, focus = true, signal, ""
	}
	v.focus = focus
	v.load = func(ctx context.Context, window time.Duration) *graphSnapshot {
		now := time.Now()
		source, st := observability.Resolve(sources, observability.KindPrometheus, restConfig)
		if source == nil {
			start, end, step := graphFrameTimes(now, window)
			return &graphSnapshot{Target: *t, Window: window, Step: step, Start: start, End: end, ReadAt: now.UTC(), Source: "no source", Status: &st}
		}
		defer source.Close()
		var members *graphMembers
		if t.Kind == graphKindService {
			// The Pods behind a Service come from the cluster, through the
			// same bounded collector :network-review uses.
			members = &graphMembers{Problem: "The Pods behind the Service were not resolved. " + objectNote}
			if objects != nil {
				members = graphServiceMembers(ctx, objects, t)
			}
		}
		snapshot := loadGraphOf(ctx, source, t, window, now, members)
		snapshot.MarkNote = objectNote
		if objects != nil {
			marks, cancel := context.WithTimeout(ctx, observability.DefaultTimeout)
			defer cancel()
			snapshot.Marks, snapshot.MarkNote, snapshot.Revisions = graphObjectFacts(marks, objects, t)
		}
		return snapshot
	}
	v.loadCompare = func(ctx context.Context, base *graphSnapshot, earlier *graphCompare) *graphCompare {
		source, _ := observability.Resolve(sources, observability.KindPrometheus, restConfig)
		if source == nil {
			return nil
		}
		defer source.Close()
		if earlier != nil {
			return loadGraphContainers(ctx, source, earlier)
		}
		return loadGraphCompare(ctx, source, base, sources, graphServiceFor(ctx, objects, t, sources), time.Now())
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
	reason := graphAvailability(app, target)
	pods := reason
	if t, err := graphTargetFor(target); pods == "" && err == nil && !t.workload() {
		pods = graphNoPeers
	}
	return []ui.ActionDescriptor{{
		ID: "resource.graph", Label: "Graph: usage vs limits over time", Category: ui.ActionInspect,
		Shortcut: ":" + graphCommandToken, Discoverable: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openGraph(target, 0); return nil },
	}, {
		ID: "resource.graph-pods", Label: "Per-Pod graphs: which Pod differs", Category: ui.ActionInspect,
		Shortcut: ":" + graphCommandToken + " " + graphPodsWord, Discoverable: true, UnavailableReason: pods,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openGraphAt(target, 0, graphPodsWord); return nil },
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
		ui.KeyR:         ui.NewKeyAction("Re-read", do(v.reread), true),
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
		chartStyleKey:   chartStyleAction(v.app, v.render),
	})
	// The comparison keys are named in the header only where there are
	// Pods to compare; elsewhere they answer with the reason.
	pods := v.target.workload()
	opts := func(priority int) ui.ActionOpts {
		if !pods {
			return ui.ActionOpts{}
		}
		return ui.ActionOpts{Visible: true, Priority: priority}
	}
	// While the report is open these keys stay with the text: g and G are
	// its top and bottom.
	charts := func(run func()) ui.ActionHandler {
		return func(event *tcell.EventKey) *tcell.EventKey {
			if v.report {
				return event
			}
			run()
			return nil
		}
	}
	// The sibling views of a hang open from here with the keys that open
	// them from a row: Shift-W the request path, Shift-B the probe. Each
	// says why when the object has none.
	handOff := func(label string, reason func(SelectedResourceTarget) string, open func(SelectedResourceTarget)) ui.KeyAction {
		action := ui.NewKeyAction(label, do(func() {
			target := v.SelectedResource()
			switch why := reason(target); {
			case !v.current():
				v.app.Flash().Warn("Destination changed; reopen :graph")
			case why != "":
				v.app.Flash().Warn(why)
			default:
				open(target)
			}
		}), true)
		action.Availability = func() string { return reason(v.SelectedResource()) }
		return action
	}
	v.actions.Add(slowKey, handOff(slowKeyLabel, func(t SelectedResourceTarget) string { return slowAvailability(v.app, t) }, v.app.openSlow))
	v.actions.Add(probeKey, handOff("Probe pods", probeAvailability, v.app.openProbe))
	perPod := ui.NewKeyActionWithOpts("Per pod", charts(v.toggleCompare), opts(1))
	perPod.Availability = func() string {
		if !v.target.workload() {
			return graphNoPeers
		}
		return ""
	}
	v.actions.Bulk(ui.KeyMap{
		ui.KeyP:      perPod,
		ui.KeyG:      ui.NewKeyActionWithOpts("Regroup", charts(func() { v.regroup(1) }), opts(3)),
		ui.KeyShiftG: ui.NewKeyActionWithOpts("Regroup back", charts(func() { v.regroup(-1) }), ui.ActionOpts{Visible: pods}),
		// Tab is the next-chart key of every incident view. It is the same
		// step as n here, so the header names n only: a negative priority
		// keeps a key out of the header's primary hints.
		calmNextChartAltKey: ui.NewKeyActionWithOpts(calmNextChartLabel, charts(func() { v.step(1) }), ui.ActionOpts{Priority: -1}),
		tcell.KeyBacktab:    ui.NewKeyAction("Previous chart", charts(func() { v.step(-1) }), false),
		calmDetailKey:       ui.NewKeyActionWithOpts(calmDetailLabel, charts(v.toggleDetail), ui.ActionOpts{Visible: true, Priority: 4}),
		ui.KeyT:             ui.NewKeyAction("Timeline", do(v.openTimeline), true),
	})
	v.bindStep()
	if pods {
		// The calm comparison names these on its own key lines, where they
		// apply: Enter opens the Pod it marks, PgUp and PgDn turn its pages.
		v.actions.Bulk(ui.KeyMap{
			tcell.KeyEnter: ui.NewKeyActionWithOpts("Open the marked Pod", v.openPodCmd, ui.ActionOpts{Priority: -1}),
			tcell.KeyPgDn:  ui.NewKeyAction("More Pods", v.pageCmd(1), false),
			tcell.KeyPgUp:  ui.NewKeyAction("Earlier Pods", v.pageCmd(-1), false),
		})
	}
	v.render()
	return nil
}

// toggleDetail switches between the calm page and the page with all the
// detail: every chart stacked, or on the per-Pod page a chart per Pod and
// the table of every signal. It reads nothing.
func (v *graphView) toggleDetail() {
	v.detail = !v.detail
	v.page = 0
	if v.detail && !v.comparing && v.snapshot != nil {
		// The chart that was at full size is the one the stacked page
		// starts at.
		for index := range v.snapshot.Plan.Panels {
			if v.snapshot.Plan.Panels[index].ID == v.shown {
				v.first = index
			}
		}
	}
	v.render()
}

// step moves to the next or the previous of what the page steps through:
// the signal on the per-Pod page, the chart elsewhere.
func (v *graphView) step(delta int) {
	if v.comparing {
		v.stepSignal(delta)
		return
	}
	v.stepChart(delta)
}

// stepChart makes the next chart the one drawn at full size. On the page
// with every chart it scrolls to it instead.
func (v *graphView) stepChart(delta int) {
	if v.snapshot == nil || v.snapshot.Window != v.window {
		return
	}
	if v.snapshot.failure() != nil && !v.loading {
		// A key named in the header never does nothing.
		v.app.Flash().Warn("No chart to step to: the read above did not succeed; r reads again")
		return
	}
	panels := v.snapshot.Plan.Panels
	if v.detail {
		v.first = min(max(v.first+delta, 0), max(len(panels)-1, 0))
		v.render()
		return
	}
	if len(panels) < 2 {
		v.app.Flash().Warn("There is no other chart to show")
		return
	}
	at := 0
	for index := range panels {
		if panels[index].ID == v.shown {
			at = index
		}
	}
	v.main = panels[(at+delta+len(panels))%len(panels)].ID
	v.render()
}

// openTimeline opens what changed around the object the charts are of.
func (v *graphView) openTimeline() {
	if !v.current() {
		v.app.Flash().Warn("Destination changed; reopen :graph")
		return
	}
	v.app.openTimeline(v.SelectedResource(), "")
}

// openPodCmd opens the charts of the Pod the calm comparison marks: the
// Pod that differs in the selected signal.
func (v *graphView) openPodCmd(event *tcell.EventKey) *tcell.EventKey {
	switch {
	case v.report || !v.comparing || v.detail:
		return event
	case !v.current():
		v.app.Flash().Warn("Destination changed; reopen :graph")
	case v.open == "":
		v.app.Flash().Warn("No Pod is marked on this page")
	default:
		v.app.openGraphAt(SelectedResourceTarget{Context: v.target.Context, GVR: client.PodGVR, Namespace: v.target.Namespace, Name: v.open}, v.window, "")
	}
	return nil
}

// pageCmd turns the page of the per-Pod comparison when it has more Pods
// than it shows. Elsewhere the key stays with the text.
func (v *graphView) pageCmd(delta int) ui.ActionHandler {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if v.report || !v.comparing || v.snapshot == nil {
			return event
		}
		if v.detail {
			v.page = max(v.page+delta*max(v.pageSize, 1), 0)
		} else {
			v.peers = min(max(v.peers+delta, 0), max(v.peerPages-1, 0))
		}
		v.render()
		return nil
	}
}

// comparable says whether the comparison keys apply now, and tells the user
// why when they do not: a key named in the header never does nothing.
func (v *graphView) comparable() bool {
	switch {
	case !v.target.workload():
		v.app.Flash().Warn(graphNoPeers)
		return false
	case v.snapshot != nil && v.snapshot.Window == v.window && v.snapshot.failure() != nil && !v.loading:
		v.app.Flash().Warn("Nothing to compare: the read above did not succeed; r reads again")
		return false
	}
	return true
}

// toggleCompare switches between the overlaid lines and one chart per Pod.
func (v *graphView) toggleCompare() {
	if !v.comparable() {
		return
	}
	v.comparing = !v.comparing
	v.nameStep()
	v.ensureCompare()
	v.render()
}

// bindStep binds n and Shift-N, which step through what the page shows,
// under the name of that: the chart at full size on the charts of an
// object, the marked signal on the per-Pod page. The page writes the same
// word on its line of next steps.
func (v *graphView) bindStep() {
	next, previous := calmNextChartLabel, "Previous chart"
	if v.comparing {
		next, previous = "Next signal", "Previous signal"
	}
	// While the report is open these keys stay with the text.
	charts := func(run func()) ui.ActionHandler {
		return func(event *tcell.EventKey) *tcell.EventKey {
			if v.report {
				return event
			}
			run()
			return nil
		}
	}
	v.actions.Bulk(ui.KeyMap{
		calmNextChartKey: ui.NewKeyActionWithOpts(next, charts(func() { v.step(1) }), ui.ActionOpts{Visible: true, Priority: 2}),
		ui.KeyShiftN:     ui.NewKeyActionWithOpts(previous, charts(func() { v.step(-1) }), ui.ActionOpts{Visible: v.target.workload()}),
	})
}

// nameStep names n again after the page changed between the charts and
// the per-Pod page, and shows the new name in the menu.
func (v *graphView) nameStep() {
	v.bindStep()
	if v.app.Content != nil && v.app.Content.Top() == v {
		v.app.Menu().HydrateMenu(v.Hints())
	}
}

// regroup cycles how the series are folded: by pod, container, revision,
// node and zone. From the overlaid charts it opens the comparison.
func (v *graphView) regroup(delta int) {
	if !v.comparable() {
		return
	}
	v.comparing = true
	v.nameStep()
	v.grouping = (v.grouping + graphGrouping(delta) + graphGroupings) % graphGroupings
	v.page = 0
	v.ensureCompare()
	v.render()
}

// stepSignal cycles the signal of the comparison. The calm page steps in
// the order it shows them, the ones in which a Pod differs first; the page
// with a chart per Pod in the order they are read in.
func (v *graphView) stepSignal(delta int) {
	if !v.comparable() {
		return
	}
	if v.compare == nil || v.compare.Base != v.snapshot || len(v.compare.Signals) == 0 {
		return
	}
	if !v.detail {
		order, _, _ := graphCalmSignals(v.compare, v.grouping)
		if len(order) == 0 {
			return
		}
		at := 0
		for index := range order {
			if order[index].sig.ID == v.signal {
				at = index
			}
		}
		v.signal = order[(at+delta+len(order))%len(order)].sig.ID
		v.peers = 0
		v.render()
		return
	}
	_, index := v.compare.signal(v.signal)
	n := len(v.compare.Signals)
	v.signal = v.compare.Signals[(index+delta+n)%n].ID
	v.page = 0
	v.render()
}

// ensureCompare starts the read the comparison still needs, if any: every
// signal per Pod for the snapshot shown, and the per-container series the
// first time the series are regrouped by container. Each is asked once per
// snapshot; r reads again.
func (v *graphView) ensureCompare() {
	if !v.comparing || v.snapshot == nil || v.snapshot.Window != v.window || v.snapshot.failure() != nil || v.loadCompare == nil {
		return
	}
	if v.loading {
		// The charts are being read again: the comparison is read for the
		// snapshot that arrives, never in place of it.
		return
	}
	switch {
	case v.compare == nil || v.compare.Base != v.snapshot:
		if v.compareAsked != v.snapshot {
			v.compareAsked = v.snapshot
			v.readCompare(nil, "Reading every signal once per Pod…")
		}
	case v.grouping == graphByContainer && !v.compare.Containers:
		if v.containersAsked != v.compare {
			v.containersAsked = v.compare
			v.readCompare(v.compare, "Reading the per-container series once…")
		}
	}
}

// readCompare starts one bounded read of the comparison off the UI thread.
// A newer request or leaving the view cancels it; a late reply is dropped.
func (v *graphView) readCompare(earlier *graphCompare, busy string) {
	if !v.current() {
		v.app.Flash().Warn("Destination changed; reopen :graph")
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, base, load := v.generation, v.snapshot, v.loadCompare
	ctx, cancel := context.WithTimeout(context.Background(), graphDeadline)
	v.cancel = cancel
	v.busy, v.comparePending = busy, true
	go func() {
		defer cancel()
		compare := load(ctx, base, earlier)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && generation == v.generation && v.current() {
				v.acceptCompare(compare)
			}
		})
	}()
}

func (v *graphView) acceptCompare(compare *graphCompare) {
	v.busy, v.comparePending = "", false
	if compare == nil {
		v.app.Flash().Warn("The per-Pod series were not read: the Prometheus source is unavailable; :sources shows why, r reads again")
	} else if compare.Base == v.snapshot {
		v.compare = compare
	}
	v.ensureCompare()
	v.render()
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
		// The step is local: the handler is called again and again, and a
		// Shift press must not make every later press ten times larger.
		step := delta
		if event != nil && event.Modifiers()&tcell.ModShift != 0 {
			step *= 10
		}
		last := int(v.snapshot.Window / v.snapshot.Step)
		v.cursor = min(max(v.cursor+step, 0), last)
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
		switch {
		case v.comparing && v.detail:
			// A page of charts at a time; the layout settles the bounds.
			v.page = max(v.page+delta*max(v.pageSize, 1), 0)
			v.render()
		case v.comparing:
			v.stepSignal(delta)
		default:
			// The calm page has one chart at full size: the arrows step it,
			// as they scroll the page with every chart.
			v.stepChart(delta)
		}
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

// reread reads the page again: the charts, and with them the comparison.
func (v *graphView) reread() {
	v.compare, v.compareAsked, v.containersAsked = nil, nil, nil
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
		return
	}
	// A comparison read that leaving the view cancelled is asked again.
	if v.busy == "" {
		v.ensureCompare()
		v.render()
	}
}

func (v *graphView) Stop() {
	v.active, v.busy, v.loading = false, "", false
	if v.comparePending {
		// The read is cancelled below; it was never answered, so coming
		// back asks it again instead of showing a page that has no data.
		v.comparePending, v.compareAsked, v.containersAsked = false, nil, nil
	}
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
	v.loading, v.comparePending = true, false
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
	v.busy, v.loading = "", false
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
				// The chart a caller arrives about is the one at full size.
				v.first, v.main = index, v.focus
			}
		}
		v.focus = ""
	}
	v.snapshot = snapshot
	// The comparison belongs to the read it was made for.
	v.compare = nil
	v.ensureCompare()
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
		text := graphReport(v.snapshot)
		if v.compare != nil && v.compare.Base == v.snapshot {
			text += graphCompareReport(v.compare)
		}
		text += "\ns or Esc returns to the charts · r re-read"
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
		Cursor: v.cursor, First: v.first, Busy: v.busy, Style: v.app.chartStyle(),
		Palette: calmPalette(v.app.Styles.Semantic()), Main: v.main, Moved: !v.atEnd, Offers: v.offers(),
		Episodes: v.episodes, Peers: v.peers, Namespace: v.app.Config.ActiveNamespace(),
	}
	var lines []string
	switch {
	case v.snapshot == nil || v.snapshot.Window != v.window:
		// Charts of another window are not shown under the new window's name.
		name := graphTitle
		if v.comparing {
			name = graphCompareTitle
		}
		waiting := graphCalmWaiting(name, graphCalmObject(&v.target, page), v.window, "Reading the declared Prometheus source once…")
		lines = graphCalmLines(&waiting, page)
	case v.comparing && v.detail:
		page.First, page.Signal, page.Grouping = v.page, v.signal, v.grouping
		lines = renderGraphCompare(v.snapshot, v.compare, page)
		v.page, v.pageSize, v.signal = page.First, page.Shown, page.Signal
	case v.comparing:
		page.Signal, page.Grouping = v.signal, v.grouping
		lines = renderGraphCompareCalm(v.snapshot, v.compare, page)
		v.signal, v.peers, v.peerPages, v.open = page.Signal, page.Peers, page.PeerPages, page.Open
	case v.detail:
		lines = renderGraph(v.snapshot, page)
		// The page settles the scroll position: a chart that fits is shown.
		v.first = page.First
	default:
		lines = renderGraphCalm(v.snapshot, page)
		v.shown = page.Main
	}
	v.dropped = page.Dropped
	if v.detail && len(lines) > 0 && v.snapshot != nil && v.snapshot.Window == v.window {
		// The page with all the detail names the way back where its first
		// line has room; the key is in the header too.
		hint := graphWords("  ·  a short page", page.ASCII)
		if tview.TaggedStringWidth(lines[0])+runewidth.StringWidth(hint) <= width {
			lines[0] += graphPaint(page.Colors.Muted, hint)
		}
	}
	v.text.SetText(strings.Join(lines, "\n"))
	v.text.ScrollToBeginning()
}

// offers are the hand-offs that answer for this object now: the line of
// next steps names a key only when it opens something.
func (v *graphView) offers() graphOffers {
	offers := graphOffers{Timeline: true}
	if action, ok := v.actions.Get(grafanaKey); ok && action.Availability != nil {
		offers.Grafana = action.Availability() == ""
	}
	if action, ok := v.actions.Get(probeKey); ok && action.Availability != nil {
		offers.Probe = action.Availability() == ""
	}
	return offers
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
	marks, note, _ = graphObjectFacts(ctx, dial, t)
	return marks, note
}

// graphObjectFacts is graphMarksFrom plus the revision number of every
// ReplicaSet or ControllerRevision those same reads named, so that a group
// of Pods can be titled "rev 2" instead of only by its hash.
func graphObjectFacts(ctx context.Context, dial dynamic.Interface, t *graphTarget) (marks []graphMark, note string, revisions map[string]int64) {
	revisions = make(map[string]int64)
	replicaSets := dial.Resource(client.RsGVR.GVR()).Namespace(t.Namespace)
	rollout := func(rs *unstructured.Unstructured) {
		revision := rs.GetAnnotations()[graphRevisionKey]
		created := rs.GetCreationTimestamp()
		number, err := strconv.Atoi(revision)
		if err != nil || created.IsZero() {
			return
		}
		revisions[rs.GetName()] = int64(number)
		marks = append(marks, graphMark{Key: 'R', T: created.UTC(), Label: "rollout rev " + revision,
			Origin: "creation time of ReplicaSet " + rs.GetName() + " (revision " + revision + ")"})
	}
	switch t.Kind {
	case "Pod":
		pod, err := dial.Resource(client.PodGVR.GVR()).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return nil, "The Pod was not read, so no object times are marked.", revisions
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
			return nil, "The revision history was not read, so no rollout time is marked.", revisions
		}
		for i := range history.Revisions {
			revisions[history.Revisions[i].Name] = history.Revisions[i].Number
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
	return marks, note, revisions
}

// graphServiceMembers resolves the Pods behind a Service with the bounded,
// UID-checked collector of :network-review: the Pods its selector matches
// and the Pods its EndpointSlices name. It reads configuration only.
func graphServiceMembers(ctx context.Context, dial dynamic.Interface, t *graphTarget) *graphMembers {
	members := &graphMembers{}
	snapshot, err := networkpath.Collect(ctx, dial, networkpath.Scope{Service: inspect.ResourceIdentity{
		Context: t.Context, GVR: networkpath.ServiceGVR, Namespace: t.Namespace, Name: t.Name, UID: t.UID}})
	failed := ""
	if err != nil {
		// The collector's own sentences name no address and no credential.
		failed = observability.Clean(err.Error(), 200) + "."
	}
	if snapshot == nil {
		members.Problem = strings.TrimSpace("The Service was not read, so the Pods behind it are not known. " + failed)
		return members
	}
	seen := make(map[string]bool)
	for i := range snapshot.Pods {
		if name := snapshot.Pods[i].Source.Identity.Name; name != "" && !seen[name] {
			seen[name] = true
			members.Pods = append(members.Pods, name)
		}
	}
	sort.Strings(members.Pods)
	members.Origin = fmt.Sprintf("%d Pods from the Service's selector and its EndpointSlice targets, read %s.", len(members.Pods), graphClock(snapshot.CapturedAt))
	if snapshot.Omitted > 0 {
		members.Notes = append(members.Notes, fmt.Sprintf("%d endpoint targets beyond the read limit were not read.", snapshot.Omitted))
	}
	for i := range snapshot.Coverage {
		if cov := &snapshot.Coverage[i]; cov.GVR == networkpath.PodGVR && cov.State != "" && cov.Detail != "" {
			members.Notes = append(members.Notes, "Pods: "+observability.Clean(cov.Detail, 200)+".")
		}
	}
	switch {
	case len(members.Pods) == 0 && failed != "":
		members.Problem = "The Pods behind the Service were not resolved: " + failed
	case len(members.Pods) == 0:
		members.Problem = "The Service selects no Pod now: its selector and its EndpointSlices name none."
	case failed != "":
		members.Notes = append(members.Notes, "The read did not complete, so Pods may be missing: "+failed)
	}
	return members
}

// graphServiceFor finds the Service whose request-path declaration chooses
// the app-hop profiles for the Pods of a workload: a Service of the
// namespace whose selector matches the workload's Pod template. It is one
// Get and one bounded list, and a Service with a declaration is preferred.
// Without a Service the default profiles apply, and the note says so.
func graphServiceFor(ctx context.Context, dial dynamic.Interface, t *graphTarget, sources *observability.Sources) graphServicePath {
	if t.Kind == graphKindService {
		return graphServicePath{Path: t.Namespace + "/" + t.Name, Note: "the Service the graph was opened on"}
	}
	const fallback = "; the default app-hop profiles apply"
	if dial == nil || !t.owned() {
		return graphServicePath{Note: "no Kubernetes connection to find the Service in front of these Pods" + fallback}
	}
	ctx, cancel := context.WithTimeout(ctx, observability.DefaultTimeout)
	defer cancel()
	object, err := dial.Resource(client.NewGVR(t.Resource).GVR()).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return graphServicePath{Note: "the " + t.Kind + " was not read, so the Service in front of its Pods is not known" + fallback}
	}
	labels, _, _ := unstructured.NestedStringMap(object.Object, "spec", "template", "metadata", "labels")
	if len(labels) == 0 {
		return graphServicePath{Note: "the " + t.Kind + " has no Pod template labels to match a Service by" + fallback}
	}
	// The one list and the one choice :slow makes for the same Pods.
	names, read, more, err := slowpath.SelectingServices(ctx, dial, t.Namespace, labels)
	if err != nil {
		return graphServicePath{Note: "the Services of the namespace were not read" + fallback}
	}
	cut := ""
	if more {
		cut = fmt.Sprintf(" (only the first %d Services of the namespace were read)", read)
	}
	if len(names) == 0 {
		return graphServicePath{Note: "no Service selects this " + t.Kind + "'s Pod template" + cut + fallback}
	}
	how := "its selector matches the Pod template"
	if len(names) > 1 {
		how = fmt.Sprintf("the first by name of %d Services whose selector matches the Pod template", len(names))
	}
	chosen, declared := slowpath.Preferred(names, t.Namespace, sources)
	if declared {
		how = "its selector matches the Pod template and it has a requestPath declaration"
	}
	return graphServicePath{Path: t.Namespace + "/" + chosen, Note: how + cut}
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
