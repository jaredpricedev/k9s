// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dashboard

import (
	"math"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
)

// Unit is how a panel's values are written: the chart unit they are drawn
// in and the factor that turns a query result into it.
type Unit struct {
	// ID is Grafana's unit id, as written in fieldConfig.defaults.unit.
	ID    string
	Chart chart.Unit
	// Scale multiplies a sample to reach the chart unit (ms to seconds).
	Scale float64
	// Known is false for a unit id this package has no mapping for; the
	// values are then plain numbers and the page names the id.
	Known bool
}

// Word names the unit for a title line: "seconds", "percent", or the
// Grafana id when it is not mapped.
func (u Unit) Word() string {
	if !u.Known {
		if u.ID == "" {
			return ""
		}
		return "unit " + u.ID + " (plain numbers)"
	}
	switch u.Chart {
	case chart.Seconds:
		return "seconds"
	case chart.Ratio:
		return "percent"
	case chart.PerSecond:
		return "per second"
	case chart.Bytes:
		return "bytes"
	case chart.BytesPerSecond:
		return "bytes per second"
	case chart.Cores:
		return "cores"
	}
	return ""
}

var unitTable = map[string]Unit{
	"": {Chart: chart.Count, Scale: 1, Known: true}, "none": {Chart: chart.Count, Scale: 1, Known: true},
	"short": {Chart: chart.Count, Scale: 1, Known: true}, "locale": {Chart: chart.Count, Scale: 1, Known: true},
	"percent": {Chart: chart.Ratio, Scale: 0.01, Known: true}, "percentunit": {Chart: chart.Ratio, Scale: 1, Known: true},
	"s": {Chart: chart.Seconds, Scale: 1, Known: true}, "ms": {Chart: chart.Seconds, Scale: 1e-3, Known: true},
	"µs": {Chart: chart.Seconds, Scale: 1e-6, Known: true}, "ns": {Chart: chart.Seconds, Scale: 1e-9, Known: true},
	"m": {Chart: chart.Seconds, Scale: 60, Known: true}, "h": {Chart: chart.Seconds, Scale: 3600, Known: true},
	"d": {Chart: chart.Seconds, Scale: 86400, Known: true}, "dtdurations": {Chart: chart.Seconds, Scale: 1, Known: true},
	"dtdurationms": {Chart: chart.Seconds, Scale: 1e-3, Known: true},
	"bytes":        {Chart: chart.Bytes, Scale: 1, Known: true}, "decbytes": {Chart: chart.Bytes, Scale: 1, Known: true},
	"kbytes": {Chart: chart.Bytes, Scale: 1024, Known: true}, "mbytes": {Chart: chart.Bytes, Scale: 1024 * 1024, Known: true},
	"gbytes": {Chart: chart.Bytes, Scale: 1024 * 1024 * 1024, Known: true},
	"Bps":    {Chart: chart.BytesPerSecond, Scale: 1, Known: true}, "binBps": {Chart: chart.BytesPerSecond, Scale: 1, Known: true},
	"reqps": {Chart: chart.PerSecond, Scale: 1, Known: true}, "ops": {Chart: chart.PerSecond, Scale: 1, Known: true},
	"rps": {Chart: chart.PerSecond, Scale: 1, Known: true}, "wps": {Chart: chart.PerSecond, Scale: 1, Known: true},
	"iops": {Chart: chart.PerSecond, Scale: 1, Known: true}, "cps": {Chart: chart.PerSecond, Scale: 1, Known: true},
	"eps": {Chart: chart.PerSecond, Scale: 1, Known: true}, "pps": {Chart: chart.PerSecond, Scale: 1, Known: true},
	"cores": {Chart: chart.Cores, Scale: 1, Known: true},
}

// UnitFor maps a Grafana unit id. An id without a mapping is drawn as plain
// numbers and stays named, so that a number is never read in the wrong unit.
func UnitFor(id string) Unit {
	if u, ok := unitTable[id]; ok {
		u.ID = id
		return u
	}
	return Unit{ID: id, Chart: chart.Count, Scale: 1}
}

// UnitNamed maps the unit names of a saved query (seconds, ratio,
// perSecond, count, bytes, cores).
func UnitNamed(name string) Unit {
	u, ok := chart.UnitNamed(name)
	return Unit{ID: name, Chart: u, Scale: 1, Known: ok || name == ""}
}

// Level is how a threshold step reads, taken from its color name.
type Level int

const (
	// LevelNone is a step whose color is not one of the usual names.
	LevelNone Level = iota
	LevelGood
	LevelWarn
	LevelBad
)

// Step is the threshold step a value falls into.
type Step struct {
	// Word is the step's color name as the dashboard author chose it
	// (green, orange, red), or "step 2 of 3" for a color without a name.
	Word  string
	Level Level
	// From is the value the step starts at, in the unit of the query
	// result; HasFrom is false for the base step.
	From    float64
	HasFrom bool
}

var colorWords = []struct {
	word  string
	level Level
}{{"red", LevelBad}, {"orange", LevelWarn}, {"yellow", LevelWarn}, {"green", LevelGood}, {"blue", LevelNone}, {"purple", LevelNone}}

