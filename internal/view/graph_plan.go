// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
)

const (
	// graphLineCap is the most Pods of one workload drawn as separate lines;
	// more are folded into minimum, average and maximum.
	graphLineCap = 4
	// graphPodNameLimit and graphPodRegexLimit bound the Pod names put into
	// one matcher. Pods beyond them are counted and stated, not read.
	graphPodNameLimit  = 30
	graphPodRegexLimit = 900
	// graphParallel bounds concurrent reads against the declared source.
	graphParallel = 4
	// graphDeadline bounds one whole page of reads.
	graphDeadline = 30 * time.Second
)

// graphWindows are the selectable windows, on keys 1 to 4.
var graphWindows = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

const graphDefaultWindow = time.Hour

var graphKinds = map[string]string{
	client.PodGVR.String(): "Pod", client.DpGVR.String(): "Deployment", client.RsGVR.String(): "ReplicaSet",
	client.StsGVR.String(): "StatefulSet", client.DsGVR.String(): "DaemonSet", client.NodeGVR.String(): "Node",
}

// graphTarget is the captured object a graph page describes.
type graphTarget struct {
	Context, Kind, Resource, Namespace, Name string
}

func (t *graphTarget) workload() bool { return t.Kind != "Pod" && t.Kind != "Node" }

func (t *graphTarget) Label() string {
	if t.Namespace == "" {
		return t.Kind + " " + t.Name
	}
	return t.Kind + " " + t.Namespace + "/" + t.Name
}

//nolint:gocritic // The immutable selection is captured by value.
func graphTargetFor(target SelectedResourceTarget) (*graphTarget, error) {
	const hint = "Select a Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet or Node first: a graph is drawn for one object"
	if target.Err() != nil {
		return nil, errors.New(hint)
	}
	kind, ok := graphKinds[target.GVR.String()]
	if !ok || (kind != "Node" && target.Namespace == "") {
		return nil, errors.New(hint)
	}
	return &graphTarget{Context: target.Context, Kind: kind, Resource: target.GVR.String(), Namespace: target.Namespace, Name: target.Name}, nil
}

type graphRole int

const (
	graphRoleData graphRole = iota
	graphRoleCeiling
	graphRoleRequest
)

// graphSeriesSpec is one fixed query and how its answer is drawn.
type graphSeriesSpec struct {
	ID, Name string
	Expr     observability.Expr
	// By names the label that tells the returned series apart.
	By    string
	Kind  chart.Kind
	Role  graphRole
	Unit  *chart.Unit
	Glyph rune
	ASCII rune
}

type graphPanelSpec struct {
	ID, Title, Note string
	Unit            chart.Unit
	Series          []graphSeriesSpec
}

type graphPlan struct {
	Panels   []graphPanelSpec
	Restarts *graphSeriesSpec
}

// graphRateWindow is the range a rate is taken over: four steps, and never
// less than two minutes so that two scrapes fall inside it. It is the rule
// :impact uses, so a rate reads the same in both views.
func graphRateWindow(step time.Duration) time.Duration { return impact.RateWindowFor(step) }

// graphPeakWindow is the range a peak is taken over: one step, so that a
// spike between two drawn points is not lost.
func graphPeakWindow(step time.Duration) time.Duration { return max(step, time.Minute) }

func graphSpan(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}

func graphCeiling(id, name string, e observability.Expr) graphSeriesSpec {
	return graphSeriesSpec{ID: id, Name: name, Expr: e, Kind: chart.Reference, Role: graphRoleCeiling, Glyph: '─', ASCII: '='}
}

func graphRequest(e observability.Expr) graphSeriesSpec {
	return graphSeriesSpec{ID: "request", Name: "request", Expr: e, Kind: chart.Reference, Role: graphRoleRequest, Glyph: '┄', ASCII: '.'}
}

// graphOwnerQuery asks kube-state-metrics which Pods the workload owned at
// any time in the window, so that Pods already replaced are still drawn.
func graphOwnerQuery(t *graphTarget, window time.Duration) observability.Expr {
	kind, name := t.Kind, observability.Eq("created_by_name", t.Name)
	if t.Kind == "Deployment" {
		// A Deployment owns Pods through ReplicaSets named <name>-<hash>.
		kind, name = "ReplicaSet", observability.Re("created_by_name", observability.RegexQuote(t.Name)+"-[a-z0-9]{5,10}")
	}
	sel := observability.Select("kube_pod_info", observability.Eq("namespace", t.Namespace), observability.Eq("created_by_kind", kind), name)
	return observability.Aggregate("count", observability.OverTime("max", sel, window), "pod")
}

