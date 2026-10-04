// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func quantities(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}
}
func object(t *testing.T, obj runtime.Object) *unstructured.Unstructured {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	require.NoError(t, err)
	return &unstructured.Unstructured{Object: raw}
}
func reader(t *testing.T, objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	t.Helper()
	kinds := make(map[schema.GroupVersionResource]string)
	for _, source := range sources {
		kinds[source.gvr] = map[string]string{"Pods": "PodList", "Nodes": "NodeList", "Quota": "ResourceQuotaList", "LimitRange": "LimitRangeList", "HPA": "HorizontalPodAutoscalerList", "VPA": "VerticalPodAutoscalerList", "Metrics": "PodMetricsList"}[source.name]
	}
	var ordinary []runtime.Object
	var metricObjects []runtime.Object
	for _, obj := range objects {
		if obj.GetObjectKind().GroupVersionKind().Kind == "PodMetrics" {
			metricObjects = append(metricObjects, obj)
		} else {
			ordinary = append(ordinary, obj)
		}
	}
	r := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, ordinary...)
	// PodMetrics has resource name "pods", not the fake tracker's guessed
	// "podmetrics" plural. Use the actual endpoint mapping explicitly.
	for _, obj := range metricObjects {
		require.NoError(t, r.Tracker().Create(sources[6].gvr, obj, obj.(*unstructured.Unstructured).GetNamespace()))
	}
	return r
}
func scope() *Scope {
	return &Scope{Identity: inspect.ResourceIdentity{Context: "captured-dev", GVR: "v1/namespaces", Name: "apps", UID: "namespace-uid"}, Namespace: "apps"}
}
func testPod(now time.Time) *corev1.Pod {
	return &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: "pod-uid", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Resources: corev1.ResourceRequirements{Requests: quantities("750m", "512Mi"), Limits: quantities("1500m", "1Gi")}}}, NodeSelector: map[string]string{"zone": "west"}}, Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "Insufficient memory and node affinity mismatch"}}}}
}
func deny(r *dynamicfake.FakeDynamicClient, resourceName string, err error) {
	r.PrependReactor("list", resourceName, func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, err })
}

