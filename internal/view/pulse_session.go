// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
)

func (p *Pulse) rebindSession(namespace string) {
	p.model = model.NewPulse(p.GVR())
	p.model.SetNamespace(namespace)
	source := p.metricsSample.Source
	if source == "" {
		source = client.NodeMetricsSource
		if !client.IsClusterWide(namespace) {
			source = client.PodMetricsSource
		}
	}
	p.metricsSample = client.MetricFailure(p.metricsSample, client.MetricsUnavailable, source,
		"session reconnected; waiting for a fresh sample")
	p.metricsPoint.Sample = p.metricsSample
	p.SeriesChanged(dao.TimeSeries{p.metricsPoint})
}
