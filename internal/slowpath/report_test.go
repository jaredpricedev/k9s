// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sentences(report *slowpath.Report) string {
	parts := make([]string, 0, len(report.Summary))
	for _, sentence := range report.Summary {
		parts = append(parts, sentence.Text)
	}
	return strings.Join(parts, " ")
}

func lane(t *testing.T, report *slowpath.Report, id string) *slowpath.Lane {
	t.Helper()
	for i := range report.Lanes {
		if report.Lanes[i].ID == id {
			return &report.Lanes[i]
		}
	}
	require.Failf(t, "lane not found", "%s", id)
	return nil
}

// before is a time of the story, minutes before its read time.
func before(minutes float64) time.Time {
	return slowtest.Now.Add(-time.Duration(minutes * float64(time.Minute)))
}

func TestTheHangIsLocatedOnOnePodAndTheUnrelatedBlipIsNotBlamed(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)
	require.Equal(t, 35, report.Asked)
	require.Equal(t, 35, report.Answered)
	require.Empty(t, report.Failure)

	// One row per hop, in the order a request meets them.
	var ids []string
	states := map[string]slowpath.State{}
	for i := range report.Hops {
		ids = append(ids, report.Hops[i].Spec.ID)
		states[report.Hops[i].Spec.ID] = report.Hops[i].State
	}
	assert.Equal(t, []string{slowpath.HopIngress, slowpath.HopService, slowpath.HopApp, "dep:gitlab/gitlab-postgresql", "dep:gitlab/gitlab-redis-master",
		"dep:gitlab/gitlab-gitaly", slowpath.HopDNS, slowpath.HopNetwork}, ids)
	assert.Equal(t, map[string]slowpath.State{
		slowpath.HopIngress: slowpath.Elevated, slowpath.HopService: slowpath.Elevated, slowpath.HopApp: slowpath.Outlier,
		"dep:gitlab/gitlab-postgresql": slowpath.Quiet, "dep:gitlab/gitlab-redis-master": slowpath.Quiet, "dep:gitlab/gitlab-gitaly": slowpath.Quiet,
		slowpath.HopDNS: slowpath.Quiet, slowpath.HopNetwork: slowpath.Elevated,
	}, states)

	ingress := report.Hop(slowpath.HopIngress)
	assert.Equal(t, "client → ingress", ingress.Spec.Label())
	assert.Equal(t, slowtest.Host, ingress.Spec.What)
	assert.Equal(t, "499 2.2/s", ingress.Key)
	assert.Equal(t, "6.29% of requests · p99 10s · upstream-p99 10s · 5xx 0% · requests 39.6/s", ingress.Evidence,
		"the give-ups as a share of all requests, then the other raised signals, then the rest")

	service := report.Hop(slowpath.HopService)
	assert.Equal(t, "2 of 3 ready", service.Key, "the worst moment of the window, not only now")
	assert.Equal(t, "…-hx4mt not Ready twice, 3m longest · now 3 of 3 ready · …-hx4mt Ready=True since 07:51", service.Evidence)

	app := report.Hop(slowpath.HopApp)
	assert.Equal(t, "p99 48s", app.Key)
	assert.Equal(t, "1 of 3 pods: …-hx4mt p99 48s vs peers 350ms · also queued, free-threads, throttled", app.Evidence)
	assert.Equal(t, []string{"1 of 3 pods: …-hx4mt vs peers 350ms", "1 of 3: …-hx4mt vs 350ms"}, app.Briefs)
	require.Len(t, app.Pods, 3)
	assert.Equal(t, []slowpath.State{slowpath.Quiet, slowpath.Quiet, slowpath.Outlier}, []slowpath.State{app.Pods[0].State, app.Pods[1].State, app.Pods[2].State})
	assert.Equal(t, "p99 350ms", app.Pods[0].Key)
	assert.Equal(t, "p99 48s", app.Pods[2].Key)
	assert.True(t, strings.HasPrefix(app.Pods[2].Evidence, "queued 30 · free-threads 0 · throttled 86% · "), app.Pods[2].Evidence)
	assert.True(t, strings.HasSuffix(app.Pods[2].Evidence, "memory 62% · restarts 0 · requests 12/s · threads 8 · cpu 350m · node worker-f"), app.Pods[2].Evidence)
	p99 := app.Signal("p99")
	require.Len(t, p99.Outliers, 1)
	assert.Equal(t, slowpath.Deviation{Pod: slowtest.Outlier, Value: 48, Peers: 0.35}, p99.Outliers[0])
	assert.Empty(t, app.Signal("busy-threads").Outliers, "eight busy threads against three is not three times the usual level of that Pod")

	assert.Equal(t, "connections 41", report.Hop("dep:gitlab/gitlab-postgresql").Key)
	assert.Equal(t, "up 1 · active 6 · max-connections 100", report.Hop("dep:gitlab/gitlab-postgresql").Evidence)
	assert.Equal(t, "latency 120µs", report.Hop("dep:gitlab/gitlab-redis-master").Key)
	assert.Equal(t, "p99 100ms", report.Hop("dep:gitlab/gitlab-gitaly").Key)
	dns := report.Hop(slowpath.HopDNS)
	assert.Equal(t, "p99 6ms", dns.Key)
	assert.Equal(t, "servfail 0 · dns-errors 0 · nxdomain 15% · requests 40/s", dns.Evidence, "the Hubble DNS signal is DNS evidence")
	network := report.Hop(slowpath.HopNetwork)
	assert.Equal(t, "drops 0.8/s", network.Key)
	assert.Equal(t, "POLICY_DENIED to …-2bq7k · rst 0.17% · flows 360/s", network.Evidence)
	assert.Nil(t, network.Signal("dns-errors"))

	// The episodes are the stretches of the first raised signal on the
	// path. Each begins half a minute (one step) before its first raised
	// sample, which is the time the headline states.
	assert.Equal(t, []chart.Interval{{Start: before(52.5), End: before(49.5)}, {Start: before(33.5), End: before(29.5)}, {Start: before(7.5), End: before(5.5)}},
		report.Episodes)
	assert.Equal(t, "3 episodes of client give-ups (HTTP 499) at the ingress: 07:28, 07:47, 08:13 UTC, 2m-4m each.", report.Headline)
	assert.Equal(t, "3 episodes of client give-ups (HTTP 499) at the ingress, 2m-4m each.", report.HeadlineShort)
	require.Equal(t, 0, report.Symptom)

	var labels []string
	for i := range report.Lanes {
		labels = append(labels, report.Lanes[i].Label)
	}
	assert.Equal(t, []string{"ingress: 499, p99, +1", "endpoints: not ready …-hx4mt", "pods: p99 …-hx4mt", "pods: queued …-hx4mt",
		"pods: free-threads …-hx4mt", "pods: throttled …-hx4mt", "3 dependencies", "dns", "network: drops …-2bq7k", "restarts", "rollouts, new pods"}, labels)
	drops := lane(t, report, slowpath.HopNetwork)
	assert.Equal(t, []chart.Interval{{Start: before(41.5), End: before(40.5)}}, drops.Marks)
	assert.Equal(t, slowtest.Calm, drops.Pod)
	assert.Zero(t, slowpath.OverlapCount(report.Episodes, drops.Marks), "the blip is between two episodes")
	assert.Equal(t, 3, slowpath.OverlapCount(report.Episodes, lane(t, report, "app:throttled").Marks))
	assert.Equal(t, 2, slowpath.OverlapCount(report.Episodes, lane(t, report, slowpath.HopService).Marks))

	text := sentences(report)
	for _, want := range []string{
		"All 3 overlap slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.",
		"2 of 3 (07:28, 07:47) overlap not Ready on pod …-hx4mt.",
		"None overlaps dependencies, dns, restarts, rollouts or network.",
		"Packet drops (POLICY_DENIED) on pod …-2bq7k: 07:39 for 1m, outside every episode.",
		"At worst 6.29% of requests. 5xx stayed at 0%: clients gave up waiting, they were not refused.",
		"Overlap is order in time, not cause.",
	} {
		assert.Contains(t, text, want)
	}
	lower := strings.ToLower(report.Headline + " " + text)
	for _, banned := range []string{"because", "caused", "due to", "root cause", "blame", "healthy"} {
		assert.NotContains(t, lower, banned)
	}
	assert.NotContains(t, text, "overlap packet drops", "a burst that overlaps no episode is never listed with what overlaps")
	// The statement about overlap and the caveat are what stay on a small screen.
	ranks := map[string]int{}
	for _, sentence := range report.Summary {
		ranks[sentence.Text] = sentence.Rank
	}
	assert.Equal(t, slowpath.RankOverlap, ranks["All 3 overlap slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt."])
	assert.Equal(t, slowpath.RankCaveat, ranks["Overlap is order in time, not cause."])
	assert.Equal(t, slowpath.RankOutside, ranks["Packet drops (POLICY_DENIED) on pod …-2bq7k: 07:39 for 1m, outside every episode."])
}

