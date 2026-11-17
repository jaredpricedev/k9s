// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	queryCommandToken = "query"
	// queryTyped names a page whose expression was typed, not recalled.
	queryTyped     = "typed expression"
	dashViewTitle  = "Dashboard"
	queryViewTitle = "Query"
	dashViewKey    = ui.KeyV
	dashEditKey    = ui.KeyE
)

// dashView draws a Grafana dashboard, or the queries of the :query bar, as
// charts in the terminal. It reads when opened, when the window changes
// and on r; it never polls, and a reply that arrives after the view closed
// or the destination changed is discarded.
type dashView struct {
	*Details
	spec        dashSpec
	target      *grafanaTarget
	origin      SelectedResourceTarget
	selection   dashboard.Selection
	contextName string
	revision    uint64
	window      time.Duration
	snapshot    *dashSnapshot
	// load reads one page. kept is the definition of an earlier read, so
	// that a dashboard's JSON is read once.
	load          func(ctx context.Context, spec *dashSpec, window time.Duration, kept *dashboard.Dashboard) *dashSnapshot
	cancel        context.CancelFunc
	generation    uint64
	active        bool
	busy          string
	selected      int
	first, cursor int
	lines         []dashboard.Line
	zoom, report  bool
	atEnd         bool
	width, height int
}

