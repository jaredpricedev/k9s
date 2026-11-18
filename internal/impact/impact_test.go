// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
)

var rolloutAt = time.Date(2026, 11, 17, 4, 20, 0, 0, time.UTC)

func windows(t *testing.T, width time.Duration) *Windows {
	t.Helper()
	w, err := PlanWindows(rolloutAt, time.Time{}, time.Time{}, rolloutAt.Add(6*time.Hour), width)
	require.NoError(t, err)
	return &w
}

// series samples fn every step across both windows; a NaN is no sample.
func series(w *Windows, step time.Duration, fn func(offset time.Duration) float64) []Point {
	var out []Point
	for at := w.Before.Start; !at.After(w.After.End); at = at.Add(step) {
		if v := fn(at.Sub(w.Rollout)); !math.IsNaN(v) {
			out = append(out, Point{T: at, V: v})
		}
	}
	return out
}

func stepped(before, after float64) func(time.Duration) float64 {
	return func(offset time.Duration) float64 {
		if offset <= 0 {
			return before
		}
		return after
	}
}

func TestPlanWindowsAreEqualAndClippedToWhatExists(t *testing.T) {
	now := rolloutAt.Add(12*time.Minute + 300*time.Millisecond)
	w, err := PlanWindows(rolloutAt, time.Time{}, time.Time{}, now, 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 12*time.Minute, w.Width)
	require.Equal(t, 30*time.Minute, w.Asked)
	require.Equal(t, "the rollout was 12m before this read", w.Clipped)
	require.Equal(t, rolloutAt.Add(-12*time.Minute), w.Before.Start)
	require.Equal(t, rolloutAt, w.Before.End)
	require.Equal(t, rolloutAt, w.After.Start)
	require.Equal(t, rolloutAt.Add(12*time.Minute), w.After.End)

	w, err = PlanWindows(rolloutAt, time.Time{}, time.Time{}, rolloutAt.Add(5*time.Hour), 0)
	require.NoError(t, err)
	require.Equal(t, DefaultWidth, w.Width)
	require.Empty(t, w.Clipped)

	// A later rollout ends the after window; an earlier one bounds the
	// before window, and both windows stay equal.
	w, err = PlanWindows(rolloutAt, rolloutAt.Add(-48*time.Hour), rolloutAt.Add(20*time.Minute), rolloutAt.Add(5*time.Hour), time.Hour)
	require.NoError(t, err)
	require.Equal(t, 20*time.Minute, w.Width)
	require.Equal(t, "the next rollout came 20m later", w.Clipped)
	w, err = PlanWindows(rolloutAt, rolloutAt.Add(-7*time.Minute), time.Time{}, rolloutAt.Add(5*time.Hour), time.Hour)
	require.NoError(t, err)
	require.Equal(t, 7*time.Minute, w.Width)
	require.Equal(t, w.After.End.Sub(w.After.Start), w.Before.End.Sub(w.Before.Start))
	require.Contains(t, w.Clipped, "previous rollout was only 7m earlier")

	_, err = PlanWindows(rolloutAt, time.Time{}, time.Time{}, rolloutAt.Add(20*time.Second), DefaultWidth)
	require.ErrorContains(t, err, "only 20s can be compared")
	_, err = PlanWindows(rolloutAt, time.Time{}, time.Time{}, rolloutAt.Add(-time.Minute), DefaultWidth)
	require.ErrorContains(t, err, "later than this machine's clock")
	_, err = PlanWindows(time.Time{}, time.Time{}, time.Time{}, rolloutAt, DefaultWidth)
	require.ErrorContains(t, err, "not known")
}