func TestCollectConstrainedQuotaUnavailableScalingAndDeniedNodesRemainDistinct(t *testing.T) {
	now := time.Now()
	generation := int64(2)
	desiredCPU := int32(80)
	quota := &corev1.ResourceQuota{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"}, ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "apps", UID: "quota-uid"}, Status: corev1.ResourceQuotaStatus{Hard: quantities("2", "4Gi"), Used: quantities("2", "3Gi")}}
	limit := &corev1.LimitRange{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"}, ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "apps"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Min: quantities("100m", "64Mi"), DefaultRequest: quantities("200m", "128Mi")}}}}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"}, ObjectMeta: metav1.ObjectMeta{Name: "api-scale", Namespace: "apps", UID: "hpa-uid", Generation: 3}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MaxReplicas: 8, Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &desiredCPU}}}}}, Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3, DesiredReplicas: 3, ObservedGeneration: &generation, Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "Metrics API not available"}}}}
	r := reader(t, object(t, testPod(now)), object(t, quota), object(t, limit), object(t, hpa))
	deny(r, "nodes", apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", fmt.Errorf("node visibility denied")))
	deny(r, "verticalpodautoscalers", apierrors.NewNotFound(schema.GroupResource{Group: "autoscaling.k8s.io", Resource: "verticalpodautoscalers"}, ""))
	r.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetResource().Group == "metrics.k8s.io" {
			return true, nil, apierrors.NewServiceUnavailable("metrics unavailable")
		}
		return false, nil, nil
	})
	snapshot := Collect(context.Background(), r, scope(), now)
	require.Len(t, snapshot.Pods, 1)
	require.Len(t, snapshot.Quotas, 1)
	require.Len(t, snapshot.Autoscalers, 1)
	require.Equal(t, Denied, snapshot.Source("Nodes").State)
	require.Equal(t, Absent, snapshot.Source("VPA").State)
	require.Equal(t, client.MetricsUnavailable, snapshot.Pods[0].Usage.Sample.State)
	require.True(t, snapshot.Partial())
	overview := snapshot.Render(0)
	require.Contains(t, overview, "1 Pending Pods")
	require.Contains(t, overview, "CPU 750m")
	require.Contains(t, overview, "Usage: N/A")
	require.Contains(t, overview, "cpu remaining 0")
	require.NotContains(t, overview, "Usage: CPU 0")
	require.Contains(t, snapshot.Render(1), "Insufficient memory and node affinity mismatch")
	require.Contains(t, snapshot.Render(2), "default request cpu=200m")
	require.Contains(t, snapshot.Render(3), "Current metric inputs: N/A")
	require.Contains(t, snapshot.Render(3), "Status stale: observed generation 2, spec generation 3")
	require.Contains(t, snapshot.Render(3), "FailedGetResourceMetric")
	require.Contains(t, snapshot.Render(4), "denied")
	require.Contains(t, snapshot.Render(5), "No Secret API is queried")
	require.Len(t, r.Actions(), 7, "fixed source set must not expand to discovery or Secret reads")
	for _, action := range r.Actions() {
		require.Equal(t, "list", action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
}

func TestCollectMetricsIdentityTimeAndCompletenessPreventInventedUsage(t *testing.T) {
	now := time.Now()
	base := testPod(now)
	base.Status.Phase = corev1.PodRunning
	base.Status.Conditions = nil
	for _, test := range []struct {
		name      string
		timestamp time.Time
		uid       types.UID
		usage     corev1.ResourceList
		window    time.Duration
		want      client.MetricState
		complete  bool
	}{
		{"measured zero is readable", now, "pod-uid", quantities("0", "0"), time.Second, client.MetricsAvailable, true},
		{"missing UID is explicitly weaker association", now, "", quantities("40m", "10Mi"), time.Second, client.MetricsAvailable, true},
		{"old sample", now.Add(-10 * time.Minute), "pod-uid", quantities("40m", "10Mi"), time.Second, client.MetricsStale, true},
		{"replacement Pod", now, "another-pod", quantities("40m", "10Mi"), time.Second, client.MetricsUnavailable, false},
		{"predates creation", now.Add(-2 * time.Hour), "pod-uid", quantities("40m", "10Mi"), time.Second, client.MetricsStale, true},
		{"future sample", now.Add(2 * time.Minute), "pod-uid", quantities("40m", "10Mi"), time.Second, client.MetricsUnavailable, true},
		{"partial container quantities", now, "pod-uid", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("40m")}, time.Second, client.MetricsAvailable, false},
		{"window overlaps earlier Pod", now, "", quantities("40m", "10Mi"), 2 * time.Hour, client.MetricsStale, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &metrics.PodMetrics{TypeMeta: metav1.TypeMeta{APIVersion: "metrics.k8s.io/v1beta1", Kind: "PodMetrics"}, ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: test.uid}, Timestamp: metav1.NewTime(test.timestamp), Window: metav1.Duration{Duration: test.window}, Containers: []metrics.ContainerMetrics{{Name: "api", Usage: test.usage}}}
			snapshot := Collect(context.Background(), reader(t, object(t, base), object(t, m)), scope(), now)
			require.Len(t, snapshot.Pods, 1)
			usage := snapshot.Pods[0].Usage
			require.Equal(t, test.want, usage.Sample.State)
			require.Equal(t, test.complete, usage.Complete)
			if test.want != client.MetricsAvailable || !test.complete {
				require.Contains(t, snapshot.Render(0), "Usage: N/A")
				require.True(t, snapshot.Partial())
			} else {
				require.Contains(t, snapshot.Render(0), "1/1 fresh complete Pods")
			}
			if test.uid == "" && test.want == client.MetricsAvailable {
				require.Contains(t, usage.Sample.Reason, "no Pod UID")
			}
		})
	}
}

