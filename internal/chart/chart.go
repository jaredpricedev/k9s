// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package chart draws small time-series charts as terminal text.
//
// It knows nothing about where samples come from. A sample is drawn where it
// is, two samples further apart than one and a half steps are not joined,
// and a series without samples draws nothing: a hole is never drawn as zero.
//
// A plot is drawn in one of three forms. The default fills a panel's only
// line as an area of eighth blocks and draws several lines as stepped lines
// of box-drawing characters; Dots draws every line in braille; ASCII uses
// plain characters. The rules above hold in each of them.
package chart

import (
	"math"
	"sort"
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
	// be told from another without colour. Zero draws no mark, and a filled
	// area has none: it is the only one in its panel.
	Key rune
	// Dash draws only every Dash-th dot of a braille line (0 and 1 are
	// solid). It is ignored by the Solid and Bold strokes and by the
	// default style, whose lines are always continuous.
	Dash int
	// Stroke chooses how a line is drawn in braille; the zero value is
	// Thin. The default style has one stroke.
	Stroke Stroke
	// Marker is repeated along a line so that it can be told from another
	// without colour and without dashes. Zero draws none. The plain
	// character form draws a marker only when it is a plain character
	// itself (a letter); there each line also has its own character.
	Marker rune
	// MarkerEvery is the distance between markers in cells (0 means 12).
	MarkerEvery int
	// Glyph and ASCII are the reference glyphs with and without Unicode.
	Glyph, ASCII rune
	// Fill asks for this line to be the filled area of its panel although
	// the panel has other lines (a median between its minimum and maximum).
	// A panel's only line is filled without it. See Panel.Filled.
	Fill bool
	// Limit marks a Reference that the data is held under. A filled area
	// that comes Near it is drawn in Options.WarnColor; a caller says the
	// same in words, because a colour is never the only signal.
	Limit bool
	// Color is a tview colour such as "#79c7d4"; empty adds no tag.
	Color string
	// Unit overrides the panel unit for an OffAxis series.
	Unit *Unit
}

// Stroke chooses how a Line series is drawn in the Dots style.
type Stroke int

const (
	// Thin walks from sample to sample one dot at a time.
	Thin Stroke = iota
	// Solid is continuous: every dot column the line crosses is filled
	// from where the line enters it to where it leaves, so a steep rise or
	// a sawtooth is one unbroken stroke. Dash is ignored.
	Solid
	// Bold is Solid drawn two dots thick, for a single line that must read
	// at a glance in a small plot.
	Bold
)

// Style chooses the glyphs lines are drawn with.
type Style int

const (
	// Auto fills a panel's only line as an area of eighth blocks, which
	// reads at a glance, and draws several lines that share a panel as
	// stepped lines of box-drawing characters.
	Auto Style = iota
	// Dots draws every line in braille, eight dots per cell: finer than
	// the other forms and harder to read. Sparklines follow it.
	Dots
)

// Next is the style the style key switches to.
func (s Style) Next() Style {
	if s == Auto {
		return Dots
	}
	return Auto
}

// FillGlyph names a filled series in a legend, as a letter names a line.
const FillGlyph = '█'

// NearLimit is the share of a limit from which a value is said to be at
// it: a filled area changes colour there and the readout says so.
const NearLimit = 0.95

// Near reports whether a value is at a limit: within NearLimit of it or
// above it.
func Near(v, limit float64) bool { return limit > 0 && v >= limit*NearLimit }

// Markers are distinct single-cell marks, one per line of a plot.
var Markers = []rune{'●', '■', '▲', '◆', '▼', '○', '□', '△'}

// MarkerFor returns the marker of the n-th line of a plot.
func MarkerFor(n int) rune { return Markers[((n%len(Markers))+len(Markers))%len(Markers)] }

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
	// Style chooses the glyphs of lines; ASCII overrides it.
	Style Style
	// WarnColor is the tview colour of a filled area where it is Near a
	// Limit; empty keeps the colour of the series.
	WarnColor string
}

