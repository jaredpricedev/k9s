// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const graphMi = 1024.0 * 1024.0

var graphNow = time.Date(2026, 11, 17, 5, 0, 7, 0, time.UTC)

// graphFake answers queries by the first rule whose text is in the query.
// A rule returns the value at a time, or false for "no sample there".
type graphFake struct {
	mu      sync.Mutex
	asked   []string
	rules   []graphRule
	status  *observability.Status
	pods    []string
	ownerSt *observability.Status
}

type graphRule struct {
	contains string
	by       []string // one series per label value of "pod"; nil is one unlabelled series
	value    func(pod string, at time.Time) (float64, bool)
}

func (*graphFake) Display() string { return "service monitoring/prometheus:9090" }

func (f *graphFake) record(e observability.Expr) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, e.String())
	return e.String()
}

func (f *graphFake) Query(_ context.Context, e observability.Expr, at time.Time) observability.PromResult {
	res := observability.PromResult{Status: observability.Status{State: observability.StateComplete, ObservedAt: graphNow}, Query: f.record(e), Start: at, End: at}
	if f.ownerSt != nil {
		res.Status = *f.ownerSt
		return res
	}
	for _, pod := range f.pods {
		res.Series = append(res.Series, observability.PromSeries{Labels: map[string]string{"pod": pod}, Samples: []observability.Sample{{Timestamp: at, Value: 1}}})
	}
	return res
}

