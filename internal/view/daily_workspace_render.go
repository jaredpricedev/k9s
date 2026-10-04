// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

type dailyWorkspaceTerm struct{ field, value string }

// Search is deliberately local, literal and small: all whitespace-separated
// tokens must match, optionally qualified with a known identity/status field.
// A mistyped structured term is rejected instead of widening the observation.
const dailyWorkspaceCoverageAbsent = "absent"

func parseDailyWorkspaceQuery(query string) ([]dailyWorkspaceTerm, error) {
	var terms []dailyWorkspaceTerm
	for _, token := range strings.Fields(query) {
		term := dailyWorkspaceTerm{value: strings.ToLower(token)}
		if field, value, found := strings.Cut(token, ":"); found {
			field = strings.ToLower(field)
			switch field {
			case "kind", "ns", "name", "status":
			default:
				return nil, fmt.Errorf("unknown search field %q; use kind:, ns:, name: or status:", field)
			}
			if value == "" {
				return nil, fmt.Errorf("%s: needs a value", field)
			}
			term.field, term.value = field, strings.ToLower(value)
		}
		terms = append(terms, term)
	}
	return terms, nil
}
func dailyWorkspaceMatch(terms []dailyWorkspaceTerm, kind, namespace, name, status string) bool {
	fields := map[string]string{"kind": kind, "ns": namespace, "name": name, "status": status}
	all := strings.Join([]string{kind, namespace, name, status}, " ")
	for _, term := range terms {
		value := all
		if term.field != "" {
			value = fields[term.field]
		}
		if !strings.Contains(strings.ToLower(value), term.value) {
			return false
		}
	}
	return true
}
func (w *dailyWorkspace) applyQuery(query string) bool {
	if _, err := parseDailyWorkspaceQuery(query); err != nil {
		w.notice = err.Error() + "; prior search retained"
		w.render()
		return false
	}
	w.query = strings.TrimSpace(query)
	if w.tabQueries == nil {
		w.tabQueries = make(map[string]string)
	}
	w.tabQueries[w.mode] = w.query
	w.render()
	return true
}
func dailyWorkspaceRefKey(ref *workspace.ResourceRef) string {
	if ref == nil {
		return ""
	}
	return ref.GVR + "/" + ref.Namespace + "/" + ref.Name + "/" + ref.UID
}
func dailyWorkspaceRowKey(row *dailyWorkspaceRow) string {
	if row.scopeName != "" {
		return "scope/" + row.scopeName
	}
	if row.key != "" {
		return row.key
	}
	if row.ref != nil {
		return dailyWorkspaceRefKey(row.ref)
	}
	return strings.Join(row.cells, "\x00")
}
func (w *dailyWorkspace) selectedKey() string {
	row, _ := w.table.GetSelection()
	if row > 0 && row <= len(w.rows) {
		return dailyWorkspaceRowKey(&w.rows[row-1])
	}
	return ""
}
func (w *dailyWorkspace) makeRows(terms []dailyWorkspaceTerm) ([]string, []dailyWorkspaceRow) {
	if w.mode == dailyWorkspaceScopesMode {
		return w.scopeRows(terms)
	}
	rows := make([]dailyWorkspaceRow, 0)
	headers := []string{"PRIORITY", dailyWorkspaceKindCol, "NAMESPACE", "NAME", "FINDING"}
	add := func(row dailyWorkspaceRow) { rows = append(rows, row) }
	switch w.mode {
	case inventoryCommand:
		headers = []string{dailyWorkspaceKindCol, "NAMESPACE", "NAME", statusCol, "AGE"}
		for _, resource := range w.snapshot.Resources {
			if !dailyWorkspaceMatch(terms, resource.Kind, resource.Ref.Namespace, resource.Ref.Name, resource.Summary) {
				continue
			}
			age := workspaceUnknown
			if resource.Object != nil {
				created := resource.Object.GetCreationTimestamp()
				if !created.IsZero() {
					age = dailyWorkspaceAge(created.Time, w.snapshot.ObservedAt)
				}
			}
			ref := resource.Ref
			add(dailyWorkspaceRow{
				cells: []string{resource.Kind, ref.Namespace, ref.Name, resource.Summary, age}, ref: &ref,
				detail: "Enter investigates this captured identity. p pins it. Search is local to this scope's retained inventory.",
			})
		}
	case dailyWorkspaceCoverageMode:
		headers = []string{"STATE", "KIND / API", "NAMESPACE", "DETAIL"}
		coverageRows := append([]workspace.Coverage(nil), w.coverage...)
		sort.SliceStable(coverageRows, func(i, j int) bool {
			a, b := coverageRows[i], coverageRows[j]
			aComplete := a.State == dailyWorkspaceCoverageComplete || a.State == dailyWorkspaceCoverageAbsent
			bComplete := b.State == dailyWorkspaceCoverageComplete || b.State == dailyWorkspaceCoverageAbsent
			if aComplete != bComplete {
				return !aComplete
			}
			return a.GVR+"/"+a.Namespace < b.GVR+"/"+b.Namespace
		})
		for _, coverage := range coverageRows {
			if !dailyWorkspaceMatch(terms, coverage.GVR, coverage.Namespace, "", coverage.State+" "+coverage.Detail) {
				continue
			}
			add(dailyWorkspaceRow{
				cells: []string{coverage.State, coverage.GVR, coverage.Namespace, coverage.Detail},
				key:   "coverage/" + coverage.GVR + "/" + coverage.Namespace, detail: coverage.Detail,
			})
		}
	case dailyWorkspaceHistoryMode:
		headers, rows = w.historyRows(terms)
	case dailyWorkspaceActivityMode:
		headers, rows = w.activityRows(terms)
	case dailyWorkspacePinsMode:
		headers = []string{"KIND / API", "NAMESPACE", "NAME", statusCol}
		for _, pin := range w.scope.Pins {
			status := "Not observed in snapshot"
			for _, resource := range w.snapshot.Resources {
				if resource.Ref.GVR == pin.GVR && resource.Ref.Namespace == pin.Namespace && resource.Ref.Name == pin.Name {
					status = resource.Summary
					if pin.UID != "" && pin.UID != resource.Ref.UID {
						status = "Identity changed — pin captures prior UID"
					}
					break
				}
			}
			if !dailyWorkspaceMatch(terms, pin.GVR, pin.Namespace, pin.Name, status) {
				continue
			}
			ref := pin
			add(dailyWorkspaceRow{
				cells: []string{pin.GVR, pin.Namespace, pin.Name, status}, ref: &ref,
				detail: "Pinned identity is retained even if absent. Enter verifies the pinned UID before inspection. p removes this pin.",
			})
		}
	default:
		for i := range w.snapshot.Findings {
			finding := &w.snapshot.Findings[i]
			if !dailyWorkspaceMatch(terms, finding.Kind, finding.Ref.Namespace, finding.Ref.Name, finding.Reason+" "+finding.Detail+" "+finding.Category) {
				continue
			}
			ref := finding.Ref
			severity := strings.ToUpper(finding.Severity)
			if severity == "" {
				severity = "ATTENTION"
			}
			add(dailyWorkspaceRow{cells: []string{severity, finding.Kind, ref.Namespace, ref.Name, finding.Reason}, ref: &ref, detail: finding.Detail})
		}
	}
	return headers, rows
}

