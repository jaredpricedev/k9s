// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
)

const jobReviewIndent, jobReviewSelectedMarker = "  ", "> "

func (v *jobReviewView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading bounded, read-only Job outcome evidence..."
	width, rows := v.width, v.bodyRows
	if width == 0 {
		width = 76
	}
	if rows == 0 {
		rows = 16
	}
	if v.snapshot != nil {
		switch v.activeTab {
		case 1:
			text = jobReviewRuns(v.snapshot, v.runIndex(), width, rows)
		case 2:
			text = jobReviewPods(v.snapshot, v.podIndex(), width, rows)
		case 3:
			text = jobScheduleText(v.snapshot)
		case 4:
			text = jobReviewEvidence(v.snapshot)
		default:
			text = jobReviewOverview(v.snapshot, width, rows)
		}
	} else if v.refreshFailure != "" {
		text = "[?] Job evidence unavailable: " + v.refreshFailure
	}
	v.text.SetWrap(v.activeTab == 4 || v.activeTab == 3)
	query, region := v.inspectionQuery, v.currentRegion
	row, col := v.text.GetScrollOffset()
	v.Update(text)
	if query != "" {
		v.model.Filter(query)
		if region < v.maxRegions {
			v.currentRegion = region
			v.text.Highlight(fmt.Sprintf("search_%d", region))
		}
	}
	v.text.ScrollTo(row, col)
	v.updateTitle()
}

func (v *jobReviewView) renderChrome() {
	if v.identityBar == nil {
		return
	}
	p := v.app.Styles.Semantic()
	for _, item := range []*tview.TextView{v.identityBar, v.tabsBar, v.footer} {
		item.SetBackgroundColor(p.Panel.Color())
		item.SetTextColor(p.Text.Color())
	}
	width := v.width
	if width <= 0 {
		width = 76
	}
	identity := v.target
	if v.activeTab == 1 || v.activeTab == 2 {
		if selected := v.SelectedResource(); selected.Err() == nil {
			identity = selected
		}
	}
	first := "Job review · " + identity.Path() + " · " + identity.Context
	second, third := "No observation obtained yet", "READ ONLY · bounded Job evidence"
	if v.snapshot != nil {
		second = fmt.Sprintf("UID %s · captured %s · age %s", identity.UID, v.snapshot.CapturedAt.Format("15:04:05Z"),
			investigationAge(v.snapshot.CapturedAt, time.Now()))
		third = rolloutCoverageLine(v.snapshot.Coverage, width)
	}
	if v.loading {
		third = "[~] Refreshing; retained source/time stays visible"
	} else if v.refreshFailure != "" {
		third = "[~] Refresh failed; retained evidence · " + v.refreshFailure
	}
	if v.selectionNotice != "" && v.refreshFailure == "" && !v.loading {
		third = "[~] " + v.selectionNotice
	}
	if !v.destinationCurrent() {
		third = "[~] Destination changed; original evidence retained"
	}
	if v.bodyRows > 0 && v.bodyRows < 10 && (v.refreshFailure != "" || !v.destinationCurrent() || v.selectionNotice != "") {
		first = third
	}
	v.identityBar.SetText(detailStyled(p.Focus.String(), "b", fitInvestigation(first, width)) + "\n" +
		detailStyled(p.Muted.String(), "", fitInvestigation(second, width)) + "\n" +
		detailStyled(p.Warning.String(), "", fitInvestigation(third, width)))
	v.tabsBar.SetText(ui.TaskTabs(jobReviewTabs, v.activeTab, width))
	footer := "Enter inspect · j/k select · l Pod logs · o source actions · v details · r refresh · ? help"
	if width < 80 {
		footer = "Enter inspect · v details · r refresh · ? help"
	}
	if width < 50 {
		footer = "v details · r refresh · ? help"
	}
	v.footer.SetText(detailStyled(p.Muted.String(), "", footer))
}

