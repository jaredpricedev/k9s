// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/activity"
	"github.com/derailed/k9s/internal/workspace"
)

const activityCommand = "activity"

func (w *dailyWorkspace) localEvidenceMode() bool {
	return w.mode == dailyWorkspaceCoverageMode || w.mode == dailyWorkspaceHistoryMode || w.mode == dailyWorkspaceActivityMode
}

func (w *dailyWorkspace) activityStatus(width int) string {
	if w.activityWindow == nil {
		return "New app observation window · r reads scope"
	}
	eventState := "?"
	if len(w.activityEventCoverage) > 0 {
		eventState = "retained"
		for _, c := range w.activityEventCoverage {
			if c.State != dailyWorkspaceCoverageComplete || c.Truncated {
				eventState = "partial"
				break
			}
		}
	}
	gaps := 0
	for _, c := range w.coverage {
		if c.State != dailyWorkspaceCoverageComplete || c.Truncated {
			gaps++
		}
	}
	for _, c := range w.activityEventCoverage {
		if c.State != dailyWorkspaceCoverageComplete || c.Truncated {
			gaps++
		}
	}
	window := w.activityWindow
	if len(w.activityEventCoverage) == 0 {
		gaps++
	}
	for i := range window.Entries {
		entry := &window.Entries[i]
		if entry.ObservedAt.Before(window.LastRefreshAt) {
			continue
		}
		if entry.HistorySource == "Source projection gap" || entry.HistorySource == "Local projection limits" {
			gaps++
		}
	}
	if width < 50 {
		return fmt.Sprintf("Since %sZ · Events %s · gaps %d", window.StartedAt.UTC().Format("15:04"), eventState, gaps)
	}
	return fmt.Sprintf("Since %sZ · Events %s · gaps %d · omitted %d", window.StartedAt.UTC().Format("15:04:05"), eventState, gaps, window.Dropped)
}

func (w *dailyWorkspace) activityRows(terms []dailyWorkspaceTerm) ([]string, []dailyWorkspaceRow) {
	headers := []string{"STATE", "OBS UTC", "NAME / QUERY", "ACTIVITY"}
	rows := make([]dailyWorkspaceRow, 0)
	if w.activityWindow == nil {
		return headers, rows
	}
	window := w.activityWindow
	indices := make([]int, 0, len(window.Entries))
	for index := len(window.Entries) - 1; index >= 0; index-- {
		if window.Entries[index].State != activity.EntryGap {
			indices = append(indices, index)
		}
	}
	for index := len(window.Entries) - 1; index >= 0; index-- {
		if window.Entries[index].State == activity.EntryGap {
			indices = append(indices, index)
		}
	}
	for _, index := range indices {
		entry := &window.Entries[index]
		var ref *workspace.ResourceRef
		kind, name, namespace := "", entry.Coverage.GVR, entry.Coverage.Namespace
		if entry.Source != nil {
			i := entry.Source.Identity
			ref = &workspace.ResourceRef{GVR: i.GVR, Namespace: i.Namespace, Name: i.Name, UID: i.UID}
			kind, name, namespace = entry.Source.Kind, i.Name, i.Namespace
		}
		if entry.Event != nil {
			i := entry.Event.Regarding
			ref = &workspace.ResourceRef{GVR: i.GVR, Namespace: i.Namespace, Name: i.Name, UID: i.UID}
			kind, name, namespace = entry.Event.Kind, i.Name, i.Namespace
		}
		if name == "" {
			name = window.ScopeName
		}
		if !dailyWorkspaceMatch(terms, kind, namespace, name, entry.State+" "+entry.Summary) {
			continue
		}
		data, _ := json.MarshalIndent(entry, "", "  ")
		state := entry.State
		switch state {
		case activity.EntryObserved:
			state = "observed"
		case activity.EntryMissing:
			state = "unconfirmed"
		case activity.EntryEvent:
			state = "Event"
		}
		summary := entry.Summary
		if w.viewportWidth > 0 && w.viewportWidth < 80 {
			summary = entry.ObservedAt.UTC().Format("15:04") + "Z · " + summary
		}
		rows = append(rows, dailyWorkspaceRow{cells: []string{state, entry.ObservedAt.UTC().Format("15:04:05"), name, summary},
			ref: ref, key: fmt.Sprintf("activity/%d", entry.Sequence), detail: activityEntryDetail(entry, w.viewportWidth),
			evidence: fmt.Sprintf("Observation began: %s\nScope: %s · context %s · selector %s\nHistory source: %s\n"+
				"Retained entries: at most %d for %s; omitted %d, earlier boundary %s.\n"+
				"Current lists and retained Events do not establish complete historical activity.\n\n%s",
				fullAt(window.StartedAt), window.ScopeName, window.Context, window.LabelSelector, entry.HistorySource,
				activity.MaxEntries, activity.Retention, window.Dropped, fullAt(window.PrunedBefore), string(data))})
	}
	return headers, rows
}

func activityEntryDetail(entry *activity.Entry, width int) string {
	var b strings.Builder
	if len(entry.Changes) > 0 {
		for i := range min(2, len(entry.Changes)) {
			change := &entry.Changes[i]
			label := change.Field
			switch change.Group {
			case activity.GroupDeclaredImage:
				label = "Image " + change.Field
			case activity.GroupRuntimeImage:
				label = "Runtime imageID " + change.Field
			case "Chart version", "Application version":
				label = change.Group
			}
			if width > 0 && width < 50 {
				fmt.Fprintf(&b, "%s observed change\nAfter: %s\n", label, change.After)
				break
			}
			fmt.Fprintf(&b, "%s: %s → %s\n", label, change.Before, change.After)
		}
	} else if entry.Event != nil {
		fmt.Fprintf(&b, "%s · %s\nAPI last time %s · aggregate count %s\n", entry.Event.Type, entry.Event.Reason, fullAt(entry.Event.LastAt), rolloutCount(entry.Event.Count))
	} else {
		fmt.Fprintln(&b, entry.Summary)
	}
	fmt.Fprintf(&b, "Observed %s · Enter exact evidence; i captured-UID investigation; J Job review", fullAt(entry.ObservedAt))
	return b.String()
}
