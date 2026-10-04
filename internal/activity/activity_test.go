// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func activityFixture() (workspace.Scope, workspace.Snapshot) {
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "apps", "name": "api", "uid": "deploy-a", "resourceVersion": "1", "generation": int64(2)},
		"spec":   map[string]any{"replicas": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "repo/api:v1", "env": []any{map[string]any{"name": "TOKEN", "value": "ENV-SHOULD-NOT-APPEAR"}}}}}}},
		"status": map[string]any{"observedGeneration": int64(2), "readyReplicas": int64(1), "updatedReplicas": int64(2), "conditions": []any{map[string]any{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable", "lastTransitionTime": "2026-10-04T09:30:00Z"}}},
	}}
	ref := workspace.ResourceRef{GVR: "apps/v1/deployments", Namespace: "apps", Name: "api", UID: "deploy-a"}
	return workspace.Scope{Name: "app", Context: "lab", Namespaces: []string{"apps"}, Kinds: []string{"deployments"}, LabelSelector: "app=api"},
		workspace.Snapshot{ObservedAt: at, Resources: []workspace.Resource{{Ref: ref, Kind: "Deployment", Object: object}}, Coverage: []workspace.Coverage{{GVR: ref.GVR, Namespace: ref.Namespace, State: "complete"}}}
}

func TestActivityProjectionSeparatesImageIDsChartAndAppVersions(t *testing.T) {
	_, s := activityFixture()
	projected := Project(&s.Resources[0], "lab", s.ObservedAt)
	raw, err := json.Marshal(projected)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ENV-SHOULD-NOT-APPEAR")
	pod := s.Resources[0]
	pod.Ref.GVR = "v1/pods"
	pod.Kind = "Pod"
	pod.Object = pod.Object.DeepCopy()
	pod.Object.SetAPIVersion("v1")
	pod.Object.SetKind("Pod")
	containers, _, _ := unstructured.NestedSlice(pod.Object.Object, "spec", "template", "spec", "containers")
	require.NoError(t, unstructured.SetNestedSlice(pod.Object.Object, containers, "spec", "containers"))
	require.NoError(t, unstructured.SetNestedSlice(pod.Object.Object, []any{map[string]any{"name": "api", "imageID": "containerd://sha256:resolved-digest"}}, "status", "containerStatuses"))
	projected = Project(&pod, "lab", s.ObservedAt)
	raw, err = json.Marshal(projected)
	require.NoError(t, err)
	require.Contains(t, string(raw), "Declared image reference")
	require.Contains(t, string(raw), "repo/api:v1")
	require.Contains(t, string(raw), "Resolved runtime image ID")
	require.Contains(t, string(raw), "sha256:resolved-digest")
	helm := pod
	helm.Ref.GVR = "helm.toolkit.fluxcd.io/v2/helmreleases"
	helm.Kind = "HelmRelease"
	helm.Object = pod.Object.DeepCopy()
	helm.Object.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	helm.Object.SetKind("HelmRelease")
	require.NoError(t, unstructured.SetNestedSlice(helm.Object.Object, []any{map[string]any{"chartName": "api", "chartVersion": "3.1.0", "appVersion": "1.6.0", "version": int64(7)}}, "status", "history"))
	raw, err = json.Marshal(Project(&helm, "lab", s.ObservedAt))
	require.NoError(t, err)
	for _, text := range []string{"Chart version", "3.1.0", "Application version", "1.6.0", "Helm release revision"} {
		require.Contains(t, string(raw), text)
	}
	helm.Object.SetKind("Secret")
	require.Nil(t, Project(&helm, "lab", s.ObservedAt))
}

