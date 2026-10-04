// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	investigationEvidenceTab = 4
	investigationAppRole     = "app"
)

var investigationTabs = []string{"Overview", "Containers", "Events", "Resources", "Evidence"}

type investigationTabState struct {
	query            string
	region, row, col int
}

func (d *inspectionDetails) CompactWorkspace() bool {
	return d.title == troubleshootCommand || d.title == pressureCommand
}

func (d *inspectionDetails) initInvestigationTabs() {
	d.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	d.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	d.Flex.Clear().SetDirection(tview.FlexRow).AddItem(d.identityBar, 3, 0, false).AddItem(d.tabsBar, 1, 0, false).AddItem(d.text, 0, 1, true)
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		d.actions.Add(key, ui.NewKeyAction(investigationTabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if d.cmdBuff.IsActive() {
				return event
			}
			d.selectInvestigationTab(tab)
			return nil
		}, true))
	}
	d.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next investigation tab", func(event *tcell.EventKey) *tcell.EventKey {
		if d.cmdBuff.IsActive() {
			return event
		}
		d.selectInvestigationTab((d.activeTab + 1) % len(investigationTabs))
		return nil
	}, true))
	d.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous investigation tab", func(event *tcell.EventKey) *tcell.EventKey {
		if d.cmdBuff.IsActive() {
			return event
		}
		d.selectInvestigationTab((d.activeTab + len(investigationTabs) - 1) % len(investigationTabs))
		return nil
	}, true))
	d.actions.Add(ui.Key6, ui.NewKeyAction("Changes: choose comparison A", func(event *tcell.EventKey) *tcell.EventKey {
		if d.cmdBuff.IsActive() {
			return event
		}
		d.app.openResourceComparison(d.target)
		return nil
	}, true))
	logs := ui.NewKeyActionWithOpts("Open Pod logs", func(event *tcell.EventKey) *tcell.EventKey {
		if d.cmdBuff.IsActive() {
			return event
		}
		if reason := d.podLogsAvailability(); reason != "" {
			d.app.Flash().Warn(reason)
			return nil
		}
		if err := d.app.dailyWorkspaceOpenLogs(d.target); err != nil {
			d.app.Flash().Err(err)
		}
		return nil
	}, ui.ActionOpts{Visible: true, RequiresSelection: true})
	logs.ID = "inspection.pod-logs"
	logs.Category = ui.ActionNavigate
	logs.Availability = d.podLogsAvailability
	d.actions.Add(ui.KeyL, logs)
	d.renderInvestigationChrome()
}

func (d *inspectionDetails) podLogsAvailability() string {
	if err := d.target.Err(); err != nil {
		return err.Error()
	}
	if d.target.GVR != client.PodGVR {
		return "Select a Pod to open logs; use related resources to choose a workload Pod"
	}
	if d.target.Context != d.app.Config.ActiveContextName() {
		return "Context changed; reopen the selected Pod in its context"
	}
	return ""
}

func (d *inspectionDetails) selectInvestigationTab(tab int) {
	if tab < 0 || tab >= len(investigationTabs) || tab == d.activeTab {
		return
	}
	row, col := d.text.GetScrollOffset()
	d.tabStates[d.activeTab] = investigationTabState{query: d.inspectionQuery, region: d.currentRegion, row: row, col: col}
	d.activeTab = tab
	d.text.SetWrap(tab == investigationEvidenceTab)
	state := d.tabStates[tab]
	d.inspectionQuery, d.currentRegion = state.query, state.region
	d.cmdBuff.SetText(state.query, "", true)
	d.currentRegion = state.region
	d.text.ScrollTo(state.row, state.col)
	d.renderSnapshotText(d.displayEvidence)
}