func (w *dailyWorkspace) scopeRows(terms []dailyWorkspaceTerm) ([]string, []dailyWorkspaceRow) {
	headers := []string{"", "SCOPE", "CONTEXT", "NAMESPACES", "SELECTOR"}
	rows := make([]dailyWorkspaceRow, 0)
	for i := range w.store.Scopes {
		scope := &w.store.Scopes[i]
		if !dailyWorkspaceMatch(terms, "scope", strings.Join(scope.Namespaces, ","), scope.Name, scope.Context+" "+scope.LabelSelector) {
			continue
		}
		marker := " "
		if scope.Name == w.store.Active {
			marker = "*"
		}
		if scope.Context != w.contextName {
			marker = "↗"
		}
		rows = append(rows, dailyWorkspaceRow{
			cells: []string{marker, scope.Name, scope.Context, strings.Join(scope.Namespaces, ", "), scope.LabelSelector}, scopeName: scope.Name,
			detail: "Enter opens this saved scope. Context changes are explicit with :ctx. " +
				"n creates; e edits the selected scope; d removes local metadata.",
		})
	}
	return headers, rows
}

func (w *dailyWorkspace) render() {
	selected := w.selectedKey()
	terms, _ := parseDailyWorkspaceQuery(w.query)
	headers, rows := w.makeRows(terms)
	w.rows = rows
	w.displayColumns = make([]int, len(headers))
	for col := range headers {
		w.displayColumns[col] = col
	}
	if w.viewportWidth > 0 && w.viewportWidth < 80 {
		switch w.mode {
		case inventoryCommand:
			w.displayColumns = []int{2, 3}
		case dailyWorkspaceCoverageMode:
			w.displayColumns = []int{0, 1, 3}
		case dailyWorkspaceHistoryMode, dailyWorkspaceActivityMode:
			w.displayColumns = []int{0, 2, 3}
		case dailyWorkspacePinsMode:
			w.displayColumns = []int{2, 3}
		case dailyWorkspaceScopesMode:
			w.displayColumns = []int{1, 2}
		default:
			w.displayColumns = []int{0, 3, 4}
		}
	}
	w.table.Clear()
	palette := w.app.Styles.Semantic()
	canvas := palette.Canvas.Color()
	text := config.ReadableForeground(palette.Text.Color(), canvas)
	for col, original := range w.displayColumns {
		header := headers[original]
		w.table.SetCell(0, col, tview.NewTableCell(header).SetSelectable(false).SetTextColor(palette.Focus.Color()).SetBackgroundColor(canvas).SetAttributes(tcell.AttrBold))
	}
	selectRow := 1
	for i := range rows {
		row := &rows[i]
		for col, original := range w.displayColumns {
			value := row.cells[original]
			cell := tview.NewTableCell(tview.Escape(value)).SetTextColor(text).SetBackgroundColor(canvas)
			if col == 0 && w.mode == dailyWorkspaceQueueMode {
				switch value {
				case "CRITICAL":
					cell.SetTextColor(palette.Failure.Color())
				case "WARNING":
					cell.SetTextColor(palette.Warning.Color())
				default:
					cell.SetTextColor(palette.Unknown.Color())
				}
			}
			if col == 0 && w.mode == dailyWorkspaceCoverageMode {
				if value == dailyWorkspaceCoverageComplete {
					cell.SetTextColor(palette.Healthy.Color())
				} else {
					cell.SetTextColor(palette.Warning.Color())
				}
			}
			if col == len(w.displayColumns)-1 {
				cell.SetMaxWidth(70).SetExpansion(1)
			}
			w.table.SetCell(i+1, col, cell)
		}
		if selected != "" && dailyWorkspaceRowKey(row) == selected {
			selectRow = i + 1
		}
	}
	if len(rows) == 0 {
		// An empty-state message occupies one full-width row, rather than the
		// resource table's narrow identity column.
		w.table.Clear()
		w.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(w.emptyRowsMessage())).SetSelectable(false).
			SetTextColor(palette.Unknown.Color()).SetExpansion(1))
		selectRow = 0
	}
	w.table.Select(selectRow, 0)
	w.renderHeader()
	w.renderDetail()
	w.renderFooter()
}

