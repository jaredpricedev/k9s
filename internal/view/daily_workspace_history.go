// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/workspace"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const maxWorkspaceJobSources = 64

func (w *dailyWorkspace) resetObservationWindow(at time.Time) {
	w.observationWindow = workspace.NewQueueWindow(w.scope, at)
	w.jobSources = make(map[string]*review.JobReviewSnapshot)
	w.jobSourcesOmitted = 0
}

func (w *dailyWorkspace) recordQueueGap(reason string) {
	if w.observationWindow != nil {
		w.observationWindow.Observe(&workspace.Snapshot{ObservedAt: time.Now()}, errors.New(reason))
	}
}

func (w *dailyWorkspace) observeSnapshot(snapshot *workspace.Snapshot, err error) bool {
	if w.observationWindow == nil {
		at := snapshot.ObservedAt
		if !w.snapshot.ObservedAt.IsZero() {
			at = w.snapshot.ObservedAt
		}
		if at.IsZero() {
			at = time.Now()
		}
		w.resetObservationWindow(at)
		if !w.snapshot.ObservedAt.IsZero() {
			w.observationWindow.Observe(&w.snapshot, nil)
		}
	}
	prior := w.observationWindow.LastRefreshAt
	w.observationWindow.Observe(snapshot, err)
	if w.observationWindow.LastRefreshAt.After(prior) {
		w.retainJobSources(snapshot)
		return true
	}
	return false
}

func (w *dailyWorkspace) retainJobSources(snapshot *workspace.Snapshot) {
	var jobs, pods []*unstructured.Unstructured
	for _, r := range snapshot.Resources {
		if !w.observationWindow.Contains(&r.Ref) {
			continue
		}
		if r.Ref.GVR == "batch/v1/jobs" {
			jobs = append(jobs, r.Object)
		}
		if r.Ref.GVR == "v1/pods" {
			pods = append(pods, r.Object)
		}
	}
	for _, r := range snapshot.Resources {
		if !w.observationWindow.Contains(&r.Ref) || r.Object == nil || r.Ref.GVR != "batch/v1/cronjobs" && r.Ref.GVR != "batch/v1/jobs" || r.Ref.UID == "" {
			continue
		}
		coverage := workspaceJobCoverage(snapshot, r.Ref.GVR)
		source := review.NewJobReviewSnapshot(r.Object, jobs, pods, coverage, w.scope.Context, snapshot.ObservedAt)
		if source.Identity.UID != r.Ref.UID || source.Identity.Name != r.Ref.Name || source.Identity.Namespace != r.Ref.Namespace || source.Identity.GVR != r.Ref.GVR {
			continue
		}
		w.jobSources[dailyWorkspaceRefKey(&r.Ref)] = source
	}
	cutoff := snapshot.ObservedAt.Add(-workspace.QueueRetention)
	ordered := make([]string, 0, len(w.jobSources))
	for key, source := range w.jobSources {
		if source.CapturedAt.Before(cutoff) {
			delete(w.jobSources, key)
			w.jobSourcesOmitted++
			continue
		}
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := w.jobSources[ordered[i]].CapturedAt, w.jobSources[ordered[j]].CapturedAt
		if a.Equal(b) {
			return ordered[i] < ordered[j]
		}
		return a.Before(b)
	})
	for _, key := range ordered[:max(0, len(ordered)-maxWorkspaceJobSources)] {
		delete(w.jobSources, key)
		w.jobSourcesOmitted++
	}
}

func workspaceJobCoverage(snapshot *workspace.Snapshot, sourceGVR string) []review.RolloutCoverage {
	result := []review.RolloutCoverage{{Source: "Workspace scope", State: inspect.ObservationIncomplete,
		Detail: "Current explicitly selected namespaces/kinds/labels; child labels can differ. Bounded present records are not complete execution history"}}
	for _, item := range []struct{ gvr, name string }{{sourceGVR, "Source"}, {"batch/v1/jobs", "Jobs"}, {"v1/pods", "Pods"}} {
		matched := false
		for _, c := range snapshot.Coverage {
			if c.GVR != item.gvr {
				continue
			}
			matched = true
			result = append(result, review.RolloutCoverage{Source: item.name + " · " + c.Namespace, State: c.State, Detail: c.Detail})
		}
		if !matched {
			result = append(result, review.RolloutCoverage{Source: item.name, State: inspect.ObservationUnknown, Detail: "Not queried in this workspace refresh"})
		}
	}
	return result
}

