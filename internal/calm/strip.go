// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"math"
	"time"

	"github.com/derailed/k9s/internal/chart"
)

// The glyphs of a strip. Each means one thing on every page:
//
//	▁        sampled, and not over its rule (the quiet baseline)
//	▂ to █   over its rule: the height is the value
//	▲        an event, such as a restart
//	·        read, and nothing happened (event strips and quiet lines)
//	×        no sample: not read, never quiet
//	▼        an episode, on the marker row
//	┊        a labelled moment, such as a rollout
const (
	baseGlyph, baseASCII   = '▁', '_'
	quietGlyph, quietASCII = '·', '.'
	eventGlyph, eventASCII = '▲', '^'
	heldGlyph, heldASCII   = '█', '#'
	bandGlyph, bandASCII   = '▼', 'v'
	guideGlyph, guideASCII = '┊', '|'
	cursorGlyph            = '┆'
	cursorASCII            = ':'
)

var (
	levelGlyphs = []rune("▁▂▃▄▅▆▇█")
	levelASCII  = []rune("_.:-=+*#")
)

type stripKind int

const (
	stripNone stripKind = iota
	stripLevels
	stripEvents
	stripHeld
	stripQuiet
	stripNotRead
)

// Strip is the timeline of one row: N columns on the shared Frame. Make
// one with Levels, Trend, Events, Held, Quiet or UnreadStrip; the zero Strip
// draws nothing (the bands still run through it).
type Strip struct {
	kind    stripKind
	points  []chart.Point
	over    func(float64) bool
	top     float64
	events  []time.Time
	held    []chart.Interval
	unknown []chart.Interval
}

// Levels is the strip of a value with a rule. A column whose value is over
// the rule is drawn as an eighth-block level in the row's colour, at least
// one step above the baseline; every other sampled column is the quiet
// baseline; a column without a sample is the gap glyph. A sample stands
// for the step that ends at it (see Held), and a column holds the largest
// sample that covers it, so a short peak is not averaged away. For a
// signal where low is bad, pass the shortfall.
func Levels(points []chart.Point, over func(v float64) bool) Strip {
	if over == nil {
		over = func(float64) bool { return false }
	}
	return Strip{kind: stripLevels, points: points, over: over}
}

// Trend is the strip of a value without a rule: its level in every sampled
// column, in the row's colour.
func Trend(points []chart.Point) Strip {
	return Strip{kind: stripLevels, points: points}
}

// Events is the strip of moments such as restarts: one mark per column in
// which an event falls. More than one event in a column is still one mark;
// the count belongs in the row's number.
func Events(at ...time.Time) Strip {
	return Strip{kind: stripEvents, events: at}
}

// Held is the strip of a condition, such as "not ready": a full block in
// the columns in which it held. The intervals are stated as the episodes
// are: a sample that crossed its rule stands for the step that ends at it
// (slowpath.Crossings).
func Held(when ...chart.Interval) Strip {
	return Strip{kind: stripHeld, held: when}
}

// Quiet is the strip of a row that was read and has nothing to show.
func Quiet() Strip { return Strip{kind: stripQuiet} }

// UnreadStrip is the strip of a row that was not read: the gap glyph all
// along.
func UnreadStrip() Strip { return Strip{kind: stripNotRead} }

// Top sets the value drawn at full height; without it the largest value of
// the strip is. Rows that are compared with each other share a Top.
func (s Strip) Top(v float64) Strip {
	s.top = v
	return s
}

// Unknown marks the stretches of an Events or Held strip that were not
// read. They are drawn with the gap glyph: an event cannot be known not to
// have happened where nothing was read.
func (s Strip) Unknown(when ...chart.Interval) Strip {
	s.unknown = append(append([]chart.Interval(nil), s.unknown...), when...)
	return s
}

// cell is one cell of a strip.
type cell struct {
	r rune
	k look
}

func level(v, top float64, levels int) int {
	if !(top > 0) {
		return 0
	}
	return min(max(int(math.Round(v/top*float64(levels-1))), 0), levels-1)
}

