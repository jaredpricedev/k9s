// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const graphGi = 1024 * graphMi

// compareSeries is one series the comparison fake answers with: it is
// returned for a range query whose text holds every word of match.
type compareSeries struct {
	match  []string
	labels map[string]string
	value  func(at time.Time) (float64, bool)
}

// compareFake is a Prometheus that knows the hang story: three Pods of one
// ReplicaSet and one of a newer one, of which hx4mt stalls for four
// minutes in the middle of the window.
type compareFake struct {
	mu     sync.Mutex
	asked  []string
	pods   map[string]graphPodMeta
	zones  map[string]string
	hashes map[string]string
	series []compareSeries
	// states answers a query holding the key with that state instead.
	states map[string]observability.State
}

func (*compareFake) Display() string { return "service monitoring/prometheus:9090" }

func (f *compareFake) result(e observability.Expr, start, end time.Time) observability.PromResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, e.String())
	res := observability.PromResult{Status: observability.Status{State: observability.StateComplete, ObservedAt: graphNow}, Query: e.String(), Start: start, End: end}
	for word, state := range f.states {
		if strings.Contains(res.Query, word) {
			res.State = state
			if state != observability.StateIncomplete {
				res.Reason = "HTTP 403"
			}
		}
	}
	return res
}

func (f *compareFake) Query(_ context.Context, e observability.Expr, at time.Time) observability.PromResult {
	res := f.result(e, at, at)
	if !res.OK() {
		return res
	}
	one := func(labels map[string]string) {
		res.Series = append(res.Series, observability.PromSeries{Labels: labels, Samples: []observability.Sample{{Timestamp: at, Value: 1}}})
	}
	switch {
	case strings.Contains(res.Query, "kube_pod_info"):
		for pod, meta := range f.pods {
			if strings.Contains(res.Query, "pod=~") && !strings.Contains(res.Query, pod) {
				continue
			}
			one(map[string]string{"pod": pod, "node": meta.Node, "created_by_kind": meta.OwnerKind, "created_by_name": meta.OwnerName})
		}
	case strings.Contains(res.Query, "kube_node_labels"):
		for node, zone := range f.zones {
			one(map[string]string{"node": node, graphZoneLabel: zone})
		}
	case strings.Contains(res.Query, "kube_pod_labels"):
		for pod, hash := range f.hashes {
			one(map[string]string{"pod": pod, graphRevisionLabel: hash})
		}
	}
	return res
}

func (f *compareFake) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	res := f.result(e, start, end)
	res.Step = observability.StepFor(end.Sub(start))
	if !res.OK() {
		return res
	}
	for _, spec := range f.series {
		matches := true
		for _, word := range spec.match {
			matches = matches && strings.Contains(res.Query, word)
		}
		if !matches {
			continue
		}
		series := observability.PromSeries{Labels: spec.labels}
		for at := start; !at.After(end); at = at.Add(res.Step) {
			if v, ok := spec.value(at); ok {
				series.Samples = append(series.Samples, observability.Sample{Timestamp: at, Value: v})
			}
		}
		if len(series.Samples) > 0 {
			res.Series = append(res.Series, series)
		}
	}
	return res
}

const (
	hangOld = "web-6f9c7d5b84"
	hangNew = "web-7c5d8e9f01"
)

var (
	hangPods   = []string{hangOld + "-2bq7k", hangOld + "-8wzpd", hangOld + "-hx4mt", hangNew + "-r5n9j"}
	hangTarget = &graphTarget{Context: "ct-1-1", Kind: "Deployment", Resource: client.DpGVR.String(), Namespace: "gitlab", Name: "web"}
)

// hangStall is true inside the four minutes hx4mt hangs: 30 to 26 minutes
// before the window end.
func hangStall(at time.Time) bool {
	end := graphNow.Truncate(30 * time.Second)
	return !at.Before(end.Add(-30*time.Minute)) && at.Before(end.Add(-26*time.Minute))
}

func hangStory() *compareFake {
	f := &compareFake{
		pods: map[string]graphPodMeta{
			hangPods[0]: {Node: "worker-d", OwnerKind: graphReplicaSet, OwnerName: hangOld},
			hangPods[1]: {Node: "worker-e", OwnerKind: graphReplicaSet, OwnerName: hangOld},
			hangPods[2]: {Node: "worker-f", OwnerKind: graphReplicaSet, OwnerName: hangOld},
			hangPods[3]: {Node: "worker-d", OwnerKind: graphReplicaSet, OwnerName: hangNew},
		},
		zones: map[string]string{"worker-d": "zone-a", "worker-e": "zone-b", "worker-f": "zone-c"},
	}
	flat := func(v float64) func(time.Time) (float64, bool) {
		return func(time.Time) (float64, bool) { return v, true }
	}
	for i, pod := range hangPods {
		slow := strings.HasSuffix(pod, "hx4mt")
		phases := func(normal, stalled float64) func(time.Time) (float64, bool) {
			return func(at time.Time) (float64, bool) {
				if slow && hangStall(at) {
					return stalled, true
				}
				return normal, true
			}
		}
		labels := map[string]string{"pod": pod}
		add := func(value func(time.Time) (float64, bool), match ...string) {
			f.series = append(f.series, compareSeries{match: match, labels: labels, value: value})
		}
		wobble := 0.001 * float64(i)
		add(phases(0.012+wobble, 0.86), "by (pod) (rate(container_cpu_cfs_throttled_periods_total")
		add(phases(0.35+10*wobble, 1.47), "sum by (pod) (rate(container_cpu_usage_seconds_total")
		add(flat((1.9+0.01*float64(i))*graphGi), "by (pod) (max_over_time(container_memory_working_set_bytes")
		add(flat(3*graphGi), "by (pod) (kube_pod_container_resource_limits", `resource="memory"`)
		add(flat(1.5), "by (pod) (kube_pod_container_resource_limits", `resource="cpu"`)
		add(flat(0), "sum by (pod) (kube_pod_container_status_restarts_total")
		add(flat(350e3), "container_network_receive_bytes_total")
		add(phases(0.35, 48), "histogram_quantile(0.99", "http_request_duration_seconds_bucket", "by (le,pod)")
		add(phases(0, 14), "by (pod) (puma_queued_connections")
		// Per container: the Rails container carries the load, the proxy
		// beside it does not.
		for _, c := range []struct {
			name            string
			normal, stalled float64
		}{{"webservice", 0.32 + 10*wobble, 1.44}, {"gitlab-workhorse", 0.03, 0.03}} {
			f.series = append(f.series, compareSeries{match: []string{"sum by (pod,container) (rate(container_cpu_usage_seconds_total"},
				labels: map[string]string{"pod": pod, "container": c.name}, value: phases(c.normal, c.stalled)})
		}
	}
	return f
}

