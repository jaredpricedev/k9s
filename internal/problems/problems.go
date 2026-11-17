// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package problems builds one ranked, cross-kind list of resources whose own
// API status reports that they are not running cleanly. It is an observation:
// every source is read once, bounded, on request, and a source that could not
// be read is reported as such instead of being counted as healthy.
package problems

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Severity orders rows. The word, never a color, carries the meaning.
type Severity int

const (
	// Failing means the status reports a current failure.
	Failing Severity = iota
	// Degraded means the resource is partly serving or blocked.
	Degraded
	// Progressing means the status reports work that has not finished.
	Progressing
	// Warned means only a recent Warning event names the resource; its own
	// status, where it was read, reports no problem.
	Warned
)

func (s Severity) String() string {
	switch s {
	case Failing:
		return "failing"
	case Degraded:
		return "degraded"
	case Progressing:
		return "in progress"
	default:
		return "warning"
	}
}

// Source states follow the investigation vocabulary in CONTEXT.md.
const (
	StateComplete   = "complete"
	StateDenied     = "denied"
	StateIncomplete = "incomplete"
	StateUnknown    = "unknown"
)

// Source records what one bounded read obtained.
type Source struct {
	Name   string // plural, lower case: "pods"
	State  string
	Count  int    // objects obtained (for events: Warning events inside the window)
	Detail string // why the read is not complete; empty when complete
	// Support marks a read used only to resolve ownership, never listed as rows.
	Support bool
}

// EventNote is one Warning reason reported for a resource inside the window.
type EventNote struct {
	Reason, Message string
	Count           int
	Last            time.Time
}

// Row is one resource that reports a problem, with the pods it owns folded in.
type Row struct {
	Severity  Severity
	Kind      string
	Resource  schema.GroupVersionResource // zero when the kind has no known list
	Namespace string
	Name      string
	UID       types.UID
	Cause     string    // status evidence in one phrase; never an inferred root cause
	Since     time.Time // zero when the status does not say
	Signals   int
	Events    []EventNote
	Children  []Row // affected pods owned (by controller UID) by this resource

	own    int  // findings in the resource's own status
	weak   bool // the status names a state but no reason
	former bool // named only by events about an earlier UID
}

// Snapshot is one explicit observation of a namespace (or all namespaces).
type Snapshot struct {
	Namespace string // empty means all namespaces
	ReadAt    time.Time
	Window    time.Duration
	Sources   []Source
	Rows      []Row
}

// Gaps returns the sources that were not read completely.
func (s *Snapshot) Gaps() []Source {
	var gaps []Source
	for _, source := range s.Sources {
		if source.State != StateComplete {
			gaps = append(gaps, source)
		}
	}
	return gaps
}

// Affected is the number of pods folded into the row.
func (r *Row) Affected() int { return len(r.Children) }

// Key identifies a row for selection and expansion across re-reads.
func (r *Row) Key() string {
	if r.former {
		return ident(r.Kind, r.Namespace, r.Name) + "/" + string(r.UID)
	}
	return ident(r.Kind, r.Namespace, r.Name)
}

func ident(kind, namespace, name string) string { return kind + "/" + namespace + "/" + name }

