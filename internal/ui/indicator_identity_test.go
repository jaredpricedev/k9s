// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

func TestLongContextPreservesModeConnectionMetricStateAndFullDestination(t *testing.T) {
	cfg := mock.NewMockConfig(t)
	cfg.K9s.ReadOnly = true
	cfg.K9s.UI.NoIcons = true
	fullContext := "production-[blue]-長い-context-" + strings.Repeat("region-", 18)
	cfg.K9s.UI.ProductionContexts = []string{fullContext}
	indicator := NewStatusIndicator(NewApp(cfg, ""), config.NewStyles())
	indicator.current = &model.ClusterMeta{Context: fullContext, Connected: true, Metrics: client.MetricSample{State: client.MetricsUnavailable, Source: client.NodeMetricsSource, Reason: "HTTP 503"}}
	indicator.previous = indicator.current
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	for _, width := range []int{80, 100, 160} {
		screen.SetSize(width, 1)
		indicator.SetRect(0, 0, width, 1)
		indicator.Draw(screen)
		var line strings.Builder
		for x := range width {
			r, _, _, _ := screen.GetContent(x, 0)
			line.WriteRune(r)
		}
		require.Contains(t, line.String(), "[RO]")
		require.Contains(t, line.String(), "CONNECTED")
		require.Contains(t, line.String(), "PRODUCTION")
		require.Contains(t, line.String(), "ctx:")
		require.Contains(t, line.String(), "ns:")
		require.Contains(t, line.String(), "unavailable")
		require.Contains(t, line.String(), "…")
		require.NotContains(t, line.String(), "0%")
	}
	reveal := indicator.FullDestination()
	require.Contains(t, reveal, fullContext)
	require.Contains(t, reveal, "Mode: read-only")
	require.Contains(t, reveal, client.NodeMetricsSource)
	require.Contains(t, reveal, "HTTP 503")
}

func TestCompactMemoryTrendUsesPreviousMemory(t *testing.T) {
	cfg := mock.NewMockConfig(t)
	indicator := NewStatusIndicator(NewApp(cfg, ""), config.NewStyles())
	now := time.Now()
	indicator.previous = &model.ClusterMeta{Context: "short", Connected: true, Metrics: client.NewMetricSample(client.ClusterMetrics{PercCPU: 80, PercMEM: 20}, now, client.NodeMetricsSource, now)}
	indicator.current = &model.ClusterMeta{Context: "short", Connected: true, Metrics: client.NewMetricSample(client.ClusterMetrics{PercCPU: 70, PercMEM: 30}, now, client.NodeMetricsSource, now)}
	text := indicator.identityText(100)
	require.Contains(t, text, "CPU 70% ↓")
	require.Contains(t, text, "MEM 30% ↑")
	require.NotContains(t, text, "[red")
	require.NotContains(t, text, "[green")
}