// hangSources declares the request path the fixture declares: the GitLab
// profile for the app hop of the Service in front of the Pods.
func hangSources() *observability.Sources {
	return &observability.Sources{
		Prometheus: &observability.Source{Service: "monitoring/prometheus:9090"},
		RequestPath: &observability.RequestPath{Services: map[string]observability.ServicePath{
			"gitlab/web": {App: []string{"gitlab-webservice", graphCPUProfile}},
		}},
	}
}

func hangCompare(t *testing.T, fake *compareFake) (*graphSnapshot, *graphCompare) {
	t.Helper()
	base := loadGraph(t.Context(), fake, hangTarget, time.Hour, graphNow)
	require.Nil(t, base.failure())
	base.Revisions = map[string]int64{hangOld: 1, hangNew: 2}
	path := graphServicePath{Path: "gitlab/web", Note: "its selector matches the Pod template and it has a requestPath declaration"}
	return base, loadGraphCompare(t.Context(), fake, base, hangSources(), path, graphNow)
}

func comparePage(width, height int) *graphPage {
	return &graphPage{Width: width, Height: height, Cursor: 1 << 20, Colors: graphPalette(config.DefaultSemanticPalette())}
}

func TestGraphCompareAsksEverySignalOncePerPodWithFixedTemplates(t *testing.T) {
	fake := hangStory()
	base, c := hangCompare(t, fake)
	before := len(fake.asked)
	require.Len(t, base.Pods, 4)
	require.Equal(t, graphPodMeta{Node: "worker-f", OwnerKind: graphReplicaSet, OwnerName: hangOld}, base.Meta[hangPods[2]])

	var names []string
	for i := range c.Signals {
		names = append(names, c.Signals[i].Name)
		require.NoError(t, c.Signals[i].ByPod.Err(), c.Signals[i].Name)
	}
	// cAdvisor and kube-state-metrics first, then what the declared app-hop
	// profile provides, the network last. The profile's own CPU signals are
	// not asked twice, and signals without a worse side are left out.
	require.Equal(t, []string{"throttled", "cpu", "memory", "restarts", "p99", "5xx", "queued", "free-threads", "busy-threads", "network"}, names)
	require.Equal(t, []string{"gitlab-webservice"}, c.Profiles)

	pods := `pod=~"web-6f9c7d5b84-2bq7k|web-6f9c7d5b84-8wzpd|web-6f9c7d5b84-hx4mt|web-7c5d8e9f01-r5n9j"`
	for key, want := range map[string]string{
		graphCompareKey("throttled", graphPartPod): `(sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{namespace="gitlab",` + pods + `,container!="",container!="POD"}[120s])) / ` +
			`sum by (pod) (rate(container_cpu_cfs_periods_total{namespace="gitlab",` + pods + `,container!="",container!="POD"}[120s])))`,
		graphCompareKey("memory", graphPartLimit):                 `sum by (pod) (kube_pod_container_resource_limits{namespace="gitlab",` + pods + `,resource="memory"})`,
		graphCompareKey("gitlab-webservice/p99", graphPartPod):    `histogram_quantile(0.99, sum by (le,pod) (rate(http_request_duration_seconds_bucket{namespace="gitlab",` + pods + `}[120s])))`,
		graphCompareKey("gitlab-webservice/queued", graphPartPod): `sum by (pod) (puma_queued_connections{namespace="gitlab",` + pods + `})`,
		graphLookupZones: `max by (node,label_topology_kubernetes_io_zone) (kube_node_labels{node=~"worker-d|worker-e|worker-f"})`,
	} {
		require.Equal(t, want, c.Results[key].Query, key)
	}
	// Ten signals, two limits and the zone lookup; every Pod has a
	// ReplicaSet owner, so no controller-revision lookup is needed. The
	// per-Pod memory, CPU and restart series of the charts are not asked again.
	require.Len(t, c.Results, 13)
	require.Equal(t, 3, c.Reused)
	seen := make(map[string]bool)
	for _, query := range fake.asked {
		require.False(t, seen[query], "asked twice: %s", query)
		seen[query] = true
		require.NotContains(t, query, "by (pod,container)", "per-container series are read only when regrouped by container")
	}

	// Regrouping by container adds the four per-container reads, once.
	with := loadGraphContainers(t.Context(), fake, c)
	require.True(t, with.Containers)
	require.False(t, c.Containers, "the earlier read is not changed")
	require.Len(t, fake.asked, before+4)
	require.Equal(t, `sum by (pod,container) (rate(container_cpu_usage_seconds_total{namespace="gitlab",`+pods+`,container!="",container!="POD"}[120s]))`,
		with.Results[graphCompareKey("cpu", graphPartContainer)].Query)

	// A name from the cluster is only ever a matcher value, and escaped.
	odd := graphFixedSignals("gitlab", []string{`web.1`, `a"b`}, 30*time.Second)
	require.Contains(t, odd[1].ByPod.String(), `pod=~"web\\.1|a\"b"`)
}

func TestGraphCompareRanksWorstFirstAndMarksTheOutlier(t *testing.T) {
	_, c := hangCompare(t, hangStory())
	for _, tc := range []struct {
		signal   string
		order    []string
		outliers []string
		state    string
		rank     string
	}{
		{signal: "throttled", order: []string{"hx4mt", "r5n9j", "8wzpd", "2bq7k"}, outliers: []string{"hx4mt"}, rank: "86%"},
		{signal: "cpu", order: []string{"hx4mt", "r5n9j", "8wzpd", "2bq7k"}, outliers: []string{"hx4mt"}, rank: "1.47"},
		// Every Pod has a limit, so memory ranks by its share of it; the
		// Pods are within a few percent of each other and none stands out.
		{signal: "memory", order: []string{"r5n9j", "hx4mt", "8wzpd", "2bq7k"}, rank: "64.3%"},
		// Equal values keep the order of the names.
		{signal: "restarts", order: []string{"2bq7k", "8wzpd", "hx4mt", "r5n9j"}, rank: "+0"},
		{signal: "p99", order: []string{"hx4mt", "2bq7k", "8wzpd", "r5n9j"}, outliers: []string{"hx4mt"}, rank: "48s"},
		{signal: "queued", order: []string{"hx4mt", "2bq7k", "8wzpd", "r5n9j"}, outliers: []string{"hx4mt"}, rank: "14"},
		// Answered, matched nothing: missing data, never a row of zeros.
		{signal: "5xx", state: graphNoData},
		{signal: "free-threads", state: graphNoData},
	} {
		sig, _ := c.signal(tc.signal)
		require.Equal(t, tc.signal, sig.Name)
		set := c.series(sig, false)
		require.Equal(t, tc.state, set.State, tc.signal)
		if tc.state != "" {
			require.Contains(t, set.Reason, "missing data, not a reading of zero", tc.signal)
			require.Empty(t, set.Members)
			continue
		}
		var order, outliers []string
		for i := range set.Members {
			order = append(order, set.Members[i].Name)
			if set.Members[i].Outlier {
				outliers = append(outliers, set.Members[i].Name)
			}
		}
		require.Equal(t, tc.order, order, tc.signal)
		require.Equal(t, tc.outliers, outliers, tc.signal)
		first := &set.Members[0]
		_, _, rank := graphValueWords(sig, set.Shares, first.Value, first.Peak, first.Last, first.Share)
		require.Equal(t, tc.rank, rank, tc.signal)
	}

	standing := c.standing()
	require.Equal(t, []string{"hx4mt"}, standing.Pods)
	require.Equal(t, []string{"throttled", "cpu", "p99", "queued"}, standing.Signals["hx4mt"])
	require.Equal(t, 7, standing.Read)
	require.Equal(t, "throttled", standing.first(c), "a page opens on the first signal in which a Pod differs")
	for width, want := range map[int]string{
		140: "* hx4mt differs from its peers in 4 of 7 signals read (3 of 10 had no reading): throttled, cpu, p99, queued · a difference, not a cause",
		100: "* hx4mt differs in 4 of 7 signals: throttled, cpu, p99, queued · a difference, not a cause",
		80:  "* hx4mt differs in 4 of 7 signals · a difference, not a cause",
		60:  "* hx4mt differs in 4 of 7 · a difference, not a cause",
		40:  "* hx4mt differs in 4/7 · not a cause",
	} {
		text, flagged := graphStandingLine(&standing, 4, width)
		require.True(t, flagged)
		require.Equal(t, want, text, "width %d keeps the caveat", width)
	}
}

