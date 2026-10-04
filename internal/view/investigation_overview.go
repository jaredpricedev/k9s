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
	_, _, width, _ := d.GetInnerRect()
	if d.snapshot.Investigation != nil && d.CompactWorkspace() {
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
	var tabs strings.Builder
	for index, name := range investigationTabs {
		label := fmt.Sprintf("%d %s", index+1, name)
		if index == d.activeTab {
			tabs.WriteString(detailStyled(p.Focus.String(), "b", "["+label+"]"))
		} else {
			tabs.WriteString(detailStyled(p.Muted.String(), "", label))
		}
		tabs.WriteString("  ")
	}
	d.tabsBar.SetText(tabs.String())
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
		if !c.CurrentIssue() {
			continue
		}
		issues++
		if issues > 2 {
			continue
		}
		fmt.Fprintln(&b, fitInvestigation("[!] "+c.CurrentLabel()+" · "+c.Name+" · Pod "+c.Pod, width))
		if c.LastTermination != nil {
			fmt.Fprintln(&b, fitInvestigation("[~] Previous termination: "+c.LastTermination.Label()+" · "+relativeAt(c.LastTermination.FinishedAt, i.CapturedAt), width))
		} else {
			fmt.Fprintln(&b, "[?] Previous termination: not reported")
		}
	}
	if issues == 0 {
		for _, c := range i.Conditions {
			if c.Adverse {
				issues++
				fmt.Fprintln(&b, fitInvestigation("[!] "+c.Type+"="+c.Status+" · "+c.Reason, width))
				if issues >= 2 {
					break
				}
			}
		}
	}
	if issues == 0 {
		fmt.Fprintln(&b, "[?] No current fault established by these sources.")
		if i.Phase != "" {
			fmt.Fprintln(&b, fitInvestigation("    API phase: "+i.Phase+"; inspect conditions and retained evidence.", width))
		}
	}
	if issues > 2 {
		fmt.Fprintf(&b, "    %d more affected containers; 2 opens all containers.\n", issues-2)
	}
	if len(i.Containers) > 0 {
		b.WriteString("\nCONTAINER STATUS · current / previous remain separate\n")
		b.WriteString(investigationContainerTable(i, width, 4))
	} else {
		b.WriteString("\nCONDITIONS · current API values\n")
		b.WriteString(tableRow([]string{"CONDITION", statusCol, "REASON"}, []int{24, 9, max(12, width-35)}) + "\n")
		for _, c := range i.Conditions[:min(len(i.Conditions), 4)] {
			b.WriteString(tableRow([]string{c.Type, c.Status, c.Reason}, []int{24, 9, max(12, width-35)}) + "\n")
		}
		if len(i.Conditions) == 0 {
			b.WriteString("[?] No conditions reported; absence is not proof of health.\n")
		}
	}
	b.WriteString("\nVISIBILITY\n")
	coverage := make([]string, 0, len(i.Coverage))
	for _, c := range i.Coverage {
		if c.Source == "selector-matching Pods" {
			continue
		}
		state := c.State
		if state == inspect.ObservationComplete {
			state = "collected"
			if strings.HasPrefix(c.Source, "events") {
				state = "retained / bounded"
			}
		}
		coverage = append(coverage, c.Source+": "+state)
	}
	// Wrap badges at word boundaries so a missing source is never clipped out.
	line := ""
	for _, badge := range coverage {
		if line != "" && runewidth.StringWidth(line+" · "+badge) > width {
			fmt.Fprintln(&b, line)
			line = ""
		}
		if line != "" {
			line += " · "
		}
		line += badge
	}
	if line != "" {
		fmt.Fprintln(&b, line)
	}
	b.WriteString("NEXT CHECKS\n")
	if issues > 0 {
		b.WriteString("2 containers: current vs previous · 3 events: retained history\n")
	} else {
		b.WriteString("2 containers · 3 events · 5 evidence: sources and full messages\n")
	}
	if i.Identity.GVR == client.PodGVR.String() {
		b.WriteString("l Pod logs · :pressure budgets · 6 compare chosen A\n")
	} else {
		b.WriteString("g related: choose Pod · :pressure budgets · 6 compare chosen A\n")
	}
	return b.String()
}

func investigationContainerTable(i *inspect.Investigation, width, limit int) string {
	var b strings.Builder
	nameWidth := 18
	stateWidth := 25
	if width < 90 {
		nameWidth = 13
		stateWidth = 22
	}
	fmt.Fprintln(&b, tableRow([]string{"CONTAINER", "CURRENT", "READY", "RESTART", "PREVIOUS"}, []int{nameWidth, stateWidth, 7, 7, max(8, width-nameWidth-stateWidth-25)}))
	if len(i.Containers) == 0 {
		b.WriteString("[?] No container statuses reported for this resource.\n")
		return b.String()
	}
	for index := range min(len(i.Containers), limit) {
		c := &i.Containers[index]
		ready := "?"
		if c.Ready != nil {
			ready = "no"
			if *c.Ready {
				ready = "yes"
			}
		}
		restarts := "?"
		if c.Restarts != nil {
			restarts = fmt.Sprint(*c.Restarts)
		}
		previous := "none reported"
		if c.LastTermination != nil {
			previous = c.LastTermination.Reason
			if previous == "" {
				previous = "terminated"
			}
		}
		name := c.Name
		if c.Role != investigationAppRole {
			name += " (" + c.Role + ")"
		}
		fmt.Fprintln(&b, tableRow([]string{name, c.CurrentLabel(), ready, restarts, previous}, []int{nameWidth, stateWidth, 7, 7, max(8, width-nameWidth-stateWidth-25)}))
	}
	if len(i.Containers) > limit {
		fmt.Fprintf(&b, "%d more rows; 2 opens full container detail.\n", len(i.Containers)-limit)
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
