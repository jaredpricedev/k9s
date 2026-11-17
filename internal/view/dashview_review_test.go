// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/observability"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// A gauge has no line for the window's minimum and maximum. "4% green"
// alone would hide the stall the tile next to it shows as "max ... (red)".
func TestDashboardGaugeSaysWhenTheWindowHeldAnotherThresholdStep(t *testing.T) {
	s := dashStorySnapshot(t)
	text := dashText(t, s, dashPageOf(132, 36))
	require.Contains(t, text, "4%  green · max 86% red")
	// The gauge that stayed in its step says nothing more.
	require.Contains(t, text, "37.5%  green   ")

	require.Contains(t, dashText(t, s, dashPageOf(60, 24)), "4%  green · max 86% red")

	// On the narrowest tile the spacing gives way; the two words and the
	// maximum stay whole.
	narrow := dashText(t, s, dashPageOf(50, 24))
	require.Contains(t, narrow, "4% green · max 86% red")
	require.NotContains(t, narrow, "max 86% r…")
}

func TestDashboardPlainPageNeverCutsItsHeadlineOrHidesItsKeys(t *testing.T) {
	d, err := dashboard.Parse([]byte(`{"dashboard": {"uid": "long", "title": "Kubernetes / Compute Resources / Workload with a long name",
		"panels": [
			{"id": 1, "type": "timeseries", "title": "A", "gridPos": {"x": 0, "y": 0, "w": 12, "h": 8}, "datasource": {"type": "prometheus", "uid": "p"},
			 "targets": [{"refId": "A", "expr": "sum by (pod) (puma_queued_connections{namespace=\"$namespace\"})", "legendFormat": "{{pod}}"}]},
			{"id": 2, "type": "timeseries", "title": "B", "gridPos": {"x": 0, "y": 8, "w": 12, "h": 8}, "datasource": {"type": "prometheus", "uid": "p"},
			 "targets": [{"refId": "A", "expr": "sum by (pod) (puma_pool_capacity{namespace=\"$namespace\"})", "legendFormat": "{{pod}}"}]}
		],
		"templating": {"list": [{"name": "namespace", "type": "query", "current": {"value": "x"}}]}}}`))
	require.NoError(t, err)
	spec := &dashSpec{UID: "long", Hint: "long", Mapping: dashboard.ConventionalMapping}
	s := loadDash(t.Context(), dashStory(), d, spec, dashLabel, dashDeployment, time.Hour, graphNow)

	for _, ascii := range []bool{false, true} {
		for _, zoom := range []bool{false, true} {
			page := dashPageOf(60, 24)
			page.ASCII, page.Zoom = ascii, zoom
			lines := strings.Split(dashText(t, s, page), "\n")
			// The window end and the step close the headline whole; only the
			// title gives way.
			require.True(t, strings.HasSuffix(lines[0], " to 05:00Z · step 30s"), lines[0])
			require.LessOrEqual(t, runewidth.StringWidth(lines[0]), 60)
			footer := lines[len(lines)-1]
			if zoom {
				require.Contains(t, footer, "cursor")
				require.Contains(t, footer, "Esc grid", "the way back to the grid is named: %q", footer)
			} else {
				require.Contains(t, footer, "Enter zoom", "%q", footer)
				require.Contains(t, footer, "s queries", "%q", footer)
			}
			if ascii {
				require.Contains(t, lines[0], "...")
				for _, line := range lines {
					for _, r := range line {
						require.True(t, r < 128 || r == '·', "%q in %q is neither ASCII nor the shared separator", r, line)
					}
				}
			}
		}
	}
}

// Explore is the way on when the terminal cannot draw: a page whose every
// read was denied has no selected panel, and still names what it sent.
func TestQueryPageOffersExploreWhenEveryReadFailed(t *testing.T) {
	t.Setenv(clipboardModeEnv, "")
	fake := newFakeDelivery("linux", map[string]string{})
	useDelivery(t, fake)
	denied := observability.Status{Kind: observability.KindPrometheus, State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: graphNow}
	source := &dashFake{rules: []dashRule{{contains: "", status: &denied}}}
	for _, spec := range []dashSpec{querySpec(nil, "puma-queue"), {Board: true}} {
		v := dashViewFixture(t, spec)
		v.active = true
		d := dashboard.QueryBoard(dashboard.NamedQueries(nil))
		if spec.Query != nil {
			d = dashboard.QueryPage(spec.Query)
		}
		v.accept(loadDash(t.Context(), source, d, &v.spec, v.target.Label, v.selection, time.Hour, graphNow))
		require.Contains(t, v.text.GetText(true), "Prometheus DENIED")
		require.Equal(t, -1, v.selected, "a page that drew nothing selects no panel")
		v.openInGrafana()
		h := v.app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
		text := handoffText(h, 100)
		require.NotContains(t, text, "No expression was sent")
		require.Regexp(t, `Link    Explore metrics · \(?sum by \(pod\)`, text)
		h.close()
	}
	require.Len(t, fake.copied, 2)
	require.Contains(t, fake.copied[0], "https://grafana.example.test/explore?")
}