func quietPath() *slowpath.Path {
	path := slowtest.Path()
	for i := range path.Pods {
		path.Pods[i].ReadySince = slowtest.Now.Add(-6 * 24 * time.Hour)
	}
	return path
}

func TestAQuietWindowIsQuietForWhatWasReadAndListsWhatWasNot(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{Quiet: true, Absent: []string{"hubble_"}}, slowtest.Sources(), quietPath(), time.Hour)
	assert.Equal(t, "No signal crossed its rule in the last 1h.", report.Headline)
	assert.Empty(t, report.Episodes)
	assert.Equal(t, -1, report.Symptom)
	text := sentences(report)
	assert.Contains(t, text, "Quiet for the hops that were read: ingress, endpoints, pods, dependencies and dns.")
	assert.Contains(t, text, "No restarts or rollouts in the window.")
	assert.Contains(t, text, "Not read: network.")
	assert.Contains(t, text, "Quiet is not healthy: a rule compares each signal with its own usual level in this window.")
	for i := range report.Hops {
		hop := &report.Hops[i]
		if hop.Spec.ID == slowpath.HopNetwork {
			assert.Equal(t, slowpath.NotRead, hop.State)
			assert.Contains(t, hop.Reason, "no series matched hubble_drop_total with destination_namespace=\"gitlab\"")
			assert.Contains(t, hop.Reason, "the metric is not scraped, or its labels differ from the cilium-hubble profile")
			assert.Empty(t, hop.Key, "a hop that was not read has no number")
			continue
		}
		assert.Equal(t, slowpath.Quiet, hop.State, hop.Spec.ID)
	}
	assert.Equal(t, "p99 440ms", report.Hop(slowpath.HopIngress).Key, "a quiet hop still shows its latency")
	assert.Equal(t, "3 of 3 ready", report.Hop(slowpath.HopService).Key)
	assert.Equal(t, "no Pod changed Ready in the window", report.Hop(slowpath.HopService).Evidence)
	assert.True(t, strings.HasPrefix(report.Hop(slowpath.HopApp).Evidence, "3 pods, none differs · "), report.Hop(slowpath.HopApp).Evidence)
	// The DNS row is quiet on CoreDNS alone, and says one signal is missing.
	assert.Contains(t, report.Hop(slowpath.HopDNS).Evidence, "1 of 5 signals not read")
	network := lane(t, report, slowpath.HopNetwork)
	assert.NotEmpty(t, network.NotRead)
	assert.Equal(t, []chart.Interval{{Start: report.Start, End: report.End}}, network.Unknown, "a lane that was not read is unknown from end to end, never quiet")
}

