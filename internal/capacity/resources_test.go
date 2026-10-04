// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestRequirementsRespectSequentialInitRestartableSidecarAndOverhead(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: quantities("200m", "128Mi"), Limits: quantities("400m", "256Mi")}}},
		InitContainers: []corev1.Container{
			{Name: "sidecar", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: quantities("50m", "16Mi"), Limits: quantities("100m", "32Mi")}},
			{Name: "migration", Resources: corev1.ResourceRequirements{Requests: quantities("1", "1Gi"), Limits: quantities("2", "2Gi")}},
			{Name: "warmup", Resources: corev1.ResourceRequirements{Requests: quantities("500m", "256Mi"), Limits: quantities("1", "512Mi")}},
		}, Overhead: quantities("30m", "8Mi"),
	}}
	requests, limits, reservation := Requirements(pod)
	require.Equal(t, "1080m", Quantity(requests, corev1.ResourceCPU))
	require.Equal(t, "1048Mi", Quantity(requests, corev1.ResourceMemory))
	require.Equal(t, "2130m", Quantity(limits, corev1.ResourceCPU))
	require.Equal(t, "2088Mi", Quantity(limits, corev1.ResourceMemory))
	require.Equal(t, "1080m", Quantity(reservation, corev1.ResourceCPU))
	require.Equal(t, "1", Quantity(pod.Spec.InitContainers[1].Resources.Requests, corev1.ResourceCPU), "source quantities remain immutable")
	require.Empty(t, LimitGaps(pod))
}

func TestRequirementsPreservePodLevelBudgetAndObservedResizeAllocation(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: quantities("250m", "64Mi")}}}, Resources: &corev1.ResourceRequirements{Requests: quantities("2", "1Gi"), Limits: quantities("4", "2Gi")}, Overhead: quantities("50m", "8Mi")}}
	requests, limits, _ := Requirements(pod)
	require.Equal(t, "2050m", Quantity(requests, corev1.ResourceCPU))
	require.Equal(t, "1032Mi", Quantity(requests, corev1.ResourceMemory))
	require.Equal(t, "4050m", Quantity(limits, corev1.ResourceCPU))
	require.Empty(t, LimitGaps(pod), "accepted Pod-level limits are reported explicitly")
	pod.Spec.Resources = nil
	pod.Spec.Overhead = nil
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", AllocatedResources: quantities("750m", "128Mi"), Resources: &corev1.ResourceRequirements{Requests: quantities("500m", "96Mi")}}}
	requested, _, reservation := Requirements(pod)
	require.Equal(t, "250m", Quantity(requested, corev1.ResourceCPU))
	require.Equal(t, "750m", Quantity(reservation, corev1.ResourceCPU), "reservation retains observed allocated CPU while spec is smaller")
	require.Equal(t, "128Mi", Quantity(reservation, corev1.ResourceMemory))
	require.Len(t, LimitGaps(pod), 2)
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}
	require.Len(t, LimitGaps(pod), 2, "zero limits cannot be advertised as an upper bound")
}
