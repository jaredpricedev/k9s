// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/timeline"
)

const (
	// timelineAnnotationSpan is how far back annotations are read: the
	// widest window the timeline can show.
	timelineAnnotationSpan  = 24 * time.Hour
	timelineAnnotationsName = "Grafana annotations"
	// annotationLinkMargin is the time shown either side of an annotation
	// when its dashboard is opened.
	annotationLinkMargin = 15 * time.Minute
)

// timelineAnnotations is the outcome of the one annotation read made with
// a timeline. declared is false when no Grafana is declared, which is not a
// gap in the read: nothing was asked.
type timelineAnnotations struct {
	declared bool
	result   observability.AnnotationsResult
}

// readTimelineAnnotations makes one bounded GET of the annotations in the
// 24h before now. It never writes an annotation.
func readTimelineAnnotations(ctx context.Context, sources *observability.Sources, restConfig observability.RESTConfigFunc, now time.Time) *timelineAnnotations {
	if sources == nil || sources.Grafana == nil {
		return &timelineAnnotations{}
	}
	out := &timelineAnnotations{declared: true}
	grafana, st := observability.Resolve(sources, observability.KindGrafana, restConfig)
	if grafana == nil {
		out.result.Status = st
		return out
	}
	defer grafana.Close()
	out.result = grafana.Annotations(ctx, observability.AnnotationQuery{From: now.Add(-timelineAnnotationSpan), To: now})
	return out
}

// mergeInto adds the annotations that name the snapshot's scope and states
// what the read established. An unreadable source is a gap, never "nothing
// was annotated".
func (a *timelineAnnotations) mergeInto(s *timeline.Snapshot) {
	if !a.declared {
		s.AddExternal(nil, nil, "Grafana is not declared for this context, so no annotations were read. :sources shows how to declare it.")
		return
	}
	r := &a.result
	coverage := timeline.Coverage{Source: timelineAnnotationsName}
	switch r.State {
	case observability.StateComplete, observability.StateIncomplete:
		entries, elsewhere := annotationEntries(&s.Scope, snapshotNames(s), snapshotNodes(s), r.Annotations)
		coverage.State, coverage.Count = timeline.Complete, len(entries)
		coverage.Detail = fmt.Sprintf("%d in the last 24h, %d name this scope by tag or text, %d not shown (limit %d)",
			len(r.Annotations), len(entries), elsewhere, observability.MaxAnnotations)
		if r.State == observability.StateIncomplete {
			coverage.State = timeline.Incomplete
			coverage.Detail += "; " + r.Reason
		}
		s.AddExternal(entries, &coverage,
			"Grafana annotations are what a person or a pipeline wrote down. They are matched to this scope by tag or text only; one that names neither the namespace nor the workload is not shown.")
		return
	case observability.StateDenied:
		coverage.State = timeline.Denied
	default:
		coverage.State = timeline.Unknown
	}
	coverage.Detail = string(r.State)
	if r.Reason != "" {
		coverage.Detail += " (" + r.Reason + ")"
	}
	coverage.Detail += "; annotations are unknown, not absent. :sources probes the declaration."
	s.AddExternal(nil, &coverage)
}

// snapshotNodes are the nodes the cluster read placed in the timeline.
func snapshotNodes(s *timeline.Snapshot) map[string]bool {
	nodes := map[string]bool{}
	for i := range s.Entries {
		if s.Entries[i].Ref.Kind == "Node" {
			nodes[s.Entries[i].Ref.Name] = true
		}
	}
	return nodes
}

// snapshotNames are the names an annotation can use for a workload scope:
// the selected object and the controllers the cluster read tied to it, so
// a Pod's timeline shows what was annotated for its Deployment. A namespace
// scope has none: it is matched by namespace.
func snapshotNames(s *timeline.Snapshot) []string {
	if s.Scope.Kind == "" {
		return nil
	}
	names, seen := []string{s.Scope.Name}, map[string]bool{s.Scope.Name: true}
	for i := range s.Entries {
		ref := &s.Entries[i].Ref
		switch ref.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job":
			if ref.Name != "" && !seen[ref.Name] {
				seen[ref.Name] = true
				names = append(names, ref.Name)
			}
		}
	}
	return names
}