func TestGaugeReductionSplitsAtTheRolloutAndSkipsBlendedRateSamples(t *testing.T) {
	w := windows(t, 10*time.Minute)
	points := series(w, time.Minute, stepped(10, 30))
	plain := reduceGauge(points, w, 0, ReduceMean)
	require.Equal(t, Side{Value: 10, Samples: 11, Valid: true}, plain.before, "the sample at the rollout instant describes the time before it")
	require.Equal(t, Side{Value: 30, Samples: 10, Valid: true}, plain.after)
	require.Zero(t, plain.skipped)

	guarded := reduceGauge(points, w, 2*time.Minute, ReduceMean)
	require.Equal(t, 11, guarded.before.Samples)
	require.Equal(t, 9, guarded.after.Samples, "the sample one minute after the rollout still averages over it")
	require.Equal(t, 1, guarded.skipped)

	saw := series(w, time.Minute, func(offset time.Duration) float64 { return float64(int(offset.Minutes())%4) + 5 })
	require.InDelta(t, 8, reduceGauge(saw, w, 0, ReduceMax).after.Value, 1e-9)
	require.InDelta(t, 5, reduceGauge(saw, w, 0, ReduceMin).after.Value, 1e-9)

	empty := reduceGauge(nil, w, 0, ReduceMean)
	require.False(t, empty.before.Valid)
	require.False(t, empty.after.Valid)
}

func TestIncreaseCountsPositiveStepsAndIgnoresResets(t *testing.T) {
	w := windows(t, 10*time.Minute)
	// Flat at 3 before; after the rollout the summed counter drops when an
	// old pod goes away, then climbs by six.
	points := series(w, time.Minute, func(offset time.Duration) float64 {
		minutes := int(offset.Minutes())
		switch {
		case minutes <= 0:
			return 3
		case minutes <= 2:
			return 0
		default:
			return math.Min(float64(minutes-2), 6)
		}
	})
	got := reduceIncrease(points, w)
	require.InDelta(t, 0, got.before.Value, 1e-9)
	require.InDelta(t, 6, got.after.Value, 1e-9)
	require.Equal(t, 11, got.before.Samples)
	require.Equal(t, 10, got.after.Samples)
	require.Len(t, got.series, 21)
}

func TestRatioWeightsByVolumeAndNeverInventsAValue(t *testing.T) {
	w := windows(t, 10*time.Minute)
	requests := series(w, time.Minute, func(offset time.Duration) float64 {
		if offset > 5*time.Minute {
			return 100
		}
		return 10
	})
	errors := series(w, time.Minute, func(offset time.Duration) float64 {
		switch {
		case offset <= 0:
			return math.NaN() // no error series yet: zero errors, because requests exist
		case offset > 5*time.Minute:
			return 50
		default:
			return 0
		}
	})
	got := reduceRatio(errors, requests, w, 0)
	require.True(t, got.before.Valid)
	require.Zero(t, got.before.Value)
	// 5 quiet steps with no errors and 5 busy steps at 50%: weighted by
	// volume this is 250/550, not the 25% an average of ratios would give.
	require.InDelta(t, 250.0/550.0, got.after.Value, 1e-9)
	require.Equal(t, 10, got.after.Samples)

	silent := reduceRatio(nil, series(w, time.Minute, stepped(5, 0)), w, 0)
	require.True(t, silent.before.Valid)
	require.False(t, silent.after.Valid, "no requests means no ratio, not a ratio of zero")
	require.Zero(t, silent.after.Samples)
}

func TestQuantileInterpolatesLikePrometheus(t *testing.T) {
	inf := math.Inf(1)
	buckets := []Bucket{{0.1, 80}, {0.05, 55}, {0.25, 96}, {0.5, 99}, {1, 99.8}, {2.5, 100}, {inf, 100}}
	p50, ok := Quantile(0.5, buckets)
	require.True(t, ok)
	require.InDelta(t, 0.05*50/55, p50, 1e-9, "the first bucket interpolates from zero")
	p95, _ := Quantile(0.95, buckets)
	require.InDelta(t, 0.1+0.15*(95-80)/16, p95, 1e-9)
	p985, _ := Quantile(0.985, buckets)
	require.InDelta(t, 0.25+0.25*(98.5-96)/3, p985, 1e-9)

	top, ok := Quantile(0.99, []Bucket{{0.1, 10}, {1, 20}, {inf, 100}})
	require.True(t, ok)
	require.InDelta(t, 1, top, 1e-9, "a rank in the +Inf bucket reports the highest finite bound")

	for name, bad := range map[string][]Bucket{
		"no +Inf bucket":  {{0.1, 1}, {1, 2}},
		"no observations": {{0.1, 0}, {inf, 0}},
		"one bucket":      {{inf, 4}},
	} {
		_, ok := Quantile(0.5, bad)
		require.False(t, ok, name)
	}
}

