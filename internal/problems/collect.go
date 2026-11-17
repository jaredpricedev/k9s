// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package problems

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	// ListLimit bounds every read to one page. A longer list is reported as
	// incomplete; it is never paged in the background.
	ListLimit = 500
	// ReadTimeout bounds one kind; TotalDeadline bounds the whole observation.
	ReadTimeout   = 4 * time.Second
	TotalDeadline = 10 * time.Second
	// EventWindow is how far back Warning events are considered.
	EventWindow = time.Hour
)

type source struct {
	name, kind string
	namespaced bool
	support    bool
}

var sources = []source{
	{"pods", "Pod", true, false},
	{"deployments", "Deployment", true, false},
	{"replicasets", "ReplicaSet", true, true},
	{"statefulsets", "StatefulSet", true, false},
	{"daemonsets", "DaemonSet", true, false},
	{"jobs", "Job", true, false},
	{"pvcs", "PersistentVolumeClaim", true, false},
	{"nodes", "Node", false, false},
}

// Collect performs one bounded list per source, concurrently, and classifies
// what was obtained. It never retries, pages or watches. namespace "" reads all
// namespaces; Nodes are cluster-scoped and reported as denied when forbidden.
func Collect(ctx context.Context, reader dynamic.Interface, namespace string, now time.Time) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, TotalDeadline)
	defer cancel()
	snapshot := Snapshot{Namespace: namespace, ReadAt: now, Window: EventWindow, Sources: make([]Source, len(sources)+1)}
	lists := make([][]unstructured.Unstructured, len(sources)+1)
	type outcome struct {
		index  int
		source Source
		items  []unstructured.Unstructured
	}
	all := append(append([]source(nil), sources...), source{name: "warning events", kind: "Event", namespaced: true})
	last := len(sources)
	results := make(chan outcome, len(all))
	for index, s := range all {
		snapshot.Sources[index] = Source{Name: s.name, State: StateUnknown, Detail: "timed out", Support: s.support}
		go func() {
			readCtx, done := context.WithTimeout(ctx, ReadTimeout)
			defer done()
			var client dynamic.ResourceInterface = reader.Resource(resources[s.kind])
			if s.namespaced && namespace != "" {
				client = reader.Resource(resources[s.kind]).Namespace(namespace)
			}
			options := metav1.ListOptions{Limit: ListLimit}
			if index == last {
				options.FieldSelector = "type=Warning"
			}
			result := outcome{index: index, source: Source{Name: s.name, State: StateComplete, Support: s.support}}
			list, err := client.List(readCtx, options)
			switch {
			case err != nil:
				result.source.State, result.source.Detail = failure(readCtx, err)
			case list.GetContinue() != "":
				result.source.State = StateIncomplete
				result.source.Detail = fmt.Sprintf("first %d only", len(list.Items))
				fallthrough
			default:
				result.items, result.source.Count = list.Items, len(list.Items)
			}
			results <- result
		}()
	}
	// A reader that ignores its context cannot hold the observation open: at
	// the deadline whatever has not answered stays reported as timed out.
wait:
	for range all {
		select {
		case result := <-results:
			snapshot.Sources[result.index], lists[result.index] = result.source, result.items
		case <-ctx.Done():
			break wait
		}
	}

	collected := objects{byKind: make(map[string][]unstructured.Unstructured), events: lists[last]}
	for index, s := range sources {
		collected.byKind[s.kind] = lists[index]
	}
	snapshot.Rows = assemble(&collected, now, EventWindow)
	snapshot.owners = collected.owners
	recent := 0
	for i := range lists[last] {
		if _, ok := recentWarning(&lists[last][i], now, EventWindow); ok {
			recent++
		}
	}
	snapshot.Sources[last].Count = recent
	return snapshot
}

func failure(ctx context.Context, err error) (state, detail string) {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return StateDenied, ""
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil, apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return StateUnknown, "timed out"
	case apierrors.IsNotFound(err):
		return StateUnknown, "not served by this API"
	}
	return StateUnknown, bounded(err.Error(), 80)
}

type eventGroup struct {
	kind, namespace, name string
	uid                   types.UID
	notes                 []EventNote
}

// recentWarning returns when a Warning event was last observed, and whether
// that is inside the window. Servers may ignore the type field selector, so
// the type is checked again here.
func recentWarning(e *unstructured.Unstructured, now time.Time, window time.Duration) (time.Time, bool) {
	if kind, _, _ := unstructured.NestedString(e.Object, "type"); kind != "Warning" {
		return time.Time{}, false
	}
	var last time.Time
	for _, path := range [][]string{{"series", "lastObservedTime"}, {"lastTimestamp"}, {"eventTime"}, {"metadata", "creationTimestamp"}} {
		raw, _, _ := unstructured.NestedString(e.Object, path...)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if at, err := time.Parse(layout, raw); err == nil && at.After(last) {
				last = at
			}
		}
	}
	return last, !last.IsZero() && now.Sub(last) <= window
}

// groupEvents folds recent Warning events by involved object and reason.
func groupEvents(events []unstructured.Unstructured, now time.Time, window time.Duration) []*eventGroup {
	groups := make(map[string]*eventGroup)
	var order []*eventGroup
	for i := range events {
		e := &events[i]
		last, ok := recentWarning(e, now, window)
		if !ok {
			continue
		}
		field := func(path ...string) string {
			value, _, _ := unstructured.NestedString(e.Object, path...)
			return value
		}
		kind, name := field("involvedObject", "kind"), field("involvedObject", "name")
		if kind == "" || name == "" {
			continue
		}
		namespace, uid := field("involvedObject", "namespace"), types.UID(field("involvedObject", "uid"))
		key := ident(kind, namespace, name) + "/" + string(uid)
		group := groups[key]
		if group == nil {
			group = &eventGroup{kind: kind, namespace: namespace, name: name, uid: uid}
			groups[key] = group
			order = append(order, group)
		}
		wrapped := unstructured.Unstructured{Object: e.Object}
		count := int(max(number(&wrapped, 1, "count"), number(&wrapped, 0, "series", "count"), 1))
		reason := field("reason")
		merged := false
		for n := range group.notes {
			if note := &group.notes[n]; note.Reason == reason {
				note.Count += count
				if last.After(note.Last) {
					note.Last, note.Message = last, bounded(field("message"), 200)
				}
				merged = true
			}
		}
		if !merged {
			group.notes = append(group.notes, EventNote{Reason: reason, Message: bounded(field("message"), 200), Count: count, Last: last})
		}
	}
	return order
}
