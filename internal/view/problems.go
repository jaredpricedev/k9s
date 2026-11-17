// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/problems"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	problemsCommandToken = "problems"
	problemsNoConnection = "No Kubernetes connection; :connection diagnoses this destination"
)

// problemsView is the instant cross-kind answer to "what is broken here right
// now?". It owns one retained observation of the viewing destination, read on
// open and again only on request. It stores nothing, polls nothing and never
// writes to the cluster. Mutable state belongs to the application draw
// goroutine; the read runs on its own goroutine against a pinned client.
type problemsView struct {
	*tview.Flex
	app                    *App
	table                  *tview.Table
	header, detail, footer *tview.TextView
	prompt                 *tview.InputField
	actions                *ui.KeyActions
	contextName, namespace string
	snapshot               *problems.Snapshot
	collect                func(context.Context, dynamic.Interface, string, time.Time) problems.Snapshot
	lines                  []problemsLine
	expanded               map[string]bool
	query, status          string
	loading, started       bool
	prompting              bool
	cancel                 context.CancelFunc
	generation             uint64
	destinationRevision    uint64
	originalCapture        func(*tcell.EventKey) *tcell.EventKey
	returnDestination      *inspectionReturnDestination
	width, height          int
	headerRows, detailRows int
}

var (
	_ model.Component  = (*problemsView)(nil)
	_ SelectedResource = (*problemsView)(nil)
)

func (c *Command) problemsCommand() {
	if reason := problemsUnavailable(c.app); reason != "" {
		c.app.Flash().Warn(reason)
		return
	}
	if err := c.app.inject(newProblemsView(c.app), false); err != nil {
		c.app.Flash().Err(err)
	}
}

func problemsUnavailable(a *App) string {
	if a.Conn() == nil || !a.Conn().ConnectionOK() {
		return problemsNoConnection
	}
	return ""
}

func problemsAction(app *App) ui.ActionDescriptor {
	return ui.ActionDescriptor{
		ID: "command.problems", Label: "Problems across kinds, ranked", Category: ui.ActionNavigate,
		Shortcut: ":" + problemsCommandToken, Discoverable: true, UnavailableReason: problemsUnavailable(app),
		Handler: func(*tcell.EventKey) *tcell.EventKey { NewCommand(app).problemsCommand(); return nil },
	}
}

func newProblemsView(a *App) *problemsView {
	namespace := a.Config.ActiveNamespace()
	if client.IsClusterWide(namespace) {
		namespace = ""
	}
	v := &problemsView{
		Flex: tview.NewFlex().SetDirection(tview.FlexRow), app: a,
		table: tview.NewTable(), header: tview.NewTextView(), detail: tview.NewTextView(), footer: tview.NewTextView(),
		prompt: tview.NewInputField(), contextName: a.Config.ActiveContextName(), namespace: namespace,
		collect: problems.Collect, expanded: make(map[string]bool), destinationRevision: a.Config.DestinationRevision(),
	}
	v.table.SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(true)
	v.header.SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	v.detail.SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	v.footer.SetDynamicColors(true).SetWrap(false)
	v.prompt.SetLabel("Filter (all words must match): ")
	v.AddItem(v.header, 3, 0, false).AddItem(v.table, 0, 1, true).AddItem(v.detail, 3, 0, false).AddItem(v.footer, 1, 0, false)
	v.SetBorder(true).SetTitle(" Problems ")
	v.table.SetInputCapture(v.key)
	v.table.SetSelectionChangedFunc(func(int, int) { v.renderDetail() })
	v.prompt.SetChangedFunc(func(text string) {
		if v.prompting {
			v.query = strings.TrimSpace(text)
			v.render()
		}
	})
	v.prompt.SetDoneFunc(func(key tcell.Key) {
		if key != tcell.KeyEnter && key != tcell.KeyEscape {
			return
		}
		if key == tcell.KeyEscape {
			v.query = ""
		}
		v.prompting = false
		v.RemoveItem(v.prompt)
		v.app.SetFocus(v.table)
		v.render()
	})
	v.actions = v.makeActions()
	return v
}