func jobReviewOverview(s *review.JobReviewSnapshot, width, rows int) string {
	var b strings.Builder
	b.WriteString("JOB OUTCOMES · RETAINED\n")
	if s.Schedule != nil {
		label := "Suspend unknown"
		if s.Schedule.Suspended != nil {
			label = "Schedule enabled"
			if *s.Schedule.Suspended {
				label = "Schedule suspended"
			}
		}
		fmt.Fprintln(&b, fitInvestigation(label+" · "+rolloutKnown(s.Schedule.Concurrency), width))
		if rows >= 10 {
			fmt.Fprintln(&b, fitInvestigation("Schedule: "+s.Schedule.Expression+" · "+rolloutKnown(s.Schedule.TimeZone), width))
			fmt.Fprintf(&b, "Start deadline %ss · Job deadline %ss\n", rolloutCount(s.Schedule.StartingDeadline), rolloutCount(s.Schedule.JobDeadline))
		}
	}
	if len(s.Runs) > 0 {
		run := &s.Runs[0]
		state, reason := run.Outcome()
		fmt.Fprintln(&b, fitInvestigation(jobOutcomeMarker(state)+" "+state+" · "+run.Identity.Name, width))
		fmt.Fprintln(&b, fitInvestigation("Reason: "+reason, width))
		fmt.Fprintln(&b, fitInvestigation(jobRunCounts(run), width))
		if rows >= 10 {
			fmt.Fprintln(&b, fitInvestigation("Start: "+fullAt(run.StartedAt)+" · completion: "+fullAt(run.CompletedAt), width))
		}
	} else {
		b.WriteString("[?] No verified retained Job obtained\n")
	}
	gap := "Retained list; missing Jobs != missed runs"
	for _, item := range s.Coverage {
		if item.State != inspect.ObservationComplete {
			gap = "[?] " + item.Source + ": " + item.State + " · 5 full limits"
			break
		}
	}
	fmt.Fprintln(&b, fitInvestigation(gap, width))
	if rows >= 10 && s.Schedule != nil && len(s.Schedule.Preview.Times) > 0 {
		fmt.Fprintln(&b, fitInvestigation("Preview only: "+s.Schedule.Preview.Times[0].Format(time.RFC3339), width))
	}
	next := "2 Runs · 3 Pods/logs · 4 preview · o actions"
	if width < 50 {
		next = "2 Runs · 3 Pods · o actions · v full"
	}
	fmt.Fprintln(&b, fitInvestigation(next, width))
	return b.String()
}

func jobReviewRuns(s *review.JobReviewSnapshot, selected, width, rows int) string {
	var b strings.Builder
	if len(s.Runs) == 0 {
		return "[?] No retained UID-owned Jobs obtained.\nMissing Jobs do not prove missed execution.\n5 Evidence shows collection and history limits."
	}
	limit := max(1, min(8, rows-6))
	start := max(0, selected-limit+1)
	columns := []int{max(3, width-14), 12}
	fmt.Fprintln(&b, tableRow([]string{"JOB", "OUTCOME"}, columns))
	for index := start; index < min(len(s.Runs), start+limit); index++ {
		run := &s.Runs[index]
		marker := jobReviewIndent
		if index == selected {
			marker = jobReviewSelectedMarker
		}
		state, _ := run.Outcome()
		fmt.Fprintln(&b, tableRow([]string{marker + run.Identity.Name, state}, columns))
	}
	if len(s.Runs) > limit {
		fmt.Fprintf(&b, "j/k: %d retained runs · newest first\n", len(s.Runs))
	}
	run := &s.Runs[selected]
	_, reason := run.Outcome()
	fmt.Fprintln(&b, fitInvestigation("Reason: "+reason, width))
	fmt.Fprintln(&b, fitInvestigation(jobRunCounts(run), width))
	fmt.Fprintln(&b, fitInvestigation("Started: "+fullAt(run.StartedAt), width))
	fmt.Fprintln(&b, fitInvestigation("Enter inspect · 3 Pods · v exact evidence", width))
	return b.String()
}

func jobReviewPods(s *review.JobReviewSnapshot, selected, width, rows int) string {
	var b strings.Builder
	if len(s.Pods) == 0 {
		return "[?] No verified owned Pod obtained.\nMissing Pods do not imply no logs.\n5 Evidence shows coverage; r explicitly refreshes."
	}
	limit := max(1, min(8, rows-5))
	start := max(0, selected-limit+1)
	columns := []int{max(3, width-12), 10}
	fmt.Fprintln(&b, tableRow([]string{"POD", "PHASE"}, columns))
	for index := start; index < min(len(s.Pods), start+limit); index++ {
		pod := &s.Pods[index]
		marker := jobReviewIndent
		if index == selected {
			marker = jobReviewSelectedMarker
		}
		fmt.Fprintln(&b, tableRow([]string{marker + pod.Identity.Name, rolloutKnown(pod.Phase)}, columns))
	}
	if len(s.Pods) > limit {
		fmt.Fprintf(&b, "j/k: %d retained UID-owned Pods\n", len(s.Pods))
	}
	pod := &s.Pods[selected]
	fmt.Fprintln(&b, fitInvestigation("Owner Job UID: "+pod.JobUID, width))
	fmt.Fprintln(&b, fitInvestigation("l opens selected Pod logs · Enter inspect", width))
	fmt.Fprintln(&b, fitInvestigation("v exact declared image / runtime imageID", width))
	return b.String()
}

