// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
)

// The comparison page of :graph answers "which one is different?" for the
// Pods of a workload or Service. It reads one series per Pod for each
// signal, draws them side by side on one scale, and can fold them into
// groups (container, revision, Node, zone). Every query is a fixed template
// with escaped matchers, read once on request within the limits of the
// declared source.

const (
	// graphProfileSignalCap bounds the signals taken from the request-path
	// profiles of the app hop; each is one more read.
	graphProfileSignalCap = 6
	// graphCPUProfile is the built-in profile whose two signals the page
	// already reads with its own templates.
	graphCPUProfile = "container-cpu"

	graphZoneLabel     = "label_topology_kubernetes_io_zone"
	graphRevisionLabel = "label_controller_revision_hash"
	graphReplicaSet    = "ReplicaSet"

	graphPartPod       = "pod"
	graphPartContainer = "container"
	graphPartLimit     = "limit"
	graphLookupZones   = "lookup/zones"
	graphLookupHashes  = "lookup/revisions"
)

// graphGrouping is how the series of the comparison page are folded.
type graphGrouping int

const (
	graphByPod graphGrouping = iota
	graphByContainer
	graphByRevision
	graphByNode
	graphByZone
	graphGroupings
)

func (g graphGrouping) String() string {
	return [...]string{"pod", "container", "revision", "node", "zone"}[min(max(int(g), 0), int(graphGroupings)-1)]
}

// graphRank says which single number of a series ranks it against its
// peers.
type graphRank int

const (
	// graphRankPeak is the largest sample in the window.
	graphRankPeak graphRank = iota
	// graphRankLow is the smallest, for a signal that is worse when low.
	graphRankLow
	// graphRankRise is the rise of a counter inside the window.
	graphRankRise
)

// graphSignal is one reading that every Pod has, with the fixed queries
// that ask it per Pod and, where the metric knows containers, per
// container.
type graphSignal struct {
	// ID is stable and names the answers; Name is the short word in the
	// signal strip; Title heads the charts; About says what is measured.
	ID, Name, Title, About string
	Unit                   chart.Unit
	Rank                   graphRank
	// Direction is the side on which a Pod may be called different.
	Direction chart.Direction
	// Floor is the difference from the median below which nothing is
	// called different, in the unit of the signal.
	Floor float64
	ByPod observability.Expr
	// ByContainer is set when Containers is true.
	ByContainer observability.Expr
	Containers  bool
	// Limit is a per-Pod level drawn with the series, when HasLimit.
	Limit    observability.Expr
	HasLimit bool
	// ShareRank ranks by the peak as a share of the limit when every Pod
	// has one.
	ShareRank bool
	// Profile is the request-path profile the signal came from; empty for
	// the page's own templates. PodLabel carries the Pod name in answers.
	Profile, PodLabel, Metric string
}

// graphUnitFloor is the noise floor of a unit: a Pod closer than this to
// the median of its peers is never called different.
func graphUnitFloor(u chart.Unit) float64 {
	switch u {
	case chart.Ratio:
		return 0.05
	case chart.Seconds:
		return 0.1
	case chart.Cores:
		return 0.1
	case chart.Bytes:
		return 64 * 1024 * 1024
	case chart.BytesPerSecond:
		return 10e3
	}
	return 1
}