func (f *graphFake) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	res := observability.PromResult{Status: observability.Status{State: observability.StateComplete, ObservedAt: graphNow},
		Query: f.record(e), Start: start, End: end, Step: observability.StepFor(end.Sub(start))}
	if f.status != nil {
		res.Status = *f.status
		return res
	}
	for _, rule := range f.rules {
		if !strings.Contains(res.Query, rule.contains) {
			continue
		}
		pods := rule.by
		if pods == nil {
			pods = []string{""}
		}
		for _, pod := range pods {
			series := observability.PromSeries{Labels: map[string]string{}}
			if pod != "" {
				series.Labels["pod"] = pod
			}
			for at := start; !at.After(end); at = at.Add(res.Step) {
				if v, ok := rule.value(pod, at); ok {
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

func graphConst(v float64) func(string, time.Time) (float64, bool) {
	return func(string, time.Time) (float64, bool) { return v, true }
}

// graphStory is the fixture incident seen from one Pod: created 12 minutes
// before the window end, memory climbing to its 256Mi limit, six restarts.
func graphStory() *graphFake {
	end := graphNow.Truncate(30 * time.Second)
	created := end.Add(-12 * time.Minute)
	since := func(v func(time.Time) float64) func(string, time.Time) (float64, bool) {
		return func(_ string, at time.Time) (float64, bool) { return v(at), !at.Before(created) }
	}
	return &graphFake{rules: []graphRule{
		{contains: "container_memory_working_set_bytes", value: since(func(at time.Time) float64 {
			return (60 + 194*float64(at.Sub(created)%(2*time.Minute))/float64(2*time.Minute)) * graphMi
		})},
		{contains: `kube_pod_container_resource_limits{namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q",resource="memory"}`, value: since(func(time.Time) float64 { return 256 * graphMi })},
		{contains: `kube_pod_container_resource_requests{namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q",resource="memory"}`, value: since(func(time.Time) float64 { return 128 * graphMi })},
		{contains: "container_cpu_usage_seconds_total", value: since(func(time.Time) float64 { return 0.34 })},
		{contains: `kube_pod_container_resource_requests{namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q",resource="cpu"}`, value: since(func(time.Time) float64 { return 0.1 })},
		{contains: `resource="cpu"}`, value: func(string, time.Time) (float64, bool) { return 0, false }},
		{contains: "container_cpu_cfs", value: func(string, time.Time) (float64, bool) { return 0, false }},
		{contains: "container_network_receive_bytes_total", value: since(func(time.Time) float64 { return 2600 })},
		{contains: "container_network_transmit_bytes_total", value: since(func(time.Time) float64 { return 9000 })},
		{contains: "kube_pod_container_status_restarts_total", by: []string{"checkout-api-7d9f8c6b5d-x2x9q"}, value: func(_ string, at time.Time) (float64, bool) {
			if at.Before(created.Add(2 * time.Minute)) {
				return 0, !at.Before(created)
			}
			return float64(min(int(at.Sub(created.Add(2*time.Minute))/(100*time.Second))+1, 6)), true
		}},
	}}
}

var graphPod = &graphTarget{Context: "ct-1-1", Kind: "Pod", Resource: client.PodGVR.String(), Namespace: "apps", Name: "checkout-api-7d9f8c6b5d-x2x9q"}

func graphPlainLines(t *testing.T, lines []string, width int) string {
	t.Helper()
	for _, line := range lines {
		require.LessOrEqual(t, tview.TaggedStringWidth(line), width, "line exceeds %d cells: %q", width, line)
	}
	text := strings.Join(lines, "\n")
	return tview.NewTextView().SetDynamicColors(true).SetText(text).GetText(true)
}

func TestGraphTargetNeedsOneSupportedObject(t *testing.T) {
	for _, tc := range []struct {
		target SelectedResourceTarget
		want   string
	}{
		{SelectedResourceTarget{GVR: client.PodGVR, Namespace: "apps", Name: "a"}, "Pod apps/a"},
		{SelectedResourceTarget{GVR: client.DpGVR, Namespace: "apps", Name: "a"}, "Deployment apps/a"},
		{SelectedResourceTarget{GVR: client.StsGVR, Namespace: "apps", Name: "a"}, "StatefulSet apps/a"},
		{SelectedResourceTarget{GVR: client.DsGVR, Namespace: "apps", Name: "a"}, "DaemonSet apps/a"},
		{SelectedResourceTarget{GVR: client.RsGVR, Namespace: "apps", Name: "a"}, "ReplicaSet apps/a"},
		{SelectedResourceTarget{GVR: client.NodeGVR, Name: "worker-b"}, "Node worker-b"},
		// A Service is graphed through the Pods behind it, which are resolved
		// by its captured UID.
		{SelectedResourceTarget{GVR: client.SvcGVR, Namespace: "apps", Name: "a", UID: "uid-a"}, "Service apps/a"},
		{SelectedResourceTarget{GVR: client.CmGVR, Namespace: "apps", Name: "a"}, ""},
		{SelectedResourceTarget{GVR: client.PodGVR, Name: "a"}, ""},
		{SelectedResourceTarget{UnavailableReason: "no selection"}, ""},
	} {
		got, err := graphTargetFor(tc.target)
		if tc.want == "" {
			require.ErrorContains(t, err, "Select a Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Service or Node first")
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.want, got.Label())
	}
	_, err := graphTargetFor(SelectedResourceTarget{GVR: client.SvcGVR, Namespace: "apps", Name: "a"})
	require.ErrorContains(t, err, "Select the Service in its list first", "without its UID the Pods behind a Service are not resolved")
}

func TestGraphPodQueriesAreFixedTemplatesWithEscapedMatchers(t *testing.T) {
	plan := buildGraphPlan(graphPod, nil, 30*time.Second)
	got := map[string]string{}
	for _, panel := range plan.Panels {
		for _, series := range panel.Series {
			require.NoError(t, series.Expr.Err())
			got[panel.ID+"/"+series.ID] = series.Expr.String()
		}
	}
	got["restarts"] = plan.Restarts.Expr.String()
	const pod = `namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q"`
	const containers = pod + `,container!="",container!="POD"`
	require.Equal(t, map[string]string{
		"memory/usage":   `sum by (pod) (max_over_time(container_memory_working_set_bytes{` + containers + `}[60s]))`,
		"memory/limit":   `sum(sum by (pod) (kube_pod_container_resource_limits{` + pod + `,resource="memory"}))`,
		"memory/request": `sum(sum by (pod) (kube_pod_container_resource_requests{` + pod + `,resource="memory"}))`,
		"cpu/usage":      `sum by (pod) (rate(container_cpu_usage_seconds_total{` + containers + `}[120s]))`,
		"cpu/limit":      `sum(sum by (pod) (kube_pod_container_resource_limits{` + pod + `,resource="cpu"}))`,
		"cpu/request":    `sum(sum by (pod) (kube_pod_container_resource_requests{` + pod + `,resource="cpu"}))`,
		"cpu/throttled": `(sum(rate(container_cpu_cfs_throttled_periods_total{` + containers + `}[120s])) / ` +
			`sum(rate(container_cpu_cfs_periods_total{` + containers + `}[120s])))`,
		"network/rx": `sum(rate(container_network_receive_bytes_total{` + pod + `}[120s]))`,
		"network/tx": `sum(rate(container_network_transmit_bytes_total{` + pod + `}[120s]))`,
		"restarts":   `sum by (pod) (kube_pod_container_status_restarts_total{` + pod + `})`,
	}, got)

	// The rate and peak ranges follow the step of longer windows.
	day := buildGraphPlan(graphPod, nil, 15*time.Minute)
	require.Contains(t, day.Panels[0].Series[0].Expr.String(), "[900s]")
	require.Contains(t, day.Panels[1].Series[0].Expr.String(), "[3600s]")
	require.Equal(t, "cores, rate over 1h", day.Panels[1].Note)

	// A name is data: it cannot close the matcher or add one.
	hostile := *graphPod
	hostile.Name = `x"} or vector(1) #`
	text := buildGraphPlan(&hostile, nil, 30*time.Second).Panels[0].Series[0].Expr.String()
	require.Contains(t, text, `pod="x\"} or vector(1) #"`)
}

func TestGraphWorkloadFindsPodsThroughKubeStateMetricsAndFoldsPastTheCap(t *testing.T) {
	dp := &graphTarget{Kind: "Deployment", Namespace: "apps", Name: "checkout.api"}
	// The lookup also keeps the Node and the owner of each Pod: they place a
	// Pod in a group when the series are regrouped.
	require.Equal(t, `count by (pod,node,created_by_kind,created_by_name) (max_over_time(kube_pod_info{namespace="apps",created_by_kind="ReplicaSet",created_by_name=~"checkout\\.api-[a-z0-9]{5,10}"}[3600s]))`,
		graphOwnerQuery(dp, time.Hour).String())
	sts := &graphTarget{Kind: "StatefulSet", Namespace: "ops", Name: "ledger-db"}
	require.Equal(t, `count by (pod,node,created_by_kind,created_by_name) (max_over_time(kube_pod_info{namespace="ops",created_by_kind="StatefulSet",created_by_name="ledger-db"}[900s]))`,
		graphOwnerQuery(sts, 15*time.Minute).String())

	few := buildGraphPlan(sts, []string{"ledger-db-0", "ledger-db-1"}, 30*time.Second)
	usage := few.Panels[0].Series[0]
	require.Equal(t, "pod", usage.By, "one line per Pod")
	require.Contains(t, usage.Expr.String(), `pod=~"ledger-db-0|ledger-db-1"`)
	require.Equal(t, "min limit", few.Panels[0].Series[1].Name)
	require.Equal(t, `min(sum by (pod) (kube_pod_container_resource_limits{namespace="ops",pod=~"ledger-db-0|ledger-db-1",resource="memory"}))`,
		few.Panels[0].Series[1].Expr.String())

	many := buildGraphPlan(sts, []string{"p-0", "p-1", "p-2", "p-3", "p-4"}, 30*time.Second)
	var names []string
	for _, series := range many.Panels[0].Series {
		names = append(names, series.Name)
	}
	require.Equal(t, []string{"avg", "max", "min", "min limit", "min request"}, names)
	require.True(t, strings.HasPrefix(many.Panels[0].Series[0].Expr.String(), "avg(sum by (pod) ("))
	require.Empty(t, many.Restarts.By)

	// The matcher stays bounded however many Pods there are.
	var pods []string
	for i := range 200 {
		pods = append(pods, "very-long-generated-pod-name-for-a-large-daemonset-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	matcher, used, omitted := graphPodMatcher(pods)
	require.LessOrEqual(t, len(matcher.Value), graphPodRegexLimit)
	require.LessOrEqual(t, used, graphPodNameLimit)
	require.Equal(t, 200, used+omitted)
	require.NoError(t, observability.Select("up", matcher).Err())
}

func TestGraphNodeQueriesCompareWithAllocatable(t *testing.T) {
	plan := buildGraphPlan(&graphTarget{Kind: "Node", Name: "worker-c"}, nil, 30*time.Second)
	require.Nil(t, plan.Restarts)
	var titles []string
	for _, panel := range plan.Panels {
		titles = append(titles, panel.Title)
		for _, series := range panel.Series {
			require.NoError(t, series.Expr.Err(), panel.Title+" "+series.Name)
		}
	}
	require.Equal(t, []string{"CPU cores", "MEMORY", "DISK /", "PODS"}, titles)
	require.Equal(t, `sum(rate(node_cpu_seconds_total{node="worker-c",mode!~"idle|iowait|steal"}[120s]))`, plan.Panels[0].Series[0].Expr.String())
	require.Equal(t, `sum(kube_node_status_allocatable{node="worker-c",resource="cpu"})`, plan.Panels[0].Series[1].Expr.String())
	require.Equal(t, `(sum(node_memory_MemTotal_bytes{node="worker-c"}) - sum(node_memory_MemAvailable_bytes{node="worker-c"}))`, plan.Panels[1].Series[0].Expr.String())
	require.Equal(t, `(sum(node_filesystem_size_bytes{node="worker-c",mountpoint="/"}) - sum(node_filesystem_avail_bytes{node="worker-c",mountpoint="/"}))`,
		plan.Panels[2].Series[0].Expr.String())
	require.Equal(t, `count(kube_pod_info{node="worker-c"})`, plan.Panels[3].Series[0].Expr.String())
	require.Equal(t, `sum(kube_node_status_allocatable{node="worker-c",resource="pods"})`, plan.Panels[3].Series[1].Expr.String())
}

func TestGraphPageDrawsTheStoryAtEverySize(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphStory(), graphPod, time.Hour, graphNow)
	require.Equal(t, time.Date(2026, 11, 17, 5, 0, 0, 0, time.UTC), snapshot.End, "the window end is aligned to the step")
	require.Equal(t, 30*time.Second, snapshot.Step)
	require.Len(t, snapshot.Results, 10)
	snapshot.Marks = []graphMark{{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2", Origin: "creation time of ReplicaSet checkout-api-7d9f8c6b5d (revision 2)"}}
	last := int(snapshot.Window / snapshot.Step)

	wide := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 120, Height: 30, Cursor: last, Colors: graphPalette(config.DefaultSemanticPalette())}), 120)
	for _, want := range []string{
		"Graph · Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h to 05:00Z · step 30s",
		"read 05:00:07Z · 10 of 10 queries answered · 2 no data",
		// A chart with one line is a filled area, named by a block in the
		// legend; the two lines of the network chart keep their letters.
		"MEMORY  █ working set 60Mi  ─ limit 256Mi  ┄ request 128Mi",
		"CPU cores  █ usage 340m  ─ limit no data  ┄ request 100m  throttled no data",
		"NETWORK  a rx 2.6kB/s  b tx 9kB/s",
		"256Mi ┤", "128Mi ┤", "restarts │", "▲",
		"04:10", "04:55",
		"R rollout rev 2 04:48 · F first restart 04:50 · order, not cause",
		"Cursor 05:00:00Z (window end) · restarts 6, +6 in window",
	} {
		require.Contains(t, wide, want)
	}
	require.NotContains(t, wide, "charts (", "all three charts fit")

	// 80x24 leaves 20 rows inside the frame: two charts, and it says so.
	normal := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 80, Height: 20, Cursor: last}), 80)
	require.Contains(t, normal, "2 of 3 charts (↑/↓)")
	require.Contains(t, normal, "MEMORY  █ working set")
	require.Contains(t, normal, "CPU cores  █ usage 340m")
	require.NotContains(t, normal, "NETWORK")
	require.Contains(t, normal, "order, not cause")
	require.LessOrEqual(t, strings.Count(normal, "\n")+1, 20)

	// 60 columns: nothing is cut that changes the meaning.
	narrow := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 60, Height: 20, Cursor: last - 40}), 60)
	for _, want := range []string{
		"· 1h to 05:00Z · step 30s", "x2x9q", "10/10 answered · 2 no data · 1/3 charts ↑↓",
		"MEMORY  █ working set – first 04:48", "─ limit – first 04:48  ┄ request – first 04:48", "restarts –, +6 in window",
		"Cursor 04:40:00Z, -20m", "order, not cause", "1-4 window",
	} {
		require.Contains(t, narrow, want)
	}

	// Scrolling shows the chart that did not fit.
	scrolled := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 80, Height: 20, Cursor: last, First: 2}), 80)
	require.Contains(t, scrolled, "NETWORK  a rx 2.6kB/s  b tx 9kB/s")
	require.NotContains(t, scrolled, "MEMORY")
}