func TestGraphCompareGroupsFoldPodsAndNameWhatIsNotKnown(t *testing.T) {
	fake := hangStory()
	// One Node has no zone label, and one Pod's owner is not known.
	delete(fake.zones, "worker-e")
	fake.pods[hangPods[0]] = graphPodMeta{Node: "worker-d"}
	_, c := hangCompare(t, fake)
	c = loadGraphContainers(t.Context(), fake, c)
	sig, _ := c.signal("cpu")
	for _, tc := range []struct {
		by      graphGrouping
		groups  []string // title, members, outlier names; the unknown group last
		line    string
		unknown []string
	}{
		{by: graphByNode, groups: []string{"worker-f 1 hx4mt", "worker-d 2", "worker-e 1"},
			line: "By node (kube_pod_info): worker-f 1 pod, worker-d 2, worker-e 1 · all placed"},
		{by: graphByZone, groups: []string{"zone-c 1 hx4mt", "zone-a 2", "zone not known 1"}, unknown: []string{"8wzpd"},
			line: "By zone (kube_node_labels): zone-c 1 pod, zone-a 2 · zone not known for 8wzpd (kube_node_labels names no zone for their Nodes)"},
		{by: graphByRevision, groups: []string{"rev 1 6f9c7d5b84 2 hx4mt", "rev 2 7c5d8e9f01 1", "revision not known 1"}, unknown: []string{"2bq7k"},
			line: "By revision (the owner in kube_pod_info): rev 1 6f9c7d5b84 2 pods, rev 2 7c5d8e9f01 1 · revision not known for 2bq7k " +
				"(no ReplicaSet owner in kube_pod_info and no controller-revision-hash in kube_pod_labels)"},
		// A container is compared with the same container of the other Pods.
		{by: graphByContainer, groups: []string{"webservice 4 hx4mt/webservice", "gitlab-workhorse 4"},
			line: "By container (the container label of each series): webservice in 4 pods, gitlab-workhorse 4 · all placed"},
	} {
		set := c.series(sig, tc.by == graphByContainer)
		require.Empty(t, set.State, tc.by)
		groups := c.groups(&set, tc.by)
		var got, unknown []string
		for i := range groups {
			g := &groups[i]
			got = append(got, strings.TrimSpace(fmt.Sprintf("%s %d %s", g.Title, len(g.Members), strings.Join(g.Outliers, ","))))
			if g.Unknown {
				require.Equal(t, len(groups)-1, i, "the group that could not be placed is last")
				for j := range g.Members {
					unknown = append(unknown, g.Members[j].Name)
				}
			}
			// The band holds every sample time once, lowest to highest.
			require.Len(t, g.Mid, 121, g.Title)
			for k := range g.Mid {
				require.LessOrEqual(t, g.Low[k].V, g.Mid[k].V)
				require.LessOrEqual(t, g.Mid[k].V, g.High[k].V)
			}
		}
		require.Equal(t, tc.groups, got, tc.by)
		require.Equal(t, tc.unknown, unknown, tc.by)
		require.Equal(t, tc.line, graphGroupingLine(c, groups, tc.by), tc.by)
	}

	// The median of a group of two is between its members; the maximum is
	// the stalled Pod.
	set := c.series(sig, false)
	rev1 := c.groups(&set, graphByRevision)[0]
	stall := graphNow.Truncate(30 * time.Second).Add(-28 * time.Minute)
	for k := range rev1.Mid {
		if rev1.Mid[k].T.Equal(stall) {
			require.InDelta(t, 1.47, rev1.High[k].V, 1e-9)
			require.InDelta(t, 0.36, rev1.Low[k].V, 1e-9)
			require.InDelta(t, (1.47+0.36)/2, rev1.Mid[k].V, 1e-9)
		}
	}

	// A signal without a container label says so instead of drawing Pods
	// under a container heading.
	p99, _ := c.signal("p99")
	perPod := c.series(p99, true)
	require.Equal(t, "per Pod only", perPod.State)
	require.Contains(t, perPod.Reason, "Signals that can: throttled, cpu, memory, restarts.")
}

