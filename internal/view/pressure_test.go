// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	pressureTestNamespace        = "team"
	pressureTestFailedScheduling = "FailedScheduling"
	pressureTestBackOff          = "BackOff"
	testInspectionTimestamp      = "2026-10-03T12:00:00Z"
)

func pressurePod() *corev1.Pod {
	restart := corev1.ContainerRestartPolicyAlways
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: pressureTestNamespace, UID: "pod-uid"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("128Mi")}}},
				{Name: "unset"},
				{Name: "zero-request", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}}},
			},
			InitContainers: []corev1.Container{{Name: "init"}, {Name: "sidecar", RestartPolicy: &restart}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 3,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))}},
		}}},
	}
}

func TestPressurePreservesContainerPhasesZeroAndMissingConfiguration(t *testing.T) {
	metrics := pressureMetrics{sample: client.MetricSample{State: client.MetricsAvailable, Source: client.PodMetricsSource, ObservedAt: time.Now()}, containers: map[string]corev1.ResourceList{
		"app":          {corev1.ResourceCPU: resource.MustParse("0"), corev1.ResourceMemory: resource.MustParse("96Mi")},
		"unset":        {corev1.ResourceCPU: resource.MustParse("100m")},
		"zero-request": {corev1.ResourceCPU: resource.MustParse("100m")},
	}}
	pod := pressurePod()
	pod.Spec.Resources = &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m")}}
	pod.Spec.Overhead = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")}
	text := renderPodPressure(pod, &metrics)
	for _, expected := range []string{"usage=0m", "usage/request=0.0%", "usage/request=150.0%", "usage/limit=75.0%", "request=N/A (unset)", "limit=N/A (unset)", "init — runs before", "sidecar — restartable init", "application", "reason=OOMKilled exitCode=137", testInspectionTimestamp, "not a causal diagnosis", "container status not reported", "shared Pod budget", "request=1500m", "Pod API /spec.overhead", "16.00MiB (16Mi)"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "NaN") || strings.Contains(text, "+Inf") {
		t.Fatal(text)
	}
	for _, state := range []client.MetricState{client.MetricsUnavailable, client.MetricsDenied, client.MetricsStale, client.MetricsNotConfigured} {
		metrics.sample.State = state
		text := renderPodPressure(pressurePod(), &metrics)
		if strings.Contains(text, "usage=0m") || !strings.Contains(text, "usage=N/A") || !strings.Contains(text, "request=250m") || !strings.Contains(text, "OOMKilled") {
			t.Fatal("missing metrics removed evidence or fabricated usage", state, text)
		}
	}
}

func TestPressureMetricsFreshnessIdentityAndDenial(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pod := pressurePod()
	for _, tc := range []struct {
		name     string
		observed string
		uid      string
		state    client.MetricState
	}{
		{"zero", now.Format(time.RFC3339), "pod-uid", client.MetricsAvailable},
		{"stale", now.Add(-3 * time.Minute).Format(time.RFC3339), "pod-uid", client.MetricsStale},
		{"missing time", "", "pod-uid", client.MetricsUnavailable},
		{"replacement metrics", now.Format(time.RFC3339), "different-uid", client.MetricsUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
			dyn.PrependReactor("get", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() != pressureTestNamespace || a.GetResource().Group != "metrics.k8s.io" {
					t.Fatal("metrics scope changed", a)
				}
				return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics", "metadata": map[string]any{"name": "pod", "namespace": pressureTestNamespace, "uid": tc.uid}, "timestamp": tc.observed, "window": "30s", "containers": []any{map[string]any{"name": "app", "usage": map[string]any{string(corev1.ResourceCPU): "0", "memory": "0"}}}}}, nil
			})
			result := loadPressureMetrics(t.Context(), inspectionConnection{dynamic: dyn}, pod, now)
			if result.sample.State != tc.state || result.sample.Source != client.PodMetricsSource {
				t.Fatal(result)
			}
			if tc.state == client.MetricsAvailable {
				text := renderPodPressure(pod, &result)
				if !strings.Contains(text, "usage=0m") || !strings.Contains(text, "usage=0.00MiB (0)") || !strings.Contains(text, "window: 30s") {
					t.Fatal(text)
				}
			}
		})
	}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "pod", fmt.Errorf("denied"))
	})
	result := loadPressureMetrics(t.Context(), inspectionConnection{dynamic: dyn}, pod, now)
	if result.sample.State != client.MetricsDenied {
		t.Fatal(result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(dyn.Actions())
	result = loadPressureMetrics(ctx, inspectionConnection{dynamic: dyn}, pod, now)
	if len(dyn.Actions()) != before || result.sample.Fresh() {
		t.Fatal("canceled metrics made a request", result)
	}
}

func TestPressureEventsUseUIDNamespaceAndBound(t *testing.T) {
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor("list", "events", func(a ktesting.Action) (bool, runtime.Object, error) {
		action := a.(ktesting.ListAction)
		if action.GetNamespace() != pressureTestNamespace || action.GetListRestrictions().Fields.String() != "involvedObject.uid=pod-uid" {
			t.Fatal("events broadened", a)
		}
		options := a.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		if options.Limit != maxPressureEvents {
			t.Fatal("events unbounded", options)
		}
		return true, &corev1.EventList{ListMeta: metav1.ListMeta{Continue: "more"}, Items: []corev1.Event{{ObjectMeta: metav1.ObjectMeta{Name: "event"}, InvolvedObject: corev1.ObjectReference{UID: "pod-uid"}, Reason: pressureTestFailedScheduling, Type: corev1.EventTypeWarning, Message: "Insufficient memory", LastTimestamp: metav1.NewTime(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)), Source: corev1.EventSource{Component: "default-scheduler"}}}}, nil
	})
	text := pressureEvents(t.Context(), inspectionConnection{typed: typed}, pressurePod())
	for _, expected := range []string{pressureTestFailedScheduling, "Insufficient memory", "source=default-scheduler", testInspectionTimestamp, "truncated at 20"} {
		if !strings.Contains(text, expected) {
			t.Fatal(expected, text)
		}
	}
	pod := pressurePod()
	pod.UID = ""
	before := len(typed.Actions())
	if text := pressureEvents(t.Context(), inspectionConnection{typed: typed}, pod); !strings.Contains(text, "no name-only") || len(typed.Actions()) != before {
		t.Fatal(text)
	}
}