func TestGraphCursorReadsExactSamplesNotInterpolations(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphStory(), graphPod, time.Hour, graphNow)
	page := &graphPage{Width: 100, Height: 30}
	// Seven and a half minutes after creation: 60 + 194*(1.5min of 2min).
	page.Cursor = int(snapshot.Window/snapshot.Step) - 9
	text := graphPlainLines(t, renderGraph(snapshot, page), 100)
	require.Contains(t, text, "Cursor 04:55:30Z, 4m30s before window end")
	require.Contains(t, text, "█ working set 206Mi")
	require.Contains(t, text, "restarts 4, +6 in window")
}

func TestGraphNoIconsPageIsASCII(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphStory(), graphPod, time.Hour, graphNow)
	snapshot.Marks = []graphMark{{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2"}}
	text := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 60, Height: 20, ASCII: true, Cursor: 100}), 60)
	for _, r := range text {
		require.True(t, r < 128 || r == '·', "%q is neither ASCII nor the shared separator", r)
	}
	for _, want := range []string{"= limit 256Mi", ". request 128Mi", "restarts |", "^", "Left/Right cursor", "Pod apps/checkout...7d9f8c6b5d-x2x9q", "10/10 ok · 2 no data · 2/3 charts Up/Down", "order, not cause"} {
		require.Contains(t, text, want)
	}
}

