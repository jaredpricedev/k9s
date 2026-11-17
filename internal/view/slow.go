// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	slowCommandToken = "slow"
	slowCommandAlias = "requests"
	slowTitle        = "Slow requests"
	slowKeyLabel     = "Slow requests: which hop"
	// slowKey opens the view from a Service, Ingress, workload or Pod row,
	// and from :probe. slowKeyName is how hints write it.
	slowKey     = ui.KeyShiftW
	slowKeyName = "Shift-W"
	// slowDeadline covers the bounded Kubernetes reads and the batch.
	slowDeadline = slowpath.Deadline + 20*time.Second
)

var slowKinds = map[string]string{
	client.SvcGVR.String(): slowpath.KindService, client.IngGVR.String(): slowpath.KindIngress,
	client.DpGVR.String(): slowpath.KindDeployment, client.StsGVR.String(): slowpath.KindStatefulSet,
	client.DsGVR.String(): slowpath.KindDaemonSet, client.PodGVR.String(): slowpath.KindPod,
}

// slowRead is the outcome of one explicit read. The resolved path is kept
// so that changing the window sends the batch again without re-reading it.
type slowRead struct {
	path   *slowpath.Path
	report *slowpath.Report
	err    error
}

// slowRow is one table row: a hop, or one Pod of the app hop.
type slowRow struct {
	hop string
	pod string
}

// slowView is the on-demand request-path view. It reads when opened, when
// the window changes and on r; it never polls, and a reply that arrives
// after the view closed or its destination changed is discarded.
type slowView struct {
	*Details
	selection  slowpath.Selection
	revision   uint64
	loader     func(ctx context.Context, path *slowpath.Path, window time.Duration) slowRead
	read       *slowRead
	window     time.Duration
	cancel     context.CancelFunc
	generation uint64
	active     bool
	loading    bool
	rendering  bool

	header, body, footer *tview.TextView
	table                *tview.Table
	rows                 []slowRow
	focus                slowRow
	// expanded says whether the Pods of the app hop are listed; nil picks
	// by the room there is.
	expanded *bool
	// detail swaps the lanes for the queries and rules of the focused hop;
	// detailTop is its first visible line and detailPage its height.
	detail                bool
	detailTop, detailPage int
	// cursor is a lane column, or -1 while the cursor is hidden; cursorAt
	// is the time it stands on, so that it keeps its place when the strip
	// is redrawn with wider or narrower columns.
	cursor   int
	cursorAt time.Time
	frame    chart.Frame

	width, height             int
	headerHeight, tableHeight int
	observed                  *HubbleView
}