func TestADeniedSourceIsNotReadAndTheAPIFactsRemain(t *testing.T) {
	story := &slowtest.Story{Deny: observability.Status{State: observability.StateDenied, Reason: "HTTP 403", HTTPStatus: 403}}
	report := slowtest.Report(story, slowtest.Sources(), slowtest.Path(), time.Hour)
	assert.Zero(t, report.Answered)
	assert.Equal(t, "denied: HTTP 403", report.Failure)
	assert.Equal(t, "Prometheus was not read (denied: HTTP 403), so no hop has latency evidence.", report.Headline)
	for i := range report.Hops {
		hop := &report.Hops[i]
		if hop.Spec.ID == slowpath.HopService {
			continue
		}
		assert.Equal(t, slowpath.NotRead, hop.State, hop.Spec.ID)
		assert.Equal(t, "prometheus: denied (HTTP 403)", hop.Reason, hop.Spec.ID)
	}
	service := report.Hop(slowpath.HopService)
	assert.Equal(t, slowpath.Elevated, service.State, "the Kubernetes API still knows that a Pod changed Ready inside the window")
	assert.Equal(t, "3 of 3 ready", service.Key)
	assert.Equal(t, "…-hx4mt Ready=True since 07:51 · API only: kube_pod_status_ready not read", service.Evidence)
	endpoints := lane(t, report, slowpath.HopService)
	assert.False(t, endpoints.Signal, "a dated fact from the API is not a symptom to count episodes from")
	assert.Equal(t, []chart.Interval{{Start: report.Start, End: before(28.5)}}, endpoints.Unknown, "what happened before the last change is not kept by the API")
	text := sentences(report)
	assert.Contains(t, text, "In the window: a Ready change on the endpoints at 07:51.")
	assert.Contains(t, text, "Not read: ingress, pods, dependencies, dns and network.")
	assert.Contains(t, text, "Not read is not quiet and not healthy.")
	assert.NotContains(t, text, "Quiet for")

	// A batch that was never sent reads the same way.
	plan := slowpath.BuildPlan(slowtest.Sources(), slowtest.Path(), time.Hour, slowtest.Now)
	status := observability.Status{Kind: observability.KindPrometheus, State: observability.StateNotConfigured, Reason: "no Prometheus source is declared for this context"}
	unsent := slowpath.Evaluate(plan, slowpath.Unread(plan, status), nil, slowtest.Now)
	assert.Equal(t, "not-configured: no Prometheus source is declared for this context", unsent.Failure)
	assert.Equal(t, slowpath.NotRead, unsent.Hop(slowpath.HopApp).State)
	require.Len(t, unsent.Hop(slowpath.HopApp).Pods, 3, "the Pods are still listed, each not read")
	assert.Equal(t, slowpath.NotRead, unsent.Hop(slowpath.HopApp).Pods[0].State)
}