func TestGraphEmptyAnswersAreNoDataNeverAZeroLine(t *testing.T) {
	fake := &graphFake{}
	snapshot := loadGraph(t.Context(), fake, graphPod, 15*time.Minute, graphNow)
	text := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 80, Height: 30}), 80)
	require.Contains(t, text, "10 of 10 queries answered · 10 no data")
	require.Equal(t, 3, strings.Count(text, "no samples in this window (not a zero)"))
	require.Contains(t, text, "█ working set no data")
	require.Contains(t, text, "restarts no data")
	// Nothing is plotted without samples: no dot, no block of an area
	// (the one block of a line is the legend's name for the series) and
	// no stroke of a line.
	for _, r := range text {
		require.False(t, r >= 0x2800 && r <= 0x28ff, "nothing is plotted without samples")
		require.False(t, r >= 0x2581 && r <= 0x2587, "nothing is filled without samples")
	}
	for _, line := range strings.Split(text, "\n") {
		if at := strings.Index(line, "│"); at >= 0 && !strings.Contains(line, "└") {
			require.Empty(t, strings.Trim(line[at+len("│"):], " ┆"+"no samples in this window (not a zero)"), "an empty plot row: %q", line)
		}
	}
	dotted := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 80, Height: 30, Style: chart.Dots}), 80)
	require.Contains(t, dotted, "a working set no data")
	require.Equal(t, 3, strings.Count(dotted, "no samples in this window (not a zero)"))
	require.NotContains(t, text, "┤", "no scale is invented")
	require.Nil(t, snapshot.failure(), "an answered query is not a failure")
	report := graphReport(snapshot)
	require.Contains(t, report, "answered, matched nothing: missing data, not zero")
	require.Contains(t, report, "Window: 2026-11-17 04:45:00Z to 2026-11-17 05:00:00Z (last 15m, UTC)")
	require.Contains(t, report, "Step: 15s")
}

func TestGraphFailuresSayWhichAndPointToSources(t *testing.T) {
	for _, tc := range []struct {
		state observability.State
		want  []string
	}{
		{observability.StateDenied, []string{"Prometheus DENIED all 10 queries (HTTP 403) at 05:00:07Z.", "a denied read is not a reading of zero", ":sources"}},
		{observability.StateTimeout, []string{"Prometheus TIMEOUT: all 10 queries was not answered in time", "Press r to ask again", ":sources"}},
		{observability.StateAbsent, []string{"Prometheus ABSENT: the declared source was not found", ":sources"}},
		{observability.StateUnavailable, []string{"Prometheus UNAVAILABLE: all 10 queries failed", ":sources"}},
	} {
		st := observability.Status{State: tc.state, Reason: "HTTP 403", ObservedAt: graphNow}
		snapshot := loadGraph(t.Context(), &graphFake{status: &st}, graphPod, time.Hour, graphNow)
		for _, width := range []int{80, 60} {
			text := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: width, Height: 20}), width)
			flat := strings.Join(strings.Fields(text), " ")
			for _, want := range tc.want {
				require.Contains(t, flat, want)
			}
			require.NotContains(t, text, "┤")
		}
	}

	st := observability.Status{State: observability.StateNotConfigured}
	none := &graphSnapshot{Target: *graphPod, Window: time.Hour, Step: 30 * time.Second, Start: graphNow.Add(-time.Hour), End: graphNow, Status: &st, Source: "no source"}
	text := graphPlainLines(t, renderGraph(none, &graphPage{Width: 80, Height: 20}), 80)
	require.Contains(t, text, "No Prometheus source is declared for this context, so nothing was asked.")
	require.Contains(t, text, "Open :sources to see what to declare")

	// One failed query among answered ones is counted, and its chart says so.
	partial := graphStory()
	snapshot := loadGraph(t.Context(), partial, graphPod, time.Hour, graphNow)
	denied := snapshot.Results["network/rx"]
	denied.Status, denied.Series = observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}, nil
	snapshot.Results["network/rx"] = denied
	text = graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 100, Height: 34, Cursor: 120}), 100)
	require.Contains(t, text, "9 of 10 queries answered, 1 denied")
	require.Contains(t, text, "NETWORK  a rx denied  b tx 9kB/s")
}