// slowTarget says whether a selection has a request path to read.
//
//nolint:gocritic // The immutable selection is captured by value.
func slowTarget(target SelectedResourceTarget) (slowpath.Selection, error) {
	const hint = "Select a Service, an Ingress, a Deployment, a StatefulSet or a Pod first: a request path belongs to a Service"
	if target.Err() != nil || target.GVR == nil {
		return slowpath.Selection{}, errors.New(hint)
	}
	kind, ok := slowKinds[target.GVR.String()]
	if !ok || target.Namespace == "" || target.Name == "" {
		return slowpath.Selection{}, errors.New(hint)
	}
	return slowpath.Selection{Context: target.Context, Kind: kind, Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, nil
}

func (c *Command) slowCommand() {
	target := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	if owner, ok := c.app.Content.Top().(actionOwner); ok {
		target = actionTarget(owner, c.app.Config.ActiveContextName())
	}
	c.app.openSlow(target)
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openSlow(target SelectedResourceTarget) {
	target.Context = a.Config.ActiveContextName()
	selection, err := slowTarget(target)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if a.Conn() == nil || a.Conn().Config() == nil {
		a.Flash().Warn("Select a configured context first")
		return
	}
	cfg, err := a.Conn().Config().Snapshot(selection.Context).RESTConfig()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	objects, err := dynamic.NewForConfig(cfg)
	if err != nil {
		a.Flash().Err(errors.New("captured resource transport unavailable"))
		return
	}
	// The declaration and the connection are captured here, on the UI
	// thread; resolving a source contacts nothing. Each read opens and
	// closes its own client, off the UI thread.
	sources, restConfig := declaredSources(a), kubeRESTConfig(a)
	v := newSlowView(a, selection)
	v.loader = func(ctx context.Context, path *slowpath.Path, window time.Duration) slowRead {
		return readSlow(ctx, objects, sources, restConfig, selection, path, window)
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

// readSlow makes one read: the configured path from the Kubernetes API
// (unless it was already resolved), then one bounded batch of range reads
// from the declared Prometheus, then the alerts of the namespace when an
// Alertmanager is declared.
//
//nolint:gocritic // The selection is a small captured value.
func readSlow(ctx context.Context, objects dynamic.Interface, sources *observability.Sources, restConfig observability.RESTConfigFunc,
	selection slowpath.Selection, path *slowpath.Path, window time.Duration,
) slowRead {
	var read slowRead
	if path == nil {
		if path, read.err = slowpath.Resolve(ctx, objects, sources, selection); read.err != nil {
			return read
		}
	}
	read.path = path
	plan := slowpath.BuildPlan(sources, path, window, time.Now())
	var (
		results []observability.PromResult
		display string
	)
	source, status := observability.Resolve(sources, observability.KindPrometheus, restConfig)
	if source == nil {
		if status.State == observability.StateNotConfigured {
			status.Reason = "no Prometheus source is declared for this context; :sources shows how to declare one"
		}
		results = slowpath.Unread(plan, status)
	} else {
		defer source.Close()
		display = source.Display()
		results = slowpath.Collect(ctx, source, plan)
	}
	read.report = slowpath.Evaluate(plan, results, readSlowAlerts(ctx, sources, restConfig, path.Namespace), time.Now())
	read.report.Source = display
	return read
}

// readSlowAlerts reads the alerts of one namespace once, when the context
// declares an Alertmanager. Without a declaration there is no alerts lane.
func readSlowAlerts(ctx context.Context, sources *observability.Sources, restConfig observability.RESTConfigFunc, namespace string) *slowpath.Alerts {
	if sources.Source(observability.KindAlertmanager) == nil {
		return nil
	}
	source, status := observability.Resolve(sources, observability.KindAlertmanager, restConfig)
	if source == nil {
		return &slowpath.Alerts{Note: status.String()}
	}
	defer source.Close()
	result := source.Alerts(ctx, observability.AlertQuery{Matchers: []observability.Matcher{observability.Eq("namespace", namespace)}})
	if !result.OK() {
		return &slowpath.Alerts{Note: result.Status.String()}
	}
	alerts := &slowpath.Alerts{Read: true, Note: source.Display()}
	for i := range result.Alerts {
		alert := &result.Alerts[i]
		if alert.State == observability.AlertActive {
			alerts.Firing = append(alerts.Firing, slowpath.AlertSpan{Name: alert.Name(), Start: alert.StartsAt, End: alert.EndsAt})
		}
	}
	return alerts
}

func newSlowView(app *App, selection slowpath.Selection) *slowView { //nolint:gocritic // A small captured value.
	return &slowView{
		Details:   NewDetails(app, slowTitle, selection.Label(), contentTXT, false),
		selection: selection, revision: app.Config.DestinationRevision(), window: slowpath.DefaultWindow, cursor: -1,
	}
}

func slowAvailability(app *App, target SelectedResourceTarget) string { //nolint:gocritic // Immutable selection.
	if _, err := slowTarget(target); err != nil {
		return err.Error()
	}
	sources := declaredSources(app)
	if sources.Source(observability.KindPrometheus) == nil && (sources == nil || sources.Grafana == nil || sources.Grafana.Datasource == "") {
		return "No Prometheus source is declared for this context; :sources shows what to add"
	}
	return ""
}

// slowActions registers the view with the action palette and help, with
// the reason it is unavailable for the selection.
func slowActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	return []ui.ActionDescriptor{{
		ID: "resource.slow", Label: slowKeyLabel, Category: ui.ActionInspect,
		Shortcut: ":" + slowCommandToken, Discoverable: true, RequiresSelection: true, UnavailableReason: slowAvailability(app, target),
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openSlow(target); return nil },
	}}
}

// slowKeyAction is the list key on Services, Ingresses, workloads and Pods.
func slowKeyAction(app *App, owner actionOwner) ui.KeyAction {
	return ui.NewKeyActionWithOpts(slowTitle, func(*tcell.EventKey) *tcell.EventKey {
		app.openSlow(actionTarget(owner, app.Config.ActiveContextName()))
		return nil
	}, ui.ActionOpts{Visible: true, RequiresSelection: true})
}

func (*slowView) CompactWorkspace() bool { return true }

func (v *slowView) report() *slowpath.Report {
	if v.read == nil {
		return nil
	}
	return v.read.report
}

func (v *slowView) path() *slowpath.Path {
	if v.read == nil {
		return nil
	}
	return v.read.path
}

// serviceTarget is the Service whose path is read, or the selection while
// it is unresolved.
func (v *slowView) serviceTarget() SelectedResourceTarget {
	target := SelectedResourceTarget{Context: v.selection.Context, Namespace: v.selection.Namespace}
	if path := v.path(); path != nil {
		target.GVR, target.Name, target.UID = client.SvcGVR, path.Service, types.UID(path.ServiceUID)
		return target
	}
	for gvr, kind := range slowKinds {
		if kind == v.selection.Kind {
			target.GVR = client.NewGVR(gvr)
		}
	}
	target.Name, target.UID = v.selection.Name, types.UID(v.selection.UID)
	return target
}

// workloadTarget is the focused Pod, else the workload that controls the
// backends, else the Service: what a graph, a timeline or a dashboard is
// about.
func (v *slowView) workloadTarget() SelectedResourceTarget {
	target := SelectedResourceTarget{Context: v.selection.Context, Namespace: v.selection.Namespace}
	path := v.path()
	if path == nil {
		return v.serviceTarget()
	}
	if v.focus.pod != "" {
		for i := range path.Pods {
			if path.Pods[i].Name == v.focus.pod {
				target.GVR, target.Name, target.UID = client.PodGVR, path.Pods[i].Name, types.UID(path.Pods[i].UID)
				return target
			}
		}
	}
	if path.Workload.Name != "" {
		target.GVR, target.Name = client.NewGVR(path.Workload.Resource()), path.Workload.Name
		return target
	}
	return v.serviceTarget()
}

// SelectedResource exposes what the view is about to shared actions: the
// focused Pod or the workload.
func (v *slowView) SelectedResource() SelectedResourceTarget { return v.workloadTarget() }

// GrafanaWindow hands the window that was read to a Grafana link.
func (v *slowView) GrafanaWindow() (grafanaWindow, bool) {
	report := v.report()
	if report == nil {
		return grafanaWindow{}, false
	}
	return grafanaWindow{From: report.Start, To: report.End, Reason: "the window :slow read, " + report.Span()}, true
}

func (v *slowView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.body = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	// The view draws its own selection (a marker and a row surface), so the
	// state colors of the focused row stay as they are.
	v.table = tview.NewTable().SetSelectable(false, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.headerHeight, v.tableHeight = 2, 2
	v.Flex.Clear().SetDirection(tview.FlexRow).
		AddItem(v.header, v.headerHeight, 0, false).
		AddItem(v.table, v.tableHeight, 0, true).
		AddItem(v.body, 0, 1, false).
		AddItem(v.footer, 1, 0, false)
	v.bindSlowKeys()
	v.render()
	return nil
}

func (v *slowView) bindSlowKeys() {
	v.actions.Delete(ui.KeyC, ui.KeyF, ui.KeyN, ui.KeyShiftN, ui.KeySlash, tcell.KeyCtrlS, tcell.KeyDelete)
	do := func(run func()) ui.ActionHandler {
		return func(*tcell.EventKey) *tcell.EventKey {
			run()
			return nil
		}
	}
	// The windows and their keys are the ones of :graph, the dashboards
	// and :query; the cursor keys are theirs too.
	for index, window := range graphWindows {
		v.actions.Add(ui.Key1+tcell.Key(index), ui.NewKeyAction("Window "+impact.Dur(window), do(func() { v.setWindow(window) }), true))
	}
	// Shift moves the cursor ten columns, as in every chart view. The step
	// is local to one press.
	cursor := func(delta int) ui.ActionHandler {
		return func(event *tcell.EventKey) *tcell.EventKey {
			step := delta
			if event != nil && event.Modifiers()&tcell.ModShift != 0 {
				step *= 10
			}
			v.moveCursor(step)
			return nil
		}
	}
	v.actions.Bulk(ui.KeyMap{
		// Enter is in the key line under the view at every size; the line
		// above it names the hand-offs first.
		tcell.KeyEnter: ui.NewKeyActionWithOpts("Open row", do(v.enter), ui.ActionOpts{Visible: true, Priority: 4}),
		// The two hand-offs that follow the evidence are named in the key
		// line above the view, where a short screen has no next: line.
		probeKey:       ui.NewKeyActionWithOpts("Probe pods", do(v.openProbe), ui.ActionOpts{Visible: true, Priority: 2}),
		ui.KeyM:        ui.NewKeyActionWithOpts(graphPodsShort, do(v.openGraph), ui.ActionOpts{Visible: true, Priority: 3}),
		ui.KeyH:        ui.NewKeyAction("Hubble flows", do(v.openHubble), true),
		ui.KeyT:        ui.NewKeyAction("Timeline", do(v.openTimeline), true),
		ui.KeyS:        ui.NewKeyAction("Queries and rules", do(v.toggleDetail), true),
		ui.KeyR:        ui.NewKeyAction("Re-read", do(func() { v.refresh(true) }), true),
		tcell.KeyUp:    ui.NewKeyAction("Previous hop", do(func() { v.moveFocus(-1) }), false),
		tcell.KeyDown:  ui.NewKeyAction("Next hop", do(func() { v.moveFocus(1) }), false),
		ui.KeyK:        ui.NewKeyAction("Previous hop", do(func() { v.moveFocus(-1) }), false),
		ui.KeyJ:        ui.NewKeyAction("Next hop", do(func() { v.moveFocus(1) }), false),
		tcell.KeyLeft:  ui.NewKeyAction("Cursor earlier", cursor(-1), false),
		tcell.KeyRight: ui.NewKeyAction("Cursor later", cursor(1), false),
		tcell.KeyHome:  ui.NewKeyAction("Cursor to window start", do(func() { v.cursorEdge(-1) }), false),
		tcell.KeyEnd:   ui.NewKeyAction("Cursor to window end", do(func() { v.cursorEdge(1) }), false),
		ui.KeyN:        ui.NewKeyAction("Cursor to next episode", do(func() { v.jumpEpisode(1) }), false),
		ui.KeyShiftN:   ui.NewKeyAction("Cursor to previous episode", do(func() { v.jumpEpisode(-1) }), false),
		tcell.KeyPgDn:  ui.NewKeyAction("Queries: page down", do(func() { v.scrollDetail(1) }), false),
		tcell.KeyPgUp:  ui.NewKeyAction("Queries: page up", do(func() { v.scrollDetail(-1) }), false),
		tcell.KeyCtrlF: ui.NewKeyAction("Queries: page down", do(func() { v.scrollDetail(1) }), false),
		tcell.KeyCtrlB: ui.NewKeyAction("Queries: page up", do(func() { v.scrollDetail(-1) }), false),
		grafanaKey:     grafanaKeyAction(v.app, func() actionOwner { return v }, nil),
		// The trends of the hop table follow the chart style of the
		// session, as the charts a hop opens do.
		chartStyleKey: chartStyleAction(v.app, v.render),
	})
	back := func(event *tcell.EventKey) *tcell.EventKey {
		if v.cursor >= 0 {
			// Esc first puts the cursor away; a second Esc leaves.
			v.cursor = -1
			v.render()
			return nil
		}
		return v.app.PrevCmd(event)
	}
	v.actions.Add(tcell.KeyEscape, ui.NewKeyAction("Back", back, false))
	v.actions.Add(ui.KeyQ, ui.NewKeyAction("Back", func(event *tcell.EventKey) *tcell.EventKey { return v.app.PrevCmd(event) }, false))
	// The palette and help say why a hand-off would not open, as the key
	// itself does.
	for key, reason := range map[tcell.Key]func() string{
		ui.KeyM:  func() string { return graphAvailability(v.app, v.serviceTarget()) },
		probeKey: func() string { return probeAvailability(v.serviceTarget()) },
	} {
		if action, ok := v.actions.Get(key); ok {
			action.Availability = reason
			v.actions.Add(key, action)
		}
	}
}

func (v *slowView) destinationCurrent() bool {
	return v.selection.Context == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}

// stale warns when the destination changed under the view; nothing is then
// opened from it.
func (v *slowView) stale() bool {
	if v.destinationCurrent() {
		return false
	}
	v.app.Flash().Warn("Destination changed; reopen :" + slowCommandToken)
	return true
}

// openProbe hands the Service to :probe. It opens that view; nothing is
// sent to a Pod until the probe is run and confirmed there.
func (v *slowView) openProbe() {
	if v.stale() {
		return
	}
	target := v.serviceTarget()
	if reason := probeAvailability(target); reason != "" {
		v.app.Flash().Warn(reason)
		return
	}
	v.app.openProbe(target)
}

// slowGraphSignal names the signal the per-Pod comparison of :graph opens
// on: the signal of the app hop that set a Pod apart, else the hop's main
// signal. The second value is its short name. Both are empty when the app
// hop has no reading.
func slowGraphSignal(report *slowpath.Report) (id, name string) {
	if report == nil {
		return "", ""
	}
	app := report.Hop(slowpath.HopApp)
	if app == nil || app.Main < 0 || app.Main >= len(app.Signals) || !app.Signals[app.Main].Read {
		return "", ""
	}
	sig := &app.Signals[app.Main]
	switch sig.Profile {
	case "", graphCPUProfile, slowpath.ProfileCAdvisor, slowpath.ProfileKubeState:
		// :graph reads these with its own templates and names them plainly.
		return sig.Name, sig.Name
	}
	return sig.Profile + "/" + sig.Name, sig.Name
}

// openGraph opens :graph on the Pods of the Service, one chart per Pod, on
// the signal that set a Pod apart here and for the window that was read.
func (v *slowView) openGraph() {
	if v.stale() {
		return
	}
	target, focus := v.serviceTarget(), graphPodsWord
	if reason := graphAvailability(v.app, target); reason != "" {
		v.app.Flash().Warn(reason)
		return
	}
	if t, err := graphTargetFor(target); err != nil || !t.workload() {
		// The path is not resolved yet and one Pod was selected: its own
		// charts are what there is.
		focus = ""
	} else if id, _ := slowGraphSignal(v.report()); id != "" {
		focus += ":" + id
	}
	v.app.openGraphAt(target, v.window, focus)
}

// openPodGraph opens the focused Pod's own charts, usage against its
// limits, for the window that was read.
func (v *slowView) openPodGraph() {
	if v.stale() {
		return
	}
	target := v.workloadTarget()
	if reason := graphAvailability(v.app, target); reason != "" {
		v.app.Flash().Warn(reason)
		return
	}
	v.app.openGraphAt(target, v.window, "")
}

func (v *slowView) openTimeline() {
	if v.stale() {
		return
	}
	v.app.openTimeline(v.workloadTarget(), "")
}

// openHubble opens the existing native Hubble view for the backend Pods,
// as :network-review does. It starts no probe and changes nothing.
func (v *slowView) openHubble() {
	path := v.path()
	if !v.destinationCurrent() || path == nil || len(path.Pods) == 0 {
		v.app.Flash().Warn("No backend Pod is known yet; Hubble is opened for the Pods of the path")
		return
	}
	if v.observed == nil {
		pods := make([]string, 0, len(path.Pods))
		for i := range path.Pods {
			pods = append(pods, client.FQN(path.Namespace, path.Pods[i].Name))
		}
		v.observed = newHubbleView(hubble.Scope{Title: "Service " + path.Key() + " (backend Pod names; flow UIDs unreported)", Pods: pods}, false)
	}
	if err := v.app.inject(v.observed, false); err != nil {
		v.app.Flash().Err(err)
	}
}

// enter opens what the focused row has: the Pods of the app hop, the graph
// of a Pod, or the queries and rules of any other hop.
func (v *slowView) enter() {
	report := v.report()
	switch {
	case report == nil:
	case v.focus.pod != "":
		v.openPodGraph()
	case v.focus.hop == slowpath.HopApp && len(report.Path.Pods) > 0:
		open := !v.podsListed()
		v.expanded = &open
		v.render()
	default:
		v.toggleDetail()
	}
}

// moveFocus moves the row selection.
func (v *slowView) moveFocus(delta int) {
	if len(v.rows) == 0 {
		return
	}
	index := 0
	for i, row := range v.rows {
		if row == v.focus {
			index = i
		}
	}
	v.focus, v.detailTop = v.rows[min(max(index+delta, 0), len(v.rows)-1)], 0
	v.render()
}

func (v *slowView) toggleDetail() {
	v.detail, v.detailTop = !v.detail, 0
	v.render()
}

// scrollDetail pages through the queries and rules of the focused hop.
func (v *slowView) scrollDetail(pages int) {
	if !v.detail {
		return
	}
	v.detailTop = max(v.detailTop+pages*max(v.detailPage-1, 1), 0)
	v.render()
}

func (v *slowView) setWindow(window time.Duration) {
	if window == v.window && v.read != nil && !v.loading {
		return
	}
	v.window, v.cursor = window, -1
	v.refresh(false)
}

// cursorEdge puts the cursor on the first or the last column of the lanes.
func (v *slowView) cursorEdge(direction int) {
	if v.report() == nil || v.frame.Width <= 0 {
		return
	}
	v.cursor = 0
	if direction > 0 {
		v.cursor = v.frame.Width - 1
	}
	v.placeCursor()
}

// moveCursor shows the cursor on the lanes and moves it by delta columns.
func (v *slowView) moveCursor(delta int) {
	if v.report() == nil || v.frame.Width <= 0 {
		return
	}
	switch {
	case v.cursor < 0 && delta < 0:
		v.cursor = v.frame.Width - 1
	case v.cursor < 0:
		v.cursor = 0
	default:
		v.cursor = min(max(v.cursor+delta, 0), v.frame.Width-1)
	}
	v.placeCursor()
}

// placeCursor remembers the time under the cursor and redraws the lanes.
func (v *slowView) placeCursor() {
	v.cursorAt, _ = v.frame.ColumnSpan(v.cursor)
	v.detail = false
	v.render()
}

// jumpEpisode moves the cursor to the start of the next or previous
// episode.
func (v *slowView) jumpEpisode(direction int) {
	report := v.report()
	if report == nil || v.frame.Width <= 0 {
		return
	}
	if len(report.Episodes) == 0 {
		v.app.Flash().Info("No episode in this window: no signal crossed its rule")
		return
	}
	columns := make([]int, 0, len(report.Episodes))
	for _, episode := range report.Episodes {
		// An episode is named by its first raised sample, one step after
		// its stretch begins; that is the column the cursor lands on.
		if col, ok := v.frame.Col(episode.Start.Add(report.Step)); ok {
			columns = append(columns, col)
		}
	}
	if len(columns) == 0 {
		return
	}
	target := columns[0]
	if direction < 0 {
		target = columns[len(columns)-1]
	}
	if v.cursor >= 0 {
		for _, col := range columns {
			if direction > 0 && col > v.cursor {
				target = col
				break
			}
		}
		for i := len(columns) - 1; i >= 0 && direction < 0; i-- {
			if columns[i] < v.cursor {
				target = columns[i]
				break
			}
		}
	}
	v.cursor = target
	v.placeCursor()
}

func (v *slowView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.read == nil && !v.loading && v.loader != nil {
		v.refresh(true)
	}
}

func (v *slowView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *slowView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.table != nil {
		p := styles.Semantic()
		v.table.SetBackgroundColor(p.Canvas.Color())
		v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(p.Text.Color()).Bold(true))
		for _, item := range []*tview.TextView{v.header, v.body, v.footer} {
			item.SetBackgroundColor(p.Canvas.Color())
			item.SetTextColor(p.Text.Color())
		}
	}
	v.render()
}

func (v *slowView) Draw(screen tcell.Screen) {
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

// refresh starts one bounded read. resolve re-reads the configured path;
// a window change keeps it. A newer request or leaving the view cancels
// the read, and a late reply never replaces a newer one.
func (v *slowView) refresh(resolve bool) {
	if !v.destinationCurrent() || v.loader == nil {
		v.render()
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, window := v.generation, v.window
	var path *slowpath.Path
	if !resolve {
		path = v.path()
	}
	ctx, cancel := context.WithTimeout(context.Background(), slowDeadline)
	v.cancel, v.loading = cancel, true
	v.render()
	go func() {
		defer cancel()
		read := v.loader(ctx, path, window)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.accept(&read)
			}
		})
	}()
}

// accept takes a finished read. A read that failed keeps the path and the
// report that were on screen, and says so.
func (v *slowView) accept(read *slowRead) {
	v.loading = false
	if read.err != nil && v.read != nil && v.read.report != nil {
		v.read.err = read.err
		v.render()
		return
	}
	v.read = read
	v.cursor = -1
	if report := v.report(); report != nil {
		// The focus stays where it was when the row still exists.
		if v.focus.hop == "" || (v.focus.hop != slowDepsFolded && report.Hop(v.focus.hop) == nil) {
			v.focus = v.firstInteresting(report)
		}
	}
	v.render()
}

// firstInteresting focuses the hop with the strongest state, so the detail
// starts on evidence: an outlier, else a raised hop, else the first row.
func (v *slowView) firstInteresting(report *slowpath.Report) slowRow {
	best, rank := slowRow{}, -1
	for i := range report.Hops {
		hop := &report.Hops[i]
		score := 0
		switch hop.State {
		case slowpath.Outlier:
			score = 2
		case slowpath.Elevated:
			score = 1
		}
		if score > rank {
			best, rank = slowRow{hop: hop.Spec.ID}, score
		}
	}
	if path := report.Path; path.Focus != "" {
		// A Pod was selected: its row is the one the user came for.
		expanded := true
		v.expanded = &expanded
		return slowRow{hop: slowpath.HopApp, pod: path.Focus}
	}
	return best
}

func (v *slowView) viewWidth() int {
	if v.width <= 0 {
		return 80
	}
	return v.width
}

func (v *slowView) noIcons() bool { return v.app.Config.K9s.UI.NoIcons }