func (w *dailyWorkspace) emptyRowsMessage() string {
	if w.query != "" {
		return "No rows match this search. / changes or clears it."
	}
	switch w.mode {
	case dailyWorkspaceScopesMode:
		return "No saved scopes. Press n to create your daily workspace."
	case dailyWorkspaceQueueMode:
		if w.snapshot.ObservedAt.IsZero() {
			return "No observation yet. Press r to read this scope."
		}
		return "No findings observed. Coverage shows checks and unknowns."
	case dailyWorkspaceHistoryMode, dailyWorkspaceActivityMode:
		return "No retained history. r observes; reopening begins a new window."
	case inventoryCommand:
		return "No resources observed. r refreshes; Coverage shows gaps."
	case dailyWorkspacePinsMode:
		return "No pins. Select a resource in Daily or Inventory and press p."
	case dailyWorkspaceCoverageMode:
		return "No observation yet. r reads the saved namespaces."
	}
	return "No rows match this search. / changes or clears it."
}

var dailyWorkspaceModes = []string{
	dailyWorkspaceQueueMode, inventoryCommand, dailyWorkspaceCoverageMode, dailyWorkspacePinsMode,
	dailyWorkspaceScopesMode, dailyWorkspaceHistoryMode, dailyWorkspaceActivityMode,
}
var dailyWorkspaceLabels = []string{"Daily", "Inventory", "Coverage", "Pins", "Scopes", "History", "Activity"}

