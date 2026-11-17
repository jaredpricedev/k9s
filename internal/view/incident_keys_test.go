// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The incident views were built apart. These tests pin what joins them: one
// key means one thing in all of them, and a key that needs a source says so
// instead of doing nothing.

func TestIncidentViewsShareTheGraphGrafanaAndTimelineKeys(t *testing.T) {
	alertsList := alertsTestView(t, true)
	alertsList.acceptSnapshot(alertsTestSnapshot())
	problemsList := newProblemsView(alertsList.app)
	require.NoError(t, problemsList.Init(t.Context()))
	timelineList := timelineViewFixture(t)
	graph := graphViewFixture(t)
	comparison := impactViewFixture(t)

	type bound interface{ Actions() *ui.KeyActions }
	label := func(v bound, key tcell.Key) string {
		action, ok := v.Actions().Get(key)
		if !ok {
			return ""
		}
		return action.Description
	}
	for name, v := range map[string]bound{"alerts": alertsList, "problems": problemsList, "timeline": timelineList} {
		assert.Contains(t, label(v, ui.KeyM), "Graph", "%s: m is the graph key", name)
		assert.Equal(t, grafanaKeyLabel, label(v, grafanaKey), "%s: Shift-D is the Grafana hand-off", name)
	}
	for name, v := range map[string]bound{"graph": graph, "impact": comparison} {
		assert.Equal(t, grafanaKeyLabel, label(v, grafanaKey), "%s: Shift-D is the Grafana hand-off", name)
	}
	for name, v := range map[string]bound{"alerts": alertsList, "problems": problemsList, "impact": comparison} {
		assert.Contains(t, label(v, ui.KeyT), "imeline", "%s: t opens the timeline", name)
	}
	// s is "sources and limits" where it is bound, so the alerts toggle and
	// the timeline's reference mark sit on keys of their own.
	assert.Equal(t, "Sources and limits", label(timelineList, ui.KeyS))
	assert.Equal(t, "Queries and gaps", label(graph, ui.KeyS))
	assert.Empty(t, label(alertsList, ui.KeyS))
	assert.Equal(t, "Show / hide silenced and inhibited", label(alertsList, ui.KeyZ))
	assert.Equal(t, "Mark reference moment", label(timelineList, ui.KeyX))
	// l is logs in :problems; the graph cursor moves with the arrow keys only.
	assert.Empty(t, label(graph, ui.KeyL))
	assert.Empty(t, label(graph, ui.KeyH))
}

func TestIncidentGraphKeyIsRefusedWithoutAPrometheusSource(t *testing.T) {
	declared := alertsTestView(t, true)
	declared.acceptSnapshot(alertsTestSnapshot())
	assert.Empty(t, declared.availability(ui.KeyM), "a Deployment row can be graphed when Prometheus is declared")
	assert.Empty(t, declared.availability(grafanaKey), "the hand-off always answers; its dialog states what is missing")
	declared.table.Select(5, 0) // Watchdog names no object
	assert.Contains(t, declared.availability(ui.KeyM), "labels name no Kubernetes object")

	undeclared := alertsTestView(t, false)
	undeclared.acceptSnapshot(alertsTestSnapshot())
	assert.Contains(t, undeclared.availability(ui.KeyM), "No Prometheus source is declared")
}

func TestAlertHandoffStartsShortlyBeforeTheAlertBegan(t *testing.T) {
	v := alertsTestView(t, true)
	v.acceptSnapshot(alertsTestSnapshot())
	line := v.selectedLine()
	require.NotNil(t, line)
	require.NotNil(t, line.item)
	line.item.Alert.StartsAt = time.Now().Add(-20 * time.Minute)
	window, ok := v.GrafanaWindow()
	require.True(t, ok)
	assert.True(t, window.From.Before(line.item.Alert.StartsAt))
	assert.Contains(t, window.Reason, "the alert began")
	v.table.Select(3, 0) // folded row: several alerts, no single start
	_, ok = v.GrafanaWindow()
	assert.False(t, ok)
}

func TestGraphScrollsToTheChartItWasOpenedFor(t *testing.T) {
	v := graphViewFixture(t)
	v.focus = "cpu"
	plan := buildGraphPlan(&v.target, nil, time.Minute)
	v.accept(&graphSnapshot{Target: v.target, Window: v.window, Step: time.Minute, Plan: plan})
	assert.Equal(t, 1, v.first, "the CPU chart is the second of memory, CPU and network")
	assert.Empty(t, v.focus, "a later re-read keeps the user's own scroll position")
	window, ok := v.GrafanaWindow()
	require.True(t, ok)
	assert.Contains(t, window.Reason, "the graph's window")
}

func TestPaletteListsGraphAndTimelineOnceWithTheirKey(t *testing.T) {
	v := alertsTestView(t, true)
	v.acceptSnapshot(alertsTestSnapshot())
	count := map[string]int{}
	labels := map[string]string{}
	for _, item := range actionCatalog(v, v.app) {
		for _, word := range []string{"Graph", "Timeline", "Impact", "Alerts", "Open in Grafana", "Grafana dashboards"} {
			if len(item.Label) >= len(word) && item.Label[:len(word)] == word {
				count[word]++
				labels[word] = item.Label
			}
		}
	}
	for _, word := range []string{"Graph", "Timeline", "Impact", "Open in Grafana", "Grafana dashboards"} {
		assert.Equal(t, 1, count[word], "%s is listed once, not once per key and once per command", word)
	}
	assert.Equal(t, "Graph: usage vs limits over time (m)", labels["Graph"])
	assert.Equal(t, "Timeline: what changed, in order (t)", labels["Timeline"])
}