func TestGraphComparePageFitsEverySizeAndCountsWhatIsNotShown(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	stalled := int(base.Window/base.Step) - 56 // 28 minutes before the window end
	for _, tc := range []struct {
		name          string
		width, height int
		ascii         bool
		grouping      graphGrouping
		cursor        int
		want, not     []string
	}{
		{name: "wide: one row of charts, then every signal", width: 132, height: 33, cursor: stalled, want: []string{
			"Graph · Deployment gitlab/web · last 1h to 05:00Z · step 30s",
			"read 05:00:07Z · 13 of 13 queries answered · 3 no data · 4 pods",
			"[throttled*]  cpu*  memory  restarts  p99*  5xx (no data)  queued*  free-threads (no data)  busy-threads (no data)  network",
			"* hx4mt differs in 4 of 7 signals: throttled, cpu, p99, queued · a difference, not a cause",
			"CPU THROTTLED by pod · worst first by peak · share of CPU periods in which a container was held at its limit, rate over 2m",
			"hx4mt      OUTLIER · peak 86%", "r5n9j               peak 1.5%", "100% ┤", " 50% ┤", "hx4mt · worker-f",
			"EVERY SIGNAL · one row per signal, each on its own scale", "> throttled*", "  p99*", " 86%*", " 48s*",
			"  5xx           no data: not compared (missing data, not a reading of zero)",
			"Cursor 04:32:00Z, 28m before window end · hx4mt 86% · r5n9j 1.5% · 8wzpd 1.3% · 2bq7k 1.2%",
			"n/N signal · g/G regroup · p overlaid lines",
		}, not: []string{"pods 1-"}},
		{name: "80x24 keeps two by two", width: 80, height: 20, cursor: stalled, want: []string{
			"13 of 13 queries answered · 3 no data · 4 pods",
			"[throttled*]  cpu*  memory  restarts  p99*  5xx (no data)  queued*  +3 more",
			"* hx4mt differs in 4 of 7 signals · a difference, not a cause",
			"hx4mt · worker-f ", " OUTLIER · 86%  ", "r5n9j · worker-d ", " 1.5%\n", "8wzpd · worker-e", "2bq7k · worker-d",
			"Cursor 04:32:00Z, -28m · hx4mt 86% · r5n9j 1.5% · 8wzpd 1.3% · 2bq7k 1.2%",
		}, not: []string{"EVERY SIGNAL", "POD  "}},
		{name: "60 columns is a ranked list", width: 60, height: 20, cursor: stalled, want: []string{
			"* hx4mt differs in 4 of 7 · a difference, not a cause",
			"POD   NODE     LAST 1h", "PEAK AT 04:32",
			"  86%      86% OUTLIER", "r5n9j worker-d", " 1.5%     1.5%",
			"EVERY SIGNAL", "WORST POD VALUE   MEDIAN OF ALL", "> throttled*", "hx4mt     86%     1.4%    OUTLIER",
			"more signals: n and N step through all 10",
			"Cursor 04:32:00Z, -28m",
		}, not: []string{"┤", "[throttled*]"}},
		{name: "plain characters", width: 80, height: 20, ascii: true, cursor: stalled, want: []string{
			"hx4mt · worker-f ", " OUTLIER · 86%  ", "100% +", "Left/Right cursor",
		}},
		{name: "by node at 80", width: 80, height: 20, grouping: graphByNode, cursor: stalled, want: []string{
			"CPU THROTTLED by node · area median, lines min and max · worst first by peak · …",
			"worker-f · 1 pod ", " OUTLIER · 86%  ", "worker-d · 2 pods", "worker-e · 1 pod",
			"Cursor 04:32:00Z, -28m · medians: worker-f 86% · worker-d 1.35% · worker-e 1.3%",
			"By node (kube_pod_info): worker-f 1 pod, worker-d 2, worker-e 1 · all placed",
		}},
	} {
		page := comparePage(tc.width, tc.height)
		page.ASCII, page.Grouping, page.Cursor = tc.ascii, tc.grouping, tc.cursor
		lines := renderGraphCompare(base, c, page)
		text := graphPlainLines(t, lines, tc.width)
		require.LessOrEqual(t, len(lines), tc.height, tc.name)
		for _, want := range tc.want {
			require.Contains(t, text, want, tc.name)
		}
		for _, not := range tc.not {
			require.NotContains(t, text, not, tc.name)
		}
		if tc.ascii {
			for _, r := range text {
				require.True(t, r < 128 || r == '·', "%s: %q is neither ASCII nor the shared separator", tc.name, r)
			}
		}
		require.Equal(t, "throttled", page.Signal, "%s: the page settles the signal it opened on", tc.name)
	}
}

func TestGraphComparePagesThroughPodsThatDoNotFit(t *testing.T) {
	fake := hangStory()
	fake.series = nil
	for i := range 14 {
		pod := fmt.Sprintf("%s-p%02d", hangOld, i)
		fake.pods[pod] = graphPodMeta{Node: fmt.Sprintf("worker-%d", i%3), OwnerKind: graphReplicaSet, OwnerName: hangOld}
		level := 0.01 * float64(i+1)
		fake.series = append(fake.series, compareSeries{match: []string{"by (pod) (rate(container_cpu_cfs_throttled_periods_total"},
			labels: map[string]string{"pod": pod}, value: func(time.Time) (float64, bool) { return level, true }})
	}
	for _, pod := range hangPods {
		delete(fake.pods, pod)
	}
	base, c := hangCompare(t, fake)
	require.Len(t, base.Pods, 14)

	page := comparePage(80, 20)
	first := graphPlainLines(t, renderGraphCompare(base, c, page), 80)
	require.Equal(t, 4, page.Shown)
	require.Contains(t, first, "14 pods · 1-4 of 14 pods ↑↓")
	require.Contains(t, first, "p13 · worker-1", "the worst Pod is on the first page")
	require.NotContains(t, first, "p00 · worker-0", "the best Pod is charted on the last page")
	require.Contains(t, first, "p01 2% · p00 1%", "the readout still lists every Pod at the cursor")
	// The scale is that of all fourteen, on every page.
	require.Contains(t, first, "20% ┤")

	page = comparePage(80, 20)
	page.First = 12
	last := graphPlainLines(t, renderGraphCompare(base, c, page), 80)
	require.Contains(t, last, "13-14 of 14 pods ↑↓")
	require.Contains(t, last, "p00 · worker-0")
	require.Contains(t, last, "20% ┤")

	// A readout without room for every Pod counts the ones it leaves out.
	page = comparePage(72, 20)
	tight := graphPlainLines(t, renderGraphCompare(base, c, page), 72)
	require.Regexp(t, `p0\d \d% · \+\d more\n`, tight)

	// Past the last page the layout settles on it.
	page = comparePage(80, 20)
	page.First = 400
	renderGraphCompare(base, c, page)
	require.Equal(t, 12, page.First)

	// At 60 columns every Pod is a row of the list; the rest are counted.
	page = comparePage(60, 20)
	narrow := graphPlainLines(t, renderGraphCompare(base, c, page), 60)
	require.Contains(t, narrow, fmt.Sprintf("14 pods · pods 1-%d of 14 (↑/↓ pages)", page.Shown), "the status takes a second line rather than lose the count")
	require.Greater(t, page.Shown, 8)
}

