// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
	"math"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)

// at is t0 plus minutes.
func at(minutes float64) time.Time { return t0.Add(time.Duration(minutes * float64(time.Minute))) }

func span(from, to float64) chart.Interval { return chart.Interval{Start: at(from), End: at(to)} }

func TestJoinMergesTouchingStretchesAndThoseWithinTheGap(t *testing.T) {
	marks := []chart.Interval{span(10, 11), span(3, 4), span(4, 5), span(5.5, 6), span(20, 21)}
	assert.Equal(t, []chart.Interval{span(3, 5), span(5.5, 6), span(10, 11), span(20, 21)}, slowpath.Normalize(marks),
		"touching stretches are one; a gap keeps them apart")
	assert.Equal(t, []chart.Interval{span(3, 6), span(10, 11), span(20, 21)}, slowpath.Join(marks, time.Minute),
		"two bursts a step apart are one episode")
	assert.Nil(t, slowpath.Join(nil, time.Minute))
	assert.Equal(t, 4*time.Minute, slowpath.Total([]chart.Interval{span(3, 5), span(4, 6), span(10, 11)}), "overlapping time is counted once")
	assert.Equal(t, []chart.Interval{span(0, 5), span(10, 11)}, slowpath.Union([]chart.Interval{span(0, 3), span(10, 11)}, []chart.Interval{span(2, 5)}))
}

func TestOverlapIsSharedTimeAndNothingElse(t *testing.T) {
	moment := func(m float64) chart.Interval { return chart.Interval{Start: at(m), End: at(m)} }
	for _, c := range []struct {
		name string
		a, b chart.Interval
		want bool
	}{
		{"two stretches that share a minute", span(1, 3), span(2, 4), true},
		{"one inside the other", span(1, 10), span(4, 5), true},
		{"stretches that only touch", span(1, 2), span(2, 3), false},
		{"stretches a minute apart", span(1, 2), span(3, 4), false},
		{"a moment inside a stretch", moment(2), span(1, 3), true},
		{"a moment at the start of a stretch", span(1, 3), moment(1), true},
		{"a moment at the end of a stretch", moment(3), span(1, 3), false},
		{"the same moment", moment(2), moment(2), true},
		{"two different moments", moment(2), moment(2.5), false},
	} {
		assert.Equal(t, c.want, slowpath.Overlap(c.a, c.b), c.name)
		assert.Equal(t, c.want, slowpath.Overlap(c.b, c.a), c.name+", the other way round")
	}
	episodes := []chart.Interval{span(8, 11), span(27, 31), span(53, 55)}
	assert.Equal(t, 3, slowpath.OverlapCount(episodes, []chart.Interval{span(8, 9), span(9.5, 10), span(27, 28), span(54, 60)}),
		"an episode counts once however many marks fall in it")
	assert.Equal(t, 2, slowpath.OverlapCount(episodes, []chart.Interval{span(9.5, 11.5), span(28.5, 31.5)}))
	assert.Equal(t, 0, slowpath.OverlapCount(episodes, []chart.Interval{span(19, 20)}), "a burst between two episodes overlaps none")
	assert.Equal(t, 0, slowpath.OverlapCount(episodes, nil))
}

func TestClipAndComplementStayInsideTheWindow(t *testing.T) {
	marks := []chart.Interval{span(-5, 2), span(10, 12), {Start: at(30), End: at(30)}, span(58, 70), {Start: at(90), End: at(90)}}
	clipped := slowpath.Clip(marks, at(0), at(60))
	assert.Equal(t, []chart.Interval{span(0, 2), span(10, 12), {Start: at(30), End: at(30)}, span(58, 60)}, clipped,
		"a stretch is cut at the window; a moment outside it is dropped")
	assert.Equal(t, []chart.Interval{span(2, 10), span(12, 58)}, slowpath.Complement(marks, at(0), at(60)))
	assert.Equal(t, []chart.Interval{span(0, 60)}, slowpath.Complement(nil, at(0), at(60)), "nothing covered: the whole window is unknown")
}