func TestQuantileReductionSumsBucketRatesPerWindow(t *testing.T) {
	w := windows(t, 10*time.Minute)
	inf := math.Inf(1)
	shape := func(fast, slow float64) map[float64]func(time.Duration) float64 {
		return map[float64]func(time.Duration) float64{
			0.1: stepped(fast, slow), 1: stepped(100, 60), 5: stepped(100, 100), inf: stepped(100, 100),
		}
	}
	buckets := map[float64][]Point{}
	for le, fn := range shape(90, 10) {
		buckets[le] = series(w, time.Minute, fn)
	}
	got := reduceQuantile(0.95, buckets, w, 2*time.Minute)
	require.InDelta(t, 0.1+0.9*(95-90)/10, got.before.Value, 1e-9)
	require.InDelta(t, 1+4*(95-60)/40.0, got.after.Value, 1e-9)
	require.Equal(t, 11, got.before.Samples)
	require.Equal(t, 9, got.after.Samples)
	require.Equal(t, 1, got.skipped)
	require.Len(t, got.series, 21)
}

func TestThresholdNeedsBothTheFloorAndTheRelativeChange(t *testing.T) {
	signal := func(id string) *Signal {
		for index := range Signals {
			if Signals[index].ID == id {
				return &Signals[index]
			}
		}
		t.Fatal(id)
		return nil
	}
	errors := signal(SigErrors)
	require.Equal(t, Unchanged, compare(errors, 0.0020, 0.0025), "under the 0.1pp floor")
	require.Equal(t, Unchanged, compare(errors, 0.20, 0.215), "over the floor, under 10% of before")
	require.Equal(t, Worse, compare(errors, 0.002, 0.141))
	require.Equal(t, Better, compare(errors, 0.141, 0.002))
	require.Equal(t, Worse, compare(errors, 0, 0.001), "exactly the floor counts")

	ready := signal(SigReady)
	require.Equal(t, Worse, compare(ready, 2, 1))
	require.Equal(t, Better, compare(ready, 1, 2))
	require.Equal(t, Unchanged, compare(ready, 2, 2))

	requests := signal(SigRequests)
	require.Equal(t, Higher, compare(requests, 40, 60))
	require.Equal(t, Lower, compare(requests, 40, 12))
	require.Equal(t, Unchanged, compare(requests, 40, 42.7))
	require.Contains(t, errors.Rule(), "from 0.1pp and 10% of before; needs 4 samples on each side")
	require.Contains(t, signal(SigP99).Rule(), "from 5ms and")
}

