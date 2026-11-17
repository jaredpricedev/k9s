// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
)

// dashFake answers range queries by the first rule whose text is in the
// query: one series per label set, a status, or the source's own error.
type dashFake struct {
	mu    sync.Mutex
	asked []string
	rules []dashRule
}

type dashRule struct {
	contains string
	series   []map[string]string
	value    func(labels map[string]string, at time.Time) (float64, bool)
	status   *observability.Status
	rejected string
}

func (*dashFake) Display() string { return "service monitoring/prometheus:9090" }

func (*dashFake) Query(context.Context, observability.Expr, time.Time) observability.PromResult {
	return observability.PromResult{}
}

func (f *dashFake) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	f.mu.Lock()
	f.asked = append(f.asked, e.String())
	f.mu.Unlock()
	res := observability.PromResult{Status: observability.Status{Kind: observability.KindPrometheus, State: observability.StateComplete, ObservedAt: graphNow},
		Query: e.String(), Start: start, End: end, Step: observability.StepFor(end.Sub(start))}
	for _, rule := range f.rules {
		if !strings.Contains(res.Query, rule.contains) {
			continue
		}
		switch {
		case rule.status != nil:
			res.Status = *rule.status
			return res
		case rule.rejected != "":
			res.Status = observability.Status{Kind: observability.KindPrometheus, State: observability.StateUnavailable, Reason: "HTTP 400 request rejected", ObservedAt: graphNow}
			res.Error = rule.rejected
			return res
		}
		sets := rule.series
		if sets == nil {
			sets = []map[string]string{{}}
		}
		for _, labels := range sets {
			series := observability.PromSeries{Labels: labels}
			for at := start; !at.After(end); at = at.Add(res.Step) {
				if v, ok := rule.value(labels, at); ok {
					series.Samples = append(series.Samples, observability.Sample{Timestamp: at, Value: v})
				}
			}
			if len(series.Samples) > 0 {
				res.Series = append(res.Series, series)
			}
		}
		break
	}
	return res
}

const dashOutlier = "gitlab-webservice-default-6f9c7d5b84-hx4mt"

var dashPods = []map[string]string{
	{"pod": "gitlab-webservice-default-6f9c7d5b84-2bq7k"}, {"pod": "gitlab-webservice-default-6f9c7d5b84-8wzpd"}, {"pod": dashOutlier},
}

// dashStalled says whether a time lies in one of the three stalls of the
// GitLab story: 52 to 49, 33 to 29 and 7 to 5 minutes before the window end.
func dashStalled(at time.Time) bool {
	before := graphNow.Truncate(30 * time.Second).Sub(at)
	for _, stall := range [][2]time.Duration{{49 * time.Minute, 52 * time.Minute}, {29 * time.Minute, 33 * time.Minute}, {5 * time.Minute, 7 * time.Minute}} {
		if before >= stall[0] && before <= stall[1] {
			return true
		}
	}
	return false
}

