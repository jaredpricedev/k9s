// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package impact compares a window before a workload's rollout with a window
// after it, signal by signal, from one bounded set of Prometheus range reads.
// It states what was measured after a change. It never states that the change
// was the cause, and a signal that could not be read is never a zero.
package impact

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Comparison rules. They are fixed, documented in docs/impact.md and shown
// next to every verdict.
const (
	// MinSamples is the least number of usable samples each side needs
	// before a signal is compared at all.
	MinSamples = 4
	// RelativeThreshold is the share of the before value a change must
	// reach, in addition to the signal's absolute floor, to count.
	RelativeThreshold = 0.10
	// AtLimit is the share of the memory limit from which the peak working
	// set is described as "at limit".
	AtLimit = 0.90
	// TrafficShift is the factor by which the request rate must rise or
	// fall for ratios and latency to be flagged as not like-for-like.
	TrafficShift = 2.0
	// DefaultWidth is the window asked for on each side of the rollout.
	DefaultWidth = 30 * time.Minute
	// MinWidth is the narrowest window that is still compared.
	MinWidth = time.Minute
	// MinRateWindow is the least range a rate is taken over, so that a
	// usual scrape interval leaves several samples inside it.
	MinRateWindow = 2 * time.Minute
)

// Widths are the selectable window widths on each side of the rollout.
var Widths = []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour}

// Span is a closed time range.
type Span struct{ Start, End time.Time }

// Windows are the two compared ranges. They always have the same width.
type Windows struct {
	Rollout       time.Time
	Before, After Span
	// Asked is the width requested; Width is the width compared.
	Asked, Width time.Duration
	// Clipped says why Width is narrower than Asked, or is empty.
	Clipped string
}

// PlanWindows places equal windows on both sides of a rollout. The after
// window ends at now, or at the next rollout when one followed; the before
// window does not reach back past the previous rollout. Zero previous or
// next means there was none.
func PlanWindows(rollout, previous, next, now time.Time, asked time.Duration) (Windows, error) {
	if rollout.IsZero() {
		return Windows{}, errors.New("the rollout time is not known")
	}
	rollout, now = rollout.UTC().Truncate(time.Second), now.UTC().Truncate(time.Second)
	if asked <= 0 {
		asked = DefaultWidth
	}
	w := Windows{Rollout: rollout, Asked: asked, Width: asked}
	if rollout.After(now) {
		return w, errors.New("the rollout time is later than this machine's clock")
	}
	if since := now.Sub(rollout); !next.IsZero() && next.After(rollout) && next.Before(now) {
		if until := next.UTC().Truncate(time.Second).Sub(rollout); until < w.Width {
			w.Width, w.Clipped = until, "the next rollout came "+Dur(until)+" later"
		}
	} else if since < w.Width {
		w.Width, w.Clipped = since, "the rollout was "+Dur(since)+" before this read"
	}
	if !previous.IsZero() && previous.Before(rollout) {
		if since := rollout.Sub(previous.UTC().Truncate(time.Second)); since < w.Width {
			w.Width, w.Clipped = since, "the previous rollout was only "+Dur(since)+" earlier"
		}
	}
	if w.Width < MinWidth {
		return w, fmt.Errorf("only %s can be compared (%s); at least %s is needed on each side", Dur(w.Width), w.Clipped, Dur(MinWidth))
	}
	w.Before = Span{Start: rollout.Add(-w.Width), End: rollout}
	w.After = Span{Start: rollout, End: rollout.Add(w.Width)}
	return w, nil
}

// Unit says how a signal's value is written.
type Unit int

const (
	UnitPerSecond Unit = iota
	UnitRatio
	UnitSeconds
	UnitCount
	UnitBytes
	UnitCores
)

// Direction says which way a signal moves when it degrades.
type Direction int

const (
	// Neutral signals are reported as higher or lower, never worse or better.
	Neutral Direction = iota
	HigherWorse
	LowerWorse
)

// Reduce says how the samples of one side become one number.
type Reduce int

const (
	ReduceMean Reduce = iota
	ReduceMax
	ReduceMin
	// ReduceIncrease sums the positive steps of a counter.
	ReduceIncrease
	// ReduceRatio divides summed numerator samples by summed denominator
	// samples taken at the same times.
	ReduceRatio
	// ReduceQuantile interpolates a quantile from summed bucket rates.
	ReduceQuantile
)

