// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package activity

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/workspace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	MaxEventTargets   = 16
	MaxEvents         = 128
	eventPageSize     = 64
	maxEventPages     = 2
	coverageCanceled  = "canceled"
	coverageTruncated = "truncated"
)

type EventSource struct {
	Identity, Regarding                                inspect.ResourceIdentity
	Kind, Reason, Message, Type, ReportingController   string
	Count                                              *int64
	CapturedAt, FirstAt, LastAt, EventAt, SeriesLastAt time.Time
}

type EventCollection struct {
	CapturedAt time.Time
	Events     []EventSource
	Coverage   []workspace.Coverage
	Requested  bool
}

// CollectEvents reads only core Events for at most 16 already obtained UIDs in
// the explicit app scope. Field selection is checked locally too. Labels on an
// Event need not match the related application resource's selector.
func CollectEvents(ctx context.Context, reader dynamic.Interface, scope *workspace.Scope, snapshot *workspace.Snapshot) EventCollection {
	result := EventCollection{CapturedAt: time.Now(), Requested: true}
	if scope == nil || snapshot == nil {
		result.Coverage = []workspace.Coverage{{GVR: EventGVR, State: inspect.ObservationUnknown, Detail: "Captured scope/source snapshot unavailable"}}
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	matcher := workspace.NewQueueWindow(*scope, snapshot.ObservedAt)
	targets := make([]*workspace.Resource, 0)
	seen := make(map[string]bool)
	for i := range snapshot.Resources {
		r := &snapshot.Resources[i]
		key := r.Ref.GVR + "/" + r.Ref.Namespace + "/" + r.Ref.Name + "/" + r.Ref.UID
		if !matcher.Contains(&r.Ref) || seen[key] || r.Object == nil || r.Object.GetKind() == "Event" {
			continue
		}
		if Project(r, scope.Context, snapshot.ObservedAt) == nil {
			continue
		}
		seen[key] = true
		targets = append(targets, r)
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Ref.Namespace+"/"+targets[i].Ref.Name < targets[j].Ref.Namespace+"/"+targets[j].Ref.Name
	})
	if len(targets) == 0 {
		result.Coverage = append(result.Coverage, workspace.Coverage{GVR: EventGVR, State: inspect.ObservationUnknown,
			Detail: "No verified captured target UID available; no Event read attempted"})
		return result
	}
	if len(targets) > MaxEventTargets {
		result.Coverage = append(result.Coverage, workspace.Coverage{GVR: EventGVR, State: coverageTruncated, Truncated: true,
			Detail: fmt.Sprintf("Only the first %d of %d verified scoped UIDs queried; narrow the app scope", MaxEventTargets, len(targets))})
	}
	for _, target := range targets[:min(MaxEventTargets, len(targets))] {
		if len(result.Events) >= MaxEvents {
			result.Coverage = append(result.Coverage, workspace.Coverage{GVR: EventGVR, Namespace: target.Ref.Namespace,
				State: coverageTruncated, Truncated: true, Detail: "Activity Event retention limit reached"})
			break
		}
		collectTargetEvents(ctx, reader, scope.Context, target, &result)
	}
	return result
}

