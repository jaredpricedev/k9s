// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
)

const (
	timelineCommandToken = "timeline"
	timelineTitle        = "Timeline"
	timelineListPage     = "timeline-list"
	timelineSourcesPage  = "timeline-sources"
)

// timelineView is an on-demand, retained observation: it reads when opened
// and when the user asks again, never on a refresh tick.
type timelineView struct {
	*Details
	scope                         timeline.Scope
	scopeNote                     string
	snapshot                      *timeline.Snapshot
	loader                        func(context.Context) (*timeline.Snapshot, error)
	cancel                        context.CancelFunc
	generation                    uint64
	active, loading, showSources  bool
	refreshFailure, query         string
	window                        time.Duration
	table                         *tview.Table
	pages                         *tview.Pages
	header, detail, footer        *tview.TextView
	list                          *tview.Flex
	rows                          []int
	focused                       int
	width, height, detailHeight   int
	visibleCount, renderedEntries int

	// calm is the page of the calm layout, the default. allDetail shows
	// the list and the pane the view had before it instead.
	calm      *tview.TextView
	allDetail bool
	// windowChosen says the user set the window; until then a read picks
	// the narrowest one that holds what the answer is about.
	windowChosen bool
	// story is what the calm page last drew, calmRows its rows that are on
	// the screen and calmKey the one the row cursor is on.
	story    *timelineStory
	calmRows []timelineCalmRow
	calmKey  string
	// cursor is the column of the time cursor on the frame the strips were
	// last drawn in, or -1; cursorAt is the time it stands on.
	cursor   int
	cursorAt time.Time
	frame    chart.Frame
	// episodes are the episodes of the view the timeline was opened from,
	// such as the stalls :slow found. They were read there, not here.
	episodes *graphEpisodes
	// dropped is what the calm page gave up for the size it was last drawn
	// at, in the order it gave it up.
	dropped []string
}

var timelineKinds = map[string]string{
	client.PodGVR.String(): "Pod", client.DpGVR.String(): "Deployment", client.RsGVR.String(): "ReplicaSet",
	client.StsGVR.String(): "StatefulSet", client.DsGVR.String(): "DaemonSet", client.JobGVR.String(): "Job",
}

// timelineScope resolves what a timeline follows: the selected workload, an
// explicit namespace argument, or otherwise the active namespace.
//
//nolint:gocritic // The immutable selection is captured by value.
func timelineScope(target SelectedResourceTarget, activeNamespace, argument string) (*timeline.Scope, string, error) {
	scope := &timeline.Scope{Context: target.Context}
	if argument != "" {
		if client.IsAllNamespaces(argument) || client.IsClusterScoped(argument) {
			return nil, "", errors.New("A timeline reads a single namespace: name one, as in :timeline apps")
		}
		scope.Namespace = argument
		return scope, "", nil
	}
	selected := target.Err() == nil
	if selected {
		if kind, ok := timelineKinds[target.GVR.String()]; ok && target.Namespace != "" {
			scope.Namespace, scope.Kind, scope.Name = target.Namespace, kind, target.Name
			scope.Resource, scope.UID = target.GVR.String(), string(target.UID)
			return scope, "", nil
		}
		if target.GVR.String() == client.NsGVR.String() {
			scope.Namespace = target.Name
			return scope, "", nil
		}
	}
	note := ""
	switch {
	case selected && target.Namespace != "" && !client.IsAllNamespaces(target.Namespace):
		scope.Namespace = target.Namespace
		note = "selected " + target.GVR.R() + " is not a workload; showing its namespace"
	case activeNamespace != "" && !client.IsAllNamespaces(activeNamespace) && !client.IsClusterScoped(activeNamespace):
		scope.Namespace = activeNamespace
	default:
		return nil, "", errors.New("Select a Pod or workload, or one namespace, first: a timeline reads a single namespace")
	}
	return scope, note, nil
}

