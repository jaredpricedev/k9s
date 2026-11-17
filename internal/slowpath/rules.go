// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath

import (
	"fmt"
	"math"
	"strings"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
)

// State is the status word of a hop, a signal or a Pod row. There is no
// word for healthy: a rule can only say that it was not crossed.
type State string

const (
	// Quiet means every signal that was read stayed under its rule.
	Quiet State = "quiet"
	// Elevated means at least one signal crossed its rule in the window.
	Elevated State = "elevated"
	// Outlier means one Pod differs from its peers on a crossed signal.
	Outlier State = "outlier"
	// NotRead means nothing was read: a mapping, a metric or a permission
	// is missing. It is never quiet.
	NotRead State = "not read"
)

// Directions a signal can be bad in; the values of observability.Signal.Worse.
const (
	WorseHigh = observability.WorseHigh
	WorseLow  = observability.WorseLow
	WorseNone = observability.WorseNone
)

// Rule decides whether one sample of a signal counts as raised. It compares
// the sample with the median of the same series over the window, so that a
// signal is measured against its own usual level, and has an absolute level
// for a signal that is bad the whole window.
type Rule struct {
	// Worse is high, low or none. A rule with none never crosses.
	Worse string
	// Factor is how many times the median a sample must be (high), or what
	// fraction of it (low: at most median/Factor). Zero turns the relative
	// part off.
	Factor float64
	// Floor is the smallest distance from the median that counts, in the
	// unit of the signal. It keeps noise around a tiny median quiet.
	Floor float64
	// Always is the level at or above which a high signal crosses whatever
	// its median is. Zero means there is none.
	Always float64
	// AtZero makes a low signal cross at zero whatever its median is: no
	// free thread, no ready endpoint, an exporter that is down.
	AtZero bool
	// Counter means the series only grows (restarts); a sample crosses when
	// it is larger than the one before.
	Counter bool
	Unit    chart.Unit
}

// Rule levels. They are deliberately few and are documented in
// docs/slow-requests.md; a view shows the rule beside every status word.
const (
	// RuleFactor is the multiple of the window median a sample must reach.
	RuleFactor = 3.0
	// LatencyFloor: a hop that adds less than this cannot explain a page
	// that waits.
	LatencyFloor = 0.05
	// LatencyAlways: a 99th percentile at or above this is slow whatever
	// the usual level.
	LatencyAlways = 5.0
	// ShareFloor and ShareAlways apply to shares such as 5xx or SERVFAIL.
	ShareFloor  = 0.01
	ShareAlways = 0.05
	// ThrottleFloor and ThrottleAlways apply to the share of CPU periods
	// throttled, which is rarely zero for a container with a limit.
	ThrottleFloor  = 0.25
	ThrottleAlways = 0.5
	// MemoryAlways is the share of the memory limit in use that counts.
	MemoryAlways = 0.9
	// RateFloor is in events per second, CountFloor in whole things.
	RateFloor  = 0.1
	CountFloor = 1.0
	CoresFloor = 0.1
	BytesFloor = 64 << 20
)

// Names of the readings :slow adds to the declared profiles. They use
// kube-state-metrics and cAdvisor series, whose names are fixed.
const (
	SignalReady    = "ready"
	SignalRestarts = "restarts"
	SignalMemory   = "memory"
)

// RuleFor returns the rule of one signal: by unit and direction, with the
// few signals that need their own levels named.
func RuleFor(sig *observability.Signal) Rule {
	unit, _ := chart.UnitNamed(sig.Unit)
	rule := Rule{Worse: sig.Worse, Factor: RuleFactor, Unit: unit}
	if rule.Worse == "" {
		rule.Worse = WorseHigh
	}
	switch sig.Unit {
	case observability.UnitSeconds:
		rule.Floor, rule.Always = LatencyFloor, LatencyAlways
	case observability.UnitRatio:
		rule.Floor, rule.Always = ShareFloor, ShareAlways
	case observability.UnitPerSecond:
		rule.Floor = RateFloor
	case observability.UnitCores:
		rule.Floor = CoresFloor
	case observability.UnitBytes:
		rule.Floor = BytesFloor
	default:
		rule.Floor = CountFloor
	}
	switch sig.Name {
	case "throttled":
		if sig.Unit == observability.UnitRatio {
			rule.Floor, rule.Always = ThrottleFloor, ThrottleAlways
		}
	case SignalMemory:
		if sig.Unit == observability.UnitRatio {
			rule.Factor, rule.Floor, rule.Always = 0, 0, MemoryAlways
		}
	case SignalRestarts:
		rule.Counter = true
	}
	if rule.Worse == WorseLow {
		rule.Always, rule.AtZero = 0, true
	}
	return rule
}

