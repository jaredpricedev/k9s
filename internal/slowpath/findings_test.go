// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relations is how every lane of a report stands to its episodes.
func relations(report *slowpath.Report) map[string]slowpath.Relation {
	out := map[string]slowpath.Relation{}
	for _, finding := range report.Findings() {
		out[finding.Lane.ID] = finding.Relation
	}
	return out
}

// Which lane lines up with the episodes is interval overlap and nothing
// else: the same measure the summary sentences are computed from.
func TestEveryLaneStandsToTheEpisodesByOverlapAlone(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)
	assert.Equal(t, map[string]slowpath.Relation{
		slowpath.HopIngress: slowpath.LaneSymptom, slowpath.HopService: slowpath.LaneSome,
		"app:p99": slowpath.LaneEvery, "app:queued": slowpath.LaneEvery, "app:free-threads": slowpath.LaneEvery, "app:throttled": slowpath.LaneEvery,
		slowpath.HopDependencies: slowpath.LaneQuiet, slowpath.HopDNS: slowpath.LaneQuiet, slowpath.HopNetwork: slowpath.LaneOutside,
		"restarts": slowpath.LaneQuiet, "rollouts": slowpath.LaneQuiet,
	}, relations(report))
	text := sentences(report)
	for _, finding := range report.Findings() {
		lane := finding.Lane
		assert.Equal(t, slowpath.OverlapCount(report.Episodes, slowpath.Clip(lane.Marks, report.Start, report.End)), finding.Overlapped, lane.ID)
		switch finding.Relation {
		case slowpath.LaneEvery:
			assert.Equal(t, len(report.Episodes), finding.Overlapped, lane.ID)
			assert.Contains(t, text, lane.Phrase, "what overlaps every episode in the summary is what lines up on the page")
		case slowpath.LaneOutside:
			assert.Zero(t, finding.Overlapped, lane.ID)
			assert.Contains(t, text, "outside every episode")
		case slowpath.LaneQuiet:
			assert.Empty(t, finding.Marks, lane.ID)
		}
	}
	// A lane says which hops and which signal are behind it.
	byID := map[string]slowpath.Finding{}
	for _, finding := range report.Findings() {
		byID[finding.Lane.ID] = finding
	}
	require.Len(t, byID[slowpath.HopDependencies].Hops, 3, "the quiet dependencies share a lane")
	assert.Equal(t, "dep:gitlab/gitlab-postgresql", byID[slowpath.HopDependencies].Hops[0].Spec.ID)
	assert.Equal(t, "499", byID[slowpath.HopIngress].Signal.Name, "the lane of a hop names its first raised signal")
	assert.Equal(t, "throttled", byID["app:throttled"].Signal.Name)
	assert.Equal(t, slowpath.SignalReady, byID[slowpath.HopService].Signal.Name)
	assert.Empty(t, byID["restarts"].Hops, "dated facts belong to no hop")
	assert.Nil(t, byID["restarts"].Signal)
	assert.InDelta(t, 48, byID["app:p99"].Signal.PodSeries(slowtest.Outlier).Worst, 0.001)
	assert.Nil(t, byID["app:p99"].Signal.PodSeries("no-such-pod"))

	// Nothing is quiet that was not read, and what the API still knows is
	// a dated fact in the window.
	denied := slowtest.Report(&slowtest.Story{Deny: observability.Status{State: observability.StateDenied, Reason: "HTTP 403", HTTPStatus: 403}},
		slowtest.Sources(), slowtest.Path(), time.Hour)
	assert.Equal(t, map[string]slowpath.Relation{
		slowpath.HopIngress: slowpath.LaneNotRead, slowpath.HopService: slowpath.LaneOutside, slowpath.HopApp: slowpath.LaneNotRead,
		slowpath.HopDependencies + ":unread": slowpath.LaneNotRead, slowpath.HopDNS: slowpath.LaneNotRead, slowpath.HopNetwork: slowpath.LaneNotRead,
		"restarts": slowpath.LaneQuiet, "rollouts": slowpath.LaneQuiet,
	}, relations(denied))
	for _, finding := range denied.Findings() {
		if finding.Lane.ID == slowpath.HopDependencies+":unread" {
			assert.Len(t, finding.Hops, 3, "the dependencies that were not read share a lane too")
		}
		assert.Nil(t, finding.Signal, "%s: a lane that was not read, or a dated fact, names no signal", finding.Lane.ID)
	}
}