// points are samples half a minute apart.
func points(start time.Time, values ...float64) []chart.Point {
	out := make([]chart.Point, 0, len(values))
	for i, v := range values {
		out = append(out, chart.Point{T: start.Add(time.Duration(i) * 30 * time.Second), V: v})
	}
	return out
}

func TestARaisedSampleStandsForTheStepThatEndsAtIt(t *testing.T) {
	step := 30 * time.Second
	latency := slowpath.RuleFor(&observability.Signal{Name: "p99", Unit: observability.UnitSeconds, Worse: observability.WorseHigh})
	series := points(at(0), 0.35, 0.36, 0.34, 48, 47, 0.35, 0.35, 0.35, 41, 0.35, 0.36)
	assert.InDelta(t, 0.35, slowpath.Median(series), 1e-9, "the usual level is the median, which three bad samples do not move")
	crossed := slowpath.Crossings(series, latency, step, slowpath.Lookback{})
	require.Equal(t, []chart.Interval{span(1, 2), span(3.5, 4)}, crossed,
		"the samples at 1m30s and 2m were raised: the rule was crossed in the half minute before each")
	assert.Equal(t, []chart.Interval{span(-0.5, 5)}, slowpath.Covered(series, step), "the newest sample is inside the window that ends at it")

	hole := append(points(at(0), 1, 1), points(at(5), 1, 1)...)
	assert.Equal(t, []chart.Interval{span(0.5, 4.5), span(5.5, 6)}, slowpath.Complement(slowpath.Covered(hole, step), at(0), at(6)),
		"a hole in the samples is unknown, not quiet")

	restarts := slowpath.RuleFor(&observability.Signal{Name: slowpath.SignalRestarts, Unit: observability.UnitCount, Worse: observability.WorseHigh})
	assert.Equal(t, []chart.Interval{span(0.5, 1), span(1.5, 2)}, slowpath.Crossings(points(at(0), 4, 4, 5, 5, 6, 6), restarts, step, slowpath.Lookback{}),
		"a counter is raised where it grew since the sample before, however large it already was")
	context := slowpath.RuleFor(&observability.Signal{Name: "requests", Unit: observability.UnitPerSecond, Worse: observability.WorseNone})
	assert.Nil(t, slowpath.Crossings(points(at(0), 1, 500, 1), context, step, slowpath.Lookback{}), "a signal with no bad direction is never raised")
	assert.Zero(t, slowpath.Median(nil))
}

// mean answers as a rate over two minutes does: the mean of the level over
// the two minutes that end at a sample, read every ten seconds.
func mean(level func(minutes float64) float64, minutes float64) float64 {
	total, count := 0.0, 0
	for back := range 13 {
		total += level(minutes - float64(back)/6)
		count++
	}
	return total / float64(count)
}

// rate is such a rate, sampled every half minute from 0 to a minute.
func rate(level func(minutes float64) float64, to float64) []chart.Point {
	var out []chart.Point
	for minutes := 0.0; minutes <= to; minutes += 0.5 {
		out = append(out, chart.Point{T: at(minutes), V: mean(level, minutes)})
	}
	return out
}

// burst is a level that is raised from one minute to another. The edges
// are a second off the sample times, as real ones are.
func burst(from, to, usual, raised float64) func(float64) float64 {
	return func(minutes float64) float64 {
		if minutes >= from-0.01 && minutes < to-0.01 {
			return raised
		}
		return usual
	}
}

