// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package render

import (
	"github.com/derailed/k9s/internal/client"
	v1 "k8s.io/api/core/v1"
	mv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func metricUsage(value int64, available, memory bool) string {
	if !available {
		return NAValue
	}
	if memory {
		return toMi(value)
	}
	return toMc(value)
}

func metricPercentage(value, capacity int64, available bool) string {
	if !available {
		return NAValue
	}
	return client.ToPercentageStr(value, capacity)
}

// A pod aggregate is known only when every app container and restartable init
// container contributes that dimension. Missing CPU does not invalidate memory.
func podUsageAvailability(spec *v1.PodSpec, metrics []mv1beta1.ContainerMetrics) (cpu, memory bool) {
	if len(metrics) == 0 {
		return false, false
	}
	byName := make(map[string]v1.ResourceList, len(metrics))
	for i := range metrics {
		byName[metrics[i].Name] = metrics[i].Usage
	}
	containers := append(filterSidecarCO(spec.InitContainers), spec.Containers...)
	if len(containers) == 0 {
		return false, false
	}
	cpu, memory = true, true
	for i := range containers {
		usage := byName[containers[i].Name]
		_, hasCPU := usage[v1.ResourceCPU]
		_, hasMemory := usage[v1.ResourceMemory]
		cpu, memory = cpu && hasCPU, memory && hasMemory
	}
	return cpu, memory
}