func TestACounterWithoutTheMatchedSeriesReadsAsZeroBecauseItsTotalWasRead(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{Quiet: true, Absent: []string{`status="499"`}}, slowtest.Sources(), quietPath(), time.Hour)
	ingress := report.Hop(slowpath.HopIngress)
	gaveUp := ingress.Signal("499")
	require.True(t, gaveUp.Read)
	assert.Equal(t, `no series matched status="499"; read as 0 because nginx_ingress_controller_requests itself was read`, gaveUp.Note)
	assert.Zero(t, gaveUp.Worst)
	assert.Empty(t, gaveUp.Crossed)
	assert.Equal(t, slowpath.Quiet, ingress.State)
	assert.Contains(t, strings.Join(report.Detail(ingress), "\n"), "note: no series matched status=\"499\"")

	// Without the total there is nothing to tell a zero from a metric that
	// is not scraped: the signal stays not read.
	report = slowtest.Report(&slowtest.Story{Quiet: true, Absent: []string{"nginx_ingress_controller_requests"}}, slowtest.Sources(), quietPath(), time.Hour)
	assert.False(t, report.Hop(slowpath.HopIngress).Signal("499").Read)
	assert.Contains(t, report.Hop(slowpath.HopIngress).Evidence, "3 of 5 signals not read")
}

func TestPodsAreComparedOnlyWithEnoughPeers(t *testing.T) {
	path := slowtest.Path()
	path.Pods = path.Pods[1:]
	report := slowtest.Report(&slowtest.Story{Pods: []string{slowtest.Third, slowtest.Outlier}}, slowtest.Sources(), path, time.Hour)
	app := report.Hop(slowpath.HopApp)
	assert.Equal(t, slowpath.Elevated, app.State, "with two Pods nothing says which one is the odd one")
	assert.Equal(t, "not enough peers (2 with samples)", app.Signal("p99").PeerNote)
	assert.Equal(t, "1 of 2 pods raised: …-hx4mt · not enough peers (2 with samples)", app.Evidence)
	assert.Equal(t, slowpath.Elevated, app.Pods[1].State)
	assert.Contains(t, sentences(report), "All 3 overlap slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.",
		"the lanes still say which Pod the marks belong to")
}