// The answer at the top of the page is the episodes and what lines up with
// them, in plain words, and never more than the overlap supports.
func TestTheAnswerStatesTheEpisodesAndWhatLinesUpInPlainWords(t *testing.T) {
	answer := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour).Answer()
	assert.Equal(t, slowpath.AnswerEpisodes, answer.Kind)
	assert.Equal(t, [2]string{"stall", "stalls"}, [2]string{answer.Noun, answer.Nouns}, "a client that gave up waiting is a stall")
	assert.Equal(t, "3 stalls: clients gave up waiting at 07:28, 07:47 and 08:13 UTC, for 2 to 4 minutes each.", answer.Lead)
	assert.Equal(t, "3 stalls: clients gave up waiting, for 2 to 4 minutes each.", answer.Brief, "without the times, for a page that draws them")
	assert.Equal(t, "All 3 line up with slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.", answer.Lines)
	assert.Equal(t, "All 3 line up with 4 signals on pod …-hx4mt.", answer.Counted, "for a page whose rows name the signals")
	assert.Equal(t, []string{"2 of 3 also line up with not Ready on pod …-hx4mt."}, answer.More)
	assert.Equal(t, "Overlap in time, not proof of cause.", answer.Caveat)
	assert.Empty(t, answer.Coarse)
	assert.Empty(t, answer.Differs)
	all := strings.ToLower(strings.Join(append([]string{answer.Lead, answer.Lines, answer.Counted, answer.Caveat}, answer.More...), " "))
	for _, banned := range []string{"because", "caused", "due to", "root cause", "blame", "healthy", "packet drops"} {
		assert.NotContains(t, all, banned, "the burst outside every stall is not in the answer, and nothing names a cause")
	}

	// One episode is "it"; a short window writes seconds.
	short := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), 15*time.Minute).Answer()
	assert.Equal(t, "1 stall: clients gave up waiting at 08:13:00 UTC, for 2 minutes.", short.Lead)
	assert.Equal(t, "It lines up with slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.", short.Lines)
	assert.Equal(t, []string{"Nothing else lines up."}, short.More)

	// A wide window says what it cannot show.
	wide := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), 6*time.Hour).Answer()
	assert.Equal(t, "1 stall: clients gave up waiting at 07:30 UTC, for 55 minutes.", wide.Lead)
	assert.Equal(t, "A sample here stands for 5m and a rate for 20m: a stall of a few minutes merges with the next one or disappears; 1h or 15m shows it.", wide.Coarse)

	// Without an ingress the episodes are those of the next raised signal
	// on the path. They are not called stalls: no client was seen to give
	// up. What was not read is not said to line up or not.
	path := slowtest.Path()
	path.Ingresses = nil
	direct := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), path, time.Hour)
	answer = direct.Answer()
	assert.Equal(t, [2]string{"episode", "episodes"}, [2]string{answer.Noun, answer.Nouns})
	assert.Equal(t, "2 episodes of not Ready on pod …-hx4mt at 07:29 and 07:48 UTC, for 2 to 3 minutes each.", answer.Lead)
	assert.Equal(t, "2 episodes of not Ready on pod …-hx4mt, for 2 to 3 minutes each.", answer.Brief)
	assert.Equal(t, "Both line up with slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.", answer.Lines)
	assert.Equal(t, []string{"Nothing else that was read lines up."}, answer.More, "the ingress was not read")
	assert.Equal(t, slowpath.LaneNotRead, relations(direct)[slowpath.HopIngress])
}

// answerAt is the answer of the story read over a window that ends some
// time before the story does.
func answerAt(t *testing.T, back, window time.Duration) slowpath.Answer {
	t.Helper()
	now := slowtest.Now.Add(-back)
	plan := slowpath.BuildPlan(slowtest.Sources(), slowtest.Path(), window, now)
	return slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), nil, now).Answer()
}