func (c *Command) timelineCommand(line string) {
	target := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	if owner, ok := c.app.Content.Top().(actionOwner); ok {
		target = actionTarget(owner, c.app.Config.ActiveContextName())
	}
	argument := ""
	if fields := strings.Fields(line); len(fields) > 1 {
		argument = fields[1]
	}
	c.app.openTimeline(target, argument)
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openTimeline(target SelectedResourceTarget, argument string) {
	target.Context = a.Config.ActiveContextName()
	scope, note, err := timelineScope(target, a.Config.ActiveNamespace(), argument)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if a.Conn() == nil || a.Conn().Config() == nil {
		a.Flash().Warn("Select a configured context first")
		return
	}
	cfg, err := a.Conn().Config().Snapshot(scope.Context).RESTConfig()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	objects, err := dynamic.NewForConfig(cfg)
	if err != nil {
		a.Flash().Err(errors.New("captured resource transport unavailable"))
		return
	}
	// A metadata-only client keeps ConfigMap and Secret values off the wire.
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		meta = nil
	}
	readers := &timeline.Readers{Objects: objects, Metadata: meta}
	v := newTimelineView(a, scope, note)
	if from, ok := a.Content.Top().(graphEpisodeSource); ok {
		// The view the timeline is opened from knows when its episodes
		// were: they are drawn behind the rows, and nothing is read for it.
		v.episodes = from.graphEpisodes()
	}
	// When the context declares an Alertmanager, its alerts are read once
	// beside the Kubernetes reads and placed in the same order. Undeclared,
	// nothing is contacted and the timeline does not mention alerts.
	readAlerts, alertsDeclared := alertsReader(a, false)
	// The Grafana declaration is captured here; Grafana is contacted only
	// inside the same bounded read as the cluster, never on its own.
	sources, restConfig := declaredSources(a), kubeRESTConfig(a)
	v.loader = func(ctx context.Context) (*timeline.Snapshot, error) {
		now := time.Now()
		notes := make(chan *timelineAnnotations, 1)
		go func() { notes <- readTimelineAnnotations(ctx, sources, restConfig, now) }()
		var waitAlerts func() alerts.Snapshot
		if alertsDeclared {
			waitAlerts = alertsBeside(ctx, readAlerts)
		}
		snapshot, err := timeline.Collect(ctx, readers, scope, now)
		read := <-notes
		if err != nil || snapshot == nil {
			return snapshot, err
		}
		if waitAlerts != nil {
			observed := waitAlerts()
			snapshot.AddAlerts(observed.Status, observed.Items)
		}
		read.mergeInto(snapshot)
		return snapshot, nil
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func newTimelineView(app *App, scope *timeline.Scope, note string) *timelineView {
	return &timelineView{Details: NewDetails(app, timelineTitle, scope.Label(), contentTXT, false), scope: *scope, scopeNote: note, focused: -1, cursor: -1}
}

func timelineActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := ""
	if _, _, err := timelineScope(target, app.Config.ActiveNamespace(), ""); err != nil {
		reason = err.Error()
	}
	return []ui.ActionDescriptor{{
		ID: "resource.timeline", Label: "Timeline: what changed, in order", Category: ui.ActionInspect,
		Shortcut: ":" + timelineCommandToken, Discoverable: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openTimeline(target, ""); return nil },
	}}
}

func (*timelineView) CompactWorkspace() bool { return true }

// SelectedResource exposes the focused entry's resource to shared actions.
func (v *timelineView) SelectedResource() SelectedResourceTarget {
	target := SelectedResourceTarget{Context: v.scope.Context}
	if entry := v.focusedEntry(); entry != nil && entry.Ref.Resource != "" {
		target.GVR, target.Namespace, target.Name = client.NewGVR(entry.Ref.Resource), entry.Ref.Namespace, entry.Ref.Name
		return target
	}
	if v.scope.Kind != "" {
		target.GVR, target.Namespace, target.Name = client.NewGVR(v.scope.Resource), v.scope.Namespace, v.scope.Name
		return target
	}
	target.UnavailableReason = "Select a timeline entry that names an API resource"
	return target
}

func (v *timelineView) focusedEntry() *timeline.Entry {
	if v.snapshot == nil || v.focused < 0 || v.focused >= len(v.snapshot.Entries) {
		return nil
	}
	return &v.snapshot.Entries[v.focused]
}

