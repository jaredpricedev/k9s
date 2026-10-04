// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package activity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/workspace"
)

const (
	MaxEntries        = 512
	MaxTrackedSources = workspace.MaxResources
	Retention         = 24 * time.Hour
	EntryObserved     = "first observed"
	EntryChanged      = "changed"
	EntryReplaced     = "replacement"
	EntryMissing      = "not observed"
	EntryGap          = "gap"
	EntryEvent        = "retained Event"
)

type FieldChange struct {
	Group, Field, Before, After   string
	BeforeSourceAt, AfterSourceAt time.Time
}

type Entry struct {
	Sequence                      uint64
	State, Summary, HistorySource string
	ObservedAt, PriorObservedAt   time.Time
	Source                        *Source
	Changes                       []FieldChange
	Coverage                      workspace.Coverage
	PriorUID                      string
	Event                         *EventSource
}

type Window struct {
	StartedAt, LastRefreshAt, PrunedBefore time.Time
	Context, ScopeName, LabelSelector      string
	Dropped                                int
	Entries                                []Entry
	matcher                                *workspace.QueueWindow
	tracked                                map[string]*Source
	currentUID                             map[string]string
	events                                 map[string]*EventSource
	sequence                               uint64
}

//nolint:gocritic // Capture the immutable opened app scope, excluding metadata-only edits.
func NewWindow(scope workspace.Scope, at time.Time) *Window {
	return &Window{StartedAt: at, Context: scope.Context, ScopeName: scope.Name, LabelSelector: scope.LabelSelector,
		matcher: workspace.NewQueueWindow(scope, at), tracked: make(map[string]*Source), currentUID: make(map[string]string), events: make(map[string]*EventSource)}
}

func (w *Window) Observe(snapshot *workspace.Snapshot, refreshErr error) {
	if snapshot == nil {
		return
	}
	at := snapshot.ObservedAt
	if at.IsZero() || at.Before(w.StartedAt) || !w.LastRefreshAt.IsZero() && !at.After(w.LastRefreshAt) {
		w.append(&Entry{State: EntryGap, Summary: "Stale or undated refresh ignored", HistorySource: "Explicit refresh gap", ObservedAt: w.LastRefreshAt,
			Coverage: workspace.Coverage{State: "stale", Detail: "Prior observations retained; no continuous history implied"}})
		return
	}
	w.prune(at)
	seen := make(map[string]bool)
	for i := range snapshot.Resources {
		r := &snapshot.Resources[i]
		if !w.matcher.Contains(&r.Ref) {
			continue
		}
		source := Project(r, w.Context, at)
		if source == nil {
			seen[w.Context+"\x00"+r.Ref.GVR+"\x00"+r.Ref.Namespace+"\x00"+r.Ref.Name+"\x00"+r.Ref.UID] = true
			w.append(&Entry{State: EntryGap, Summary: "Obtained source projection unavailable; prior identity evidence retained",
				HistorySource: "Source projection gap", ObservedAt: at,
				Coverage: workspace.Coverage{GVR: r.Ref.GVR, Namespace: r.Ref.Namespace, State: inspect.ObservationUnknown,
					Detail: "Source API/kind/namespace/name/UID must match captured inventory"}})
			continue
		}
		source.Coverage = matchingCoverage(snapshot, source, refreshErr)
		if source.State != inspect.ObservationComplete {
			w.append(&Entry{State: EntryGap, Summary: "Source projection cap reached", HistorySource: "Local projection limits", ObservedAt: at, Source: source})
		}
		key := identityKey(source)
		if seen[key] {
			continue
		}
		seen[key] = true
		prior := w.tracked[key]
		nameKey := resourceNameKey(source)
		if uid := w.currentUID[nameKey]; uid != "" && uid != source.Identity.UID {
			w.append(&Entry{State: EntryReplaced, Summary: "Different UID observed at this name; prior object's outcome is unknown",
				HistorySource: "Current Kubernetes API observation",
				ObservedAt:    at, Source: source, PriorUID: uid})
		}
		w.currentUID[nameKey] = source.Identity.UID
		if prior == nil {
			w.append(&Entry{State: EntryObserved, Summary: sourceSummary(source), HistorySource: "Current Kubernetes API observation", ObservedAt: at, Source: source})
		} else if changes := diffFacts(prior, source); len(changes) > 0 {
			w.append(&Entry{State: EntryChanged, Summary: changeSummary(changes), HistorySource: "Difference between explicit API observations",
				ObservedAt: at, PriorObservedAt: prior.CapturedAt, Source: source, Changes: changes})
		}
		w.tracked[key] = source
	}
	w.recordCoverage(snapshot, seen, refreshErr)
	w.LastRefreshAt = at
	w.capTracked(at)
}

