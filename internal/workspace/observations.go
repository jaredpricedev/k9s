// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package workspace

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
)

const (
	MaxQueueHistory    = 512
	MaxTrackedFindings = MaxResources
	QueueRetention     = 24 * time.Hour
	QueueFirstObserved = "first observed"
	QueueAdded         = "added"
	QueuePersistent    = "persistent"
	QueueCleared       = "cleared"
	QueueReplaced      = "replaced"
	QueueUnknown       = "unknown"
	QueueGap           = "gap"
)

// ObservedFinding is an observed interval, not the true onset or end of a fault.
// LastObservedAt is the last positive source observation, even across gaps.
type ObservedFinding struct {
	Context                                    string
	Finding                                    Finding
	State                                      string
	FirstObservedAt, LastObservedAt, CheckedAt time.Time
	Coverage                                   Coverage
}

// QueueChange retains only safe finding projections and matching query evidence.
// Old UIDs remain separate from replacements and never navigate to a new object.
type QueueChange struct {
	Sequence                                    uint64
	State, Context                              string
	Ref                                         ResourceRef
	Kind, Reason, Detail                        string
	ObservedAt, FirstObservedAt, LastObservedAt time.Time
	Coverage                                    Coverage
	ReplacementUID                              string
}

// QueueWindow is intentionally memory-only. Reopening a workspace or restarting
// the process starts another window; saved scope metadata is not cluster history.
type QueueWindow struct {
	StartedAt, LastRefreshAt, PrunedBefore time.Time
	Context, ScopeName, LabelSelector      string
	Dropped                                int
	History                                []QueueChange
	active                                 map[string]ObservedFinding
	queries                                map[string]bool
	allowed                                map[string]bool
	trackingIncomplete                     bool
	sequence                               uint64
}

//nolint:gocritic // Freeze the explicitly opened scope, excluding mutable pins/searches/layout.
func NewQueueWindow(scope Scope, at time.Time) *QueueWindow {
	w := &QueueWindow{StartedAt: at, Context: scope.Context, ScopeName: scope.Name,
		LabelSelector: scope.LabelSelector, active: make(map[string]ObservedFinding),
		queries: make(map[string]bool), allowed: make(map[string]bool)}
	kinds := scope.Kinds
	if len(kinds) == 0 {
		kinds = DefaultKinds()
	}
	for _, kind := range kinds {
		gvr, _, err := ResolveKind(kind)
		if err != nil {
			continue
		}
		for _, ns := range scope.Namespaces {
			w.allowed[queryKey(gvrString(gvr), ns)] = true
		}
	}
	return w
}

// Observe keeps positive evidence from partial reads, but only complete matching
// queries establish a disappearance. A failed refresh invalidates every query's
// negative evidence, even if a canceled collector returned a complete page.
func (w *QueueWindow) Observe(snapshot *Snapshot, refreshErr error) {
	if snapshot == nil {
		return
	}
	at := snapshot.ObservedAt
	if at.IsZero() || at.Before(w.StartedAt) || !w.LastRefreshAt.IsZero() && !at.After(w.LastRefreshAt) {
		c := Coverage{State: "stale", Detail: "Out-of-order or undated refresh ignored; prior evidence retained"}
		for key := range w.active {
			f := w.active[key]
			f.State, f.Coverage = QueueUnknown, c
			w.active[key] = f
		}
		clear(w.queries)
		w.addGap(w.LastRefreshAt, c)
		return
	}
	w.prune(at)
	coverage := w.queryCoverage(snapshot, refreshErr)
	w.observeFindings(snapshot, coverage)
	w.checkMissing(snapshot, coverage)
	w.LastRefreshAt = at
	for key, c := range coverage {
		w.queries[key] = c.State == "complete" && !c.Truncated
		if !w.queries[key] {
			w.addGap(at, c)
		}
	}
}