func (v *timelineView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.cmdBuff.RemoveListener(v.Details)
	v.cmdBuff.AddListener(v)
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.calm = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.text.SetWrap(true).SetWordWrap(true)
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.table.SetSelectionChangedFunc(func(row, _ int) {
		if v.allDetail && row > 0 && row <= len(v.rows) && v.rows[row-1] >= 0 {
			v.focused = v.rows[row-1]
			v.renderDetail()
		}
	})
	v.detailHeight = 5
	v.list = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(v.table, 0, 1, true).AddItem(v.detail, v.detailHeight, 0, false)
	v.pages = tview.NewPages().AddPage(timelineListPage, v.list, true, true).AddPage(timelineSourcesPage, v.text, true, false)
	v.Flex.SetDirection(tview.FlexRow)
	v.arrange()
	v.bindTimelineKeys()
	v.render()
	return nil
}

// arrange puts the widgets of the current page into the view: the one
// calm page; or the header, the list with its pane and the key line this
// view had before it, which is also where the sources and limits are
// written, under the title alone when they are opened from the calm page.
func (v *timelineView) arrange() {
	v.Flex.Clear()
	switch {
	case v.allDetail:
		v.Flex.AddItem(v.header, 4, 0, false).AddItem(v.pages, 0, 1, true).AddItem(v.footer, 1, 0, false)
	case v.showSources:
		v.Flex.AddItem(v.header, 1, 0, false).AddItem(v.pages, 0, 1, true).AddItem(v.footer, 1, 0, false)
	default:
		v.Flex.AddItem(v.calm, 0, 1, true)
	}
}

func (v *timelineView) bindTimelineKeys() {
	v.actions.Delete(ui.KeyC, ui.KeyF, tcell.KeyCtrlS)
	idle := func(run func()) ui.ActionHandler {
		return func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			run()
			return nil
		}
	}
	for index, window := range timeline.Windows {
		v.actions.Add(ui.Key1+tcell.Key(index), ui.NewKeyAction("Window "+timeline.Span(window), idle(func() { v.setWindow(window) }), true))
	}
	v.actions.Add(ui.Key0, ui.NewKeyAction("Window all read", idle(func() { v.setWindow(0) }), true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Re-read timeline", idle(v.refresh), true))
	v.actions.Add(ui.KeyS, ui.NewKeyAction("Sources and limits", idle(v.toggleSources), true))
	// The calm layout is the default; a shows the list and the pane the
	// view had before it. The cursors are the calm page's: on the list and
	// on the sources text the same keys scroll, as they did.
	v.actions.Add(calmDetailKey, ui.NewKeyAction(calmDetailLabel, idle(v.toggleDetail), true))
	calmly := func(run func()) ui.ActionHandler {
		return func(event *tcell.EventKey) *tcell.EventKey {
			if !v.calmShown() {
				return event
			}
			run()
			return nil
		}
	}
	// Shift moves the time cursor ten columns, as in every chart view.
	cursor := func(delta int) ui.ActionHandler {
		return func(event *tcell.EventKey) *tcell.EventKey {
			if !v.calmShown() {
				return event
			}
			step := delta
			if event != nil && event.Modifiers()&tcell.ModShift != 0 {
				step *= 10
			}
			v.moveCursor(step)
			return nil
		}
	}
	v.actions.Bulk(ui.KeyMap{
		tcell.KeyUp:    ui.NewKeyAction("Previous row", calmly(func() { v.moveRow(-1) }), false),
		tcell.KeyDown:  ui.NewKeyAction("Next row", calmly(func() { v.moveRow(1) }), false),
		ui.KeyK:        ui.NewKeyAction("Previous row", calmly(func() { v.moveRow(-1) }), false),
		ui.KeyJ:        ui.NewKeyAction("Next row", calmly(func() { v.moveRow(1) }), false),
		tcell.KeyLeft:  ui.NewKeyAction("Cursor earlier", cursor(-1), false),
		tcell.KeyRight: ui.NewKeyAction("Cursor later", cursor(1), false),
		tcell.KeyHome:  ui.NewKeyAction("Cursor to window start", calmly(func() { v.cursorEdge(-1) }), false),
		tcell.KeyEnd:   ui.NewKeyAction("Cursor to window end", calmly(func() { v.cursorEdge(1) }), false),
		// n and Shift-N step through what a view steps through: here the
		// entries, in the order they began.
		ui.KeyN:      ui.NewKeyAction("Cursor to next entry", calmly(func() { v.stepEntry(1) }), false),
		ui.KeyShiftN: ui.NewKeyAction("Cursor to previous entry", calmly(func() { v.stepEntry(-1) }), false),
	})
	// m is the graph key everywhere a row names an object, so the reference
	// mark is on x here.
	v.actions.Add(ui.KeyX, ui.NewKeyAction("Mark reference moment", idle(v.markReference), true))
	v.actions.Add(ui.KeyM, ui.NewKeyAction("Graph", idle(v.openGraph), true))
	v.actions.Add(grafanaKey, grafanaKeyAction(v.app, func() actionOwner { return v }, func() bool { return !v.cmdBuff.IsActive() }))
	v.actions.Add(tcell.KeyEnter, ui.NewKeyAction("Open resource", func(*tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			v.applyQuery(v.cmdBuff.GetText())
			v.cmdBuff.SetActive(false)
			return nil
		}
		v.openEntry()
		return nil
	}, true))
	v.actions.Add(tcell.KeyEscape, ui.NewKeyAction("Back", v.escape, false))
	v.actions.Add(ui.KeyQ, ui.NewKeyAction("Back", v.escape, false))
}

