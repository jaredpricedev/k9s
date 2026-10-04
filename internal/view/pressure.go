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
	"github.com/derailed/k9s/internal/inspect"
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
	pressureUnknown   = workspaceUnknown
	pressureUnset     = "N/A (unset)"
)

// pressureCommand uses the inspection lifetime, cancellation and destination
// guard. Read-only mode remains fully supported; this view never submits writes.
func (c *Command) pressureCommand() {
	var target SelectedResourceTarget
	switch view := c.app.Content.Top().(type) {
	case SelectedResource:
		target = view.SelectedResource()
	case ResourceViewer:
		target = resolveSelectedResource(view, c.app.Config.ActiveContextName())
	default:
		c.app.Flash().Err(fmt.Errorf("open a Pod or workload resource list first"))
		return
	}
	if err := target.Err(); err != nil {
		c.app.Flash().Err(err)
		return
	}
	if target.Context != c.app.Config.ActiveContextName() {
		c.app.Flash().Warn("Context changed; reopen resource pressure")
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
		return loadResourcePressureSnapshot(ctx, connection, target, time.Now())
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
	snapshot, err := loadResourcePressureSnapshot(ctx, conn, target, now)
	return snapshot.Text, snapshot.UID, err
}

// loadResourcePressureSnapshot retains both the full source report and typed
// budgets from the same reads. Presentation never extracts facts from prose.
//
//nolint:gocritic // The captured target is immutable throughout the observation.
func loadResourcePressureSnapshot(ctx context.Context, conn client.Connection, target SelectedResourceTarget, now time.Time) (inspectionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return inspectionSnapshot{}, err
	}
	if err := target.Err(); err != nil {
		return inspectionSnapshot{}, err
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return inspectionSnapshot{}, err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return inspectionSnapshot{}, err
	}
	if err := verifySelectedIdentity(target, obj); err != nil {
		return inspectionSnapshot{}, err
	}
	investigation := newPressureInvestigation(obj, target.Context, target.GVR.String(), now)
	snapshot := inspectionSnapshot{UID: obj.GetUID(), CapturedAt: now, Investigation: investigation}
	var b strings.Builder
	fmt.Fprintf(&b, "RESOURCE PRESSURE · READ-ONLY SNAPSHOT\nContext: %s\n%s %s\nUID: %s\nCaptured: %s\n",
		target.Context, obj.GetKind(), target.Path(), obj.GetUID(), now.UTC().Format(time.RFC3339))
	if target.UID == "" {
		b.WriteString("Selected UID: unknown; continuity with the selected row cannot be verified.\n")
		investigation.Coverage = append(investigation.Coverage, inspect.InvestigationCoverage{
			Source: "selection", State: inspect.ObservationUnknown,
			Detail: "Selected UID unknown; continuity with the selected row cannot be verified",
		})
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
	if obj.GetKind() == inspectionPodKind {
		pods = []*unstructured.Unstructured{obj}
	} else {
		switch obj.GetKind() {
		case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet", "Job":
			pods, notice = workloadPods(ctx, conn, obj)
		default:
			fmt.Fprintf(&b, "\nPod inspection unavailable for %s; select a Pod, Deployment, DaemonSet, StatefulSet, ReplicaSet or Job.\n", obj.GetKind())
			investigation.Coverage = append(investigation.Coverage, inspect.InvestigationCoverage{
				Source: "Pod budgets", State: "unsupported",
				Detail: "Select a Pod, Deployment, DaemonSet, StatefulSet, ReplicaSet or Job",
			})
			snapshot.Text = b.String()
			return snapshot, nil
		}
	}
	if len(pods) > maxPressurePods {
		pods = pods[:maxPressurePods]
		notice += fmt.Sprintf("\nInspection limited to %d selector-matching Pods; narrow the workload for more.", maxPressurePods)
	}
	if notice != "" {
		fmt.Fprintf(&b, "\nPod visibility: %s\n", strings.TrimSpace(notice))
		investigation.Coverage = append(investigation.Coverage, inspect.InvestigationCoverage{
			Source: "Pods", State: inspect.ObservationIncomplete, Detail: strings.TrimSpace(notice),
		})
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].GetName() < pods[j].GetName() })
	for _, obj := range pods {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
			fmt.Fprintf(&b, "\nPod %s: configuration unavailable: %s\n", obj.GetName(), err)
			investigation.Coverage = append(investigation.Coverage, inspect.InvestigationCoverage{
				Source: "Pod " + obj.GetName(), State: inspect.ObservationUnknown, Detail: "Configuration unavailable: " + err.Error(),
			})
			continue
		}
		podInvestigation := inspect.NewInvestigation(obj, target.Context, client.PodGVR.String(), now)
		if investigation.Kind != inspectionPodKind {
			investigation.Containers = append(investigation.Containers, podInvestigation.Containers...)
			for _, condition := range podInvestigation.Conditions {
				condition.Type = pod.Name + " / " + condition.Type
				investigation.Conditions = append(investigation.Conditions, condition)
			}
		}
		metrics := loadPressureMetrics(ctx, conn, &pod, now)
		investigation.Resources = append(investigation.Resources, pressureBudgets(&pod, &metrics)...)
		investigation.Coverage = append(investigation.Coverage, inspect.InvestigationCoverage{
			Source: "metrics " + pod.Name, State: string(metrics.sample.State), Detail: metrics.sample.Reason,
		})
		b.WriteString(renderPodPressure(&pod, &metrics))
		eventText, events, eventCoverage := loadPressureEvents(ctx, conn, &pod)
		b.WriteString(eventText)
		podInvestigation.AddEvents(events)
		investigation.Events = append(investigation.Events, podInvestigation.Events...)
		investigation.Coverage = append(investigation.Coverage, eventCoverage)
	}
	sort.SliceStable(investigation.Events, func(i, j int) bool {
		return investigation.Events[i].LastObserved.After(investigation.Events[j].LastObserved)
	})
	if len(pods) == 0 && notice == "" {
		b.WriteString("\nNo current Pods match the workload selector. This does not establish health.\n")
		investigation.Coverage = append(investigation.Coverage, inspect.InvestigationCoverage{
			Source: "Pods", State: "empty", Detail: "No current selector-matching Pods; this does not establish health",
		})
	}
	investigation.Coverage = append(investigation.Coverage,
		inspect.InvestigationCoverage{Source: "CPU throttling", State: workspaceUnknown, Detail: "metrics-server supplies no throttling counters"},
		inspect.InvestigationCoverage{Source: "usage", State: "sample only", Detail: "Current sample over its reported window; no usage history or causal diagnosis"},
	)
	b.WriteString("\n" +
		"Evidence comes from the Kubernetes Pod API, retained Events and optional metrics.k8s.io.\n" +
		"OOM and scheduling evidence do not by themselves establish a cause.\n" +
		"No aggregate allocation is invented for missing values or overlapping init/sidecar phases.\n" +
		"Refresh with r; g opens related resources; Esc returns and cancels waiting reads.\n")
	snapshot.Text = b.String()
	return snapshot, nil
}