func (w *dailyWorkspace) tabIndex() int {
	for index, mode := range dailyWorkspaceModes {
		if mode == w.mode {
			return index
		}
	}
	return 0
}
func (w *dailyWorkspace) renderHeader() {
	w.header.SetWrap(false)
	width := w.viewportWidth
	if width <= 0 {
		width = 76
	}
	title := "Choose or create a scope · " + w.contextName
	if w.scope.Name != "" {
		title = w.scope.Name + " · " + w.scope.Context
	}
	age := "No observation · r reads scope"
	if !w.snapshot.ObservedAt.IsZero() {
		age = "Observed " + dailyWorkspaceAge(w.snapshot.ObservedAt, time.Now()) + " ago"
	}
	gaps := 0
	for _, coverage := range w.coverage {
		if coverage.State != dailyWorkspaceCoverageComplete && coverage.State != dailyWorkspaceCoverageAbsent {
			gaps++
		}
	}
	status := fmt.Sprintf("%s · %d findings · %d coverage gaps", age, len(w.snapshot.Findings), gaps)
	if width < 70 {
		status = fmt.Sprintf("%s · gaps %d · findings %d", age, gaps, len(w.snapshot.Findings))
	}
	if w.mode == dailyWorkspaceHistoryMode {
		status = w.historyStatus(width)
	}
	if w.mode == dailyWorkspaceActivityMode {
		status = w.activityStatus(width)
	}
	notice := w.notice
	if notice == "" && gaps > 0 {
		notice = "Partial coverage · 3 opens gaps; quiet is not health"
	}
	if notice == "" {
		notice = "Retained scope · v shows exact scope and row"
	}
	if w.query != "" {
		notice = "Search: " + w.query + " · " + notice
	}
	w.SetTitle(" Workspace · " + dailyWorkspaceLabels[w.tabIndex()] + " ")
	w.header.SetText("[::b]" + tview.Escape(fitInvestigation(title, width)) + "[::]\n" +
		tview.Escape(fitInvestigation(status, width)) + "\n" + tview.Escape(fitInvestigation(notice, width)) + "\n" +
		ui.TaskTabs(dailyWorkspaceLabels, w.tabIndex(), width))
}
func (w *dailyWorkspace) renderFooter() {
	width := w.viewportWidth
	if width <= 0 {
		width = 76
	}
	action := "Enter investigate"
	if w.localEvidenceMode() {
		action = "Enter evidence"
	}
	if w.mode == dailyWorkspaceScopesMode {
		action = "Enter open · n new"
	}
	hints := action + " · / search · r refresh · v details · Esc back · ? help"
	if width < 70 {
		hints = action + " · / search · r refresh · ? help"
	}
	if width < 50 {
		hints = "Enter details · / search · ? help"
	}
	w.footer.SetText(tview.Escape(hints))
}
func (w *dailyWorkspace) scopeDetail() string {
	return fmt.Sprintf("Context: %s\nNamespaces: %s\nSelector: %s\nKinds: %s\nCaptured: %s\n",
		w.scope.Context, strings.Join(w.scope.Namespaces, ", "), w.scope.LabelSelector,
		strings.Join(w.scope.Kinds, ", "), fullAt(w.snapshot.ObservedAt))
}
func (w *dailyWorkspace) showRowDetails() {
	row, _ := w.table.GetSelection()
	message := w.scopeDetail()
	if row > 0 && row <= len(w.rows) {
		selected := w.rows[row-1]
		message += "\n" + strings.Join(selected.cells, "\n") + "\n\n" + selected.detail
		if selected.ref != nil {
			message += "\nCaptured UID: " + selected.ref.UID
		}
		if selected.evidence != "" {
			message += "\n\n" + selected.evidence
		}
	}
	const page = "workspace-row-detail"
	var modal *ui.MessageModal
	done := func() { w.app.Content.Pages.RemovePage(page); w.app.SetFocus(w.table) }
	modal = ui.NewMessageModal(w.app.Styles, "Workspace evidence · retained", message, done)
	w.app.Content.Pages.AddPage(page, modal, false, true)
	w.app.Content.Pages.SetPageCleanup(page, modal.Cleanup)
	w.app.SetFocus(modal)
}
func (w *dailyWorkspace) renderDetail() {
	row, _ := w.table.GetSelection()
	if row > 0 && row <= len(w.rows) {
		selected := w.rows[row-1]
		identity := ""
		if w.mode == dailyWorkspaceCoverageMode && len(selected.cells) > 2 {
			identity = selected.cells[1] + " · " + selected.cells[2] + "\n"
		}
		if selected.ref != nil {
			identity = selected.ref.GVR + " · " + selected.ref.Namespace + "/" + selected.ref.Name + "\n"
		}
		w.detail.SetText(tview.Escape(identity + selected.detail))
		return
	}
	if w.mode == dailyWorkspaceHistoryMode || w.mode == dailyWorkspaceActivityMode {
		w.detail.SetText("No retained observations yet. r reads this explicit scope; reopening starts a new window. Prior history is unavailable.")
		return
	}
	w.detail.SetText("Scope observations include workload health, jobs, quotas, PVCs and certificate metadata when enabled. " +
		"Unknown coverage is visible; a quiet queue does not imply full cluster health.")
}
func dailyWorkspaceAge(from, to time.Time) string {
	age := max(time.Duration(0), to.Sub(from))
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

// Adapt column widths at paint time so both a laptop terminal and a wide desk
// keep the status/finding column visible. Full identity stays in the detail bar.
func (w *dailyWorkspace) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, w.Box) {
		return
	}
	_, _, width, _ := w.GetInnerRect()
	if width != w.viewportWidth {
		w.viewportWidth = width
		w.render()
	}
	w.constrainColumns(width)
	w.Flex.Draw(screen)
}
func (w *dailyWorkspace) constrainColumns(width int) {
	if width <= 0 {
		return
	}
	if len(w.rows) == 0 {
		w.table.GetCell(0, 0).SetMaxWidth(width).SetExpansion(1)
		return
	}
	var caps []int
	switch w.mode {
	case inventoryCommand:
		caps = []int{max(8, min(16, width/9)), max(10, min(22, width/7)), max(14, min(32, width/5)), 0, 6}
	case dailyWorkspaceCoverageMode:
		caps = []int{12, max(16, min(40, width/3)), max(10, min(22, width/6)), 0}
	case dailyWorkspaceHistoryMode, dailyWorkspaceActivityMode:
		caps = []int{16, 8, max(12, min(28, width/4)), 0}
	case dailyWorkspacePinsMode:
		caps = []int{max(16, min(36, width/4)), max(10, min(22, width/6)), max(14, min(32, width/5)), 0}
	case dailyWorkspaceScopesMode:
		caps = []int{1, max(14, min(24, width/5)), max(14, min(32, width/5)), max(14, min(28, width/5)), 0}
	default:
		caps = []int{8, max(8, min(16, width/9)), max(10, min(22, width/7)), max(14, min(32, width/5)), 0}
	}
	if width < 80 {
		switch w.mode {
		case dailyWorkspaceCoverageMode:
			caps = []int{10, min(18, width/3), 0}
		case dailyWorkspaceHistoryMode, dailyWorkspaceActivityMode:
			caps = []int{12, min(14, max(6, width-30)), 0}
			if width < 50 {
				caps = []int{11, 6, 0}
			}
		case dailyWorkspaceQueueMode:
			caps = []int{8, min(14, max(6, width-30)), 0}
		default:
			caps = []int{min(18, width/3), 0}
		}
	}
	available := width - (len(caps)-1)*2
	for _, cap := range caps {
		available -= cap
	}
	for col, cap := range caps {
		if cap == 0 {
			cap = max(8, available)
		}
		for row := range w.table.GetRowCount() {
			cell := w.table.GetCell(row, col)
			if cell != nil {
				cell.SetMaxWidth(cap)
			}
		}
	}
}