// StepFor returns the threshold step of a value. ok is false when the panel
// has no thresholds beyond a base step, or when percentage thresholds lack
// the minimum and maximum they are a share of: then nothing is said.
func (p *Panel) StepFor(value float64) (Step, bool) {
	if len(p.Thresholds) < 2 {
		return Step{}, false
	}
	at := value
	if p.Percentage {
		if !p.HasMin || !p.HasMax || p.Max <= p.Min {
			return Step{}, false
		}
		at = (value - p.Min) / (p.Max - p.Min) * 100
	}
	index := 0
	for i, step := range p.Thresholds {
		if step.Base || at >= step.Value {
			index = i
		}
	}
	chosen := p.Thresholds[index]
	out := Step{From: chosen.Value, HasFrom: !chosen.Base}
	if p.Percentage && out.HasFrom {
		out.From = p.Min + chosen.Value/100*(p.Max-p.Min)
	}
	name := strings.ToLower(chosen.Color)
	for _, known := range colorWords {
		if strings.Contains(name, known.word) {
			out.Word, out.Level = known.word, known.level
			return out, true
		}
	}
	out.Word = "step " + itoa(index+1) + " of " + itoa(len(p.Thresholds))
	return out, true
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// Reduce folds the samples of one series into the single value a stat or
// gauge shows, by the panel's reducer. word names what was computed; an
// unknown reducer falls back to the last sample and says so.
func Reduce(calc string, samples []observability.Sample) (value float64, word string, ok bool) {
	if len(samples) == 0 {
		return 0, "", false
	}
	last := samples[len(samples)-1].Value
	switch calc {
	case "", "last", "lastNotNull":
		return last, "last", true
	case "first", "firstNotNull":
		return samples[0].Value, "first", true
	case "mean":
		sum := 0.0
		for _, s := range samples {
			sum += s.Value
		}
		return sum / float64(len(samples)), "mean", true
	case "max":
		top := math.Inf(-1)
		for _, s := range samples {
			top = math.Max(top, s.Value)
		}
		return top, "max", true
	case "min":
		low := math.Inf(1)
		for _, s := range samples {
			low = math.Min(low, s.Value)
		}
		return low, "min", true
	case "sum":
		sum := 0.0
		for _, s := range samples {
			sum += s.Value
		}
		return sum, "sum", true
	}
	return last, "last (reducer " + calc + " is not computed here)", true
}

// LegendName names one series by a legendFormat: every {{label}} is
// replaced by the label's value. ok is false for an empty or automatic
// format, which AutoNames handles.
func LegendName(format string, labels map[string]string) (string, bool) {
	if format == "" || format == "__auto" {
		return "", false
	}
	var b strings.Builder
	rest := format
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			break
		}
		end := strings.Index(rest[open:], "}}")
		if end < 0 {
			break
		}
		b.WriteString(rest[:open])
		b.WriteString(labels[strings.TrimSpace(rest[open+2:open+end])])
		rest = rest[open+end+2:]
	}
	b.WriteString(rest)
	return strings.TrimSpace(b.String()), true
}

// AutoNames names the series of one query that has no legendFormat: by the
// values of the labels that differ between them, or by fallback when there
// is one series or nothing differs.
func AutoNames(series []map[string]string, fallback string) []string {
	names := make([]string, len(series))
	seen := map[string]map[string]bool{}
	for _, labels := range series {
		for key, value := range labels {
			if key == "__name__" {
				continue
			}
			if seen[key] == nil {
				seen[key] = map[string]bool{}
			}
			seen[key][value] = true
		}
	}
	var varying []string
	for key, values := range seen {
		if len(values) > 1 {
			varying = append(varying, key)
		}
	}
	sort.Strings(varying)
	for i, labels := range series {
		var parts []string
		for _, key := range varying {
			if len(varying) == 1 {
				parts = append(parts, labels[key])
			} else {
				parts = append(parts, key+"="+labels[key])
			}
		}
		names[i] = strings.Join(parts, " ")
		if names[i] == "" {
			names[i] = fallback
			// One series: say which, when its labels can.
			if len(series) == 1 {
				for _, key := range []string{"pod", "instance", "node", "service", "job"} {
					if v := labels[key]; v != "" {
						names[i] = v
						break
					}
				}
			}
		}
	}
	return names
}

// ShortNames drops the start that most names of a chart share, up to a
// dash, so that Pods of one ReplicaSet are told apart by their suffix. The
// cut is marked with an ellipsis. Names that do not share it, such as a
// "limit" line, are left alone, and so is a short common start.
func ShortNames(names []string) []string {
	out := append([]string(nil), names...)
	if len(names) < 2 {
		return out
	}
	counts := map[string]int{}
	for _, name := range names {
		for i := 7; i < len(name)-1; i++ {
			if name[i] == '-' {
				counts[name[:i+1]]++
			}
		}
	}
	best := ""
	for prefix, n := range counts {
		if n < 2 {
			continue
		}
		// The last rule only makes the choice the same on every draw: two
		// groups of names with equally long starts are otherwise picked in
		// map order.
		switch {
		case best == "", n > counts[best], n == counts[best] && len(prefix) > len(best),
			n == counts[best] && len(prefix) == len(best) && prefix < best:
			best = prefix
		}
	}
	if best == "" {
		return out
	}
	for i, name := range names {
		if strings.HasPrefix(name, best) {
			out[i] = "…" + name[len(best):]
		}
	}
	return out
}
