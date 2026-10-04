// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func capacityViewFixture(t *testing.T) *capacityView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR, Namespace: "apps", Name: "api", UID: "selected-pod"}
	v := &capacityView{Details: NewDetails(app, capacityTitle, target.Path(), contentInspection, true), target: target, destinationRevision: app.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	now := time.Now()
	snapshot := &capacity.Snapshot{Scope: capacity.Scope{Identity: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, Namespace: "apps", PodName: "api"}, CapturedAt: now,
		Pods:     []capacity.Pod{{Identity: inspect.ResourceIdentity{Namespace: "apps", Name: "api", UID: "selected-pod"}, Phase: "Pending", Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}, SchedulerRequests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}, Pending: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "Node affinity mismatch"}}, Usage: capacity.Usage{Sample: client.MetricSample{State: client.MetricsUnavailable, Source: client.PodMetricsSource, Reason: "Metrics unavailable"}}}},
		Quotas:   []capacity.Quota{{Identity: inspect.ResourceIdentity{Namespace: "apps", Name: "team", UID: "quota-uid"}, Hard: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}, Used: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}},
		Coverage: []capacity.Coverage{{Source: "Pods", State: capacity.Complete}, {Source: "Nodes", State: capacity.Denied, Detail: "Node access denied"}, {Source: "Metrics", State: capacity.Unavailable, Detail: "Metrics unavailable"}},
	}
	v.acceptSnapshot(snapshot, nil)
	return v
}

const capacityMetricsGroup = "metrics.k8s.io"

func TestCapacityReviewNativeFramesKeepIdentityTabsStatusAndActions(t *testing.T) {
	v := capacityViewFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}, {40, 8}, {60, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		require.Contains(t, frame, "Capacity review / apps/api")
		require.Contains(t, frame, "partial evidence")
		require.Contains(t, frame, "Overview")
		require.Contains(t, frame, "r refresh")
		require.Contains(t, frame, "Esc back")
		require.Contains(t, frame, "1 Pending Pods")
	}
	v.selectTab(3)
	for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "Scaling")
		require.Contains(t, frame, "AUTOSCALING")
		require.Equal(t, 3, v.activeTab)
	}
}

func TestCapacityReviewRetainsTabSearchScrollAndFailureSnapshot(t *testing.T) {
	v := capacityViewFixture(t)
	reads := 0
	v.loader = func(context.Context, *SelectedResourceTarget) (*capacity.Snapshot, error) {
		reads++
		return nil, fmt.Errorf("unexpected fetch")
	}
	v.BufferCompleted("Requests", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(2)
	v.selectTab(0)
	require.Equal(t, "Requests", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("capacity refresh denied"))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.identityBar.GetText(true), "failed refresh")
	require.Contains(t, strings.Join(v.model.Peek(), "\n"), "Previous captured evidence retained")
	v.StylesChanged(v.app.Styles)
	v.Stop()
	v.Start()
	require.Same(t, prior, v.snapshot)
	require.Zero(t, reads, "Back/skin changes must not start new reads for a retained observation")
	v.destinationRevision++
	v.refresh()
	require.Zero(t, reads)
	require.Contains(t, v.identityBar.GetText(true), "destination changed")
}

func TestCapacityReviewNativeDrawAgesMetricsWithoutInventingCurrentUsage(t *testing.T) {
	v := capacityViewFixture(t)
	pod := &v.snapshot.Pods[0]
	pod.Usage = capacity.Usage{Sample: client.NewMetricSample(client.ClusterMetrics{}, time.Now(), client.PodMetricsSource, time.Now()), Values: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi")}, Complete: true}
	v.render()
	require.Contains(t, v.text.GetText(true), "Usage: CPU 50m")
	pod.Usage.Sample.ObservedAt = time.Now().Add(-10 * time.Minute)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	v.app.Application.SetScreen(screen).SetRoot(v, true)
	done := make(chan struct{})
	go func() { v.app.Application.ForceDraw(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capacity native draw blocked")
	}
	defer v.app.Application.Stop()
	require.Contains(t, v.text.GetText(true), "Usage: N/A")
	require.NotContains(t, v.text.GetText(true), "Usage: CPU 50m")
}

func TestCapacityScopeUsesWorkloadExpressionsAndRejectsEmptySelector(t *testing.T) {
	target := &SelectedResourceTarget{Context: "captured", GVR: client.DpGVR, Namespace: "apps", Name: "api", UID: "deployment-uid"}
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "api", "namespace": "apps", "uid": "deployment-uid"}, "spec": map[string]any{"selector": map[string]any{"matchExpressions": []any{map[string]any{"key": "team", "operator": "In", "values": []any{"api", "worker"}}}}}}}
	scoped, err := capacityScope(target, obj, "unrelated")
	require.NoError(t, err)
	require.Equal(t, "apps", scoped.Namespace)
	require.Equal(t, "team in (api,worker)", scoped.Selector)
	require.Equal(t, "deployment-uid", scoped.Identity.UID)
	require.Equal(t, "captured", scoped.Identity.Context)
	require.NoError(t, unstructured.SetNestedMap(obj.Object, map[string]any{}, "spec", "selector"))
	_, err = capacityScope(target, obj, "unrelated")
	require.ErrorContains(t, err, "selector is empty")
}

func TestCapacityLoadRejectsRecreatedSelectionBeforeSourceReads(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "apps", "name": "api", "uid": "replacement"}}})
	target := &SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: "apps", Name: "api", UID: "selected-pod"}
	snapshot, err := loadCapacityReview(t.Context(), inspectionConnection{dynamic: dyn}, target, "apps", time.Now())
	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "identity changed")
	require.Len(t, dyn.Actions(), 1)
	require.Equal(t, "get", dyn.Actions()[0].GetVerb())
	target.GVR = client.NewGVR("v1/secrets")
	require.ErrorContains(t, capacityTargetError(target), "Select a native")
	target.GVR = client.PodGVR
	target.UID = ""
	require.ErrorContains(t, capacityTargetError(target), "UID unavailable")
}

// Ensure the selected metric GET doesn't become an unbounded namespace listing.
func TestCapacitySelectedPodMetricsStayNamedAndBounded(t *testing.T) {
	lists := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList", {Version: "v1", Resource: "nodes"}: "NodeList", {Version: "v1", Resource: "resourcequotas"}: "ResourceQuotaList", {Version: "v1", Resource: "limitranges"}: "LimitRangeList",
		{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}: "HorizontalPodAutoscalerList", {Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}: "VerticalPodAutoscalerList", {Group: capacityMetricsGroup, Version: "v1beta1", Resource: "pods"}: "PodMetricsList",
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "api", "namespace": "apps", "uid": "selected-pod"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "api"}}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, obj)
	target := &SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: "apps", Name: "api", UID: "selected-pod"}
	snapshot, err := loadCapacityReview(t.Context(), inspectionConnection{dynamic: dyn}, target, "elsewhere", time.Now())
	require.NoError(t, err)
	require.Len(t, dyn.Actions(), 8)
	require.Equal(t, 1, snapshot.Source("Metrics").Bound)
	require.Equal(t, capacity.Unavailable, snapshot.Source("Metrics").State)
	for _, action := range dyn.Actions() {
		if action.GetResource().Group == capacityMetricsGroup {
			require.Equal(t, "get", action.GetVerb())
			require.Equal(t, "apps", action.GetNamespace())
		}
	}
	require.Equal(t, "api", snapshot.Scope.PodName)
	require.Equal(t, "apps", snapshot.Scope.Namespace)
	require.Contains(t, snapshot.Render(5), "v1/pods apps/api")
}