func TestPressureWorkloadScopePodCapAndUnknownThrottling(t *testing.T) {
	workload := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "app", "namespace": pressureTestNamespace, "uid": "deployment-uid"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "selected"}}}}}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"}, workload)
	dyn.PrependReactor("list", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		action := a.(ktesting.ListAction)
		if a.GetNamespace() != pressureTestNamespace || action.GetListRestrictions().Labels.String() != "app=selected" {
			t.Fatal("workload scope broadened", a)
		}
		list := &unstructured.UnstructuredList{}
		for i := range 25 {
			list.Items = append(list.Items, unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": fmt.Sprintf("pod-%02d", i), "namespace": pressureTestNamespace, "uid": fmt.Sprintf("uid-%d", i), "labels": map[string]any{"app": "selected"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "app"}}}}})
		}
		return true, list, nil
	})
	dyn.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "pod")
	})
	target := SelectedResourceTarget{Context: "captured", GVR: client.DpGVR, Namespace: pressureTestNamespace, Name: "app", UID: "deployment-uid"}
	text, err := loadResourcePressure(t.Context(), inspectionConnection{dynamic: dyn, typed: kubefake.NewSimpleClientset()}, target, time.Now())
	if err != nil || strings.Count(text, "\nPOD team/") != maxPressurePods || !strings.Contains(text, "limited to 20") || !strings.Contains(text, "CPU throttling: unknown") || !strings.Contains(text, "Context: captured") {
		t.Fatal(err, text)
	}
	target.UID = "replacement"
	if _, err := loadResourcePressure(t.Context(), inspectionConnection{dynamic: dyn}, target, time.Now()); err == nil {
		t.Fatal("same-name replacement accepted")
	}
}

func TestPressureUnsupportedViewsRetainNavigation(t *testing.T) {
	for name, view := range map[string]ResourceViewer{"Pulse": NewPulse(client.PuGVR), "Xray": NewXray(client.PodGVR), "empty": NewBrowser(client.PodGVR)} {
		t.Run(name, func(t *testing.T) {
			app := NewApp(mock.NewMockConfig(t))
			if browser, ok := view.(*Browser); ok {
				browser.meta = &metav1.APIResource{Kind: "Pod"}
			}
			app.Content.Push(view)
			NewCommand(app).pressureCommand()
			if app.Content.Top() != view {
				t.Fatal("unsupported pressure replaced navigation view")
			}
		})
	}
}

func TestPressureSelectedResourceRejectsChangedContextBeforeRead(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	view := &workspaceDiscoveryOwner{Details: NewDetails(app, "workspace", "", contentInspection, true), target: SelectedResourceTarget{
		Context: "different-context", GVR: client.PodGVR, Namespace: pressureTestNamespace, Name: "pod", UID: "pod-uid",
	}}
	app.Content.Push(view)
	NewCommand(app).pressureCommand()
	if app.Content.Top() != view || app.Config.ActiveContextName() == "different-context" {
		t.Fatal("captured daily-workspace target redirected the active destination")
	}
}