// form is how a plot is drawn once ASCII and the style are settled.
type form int

const (
	// formBlocks draws areas in eighth blocks and lines in box drawing.
	formBlocks form = iota
	// formDots draws lines in braille.
	formDots
	// formPlain draws with plain characters, one level per row.
	formPlain
)

func (o Options) form() form {
	switch {
	case o.ASCII:
		return formPlain
	case o.Style == Dots:
		return formDots
	}
	return formBlocks
}

// scale maps values to the rows of a plot in one form.
type scale struct {
	top    float64
	height int
	form   form
}

// dots is the number of dot columns and dot rows in one cell of a line in
// braille (two by four) or plain characters (one by one).
func (s scale) dots() (x, y int) {
	if s.form == formPlain {
		return 1, 1
	}
	return 2, 4
}

// eighths is the height of a value in eighths of a row above the bottom
// edge of the plot: 0 for nothing, 8*height for the top of the axis.
func (s scale) eighths(v float64) int {
	if !(s.top > 0) || s.height < 1 || math.IsNaN(v) {
		return 0
	}
	return min(max(int(math.Round(v/s.top*float64(8*s.height))), 0), 8*s.height)
}

// row is the text row (0 at the top) a value is drawn in. In blocks it is
// the row whose share of the scale holds the value: with H rows each is one
// H-th of the axis, the bottom row starts at zero and the top row ends at
// the top of the axis. A value on the edge between two rows belongs to the
// row nearer the middle of the axis (the middle edge itself to the upper
// row). Round levels fall on such edges all the time; this keeps them off
// the top and the bottom row, which are labelled with the scale itself.
func (s scale) row(v float64) int {
	switch s.form {
	case formDots:
		return s.height - 1 - levelOf(v, s.top, s.height*4)/4
	case formPlain:
		return s.height - 1 - levelOf(v, s.top, s.height)
	}
	e := s.eighths(v)
	above := e / 8
	if e%8 == 0 && e > 4*s.height {
		above--
	}
	return s.height - 1 - min(max(above, 0), s.height-1)
}

// Panel is one chart: series that share a unit and a Y axis starting at 0.
type Panel struct {
	Unit   Unit
	Series []Series
	// Empty is written inside the plot when no series has a sample.
	Empty string
	// Ceiling, when positive, is the top of the Y axis instead of the
	// rounded maximum of the panel's own series, so that panels drawn side
	// by side share one scale. A value above it is drawn at the top row.
	Ceiling float64
	// Bare draws the axis bar without the label gutter, for a panel that
	// sits to the right of one that already carries the shared labels.
	Bare bool
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
	if p.Ceiling > 0 {
		return p.Ceiling
	}
	top, _ := p.Max()
	return p.Unit.Ceil(top)
}

// Filled is the index of the series drawn as a filled area with these
// options, or -1: the panel's only line, or the first line that asks for it
// with Fill. The braille and the plain character form fill nothing.
func (p *Panel) Filled(o Options) int {
	if o.form() != formBlocks {
		return -1
	}
	lines, only, asked := 0, -1, -1
	for i := range p.Series {
		if p.Series[i].Kind != Line {
			continue
		}
		lines++
		only = i
		if asked < 0 && p.Series[i].Fill {
			asked = i
		}
	}
	switch {
	case asked >= 0:
		return asked
	case lines == 1:
		return only
	}
	return -1
}

// Tick is a labelled row of the Y axis; row 0 is the top row.
type Tick struct {
	Row   int
	Label string
}

// Ticks labels the top, the bottom and, from four rows up, the middle. A
// row that carries a level line (a limit, a request) is labelled with that
// level's own value instead, so that a line never sits on a row that names
// another number; the top and the bottom row keep the scale.
//
// Ticks are those of the default style; TicksFor takes the options a plot
// is drawn with, because each form has its own rows.
func (p *Panel) Ticks(height int) []Tick { return p.TicksFor(height, Options{}) }