// Draw reacts to viewport width without changing retained observations,
// accepted search, tab choice or scroll position.
func (d *inspectionDetails) Draw(screen tcell.Screen) {
	if d.CompactWorkspace() && ui.DrawTaskSizeNotice(screen, d.Box) {
		return
	}
	_, _, width, height := d.GetInnerRect()
	if d.snapshot.Investigation != nil && d.CompactWorkspace() {
		identityRows := 3
		if height < 16 {
			identityRows = 1
		}
		d.Flex.ResizeItem(d.identityBar, identityRows, 0)
		d.text.SetWrap(d.activeTab == investigationEvidenceTab)
		if width != d.overviewWidth {
			d.overviewWidth = width
			d.renderSnapshotText(d.displayEvidence)
		}
		d.renderInvestigationChrome()
	}
	d.Flex.Draw(screen)
}

func (d *inspectionDetails) renderInvestigationChrome() {
	if d.identityBar == nil {
		return
	}
	p := d.app.Styles.Semantic()
	for _, view := range []*tview.TextView{d.identityBar, d.tabsBar} {
		view.SetBackgroundColor(p.Panel.Color())
		view.SetTextColor(p.Text.Color())
	}
	width := d.investigationWidth()
	i := d.snapshot.Investigation
	if i == nil {
		d.identityBar.SetText(wbText("Loading retained investigation · " + d.target.Path()))
	} else {
		uid := i.Identity.UID
		if uid == "" {
			uid = workspaceUnknown
		}
		if len(uid) > 20 {
			uid = uid[:8] + "…" + uid[len(uid)-8:]
		}
		age := investigationAge(i.CapturedAt, time.Now())
		path := i.Identity.Name
		if i.Identity.Namespace != "" {
			path = i.Identity.Namespace + "/" + path
		}
		first := fmt.Sprintf("%s %s · %s", i.Kind, path, i.Identity.Context)
		if i.Node != "" {
			first += " · node " + i.Node
		}
		second := fmt.Sprintf("UID %s · captured %s · age %s", uid, i.CapturedAt.UTC().Format("15:04:05Z"), age)
		third := "READ ONLY · API observation · r refresh · g related · 6 changes"
		if d.target.GVR == client.PodGVR {
			third = "READ ONLY · r refresh · l Pod logs · g related · 6 changes"
		}
		if d.refreshFailure != "" {
			third = "[~] Refresh failed; retaining original observation · " + d.refreshFailure
		}
		d.identityBar.SetText(detailStyled(p.Focus.String(), "b", fitInvestigation(first, width)) + "\n" +
			detailStyled(p.Muted.String(), "", fitInvestigation(second, width)) + "\n" +
			detailStyled(p.Warning.String(), "", fitInvestigation(third, width)))
	}
	d.tabsBar.SetText(ui.TaskTabs(investigationTabs, d.activeTab, width))
}

func (d *inspectionDetails) investigationWidth() int {
	if d.overviewWidth > 0 {
		return d.overviewWidth
	}
	return 76
}

func (d *inspectionDetails) investigationTabText() string {
	i := d.snapshot.Investigation
	if i == nil {
		return d.displayEvidence
	}
	width := d.investigationWidth()
	switch d.activeTab {
	case 1:
		return investigationContainers(i, width)
	case 2:
		return investigationEvents(i, width)
	case 3:
		return investigationResources(i, width, false)
	default:
		if d.title == pressureCommand {
			return investigationResources(i, width, true)
		}
		return investigationOverview(i, width)
	}
}