// ranger answers range reads from a function.
type ranger func(query string, at time.Time) map[string]float64

func (r ranger) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	res := observability.PromResult{Query: e.String(), Start: start, End: end, Step: observability.StepFor(end.Sub(start)),
		Status: observability.Status{Kind: observability.KindPrometheus, State: observability.StateComplete}}
	series := map[string]*observability.PromSeries{}
	var order []string
	for at := start; !at.After(end); at = at.Add(res.Step) {
		for pod, value := range r(res.Query, at) {
			if series[pod] == nil {
				series[pod] = &observability.PromSeries{Labels: map[string]string{"pod": pod}}
				order = append(order, pod)
			}
			series[pod].Samples = append(series[pod].Samples, observability.Sample{Timestamp: at, Value: value})
		}
	}
	for _, pod := range order {
		res.Series = append(res.Series, *series[pod])
	}
	return res
}

func TestAPodThatIsAlwaysSlowerDiffersFromItsPeersWithoutEverCrossingItsOwnRule(t *testing.T) {
	path := slowtest.Path()
	path.Ingresses = nil
	steady := ranger(func(query string, _ time.Time) map[string]float64 {
		if strings.Contains(query, "http_request_duration_seconds_bucket") && strings.Contains(query, "0.99") {
			return map[string]float64{slowtest.Calm: 0.3, slowtest.Third: 0.31, slowtest.Outlier: 2}
		}
		return nil
	})
	plan := slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	report := slowpath.Evaluate(plan, slowpath.Collect(t.Context(), steady, plan), nil, slowtest.Now)
	app := report.Hop(slowpath.HopApp)
	assert.Equal(t, slowpath.Outlier, app.State)
	assert.Equal(t, "1 of 3 pods: …-hx4mt p99 2s usually vs peers 305ms", app.Evidence)
	require.Len(t, app.Signal("p99").Outliers, 1)
	assert.True(t, app.Signal("p99").Outliers[0].Sustained)
	assert.Empty(t, app.Signal("p99").Crossed, "two seconds all window is under the always-raised level and no change against itself")
	assert.Equal(t, "No signal crossed its rule in the last 1h.", report.Headline, "there is no episode; the difference is on the row")
	assert.Equal(t, slowpath.NotRead, report.Hop(slowpath.HopIngress).State)
	assert.Contains(t, report.Hop(slowpath.HopIngress).Reason, "no Ingress in namespace gitlab routes to Service gitlab-webservice-default")
	assert.Equal(t, "client → service", report.Hop(slowpath.HopService).Spec.Label())
}

func TestAWideWindowSaysWhatItCannotShow(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), 6*time.Hour)
	assert.Equal(t, 5*time.Minute, report.Step)
	assert.Contains(t, sentences(report),
		"A sample here stands for 5m and a rate for 20m: a stall of a few minutes merges with the next one or disappears; 1h or 15m shows it.")
	assert.NotContains(t, sentences(slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)), "A sample here stands for")
	short := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), 15*time.Minute)
	assert.Equal(t, "1 episode of client give-ups (HTTP 499) at the ingress: 08:13:00 UTC, for 2m.", short.Headline, "a short window writes seconds")
	assert.Contains(t, sentences(short), "It overlaps slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.")
	assert.Contains(t, sentences(short), "It does not overlap endpoints, dependencies, dns, network, restarts or rollouts.")
}

