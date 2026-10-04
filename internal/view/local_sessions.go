// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
)

const localSessionsCommand = "sessions"

type localSessions struct {
	*tview.Flex
	app                    *App
	header, detail, footer *tview.TextView
	table                  *tview.Table
	actions                *ui.KeyActions
	records                []session.Record
	selectedID             string
	details                bool
	started                bool
	narrow                 bool
	width                  int
	selectionInvalidated   bool
	notice                 string
}

var _ model.Component = (*localSessions)(nil)

func newLocalSessions(app *App) *localSessions {
	v := &localSessions{Flex: tview.NewFlex().SetDirection(tview.FlexRow), app: app,
		header: tview.NewTextView(), detail: tview.NewTextView(), footer: tview.NewTextView(), table: tview.NewTable(), actions: ui.NewKeyActions()}
	v.SetBorder(true).SetTitle(" Local sessions ")
	v.header.SetWrap(false)
	v.footer.SetWrap(true)
	v.detail.SetWrap(true).SetWordWrap(true).SetScrollable(true)
	v.table.SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.table.SetSelectionChangedFunc(func(row, _ int) {
		if row > 0 && row <= len(v.records) {
			if v.selectedID != v.records[row-1].ID {
				v.detail.ScrollToBeginning()
			}
			v.selectedID = v.records[row-1].ID
			v.selectionInvalidated = false
		}
		v.renderSelection()
	})
	v.SetInputCapture(v.key)
	v.configureLayout()
	v.bindActions()
	return v
}

func (c *Command) localSessionsCommand() {
	if err := c.app.inject(newLocalSessions(c.app), false); err != nil {
		c.app.Flash().Err(err)
	}
}

// Navigate only to an already-owned session, using full captured identity.
// Reviewing a Service or other origin never launches a probe or forward.
func (a *App) openLocalSessionsForTarget(target *SelectedResourceTarget) {
	view := newLocalSessions(a)
	records := a.localSessions.Records()
	for i := range records {
		record := &records[i]
		if localSessionMatchesTarget(&record.Spec.Destination, target) || localSessionMatchesTarget(&record.Spec.Origin, target) {
			view.selectedID = record.ID
			break
		}
	}
	if view.selectedID == "" {
		a.Flash().Warn("No local session owned for this captured resource identity; use its explicit launch action first")
		return
	}
	if err := a.inject(view, false); err != nil {
		a.Flash().Err(err)
	}
}

func (a *App) hasLocalSessionsForTarget(target *SelectedResourceTarget) bool {
	records := a.localSessions.Records()
	for i := range records {
		record := &records[i]
		if localSessionMatchesTarget(&record.Spec.Destination, target) || localSessionMatchesTarget(&record.Spec.Origin, target) {
			return true
		}
	}
	return false
}

func localSessionMatchesTarget(destination *session.Destination, target *SelectedResourceTarget) bool {
	return target != nil && target.GVR != nil && target.UID != "" && destination.Context == target.Context &&
		destination.GVR == target.GVR.String() && destination.Namespace == target.Namespace &&
		destination.Name == target.Name && destination.UID == string(target.UID)
}

func (*localSessions) Name() string                           { return "local sessions" }
func (*localSessions) CompactWorkspace() bool                 { return true }
func (*localSessions) SetCommand(*cmd.Interpreter)            {}
func (*localSessions) SetFilter(string, bool)                 {}
func (*localSessions) SetLabelSelector(labels.Selector, bool) {}
func (*localSessions) InCmdMode() bool                        { return false }
func (v *localSessions) Actions() *ui.KeyActions              { return v.actions }
func (v *localSessions) Hints() model.MenuHints               { return actionCatalogHints(v, v.app) }
func (*localSessions) ExtraHints() map[string]string {
	return map[string]string{"Ownership": "Only launches owned by this app are listed. Navigation retains established sessions. " +
		"c stops the selected owned resource; it does not undo accepted remote writes."}
}
func (v *localSessions) Init(context.Context) error {
	v.StylesChanged(v.app.Styles)
	v.render()
	return nil
}
func (v *localSessions) Start() {
	if !v.started {
		v.started = true
		v.app.Styles.AddListener(v)
	}
	v.render()
}
func (v *localSessions) Stop() {
	if v.started {
		v.started = false
		v.app.Styles.RemoveListener(v)
	}
}
func (v *localSessions) StylesChanged(styles *config.Styles) {
	p := styles.Semantic()
	canvas := p.Canvas.Color()
	text := config.ReadableForeground(p.Text.Color(), canvas)
	v.SetBackgroundColor(canvas).SetBorderColor(p.Muted.Color()).SetBorderFocusColor(p.Focus.Color()).SetTitleColor(p.Focus.Color())
	for _, view := range []*tview.TextView{v.header, v.detail, v.footer} {
		view.SetBackgroundColor(canvas)
		view.SetTextColor(text)
	}
	v.table.SetBackgroundColor(canvas)
	v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(config.ReadableForeground(text, p.Selected.Color())).Bold(true))
	v.render()
}