func TestActivityWindowObservesChangesWithExactSourceIntervals(t *testing.T) {
	scope, s := activityFixture()
	w := NewWindow(scope, s.ObservedAt)
	w.Observe(&s, nil)
	require.Len(t, w.Entries, 1)
	require.Equal(t, EntryObserved, w.Entries[0].State)
	initial := w.Entries[0].Source
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Resources[0].Object.SetResourceVersion("2")
	w.Observe(&s, nil)
	require.Len(t, w.Entries, 1, "resourceVersion churn alone is not meaningful application activity")
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	require.NoError(t, unstructured.SetNestedField(s.Resources[0].Object.Object, int64(2), "status", "readyReplicas"))
	require.NoError(t, unstructured.SetNestedSlice(s.Resources[0].Object.Object, []any{map[string]any{"name": "api", "image": "repo/api:v2"}}, "spec", "template", "spec", "containers"))
	w.Observe(&s, nil)
	require.Len(t, w.Entries, 2)
	entry := w.Entries[1]
	require.Equal(t, EntryChanged, entry.State)
	require.Equal(t, "lab", entry.Source.Identity.Context)
	require.Equal(t, s.ObservedAt, entry.ObservedAt)
	require.Equal(t, s.ObservedAt.Add(-time.Minute), entry.PriorObservedAt)
	require.Equal(t, "complete", entry.Source.Coverage.State)
	raw, err := json.Marshal(entry)
	require.NoError(t, err)
	require.Contains(t, string(raw), "repo/api:v1")
	require.Contains(t, string(raw), "repo/api:v2")
	require.Contains(t, string(raw), "Rollout progress")
	require.Equal(t, "1", initial.ResourceVersion, "old evidence retained independently")
	require.Contains(t, sourceSummary(initial), "repo/api:v1")
}

func TestActivityWindowMissingFieldsAndMalformedProjectionRemainUnknown(t *testing.T) {
	scope, s := activityFixture()
	w := NewWindow(scope, s.ObservedAt)
	w.Observe(&s, nil)
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	unstructured.RemoveNestedField(s.Resources[0].Object.Object, "status", "readyReplicas")
	w.Observe(&s, nil)
	require.Contains(t, w.Entries[1].Changes[0].After, "unknown")
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Resources[0].Object.SetKind("Secret")
	w.Observe(&s, nil)
	require.Len(t, w.tracked, 1)
	require.Equal(t, EntryGap, w.Entries[len(w.Entries)-1].State)
	for _, entry := range w.Entries {
		require.NotEqual(t, EntryMissing, entry.State, "obtained but unprojectable source cannot establish disappearance")
	}
}

func TestActivityWindowPartialStaleReplacementAndRestartBoundaries(t *testing.T) {
	scope, s := activityFixture()
	w := NewWindow(scope, s.ObservedAt)
	w.Observe(&s, nil)
	original := s.Resources[0]
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Resources = nil
	s.Coverage[0].State = "denied"
	w.Observe(&s, nil)
	require.Len(t, w.tracked, 1)
	require.Equal(t, EntryGap, w.Entries[len(w.Entries)-1].State)
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Coverage[0].State = "complete"
	w.Observe(&s, errors.New("deadline"))
	require.Len(t, w.tracked, 1)
	s.ObservedAt = w.StartedAt
	w.Observe(&s, nil)
	require.Equal(t, EntryGap, w.Entries[len(w.Entries)-1].State)
	s.ObservedAt = w.LastRefreshAt.Add(time.Minute)
	original.Object.SetUID("deploy-b")
	original.Ref.UID = "deploy-b"
	s.Resources = []workspace.Resource{original}
	w.Observe(&s, nil)
	found := false
	for _, entry := range w.Entries {
		if entry.State == EntryReplaced {
			found = true
			require.Equal(t, "deploy-a", entry.PriorUID)
			require.Equal(t, "deploy-b", entry.Source.Identity.UID)
		}
	}
	require.True(t, found)
	restarted := NewWindow(scope, s.ObservedAt)
	require.Empty(t, restarted.Entries)
	restarted.Observe(&s, nil)
	require.Equal(t, EntryObserved, restarted.Entries[0].State)
}

func TestActivityWindowBoundsHistoryAndTime(t *testing.T) {
	scope, s := activityFixture()
	w := NewWindow(scope, s.ObservedAt)
	for i := range MaxEntries + 10 {
		s.ObservedAt = s.ObservedAt.Add(time.Second)
		require.NoError(t, unstructured.SetNestedField(s.Resources[0].Object.Object, int64(i), "status", "readyReplicas"))
		w.Observe(&s, nil)
	}
	require.Len(t, w.Entries, MaxEntries)
	require.Positive(t, w.Dropped)
	require.False(t, w.PrunedBefore.IsZero())
	s.ObservedAt = s.ObservedAt.Add(Retention + time.Minute)
	w.Observe(&s, nil)
	require.Len(t, w.Entries, 1)
	require.Equal(t, EntryObserved, w.Entries[0].State)
}

