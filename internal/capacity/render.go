// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

var Tabs = []string{"Overview", SourcePods, "Admission", "Scaling", SourceNodes, "Evidence"}

// Render formats only typed observations; the UI can filter and retain each
// tab independently. Full evidence is available without repeating it first.
func (s *Snapshot) Render(tab int) string {
	var b strings.Builder
	switch tab {
	case 1:
		s.podText(&b)
	case 2:
		s.admissionText(&b)
	case 3:
		s.scalingText(&b)
	case 4:
		s.nodeText(&b)
	case 5:
		s.evidenceText(&b)
	default:
		s.overviewText(&b)
	}
	return b.String()
}

func (s *Snapshot) overviewText(b *strings.Builder) {
	var pending, active, measured, unbounded int
	requests, reservation, limits, usage := make(corev1.ResourceList), make(corev1.ResourceList), make(corev1.ResourceList), make(corev1.ResourceList)
	for index := range s.Pods {
		pod := &s.Pods[index]
		if len(pod.Pending) > 0 || pod.Phase == string(corev1.PodPending) {
			pending++
		}
		if pod.Terminal {
			continue
		}
		active++
		Add(requests, pod.Requests)
		Add(reservation, pod.SchedulerRequests)
		Add(limits, pod.Limits)
		unbounded += len(pod.LimitGaps)
		if pod.Usage.Sample.At(time.Now()).Fresh() && pod.Usage.Complete {
			measured++
			Add(usage, pod.Usage.Values)
		}
	}
	state := "complete visible page"
	if s.Partial() {
		state = "partial evidence"
	}
	podSource := s.Source(SourcePods)
	if podSource.State != Complete && podSource.State != Empty && podSource.State != Partial {
		fmt.Fprintf(b, "Pending Pods: unknown | %s\n", state)
		fmt.Fprintf(b, "Requests: N/A (Pods %s)\nScheduling reservation: N/A\nReported limits: N/A\n", podSource.State)
	} else {
		fmt.Fprintf(b, "%d Pending Pods | %s\n", pending, state)
		fmt.Fprintf(b, "Requests: CPU %s | memory %s\n", Quantity(requests, corev1.ResourceCPU), Quantity(requests, corev1.ResourceMemory))
		fmt.Fprintf(b, "Scheduling reservation: CPU %s | memory %s\n", Quantity(reservation, corev1.ResourceCPU), Quantity(reservation, corev1.ResourceMemory))
		fmt.Fprintf(b, "Reported limits: CPU %s | memory %s", Quantity(limits, corev1.ResourceCPU), Quantity(limits, corev1.ResourceMemory))
		if unbounded > 0 {
			fmt.Fprintf(b, " | %d unset/zero limits", unbounded)
		}
		b.WriteByte('\n')
	}
	if measured > 0 {
		fmt.Fprintf(b, "Usage: CPU %s | memory %s (%d/%d fresh complete Pods)\n", Quantity(usage, corev1.ResourceCPU), Quantity(usage, corev1.ResourceMemory), measured, active)
	} else {
		fmt.Fprintf(b, "Usage: N/A (%d/%d fresh complete Pods)\n", measured, active)
	}
	if measured < active {
		b.WriteString("Usage totals cover measured Pods only; missing/stale samples are not zero.\n")
	}
	fmt.Fprintf(b, "Scope totals: %d visible nonterminal Pods (%s).\n", active, s.Source(SourcePods).State)
	b.WriteString("\nNext evidence checks\n")
	checks := 0
	for index := range s.Quotas {
		quota := &s.Quotas[index]
		for _, name := range resourceNames(quota.Hard) {
			if remaining, known := Remaining(quota.Hard, quota.Used, name); known && remaining.Sign() <= 0 {
				fmt.Fprintf(b, "- Quota %s/%s: %s remaining %s; review Admission.\n", quota.Identity.Namespace, quota.Identity.Name, name, remaining.String())
				checks++
				if checks >= 3 {
					break
				}
			}
		}
		if checks >= 3 {
			break
		}
	}
	for index := range s.Pods {
		pod := &s.Pods[index]
		if len(pod.Pending) > 0 && checks < 4 {
			fmt.Fprintf(b, "- %s/%s: %s; review Pods for the scheduler message.\n", pod.Identity.Namespace, pod.Identity.Name, pod.Pending[0].Reason)
			checks++
		}
	}
	if checks == 0 {
		b.WriteString("- Compare Admission and Scaling evidence; no blocker was established in this page.\n")
	}
	fmt.Fprintf(b, "Node visibility: %d nodes (%s); review Nodes for allocatable/taints.\n", len(s.Nodes), s.Source(SourceNodes).State)
	b.WriteString("Aggregate resources do not prove schedulability. No free-CPU estimate is made.\n")
	b.WriteString("History: not configured; observed usage is a current sample, not a trend.\n")
}

