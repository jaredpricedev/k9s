// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"reflect"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
)

// MissingInputs identifies configured inputs without a matching reported
// measurement. One reported metric does not establish all inputs are available.
func (h *Autoscaler) MissingInputs() []string {
	var missing []string
	for i := range h.Metrics {
		configured := &h.Metrics[i]
		found := false
		for j := range h.CurrentMetrics {
			if metricMatches(configured, &h.CurrentMetrics[j]) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, metricSpec(*configured))
		}
	}
	return missing
}

func metricMatches(spec *autoscalingv2.MetricSpec, status *autoscalingv2.MetricStatus) bool {
	if spec.Type != status.Type {
		return false
	}
	switch {
	case spec.Resource != nil && status.Resource != nil:
		return spec.Resource.Name == status.Resource.Name && metricValueReported(spec.Resource.Target.Type, status.Resource.Current)
	case spec.ContainerResource != nil && status.ContainerResource != nil:
		return spec.ContainerResource.Name == status.ContainerResource.Name &&
			spec.ContainerResource.Container == status.ContainerResource.Container &&
			metricValueReported(spec.ContainerResource.Target.Type, status.ContainerResource.Current)
	case spec.Pods != nil && status.Pods != nil:
		return reflect.DeepEqual(spec.Pods.Metric, status.Pods.Metric) && metricValueReported(spec.Pods.Target.Type, status.Pods.Current)
	case spec.Object != nil && status.Object != nil:
		return spec.Object.DescribedObject == status.Object.DescribedObject && reflect.DeepEqual(spec.Object.Metric, status.Object.Metric) &&
			metricValueReported(spec.Object.Target.Type, status.Object.Current)
	case spec.External != nil && status.External != nil:
		return reflect.DeepEqual(spec.External.Metric, status.External.Metric) && metricValueReported(spec.External.Target.Type, status.External.Current)
	default:
		return false
	}
}

func metricValueReported(kind autoscalingv2.MetricTargetType, value autoscalingv2.MetricValueStatus) bool {
	switch kind {
	case autoscalingv2.UtilizationMetricType:
		return value.AverageUtilization != nil
	case autoscalingv2.AverageValueMetricType:
		return value.AverageValue != nil
	case autoscalingv2.ValueMetricType:
		return value.Value != nil
	default:
		return false
	}
}