func (*problemsView) Name() string                           { return "problems" }
func (*problemsView) CompactWorkspace() bool                 { return true }
func (*problemsView) SetCommand(*cmd.Interpreter)            {}
func (*problemsView) SetLabelSelector(labels.Selector, bool) {}
func (v *problemsView) SetFilter(query string, _ bool) {
	v.query = strings.TrimSpace(query)
	v.render()
}
func (v *problemsView) InCmdMode() bool         { return v.prompting }
func (v *problemsView) Actions() *ui.KeyActions { return v.actions }
func (v *problemsView) Hints() model.MenuHints  { return actionCatalogHints(v, v.app) }
func (*problemsView) ExtraHints() map[string]string {
	return map[string]string{"Problems": "One bounded read per kind of the viewing destination, taken when opened and when r is pressed. " +
		"Nothing is polled or stored. Causes are status and event text, not inferred root causes. A source named NOT READ may hide problems."}
}

func (v *problemsView) Init(context.Context) error {
	v.StylesChanged(v.app.Styles)
	return nil
}

func (v *problemsView) Start() {
	if v.started {
		return
	}
	v.started = true
	v.restoreSourceNamespace()
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.originalCapture = v.app.GetInputCapture()
	v.app.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if v.prompting || v.app.Content.IsTopDialog() {
			return e
		}
		if !v.app.Prompt().InCmdMode() && (e.Rune() == '/' || e.Key() == tcell.KeyEscape) {
			return v.key(e)
		}
		if v.originalCapture != nil {
			return v.originalCapture(e)
		}
		return e
	})
	if v.snapshot == nil && !v.loading {
		v.refresh()
	} else {
		v.render()
	}
}

func (v *problemsView) Stop() {
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	if v.loading {
		v.loading = false
		v.status = "read canceled while away; r reads again"
	}
	if v.prompting {
		v.prompting = false
		v.RemoveItem(v.prompt)
	}
	if v.started {
		v.started = false
		v.app.Styles.RemoveListener(v)
		v.app.SetInputCapture(v.originalCapture)
	}
}

func (v *problemsView) StylesChanged(styles *config.Styles) {
	p := styles.Semantic()
	canvas := p.Canvas.Color()
	text := config.ReadableForeground(p.Text.Color(), canvas)
	v.SetBackgroundColor(canvas).SetBorderColor(p.Muted.Color()).SetBorderFocusColor(p.Focus.Color()).SetTitleColor(p.Focus.Color())
	v.table.SetBackgroundColor(canvas)
	v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(config.ReadableForeground(text, p.Selected.Color())).Bold(true))
	for _, view := range []*tview.TextView{v.header, v.detail, v.footer} {
		view.SetBackgroundColor(canvas)
		view.SetTextColor(text)
	}
	v.footer.SetTextColor(config.ReadableForeground(p.Muted.Color(), canvas))
	v.prompt.SetFieldBackgroundColor(p.Panel.Color()).SetFieldTextColor(text).SetLabelColor(p.Focus.Color())
	v.render()
}

func (v *problemsView) selectedLine() *problemsLine {
	row, _ := v.table.GetSelection()
	if row < 1 || row > len(v.lines) {
		return nil
	}
	return &v.lines[row-1]
}

// SelectedResource exposes the selected row's captured identity, so the shared
// inspection actions in the palette work on it without another lookup.
func (v *problemsView) SelectedResource() SelectedResourceTarget {
	line := v.selectedLine()
	if line == nil {
		return SelectedResourceTarget{Context: v.contextName, UnavailableReason: "Select a listed resource first"}
	}
	row := line.row
	if row.Resource == (schema.GroupVersionResource{}) {
		return SelectedResourceTarget{Context: v.contextName, Namespace: row.Namespace, Name: row.Name,
			UnavailableReason: fmt.Sprintf("%s is named only by an event; open its list with :%s", row.Kind, strings.ToLower(row.Kind))}
	}
	gvr := row.Resource.Version + "/" + row.Resource.Resource
	if row.Resource.Group != "" {
		gvr = row.Resource.Group + "/" + gvr
	}
	return SelectedResourceTarget{Context: v.contextName, GVR: client.NewGVR(gvr), Namespace: row.Namespace, Name: row.Name, UID: row.UID}
}

func (v *problemsView) availability(key tcell.Key) string {
	if !v.destinationCurrent() {
		return "Destination changed; reopen :problems"
	}
	if key == ui.KeyR {
		return problemsUnavailable(v.app)
	}
	line := v.selectedLine()
	if key == ui.KeySpace {
		if line == nil || (!line.child && line.row.Affected() == 0) {
			return "Select a resource that has affected pods folded into it"
		}
		return ""
	}
	target := v.SelectedResource()
	if err := target.Err(); err != nil {
		return err.Error()
	}
	if key == ui.KeyL && target.GVR != client.PodGVR {
		if line.row.Affected() > 0 {
			return "Space expands the affected pods; select one to open its logs"
		}
		return "Select a Pod to open logs"
	}
	return ""
}