func (s *Snapshot) podText(b *strings.Builder) {
	fmt.Fprintf(b, "POD BUDGETS | %s\n", s.Source(SourcePods).State)
	b.WriteString("Requests/limits use accepted spec; reservation includes observed resize allocation, init phases, restartable sidecars and overhead.\n")
	for index := range s.Pods {
		pod := &s.Pods[index]
		fmt.Fprintf(b, "\n%s/%s | %s | node %s\nUID: %s\n", pod.Identity.Namespace, pod.Identity.Name, pod.Phase, orUnknown(pod.Node), orUnknown(pod.Identity.UID))
		for _, pending := range pod.Pending {
			fmt.Fprintf(b, "PodScheduled=%s %s at %s\n%s\n", pending.Status, pending.Reason, stamp(pending.LastTransitionTime.Time), pending.Message)
		}
		fmt.Fprintf(b, "Requests: %s\nReservation: %s\nReported limits: %s\n", ResourceText(pod.Requests), ResourceText(pod.SchedulerRequests), ResourceText(pod.Limits))
		for _, gap := range pod.LimitGaps {
			fmt.Fprintf(b, "Limit gap: %s; the reported sum is not a resource cap.\n", gap)
		}
		sample := pod.Usage.Sample.At(time.Now())
		fmt.Fprintf(b, "Metrics: %s | source %s | observed %s | window %s\n",
			sample.State, sample.Source, stamp(sample.ObservedAt), pod.Usage.Window)
		if pod.Usage.Sample.Reason != "" {
			fmt.Fprintf(b, "Metrics detail: %s\n", pod.Usage.Sample.Reason)
		}
		if sample.Reason != "" && sample.Reason != pod.Usage.Sample.Reason {
			fmt.Fprintf(b, "Current availability: %s\n", sample.Reason)
		}
		if sample.HasValue() && !sample.Fresh() {
			fmt.Fprintf(b, "Retained usage (stale; not current): %s\n", ResourceText(pod.Usage.Values))
		}
		for index := range pod.Containers {
			container := &pod.Containers[index]
			cpuUsage, memoryUsage := container.CPUUsage, container.MemoryUsage
			if !sample.Fresh() {
				cpuUsage, memoryUsage = "N/A ("+string(sample.State)+")", "N/A ("+string(sample.State)+")"
			}
			fmt.Fprintf(b, "%s (%s): CPU req %s / limit %s / usage %s\n",
				container.Container, container.Role, container.CPURequest, container.CPULimit, cpuUsage)
			fmt.Fprintf(b, "  memory req %s / limit %s / usage %s\n", container.MemoryRequest, container.MemoryLimit, memoryUsage)
		}
		for _, constraint := range pod.Placement {
			fmt.Fprintf(b, "Placement: %s\n", constraint)
		}
	}
	if len(s.Pods) == 0 {
		b.WriteString("No Pods in the visible page; this does not establish available capacity.\n")
	}
}