// draw is the strip on the frame, one cell per column. color is the row's colour; without
// one the strip is drawn in the muted colour.
func (s Strip) draw(f chart.Frame, color string, o Options) []cell {
	if f.Width < 1 || !f.End.After(f.Start) {
		return nil
	}
	pick := func(glyph, ascii rune) rune {
		if o.ASCII {
			return ascii
		}
		return glyph
	}
	if color == "" {
		color = o.Palette.Muted
	}
	hot, dim := look{color: color}, o.dim()
	gap := cell{r: pick(chart.GapGlyph, chart.GapASCII), k: dim}
	base := cell{r: pick(baseGlyph, baseASCII), k: dim}
	quiet := cell{r: pick(quietGlyph, quietASCII), k: dim}
	out := make([]cell, f.Width)
	for i := range out {
		out[i] = cell{r: ' '}
	}
	unknown := f.Marked(s.unknown)
	switch s.kind {
	case stripLevels:
		glyphs := levelGlyphs
		if o.ASCII {
			glyphs = levelASCII
		}
		values := sampled(s.points, f)
		top := s.top
		if !(top > 0) {
			for _, v := range values {
				if !math.IsNaN(v) && v > top {
					top = v
				}
			}
		}
		for col, v := range values {
			switch {
			case math.IsNaN(v):
				out[col] = gap
			case s.over == nil:
				out[col] = cell{r: glyphs[level(v, top, len(glyphs))], k: hot}
			case s.over(v):
				// One step above the baseline at least: over the rule is
				// a different glyph, not only a different colour.
				out[col] = cell{r: glyphs[max(level(v, top, len(glyphs)), 1)], k: hot}
			default:
				out[col] = base
			}
		}
	case stripEvents:
		for col := range out {
			out[col] = quiet
			if unknown[col] {
				out[col] = gap
			}
		}
		for _, t := range s.events {
			if col, ok := f.Col(t); ok {
				out[col] = cell{r: pick(eventGlyph, eventASCII), k: look{color: color, bold: true}}
			}
		}
	case stripHeld:
		held := f.Marked(s.held)
		for col := range out {
			switch {
			case unknown[col]:
				// Not read wins: a condition cannot be known to hold
				// where it was not read.
				out[col] = gap
			case held[col]:
				out[col] = cell{r: pick(heldGlyph, heldASCII), k: hot}
			default:
				out[col] = base
			}
		}
	case stripQuiet:
		for col := range out {
			out[col] = quiet
		}
	case stripNotRead:
		for col := range out {
			out[col] = gap
		}
	}
	return out
}

// step is the time one sample stands for: the step of the frame, or one
// column when the frame has none.
func step(f chart.Frame) time.Duration {
	if f.Step > 0 {
		return f.Step
	}
	return f.End.Sub(f.Start) / time.Duration(max(f.Width, 1))
}

// sampled folds samples into one value per column: the largest sample that
// covers the column, NaN for a column no sample covers. A sample covers
// the step that ends at it, as a query evaluated at a time looks back from
// it; it is how the request-path views state when a rule held, so a strip
// and the episodes drawn behind it line up.
func sampled(points []chart.Point, f chart.Frame) []float64 {
	out := make([]float64, f.Width)
	for i := range out {
		out[i] = math.NaN()
	}
	for _, p := range points {
		if math.IsNaN(p.V) {
			continue
		}
		for col, hit := range f.Marked([]chart.Interval{{Start: p.T.Add(-step(f)), End: p.T}}) {
			if hit && (math.IsNaN(out[col]) || p.V > out[col]) {
				out[col] = p.V
			}
		}
	}
	return out
}

// SampleFrame is the frame a chart.Panel is drawn in on a calm page, so
// that its samples stand where the strips put theirs: over the step that
// ends at each. It is the page's frame moved on by half a step. Render
// hands it to Chart.Draw; a view that draws a chart itself uses it too.
func SampleFrame(f chart.Frame) chart.Frame {
	if f.Width < 1 || !f.End.After(f.Start) {
		return f
	}
	half := step(f) / 2
	f.Start, f.End = f.Start.Add(half), f.End.Add(half)
	return f
}

// cursorCol is the column of the sample under the cursor: the step that
// ends at the cursor's time.
func (b Bands) cursorCol(f chart.Frame) (int, bool) {
	if b.Cursor.IsZero() || f.Width < 1 || !f.End.After(f.Start) {
		return 0, false
	}
	return f.Col(b.Cursor.Add(-step(f) / 2))
}

// through draws the labelled moments and the cursor through the cells that
// hold nothing: a level or an event is never overdrawn.
func (b Bands) through(row []cell, f chart.Frame, o Options) {
	empty := func(c cell) bool {
		return c.r == ' ' || c.r == quietGlyph || c.r == quietASCII
	}
	guide, cursor := guideGlyph, cursorGlyph
	if o.ASCII {
		guide, cursor = guideASCII, cursorASCII
	}
	for _, mark := range b.Marks {
		if col, ok := f.Col(mark.T); ok && col < len(row) && empty(row[col]) {
			row[col] = cell{r: guide, k: look{color: o.Palette.Mark}}
		}
	}
	if col, ok := b.cursorCol(f); ok && col < len(row) && empty(row[col]) {
		row[col] = cell{r: cursor, k: o.text()}
	}
}

// StripLine is one strip alone, f.Width cells wide, in the row's colour,
// with the moments and the cursor of the bands drawn through it. It is not
// shaded: Shade does that for a whole line.
func StripLine(s Strip, color string, b Bands, f chart.Frame, o Options) string {
	row := s.draw(f, color, o)
	b.through(row, f, o)
	l := newLine(o)
	l.strip(row)
	return l.String()
}
