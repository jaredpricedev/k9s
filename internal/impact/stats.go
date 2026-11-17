// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"math"
	"sort"
	"time"
)

// sideOf places a sample time: -1 before the rollout, +1 after it, 0 when it
// is outside both windows or inside the guard. A sample at the rollout
// instant describes the time up to it and so belongs to the before window.
// The guard keeps out after-samples whose rate window still reaches back
// across the rollout and would blend both sides.
func (w *Windows) sideOf(t time.Time, guard time.Duration) int {
	switch {
	case t.Before(w.Before.Start) || t.After(w.After.End):
		return 0
	case !t.After(w.Rollout):
		return -1
	case t.Before(w.Rollout.Add(guard)):
		return 0
	default:
		return 1
	}
}

// reduction is both sides of one signal plus what was left out.
type reduction struct {
	before, after Side
	skipped       int
	series        []Point
}

func (w *Windows) skippedIn(points []Point, guard time.Duration) int {
	count := 0
	for _, p := range points {
		if guard > 0 && p.T.After(w.Rollout) && p.T.Before(w.Rollout.Add(guard)) {
			count++
		}
	}
	return count
}

// reduceGauge reduces plain samples with mean, max or min.
func reduceGauge(points []Point, w *Windows, guard time.Duration, how Reduce) reduction {
	out := reduction{series: points, skipped: w.skippedIn(points, guard)}
	fold := func(side *Side, v float64) {
		switch {
		case side.Samples == 0:
			side.Value = v
		case how == ReduceMax:
			side.Value = math.Max(side.Value, v)
		case how == ReduceMin:
			side.Value = math.Min(side.Value, v)
		default:
			side.Value += v
		}
		side.Samples++
		side.Valid = true
	}
	for _, p := range points {
		switch w.sideOf(p.T, guard) {
		case -1:
			fold(&out.before, p.V)
		case 1:
			fold(&out.after, p.V)
		}
	}
	if how == ReduceMean {
		for _, side := range []*Side{&out.before, &out.after} {
			if side.Samples > 0 {
				side.Value /= float64(side.Samples)
			}
		}
	}
	return out
}

// reduceIncrease counts how far a counter rose inside each window: the sum
// of its positive steps. A drop is a reset or a series that went away, and
// adds nothing. The step that crosses the rollout belongs to the after
// window. The display series is the rise per step.
func reduceIncrease(points []Point, w *Windows) reduction {
	var out reduction
	for index, p := range points {
		side := w.sideOf(p.T, 0)
		target := &out.before
		if side == 1 {
			target = &out.after
		}
		if side == 0 {
			continue
		}
		target.Samples++
		target.Valid = true
		step := 0.0
		if index > 0 {
			step = math.Max(p.V-points[index-1].V, 0)
			target.Value += step
		}
		out.series = append(out.series, Point{T: p.T, V: step})
	}
	return out
}

// reduceRatio divides summed numerator samples by summed denominator samples
// taken at the same times, which weights every step by its volume. A time
// with no numerator sample counts as zero of it: a counter for an outcome
// that has not happened yet does not exist. A side whose denominator sums to
// zero has no ratio.
func reduceRatio(numerator, denominator []Point, w *Windows, guard time.Duration) reduction {
	out := reduction{skipped: w.skippedIn(denominator, guard)}
	top := make(map[int64]float64, len(numerator))
	for _, p := range numerator {
		top[p.T.UnixNano()] = p.V
	}
	var sums [2][2]float64
	for _, p := range denominator {
		n := top[p.T.UnixNano()]
		if p.V > 0 {
			out.series = append(out.series, Point{T: p.T, V: n / p.V})
		}
		side := w.sideOf(p.T, guard)
		if side == 0 || p.V <= 0 {
			continue
		}
		index, target := 0, &out.before
		if side == 1 {
			index, target = 1, &out.after
		}
		sums[index][0] += n
		sums[index][1] += p.V
		target.Samples++
	}
	for index, side := range []*Side{&out.before, &out.after} {
		if sums[index][1] > 0 {
			side.Value, side.Valid = sums[index][0]/sums[index][1], true
		}
	}
	return out
}

// Bucket is one cumulative histogram bucket: observations at or below Le.
type Bucket struct {
	Le    float64
	Count float64
}

