// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

const (
	pressureCommand   = "pressure"
	maxPressurePods   = 20
	maxPressureEvents = 20
	pressureNA        = "N/A"
	pressureUnknown   = "unknown"
)

// pressureCommand uses the inspection lifetime, cancellation and destination
// guard. Read-only mode remains fully supported; this view never submits writes.
func (c *Command) pressureCommand() {
	v, ok := c.app.Content.Top().(ResourceViewer)
	if !ok {
		c.app.Flash().Err(fmt.Errorf("open a Pod or workload resource list first"))
		return
	}
	target := resolveSelectedResource(v, c.app.Config.ActiveContextName())
	if err := target.Err(); err != nil {
		c.app.Flash().Err(err)
		return
	}
	connection, err := pinInspectionConnection(c.app.Conn())
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	d := &inspectionDetails{Details: NewDetails(c.app, pressureCommand, target.Path(), contentInspection, true).
		Update("Loading read-only resource pressure snapshot..."), target: target}
	d.connection = connection
	d.snapshotLoader = func(ctx context.Context, target SelectedResourceTarget) (inspectionSnapshot, error) {
		now := time.Now()
		text, uid, err := loadResourcePressureEvidence(ctx, connection, target, now)
		return inspectionSnapshot{Text: text, UID: uid, CapturedAt: now}, err
	}
	d.related = func(ctx context.Context, target SelectedResourceTarget) ([]inspectionReference, error) {
		return loadTargetInspectionReferences(ctx, connection, target, troubleshootCommand)
	}
	if err := c.app.inject(d, false); err != nil {
		c.app.Flash().Err(err)
		return
	}
	d.refresh()
}

//nolint:gocritic // Preserve the captured selection value when entering an asynchronous inspection.
func loadResourcePressure(ctx context.Context, conn client.Connection, target SelectedResourceTarget, now time.Time) (string, error) {
	text, _, err := loadResourcePressureEvidence(ctx, conn, target, now)
	return text, err
}

//nolint:gocritic // Match the shared inspection loader's immutable captured identity interface.
func loadResourcePressureEvidence(ctx context.Context, conn client.Connection, target SelectedResourceTarget, now time.Time) (string, types.UID, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := target.Err(); err != nil {
		return "", "", err
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return "", "", err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	if err := verifySelectedIdentity(target, obj); err != nil {
		return "", "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "RESOURCE PRESSURE · READ-ONLY SNAPSHOT\nContext: %s\n%s %s\nUID: %s\nCaptured: %s\n",
		target.Context, obj.GetKind(), target.Path(), obj.GetUID(), now.UTC().Format(time.RFC3339))
	if target.UID == "" {
		b.WriteString("Selected UID: unknown; continuity with the selected row cannot be verified.\n")
	}
	b.WriteString("\n" +
		"CPU uses millicores: 1000m = one CPU. Memory uses binary MiB.\n" +
		"Requests describe scheduling allocations; limits bound configured resources.\n" +
		"Configuration is from the current Pod spec and may differ from its last termination.\n" +
		"Usage is a current sample over its reported window, not a history.\n" +
		"Ratios compare observed usage with a configured nonzero request or limit.\n" +
		"CPU throttling: unknown — metrics-server supplies no throttling counters.\n")
	var pods []*unstructured.Unstructured
	notice := ""
	if obj.GetKind() == "Pod" {
		pods = []*unstructured.Unstructured{obj}
	} else {
		switch obj.GetKind() {
		case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet", "Job":
			pods, notice = workloadPods(ctx, conn, obj)
		default:
			fmt.Fprintf(&b, "\nPod inspection unavailable for %s; select a Pod, Deployment, DaemonSet, StatefulSet, ReplicaSet or Job.\n", obj.GetKind())
			return b.String(), obj.GetUID(), nil
		}
	}
	if len(pods) > maxPressurePods {
		pods = pods[:maxPressurePods]
		notice += fmt.Sprintf("\nInspection limited to %d selector-matching Pods; narrow the workload for more.", maxPressurePods)
	}
	if notice != "" {
		fmt.Fprintf(&b, "\nPod visibility: %s\n", strings.TrimSpace(notice))
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].GetName() < pods[j].GetName() })
	for _, obj := range pods {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
			fmt.Fprintf(&b, "\nPod %s: configuration unavailable: %s\n", obj.GetName(), err)
			continue
		}
		metrics := loadPressureMetrics(ctx, conn, &pod, now)
		b.WriteString(renderPodPressure(&pod, &metrics))
		b.WriteString(pressureEvents(ctx, conn, &pod))
	}
	if len(pods) == 0 && notice == "" {
		b.WriteString("\nNo current Pods match the workload selector. This does not establish health.\n")
	}
	b.WriteString("\n" +
		"Evidence comes from the Kubernetes Pod API, retained Events and optional metrics.k8s.io.\n" +
		"OOM and scheduling evidence do not by themselves establish a cause.\n" +
		"No aggregate allocation is invented for missing values or overlapping init/sidecar phases.\n" +
		"Refresh with r; g opens related resources; Esc returns and cancels waiting reads.\n")
	return b.String(), obj.GetUID(), nil
}

