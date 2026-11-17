// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const dashboardsTitle = "Dashboards"

// Row origins. The word is shown, so the meaning never rests on color.
const (
	originDeclared = "declared"
	originExplore  = "explore"
	originSearch   = "search"
)

// dashboardRow is one line of the list: a declared dashboard, an Explore
// link, or a dashboard Grafana's search returned.
type dashboardRow struct {
	Origin, For, Title, UID, Folder string
	Tags                            []string
	Link                            grafanaLink
}

// dashboardWords are the lower-case words a search hit is ranked by: the
// kind, the namespace and the name parts of the selection. Ranking only
// orders the list; it claims nothing about a dashboard.
func dashboardWords(t *grafanaTarget) []string {
	words := []string{t.Kind, t.Link.Namespace, t.Link.Node}
	for _, name := range []string{t.Link.Workload, t.Link.Pod} {
		for _, part := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '.' || r == '_' }) {
			if len(part) >= 4 && !strings.ContainsAny(part, "0123456789") {
				words = append(words, part)
			}
		}
	}
	var out []string
	for _, w := range words {
		if w = strings.ToLower(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

func dashboardScore(hit *observability.DashboardHit, words []string) int {
	text := strings.ToLower(hit.Title + " " + hit.FolderTitle + " " + strings.Join(hit.Tags, " "))
	score := 0
	for _, w := range words {
		if strings.Contains(text, w) {
			score++
		}
	}
	return score
}

// dashboardRows orders the list: the dashboard declared for the selected
// kind, the other declared ones, the Explore links, then search results
// that are not declared, those sharing a word with the selection first.
func dashboardRows(g *observability.GrafanaSource, t *grafanaTarget, hits []observability.DashboardHit) []dashboardRow {
	byUID := make(map[string]*observability.DashboardHit, len(hits))
	for i := range hits {
		byUID[hits[i].UID] = &hits[i]
	}
	kinds := make([]string, 0, len(observability.DashboardKinds))
	if _, ok := g.Dashboards[t.Kind]; ok {
		kinds = append(kinds, t.Kind)
	}
	for _, kind := range observability.DashboardKinds {
		if _, ok := g.Dashboards[kind]; ok && kind != t.Kind {
			kinds = append(kinds, kind)
		}
	}
	var rows []dashboardRow
	declared := make(map[string]bool)
	for _, kind := range kinds {
		d := g.Dashboards[kind]
		declared[d.UID] = true
		row := dashboardRow{Origin: originDeclared, For: kind, Title: d.UID, UID: d.UID, Link: dashboardLink(g, kind, t)}
		if hit := byUID[d.UID]; hit != nil {
			row.Title, row.Folder, row.Tags = hit.Title, hit.FolderTitle, hit.Tags
		}
		rows = append(rows, row)
	}
	if t.Kind != "" {
		for _, link := range exploreLinks(g, t) {
			row := dashboardRow{Origin: originExplore, For: strings.TrimPrefix(link.Name, "Explore "), Title: link.Detail, Link: link}
			if link.URL == "" {
				row.Title = "unavailable: " + link.Unavailable
			}
			rows = append(rows, row)
		}
	}
	words := dashboardWords(t)
	found := make([]*observability.DashboardHit, 0, len(hits))
	for i := range hits {
		if !declared[hits[i].UID] {
			found = append(found, &hits[i])
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return dashboardScore(found[i], words) > dashboardScore(found[j], words) })
	for _, hit := range found {
		rows = append(rows, dashboardRow{Origin: originSearch, For: "-", Title: hit.Title, UID: hit.UID, Folder: hit.FolderTitle,
			Tags: hit.Tags, Link: searchLink(g, hit, t)})
	}
	return rows
}

func (r *dashboardRow) searchText() string {
	return strings.ToLower(strings.Join([]string{r.Origin, r.For, r.Title, r.UID, r.Folder, strings.Join(r.Tags, " ")}, " "))
}

// dashboardsView is the on-demand dashboard list. Opening it makes one
// bounded Grafana search; nothing is read again until r is pressed.
type dashboardsView struct {
	*Details
	target                 *grafanaTarget
	grafana                *observability.GrafanaSource
	sourceLabel            string
	query, notice, filter  string
	contextName            string
	revision               uint64
	search                 func(context.Context) observability.DashboardsResult
	result                 *observability.DashboardsResult
	rows                   []dashboardRow
	visible                []int
	lines                  []string
	cancel                 context.CancelFunc
	generation             uint64
	active, loading        bool
	table                  *tview.Table
	header, detail, footer *tview.TextView
	body                   *tview.Flex
	width, height, detailH int
}

func newDashboardsView(app *App, target *grafanaTarget, query, notice string) *dashboardsView {
	sources := declaredSources(app)
	grafana := *sources.Grafana
	v := &dashboardsView{
		Details: NewDetails(app, dashboardsTitle, target.Label, contentTXT, false),
		target:  target, grafana: &grafana, sourceLabel: grafana.Display(), query: observability.Clean(query, 200), notice: notice,
		contextName: app.Config.ActiveContextName(), revision: app.Config.DestinationRevision(),
	}
	restConfig := kubeRESTConfig(app)
	v.search = func(ctx context.Context) observability.DashboardsResult {
		client, st := observability.Resolve(sources, observability.KindGrafana, restConfig)
		if client == nil {
			return observability.DashboardsResult{Status: st}
		}
		defer client.Close()
		return client.SearchDashboards(ctx, v.query, nil, 0)
	}
	return v
}

func (*dashboardsView) CompactWorkspace() bool { return true }

func (v *dashboardsView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.cmdBuff.RemoveListener(v.Details)
	v.cmdBuff.AddListener(v)
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.table.SetSelectionChangedFunc(func(int, int) {
		v.markSelection()
		v.renderDetail()
	})
	v.detailH = 6
	v.body = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(v.table, 0, 1, true).AddItem(v.detail, v.detailH, 0, false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.header, 4, 0, false).AddItem(v.body, 0, 1, true).AddItem(v.footer, 1, 0, false)
	v.bindDashboardKeys()
	v.rebuild()
	return nil
}

func (v *dashboardsView) bindDashboardKeys() {
	v.actions.Delete(ui.KeyC, ui.KeyF, tcell.KeyCtrlS)
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Re-read Grafana search", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.actions.Add(tcell.KeyEnter, ui.NewKeyAction("Open or copy link", func(*tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			v.applyFilter(v.cmdBuff.GetText())
			v.cmdBuff.SetActive(false)
			return nil
		}
		v.openRow()
		return nil
	}, true))
	v.actions.Add(tcell.KeyEscape, ui.NewKeyAction("Back", v.escape, false))
	v.actions.Add(ui.KeyQ, ui.NewKeyAction("Back", v.escape, false))
}