func activityEvent(target *workspace.Resource, uid string, count int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "api." + uid, "namespace": target.Ref.Namespace, "uid": uid},
		"involvedObject": map[string]any{"apiVersion": target.Object.GetAPIVersion(), "kind": target.Object.GetKind(), "namespace": target.Ref.Namespace, "name": target.Ref.Name, "uid": target.Ref.UID},
		"type":           "Warning", "reason": "BackOff", "message": "retained attempt", "count": count, "firstTimestamp": "2026-10-04T09:20:00Z", "lastTimestamp": "2026-10-04T09:30:00Z"}}
}

func TestActivityEventCollectorScopesNamespaceUIDAndRejectsUnmatchedEvents(t *testing.T) {
	scope, s := activityFixture()
	event := activityEvent(&s.Resources[0], "event-a", int64(2))
	wrong := activityEvent(&s.Resources[0], "event-b", int64(4))
	require.NoError(t, unstructured.SetNestedField(wrong.Object, "different-uid", "involvedObject", "uid"))
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "EventList"}, event, wrong)
	result := CollectEvents(t.Context(), dyn, &scope, &s)
	require.Len(t, result.Events, 1)
	require.Equal(t, "unknown", result.Coverage[0].State, "excluded mismatching API records leave a collection gap")
	require.Equal(t, "deploy-a", result.Events[0].Regarding.UID)
	require.Equal(t, int64(2), *result.Events[0].Count)
	require.Equal(t, time.Date(2026, 10, 4, 9, 30, 0, 0, time.UTC), result.Events[0].LastAt)
	for _, action := range dyn.Actions() {
		require.Equal(t, "list", action.GetVerb())
		require.Equal(t, "apps", action.GetNamespace())
		list, ok := action.(ktesting.ListAction)
		require.True(t, ok)
		require.Equal(t, "involvedObject.uid=deploy-a", list.GetListRestrictions().Fields.String())
		require.Empty(t, list.GetListRestrictions().Labels.String(), "Events are scoped by captured UID; resource labels need not be copied onto Events")
	}
}

func TestActivityCanceledEventReadDoesNotEstablishCompleteCoverage(t *testing.T) {
	scope, s := activityFixture()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "EventList"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := CollectEvents(ctx, dyn, &scope, &s)
	require.Equal(t, coverageCanceled, result.Coverage[0].State)
	require.Empty(t, dyn.Actions(), "a canceled scope must not start another API read")
}

func TestActivityEventCollectorDeniedAndIgnoredLimitsStayExplicit(t *testing.T) {
	scope, s := activityFixture()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "EventList"})
	dyn.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("denied"))
	})
	result := CollectEvents(t.Context(), dyn, &scope, &s)
	require.Empty(t, result.Events)
	require.Equal(t, "denied", result.Coverage[0].State)
	require.Contains(t, result.Coverage[0].Detail, "apps/v1/deployments apps/api UID=deploy-a")
	dyn.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		for i := range eventPageSize + 3 {
			event := activityEvent(&s.Resources[0], fmt.Sprint(i), 1)
			list.Items = append(list.Items, *event)
		}
		return true, list, nil
	})
	result = CollectEvents(t.Context(), dyn, &scope, &s)
	require.Len(t, result.Events, eventPageSize)
	require.Equal(t, "truncated", result.Coverage[0].State)
	require.Contains(t, result.Coverage[0].Detail, "ignored")
}

