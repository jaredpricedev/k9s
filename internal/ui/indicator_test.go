// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func TestIndicatorTransientMessagesPreserveDestination(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, noIcons := range []bool{false, true} {
			t.Run(ui.ModeLabel(readOnly)+" icons="+map[bool]string{true: "off", false: "on"}[noIcons], func(t *testing.T) {
				cfg := mock.NewMockConfig(t)
				cfg.K9s.ReadOnly, cfg.K9s.UI.NoIcons = readOnly, noIcons
				app := ui.NewApp(cfg, "")
				indicator := ui.NewStatusIndicator(app, config.NewStyles())
				indicator.SetRect(0, 0, 100, 1)
				indicator.RefreshIdentity()
				before := indicator.GetText(true)
				require.Contains(t, before, tview.Escape(ui.ModeLabel(readOnly)))
				require.Contains(t, before, "ctx:")
				require.Contains(t, before, "ns:")
				for _, message := range []struct {
					level model.FlashLevel
					send  func(string)
				}{{model.FlashInfo, indicator.Info}, {model.FlashWarn, indicator.Warn}, {model.FlashErr, indicator.Err}} {
					message.send("operation progress")
					got := <-app.Flash().Channel()
					require.Equal(t, message.level, got.Level)
					require.Equal(t, "operation progress", got.Text)
					require.Equal(t, before, indicator.GetText(true))
				}
				indicator.Reset()
				require.Equal(t, before, indicator.GetText(true))
				mode := "read-write"
				if readOnly {
					mode = "read-only"
				}
				require.Contains(t, indicator.FullDestination(), "Mode: "+mode)
				require.Equal(t, tview.Escape(ui.ModeLabel(readOnly)), ui.ROIndicator(readOnly, noIcons))
			})
		}
	}
}

func TestIndicatorReset(t *testing.T) {
	indicator := ui.NewStatusIndicator(ui.NewApp(mock.NewMockConfig(t), ""), config.NewStyles())
	indicator.SetPermanent("Blee")
	indicator.Info("duh")
	indicator.Reset()
	require.Equal(t, "Blee\n", indicator.GetText(false))
}

func TestMetricPercentStateAndIndependentMemoryTrend(t *testing.T) {
	now := time.Now()
	previous := client.NewMetricSample(client.ClusterMetrics{PercCPU: 80, PercMEM: 20}, now, client.NodeMetricsSource, now)
	current := client.NewMetricSample(client.ClusterMetrics{PercCPU: 70, PercMEM: 30}, now, client.NodeMetricsSource, now)
	require.Contains(t, ui.MetricPercent(previous, current, true), "30%")
	require.Contains(t, ui.MetricPercent(previous, current, true), "↑")
	require.Contains(t, ui.MetricPercent(previous, current, false), "↓")
	require.Equal(t, "30%", ui.MetricPercent(current, current, true))
	require.Equal(t, "30%", ui.MetricPercent(client.MetricSample{}, current, true))
	current.State = client.MetricsStale
	require.Equal(t, "30% (stale)", ui.MetricPercent(previous, current, true))
	require.NotContains(t, ui.MetricPercent(previous, current, true), "↑")
	for _, state := range []client.MetricState{client.MetricsUnavailable, client.MetricsDenied, client.MetricsNotConfigured} {
		text := ui.MetricPercent(previous, client.MetricSample{State: state}, true)
		require.Contains(t, text, "N/A")
		require.Contains(t, text, string(state))
		require.NotContains(t, text, "0%")
	}
	zero := client.NewMetricSample(client.ClusterMetrics{}, now, client.NodeMetricsSource, now)
	require.Equal(t, "0%", ui.MetricPercent(client.MetricSample{}, zero, true))
	previous.State = client.MetricsStale
	current.State = client.MetricsAvailable
	require.Equal(t, "30%", ui.MetricPercent(previous, current, true))
}

func TestIndicatorDrawKeepsModeAtNarrowWidths(t *testing.T) {
	cfg := mock.NewMockConfig(t)
	cfg.K9s.ReadOnly = true
	cfg.K9s.UI.NoIcons = true
	indicator := ui.NewStatusIndicator(ui.NewApp(cfg, ""), config.NewStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	for _, width := range []int{40, 80, 100, 160} {
		screen.SetSize(width, 1)
		indicator.SetRect(0, 0, width, 1)
		indicator.Draw(screen)
		var text strings.Builder
		for x := range width {
			ch, _, _, _ := screen.GetContent(x, 0)
			text.WriteRune(ch)
		}
		require.Contains(t, text.String(), "[RO]")
		require.Contains(t, text.String(), "ctx:")
		require.Contains(t, text.String(), "ns:")
	}
}