func newPressureInvestigation(obj *unstructured.Unstructured, contextName, gvr string, now time.Time) *inspect.Investigation {
	investigation := inspect.NewInvestigation(obj, contextName, gvr, now)
	investigation.Resources = nil
	// Metrics coverage is reported per Pod, including denied and stale samples;
	// replace the generic placeholder before collecting from that same snapshot.
	coverage := investigation.Coverage[:0]
	for _, source := range investigation.Coverage {
		if source.Source != capabilityTaskMetrics {
			coverage = append(coverage, source)
		}
	}
	investigation.Coverage = coverage
	return investigation
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

func pressureBudgets(pod *corev1.Pod, metrics *pressureMetrics) []inspect.ResourceBudget {
	return inspect.NewResourceBudgets(pod, &metrics.sample, metrics.containers, metrics.window)
}

func pressureResourceValues(
	resources corev1.ResourceRequirements, usage corev1.ResourceList, resource corev1.ResourceName,
	fresh bool, quantityLabel func(corev1.ResourceList, corev1.ResourceName) string,
) (request, limit, used, requestRatio, limitRatio string) {
	request, limit = quantityLabel(resources.Requests, resource), quantityLabel(resources.Limits, resource)
	used, requestRatio, limitRatio = pressureNA, pressureNA, pressureNA
	quantity, found := usage[resource]
	if !found || quantity.Sign() < 0 || !fresh {
		return
	}
	used = quantityLabel(usage, resource)
	if allocation, found := resources.Requests[resource]; found && allocation.Sign() > 0 {
		requestRatio = fmt.Sprintf("%.1f%%", 100*quantity.AsApproximateFloat64()/allocation.AsApproximateFloat64())
	}
	if allocation, found := resources.Limits[resource]; found && allocation.Sign() > 0 {
		limitRatio = fmt.Sprintf("%.1f%%", 100*quantity.AsApproximateFloat64()/allocation.AsApproximateFloat64())
	}
	return
}

func renderContainerPressure(b *strings.Builder, name, phase string, resources corev1.ResourceRequirements, status *corev1.ContainerStatus, metrics *pressureMetrics) {
	fmt.Fprintf(b, "\n  CONTAINER %s (%s)\n", name, phase)
	usage := metrics.containers[name]
	for _, resource := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, limit, use, requestRatio, limitRatio := pressureResourceValues(resources, usage, resource, metrics.sample.Fresh(), pressureQuantity)
		fmt.Fprintf(b, "    %s request=%s  limit=%s  usage=%s  usage/request=%s  usage/limit=%s\n",
			resource, request, limit, use, requestRatio, limitRatio)
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
		return pressureUnset
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
	text, _, _ := loadPressureEvents(ctx, conn, pod)
	return text
}

// loadPressureEvents gives the compact view source records from the same
// bounded UID lookup as the full report, including source visibility.
func loadPressureEvents(ctx context.Context, conn client.Connection, pod *corev1.Pod) (string, []corev1.Event, inspect.InvestigationCoverage) {
	coverage := inspect.InvestigationCoverage{Source: "events " + pod.Name, State: inspect.ObservationUnknown}
	if err := ctx.Err(); err != nil {
		coverage.Detail = err.Error()
		return "\nEVENTS: N/A (" + err.Error() + ")\n", nil, coverage
	}
	if pod.UID == "" {
		coverage.Detail = "Pod UID missing; no name-only event lookup"
		return "\nEVENTS: N/A (Pod UID missing; no name-only event lookup)\n", nil, coverage
	}
	k, err := conn.Dial()
	if err != nil {
		coverage.Detail = err.Error()
		return "\nEVENTS: N/A (" + err.Error() + ")\n", nil, coverage
	}
	events, err := k.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(pod.UID)).String(), Limit: maxPressureEvents,
	})
	if err != nil {
		coverage.State, coverage.Detail = string(client.MetricErrorState(err)), err.Error()
		return "\nEVENTS: N/A (" + err.Error() + ")\n", nil, coverage
	}
	coverage.State, coverage.Detail = inspect.ObservationComplete, "Retained Events snapshot; matching Pod UID"
	sort.SliceStable(events.Items, func(i, j int) bool { return eventTime(&events.Items[i]).After(eventTime(&events.Items[j])) })
	var b strings.Builder
	b.WriteString("\nEVENTS (Kubernetes Events API, matching Pod UID; retained snapshot)\n")
	if len(events.Items) == 0 {
		b.WriteString("No retained events reported; this does not establish health.\n")
		coverage.State, coverage.Detail = "empty", "No retained events; this does not establish health"
	}
	var relevant []corev1.Event
	for i := range events.Items {
		event := &events.Items[i]
		if event.InvolvedObject.UID != pod.UID {
			continue
		}
		if event.Reason != "FailedScheduling" && event.Reason != "OOMKilling" && event.Reason != "BackOff" && event.Type != corev1.EventTypeWarning {
			continue
		}
		relevant = append(relevant, *event)
		fmt.Fprintf(&b, "%s %s %s count=%d source=%s/%s\n  %s\n",
			pressureTimestamp(eventTime(event)), event.Type, event.Reason, event.Count, event.Source.Component, event.ReportingController, event.Message)
	}
	if events.Continue != "" {
		fmt.Fprintf(&b, "Events truncated at %d; open Events for more.\n", maxPressureEvents)
		coverage.State, coverage.Detail = inspect.ObservationIncomplete, fmt.Sprintf("Events truncated at %d; open Events for more", maxPressureEvents)
	}
	return b.String(), relevant, coverage
}

func pressureTimestamp(t time.Time) string {
	if t.IsZero() {
		return pressureUnknown
	}
	return t.UTC().Format(time.RFC3339)
}