type pressureMetrics struct {
	sample     client.MetricSample
	window     time.Duration
	containers map[string]corev1.ResourceList
}

func loadPressureMetrics(ctx context.Context, conn client.Connection, pod *corev1.Pod, now time.Time) pressureMetrics {
	result := pressureMetrics{sample: client.MetricSample{State: client.MetricsUnavailable, Source: client.PodMetricsSource}, containers: map[string]corev1.ResourceList{}}
	if err := ctx.Err(); err != nil {
		result.sample.Reason = err.Error()
		return result
	}
	dyn, err := conn.DynDial()
	if err != nil {
		result.sample.Reason = err.Error()
		return result
	}
	metricGVR := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	o, err := dyn.Resource(metricGVR).Namespace(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		result.sample.State, result.sample.Reason = client.MetricErrorState(err), err.Error()
		return result
	}
	var metrics metricsv1beta1.PodMetrics
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &metrics); err != nil {
		result.sample.Reason = "invalid metrics response: " + err.Error()
		return result
	}
	result.sample = client.NewMetricSample(client.ClusterMetrics{}, metrics.Timestamp.Time, client.PodMetricsSource, now)
	result.window = metrics.Window.Duration
	if metrics.Name != pod.Name || metrics.Namespace != pod.Namespace || (metrics.UID != "" && metrics.UID != pod.UID) {
		result.sample.State, result.sample.Reason = client.MetricsUnavailable, "metrics identity does not match this Pod"
		return result
	}
	if !pod.CreationTimestamp.IsZero() && metrics.Timestamp.Before(&pod.CreationTimestamp) {
		result.sample.State, result.sample.Reason = client.MetricsStale, "observation predates this Pod's creation"
	}
	if metrics.Timestamp.Time.After(now.Add(time.Minute)) {
		result.sample.State, result.sample.Reason = client.MetricsUnavailable, "observation time is in the future"
	}
	for _, container := range metrics.Containers {
		result.containers[container.Name] = container.Usage
	}
	return result
}

func renderPodPressure(pod *corev1.Pod, metrics *pressureMetrics) string {
	var b strings.Builder
	node, phase, qos := pod.Spec.NodeName, string(pod.Status.Phase), string(pod.Status.QOSClass)
	if node == "" {
		node = "unassigned"
	}
	if phase == "" {
		phase = pressureNA
	}
	if qos == "" {
		qos = pressureNA
	}
	fmt.Fprintf(&b, "\nPOD %s/%s\nUID: %s | node: %s | phase: %s | QoS: %s\n", pod.Namespace, pod.Name, pod.UID, node, phase, qos)
	observed := pressureTimestamp(metrics.sample.ObservedAt)
	window := pressureUnknown
	if metrics.window > 0 {
		window = metrics.window.String()
	}
	fmt.Fprintf(&b, "Metrics: %s | source: %s | observed: %s | window: %s\n", metrics.sample.State, metrics.sample.Source, observed, window)
	if metrics.sample.Reason != "" {
		fmt.Fprintf(&b, "Metrics reason: %s\n", metrics.sample.Reason)
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status != corev1.ConditionTrue {
			fmt.Fprintf(&b, "Pod API /status.conditions: PodScheduled=%s reason=%s at %s\n  %s\n",
				condition.Status, condition.Reason, pressureTimestamp(condition.LastTransitionTime.Time), condition.Message)
		}
	}
	if pod.Spec.Resources != nil {
		renderPressureConfiguration(&b, "Pod API /spec.resources — shared Pod budget; not assigned to individual containers", *pod.Spec.Resources)
	}
	if len(pod.Spec.Overhead) > 0 {
		fmt.Fprintf(&b, "Pod API /spec.overhead: cpu=%s memory=%s\n",
			pressureQuantity(pod.Spec.Overhead, corev1.ResourceCPU), pressureQuantity(pod.Spec.Overhead, corev1.ResourceMemory))
	}
	statuses := map[string]*corev1.ContainerStatus{}
	for _, group := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for i := range group {
			statuses[group[i].Name] = &group[i]
		}
	}
	for i := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[i]
		phase := "init — runs before application containers"
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			phase = "sidecar — restartable init; overlaps application containers"
		}
		renderContainerPressure(&b, container.Name, phase, container.Resources, statuses[container.Name], metrics)
	}
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		renderContainerPressure(&b, container.Name, "application", container.Resources, statuses[container.Name], metrics)
	}
	for i := range pod.Spec.EphemeralContainers {
		container := &pod.Spec.EphemeralContainers[i]
		renderContainerPressure(&b, container.Name, "ephemeral debug", container.Resources, statuses[container.Name], metrics)
	}
	return b.String()
}

