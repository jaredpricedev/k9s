// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package timeline merges timed observations about one workload, or one
// namespace, into a single chronological list. It orders evidence; it never
// asserts that one entry caused another.
package timeline

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source names the kind of evidence an entry was derived from.
type Source string

const (
	SourceEvent      Source = "Event"
	SourceRollout    Source = "Rollout"
	SourceRestart    Source = "Restart"
	SourceConfig     Source = "Config"
	SourceAutoscaler Source = "Autoscaler"
	SourceNode       Source = "Node"
	SourceCondition  Source = "Condition"
	// SourceAnnotation is a Grafana annotation: something a person or a
	// pipeline wrote down, not something read from the cluster.
	SourceAnnotation Source = "Annotation"
)

// Tag is the fixed-width list label for a source.
func (s Source) Tag() string {
	switch s {
	case SourceAutoscaler:
		return "HPA"
	case SourceCondition:
		return "STATUS"
	case SourceAnnotation:
		return "ANNOT"
	default:
		return strings.ToUpper(string(s))
	}
}

func (s Source) rank() int {
	for index, source := range []Source{SourceCondition, SourceEvent, SourceRestart, SourceNode, SourceAutoscaler, SourceRollout, SourceConfig, SourceAnnotation} {
		if s == source {
			return index
		}
	}
	return 99
}

// Ref identifies the resource an entry concerns. Resource is the API resource
// in group/version/resource form, as typed commands accept it.
type Ref struct {
	Resource, Kind, Namespace, Name string
}

var shortKinds = map[string]string{
	"HorizontalPodAutoscaler": "hpa", "Deployment": "deploy", "ReplicaSet": "rs", "ConfigMap": "cm",
	"StatefulSet": "sts", "DaemonSet": "ds", "PersistentVolumeClaim": "pvc", "CronJob": "cj", "Service": "svc",
}

// Short renders kind/name for one-line display, using the familiar kubectl
// short names so the name keeps most of the width.
func (r Ref) Short() string {
	kind := shortKinds[r.Kind]
	if kind == "" {
		kind = strings.ToLower(r.Kind)
	}
	if r.Name == "" {
		return kind
	}
	return kind + "/" + r.Name
}

// Entry is one timed observation. At is when the entry began: the first
// occurrence of a repeated event, a ReplicaSet's creation, a container exit.
type Entry struct {
	At, Last time.Time
	Count    int
	Source   Source
	Origin   string
	Ref      Ref
	Summary  string
	Detail   []string
	// Failing marks a failing signal. Change marks a change to desired state or
	// configuration. Neither field states a cause.
	Failing, Change bool
}

// Coverage states use the investigation vocabulary in CONTEXT.md.
const (
	Complete   = "complete"
	Incomplete = "incomplete"
	Denied     = "denied"
	Unknown    = "unknown"
)

// Coverage reports what one bounded read could establish.
type Coverage struct {
	Source, State, Detail string
	Count                 int
}

// Scope is the captured request. An empty Kind means the whole namespace.
type Scope struct {
	Context, Namespace        string
	Resource, Kind, Name, UID string
}

// Label names the scope in one phrase.
func (s *Scope) Label() string {
	if s.Kind == "" {
		return "namespace " + s.Namespace
	}
	return s.Kind + " " + s.Namespace + "/" + s.Name
}

// Snapshot is a retained observation. Entries are ordered newest first.
type Snapshot struct {
	Scope      Scope
	CapturedAt time.Time
	Entries    []Entry
	Coverage   []Coverage
	Onset      int
	// Chosen is true when the user picked the reference moment; otherwise
	// Onset is the earliest failing signal that was read.
	Chosen      bool
	OldestEvent time.Time
	EventsRead  int
	// Untimed lists failing status that carries no time and so cannot be
	// placed in the order.
	Untimed []string
	Notes   []string
}

// Relation places an entry against the first failing signal.
type Relation int

const (
	RelationNone Relation = iota
	RelationBefore
	RelationOnset
	RelationAfter
)

// Windows are the selectable observation windows; zero means everything read.
var Windows = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// finish orders entries and finds the first failing signal.
func (s *Snapshot) finish() {
	sort.SliceStable(s.Entries, func(i, j int) bool {
		a, b := &s.Entries[i], &s.Entries[j]
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		if a.Source != b.Source {
			return a.Source.rank() < b.Source.rank()
		}
		if a.Ref.Short() != b.Ref.Short() {
			return a.Ref.Short() < b.Ref.Short()
		}
		return a.Summary < b.Summary
	})
	s.ResetReference()
}

// ResetReference returns to the earliest failing signal that was read.
func (s *Snapshot) ResetReference() {
	s.Onset, s.Chosen = -1, false
	for index := range s.Entries {
		// Entries are newest first, so the last failing entry is the earliest.
		if s.Entries[index].Failing {
			s.Onset = index
		}
	}
}