// dashSelection is what the variables are filled from.
func dashSelection(t *grafanaTarget) dashboard.Selection {
	if t == nil {
		return dashboard.Selection{}
	}
	return dashboard.Selection{Namespace: t.Link.Namespace, Pod: t.Link.Pod, Workload: t.Link.Workload, Node: t.Link.Node}
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func newDashView(app *App, spec dashSpec, target *grafanaTarget, origin SelectedResourceTarget, window time.Duration) *dashView {
	if window <= 0 {
		window = graphDefaultWindow
	}
	title, subject := dashViewTitle, spec.Hint
	if spec.isQuery() {
		title, subject = queryViewTitle, "named queries"
		if spec.Query != nil {
			subject = spec.Query.Name
			if subject == "" {
				subject = queryTyped
			}
		}
	}
	v := &dashView{
		Details: NewDetails(app, title, subject, contentTXT, false),
		spec:    spec, target: target, origin: origin, selection: dashSelection(target),
		contextName: app.Config.ActiveContextName(), revision: app.Config.DestinationRevision(), window: window, atEnd: true,
		selected: -1,
	}
	v.zoom = spec.isQuery() && !spec.Board
	// The declaration and the connection are captured here, on the UI
	// thread; the reads below run off it and touch neither.
	sources, restConfig := declaredSources(app), kubeRESTConfig(app)
	label, selection := target.Label, v.selection
	v.load = func(ctx context.Context, spec *dashSpec, window time.Duration, kept *dashboard.Dashboard) *dashSnapshot {
		now := time.Now()
		s := newDashSnapshot(spec, label, window, now)
		s.Dash = kept
		switch {
		case s.Dash != nil:
		case spec.Board:
			s.Dash = dashboard.QueryBoard(dashboard.NamedQueries(sources))
		case spec.isQuery():
			s.Dash = dashboard.QueryPage(spec.Query)
		default:
			grafana, st := observability.Resolve(sources, observability.KindGrafana, restConfig)
			if grafana == nil {
				s.Grafana = &st
				return s
			}
			body, st := grafana.DashboardJSON(ctx, spec.UID)
			grafana.Close()
			s.Grafana = &st
			if st.State != observability.StateComplete {
				return s
			}
			d, err := dashboard.Parse(body)
			if err != nil {
				s.ParseErr = err.Error()
				return s
			}
			s.Dash = d
		}
		s.bind(selection)
		source, st := observability.Resolve(sources, observability.KindPrometheus, restConfig)
		if source == nil {
			s.Prom = &st
			return s
		}
		defer source.Close()
		s.run(ctx, source)
		return s
	}
	return v
}

func (*dashView) CompactWorkspace() bool { return true }

// SelectedResource lets shared actions and a following :query keep the
// selection the page was opened for.
func (v *dashView) SelectedResource() SelectedResourceTarget { return v.origin }

// GrafanaSelection keeps a hand-off about the page's own selection.
func (v *dashView) GrafanaSelection() SelectedResourceTarget { return v.origin }

// GrafanaWindow hands the page's own window to a Grafana link.
func (v *dashView) GrafanaWindow() (grafanaWindow, bool) {
	now := time.Now()
	return grafanaWindow{From: now.Add(-v.window), To: now, Live: true, Reason: "the window drawn here, last " + graphSpan(v.window)}, true
}

func (v *dashView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.text.SetWrap(false).SetWordWrap(false).SetRegions(false)
	v.actions.Delete(ui.KeyC, ui.KeyF, tcell.KeyCtrlS, ui.KeySlash, tcell.KeyDelete)
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
		ui.KeyR:          ui.NewKeyAction("Re-read", do(v.refresh), true),
		ui.KeyS:          ui.NewKeyActionWithOpts("Queries and what was not drawn", do(v.toggleReport), ui.ActionOpts{Visible: true, Priority: 3}),
		tcell.KeyLeft:    ui.NewKeyAction("Previous panel or cursor earlier", v.moveCmd(-1, false), true),
		tcell.KeyRight:   ui.NewKeyAction("Next panel or cursor later", v.moveCmd(1, false), true),
		tcell.KeyUp:      ui.NewKeyAction("Panel above", v.moveCmd(-1, true), true),
		tcell.KeyDown:    ui.NewKeyAction("Panel below", v.moveCmd(1, true), true),
		tcell.KeyTab:     ui.NewKeyActionWithOpts("Next panel", v.stepCmd(1), ui.ActionOpts{Priority: 9}),
		tcell.KeyBacktab: ui.NewKeyAction("Previous panel", v.stepCmd(-1), false),
		tcell.KeyHome:    ui.NewKeyAction("First panel or window start", v.edgeCmd(-1), false),
		tcell.KeyEnd:     ui.NewKeyAction("Last panel or window end", v.edgeCmd(1), false),
		tcell.KeyEscape:  ui.NewKeyAction("Back", v.escape, false),
		ui.KeyQ:          ui.NewKeyAction("Back", v.escape, false),
		grafanaKey:       ui.NewKeyActionWithOpts(grafanaKeyLabel, do(v.openInGrafana), ui.ActionOpts{Visible: true, Priority: 5}),
		chartStyleKey:    chartStyleAction(v.app, v.render),
	})
	if v.spec.isQuery() {
		v.actions.Add(dashEditKey, ui.NewKeyAction("Edit the expression", do(v.editQuery), true))
	}
	v.bindZoom()
	v.render()
	return nil
}

// bindZoom binds Enter only where it does something: the page of one query
// is already its zoomed panel. The compact key line names every bound
// Enter, shown or not, so the key is unbound there rather than hidden.
func (v *dashView) bindZoom() {
	if v.spec.isQuery() && !v.spec.Board {
		v.actions.Delete(tcell.KeyEnter)
		return
	}
	v.actions.Add(tcell.KeyEnter, ui.NewKeyAction("Zoom panel", func(*tcell.EventKey) *tcell.EventKey {
		v.toggleZoom()
		return nil
	}, true))
}

func (v *dashView) single() bool {
	return v.snapshot != nil && v.snapshot.Dash != nil && len(v.snapshot.Dash.Panels) == 1
}

func (v *dashView) escape(event *tcell.EventKey) *tcell.EventKey {
	switch {
	case v.report:
		v.toggleReport()
	case v.zoom && !v.single() && !(v.spec.isQuery() && !v.spec.Board):
		v.toggleZoom()
	default:
		return v.app.PrevCmd(event)
	}
	return nil
}