func (v *problemsView) makeActions() *ui.KeyActions {
	actions := ui.NewKeyActions()
	for _, item := range []struct {
		key       tcell.Key
		id, label string
		selection bool
	}{
		{tcell.KeyEnter, "open", "Open in its resource list", true},
		{ui.KeyD, "describe", "Describe", true},
		{ui.KeyL, "logs", "Pod logs", true},
		{ui.KeySpace, "expand", "Expand / fold affected pods", true},
		{ui.KeySlash, "filter", "Filter rows", false},
		{ui.KeyR, "reread", "Re-read now (no polling)", false},
	} {
		key := item.key
		action := ui.NewKeyAction(item.label, func(*tcell.EventKey) *tcell.EventKey { return v.key(actionEvent(key)) }, true)
		action.ID = "problems." + item.id
		action.Category = ui.ActionNavigate
		action.Opts.RequiresSelection = item.selection
		if key != ui.KeySlash {
			action.Availability = func() string { return v.availability(key) }
		}
		actions.Add(key, action)
	}
	actions.Add(tcell.KeyCtrlO, ui.NewKeyAction("Actions", v.app.actionsCmd, true))
	return actions
}

func (v *problemsView) key(e *tcell.EventKey) *tcell.EventKey {
	if v.prompting {
		return e
	}
	var key tcell.Key
	switch {
	case e.Key() == tcell.KeyEscape:
		if v.query != "" {
			v.query = ""
			v.render()
			return nil
		}
		return v.app.PrevCmd(e)
	case e.Key() == tcell.KeyEnter:
		key = tcell.KeyEnter
	case e.Key() == tcell.KeyRune && strings.ContainsRune("dlr/ ", e.Rune()):
		key = tcell.Key(e.Rune())
	case e.Key() == tcell.KeyRight || e.Key() == tcell.KeyLeft:
		if line := v.selectedLine(); line != nil && !line.child && line.row.Affected() > 0 && v.expanded[line.row.Key()] != (e.Key() == tcell.KeyRight) {
			v.toggle()
		}
		return nil
	default:
		return e
	}
	if key == ui.KeySlash {
		v.prompting = true
		v.prompt.SetText(v.query)
		v.AddItem(v.prompt, 1, 0, true)
		v.app.SetFocus(v.prompt)
		return nil
	}
	if reason := v.availability(key); reason != "" {
		v.app.Flash().Warn(reason)
		return nil
	}
	switch key {
	case tcell.KeyEnter:
		v.jump(v.SelectedResource())
	case ui.KeyD:
		target := v.SelectedResource()
		describeResource(v.app, nil, target.GVR, target.Path())
	case ui.KeyL:
		if err := v.app.dailyWorkspaceOpenLogs(v.SelectedResource()); err != nil {
			v.app.Flash().Err(err)
		}
	case ui.KeySpace:
		v.toggle()
	case ui.KeyR:
		v.refresh()
	}
	return nil
}

// toggle expands or folds the affected pods of the selected resource. On a
// folded pod it folds the owner and selects it.
func (v *problemsView) toggle() {
	row, _ := v.table.GetSelection()
	line := v.selectedLine()
	if line == nil {
		return
	}
	for line.child && row > 1 {
		row--
		line = &v.lines[row-1]
	}
	key := line.row.Key()
	v.expanded[key] = !v.expanded[key]
	v.table.Select(row, 0)
	v.render()
}

func (v *problemsView) destinationCurrent() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}