func (w *Window) recordCoverage(snapshot *workspace.Snapshot, seen map[string]bool, refreshErr error) {
	coverage := make(map[string]workspace.Coverage)
	for _, c := range snapshot.Coverage {
		c.Detail = safe(c.Detail)
		key := c.GVR + "\x00" + c.Namespace
		if _, duplicate := coverage[key]; duplicate {
			c.State, c.Detail = inspect.ObservationUnknown, "Duplicate query coverage is ambiguous"
		}
		if refreshErr != nil {
			c.State, c.Detail = coverageCanceled, safe(refreshErr.Error())
		}
		coverage[key] = c
		if c.State != inspect.ObservationComplete || c.Truncated {
			w.append(&Entry{State: EntryGap, Summary: "Collection " + c.State + " · " + safe(c.Detail),
				HistorySource: "Explicit query coverage", ObservedAt: snapshot.ObservedAt, Coverage: c})
		}
	}
	keys := make([]string, 0, len(w.tracked))
	for key := range w.tracked {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		prior := w.tracked[key]
		c, found := coverage[prior.Identity.GVR+"\x00"+prior.Identity.Namespace]
		if !found {
			c = workspace.Coverage{GVR: prior.Identity.GVR, Namespace: prior.Identity.Namespace, State: inspect.ObservationUnknown, Detail: "Matching query coverage not returned"}
		}
		if c.State != inspect.ObservationComplete || c.Truncated || refreshErr != nil {
			if !found {
				w.append(&Entry{State: EntryGap, Summary: c.Detail, HistorySource: "Missing query coverage", ObservedAt: snapshot.ObservedAt, Source: prior, Coverage: c})
			}
			continue
		}
		w.append(&Entry{State: EntryMissing, Summary: "Absent from a complete matching current list; deletion, health or earlier activity is not established",
			HistorySource: "Complete explicitly scoped query", ObservedAt: snapshot.ObservedAt, PriorObservedAt: prior.CapturedAt, Source: prior, Coverage: c})
		delete(w.tracked, key)
		if w.currentUID[resourceNameKey(prior)] == prior.Identity.UID {
			delete(w.currentUID, resourceNameKey(prior))
		}
	}
	if len(snapshot.Coverage) == 0 {
		detail := "No query coverage returned; source continuity unavailable"
		if refreshErr != nil {
			detail = safe(refreshErr.Error())
		}
		w.append(&Entry{State: EntryGap, Summary: detail, HistorySource: "Explicit refresh gap", ObservedAt: snapshot.ObservedAt,
			Coverage: workspace.Coverage{State: inspect.ObservationUnknown, Detail: detail}})
	}
}

func diffFacts(before, after *Source) []FieldChange {
	old := make(map[string]Fact, len(before.Facts))
	for i := range before.Facts {
		f := &before.Facts[i]
		old[factKey(f)] = *f
	}
	changes := make([]FieldChange, 0)
	for i := range after.Facts {
		f := &after.Facts[i]
		key := factKey(f)
		prior, found := old[key]
		delete(old, key)
		if found && prior.Value == f.Value && prior.SourceAt.Equal(f.SourceAt) {
			continue
		}
		oldValue := "unknown / previously unavailable"
		if found {
			oldValue = prior.Value
		}
		changes = append(changes, FieldChange{f.Group, f.Field, oldValue, f.Value, prior.SourceAt, f.SourceAt})
	}
	for _, f := range old {
		changes = append(changes, FieldChange{f.Group, f.Field, f.Value, "unknown / no longer present in this projection", f.SourceAt, time.Time{}})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Group+changes[i].Field < changes[j].Group+changes[j].Field })
	return changes
}