func (v *dashView) ready() bool {
	return !v.report && v.snapshot != nil && v.snapshot.Dash != nil && v.snapshot.failure() == nil
}

// moveCmd moves between panels in the grid. In a zoomed panel left and
// right move the cursor along the sample grid (ten points with Shift) and
// up and down change the panel. While the report is open the keys scroll.
func (v *dashView) moveCmd(delta int, vertical bool) ui.ActionHandler {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if !v.ready() {
			return event
		}
		switch {
		case v.zoom && !vertical:
			step := delta
			if event != nil && event.Modifiers()&tcell.ModShift != 0 {
				step *= 10
			}
			last := int(v.snapshot.Window / v.snapshot.Step)
			v.cursor = min(max(v.cursor+step, 0), last)
			v.atEnd = v.cursor == last
		case v.zoom || !vertical:
			v.selected = dashboard.Advance(v.lines, v.selected, delta)
		default:
			v.selected = dashboard.AdvanceLine(v.lines, v.selected, delta)
		}
		v.render()
		return nil
	}
}

func (v *dashView) stepCmd(delta int) ui.ActionHandler {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if !v.ready() {
			return event
		}
		v.selected = dashboard.Advance(v.lines, v.selected, delta)
		v.render()
		return nil
	}
}

func (v *dashView) edgeCmd(delta int) ui.ActionHandler {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if !v.ready() {
			return event
		}
		if v.zoom {
			last := int(v.snapshot.Window / v.snapshot.Step)
			v.cursor, v.atEnd = 0, delta > 0
			if delta > 0 {
				v.cursor = last
			}
		} else {
			v.selected = dashboard.Advance(v.lines, v.selected, delta*len(v.snapshot.Dash.Panels))
		}
		v.render()
		return nil
	}
}

func (v *dashView) toggleZoom() {
	if !v.ready() || v.single() {
		return
	}
	v.zoom = !v.zoom
	v.render()
}

func (v *dashView) toggleReport() {
	v.report = !v.report
	v.render()
	v.text.ScrollToBeginning()
}

func (v *dashView) setWindow(window time.Duration) {
	if window == v.window && v.snapshot != nil {
		return
	}
	v.window, v.atEnd = window, true
	v.refresh()
}

// editQuery puts the expression back into the command prompt, so that it
// can be changed and run again in this view.
func (v *dashView) editQuery() {
	text := queryCommandToken + " "
	if v.spec.Query != nil {
		text += strings.Join(strings.Fields(v.spec.Query.Expr), " ")
	}
	v.app.CmdBuff().SetActive(true)
	v.app.CmdBuff().SetText(text, "", true)
}

// setQuery replaces what a query page draws and reads it once.
func (v *dashView) setQuery(spec *dashSpec) {
	v.spec = *spec
	v.zoom, v.report = !spec.Board, false
	v.snapshot, v.selected, v.first, v.atEnd = nil, -1, 0, true
	// The board zooms on Enter, the page of one query does not: the hints
	// follow what the page now is.
	v.bindZoom()
	if v.app.Content != nil && v.app.Content.Top() == v {
		v.app.Menu().HydrateMenu(v.Hints())
	}
	v.refresh()
}