func (v *dashboardsView) escape(event *tcell.EventKey) *tcell.EventKey {
	switch {
	case v.cmdBuff.InCmdMode():
		if event.Key() != tcell.KeyEscape {
			return event
		}
		v.cmdBuff.SetActive(false)
		v.cmdBuff.Reset()
		v.applyFilter("")
	case v.filter != "":
		v.cmdBuff.Reset()
		v.applyFilter("")
	default:
		v.cmdBuff.Reset()
		return v.app.PrevCmd(event)
	}
	return nil
}

// BufferCompleted applies the filter on the UI loop.
func (v *dashboardsView) BufferCompleted(text, _ string) {
	if v.app.lifecycle.requested.Load() {
		return
	}
	if !v.app.IsRunning() {
		v.applyFilter(text)
		return
	}
	v.app.QueueUpdateDraw(func() {
		if v.active && v.app.Content.Top() == v && text == v.cmdBuff.GetText() {
			v.applyFilter(text)
		}
	})
}

func (v *dashboardsView) applyFilter(text string) {
	if text == v.filter {
		return
	}
	v.filter = text
	v.render()
}

func (v *dashboardsView) selectedRow() *dashboardRow {
	if v.table == nil {
		return nil
	}
	row, _ := v.table.GetSelection()
	if row < 1 || row > len(v.visible) {
		return nil
	}
	return &v.rows[v.visible[row-1]]
}