func investigationOverview(i *inspect.Investigation, width int) string {
	var b strings.Builder
	b.WriteString("CURRENT FINDINGS\n")
	issues := 0
	for index := range i.Containers {
		c := &i.Containers[index]
		if c.CurrentIssue() {
			issues++
		}
	}
	if issues == 0 {
		for _, c := range i.Conditions {
			if c.Adverse {
				issues++
				fmt.Fprintln(&b, fitInvestigation("Current: "+c.Type+"="+c.Status+" · "+c.Reason, width))
				if issues >= 2 {
					break
				}
			}
		}
	}
	if issues == 0 {
		fmt.Fprintln(&b, "[?] No current fault established by these sources.")
	}
	if len(i.Containers) > 0 {
		b.WriteString(investigationCurrentTable(i, width, 4))
		for index := range min(len(i.Containers), 4) {
			c := &i.Containers[index]
			if c.LastTermination != nil {
				previous := "Previous termination: " + c.LastTermination.Label() + " · " + c.Name
				if width < 50 {
					label := c.LastTermination.Reason
					if c.LastTermination.ExitCode != nil {
						label += fmt.Sprintf("/%d", *c.LastTermination.ExitCode)
					}
					previous = "Previous: " + label + " · " + c.Name
				}
				fmt.Fprintln(&b, fitInvestigation(previous, width))
			}
		}
	} else {
		for _, c := range i.Conditions[:min(len(i.Conditions), 3)] {
			fmt.Fprintln(&b, fitInvestigation(c.Type+"="+c.Status+" · "+c.Reason, width))
		}
		if len(i.Conditions) == 0 {
			b.WriteString("[?] Conditions unavailable; absence is not health.\n")
		}
	}
	var gaps []string
	for _, coverage := range i.Coverage {
		if coverage.State != inspect.ObservationComplete {
			gaps = append(gaps, coverage.Source+": "+coverage.State)
		}
	}
	if width < 50 {
		next := "NEXT CHECKS: g Pod · 2 containers · 5"
		if i.Identity.GVR == client.PodGVR.String() {
			next = "NEXT CHECKS: l logs · 2 containers · 5"
		}
		fmt.Fprintln(&b, fitInvestigation(next, width))
		gap := "Sources collected; 5 full evidence"
		if len(gaps) > 0 {
			gap = "Unknown coverage · 5 full evidence"
			for _, item := range gaps {
				if strings.HasPrefix(item, "metrics:") {
					gap = item + " · 5 all gaps"
					break
				}
			}
		}
		fmt.Fprintln(&b, fitInvestigation(gap, width))
		b.WriteString("Past exit historical; cause unknown.\n")
		return b.String()
	}
	if len(gaps) > 0 {
		b.WriteString("VISIBILITY GAPS · 5 full evidence\n")
		for _, gap := range gaps[:min(2, len(gaps))] {
			fmt.Fprintln(&b, fitInvestigation("[?] "+gap, width))
		}
		if len(gaps) > 2 {
			fmt.Fprintf(&b, "%d more gaps in 5 Evidence\n", len(gaps)-2)
		}
	} else {
		b.WriteString("Sources collected; events remain retained history.\n")
	}
	b.WriteString("NEXT CHECKS · proposed, cause unconfirmed\n")
	if i.Identity.GVR == client.PodGVR.String() {
		b.WriteString("l Pod logs · 2 containers · 3 events\n")
	} else {
		b.WriteString("g related Pod · 2 containers · 3 events\n")
	}
	b.WriteString("5 evidence · :pressure budgets · 6 compare\n")
	b.WriteString("Previous exit is historical; current cause unknown.\n")
	return b.String()
}

func investigationCurrentTable(i *inspect.Investigation, width, limit int) string {
	var b strings.Builder
	widths := []int{min(18, max(7, width-39)), min(25, max(17, width-30)), 5, 7}
	if width < 50 {
		widths = []int{max(7, width-32), 17, 5, 7}
	}
	// Preserve the complete fault state before secondary identity characters.
	for total := widths[0] + widths[1] + widths[2] + widths[3] + 3; total > width; total-- {
		if widths[0] > 3 {
			widths[0]--
		} else if widths[3] > 2 {
			widths[3]--
		} else {
			break
		}
	}
	headers := []string{"CONTAINER", "CURRENT", "READY", "RESTART"}
	if width < 50 {
		headers = []string{"NAME", "CURRENT", "RDY", "R"}
	}
	fmt.Fprintln(&b, tableRow(headers, widths))
	for index := range min(len(i.Containers), limit) {
		c := &i.Containers[index]
		ready, restarts := investigationReady(c), "?"
		if c.Restarts != nil {
			restarts = fmt.Sprint(*c.Restarts)
		}
		fmt.Fprintln(&b, tableRow([]string{c.Name, c.CurrentLabel(), ready, restarts}, widths))
	}
	if len(i.Containers) > limit {
		fmt.Fprintf(&b, "%d more; 2 full containers\n", len(i.Containers)-limit)
	}
	return b.String()
}