// openInGrafana offers the same dashboard, with the variables the selection
// filled and the window drawn here. A query page offers Explore.
func (v *dashView) openInGrafana() {
	sources := declaredSources(v.app)
	if reason := grafanaUnavailable(sources); reason != "" {
		showGrafanaHandoff(v.app, &grafanaHandoff{problem: reason})
		return
	}
	target := *v.target
	target.Window, _ = v.GrafanaWindow()
	g := sources.Grafana
	var link grafanaLink
	if v.spec.isQuery() {
		link = grafanaLink{Name: "Explore metrics", Unavailable: "No expression was sent, so there is nothing to open in Explore."}
		if expr, ok := v.sentExpr(); ok {
			link.Detail = expr.String()
			url, err := g.ExploreMetricsURL(expr, target.Window.linkRange())
			switch {
			case errors.Is(err, observability.ErrNoDatasource):
				link.Unavailable = "No Prometheus datasource UID is declared. Add datasource under k9s.observability.grafana."
			case err != nil:
				link.Unavailable = observability.Clean(err.Error(), 200)
			default:
				link.URL, link.Unavailable = url, ""
				link.Fills = "datasource " + g.Datasource + " · the expression as it was sent here"
			}
		}
	} else {
		link = grafanaLink{Name: "Dashboard", Detail: v.spec.UID + " · the dashboard drawn here"}
		var vars [][2]string
		var filled []string
		if v.snapshot != nil {
			for i := range v.snapshot.Plan.Bindings.List {
				b := &v.snapshot.Plan.Bindings.List[i]
				if b.Origin == dashboard.OriginSelection && len(b.Values) == 1 {
					vars = append(vars, [2]string{b.Name, b.Values[0]})
					filled = append(filled, b.Name+"="+b.Values[0])
				}
			}
		}
		link.Fills = strings.Join(filled, ", ")
		if link.Fills == "" {
			link.Fills = "no variable"
		}
		url, err := g.DashboardVarsURL(v.spec.UID, vars, target.Window.linkRange())
		if err != nil {
			link.Unavailable = observability.Clean(err.Error(), 200)
		} else {
			link.URL = url
		}
	}
	showGrafanaHandoff(v.app, &grafanaHandoff{target: &target, links: []grafanaLink{link}, deliverNow: true})
}

// sentExpr is the expression of the selected panel as it was sent. A page
// that drew nothing has no selection (every read was denied, say); the
// first expression that was sent then stands for it, so that Explore stays
// reachable exactly when the terminal cannot draw.
func (v *dashView) sentExpr() (observability.Expr, bool) {
	if v.snapshot == nil || v.snapshot.Dash == nil {
		return observability.Expr{}, false
	}
	panels := v.snapshot.Plan.Panels
	first := func(index int) (observability.Expr, bool) {
		for _, q := range panels[index].Queries {
			if q.Sent() {
				return q.Expr, true
			}
		}
		return observability.Expr{}, false
	}
	if v.selected >= 0 && v.selected < len(panels) {
		return first(v.selected)
	}
	for index := range panels {
		if panels[index].NotDrawn != "" {
			continue
		}
		if expr, ok := first(index); ok {
			return expr, true
		}
	}
	return observability.Expr{}, false
}

func (v *dashView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.StylesChanged(v.app.Styles)
	// Leaving the page cancels a read. Coming back to a page without a read
	// of its window asks once more: it would otherwise say "Reading" with
	// nothing in flight.
	if (v.snapshot == nil || v.snapshot.Window != v.window) && v.busy == "" {
		v.refresh()
	}
}

func (v *dashView) Stop() {
	v.active, v.busy = false, ""
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *dashView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	v.render()
}

func (v *dashView) current() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}

func (v *dashView) reading(window time.Duration) string {
	if v.spec.isQuery() {
		return "Reading last " + graphSpan(window) + " once…"
	}
	if v.snapshot != nil && v.snapshot.Dash != nil {
		return "Reading the panels' last " + graphSpan(window) + " once…"
	}
	return "Reading the dashboard definition, then its panels' last " + graphSpan(window) + ", once…"
}