// jump opens the resource's ordinary list with the row selected, the same way
// related-resource jumps do. Esc there returns here; the viewing namespace this
// jump changed is restored on return unless the user changed it again.
//
//nolint:gocritic // Navigation owns an immutable copy of the captured identity.
func (v *problemsView) jump(target SelectedResourceTarget) {
	command := target.GVR.String()
	if target.Namespace != "" {
		command += " " + target.Namespace
	}
	sourceNamespace := v.app.Config.ActiveNamespace()
	v.app.gotoResource(command, target.Path(), false, true)
	destination := &inspectionReturnDestination{sourceNamespace: sourceNamespace,
		destinationNamespace: v.app.Config.ActiveNamespace(), revision: v.app.Config.DestinationRevision()}
	if native, ok := v.app.Content.Top().(ResourceViewer); ok && native.GetTable() != nil && native.GVR() == target.GVR {
		native.GetTable().expectedTarget = &target
		v.returnDestination = destination
		v.app.Flash().Info("Esc returns to the problems list as it was read")
	} else if v.app.Content.Top() == v {
		v.returnDestination = destination
		v.restoreSourceNamespace()
	}
}

func (v *problemsView) restoreSourceNamespace() {
	destination := v.returnDestination
	v.returnDestination = nil
	if destination == nil || v.app.Content.Top() != v || v.contextName != v.app.Config.ActiveContextName() ||
		v.app.Config.ActiveNamespace() != destination.destinationNamespace || v.app.Config.DestinationRevision() != destination.revision {
		return
	}
	if v.app.Config.ActiveNamespace() != destination.sourceNamespace {
		if err := v.app.switchNS(destination.sourceNamespace); err != nil {
			v.app.Flash().Err(fmt.Errorf("Return namespace unavailable: %w", err))
			return
		}
	}
	v.destinationRevision = v.app.Config.DestinationRevision()
}

// refresh starts the one bounded observation. The client is pinned before the
// goroutine starts, so a later context change cannot redirect the read, and a
// result is accepted only by the view, destination and request that asked.
func (v *problemsView) refresh() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.generation++
	v.loading = false
	if !v.destinationCurrent() {
		v.status = "destination changed; reopen :problems"
		v.render()
		return
	}
	connection := v.app.Conn()
	if connection == nil {
		v.status = "not read: " + problemsNoConnection
		v.render()
		return
	}
	reader, err := connection.DynDial()
	if err != nil || reader == nil {
		v.status = "not read: Kubernetes client unavailable"
		if err != nil {
			v.status = "not read: " + err.Error()
		}
		v.render()
		return
	}
	generation, namespace := v.generation, v.namespace
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel, v.loading = cancel, true
	v.status = fmt.Sprintf("reading each source once (%s each, %s in total)...", problems.ReadTimeout, problems.TotalDeadline)
	v.render()
	go func() {
		defer cancel()
		snapshot := v.collect(ctx, reader, namespace, time.Now())
		if ctx.Err() != nil || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if generation != v.generation || !v.destinationCurrent() || v.app.Content.Top() != v {
				return
			}
			v.cancel, v.loading, v.status = nil, false, ""
			v.acceptSnapshot(&snapshot)
		})
	}()
}

func (v *problemsView) acceptSnapshot(snapshot *problems.Snapshot) {
	selected := ""
	if line := v.selectedLine(); line != nil {
		selected = line.row.Key()
	}
	v.snapshot = snapshot
	v.render()
	v.table.Select(1, 0)
	for i := range v.lines {
		if v.lines[i].row.Key() == selected {
			v.table.Select(i+1, 0)
			break
		}
	}
	v.renderDetail()
	// Row actions become available with the first rows; refresh their hints.
	if v.app.Content != nil && v.app.Content.Top() == v {
		v.app.Menu().HydrateMenu(v.Hints())
	}
}

func (v *problemsView) severityColor(severity problems.Severity) tcell.Color {
	p := v.app.Styles.Semantic()
	color := p.Unknown
	switch severity {
	case problems.Failing:
		color = p.Failure
	case problems.Degraded:
		color = p.Warning
	case problems.Progressing:
		color = p.Progress
	}
	return config.ReadableForeground(color.Color(), p.Canvas.Color())
}