func (v *timelineView) escape(event *tcell.EventKey) *tcell.EventKey {
	switch {
	case v.cmdBuff.InCmdMode():
		if event.Key() != tcell.KeyEscape {
			return event
		}
		v.cmdBuff.SetActive(false)
		v.cmdBuff.Reset()
		v.applyQuery("")
	case v.showSources:
		v.toggleSources()
	case v.cursor >= 0 && !v.allDetail:
		// Esc first puts the time cursor away; the Esc after that clears
		// the filter or leaves.
		v.cursor = -1
		v.render()
	case v.query != "":
		v.cmdBuff.Reset()
		v.applyQuery("")
	default:
		v.cmdBuff.Reset()
		return v.app.PrevCmd(event)
	}
	return nil
}

// BufferCompleted applies the filter on the UI loop; the prompt debounces on a
// worker goroutine.
func (v *timelineView) BufferCompleted(text, _ string) {
	if v.app.lifecycle.requested.Load() {
		return
	}
	if !v.app.IsRunning() {
		v.applyQuery(text)
		return
	}
	v.app.QueueUpdateDraw(func() {
		if v.active && v.app.Content.Top() == v && text == v.cmdBuff.GetText() {
			v.applyQuery(text)
		}
	})
}

func (v *timelineView) applyQuery(text string) {
	if text == v.query {
		return
	}
	v.query = text
	v.render()
}

func (v *timelineView) setWindow(window time.Duration) {
	v.window, v.windowChosen, v.cursor = window, true, -1
	v.render()
}

func (v *timelineView) toggleSources() {
	v.showSources = !v.showSources
	page := timelineListPage
	if v.showSources {
		page = timelineSourcesPage
	}
	v.pages.SwitchToPage(page)
	v.arrange()
	v.render()
	if v.active {
		v.app.SetFocus(v)
	}
}

// openGraph draws the focused entry's object, or the timeline's own scope,
// over the timeline's window when :graph offers that window.
func (v *timelineView) openGraph() {
	if v.showSources {
		return
	}
	window := time.Duration(0)
	if slices.Contains(graphWindows, v.window) {
		window = v.window
	}
	v.app.openGraph(v.SelectedResource(), window)
}

// markReference lets the user choose the moment entries are compared with.
// Marking the current reference again returns to the first failing signal.
func (v *timelineView) markReference() {
	if v.snapshot == nil || v.focusedEntry() == nil || v.showSources {
		return
	}
	if v.snapshot.Chosen && v.snapshot.Onset == v.focused {
		v.snapshot.ResetReference()
	} else {
		v.snapshot.SetReference(v.focused)
	}
	v.render()
}

func (v *timelineView) openEntry() {
	entry := v.focusedEntry()
	if entry == nil || v.showSources {
		return
	}
	if v.scope.Context != v.app.Config.ActiveContextName() {
		v.app.Flash().Warn("Context changed; reopen the timeline")
		return
	}
	if entry.Source == timeline.SourceAnnotation {
		v.openAnnotation(entry)
		return
	}
	if entry.Ref.Resource == "" || entry.Ref.Name == "" {
		v.app.Flash().Warnf("No resource list is known for %s", entry.Ref.Short())
		return
	}
	command, path := entry.Ref.Resource, entry.Ref.Name
	if entry.Ref.Namespace != "" {
		command += " " + entry.Ref.Namespace
		path = client.FQN(entry.Ref.Namespace, entry.Ref.Name)
	}
	v.app.gotoResource(command, path, false, true)
}