func TestDetailStatesTheRuleTheSamplesAndTheExactQuery(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)
	detail := strings.Join(report.Detail(report.Hop(slowpath.HopApp)), "\n")
	for _, want := range []string{
		"service → pods · 3 pods · profiles gitlab-webservice, container-cpu · outlier",
		"p99 [gitlab-webservice] Rails request duration, 99th percentile",
		"  worst 48s at 07:28 (…-hx4mt) · usual 350ms · raised 9m of 1h in 3 stretches · 363 samples in 3 series, step 30s",
		"  rule: raised at 3x its window median and at least 50ms above it; always raised at 5s",
		`  query: histogram_quantile(0.99, sum by (le,pod) (rate(http_request_duration_seconds_bucket{namespace="gitlab",pod=~"`,
		"  differs from peers: …-hx4mt worst sample 48s vs peers 350ms",
		"memory [cadvisor] memory in use against the limit, fullest container",
		"restarts [kube-state-metrics] container restarts",
		"peer rule: a Pod differs when its worst sample deviates from its peers'",
	} {
		assert.Contains(t, detail, want)
	}
	assert.Less(t, strings.Index(detail, "throttled [container-cpu]"), strings.Index(detail, "requests [gitlab-webservice]"),
		"the raised signals are listed before context")
	service := strings.Join(report.Detail(report.Hop(slowpath.HopService)), "\n")
	assert.Contains(t, service, "Kubernetes API at 08:20: 3 of 3 EndpointSlice records ready, 3 Pods")
	assert.Contains(t, service, `query: min by (pod) (kube_pod_status_ready{namespace="gitlab",pod=~"`)
	assert.Contains(t, service, `condition="true"`)
}

func TestTheStripForASmallScreenKeepsTheAppHopOnOneLane(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)
	compact := report.CompactLanes()
	var labels []string
	for i := range compact {
		labels = append(labels, compact[i].Label)
	}
	assert.Equal(t, []string{"ingress: 499, p99, +1", "endpoints: not ready …-hx4mt", "pods: p99, queued, +2 …-hx4mt", "3 dependencies", "dns",
		"network: drops …-2bq7k", "restarts", "rollouts, new pods"}, labels)
	assert.Equal(t, []string{"p99", "queued", "free-threads", "throttled"}, compact[2].Names)
	assert.Len(t, report.Lanes, 11, "the full strip is unchanged")

	// The cursor reads the worst value of a column: the largest, or the
	// smallest for a signal that is bad when low.
	free := lane(t, report, "app:free-threads")
	value, ok := free.ValueAt(before(52), before(51))
	require.True(t, ok)
	assert.Zero(t, value)
	queued := lane(t, report, "app:queued")
	value, ok = queued.ValueAt(before(52), before(51))
	require.True(t, ok)
	assert.InDelta(t, 30, value, 1e-9)
	_, ok = queued.ValueAt(before(90), before(80))
	assert.False(t, ok, "no sample, no value")
}

func TestAlertsAndRolloutsAreDatedFactsOnTheirOwnLanes(t *testing.T) {
	path := slowtest.Path()
	path.Rollouts = append(path.Rollouts, slowpath.Event{At: before(34), Label: "rev 2"}, slowpath.Event{At: before(6.5), Label: "rev 3"})
	path.Pods[0].Restarts, path.Pods[0].LastRestart = 3, before(20)
	plan := slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	alerts := &slowpath.Alerts{Read: true, Firing: []slowpath.AlertSpan{{Name: "GitLabSlow", Start: before(6), End: slowtest.Now.Add(time.Hour)}}}
	report := slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), alerts, slowtest.Now)
	rollouts := lane(t, report, "rollouts")
	require.Len(t, rollouts.Marks, 2)
	assert.Equal(t, []string{"rev 2 rolled out"}, rollouts.EventsAt(before(35), before(33)))
	firing := lane(t, report, "alerts")
	assert.Equal(t, []chart.Interval{{Start: before(6), End: slowtest.Now}}, firing.Marks, "a firing alert is clipped to the window")
	restarts := lane(t, report, "restarts")
	assert.Equal(t, []chart.Interval{{Start: before(20), End: before(20)}}, restarts.Marks, "the last restart the API reports")
	text := sentences(report)
	assert.Contains(t, text, "1 of 3 (08:13) overlaps a rollout or new Pod and a firing alert.",
		"lanes that overlap the same episode share a sentence, and the episode is named")
	assert.Contains(t, text, "Restarts on pod …-2bq7k: 08:00, outside every episode; 13m before the 08:13 one.",
		"a dated fact shortly before an episode is said to be before it: order, never cause")

	unread := slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), &slowpath.Alerts{Note: "alertmanager: denied (HTTP 403)"}, slowtest.Now)
	assert.Equal(t, "alertmanager: denied (HTTP 403)", lane(t, unread, "alerts").NotRead)
	assert.Contains(t, sentences(unread), "Not read: alerts.")
}