func renderContainerPressure(b *strings.Builder, name, phase string, resources corev1.ResourceRequirements, status *corev1.ContainerStatus, metrics *pressureMetrics) {
	fmt.Fprintf(b, "\n  CONTAINER %s (%s)\n", name, phase)
	usage := metrics.containers[name]
	for _, resource := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, hasRequest := resources.Requests[resource]
		limit, hasLimit := resources.Limits[resource]
		used, hasUsage := usage[resource]
		hasUsage = hasUsage && used.Sign() >= 0 && metrics.sample.Fresh()
		use := pressureNA
		if hasUsage {
			use = pressureQuantity(usage, resource)
		}
		requestRatio, limitRatio := pressureNA, pressureNA
		if hasUsage && hasRequest && request.Sign() > 0 {
			requestRatio = fmt.Sprintf("%.1f%%", 100*used.AsApproximateFloat64()/request.AsApproximateFloat64())
		}
		if hasUsage && hasLimit && limit.Sign() > 0 {
			limitRatio = fmt.Sprintf("%.1f%%", 100*used.AsApproximateFloat64()/limit.AsApproximateFloat64())
		}
		fmt.Fprintf(b, "    %s request=%s  limit=%s  usage=%s  usage/request=%s  usage/limit=%s\n",
			resource, pressureQuantity(resources.Requests, resource), pressureQuantity(resources.Limits, resource), use, requestRatio, limitRatio)
	}
	if status == nil {
		b.WriteString("    Pod API /status: N/A (container status not reported)\n")
		return
	}
	fmt.Fprintf(b, "    Pod API /status: ready=%t restartCount=%d\n", status.Ready, status.RestartCount)
	if status.Resources != nil {
		renderPressureConfiguration(b, "    Pod API /status.resources — enacted container configuration", *status.Resources)
	}
	for _, evidence := range []struct {
		source string
		state  corev1.ContainerState
	}{{"current state", status.State}, {"last termination state", status.LastTerminationState}} {
		if terminated := evidence.state.Terminated; terminated != nil {
			fmt.Fprintf(b, "    Pod API /status %s: reason=%s exitCode=%d started=%s finished=%s\n",
				evidence.source, terminated.Reason, terminated.ExitCode, pressureTimestamp(terminated.StartedAt.Time), pressureTimestamp(terminated.FinishedAt.Time))
			if terminated.Message != "" {
				fmt.Fprintf(b, "      %s\n", terminated.Message)
			}
			if terminated.Reason == "OOMKilled" {
				b.WriteString("      OOMKilled was reported by Kubernetes; the configured limits above are evidence, not a causal diagnosis.\n")
			}
		}
		if waiting := evidence.state.Waiting; waiting != nil {
			fmt.Fprintf(b, "    Pod API /status %s: waiting reason=%s %s\n", evidence.source, waiting.Reason, waiting.Message)
		}
	}
}

func pressureQuantity(values corev1.ResourceList, resource corev1.ResourceName) string {
	q, ok := values[resource]
	if !ok {
		return "N/A (unset)"
	}
	if resource == corev1.ResourceCPU {
		return fmt.Sprintf("%gm", 1000*q.AsApproximateFloat64())
	}
	return fmt.Sprintf("%.2fMiB (%s)", float64(q.Value())/(1024*1024), q.String())
}

func renderPressureConfiguration(b *strings.Builder, source string, resources corev1.ResourceRequirements) {
	fmt.Fprintf(b, "%s\n", source)
	for _, resource := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		fmt.Fprintf(b, "    %s request=%s limit=%s\n", resource, pressureQuantity(resources.Requests, resource), pressureQuantity(resources.Limits, resource))
	}
}

func pressureEvents(ctx context.Context, conn client.Connection, pod *corev1.Pod) string {
	if err := ctx.Err(); err != nil {
		return "\nEVENTS: N/A (" + err.Error() + ")\n"
	}
	if pod.UID == "" {
		return "\nEVENTS: N/A (Pod UID missing; no name-only event lookup)\n"
	}
	k, err := conn.Dial()
	if err != nil {
		return "\nEVENTS: N/A (" + err.Error() + ")\n"
	}
	events, err := k.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(pod.UID)).String(), Limit: maxPressureEvents,
	})
	if err != nil {
		return "\nEVENTS: N/A (" + err.Error() + ")\n"
	}
	sort.SliceStable(events.Items, func(i, j int) bool { return eventTime(&events.Items[i]).After(eventTime(&events.Items[j])) })
	var b strings.Builder
	b.WriteString("\nEVENTS (Kubernetes Events API, matching Pod UID; retained snapshot)\n")
	if len(events.Items) == 0 {
		b.WriteString("No retained events reported; this does not establish health.\n")
	}
	for i := range events.Items {
		event := &events.Items[i]
		if event.Reason != "FailedScheduling" && event.Reason != "OOMKilling" && event.Reason != "BackOff" && event.Type != corev1.EventTypeWarning {
			continue
		}
		fmt.Fprintf(&b, "%s %s %s count=%d source=%s/%s\n  %s\n",
			pressureTimestamp(eventTime(event)), event.Type, event.Reason, event.Count, event.Source.Component, event.ReportingController, event.Message)
	}
	if events.Continue != "" {
		fmt.Fprintf(&b, "Events truncated at %d; open Events for more.\n", maxPressureEvents)
	}
	return b.String()
}

func pressureTimestamp(t time.Time) string {
	if t.IsZero() {
		return pressureUnknown
	}
	return t.UTC().Format(time.RFC3339)
}