func (s *Snapshot) admissionText(b *strings.Builder) {
	fmt.Fprintf(b, "ADMISSION | Quota %s | LimitRange %s\n", s.Source(SourceQuota).State, s.Source(SourceLimitRange).State)
	b.WriteString("Quota status is controller-reported consumption. Each quota has its own scope; quota headroom does not prove admission or placement.\n")
	for index := range s.Quotas {
		quota := &s.Quotas[index]
		fmt.Fprintf(b, "\nQuota %s/%s | UID %s\n", quota.Identity.Namespace, quota.Identity.Name, orUnknown(quota.Identity.UID))
		fmt.Fprintf(b, "Configured hard: %s\n", ResourceText(quota.Desired))
		if len(quota.Scopes) > 0 {
			fmt.Fprintf(b, "Scopes: %v\n", quota.Scopes)
		}
		if quota.ScopeSelector != nil {
			for _, requirement := range quota.ScopeSelector.MatchExpressions {
				fmt.Fprintf(b, "Scope selector: %s %s %v\n", requirement.ScopeName, requirement.Operator, requirement.Values)
			}
		}
		if len(quota.Hard) == 0 {
			b.WriteString("No status hard values reported; enforcement status is unknown.\n")
		}
		for _, name := range resourceNames(quota.Hard) {
			remaining := "unknown (used not reported)"
			if q, known := Remaining(quota.Hard, quota.Used, name); known {
				remaining = q.String()
			}
			fmt.Fprintf(b, "%s hard %s | used %s | remaining %s\n", name, Quantity(quota.Hard, name), Quantity(quota.Used, name), remaining)
		}
	}
	for index := range s.LimitRanges {
		limit := &s.LimitRanges[index]
		fmt.Fprintf(b, "\nLimitRange %s/%s | UID %s\n", limit.Identity.Namespace, limit.Identity.Name, orUnknown(limit.Identity.UID))
		for _, item := range limit.Limits {
			fmt.Fprintf(b, "%s: min %s\n  max %s\n  default request %s\n  default limit %s\n  max limit/request ratio %s\n",
				item.Type,
				ResourceText(item.Min),
				ResourceText(item.Max),
				ResourceText(item.DefaultRequest),
				ResourceText(item.Default),
				ResourceText(item.MaxLimitRequestRatio))
		}
	}
	if len(s.Quotas) == 0 {
		fmt.Fprintf(b, "\nNo quota objects retained (%s); denied/absent is distinct from an empty successful read.\n", s.Source(SourceQuota).State)
	}
	b.WriteString("\nCurrent Pod specs already reflect admission defaults. No LimitRange default is silently applied to these observations.\n")
}