func TestDashboardFailuresAreCountedByStateInTheSameWordsEveryTime(t *testing.T) {
	at := time.Date(2026, 11, 17, 5, 0, 9, 0, time.UTC)
	denied := observability.Status{Kind: observability.KindPrometheus, State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: at}
	timeout := observability.Status{Kind: observability.KindPrometheus, State: observability.StateTimeout, Reason: "no answer within the deadline", ObservedAt: at}
	definition := dashDefinition(t, "grafana12-gitlab-webservice.json")

	// Reads that failed in different ways are not summed up under the name
	// of whichever one a map happened to yield last.
	mixed := &dashFake{rules: []dashRule{{contains: "puma", status: &timeout}, {contains: "", status: &denied}}}
	s := loadDash(t.Context(), mixed, definition, dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	want := "Prometheus answered none of the 11 queries: 8 denied, 3 timeout."
	for range 20 {
		require.Contains(t, dashText(t, s, dashPageOf(100, 20)), want)
	}
	require.NotContains(t, dashText(t, s, dashPageOf(100, 20)), "┤")

	all := loadDash(t.Context(), &dashFake{rules: []dashRule{{contains: "", status: &timeout}}}, definition, dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	require.Contains(t, dashText(t, all, dashPageOf(132, 20)), "Prometheus TIMEOUT: all 11 queries were not answered in time (no answer within the deadline) at 05:00:09Z.")

	// Some answered: the status line counts each way of failing, and a
	// chart drawn from one of its two queries names the one that is missing
	// before the line it does have.
	some := &dashFake{rules: append([]dashRule{{contains: "puma", status: &timeout}, {contains: "container_cpu", status: &denied}}, dashStory().rules...)}
	s = loadDash(t.Context(), some, definition, dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	partly := dashText(t, s, dashPageOf(132, 36))
	require.Contains(t, partly, "5/11 answered, 3 denied, 3 timeout")
	require.Contains(t, partly, "CPU usage against limit  query A denied  limit 1.5")

	// A query the page's deadline left unsent has no state. It is named,
	// with the deadline, and never drawn.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unsent := loadDash(ctx, dashStory(), definition, dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	text := dashText(t, unsent, dashPageOf(132, 20))
	require.Contains(t, text, "Prometheus NOT READ: none of the 11 queries could be sent before the page's 45s deadline ended.")
	require.Contains(t, dashReport(unsent), "NOT DRAWN: not read: the page's 45s deadline ended first; r reads again")
	require.NotContains(t, text, "┤")
}

func TestDashboardVariableLineFindsEveryUseOfAVariable(t *testing.T) {
	d, err := dashboard.Parse([]byte(`{"dashboard": {"uid": "u", "title": "T", "panels": [
		{"id": 1, "type": "timeseries", "title": "A", "datasource": {"type": "prometheus", "uid": "p"},
		 "targets": [{"refId": "A", "expr": "up{pod_name=\"$pod_name\", namespace=\"$namespace\", pod=~\"$pod\"}"}]}],
		"templating": {"list": [
			{"name": "namespace", "type": "query", "current": {"value": "x"}},
			{"name": "pod_name", "type": "constant", "query": "web"},
			{"name": "pod", "type": "query", "includeAll": true, "allValue": ".*", "current": {"value": "$__all"}},
			{"name": "po", "type": "constant", "query": "unused"}]}}}`))
	require.NoError(t, err)
	s := loadDash(t.Context(), dashStory(), d, &dashSpec{UID: "u", Mapping: dashboard.ConventionalMapping}, dashLabel, dashDeployment, time.Hour, graphNow)
	// $pod stands behind $pod_name in the text and is still a use; $po is not.
	require.Equal(t, []string{"namespace=gitlab (selection)", "pod_name=web (constant)", "pod=~.* (All)"}, s.variableParts())
}

// Leaving the page cancels a read. A page that comes back without a read of
// its window must ask again: it would otherwise say "Reading" for ever.
func TestDashboardViewAsksAgainWhenItComesBackWithoutItsWindow(t *testing.T) {
	v := dashViewFixture(t, *dashWebservice)
	v.active = true
	v.accept(loadDash(t.Context(), dashStory(), dashDefinition(t, "grafana12-gitlab-webservice.json"), &v.spec, v.target.Label, v.selection, time.Hour, graphNow))
	started := make(chan time.Duration, 4)
	v.load = func(_ context.Context, _ *dashSpec, window time.Duration, _ *dashboard.Dashboard) *dashSnapshot {
		started <- window
		return nil
	}
	v.setWindow(6 * time.Hour)
	require.Equal(t, 6*time.Hour, <-started)
	v.Stop()
	require.Empty(t, v.busy)

	v.Start()
	require.Equal(t, 6*time.Hour, <-started, "the 6h window was never read")
	require.Equal(t, "Reading the panels' last 6h once…", v.busy)
	require.Contains(t, v.text.GetText(true), "Reading the panels' last 6h once…")

	// A page that holds its window reads nothing on coming back.
	v.Stop()
	v.window = time.Hour
	v.Start()
	select {
	case window := <-started:
		t.Fatalf("read %s again without being asked", window)
	case <-time.After(50 * time.Millisecond):
	}
	require.Empty(t, v.busy)
	require.Contains(t, v.text.GetText(true), "last 1h to 05:00Z")
}

// A bar gauge draws one row per series; a tile holds three rows, and says
// how many series it leaves to the zoomed panel.
func TestDashboardBarGaugeDrawsARowPerSeries(t *testing.T) {
	d, err := dashboard.Parse([]byte(`{"dashboard": {"uid": "bars", "title": "Bars", "panels": [
		{"id": 1, "type": "bargauge", "title": "Queue by pod", "gridPos": {"x": 0, "y": 0, "w": 8, "h": 4}, "datasource": {"type": "prometheus", "uid": "p"},
		 "fieldConfig": {"defaults": {"unit": "short", "min": 0, "max": 50, "thresholds": {"mode": "absolute", "steps": [
			{"color": "green", "value": null}, {"color": "red", "value": 20}]}}},
		 "targets": [{"refId": "A", "expr": "sum by (pod) (puma_queued_connections{namespace=\"$namespace\"})", "legendFormat": "{{pod}}"}]},
		{"id": 2, "type": "bargauge", "title": "Drops by reason", "gridPos": {"x": 8, "y": 0, "w": 8, "h": 4}, "datasource": {"type": "prometheus", "uid": "p"},
		 "fieldConfig": {"defaults": {"unit": "reqps"}},
		 "targets": [{"refId": "A", "expr": "sum by (reason) (rate(many_reasons_total[5m]))", "legendFormat": "{{reason}}"}]}
	],
	"templating": {"list": [{"name": "namespace", "type": "query", "current": {"value": "x"}}]}}}`))
	require.NoError(t, err)
	reasons := []map[string]string{{"reason": "a"}, {"reason": "b"}, {"reason": "c"}, {"reason": "d"}, {"reason": "e"}}
	source := &dashFake{rules: []dashRule{
		{contains: "puma_queued_connections", series: dashPods, value: func(labels map[string]string, _ time.Time) (float64, bool) {
			if labels["pod"] == dashOutlier {
				return 40, true
			}
			return 0, true
		}},
		{contains: "many_reasons_total", series: reasons, value: func(labels map[string]string, _ time.Time) (float64, bool) {
			return float64(labels["reason"][0]-'a') + 1, true
		}},
	}}
	s := loadDash(t.Context(), source, d, &dashSpec{UID: "bars", Mapping: dashboard.ConventionalMapping}, dashLabel, dashDeployment, time.Hour, graphNow)
	for _, width := range []int{132, 80, 60} {
		text := dashText(t, s, dashPageOf(width, 24))
		// Name, bar against the declared 0 to 50, value.
		require.Regexp(t, `…2bq7k ─+ +0\b`, text, "width %d", width)
		require.Regexp(t, `…hx4mt ━+─+ +40\b`, text, "width %d", width)
		// Five series in three rows: two are shown and three are counted.
		require.Contains(t, text, "+3 more · Enter shows all", "width %d", width)
	}
	text := dashText(t, s, dashPageOf(132, 24))
	var bar string
	for _, line := range strings.Split(text, "\n") {
		if at := strings.Index(line, "…hx4mt "); at >= 0 {
			bar = strings.Fields(line[at:])[1]
		}
	}
	filled, total := strings.Count(bar, "━"), runewidth.StringWidth(bar)
	require.InDelta(t, 0.8, float64(filled)/float64(total), 0.06, "40 of 50 is four fifths of %q", bar)

	// The zoomed panel is the series behind the bars, every one of them.
	page := dashPageOf(132, 30)
	page.Zoom, page.Selected = true, 1
	zoomed := dashText(t, s, page)
	for _, want := range []string{"a a 1/s", "b b 2/s", "c c 3/s", "d d 4/s", "e e 5/s"} {
		require.Contains(t, zoomed, want)
	}
}
