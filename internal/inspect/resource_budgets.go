// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	corev1 "k8s.io/api/core/v1"
)

const budgetUnavailable = "N/A"

// NewResourceBudgets is the shared per-container pressure projection. It keeps
// measured usage, configured requests/limits, phases and unavailable evidence
// distinct; it does not sum overlapping init/sidecar phases.
func NewResourceBudgets(pod *corev1.Pod, sample *client.MetricSample, containers map[string]corev1.ResourceList, window time.Duration) []ResourceBudget {
	statuses := make(map[string]*corev1.ContainerStatus)
	for _, group := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for index := range group {
			statuses[group[index].Name] = &group[index]
		}
	}
	var budgets []ResourceBudget
	add := func(name, role string, resources corev1.ResourceRequirements) {
		budget := ResourceBudget{Pod: pod.Name, UID: string(pod.UID), Container: name, Role: role,
			MetricsState: string(sample.State), MetricsReason: sample.Reason,
			ObservedAt: sample.ObservedAt, Window: window, CurrentState: ObservationUnknown}
		budget.CPURequest, budget.CPULimit, budget.CPUUsage, budget.CPURequestRatio, budget.CPULimitRatio = budgetResourceValues(
			resources, containers[name], corev1.ResourceCPU, sample.Fresh(), budgetQuantity,
		)
		budget.MemoryRequest, budget.MemoryLimit, budget.MemoryUsage, budget.MemoryRequestRatio, budget.MemoryLimitRatio = budgetResourceValues(
			resources, containers[name], corev1.ResourceMemory, sample.Fresh(), budgetQuantity,
		)
		if status := statuses[name]; status != nil {
			switch {
			case status.State.Waiting != nil:
				budget.CurrentState = "waiting " + status.State.Waiting.Reason
			case status.State.Terminated != nil:
				budget.CurrentState = fmt.Sprintf("%s · exit %d", status.State.Terminated.Reason, status.State.Terminated.ExitCode)
			case status.State.Running != nil:
				budget.CurrentState = "running"
				if role == containerRoleApp && !status.Ready {
					budget.CurrentState += " / not ready"
				}
			}
		}
		budgets = append(budgets, budget)
	}
	for index := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[index]
		role := "init"
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			role = "sidecar"
		}
		add(container.Name, role, container.Resources)
	}
	for index := range pod.Spec.Containers {
		container := &pod.Spec.Containers[index]
		add(container.Name, containerRoleApp, container.Resources)
	}
	for index := range pod.Spec.EphemeralContainers {
		container := &pod.Spec.EphemeralContainers[index]
		add(container.Name, "ephemeral", container.Resources)
	}
	return budgets
}

func budgetResourceValues(
	resources corev1.ResourceRequirements, usage corev1.ResourceList, resource corev1.ResourceName,
	fresh bool, quantityLabel func(corev1.ResourceList, corev1.ResourceName) string,
) (request, limit, used, requestRatio, limitRatio string) {
	request, limit = quantityLabel(resources.Requests, resource), quantityLabel(resources.Limits, resource)
	used, requestRatio, limitRatio = budgetUnavailable, budgetUnavailable, budgetUnavailable
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

func budgetQuantity(values corev1.ResourceList, name corev1.ResourceName) string {
	q, ok := values[name]
	if !ok {
		return "N/A (unset)"
	}
	if name == corev1.ResourceCPU {
		return fmt.Sprintf("%gm", 1000*q.AsApproximateFloat64())
	}
	return fmt.Sprintf("%.2fMiB", float64(q.Value())/(1024*1024))
}