func TestValuesAreWrittenInTheirOwnUnits(t *testing.T) {
	format := func(id string, v float64) string {
		for index := range Signals {
			if Signals[index].ID == id {
				return Signals[index].Format(v)
			}
		}
		return ""
	}
	require.Equal(t, "40.1/s", format(SigRequests, 40.08))
	require.Equal(t, "0.2%", format(SigErrors, 0.002))
	require.Equal(t, "14.1%", format(SigErrors, 0.141))
	require.Equal(t, "0%", format(SigErrors, 0))
	require.Equal(t, "<0.01%", format(SigErrors, 0.00001))
	require.Equal(t, "180ms", format(SigP99, 0.18))
	require.Equal(t, "2.4s", format(SigP99, 2.4))
	require.Equal(t, "850µs", format(SigP99, 0.00085))
	require.Equal(t, "254Mi", format(SigMemory, 254*1024*1024))
	require.Equal(t, "1.5Gi", format(SigMemory, 1.5*1024*1024*1024))
	require.Equal(t, "340m", format(SigCPU, 0.34))
	require.Equal(t, "1.25", format(SigCPU, 1.25))
	require.Equal(t, "6", format(SigRestarts, 6))

	require.Equal(t, "+13.9pp", Signals[1].FormatDelta(0.139))
	require.Equal(t, "-1", Signals[10].FormatDelta(-1))
	require.Equal(t, "+7%", PercentChange(40, 42.8))
	require.Equal(t, "-50%", PercentChange(2, 1))
	require.Equal(t, "x71", PercentChange(0.002, 0.142))
	require.Equal(t, "new", PercentChange(0, 6))
	require.Equal(t, "0%", PercentChange(2, 2))
	require.Equal(t, "+0.2%", PercentChange(1000, 1002))
	require.Equal(t, "12m", Dur(12*time.Minute))
	require.Equal(t, "1h30m", Dur(90*time.Minute))
	require.Equal(t, "2m30s", Dur(150*time.Second))
}

func TestSparkSharesOneScaleAndLeavesGapsEmpty(t *testing.T) {
	w := windows(t, 8*time.Minute)
	points := series(w, time.Minute, func(offset time.Duration) float64 {
		switch {
		case offset <= -6*time.Minute:
			return math.NaN()
		case offset <= 0:
			return 1
		default:
			return 8
		}
	})
	before, after := Spark(points, w, 4, false)
	require.Equal(t, []int{-1, 1, 1, 1}, before, "a cell with no sample is a gap, not a low value")
	require.Equal(t, []int{7, 7, 7, 7}, after)
	before, after = Spark(series(w, time.Minute, stepped(0, 0)), w, 4, false)
	require.Equal(t, []int{0, 0, 0, 0}, before)
	require.Equal(t, []int{0, 0, 0, 0}, after)
	before, after = Spark(nil, w, 0, false)
	require.Nil(t, before)
	require.Nil(t, after)
}

// fakeSource answers range queries from generators keyed by a fragment of
// the query text, sampling them at the step the client would use.
type fakeSource struct {
	answers map[string]func(offset time.Duration) float64
	buckets map[string]func(offset time.Duration) float64
	status  observability.Status
	// Collect reads several templates at once; mu guards queries.
	mu      sync.Mutex
	queries []string
}

func (f *fakeSource) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	query := e.String()
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	result := observability.PromResult{Status: observability.Status{State: observability.StateComplete}, Query: query,
		Start: start, End: end, Step: observability.StepFor(end.Sub(start))}
	if f.status.State != "" {
		result.Status = f.status
		return result
	}
	sample := func(fn func(time.Duration) float64) []observability.Sample {
		var out []observability.Sample
		for at := start; !at.After(end); at = at.Add(result.Step) {
			if v := fn(at.Sub(rolloutAt)); !math.IsNaN(v) {
				out = append(out, observability.Sample{Timestamp: at, Value: v})
			}
		}
		return out
	}
	if strings.Contains(query, "_bucket") {
		for le, fn := range f.buckets {
			result.Series = append(result.Series, observability.PromSeries{Labels: map[string]string{"le": le}, Samples: sample(fn)})
		}
		return result
	}
	// The longest matching fragment wins, and the error selector always
	// beats the plain request selector it extends.
	best, bestScore := "", 0
	for fragment := range f.answers {
		score := len(fragment)
		if strings.Contains(fragment, "code=~") {
			score += 1000
		}
		if strings.Contains(query, fragment) && score > bestScore {
			best, bestScore = fragment, score
		}
	}
	if best != "" {
		if samples := sample(f.answers[best]); len(samples) > 0 {
			result.Series = []observability.PromSeries{{Labels: map[string]string{}, Samples: samples}}
		}
	}
	return result
}

