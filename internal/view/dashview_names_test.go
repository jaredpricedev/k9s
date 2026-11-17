// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/dashboard"
	"github.com/stretchr/testify/require"
)

func TestDashboardLimitsOfOneReadAreCountedAndNamed(t *testing.T) {
	var panels []string
	for i := range dashboard.MaxDrawn + 3 {
		panels = append(panels, fmt.Sprintf(`{"id": %d, "type": "stat", "title": "Panel %d", "gridPos": {"x": 0, "y": %d, "w": 24, "h": 4},
			"targets": [{"expr": "sum(puma_active_connections)"}, {"expr": "sum(puma_active_connections)"}]}`, i+1, i+1, i))
	}
	big, err := dashboard.Parse([]byte(`{"title": "Big", "uid": "big", "panels": [` + strings.Join(panels, ",") + `]}`))
	require.NoError(t, err)
	fake := dashStory()
	s := loadDash(t.Context(), fake, big, &dashSpec{UID: "big", Mapping: dashboard.ConventionalMapping}, "no selection", dashboard.Selection{}, time.Hour, graphNow)
	require.Len(t, fake.asked, dashboard.MaxQueries, "one read sends no more than its limit")
	page := dashPageOf(132, 32)
	page.Selected = len(big.Panels) - 1
	text := dashText(t, s, page)
	// Twenty panels took the forty queries; the seven after them are named.
	require.Contains(t, text, "read 05:00:07Z · 20/27 panels drawn · 40/40 answered · 7 over limit · 16-27/27 ↑↓")
	require.Contains(t, text, "for no selection · no expression on this page uses a variable")
	require.Contains(t, text, "> Panel 27 · NOT DRAWN: not read: one read sends at most 40 queries")
	require.Contains(t, dashReport(s), "  Panel 21: not read: one read sends at most 40 queries")
}

func TestDashboardLegendKeepsALoneLongNameReadable(t *testing.T) {
	one := func(map[string]string, time.Time) (float64, bool) { return 0.48, true }
	fake := &dashFake{rules: []dashRule{
		{contains: "kube_pod_container_resource_limits", value: func(map[string]string, time.Time) (float64, bool) { return 1.5, true }},
		{contains: "container_cpu_usage_seconds_total", series: []map[string]string{{"pod": dashOutlier}}, value: one},
	}}
	s := loadDash(t.Context(), fake, dashDefinition(t, "grafana12-gitlab-webservice.json"), dashWebservice, "Pod gitlab/"+dashOutlier,
		dashboard.Selection{Namespace: "gitlab", Pod: dashOutlier}, time.Hour, graphNow)
	// The Pod's name fills both variables of the panel's expression.
	require.Contains(t, strings.Join(fake.asked, "\n"),
		`sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="gitlab", pod=~"gitlab-webservice-default-6f9c7d5b84-hx4mt", container="webservice"}[2m]))`)
	page := dashPageOf(132, 32)
	page.Selected = 9
	text := dashText(t, s, page)
	// One Pod line next to a "limit" line shares no start with it: it keeps
	// both ends instead of crowding the other entry out of the legend.
	require.Contains(t, text, "> CPU usage against limit  a gitlab-webserv…c7d5b84-hx4mt 480m  b limit 1.5")
	require.Contains(t, text, "namespace=gitlab (selection) · pod=gitlab-webservice-default-6f9c7d5b84-hx4mt (selection)")
	require.NotContains(t, text, "for Pod", "the variables are kept when the line is too short for the selection too")

	// The window's maximum is in another step than the value: the stat says
	// so in words.
	stalled := dashStorySnapshot(t)
	text = dashText(t, stalled, dashPageOf(132, 32))
	require.Contains(t, text, "min 346ms  max 47.9s (red)")
	require.Contains(t, strings.Join(renderDash(stalled, dashPageOf(132, 32)), "\n"), "[#ef8278::]max 47.9s (red)[-::]", "and repeats it in the failure color")
}