func (w *dailyWorkspace) historyStatus(width int) string {
	if w.observationWindow == nil {
		return "New observation window · r begins reading"
	}
	window := w.observationWindow
	unknown := 0
	active := window.Active()
	for i := range active {
		f := &active[i]
		if f.State == workspace.QueueUnknown {
			unknown++
		}
	}
	if width < 70 {
		return fmt.Sprintf("Since %s · unknown %d · omitted %d", window.StartedAt.UTC().Format("15:04:05"), unknown, window.Dropped+w.jobSourcesOmitted)
	}
	return fmt.Sprintf("Observing since %s · %d unresolved gaps · %d entries omitted", fullAt(window.StartedAt), unknown, window.Dropped+w.jobSourcesOmitted)
}

func (w *dailyWorkspace) historyRows(terms []dailyWorkspaceTerm) ([]string, []dailyWorkspaceRow) {
	headers := []string{"STATE", "TIME UTC", "NAME / QUERY", "EVIDENCE"}
	rows := make([]dailyWorkspaceRow, 0)
	window := w.observationWindow
	if window == nil {
		return headers, rows
	}
	appendRow := func(state string, at time.Time, ref *workspace.ResourceRef, kind, reason, detail, key string, evidence any) {
		ns, name := "", window.ScopeName
		if c, ok := evidence.(workspace.QueueChange); ok && ref == nil {
			ns, name = c.Ref.Namespace, c.Ref.GVR
		}
		if ref != nil {
			ns, name = ref.Namespace, ref.Name
			if name == "" {
				name = ref.GVR
			}
		}
		if !dailyWorkspaceMatch(terms, kind, ns, name, state+" "+reason+" "+detail) {
			return
		}
		data, _ := json.MarshalIndent(evidence, "", "  ")
		rows = append(rows, dailyWorkspaceRow{cells: []string{state, at.UTC().Format("15:04:05"), name, reason}, ref: ref, key: key,
			detail: detail, evidence: "Retained observation evidence; no continuous history implied.\n" + string(data)})
	}
	active := window.Active()
	for i := range active {
		f := &active[i]
		ref := f.Finding.Ref
		detail := fmt.Sprintf("Last positive %s · first %s. Matching query: %s. %s. Enter evidence; i checks captured UID; J Job review.",
			fullAt(f.LastObservedAt), fullAt(f.FirstObservedAt), f.Coverage.State, f.Finding.Detail)
		key := "active/" + dailyWorkspaceRefKey(&ref) + "/" + f.Finding.Category + "/" + f.Finding.Reason
		appendRow(f.State, f.LastObservedAt, &ref, f.Finding.Kind, f.Finding.Reason, detail, key, f)
	}
	for i := len(window.History) - 1; i >= 0; i-- {
		c := window.History[i]
		ref := c.Ref
		var target *workspace.ResourceRef
		if ref.Name != "" && ref.UID != "" {
			target = &ref
		}
		detail := c.Detail + " · " + fullAt(c.ObservedAt) + ". Enter retained evidence; i checks the captured UID."
		appendRow("H: "+c.State, c.ObservedAt, target, c.Kind, c.Reason, detail, fmt.Sprintf("history/%d", c.Sequence), c)
	}
	keys := make([]string, 0, len(w.jobSources))
	for key := range w.jobSources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		source := w.jobSources[key]
		ref := workspace.ResourceRef{GVR: source.Identity.GVR, Namespace: source.Identity.Namespace, Name: source.Identity.Name, UID: source.Identity.UID}
		reason := fmt.Sprintf("%d retained runs", len(source.Runs))
		detail := "Current API records, not continuous history. Missing Jobs do not prove missed execution. " +
			"Enter source evidence; J reads Job review."
		if source.Schedule != nil {
			reason = source.Schedule.Expression
			detail = jobScheduleText(source)
		}
		if source.Kind == inspectionJobKind && len(source.Runs) > 0 {
			state, why := source.Runs[0].Outcome()
			reason = state + " · " + why
		}
		if source.CapturedAt.Before(window.LastRefreshAt) {
			detail = "Not observed in the latest refresh; reason unknown. Retained capture: " + fullAt(source.CapturedAt) + "\n" + detail
		}
		appendRow("job source", source.CapturedAt, &ref, source.Kind, reason, detail, "job-source/"+key, source)
	}
	return headers, rows
}