// TicksFor labels the rows of a plot drawn with the given options.
func (p *Panel) TicksFor(height int, o Options) []Tick {
	return p.ticks(scale{top: p.Top(), height: height, form: o.form()})
}

func (p *Panel) ticks(sc scale) []Tick {
	height := sc.height
	if height < 1 {
		return nil
	}
	top := sc.top
	ticks := []Tick{{Row: 0, Label: p.Unit.Format(top)}}
	if height >= 2 {
		ticks = append(ticks, Tick{Row: height - 1, Label: "0"})
	}
	taken := map[int]bool{0: true, height - 1: true}
	for i := range p.Series {
		s := &p.Series[i]
		if s.Kind != Reference || len(s.Points) == 0 {
			continue
		}
		// The latest level names the row; where a level stepped during
		// the window, the earlier part runs on an unlabelled row.
		level := s.Points[len(s.Points)-1].V
		if row := sc.row(level); !taken[row] {
			taken[row] = true
			ticks = append(ticks, Tick{Row: row, Label: p.Unit.Format(level)})
		}
	}
	if row := sc.row(top / 2); height >= 4 && !taken[row] {
		ticks = append(ticks, Tick{Row: row, Label: p.Unit.Format(top / 2)})
	}
	sort.Slice(ticks, func(a, b int) bool { return ticks[a].Row < ticks[b].Row })
	return ticks
}