func (v *timelineView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.snapshot == nil && !v.loading && v.loader != nil {
		v.refresh()
	}
}

func (v *timelineView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *timelineView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.table != nil {
		p := styles.Semantic()
		v.table.SetBackgroundColor(p.Canvas.Color())
		v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(p.Text.Color()).Bold(true))
		v.detail.SetBackgroundColor(p.Panel.Color())
		v.detail.SetTextColor(p.Text.Color())
		for _, item := range []*tview.TextView{v.header, v.footer} {
			item.SetBackgroundColor(p.Panel.Color())
			item.SetTextColor(p.Text.Color())
		}
		v.calm.SetBackgroundColor(p.Canvas.Color())
		v.calm.SetTextColor(p.Text.Color())
	}
	v.render()
}

func (v *timelineView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		detail := 5
		switch {
		case height < 18:
			detail = 3
		case height >= 28:
			detail = 7
		}
		if detail != v.detailHeight {
			v.detailHeight = detail
			v.list.ResizeItem(v.detail, detail, 0)
		}
		v.render()
	}
	v.Flex.Draw(screen)
}

func (v *timelineView) contextCurrent() bool {
	return v.scope.Context == v.app.Config.ActiveContextName()
}

// refresh starts one bounded read. A newer request or leaving the view cancels
// it, and a late reply never replaces a newer observation.
func (v *timelineView) refresh() {
	if !v.contextCurrent() {
		v.refreshFailure = "Context changed; reopen the timeline"
		v.render()
		return
	}
	if v.loader == nil {
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), timeline.TotalDeadline)
	v.cancel, v.loading = cancel, true
	v.render()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx)
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && v.generation == generation && v.contextCurrent() && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, err)
			}
		})
	}()
}

func (v *timelineView) acceptSnapshot(snapshot *timeline.Snapshot, err error) {
	v.loading = false
	switch {
	case err != nil:
		v.refreshFailure = err.Error()
	case snapshot == nil:
		v.refreshFailure = "No timeline observation returned"
	default:
		v.refreshFailure = ""
		v.snapshot = snapshot
		v.focused = snapshot.Onset
		// The row cursor starts on the row of the first failing signal
		// again, and the time cursor is put away: its column was a time of
		// the earlier observation.
		v.calmKey, v.cursor = "", -1
		if !v.windowChosen {
			v.window = timelineDefaultWindow(snapshot, v.episodes)
		}
	}
	v.render()
}

// timelineDefaultWindow is the window a read opens on, until the user
// chooses one: the narrowest that holds the first failing signal and the
// change before it with a tenth to spare, so that the page opens on what
// its answer is about instead of on every day that was read. A change
// much older than the fault does not widen it, and without a fault it is
// the narrowest window that holds the latest change. With nothing recent
// it is an hour; opened from a view that found episodes, it is the
// window they were looked for in. 0 still shows everything read.
func timelineDefaultWindow(s *timeline.Snapshot, episodes *graphEpisodes) time.Duration {
	fits := func(need time.Duration) (time.Duration, bool) {
		for _, window := range timeline.Windows {
			if need+need/10 <= window {
				return window, true
			}
		}
		return 0, false
	}
	if episodes != nil && len(episodes.Intervals) > 0 {
		if window, ok := fits(episodes.End.Sub(episodes.Start) * 4 / 5); ok {
			return window
		}
	}
	need := time.Duration(0)
	onset := s.OnsetEntry()
	for i := range s.Entries {
		// The entries are newest first: the first change found is the
		// latest, or the latest before the first failing signal.
		e := &s.Entries[i]
		if !e.Change || onset != nil && !e.At.Before(onset.At) {
			continue
		}
		need = s.CapturedAt.Sub(e.At)
		break
	}
	if onset != nil {
		fault := s.CapturedAt.Sub(onset.At)
		if need > max(4*fault, timeline.Windows[0]) || need < fault {
			need = fault
		}
	}
	if window, ok := fits(need); ok && need > 0 {
		return window
	}
	return time.Hour
}