func TestCollectBoundedPageAndSelectedReplacementArePartial(t *testing.T) {
	now := time.Now()
	r := reader(t)
	r.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetResource().Group != "" {
			return false, nil, nil
		}
		require.EqualValues(t, MaxObjects+1, action.(ktesting.ListActionImpl).ListOptions.Limit)
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion("v1")
		list.SetKind("PodList")
		list.SetContinue("another-page")
		for index := range MaxObjects + 1 {
			pod := testPod(now)
			pod.Name = fmt.Sprintf("api-%d", index)
			list.Items = append(list.Items, *object(t, pod))
		}
		return true, list, nil
	})
	snapshot := Collect(context.Background(), r, scope(), now)
	require.Len(t, snapshot.Pods, MaxObjects)
	require.Equal(t, Partial, snapshot.Source("Pods").State)
	require.Equal(t, MaxObjects, snapshot.Source("Pods").Visible)
	require.Len(t, r.Actions(), 7, "bounded read must not follow pagination")
	selected := scope()
	selected.PodName = "api"
	selected.Identity.UID = "original-uid"
	replaced := Collect(context.Background(), reader(t, object(t, testPod(now))), selected, now)
	require.Empty(t, replaced.Pods)
	require.Equal(t, Partial, replaced.Source("Pods").State)
	require.Contains(t, replaced.Source("Pods").Detail, "identity changed")
}

func TestQuotaRemainingRequiresReportedConsumption(t *testing.T) {
	hard := quantities("2", "1Gi")
	used := corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}
	_, known := Remaining(hard, used, corev1.ResourceCPU)
	require.False(t, known)
	remaining, known := Remaining(hard, used, corev1.ResourceMemory)
	require.True(t, known)
	require.Equal(t, "512Mi", remaining.String())
	snapshot := &Snapshot{Quotas: []Quota{{Hard: hard, Used: used}}}
	require.True(t, snapshot.Partial())
	require.Contains(t, snapshot.Render(2), "unknown (used not reported)")
	require.Equal(t, "2", Quantity(hard, corev1.ResourceCPU), "remaining calculation cannot mutate source quantities")
}

func TestCollectMatchesAutoscalerTargetsAndKeepsVPARecommendationsSeparate(t *testing.T) {
	now := time.Now()
	pod := testPod(now)
	pod.Labels = map[string]string{"app": "api"}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"}, ObjectMeta: metav1.ObjectMeta{Name: "api-scale", Namespace: "apps", UID: "api-hpa"}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"}, MaxReplicas: 4}}
	unrelated := hpa.DeepCopy()
	unrelated.Name = "other-scale"
	unrelated.UID = "other-hpa"
	unrelated.Spec.ScaleTargetRef.Name = "other"
	wrongGroup := hpa.DeepCopy()
	wrongGroup.Name = "wrong-group-scale"
	wrongGroup.Spec.ScaleTargetRef.APIVersion = "example.test/v1"
	vpa := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "autoscaling.k8s.io/v1", "kind": "VerticalPodAutoscaler", "metadata": map[string]any{"namespace": "apps", "name": "api-sizing", "uid": "vpa-uid"}, "spec": map[string]any{"targetRef": map[string]any{"kind": "Deployment", "name": "api"}, "updatePolicy": map[string]any{"updateMode": "Off"}}, "status": map[string]any{"recommendation": map[string]any{"containerRecommendations": []any{map[string]any{"containerName": "api", "target": map[string]any{"cpu": "200m", "memory": "128Mi"}, "lowerBound": map[string]any{"cpu": "100m", "memory": "64Mi"}, "upperBound": map[string]any{"cpu": "400m", "memory": "256Mi"}}}}}}}
	selected := scope()
	selected.Kind = "Deployment"
	selected.Selector = "app=api"
	selected.Identity.GVR = "apps/v1/deployments"
	selected.Identity.Name = "api"
	wrongVPA := vpa.DeepCopy()
	wrongVPA.SetName("wrong-group-sizing")
	require.NoError(t, unstructured.SetNestedField(wrongVPA.Object, "example.test/v1", "spec", "targetRef", "apiVersion"))
	hpaObject := object(t, hpa)
	unstructured.RemoveNestedField(hpaObject.Object, "status", "currentReplicas")
	unstructured.RemoveNestedField(hpaObject.Object, "status", "desiredReplicas")
	snapshot := Collect(t.Context(), reader(t, object(t, pod), hpaObject, object(t, unrelated), object(t, wrongGroup), vpa, wrongVPA), selected, now)
	require.Len(t, snapshot.Autoscalers, 1)
	require.Equal(t, "api-hpa", snapshot.Autoscalers[0].Identity.UID)
	require.Len(t, snapshot.Recommendations, 1)
	require.Len(t, snapshot.Recommendations[0].Containers, 1)
	require.Equal(t, "200m", Quantity(snapshot.Recommendations[0].Containers[0].Target, corev1.ResourceCPU))
	require.Equal(t, "750m", Quantity(snapshot.Pods[0].Requests, corev1.ResourceCPU))
	scaling := snapshot.Render(3)
	require.Contains(t, scaling, "update mode Off")
	require.Contains(t, scaling, "lower cpu=100m")
	require.Contains(t, scaling, "upper cpu=400m")
	require.Contains(t, scaling, "not verified owner UIDs")
	require.Contains(t, scaling, "current unknown | desired unknown")
	require.NotContains(t, scaling, "other-scale")
	require.NotContains(t, scaling, "wrong-group")
	require.Equal(t, NotConfigured, snapshot.Source("History").State)
}