// GutterWidth is the width the Y labels of a panel need at any height.
func (p *Panel) GutterWidth() int {
	width := 1
	for _, tick := range p.Ticks(4) {
		width = max(width, runewidth.StringWidth(tick.Label))
	}
	for i := range p.Series {
		if s := &p.Series[i]; s.Kind == Reference && len(s.Points) > 0 {
			width = max(width, runewidth.StringWidth(p.Unit.Format(s.Points[len(s.Points)-1].V)))
		}
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

// cell is one plot cell. glyph is a plain line character or a box-drawing
// one; fill is the height of an area in the cell, in eighths.
type cell struct {
	dots, ref, mark, glyph rune
	dotsBy, refBy, markBy  int
	fill, fillBy           int
	// fillTop marks the cell in which an area ends: the value itself.
	fillTop, warn bool
	cursor        bool
}

var blockGlyphs = []rune(" ▁▂▃▄▅▆▇█")

var brailleBits = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

var asciiGlyphs = []rune{'*', '+', 'o', 'x'}

func paint(color, text string) string {
	if color == "" || text == "" {
		return text
	}
	if strings.HasSuffix(color, reverseSuffix) {
		return "[" + color + "]" + text + "[-::-]"
	}
	return "[" + color + "::]" + text + "[-::]"
}

// reverseSuffix on a colour asks for reverse video: the colour becomes the
// background and the glyph is cut out of it. A level that crosses the body
// of an area is drawn this way, so the fill carries on behind the line.
const reverseSuffix = "::r"

// Render draws the panel as height lines: a Y label gutter of the given
// width, the axis, and f.Width plot cells.
func (p *Panel) Render(f Frame, height, gutter int, o Options) []string {
	if height < 1 || !f.valid() {
		return nil
	}
	sc := scale{top: p.Top(), height: height, form: o.form()}
	grid := make([][]cell, height)
	for r := range grid {
		grid[r] = make([]cell, f.Width)
	}
	p.draw(grid, f, sc, o)
	if o.Cursor >= 0 {
		if col, ok := f.Col(f.TimeAt(o.Cursor)); ok {
			for r := range grid {
				grid[r][col].cursor = true
			}
		}
	}
	_, drawn := p.Max()
	ticks := make(map[int]string)
	for _, tick := range p.ticks(sc) {
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
		if !drawn && p.Ceiling <= 0 {
			// Without a sample there is no scale to label, unless the
			// scale is shared with panels that have samples.
			label, labelled = "", false
		}
		axis := bar
		if labelled && !p.Bare {
			axis = tickBar
		}
		if p.Bare {
			b.WriteString(paint(o.AxisColor, axis))
		} else {
			b.WriteString(paint(o.AxisColor, strings.Repeat(" ", max(gutter-runewidth.StringWidth(label), 0))+label+" "+axis))
		}
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
			glyph, color := p.glyph(&grid[r][c], o, cursor)
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

// strokes is what drawing the lines of a panel leaves behind for its marks:
// per line the row it runs in at each column, whether it is level there,
// and its place among the lines.
type strokes struct {
	traces map[int][]int
	flats  map[int][]bool
	orders map[int]int
	filled int
}

// draw puts every series of the panel into the grid: levels, lines, the
// filled area, and last the marks that name the lines.
func (p *Panel) draw(grid [][]cell, f Frame, sc scale, o Options) {
	d := strokes{traces: make(map[int][]int), flats: make(map[int][]bool), orders: make(map[int]int), filled: p.Filled(o)}
	lines := 0
	// The lowest limit of each column, for the area that comes near it.
	var limit []float64
	for index := range p.Series {
		s := &p.Series[index]
		switch s.Kind {
		case Reference:
			levels := drawReference(grid, f, s, index, sc, o)
			if s.Limit {
				limit = lowest(limit, levels)
			}
		case Line:
			switch {
			case index == d.filled:
				// Drawn below, once every limit is known.
			case sc.form == formBlocks:
				d.traces[index], d.flats[index] = drawSteps(grid, f, s, index, sc)
			default:
				xDots, yDots := sc.dots()
				d.traces[index] = drawLine(grid, f, s, index, lines, sc.top, xDots, yDots)
			}
			d.orders[index] = lines
			lines++
		}
	}
	if d.filled >= 0 {
		drawArea(grid, columnValues(f, &p.Series[d.filled]), d.filled, sc, limit)
		levelInBody(grid, d.traces)
	}
	p.endMarks(grid, f, sc, d.filled)
	p.markers(grid, o, &d)
}

// levelInBody thins a line that runs through the body of an area (the
// minimum under a filled median) to its level, one dash per column: the
// runs that join its levels would cut the area open.
func levelInBody(grid [][]cell, traces map[int][]int) {
	for row := range grid {
		for col := range grid[row] {
			cl := &grid[row][col]
			if cl.glyph == 0 || cl.fill == 0 || cl.fillTop {
				continue
			}
			if trace := traces[cl.dotsBy]; col < len(trace) && trace[col] == row {
				cl.glyph = '─'
			} else {
				cl.glyph = 0
			}
		}
	}
}

// endMarks writes the letter that ends each line. They go on after every
// line is drawn so that a line never hides the name of another. An area
// has no end mark: it is the only one of its panel.
func (p *Panel) endMarks(grid [][]cell, f Frame, sc scale, filled int) {
	for index := range p.Series {
		s := &p.Series[index]
		if s.Kind != Line || s.Key == 0 || len(s.Points) == 0 || index == filled {
			continue
		}
		last := s.Points[len(s.Points)-1]
		col, ok := f.Col(last.T)
		if !ok {
			continue
		}
		row := sc.row(last.V)
		for col > 0 && grid[row][col].mark != 0 {
			col--
		}
		grid[row][col].mark, grid[row][col].markBy = s.Key, index
	}
}

// markers repeat a line's marker along it, each line starting at its own
// offset so that two lines running together do not hide each other's.
func (p *Panel) markers(grid [][]cell, o Options, d *strokes) {
	for index := range p.Series {
		s := &p.Series[index]
		// The plain character form draws only markers that are plain
		// characters themselves, such as a line's letter.
		if s.Kind != Line || s.Marker == 0 || (o.ASCII && s.Marker >= 0x80) || index == d.filled {
			continue
		}
		every := s.MarkerEvery
		if every <= 0 {
			every = 12
		}
		trace, flat := d.traces[index], d.flats[index]
		// A marker right before the letter that ends the line would
		// only repeat it.
		end := len(trace)
		if s.Key != 0 {
			for end > 0 && trace[end-1] < 0 {
				end--
			}
			end -= 4
		}
		for col := (d.orders[index]*5 + every/2) % every; col < end; col += every {
			at := col
			if len(flat) > 0 {
				// A stepped line keeps its turns: the marker moves on to
				// the next level stretch, and is left out when there is
				// none before the next marker's place.
				limit := min(col+every/2, end)
				for at < limit && !flat[at] {
					at++
				}
				if at >= limit {
					continue
				}
			}
			if row := trace[at]; row >= 0 && grid[row][at].mark == 0 {
				grid[row][at].mark, grid[row][at].markBy = s.Marker, index
			}
		}
	}
}

// glyph is what one cell shows and in which colour. What is drawn over
// what is decided here: a mark names a line and goes over everything; the
// cell in which an area ends is the value and nothing else is drawn over
// it; a line goes over a level; a level goes over the body of an area, so
// that it stays visible; the cursor shows only where nothing else is.
func (p *Panel) glyph(cl *cell, o Options, cursor rune) (glyph rune, color string) {
	switch {
	case cl.mark != 0:
		return cl.mark, p.Series[cl.markBy].Color
	case cl.fill > 0 && cl.fillTop:
		return blockGlyphs[cl.fill], p.fillColor(cl, o)
	case cl.glyph != 0:
		return cl.glyph, p.Series[cl.dotsBy].Color
	case cl.dots != 0:
		return 0x2800 + cl.dots, p.Series[cl.dotsBy].Color
	case cl.ref != 0:
		if fill := p.fillColor(cl, o); cl.fill == len(blockGlyphs)-1 && !o.ASCII && fill != "" {
			return cl.ref, fill + reverseSuffix
		}
		return cl.ref, p.Series[cl.refBy].Color
	case cl.fill > 0:
		return blockGlyphs[cl.fill], p.fillColor(cl, o)
	case cl.cursor:
		return cursor, o.CursorColor
	}
	return ' ', ""
}

// fillColor is the colour of an area cell: the cursor's in the cursor
// column, the warning where the value is near a limit, else the series'.
func (p *Panel) fillColor(cl *cell, o Options) string {
	switch {
	case cl.cursor && o.CursorColor != "":
		return o.CursorColor
	case cl.warn && o.WarnColor != "":
		return o.WarnColor
	}
	return p.Series[cl.fillBy].Color
}

// lowest keeps the smaller of two levels per column; NaN is no level.
func lowest(a, b []float64) []float64 {
	if a == nil {
		return b
	}
	for i := range a {
		if i < len(b) && !math.IsNaN(b[i]) && (math.IsNaN(a[i]) || b[i] < a[i]) {
			a[i] = b[i]
		}
	}
	return a
}

// drawReference draws a level line and returns its level in each plot
// column (NaN where it has none).
func drawReference(grid [][]cell, f Frame, s *Series, index int, sc scale, o Options) []float64 {
	glyph := s.Glyph
	if o.ASCII {
		glyph = s.ASCII
	}
	if glyph == 0 {
		glyph = '-'
	}
	levels := make([]float64, f.Width)
	for i := range levels {
		levels[i] = math.NaN()
	}
	if len(s.Points) == 0 {
		return levels
	}
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
		levels[col] = s.Points[next].V
		row := sc.row(s.Points[next].V)
		if grid[row][col].ref == 0 {
			grid[row][col].ref, grid[row][col].refBy = glyph, index
		}
	}
	return levels
}

// walk visits a line column by column, in time order: each sample inside
// the window with its plot column and, between two samples that are joined,
// the columns that lie between them with the value on the straight line
// from one to the other. joined says that a visit continues the line from
// the visit before it; a hole in the samples starts a new stretch.
func walk(f Frame, s *Series, visit func(col int, v float64, joined bool)) {
	havePrev := false
	var px int
	var pv float64
	var pt time.Time
	for _, point := range s.Points {
		col, ok := f.Col(point.T)
		if !ok {
			havePrev = false
			continue
		}
		joined := havePrev && point.T.Sub(pt) <= f.gap()
		if joined {
			for c := px + 1; c < col; c++ {
				visit(c, pv+(point.V-pv)*float64(c-px)/float64(col-px), true)
			}
		}
		visit(col, point.V, joined)
		px, pv, pt, havePrev = col, point.V, point.T, true
	}
}

// columnValues folds a line into one value per plot column: the largest
// value the line takes in the column, so that a short peak is not folded
// away, and NaN where the line is not drawn. Columns between two joined
// samples lie on the line between them; a hole stays a hole.
func columnValues(f Frame, s *Series) []float64 {
	values := make([]float64, f.Width)
	for i := range values {
		values[i] = math.NaN()
	}
	walk(f, s, func(col int, v float64, _ bool) {
		if math.IsNaN(values[col]) || v > values[col] {
			values[col] = v
		}
	})
	return values
}

// drawArea fills each column from the baseline up to its value, in eighths
// of a row. A sample of zero is still one eighth: only a column without a
// sample is empty. limit, when given, is the level per column that the
// area warns of when it comes near.
func drawArea(grid [][]cell, values []float64, index int, sc scale, limit []float64) {
	height := len(grid)
	for col, v := range values {
		if math.IsNaN(v) {
			continue
		}
		warn := col < len(limit) && !math.IsNaN(limit[col]) && Near(v, limit[col])
		left := max(sc.eighths(v), 1)
		for row := height - 1; row >= 0 && left > 0; row-- {
			cl := &grid[row][col]
			cl.fill, cl.fillBy, cl.warn = min(left, 8), index, warn
			left -= cl.fill
			cl.fillTop = left == 0
		}
	}
}

// Sides of a cell that a stepped line leaves through.
const (
	sideLeft = 1 << iota
	sideRight
	sideUp
	sideDown
)

// stepGlyphs are the box-drawing characters of a stepped line by the
// sides of the cell it connects.
var stepGlyphs = map[int]rune{
	sideLeft | sideRight: '─',
	sideUp | sideDown:    '│',
	sideLeft | sideUp:    '╯',
	sideLeft | sideDown:  '╮',
	sideRight | sideUp:   '╰',
	sideRight | sideDown: '╭',
	// A column with several samples: the line also runs up or down in it.
	sideLeft | sideRight | sideUp:            '┴',
	sideLeft | sideRight | sideDown:          '┬',
	sideLeft | sideUp | sideDown:             '┤',
	sideRight | sideUp | sideDown:            '├',
	sideLeft | sideRight | sideUp | sideDown: '┼',
	sideUp:                                   '│',
	sideDown:                                 '│',
}

// drawSteps draws one line as a continuous stepped line of box-drawing
// characters: level along a row, and a vertical run in the column where it
// changes rows, however far. A column that holds several samples is drawn
// over all the rows they reach, so a peak inside one column is not lost.
// It returns, per plot column, the row the line leaves the column in (-1
// where it is not drawn) and whether the line is level there, which is
// where a marker belongs.
func drawSteps(grid [][]cell, f Frame, s *Series, index int, sc scale) (trace []int, flat []bool) {
	type span struct {
		in, out, lo, hi int
		ok              bool
	}
	spans := make([]span, f.Width)
	pen := 0
	walk(f, s, func(col int, v float64, joined bool) {
		row := sc.row(v)
		sp := &spans[col]
		switch {
		case sp.ok:
			// Another sample in the same column.
			sp.out, sp.lo, sp.hi = row, min(sp.lo, row), max(sp.hi, row)
		case joined:
			// The line enters in the row it left the last column in.
			*sp = span{in: pen, out: row, lo: min(pen, row), hi: max(pen, row), ok: true}
		default:
			*sp = span{in: row, out: row, lo: row, hi: row, ok: true}
		}
		pen = row
	})
	trace, flat = make([]int, f.Width), make([]bool, f.Width)
	for col := range spans {
		sp := &spans[col]
		trace[col] = -1
		if !sp.ok {
			continue
		}
		trace[col], flat[col] = sp.out, sp.lo == sp.hi
		for row := sp.lo; row <= sp.hi; row++ {
			sides := 0
			if row == sp.in {
				sides |= sideLeft
			}
			if row == sp.out {
				sides |= sideRight
			}
			if row > sp.lo {
				sides |= sideUp
			}
			if row < sp.hi {
				sides |= sideDown
			}
			// Where two lines share a cell the first keeps it, unless it
			// only runs level there and this one turns: a turn says where
			// a line leaves the other, a level stretch says nothing new.
			glyph := stepGlyphs[sides]
			if cl := &grid[row][col]; cl.glyph == 0 || (cl.glyph == '─' && glyph != '─') {
				cl.glyph, cl.dotsBy = glyph, index
			}
		}
	}
	return trace, flat
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// drawLine draws one Line series in braille dots or plain characters and
// returns, per plot column, the row the line was last drawn in (-1 where it
// was not), which is where a marker of the series belongs.
func drawLine(grid [][]cell, f Frame, s *Series, index, order int, top float64, xDots, yDots int) []int {
	height, width := len(grid), f.Width*xDots
	trace := make([]int, f.Width)
	for i := range trace {
		trace[i] = -1
	}
	dash := max(s.Dash, 1)
	if s.Stroke != Thin {
		dash = 1
	}
	drawn := 0
	set := func(x, y int) {
		if x < 0 || x >= width || y < 0 || y >= height*yDots {
			return
		}
		drawn++
		if (drawn-1)%dash != 0 {
			return
		}
		row := height - 1 - y/yDots
		trace[x/xDots] = row
		cl := &grid[row][x/xDots]
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
	// column fills one dot column between two levels; a Bold stroke adds
	// the dot below (or, on the bottom line, above).
	column := func(x, from, to int) {
		lo, hi := min(from, to), max(from, to)
		if s.Stroke == Bold && yDots > 1 {
			if lo > 0 {
				lo--
			} else {
				hi++
			}
		}
		for y := lo; y <= hi; y++ {
			set(x, y)
		}
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
		joined := havePrev && point.T.Sub(pt) <= f.gap()
		switch {
		case s.Stroke != Thin && !joined:
			column(x, y, y)
		case s.Stroke != Thin:
			// Each dot column is filled from the level at which the line
			// enters it to the level at which it leaves: neighbouring
			// columns always meet, however steep the segment.
			dx, dy := x-px, y-py
			level := func(at float64) int {
				if dx == 0 {
					return y
				}
				return py + int(math.Round(float64(dy)*(at-float64(px))/float64(dx)))
			}
			if dx == 0 {
				column(x, py, y)
			}
			for cx := px; cx <= x && dx != 0; cx++ {
				from := level(math.Max(float64(cx)-0.5, float64(px)))
				to := level(math.Min(float64(cx)+0.5, float64(x)))
				column(cx, from, to)
			}
		case !joined:
			set(x, y)
		default:
			// Walk from the previous sample, which is already drawn.
			dx, dy := x-px, y-py
			steps := max(abs(dx), abs(dy))
			for i := 1; i <= steps; i++ {
				set(px+int(math.Round(float64(dx)*float64(i)/float64(steps))), py+int(math.Round(float64(dy)*float64(i)/float64(steps))))
			}
		}
		px, py, pt, havePrev = x, y, point.T, true
	}
	return trace
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