// tagPair splits a tag such as namespace:apps or node=worker-b.
func tagPair(tag string) (key, value string, ok bool) {
	index := strings.IndexAny(tag, ":=")
	if index <= 0 || index == len(tag)-1 {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(tag[:index])), strings.TrimSpace(tag[index+1:]), true
}

// mentions reports whether text contains name as a whole Kubernetes name:
// the characters around it must not continue a name.
func mentions(text, name string) bool {
	if name == "" {
		return false
	}
	part := func(b byte) bool {
		return b == '-' || b == '.' || b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
	}
	for start := 0; ; {
		index := strings.Index(text[start:], name)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(name)
		if (index == 0 || !part(text[index-1])) && (end == len(text) || !part(text[end]) || (text[end] == '.' && (end+1 == len(text) || !part(text[end+1])))) {
			return true
		}
		start = index + 1
	}
}

// annotationMatch says how an annotation names the scope, or "" when it
// does not. Only tags and text are used; nothing is inferred from time.
func annotationMatch(scope *timeline.Scope, names []string, nodes map[string]bool, a *observability.Annotation) string {
	namespace := scope.Namespace
	named := func(value string) bool {
		for _, name := range names {
			if value == name {
				return true
			}
		}
		return false
	}
	otherNamespace, tagged := false, ""
	for _, tag := range a.Tags {
		key, value, ok := tagPair(tag)
		switch {
		case named(tag) || (ok && named(value)):
			if tagged == "" {
				tagged = "tag " + tag
			}
		case !ok:
		case key == "namespace" || key == "ns" || key == "kubernetes_namespace":
			if value != namespace {
				otherNamespace = true
			}
		case (key == "node" || key == "instance") && nodes[value] && len(names) == 0:
			if tagged == "" {
				tagged = "tag " + tag + " (a node in this timeline)"
			}
		}
	}
	if otherNamespace {
		return ""
	}
	if tagged != "" {
		return tagged
	}
	if len(names) > 0 {
		for _, name := range names {
			if mentions(a.Text, name) {
				return "text names " + name
			}
		}
		return ""
	}
	for _, tag := range a.Tags {
		if key, value, ok := tagPair(tag); ok && value == namespace && (key == "namespace" || key == "ns" || key == "kubernetes_namespace") {
			return "tag " + tag
		}
	}
	if mentions(a.Text, namespace+"/") || strings.Contains(a.Text, "namespace="+namespace) || strings.Contains(a.Text, "namespace "+namespace) {
		return "text names namespace " + namespace
	}
	return ""
}

