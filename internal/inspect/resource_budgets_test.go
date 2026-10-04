// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResourceBudgetsSharePressureRolesAndUnavailableMeaning(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	budget := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api", UID: "selected-pod"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Resources: budget}}, InitContainers: []corev1.Container{{Name: "init", Resources: budget}, {Name: "proxy", RestartPolicy: &always, Resources: budget}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "api", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, Ready: false}}}}
	usage := map[string]corev1.ResourceList{"api": {corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}
	sample := client.NewMetricSample(client.ClusterMetrics{}, time.Now(), client.PodMetricsSource, time.Now())
	rows := NewResourceBudgets(pod, &sample, usage, time.Minute)
	require.Len(t, rows, 3)
	require.Equal(t, "init", rows[0].Role)
	require.Equal(t, "sidecar", rows[1].Role)
	require.Equal(t, "app", rows[2].Role)
	require.Equal(t, "running / not ready", rows[2].CurrentState)
	require.Equal(t, "25m", rows[2].CPUUsage)
	require.Equal(t, "25.0%", rows[2].CPURequestRatio)
	require.Equal(t, "N/A", rows[2].CPULimitRatio)
	require.Equal(t, "50.0%", rows[2].MemoryRequestRatio)
	require.Equal(t, "25.0%", rows[2].MemoryLimitRatio)
	require.Equal(t, "selected-pod", rows[2].UID)
	require.Equal(t, time.Minute, rows[2].Window)
	sample.State = client.MetricsDenied
	sample.Reason = "permission denied"
	unavailable := NewResourceBudgets(pod, &sample, usage, time.Minute)
	require.Equal(t, "N/A", unavailable[2].CPUUsage)
	require.Equal(t, "N/A", unavailable[2].CPURequestRatio)
	require.Equal(t, "permission denied", unavailable[2].MetricsReason)
}
