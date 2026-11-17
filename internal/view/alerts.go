// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/metadata"
)

const (
	alertsCommandToken = "alerts"
	alertsNotDeclared  = "No Alertmanager source is declared for this context; :sources shows how to declare one"
)

// alertsReadDeadline bounds one observation: the alert read and the owner
// reads that follow it.
const alertsReadDeadline = observability.DefaultTimeout + alerts.OwnerDeadline + time.Second

// alertsView answers "what is paging, and which object is it about?". It
// owns one retained observation of the declared Alertmanager source, read on
// open and again only on request. It stores nothing, polls nothing and never
// writes. Mutable state belongs to the application draw goroutine; the read
// runs on its own goroutine against a client resolved before it starts.
type alertsView struct {
	*tview.Flex
	app                    *App
	table                  *tview.Table
	header, detail, footer *tview.TextView
	prompt                 *tview.InputField
	actions                *ui.KeyActions
	contextName, source    string
	snapshot               *alerts.Snapshot
	groups                 []alerts.Group
	names                  map[string]string // fingerprint -> alert name, for "inhibited by"
	// read performs the bounded observation; tests replace it.
	read                   func(context.Context) alerts.Snapshot
	lines                  []alertsLine
	expanded               map[string]bool
	hideSuppressed         bool
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
	_ model.Component  = (*alertsView)(nil)
	_ SelectedResource = (*alertsView)(nil)
)

func (c *Command) alertsCommand() {
	if reason := alertsUnavailable(c.app); reason != "" {
		c.app.Flash().Warn(reason)
		return
	}
	if err := c.app.inject(newAlertsView(c.app), false); err != nil {
		c.app.Flash().Err(err)
	}
}

// alertsUnavailable reads configuration only; nothing is contacted.
func alertsUnavailable(a *App) string {
	if sources := declaredSources(a); sources == nil || sources.Alertmanager == nil {
		return alertsNotDeclared
	}
	return ""
}

func alertsAction(app *App) ui.ActionDescriptor {
	return ui.ActionDescriptor{
		ID: "command.alerts", Label: "Alerts firing, tied to objects", Category: ui.ActionNavigate,
		Shortcut: ":" + alertsCommandToken, Discoverable: true, UnavailableReason: alertsUnavailable(app),
		Handler: func(*tcell.EventKey) *tcell.EventKey { NewCommand(app).alertsCommand(); return nil },
	}
}