func (v *problemsView) render() {
	if v.app == nil || v.app.Styles == nil {
		return
	}
	width := v.width
	if width <= 0 {
		width = 78
	}
	p := v.app.Styles.Semantic()
	canvas := p.Canvas.Color()
	text := config.ReadableForeground(p.Text.Color(), canvas)
	muted := config.ReadableForeground(p.Muted.Color(), canvas)
	status := v.status
	if v.query != "" && v.snapshot != nil {
		shown := 0
		for i := range v.snapshot.Rows {
			if row := &v.snapshot.Rows[i]; problemsMatch(row, v.query) {
				shown++
			} else {
				for j := range row.Children {
					if problemsMatch(&row.Children[j], v.query) {
						shown++
						break
					}
				}
			}
		}
		status = strings.TrimPrefix(status+" · ", " · ") + fmt.Sprintf("filter %q shows %d of %d", v.query, shown, len(v.snapshot.Rows))
	}
	// One cell narrower than the pane: a line that exactly fills it wraps.
	var header []string
	for index, line := range problemsHeader(v.snapshot, v.namespace, status, width) {
		for _, part := range strings.Split(problemsPack(line, width-1), "\n") {
			part = tview.Escape(part)
			if strings.HasPrefix(line, "NOT READ") {
				part = fmt.Sprintf("[#%06x::b]%s[-::-]", config.ReadableForeground(p.Warning.Color(), canvas).Hex(), part)
			} else if index == 0 {
				part = "[::b]" + part + "[::-]"
			}
			header = append(header, part)
		}
	}
	v.header.SetText(strings.Join(header, "\n"))
	v.headerRows = min(problemsWrappedRows(v.header.GetText(true), width-1), 7)
	v.ResizeItem(v.header, v.headerRows, 0)

	selected, _ := v.table.GetSelection()
	v.table.Clear()
	v.lines = nil
	if v.snapshot != nil {
		v.lines = problemsLines(v.snapshot.Rows, v.expanded, v.query)
	}
	ascii := v.app.Config.K9s.UI.NoIcons
	allNamespaces := v.namespace == ""
	pods := false
	for i := range v.lines {
		pods = pods || v.lines[i].row.Affected() > 0
	}
	var readAt time.Time
	if v.snapshot != nil {
		readAt = v.snapshot.ReadAt
	}
	columns := problemsLayout(width, v.lines, allNamespaces, ascii, readAt)
	for col, label := range problemsHeaders(columns) {
		cell := tview.NewTableCell(label).SetSelectable(false).SetTextColor(muted).SetAttributes(tcell.AttrBold)
		if label == "CAUSE" {
			cell.SetExpansion(1)
		}
		v.table.SetCell(0, col, cell)
	}
	for i := range v.lines {
		line := &v.lines[i]
		for col, value := range problemsCells(line, columns, allNamespaces, ascii, width < 100, readAt) {
			cell := tview.NewTableCell(tview.Escape(value)).SetTextColor(text)
			switch {
			case col == 0:
				cell.SetTextColor(v.severityColor(line.row.Severity)).SetAttributes(tcell.AttrBold)
			case line.child:
				cell.SetTextColor(muted)
			}
			v.table.SetCell(i+1, col, cell)
		}
	}
	if len(v.lines) > 0 {
		v.table.Select(max(1, min(selected, len(v.lines))), 0)
		v.ResizeItem(v.table, 0, 1)
	} else {
		// Nothing to select: the explanation takes the room instead of a blank list.
		v.ResizeItem(v.table, 0, 0)
		v.ResizeItem(v.detail, 0, 1)
	}
	v.footer.SetText(tview.Escape(problemsFooter(width, len(v.lines) > 0, pods)))
	v.renderDetail()
}

// renderDetail spells out the selected row. The pane grows from three lines up
// to a share of the height so that the Warning events under a long cause are
// not cut off; the list keeps the rest.
func (v *problemsView) renderDetail() {
	width := v.width
	if width <= 0 {
		width = 78
	}
	switch line := v.selectedLine(); {
	case line != nil:
		text := problemsDetail(line, v.snapshot.ReadAt)
		limit := 6
		if v.height > 0 {
			limit = max(3, min(limit, (v.height-v.headerRows-1)/3))
		}
		v.detail.SetText(tview.Escape(text))
		v.detailRows = max(3, min(limit, problemsWrappedRows(text, width-1)))
		v.ResizeItem(v.detail, v.detailRows, 0)
	case v.snapshot != nil:
		v.detail.SetText(tview.Escape(problemsPack(problemsEmpty(v.snapshot, v.namespace, v.query), width-1)))
	default:
		v.detail.SetText("")
	}
	v.detail.ScrollToBeginning()
}

// Draw re-budgets the columns when the viewport width changes.
func (v *problemsView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Box) {
		return
	}
	if _, _, width, height := v.GetInnerRect(); width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	// A table that once fit keeps tracking its end after the viewport shrinks,
	// which would paint one frame with the selected row scrolled out of view.
	v.table.SetOffset(v.table.GetOffset())
	v.Flex.Draw(screen)
}