func TestPressureTypedBudgetsPreserveFreshnessUnknownsAndPhases(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pod := pressurePod()
	pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now)}
	metrics := pressureMetrics{sample: client.MetricSample{State: client.MetricsAvailable, Source: client.PodMetricsSource, ObservedAt: now}, window: 30 * time.Second, containers: map[string]corev1.ResourceList{
		"app":          {corev1.ResourceCPU: resource.MustParse("0"), corev1.ResourceMemory: resource.MustParse("96Mi")},
		"zero-request": {corev1.ResourceCPU: resource.MustParse("100m")},
	}}
	budgets := pressureBudgets(pod, &metrics)
	if len(budgets) != 5 || budgets[0].Role != "init" || budgets[1].Role != "sidecar" {
		t.Fatal("container phases were combined", budgets)
	}
	app := budgets[2]
	if app.CPUUsage != "0m" || app.CPURequestRatio != "0.0%" || app.MemoryUsage != "96.00MiB" || app.MemoryLimitRatio != "75.0%" || app.MetricsState != "available" || app.ObservedAt != now || app.Window != 30*time.Second || app.CurrentState != "running / not ready" {
		t.Fatal("available sample facts changed", app)
	}
	if budgets[3].CPURequest != "N/A (unset)" || budgets[3].CurrentState != "unknown" || budgets[4].CPURequest != "0m" || budgets[4].CPURequestRatio != "N/A" {
		t.Fatal("unset and zero allocations became equivalent", budgets)
	}
	for _, state := range []client.MetricState{client.MetricsStale, client.MetricsDenied, client.MetricsUnavailable} {
		metrics.sample.State, metrics.sample.Reason = state, "sample unavailable"
		app = pressureBudgets(pod, &metrics)[2]
		if app.CPUUsage != pressureNA || app.MemoryUsage != pressureNA || app.CPURequestRatio != pressureNA || app.CPULimitRatio != pressureNA || app.MemoryRequestRatio != pressureNA || app.MemoryLimitRatio != pressureNA || app.CPURequest != "250m" || app.ObservedAt != now || app.MetricsReason != "sample unavailable" {
			t.Fatal("unavailable sample fabricated usage or removed configuration", app)
		}
	}
}

func TestPressureSnapshotKeepsTypedSourcesAndFullEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pod := pressurePod()
	pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	pod.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now)}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: object})
	dyn.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetResource().Group != "metrics.k8s.io" {
			return false, nil, nil
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics", "metadata": map[string]any{"name": "pod", "namespace": pressureTestNamespace, "uid": "pod-uid"}, "timestamp": now.Format(time.RFC3339), "window": "30s", "containers": []any{map[string]any{"name": "app", "usage": map[string]any{string(corev1.ResourceCPU): "0", "memory": "96Mi"}}}}}, nil
	})
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.EventList{Items: []corev1.Event{
			{ObjectMeta: metav1.ObjectMeta{Name: "matching"}, InvolvedObject: corev1.ObjectReference{UID: "pod-uid"}, Type: corev1.EventTypeWarning, Reason: pressureTestBackOff, Message: "Retained warning", LastTimestamp: metav1.NewTime(now)},
			{ObjectMeta: metav1.ObjectMeta{Name: "replacement"}, InvolvedObject: corev1.ObjectReference{UID: "different-uid"}, Type: corev1.EventTypeWarning, Reason: pressureTestFailedScheduling, Message: "Wrong object", LastTimestamp: metav1.NewTime(now)},
		}}, nil
	})
	target := SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: pressureTestNamespace, Name: "pod", UID: "pod-uid"}
	snapshot, err := loadResourcePressureSnapshot(t.Context(), inspectionConnection{dynamic: dyn, typed: typed}, target, now)
	if err != nil {
		t.Fatal(err)
	}
	investigation := snapshot.Investigation
	if investigation == nil || snapshot.UID != "pod-uid" || snapshot.CapturedAt != now || investigation.Identity.Context != "captured" || investigation.Identity.UID != "pod-uid" || len(investigation.Resources) != 5 || len(investigation.Containers) != 1 || len(investigation.Events) != 1 || investigation.Events[0].Reason != pressureTestBackOff {
		t.Fatal("snapshot omitted or changed typed evidence", snapshot)
	}
	if !strings.Contains(snapshot.Text, "reason=OOMKilled") || !strings.Contains(snapshot.Text, "Retained warning") || strings.Contains(snapshot.Text, "Wrong object") || investigation.Containers[0].State != "running" || investigation.Containers[0].LastTermination == nil || investigation.Containers[0].LastTermination.Reason != "OOMKilled" {
		t.Fatal("historical termination became current or UID-mismatched evidence leaked", investigation)
	}
	if len(dyn.Actions()) != 2 || len(typed.Actions()) != 1 {
		t.Fatal("typed overview fetched a second observation", dyn.Actions(), typed.Actions())
	}
	metricsCoverage := false
	for _, coverage := range investigation.Coverage {
		if coverage.Source == "metrics pod" && coverage.State == string(client.MetricsAvailable) {
			metricsCoverage = true
		}
		if coverage.Source == "metrics" && coverage.State == "not collected" {
			t.Fatal("metrics placeholder survived successful collection")
		}
	}
	if !metricsCoverage {
		t.Fatal("sample coverage absent", investigation.Coverage)
	}
	if got := investigation.Coverage[0].State; got != inspect.ObservationComplete {
		t.Fatal("object observation not marked complete", got)
	}
}