func (s *Snapshot) scalingText(b *strings.Builder) {
	fmt.Fprintf(b, "AUTOSCALING | HPA %s | VPA %s\n", s.Source(SourceHPA).State, s.Source(SourceVPA).State)
	b.WriteString("Configured target references are names, not verified owner UIDs. Recommendations and current usage are separate evidence.\n")
	for index := range s.Autoscalers {
		hpa := &s.Autoscalers[index]
		minimum := "default (1)"
		if hpa.Min != nil {
			minimum = fmt.Sprint(*hpa.Min)
		}
		current, desired := unknownValue, unknownValue
		if hpa.CurrentReported {
			current = fmt.Sprint(hpa.Current)
		}
		if hpa.DesiredReported {
			desired = fmt.Sprint(hpa.Desired)
		}
		fmt.Fprintf(b, "\nHPA %s/%s | UID %s\nTarget: %s %s/%s\nReplicas: min %s | max %d | current %s | desired %s\n",
			hpa.Identity.Namespace, hpa.Identity.Name, orUnknown(hpa.Identity.UID), hpa.Target.APIVersion,
			hpa.Target.Kind, hpa.Target.Name, minimum, hpa.Max, current, desired)
		if hpa.ObservedGeneration == nil {
			b.WriteString("Status generation: not reported; current controller progress is unknown.\n")
		} else if *hpa.ObservedGeneration < hpa.Generation {
			fmt.Fprintf(b, "Status stale: observed generation %d, spec generation %d\n", *hpa.ObservedGeneration, hpa.Generation)
		}
		for _, metric := range hpa.Metrics {
			fmt.Fprintf(b, "Configured metric: %s\n", metricSpec(metric))
		}
		if len(hpa.CurrentMetrics) == 0 {
			b.WriteString("Current metric inputs: N/A (not reported by HPA status)\n")
		}
		for _, missing := range hpa.MissingInputs() {
			fmt.Fprintf(b, "Missing current input: %s\n", missing)
		}
		for _, metric := range hpa.CurrentMetrics {
			fmt.Fprintf(b, "Current metric: %s\n", metricStatus(metric))
		}
		for _, condition := range hpa.Conditions {
			fmt.Fprintf(b, "%s=%s | %s | at %s\n  %s\n", condition.Type, condition.Status, condition.Reason, stamp(condition.LastTransitionTime.Time), condition.Message)
		}
	}
	for index := range s.Recommendations {
		vpa := &s.Recommendations[index]
		fmt.Fprintf(b, "\nVPA %s/%s | UID %s\nTarget: %s | update mode %s\n", vpa.Identity.Namespace, vpa.Identity.Name, orUnknown(vpa.Identity.UID),
			orUnknown(vpa.APIVersion)+" "+vpa.Target, orUnknown(vpa.Mode))
		if len(vpa.Containers) == 0 {
			b.WriteString("No container recommendation reported.\n")
		}
		for _, c := range vpa.Containers {
			fmt.Fprintf(b, "%s target %s\n  lower %s\n  upper %s\n", c.Name, ResourceText(c.Target), ResourceText(c.Lower), ResourceText(c.Upper))
		}
	}
	b.WriteString("\nThis review changes no replica count or resource request. HPA status is not a historical workload trend.\n")
}

func (s *Snapshot) nodeText(b *strings.Builder) {
	fmt.Fprintf(b, "NODE EVIDENCE | %d visible | %s\n", len(s.Nodes), s.Source(SourceNodes).State)
	b.WriteString("Allocatable is configured node capacity. Selected-scope Pod requests do not describe other namespaces or all " +
		"allocations. No schedulability verdict is inferred.\n")
	for index := range s.Nodes {
		node := &s.Nodes[index]
		fmt.Fprintf(b, "\n%s | UID %s | unschedulable %t\nAllocatable: %s\n",
			node.Identity.Name,
			orUnknown(node.Identity.UID),
			node.Unschedulable,
			ResourceText(node.Allocatable))
		for _, condition := range node.Conditions {
			if condition.Type == corev1.NodeReady || condition.Status == corev1.ConditionTrue {
				fmt.Fprintf(b, "%s=%s | %s | %s\n", condition.Type, condition.Status, condition.Reason, condition.Message)
			}
		}
		for _, taint := range node.Taints {
			fmt.Fprintf(b, "Taint: %s=%s:%s\n", taint.Key, taint.Value, taint.Effect)
		}
	}
	b.WriteString("\nPlacement also depends on per-node reservations, affinities, taints, topology, volumes, ports and scheduler " +
		"policy; those checks are not reduced to aggregate CPU.\n")
}

