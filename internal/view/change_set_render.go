// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

func (v *changeSetView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	if v.activeTab == 0 {
		v.renderRows()
		return
	}
	query, region := v.inspectionQuery, v.currentRegion
	row, col := v.text.GetScrollOffset()
	text := v.tabText()
	if len(text) > inspect.MaxComparisonText {
		text = text[:inspect.MaxComparisonText] + "\nDisplay capped at 256KiB; retained evidence is incomplete here."
	}
	v.Update(tview.Escape(logstream.SafeText(text)))
	if query != "" {
		v.model.Filter(query)
		if region < v.maxRegions {
			v.currentRegion = region
			v.text.Highlight(fmt.Sprintf("search_%d", region))
		}
	}
	v.text.ScrollTo(row, col)
}

func (v *changeSetView) renderChrome() {
	width := max(40, v.width)
	p := v.app.Styles.Semantic()
	for _, item := range []*tview.TextView{v.identityBar, v.tabsBar, v.footer} {
		item.SetBackgroundColor(p.Panel.Color())
		item.SetTextColor(p.Text.Color())
	}
	target := v.SelectedResource()
	identity := "ctx " + v.contextName + " · namespaces " + strings.Join(v.scope.Namespaces, ",")
	if target.Name != "" {
		identity = "ctx " + v.contextName + " · " + target.Path()
	}
	uid := "UID unavailable; prepare exact named identity"
	if target.UID != "" {
		uid = "UID " + string(target.UID)[:min(12, len(target.UID))]
	} else if v.plan != nil && v.focusedEntry < len(v.plan.Entries) && v.plan.Entries[v.focusedEntry].ExpectedAbsent {
		uid = "Reviewed named absence; create only"
	}
	sha := v.source.Identity.SHA256
	source := uid + " · SHA " + sha[:min(12, len(sha))]
	state := "NOT SUBMITTED · p prepare explicit dry-run"
	if v.plan != nil {
		selected := len(strings.Split(strings.TrimSuffix(v.selectionKey(), ","), ","))
		state = fmt.Sprintf("Preview %s · %d selected · no atomic batch", v.plan.PreparedAt.UTC().Format("15:04:05Z"), selected)
	}
	if v.selectionKey() == "" && v.plan != nil {
		state = "Preview retained · 0 selected · no atomic batch"
	}
	if v.loading {
		state = "Dry-run pending · nothing persisted · x cancel"
	}
	if v.task != nil {
		receipt := v.task.receipt()
		state = fmt.Sprintf("Task #%d · r receipts · x cancel remaining", receipt.ID)
	}
	if !v.destinationCurrent() {
		state = "Retained · destination changed; reopen"
	}
	if v.app.Config.IsReadOnly() {
		state = "READ ONLY · preview / apply unavailable"
	}
	lines := []string{identity, source, state}
	for index := range lines {
		lines[index] = tview.Escape(ui.Truncate(changeSetSingleLine(lines[index]), width))
	}
	v.identityBar.SetText(strings.Join(lines, "\n"))
	v.tabsBar.SetText(ui.TaskTabs(changeSetTabs, v.activeTab, width))
	footer := "Space select · p preview · a apply · Tab · Esc back"
	if width < 60 {
		footer = "Space · p preview · a apply · Esc back"
	}
	if v.activeTab != 0 {
		footer = "Tab next · r receipt · g source · Esc back"
	}
	v.footer.SetText(tview.Escape(ui.Truncate(footer, width)))
}

func (v *changeSetView) renderRows() {
	v.table.Clear()
	v.rows = nil
	width := max(40, v.width)
	headings := []string{"Pick / target", "Preview"}
	if width >= 80 {
		headings = append(headings, "Fields", "Precondition")
	}
	for column, heading := range headings {
		v.table.SetCell(0, column, tview.NewTableCell(heading).SetSelectable(false).SetAttributes(tcell.AttrBold))
	}
	count := len(v.initial)
	if v.plan != nil {
		count = len(v.plan.Entries)
	}
	query := strings.ToLower(v.inspectionQuery)
	selectedRow := 1
	for index := range count {
		entry := v.rowEntry(index)
		label := entry.Identity.Kind + " " + entry.Identity.Namespace + "/" + entry.Identity.Name
		if query != "" && !strings.Contains(strings.ToLower(label+" "+entry.State+" "+entry.Reason), query) {
			continue
		}
		v.rows = append(v.rows, index)
		row := len(v.rows)
		if index == v.focusedEntry {
			selectedRow = row
		}
		picked := "[ ] "
		if v.selected[index] {
			picked = "[x] "
		}
		if v.plan == nil {
			picked = "    "
		}
		v.table.SetCell(row, 0, tview.NewTableCell(tview.Escape(picked+changeSetSingleLine(label))).SetExpansion(1).SetMaxWidth(max(14, width/2)))
		state := entry.State
		if state == review.ChangeSetPrepared {
			state = "PREVIEW ACCEPTED"
		} else if state == review.ChangeSetBlocked {
			state = "SOURCE REVIEW"
		}
		if v.plan == nil {
			state = "NOT PREPARED"
		}
		v.table.SetCell(row, 1, tview.NewTableCell(tview.Escape(changeSetSingleLine(state))).SetMaxWidth(max(16, width/2-3)))
		if width >= 80 {
			v.table.SetCell(row, 2, tview.NewTableCell(fmt.Sprintf("%d", len(entry.Intent.Changes))))
			precondition := "UID/RV unknown"
			if entry.ExpectedAbsent {
				precondition = "named absence"
			} else if entry.Identity.UID != "" {
				precondition = "UID " + string(entry.Identity.UID)[:min(8, len(entry.Identity.UID))] + " RV " + entry.ResourceVersion
			}
			v.table.SetCell(row, 3, tview.NewTableCell(tview.Escape(precondition)).SetMaxWidth(28))
		}
	}
	if len(v.rows) == 0 {
		v.table.SetCell(1, 0, tview.NewTableCell("No matching targets; / clears search").SetSelectable(false))
	} else {
		v.table.Select(selectedRow, 0)
	}
}