// Quantile interpolates a quantile from cumulative buckets the way
// Prometheus does: linearly inside the bucket that holds the rank, from zero
// for the first bucket, and capped at the highest finite bound when the rank
// falls in the +Inf bucket. It reports false without a +Inf bucket, with
// fewer than two buckets, or with no observations.
func Quantile(q float64, buckets []Bucket) (float64, bool) {
	if len(buckets) < 2 || q < 0 || q > 1 {
		return 0, false
	}
	sorted := append([]Bucket(nil), buckets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Le < sorted[j].Le })
	last := len(sorted) - 1
	if !math.IsInf(sorted[last].Le, 1) {
		return 0, false
	}
	// Rates of cumulative buckets can dip by rounding; keep them monotone.
	for index := 1; index <= last; index++ {
		sorted[index].Count = math.Max(sorted[index].Count, sorted[index-1].Count)
	}
	total := sorted[last].Count
	if total <= 0 {
		return 0, false
	}
	rank := q * total
	index := sort.Search(last, func(i int) bool { return sorted[i].Count >= rank })
	if index == last {
		return sorted[last-1].Le, true
	}
	lower, below := 0.0, 0.0
	if index > 0 {
		lower, below = sorted[index-1].Le, sorted[index-1].Count
	} else if sorted[0].Le <= 0 {
		return sorted[0].Le, true
	}
	width := sorted[index].Count - below
	if width <= 0 {
		return sorted[index].Le, true
	}
	return lower + (sorted[index].Le-lower)*(rank-below)/width, true
}

// reduceQuantile sums each bucket's rate over a window and interpolates the
// quantile of that sum, so busy steps weigh more than quiet ones. Samples
// counts the steps that saw any request.
func reduceQuantile(q float64, buckets map[float64][]Point, w *Windows, guard time.Duration) reduction {
	var out reduction
	type column struct {
		t time.Time
		b []Bucket
	}
	steps := map[int64]*column{}
	for le, points := range buckets {
		for _, p := range points {
			key := p.T.UnixNano()
			if steps[key] == nil {
				steps[key] = &column{t: p.T}
			}
			steps[key].b = append(steps[key].b, Bucket{Le: le, Count: p.V})
		}
	}
	keys := make([]int64, 0, len(steps))
	for key := range steps {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	sums := [2]map[float64]float64{{}, {}}
	for _, key := range keys {
		step := steps[key]
		value, ok := Quantile(q, step.b)
		if !ok {
			continue
		}
		out.series = append(out.series, Point{T: step.t, V: value})
		side := w.sideOf(step.t, guard)
		if side == 0 {
			if guard > 0 && step.t.After(w.Rollout) && step.t.Before(w.Rollout.Add(guard)) {
				out.skipped++
			}
			continue
		}
		index, target := 0, &out.before
		if side == 1 {
			index, target = 1, &out.after
		}
		target.Samples++
		for _, bucket := range step.b {
			sums[index][bucket.Le] += bucket.Count
		}
	}
	for index, side := range []*Side{&out.before, &out.after} {
		merged := make([]Bucket, 0, len(sums[index]))
		for le, count := range sums[index] {
			merged = append(merged, Bucket{Le: le, Count: count})
		}
		side.Value, side.Valid = Quantile(q, merged)
	}
	return out
}

// SparkLevels is the number of heights a sparkline cell can take.
const SparkLevels = 8

// Spark buckets a display series into cells per side. A level is 0 to
// SparkLevels-1 on a scale from zero to the largest cell on either side, so
// both halves share one scale; -1 marks a cell with no sample. Cells hold
// the mean of their samples, or the peak when peak is set.
func Spark(series []Point, w *Windows, cells int, peak bool) (before, after []int) {
	if cells <= 0 || w.Width <= 0 {
		return nil, nil
	}
	sum := make([]float64, 2*cells)
	count := make([]int, 2*cells)
	for _, p := range series {
		offset := p.T.Sub(w.Before.Start)
		if offset < 0 || p.T.After(w.After.End) || math.IsNaN(p.V) {
			continue
		}
		// A sample describes the time up to it, so it falls in the cell
		// that ends at or after it.
		cell := int(math.Ceil(float64(offset)/float64(w.Width)*float64(cells))) - 1
		cell = min(max(cell, 0), 2*cells-1)
		if peak && count[cell] > 0 {
			sum[cell] = math.Max(sum[cell], p.V)
		} else if peak {
			sum[cell] = p.V
		} else {
			sum[cell] += p.V
		}
		count[cell]++
	}
	top := 0.0
	for cell := range sum {
		if count[cell] > 0 && !peak {
			sum[cell] /= float64(count[cell])
		}
		if count[cell] > 0 {
			top = math.Max(top, sum[cell])
		}
	}
	levels := make([]int, 2*cells)
	for cell := range levels {
		switch {
		case count[cell] == 0:
			levels[cell] = -1
		case top <= 0:
			levels[cell] = 0
		default:
			levels[cell] = min(max(int(math.Round(sum[cell]/top*float64(SparkLevels-1))), 0), SparkLevels-1)
		}
	}
	return levels[:cells], levels[cells:]
}