func (v *dashboardsView) openRow() {
	row := v.selectedRow()
	if row == nil {
		return
	}
	showGrafanaHandoff(v.app, &grafanaHandoff{target: v.target, links: []grafanaLink{row.Link}, deliverNow: true})
}

func (v *dashboardsView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.result == nil && !v.loading {
		v.refresh()
	}
}

func (v *dashboardsView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *dashboardsView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.table != nil {
		p := styles.Semantic()
		v.table.SetBackgroundColor(p.Canvas.Color())
		v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(p.Text.Color()).Bold(true))
		for _, item := range []*tview.TextView{v.header, v.footer, v.detail} {
			item.SetBackgroundColor(p.Panel.Color())
			item.SetTextColor(p.Text.Color())
		}
	}
	v.render()
}

func (v *dashboardsView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		detail := 6
		if height < 18 {
			detail = 4
		}
		if detail != v.detailH {
			v.detailH = detail
			v.body.ResizeItem(v.detail, detail, 0)
		}
		v.render()
	}
	v.Flex.Draw(screen)
}

func (v *dashboardsView) current() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}

// refresh makes one bounded, cancellable search off the UI thread. A reply
// that arrives after the view closed or the destination changed is dropped.
func (v *dashboardsView) refresh() {
	if !v.current() || v.search == nil {
		v.render()
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), 2*observability.DefaultTimeout)
	v.cancel, v.loading = cancel, true
	v.renderHeader()
	search := v.search
	go func() {
		defer cancel()
		result := search(ctx)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && generation == v.generation && v.current() {
				v.accept(&result)
			}
		})
	}()
}

func (v *dashboardsView) accept(result *observability.DashboardsResult) {
	v.loading = false
	if result.ObservedAt.IsZero() {
		result.ObservedAt = time.Now().UTC()
	}
	v.result = result
	v.rebuild()
}

// rebuild keeps the selection on the same row across a re-read.
func (v *dashboardsView) rebuild() {
	var hits []observability.DashboardHit
	if v.result != nil && v.result.OK() {
		hits = v.result.Dashboards
	}
	v.rows = dashboardRows(v.grafana, v.target, hits)
	v.render()
}

func (v *dashboardsView) viewWidth() int {
	if v.width <= 0 {
		return 80
	}
	return v.width
}

func (v *dashboardsView) render() {
	if v.header == nil {
		return
	}
	v.renderRows()
	v.renderHeader()
	v.renderDetail()
	width := v.viewWidth()
	footer := "Enter open or copy the link · / filter · r re-read search · Esc back"
	if runewidth.StringWidth(footer) > width {
		footer = "Enter open/copy · / filter · r re-read · Esc back"
	}
	v.footer.SetText(tview.Escape(ui.Truncate(footer, width)))
}