func TestGraphWorkloadPageNamesPodsAndStatesMissingOwnership(t *testing.T) {
	dp := &graphTarget{Context: "ct-1-1", Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}
	pods := []string{"checkout-api-6c8d7f9b5-k4m2t", "checkout-api-7d9f8c6b5d-x2x9q"}
	end := graphNow.Truncate(30 * time.Second)
	fake := &graphFake{pods: pods, rules: []graphRule{
		{contains: "container_memory_working_set_bytes", by: pods, value: func(pod string, at time.Time) (float64, bool) {
			if strings.HasSuffix(pod, "k4m2t") {
				return 185 * graphMi, at.Before(end.Add(-11 * time.Minute))
			}
			return 250 * graphMi, !at.Before(end.Add(-12 * time.Minute))
		}},
		{contains: "kube_pod_container_resource_limits", value: func(_ string, at time.Time) (float64, bool) {
			if at.Before(end.Add(-12 * time.Minute)) {
				return 512 * graphMi, true
			}
			return 256 * graphMi, true
		}},
	}}
	snapshot := loadGraph(t.Context(), fake, dp, time.Hour, graphNow)
	require.Equal(t, pods, snapshot.Pods)
	text := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 100, Height: 30, Cursor: 120}), 100)
	require.Contains(t, text, "2 pods")
	// 250Mi of a 256Mi limit is at the limit, and the legend says so.
	require.Contains(t, text, "MEMORY  a k4m2t – last 04:48  b x2x9q 250Mi at min limit  ─ min limit 256Mi")
	require.Contains(t, text, "512Mi ┤")
	report := graphReport(snapshot)
	require.Contains(t, report, "Pods found: checkout-api-6c8d7f9b5-k4m2t, checkout-api-7d9f8c6b5d-x2x9q")
	require.Contains(t, report, "holes: checkout-api-6c8d7f9b5-k4m2t ends 04:48:30Z (drawn as gaps)")
	require.Contains(t, report, "holes: checkout-api-7d9f8c6b5d-x2x9q begins 04:48:00Z (drawn as gaps)")

	// More Pods than the cap: folded, and the page says how many.
	many := &graphFake{pods: []string{"a-1", "a-2", "a-3", "a-4", "a-5", "a-6"}, rules: []graphRule{
		{contains: "avg(sum by (pod) (max_over_time(container_memory", value: graphConst(100 * graphMi)},
		{contains: "max(sum by (pod) (max_over_time(container_memory", value: graphConst(180 * graphMi)},
		{contains: "min(sum by (pod) (max_over_time(container_memory", value: graphConst(40 * graphMi)},
	}}
	folded := loadGraph(t.Context(), many, dp, time.Hour, graphNow)
	text = graphPlainLines(t, renderGraph(folded, &graphPage{Width: 100, Height: 30, Cursor: 120}), 100)
	require.Contains(t, text, "6 pods folded into min/avg/max")
	require.Contains(t, text, "MEMORY  a avg 100Mi  b max 180Mi  c min 40Mi")

	// Prometheus answers but names no Pod: missing data, stated as such.
	orphan := loadGraph(t.Context(), &graphFake{}, dp, time.Hour, graphNow)
	text = strings.Join(strings.Fields(graphPlainLines(t, renderGraph(orphan, &graphPage{Width: 80, Height: 20}), 80)), " ")
	require.Contains(t, text, "kube_pod_info names no Pod owned by this Deployment in the window")
	require.Contains(t, text, "missing data, not a healthy reading")
	require.Empty(t, orphan.Results, "no chart query is sent without Pods")

	st := observability.Status{State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: graphNow}
	refused := loadGraph(t.Context(), &graphFake{ownerSt: &st}, dp, time.Hour, graphNow)
	text = graphPlainLines(t, renderGraph(refused, &graphPage{Width: 80, Height: 20}), 80)
	require.Contains(t, text, "Prometheus DENIED the lookup of this Deployment's Pods (HTTP 403)")
}

func TestGraphRestartEventsAreRisesBetweenNeighbouringSamples(t *testing.T) {
	at := func(m int) time.Time { return graphNow.Add(time.Duration(m) * time.Minute) }
	res := &observability.PromResult{Series: []observability.PromSeries{{Samples: []observability.Sample{
		{Timestamp: at(0), Value: 2}, {Timestamp: at(1), Value: 2}, {Timestamp: at(2), Value: 3}, {Timestamp: at(3), Value: 5},
		// A hole: the rise across it has no known time and is not marked,
		// but it did happen inside the window and is counted.
		{Timestamp: at(10), Value: 9}, {Timestamp: at(11), Value: 9},
		// A counter that falls (a replaced Pod) is not a restart.
		{Timestamp: at(12), Value: 0},
	}}}}
	events, rise, measured := graphRestartEvents(res, time.Minute)
	require.Equal(t, []time.Time{at(2), at(3)}, events)
	require.InDelta(t, 7.0, rise, 0)
	require.True(t, measured)

	one := &observability.PromResult{Series: []observability.PromSeries{{Samples: []observability.Sample{{Timestamp: at(0), Value: 6}}}}}
	_, rise, measured = graphRestartEvents(one, time.Minute)
	require.Zero(t, rise)
	require.False(t, measured, "one sample cannot show a rise")
}

func graphObject(apiVersion, kind, name string, created time.Time, mutate func(*unstructured.Unstructured)) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"namespace": "apps", "name": name, "creationTimestamp": created.UTC().Format(time.RFC3339)}}}
	if mutate != nil {
		mutate(u)
	}
	return u
}

