// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
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
	crossed := slowpath.Crossings(series, latency, step)
	require.Equal(t, []chart.Interval{span(1, 2), span(3.5, 4)}, crossed,
		"the samples at 1m30s and 2m were raised: the rule was crossed in the half minute before each")
	assert.Equal(t, []chart.Interval{span(-0.5, 5)}, slowpath.Covered(series, step), "the newest sample is inside the window that ends at it")

	hole := append(points(at(0), 1, 1), points(at(5), 1, 1)...)
	assert.Equal(t, []chart.Interval{span(0.5, 4.5), span(5.5, 6)}, slowpath.Complement(slowpath.Covered(hole, step), at(0), at(6)),
		"a hole in the samples is unknown, not quiet")

	restarts := slowpath.RuleFor(&observability.Signal{Name: slowpath.SignalRestarts, Unit: observability.UnitCount, Worse: observability.WorseHigh})
	assert.Equal(t, []chart.Interval{span(0.5, 1), span(1.5, 2)}, slowpath.Crossings(points(at(0), 4, 4, 5, 5, 6, 6), restarts, step),
		"a counter is raised where it grew since the sample before, however large it already was")
	context := slowpath.RuleFor(&observability.Signal{Name: "requests", Unit: observability.UnitPerSecond, Worse: observability.WorseNone})
	assert.Nil(t, slowpath.Crossings(points(at(0), 1, 500, 1), context, step), "a signal with no bad direction is never raised")
	assert.Zero(t, slowpath.Median(nil))
}