// graphPodMatcher matches exactly the named Pods. It reports how many names
// were left out to keep the matcher bounded.
func graphPodMatcher(pods []string) (m observability.Matcher, used, omitted int) {
	var parts []string
	size := 0
	for _, pod := range pods {
		quoted := observability.RegexQuote(pod)
		if len(parts) >= graphPodNameLimit || size+len(quoted)+1 > graphPodRegexLimit {
			break
		}
		parts = append(parts, quoted)
		size += len(quoted) + 1
	}
	return observability.Re("pod", strings.Join(parts, "|")), len(parts), len(pods) - len(parts)
}

// buildGraphPlan returns the fixed query templates for a target. For a
// workload, pods are the names found by graphOwnerQuery.
func buildGraphPlan(t *graphTarget, pods []string, step time.Duration) graphPlan {
	if t.Kind == "Node" {
		return graphNodePlan(t, step)
	}
	rate, peak := graphRateWindow(step), graphPeakWindow(step)
	pod := observability.Eq("pod", t.Name)
	folded := false
	if t.workload() {
		pod, _, _ = graphPodMatcher(pods)
		folded = len(pods) > graphLineCap
	}
	ns := observability.Eq("namespace", t.Namespace)
	// cAdvisor also reports the pod cgroup (container="") and the pause
	// container; both would double the sum.
	containers := []observability.Matcher{
		ns, pod,
		{Label: "container", Op: observability.MatchNotEqual, Value: ""},
		{Label: "container", Op: observability.MatchNotEqual, Value: "POD"},
	}
	resource := func(metric, name string) observability.Expr {
		return observability.Sum(observability.Select(metric, ns, pod, observability.Eq("resource", name)), "pod")
	}
	// perPod turns a per-Pod expression into the drawn series: the Pod
	// itself, one line per Pod, or minimum, average and maximum.
	perPod := func(name string, e observability.Expr) []graphSeriesSpec {
		switch {
		case !t.workload():
			return []graphSeriesSpec{{ID: "usage", Name: name, Expr: e}}
		case !folded:
			return []graphSeriesSpec{{ID: "usage", Name: name, Expr: e, By: "pod"}}
		}
		return []graphSeriesSpec{
			{ID: "avg", Name: "avg", Expr: observability.Aggregate("avg", e)},
			{ID: "max", Name: "max", Expr: observability.Aggregate("max", e)},
			{ID: "min", Name: "min", Expr: observability.Aggregate("min", e)},
		}
	}
	// A workload's Pods can differ (a rollout changes the template), so the
	// level drawn is the lowest one: the first a Pod would reach.
	level := func(e observability.Expr) (observability.Expr, string) {
		if t.workload() {
			return observability.Aggregate("min", e), "min "
		}
		return observability.Sum(e), ""
	}
	ratio := chart.Ratio

	memory := observability.Sum(observability.OverTime("max", observability.Select("container_memory_working_set_bytes", containers...), peak), "pod")
	memLimit, lowest := level(resource("kube_pod_container_resource_limits", "memory"))
	memRequest, _ := level(resource("kube_pod_container_resource_requests", "memory"))
	cpu := observability.Sum(observability.Rate(observability.Select("container_cpu_usage_seconds_total", containers...), rate), "pod")
	cpuLimit, _ := level(resource("kube_pod_container_resource_limits", "cpu"))
	cpuRequest, _ := level(resource("kube_pod_container_resource_requests", "cpu"))
	throttled := observability.Div(
		observability.Sum(observability.Rate(observability.Select("container_cpu_cfs_throttled_periods_total", containers...), rate)),
		observability.Sum(observability.Rate(observability.Select("container_cpu_cfs_periods_total", containers...), rate)))
	network := func(metric string) observability.Expr {
		return observability.Sum(observability.Rate(observability.Select(metric, ns, pod), rate))
	}
	all := ""
	if t.workload() {
		all = ", all pods"
	}
	memPanel := graphPanelSpec{ID: "memory", Title: "MEMORY", Unit: chart.Bytes, Note: "working set, peak per " + graphSpan(peak),
		Series: perPod("working set", memory)}
	limit := graphCeiling("limit", lowest+"limit", memLimit)
	request := graphRequest(memRequest)
	request.Name = lowest + request.Name
	memPanel.Series = append(memPanel.Series, limit, request)

	cpuPanel := graphPanelSpec{ID: "cpu", Title: "CPU cores", Unit: chart.Cores, Note: "cores, rate over " + graphSpan(rate), Series: perPod("usage", cpu)}
	limit = graphCeiling("limit", lowest+"limit", cpuLimit)
	request = graphRequest(cpuRequest)
	request.Name = lowest + request.Name
	cpuPanel.Series = append(cpuPanel.Series, limit, request,
		graphSeriesSpec{ID: "throttled", Name: "throttled", Expr: throttled, Kind: chart.OffAxis, Unit: &ratio})

	netPanel := graphPanelSpec{ID: "network", Title: "NETWORK", Unit: chart.BytesPerSecond, Note: "bytes per second, rate over " + graphSpan(rate) + all,
		Series: []graphSeriesSpec{
			{ID: "rx", Name: "rx", Expr: network("container_network_receive_bytes_total")},
			{ID: "tx", Name: "tx", Expr: network("container_network_transmit_bytes_total")},
		}}

	restarts := graphSeriesSpec{ID: "restarts", Name: "restarts", By: "pod",
		Expr: observability.Sum(observability.Select("kube_pod_container_status_restarts_total", ns, pod), "pod")}
	if folded {
		// Past the series limit, per-Pod counters would not all be kept.
		restarts.By = ""
		restarts.Expr = observability.Sum(observability.Select("kube_pod_container_status_restarts_total", ns, pod))
	}
	return graphPlan{Panels: []graphPanelSpec{memPanel, cpuPanel, netPanel}, Restarts: &restarts}
}