// refresh starts one explicit, bounded read off the UI thread. A newer
// request or leaving the view cancels it. The dashboard definition is kept
// from the first read that returned one.
func (v *dashView) refresh() {
	if !v.current() {
		v.app.Flash().Warn("Destination changed; reopen the dashboard")
		return
	}
	if v.load == nil {
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, window, load, spec := v.generation, v.window, v.load, v.spec
	var kept *dashboard.Dashboard
	if v.snapshot != nil && !v.spec.isQuery() {
		kept = v.snapshot.Dash
	}
	ctx, cancel := context.WithTimeout(context.Background(), dashDeadline)
	v.cancel = cancel
	v.busy = v.reading(window)
	v.render()
	go func() {
		defer cancel()
		snapshot := load(ctx, &spec, window, kept)
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

func (v *dashView) accept(snapshot *dashSnapshot) {
	v.busy = ""
	if snapshot == nil {
		return
	}
	last := int(snapshot.Window / snapshot.Step)
	if v.atEnd || v.snapshot == nil || v.snapshot.Window != snapshot.Window {
		v.cursor, v.atEnd = last, true
	}
	v.cursor = min(v.cursor, last)
	v.snapshot = snapshot
	v.render()
}

func (v *dashView) Draw(screen tcell.Screen) {
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

func (v *dashView) render() {
	if v.text == nil {
		return
	}
	width, height := v.width, v.height
	if width <= 0 || height <= 0 {
		width, height = 80, 20
	}
	ascii := v.app.Config.K9s.UI.NoIcons
	if v.report {
		// Wrapped here, on plain text: the queries hold brackets, which the
		// text view's own wrapping would take for markup.
		text := dashReport(v.snapshot) + "\ns or Esc returns to the charts · r re-read"
		v.text.SetText(tview.Escape(strings.Join(graphWrap(graphWords(text, ascii), width, "  "), "\n")))
		return
	}
	page := &dashPage{
		Width: width, Height: height, ASCII: ascii, Colors: dashPalette(v.app.Styles.Semantic()),
		Selected: v.selected, First: v.first, Zoom: v.zoom, Cursor: v.cursor, Busy: v.busy, Style: v.app.chartStyle(),
	}
	var lines []string
	if v.snapshot == nil || v.snapshot.Window != v.window {
		// Charts of another window are not shown under the new window's name.
		subject := v.spec.Hint
		if subject == "" {
			subject = v.spec.UID
		}
		if v.spec.isQuery() {
			subject = "named queries"
			if v.spec.Query != nil {
				subject = v.spec.Query.Name
				if subject == "" {
					subject = queryTyped
				}
			}
		}
		busy := v.busy
		if busy == "" {
			busy = v.reading(v.window)
		}
		lines = []string{
			graphBold(page.Colors.Title, dashFit(v.spec.noun()+" · "+safeProviderText(subject)+" · last "+graphSpan(v.window), width, ascii)),
			graphPaint(page.Colors.Muted, dashFit(busy, width, ascii)),
			graphPaint(page.Colors.Muted, dashFit("Esc cancels and goes back.", width, ascii)),
		}
	} else {
		lines = renderDash(v.snapshot, page)
		// The page settles the selection and the scroll position.
		v.selected, v.first, v.lines = page.Selected, page.First, page.Lines
	}
	v.text.SetText(strings.Join(lines, "\n"))
	v.text.ScrollToBeginning()
}

// Opening a dashboard or a query.

// dashMapping is how a dashboard's variables are matched to the selection:
// as declared for its kind, or by the conventional names.
func dashMapping(g *observability.GrafanaSource, uid string) (kind string, m dashboard.Mapping, declared bool) {
	if g != nil {
		kinds := make([]string, 0, len(g.Dashboards))
		for kind := range g.Dashboards {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			if d := g.Dashboards[kind]; d.UID == uid {
				return kind, dashboard.Mapping{Namespace: d.NamespaceVar, Pod: d.PodVar, Workload: d.WorkloadVar, Node: d.NodeVar}, true
			}
		}
	}
	return "", dashboard.ConventionalMapping, false
}

// dashAvailability is why panels cannot be drawn at all, or "".
func dashAvailability(app *App) string {
	sources := declaredSources(app)
	if sources == nil || (sources.Prometheus == nil && (sources.Grafana == nil || sources.Grafana.Datasource == "")) {
		return "No Prometheus source is declared for this context; :sources shows what to add"
	}
	return ""
}

// openDashboardView draws one dashboard of the list for the list's target.
func (a *App) openDashboardView(uid, title string, target *grafanaTarget, origin SelectedResourceTarget) { //nolint:gocritic // Immutable selection.
	if reason := dashAvailability(a); reason != "" {
		a.Flash().Warn(reason)
		return
	}
	sources := declaredSources(a)
	spec := dashSpec{UID: uid, Hint: title}
	spec.For, spec.Mapping, spec.Declared = dashMapping(sources.Grafana, uid)
	if err := a.inject(newDashView(a, spec, target, origin, 0), false); err != nil {
		a.Flash().Err(err)
	}
}

// queryTarget is what the bar's variables are filled from: the selection,
// or nothing.
func (a *App) queryTarget(selected SelectedResourceTarget) *grafanaTarget { //nolint:gocritic // Immutable selection.
	now := time.Now()
	target, err := grafanaTargetFor(selected, a.Config.ActiveNamespace(), now)
	if err != nil {
		return &grafanaTarget{Label: "no selection", Window: defaultGrafanaWindow(now, "nothing is selected")}
	}
	refineGrafanaTarget(a, selected, target, now)
	return target
}

// querySpec turns what was typed after :query into a page: nothing is the
// board of named queries, a known name is that query, anything else is
// taken as an expression.
func querySpec(sources *observability.Sources, text string) dashSpec {
	text = strings.TrimSpace(text)
	if text == "" {
		return dashSpec{Board: true}
	}
	if named, ok := dashboard.FindQuery(sources, text); ok {
		return dashSpec{Query: &named}
	}
	return dashSpec{Query: &dashboard.Named{NamedQuery: observability.NamedQuery{Expr: text}}}
}

func (c *Command) queryCommand(line string) {
	text := strings.TrimSpace(strings.TrimSpace(line)[min(len(queryCommandToken), len(strings.TrimSpace(line))):])
	app := c.app
	if reason := dashAvailability(app); reason != "" {
		app.Flash().Warn(reason)
		return
	}
	sources := declaredSources(app)
	if err := sources.ValidateQueries(); err != nil {
		app.Flash().Warn("k9s.observability.queries cannot be used: " + observability.Clean(err.Error(), 200))
		return
	}
	spec := querySpec(sources, text)
	// A query typed while a query page is open replaces what it draws.
	if top, ok := app.Content.Top().(*dashView); ok && top.spec.isQuery() && top.current() {
		top.setQuery(&spec)
		return
	}
	selected := SelectedResourceTarget{Context: app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	if owner, ok := app.Content.Top().(actionOwner); ok {
		selected = grafanaSelection(owner, app.Config.ActiveContextName())
	}
	app.openQuery(spec, selected)
}

func (a *App) openQuery(spec dashSpec, selected SelectedResourceTarget) { //nolint:gocritic // Immutable selection.
	if err := a.inject(newDashView(a, spec, a.queryTarget(selected), selected, 0), false); err != nil {
		a.Flash().Err(err)
	}
}

// queryActions registers the query bar with the action palette and help.
func queryActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	selected := grafanaSelection(owner, app.Config.ActiveContextName())
	return []ui.ActionDescriptor{{
		// Short enough to stand whole in the palette's label column.
		ID: "command.query", Label: "Query: chart PromQL or a named query", Category: ui.ActionInspect,
		Shortcut: ":" + queryCommandToken, Discoverable: true, UnavailableReason: dashAvailability(app),
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openQuery(dashSpec{Board: true}, selected); return nil },
	}}
}

// querySuggestions completes the name of a named query after ":query ".
func querySuggestions(sources *observability.Sources, typed string) []string {
	rest, ok := strings.CutPrefix(typed, queryCommandToken+" ")
	if !ok {
		return nil
	}
	var out []string
	for _, q := range dashboard.NamedQueries(sources) {
		if rest != q.Name && strings.HasPrefix(q.Name, rest) {
			out = append(out, strings.TrimPrefix(q.Name, rest))
		}
	}
	return out
}
