// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath

import (
	"math"
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

// Lookback says how far back one sample of a signal looks. A gauge looks at
// its last scrape; a rate, a share of two rates and a quantile of a rate
// look back over the rate window, so they are still raised while a burst
// that has ended is leaving that window.
type Lookback struct {
	// Window is the range the rate was taken over; zero for a gauge.
	Window time.Duration
	// Quantile is true for a quantile of a rate. It does not rise in
	// proportion to the part of the window a burst fills: it is raised as
	// soon as a small share of the window's requests is slow.
	Quantile bool
}

// maxRamp bounds the part of a ramp that is taken off a stretch at each
// end. A rule that is crossed only near the peak leaves raised samples
// that say little about the length of the burst.
const maxRamp = 0.4

// steepRamp is the part of a ramp up to which a burst shorter than the
// window is solved from the number of its raised samples: further up, the
// number hardly changes with the length of the burst.
const steepRamp = 0.3

// maxHeld bounds how much of their ramps the first and the last raised
// sample may hold together for a burst shorter than the window to be read
// off them: near one the reading divides by almost nothing.
const maxHeld = 0.8

// Crossings turns the samples of one series into the stretches in which the
// rule held. Each crossing sample stands for the step that ends at it, so
// that adjacent crossing samples form one stretch. For a signal that looks
// back over a rate window the stretch is then stated without the time the
// burst needed to leave that window (see unsmear).
func Crossings(points []chart.Point, rule Rule, step time.Duration, look Lookback) []chart.Interval {
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
	out = Normalize(out)
	if look.Window <= step || step <= 0 || rule.Worse != WorseHigh || rule.Counter {
		return out
	}
	return unsmear(out, points, rule.Level(level), level, step, look)
}

// unsmear states the stretches of a rate without its window. A rate over W
// is the mean of the last W, so a burst of length D that ends is still in
// it for up to W: its samples are raised for about D + W, not D, and a lane
// drawn from them would run on after a gauge of the same burst has cleared.
//
// While a burst enters the window the rate climbs in proportion to the
// part of it that is inside, and falls the same way while it leaves: two
// ramps as long as the window, or as long as the burst when that is
// shorter and never fills the window. D is solved for in two ways.
//
// By the first and the last raised sample: each holds a known part of the
// peak, which is how far up its ramp it sits, so the first says how long
// the burst had been going on and the last how much of it was still in the
// window. Read with ramps as long as the window, or as long as the burst,
// the wrong reading is always the longer one, so the shorter is taken.
//
// By the number of raised samples, when those two samples do not sit on
// such ramps (the sample before or after would have been over the rule
// too) or hold almost all of them: the rule is crossed once a part f of a
// ramp is climbed (the distance from the usual level to the rule, over the
// distance to the peak) and until less than f is left, so the raised
// samples span D + (1-2f)W for a burst longer than the window and
// W + (1-2f)D for a shorter one. A quantile does not climb in proportion:
// it is taken to be slow while any part of its window is, and is always
// solved this way.
//
// A stretch is stated to whole steps, never less than one, and from the
// step in which a gauge of the same burst would begin. One that touches
// the first or the last sample keeps that end: the burst may have begun
// before the window, or may not be over.
func unsmear(stretches []chart.Interval, points []chart.Point, rule, usual float64, step time.Duration, look Lookback) []chart.Interval {
	oldest, newest := points[0].T.Add(-step), points[len(points)-1].T
	window := float64(look.Window)
	out := make([]chart.Interval, 0, len(stretches))
	for _, iv := range stretches {
		// The first raised sample ends the first step of the stretch.
		first, last := iv.Start.Add(step), iv.End
		e := edgesOf(points, iv, rule, usual, step, look.Quantile)
		open, ongoing := !iv.Start.After(oldest), !iv.End.Before(newest)
		seen := float64(iv.End.Sub(iv.Start))
		switch {
		case open && ongoing:
			// Raised from the first sample to the last: nothing to solve.
		case ongoing:
			if seen >= window {
				iv.Start = first.Add(-e.lead(window))
			}
		case open:
			tail := wholeSteps((1-e.off())*window, step, math.Round)
			if e.onRamp(e.left, window) {
				tail = wholeSteps((1-e.left)*window, step, math.Ceil)
			}
			if iv.End = last.Add(-tail); !iv.End.After(iv.Start) {
				iv.End = iv.Start.Add(step)
			}
		default:
			length := math.Max(e.length(seen, float64(last.Sub(first)), window), float64(step))
			iv.Start = first.Add(-e.lead(math.Min(length, window)))
			iv.End = iv.Start.Add(max(wholeSteps(length, step, math.Round), step))
		}
		if iv.Start.Before(oldest) {
			iv.Start = oldest
		}
		out = append(out, iv)
	}
	return Normalize(out)
}

// wholeSteps is a length of time in whole steps.
func wholeSteps(d float64, step time.Duration, round func(float64) float64) time.Duration {
	return time.Duration(round(d/float64(step))) * step
}

// edges says where the first and the last raised sample of a stretch sit
// on the ramps of a rate.
type edges struct {
	// linear is false for a quantile and for a stretch without a peak.
	linear bool
	// in is the part of its ramp the first raised sample has climbed, left
	// the part the last one still holds, and ramp the part at which the
	// rule is crossed.
	in, left, ramp float64
	step           time.Duration
}

func edgesOf(points []chart.Point, iv chart.Interval, rule, usual float64, step time.Duration, quantile bool) edges {
	first, last := iv.Start.Add(step), iv.End
	peak, entering, leaving := usual, usual, usual
	for _, point := range points {
		if !point.T.After(iv.Start) || point.T.After(iv.End) {
			continue
		}
		peak = math.Max(peak, point.V)
		if point.T.Equal(first) {
			entering = point.V
		}
		if point.T.Equal(last) {
			leaving = point.V
		}
	}
	e := edges{linear: !quantile && peak > usual, step: step}
	if !e.linear {
		return e
	}
	part := func(v float64) float64 { return math.Min(math.Max((v-usual)/(peak-usual), 0), 1) }
	e.in, e.left = part(entering), part(leaving)
	if !math.IsInf(rule, 0) {
		e.ramp = part(rule)
	}
	return e
}

// off is the part of a ramp taken off a stretch at each end.
func (e *edges) off() float64 { return math.Min(e.ramp, maxRamp) }

// onRamp reports whether a sample at that part of a ramp of the given
// length is the first over the rule, as it is for a mean over a window. A
// series that jumps to its peak in one step is not. The largest sample can
// be half a step short of the top of the ramp, which makes every part look
// that much larger.
func (e *edges) onRamp(at, length float64) bool {
	if !e.linear || length <= 0 {
		return false
	}
	stride := float64(e.step) / length
	return at*(1-stride/2) < e.ramp+stride
}

// lead is how long before its first raised sample the burst began, for
// ramps of the given length.
func (e *edges) lead(length float64) time.Duration {
	if e.onRamp(e.in, length) {
		return max(wholeSteps(e.in*length, e.step, math.Ceil), e.step)
	}
	return e.step + wholeSteps(e.ramp*length, e.step, math.Floor)
}

// length solves for the length of a burst whose raised samples stand for
// seen and whose first and last raised sample are between apart.
func (e *edges) length(seen, between, window float64) float64 {
	// By the number of raised samples.
	off := e.off()
	long := seen >= 2*(1-off)*window
	length := seen - (1-2*off)*window
	if !long && e.ramp <= steepRamp {
		length = (seen - window) / (1 - 2*e.ramp)
	}
	// By the first and the last raised sample.
	held := e.in + e.left
	read := between - (1-held)*window
	switch {
	case held <= maxHeld:
		read = math.Min(read, (between-window)/(1-held))
	case !long:
		read = 0
	}
	if reach := math.Min(read, window); e.onRamp(e.in, reach) && e.onRamp(e.left, reach) {
		return read
	}
	return length
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