func TestGraphCompareNeverReadsMissingDataAsHealth(t *testing.T) {
	// Fewer than three Pods are never compared.
	fake := hangStory()
	delete(fake.pods, hangPods[0])
	delete(fake.pods, hangPods[3])
	base, c := hangCompare(t, fake)
	text := graphPlainLines(t, renderGraphCompare(base, c, comparePage(100, 30)), 100)
	require.Contains(t, text, "2 Pods: too few peers to call one different (3 are needed)")
	require.NotContains(t, text, graphOutlierWord)

	// A Pod that was asked for and has no series is still on the page.
	fake = hangStory()
	fake.series = fake.series[11:] // the first Pod has no series at all
	base, c = hangCompare(t, fake)
	text = graphPlainLines(t, renderGraphCompare(base, c, comparePage(132, 33)), 132)
	require.Contains(t, text, "2bq7k · worker-d")
	require.Contains(t, text, "no samples")
	require.Contains(t, text, "2bq7k –")

	// A denied read is named; the signal is not drawn and not called fine.
	fake = hangStory()
	fake.states = map[string]observability.State{"container_cpu_cfs_throttled": observability.StateDenied}
	base, c = hangCompare(t, fake)
	page := comparePage(100, 30)
	page.Signal = "throttled"
	text = graphPlainLines(t, renderGraphCompare(base, c, page), 100)
	for _, want := range []string{
		"12 of 13 queries answered, 1 denied", "[throttled (denied)]", "CPU THROTTLED by pod · denied",
		"The read of throttled did not succeed (denied: HTTP 403). Nothing is drawn; that says nothing",
		"* hx4mt differs in 3 of 6 signals: cpu, p99, queued · a difference, not a cause",
	} {
		require.Contains(t, text, want)
	}
	// With nothing chosen the page opens on a signal that has samples.
	page = comparePage(100, 30)
	renderGraphCompare(base, c, page)
	require.Equal(t, "cpu", page.Signal)

	// An answer the source cut at its series or sample limit says so.
	fake = hangStory()
	fake.states = map[string]observability.State{"container_cpu_cfs_throttled": observability.StateIncomplete}
	base, c = hangCompare(t, fake)
	text = graphPlainLines(t, renderGraphCompare(base, c, comparePage(132, 33)), 132)
	require.Contains(t, text, "1 cut at the series or sample limit")
	require.Contains(t, text, "CPU THROTTLED by pod · CUT at the source's series or sample limit")
	require.Contains(t, graphCompareReport(c), "CUT: the source's series or sample limit folded this answer; Pods may be missing")

	// Before the per-Pod read arrives nothing of an older read is shown.
	text = graphPlainLines(t, renderGraphCompare(base, nil, comparePage(100, 30)), 100)
	require.Contains(t, text, "The per-Pod series of this window have not been read.")
	require.NotContains(t, text, "hx4mt")
}

func TestGraphSparkKeepsTheWorstSampleOfACellAndLeavesHolesEmpty(t *testing.T) {
	start := graphNow.Truncate(time.Minute).Add(-time.Hour)
	frame := chart.Frame{Start: start, End: start.Add(time.Hour), Step: 30 * time.Second, Width: 10}
	var points []chart.Point
	for i := 20; i <= 100; i++ {
		if i >= 48 && i < 60 {
			continue // six minutes without a sample
		}
		v := 4.0
		if i == 30 {
			v = 0 // one sample at zero among eleven fours in its cell
		}
		points = append(points, chart.Point{T: start.Add(time.Duration(i) * 30 * time.Second), V: v})
	}
	for _, tc := range []struct {
		name  string
		low   bool
		style chart.Style
		want  string
	}{
		// The series begins ten minutes in and ends ten minutes early:
		// those cells are empty. The hole inside is the gap glyph.
		{name: "a peak signal keeps the largest sample", want: " ███×████ "},
		{name: "a signal that is worse when low keeps the smallest", low: true, want: " █▁█×████ "},
		// The braille style draws the same cells as a thin line.
		{name: "dots: a peak signal keeps the largest sample", style: chart.Dots, want: " ⠉⠉⠉×⠉⠉⠉⠉ "},
		{name: "dots: a signal that is worse when low keeps the smallest", style: chart.Dots, low: true, want: " ⢹⣸⠉×⠉⠉⠉⠉ "},
	} {
		require.Equal(t, tc.want, graphSpark(points, frame, 4, "", "", chart.Options{Style: tc.style}, tc.low), tc.name)
	}
	for _, o := range []chart.Options{{}, {Style: chart.Dots}, {ASCII: true}} {
		require.Equal(t, strings.Repeat(" ", 10), graphSpark(nil, frame, 4, "", "", o, false), "no samples draw nothing, never a level")
	}
	require.Equal(t, " ^_^x^^^^ ", graphSpark(points, frame, 4, "", "", chart.Options{ASCII: true, Style: chart.Dots}, true), "noIcons keeps its plain characters in either style")
}

func TestGraphCompareReportNamesEveryQueryAndTheRule(t *testing.T) {
	_, c := hangCompare(t, hangStory())
	report := graphCompareReport(c)
	for _, want := range []string{
		"PER-POD COMPARISON (p)",
		"Request path of Service gitlab/web · its selector matches the Pod template and it has a requestPath declaration",
		"App-hop profiles that supplied signals: gitlab-webservice",
		"P99 per Pod · Rails request duration, 99th percentile, gitlab-webservice profile",
		"histogram_quantile(0.99, sum by (le,pod) (rate(http_request_duration_seconds_bucket{",
		"complete · 0 series · 0 samples · answered, matched nothing: missing data, not zero",
		"ZONE OF EACH NODE (instant query at the window end)",
		"a modified z-score of at least 3.5 on the median absolute deviation",
		"It does not say why, and a Pod that is not marked is not thereby healthy.",
	} {
		require.Contains(t, report, want)
	}
}

// compareViewFixture is a graph view over the hang story. The app is not
// running in a test, so a read started by a key only counts itself; deliver
// then applies what that read would have returned.
func compareViewFixture(t *testing.T, target *graphTarget) (v *graphView, fake *compareFake, reads *atomic.Int32, deliver func()) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	declareStudySources(t, app)
	copied := *target
	copied.Context = app.Config.ActiveContextName()
	v = newGraphView(app, &copied, 0)
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	fake, reads = hangStory(), new(atomic.Int32)
	v.load = func(ctx context.Context, window time.Duration) *graphSnapshot {
		return loadGraph(ctx, fake, &v.target, window, graphNow)
	}
	v.loadCompare = func(context.Context, *graphSnapshot, *graphCompare) *graphCompare {
		reads.Add(1)
		return nil
	}
	delivered := int32(0)
	deliver = func() {
		t.Helper()
		delivered++
		require.Eventually(t, func() bool { return reads.Load() == delivered }, 5*time.Second, 2*time.Millisecond, "one read per request")
		if v.compare != nil && v.compare.Base == v.snapshot {
			v.acceptCompare(loadGraphContainers(t.Context(), fake, v.compare))
			return
		}
		v.acceptCompare(loadGraphCompare(t.Context(), fake, v.snapshot, hangSources(), graphServicePath{Path: "gitlab/web"}, graphNow))
	}
	v.active = true
	v.accept(v.load(t.Context(), v.window))
	return v, fake, reads, deliver
}