func graphNodePlan(t *graphTarget, step time.Duration) graphPlan {
	rate := graphRateWindow(step)
	node := observability.Eq("node", t.Name)
	one := func(metric string, ms ...observability.Matcher) observability.Expr {
		return observability.Sum(observability.Select(metric, append([]observability.Matcher{node}, ms...)...))
	}
	allocatable := func(resource string) graphSeriesSpec {
		return graphCeiling("allocatable", "allocatable", one("kube_node_status_allocatable", observability.Eq("resource", resource)))
	}
	root := observability.Eq("mountpoint", "/")
	busy := observability.Matcher{Label: "mode", Op: observability.MatchNotRegex, Value: "idle|iowait|steal"}
	return graphPlan{Panels: []graphPanelSpec{
		{ID: "cpu", Title: "CPU cores", Unit: chart.Cores, Note: "cores busy, rate over " + graphSpan(rate), Series: []graphSeriesSpec{
			{ID: "used", Name: "busy", Expr: observability.Sum(observability.Rate(observability.Select("node_cpu_seconds_total", node, busy), rate))},
			allocatable("cpu"),
		}},
		{ID: "memory", Title: "MEMORY", Unit: chart.Bytes, Note: "total minus available", Series: []graphSeriesSpec{
			{ID: "used", Name: "used", Expr: observability.Binary("-", one("node_memory_MemTotal_bytes"), one("node_memory_MemAvailable_bytes"))},
			allocatable("memory"),
		}},
		{ID: "disk", Title: "DISK /", Unit: chart.Bytes, Note: "root filesystem, size minus available", Series: []graphSeriesSpec{
			{ID: "used", Name: "used", Expr: observability.Binary("-", one("node_filesystem_size_bytes", root), one("node_filesystem_avail_bytes", root))},
			graphCeiling("size", "size", one("node_filesystem_size_bytes", root)),
		}},
		{ID: "pods", Title: "PODS", Unit: chart.Count, Note: "Pods bound to the Node", Series: []graphSeriesSpec{
			{ID: "used", Name: "pods", Expr: observability.Aggregate("count", observability.Select("kube_pod_info", node))},
			allocatable("pods"),
		}},
	}}
}

// graphQuerier is the part of the declared Prometheus client a graph uses.
type graphQuerier interface {
	Display() string
	Query(ctx context.Context, e observability.Expr, at time.Time) observability.PromResult
	QueryRange(ctx context.Context, e observability.Expr, start, end time.Time) observability.PromResult
}

// graphSnapshot is one retained read: what was asked, when, and each answer.
type graphSnapshot struct {
	Target             graphTarget
	Window, Step       time.Duration
	Start, End, ReadAt time.Time
	Source             string
	// Status is set when nothing could be asked at all.
	Status *observability.Status
	// Owner is the Pod lookup of a workload; Pods are the names it found
	// and PodsOmitted those left out of the matcher.
	Owner       *observability.PromResult
	Pods        []string
	PodsOmitted int
	Plan        graphPlan
	Results     map[string]observability.PromResult
	Marks       []graphMark
	MarkNote    string
}