// searchLine states what the one search established.
func (v *dashboardsView) searchLine() (string, config.Color) {
	p := v.app.Styles.Semantic()
	if !v.current() {
		return "Destination changed; this list is retained. Reopen :dashboards for the current context.", p.Warning
	}
	if v.loading {
		return "Reading Grafana dashboard search once: bounded, read-only...", p.Progress
	}
	r := v.result
	if r == nil {
		return "Grafana search NOT READ yet · r reads once", p.Muted
	}
	at := r.ObservedAt.UTC().Format("15:04:05Z")
	scope := ""
	if v.query != "" {
		scope = fmt.Sprintf(" with title containing %q", v.query)
	}
	switch r.State {
	case observability.StateComplete:
		return fmt.Sprintf("Grafana search: %d dashboards read%s at %s via %s", len(r.Dashboards), scope, at, v.sourceLabel), p.Text
	case observability.StateIncomplete:
		return fmt.Sprintf("INCOMPLETE: first %d dashboards read%s at %s; more exist · :dashboards <title words> narrows", len(r.Dashboards), scope, at), p.Warning
	}
	reason := string(r.State)
	if r.Reason != "" {
		reason += " (" + r.Reason + ")"
	}
	return fmt.Sprintf("NOT READ: search %s %s · see :sources · declared links still work", reason, at), p.Failure
}

func (v *dashboardsView) renderHeader() {
	if v.header == nil {
		return
	}
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	title := dashboardsTitle + " / " + v.target.Label
	if v.target.Note != "" {
		title += " (" + v.target.Note + ")"
	}
	search, searchColor := v.searchLine()
	last, lastColor := "Links open at "+observability.Clean(v.grafana.PublicURL, 200)+" · nothing is opened until Enter", p.Muted
	if v.notice != "" {
		last, lastColor = v.notice, p.Warning
	}
	if v.filter != "" {
		last, lastColor = fmt.Sprintf("Filter %q: %d of %d rows · Esc clears", v.filter, len(v.visible), len(v.rows)), p.Focus
	}
	lines := []string{title, "Window " + v.target.Window.String(), search, last}
	colors := []config.Color{p.Focus, p.Text, searchColor, lastColor}
	for i, line := range lines {
		lines[i] = timelineTag(colors[i], i == 0) + tview.Escape(ui.Truncate(safeProviderText(line), width)) + "[-:-:-]"
	}
	v.header.SetText(strings.Join(lines, "\n"))
}

func (v *dashboardsView) renderRows() {
	if v.table == nil {
		return
	}
	selected := ""
	if row := v.selectedRow(); row != nil {
		selected = row.Origin + "/" + row.For + "/" + row.UID
	}
	v.table.Clear()
	v.visible = v.visible[:0]
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	filter := strings.ToLower(strings.TrimSpace(v.filter))
	for i := range v.rows {
		if filter == "" || strings.Contains(v.rows[i].searchText(), filter) {
			v.visible = append(v.visible, i)
		}
	}
	const originWidth, forWidth = 8, 9
	uidWidth, folderWidth := 0, 0
	if width >= 100 {
		uidWidth = 16
	}
	if width >= 116 {
		folderWidth = 14
	}
	titleWidth := max(width-(2+originWidth+1+forWidth+1), 10)
	if uidWidth > 0 {
		titleWidth -= uidWidth + 1
	}
	if folderWidth > 0 {
		titleWidth -= folderWidth + 1
	}
	heading := "  " + timelineCell("ORIGIN", originWidth) + " " + timelineCell("FOR", forWidth) + " " + timelineCell("DASHBOARD OR QUERY", titleWidth)
	if folderWidth > 0 {
		heading += " " + timelineCell("FOLDER", folderWidth)
	}
	if uidWidth > 0 {
		heading += " " + timelineCell("UID", uidWidth)
	}
	v.lines = v.lines[:0]
	v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(heading)).SetSelectable(false).SetTextColor(p.Focus.Color()).SetAttributes(tcell.AttrBold))
	if len(v.visible) == 0 {
		text := "No dashboard is declared and none was read."
		if filter != "" {
			text = "No row matches the filter. Esc clears it."
		}
		v.table.SetCell(1, 0, tview.NewTableCell(tview.Escape(timelineCell(text, width))).SetSelectable(false).SetTextColor(p.Muted.Color()))
		return
	}
	selectedRow := 1
	for index, at := range v.visible {
		row := &v.rows[at]
		if row.Origin+"/"+row.For+"/"+row.UID == selected {
			selectedRow = index + 1
		}
		title := row.Title
		if row.Origin == originSearch && row.Folder != "" && folderWidth == 0 {
			title = row.Title + " (folder " + row.Folder + ")"
		}
		color := p.Text
		switch {
		case row.Link.URL == "":
			color = p.Muted
		case row.Origin == originDeclared:
			color = p.Healthy
		case row.Origin == originExplore:
			color = p.Progress
		}
		line := timelineTag(color, false) + tview.Escape(timelineCell(row.Origin, originWidth)+" "+timelineCell(row.For, forWidth)+" ") +
			timelineTag(p.Text, false)
		if row.Origin == originExplore {
			// A query has no folder or UID, so it takes the whole row.
			line += tview.Escape(timelineCell(safeProviderText(title), width-(2+originWidth+1+forWidth+1)))
		} else {
			line += tview.Escape(timelineCell(safeProviderText(title), titleWidth))
			if folderWidth > 0 {
				line += tview.Escape(" " + timelineCell(safeProviderText(row.Folder), folderWidth))
			}
			if uidWidth > 0 {
				line += tview.Escape(" " + timelineCell(row.UID, uidWidth))
			}
		}
		v.lines = append(v.lines, line+"[-:-:-]")
		v.table.SetCell(index+1, 0, tview.NewTableCell("  "+line+"[-:-:-]").SetExpansion(1))
	}
	v.table.Select(selectedRow, 0)
	v.table.SetOffset(v.table.GetOffset())
	v.markSelection()
}