func (v *changeSetView) rowEntry(index int) *review.ChangeSetEntry {
	if v.plan != nil {
		return &v.plan.Entries[index]
	}
	initial := &v.initial[index]
	return &review.ChangeSetEntry{Identity: initial.Identity, State: initial.State, Reason: initial.Reason, Document: initial.Document, Intent: initial.Intent}
}

func (v *changeSetView) tabText() string {
	if v.activeTab == 3 {
		return v.outcomesText()
	}
	if v.activeTab == 4 {
		return v.evidenceText()
	}
	if v.plan == nil {
		return "NO EXECUTABLE PREVIEW\n" + v.notice + "\n\np explicitly prepares strict server dry-run; no automatic write.\nEsc returns to the retained source review."
	}
	if v.focusedEntry < 0 || v.focusedEntry >= len(v.plan.Entries) {
		return "Select a target in Plan first."
	}
	entry := &v.plan.Entries[v.focusedEntry]
	var text strings.Builder
	if v.activeTab == 2 {
		fmt.Fprintf(&text, "OWNERSHIP ROUTING\n%s\n%s\n\n", entry.Policy.Route, entry.Policy.Reason)
		if entry.Ownership != nil {
			text.WriteString(entry.Ownership.Render(1))
			text.WriteString("\nSOURCE REVIEW\n")
			text.WriteString(entry.Ownership.Render(2))
		}
		text.WriteString("\ng reviews the selected live identity with supported native ownership/source evidence.\n" +
			"Source/Git changes require explicit source selection and review; no inferred repo path, branch substitution or automatic controller action.")
		return text.String()
	}
	fmt.Fprintf(&text, "REVIEWED TARGET CHANGES\n%s %s/%s · %s\n%s\n", entry.Identity.Kind, entry.Identity.Namespace, entry.Identity.Name, entry.State, entry.Reason)
	for _, change := range entry.Intent.Changes {
		fmt.Fprintf(&text, "\n%s %s\n  live: %s\n  authored: %s\n", change.Kind, change.Path, change.Before, change.After)
	}
	if len(entry.Intent.Changes) == 0 {
		text.WriteString("\nNo differences in the declared fields reviewed. Omitted/sensitive fields do not establish full equality.\n")
	}
	text.WriteString("\nADMISSION / DEFAULTING PROJECTION (DRY RUN)\n")
	for _, change := range entry.Projection.Changes {
		before, after := comparisonValues(change.Before, change.After)
		fmt.Fprintf(&text, "%s %s\n  live: %s\n  preview: %s\n", change.Kind, change.Path, before, after)
	}
	fmt.Fprintf(&text, "\nPRECONDITIONS\n%s · UID %s · RV %s · generation %d\nPrepared %s · expires %s\n",
		entry.Request, entry.Identity.UID, entry.ResourceVersion, entry.Generation,
		entry.PreparedAt.UTC().Format(time.RFC3339), v.plan.ExpiresAt.UTC().Format(time.RFC3339))
	text.WriteString("UNREVIEWED\n")
	for _, omitted := range entry.Intent.Unreviewed {
		text.WriteString(omitted + "\n")
	}
	text.WriteString("Raw authored payload remains private; full-object application requires acknowledgment. No pruning, force transfer or batch atomicity.\n")
	return text.String()
}
func (v *changeSetView) outcomesText() string {
	if v.task == nil {
		return "PER-TARGET OPERATION OUTCOMES\nNOT SUBMITTED\n" + v.notice + "\n\nApply needs selection and a separate explicit acknowledgment. No batch atomicity.\n"
	}
	receipt := v.task.receipt()
	return "PER-TARGET OPERATION OUTCOMES\n" + operationReceiptText(receipt, 1, 1) + "\n" +
		"o reads the selected native controller outcome separately. OBSERVED here describes API object fields only.\n"
}
func (v *changeSetView) evidenceText() string {
	value := any(v.plan)
	if v.plan == nil {
		value = struct {
			Source  review.SourceIdentity
			Scope   review.Scope
			Entries []review.Entry
		}{v.source.Identity, v.scope, v.initial}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "Retained change-set evidence unavailable"
	}
	return "RETAINED CHANGE-SET EVIDENCE\n" + v.notice + "\n" + string(data) +
		"\nSource and each observation keep independent identity/time. Raw objects and Secret values excluded."
}