// dashStory answers the GitLab webservice dashboard and the named queries:
// one Pod stalls three times, the others stay level.
func dashStory() *dashFake {
	level := func(normal, stalled float64) func(map[string]string, time.Time) (float64, bool) {
		return func(_ map[string]string, at time.Time) (float64, bool) {
			if dashStalled(at) {
				return stalled, true
			}
			return normal, true
		}
	}
	perPod := func(normal, outlier, stalled float64) func(map[string]string, time.Time) (float64, bool) {
		return func(labels map[string]string, at time.Time) (float64, bool) {
			switch {
			case labels["pod"] != dashOutlier:
				return normal, true
			case dashStalled(at):
				return stalled, true
			}
			return outlier, true
		}
	}
	return &dashFake{rules: []dashRule{
		{contains: "max(histogram_quantile(0.99", value: level(0.346, 47.9)},
		{contains: "histogram_quantile(0.99, sum by (pod, le)", series: dashPods, value: perPod(0.31, 0.35, 47.9)},
		{contains: "sum(rate(http_requests_total", value: level(36.6, 25.1)},
		{contains: "sum by (pod) (rate(http_requests_total", series: dashPods, value: perPod(12.2, 12.2, 1.5)},
		{contains: "puma_active_connections", value: level(0.375, 0.6)},
		{contains: "max(sum by (pod) (rate(container_cpu_cfs_throttled_periods_total", value: level(0.04, 0.86)},
		{contains: "puma_queued_connections", series: dashPods, value: perPod(0, 0, 40)},
		{contains: "puma_pool_capacity", series: dashPods, value: perPod(5, 4, 0)},
		{contains: "container_cpu_usage_seconds_total", series: dashPods, value: perPod(0.35, 0.48, 1.47)},
		{contains: "kube_pod_container_resource_limits", value: level(1.5, 1.5)},
		{contains: "container_cpu_cfs_throttled_periods_total", series: dashPods, value: perPod(0.012, 0.04, 0.86)},
		{contains: `status="499"`, series: []map[string]string{{"ingress": "gitlab-webservice-default"}}, value: level(0.0002, 0.063)},
		{contains: "coredns_dns_request_duration_seconds_bucket", value: level(0.006, 0.006)},
		{contains: "hubble_drop_total", series: []map[string]string{{"reason": "POLICY_DENIED"}, {"reason": "STALE_OR_UNROUTABLE_IP"}},
			value: func(labels map[string]string, at time.Time) (float64, bool) {
				if labels["reason"] == "POLICY_DENIED" {
					return 0.8, at.After(graphNow.Add(-41*time.Minute)) && at.Before(graphNow.Add(-40*time.Minute))
				}
				return 0.006, true
			}},
	}}
}

func dashDefinition(t *testing.T, name string) *dashboard.Dashboard {
	t.Helper()
	body, err := os.ReadFile("../dashboard/testdata/" + name)
	require.NoError(t, err)
	d, err := dashboard.Parse(body)
	require.NoError(t, err)
	return d
}

var (
	dashWebservice = &dashSpec{UID: "gitlab-webservice", Hint: "GitLab / Webservice overview", Mapping: dashboard.ConventionalMapping}
	dashDeployment = dashboard.Selection{Namespace: "gitlab", Workload: "gitlab-webservice-default"}
)

const dashLabel = "Deployment gitlab/gitlab-webservice-default"