func checkoutStory() *fakeSource {
	return &fakeSource{
		answers: map[string]func(time.Duration) float64{
			"http_requests_total": stepped(40, 42),
			`code=~"5.."`:         stepped(0.08, 5.922),
			"kube_pod_container_status_restarts_total": func(offset time.Duration) float64 { return math.Max(0, math.Min(6, math.Floor(offset.Minutes()/2))) },
			"max(container_memory_working_set_bytes":   stepped(188<<20, 254<<20),
			"kube_pod_container_resource_limits":       stepped(0.367, 0.992),
			"container_cpu_usage_seconds_total":        stepped(0.06, 0.34),
			"kube_deployment_status_replicas_available": func(offset time.Duration) float64 {
				if offset <= time.Minute {
					return 2
				}
				return 1
			},
		},
		buckets: map[string]func(time.Duration) float64{
			"0.1": stepped(30, 2), "0.25": stepped(39.7, 10), "0.5": stepped(40, 20), "2.5": stepped(40, 41.6), "5": stepped(40, 42), "+Inf": stepped(40, 42),
		},
	}
}

func checkoutRequest(t *testing.T) *Request {
	t.Helper()
	w, err := PlanWindows(rolloutAt, time.Time{}, time.Time{}, rolloutAt.Add(12*time.Minute), DefaultWidth)
	require.NoError(t, err)
	previous := Revision{Number: 1, Name: "checkout-api-6c8d7f9b5", Created: rolloutAt.Add(-72 * time.Hour), Template: template("v1", "512Mi", "token-v1", false)}
	return &Request{
		Workload: Workload{Kind: KindDeployment, Namespace: "apps", Name: "checkout-api"},
		Rollout: Rollout{Revision: Revision{Number: 2, Name: "checkout-api-7d9f8c6b5d", Created: rolloutAt, Template: template("v2", "256Mi", "token-v2", true)},
			Previous: &previous, Total: 2},
		Windows: w, Source: "service monitoring/prometheus:9090",
	}
}

func TestCollectStatesWhatWasMeasuredAfterTheRolloutAndNoCause(t *testing.T) {
	source := checkoutStory()
	report := Collect(t.Context(), source, checkoutRequest(t))
	require.WithinDuration(t, time.Now(), report.ReadAt, time.Minute, "the read is stamped with the wall clock")
	// The sentence names the day of a rollout that was not today, so the
	// read is pinned to the day of the rollout: the text does not depend
	// on the day the test runs.
	report.ReadAt = rolloutAt.Add(13 * time.Minute)
	require.Len(t, source.queries, 9, "one bounded range read per query template")
	require.Equal(t, 15*time.Second, report.Step)
	require.Equal(t, 2*time.Minute, report.RateWindow)
	require.Len(t, report.Outcomes, len(Signals))

	words := map[string]string{}
	for index := range report.Outcomes {
		words[report.Outcomes[index].Signal.ID] = report.Outcomes[index].Word()
	}
	require.Equal(t, map[string]string{
		SigRequests: Unchanged, SigErrors: Worse, SigP50: Worse, SigP95: Worse, SigP99: Worse, SigRestarts: Worse,
		SigMemory: Higher, SigMemLimit: Worse, SigCPU: Higher, SigThrottled: NotAvailable, SigReady: Worse,
	}, words)

	errors := report.Outcome(SigErrors)
	require.InDelta(t, 0.002, errors.Before.Value, 1e-9)
	require.InDelta(t, 0.141, errors.After.Value, 1e-9)
	require.Equal(t, 7, errors.Skipped, "after-samples inside the 2m rate window are left out")
	require.Len(t, errors.Queries, 2)
	require.Equal(t, 6.0, report.Outcome(SigRestarts).After.Value)
	require.Zero(t, report.Outcome(SigRestarts).Before.Value)
	throttled := report.Outcome(SigThrottled)
	require.Equal(t, "no series matched (the source answered)", throttled.Reason)
	require.False(t, throttled.Before.Valid, "a missing metric has no value; it is not zero")

	require.Equal(t, "After rev 2 (04:20): error ratio 0.2% -> 14.1%, p99 248ms -> 2.5s, restarts 0 -> 6, "+
		"memory at limit (limit 512Mi -> 256Mi), ready replicas 2 -> 1.", report.Summary())
	require.Equal(t, "not available: CPU throttled", report.Uncompared())
	later := *report
	later.ReadAt = rolloutAt.Add(26 * time.Hour)
	require.True(t, strings.HasPrefix(later.Summary(), "After rev 2 (Nov17 04:20): "), "read on a later day, the rollout carries its date")
	require.Equal(t, []string{"image v1 -> v2", "limits.memory 512Mi -> 256Mi", "env ~PAYMENT_TOKEN +FEATURE_FLAGS"}, report.Changes)
	require.Empty(t, report.Traffic)
	for _, text := range append([]string{report.Summary(), report.Uncompared()}, report.Changes...) {
		lower := strings.ToLower(text)
		for _, banned := range []string{"because", "caused", "due to", "synthetic", "token-v"} {
			require.NotContains(t, lower, banned)
		}
	}
}