func (w *QueueWindow) queryCoverage(snapshot *Snapshot, refreshErr error) map[string]Coverage {
	result := make(map[string]Coverage, len(w.allowed))
	for key := range w.allowed {
		gvr, ns, _ := strings.Cut(key, "\x00")
		result[key] = Coverage{GVR: gvr, Namespace: ns, State: QueueUnknown, Detail: "Matching query coverage was not returned"}
	}
	seen := make(map[string]bool)
	for _, c := range snapshot.Coverage {
		key := queryKey(c.GVR, c.Namespace)
		if !w.allowed[key] {
			continue
		}
		if seen[key] {
			c.State, c.Detail = QueueUnknown, "Duplicate query coverage is ambiguous"
		}
		seen[key] = true
		c.Detail = safeQueueText(c.Detail)
		result[key] = c
	}
	if refreshErr != nil {
		for key, c := range result {
			c.State, c.Detail = "canceled", "Refresh failed; negative evidence unavailable: "+safeQueueText(refreshErr.Error())
			result[key] = c
		}
	}
	return result
}

func (w *QueueWindow) observeFindings(snapshot *Snapshot, coverage map[string]Coverage) {
	findings := normalizedFindings(snapshot.Findings)
	for i := range findings {
		f := &findings[i]
		q := queryKey(f.Ref.GVR, f.Ref.Namespace)
		if !w.allowed[q] || f.Ref.Name == "" || f.Ref.UID == "" {
			continue
		}
		key := findingKey(f)
		if existing, ok := w.active[key]; ok {
			existing.Finding, existing.LastObservedAt, existing.CheckedAt = *f, snapshot.ObservedAt, snapshot.ObservedAt
			existing.State, existing.Coverage = QueuePersistent, coverage[q]
			w.active[key] = existing
			continue
		}
		state := QueueFirstObserved
		if w.queries[q] && !w.trackingIncomplete {
			state = QueueAdded
		}
		observed := ObservedFinding{Context: w.Context, Finding: *f, State: state,
			FirstObservedAt: snapshot.ObservedAt, LastObservedAt: snapshot.ObservedAt,
			CheckedAt: snapshot.ObservedAt, Coverage: coverage[q]}
		w.active[key] = observed
		w.appendChange(&QueueChange{State: state, Context: w.Context, Ref: f.Ref, Kind: f.Kind,
			Reason: f.Reason, Detail: f.Detail, ObservedAt: snapshot.ObservedAt,
			FirstObservedAt: snapshot.ObservedAt, LastObservedAt: snapshot.ObservedAt, Coverage: coverage[q]})
	}
	w.capActive(snapshot.ObservedAt)
}

func (w *QueueWindow) checkMissing(snapshot *Snapshot, coverage map[string]Coverage) {
	seen := make(map[string]bool)
	findings := normalizedFindings(snapshot.Findings)
	for i := range findings {
		seen[findingKey(&findings[i])] = true
	}
	replacements := make(map[string]string)
	for _, resource := range snapshot.Resources {
		if resource.Ref.UID != "" {
			replacements[refKey(resource.Ref)] = resource.Ref.UID
		}
	}
	keys := make([]string, 0, len(w.active))
	for key := range w.active {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		f := w.active[key]
		c := coverage[queryKey(f.Finding.Ref.GVR, f.Finding.Ref.Namespace)]
		f.CheckedAt, f.Coverage = snapshot.ObservedAt, c
		if c.State != "complete" || c.Truncated {
			f.State = QueueUnknown
			w.active[key] = f
			continue
		}
		state, detail := QueueCleared, "Finding absent from a complete matching query; this is not proof of cause resolution or resource health"
		newUID := replacements[refKey(f.Finding.Ref)]
		if newUID != "" && newUID != f.Finding.Ref.UID {
			state, detail = QueueReplaced, "A different UID was observed at this name; the prior identity's condition was not proven resolved"
		}
		w.appendChange(&QueueChange{State: state, Context: w.Context, Ref: f.Finding.Ref, Kind: f.Finding.Kind,
			Reason: f.Finding.Reason, Detail: detail + ". Prior: " + f.Finding.Detail,
			ObservedAt: snapshot.ObservedAt, FirstObservedAt: f.FirstObservedAt,
			LastObservedAt: f.LastObservedAt, Coverage: c, ReplacementUID: newUID})
		delete(w.active, key)
	}
}