func TestGraphViewComparisonKeysReadOnRequestOnly(t *testing.T) {
	v, fake, reads, deliver := compareViewFixture(t, hangTarget)
	key := func(k tcell.Key) {
		action, ok := v.actions.Get(k)
		require.True(t, ok)
		action.Action(tcell.NewEventKey(k, 0, tcell.ModNone))
	}
	asked := len(fake.asked)
	require.Contains(t, drawnText(t, v, 100, 24), "p per pod · g regroup")
	require.Zero(t, reads.Load(), "the overlaid charts read nothing per Pod")

	key(ui.KeyP)
	require.Equal(t, "Reading every signal once per Pod…", v.busy)
	require.Contains(t, drawnText(t, v, 132, 33), "Reading every signal once per Pod")
	deliver()
	require.Empty(t, v.busy)
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, frame, "CPU THROTTLED by pod")
	require.Contains(t, frame, "hx4mt · worker-f")
	require.Len(t, fake.asked, asked+10, "the three per-Pod series the charts already hold are not asked again")

	// n and N cycle the signal, the cursor moves across every chart, and
	// neither reads anything.
	key(ui.KeyN)
	require.Contains(t, drawnText(t, v, 132, 33), "CPU by pod")
	key(ui.KeyShiftN)
	key(ui.KeyShiftN)
	require.Contains(t, drawnText(t, v, 132, 33), "NETWORK by pod")
	key(ui.KeyN)
	left, _ := v.actions.Get(tcell.KeyLeft)
	for range 6 {
		left.Action(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModShift))
	}
	require.Contains(t, drawnText(t, v, 132, 33), "Cursor 04:30:00Z, 30m before window end · hx4mt 86%")
	require.EqualValues(t, 1, reads.Load())

	// g regroups; by container asks the per-container series once.
	key(ui.KeyG)
	require.Equal(t, "Reading the per-container series once…", v.busy)
	deliver()
	require.True(t, v.compare.Containers)
	key(ui.KeyN)
	require.Contains(t, drawnText(t, v, 132, 33), "CPU by container")
	require.Contains(t, drawnText(t, v, 132, 33), "webservice · 4 pods")
	key(ui.KeyG)
	require.Contains(t, drawnText(t, v, 132, 33), "CPU by revision")
	key(ui.KeyShiftG)
	key(ui.KeyShiftG)
	require.Contains(t, drawnText(t, v, 132, 33), "CPU by pod")
	require.EqualValues(t, 2, reads.Load(), "a grouping that was read is not read again")

	// s lists the per-Pod queries too; p returns to the overlaid lines.
	queries, _ := v.actions.Get(ui.KeyS)
	queries.Action(nil)
	require.Contains(t, v.text.GetText(true), "PER-POD COMPARISON (p)")
	// While the report is open g stays with the text (its top), and
	// regroups nothing.
	regroup, _ := v.actions.Get(ui.KeyG)
	typed := tcell.NewEventKey(tcell.KeyRune, 'g', tcell.ModNone)
	require.Equal(t, typed, regroup.Action(typed))
	require.Equal(t, graphByPod, v.grouping)
	queries.Action(nil)
	key(ui.KeyP)
	require.Contains(t, drawnText(t, v, 132, 33), "MEMORY  a ")

	// The header names the three keys only where there are Pods to compare.
	for k, want := range map[tcell.Key]string{ui.KeyP: "Per pod", ui.KeyN: "Next signal", ui.KeyG: "Regroup"} {
		action, _ := v.actions.Get(k)
		require.Equal(t, want, action.Description)
		require.True(t, action.Opts.Visible)
		require.Positive(t, action.Opts.Priority)
	}

	pod, _, podReads, _ := compareViewFixture(t, graphPod)
	for _, k := range []tcell.Key{ui.KeyP, ui.KeyG, ui.KeyN} {
		action, ok := pod.actions.Get(k)
		require.True(t, ok)
		require.False(t, action.Opts.Visible, "one Pod has no peers, so the key is not advertised")
		action.Action(tcell.NewEventKey(k, 0, tcell.ModNone))
	}
	require.False(t, pod.comparing)
	require.Zero(t, podReads.Load())
}

func TestGraphPodsCommandAndPaletteEntrySayWhenThereAreNoPeers(t *testing.T) {
	v, _, _, _ := compareViewFixture(t, hangTarget)
	find := func(owner actionOwner) *ui.ActionDescriptor {
		catalog := actionCatalog(owner, v.app)
		for i := range catalog {
			if catalog[i].ID == "resource.graph-pods" {
				return &catalog[i]
			}
		}
		return nil
	}
	entry := find(v)
	require.NotNil(t, entry)
	require.Equal(t, ":graph pods", entry.Shortcut)
	require.Equal(t, "Per-Pod graphs: which Pod differs (p)", entry.Label, "the key that shows the page here is named on the command")
	require.Empty(t, entry.UnavailableReason)

	pod, _, _, _ := compareViewFixture(t, graphPod)
	require.Equal(t, graphNoPeers, find(pod).UnavailableReason)
}