func (v *localSessions) Draw(screen tcell.Screen) {
	_, _, width, _ := v.GetInnerRect()
	if width != v.width {
		v.width = width
		v.narrow = width < 72
		v.render()
	}
	v.Flex.Draw(screen)
}
func (v *localSessions) Focus(delegate func(tview.Primitive)) {
	if v.details {
		delegate(v.detail)
	} else {
		delegate(v.table)
	}
}
func (v *localSessions) HasFocus() bool {
	return v.table.HasFocus() || v.detail.HasFocus() || v.Flex.HasFocus()
}

func (v *localSessions) bindActions() {
	for _, item := range []struct {
		key   tcell.Key
		label string
	}{
		{tcell.KeyEnter, "Lifecycle details"}, {ui.KeyR, "Refresh local state"}, {ui.KeyC, "Stop owned session"}, {tcell.KeyEscape, "Back"},
	} {
		key := item.key
		action := ui.NewKeyAction(item.label, func(*tcell.EventKey) *tcell.EventKey { return v.key(actionEvent(key)) }, key != tcell.KeyEscape)
		action.ID = "sessions." + strings.ReplaceAll(strings.ToLower(item.label), " ", "-")
		action.Category = ui.ActionNavigate
		if key == ui.KeyC || key == tcell.KeyEnter {
			// Selection is a local handle, independent of an API resource row.
			action.Availability = func() string {
				record, ok := v.selected()
				if !ok {
					return "Select an owned local session first"
				}
				if key == ui.KeyC && !record.Active {
					return "Selected local session has already ended"
				}
				return ""
			}
		}
		v.actions.Add(key, action)
	}
}
func (v *localSessions) key(event *tcell.EventKey) *tcell.EventKey {
	switch ui.AsKey(event) {
	case ui.KeyR:
		v.render()
		return nil
	case ui.KeyC:
		if record, ok := v.selected(); ok && record.Active {
			v.app.localSessions.Cancel(record.ID)
			v.notice = "Stop requested for " + record.ID + "; r checks confirmed cleanup."
			v.render()
		}
		return nil
	case tcell.KeyEnter:
		if _, ok := v.selected(); ok {
			v.details = !v.details
			v.detail.ScrollToBeginning()
			v.configureLayout()
			v.renderSelection()
			v.app.SetFocus(v)
		}
		return nil
	case tcell.KeyEscape:
		if v.details {
			v.details = false
			v.detail.ScrollToBeginning()
			v.configureLayout()
			v.renderSelection()
			v.app.SetFocus(v)
		} else {
			v.app.PrevCmd(event)
		}
		return nil
	}
	return event
}
func (v *localSessions) configureLayout() {
	v.Clear()
	v.detail.SetWrap(v.details).SetWordWrap(v.details)
	v.AddItem(v.header, 2, 0, false)
	if v.details {
		v.AddItem(v.detail, 0, 1, true)
	} else {
		v.AddItem(v.table, 0, 1, true).AddItem(v.detail, 4, 0, false)
	}
	v.AddItem(v.footer, 2, 0, false)
}
func (v *localSessions) render() {
	v.records = v.app.localSessions.Records()
	active := 0
	for i := range v.records {
		if v.records[i].Active {
			active++
		}
	}
	v.header.SetText(fmt.Sprintf("LOCAL SESSIONS · app-owned only\n%d active · %d ended · captured destinations", active, len(v.records)-active))
	v.table.Clear()
	headings := []string{"STATE", "WORKFLOW", "TARGET", "LOCAL BINDING"}
	if v.narrow {
		headings = []string{"STATE", "WORKFLOW", "TARGET"}
	}
	for col, label := range headings {
		v.table.SetCell(0, col, tview.NewTableCell(label).SetSelectable(false).SetAttributes(tcell.AttrBold))
	}
	selectedRow := 0
	if v.selectedID == "" && !v.selectionInvalidated {
		selectedRow = 1
	}
	for i := range v.records {
		record := &v.records[i]
		target := record.Spec.Destination.Name
		if target == "" {
			target = record.Spec.Label
		}
		workflow := record.Spec.Label
		if workflow == "" {
			workflow = string(record.Spec.Kind)
		}
		cells := []string{strings.ToUpper(string(record.State)), workflow, target, record.Spec.Binding}
		if v.narrow {
			cells = cells[:3]
		}
		for col, text := range cells {
			v.table.SetCell(i+1, col, tview.NewTableCell(tview.Escape(text)).SetMaxWidth([]int{9, 18, 24, 24}[col]).SetExpansion(1))
		}
		if record.ID == v.selectedID {
			selectedRow = i + 1
		}
	}
	if len(v.records) > 0 && selectedRow > 0 {
		v.table.Select(selectedRow, 0)
		v.selectedID = v.records[selectedRow-1].ID
	} else {
		if v.selectedID != "" {
			v.selectionInvalidated = true
		}
		v.selectedID = ""
		v.table.Select(0, 0)
	}
	v.renderSelection()
}
func (v *localSessions) selected() (session.Record, bool) {
	for i := range v.records {
		if v.records[i].ID == v.selectedID {
			return v.records[i], true
		}
	}
	return session.Record{}, false
}
func (v *localSessions) renderSelection() {
	record, ok := v.selected()
	if !ok {
		if len(v.records) == 0 {
			v.detail.SetText("No owned local sessions yet.\nLaunch port-forwards, container shells or plugins from their existing actions.")
		} else {
			v.detail.SetText("Selected history entry expired.\nChoose an owned local session before viewing details or requesting cleanup.")
		}
	} else if v.details {
		v.detail.SetText(localSessionDetails(&record))
	} else {
		destination := &record.Spec.Destination
		target := destination.Name
		if destination.Namespace != "" {
			target = destination.Namespace + "/" + target
		}
		uid := destination.UID
		if uid == "" {
			uid = "not asserted; plugin owns target scope"
		}
		width := max(1, v.width)
		if v.width == 0 {
			width = 78
		}
		v.detail.SetText(ui.Truncate(fmt.Sprintf("%s · %s · %s · %s", record.ID, strings.ToUpper(string(record.State)),
			record.Spec.Kind, record.Spec.Label), width) + "\n" +
			ui.Truncate("Context: "+ui.Truncate(destination.Context, max(1, (width-14)/2))+" · "+ui.Truncate(target, max(1, (width-14)/2)), width) + "\n" +
			ui.Truncate("UID: "+uid, width) + "\n" + ui.Truncate("Binding: "+record.Spec.Binding+" · Container: "+destination.Container, width))
	}
	footer := "Enter details · r refresh · c stop owned · Esc back\nLocal cleanup does not undo accepted remote writes."
	if v.notice != "" {
		footer = "Enter details · r refresh · c stop owned · Esc back\n" + v.notice
	}
	v.footer.SetText(footer)
}
func localSessionDetails(record *session.Record) string {
	d := &record.Spec.Destination
	var out strings.Builder
	server := d.Server
	if server == "" {
		server = "not asserted; plugin owns its destinations"
	}
	fmt.Fprintf(&out, "%s · %s · %s\nWorkflow: %s\nContext: %s\nResource: %s %s/%s\nUID: %s\nContainer: %s\nLocal binding: %s\nStarted: %s\n",
		record.ID, strings.ToUpper(string(record.State)), record.Spec.Kind, record.Spec.Label, d.Context, d.GVR, d.Namespace, d.Name, d.UID,
		d.Container, record.Spec.Binding, record.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&out, "Captured API connection: %s\nDestination revision: %d\n", server, d.Revision)
	if origin := &record.Spec.Origin; origin.UID != "" {
		fmt.Fprintf(&out, "Originating resource: %s %s/%s UID %s · context %s\n", origin.GVR, origin.Namespace, origin.Name, origin.UID, origin.Context)
	}
	if !record.EndedAt.IsZero() {
		fmt.Fprintf(&out, "Ended: %s\n", record.EndedAt.UTC().Format(time.RFC3339))
	}
	if record.Spec.IdentityNote != "" {
		fmt.Fprintf(&out, "Identity boundary: %s\n", record.Spec.IdentityNote)
	}
	if record.Spec.OperationID != "" {
		fmt.Fprintf(&out, "Operation receipt: %s (:operations)\n", record.Spec.OperationID)
	}
	out.WriteString("\nLIFECYCLE LOG · local source\n")
	for _, event := range record.Events {
		fmt.Fprintf(&out, "%s %s\n", event.At.UTC().Format(time.RFC3339), event.Message)
	}
	out.WriteString("\nLogs contain authored lifecycle facts; command arguments, environment and raw output are excluded.\n" +
		"Cancellation stops owned local work. It does not undo accepted cluster changes or prove remote commands stopped.\n")
	return out.String()
}
