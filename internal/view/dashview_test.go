// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"math"
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
	palette := config.DefaultSemanticPalette()
	// The cursor stands at the window's end, where the view puts it.
	return &dashPage{Width: width, Height: height, Selected: -1, Cursor: math.MaxInt32, Colors: dashPalette(palette), Calm: calmPalette(palette)}
}

// dashDetailOf is the page under the all-detail key: the grid of every
// panel, as the page was before the calm layout.
func dashDetailOf(width, height int) *dashPage {
	page := dashPageOf(width, height)
	page.Detail = true
	return page
}

func dashText(t *testing.T, s *dashSnapshot, pg *dashPage) string {
	t.Helper()
	lines := renderDash(s, pg)
	require.LessOrEqual(t, len(lines), pg.Height, "the page is taller than the view")
	return graphPlainLines(t, lines, pg.Width)
}

// dashHasLines requires every wanted line to be a whole line of the page,
// in the order given.
func dashHasLines(t *testing.T, text string, want ...string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	at := 0
	for _, line := range want {
		found := false
		for ; at < len(lines); at++ {
			if strings.TrimRight(lines[at], " ") == line {
				found = true
				at++
				break
			}
		}
		require.True(t, found, "the page has no line %q after the lines before it:\n%s", line, text)
	}
}