// Summary is the cause phrase shown in the list, including folded pods.
func (r *Row) Summary() string {
	n := len(r.Children)
	if n == 0 {
		return r.Cause
	}
	// The count leads so it survives elision of a long cause.
	pods := plural(n, "pod") + " affected"
	if r.Cause == "" {
		return pods + ": " + r.Children[0].Cause
	}
	return pods + " · " + r.Cause
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

type objects struct {
	byKind map[string][]unstructured.Unstructured
	events []unstructured.Unstructured
}

var workloadKinds = []string{"Deployment", "StatefulSet", "DaemonSet", "Job"}

// assemble classifies every object, folds affected pods into the workload that
// controls them (matched by owner UID, never by name), attaches Warning events
// and ranks the result.
func assemble(o *objects, now time.Time, window time.Duration) []Row {
	rows := make(map[string]*Row)
	var order []string
	put := func(r *Row) *Row {
		key := r.Key()
		if existing, ok := rows[key]; ok {
			return existing
		}
		rows[key] = r
		order = append(order, key)
		return r
	}
	workloads := make(map[types.UID]*unstructured.Unstructured)
	workloadKind := make(map[types.UID]string)
	for _, kind := range workloadKinds {
		for i := range o.byKind[kind] {
			u := &o.byKind[kind][i]
			if u.GetUID() != "" {
				// The kind comes from the source that was read, not from the
				// item, which a list response may leave without one.
				workloads[u.GetUID()], workloadKind[u.GetUID()] = u, kind
			}
			if r := classify(kind, u); r != nil {
				put(r)
			}
		}
	}
	replicaSets := make(map[types.UID]*unstructured.Unstructured)
	replicaSetNames := make(map[string]*unstructured.Unstructured)
	for i := range o.byKind["ReplicaSet"] {
		u := &o.byKind["ReplicaSet"][i]
		if u.GetUID() != "" {
			replicaSets[u.GetUID()] = u
		}
		replicaSetNames[u.GetNamespace()+"/"+u.GetName()] = u
	}
	for _, kind := range []string{"PersistentVolumeClaim", "Node"} {
		for i := range o.byKind[kind] {
			if r := classify(kind, &o.byKind[kind][i]); r != nil {
				put(r)
			}
		}
	}
	for i := range o.byKind["Pod"] {
		u := &o.byKind["Pod"][i]
		r := classify("Pod", u)
		if r == nil {
			continue
		}
		owner := controllerOf(u)
		if rs, ok := replicaSets[owner]; ok && owner != "" {
			owner = controllerOf(rs)
		}
		parent, ok := workloads[owner]
		if !ok || owner == "" {
			put(r)
			continue
		}
		parentKind := workloadKind[owner]
		// A Job that reports Complete keeps the failed pods of its earlier
		// attempts. They are history the Job itself has superseded, so they
		// are not listed as a current problem of the Job.
		if parentKind == "Job" && condition(parent, "Complete", "True") != nil {
			continue
		}
		row := put(&Row{Severity: r.Severity, Kind: parentKind, Resource: resources[parentKind],
			Namespace: parent.GetNamespace(), Name: parent.GetName(), UID: parent.GetUID()})
		row.Children = append(row.Children, *r)
		row.Severity = min(row.Severity, r.Severity)
	}
	children := make(map[string]*Row) // pod identity -> folded row
	for _, row := range rows {
		for i := range row.Children {
			children[row.Children[i].Key()] = &row.Children[i]
		}
	}
	for _, group := range groupEvents(o.events, now, window) {
		kind, name := group.kind, group.name
		// A ReplicaSet warning (for example FailedCreate) is evidence about the
		// Deployment that controls it when that ownership was read.
		if rs, ok := replicaSetNames[group.namespace+"/"+name]; ok && kind == "ReplicaSet" && (group.uid == "" || group.uid == rs.GetUID()) {
			if parent, ok := workloads[controllerOf(rs)]; ok {
				kind, name, group.uid = workloadKind[parent.GetUID()], parent.GetName(), parent.GetUID()
			}
		}
		key := ident(kind, group.namespace, name)
		target := rows[key]
		if target == nil {
			target = children[key]
		}
		former := target != nil && group.uid != "" && target.UID != "" && group.uid != target.UID
		if target == nil || former {
			target = put(&Row{Severity: Warned, Kind: kind, Resource: resources[kind], Namespace: group.namespace, Name: name,
				UID: group.uid, weak: true, former: former})
			if former {
				target.Cause = "earlier identity of this name"
			}
		}
		target.Events = append(target.Events, group.notes...)
	}
	result := make([]Row, 0, len(order))
	for _, key := range order {
		row := rows[key]
		finish(row)
		for i := range row.Children {
			finish(&row.Children[i])
			row.Signals += len(row.Children[i].Events)
		}
		row.Signals += len(row.Children)
		sortRows(row.Children)
		result = append(result, *row)
	}
	sortRows(result)
	return result
}

// finish counts a row's own signals and, when the status names a state without
// a reason, quotes the most frequent Warning event beside it as an event.
func finish(r *Row) {
	sort.SliceStable(r.Events, func(i, j int) bool {
		if r.Events[i].Count != r.Events[j].Count {
			return r.Events[i].Count > r.Events[j].Count
		}
		return r.Events[i].Last.After(r.Events[j].Last)
	})
	r.Signals = r.own + len(r.Events)
	if r.weak && len(r.Events) > 0 {
		note := r.Events[0]
		quoted := "event " + note.Reason
		if note.Count > 1 {
			quoted += " x" + strconv.Itoa(note.Count)
		}
		if note.Message != "" {
			quoted += ": " + note.Message
		}
		r.Cause = strings.TrimPrefix(r.Cause+" · "+quoted, " · ")
	}
	r.Cause = bounded(r.Cause, 300)
}

var kindOrder = map[string]int{"Node": 0, "Deployment": 1, "StatefulSet": 2, "DaemonSet": 3, "Job": 4, "Pod": 5, "PersistentVolumeClaim": 6}

func sortRows(rows []Row) {
	rank := func(kind string) int {
		if order, ok := kindOrder[kind]; ok {
			return order
		}
		return len(kindOrder)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := &rows[i], &rows[j]
		switch {
		case a.Severity != b.Severity:
			return a.Severity < b.Severity
		case a.Signals != b.Signals:
			return a.Signals > b.Signals
		case rank(a.Kind) != rank(b.Kind):
			return rank(a.Kind) < rank(b.Kind)
		case a.Namespace != b.Namespace:
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}

func controllerOf(u *unstructured.Unstructured) types.UID {
	for _, ref := range u.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			return ref.UID
		}
	}
	return ""
}

func bounded(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}