// Crosses reports whether a sample counts as raised against the median of
// its own series.
func (r Rule) Crosses(value, median float64) bool {
	switch r.Worse {
	case WorseHigh:
		if r.Always > 0 && value >= r.Always {
			return true
		}
		return r.Factor > 0 && value >= r.Factor*median && value-median >= r.Floor && value > 0
	case WorseLow:
		if r.AtZero && value <= 0 {
			return true
		}
		return r.Factor > 0 && median > 0 && value <= median/r.Factor && median-value >= r.Floor
	}
	return false
}

// Level is the lowest value that crosses the rule of a signal that is bad
// when high, for a series whose usual level is median. It is infinite for
// a rule that such a value cannot cross.
func (r Rule) Level(median float64) float64 {
	level := math.Inf(1)
	if r.Worse != WorseHigh || r.Counter {
		return level
	}
	if r.Factor > 0 {
		level = math.Max(r.Factor*median, median+r.Floor)
	}
	if r.Always > 0 {
		level = math.Min(level, r.Always)
	}
	return level
}

// factor writes 3 for 3.0 and 2.5 for 2.5.
func factor(f float64) string { return fmt.Sprintf("%g", f) }

// distance writes how far from the usual level a sample must be. A share
// is measured in points, so that "25 points above 4%" is not read as a
// quarter more than 4%.
func (r Rule) distance() string {
	if r.Unit == chart.Ratio {
		points := strings.TrimSuffix(r.Unit.Format(r.Floor), "%")
		if points == "1" {
			return "1 point"
		}
		return points + " points"
	}
	return r.Unit.Format(r.Floor)
}

// Text states the rule in one sentence part, for the detail area.
func (r Rule) Text() string {
	switch {
	case r.Worse == WorseNone:
		return "shown for context; no rule (neither direction is bad)"
	case r.Counter:
		return "raised where the counter grew between two samples"
	case r.Worse == WorseLow:
		text := ""
		if r.Factor > 0 {
			text = "raised at 1/" + factor(r.Factor) + " of its window median or less and at least " + r.distance() + " below it"
		}
		if r.AtZero {
			if text != "" {
				text += "; "
			}
			text += "always raised at 0"
		}
		return text
	}
	text := ""
	if r.Factor > 0 {
		text = "raised at " + factor(r.Factor) + "x its window median and at least " + r.distance() + " above it"
	}
	if r.Always > 0 {
		if text != "" {
			text += "; "
		}
		text += "always raised at " + r.Unit.Format(r.Always)
	}
	return text
}

// phrases name a raised signal in the summary. A signal without an entry
// is named by its own name.
var phrases = map[string]string{
	"499":          "client give-ups (HTTP 499)",
	"5xx":          "5xx answers",
	"errors":       "errors",
	"p99":          "slow p99",
	"p50":          "slow median",
	"upstream-p99": "slow backend p99",
	"latency":      "slow commands",
	"queued":       "queueing",
	"free-threads": "no free threads",
	"busy-threads": "busy threads",
	"throttled":    "CPU throttling",
	"drops":        "packet drops",
	"rst":          "TCP resets",
	"dns-errors":   "DNS errors",
	"servfail":     "SERVFAIL answers",
	"up":           "exporter down",
	"connections":  "more connections",
	"active":       "more active connections",
	"clients":      "more clients",
	SignalReady:    "not Ready",
	SignalRestarts: "restarts",
	SignalMemory:   "memory near its limit",
}

// Phrase names a raised signal in plain words.
func Phrase(signal string) string {
	if text, ok := phrases[signal]; ok {
		return text
	}
	return signal + " raised"
}