// The calm layout is the default page of a drawn dashboard: what was read,
// one panel at full size, every other panel as one row on the same time
// axis, and what was not drawn as one line.
func TestDashboardCalmPageDrawsTheGitLabStoryAtEverySize(t *testing.T) {
	s := dashStorySnapshot(t)
	require.Len(t, s.Results, 11, "one query per Prometheus target of a drawable panel")

	// 132x36 leaves the view 132x32.
	page := dashPageOf(132, 32)
	wide := dashText(t, s, page)
	dashHasLines(t, wide,
		"Dashboard  GitLab / Webservice overview · last 1h                                                    read 05:00 · 11 of 11 queries",
		"",
		// The answer first: how much of the dashboard is on the page.
		"  9 of 11 panels read. 2 cannot be drawn here.                                                           A reading, not a finding.",
		"",
		// One panel at full size, its legend on one line, the highest first.
		"  REQUEST DURATION P99 BY POD  c …hx4mt 350ms peak 47.9s  a …2bq7k 310ms  b …8wzpd 310ms",
		"                       50s ┤        ╭──╮                ╭───╮                      ╭c─╮",
		"                         0 ┤────────╯──╰───a────b────c──╯───╰────────────a────b────╯──╰─cba",
		"",
		// A dashboard row is a group. A stat is its value with the author's
		// threshold word, drawn against the author's rule: what stood over
		// it stands in the same columns as the stalls of the chart above.
		"  REQUESTS",
		"  Requests per second       ████████▆▆▆█████████████████▆▆▆▆████████████████████████▆██████  36.6/s",
		"  Slowest pod p99           ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  346ms green       max 47.9s red",
		"  Puma threads in use       ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  37.5% green",
		"  CPU throttled, worst pod  ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  4% green          max 86% red",
		// A chart of several series is its highest one, named.
		"  Requests by pod           ████████▂▂▂█████████████████▂▂▂▂████████████████████████▂██████  …hx4mt 12.2/s     +2 more",
		"",
		"  PUMA AND CPU",
		// The number column is as wide as the widest number of the window
		// ("queued 40"), so it is the same wherever the cursor stands.
		"  Puma queued connections   ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  …hx4mt queued 0   peak 40 · +5 more",
		// A level that never moves (the limit) does not stand for the panel.
		"  CPU usage against limit   ▃▃▃▃▃▃▃█████▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃██████▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃███▃▃▃▃▃  …hx4mt 480m       peak 1.47 · +3 more",
		"  CPU throttled periods     ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  …hx4mt 4%         peak 86% · +2 more",
		// One time axis for the chart and every row.
		"                            ┬─────────┬──────────┬─────────┬──────────┬─────────┬─────────┬",
		"  UTC                     04:00     04:10      04:20     04:30      04:40     04:50     05:00",
		"",
		// What cannot be drawn is one line, with the reason in a word.
		"  NOT DRAWN  Pods and nodes (table) · Webservice log lines with timeout (logs)",
		"",
		// Two lines of keys: the likely next steps in words, then the rest.
		"  next  n next chart (Requests by pod)    Enter zoom    a all panels    D open in Grafana",
		"",
		"  ←→ time · ↑↓ chart · s queries · 1 15m  2 1h  3 6h  4 24h · r re-read · b dots · Esc back",
	)
	// The page settles on its main chart, so Enter zooms it, and on the
	// panels the next-chart key steps through: every panel that was drawn.
	require.Equal(t, 5, page.Selected, "the first line chart is the main chart")
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 8, 9, 10}, page.Mains)
	for _, gone := range []string{"Panel 1 of 11", "for Deployment", "step 30s", "── Requests", "of 100%", "min 346ms"} {
		require.NotContains(t, wide, gone, "the calm page leaves it to a, s and Enter")
	}

	// 80x24 leaves 80x20: the first group keeps its rows, the second folds
	// to one line, and the legend counts what it has no room for.
	normal := dashText(t, s, dashPageOf(80, 20))
	dashHasLines(t, normal,
		"Dashboard  GitLab / Webservice overview · last 1h  read 05:00 · 11 of 11 queries",
		"  9 of 11 panels read. 2 cannot be drawn here.         A reading, not a finding.",
		"",
		"  REQUEST DURATION P99 BY POD  c …hx4mt 350ms peak 47.9s  +2 more",
		"                       50s ┤   ╭─╮      ╭─╮         ╭╮",
		"                       25s ┤   │ │      │ │         ││",
		"                           │   │ │      │ │         ││",
		"                         0 ┤───╯─╰──────╯─╰a────b───╯cba",
		"  REQUESTS",
		"  Requests per second       ████▆████████▆██████████████  36.6/s",
		// The stat that reads green now says in a word that its window held
		// red: the number of the maximum is given up, not the fact.
		"  Slowest pod p99           ▁▁▁███▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁  346ms green    max red",
		"  Puma threads in use       ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  37.5% green",
		"  CPU throttled, worst pod  ▁▁▁███▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁██▁▁  4% green       max red",
		"  Requests by pod           ████▂████████▂██████████████  …hx4mt 12.2/s",
		"                            ┬─────────────┬────────────┬",
		"  UTC                     04:00         04:30        05:00",
		// A folded group says the peak its first panel's number would hide.
		"  PUMA AND CPU  Puma queued connections peak 40, …hx4mt queued 0 +2 more",
		"  NOT DRAWN  Pods and nodes (table) · Webservice log lines with timeout (logs)",
		"",
		"  next  n next chart (Requests by pod)  Enter zoom  a all panels",
	)
	require.Len(t, strings.Split(normal, "\n"), 20, "the page fills the view and no more")

	// 60 columns have no room for a chart beside the rows: every group
	// folds to one line and the chart takes the width. The name of the
	// chart gives way to its highest line.
	narrow := dashText(t, s, dashPageOf(60, 20))
	dashHasLines(t, narrow,
		"Dashboard  GitLab / Webservice ove… · last 1h  05:00 · 11/11",
		"  9 of 11 panels read. 2 cannot be drawn here.",
		"  A reading, not a finding.",
		"",
		"  REQUEST DURATION P99 …  c …hx4mt 350ms peak 47.9s  +2 more",
		"50s ┤       ╭──╮             ╭c──╮                   ╭─╮",
		"    │       │  │             │   │                   │ │",
		"25s ┤       │  │             │   │                   │ │",
		"    │       │  │             │   │                   │ │",
		"    │       │  │             │   │                   │ │",
		"  0 ┤───────╯──╰────a────b───╯───╰────────────────a──╯─b─cba",
		"     ┬────────┬────────┬────────┬────────┬────────┬────────┬",
		"   04:00    04:10    04:20    04:30    04:40    04:50 UTC",
		"",
		// Every group is one line, and counts what it does not name. It
		// names first the panel whose window held the worst step of its
		// author's rule: the count never counts that one away.
		"  REQUESTS  Slowest pod p99 max red, 346ms green +4 more",
		"  PUMA AND CPU  Puma queued connections peak 40, …h… +2 more",
		"  NOT DRAWN  Pods and nodes (table) +1 more",
		"",
		"  next  n next chart (Requests by pod)  Enter zoom",
		"  ←→ time · ↑↓ chart · s queries · 1-4 window · Esc back",
	)

	// A page shorter than its evidence still says what was read, draws the
	// chart, and names what it left out with the key that shows it.
	short := dashText(t, s, dashPageOf(80, 12))
	dashHasLines(t, short,
		"  9 of 11 panels read. 2 cannot be drawn here.         A reading, not a finding.",
		"  REQUEST DURATION P99 BY POD  c …hx4mt 350ms peak 47.9s  +2 more",
		"  NOT DRAWN  Pods and nodes (table) · Webservice log lines with timeout (logs)",
	)
	require.Contains(t, short, "not shown at this size (a shows all): ")
	tiny := dashText(t, s, dashPageOf(80, 4))
	require.Contains(t, tiny, "9 of 11 panels read.")
	require.Contains(t, tiny, "a shows all detail")
}