func newAlertsView(a *App) *alertsView {
	v := &alertsView{
		Flex: tview.NewFlex().SetDirection(tview.FlexRow), app: a,
		table: tview.NewTable(), header: tview.NewTextView(), detail: tview.NewTextView(), footer: tview.NewTextView(),
		prompt: tview.NewInputField(), contextName: a.Config.ActiveContextName(), source: "Alertmanager",
		expanded: make(map[string]bool), destinationRevision: a.Config.DestinationRevision(),
	}
	if sources := declaredSources(a); sources != nil && sources.Alertmanager != nil {
		v.source = "Alertmanager " + observability.Clean(sources.Alertmanager.Display(), 120)
	}
	v.table.SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.header.SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	v.detail.SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	v.footer.SetDynamicColors(true).SetWrap(false)
	v.prompt.SetLabel("Filter (all words must match): ")
	v.AddItem(v.header, 3, 0, false).AddItem(v.table, 0, 1, true).AddItem(v.detail, 3, 0, false).AddItem(v.footer, 1, 0, false)
	v.SetBorder(true).SetTitle(" Alerts ")
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

func (*alertsView) Name() string                           { return "alerts" }
func (*alertsView) CompactWorkspace() bool                 { return true }
func (*alertsView) SetCommand(*cmd.Interpreter)            {}
func (*alertsView) SetLabelSelector(labels.Selector, bool) {}
func (v *alertsView) SetFilter(query string, _ bool) {
	v.query = strings.TrimSpace(query)
	v.render()
}
func (v *alertsView) InCmdMode() bool         { return v.prompting }
func (v *alertsView) Actions() *ui.KeyActions { return v.actions }
func (v *alertsView) Hints() model.MenuHints  { return actionCatalogHints(v, v.app) }
func (*alertsView) ExtraHints() map[string]string {
	return map[string]string{"Alerts": "One bounded read of the declared Alertmanager, taken when opened and when r is pressed. " +
		"Nothing is polled or stored. An alert is tied to an object by its labels only. Resolved alerts are not held, and " +
		"an empty list is not a health check."}
}

func (v *alertsView) Init(context.Context) error {
	v.StylesChanged(v.app.Styles)
	return nil
}

func (v *alertsView) Start() {
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

func (v *alertsView) Stop() {
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

func (v *alertsView) StylesChanged(styles *config.Styles) {
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

func (v *alertsView) selectedLine() *alertsLine {
	row, _ := v.table.GetSelection()
	if row < 1 || row > len(v.lines) || v.lines[row-1].heading != "" {
		return nil
	}
	return &v.lines[row-1]
}

// selectedTarget is the object of the selected row: the workload of a folded
// row, otherwise the alert's own object.
func (v *alertsView) selectedTarget() *alerts.Target {
	line := v.selectedLine()
	switch {
	case line == nil:
		return nil
	case line.item != nil:
		return &line.item.Target
	}
	return &line.group.Target
}

// SelectedResource exposes the mapped object, so the shared inspection
// actions in the palette work on it without another lookup.
func (v *alertsView) SelectedResource() SelectedResourceTarget {
	target := v.selectedTarget()
	switch {
	case target == nil:
		return SelectedResourceTarget{Context: v.contextName, UnavailableReason: "Select a listed alert first"}
	case !target.Mapped():
		return SelectedResourceTarget{Context: v.contextName,
			UnavailableReason: "This alert's labels name no Kubernetes object; d shows every label"}
	}
	return SelectedResourceTarget{Context: v.contextName, GVR: client.NewGVR(target.Resource), Namespace: target.Namespace, Name: target.Name}
}

func (v *alertsView) availability(key tcell.Key) string {
	if !v.destinationCurrent() {
		return "Destination changed; reopen :alerts"
	}
	line := v.selectedLine()
	switch key {
	case ui.KeyR:
		return alertsUnavailable(v.app)
	case ui.KeyS:
		return ""
	case ui.KeySpace:
		if line == nil || !line.group.Folded() {
			return "Select a row that folds several alerts"
		}
		return ""
	case ui.KeyD, ui.KeyC:
		if line == nil {
			return "Select a listed alert first"
		}
		if line.item == nil {
			return "Space lists each alert of this row; select one"
		}
		if key == ui.KeyC && line.item.Runbook() == "" && line.item.Alert.GeneratorURL == "" {
			return "This alert carries no runbook or generator URL"
		}
		return ""
	}
	target := v.SelectedResource()
	if err := target.Err(); err != nil {
		return err.Error()
	}
	if key == ui.KeyT && target.Namespace == "" {
		return "A timeline follows a namespaced workload; Enter opens this " + strings.ToLower(v.selectedTarget().Kind)
	}
	return ""
}

func (v *alertsView) makeActions() *ui.KeyActions {
	actions := ui.NewKeyActions()
	for _, item := range []struct {
		key       tcell.Key
		id, label string
		selection bool
	}{
		{tcell.KeyEnter, "open", "Open the alerted object in its list", true},
		{ui.KeyT, "timeline", "Timeline of the alerted object", true},
		{ui.KeyD, "fields", "All labels, annotations and links", true},
		{ui.KeyC, "copy", "Copy runbook URL", true},
		{ui.KeySpace, "expand", "Expand / fold alerts of one workload", true},
		{ui.KeyS, "suppressed", "Show / hide silenced and inhibited", false},
		{ui.KeySlash, "filter", "Filter alerts", false},
		{ui.KeyR, "reread", "Re-read now (no polling)", false},
	} {
		key := item.key
		action := ui.NewKeyAction(item.label, func(*tcell.EventKey) *tcell.EventKey { return v.key(actionEvent(key)) }, true)
		action.ID = "alerts." + item.id
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

func (v *alertsView) key(e *tcell.EventKey) *tcell.EventKey {
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
	case e.Key() == tcell.KeyRune && strings.ContainsRune("tdcsr/ ", e.Rune()):
		key = tcell.Key(e.Rune())
	case e.Key() == tcell.KeyRight || e.Key() == tcell.KeyLeft:
		if line := v.selectedLine(); line != nil && !line.child && line.group.Folded() && v.expanded[line.group.Key()] != (e.Key() == tcell.KeyRight) {
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
	case ui.KeyT:
		v.app.openTimeline(v.SelectedResource(), "")
	case ui.KeyD:
		v.showFields()
	case ui.KeyC:
		v.copyLink()
	case ui.KeySpace:
		v.toggle()
	case ui.KeyS:
		v.hideSuppressed = !v.hideSuppressed
		v.render()
	case ui.KeyR:
		v.refresh()
	}
	return nil
}

// showFields opens every field of the selected alert as scrollable text.
func (v *alertsView) showFields() {
	line := v.selectedLine()
	if line == nil || line.item == nil || v.snapshot == nil {
		return
	}
	details := NewDetails(v.app, "Alert", line.item.Name(), contentInspection, true).
		Update(alertsReport(line.item, v.snapshot, v.source, v.names))
	if err := v.app.inject(details, false); err != nil {
		v.app.Flash().Err(err)
	}
}

// copyLink copies the runbook URL, or the generator URL when the alert has no
// runbook, to the clipboard. The link is never opened or contacted.
func (v *alertsView) copyLink() {
	line := v.selectedLine()
	if line == nil || line.item == nil {
		return
	}
	what, link := "Runbook URL", line.item.Runbook()
	if link == "" {
		what, link = "Generator URL", line.item.Alert.GeneratorURL
	}
	if err := clipboardWrite(link); err != nil {
		v.app.Flash().Err(err)
		return
	}
	v.app.Flash().Info(what + " copied to the clipboard")
}

// toggle expands or folds the selected row. On a folded alert it folds the
// row above it and selects that row.
func (v *alertsView) toggle() {
	row, _ := v.table.GetSelection()
	line := v.selectedLine()
	if line == nil {
		return
	}
	for line.child && row > 1 {
		row--
		line = &v.lines[row-1]
	}
	key := line.group.Key()
	v.expanded[key] = !v.expanded[key]
	v.table.Select(row, 0)
	v.render()
}

func (v *alertsView) destinationCurrent() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}

// jump opens the object's ordinary list with it selected, the same way the
// problems list does. Esc there returns here; the viewing namespace this jump
// changed is restored on return unless the user changed it again.
//
//nolint:gocritic // Navigation owns an immutable copy of the captured identity.
func (v *alertsView) jump(target SelectedResourceTarget) {
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
		v.app.Flash().Info("Esc returns to the alerts as they were read")
	} else if v.app.Content.Top() == v {
		v.returnDestination = destination
		v.restoreSourceNamespace()
	}
}

func (v *alertsView) restoreSourceNamespace() {
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

// alertsReader resolves the declared source and a metadata-only Kubernetes
// client on the calling (draw) goroutine without contacting either, and
// returns the bounded read to run off it. declared is false when the context
// declares no Alertmanager, in which case callers that only add alerts to
// another observation say nothing about them. A declaration that cannot be
// used still returns a read, which reports why. owners asks for the pod
// owner reads; views that already know ownership pass false.
func alertsReader(app *App, owners bool) (read func(context.Context) alerts.Snapshot, declared bool) {
	source, status := observabilitySource(app, observability.KindAlertmanager)
	if source == nil {
		return func(context.Context) alerts.Snapshot { return alerts.Snapshot{Status: status, ReadAt: time.Now()} },
			status.State != observability.StateNotConfigured
	}
	var meta metadata.Interface
	if owners && app.Conn() != nil && app.Conn().Config() != nil {
		if cfg, err := app.Conn().Config().RESTConfig(); err == nil {
			if dial, err := metadata.NewForConfig(cfg); err == nil {
				meta = dial
			}
		}
	}
	return func(ctx context.Context) alerts.Snapshot {
		defer source.Close()
		return alerts.Collect(ctx, source, meta, time.Now())
	}, true
}

// alertsBeside runs the alert read beside another bounded observation and
// returns a function that waits for it. The wait ends with ctx at the latest.
func alertsBeside(ctx context.Context, read func(context.Context) alerts.Snapshot) func() alerts.Snapshot {
	observed := make(chan alerts.Snapshot, 1)
	go func() { observed <- read(ctx) }()
	return func() alerts.Snapshot {
		select {
		case snapshot := <-observed:
			return snapshot
		case <-ctx.Done():
			return alerts.Snapshot{ReadAt: time.Now(), Status: observability.Status{Kind: observability.KindAlertmanager,
				State: observability.StateTimeout, Reason: "no answer inside this view's deadline"}}
		}
	}
}

// refresh starts the one bounded observation. A newer request or leaving the
// view cancels it, and a result is accepted only by the view, destination
// and request that asked.
func (v *alertsView) refresh() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.generation++
	v.loading = false
	if !v.destinationCurrent() {
		v.status = "destination changed; reopen :alerts"
		v.render()
		return
	}
	read := v.read
	if read == nil {
		read, _ = alertsReader(v.app, true)
	}
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), alertsReadDeadline)
	v.cancel, v.loading = cancel, true
	v.status = fmt.Sprintf("reading Alertmanager once (at most %s)...", observability.DefaultTimeout)
	v.render()
	go func() {
		defer cancel()
		snapshot := read(ctx)
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
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

func (v *alertsView) acceptSnapshot(snapshot *alerts.Snapshot) {
	selected := ""
	if line := v.selectedLine(); line != nil {
		selected = v.lineKey(line)
	}
	v.snapshot = snapshot
	v.groups = alerts.Groups(snapshot.Items)
	v.names = make(map[string]string, len(snapshot.Items))
	for i := range snapshot.Items {
		if item := &snapshot.Items[i]; item.Alert.Fingerprint != "" {
			v.names[item.Alert.Fingerprint] = item.Name()
		}
	}
	v.render()
	v.selectFirst()
	for i := range v.lines {
		if v.lines[i].heading == "" && v.lineKey(&v.lines[i]) == selected {
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

func (*alertsView) lineKey(line *alertsLine) string {
	key := line.group.Key()
	if line.child {
		key += "|" + line.item.Target.Key() + "|" + line.item.Alert.Fingerprint
	}
	return key
}

func (v *alertsView) selectFirst() {
	for i := range v.lines {
		if v.lines[i].heading == "" {
			v.table.Select(i+1, 0)
			return
		}
	}
}

func (v *alertsView) severityColor(severity string) config.Color {
	p := v.app.Styles.Semantic()
	switch alerts.SeverityRank(severity) {
	case 0, 1:
		return p.Failure
	case 2:
		return p.Warning
	case 3:
		return p.Unknown
	}
	return p.Muted
}

func (v *alertsView) hiddenSuppressed() int {
	if !v.hideSuppressed || v.snapshot == nil {
		return 0
	}
	hidden := 0
	for i := range v.snapshot.Items {
		if !v.snapshot.Items[i].IsFiring() {
			hidden++
		}
	}
	return hidden
}

func (v *alertsView) render() {
	if v.app == nil || v.app.Styles == nil {
		return
	}
	width := v.width
	if width <= 0 {
		width = 78
	}
	p := v.app.Styles.Semantic()
	canvas := p.Canvas.Color()
	ascii := v.app.Config.K9s.UI.NoIcons
	hidden := v.hiddenSuppressed()

	selected, _ := v.table.GetSelection()
	v.table.Clear()
	v.lines = nil
	var readAt time.Time
	if v.snapshot != nil {
		readAt = v.snapshot.ReadAt
		v.lines = alertsLines(v.groups, v.expanded, v.query, !v.hideSuppressed)
	}
	rows, folded := 0, false
	for i := range v.lines {
		if v.lines[i].heading == "" {
			rows++
			folded = folded || v.lines[i].group.Folded()
		}
	}

	status := v.status
	if v.query != "" && v.snapshot != nil && v.snapshot.Status.OK() {
		status = strings.TrimPrefix(status+" · ", " · ") + fmt.Sprintf("filter %q shows %d of %d rows", v.query, rows, len(v.groups))
	}
	// One cell narrower than the pane: a line that exactly fills it wraps.
	var header []string
	for index, line := range alertsHeader(v.snapshot, v.source, status, hidden, width) {
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

	columns := alertsLayout(width, v.lines, ascii, readAt)
	muted := config.ReadableForeground(p.Muted.Color(), canvas)
	v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(alertsHeading(columns))).SetSelectable(false).
		SetTextColor(muted).SetAttributes(tcell.AttrBold).SetExpansion(1))
	rule := "─"
	if ascii {
		rule = "-"
	}
	for i := range v.lines {
		line := &v.lines[i]
		if line.heading != "" {
			label := rule + rule + " " + line.heading + " "
			text := label + strings.Repeat(rule, max(width-tview.TaggedStringWidth(label), 0))
			v.table.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(timelineCell(text, width))).SetSelectable(false).SetTextColor(muted))
			continue
		}
		severity, rest := alertsCells(line, columns, ascii, width < 100, readAt)
		// A row that is not notifying is dimmed as a whole; its state is also
		// stated by the section heading and, where it fits, the STATE word.
		tone, bold := v.severityColor(line.group.Severity), true
		body := p.Text
		if line.group.State != alerts.Firing {
			tone, body, bold = p.Muted, p.Muted, false
		} else if line.child {
			body = p.Muted
		}
		text := timelineTag(tone, bold) + tview.Escape(severity) + timelineTag(body, false) + tview.Escape(rest) + "[-:-:-]"
		v.table.SetCell(i+1, 0, tview.NewTableCell(text).SetExpansion(1))
	}
	if rows > 0 {
		row := max(1, min(selected, len(v.lines)))
		for row <= len(v.lines) && v.lines[row-1].heading != "" {
			row++
		}
		if row > len(v.lines) {
			v.selectFirst()
		} else {
			v.table.Select(row, 0)
		}
		v.ResizeItem(v.table, 0, 1)
	} else {
		// Nothing to select: the explanation takes the room instead of a blank list.
		v.ResizeItem(v.table, 0, 0)
		v.ResizeItem(v.detail, 0, 1)
	}
	v.footer.SetText(tview.Escape(alertsFooter(width, rows > 0, folded)))
	v.renderDetail()
}

// renderDetail spells out the selected alert. The pane grows with its text up
// to a share of the height; d shows every field without a limit.
func (v *alertsView) renderDetail() {
	width := v.width
	if width <= 0 {
		width = 78
	}
	switch line := v.selectedLine(); {
	case line != nil && v.snapshot != nil:
		head, body := alertsDetail(line, v.snapshot.ReadAt, v.names)
		limit := 8
		if v.height > 0 {
			limit = max(4, min(limit, (v.height-v.headerRows-1)*2/5))
		}
		v.detail.SetText("[::b]" + tview.Escape(head) + "[::-]\n" + tview.Escape(body))
		v.detailRows = max(3, min(limit, problemsWrappedRows(head+"\n"+body, width-1)))
		v.ResizeItem(v.detail, v.detailRows, 0)
	case v.snapshot != nil:
		v.detail.SetText(tview.Escape(alertsEmpty(v.snapshot, v.query, v.hiddenSuppressed())))
	default:
		v.detail.SetText("")
	}
	v.detail.ScrollToBeginning()
}

// Draw re-budgets the columns when the viewport width changes.
func (v *alertsView) Draw(screen tcell.Screen) {
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