// Signal is one compared measurement.
type Signal struct {
	ID, Label string
	// Phrase is how the signal reads inside the summary sentence.
	Phrase    string
	Unit      Unit
	Direction Direction
	Reduce    Reduce
	// Floor is the smallest absolute change that counts, in the signal's
	// own unit, alongside RelativeThreshold.
	Floor float64
	// Rate marks a signal derived from rate(): after-samples whose rate
	// window still reaches back across the rollout are left out.
	Rate bool
	// Quantile is set for ReduceQuantile signals.
	Quantile float64
}

// Signal identifiers.
const (
	SigRequests  = "requests"
	SigErrors    = "errors"
	SigP50       = "p50"
	SigP95       = "p95"
	SigP99       = "p99"
	SigRestarts  = "restarts"
	SigMemory    = "memory"
	SigMemLimit  = "memlimit"
	SigCPU       = "cpu"
	SigThrottled = "throttled"
	SigReady     = "ready"
)

// Signals lists every compared signal in display order.
var Signals = []Signal{
	{ID: SigRequests, Label: "Request rate", Phrase: "request rate", Unit: UnitPerSecond, Reduce: ReduceMean, Floor: 0.1, Rate: true},
	{ID: SigErrors, Label: "Error ratio", Phrase: "error ratio", Unit: UnitRatio, Direction: HigherWorse, Reduce: ReduceRatio, Floor: 0.001, Rate: true},
	{ID: SigP50, Label: "Latency p50", Phrase: "p50", Unit: UnitSeconds, Direction: HigherWorse, Reduce: ReduceQuantile, Floor: 0.005, Rate: true, Quantile: 0.50},
	{ID: SigP95, Label: "Latency p95", Phrase: "p95", Unit: UnitSeconds, Direction: HigherWorse, Reduce: ReduceQuantile, Floor: 0.005, Rate: true, Quantile: 0.95},
	{ID: SigP99, Label: "Latency p99", Phrase: "p99", Unit: UnitSeconds, Direction: HigherWorse, Reduce: ReduceQuantile, Floor: 0.005, Rate: true, Quantile: 0.99},
	{ID: SigRestarts, Label: "Restarts", Phrase: "restarts", Unit: UnitCount, Direction: HigherWorse, Reduce: ReduceIncrease, Floor: 1},
	{ID: SigMemory, Label: "Memory peak", Phrase: "memory peak", Unit: UnitBytes, Reduce: ReduceMax, Floor: 1 << 20},
	{ID: SigMemLimit, Label: "Memory/limit", Phrase: "memory/limit", Unit: UnitRatio, Direction: HigherWorse, Reduce: ReduceMax, Floor: 0.05},
	{ID: SigCPU, Label: "CPU usage", Phrase: "CPU", Unit: UnitCores, Reduce: ReduceMean, Floor: 0.01, Rate: true},
	{ID: SigThrottled, Label: "CPU throttled", Phrase: "CPU throttled", Unit: UnitRatio, Direction: HigherWorse, Reduce: ReduceMean, Floor: 0.01, Rate: true},
	{ID: SigReady, Label: "Ready (min)", Phrase: "ready replicas", Unit: UnitCount, Direction: LowerWorse, Reduce: ReduceMin, Floor: 1},
}

// Rule states the signal's threshold in words.
func (s *Signal) Rule() string {
	return fmt.Sprintf("counts as changed from %s and %d%% of before; needs %d samples on each side",
		s.FormatDelta(s.Floor)[1:], int(RelativeThreshold*100), MinSamples)
}

// Outcome states say whether a signal was compared.
const (
	// Compared: both sides had enough samples and Verdict is set.
	Compared = "compared"
	// TooFewSamples: the series exist but a side has under MinSamples.
	TooFewSamples = "not enough samples"
	// NotAvailable: the source answered and no series matched.
	NotAvailable = "not available"
	// NotRead: the read failed, was denied or was cut short.
	NotRead = "not read"
)

// Verdict words. They compare two measurements; they name no cause.
const (
	Worse     = "worse"
	Better    = "better"
	Unchanged = "unchanged"
	Higher    = "higher"
	Lower     = "lower"
)

// Side is one window's reduced value.
type Side struct {
	Value   float64
	Samples int
	// Valid is false when no value could be formed, however many samples
	// were read (for example a ratio with no requests).
	Valid bool
}

// Point is one sample of a signal's display series.
type Point struct {
	T time.Time
	V float64
}