func jobScheduleText(s *review.JobReviewSnapshot) string {
	if s.Schedule == nil {
		return "SELECTED JOB\nParent CronJob schedule was not read.\nRuns show owner/annotation evidence; Pods connect scoped logs."
	}
	var b strings.Builder
	schedule := s.Schedule
	b.WriteString("SCHEDULE PREVIEW · CALCULATED, NOT EXECUTED\n")
	fmt.Fprintf(&b, "Schedule: %s\nspec.timeZone: %s\nPreview timezone: %s\nConcurrency: %s\nSuspend: %s\n",
		schedule.Expression, rolloutKnown(schedule.TimeZone), schedule.Preview.TimeZone, rolloutKnown(schedule.Concurrency), jobBoolLabel(schedule.Suspended))
	fmt.Fprintf(&b, "Starting deadline: %s seconds\nJob deadline: %s seconds\nHistory limits: successful %s / failed %s\n",
		rolloutCount(schedule.StartingDeadline), rolloutCount(schedule.JobDeadline), rolloutCount(schedule.HistorySuccess), rolloutCount(schedule.HistoryFailed))
	fmt.Fprintf(&b, "Last scheduled API time: %s\nLast successful API time: %s\nReference capture: %s\n\n",
		fullAt(schedule.LastScheduled), fullAt(schedule.LastSuccessful), fullAt(s.CapturedAt))
	if schedule.Preview.Reason != "" {
		fmt.Fprintln(&b, "[?] "+schedule.Preview.Reason)
	}
	for _, at := range schedule.Preview.Times {
		fmt.Fprintf(&b, "Calculated match: %s\n", at.Format(time.RFC3339))
	}
	b.WriteString("\nASSUMPTIONS AND HISTORY LIMITS\n")
	for _, assumption := range schedule.Preview.Assumptions {
		fmt.Fprintln(&b, assumption)
	}
	return b.String()
}

func jobRunCounts(run *review.JobRun) string {
	return fmt.Sprintf("Active %s · succeeded %s · failed %s", rolloutCount(run.Active), rolloutCount(run.Succeeded), rolloutCount(run.Failed))
}

func jobBoolLabel(value *bool) string {
	if value == nil {
		return inspect.ObservationUnknown
	}
	return fmt.Sprint(*value)
}

func jobOutcomeMarker(state string) string {
	switch state {
	case review.JobOutcomeFailed:
		return "[!]"
	case review.JobOutcomeSucceeded:
		return "[+]"
	case review.JobOutcomeRunning, review.JobOutcomeSuspended:
		return "[~]"
	default:
		return "[?]"
	}
}

func jobReviewEvidence(s *review.JobReviewSnapshot) string {
	data, err := json.MarshalIndent(s, "", jobReviewIndent)
	if err != nil {
		return "Job evidence unavailable: " + err.Error()
	}
	return "RETAINED JOB SOURCE EVIDENCE\nCurrent bounded API records; not complete execution history.\nNo raw object specs or Secret values retained.\n\n" + string(data)
}

func jobRunEvidence(run *review.JobRun, at time.Time) string {
	data, err := json.MarshalIndent(run, "", jobReviewIndent)
	if err != nil {
		return "Job evidence unavailable: " + err.Error()
	}
	return "RETAINED JOB · captured " + fullAt(at) + "\nSource: batch/v1 Job API fields and conditions\n\n" + string(data)
}

func jobPodEvidence(pod *review.JobReviewPod, at time.Time) string {
	data, err := json.MarshalIndent(pod, "", jobReviewIndent)
	if err != nil {
		return "Pod evidence unavailable: " + err.Error()
	}
	return "RETAINED POD · captured " + fullAt(at) + "\nSource: v1 Pod status; owner Job UID verified\n\n" + string(data)
}