func (s *Snapshot) evidenceText(b *strings.Builder) {
	fmt.Fprintf(b, "READ-ONLY CAPACITY SNAPSHOT\nContext: %s\nSelected: %s %s/%s\nUID: %s\nCaptured: %s\n",
		s.Scope.Identity.Context,
		s.Scope.Identity.GVR,
		s.Scope.Identity.Namespace,
		s.Scope.Identity.Name,
		orUnknown(s.Scope.Identity.UID),
		stamp(s.CapturedAt))
	namespace := s.Scope.Namespace
	if namespace == "" {
		namespace = "all"
	}
	selector := s.Scope.Selector
	if selector == "" {
		selector = "none"
	}
	fmt.Fprintf(b, "Pod namespace: %s\nSelector: %s\nPod name: %s\nNode: %s\n",
		namespace, selector, orUnknown(s.Scope.PodName), orUnknown(s.Scope.Node))
	b.WriteString("\nIndependent source reads are not an atomic cluster snapshot. No Secret API is queried.\n")
	for _, coverage := range s.Coverage {
		fmt.Fprintf(b, "\n%s: %s | read %s | visible %d | bound %d\n%s\n",
			coverage.Source,
			coverage.State,
			stamp(coverage.ReadAt),
			coverage.Visible,
			coverage.Bound,
			coverage.Detail)
	}
	b.WriteString("\nCPU: 1000m = one core. Memory quantities retain Kubernetes decimal/binary units. Effective requirement " +
		"calculation uses the Kubernetes v1.35 client helper and observed feature fields; it does not assert the cluster's scheduler feature-gate configuration.\n")
	b.WriteString("Requests reserve scheduling resources. Reported container limits may leave other containers unbounded. " +
		"Measured usage has its own observation time/window and does not become a configured budget or historical recommendation.\n")
	b.WriteString("History is unavailable until a named provider and explicit coverage window are configured.\n")
}

func metricSpec(m autoscalingv2.MetricSpec) string {
	switch {
	case m.Resource != nil:
		return "Resource " + string(m.Resource.Name) + " " + metricTarget(m.Resource.Target)
	case m.ContainerResource != nil:
		return "Container " + m.ContainerResource.Container + " " + string(m.ContainerResource.Name) + " " + metricTarget(m.ContainerResource.Target)
	case m.Pods != nil:
		return "Pods " + m.Pods.Metric.Name + " " + metricTarget(m.Pods.Target)
	case m.Object != nil:
		return "Object " + m.Object.DescribedObject.Kind + "/" + m.Object.DescribedObject.Name + " " + m.Object.Metric.Name + " " + metricTarget(m.Object.Target)
	case m.External != nil:
		return "External " + m.External.Metric.Name + " " + metricTarget(m.External.Target)
	default:
		return "unknown/unreported specification"
	}
}
func metricStatus(m autoscalingv2.MetricStatus) string {
	switch {
	case m.Resource != nil:
		return "Resource " + string(m.Resource.Name) + " " + metricValue(m.Resource.Current)
	case m.ContainerResource != nil:
		return "Container " + m.ContainerResource.Container + " " + string(m.ContainerResource.Name) + " " + metricValue(m.ContainerResource.Current)
	case m.Pods != nil:
		return "Pods " + m.Pods.Metric.Name + " " + metricValue(m.Pods.Current)
	case m.Object != nil:
		return "Object " + m.Object.Metric.Name + " " + metricValue(m.Object.Current)
	case m.External != nil:
		return "External " + m.External.Metric.Name + " " + metricValue(m.External.Current)
	default:
		return notReported
	}
}
func metricTarget(t autoscalingv2.MetricTarget) string {
	v := autoscalingv2.MetricValueStatus{Value: t.Value, AverageValue: t.AverageValue, AverageUtilization: t.AverageUtilization}
	return string(t.Type) + " " + metricValue(v)
}
func metricValue(v autoscalingv2.MetricValueStatus) string {
	var values []string
	if v.Value != nil {
		values = append(values, "value="+v.Value.String())
	}
	if v.AverageValue != nil {
		values = append(values, "average="+v.AverageValue.String())
	}
	if v.AverageUtilization != nil {
		values = append(values, fmt.Sprintf("average utilization=%d%%", *v.AverageUtilization))
	}
	if len(values) == 0 {
		return "N/A (not reported)"
	}
	return strings.Join(values, " ")
}
func resourceNames(values corev1.ResourceList) []corev1.ResourceName {
	var names []corev1.ResourceName
	for name := range values {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}
func orUnknown(value string) string {
	if value == "" {
		return unknownValue
	}
	return value
}
func stamp(at time.Time) string {
	if at.IsZero() {
		return notReported
	}
	return at.UTC().Format(time.RFC3339)
}