func TestGraphServiceIsGraphedThroughThePodsBehindIt(t *testing.T) {
	now := graphNow
	object := func(apiVersion, kind, name, uid string, mutate func(*unstructured.Unstructured)) *unstructured.Unstructured {
		u := graphObject(apiVersion, kind, name, now.Add(-time.Hour), mutate)
		u.SetNamespace("gitlab")
		u.SetUID(types.UID("uid-" + uid))
		return u
	}
	selector := map[string]any{"app": "webservice"}
	podObject := func(name string) *unstructured.Unstructured {
		return object("v1", "Pod", name, name, func(u *unstructured.Unstructured) { u.SetLabels(map[string]string{"app": "webservice"}) })
	}
	kinds := map[schema.GroupVersionResource]string{
		client.SvcGVR.GVR(): "ServiceList", client.PodGVR.GVR(): "PodList", client.DpGVR.GVR(): "DeploymentList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}:      "EndpointSliceList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}:          "IngressList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}:    "NetworkPolicyList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
	}
	dial := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds,
		object("v1", "Service", "web", "web", func(u *unstructured.Unstructured) { u.Object["spec"] = map[string]any{"selector": selector} }),
		object("v1", "Service", "web-metrics", "metrics", func(u *unstructured.Unstructured) { u.Object["spec"] = map[string]any{"selector": selector} }),
		object("v1", "Service", "other", "other", func(u *unstructured.Unstructured) {
			u.Object["spec"] = map[string]any{"selector": map[string]any{"app": "other"}}
		}),
		object("apps/v1", "Deployment", "web", "dp", func(u *unstructured.Unstructured) {
			u.Object["spec"] = map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "webservice", "pod-template-hash": "6f9c7d5b84"}}}}
		}),
		podObject(hangPods[0]), podObject(hangPods[1]), podObject(hangPods[2]),
		object("v1", "Pod", "unrelated", "unrelated", nil))

	service := &graphTarget{Context: "ct-1-1", Kind: graphKindService, Resource: client.SvcGVR.String(), Namespace: "gitlab", Name: "web", UID: "uid-web"}
	members := graphServiceMembers(t.Context(), dial, service)
	require.Empty(t, members.Problem)
	require.Equal(t, hangPods[:3], members.Pods)
	require.Contains(t, members.Origin, "3 Pods from the Service's selector and its EndpointSlice targets")

	// The Pods come from the cluster, their Node and owner from
	// kube_pod_info, and the charts are the workload charts.
	fake := hangStory()
	snapshot := loadGraphOf(t.Context(), fake, service, time.Hour, now, members)
	require.Nil(t, snapshot.Owner)
	require.Equal(t, hangPods[:3], snapshot.Pods)
	require.Equal(t, "worker-f", snapshot.Meta[hangPods[2]].Node)
	require.Equal(t, `count by (pod,node,created_by_kind,created_by_name) (max_over_time(kube_pod_info{namespace="gitlab",`+
		`pod=~"web-6f9c7d5b84-2bq7k|web-6f9c7d5b84-8wzpd|web-6f9c7d5b84-hx4mt"}[3600s]))`, snapshot.PodInfo.Query)
	require.Nil(t, snapshot.failure())
	text := graphPlainLines(t, renderGraph(snapshot, comparePage(120, 30)), 120)
	require.Contains(t, text, "Graph · Service gitlab/web · last 1h to 05:00Z · step 30s")
	require.Contains(t, text, "3 pods")
	require.Contains(t, graphReport(snapshot), "PODS BEHIND THE SERVICE (Kubernetes API, your credentials; the Pods it selects now)")

	// A Service that was replaced is not followed to its namesake, and a
	// Service that selects nothing is not a healthy empty chart.
	replaced := *service
	replaced.UID = "uid-older"
	gone := graphServiceMembers(t.Context(), dial, &replaced)
	require.Empty(t, gone.Pods)
	snapshot = loadGraphOf(t.Context(), fake, &replaced, time.Hour, now, gone)
	text = graphPlainLines(t, renderGraph(snapshot, comparePage(120, 30)), 120)
	require.Contains(t, text, "Nothing was asked of Prometheus. That is missing data, not a healthy reading.")
	require.Contains(t, text, "captured Service identity changed")
	empty := *service
	empty.Name, empty.UID = "other", "uid-other"
	require.Equal(t, "The Service selects no Pod now: its selector and its EndpointSlices name none.", graphServiceMembers(t.Context(), dial, &empty).Problem)

	// The request path of a workload is that of the Service in front of
	// it: one whose selector matches the Pod template, a declared one first.
	for _, tc := range []struct {
		name    string
		target  graphTarget
		sources *observability.Sources
		path    string
		note    string
	}{
		{name: "a declared Service wins", target: *hangTarget, path: "gitlab/web-metrics", note: "it has a requestPath declaration",
			sources: &observability.Sources{RequestPath: &observability.RequestPath{Services: map[string]observability.ServicePath{"gitlab/web-metrics": {}}}}},
		{name: "else the first by name", target: *hangTarget, sources: hangSources(), path: "gitlab/web", note: "it has a requestPath declaration"},
		{name: "nothing declared", target: *hangTarget, sources: &observability.Sources{}, path: "gitlab/web", note: "the first by name of 2 Services"},
		{name: "the Service itself", target: *service, sources: hangSources(), path: "gitlab/web", note: "the Service the graph was opened on"},
		{name: "no workload object", target: graphTarget{Kind: "Deployment", Resource: client.DpGVR.String(), Namespace: "gitlab", Name: "absent"},
			sources: hangSources(), note: "the Deployment was not read, so the Service in front of its Pods is not known; the default app-hop profiles apply"},
	} {
		got := graphServiceFor(t.Context(), dial, &tc.target, tc.sources)
		require.Equal(t, tc.path, got.Path, tc.name)
		require.Contains(t, got.Note, tc.note, tc.name)
	}

	// Without a declaration the default profiles of the app hop apply:
	// the generic HTTP signals instead of the GitLab ones.
	signals, profiles, _ := graphProfileSignals(&observability.Sources{}, "", hangTarget, "", hangPods, 30*time.Second)
	var names []string
	for i := range signals {
		names = append(names, signals[i].Name)
	}
	require.Equal(t, []string{observability.ProfileRED}, profiles)
	require.Equal(t, []string{"p99", "p50", "errors"}, names)
}

func TestGraphComparePageFitsFromTheSmallestSupportedSize(t *testing.T) {
	// Every line fits the width and the page fits the height, so the key
	// line is never pushed out: at every size from the smallest the view
	// draws at, for every signal (with and without data) and grouping.
	denied := hangStory()
	denied.states = map[string]observability.State{"container_cpu_cfs_throttled": observability.StateDenied}
	for name, fake := range map[string]*compareFake{"hang": hangStory(), "denied": denied} {
		base, c := hangCompare(t, fake)
		c = loadGraphContainers(t.Context(), fake, c)
		for grouping := graphByPod; grouping < graphGroupings; grouping++ {
			for i := range c.Signals {
				for _, size := range [][2]int{{ui.MinTaskWidth, ui.MinTaskHeight}, {41, 16}, {47, 13}, {60, 12}, {72, 14}, {80, 20}, {132, 33}} {
					page := comparePage(size[0], size[1])
					page.Grouping, page.Signal, page.ASCII = grouping, c.Signals[i].ID, i%2 == 1
					lines := renderGraphCompare(base, c, page)
					where := fmt.Sprintf("%s %s by %s at %dx%d", name, c.Signals[i].Name, grouping, size[0], size[1])
					graphPlainLines(t, lines, size[0])
					require.LessOrEqual(t, len(lines), size[1], where)
					require.Contains(t, lines[len(lines)-1], "regroup · p overlaid", "%s: the key line is the last line", where)
				}
			}
		}
	}

	// At the smallest width the selected signal keeps its whole name and
	// the signals beside it are counted in fewer words.
	base, c := hangCompare(t, hangStory())
	page := comparePage(ui.MinTaskWidth, 30)
	page.Signal, page.Grouping = "free-threads", graphByNode
	text := graphPlainLines(t, renderGraphCompare(base, c, page), ui.MinTaskWidth)
	require.Contains(t, text, "+7  [free-threads (no data)]  +2")

	// A status that fits no two lines keeps the count of what is not shown.
	fake := hangStory()
	for i := range 14 {
		fake.pods[fmt.Sprintf("%s-p%02d", hangOld, i)] = graphPodMeta{Node: "worker-d", OwnerKind: graphReplicaSet, OwnerName: hangOld}
	}
	base, c = hangCompare(t, fake)
	page = comparePage(ui.MinTaskWidth, ui.MinTaskHeight)
	text = graphPlainLines(t, renderGraphCompare(base, c, page), ui.MinTaskWidth)
	require.Regexp(t, `18 pods · 1-\d of 18 pods ↑↓\n`, text)
}