// The next-chart key makes another panel the main chart; the one that was
// becomes a row. A stat is drawn at full size as the series behind it, and
// a chart of more than four series draws the four with the highest peak
// and says so.
func TestDashboardCalmPageDrawsAnyPanelAtFullSize(t *testing.T) {
	s := dashStorySnapshot(t)
	page := dashPageOf(132, 32)
	page.Selected = 2
	stat := dashText(t, s, page)
	dashHasLines(t, stat,
		"  SLOWEST POD P99  346ms green  max 47.9s red",
		"  REQUESTS",
		// The line chart it replaced is a row now: its highest series.
		"  Request duration p99 by pod  ▁▁▁▁▁▁▁████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁████▁▁▁▁  …hx4mt 350ms      peak 47.9s · +2 more",
		"  next  n next chart (Puma threads in use)    Enter zoom    a all panels    D open in Grafana",
	)
	require.NotContains(t, stat, "  Slowest pod p99  ", "the main chart is not a row as well")
	require.True(t, hasBlocks(stat), "one series is an area")
	require.Equal(t, 2, page.Selected)

	// Six series: the four with the highest peak are drawn, each under the
	// letter it has on every page, and the line says how many there are.
	page = dashPageOf(132, 32)
	page.Selected = 8
	many := dashText(t, s, page)
	dashHasLines(t, many,
		"  PUMA QUEUED CONNECTIONS  4 highest of 6 series  c …hx4mt queued 0 peak 40  d …2bq7k free threads 5  +2 more")
	for _, line := range strings.Split(many, "\n") {
		if strings.Contains(line, "┤") || strings.Contains(line, "│") {
			require.NotContains(t, line, "a", "a line that is not drawn has no letter in the plot: %q", line)
			require.NotContains(t, line, "b", "%q", line)
		}
	}
	require.Contains(t, many, "fedc", "the four lines end under the letters they have in the grid and in the zoomed panel")

	// A panel the page cannot draw is never the main chart: the choice
	// falls back to the first line chart.
	page = dashPageOf(132, 32)
	page.Selected = len(s.Dash.Panels) - 1
	require.Contains(t, dashText(t, s, page), "  REQUEST DURATION P99 BY POD ")
	require.Equal(t, 5, page.Selected)
}

// Left and right move a cursor through the chart and every row: the
// legend and the numbers then read the sample taken at that time, and the
// line over the chart says which time that is.
func TestDashboardCalmPageReadsEveryRowAtTheCursor(t *testing.T) {
	s := dashStorySnapshot(t)
	page := dashPageOf(132, 32)
	// Thirty-one minutes before the end lies inside the second stall.
	page.Cursor = int(s.Window/s.Step) - 62
	text := dashText(t, s, page)
	dashHasLines(t, text,
		"  REQUEST DURATION P99 BY POD  at 04:29:00Z  c …hx4mt 47.9s  a …2bq7k 310ms  b …8wzpd 310ms",
		"  Requests per second       ████████▆▆▆█████████████████▆▆▆▆████████████████████████▆██████  25.1/s",
		"  Slowest pod p99           ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  47.9s red         max 47.9s red",
		"  CPU throttled, worst pod  ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  86% red           max 86% red",
		"  Requests by pod           ████████▂▂▂█████████████████▂▂▂▂████████████████████████▂██████  …hx4mt 1.5/s      +2 more",
		// The number is the sample at the cursor. The peak beside it is the
		// window's, the scale of the strip, and stays.
		"  Puma queued connections   ▁▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁██████▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁███▁▁▁▁▁  …hx4mt queued 40  peak 40 · +5 more",
		// The caret on the axis stands under the second stall.
		"                            ┬─────────┬──────────┬────────^┬──────────┬─────────┬─────────┬",
	)
	// A line of words is no place for the cursor: it runs through the
	// blank cells of the plot and of the rows between the groups.
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "REQUEST DURATION") {
			require.NotContains(t, line, "┆")
			require.NotContains(t, line, "peak", "at the cursor the legend is the samples only")
		}
	}
	require.Contains(t, text, "┆", "the cursor runs through the page")
	// At the window's end no cursor is drawn.
	end := dashText(t, s, dashPageOf(132, 32))
	require.NotContains(t, end, "┆")
	require.NotContains(t, end, "^")
}