func TestActivityProjectionQualifiesOlderReconciliationAndSeparateRevisions(t *testing.T) {
	_, s := activityFixture()
	r := &s.Resources[0]
	r.Ref.GVR = "kustomize.toolkit.fluxcd.io/v1/kustomizations"
	r.Kind = "Kustomization"
	r.Object.SetAPIVersion("kustomize.toolkit.fluxcd.io/v1")
	r.Object.SetKind(r.Kind)
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, int64(1), "status", "observedGeneration"))
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, "main@sha1:applied", "status", "lastAppliedRevision"))
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, "main@sha1:attempted", "status", "lastAttemptedRevision"))
	raw, err := json.Marshal(Project(r, "lab", s.ObservedAt))
	require.NoError(t, err)
	for _, want := range []string{"older observed generation", "Reconciliation", "main@sha1:applied", "main@sha1:attempted"} {
		require.Contains(t, string(raw), want)
	}
	r.Ref.GVR = "argoproj.io/v1alpha1/applications"
	r.Kind = KindApplication
	r.Object.SetAPIVersion("argoproj.io/v1alpha1")
	r.Object.SetKind(r.Kind)
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, "commit-observed", "status", "sync", "revision"))
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, "main", "spec", "source", "targetRevision"))
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, "Degraded", "status", "health", "status"))
	raw, err = json.Marshal(Project(r, "lab", s.ObservedAt))
	require.NoError(t, err)
	for _, want := range []string{"Argo sync revision", "commit-observed", "Argo declared target", "main", "Argo health status", "Degraded"} {
		require.Contains(t, string(raw), want)
	}
}

func TestActivityProjectionDoesNotPromoteFailedAttemptsToTerminalJobFailure(t *testing.T) {
	_, s := activityFixture()
	r := &s.Resources[0]
	r.Ref.GVR = "batch/v1/jobs"
	r.Kind = KindJob
	r.Object.SetAPIVersion("batch/v1")
	r.Object.SetKind(r.Kind)
	require.NoError(t, unstructured.SetNestedField(r.Object.Object, int64(3), "status", "failed"))
	unstructured.RemoveNestedField(r.Object.Object, "status", "conditions")
	projected := Project(r, "lab", s.ObservedAt)
	require.NotNil(t, projected.Job)
	outcome, _ := projected.Job.Outcome()
	require.NotEqual(t, review.JobOutcomeFailed, outcome)
	raw, err := json.Marshal(projected)
	require.NoError(t, err)
	require.Contains(t, string(raw), "failed Pod attempts")
	require.NoError(t, unstructured.SetNestedSlice(r.Object.Object, []any{map[string]any{
		"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded",
	}}, "status", "conditions"))
	projected = Project(r, "lab", s.ObservedAt)
	outcome, reason := projected.Job.Outcome()
	require.Equal(t, review.JobOutcomeFailed, outcome)
	require.Contains(t, reason, "BackoffLimitExceeded")
}

func TestActivityEventDedupAggregationAndMissingHistory(t *testing.T) {
	scope, s := activityFixture()
	w := NewWindow(scope, s.ObservedAt)
	w.Observe(&s, nil)
	event := projectEvent(activityEvent(&s.Resources[0], "event-a", 2), &s.Resources[0], scope.Context, s.ObservedAt)
	collection := EventCollection{Requested: true, CapturedAt: s.ObservedAt, Events: []EventSource{*event, *event}, Coverage: []workspace.Coverage{{GVR: "v1/events", Namespace: "apps", State: "complete"}}}
	w.ObserveEvents(&collection)
	require.Len(t, w.events, 1)
	require.Len(t, w.Entries, 2)
	w.ObserveEvents(&collection)
	require.Len(t, w.Entries, 2, "same retained aggregate must not invent repeated occurrences")
	count := int64(4)
	collection.Events[0].Count = &count
	collection.Events = collection.Events[:1]
	collection.CapturedAt = collection.CapturedAt.Add(time.Minute)
	w.ObserveEvents(&collection)
	require.Len(t, w.Entries, 3)
	require.Contains(t, w.Entries[2].Summary, "Aggregated Event changed")
	count = 100
	require.Equal(t, int64(4), *w.Entries[2].Event.Count, "caller cannot mutate retained evidence")
	w.ObserveEvents(nil)
	require.Equal(t, EntryGap, w.Entries[3].State)
	require.Contains(t, w.Entries[3].Summary, "not collected")
	raw, err := json.Marshal(w.Entries[2])
	require.NoError(t, err)
	require.Contains(t, string(raw), "event-a")
	require.Contains(t, string(raw), "deploy-a")
	require.NotContains(t, string(raw), "complete historical activity")
	unmatched := collection.Events[0]
	unmatched.Regarding.UID = ""
	unmatched.Identity.UID = string(types.UID("event-unidentified"))
	collection.Events = []EventSource{unmatched}
	w.ObserveEvents(&collection)
	require.Len(t, w.events, 1)
}