func sourceSummary(source *Source) string {
	if source.Job != nil {
		state, reason := source.Job.Outcome()
		return "Job " + state + " · " + reason
	}
	for i := range source.Facts {
		f := &source.Facts[i]
		if f.Group == GroupDeclaredImage || f.Group == "Source revision" || f.Group == "Chart version" {
			return f.Group + " · " + f.Value
		}
	}
	return source.Kind + " current source retained"
}

func changeSummary(changes []FieldChange) string {
	groups := make([]string, 0)
	seen := make(map[string]bool)
	for i := range changes {
		g := changes[i].Group
		if !seen[g] {
			groups = append(groups, g)
			seen[g] = true
		}
	}
	return fmt.Sprintf("%d observed field changes · %s", len(changes), strings.Join(groups, ", "))
}

func (w *Window) append(entry *Entry) {
	w.sequence++
	entry.Sequence = w.sequence
	w.Entries = append(w.Entries, *entry)
	if len(w.Entries) > MaxEntries {
		w.Dropped++
		w.PrunedBefore = w.Entries[0].ObservedAt
		w.Entries = append([]Entry(nil), w.Entries[len(w.Entries)-MaxEntries:]...)
	}
}

func (w *Window) prune(at time.Time) {
	cutoff := at.Add(-Retention)
	for key, source := range w.tracked {
		if source.CapturedAt.Before(cutoff) {
			delete(w.tracked, key)
			delete(w.currentUID, resourceNameKey(source))
			w.Dropped++
			w.PrunedBefore = cutoff
		}
	}
	for key, event := range w.events {
		if event.CapturedAt.Before(cutoff) {
			delete(w.events, key)
		}
	}
	first := 0
	for first < len(w.Entries) && w.Entries[first].ObservedAt.Before(cutoff) {
		first++
	}
	if first > 0 {
		w.Dropped += first
		w.PrunedBefore = cutoff
		w.Entries = append([]Entry(nil), w.Entries[first:]...)
	}
}

func (w *Window) capTracked(at time.Time) {
	if len(w.tracked) <= MaxTrackedSources {
		return
	}
	keys := make([]string, 0, len(w.tracked))
	for key := range w.tracked {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return w.tracked[keys[i]].CapturedAt.Before(w.tracked[keys[j]].CapturedAt) })
	for _, key := range keys[:len(keys)-MaxTrackedSources] {
		delete(w.currentUID, resourceNameKey(w.tracked[key]))
		delete(w.tracked, key)
		w.Dropped++
	}
	w.PrunedBefore = at
}

func identityKey(source *Source) string {
	return resourceNameKey(source) + "\x00" + source.Identity.UID
}
func resourceNameKey(source *Source) string {
	i := source.Identity
	return i.Context + "\x00" + i.GVR + "\x00" + i.Namespace + "\x00" + i.Name
}

func matchingCoverage(snapshot *workspace.Snapshot, source *Source, refreshErr error) workspace.Coverage {
	result := workspace.Coverage{GVR: source.Identity.GVR, Namespace: source.Identity.Namespace,
		State: inspect.ObservationUnknown, Detail: "Matching query coverage unavailable"}
	found := false
	for _, c := range snapshot.Coverage {
		c.Detail = safe(c.Detail)
		if c.GVR != result.GVR || c.Namespace != result.Namespace {
			continue
		}
		if found {
			result.State, result.Detail = inspect.ObservationUnknown, "Duplicate matching query coverage"
			break
		}
		found = true
		result = c
	}
	if refreshErr != nil {
		result.State, result.Detail = coverageCanceled, safe(refreshErr.Error())
	}
	return result
}