func TestAStretchOfARateLeavesOutTheTimeTheBurstTakesToLeaveItsWindow(t *testing.T) {
	step, window := 30*time.Second, 2*time.Minute
	over := slowpath.Lookback{Window: window}
	drops := slowpath.RuleFor(&observability.Signal{Name: "drops", Unit: observability.UnitPerSecond, Worse: observability.WorseHigh})
	share := slowpath.RuleFor(&observability.Signal{Name: "throttled", Unit: observability.UnitRatio, Worse: observability.WorseHigh})
	latency := slowpath.RuleFor(&observability.Signal{Name: "p99", Unit: observability.UnitSeconds, Worse: observability.WorseHigh})

	// Four minutes of 2/s, far over the rule: the rate is raised for six.
	long := rate(burst(10, 14, 0.01, 2), 30)
	assert.Equal(t, []chart.Interval{span(9.5, 15.5)}, slowpath.Crossings(long, drops, step, slowpath.Lookback{}), "read as a gauge it would run two minutes on")
	assert.Equal(t, []chart.Interval{span(9.5, 13.5)}, slowpath.Crossings(long, drops, step, over), "four minutes, from the step in which it began")

	// One minute of 0.8/s never fills the window: the rate peaks under half
	// of it and is raised for two and a half minutes.
	short := rate(burst(10, 11, 0, 0.8), 30)
	assert.Equal(t, []chart.Interval{span(10, 12.5)}, slowpath.Crossings(short, drops, step, slowpath.Lookback{}))
	assert.Equal(t, []chart.Interval{span(9.5, 10.5)}, slowpath.Crossings(short, drops, step, over), "the two steps a gauge of the same minute would mark")

	// A share whose rule sits a third of the way to its peak crosses it
	// once a third of the window is in the burst, and is stated from the
	// beginning of the burst, not from that sample.
	throttled := rate(burst(10, 13, 0.04, 0.86), 30)
	assert.Equal(t, []chart.Interval{span(10, 14)}, slowpath.Crossings(throttled, share, step, slowpath.Lookback{}))
	assert.Equal(t, []chart.Interval{span(9.5, 12.5)}, slowpath.Crossings(throttled, share, step, over))

	// A burst that is not over keeps its end, and one that was under way at
	// the first sample keeps its beginning: neither is a guess.
	ongoing := rate(burst(26, 99, 0.01, 2), 30)
	assert.Equal(t, []chart.Interval{span(25.5, 30)}, slowpath.Crossings(ongoing, drops, step, over), "raised at the newest sample: still going on")
	under := rate(burst(-9, 3.2, 0.01, 2), 30)
	assert.Equal(t, []chart.Interval{span(-0.5, 5)}, slowpath.Crossings(under, drops, step, slowpath.Lookback{}))
	assert.Equal(t, []chart.Interval{span(-0.5, 3)}, slowpath.Crossings(under, drops, step, over), "began before the window; ended at 3m, not at 5m")
	always := rate(burst(-9, 99, 0.01, 7), 10)
	assert.Equal(t, []chart.Interval{span(-0.5, 10)}, slowpath.Crossings(always, latency, step, slowpath.Lookback{Window: window, Quantile: true}),
		"raised from the first sample to the last")

	// A quantile is slow while any part of its window is: the whole window
	// is left out. A stretch never shrinks to nothing.
	quantile := slowpath.Lookback{Window: window, Quantile: true}
	series := points(at(0), 0.35, 0.35, 48, 48, 48, 48, 48, 48, 48, 48, 0.35, 0.35, 0.35, 0.35, 0.35, 0.35, 0.35, 0.35, 0.35)
	assert.Equal(t, []chart.Interval{span(0.5, 2.5)}, slowpath.Crossings(series, latency, step, quantile), "eight raised samples, four of them the window")
	blip := points(at(0), 0.35, 0.35, 48, 48, 0.35, 0.35, 0.35, 0.35, 0.35, 0.35, 0.35)
	assert.Equal(t, []chart.Interval{span(0.5, 1)}, slowpath.Crossings(blip, latency, step, quantile), "at least the step of its first raised sample")

	// A series that jumps to its peak in one step is not a mean over a
	// window. Its edges say nothing; only the window is left out.
	square := points(at(0), 0.01, 0.01, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 0.01)
	assert.Equal(t, []chart.Interval{span(0.5, 3.5)}, slowpath.Crossings(square, drops, step, over))

	// A signal that is bad when low and a window no longer than a step are
	// read as they are.
	free := slowpath.RuleFor(&observability.Signal{Name: "free-threads", Unit: observability.UnitCount, Worse: observability.WorseLow})
	low := points(at(0), 5, 5, 0, 0, 0, 0, 0, 0, 5, 5, 5, 5, 5, 5, 5)
	assert.Equal(t, slowpath.Crossings(low, free, step, slowpath.Lookback{}), slowpath.Crossings(low, free, step, over))
	assert.Equal(t, []chart.Interval{span(9.5, 15.5)}, slowpath.Crossings(long, drops, step, slowpath.Lookback{Window: step}))
}