func TestGraphMarksComeFromTheObjectsOwnTimes(t *testing.T) {
	created := graphNow.Add(-12 * time.Minute).Truncate(time.Second)
	controller := true
	owned := func(revision string) func(*unstructured.Unstructured) {
		return func(u *unstructured.Unstructured) {
			u.SetAnnotations(map[string]string{graphRevisionKey: revision})
			u.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "checkout-api", Controller: &controller}})
		}
	}
	pod := graphObject("v1", "Pod", graphPod.Name, created, func(u *unstructured.Unstructured) {
		u.SetOwnerReferences([]metav1.OwnerReference{{Kind: "ReplicaSet", Name: "checkout-api-7d9f8c6b5d"}})
		u.Object["status"] = map[string]any{"containerStatuses": []any{map[string]any{"name": "api", "lastState": map[string]any{"terminated": map[string]any{
			"reason": "OOMKilled", "exitCode": int64(137), "finishedAt": graphNow.Add(-2 * time.Minute).Format(time.RFC3339)}}}}}
	})
	dial := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		client.RsGVR.GVR(): "ReplicaSetList", client.PodGVR.GVR(): "PodList", client.DpGVR.GVR(): "DeploymentList"},
		pod,
		graphObject("apps/v1", "ReplicaSet", "checkout-api-6c8d7f9b5", created.Add(-72*time.Hour), owned("1")),
		graphObject("apps/v1", "ReplicaSet", "checkout-api-7d9f8c6b5d", created, owned("2")),
		graphObject("apps/v1", "ReplicaSet", "other-5f4", graphNow, func(u *unstructured.Unstructured) {
			u.SetAnnotations(map[string]string{graphRevisionKey: "9"})
			u.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "other", Controller: &controller}})
		}),
		graphObject("apps/v1", "Deployment", "checkout-api", created.Add(-96*time.Hour), nil))

	marks, note := graphMarksFrom(t.Context(), dial, graphPod)
	require.Empty(t, note)
	require.Len(t, marks, 2)
	require.Equal(t, graphMark{Key: 'R', T: created, Label: "rollout rev 2", Origin: "creation time of ReplicaSet checkout-api-7d9f8c6b5d (revision 2)"}, marks[0])
	require.Equal(t, "last exit OOMKilled (137)", marks[1].Label)
	require.Equal(t, 'X', marks[1].Key)
	require.Equal(t, "lastState.terminated.finishedAt of container api", marks[1].Origin)

	marks, _ = graphMarksFrom(t.Context(), dial, &graphTarget{Kind: "Deployment", Namespace: "apps", Name: "checkout-api"})
	require.Len(t, marks, 1)
	require.Equal(t, "rollout rev 2", marks[0].Label, "the newest revision this Deployment owns, not another Deployment's")
	require.Equal(t, "creation time of ReplicaSet checkout-api-7d9f8c6b5d (revision 2)", marks[0].Origin, "the same revision read :impact uses")
	_, note = graphMarksFrom(t.Context(), dial, &graphTarget{Kind: "Deployment", Namespace: "apps", Name: "gone"})
	require.Contains(t, note, "revision history was not read")

	marks, note = graphMarksFrom(t.Context(), dial, &graphTarget{Kind: "Pod", Namespace: "apps", Name: "gone"})
	require.Empty(t, marks)
	require.Contains(t, note, "The Pod was not read")
	marks, _ = graphMarksFrom(t.Context(), dial, &graphTarget{Kind: "Node", Name: "worker-b"})
	require.Empty(t, marks)

	lines := graphMarkLines([]graphMark{{Key: 'R', T: created, Label: "rollout rev 2"}}, chart.Frame{Start: graphNow.Add(-5 * time.Minute), End: graphNow, Step: 15 * time.Second, Width: 60}, 80)
	require.Equal(t, []string{"R rollout rev 2 04:48 (before window) · order, not cause"}, lines)
}

func graphViewFixture(t *testing.T) *graphView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	declareStudySources(t, app)
	target := *graphPod
	target.Context = app.Config.ActiveContextName()
	v := newGraphView(app, &target, 0)
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	return v
}

func TestGraphViewReadsOnceMovesTheCursorAndChangesWindowOnRequest(t *testing.T) {
	v := graphViewFixture(t)
	fake := graphStory()
	reads := 0
	v.load = func(ctx context.Context, window time.Duration) *graphSnapshot {
		reads++
		return loadGraph(ctx, fake, &v.target, window, graphNow)
	}
	// The app is not running in a test, so apply what a read returns directly.
	v.active = true
	v.accept(v.load(t.Context(), v.window))
	require.Equal(t, 1, reads)
	frame := drawnText(t, v, 80, 20)
	require.Contains(t, frame, "Graph · Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h to 05:00Z · step 30s")
	require.Contains(t, frame, "Cursor 05:00:00Z (window end)")
	require.Contains(t, drawnText(t, v, 60, 20), "· 1h to 05:00Z · step 30s")

	left, _ := v.actions.Get(tcell.KeyLeft)
	for range 3 {
		require.Nil(t, left.Action(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone)))
	}
	require.Contains(t, drawnText(t, v, 80, 20), "Cursor 04:58:30Z, 1m30s before window end")
	require.Nil(t, left.Action(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModShift)))
	require.Contains(t, drawnText(t, v, 80, 20), "Cursor 04:53:30Z")
	home, _ := v.actions.Get(tcell.KeyHome)
	home.Action(nil)
	require.Contains(t, drawnText(t, v, 80, 20), "Cursor 04:00:00Z, 1h00m before window end")
	end, _ := v.actions.Get(tcell.KeyEnd)
	end.Action(nil)
	require.True(t, v.atEnd)
	require.Equal(t, 1, reads, "moving the cursor reads nothing")

	queries, _ := v.actions.Get(ui.KeyS)
	queries.Action(nil)
	// The report is wrapped to the view's width on plain text, so a query
	// is cut after a comma and its brackets are never taken for markup.
	report := v.text.GetText(true)
	require.Contains(t, report, "  sum by (pod) (max_over_time(container_memory_working_set_bytes{\n")
	require.Contains(t, report, "    namespace=\"apps\",pod=\"checkout-api-7d9f8c6b5d-x2x9q\",container!=\"\",\n    container!=\"POD\"}")
	require.Contains(t, strings.Join(strings.Fields(report), " "), "Each query is one bounded GET (8s, 32 series, 4096 samples). Nothing is polled; r reads again.")
	for _, line := range strings.Split(report, "\n") {
		require.LessOrEqual(t, runewidth.StringWidth(strings.ReplaceAll(line, "[]", "]")), 80, line)
	}
	require.Contains(t, report, "F, when shown, is the first rise of the restart counter inside the window.")
	queries.Action(nil)

	// A window key shows the new window's name only with its own data.
	v.load = func(context.Context, time.Duration) *graphSnapshot { return nil }
	v.window = 6 * time.Hour
	v.render()
	text := v.text.GetText(true)
	require.Contains(t, text, "last 6h")
	require.Contains(t, text, "Reading the declared Prometheus source once")
	require.NotContains(t, text, "MEMORY")
	v.accept(loadGraph(t.Context(), fake, &v.target, 6*time.Hour, graphNow))
	require.Contains(t, v.text.GetText(true), "last 6h to 05:00Z · step 5m")
}