func (v *timelineView) render() {
	if v.header == nil {
		return
	}
	if !v.allDetail && !v.showSources {
		v.renderCalm()
		return
	}
	if v.allDetail {
		v.renderRows()
		v.renderHeader()
		v.renderDetail()
	} else {
		// The sources and limits, opened from the calm page: its title
		// line stays over them.
		var st *timelineStory
		if v.snapshot != nil {
			st = timelineStoryOf(v.snapshot, v.window, v.query, v.episodes)
		}
		v.header.SetText(calm.TitleLine(v.calmTitle(st), v.viewWidth(), calmOptions(v.app)))
	}
	if v.snapshot != nil {
		v.text.SetText(tview.Escape(v.snapshot.Report(v.window)))
	} else {
		v.text.SetText("No observation yet.")
	}
	width := v.viewWidth()
	// The key line of the list under a names the way back to the calm
	// page at every width, and keeps the keys it named before: Shift-D at
	// 132 columns, the mark and the graph at 80, re-reading at 60.
	footer := ""
	for _, footer = range []string{
		"Enter open · / filter · 1 15m  2 1h  3 6h  4 24h  0 all · x mark · m graph · s sources · r re-read · Shift-D Grafana · a less detail · Esc back",
		"Enter open · / filter · 1 15m  2 1h  3 6h  4 24h  0 all · x mark · m graph · s sources · r read · Shift-D Grafana · a less · Esc",
		"Enter open · / filter · 1 15m  2 1h  3 6h  4 24h  0 all · x mark · m graph · s sources · r read · a less · Esc",
		"Enter open · / filter · 1-4,0 · x mark · m graph · s sources · r read · a less",
		"Enter open · / filter · 1-4,0 · s sources · r read · a less",
	} {
		if tview.TaggedStringWidth(footer) <= width {
			break
		}
	}
	if v.showSources {
		footer = "s or Esc returns to the timeline · r re-read"
	}
	v.footer.SetText(tview.Escape(ui.Truncate(footer, width)))
}

func (v *timelineView) viewWidth() int {
	if v.width <= 0 {
		return 80
	}
	return v.width
}

func (v *timelineView) windowLabel() string {
	if v.window == 0 {
		return "all read"
	}
	return "last " + timeline.Span(v.window)
}

func (v *timelineView) renderHeader() {
	if v.header == nil {
		return
	}
	width := v.viewWidth()
	p := v.app.Styles.Semantic()
	lines := []string{timelineTitle + " / " + v.scope.Label(), retainedObservationPending, "", retainedWaitingForReads}
	colors := []config.Color{p.Focus, p.Text, p.Muted, p.Muted}
	if v.scopeNote != "" {
		lines[0] += " (" + v.scopeNote + ")"
	}
	if s := v.snapshot; s != nil {
		lines[1] = fmt.Sprintf("%d of %d entries · window %s", v.visibleCount, len(s.Entries), v.windowLabel())
		if v.query != "" {
			lines[1] += " · filter " + v.query
		}
		lines[1] += fmt.Sprintf(" · captured %s · %s", s.CapturedAt.UTC().Format("15:04:05Z"), s.Scope.Context)
		if onset := s.OnsetEntry(); onset != nil {
			label := "Fault began "
			if s.Chosen {
				label = "Reference "
			}
			lines[2] = fmt.Sprintf("%s%s (%s before capture): %s %s", label, timeline.Clock(onset.At, s.CapturedAt)+"Z",
				timeline.Span(s.CapturedAt.Sub(onset.At)), onset.Ref.Short(), onset.Summary)
			colors[2] = p.Failure
			if s.Chosen {
				colors[2] = p.Focus
			}
		} else {
			lines[2] = "No failing signal read. Not evidence of health: s shows what was read."
			colors[2] = p.Warning
		}
		lines[3] = "All sources read within limits · events expire · s sources"
		var gaps []string
		if text := s.Gaps(); text != "" {
			gaps = append(gaps, "NOT READ: "+text)
		}
		if len(s.Untimed) > 0 {
			gaps = append(gaps, fmt.Sprintf("%d failing status untimed, not placed", len(s.Untimed)))
		}
		if len(gaps) > 0 {
			lines[3], colors[3] = strings.Join(gaps, " · ")+" · s sources and limits", p.Warning
		}
	}
	switch {
	case !v.contextCurrent():
		lines[3], colors[3] = "Context changed; this observation is retained. Reopen the timeline for the current context.", p.Warning
	case v.loading:
		lines[3], colors[3] = "Reading: bounded, read-only, on request only...", p.Progress
	case v.refreshFailure != "" && v.snapshot != nil:
		lines[3], colors[3] = "Re-read failed, earlier observation retained: "+v.refreshFailure, p.Failure
	case v.refreshFailure != "":
		lines[3], colors[3] = "Unavailable · r retry · "+v.refreshFailure, p.Failure
	}
	for index, line := range lines {
		lines[index] = timelineTag(colors[index], index == 0 || index == 2) + tview.Escape(ui.Truncate(line, width)) + "[-:-:-]"
	}
	v.header.SetText(strings.Join(lines, "\n"))
}