func dashStorySnapshot(t *testing.T) *dashSnapshot {
	t.Helper()
	return loadDash(t.Context(), dashStory(), dashDefinition(t, "grafana12-gitlab-webservice.json"), dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
}

func dashPageOf(width, height int) *dashPage {
	return &dashPage{Width: width, Height: height, Selected: -1, Colors: dashPalette(config.DefaultSemanticPalette())}
}

func dashText(t *testing.T, s *dashSnapshot, pg *dashPage) string {
	t.Helper()
	lines := renderDash(s, pg)
	require.LessOrEqual(t, len(lines), pg.Height, "the page is taller than the view")
	return graphPlainLines(t, lines, pg.Width)
}

func TestDashboardPageDrawsTheGitLabStoryAtEverySize(t *testing.T) {
	s := dashStorySnapshot(t)
	require.Len(t, s.Results, 11, "one query per Prometheus target of a drawable panel")
	require.Equal(t, time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC), s.End, "the window end is aligned to the step")

	page := dashPageOf(132, 32)
	wide := dashText(t, s, page)
	for _, want := range []string{
		"Dashboard · GitLab / Webservice overview · last 1h to 05:00Z · step 30s",
		"read 05:00:07Z · 9 of 11 panels drawn, 2 not drawn (s lists why) · 11 of 11 queries answered · showing 1-8 of 11 (↑/↓)",
		"for Deployment gitlab/gitlab-webservice-default · namespace=gitlab (selection) · pod=~.* (All)",
		"── Requests ─────", "── Puma and CPU ─────",
		// Four values side by side: the value, the dashboard's own threshold
		// word, a thin trend and the range of the window.
		"> Requests per second              Slowest pod p99                  Puma threads in use              CPU throttled, worst pod",
		"  36.6/s                           346ms  green                     37.5%  green                     4%  green",
		"min 25.1/s  max 36.6/s           min 346ms  max 47.9s (red)",
		"━━━━━━━━───────────── of 100%    ━──────────────────── of 100%",
		// One line per Pod, named by the legendFormat, shortened to what
		// tells them apart; the line that stood far above its end says so.
		"Request duration p99 by pod  a …2bq7k 310ms", "b …8wzpd 310ms  c …hx4mt 350ms peak 47.9s",
		"Requests by pod  a …2bq7k 12.2/s  b …8wzpd 12.2/s",
		"b …8wzpd queued 0  c …hx4mt queued 0 peak 40", "d …2bq7k free threads 5  e …8wzpd free threads 5  +1 more",
		"CPU usage against limit  a …2bq7k 350m  b …8wzpd 350m", "c …hx4mt 480m peak 1.47  d limit 1.5",
		"50s ┤", "25s ┤", "15/s ┤", "1.5 ┤", "04:10     04:20     04:30",
		`Panel 1 of 11 · stat · A sum(rate(http_requests_total{namespace="gitlab", pod=~".*"}[2m[]))`,
		"←/→ ↑/↓ panel · Enter zoom · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Shift-D Grafana · Esc back",
	} {
		require.Contains(t, wide, want)
	}
	require.Equal(t, 1, page.Selected, "the first panel under the row title is selected")
	// Both charts of a dashboard row start their plots in the same columns.
	for _, line := range strings.Split(wide, "\n") {
		if strings.Contains(line, "50s ┤") {
			require.Equal(t, 2+len("50s "), strings.Index(line, "┤"))
			require.Contains(t, line, "      15/s ┤")
		}
	}

	// 80x24 leaves 20 rows: the four values in two lines and the first
	// chart in the rows that are left, with its legend whole.
	normal := dashText(t, s, dashPageOf(80, 20))
	for _, want := range []string{
		"read 05:00:07Z · 9/11 panels drawn · 11/11 answered · 1-5/11 ↑↓",
		"> Requests per second                      Slowest pod p99",
		"  Puma threads in use                      CPU throttled, worst pod",
		"min 346ms  max 47.9s (red)",
		"Request duration p99 by pod  a …2bq7k 310ms  b …8wzpd 310ms", "c …hx4mt 350ms peak 47.9s",
		"←/→ ↑/↓ panel · Enter zoom · 1-4 window · r re-read · s queries · Esc",
	} {
		require.Contains(t, normal, want)
	}
	require.NotContains(t, normal, "Requests by pod")
	require.NotContains(t, normal, "for Deployment", "a short page gives the line to the panels; s lists the variables")

	// 60 columns: nothing is cut that changes the meaning.
	narrow := dashText(t, s, dashPageOf(60, 20))
	for _, want := range []string{
		"GitLab / Webservice overview · 1h to 05:00Z · step 30s", "read 05:00:07Z · 9/11 drawn · 11/11 ok · 1-5/11 ↑↓",
		"> Requests per second            Slowest pod p99", "346ms  green", "min 346ms  max 47.9s (red)",
		"b …8wzpd 310ms  c …hx4mt 350ms peak 47.9s", "←/→ panel · Enter zoom · 1-4 window · r read · s queries",
	} {
		require.Contains(t, narrow, want)
	}

	// Scrolling to the end shows what was not drawn, each with its reason.
	last := dashPageOf(132, 32)
	last.Selected = len(s.Dash.Panels) - 1
	end := dashText(t, s, last)
	for _, want := range []string{
		"showing 7-11 of 11 (↑/↓)", "── Detail ─────",
		`  Pods and nodes · NOT DRAWN: panel type "table" is not drawn here`,
		`> Webservice log lines with timeout · NOT DRAWN: panel type "logs" is not drawn here`,
		"CPU throttled periods  a …2bq7k 1.2%  b …8wzpd 1.2%", "c …hx4mt 4% peak 86%", "100% ┤",
		`Panel 11 of 11 · logs · not drawn: panel type "logs" is not drawn here`,
	} {
		require.Contains(t, end, want)
	}
	require.NotContains(t, end, "Requests per second")

	// A page shorter than one chart still ends with the readout and keys.
	tiny := dashPageOf(80, 8)
	tiny.Selected = 5
	short := dashText(t, s, tiny)
	require.Contains(t, short, "> Request duration p99 by pod")
	require.Contains(t, short, "Panel 5 of 11 · timeseries")
	require.Contains(t, short, "Enter zoom")
}