func TestGraphViewDiscardsRepliesAfterItClosedOrTheDestinationChanged(t *testing.T) {
	v := graphViewFixture(t)
	release, done := make(chan struct{}), make(chan struct{})
	v.load = func(ctx context.Context, window time.Duration) *graphSnapshot {
		defer close(done)
		<-release
		return loadGraph(ctx, graphStory(), &v.target, window, graphNow)
	}
	v.Start()
	require.Equal(t, "Reading last 1h once…", v.busy)
	require.Contains(t, v.text.GetText(true), "Reading the declared Prometheus source once")
	v.Stop()
	close(release)
	<-done
	require.Nil(t, v.snapshot)

	// A changed destination refuses a new read instead of mixing contexts.
	v.active = true
	v.revision++
	v.load = func(context.Context, time.Duration) *graphSnapshot { t.Fatal("must not read"); return nil }
	v.refresh()
	require.Empty(t, v.busy)
}

func TestGraphIsRegisteredAsCommandPaletteEntryAndListKey(t *testing.T) {
	v := graphViewFixture(t)
	var found *ui.ActionDescriptor
	catalog := actionCatalog(v, v.app)
	for index := range catalog {
		if catalog[index].ID == "resource.graph" {
			found = &catalog[index]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, ":graph", found.Shortcut)
	require.Equal(t, ui.ActionInspect, found.Category)
	require.Empty(t, found.UnavailableReason)
	command := NewCommand(v.app)
	command.updateSuggestionAliases()
	require.Contains(t, command.suggestionAliases(), graphCommandToken)

	// Honest reasons: the wrong kind of selection, a Service without its
	// captured UID, and no declared source.
	configMap := SelectedResourceTarget{Context: v.target.Context, GVR: client.CmGVR, Namespace: "apps", Name: "checkout-api"}
	require.Contains(t, graphAvailability(v.app, configMap), "Select a Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Service or Node first")
	service := SelectedResourceTarget{Context: v.target.Context, GVR: client.SvcGVR, Namespace: "apps", Name: "checkout-api"}
	require.Contains(t, graphAvailability(v.app, service), "Select the Service in its list first")
	service.UID = "uid-checkout"
	require.Empty(t, graphAvailability(v.app, service), "a Service is graphed through the Pods behind it")
	ct, err := v.app.Config.CurrentContext()
	require.NoError(t, err)
	ct.Observability = observability.Sources{}
	require.Equal(t, "No Prometheus source is declared for this context; :sources shows what to add", graphAvailability(v.app, v.SelectedResource()))
	ct.Observability = observability.Sources{Grafana: &observability.GrafanaSource{Source: observability.Source{Service: "monitoring/grafana:3000"}, Datasource: "prom-main"}}
	require.Empty(t, graphAvailability(v.app, v.SelectedResource()), "a Grafana datasource can answer the queries")

	for _, text := range []string{"15m", "1h", "6h", "24h"} {
		_, ok := parseGraphWindow(text)
		require.True(t, ok, text)
	}
	_, ok := parseGraphWindow("7d")
	require.False(t, ok)
}

func TestGraphWrapKeepsEveryCharacterAndBrackets(t *testing.T) {
	query := `  count by (pod) (max_over_time(kube_pod_info{namespace="apps",created_by_kind="ReplicaSet",created_by_name=~"checkout-api-[a-z0-9]{5,10}"}[3600s]))`
	for _, width := range []int{120, 80, 60, 40} {
		lines := graphWrap(query, width, "  ")
		joined := ""
		for i, line := range lines {
			require.LessOrEqual(t, runewidth.StringWidth(line), width, line)
			if i > 0 {
				require.True(t, strings.HasPrefix(line, "    "), "continuation lines are indented: %q", line)
			}
			joined += strings.TrimSpace(line)
		}
		require.Equal(t, strings.ReplaceAll(strings.TrimSpace(query), " ", ""), strings.ReplaceAll(joined, " ", ""), "width %d", width)
		if width >= 60 {
			require.Contains(t, strings.Join(lines, "\n"), "[a-z0-9]", "width %d", width)
		}
	}
	// A list of Pod names breaks between names, never inside one.
	names := `  sum by (pod) (up{namespace="gitlab",pod=~"gitlab-webservice-default-6f9c7d5b84-2bq7k|gitlab-webservice-default-6f9c7d5b84-8wzpd|gitlab-webservice-default-6f9c7d5b84-hx4mt"})`
	for _, line := range graphWrap(names, 120, "  ")[1:] {
		require.True(t, strings.HasPrefix(strings.TrimSpace(line), "pod=~") || strings.HasPrefix(strings.TrimSpace(line), "gitlab-webservice-default-"), "a name was cut: %q", line)
	}
	require.Equal(t, []string{"one two three four", "five"}, graphWrap("one two three four five", 20, ""))
	require.Equal(t, []string{"a", "", "b"}, graphWrap("a\n\nb", 20, ""))
}

func TestGraphNarrowStatusKeepsTheChartCountAndNamesAPodWithoutSamples(t *testing.T) {
	dp := &graphTarget{Context: "ct-1-1", Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}
	pods := []string{"checkout-api-6c8d7f9b5-k4m2t", "checkout-api-7d9f8c6b5d-p8wm", "checkout-api-7d9f8c6b5d-x2x9q"}
	fake := &graphFake{pods: pods, rules: []graphRule{
		// The image of p8wm never pulled: it has no usage samples at all.
		{contains: "container_memory_working_set_bytes", by: []string{pods[0], pods[2]}, value: func(string, time.Time) (float64, bool) {
			return 200 * graphMi, true
		}},
	}}
	snapshot := loadGraph(t.Context(), fake, dp, time.Hour, graphNow)
	wide := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 120, Height: 30, Cursor: 120}), 120)
	require.Contains(t, wide, "MEMORY  a k4m2t 200Mi  b x2x9q 200Mi  c p8wm no data")
	for _, width := range []int{60, 50, 44} {
		text := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: width, Height: 20}), width)
		require.Regexp(t, `\d/3 charts ↑↓`, text, "width %d", width)
		require.Contains(t, text, "3 pods", "width %d", width)
	}
}