func (w *QueueWindow) Active() []ObservedFinding {
	result := make([]ObservedFinding, 0, len(w.active))
	for key := range w.active {
		result = append(result, w.active[key])
	}
	sort.Slice(result, func(i, j int) bool { return findingKey(&result[i].Finding) < findingKey(&result[j].Finding) })
	return result
}

func (w *QueueWindow) addGap(at time.Time, c Coverage) {
	w.appendChange(&QueueChange{State: QueueGap, Context: w.Context, Ref: ResourceRef{GVR: c.GVR, Namespace: c.Namespace},
		Reason: "Collection " + c.State, Detail: c.Detail, ObservedAt: at, Coverage: c})
}

func (w *QueueWindow) appendChange(c *QueueChange) {
	w.sequence++
	c.Sequence = w.sequence
	w.History = append(w.History, *c)
	if len(w.History) > MaxQueueHistory {
		w.Dropped++
		w.PrunedBefore = w.History[0].ObservedAt
		w.History = append([]QueueChange(nil), w.History[len(w.History)-MaxQueueHistory:]...)
	}
}

func (w *QueueWindow) prune(at time.Time) {
	cutoff := at.Add(-QueueRetention)
	for key := range w.active {
		f := w.active[key]
		if !f.LastObservedAt.Before(cutoff) {
			continue
		}
		delete(w.active, key)
		w.trackingIncomplete = true
		w.Dropped++
		w.PrunedBefore = cutoff
		clear(w.queries)
	}
	first := 0
	for first < len(w.History) && w.History[first].ObservedAt.Before(cutoff) {
		first++
	}
	if first > 0 {
		w.Dropped += first
		w.PrunedBefore = cutoff
		w.History = append([]QueueChange(nil), w.History[first:]...)
	}
}

func (w *QueueWindow) capActive(at time.Time) {
	if len(w.active) <= MaxTrackedFindings {
		return
	}
	ordered := w.Active()
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].LastObservedAt.Before(ordered[j].LastObservedAt) })
	for i := range len(ordered) - MaxTrackedFindings {
		delete(w.active, findingKey(&ordered[i].Finding))
		w.Dropped++
	}
	w.PrunedBefore = at
	w.trackingIncomplete = true
	clear(w.queries)
}

func queryKey(gvr, namespace string) string { return gvr + "\x00" + namespace }
func findingKey(f *Finding) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", f.Ref.GVR, f.Ref.Namespace, f.Ref.Name, f.Ref.UID, f.Category, f.Reason)
}
func safeQueueText(text string) string { return logstream.SafeText(boundedText(text, 4096)) }
func safeFinding(f *Finding) Finding {
	result := *f
	result.Kind, result.Category, result.Severity = safeQueueText(f.Kind), safeQueueText(f.Category), safeQueueText(f.Severity)
	result.Reason, result.Detail = safeQueueText(f.Reason), safeQueueText(f.Detail)
	return result
}

func (w *QueueWindow) Contains(ref *ResourceRef) bool {
	return ref != nil && ref.Name != "" && ref.UID != "" && w.allowed[queryKey(ref.GVR, ref.Namespace)]
}

func normalizedFindings(findings []Finding) []Finding {
	unique := make(map[string]Finding)
	for i := range findings {
		f := safeFinding(&findings[i])
		key := findingKey(&f)
		if previous, ok := unique[key]; ok && previous.Detail != f.Detail {
			f.Detail = safeQueueText(previous.Detail + "; " + f.Detail)
		}
		unique[key] = f
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Finding, 0, len(keys))
	for _, key := range keys {
		result = append(result, unique[key])
	}
	return result
}