// graphFixedSignals are the signals every workload has: cAdvisor and
// kube-state-metrics series selected by namespace and Pod name.
func graphFixedSignals(namespace string, pods []string, step time.Duration) []graphSignal {
	rate, peak := graphRateWindow(step), graphPeakWindow(step)
	pod, _, _ := graphPodMatcher(pods)
	ns := observability.Eq("namespace", namespace)
	containers := []observability.Matcher{
		ns, pod,
		{Label: "container", Op: observability.MatchNotEqual, Value: ""},
		{Label: "container", Op: observability.MatchNotEqual, Value: "POD"},
	}
	usage := func(metric string) func(...string) observability.Expr {
		return func(by ...string) observability.Expr {
			return observability.Sum(observability.Rate(observability.Select(metric, containers...), rate), by...)
		}
	}
	throttled := func(by ...string) observability.Expr {
		return observability.Div(usage("container_cpu_cfs_throttled_periods_total")(by...), usage("container_cpu_cfs_periods_total")(by...))
	}
	memory := func(by ...string) observability.Expr {
		return observability.Sum(observability.OverTime("max", observability.Select("container_memory_working_set_bytes", containers...), peak), by...)
	}
	restarts := func(by ...string) observability.Expr {
		return observability.Sum(observability.Select("kube_pod_container_status_restarts_total", ns, pod), by...)
	}
	limit := func(resource string) observability.Expr {
		return observability.Sum(observability.Select("kube_pod_container_resource_limits", ns, pod, observability.Eq("resource", resource)), "pod")
	}
	network := observability.Binary("+",
		observability.Sum(observability.Rate(observability.Select("container_network_receive_bytes_total", ns, pod), rate), "pod"),
		observability.Sum(observability.Rate(observability.Select("container_network_transmit_bytes_total", ns, pod), rate), "pod"))
	both := func(build func(...string) observability.Expr) (byPod, byContainer observability.Expr) {
		return build("pod"), build("pod", "container")
	}
	signals := []graphSignal{
		{ID: "throttled", Name: "throttled", Title: "CPU THROTTLED", Unit: chart.Ratio, Direction: chart.Above, Containers: true,
			About: "share of CPU periods in which a container was held at its limit, rate over " + graphSpan(rate), Metric: "container_cpu_cfs_throttled_periods_total"},
		{ID: "cpu", Name: "cpu", Title: "CPU", Unit: chart.Cores, Direction: chart.Either, Containers: true, HasLimit: true, Limit: limit("cpu"),
			About: "cores used, rate over " + graphSpan(rate), Metric: "container_cpu_usage_seconds_total"},
		{ID: "memory", Name: "memory", Title: "MEMORY", Unit: chart.Bytes, Direction: chart.Either, Containers: true, HasLimit: true, Limit: limit("memory"),
			ShareRank: true, About: "working set, peak per " + graphSpan(peak), Metric: "container_memory_working_set_bytes"},
		{ID: "restarts", Name: "restarts", Title: "RESTARTS", Unit: chart.Count, Direction: chart.Above, Containers: true, Rank: graphRankRise,
			About: "container restart counter", Metric: "kube_pod_container_status_restarts_total"},
		{ID: "network", Name: "network", Title: "NETWORK", Unit: chart.BytesPerSecond, Direction: chart.Either, ByPod: network,
			About: "receive plus transmit, rate over " + graphSpan(rate), Metric: "container_network_receive_bytes_total"},
	}
	signals[0].ByPod, signals[0].ByContainer = both(throttled)
	signals[1].ByPod, signals[1].ByContainer = both(usage("container_cpu_usage_seconds_total"))
	signals[2].ByPod, signals[2].ByContainer = both(memory)
	signals[3].ByPod, signals[3].ByContainer = both(restarts)
	for i := range signals {
		signals[i].PodLabel = "pod"
		signals[i].Floor = graphUnitFloor(signals[i].Unit)
	}
	return signals
}

