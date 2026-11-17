// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath

import (
	"sort"
	"time"

	"github.com/derailed/k9s/internal/chart"
)

// span gives an interval a positive length: a moment (End not after Start)
// is treated as the instant Start.
func span(iv chart.Interval) (start, end time.Time) {
	if !iv.End.After(iv.Start) {
		return iv.Start, iv.Start
	}
	return iv.Start, iv.End
}

// Normalize sorts intervals and merges those that touch or overlap. Moments
// stay moments unless a stretch covers them.
func Normalize(intervals []chart.Interval) []chart.Interval {
	return Join(intervals, 0)
}

// Join sorts intervals and merges those separated by at most gap. It is how
// crossings become episodes: two bursts a step apart are one episode.
func Join(intervals []chart.Interval, gap time.Duration) []chart.Interval {
	if len(intervals) == 0 {
		return nil
	}
	sorted := make([]chart.Interval, 0, len(intervals))
	for _, iv := range intervals {
		start, end := span(iv)
		sorted = append(sorted, chart.Interval{Start: start, End: end})
	}
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Start.Before(sorted[b].Start) })
	out := []chart.Interval{sorted[0]}
	for _, iv := range sorted[1:] {
		last := &out[len(out)-1]
		if iv.Start.Sub(last.End) <= gap {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// Union merges several sets of intervals into one normalized set.
func Union(sets ...[]chart.Interval) []chart.Interval {
	var all []chart.Interval
	for _, set := range sets {
		all = append(all, set...)
	}
	return Normalize(all)
}

// Overlap reports whether two intervals share any time. A moment overlaps a
// stretch that contains it; two stretches that only touch do not overlap.
func Overlap(a, b chart.Interval) bool {
	aStart, aEnd := span(a)
	bStart, bEnd := span(b)
	switch {
	case aStart.Equal(aEnd) && bStart.Equal(bEnd):
		return aStart.Equal(bStart)
	case aStart.Equal(aEnd):
		return !aStart.Before(bStart) && aStart.Before(bEnd)
	case bStart.Equal(bEnd):
		return !bStart.Before(aStart) && bStart.Before(aEnd)
	}
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// OverlapsAny reports whether an interval overlaps any of the others.
func OverlapsAny(iv chart.Interval, others []chart.Interval) bool {
	for _, other := range others {
		if Overlap(iv, other) {
			return true
		}
	}
	return false
}

// OverlapCount is the number of episodes that at least one mark overlaps.
// It is the only relation the summary states: overlap in time, never cause.
func OverlapCount(episodes, marks []chart.Interval) int {
	count := 0
	for _, episode := range episodes {
		if OverlapsAny(episode, marks) {
			count++
		}
	}
	return count
}

// Total is the time the normalized intervals cover.
func Total(intervals []chart.Interval) time.Duration {
	var total time.Duration
	for _, iv := range Normalize(intervals) {
		total += iv.End.Sub(iv.Start)
	}
	return total
}

// Clip keeps the parts of the intervals inside [start, end]. A moment
// outside the window is dropped.
func Clip(intervals []chart.Interval, start, end time.Time) []chart.Interval {
	var out []chart.Interval
	for _, iv := range intervals {
		from, to := span(iv)
		if from.Equal(to) {
			if !from.Before(start) && !from.After(end) {
				out = append(out, chart.Interval{Start: from, End: to})
			}
			continue
		}
		if !to.After(start) || !from.Before(end) {
			continue
		}
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		out = append(out, chart.Interval{Start: from, End: to})
	}
	return out
}

// Complement returns the stretches of [start, end] that no interval covers.
func Complement(intervals []chart.Interval, start, end time.Time) []chart.Interval {
	var out []chart.Interval
	at := start
	for _, iv := range Normalize(Clip(intervals, start, end)) {
		if !iv.End.After(iv.Start) {
			// A moment covers no time.
			continue
		}
		if iv.Start.After(at) {
			out = append(out, chart.Interval{Start: at, End: iv.Start})
		}
		if iv.End.After(at) {
			at = iv.End
		}
	}
	if end.After(at) {
		out = append(out, chart.Interval{Start: at, End: end})
	}
	return out
}

// held is the stretch one sample stands for: the step that ends at it. A
// query evaluated at a time looks back from it (a rate over its window, a
// gauge at its last scrape, a counter since the sample before), so a raised
// sample says that the rule was crossed in the step before it. The newest
// sample, at the end of the window, is therefore inside the window.
func held(t time.Time, step time.Duration) chart.Interval {
	return chart.Interval{Start: t.Add(-step), End: t}
}

// Crossings turns the samples of one series into the stretches in which the
// rule held. Each crossing sample stands for the step that ends at it, so
// that adjacent crossing samples form one stretch.
func Crossings(points []chart.Point, rule Rule, step time.Duration) []chart.Interval {
	if rule.Worse == WorseNone || len(points) == 0 {
		return nil
	}
	level := Median(points)
	var out []chart.Interval
	for index, point := range points {
		crossed := rule.Crosses(point.V, level)
		if rule.Counter {
			// A counter crosses where it grew since the sample before.
			crossed = index > 0 && point.V > points[index-1].V
		}
		if crossed {
			out = append(out, held(point.T, step))
		}
	}
	return Normalize(out)
}

// Covered is the time the samples of a series stand for. What is left of
// the window is unknown, and is never drawn as quiet.
func Covered(points []chart.Point, step time.Duration) []chart.Interval {
	out := make([]chart.Interval, 0, len(points))
	for _, point := range points {
		out = append(out, held(point.T, step))
	}
	return Normalize(out)
}

// Median is the middle value of the samples, or zero without samples.
func Median(points []chart.Point) float64 {
	if len(points) == 0 {
		return 0
	}
	values := make([]float64, 0, len(points))
	for _, point := range points {
		values = append(values, point.V)
	}
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}