func investigationReady(c *inspect.InvestigationContainer) string {
	if c.Ready == nil {
		return "?"
	}
	if *c.Ready {
		return "yes"
	}
	return "no"
}

func investigationContainerTable(i *inspect.Investigation, width, limit int) string {
	// Previous exits are separate historical rows at narrow widths, rather than
	// wrapping a record into what looks like another container.
	if width < 80 {
		var b strings.Builder
		b.WriteString(investigationCurrentTable(i, width, limit))
		for index := range min(len(i.Containers), limit) {
			c := &i.Containers[index]
			previous := "none reported"
			if c.LastTermination != nil {
				previous = c.LastTermination.Label()
			}
			fmt.Fprintln(&b, fitInvestigation("Previous · "+c.Name+": "+previous, width))
		}
		return b.String()
	}
	var b strings.Builder
	widths := []int{18, 25, 7, 7, max(8, width-61)}
	fmt.Fprintln(&b, tableRow([]string{"CONTAINER", "CURRENT", "READY", "RESTART", "PREVIOUS"}, widths))
	for index := range min(len(i.Containers), limit) {
		c := &i.Containers[index]
		ready, restarts := investigationReady(c), "?"
		if c.Restarts != nil {
			restarts = fmt.Sprint(*c.Restarts)
		}
		previous := "none reported"
		if c.LastTermination != nil {
			previous = c.LastTermination.Reason
		}
		fmt.Fprintln(&b, tableRow([]string{c.Name, c.CurrentLabel(), ready, restarts, previous}, widths))
	}
	if len(i.Containers) == 0 {
		b.WriteString("[?] No container statuses reported.\n")
	}
	return b.String()
}

func investigationContainers(i *inspect.Investigation, width int) string {
	var b strings.Builder
	b.WriteString("CURRENT CONTAINERS · Pod API status\n")
	b.WriteString(investigationContainerTable(i, width, len(i.Containers)))
	for index := range i.Containers {
		c := &i.Containers[index]
		fmt.Fprintf(&b, "\n%s / %s · %s\nCurrent: %s\n", c.Pod, c.Name, c.Role, c.CurrentLabel())
		if c.Message != "" {
			b.WriteString(inspectionMessage(c.Message))
		}
		if c.CurrentTermination != nil {
			fmt.Fprintf(&b, "Current termination: %s · finished %s\n", c.CurrentTermination.Label(), fullAt(c.CurrentTermination.FinishedAt))
		}
		if c.LastTermination != nil {
			fmt.Fprintf(&b, "Previous termination: %s · finished %s\n", c.LastTermination.Label(), fullAt(c.LastTermination.FinishedAt))
			b.WriteString(inspectionMessage(c.LastTermination.Message))
		} else {
			b.WriteString("Previous termination: not reported\n")
		}
	}
	b.WriteString("\nPrevious termination does not establish the current cause.\n" +
		"Logs are not collected; l opens logs for a selected Pod.\nWorkloads: g opens related resources to choose a Pod.\n")
	return b.String()
}

func investigationEvents(i *inspect.Investigation, width int) string {
	var b strings.Builder
	b.WriteString("RETAINED EVENTS\nUID-scoped source records · latest first · incomplete history\n\n")
	if len(i.Events) == 0 {
		b.WriteString("[?] No retained event records obtained; absence is not health.\n")
	}
	for _, e := range i.Events {
		fmt.Fprintln(&b, fitInvestigation(fmt.Sprintf("%s  %s  %s  count=%d", relativeAt(e.LastObserved, i.CapturedAt), e.Type, e.Reason, e.Count), width))
		b.WriteString(inspectionMessage(e.Message))
		fmt.Fprintf(&b, "  Source: %s · UID %s · observed %s\n", e.Source, e.UID, fullAt(e.LastObserved))
	}
	for _, c := range i.Coverage {
		if strings.HasPrefix(c.Source, "events") {
			fmt.Fprintf(&b, "\nVisibility: %s · %s\n", c.State, c.Detail)
		}
	}
	return b.String()
}