// SetReference makes one entry the moment others are compared with. An old,
// unrelated warning can otherwise be mistaken for the start of this fault.
func (s *Snapshot) SetReference(index int) {
	if index >= 0 && index < len(s.Entries) {
		s.Onset, s.Chosen = index, true
	}
}

// Moment names what entries are compared with.
func (s *Snapshot) Moment() string {
	if s.Chosen {
		return "the reference moment you chose"
	}
	return "the first failing signal"
}

// OnsetEntry returns the first failing signal that could be read, if any.
func (s *Snapshot) OnsetEntry() *Entry {
	if s == nil || s.Onset < 0 || s.Onset >= len(s.Entries) {
		return nil
	}
	return &s.Entries[s.Onset]
}

// Relation reports whether an entry began before or after the first failing
// signal, and by how much. It is an ordering, never a cause.
func (s *Snapshot) Relation(index int) (Relation, time.Duration) {
	onset := s.OnsetEntry()
	if onset == nil || index < 0 || index >= len(s.Entries) {
		return RelationNone, 0
	}
	if index == s.Onset {
		return RelationOnset, 0
	}
	at := s.Entries[index].At
	if at.Before(onset.At) {
		return RelationBefore, onset.At.Sub(at)
	}
	return RelationAfter, at.Sub(onset.At)
}

// RelationLabel is the short list form of Relation.
func (s *Snapshot) RelationLabel(index int) string {
	relation, delta := s.Relation(index)
	switch relation {
	case RelationOnset:
		if s.Chosen {
			return "REFERENCE"
		}
		return "FAULT BEGAN"
	case RelationBefore:
		return Coarse(delta) + " before"
	case RelationAfter:
		if delta < time.Second {
			return "same time"
		}
		return Coarse(delta) + " after"
	default:
		return ""
	}
}

// RelationSentence is the full wording. It says "preceded", never "caused".
func (s *Snapshot) RelationSentence(index int) string {
	relation, delta := s.Relation(index)
	switch relation {
	case RelationOnset:
		if s.Chosen {
			return "Reference moment you chose; other entries are labelled before or after it."
		}
		return "First failing signal that could be read. The fault may have begun earlier than the evidence shows."
	case RelationBefore:
		if s.Entries[index].Change {
			return "This change preceded " + s.Moment() + " by " + Span(delta) + ". Order is not cause."
		}
		return "Began " + Span(delta) + " before " + s.Moment() + "."
	case RelationAfter:
		if delta < time.Second {
			return "Recorded at the same second as " + s.Moment() + "; their order is unknown."
		}
		return "Began " + Span(delta) + " after " + s.Moment() + "."
	default:
		return "No failing signal was read, so entries are not labelled before or after."
	}
}

// Visible returns entry indexes inside the window that match the query.
// A zero window keeps everything that was read.
func (s *Snapshot) Visible(window time.Duration, query string) []int {
	query = strings.ToLower(strings.TrimSpace(query))
	var visible []int
	for index := range s.Entries {
		entry := &s.Entries[index]
		if window > 0 && entry.At.Before(s.CapturedAt.Add(-window)) && (entry.Last.IsZero() || entry.Last.Before(s.CapturedAt.Add(-window))) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.searchText()), query) {
			continue
		}
		visible = append(visible, index)
	}
	return visible
}

func (e *Entry) searchText() string {
	return e.Source.Tag() + " " + string(e.Source) + " " + e.Ref.Short() + " " + e.Summary + " " + strings.Join(e.Detail, " ")
}

// Partial reports whether any source fell short of a complete read.
func (s *Snapshot) Partial() bool {
	for _, coverage := range s.Coverage {
		if coverage.State != Complete {
			return true
		}
	}
	return len(s.Untimed) > 0
}

// Gaps lists the sources that were not read completely, for a one-line state.
func (s *Snapshot) Gaps() string {
	var gaps []string
	for _, coverage := range s.Coverage {
		if coverage.State != Complete {
			gaps = append(gaps, strings.ToLower(coverage.Source)+" "+coverage.State)
		}
	}
	return strings.Join(gaps, ", ")
}

// EventHorizon states how far back event evidence reaches. Kubernetes expires
// events, so a missing older event is not evidence that nothing happened.
func (s *Snapshot) EventHorizon() string {
	for _, coverage := range s.Coverage {
		if coverage.Source == sourceEvents && coverage.State != Complete && coverage.State != Incomplete {
			return "Events could not be read (" + coverage.State + "); event history is unknown, not empty."
		}
	}
	if s.EventsRead == 0 || s.OldestEvent.IsZero() {
		return "No events were returned. Kubernetes expires events (commonly after 1h); this is not evidence that nothing happened."
	}
	return fmt.Sprintf("Oldest event read began %s (%s before capture). Kubernetes expires events (commonly after 1h); older event history is unavailable, not empty.",
		Stamp(s.OldestEvent), Span(s.CapturedAt.Sub(s.OldestEvent)))
}