// A window can cut an episode: begin while it is under way, or end before
// it is over. What is stated of it is then only what the window holds, and
// the answer says so instead of stating a length it did not see.
func TestTheAnswerSaysWhenTheWindowCutsAnEpisode(t *testing.T) {
	// The window holds every stall whole: nothing to add.
	whole := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour).Answer()
	assert.Empty(t, whole.Edge)

	// The second stall of the story is 07:47 to 07:51; this window begins
	// at 07:49.
	open := answerAt(t, 16*time.Minute, 15*time.Minute)
	assert.Equal(t, "1 stall: clients gave up waiting at 07:49:15 UTC, for at least 1m45s.", open.Lead)
	assert.Equal(t, "1 stall: clients gave up waiting, for at least 1m45s.", open.Brief)
	assert.Equal(t, "It was already raised when the window began.", open.Edge)

	// The third is 08:13 to 08:15; this window ends at 08:14. The length is
	// what was seen and is not called a least one: a rate stays raised while
	// a burst leaves its window.
	ongoing := answerAt(t, 6*time.Minute, 15*time.Minute)
	assert.Equal(t, "1 stall: clients gave up waiting at 08:13:15 UTC, for 1 minute.", ongoing.Lead)
	assert.Equal(t, "It was still raised at the last sample: it may not be over.", ongoing.Edge)

	// Of several, the sentence names the one that is cut.
	last := answerAt(t, 6*time.Minute, time.Hour)
	assert.Equal(t, "3 stalls: clients gave up waiting at 07:28, 07:47 and 08:13 UTC, for 1m30s to 4m each.", last.Lead)
	assert.Equal(t, "The last was still raised at the last sample: it may not be over.", last.Edge)
	first := answerAt(t, -3*time.Minute, 35*time.Minute)
	assert.Regexp(t, `^2 stalls: clients gave up waiting at 07:48 and 08:13 UTC, for .+ each\.$`, first.Lead)
	assert.Equal(t, "The first was already raised when the window began: it may be longer.", first.Edge)
	both := answerAt(t, 6*time.Minute, 26*time.Minute)
	assert.Equal(t, "The first was already raised when the window began and may be longer; the last was still raised at the last sample and may not be over.", both.Edge)

	// A wide window that ends in a rate that is still raised says so too.
	wide := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), 6*time.Hour).Answer()
	assert.Equal(t, "It was still raised at the last sample: it may not be over.", wide.Edge)

	for _, answer := range []slowpath.Answer{open, ongoing, last, first, both, wide} {
		for _, banned := range []string{"because", "caused", "due to"} {
			assert.NotContains(t, strings.ToLower(answer.Lead+" "+answer.Edge), banned)
		}
	}
}

// Lanes that overlap the same episodes share a sentence; a dated fact
// outside every episode is in none.
func TestTheAnswerNeverJoinsLanesThatOverlapDifferentEpisodes(t *testing.T) {
	path := slowtest.Path()
	path.Rollouts = append(path.Rollouts, slowpath.Event{At: before(34), Label: "rev 2"}, slowpath.Event{At: before(6.5), Label: "rev 3"})
	path.Pods[0].Restarts, path.Pods[0].LastRestart = 3, before(20)
	plan := slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	alerts := &slowpath.Alerts{Read: true, Firing: []slowpath.AlertSpan{{Name: "GitLabSlow", Start: before(6), End: slowtest.Now.Add(time.Hour)}}}
	report := slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), alerts, slowtest.Now)
	answer := report.Answer()
	assert.Equal(t, []string{
		"2 of 3 also line up with not Ready on pod …-hx4mt.",
		"1 of 3 also lines up with a rollout or new Pod and a firing alert.",
	}, answer.More)
	got := relations(report)
	assert.Equal(t, slowpath.LaneSome, got["rollouts"])
	assert.Equal(t, slowpath.LaneSome, got["alerts"])
	assert.Equal(t, slowpath.LaneOutside, got["restarts"], "a restart between two stalls lines up with neither")

	// The dated facts of a lane are stated in full for the detail area.
	assert.Equal(t, []string{
		"rollouts, new pods · retained revisions of the workload and Pods created in the window, from the Kubernetes API",
		"  07:46 UTC  rev 2 rolled out", "  08:13 UTC  rev 3 rolled out",
	}, report.LaneDetail(lane(t, report, "rollouts")))
	assert.Equal(t, []string{"alerts firing · the alerts of the namespace, from the declared Alertmanager", "  08:14 UTC  GitLabSlow"},
		report.LaneDetail(lane(t, report, "alerts")))
	quiet := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)
	assert.Equal(t, "  none in the last 1h", quiet.LaneDetail(lane(t, quiet, "restarts"))[1])
	unread := slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), &slowpath.Alerts{Note: "alertmanager: denied (HTTP 403)"}, slowtest.Now)
	assert.Equal(t, "  not read: alertmanager: denied (HTTP 403)", unread.LaneDetail(lane(t, unread, "alerts"))[1])
}