func investigationResources(i *inspect.Investigation, width int, overview bool) string {
	var b strings.Builder
	b.WriteString("RESOURCE BUDGETS · current Pod spec / sampled usage\n")
	if len(i.Resources) == 0 {
		b.WriteString("[?] No container resource configuration obtained.\n")
	}
	for index := range i.Resources {
		r := &i.Resources[index]
		fmt.Fprintln(&b, fitInvestigation(r.Pod+" / "+r.Container+" · "+r.Role, width))
		columns := []int{10, 12, 12, 12, max(12, width-54)}
		fmt.Fprintln(&b, tableRow([]string{"RESOURCE", "REQUEST", "LIMIT", "USAGE", "USE / LIMIT"}, columns))
		fmt.Fprintln(&b, tableRow([]string{"CPU", r.CPURequest, r.CPULimit, r.CPUUsage, orNA(r.CPULimitRatio)}, columns))
		fmt.Fprintln(&b, tableRow([]string{"Memory", r.MemoryRequest, r.MemoryLimit, r.MemoryUsage, orNA(r.MemoryLimitRatio)}, columns))
		fmt.Fprintln(&b, fitInvestigation("Metrics: "+r.MetricsState+" · observed "+fullAt(r.ObservedAt)+" · window "+windowLabel(r.Window), width))
		if r.MetricsReason != "" {
			fmt.Fprintln(&b, fitInvestigation("[?] "+r.MetricsReason, width))
		}
		if !overview {
			fmt.Fprintf(&b, "UID: %s\n", r.UID)
		}
		b.WriteByte('\n')
	}
	b.WriteString("N/A is not zero · CPU: millicores · memory: MiB when sampled\n" +
		"CPU throttling: unknown; metrics-server has no throttling counters.\n" +
		"Current spec may differ from configuration at prior termination.\n5 evidence: full sources, caveats, retained messages\n")
	return b.String()
}

func investigationWorkloadText(pods []*unstructured.Unstructured, notice string) string {
	if len(pods) == 0 && notice == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nWORKLOAD PODS (selector matches; %d observed in this snapshot)\n", len(pods))
	for _, pod := range pods {
		b.WriteString(resourceSummaryAt(pod, time.Now()))
	}
	if notice != "" {
		b.WriteString(notice + "\n")
	}
	b.WriteString("Selector matches do not establish controller ownership. Use g to jump to a Pod for its events and logs.\n")
	return b.String()
}

func tableRow(values []string, widths []int) string {
	var b strings.Builder
	for index, value := range values {
		if index > 0 {
			b.WriteString(" ")
		}
		cell := fitInvestigation(value, widths[index])
		b.WriteString(cell)
		if index < len(values)-1 {
			b.WriteString(strings.Repeat(" ", max(0, widths[index]-runewidth.StringWidth(cell))))
		}
	}
	return strings.TrimRight(b.String(), " ")
}
func fitInvestigation(s string, width int) string {
	if width < 1 {
		return ""
	}
	return runewidth.Truncate(strings.ReplaceAll(s, "\n", " "), width, "…")
}
func investigationAge(at, now time.Time) string {
	if at.IsZero() {
		return workspaceUnknown
	}
	if now.Before(at) {
		return "future observation"
	}
	return now.Sub(at).Round(time.Second).String()
}
func relativeAt(at, captured time.Time) string {
	if at.IsZero() {
		return "time unknown"
	}
	if at.After(captured) {
		return "after capture"
	}
	return captured.Sub(at).Round(time.Second).String() + " before capture"
}
func fullAt(at time.Time) string {
	if at.IsZero() {
		return workspaceUnknown
	}
	return at.UTC().Format(time.RFC3339)
}
func orNA(s string) string {
	if s == "" {
		return "N/A"
	}
	return s
}
func windowLabel(window time.Duration) string {
	if window <= 0 {
		return workspaceUnknown
	}
	return window.String()
}