func TestCollectReportsDeniedTooFewSamplesAndTrafficShiftsHonestly(t *testing.T) {
	denied := &fakeSource{status: observability.Status{State: observability.StateDenied, HTTPStatus: 403}}
	report := Collect(t.Context(), denied, checkoutRequest(t))
	report.ReadAt = rolloutAt.Add(13 * time.Minute)
	require.Equal(t, "denied (HTTP 403)", report.Failure)
	require.Equal(t, "After rev 2 (04:20): nothing was read from the source: denied (HTTP 403). No comparison was made.", report.Summary())
	for index := range report.Outcomes {
		require.Equal(t, NotRead, report.Outcomes[index].State)
		require.False(t, report.Outcomes[index].After.Valid)
	}

	// Series that only begin after the rollout have nothing to compare with.
	fresh := checkoutStory()
	for fragment, fn := range fresh.answers {
		fresh.answers[fragment] = func(offset time.Duration) float64 {
			if offset < 0 {
				return math.NaN()
			}
			return fn(offset)
		}
	}
	report = Collect(t.Context(), fresh, checkoutRequest(t))
	memory := report.Outcome(SigMemory)
	require.Equal(t, TooFewSamples, memory.State)
	require.Equal(t, "before has 1 of the 4 samples needed", memory.Reason)
	require.True(t, memory.After.Valid, "the side that was read is still shown")

	// No requests after the rollout: there is no error ratio, and the
	// traffic change is called out rather than a ratio of zero invented.
	quiet := checkoutStory()
	quiet.answers["http_requests_total"] = stepped(40, 0)
	quiet.answers[`code=~"5.."`] = stepped(0.08, 0)
	report = Collect(t.Context(), quiet, checkoutRequest(t))
	require.Equal(t, TooFewSamples, report.Outcome(SigErrors).State)
	require.Contains(t, report.Outcome(SigErrors).Reason, "after has 0 of the 4 samples needed")

	halved := checkoutStory()
	halved.answers["http_requests_total"] = stepped(40, 12)
	report = Collect(t.Context(), halved, checkoutRequest(t))
	require.Equal(t, "Request rate 40/s -> 12/s (-70%): error ratio and latency are not like-for-like.", report.Traffic)
	require.Contains(t, report.Summary(), "not like-for-like")
	require.Contains(t, report.Outcome(SigP99).Notes[0], "not like-for-like")

	healthy := &fakeSource{answers: map[string]func(time.Duration) float64{"http_requests_total": stepped(40, 41), `code=~"5.."`: stepped(4, 0.041)}}
	report = Collect(t.Context(), healthy, checkoutRequest(t))
	require.Contains(t, report.Summary(), "no compared signal is worse by the stated thresholds (2 compared, 1 better, 9 not compared); better: error ratio 10.0% -> 0.1%.")

	none := Collect(t.Context(), &fakeSource{}, checkoutRequest(t))
	require.Contains(t, none.Summary(), "no signal could be compared. That is missing data, not a sign that nothing changed.")
	require.Empty(t, none.Failure, "an answer that matched nothing is not a failed read")

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	unread := Unread(checkoutRequest(t), "no Prometheus source is declared")
	require.Contains(t, unread.Summary(), "nothing was read from the source: no Prometheus source is declared")
	require.NotEmpty(t, unread.Changes)
	require.NotNil(t, Collect(canceled, checkoutStory(), checkoutRequest(t)))
}