// graphProfileSignals are the signals the request-path profiles of the app
// hop provide for these Pods (latency, a thread pool queue): the ones that
// can be read per Pod and that name a worse side. What a profile cannot
// answer is returned as not read, with the reason.
func graphProfileSignals(sources *observability.Sources, path string, t *graphTarget, service string, pods []string, step time.Duration) (signals []graphSignal, profiles []string, notRead []observability.NotRead) {
	target := observability.HopTarget{Namespace: t.Namespace, Service: service, Pods: pods}
	plan := sources.ResolveHop(observability.HopApp, path, target, graphRateWindow(step))
	notRead = append(notRead, plan.NotRead...)
	names := make(map[string]bool)
	for i := range plan.Queries {
		q := &plan.Queries[i]
		if q.Profile == graphCPUProfile {
			continue
		}
		if !containsString(profiles, q.Profile) {
			profiles = append(profiles, q.Profile)
		}
		switch {
		case q.Signal.Worse == observability.WorseNone, len(q.Signal.By) > 0:
			continue
		case q.ByPod.Err() != nil:
			notRead = append(notRead, observability.NotRead{Hop: q.Hop, Profile: q.Profile, Signal: q.Signal.Name, Reason: "the profile names no Pod label, so it cannot be read per Pod"})
			continue
		case len(signals) >= graphProfileSignalCap:
			notRead = append(notRead, observability.NotRead{Hop: q.Hop, Profile: q.Profile, Signal: q.Signal.Name, Reason: "more than six profile signals; the rest are not compared here"})
			continue
		}
		unit, _ := chart.UnitNamed(q.Signal.Unit)
		sig := graphSignal{ID: q.Profile + "/" + q.Signal.Name, Name: q.Signal.Name, Title: strings.ToUpper(q.Signal.Name), Unit: unit,
			About: q.Signal.Title + ", " + q.Profile + " profile", ByPod: q.ByPod, Profile: q.Profile, PodLabel: q.PodLabel, Metric: q.Signal.Metric,
			Direction: chart.Above, Floor: graphUnitFloor(unit)}
		if q.Signal.Worse == observability.WorseLow {
			sig.Direction, sig.Rank = chart.Below, graphRankLow
		}
		if names[sig.Name] {
			// Two profiles with a signal of one name: say whose it is.
			sig.Name = q.Profile + ":" + q.Signal.Name
			sig.Title = strings.ToUpper(sig.Name)
		}
		names[sig.Name] = true
		signals = append(signals, sig)
	}
	return signals, profiles, notRead
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// graphServicePath names the Service whose request-path declaration
// chooses the profiles of the app hop, and says how it was found.
type graphServicePath struct {
	// Path is namespace/name; empty when no Service was found, and the
	// default profiles apply.
	Path, Note string
}

func (p graphServicePath) service() string {
	_, name, _ := strings.Cut(p.Path, "/")
	return name
}

// graphCompare is one retained read of the comparison page. It belongs to
// the graph read it was made for: the same window, step and Pods.
type graphCompare struct {
	Base    *graphSnapshot
	ReadAt  time.Time
	Service graphServicePath
	// Profiles are the request-path profiles that supplied signals.
	Profiles []string
	Signals  []graphSignal
	NotRead  []observability.NotRead
	Results  map[string]observability.PromResult
	// Zones maps a Node to its zone; Hashes maps a Pod to the
	// controller-revision hash of a StatefulSet or DaemonSet.
	Zones, Hashes map[string]string
	// Containers is true once the per-container series were read.
	Containers bool
	// Reused counts answers taken from the charts' own read of the same
	// window instead of being asked again.
	Reused int
}

func graphCompareKey(signal, part string) string { return signal + "#" + part }

// pods are the Pods that were asked for: the ones within the matcher
// limit.
func (c *graphCompare) pods() []string {
	s := c.Base
	return s.Pods[:len(s.Pods)-min(s.PodsOmitted, len(s.Pods))]
}

// signal finds a signal by its ID, or by the short name a caller that only
// knows "p99" arrives with; an unknown one is the first.
func (c *graphCompare) signal(id string) (*graphSignal, int) {
	for i := range c.Signals {
		if c.Signals[i].ID == id {
			return &c.Signals[i], i
		}
	}
	// A caller from another view names a profile signal as profile/name
	// (:slow does); the name alone finds it when this page reads the same
	// reading with its own template, or under another profile.
	if _, name, qualified := strings.Cut(id, "/"); qualified {
		id = name
	}
	for i := range c.Signals {
		if c.Signals[i].Name == id {
			return &c.Signals[i], i
		}
	}
	if len(c.Signals) == 0 {
		return nil, -1
	}
	return &c.Signals[0], 0
}

type graphJob struct {
	key     string
	expr    observability.Expr
	instant bool
}

// graphReadJobs runs the reads of one page, a few at a time.
func graphReadJobs(ctx context.Context, q graphQuerier, start, end time.Time, jobs []graphJob, into map[string]observability.PromResult) {
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
			var res observability.PromResult
			if j.instant {
				res = q.Query(ctx, j.expr, end)
			} else {
				res = q.QueryRange(ctx, j.expr, start, end)
			}
			mu.Lock()
			into[j.key] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
}

// loadGraphCompare reads every signal once per Pod for the window of a
// graph read, plus the two label lookups that place a Pod in a zone or a
// controller revision. It contacts nothing but the given source.
//
//nolint:gocritic // The path is a small value captured by the caller.
func loadGraphCompare(ctx context.Context, q graphQuerier, base *graphSnapshot, sources *observability.Sources, path graphServicePath, now time.Time) *graphCompare {
	c := &graphCompare{Base: base, ReadAt: now.UTC(), Service: path, Results: make(map[string]observability.PromResult),
		Zones: make(map[string]string), Hashes: make(map[string]string)}
	pods := c.pods()
	if len(pods) == 0 {
		return c
	}
	c.Signals = graphFixedSignals(base.Target.Namespace, pods, base.Step)
	profile, profiles, notRead := graphProfileSignals(sources, path.Path, &base.Target, path.service(), pods, base.Step)
	// Latency and queues sit between restarts and the network.
	last := len(c.Signals) - 1
	c.Signals = append(append(append([]graphSignal(nil), c.Signals[:last]...), profile...), c.Signals[last])
	c.Profiles, c.NotRead = profiles, notRead

	var jobs []graphJob
	for i := range c.Signals {
		sig := &c.Signals[i]
		jobs = append(jobs, graphJob{key: graphCompareKey(sig.ID, graphPartPod), expr: sig.ByPod})
		if sig.HasLimit {
			jobs = append(jobs, graphJob{key: graphCompareKey(sig.ID, graphPartLimit), expr: sig.Limit})
		}
	}
	var nodes, unhashed []string
	for _, pod := range pods {
		meta := base.Meta[pod]
		if meta.Node != "" && !containsString(nodes, meta.Node) {
			nodes = append(nodes, meta.Node)
		}
		if meta.OwnerKind != "" && meta.OwnerKind != graphReplicaSet {
			unhashed = append(unhashed, pod)
		}
	}
	if len(nodes) > 0 {
		sort.Strings(nodes)
		quoted := make([]string, len(nodes))
		for i, node := range nodes {
			quoted[i] = observability.RegexQuote(node)
		}
		jobs = append(jobs, graphJob{key: graphLookupZones, instant: true,
			expr: observability.Aggregate("max", observability.Select("kube_node_labels", observability.Re("node", strings.Join(quoted, "|"))), "node", graphZoneLabel)})
	}
	if len(unhashed) > 0 {
		matcher, _, _ := graphPodMatcher(unhashed)
		jobs = append(jobs, graphJob{key: graphLookupHashes, instant: true,
			expr: observability.Aggregate("max", observability.Select("kube_pod_labels", observability.Eq("namespace", base.Target.Namespace), matcher), "pod", graphRevisionLabel)})
	}
	// A series the charts of this window already hold (the per-Pod lines of
	// a small workload) is not asked a second time.
	have := make(map[string]observability.PromResult, len(base.Results))
	for _, res := range base.Results {
		if res.OK() && res.Query != "" {
			have[res.Query] = res
		}
	}
	ask := jobs[:0]
	for _, job := range jobs {
		if res, ok := have[job.expr.String()]; ok && !job.instant {
			c.Results[job.key] = res
			c.Reused++
			continue
		}
		ask = append(ask, job)
	}
	graphReadJobs(ctx, q, base.Start, base.End, ask, c.Results)
	if res, ok := c.Results[graphLookupZones]; ok {
		for i := range res.Series {
			if node, zone := res.Series[i].Labels["node"], res.Series[i].Labels[graphZoneLabel]; node != "" && zone != "" {
				c.Zones[node] = zone
			}
		}
	}
	if res, ok := c.Results[graphLookupHashes]; ok {
		for i := range res.Series {
			if pod, hash := res.Series[i].Labels["pod"], res.Series[i].Labels[graphRevisionLabel]; pod != "" && hash != "" {
				c.Hashes[pod] = hash
			}
		}
	}
	return c
}

// loadGraphContainers adds the per-container series to a comparison read.
// They are asked only when the series are regrouped by container.
func loadGraphContainers(ctx context.Context, q graphQuerier, c *graphCompare) *graphCompare {
	next := *c
	next.Results = make(map[string]observability.PromResult, len(c.Results)+4)
	for key, res := range c.Results {
		next.Results[key] = res
	}
	var jobs []graphJob
	for i := range c.Signals {
		if sig := &c.Signals[i]; sig.Containers {
			jobs = append(jobs, graphJob{key: graphCompareKey(sig.ID, graphPartContainer), expr: sig.ByContainer})
		}
	}
	graphReadJobs(ctx, q, c.Base.Start, c.Base.End, jobs, next.Results)
	next.Containers = true
	return &next
}

// tally counts the answers of the comparison read by outcome.
func (c *graphCompare) tally() graphTally {
	var t graphTally
	for _, res := range c.Results {
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

// graphMember is one compared series: a Pod, or one container of a Pod.
type graphMember struct {
	Pod, Container string
	// Name is the short name shown; Node is where the Pod runs, when known.
	Name, Node string
	Points     []chart.Point
	// Limit is the Pod's limit over time, when the signal has one.
	Limit []chart.Point
	// Value ranks the member (NaN without a sample). Peak and Last are the
	// largest and the latest sample; Share is Peak over the limit.
	Value, Peak, Last, Share float64
	// Outlier is true when Value is far from the peers'. Deviation says
	// how far.
	Outlier   bool
	Deviation chart.Deviation
}

func (m *graphMember) sampled() bool { return len(m.Points) > 0 }

// graphSeriesSet is one signal ready to compare.
type graphSeriesSet struct {
	Signal  *graphSignal
	Members []graphMember
	// State is set when the signal has nothing to compare; Reason says why
	// in a sentence.
	State, Reason string
	// Shares is true when the members are ranked by share of their limit.
	Shares bool
	// Peers is the result of the peer comparison (for Pods: all of them;
	// for containers: Enough is true when any container had enough peers).
	Peers chart.PeerResult
	// Incomplete is true when a limit of the source cut the answer.
	Incomplete bool
}

// graphRankOf reduces a series to the numbers that rank and describe it.
func graphRankOf(points []chart.Point, rank graphRank) (value, peak, last float64) {
	if len(points) == 0 {
		return math.NaN(), math.NaN(), math.NaN()
	}
	peak, low, rise := points[0].V, points[0].V, 0.0
	for i, p := range points {
		peak, low = math.Max(peak, p.V), math.Min(low, p.V)
		if i > 0 && p.V > points[i-1].V {
			rise += p.V - points[i-1].V
		}
	}
	last = points[len(points)-1].V
	switch rank {
	case graphRankLow:
		return low, peak, last
	case graphRankRise:
		return rise, peak, last
	}
	return peak, peak, last
}

// series builds the members of one signal, per Pod or per container, with
// their rank, sorted worst first, and marks the ones that differ from their
// peers. A Pod that was asked for and has no series is still a member.
func (c *graphCompare) series(sig *graphSignal, containers bool) graphSeriesSet {
	set := graphSeriesSet{Signal: sig}
	part := graphPartPod
	if containers {
		part = graphPartContainer
		if !sig.Containers {
			set.State = "per Pod only"
			var can []string
			for i := range c.Signals {
				if c.Signals[i].Containers {
					can = append(can, c.Signals[i].Name)
				}
			}
			set.Reason = sig.Name + " is measured per Pod, not per container, so it cannot be regrouped by container. Signals that can: " +
				strings.Join(can, ", ") + "."
			return set
		}
		if !c.Containers {
			set.State = "not read"
			set.Reason = "The per-container series have not been read yet."
			return set
		}
	}
	res := c.Results[graphCompareKey(sig.ID, part)]
	set.Incomplete = res.State == observability.StateIncomplete
	switch state := graphStateWord(&res); {
	case state == graphNoData:
		set.State = state
		set.Reason = "No series matched " + sig.Metric + " for these Pods: the metric is not scraped, or its labels differ"
		if sig.Profile != "" {
			set.Reason += " from the " + sig.Profile + " profile"
		}
		set.Reason += ". That is missing data, not a reading of zero."
		return set
	case state != "":
		set.State = state
		why := state
		if res.Reason != "" {
			why += ": " + res.Reason
		}
		set.Reason = "The read of " + sig.Name + " did not succeed (" + why + "). Nothing is drawn; that says nothing about the Pods."
		return set
	}
	limits := make(map[string][]chart.Point)
	if sig.HasLimit {
		lim := c.Results[graphCompareKey(sig.ID, graphPartLimit)]
		for i := range lim.Series {
			limits[lim.Series[i].Labels["pod"]] = graphPoints(lim.Series[i].Samples)
		}
	}
	pods := c.pods()
	short := graphShortNames(pods, c.Base.Target.Name)
	label := sig.PodLabel
	if label == "" {
		label = "pod"
	}
	asked := make(map[string]bool, len(pods))
	for _, pod := range pods {
		asked[pod] = true
	}
	answered := make(map[string]bool)
	for i := range res.Series {
		pod := res.Series[i].Labels[label]
		// A series of a Pod that was not asked for is not this object's.
		if !asked[pod] || len(res.Series[i].Samples) == 0 {
			continue
		}
		answered[pod] = true
		m := graphMember{Pod: pod, Name: short[pod], Node: c.Base.Meta[pod].Node, Points: graphPoints(res.Series[i].Samples)}
		if m.Name == "" {
			m.Name = pod
		}
		if containers {
			m.Container = res.Series[i].Labels["container"]
			m.Name += "/" + m.Container
		} else {
			m.Limit = limits[pod]
		}
		set.Members = append(set.Members, m)
	}
	for _, pod := range pods {
		if !answered[pod] {
			set.Members = append(set.Members, graphMember{Pod: pod, Name: short[pod], Node: c.Base.Meta[pod].Node})
		}
	}
	set.Shares = sig.ShareRank && !containers
	for i := range set.Members {
		m := &set.Members[i]
		m.Value, m.Peak, m.Last = graphRankOf(m.Points, sig.Rank)
		m.Share = math.NaN()
		if n := len(m.Limit); n > 0 && m.Limit[n-1].V > 0 && m.sampled() {
			m.Share = m.Peak / m.Limit[n-1].V
		}
		if m.sampled() && math.IsNaN(m.Share) {
			set.Shares = false
		}
	}
	floor := sig.Floor
	if set.Shares {
		floor = graphUnitFloor(chart.Ratio)
		for i := range set.Members {
			if m := &set.Members[i]; m.sampled() {
				m.Value = m.Share
			}
		}
	}
	options := chart.PeerOptions{Direction: sig.Direction, MinDelta: floor}
	flag := func(indexes []int) chart.PeerResult {
		values := make([]float64, len(indexes))
		for i, index := range indexes {
			values[i] = set.Members[index].Value
		}
		peers := chart.Outliers(values, options)
		for _, d := range peers.Outliers {
			m := &set.Members[indexes[d.Index]]
			m.Outlier, m.Deviation = true, d
		}
		return peers
	}
	if containers {
		// A container is compared with the same container in other Pods.
		byName := make(map[string][]int)
		var names []string
		for i := range set.Members {
			name := set.Members[i].Container
			if _, ok := byName[name]; !ok {
				names = append(names, name)
			}
			byName[name] = append(byName[name], i)
		}
		for _, name := range names {
			if peers := flag(byName[name]); peers.Enough {
				set.Peers.Enough = true
			} else if set.Peers.Reason == "" {
				set.Peers.Reason = peers.Reason
			}
		}
	} else {
		all := make([]int, len(set.Members))
		for i := range all {
			all[i] = i
		}
		set.Peers = flag(all)
	}
	graphSortMembers(set.Members, sig.Rank)
	return set
}

// graphWorse reports whether a ranks before b: the worse value first, and a
// member without samples last.
func graphWorse(a, b float64, rank graphRank) (worse, decided bool) {
	switch aNaN, bNaN := math.IsNaN(a), math.IsNaN(b); {
	case aNaN && bNaN:
		return false, false
	case aNaN || bNaN:
		return bNaN, true
	case a == b:
		return false, false
	case rank == graphRankLow:
		return a < b, true
	}
	return a > b, true
}

func graphSortMembers(members []graphMember, rank graphRank) {
	sort.SliceStable(members, func(a, b int) bool {
		if worse, decided := graphWorse(members[a].Value, members[b].Value, rank); decided {
			return worse
		}
		return members[a].Name < members[b].Name
	})
}

// graphGroup is the members that share one container, revision, Node or
// zone, with the band they span.
type graphGroup struct {
	// Key is the group's own value; Title is how it is shown.
	Key, Title string
	// Unknown is the group of members whose value could not be determined.
	Unknown bool
	Members []graphMember
	// Pods is the number of distinct Pods among the members.
	Pods     int
	Outliers []string
	// Value is the worst member's rank; Peak the largest sample of any.
	Value, Peak float64
	// Low, Mid and High are the minimum, median and maximum across the
	// members at each sample time; Limit is their lowest limit.
	Low, Mid, High, Limit []chart.Point
}

// groupOf says which group a member belongs to. known is false when the
// label that would place it is missing.
func (c *graphCompare) groupOf(by graphGrouping, m *graphMember) (key, title string, known bool) {
	meta := c.Base.Meta[m.Pod]
	switch by {
	case graphByContainer:
		return m.Container, m.Container, m.Container != ""
	case graphByNode:
		return meta.Node, meta.Node, meta.Node != ""
	case graphByZone:
		zone := c.Zones[meta.Node]
		return zone, zone, zone != ""
	case graphByRevision:
		name := c.Hashes[m.Pod]
		if meta.OwnerKind == graphReplicaSet {
			name = meta.OwnerName
		}
		if name == "" {
			return "", "", false
		}
		hash := name[strings.LastIndex(name, "-")+1:]
		if number, ok := c.Base.Revisions[name]; ok {
			return name, "rev " + chart.Count.Format(float64(number)) + " " + hash, true
		}
		return name, hash, true
	}
	return m.Pod, m.Name, true
}

// graphBand folds series into their minimum, median and maximum at each
// sample time. A time at which only some members have a sample uses those.
func graphBand(series [][]chart.Point, limits bool) (low, mid, high []chart.Point) {
	at := make(map[int64][]float64)
	var times []int64
	for _, points := range series {
		for _, p := range points {
			key := p.T.UnixNano()
			if _, ok := at[key]; !ok {
				times = append(times, key)
			}
			at[key] = append(at[key], p.V)
		}
	}
	sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })
	for _, key := range times {
		values := at[key]
		sort.Float64s(values)
		t := time.Unix(0, key).UTC()
		low = append(low, chart.Point{T: t, V: values[0]})
		if limits {
			continue
		}
		n := len(values)
		median := values[n/2]
		if n%2 == 0 {
			median = (values[n/2-1] + values[n/2]) / 2
		}
		mid = append(mid, chart.Point{T: t, V: median})
		high = append(high, chart.Point{T: t, V: values[n-1]})
	}
	return low, mid, high
}

// groups folds the members of a signal by container, revision, Node or
// zone, worst group first. Members whose group is not known form one last
// group, so that no Pod disappears from the page.
func (c *graphCompare) groups(set *graphSeriesSet, by graphGrouping) []graphGroup {
	index := make(map[string]int)
	var groups []graphGroup
	for i := range set.Members {
		m := set.Members[i]
		key, title, known := c.groupOf(by, &m)
		if !known {
			key, title = "\x00unknown", by.String()+" not known"
		}
		at, ok := index[key]
		if !ok {
			at = len(groups)
			index[key] = at
			groups = append(groups, graphGroup{Key: key, Title: title, Unknown: !known, Value: math.NaN(), Peak: math.NaN()})
		}
		groups[at].Members = append(groups[at].Members, m)
	}
	rank := set.Signal.Rank
	for i := range groups {
		g := &groups[i]
		var series, limits [][]chart.Point
		pods := make(map[string]bool)
		for j := range g.Members {
			m := &g.Members[j]
			pods[m.Pod] = true
			if m.Outlier {
				g.Outliers = append(g.Outliers, m.Name)
			}
			if !m.sampled() {
				continue
			}
			series = append(series, m.Points)
			if len(m.Limit) > 0 {
				limits = append(limits, m.Limit)
			}
			if worse, decided := graphWorse(m.Value, g.Value, rank); decided && worse {
				g.Value = m.Value
			}
			if math.IsNaN(g.Peak) || m.Peak > g.Peak {
				g.Peak = m.Peak
			}
		}
		g.Pods = len(pods)
		g.Low, g.Mid, g.High = graphBand(series, false)
		g.Limit, _, _ = graphBand(limits, true)
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].Unknown != groups[b].Unknown {
			return groups[b].Unknown
		}
		if worse, decided := graphWorse(groups[a].Value, groups[b].Value, rank); decided {
			return worse
		}
		return groups[a].Title < groups[b].Title
	})
	return groups
}