// A window without an episode is quiet for what was read, or was not read.
// Neither is health, and a Pod that differs from its peers all window is
// still a finding.
func TestTheAnswerOfAWindowWithoutEpisodesSaysWhatWasReadAndNoMore(t *testing.T) {
	quiet := slowtest.Report(&slowtest.Story{Quiet: true, Absent: []string{"hubble_"}}, slowtest.Sources(), quietPath(), time.Hour).Answer()
	assert.Equal(t, slowpath.AnswerQuiet, quiet.Kind)
	assert.Equal(t, "No signal that was read crossed its rule in the last 1h.", quiet.Lead)
	assert.Empty(t, quiet.Lines)
	assert.Empty(t, quiet.More)
	assert.Empty(t, quiet.Caveat, "the page adds how much was read and that nothing found is not health")

	denied := slowtest.Report(&slowtest.Story{Deny: observability.Status{State: observability.StateDenied, Reason: "HTTP 403", HTTPStatus: 403}},
		slowtest.Sources(), slowtest.Path(), time.Hour).Answer()
	assert.Equal(t, slowpath.AnswerNotRead, denied.Kind)
	assert.Equal(t, "Prometheus was not read (denied: HTTP 403), so no hop has latency evidence.", denied.Lead)

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
	answer := report.Answer()
	assert.Equal(t, slowpath.AnswerQuiet, answer.Kind)
	assert.Equal(t, []string{"Pod …-hx4mt usually differs from its peers: p99 2s (peers 305ms)."}, answer.Differs)
	assert.Equal(t, "A difference, not proof of cause.", answer.Caveat)
	usuals := report.Usuals()
	require.Len(t, usuals, 1)
	assert.Equal(t, "p99", usuals[0].Signal.Name)
	assert.Equal(t, slowpath.Deviation{Pod: slowtest.Outlier, Value: 2, Peers: 0.305, Sustained: true}, usuals[0].Deviation)
	assert.Empty(t, slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour).Usuals(),
		"a Pod that differs only while it stalls is on the rows that line up")
}

// The detail of a hop can state one signal first: the one a row is about.
func TestDetailStatesTheSignalOfARowFirst(t *testing.T) {
	report := slowtest.Report(&slowtest.Story{}, slowtest.Sources(), slowtest.Path(), time.Hour)
	app := report.Hop(slowpath.HopApp)
	first := report.DetailOf(app, "throttled")
	require.Greater(t, len(first), 2)
	assert.Equal(t, report.Detail(app)[0], first[0], "the hop is named first either way")
	assert.True(t, strings.HasPrefix(first[1], "throttled [container-cpu]"), first[1])
	assert.ElementsMatch(t, report.Detail(app), first, "nothing is added or left out")
	assert.Equal(t, report.Detail(app), report.DetailOf(app, ""), "without a signal the order is the one of Detail")
	assert.Equal(t, report.Detail(app), report.DetailOf(app, "no-such-signal"))
}