// Outcome is one signal's comparison.
type Outcome struct {
	Signal        Signal
	State         string
	Reason        string
	Before, After Side
	Verdict       string
	// Skipped counts after-samples left out because their rate window
	// reached back across the rollout.
	Skipped int
	// Series is the per-step value across both windows, for the sparkline.
	Series []Point
	// Queries are the exact PromQL texts behind the signal.
	Queries []string
	// Notes are caveats specific to this signal.
	Notes []string
}

// Delta is after minus before.
func (o *Outcome) Delta() float64 { return o.After.Value - o.Before.Value }

// Word is the verdict when compared and the state otherwise.
func (o *Outcome) Word() string {
	if o.State == Compared {
		return o.Verdict
	}
	return o.State
}

// compare applies the threshold: a change counts when it reaches both the
// signal's absolute floor and RelativeThreshold of the before value.
func compare(s *Signal, before, after float64) string {
	delta := after - before
	if math.Abs(delta) < math.Max(s.Floor, RelativeThreshold*math.Abs(before))-1e-12 {
		return Unchanged
	}
	up := delta > 0
	switch s.Direction {
	case HigherWorse:
		if up {
			return Worse
		}
		return Better
	case LowerWorse:
		if up {
			return Better
		}
		return Worse
	default:
		if up {
			return Higher
		}
		return Lower
	}
}

// Format writes a value in the signal's unit.
func (s *Signal) Format(v float64) string {
	switch s.Unit {
	case UnitPerSecond:
		return trim(v, 3) + "/s"
	case UnitRatio:
		return percent(v)
	case UnitSeconds:
		return seconds(v)
	case UnitBytes:
		return bytesText(v)
	case UnitCores:
		if math.Abs(v) < 1 {
			return strconv.FormatFloat(math.Round(v*1000), 'f', 0, 64) + "m"
		}
		return trim(v, 3)
	default:
		return trim(v, 3)
	}
}

// FormatDelta writes a signed absolute change. Ratios change in percentage
// points (pp), so that "+5pp" is never read as "+5%".
func (s *Signal) FormatDelta(d float64) string {
	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}
	if s.Unit == UnitRatio {
		return sign + trim(d*100, 3) + "pp"
	}
	return sign + s.Format(d)
}

// PercentChange writes the relative change: "+12%", "-40%", "x115" from a
// tenfold rise, "new" when before was zero and "0%" when nothing moved.
func PercentChange(before, after float64) string {
	switch {
	case before == after:
		return "0%"
	case before == 0:
		return "new"
	}
	ratio := after / before
	if ratio >= 10 {
		return "x" + strconv.FormatFloat(ratio, 'f', 0, 64)
	}
	change := (ratio - 1) * 100
	text := strconv.FormatFloat(change, 'f', 0, 64)
	if text == "0" || text == "-0" {
		text = strconv.FormatFloat(change, 'f', 1, 64)
	}
	if change > 0 {
		text = "+" + text
	}
	return text + "%"
}

// trim writes v with about the given significant digits and no trailing zeros.
func trim(v float64, digits int) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	decimals := digits - 1 - int(math.Floor(math.Log10(math.Abs(v))))
	decimals = min(max(decimals, 0), 4)
	text := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return text
}

func percent(v float64) string {
	p := v * 100
	switch {
	case p == 0:
		return "0%"
	case math.Abs(p) < 0.01:
		return "<0.01%"
	case math.Abs(p) < 10:
		return trim(p, 2) + "%"
	default:
		return strconv.FormatFloat(p, 'f', 1, 64) + "%"
	}
}

func seconds(v float64) string {
	switch a := math.Abs(v); {
	case a == 0:
		return "0s"
	case a < 0.001:
		return trim(v*1e6, 3) + "µs"
	case a < 1:
		return trim(v*1000, 3) + "ms"
	default:
		return trim(v, 3) + "s"
	}
}

func bytesText(v float64) string {
	units := []string{"B", "Ki", "Mi", "Gi", "Ti"}
	index := 0
	for math.Abs(v) >= 1024 && index < len(units)-1 {
		v /= 1024
		index++
	}
	return trim(v, 3) + units[index]
}

// Dur renders a duration compactly: 45s, 12m, 1h30m.
func Dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		if s := int(d.Seconds()) % 60; s != 0 && d < 10*time.Minute {
			return fmt.Sprintf("%dm%ds", int(d.Minutes()), s)
		}
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		if m := int(d.Minutes()) % 60; m != 0 {
			return fmt.Sprintf("%dh%dm", int(d.Hours()), m)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