func TestTheLevelOfARuleIsTheLowestValueThatCrossesIt(t *testing.T) {
	latency := slowpath.RuleFor(&observability.Signal{Name: "p99", Unit: observability.UnitSeconds, Worse: observability.WorseHigh})
	assert.InDelta(t, 1.05, latency.Level(0.35), 1e-9, "three times the usual level")
	assert.InDelta(t, 0.056, latency.Level(0.006), 1e-9, "the floor counts for a fast hop")
	assert.InDelta(t, 5, latency.Level(4), 1e-9, "never above the level that always counts")
	for _, median := range []float64{0.006, 0.35, 4} {
		level := latency.Level(median)
		assert.True(t, latency.Crosses(level*1.001, median))
		assert.False(t, latency.Crosses(level*0.99, median))
	}
	free := slowpath.RuleFor(&observability.Signal{Name: "free-threads", Unit: observability.UnitCount, Worse: observability.WorseLow})
	assert.True(t, math.IsInf(free.Level(5), 1), "a signal that is bad when low has no such level")
}

// exact is a rate over a window as a Prometheus computes it from a level
// that is raised from one second to another: the usual level plus the part
// of the window the burst fills. Samples are half a minute apart.
func exact(from, to, usual, raised float64, window time.Duration, samples int) []chart.Point {
	out := make([]chart.Point, 0, samples)
	for i := range samples {
		t := float64(i) * 30
		inside := math.Max(0, math.Min(t, to)-math.Max(t-window.Seconds(), from))
		out = append(out, chart.Point{T: t0.Add(time.Duration(t) * time.Second), V: usual + (raised-usual)*inside/window.Seconds()})
	}
	return out
}

func TestAStretchOfARateIsRightWhereverTheSamplesFall(t *testing.T) {
	step, window := 30*time.Second, 2*time.Minute
	over := slowpath.Lookback{Window: window}
	for _, c := range []struct {
		name          string
		signal        *observability.Signal
		usual, raised float64
	}{
		{"a rate far over its rule", &observability.Signal{Name: "499", Unit: observability.UnitPerSecond, Worse: observability.WorseHigh}, 0.01, 2.2},
		{"a share whose rule is a third of the way to its peak", &observability.Signal{Name: "throttled", Unit: observability.UnitRatio, Worse: observability.WorseHigh}, 0.04, 0.86},
	} {
		rule := slowpath.RuleFor(c.signal)
		for _, length := range []float64{60, 90, 120, 150, 180, 240, 600} {
			for phase := 0.5; phase < 30; phase += 1.5 {
				from := 600 + phase
				series := exact(from, from+length, c.usual, c.raised, window, 80)
				crossed := slowpath.Crossings(series, rule, step, over)
				require.Len(t, crossed, 1, "%s, %vs at +%vs", c.name, length, phase)
				// A gauge of the same burst is raised at every sample inside
				// it: from the step that holds its beginning, for its length.
				begins, lasts := t0.Add(600*time.Second), time.Duration(length)*time.Second
				if lasts > window {
					assert.Equal(t, chart.Interval{Start: begins, End: begins.Add(lasts)}, crossed[0], "%s, %vs at +%vs", c.name, length, phase)
					raw := slowpath.Crossings(series, rule, step, slowpath.Lookback{})
					assert.GreaterOrEqual(t, slowpath.Total(raw), lasts+step, "read as a gauge it runs on")
					continue
				}
				// A burst no longer than the window never fills it: its peak
				// does not say how large it was, and the stretch is right to
				// within one step at either end.
				assert.LessOrEqual(t, crossed[0].Start.Sub(begins).Abs(), step, "%s, %vs at +%vs begins %s", c.name, length, phase, crossed[0].Start)
				assert.LessOrEqual(t, (crossed[0].End.Sub(crossed[0].Start) - lasts).Abs(), step, "%s, %vs at +%vs lasts %s", c.name, length, phase,
					crossed[0].End.Sub(crossed[0].Start))
			}
		}
	}
}