// Moving the cursor changes numbers, never the page: the plot, every
// strip and the axis stay in the columns they have without the cursor, at
// every width and wherever the cursor stands, so a sample is read in one
// cell throughout. The columns are as wide as the widest number of the
// window, not as the number under the cursor.
func TestCalmPagesKeepTheirColumnsWhileTheCursorMoves(t *testing.T) {
	// marks is where the strips, the plot's labelled rows and the time
	// axis stand on a page: for each line that has one, the columns of its
	// first and last cell. The caret stands on the axis in place of a cell.
	marks := func(text string) []string {
		var out []string
		for _, line := range strings.Split(text, "\n") {
			if !strings.ContainsAny(line, "▁▂▃▄▅▆▇█×┬┤") {
				continue
			}
			first, last := -1, -1
			for at, r := range []rune(line) {
				if strings.ContainsRune("▁▂▃▄▅▆▇█×┬┤─^", r) {
					if first < 0 {
						first = at
					}
					last = at
				}
			}
			out = append(out, fmt.Sprintf("%d-%d", first, last))
		}
		return out
	}
	story := dashStorySnapshot(t)
	board := loadDash(t.Context(), dashStory(), dashboard.QueryBoard(dashboard.NamedQueries(nil)), &dashSpec{Board: true}, dashLabel, dashDeployment, time.Hour, graphNow)
	typed := querySpec(nil, `sum by (pod) (puma_queued_connections{namespace="$namespace", pod=~"$pod"})`)
	query := loadDash(t.Context(), dashStory(), dashboard.QueryPage(typed.Query), &typed, dashLabel, dashDeployment, time.Hour, graphNow)
	for name, s := range map[string]*dashSnapshot{"dashboard": story, "named queries": board, "one query": query} {
		for _, size := range [][2]int{{132, 32}, {100, 26}, {80, 20}, {60, 20}} {
			still := marks(dashText(t, s, dashPageOf(size[0], size[1])))
			require.NotEmpty(t, still)
			// The window's start (which has no column on the page, and reads
			// as the first step), the first step, a quiet minute, the second
			// stall, and the sample before the end.
			for _, cursor := range []int{0, 1, 30, 58, int(s.Window/s.Step) - 1} {
				page := dashPageOf(size[0], size[1])
				page.Cursor = cursor
				text := dashText(t, s, page)
				require.Contains(t, text, "^", "%s at %dx%d: the cursor is on the axis", name, size[0], size[1])
				require.Equal(t, still, marks(text), "%s at %dx%d, cursor %d: the page moved under the cursor:\n%s", name, size[0], size[1], cursor, text)
			}
		}
	}
}

// The all-detail key shows the page as it was before the calm layout: the
// grid of every panel.
func TestDashboardAllDetailIsTheGridOfEveryPanel(t *testing.T) {
	s := dashStorySnapshot(t)
	require.Equal(t, time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC), s.End, "the window end is aligned to the step")

	page := dashDetailOf(132, 32)
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
		"←/→ ↑/↓ panel · Enter zoom · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Shift-D Grafana · a calm layout · b dots · Esc back",
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
	normal := dashText(t, s, dashDetailOf(80, 20))
	for _, want := range []string{
		"read 05:00:07Z · 9/11 panels drawn · 11/11 answered · 1-5/11 ↑↓",
		"> Requests per second                      Slowest pod p99",
		"  Puma threads in use                      CPU throttled, worst pod",
		"min 346ms  max 47.9s (red)",
		"Request duration p99 by pod  a …2bq7k 310ms  b …8wzpd 310ms", "c …hx4mt 350ms peak 47.9s",
		"←/→ ↑/↓ panel · Enter zoom · 1-4 window · r re-read · s queries · a calm · Esc",
	} {
		require.Contains(t, normal, want)
	}
	require.NotContains(t, normal, "Requests by pod")
	require.NotContains(t, normal, "for Deployment", "a short page gives the line to the panels; s lists the variables")

	// 60 columns: nothing is cut that changes the meaning.
	narrow := dashText(t, s, dashDetailOf(60, 20))
	for _, want := range []string{
		"GitLab / Webservice overview · 1h to 05:00Z · step 30s", "read 05:00:07Z · 9/11 drawn · 11/11 ok · 1-5/11 ↑↓",
		"> Requests per second            Slowest pod p99", "346ms  green", "min 346ms  max 47.9s (red)",
		"b …8wzpd 310ms  c …hx4mt 350ms peak 47.9s", "←/→ panel · Enter zoom · 1-4 window · s queries · a calm",
	} {
		require.Contains(t, narrow, want)
	}

	// Scrolling to the end shows what was not drawn, each with its reason.
	last := dashDetailOf(132, 32)
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
	tiny := dashDetailOf(80, 8)
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
		// Zoomed from the calm page, Esc goes back to it.
		"←/→ cursor · ↑/↓ panel · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Shift-D Grafana · b dots · Esc back",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "peak", "at the cursor a legend holds samples only")
	// Zoomed from the grid of every panel, Esc goes back to the grid.
	page.Detail = true
	require.Contains(t, dashText(t, s, page), "Shift-D Grafana · b dots · Esc grid")
	page.Detail = false
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
		"←/→ cursor · ↑/↓ panel · 1-4 window · r read · Esc back"} {
		require.Contains(t, text, want)
	}
}