func TestDashboardZoomReadsExactSamplesAndShowsTheExpression(t *testing.T) {
	s := dashStorySnapshot(t)
	page := dashPageOf(132, 32)
	page.Zoom, page.Selected, page.Cursor = true, 5, int(s.Window/s.Step)
	text := dashText(t, s, page)
	for _, want := range []string{
		"Panel 5 of 11 · timeseries · Request duration p99 by pod · seconds",
		"a …2bq7k 310ms  b …8wzpd 310ms  c …hx4mt 350ms",
		"Cursor 05:00:00Z (window end) · a value is the sample at the cursor, not an interpolation",
		`A histogram_quantile(0.99, sum by (pod, le) (rate(http_request_duration_seconds_bucket{namespace="gitlab", pod=~".*"}[2m[])))`,
		"complete · 3 series · 363 samples · legend {{pod}}",
		"About: Rails request duration, 99th percentile, one line per pod.",
		"Variables: namespace=gitlab (selection) · pod=~.* (All)",
		"←/→ cursor · ↑/↓ panel · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Shift-D Grafana · Esc grid",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "peak", "at the cursor a legend holds samples only")
	// Thirty-one minutes before the end lies inside the second stall.
	page.Cursor -= 62
	text = dashText(t, s, page)
	require.Contains(t, text, "a …2bq7k 310ms  b …8wzpd 310ms  c …hx4mt 47.9s")
	require.Contains(t, text, "Cursor 04:29:00Z, 31m before window end")

	// A stat is zoomed as the series behind it, with its value in words.
	page.Selected = 2
	text = dashText(t, s, page)
	for _, want := range []string{
		"Panel 2 of 11 · stat · Slowest pod p99 · seconds",
		"last 346ms · threshold green · window min 346ms max 47.9s (red)",
		"p99 47.9s", "complete · 1 series · 121 samples · legend p99",
	} {
		require.Contains(t, text, want)
	}
	page.Selected = 4
	text = dashText(t, s, page)
	require.Contains(t, text, "Panel 4 of 11 · gauge · CPU throttled, worst pod · percent")
	require.Contains(t, text, "last 4% · threshold green · window min 4% max 86% (red)")

	// A panel that is not drawn is words, not an empty plot.
	page.Selected = len(s.Dash.Panels) - 1
	text = dashText(t, s, page)
	for _, want := range []string{
		"Panel 11 of 11 · logs · Webservice log lines with timeout",
		`NOT DRAWN: panel type "logs" is not drawn here.`,
		"Drawn here: timeseries, graph, stat, gauge, bargauge, when their query is a Prometheus expression.",
		`A {namespace="$namespace", pod=~"$pod", container="webservice"} |= "timeout"`, "not sent",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "Cursor")

	// 60 columns keep the cursor, the expression and the keys.
	small := dashPageOf(60, 20)
	small.Zoom, small.Selected, small.Cursor = true, 5, int(s.Window/s.Step)-62
	text = dashText(t, s, small)
	for _, want := range []string{"Request duration p99 by pod", "c …hx4mt 47.9s", "Cursor 04:29:00Z, -31m", "A histogram_quantile(0.99, sum by (pod, le)",
		"←/→ cursor · ↑/↓ panel · 1-4 window · r read · Esc grid"} {
		require.Contains(t, text, want)
	}
}