func TestAutoscalerMissingConfiguredInputsCannotBecomeComplete(t *testing.T) {
	utilization := int32(60)
	observed := int64(3)
	hpa := Autoscaler{Generation: 3, ObservedGeneration: &observed, CurrentReported: true, DesiredReported: true,
		Metrics: []autoscalingv2.MetricSpec{
			{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &utilization}}},
			{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceMemory, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: quantityPointer("128Mi")}}},
		},
		CurrentMetrics: []autoscalingv2.MetricStatus{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{Name: corev1.ResourceCPU, Current: autoscalingv2.MetricValueStatus{AverageUtilization: &utilization}}}},
	}
	snapshot := &Snapshot{Autoscalers: []Autoscaler{hpa}}
	require.True(t, snapshot.Partial())
	require.Equal(t, []string{"Resource memory AverageValue average=128Mi"}, hpa.MissingInputs())
	require.Contains(t, snapshot.Render(3), "Missing current input: Resource memory")
	hpa.CurrentMetrics = append(hpa.CurrentMetrics, autoscalingv2.MetricStatus{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{Name: corev1.ResourceMemory}})
	require.Len(t, hpa.MissingInputs(), 1, "a metric name without its measurement remains unknown")
	hpa.CurrentMetrics[1].Resource.Current.AverageValue = quantityPointer("0")
	snapshot.Autoscalers[0] = hpa
	require.False(t, snapshot.Partial(), "a reported zero is an observed value")
}

func quantityPointer(value string) *resource.Quantity {
	q := resource.MustParse(value)
	return &q
}

func TestCollectSuccessfulNodePageMissingAssignedNodeDisclosesCoverage(t *testing.T) {
	now := time.Now()
	pod := testPod(now)
	pod.Spec.NodeName = "not-visible"
	snapshot := Collect(t.Context(), reader(t, object(t, pod)), scope(), now)
	require.Equal(t, Partial, snapshot.Source(SourceNodes).State)
	require.Contains(t, snapshot.Source(SourceNodes).Detail, "1 assigned Pod node(s)")
	require.True(t, snapshot.Partial())
}

func TestDeniedPodsDoNotBecomeZeroPendingOrUnsetBudgets(t *testing.T) {
	snapshot := &Snapshot{Coverage: []Coverage{{Source: SourcePods, State: Denied, Detail: "Pod access denied"}}}
	text := snapshot.Render(0)
	require.Contains(t, text, "Pending Pods: unknown")
	require.Contains(t, text, "Requests: N/A (Pods denied)")
	require.Contains(t, text, "Scheduling reservation: N/A")
	require.NotContains(t, text, "0 Pending Pods")
	require.NotContains(t, text, "Requests: CPU unset")
}