// graphStanding is what the page says about the Pods across every signal:
// which Pod differs from its peers, and in which signals.
type graphStanding struct {
	// Pods are the Pods that differ in at least one signal, most signals
	// first; Signals lists, per Pod, the names of those signals.
	Pods    []string
	Signals map[string][]string
	// Flagged is true per signal ID that has a differing Pod; States says,
	// per signal ID, why a signal has nothing to compare.
	Flagged map[string]bool
	States  map[string]string
	// Read counts signals with samples, Enough those with enough peers.
	Read, Enough, Total int
}

// first is the signal a page opens on when none was chosen: the first in
// which a Pod differs, else the first that has samples, else the first.
func (st *graphStanding) first(c *graphCompare) string {
	for i := range c.Signals {
		if st.Flagged[c.Signals[i].ID] {
			return c.Signals[i].ID
		}
	}
	for i := range c.Signals {
		if st.States[c.Signals[i].ID] == "" {
			return c.Signals[i].ID
		}
	}
	if len(c.Signals) == 0 {
		return ""
	}
	return c.Signals[0].ID
}

func (c *graphCompare) standing() graphStanding {
	st := graphStanding{Signals: make(map[string][]string), Flagged: make(map[string]bool), States: make(map[string]string), Total: len(c.Signals)}
	for i := range c.Signals {
		sig := &c.Signals[i]
		set := c.series(sig, false)
		if set.State != "" {
			st.States[sig.ID] = set.State
			continue
		}
		st.Read++
		if !set.Peers.Enough {
			continue
		}
		st.Enough++
		for j := range set.Members {
			if m := &set.Members[j]; m.Outlier {
				if _, ok := st.Signals[m.Name]; !ok {
					st.Pods = append(st.Pods, m.Name)
				}
				st.Signals[m.Name] = append(st.Signals[m.Name], sig.Name)
				st.Flagged[sig.ID] = true
			}
		}
	}
	sort.SliceStable(st.Pods, func(a, b int) bool { return len(st.Signals[st.Pods[a]]) > len(st.Signals[st.Pods[b]]) })
	return st
}
