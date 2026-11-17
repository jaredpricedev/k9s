// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"math"
	"strconv"
	"strings"
)

// Unit says how a value is written and how an axis maximum is rounded.
type Unit int

const (
	// Count is a plain number, such as pods or restarts.
	Count Unit = iota
	// Bytes uses binary prefixes (Ki, Mi, Gi), as Kubernetes quantities do.
	Bytes
	// Cores writes less than one core in millicores.
	Cores
	// BytesPerSecond uses decimal prefixes (kB/s, MB/s).
	BytesPerSecond
	// Ratio is a share between 0 and 1, written as a percentage.
	Ratio
	// Seconds is a duration, written in the largest unit that keeps three
	// digits: 250µs, 310ms, 41s, 2.5m, 3h.
	Seconds
	// PerSecond is a rate of events, such as requests per second.
	PerSecond
)

// UnitNamed maps the unit names used by request-path signals (seconds,
// ratio, perSecond, count, bytes, cores) onto a Unit. An unknown name is
// Count and false.
func UnitNamed(name string) (Unit, bool) {
	switch name {
	case "seconds":
		return Seconds, true
	case "ratio":
		return Ratio, true
	case "perSecond":
		return PerSecond, true
	case "count":
		return Count, true
	case "bytes":
		return Bytes, true
	case "cores":
		return Cores, true
	}
	return Count, false
}

// sig writes v with at most three significant digits and no trailing zeros.
func sig(v float64) string {
	abs := math.Abs(v)
	decimals := 0
	switch {
	case abs == 0:
		return "0"
	case abs < 10:
		decimals = 2
	case abs < 100:
		decimals = 1
	}
	text := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "-0" {
		return "0"
	}
	return text
}

func scaled(v, base float64, suffixes []string) string {
	index := 0
	for math.Abs(v) >= base && index < len(suffixes)-1 {
		v /= base
		index++
	}
	// Rounding can reach the next prefix (1023.9Mi is written 1Gi).
	if math.Abs(v) >= base-0.5 && index < len(suffixes)-1 {
		v /= base
		index++
	}
	return sig(v) + suffixes[index]
}

// Format writes one value in the unit, compactly and without colour.
func (u Unit) Format(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "?"
	}
	if v == 0 {
		return "0"
	}
	switch u {
	case Bytes:
		return scaled(v, 1024, []string{"B", "Ki", "Mi", "Gi", "Ti", "Pi"})
	case BytesPerSecond:
		return scaled(v, 1000, []string{"B/s", "kB/s", "MB/s", "GB/s", "TB/s"})
	case Cores:
		if math.Abs(v) < 0.9995 {
			return sig(v*1000) + "m"
		}
		return sig(v)
	case Ratio:
		return sig(v*100) + "%"
	case Seconds:
		switch abs := math.Abs(v); {
		case abs < 0.0009995:
			return sig(v*1e6) + "µs"
		case abs < 0.9995:
			return sig(v*1000) + "ms"
		case abs < 99.95:
			return sig(v) + "s"
		case abs < 5997:
			return sig(v/60) + "m"
		}
		return sig(v/3600) + "h"
	case PerSecond:
		return sig(v) + "/s"
	}
	return sig(v)
}

// Ceil rounds a positive maximum up to a value that makes readable ticks at
// the maximum and at half of it. Bytes round on powers of two.
func (u Unit) Ceil(v float64) float64 {
	if !(v > 0) || math.IsInf(v, 0) {
		return 1
	}
	if u == Ratio && v <= 1 {
		for _, step := range []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1} {
			if v <= step {
				return step
			}
		}
	}
	base, steps := 10.0, []float64{1, 1.5, 2, 3, 4, 5, 6, 8, 10}
	if u == Bytes && v >= 1024 {
		base, steps = 2, []float64{1, 1.25, 1.5, 2}
	}
	magnitude := math.Pow(base, math.Floor(math.Log(v)/math.Log(base)+1e-9))
	for _, step := range steps {
		// The tolerance keeps 256Mi from being pushed to 320Mi by rounding.
		if v <= step*magnitude*(1+1e-9) {
			return step * magnitude
		}
	}
	return base * magnitude
}
