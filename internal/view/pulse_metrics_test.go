// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/tchart"
	"github.com/stretchr/testify/require"
)

type pulseObservedChart struct {
	*tchart.SparkLine
	legend                 string
	severity, observations int
}

func (c *pulseObservedChart) SetLegend(value string)  { c.legend = value }
func (c *pulseObservedChart) SetColorIndex(value int) { c.severity = value }
func (c *pulseObservedChart) AddMetric(at time.Time, value float64) {
	c.observations++
	c.SparkLine.AddMetric(at, value)
}

func TestPulseNamespaceShowsMeasuredTotalsWithoutInventingCapacityOrHealth(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	cpu := &pulseObservedChart{SparkLine: tchart.NewSparkLine("CPU", "m"), severity: -1}
	memory := &pulseObservedChart{SparkLine: tchart.NewSparkLine("MEM", "Mi"), severity: -1}
	pulse := &Pulse{app: app, charts: Charts{client.CpuGVR: cpu, client.MemGVR: memory}}
	now := time.Now()
	point := dao.Point{Time: now, Sample: client.NewMetricSample(client.ClusterMetrics{PercCPU: 100, PercMEM: 100}, now, client.PodMetricsSource, now)}
	point.Value.CurrentCPU, point.Value.CurrentMEM = 1234, 56
	point.Value.AllocatableCPU, point.Value.AllocatableMEM = 1234, 56
	pulse.SeriesChanged(dao.TimeSeries{point})
	require.Contains(t, cpu.legend, "1234m (usage)")
	require.Contains(t, memory.legend, "56Mi (usage)")
	require.NotContains(t, cpu.legend, "%")
	require.NotContains(t, memory.legend, "%")
	require.Zero(t, cpu.severity)
	require.Zero(t, memory.severity)
	require.Equal(t, 1, cpu.observations)
	point.Sample = client.MetricFailure(point.Sample, client.MetricsUnavailable, client.PodMetricsSource, "HTTP 503")
	pulse.SeriesChanged(dao.TimeSeries{point})
	require.Contains(t, cpu.legend, "1234m (stale: unavailable)")
	require.Contains(t, memory.legend, "56Mi (stale: unavailable)")
	require.Equal(t, 1, cpu.observations, "stale observations must not become chart history")
	require.Equal(t, 1, memory.observations)
	point.Sample = client.MetricFailure(client.MetricSample{}, client.MetricsDenied, client.PodMetricsSource, "denied")
	pulse.SeriesChanged(dao.TimeSeries{point})
	require.Contains(t, cpu.legend, "N/A (denied)")
	require.Contains(t, memory.legend, "N/A (denied)")
	require.Equal(t, 1, cpu.observations)
}