// markSelection puts a cursor mark on the selected row, so the selection
// does not depend on the highlight color.
func (v *dashboardsView) markSelection() {
	selected, _ := v.table.GetSelection()
	for index, line := range v.lines {
		mark := "  "
		if index+1 == selected {
			mark = "> "
		}
		if cell := v.table.GetCell(index+1, 0); cell != nil {
			cell.SetText(mark + line)
		}
	}
}

func (v *dashboardsView) renderDetail() {
	if v.detail == nil {
		return
	}
	row := v.selectedRow()
	if row == nil {
		v.detail.SetText("")
		return
	}
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	head := row.Title
	if row.Origin == originExplore {
		head = row.Link.Name
		if row.Link.Detail != "" {
			head += " · " + row.Link.Detail
		}
	} else {
		if row.Folder != "" {
			head += " · folder " + row.Folder
		}
		if len(row.Tags) > 0 {
			head += " · tags " + strings.Join(row.Tags, ", ")
		}
	}
	lines := []string{timelineTag(p.Focus, true) + tview.Escape(ui.Truncate(safeProviderText(head), width)) + "[-:-:-]"}
	switch {
	case row.Link.URL == "":
		for _, text := range wrapText(row.Link.Unavailable, width) {
			lines = append(lines, timelineTag(p.Warning, false)+text+"[-:-:-]")
		}
	case row.Origin == originSearch:
		note := "Not declared: no variable is filled."
		if v.target.Kind != "" {
			note += " Enter shows the YAML that pins it as the " + v.target.Kind + " dashboard."
		}
		for _, text := range wrapText(note, width) {
			lines = append(lines, timelineTag(p.Muted, false)+text+"[-:-:-]")
		}
	default:
		lines = append(lines, timelineTag(p.Muted, false)+tview.Escape(ui.Truncate("Fills "+row.Link.Fills, width))+"[-:-:-]")
	}
	if row.Link.URL != "" {
		// The pane shows as much of the link as fits; Enter shows all of it.
		room := max(v.detailH-len(lines), 1) * width
		for _, text := range hardWrap(runewidth.Truncate(row.Link.URL, room, "…"), width) {
			lines = append(lines, tview.Escape(text))
		}
	}
	v.detail.SetText(strings.Join(lines, "\n"))
	v.detail.ScrollToBeginning()
}
