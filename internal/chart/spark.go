// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"math"
	"strings"
	"time"
)

// Gap glyphs mark a cell that has no sample. They are the same in a
// sparkline and in a lane, and are never a level: missing data is not zero.
const (
	GapGlyph = '×'
	GapASCII = 'x'
)

// SparkStyle chooses the glyphs of a sparkline.
type SparkStyle int

const (
	// SparkLine is a trend in eighth blocks, eight levels in one cell: the
	// height of a cell is its value. The plain character form has five
	// levels.
	SparkLine SparkStyle = iota
	// SparkBars is one eighth-block bar per value, eight levels high, for
	// rows where each value is a separate measurement (a probe request)
	// rather than a trend.
	SparkBars
	// SparkDots is a thin connected line in braille, four levels high. A
	// jump between two neighbouring cells is joined by a vertical run. It
	// is the trend of the Dots style.
	SparkDots
)

// SparkStyleOf is the sparkline that goes with a chart style.
func SparkStyleOf(s Style) SparkStyle {
	if s == Dots {
		return SparkDots
	}
	return SparkLine
}

var (
	sparkBars      = []rune("▁▂▃▄▅▆▇█")
	sparkLineASCII = []rune("_.-~^")
	sparkBarsASCII = []rune("_.:-=+*#")
	brailleBase    = rune(0x2800)
)

// SparkOptions are the presentation choices of one sparkline.
type SparkOptions struct {
	// ASCII draws with plain characters.
	ASCII bool
	Style SparkStyle
	// Max is the value drawn at the top level; zero or less uses the
	// largest value. Values above Max are drawn at the top level.
	Max float64
	// Color and GapColor are tview colours; empty adds no tag.
	Color, GapColor string
	// ColorAt, when set, overrides Color for one value (for example to
	// mark the slow requests of a row). It is never the only signal: the
	// level already shows the value.
	ColorAt func(index int, value float64) string
}

// Spark draws values as a trend one cell high, one cell per value. A NaN
// value is a missing sample and is drawn as the gap glyph. The scale starts
// at zero.
func Spark(values []float64, o SparkOptions) string {
	top := o.Max
	if !(top > 0) {
		for _, v := range values {
			if !math.IsNaN(v) && v > top {
				top = v
			}
		}
	}
	gap := GapGlyph
	if o.ASCII {
		gap = GapASCII
	}
	var glyphs []rune
	switch {
	case o.ASCII && o.Style == SparkBars:
		glyphs = sparkBarsASCII
	case o.ASCII:
		glyphs = sparkLineASCII
	case o.Style != SparkDots:
		glyphs = sparkBars
	}
	levels := 4
	if glyphs != nil {
		levels = len(glyphs)
	}
	level := func(v float64) int { return levelOf(math.Max(v, 0), top, levels) }
	var b strings.Builder
	run, runColor := "", ""
	flush := func() {
		b.WriteString(paint(runColor, run))
		run = ""
	}
	for i, v := range values {
		glyph, color := gap, o.GapColor
		if !math.IsNaN(v) {
			color = o.Color
			if o.ColorAt != nil {
				color = o.ColorAt(i, v)
			}
			l := level(v)
			if glyphs != nil {
				glyph = glyphs[l]
			} else {
				// Both dot columns sit at the value's level; the right
				// column reaches to the next value so a jump stays joined.
				dots := brailleBits[3-l][0] | brailleBits[3-l][1]
				if i+1 < len(values) && !math.IsNaN(values[i+1]) {
					next := level(values[i+1])
					for y := min(l, next); y <= max(l, next); y++ {
						dots |= brailleBits[3-y][1]
					}
				}
				glyph = brailleBase + dots
			}
		}
		if color != runColor {
			flush()
			runColor = color
		}
		run += string(glyph)
	}
	flush()
	return b.String()
}

// SparkValues folds samples into one value per cell of the frame: the
// largest sample that falls into the cell, so a short peak is not averaged
// away, and NaN for a cell without a sample. A cell wider than the sample
// step has several samples; a cell narrower than it repeats the nearest
// sample when that is about one step away or less, so a real hole in the
// samples stays a hole.
func SparkValues(points []Point, f Frame) []float64 {
	if !f.valid() {
		return nil
	}
	out := make([]float64, f.Width)
	for i := range out {
		out[i] = math.NaN()
	}
	for _, p := range points {
		if col, ok := f.Col(p.T); ok && (math.IsNaN(out[col]) || p.V > out[col]) {
			out[col] = p.V
		}
	}
	span := f.End.Sub(f.Start) / time.Duration(f.Width)
	if f.Step <= span || len(points) == 0 {
		return out
	}
	// Fewer samples than cells: an empty cell between two samples that are
	// joined takes the nearer one.
	next := 0
	for col := range out {
		if !math.IsNaN(out[col]) {
			continue
		}
		at := f.Start.Add(span*time.Duration(col) + span/2)
		for next+1 < len(points) && absDuration(points[next+1].T.Sub(at)) <= absDuration(points[next].T.Sub(at)) {
			next++
		}
		if absDuration(points[next].T.Sub(at)) <= f.gap()/2+span {
			out[col] = points[next].V
		}
	}
	return out
}
