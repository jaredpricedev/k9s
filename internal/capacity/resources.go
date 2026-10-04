// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelper "k8s.io/component-helpers/resource"
)

// Requirements uses the Kubernetes resource semantics shipped with this
// client: sequential init phases, cumulative restartable sidecars, accepted
// Pod-level budgets, overhead and observed in-place resize allocation.
func Requirements(pod *corev1.Pod) (requests, limits, scheduling corev1.ResourceList) {
	requests = resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	limits = resourcehelper.PodLimits(pod, resourcehelper.PodResourcesOptions{})
	statusPresent := pod.Status.Resources != nil
	for _, group := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for index := range group {
			status := &group[index]
			if status.Resources != nil || len(status.AllocatedResources) != 0 {
				statusPresent = true
			}
		}
	}
	scheduling = resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{
		UseStatusResources: statusPresent,
		InPlacePodLevelResourcesVerticalScalingEnabled: pod.Status.Resources != nil,
	})
	return
}

func LimitGaps(pod *corev1.Pod) []string {
	var result []string
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if pod.Spec.Resources != nil {
			if limit, ok := pod.Spec.Resources.Limits[name]; ok && limit.Sign() > 0 {
				continue
			}
		}
		for _, group := range [][]corev1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
			for index := range group {
				container := &group[index]
				if limit, ok := container.Resources.Limits[name]; !ok || limit.Sign() == 0 {
					result = append(result, fmt.Sprintf("%s/%s: no configured nonzero limit", container.Name, name))
				}
			}
		}
	}
	return result
}

func Add(target, values corev1.ResourceList) {
	for name, value := range values {
		q := target[name]
		q.Add(value)
		target[name] = q
	}
}

func Quantity(values corev1.ResourceList, name corev1.ResourceName) string {
	q, found := values[name]
	if !found {
		return "unset"
	}
	return q.String()
}

func ResourceText(values corev1.ResourceList) string {
	if len(values) == 0 {
		return "none reported"
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, string(name))
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+Quantity(values, corev1.ResourceName(name)))
	}
	return strings.Join(parts, "  ")
}

// Remaining does not invent zero usage when a quota controller has not reported it.
func Remaining(hard, used corev1.ResourceList, name corev1.ResourceName) (resource.Quantity, bool) {
	h, haveHard := hard[name]
	u, haveUsed := used[name]
	if !haveHard || !haveUsed {
		return resource.Quantity{}, false
	}
	q := h.DeepCopy()
	q.Sub(u)
	return q, true
}

func Placement(pod *corev1.Pod) []string {
	var result []string
	if pod.Spec.NodeName != "" {
		result = append(result, "Assigned node: "+pod.Spec.NodeName)
	}
	for key, value := range pod.Spec.NodeSelector {
		result = append(result, fmt.Sprintf("Node selector: %s=%s", key, value))
	}
	sort.Strings(result)
	if pod.Spec.Affinity != nil {
		result = append(result, "Affinity/anti-affinity is configured; inspect the Pod spec for exact terms")
	}
	for _, t := range pod.Spec.Tolerations {
		result = append(result, fmt.Sprintf("Toleration: %s %s %s %s", t.Key, t.Operator, t.Value, t.Effect))
	}
	for _, spread := range pod.Spec.TopologySpreadConstraints {
		result = append(result, fmt.Sprintf("Topology spread: %s maxSkew=%d %s", spread.TopologyKey, spread.MaxSkew, spread.WhenUnsatisfiable))
	}
	for index := range pod.Spec.Volumes {
		volume := &pod.Spec.Volumes[index]
		if volume.PersistentVolumeClaim != nil {
			result = append(result, "PVC placement: "+volume.PersistentVolumeClaim.ClaimName+"; binding/topology not collected here")
		}
	}
	if pod.Spec.SchedulingGates != nil {
		for _, gate := range pod.Spec.SchedulingGates {
			result = append(result, "Scheduling gate: "+gate.Name)
		}
	}
	return result
}