func TestGraphScrollNeverHidesAChartThatFits(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphStory(), graphPod, time.Hour, graphNow)
	page := &graphPage{Width: 120, Height: 40, First: 2}
	text := graphPlainLines(t, renderGraph(snapshot, page), 120)
	require.Zero(t, page.First, "all three charts fit, so none is scrolled away")
	require.Contains(t, text, "MEMORY")
	require.NotContains(t, text, "charts (")
}

func TestGraphLongWindowsEndWithinAMinuteOfTheRead(t *testing.T) {
	now := time.Date(2026, 11, 17, 6, 27, 23, 0, time.UTC)
	for _, window := range graphWindows {
		start, end, step := graphFrameTimes(now, window)
		require.LessOrEqual(t, now.Sub(end), time.Minute, "%s: the newest minutes are inside the window", graphSpan(window))
		require.False(t, end.After(now), "nothing is asked about the future")
		require.Equal(t, window, end.Sub(start))
		require.Zero(t, window%step)
	}
	_, end, _ := graphFrameTimes(now, 24*time.Hour)
	require.Equal(t, time.Date(2026, 11, 17, 6, 27, 0, 0, time.UTC), end)
}

func TestGraphLinesAreSolidAndLetteredInsteadOfDashed(t *testing.T) {
	dp := &graphTarget{Context: "ct-1-1", Kind: "Deployment", Namespace: "gitlab", Name: "gitlab-webservice-default"}
	pods := []string{"web-6f9c-2bq7k", "web-6f9c-8wzpd", "web-6f9c-hx4mt"}
	fake := &graphFake{pods: pods, rules: []graphRule{
		{contains: "container_cpu_usage_seconds_total", by: pods, value: func(pod string, at time.Time) (float64, bool) {
			// One Pod saws between idle and its limit; the others are level.
			if strings.HasSuffix(pod, "hx4mt") && at.Minute()%4 < 2 {
				return 1.5, true
			}
			return 0.4, true
		}},
	}}
	snapshot := loadGraph(t.Context(), fake, dp, time.Hour, graphNow)
	var cpu *graphPanel
	panels := snapshot.panels(graphColors{})
	for i := range panels {
		if panels[i].Spec.ID == "cpu" {
			cpu = &panels[i]
		}
	}
	require.NotNil(t, cpu)
	lines := 0
	for i := range cpu.Chart.Series {
		s := &cpu.Chart.Series[i]
		if s.Kind != chart.Line {
			continue
		}
		lines++
		require.Equal(t, chart.Solid, s.Stroke, "a line is one continuous stroke")
		require.Zero(t, s.Dash, "no line is told apart by leaving dots out")
		require.Equal(t, s.Key, s.Marker, "with several lines, each repeats its letter along the line")
		require.GreaterOrEqual(t, s.MarkerEvery, 12)
	}
	require.Equal(t, 3, lines)
	// A Pod's CPU chart has one line: solid, and no repeated letter.
	pod := &graphTarget{Context: "ct-1-1", Kind: "Pod", Namespace: "gitlab", Name: pods[2]}
	single := loadGraph(t.Context(), &graphFake{rules: []graphRule{
		{contains: "container_cpu_usage_seconds_total", value: func(string, time.Time) (float64, bool) { return 0.4, true }},
	}}, pod, time.Hour, graphNow)
	for _, panel := range single.panels(graphColors{}) {
		if panel.Spec.ID != "cpu" {
			continue
		}
		for i := range panel.Chart.Series {
			if s := &panel.Chart.Series[i]; s.Kind == chart.Line {
				require.Equal(t, chart.Solid, s.Stroke)
				require.Zero(t, s.Marker, "a single line needs no repeated letter")
			}
		}
	}

	// The sawing line is drawn without a break and carries its letter.
	text := graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 100, Height: 40, Cursor: 120}), 100)
	require.Contains(t, text, "CPU cores  a 2bq7k")
	require.Contains(t, text, "c hx4mt")
	plot := text[strings.Index(text, "CPU cores"):]
	require.GreaterOrEqual(t, strings.Count(plot[:strings.Index(plot, "NETWORK")], "c"), 3)
}
