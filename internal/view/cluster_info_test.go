// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

func TestClusterInfoCompactMetricsRenderCompleteStatesAndMemoryTrend(t *testing.T) {
	now := time.Now()
	previous := client.NewMetricSample(client.ClusterMetrics{PercCPU: 80, PercMEM: 20}, now, client.NodeMetricsSource, now)
	current := client.NewMetricSample(client.ClusterMetrics{PercCPU: 70, PercMEM: 30}, now, client.NodeMetricsSource, now)
	cases := []struct {
		name     string
		sample   client.MetricSample
		cpu, mem string
	}{
		{"unavailable", client.MetricSample{State: client.MetricsUnavailable}, "unavailable", "unavailable"},
		{"denied", client.MetricSample{State: client.MetricsDenied}, "denied", "denied"},
		{"not configured", client.MetricSample{State: client.MetricsNotConfigured}, "not configured", "not configured"},
		{"stale", client.MetricFailure(current, client.MetricsDenied, client.NodeMetricsSource, "forbidden"), "70% stale", "30% stale"},
		{"fresh", current, "70% ↓", "30% ↑"},
	}
	for _, width := range []int{80, 100, 120} {
		for i := range cases {
			tc := &cases[i]
			t.Run(fmt.Sprintf("%d/%s", width, tc.name), func(t *testing.T) {
				lines := renderClusterInfoMetrics(t, width, previous, tc.sample, tc.cpu, tc.mem)
				require.Contains(t, lines[5], tc.cpu, "CPU state must fit the full header")
				require.Contains(t, lines[6], tc.mem, "MEM state must fit the full header")
				if tc.name != "fresh" && tc.name != "stale" {
					require.NotContains(t, lines[5]+lines[6], "0%", "missing samples must never render a measured zero")
				}
			})
		}
	}
}

//nolint:gocritic // Exercise the immutable observations used by the production callback.
func renderClusterInfoMetrics(t *testing.T, width int, previous, current client.MetricSample, cpu, mem string) []string {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	app.showHeader, app.showLogo = true, true
	info := app.clusterInfo()
	info.Init()
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(width, 24)
	frames := make(chan []string, 8)
	app.SetScreen(screen).SetRoot(app.buildHeader(), true).SetAfterDrawFunc(func(tcell.Screen) {
		lines := make([]string, 7)
		for y := range lines {
			var line strings.Builder
			for x := range width {
				ch, _, _, _ := screen.GetContent(x, y)
				line.WriteRune(ch)
			}
			lines[y] = line.String()
		}
		select {
		case frames <- lines:
		default:
		}
	})
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	t.Cleanup(func() {
		app.Application.Stop()
		select {
		case err := <-finished:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("cluster header renderer did not stop")
		}
		app.Styles.RemoveListener(info)
	})
	before := &model.ClusterMeta{Metrics: previous}
	after := &model.ClusterMeta{Context: app.Config.ActiveContextName(), Cluster: "demo-cluster", User: "demo-user",
		K9sVer: "v0.1.0-dev", K8sVer: "v1.34.0", Metrics: current}
	info.ClusterInfoChanged(before, after)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var last []string
	for {
		select {
		case last = <-frames:
			if strings.Contains(last[5], cpu) && strings.Contains(last[6], mem) {
				return last
			}
		case <-timer.C:
			t.Fatalf("metric states clipped at %d columns: %v", width, last)
		}
	}
}