// graphMark is a moment read from the object itself.
type graphMark struct {
	Key           rune
	T             time.Time
	Label, Origin string
}

func graphResultKey(panel, series string) string { return panel + "/" + series }

// graphEndAlign is the coarsest alignment of a window end. A long window
// has a long step; aligning its end to that step would leave out up to a
// quarter of an hour before now, which is when a fresh incident happened.
const graphEndAlign = time.Minute

// graphFrameTimes aligns the window end to the step, and for long steps to
// the minute, so that samples fall on round clock times, a re-read within
// that time asks the same grid, and the window never ends more than a
// minute before the read.
func graphFrameTimes(now time.Time, window time.Duration) (start, end time.Time, step time.Duration) {
	step = observability.StepFor(window)
	end = now.UTC().Truncate(min(step, graphEndAlign))
	return end.Add(-window), end, step
}

// loadGraph performs every read of one page. It never contacts anything
// but the given source, and stops when ctx ends.
func loadGraph(ctx context.Context, q graphQuerier, t *graphTarget, window time.Duration, now time.Time) *graphSnapshot {
	start, end, step := graphFrameTimes(now, window)
	s := &graphSnapshot{Target: *t, Window: window, Step: step, Start: start, End: end, ReadAt: now.UTC(), Source: q.Display(),
		Results: make(map[string]observability.PromResult)}
	if t.workload() {
		owner := q.Query(ctx, graphOwnerQuery(t, window), end)
		s.Owner = &owner
		if !owner.OK() {
			return s
		}
		for i := range owner.Series {
			if pod := owner.Series[i].Labels["pod"]; pod != "" {
				s.Pods = append(s.Pods, pod)
			}
		}
		sort.Strings(s.Pods)
		if len(s.Pods) == 0 {
			return s
		}
		_, _, s.PodsOmitted = graphPodMatcher(s.Pods)
	}
	s.Plan = buildGraphPlan(t, s.Pods, step)
	type job struct {
		key  string
		expr observability.Expr
	}
	var jobs []job
	for i := range s.Plan.Panels {
		p := &s.Plan.Panels[i]
		for j := range p.Series {
			jobs = append(jobs, job{graphResultKey(p.ID, p.Series[j].ID), p.Series[j].Expr})
		}
	}
	if s.Plan.Restarts != nil {
		jobs = append(jobs, job{graphResultKey("", s.Plan.Restarts.ID), s.Plan.Restarts.Expr})
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		gate = make(chan struct{}, graphParallel)
	)
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			res := q.QueryRange(ctx, j.expr, start, end)
			mu.Lock()
			s.Results[j.key] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	return s
}

// graphTally counts the answers of a snapshot by outcome.
type graphTally struct {
	Asked, WithSamples, Empty, Failed, Incomplete int
	// Worst is one failed answer, shown when every read failed alike.
	Worst observability.Status
}

func (s *graphSnapshot) tally() graphTally {
	var t graphTally
	for _, res := range s.Results {
		t.Asked++
		switch {
		case !res.OK():
			t.Failed++
			t.Worst = res.Status
		case graphSamples(&res) == 0:
			t.Empty++
		default:
			t.WithSamples++
		}
		if res.State == observability.StateIncomplete {
			t.Incomplete++
		}
	}
	return t
}

func graphSamples(res *observability.PromResult) int {
	total := 0
	for i := range res.Series {
		total += len(res.Series[i].Samples)
	}
	return total
}

// graphRestartEvents returns the times the restart counter rose between
// two neighbouring samples, and the total rise between the first and last
// sample of each series. A rise across a hole counts towards the total but
// has no known time, so it gets no mark. measured is false when no series
// has two samples: one sample cannot show a rise, and that is not "+0".
func graphRestartEvents(res *observability.PromResult, step time.Duration) (events []time.Time, total float64, measured bool) {
	for i := range res.Series {
		samples := res.Series[i].Samples
		for j := 1; j < len(samples); j++ {
			measured = true
			rise := samples[j].Value - samples[j-1].Value
			if rise <= 0 {
				continue
			}
			total += rise
			if samples[j].Timestamp.Sub(samples[j-1].Timestamp) <= step+step/2 {
				events = append(events, samples[j].Timestamp)
			}
		}
	}
	sort.Slice(events, func(a, b int) bool { return events[a].Before(events[b]) })
	return events, total, measured
}