func TestQueriesAreFixedTemplatesWithEscapedValuesAndMappedNames(t *testing.T) {
	hostile := Workload{Kind: KindDeployment, Namespace: `apps"}`, Name: `api.v1"} or vector(1)`}
	queries, how := Queries(hostile, nil, 2*time.Minute)
	require.Len(t, queries, 9)
	for key, query := range queries {
		require.NoError(t, query.Err(), key)
		require.NotContains(t, query.String(), `v1"}`, "a quote in a name stays escaped and cannot close the matcher: "+key)
	}
	require.Equal(t, `sum(rate(http_requests_total{namespace="apps\"}",pod=~"api\\.v1\"\\} or vector\\(1\\)-[a-z0-9]+-[a-z0-9]+"}[120s]))`,
		queries[qRequests].String())
	require.Contains(t, how, "pod=~")

	workload := Workload{Kind: KindStatefulSet, Namespace: "apps", Name: "ledger"}
	mapped, how := Queries(workload, &observability.RED{
		Requests: "nginx_ingress_controller_requests", CodeLabel: "status", ErrorCodes: "5..|429",
		DurationBuckets: "request_seconds_bucket", NamespaceLabel: "exported_namespace", WorkloadLabel: "service",
	}, 5*time.Minute)
	require.Equal(t, `service="ledger"`, how)
	require.Equal(t, `sum(rate(nginx_ingress_controller_requests{exported_namespace="apps",service="ledger",status=~"5..|429"}[300s]))`,
		mapped[qErrors].String())
	require.Equal(t, `sum by (le) (rate(request_seconds_bucket{exported_namespace="apps",service="ledger"}[300s]))`, mapped[qBuckets].String())
	require.Equal(t, `sum(kube_pod_container_status_restarts_total{namespace="apps",pod=~"ledger-[0-9]+"})`, mapped[qRestarts].String())
	require.Equal(t, `max(kube_statefulset_status_replicas_ready{namespace="apps",statefulset="ledger"})`, mapped[qReady].String())
	require.Contains(t, mapped[qMemLimit].String(), `kube_pod_container_resource_limits{namespace="apps",pod=~"ledger-[0-9]+",resource="memory"}`)

	daemon, _ := Queries(Workload{Kind: KindDaemonSet, Namespace: "kube-system", Name: "node-agent"}, nil, 2*time.Minute)
	require.Contains(t, daemon[qReady].String(), `kube_daemonset_status_number_ready{namespace="kube-system",daemonset="node-agent"}`)
	require.Contains(t, daemon[qCPU].String(), `pod=~"node-agent-[a-z0-9]+",container!="",container!="POD"`)

	bad, _ := Queries(workload, &observability.RED{Requests: "bad metric"}, 2*time.Minute)
	require.Error(t, bad[qRequests].Err())
	require.Error(t, (&observability.RED{Requests: "bad metric"}).Validate())
	require.Error(t, (&observability.RED{WorkloadLabel: "a-b"}).Validate())
	require.Error(t, (&observability.RED{ErrorCodes: "5(.."}).Validate())
	require.NoError(t, (*observability.RED)(nil).Validate())
	require.Equal(t, 2*time.Minute, RateWindowFor(15*time.Second))
	require.Equal(t, 20*time.Minute, RateWindowFor(5*time.Minute))
}
