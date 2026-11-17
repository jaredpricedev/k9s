// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package chart draws small time-series charts as terminal text.
//
// It knows nothing about where samples come from. A sample is drawn where it
// is, two samples further apart than one and a half steps are not joined,
// and a series without samples draws nothing: a hole is never drawn as zero.
package chart

import (
	"math"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// Point is one sample.
type Point struct {
	T time.Time
	V float64
}

// Kind chooses how a series is drawn.
type Kind int

const (
	// Line joins neighbouring samples.
	Line Kind = iota
	// Reference is a level such as a limit: one glyph per column at the
	// level's row, drawn under any line.
	Reference
	// OffAxis is not drawn; it only has values to read at the cursor.
	OffAxis
)

// Series is one named set of samples in ascending time order.
type Series struct {
	Name   string
	Kind   Kind
	Points []Point
	// Key marks where a line ends and names it in the legend, so a line can
	// be told from another without colour. Zero draws no mark.
	Key rune
	// Dash draws only every Dash-th dot of a line (0 and 1 are solid).
	Dash int
	// Glyph and ASCII are the reference glyphs with and without Unicode.
	Glyph, ASCII rune
	// Color is a tview colour such as "#79c7d4"; empty adds no tag.
	Color string
	// Unit overrides the panel unit for an OffAxis series.
	Unit *Unit
}

// Frame is the time axis shared by stacked panels: a window, the step of
// the sample grid, and the plot width in terminal cells.
type Frame struct {
	Start, End time.Time
	Step       time.Duration
	Width      int
}

func (f Frame) valid() bool { return f.Width > 0 && f.End.After(f.Start) }

func (f Frame) fraction(t time.Time) float64 {
	return float64(t.Sub(f.Start)) / float64(f.End.Sub(f.Start))
}

// Col is the plot column of a time; ok is false outside the window.
func (f Frame) Col(t time.Time) (col int, ok bool) {
	if !f.valid() {
		return 0, false
	}
	frac := f.fraction(t)
	if frac < 0 || frac > 1 {
		return 0, false
	}
	return min(int(frac*float64(f.Width)), f.Width-1), true
}

// Steps is the index of the last point of the sample grid.
func (f Frame) Steps() int {
	if !f.valid() || f.Step <= 0 {
		return 0
	}
	return int(f.End.Sub(f.Start) / f.Step)
}

// TimeAt is the time of grid point k, clamped to the grid.
func (f Frame) TimeAt(k int) time.Time {
	return f.Start.Add(time.Duration(min(max(k, 0), f.Steps())) * f.Step)
}

// gap is the distance beyond which two samples are not joined.
func (f Frame) gap() time.Duration {
	if f.Step <= 0 {
		return math.MaxInt64
	}
	return f.Step + f.Step/2
}

// ValueAt returns the sample taken at t, give or take half a step. There is
// no interpolation: a time without a sample has no value.
func (f Frame) ValueAt(s *Series, t time.Time) (float64, bool) {
	tolerance := f.Step / 2
	if tolerance <= 0 {
		tolerance = time.Second
	}
	best, found := time.Duration(math.MaxInt64), false
	var value float64
	for _, p := range s.Points {
		d := absDuration(p.T.Sub(t))
		if d <= tolerance && d < best {
			best, value, found = d, p.V, true
		}
	}
	return value, found
}

// Holes describes where a series has no samples inside the window.
type Holes struct {
	Samples int
	// Gaps counts interruptions between two samples.
	Gaps int
	// First and Last are the first and last sample times.
	First, Last time.Time
	// LateStart and EarlyEnd say the series does not reach a window edge.
	LateStart, EarlyEnd bool
}

// Holes reports the gaps of a series against the frame's sample grid.
func (f Frame) Holes(s *Series) Holes {
	h := Holes{Samples: len(s.Points)}
	if len(s.Points) == 0 {
		return h
	}
	h.First, h.Last = s.Points[0].T, s.Points[len(s.Points)-1].T
	for i := 1; i < len(s.Points); i++ {
		if s.Points[i].T.Sub(s.Points[i-1].T) > f.gap() {
			h.Gaps++
		}
	}
	h.LateStart = h.First.Sub(f.Start) > f.gap()
	h.EarlyEnd = f.End.Sub(h.Last) > f.gap()
	return h
}

// Options are presentation choices shared by every panel of a page.
type Options struct {
	// ASCII draws with plain characters, one level per row, for terminals
	// and fonts without braille or box drawing.
	ASCII bool
	// Cursor is a grid point index; negative draws no cursor.
	Cursor int
	// AxisColor and CursorColor are tview colours; empty adds no tag.
	AxisColor, CursorColor string
}

// Panel is one chart: series that share a unit and a Y axis starting at 0.
type Panel struct {
	Unit   Unit
	Series []Series
	// Empty is written inside the plot when no series has a sample.
	Empty string
}

// Max is the largest drawn value, and whether there is any.
func (p *Panel) Max() (float64, bool) {
	top, found := 0.0, false
	for i := range p.Series {
		if p.Series[i].Kind == OffAxis {
			continue
		}
		for _, pt := range p.Series[i].Points {
			if !found || pt.V > top {
				top, found = pt.V, true
			}
		}
	}
	return top, found
}

// Top is the value at the top of the Y axis.
func (p *Panel) Top() float64 {
	top, _ := p.Max()
	return p.Unit.Ceil(top)
}

// Tick is a labelled row of the Y axis; row 0 is the top row.
type Tick struct {
	Row   int
	Label string
}

// Ticks labels the top, the bottom and, from four rows up, the middle.
func (p *Panel) Ticks(height int) []Tick {
	if height < 1 {
		return nil
	}
	top := p.Top()
	ticks := []Tick{{Row: 0, Label: p.Unit.Format(top)}}
	if height >= 4 {
		ticks = append(ticks, Tick{Row: rowOf(top/2, top, height), Label: p.Unit.Format(top / 2)})
	}
	if height >= 2 {
		ticks = append(ticks, Tick{Row: height - 1, Label: "0"})
	}
	return ticks
}

// GutterWidth is the width the Y labels of a panel need.
func (p *Panel) GutterWidth() int {
	width := 1
	for _, tick := range p.Ticks(4) {
		width = max(width, runewidth.StringWidth(tick.Label))
	}
	return width
}

// levelOf maps a value to a level between 0 (bottom) and levels-1 (top).
func levelOf(v, top float64, levels int) int {
	if !(top > 0) || levels < 2 {
		return 0
	}
	return min(max(int(math.Round(v/top*float64(levels-1))), 0), levels-1)
}

// rowOf is the text row (0 at the top) of a value in a plot of height rows.
func rowOf(v, top float64, height int) int {
	return height - 1 - levelOf(v, top, height*4)/4
}

type cell struct {
	dots, ref, mark, glyph rune
	dotsBy, refBy, markBy  int
	cursor                 bool
}

var brailleBits = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

var asciiGlyphs = []rune{'*', '+', 'o', 'x'}

func paint(color, text string) string {
	if color == "" || text == "" {
		return text
	}
	return "[" + color + "::]" + text + "[-::]"
}

// Render draws the panel as height lines: a Y label gutter of the given
// width, the axis, and f.Width plot cells.
func (p *Panel) Render(f Frame, height, gutter int, o Options) []string {
	if height < 1 || !f.valid() {
		return nil
	}
	xDots, yDots := 2, 4
	if o.ASCII {
		xDots, yDots = 1, 1
	}
	top := p.Top()
	grid := make([][]cell, height)
	for r := range grid {
		grid[r] = make([]cell, f.Width)
	}
	lines := 0
	for index := range p.Series {
		s := &p.Series[index]
		switch s.Kind {
		case Reference:
			drawReference(grid, f, s, index, top, yDots, o)
		case Line:
			drawLine(grid, f, s, index, lines, top, xDots, yDots)
			lines++
		}
	}
	// End marks go on last so that a line never hides the name of another.
	for index := range p.Series {
		s := &p.Series[index]
		if s.Kind != Line || s.Key == 0 || len(s.Points) == 0 {
			continue
		}
		last := s.Points[len(s.Points)-1]
		col, ok := f.Col(last.T)
		if !ok {
			continue
		}
		row := height - 1 - levelOf(last.V, top, height*yDots)/yDots
		for col > 0 && grid[row][col].mark != 0 {
			col--
		}
		grid[row][col].mark, grid[row][col].markBy = s.Key, index
	}
	if o.Cursor >= 0 {
		if col, ok := f.Col(f.TimeAt(o.Cursor)); ok {
			for r := range grid {
				grid[r][col].cursor = true
			}
		}
	}
	_, drawn := p.Max()
	ticks := make(map[int]string)
	for _, tick := range p.Ticks(height) {
		if _, taken := ticks[tick.Row]; !taken {
			ticks[tick.Row] = tick.Label
		}
	}
	bar, tickBar, cursor := "│", "┤", '┆'
	if o.ASCII {
		bar, tickBar, cursor = "|", "+", ':'
	}
	out := make([]string, 0, height)
	for r := range grid {
		var b strings.Builder
		label, labelled := ticks[r]
		if !drawn {
			// Without a sample there is no scale to label.
			label, labelled = "", false
		}
		axis := bar
		if labelled {
			axis = tickBar
		}
		b.WriteString(paint(o.AxisColor, strings.Repeat(" ", max(gutter-runewidth.StringWidth(label), 0))+label+" "+axis))
		if !drawn && p.Empty != "" && r == height/2 {
			text := runewidth.Truncate(p.Empty, max(f.Width-2, 0), "")
			b.WriteString(" " + text)
			out = append(out, b.String())
			continue
		}
		// Runs of one colour share a tag, which keeps the text short.
		run, runColor := "", ""
		flush := func() {
			b.WriteString(paint(runColor, run))
			run = ""
		}
		for c := range grid[r] {
			glyph, color := ' ', ""
			switch cl := &grid[r][c]; {
			case cl.mark != 0:
				glyph, color = cl.mark, p.Series[cl.markBy].Color
			case cl.glyph != 0:
				glyph, color = cl.glyph, p.Series[cl.dotsBy].Color
			case cl.dots != 0:
				glyph, color = 0x2800+cl.dots, p.Series[cl.dotsBy].Color
			case cl.ref != 0:
				glyph, color = cl.ref, p.Series[cl.refBy].Color
			case cl.cursor:
				glyph, color = cursor, o.CursorColor
			}
			if color != runColor {
				flush()
				runColor = color
			}
			run += string(glyph)
		}
		run = strings.TrimRight(run, " ")
		flush()
		out = append(out, b.String())
	}
	return out
}

func drawReference(grid [][]cell, f Frame, s *Series, index int, top float64, yDots int, o Options) {
	glyph := s.Glyph
	if o.ASCII {
		glyph = s.ASCII
	}
	if glyph == 0 {
		glyph = '-'
	}
	if len(s.Points) == 0 {
		return
	}
	height := len(grid)
	span := f.End.Sub(f.Start) / time.Duration(f.Width)
	reach := max(f.gap(), span)
	next := 0
	for col := range f.Width {
		at := f.Start.Add(span*time.Duration(col) + span/2)
		// The nearest sample decides the level; none within reach, no glyph.
		for next+1 < len(s.Points) && absDuration(s.Points[next+1].T.Sub(at)) <= absDuration(s.Points[next].T.Sub(at)) {
			next++
		}
		if absDuration(s.Points[next].T.Sub(at)) > reach {
			continue
		}
		row := height - 1 - levelOf(s.Points[next].V, top, height*yDots)/yDots
		if grid[row][col].ref == 0 {
			grid[row][col].ref, grid[row][col].refBy = glyph, index
		}
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func drawLine(grid [][]cell, f Frame, s *Series, index, order int, top float64, xDots, yDots int) {
	height, width := len(grid), f.Width*xDots
	dash := max(s.Dash, 1)
	drawn := 0
	set := func(x, y int) {
		drawn++
		if (drawn-1)%dash != 0 {
			return
		}
		cl := &grid[height-1-y/yDots][x/xDots]
		if cl.dots == 0 && cl.glyph == 0 {
			cl.dotsBy = index
		}
		if yDots == 1 {
			if cl.glyph == 0 {
				cl.glyph = asciiGlyphs[order%len(asciiGlyphs)]
			}
			return
		}
		cl.dots |= brailleBits[3-y%4][x%2]
	}
	at := func(p Point) (x, y int, ok bool) {
		frac := f.fraction(p.T)
		if frac < 0 || frac > 1 {
			return 0, 0, false
		}
		return min(int(frac*float64(width)), width-1), levelOf(p.V, top, height*yDots), true
	}
	havePrev := false
	var px, py int
	var pt time.Time
	for _, point := range s.Points {
		x, y, ok := at(point)
		if !ok {
			havePrev = false
			continue
		}
		if !havePrev || point.T.Sub(pt) > f.gap() {
			set(x, y)
		} else {
			// Walk from the previous sample, which is already drawn.
			dx, dy := x-px, y-py
			steps := max(abs(dx), abs(dy))
			for i := 1; i <= steps; i++ {
				set(px+int(math.Round(float64(dx)*float64(i)/float64(steps))), py+int(math.Round(float64(dy)*float64(i)/float64(steps))))
			}
		}
		px, py, pt, havePrev = x, y, point.T, true
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