func collectTargetEvents(ctx context.Context, reader dynamic.Interface, contextName string, target *workspace.Resource, result *EventCollection) {
	c := workspace.Coverage{GVR: EventGVR, Namespace: target.Ref.Namespace}
	defer func() {
		c.Detail = fmt.Sprintf("%s %s/%s UID=%s: %s", target.Ref.GVR, target.Ref.Namespace, target.Ref.Name, target.Ref.UID, c.Detail)
		result.Coverage = append(result.Coverage, c)
	}()
	if reader == nil {
		c.State, c.Detail = "unavailable", "Event reader unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	continuation := ""
	invalid := 0
	for range maxEventPages {
		if err := ctx.Err(); err != nil {
			c.State, c.Detail = coverageCanceled, safe(err.Error())
			return
		}
		objects, err := reader.Resource(schema.GroupVersionResource{Version: "v1", Resource: "events"}).Namespace(target.Ref.Namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", target.Ref.UID).String(), Limit: eventPageSize, Continue: continuation})
		if ctxErr := ctx.Err(); ctxErr != nil {
			c.State, c.Detail = coverageCanceled, safe(ctxErr.Error())
			return
		}
		if err != nil {
			c.State = "unavailable"
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				c.State = "denied"
			}
			if ctx.Err() != nil {
				c.State = coverageCanceled
			}
			c.Detail = safe(err.Error())
			return
		}
		if objects == nil {
			c.State, c.Detail = inspect.ObservationUnknown, "Event query returned no response"
			return
		}
		for i := range objects.Items[:min(eventPageSize, len(objects.Items))] {
			event := projectEvent(&objects.Items[i], target, contextName, result.CapturedAt)
			if event != nil {
				result.Events = append(result.Events, *event)
			} else {
				invalid++
			}
			if len(result.Events) >= MaxEvents {
				c.State, c.Detail, c.Truncated = coverageTruncated, "Event retention cap reached; current matching records are incomplete", true
				return
			}
		}
		if len(objects.Items) > eventPageSize {
			c.State, c.Detail, c.Truncated = coverageTruncated, "Server ignored Event page limit; only the first 64 candidates retained", true
			return
		}
		continuation = objects.GetContinue()
		if continuation == "" {
			c.State = inspect.ObservationComplete
			c.Detail = "Current retained Events for captured UID " + target.Ref.UID + "; TTL/aggregation/deletion mean earlier activity is unavailable"
			if invalid > 0 {
				c.State = inspect.ObservationUnknown
				c.Detail = fmt.Sprintf("%d returned Event records did not match the captured UID identity and were excluded", invalid)
			}
			return
		}
	}
	c.State, c.Detail, c.Truncated = coverageTruncated, "Two-page Event query cap reached; retained Events are incomplete", true
}

func projectEvent(object *unstructured.Unstructured, target *workspace.Resource, contextName string, at time.Time) *EventSource {
	if object.GetAPIVersion() != "v1" || object.GetKind() != "Event" || object.GetNamespace() != target.Ref.Namespace || object.GetUID() == "" || object.GetName() == "" {
		return nil
	}
	r, _, _ := unstructured.NestedMap(object.Object, "involvedObject")
	if text(r, "uid") != target.Ref.UID || text(r, "name") != target.Ref.Name || text(r, "namespace") != target.Ref.Namespace ||
		text(r, "kind") != target.Object.GetKind() || text(r, "apiVersion") != target.Object.GetAPIVersion() {
		return nil
	}
	e := &EventSource{
		Identity: inspect.ResourceIdentity{Context: contextName, GVR: EventGVR, Namespace: object.GetNamespace(),
			Name: object.GetName(), UID: string(object.GetUID())},
		Regarding: inspect.ResourceIdentity{Context: contextName, GVR: target.Ref.GVR, Namespace: target.Ref.Namespace,
			Name: target.Ref.Name, UID: target.Ref.UID}, Kind: target.Kind,
		Reason: field(object, "reason"), Message: field(object, "message"), Type: field(object, "type"),
		ReportingController: field(object, "reportingComponent"), CapturedAt: at,
		FirstAt: eventTime(object, "firstTimestamp"), LastAt: eventTime(object, "lastTimestamp"),
		EventAt: eventTime(object, "eventTime"), SeriesLastAt: eventTime(object, "series", "lastObservedTime"),
	}
	if count, found, _ := unstructured.NestedInt64(object.Object, "count"); found && count >= 0 {
		e.Count = &count
	}
	if count, found, _ := unstructured.NestedInt64(object.Object, "series", "count"); found && count >= 0 {
		e.Count = &count
	}
	return e
}

func (w *Window) ObserveEvents(collection *EventCollection) {
	if collection == nil || !collection.Requested {
		w.append(&Entry{State: EntryGap, Summary: "Events not collected on this refresh; open Activity and r to query captured UIDs",
			HistorySource: "Event collection not requested", ObservedAt: w.LastRefreshAt,
			Coverage: workspace.Coverage{GVR: EventGVR, State: inspect.ObservationUnknown, Detail: "Current resource lists are not complete application history"}})
		return
	}
	if collection.CapturedAt.Before(w.StartedAt) || collection.CapturedAt.Before(w.LastRefreshAt) {
		return
	}
	for i := range collection.Events {
		event := &collection.Events[i]
		ref := workspace.ResourceRef{GVR: event.Regarding.GVR, Namespace: event.Regarding.Namespace, Name: event.Regarding.Name, UID: event.Regarding.UID}
		tracked := w.tracked[w.Context+"\x00"+ref.GVR+"\x00"+ref.Namespace+"\x00"+ref.Name+"\x00"+ref.UID]
		if event.Identity.Context != w.Context || event.Regarding.Context != w.Context || event.Identity.UID == "" ||
			event.Identity.GVR != EventGVR || event.Identity.Namespace != ref.Namespace || event.Identity.Name == "" ||
			!w.matcher.Contains(&ref) || tracked == nil {
			continue
		}
		key := event.Identity.Context + "/" + event.Identity.Namespace + "/" + event.Identity.Name + "/" + event.Identity.UID
		prior := w.events[key]
		if prior != nil && sameEvent(prior, event) {
			continue
		}
		retained := *event
		retained.Kind = tracked.Kind
		retained.Reason, retained.Message, retained.Type = safe(event.Reason), safe(event.Message), safe(event.Type)
		retained.ReportingController = safe(event.ReportingController)
		if event.Count != nil {
			count := *event.Count
			retained.Count = &count
		}
		w.events[key] = &retained
		summary := retained.Type + " · " + retained.Reason + " · " + retained.Message
		if prior != nil {
			summary = "Aggregated Event changed · " + summary
		}
		w.append(&Entry{State: EntryEvent, Summary: summary,
			HistorySource: "Retained Kubernetes Event; API timestamps and aggregation are source evidence", ObservedAt: collection.CapturedAt, Event: &retained})
	}
	for _, c := range collection.Coverage {
		c.Detail = safe(c.Detail)
		if c.State != inspect.ObservationComplete || c.Truncated {
			w.append(&Entry{State: EntryGap, Summary: "Events " + c.State + " · " + c.Detail,
				HistorySource: "Bounded UID Event query", ObservedAt: collection.CapturedAt, Coverage: c})
		}
	}
	if len(w.events) > MaxEvents {
		keys := make([]string, 0, len(w.events))
		for key := range w.events {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return w.events[keys[i]].CapturedAt.Before(w.events[keys[j]].CapturedAt) })
		for _, key := range keys[:len(keys)-MaxEvents] {
			delete(w.events, key)
			w.Dropped++
		}
		w.PrunedBefore = collection.CapturedAt
	}
}

func sameEvent(a, b *EventSource) bool {
	countSame := a.Count == nil && b.Count == nil || a.Count != nil && b.Count != nil && *a.Count == *b.Count
	return countSame && a.Regarding == b.Regarding && a.Reason == b.Reason && a.Message == b.Message && a.Type == b.Type &&
		a.ReportingController == b.ReportingController && a.FirstAt.Equal(b.FirstAt) &&
		a.LastAt.Equal(b.LastAt) && a.EventAt.Equal(b.EventAt) && a.SeriesLastAt.Equal(b.SeriesLastAt)
}
func text(m map[string]any, key string) string { value, _ := m[key].(string); return value }
func field(object *unstructured.Unstructured, path ...string) string {
	value, _, _ := unstructured.NestedString(object.Object, path...)
	return safe(value)
}
func eventTime(object *unstructured.Unstructured, path ...string) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, field(object, path...))
	return at
}