// Stamp renders an exact UTC time.
func Stamp(t time.Time) string {
	if t.IsZero() {
		return "time unknown"
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

// Clock renders the time of day, with the date only when it differs from the
// capture day.
func Clock(t, captured time.Time) string {
	t, captured = t.UTC(), captured.UTC()
	if t.Year() == captured.Year() && t.YearDay() == captured.YearDay() {
		return t.Format("15:04:05")
	}
	return t.Format("Jan02 15h")
}

// Coarse renders a duration in its largest unit only, rounding down: 45s,
// 12m, 3h, 2d. It is for narrow columns; exact times are shown alongside.
func Coarse(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// Span renders a duration compactly: 45s, 12m, 3h10m, 2d4h.
func Span(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		if seconds := int(d.Seconds()) % 60; seconds != 0 && d < 10*time.Minute {
			return fmt.Sprintf("%dm%ds", int(d.Minutes()), seconds)
		}
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		if minutes := int(d.Minutes()) % 60; minutes != 0 && d < 10*time.Hour {
			return fmt.Sprintf("%dh%dm", int(d.Hours()), minutes)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		days := int(d.Hours()) / 24
		if hours := int(d.Hours()) % 24; hours != 0 && days < 10 {
			return fmt.Sprintf("%dd%dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}
}

// Report is the plain-text statement of the observation window, what was
// read, and what could not be established.
func (s *Snapshot) Report(window time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TIMELINE OBSERVATION\nScope: %s\nContext: %s\nCaptured: %s\n", s.Scope.Label(), s.Scope.Context, Stamp(s.CapturedAt))
	if window > 0 {
		fmt.Fprintf(&b, "Window shown: last %s before capture (%d of %d entries)\n", Span(window), len(s.Visible(window, "")), len(s.Entries))
	} else {
		fmt.Fprintf(&b, "Window shown: everything that was read (%d entries)\n", len(s.Entries))
	}
	if len(s.Entries) > 0 {
		oldest := s.Entries[len(s.Entries)-1].At
		fmt.Fprintf(&b, "Oldest entry: %s (%s before capture)\n", Stamp(oldest), Span(s.CapturedAt.Sub(oldest)))
	}
	b.WriteString("\n" + strings.ToUpper(strings.TrimPrefix(s.Moment(), "the ")) + "\n")
	if onset := s.OnsetEntry(); onset != nil {
		fmt.Fprintf(&b, "%s  %s  %s\n%s\n", Stamp(onset.At), onset.Ref.Short(), onset.Summary, s.RelationSentence(s.Onset))
		b.WriteString("Entries are labelled before or after this moment. An earlier entry preceded it; that is order, not cause.\n")
	} else {
		b.WriteString("None read. This is not evidence of health: only the sources below were read, within their limits.\n")
	}
	b.WriteString("\nSOURCES\n")
	for _, coverage := range s.Coverage {
		fmt.Fprintf(&b, "%-22s %-10s %s\n", coverage.Source, strings.ToUpper(coverage.State), coverage.Detail)
	}
	b.WriteString("\nLIMITS\n")
	b.WriteString("- " + s.EventHorizon() + "\n")
	for _, note := range append(append([]string(nil), s.Notes...), standingLimits...) {
		b.WriteString("- " + note + "\n")
	}
	if len(s.Untimed) > 0 {
		b.WriteString("\nFAILING STATUS WITHOUT A TIME (not placed in the order)\n")
		for _, item := range s.Untimed {
			b.WriteString("- " + item + "\n")
		}
	}
	return b.String()
}

var standingLimits = []string{
	"Repeated events are merged by type, reason and kind; the entry is placed at the first occurrence that is still retained.",
	"Rollout entries use ReplicaSet creation time. A rollback reuses an older ReplicaSet, so its time is not the rollback time. Revisions pruned by revisionHistoryLimit are unavailable. StatefulSet and DaemonSet revisions are not read.",
	"Pod status retains only the most recent exit of each container; earlier restarts are counted but not timed.",
	"ConfigMap and Secret entries come from metadata.managedFields: one time per manager and operation, the last write only. Values are never read into the view.",
	"Node conditions report the last transition only. Node events are not read.",
	"Times are those the API objects carry, to the second. Entries in the same second have no known order.",
}

// AddExternal merges entries read from outside the cluster, with the
// coverage of that read, and restores the order. The first failing signal is
// found again, so a reference the user chose is not kept.
func (s *Snapshot) AddExternal(entries []Entry, coverage *Coverage, notes ...string) {
	s.Entries = append(s.Entries, entries...)
	if coverage != nil {
		s.Coverage = append(s.Coverage, *coverage)
	}
	s.Notes = append(s.Notes, notes...)
	s.finish()
}

// NewSnapshot orders entries and finds the first failing signal. Collect uses
// the same step; this constructor serves callers that already hold entries.
func NewSnapshot(scope *Scope, capturedAt time.Time, entries []Entry, coverage []Coverage) *Snapshot {
	s := &Snapshot{Scope: *scope, CapturedAt: capturedAt, Entries: entries, Coverage: coverage}
	s.finish()
	return s
}