func timelineTag(color config.Color, bold bool) string {
	attr := "-"
	if bold {
		attr = "b"
	}
	hex := color.Color().Hex()
	if hex < 0 {
		return "[-:-:" + attr + "]"
	}
	return fmt.Sprintf("[#%06x:-:%s]", hex, attr)
}

func (v *timelineView) renderDetail() {
	if v.detail == nil {
		return
	}
	entry := v.focusedEntry()
	if entry == nil || v.snapshot == nil {
		switch {
		case v.loading:
			v.detail.SetText("Reading events, rollout history, restarts, configuration metadata, autoscalers and nodes...")
		case v.snapshot == nil && v.refreshFailure != "":
			v.detail.SetText(tview.Escape("Nothing was read: " + v.refreshFailure))
		case v.snapshot != nil && len(v.snapshot.Entries) == 0:
			text := "No timed entries were read. " + v.snapshot.EventHorizon()
			if count := len(v.snapshot.Untimed); count > 0 {
				text += fmt.Sprintf(" %d failing status carries no time and is listed under s.", count)
			}
			v.detail.SetText(tview.Escape(text))
		default:
			v.detail.SetText("")
		}
		return
	}
	s := v.snapshot
	p := v.app.Styles.Semantic()
	where := entry.Ref.Kind + " " + client.FQN(entry.Ref.Namespace, entry.Ref.Name)
	if entry.Ref.Namespace == "" {
		where = entry.Ref.Kind + " " + entry.Ref.Name
	}
	// The pane is short and cannot scroll, so the identifying line must not
	// wrap: it shortens its wording, then elides the middle of the name. The
	// text view wraps on encoded length, so the budget is counted in bytes,
	// which is never less than the width in cells.
	width := v.viewWidth()
	age := timeline.Span(s.CapturedAt.Sub(entry.At))
	head := fmt.Sprintf("%s · %s before capture · %s · %s", timeline.Stamp(entry.At), age, entry.Source, where)
	if len(head) >= width {
		prefix := fmt.Sprintf("%s  %s ago  %s  ", timeline.Stamp(entry.At), age, entry.Source)
		name := entry.Ref.Short()
		if room := width - 1 - len(prefix); len(name) > room {
			name = strings.TrimRight(timelineName(name, max(room-len("…")+1, 8)), " ")
		}
		head = prefix + name
	}
	lines := []string{timelineTag(p.Focus, true) + tview.Escape(head) + "[-:-:-]"}
	relation, delta := s.Relation(v.focused)
	sentence := s.RelationSentence(v.focused)
	if relation == timeline.RelationBefore && entry.Change && len(sentence) >= width {
		moment := "the first failing signal"
		if s.Chosen {
			moment = "your reference moment"
		}
		sentence = "Preceded " + moment + " by " + timeline.Span(delta) + "; order, not cause."
	}
	color := p.Muted
	switch {
	case relation == timeline.RelationOnset:
		color = p.Failure
	case relation == timeline.RelationBefore && entry.Change:
		color = p.Warning
	}
	lines = append(lines, timelineTag(color, relation == timeline.RelationOnset || (relation == timeline.RelationBefore && entry.Change))+
		tview.Escape(sentence)+"[-:-:-]",
		tview.Escape(entry.Summary))
	var details []string
	for _, item := range entry.Detail {
		if item != "" {
			details = append(details, item)
		}
	}
	if len(details) > 0 {
		lines = append(lines, tview.Escape(strings.Join(details, " | ")))
	}
	lines = append(lines, timelineTag(p.Muted, false)+tview.Escape("Time from: "+entry.Origin)+"[-:-:-]")
	v.detail.SetText(strings.Join(lines, "\n"))
	v.detail.ScrollToBeginning()
}