// annotationEntries turns the annotations that name the scope into timeline
// entries. They are neither failing signals nor changes: an annotation is a
// statement by its author, so it never becomes the fault's start.
func annotationEntries(scope *timeline.Scope, names []string, nodes map[string]bool, notes []observability.Annotation) (entries []timeline.Entry, notShown int) {
	for i := range notes {
		a := &notes[i]
		match := annotationMatch(scope, names, nodes, a)
		if match == "" {
			notShown++
			continue
		}
		entry := timeline.Entry{At: a.Time, Last: a.TimeEnd, Source: timeline.SourceAnnotation,
			Ref:    timeline.Ref{Kind: "Grafana", Name: a.DashboardUID},
			Origin: fmt.Sprintf("Grafana annotation %d, the time its author gave it", a.ID)}
		text := strings.Join(strings.Fields(a.Text), " ")
		switch {
		case a.AlertName != "" && a.NewState != "":
			entry.Summary = fmt.Sprintf("Alert %s: %s to %s", a.AlertName, orUnknown(a.PrevState), a.NewState)
			if text != "" {
				entry.Detail = append(entry.Detail, text)
			}
		case text != "":
			entry.Summary = text
		default:
			entry.Summary = "(annotation without text)"
		}
		if !a.TimeEnd.IsZero() {
			entry.Detail = append(entry.Detail, fmt.Sprintf("region until %s (%s)", timeline.Stamp(a.TimeEnd), timeline.Span(a.TimeEnd.Sub(a.Time))))
		}
		if len(a.Tags) > 0 {
			entry.Detail = append(entry.Detail, "tags "+strings.Join(a.Tags, ", "))
		}
		entry.Detail = append(entry.Detail, "in scope by "+match)
		if a.DashboardUID == "" {
			entry.Detail = append(entry.Detail, "not attached to a dashboard")
		}
		entries = append(entries, entry)
	}
	return entries, notShown
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// openAnnotation hands off to the dashboard an annotation is attached to,
// around the annotation's own time.
func (v *timelineView) openAnnotation(entry *timeline.Entry) {
	sources := declaredSources(v.app)
	if reason := grafanaUnavailable(sources); reason != "" {
		showGrafanaHandoff(v.app, &grafanaHandoff{problem: reason})
		return
	}
	if entry.Ref.Name == "" {
		v.app.Flash().Warn("This annotation is not attached to a dashboard; Shift-D opens the dashboard for the timeline's scope")
		return
	}
	end := entry.At
	if entry.Last.After(end) {
		end = entry.Last
	}
	target := &grafanaTarget{Label: "Grafana annotation: " + entry.Summary,
		Window: grafanaWindow{From: entry.At.Add(-annotationLinkMargin), To: end.Add(annotationLinkMargin),
			Reason: "15m either side of the annotation"}}
	link := grafanaLink{Name: "Dashboard", Detail: entry.Ref.Name + " · the dashboard the annotation is attached to",
		Fills: "no variable: the annotation does not carry them"}
	url, err := sources.Grafana.DashboardUIDURL(entry.Ref.Name, target.Window.linkRange())
	if err != nil {
		link.Unavailable = observability.Clean(err.Error(), 200)
	} else {
		link.URL = url
	}
	showGrafanaHandoff(v.app, &grafanaHandoff{target: target, links: []grafanaLink{link}, deliverNow: true})
}

// GrafanaSelection makes a hand-off from the timeline about its scope: the
// workload, or the namespace, rather than the focused entry.
func (v *timelineView) GrafanaSelection() SelectedResourceTarget {
	target := SelectedResourceTarget{Context: v.scope.Context}
	if v.scope.Kind != "" {
		target.GVR, target.Namespace, target.Name = client.NewGVR(v.scope.Resource), v.scope.Namespace, v.scope.Name
		return target
	}
	target.GVR, target.Name = client.NsGVR, v.scope.Namespace
	return target
}

// GrafanaWindow is the timeline's current window, ending at the capture so
// the dashboard shows what the timeline shows.
func (v *timelineView) GrafanaWindow() (grafanaWindow, bool) {
	s := v.snapshot
	if s == nil {
		return grafanaWindow{}, false
	}
	if v.window > 0 {
		return grafanaWindow{From: s.CapturedAt.Add(-v.window), To: s.CapturedAt,
			Reason: "the timeline's window, last " + timeline.Span(v.window) + " before capture"}, true
	}
	if len(s.Entries) == 0 {
		return grafanaWindow{}, false
	}
	from := s.Entries[len(s.Entries)-1].At
	if s.CapturedAt.Sub(from) > observability.MaxWindow {
		return grafanaWindow{From: s.CapturedAt.Add(-observability.MaxWindow), To: s.CapturedAt,
			Reason: "the 7d before the timeline's capture; older entries exist"}, true
	}
	return grafanaWindow{From: from.Add(-time.Minute), To: s.CapturedAt, Reason: "everything the timeline read, up to its capture"}, true
}