func TestGraphCompareGroupTitlesSayWhoseNumberIsWhose(t *testing.T) {
	fake := hangStory()
	// r5n9j, the one Pod of the new revision, restarts twice in the window.
	for i := range fake.series {
		if s := &fake.series[i]; strings.Contains(s.match[0], "restarts_total") && strings.HasSuffix(s.labels["pod"], "r5n9j") {
			s.value = func(at time.Time) (float64, bool) {
				if at.Before(graphNow.Add(-20 * time.Minute)) {
					return 0, true
				}
				return 2, true
			}
		}
	}
	base, c := hangCompare(t, fake)

	// The ranking number of a group is its worst member's and the latest
	// value is the median of the group: the title names both.
	page := comparePage(240, 33)
	page.Signal, page.Grouping = "restarts", graphByNode
	text := graphPlainLines(t, renderGraphCompare(base, c, page), 240)
	require.Contains(t, text, "worker-d · 2 pods")
	require.Contains(t, text, "OUTLIER r5n9j · largest +2 in window · median now 1")
	require.NotContains(t, text, " +2 in window · now 1", "a rise of one Pod is not shown beside the median as if both were the group's")

	// In a group of several Pods the short word counts the Pods that
	// differ; it does not call the group an outlier.
	page = comparePage(80, 20)
	page.Signal, page.Grouping = "throttled", graphByRevision
	text = graphPlainLines(t, renderGraphCompare(base, c, page), 80)
	require.Contains(t, text, "rev 1 6f9c7d5b84    1 OUTLIER · 86%")
	require.Contains(t, text, "rev 1 6f9c7d5b84 3 pods, rev 2")
	page.Grouping = graphByNode
	text = graphPlainLines(t, renderGraphCompare(base, c, page), 80)
	require.Contains(t, text, "worker-f · 1 pod ")
	require.Contains(t, text, " OUTLIER · 86%")
	require.NotContains(t, text, "1 OUTLIER")

	// The list says that a group's value at the cursor is a median.
	page = comparePage(60, 20)
	page.Signal, page.Grouping = "throttled", graphByRevision
	text = graphPlainLines(t, renderGraphCompare(base, c, page), 60)
	require.Contains(t, text, "PEAK MEDIAN 05:00")
	require.Regexp(t, `rev 1 6f9c7d5b84 +3 .* 86% +1\.3% 1 OUTLIER\n`, text)
	page.Grouping = graphByPod
	text = graphPlainLines(t, renderGraphCompare(base, c, page), 60)
	require.Contains(t, text, "PEAK AT 05:00")

	// At the smallest width the names stay whole; the value at the cursor
	// is the column that goes, and the header no longer names it.
	page = comparePage(ui.MinTaskWidth, 20)
	page.Signal, page.Grouping = "throttled", graphByNode
	text = graphPlainLines(t, renderGraphCompare(base, c, page), ui.MinTaskWidth)
	for _, want := range []string{"worker-f ", "worker-d ", "worker-e ", " PEAK\n", " 86% OUTLIER\n"} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "MEDIAN 05:00")
}

func TestGraphViewComparisonNeverCancelsAReadOfTheChartsAndResumesItsOwn(t *testing.T) {
	v, _, reads, deliver := compareViewFixture(t, hangTarget)
	key := func(k tcell.Key) {
		action, ok := v.actions.Get(k)
		require.True(t, ok)
		action.Action(tcell.NewEventKey(k, 0, tcell.ModNone))
	}
	flashed := func() string {
		select {
		case message := <-v.app.Flash().Channel():
			return message.Text
		case <-time.After(time.Second):
			return ""
		}
	}

	// n is named in the header on the overlaid charts too: it opens the
	// comparison instead of doing nothing.
	key(ui.KeyN)
	require.True(t, v.comparing)
	require.Equal(t, "Reading every signal once per Pod…", v.busy)
	deliver()
	require.Contains(t, drawnText(t, v, 132, 33), "CPU THROTTLED by pod")

	// While the charts are read again, g and p wait for that read: they
	// neither cancel it nor read the comparison of the page being replaced.
	generation := v.generation
	key(ui.KeyR)
	require.True(t, v.loading)
	generation++
	require.Equal(t, generation, v.generation)
	key(ui.KeyG)
	key(ui.KeyP)
	key(ui.KeyP)
	require.Equal(t, generation, v.generation, "the read of the charts is still the one awaited")
	require.Equal(t, "Reading last 1h once…", v.busy)
	require.EqualValues(t, 1, reads.Load())
	// When the charts arrive the comparison is read for them, once: by Pod,
	// and then by container, the grouping g chose.
	v.accept(v.load(t.Context(), v.window))
	require.False(t, v.loading)
	require.Equal(t, "Reading every signal once per Pod…", v.busy)
	deliver()
	require.Equal(t, "Reading the per-container series once…", v.busy)

	// Leaving the page cancels that read; coming back asks it again
	// instead of leaving a page that says nothing was read.
	v.Stop()
	require.Empty(t, v.busy)
	v.Start()
	require.Equal(t, "Reading the per-container series once…", v.busy)
	require.Eventually(t, func() bool { return reads.Load() == 4 }, 5*time.Second, 2*time.Millisecond)
	// The cancelled read is never delivered: only the one asked again is.
	reads.Store(3)
	deliver()
	require.True(t, v.compare.Containers)
	require.Contains(t, drawnText(t, v, 132, 33), "CPU THROTTLED by container")
	// A page that is complete reads nothing on return.
	v.Stop()
	v.Start()
	require.Empty(t, v.busy)
	require.EqualValues(t, 3, reads.Load())

	// On a page whose read failed the keys say why they do nothing.
	failed, fake, failedReads, _ := compareViewFixture(t, hangTarget)
	fake.states = map[string]observability.State{"kube_pod_info": observability.StateDenied}
	failed.accept(failed.load(t.Context(), failed.window))
	require.NotNil(t, failed.snapshot.failure())
	for len(failed.app.Flash().Channel()) > 0 {
		<-failed.app.Flash().Channel()
	}
	v = failed
	for _, k := range []tcell.Key{ui.KeyP, ui.KeyG, ui.KeyN} {
		key(k)
		require.Equal(t, "Nothing to compare: the read above did not succeed; r reads again", flashed())
	}
	require.False(t, failed.comparing)
	require.Zero(t, failedReads.Load())
}
