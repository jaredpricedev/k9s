// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"math"
	"sort"
)

// Direction says which side of its peers a value may deviate to.
type Direction int

const (
	// Either reports values far above or far below their peers.
	Either Direction = iota
	// Above reports only values far above their peers (latency, queue).
	Above
	// Below reports only values far below their peers (free threads).
	Below
)

// Defaults of PeerOptions.
const (
	// DefaultMinPeers is the smallest group in which one member can be
	// called different: with two there is no way to say which one is odd.
	DefaultMinPeers = 3
	// DefaultPeerScore is the modified z-score from which a value deviates
	// (Iglewicz and Hoaglin recommend 3.5).
	DefaultPeerScore = 3.5
	// DefaultPeerRatio is how many times the median a value must be (or,
	// below, what fraction of it) before it is called different. It keeps
	// a tight group, where the score alone is large, from reporting a
	// difference nobody would act on.
	DefaultPeerRatio = 2.0
)

// PeerOptions tune Outliers. The zero value uses the defaults.
type PeerOptions struct {
	Direction Direction
	// MinPeers is the smallest number of finite values compared.
	MinPeers int
	// Score is the modified z-score threshold.
	Score float64
	// Ratio is the factor against the median; see DefaultPeerRatio.
	Ratio float64
	// MinDelta is the noise floor in the unit of the values: a value closer
	// than this to the median never deviates. Set it whenever the median
	// can be zero (errors, drops, queue length), where a ratio means
	// nothing.
	MinDelta float64
}

// Deviation is one value that differs from its peers.
type Deviation struct {
	// Index is the position in the values given to Outliers.
	Index int
	Value float64
	// Delta is Value minus the median of all peers.
	Delta float64
	// Ratio is Value divided by the median; +Inf when the median is zero.
	Ratio float64
	// Score is the modified z-score 0.6745*Delta/MAD; ±Inf when more than
	// half of the peers share one value (MAD is zero).
	Score float64
	// Above is true when the value is larger than the median.
	Above bool
}

// PeerResult is what Outliers found.
type PeerResult struct {
	// Enough is false when fewer than MinPeers values were finite; then
	// nothing is reported and Reason says why.
	Enough bool
	Reason string
	// Peers is the number of finite values compared; Skipped lists the
	// positions that were NaN or infinite (a peer with no reading).
	Peers   int
	Skipped []int
	// Median and MAD (median absolute deviation) describe the group.
	Median, MAD float64
	// Outliers are the deviating values, largest |Delta| first.
	Outliers []Deviation
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Outliers says which of several peer readings (one per Pod, say) differ
// from the rest. It is a pure function and knows nothing about time: give
// it one number per peer, such as each Pod's p99 over the same window.
//
// The rule is the median and the median absolute deviation (MAD), which
// one extreme value cannot move the way it moves a mean and a standard
// deviation. A value deviates when all of these hold:
//
//   - its modified z-score 0.6745*|value-median|/MAD is at least Score, or
//     MAD is zero (more than half of the peers share one value) and the
//     value is not that value;
//   - it is at least Ratio times the median (above) or at most the median
//     divided by Ratio (below), when the median is positive;
//   - it is at least MinDelta away from the median;
//   - it lies on the side named by Direction.
//
// Fewer than MinPeers finite values is "not enough peers": nothing is
// reported, because nothing can be told. A deviation is a difference
// between peers at the same time; it is not a cause.
func Outliers(values []float64, o PeerOptions) PeerResult {
	if o.MinPeers < DefaultMinPeers {
		o.MinPeers = DefaultMinPeers
	}
	if !(o.Score > 0) {
		o.Score = DefaultPeerScore
	}
	if !(o.Ratio > 1) {
		o.Ratio = DefaultPeerRatio
	}
	var (
		res    PeerResult
		finite []float64
	)
	for i, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			res.Skipped = append(res.Skipped, i)
			continue
		}
		finite = append(finite, v)
	}
	res.Peers = len(finite)
	if res.Peers < o.MinPeers {
		res.Reason = "not enough peers"
		res.Median, res.MAD = math.NaN(), math.NaN()
		return res
	}
	res.Enough = true
	sort.Float64s(finite)
	res.Median = median(finite)
	deviations := make([]float64, len(finite))
	for i, v := range finite {
		deviations[i] = math.Abs(v - res.Median)
	}
	sort.Float64s(deviations)
	res.MAD = median(deviations)
	for i, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		delta := v - res.Median
		if delta == 0 || math.Abs(delta) < o.MinDelta {
			continue
		}
		above := delta > 0
		if (o.Direction == Above && !above) || (o.Direction == Below && above) {
			continue
		}
		score := math.Inf(1)
		if res.MAD > 0 {
			score = 0.6745 * math.Abs(delta) / res.MAD
		}
		if score < o.Score {
			continue
		}
		ratio := math.Inf(1)
		if res.Median != 0 {
			ratio = v / res.Median
		}
		if res.Median > 0 && ((above && ratio < o.Ratio) || (!above && ratio > 1/o.Ratio)) {
			continue
		}
		if !above {
			score = -score
		}
		res.Outliers = append(res.Outliers, Deviation{Index: i, Value: v, Delta: delta, Ratio: ratio, Score: score, Above: above})
	}
	sort.SliceStable(res.Outliers, func(a, b int) bool {
		return math.Abs(res.Outliers[a].Delta) > math.Abs(res.Outliers[b].Delta)
	})
	return res
}
