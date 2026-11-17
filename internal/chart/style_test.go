// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default style draws a panel's only line as a filled area of eighth
// blocks and several lines as stepped lines of box-drawing characters.
// These tests pin both renderers cell by cell.

// middles puts one sample in the middle of each minute, so that with a
// frame of one column per minute sample i falls in column i. NaN leaves a
// minute without a sample.
func middles(values ...float64) []Point {
	out := make([]Point, 0, len(values))
	for i, v := range values {
		if !math.IsNaN(v) {
			out = append(out, Point{T: pat(float64(i) + 0.5), V: v})
		}
	}
	return out
}

// perMinute is a frame of the given width with one column per minute.
func perMinute(width int) Frame {
	return Frame{Start: p0, End: pat(float64(width)), Step: time.Minute, Width: width}
}

// body renders a panel and returns the plot rows without the gutter and
// the axis, each cut where the drawing ends.
func body(t *testing.T, p *Panel, f Frame, height int, o Options) []string {
	t.Helper()
	const gutter = 3
	lines := p.Render(f, height, gutter, o)
	require.Len(t, lines, height)
	out := make([]string, len(lines))
	for i, line := range lines {
		runes := []rune(line)
		require.GreaterOrEqual(t, len(runes), gutter+2, "row %d has its gutter and axis", i)
		require.Contains(t, "┤│+|", string(runes[gutter+1]), "row %d: the axis is in the same cell on every row", i)
		out[i] = string(runes[gutter+2:])
		require.LessOrEqual(t, runewidth.StringWidth(out[i]), f.Width, "row %d is at most the plot wide", i)
	}
	return out
}

func TestAreaFillsFromTheBaselineInEighths(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name    string
		points  []Point
		frame   Frame
		ceiling float64
		height  int
		want    []string
	}{
		{"one row has eight levels, and a sample of zero is still one eighth",
			middles(0, 1, 2, 3, 4, 5, 6, 7, 8), perMinute(9), 8, 1,
			[]string{"▁▁▂▃▄▅▆▇█"}},
		{"two rows have sixteen levels: full rows below, the eighths on top",
			middles(0, 4, 8, 12, 16, 9), perMinute(6), 16, 2,
			[]string{
				"   ▄█▁",
				"▁▄████",
			}},
		{"a value is rounded to the nearest eighth",
			middles(1.4, 1.6, 7.49, 7.51), perMinute(4), 8, 1,
			[]string{"▁▂▇█"}},
		{"a hole in the samples is empty columns, never a low fill",
			middles(4, 4, nan, nan, nan, 4), perMinute(6), 4, 1,
			[]string{"██   █"}},
		{"a series that starts late and ends early is filled only where it has samples",
			middles(nan, nan, 2, 2, nan, nan), perMinute(6), 4, 1,
			[]string{"  ▄▄"}},
		{"a single sample is one column",
			middles(nan, nan, 2, nan), perMinute(4), 4, 1,
			[]string{"  ▄"}},
		{"a flat series is a level top",
			middles(2, 2, 2, 2), perMinute(4), 4, 2,
			[]string{
				"",
				"████",
			}},
		{"a flat zero is a baseline, not blank",
			middles(0, 0, 0), perMinute(3), 4, 2,
			[]string{
				"",
				"▁▁▁",
			}},
		{"a steep rise and fall are neighbouring columns of different height",
			middles(0, 24, 0, 24), perMinute(4), 24, 3,
			[]string{
				" █ █",
				" █ █",
				"▁█▁█",
			}},
		{"a value above the ceiling is clipped at the top row",
			middles(2, 9), perMinute(2), 4, 1,
			[]string{"▄█"}},
		{"columns between two joined samples lie on the line between them",
			[]Point{{T: pat(0.5), V: 0}, {T: pat(4.5), V: 8}}, Frame{Start: p0, End: pat(8), Step: 4 * time.Minute, Width: 8}, 8, 1,
			[]string{"▁▂▄▆█"}},
		{"columns between two samples that are not joined stay empty",
			[]Point{{T: pat(0.5), V: 8}, {T: pat(4.5), V: 8}}, Frame{Start: p0, End: pat(8), Step: time.Minute, Width: 8}, 8, 1,
			[]string{"█   █"}},
		{"a column that holds several samples shows the largest",
			middles(1, 8, 1, 2, 2, 2), Frame{Start: p0, End: pat(6), Step: time.Minute, Width: 2}, 8, 1,
			[]string{"█▂"}},
		{"one column wide",
			middles(3), perMinute(1), 4, 1,
			[]string{"▆"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := Panel{Unit: Count, Ceiling: tt.ceiling, Series: []Series{{Points: tt.points}}}
			require.Equal(t, 0, panel.Filled(Options{}), "a panel's only line is its area")
			assert.Equal(t, tt.want, body(t, &panel, tt.frame, tt.height, Options{Cursor: -1}))
			// A solid or bold stroke and a dash belong to the braille line:
			// the area is the same with them.
			panel.Series[0].Stroke, panel.Series[0].Dash = Bold, 3
			assert.Equal(t, tt.want, body(t, &panel, tt.frame, tt.height, Options{Cursor: -1}))
			// An end letter is for telling lines apart; an area has none.
			panel.Series[0].Key, panel.Series[0].Marker = 'a', 'a'
			assert.Equal(t, tt.want, body(t, &panel, tt.frame, tt.height, Options{Cursor: -1}))
		})
	}
}

// With H rows an area has 8*H levels, and the Y ticks are rows of the same
// scale: an area whose value is a tick's value ends in the tick's row, or
// fills the row under it completely and so touches it.
func TestAreaAndTicksShareOneScale(t *testing.T) {
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	for height := 1; height <= 9; height++ {
		levels := 8 * height
		for level := 0; level <= levels; level++ {
			v := float64(level)
			panel := Panel{Unit: Count, Ceiling: float64(levels), Series: []Series{{Points: middles(v)}}}
			rows := body(t, &panel, perMinute(1), height, Options{Cursor: -1})
			filled := 0
			for row := height - 1; row >= 0; row-- {
				first, _ := utf8.DecodeRuneInString(rows[row] + " ")
				eighths := slices.Index(blocks, first)
				require.GreaterOrEqual(t, eighths, 0, "height %d level %d: %q is a block", height, level, first)
				if filled < 8*(height-1-row) {
					require.Zero(t, eighths, "height %d level %d: nothing is filled above a row that is not full", height, level)
				}
				filled += eighths
			}
			require.Equal(t, max(level, 1), filled, "height %d: level %d of %d is that many eighths", height, level, levels)
		}

		panel := Panel{Unit: Count, Ceiling: float64(levels)}
		sc := scale{top: panel.Top(), height: height, form: formBlocks}
		previous := height
		for level := 0; level <= levels; level++ {
			row := sc.row(float64(level))
			require.LessOrEqual(t, row, previous, "height %d: a larger value is never drawn lower", height)
			require.GreaterOrEqual(t, row, 0)
			previous = row
			// The row a value is drawn in holds the top of its area, or
			// is the row right above an area that ends on a row edge.
			top := height - 1 - (max(level, 1)-1)/8
			switch {
			case row == top:
			case level%8 == 0:
				require.Equal(t, top-1, row, "height %d level %d", height, level)
			default:
				t.Fatalf("height %d level %d: drawn in row %d, its area ends in row %d", height, level, row, top)
			}
		}
		require.Equal(t, 0, sc.row(float64(levels)), "the top of the axis is the top row")
		require.Equal(t, height-1, sc.row(0), "zero is the bottom row")
	}

	// The ticks of every form label the top, zero and, from four rows up,
	// the middle; the middle of the block scale is the middle row.
	usage := Panel{Unit: Bytes, Series: []Series{{Points: points(time.Minute, 0, 5, flat(254*mi))}}}
	for _, tc := range []struct {
		height int
		want   []Tick
	}{
		{1, []Tick{{0, "256Mi"}}},
		{3, []Tick{{0, "256Mi"}, {2, "0"}}},
		{4, []Tick{{0, "256Mi"}, {1, "128Mi"}, {3, "0"}}},
		{5, []Tick{{0, "256Mi"}, {2, "128Mi"}, {4, "0"}}},
		{7, []Tick{{0, "256Mi"}, {3, "128Mi"}, {6, "0"}}},
		{8, []Tick{{0, "256Mi"}, {3, "128Mi"}, {7, "0"}}},
	} {
		for _, o := range []Options{{}, {Style: Dots}} {
			require.Equal(t, tc.want, usage.TicksFor(tc.height, o), "height %d style %d", tc.height, o.Style)
		}
	}
	// A level on the edge between two rows is drawn in the row nearer the
	// middle, so that round levels keep off the rows that carry the scale:
	// with four rows a limit at three quarters is on the second row and a
	// request at one quarter on the third, each labelled with its value.
	levels := Panel{Unit: Cores, Ceiling: 2, Series: []Series{
		{Points: qualityFlat(0.4)},
		{Kind: Reference, Points: qualityFlat(1.5), Glyph: '─', ASCII: '='},
		{Kind: Reference, Points: qualityFlat(0.5), Glyph: '┄', ASCII: '.'},
	}}
	require.Equal(t, []Tick{{0, "2"}, {1, "1.5"}, {2, "500m"}, {3, "0"}}, levels.Ticks(4))
	rows := levels.Render(qualityFrame(12), 4, 4, Options{Cursor: -1})
	require.Equal(t, []string{
		"   2 ┤",
		" 1.5 ┤────────────",
		"500m ┤┄┄┄┄┄┄┄┄┄┄┄┄",
		"   0 ┤▆▆▆▆▆▆▆▆▆▆▆▆",
	}, rows)
}

func TestAreaKeepsLevelLinesVisible(t *testing.T) {
	nan := math.NaN()
	level := func(v float64, n int) []Point {
		values := make([]float64, n)
		for i := range values {
			values[i] = v
		}
		return middles(values...)
	}
	tests := []struct {
		name   string
		values []float64
		refs   []Series
		want   []string
	}{
		{"the level is drawn above the area where they do not meet",
			[]float64{4, 8, 4, 8},
			[]Series{{Kind: Reference, Points: level(16, 4), Glyph: '─'}},
			[]string{
				"────",
				"▄█▄█",
			}},
		{"where the area ends on its limit the top of the area is drawn, and the line goes on either side",
			[]float64{4, 16, 15, nan, 8},
			[]Series{{Kind: Reference, Points: level(16, 5), Glyph: '─'}},
			[]string{
				"─█▇──",
				"▄██ █",
			}},
		{"a level under the top of the area is drawn over its body",
			[]float64{16, 16, 2, 4, nan, 12},
			[]Series{{Kind: Reference, Points: level(4, 6), Glyph: '┄'}},
			[]string{
				"██   ▄",
				"┄┄▂▄┄┄",
			}},
		{"two levels: each in the row of its value",
			[]float64{2, 2, 2},
			[]Series{{Kind: Reference, Points: level(16, 3), Glyph: '─'}, {Kind: Reference, Points: level(7, 3), Glyph: '┄'}},
			[]string{
				"───",
				"▂▂▂",
			}},
		{"a level with no sample near a column is not drawn there",
			[]float64{2, 2, 2, 2, 2, 2},
			[]Series{{Kind: Reference, Points: middles(nan, nan, nan, nan, nan, 16), Glyph: '─'}},
			[]string{
				"    ──",
				"▂▂▂▂▂▂",
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := Panel{Unit: Count, Ceiling: 16, Series: append(append([]Series(nil), tt.refs...), Series{Points: middles(tt.values...)})}
			require.Equal(t, len(tt.refs), panel.Filled(Options{}), "levels do not count as lines: the one line is the area")
			assert.Equal(t, tt.want, body(t, &panel, perMinute(len(tt.values)), 2, Options{Cursor: -1}))
		})
	}
}

// Where an area comes near a limit it takes the warning colour. Colour is
// never the only signal: Near is what a caller says in words.
func TestAreaWarnsNearALimit(t *testing.T) {
	for _, tc := range []struct {
		v, limit float64
		want     bool
	}{
		{254, 256, true}, {244, 256, true}, {243, 256, false}, {128, 256, false}, {300, 256, true},
		{0, 0, false}, {5, 0, false}, {0.95, 1, true}, {0.949, 1, false},
	} {
		assert.Equal(t, tc.want, Near(tc.v, tc.limit), "%v of %v", tc.v, tc.limit)
	}

	limit := Series{Kind: Reference, Limit: true, Glyph: '─', Color: "red", Points: middles(100, 100, 100, 100, 100)}
	usage := Series{Color: "blue", Points: middles(50, 94, 95, 100, 50)}
	panel := Panel{Unit: Count, Ceiling: 100, Series: []Series{limit, usage}}
	o := Options{Cursor: -1, WarnColor: "amber"}
	lines := panel.Render(perMinute(5), 1, 3, o)
	// The whole column changes colour, from 95 of 100 on.
	assert.Equal(t, []string{"100 ┤[blue::]▄█[-::][amber::]██[-::][blue::]▄[-::]"}, lines)
	// Without a warning colour the area keeps its own.
	o.WarnColor = ""
	assert.Equal(t, []string{"100 ┤[blue::]▄███▄[-::]"}, panel.Render(perMinute(5), 1, 3, o))
	// A level that is not a limit (a request) never warns.
	panel.Series[0].Limit = false
	o.WarnColor = "amber"
	assert.Equal(t, []string{"100 ┤[blue::]▄███▄[-::]"}, panel.Render(perMinute(5), 1, 3, o))
	// The limit of a column is the one read for it: a limit that was
	// raised stops the warning from there on.
	panel.Series[0].Limit = true
	panel.Series[0].Points = middles(100, 100, 100, 200, 200)
	panel.Ceiling = 200
	assert.Equal(t, []string{"200 ┤[blue::]▂▄[-::][amber::]▄[-::][blue::]▄▂[-::]"}, panel.Render(perMinute(5), 1, 3, o))
}

func TestAreaCursorIsDrawnInItsColumn(t *testing.T) {
	panel := Panel{Unit: Count, Ceiling: 4, Series: []Series{{Color: "blue", Points: middles(2, 2, 2)}}}
	f := perMinute(3)
	lines := panel.Render(f, 2, 1, Options{Cursor: 1, CursorColor: "white"})
	col, ok := f.Col(f.TimeAt(1))
	require.True(t, ok)
	require.Equal(t, 1, col)
	assert.Equal(t, []string{
		"4 ┤ [white::]┆[-::]",
		"0 ┤[blue::]█[-::][white::]█[-::][blue::]█[-::]",
	}, lines, "above the area the cursor is its own glyph; inside, the column takes the cursor's colour")
	// Without colours the cursor is still marked above the area and on
	// the axis, in the same cell.
	panel.Series[0].Color = ""
	plain := panel.Render(f, 2, 1, Options{Cursor: 1})
	assert.Equal(t, []string{"4 ┤ ┆", "0 ┤███"}, plain)
	cellOf := func(line string, r rune) int {
		for i, c := range []rune(line) {
			if c == r {
				return i
			}
		}
		return -1
	}
	axis := Axis(f, 1, nil, Options{Cursor: 1})[0]
	require.Equal(t, 4, cellOf(plain[0], '┆'))
	assert.Equal(t, cellOf(plain[0], '┆'), cellOf(axis, '^'), "the axis marks the same column: %q", axis)
	// An area that reaches the top row leaves no room for the glyph: the
	// column is still the cursor's by colour and by the mark on the axis.
	full := Panel{Unit: Count, Ceiling: 4, Series: []Series{{Color: "blue", Points: middles(4, 4, 4)}}}
	assert.Equal(t, []string{"4 ┤[blue::]█[-::][white::]█[-::][blue::]█[-::]"}, full.Render(f, 1, 1, Options{Cursor: 1, CursorColor: "white"}))
}

// steps renders one stepped line. A second line without samples shares the
// panel, which is what makes a panel draw lines instead of an area.
func steps(t *testing.T, points []Point, f Frame) []string {
	t.Helper()
	panel := Panel{Unit: Count, Ceiling: 4, Series: []Series{{Points: points}, {}}}
	require.Equal(t, -1, panel.Filled(Options{}), "two lines share the panel: neither is filled")
	return body(t, &panel, f, 4, Options{Cursor: -1})
}

func TestStepsDrawOneContinuousLine(t *testing.T) {
	nan := math.NaN()
	// Four rows over a ceiling of four: the rows hold 3-4, 2-3, 1-2 and 0-1.
	tests := []struct {
		name   string
		points []Point
		frame  Frame
		want   []string
	}{
		{"a flat series is a level line in the row of its value",
			middles(1.5, 1.5, 1.5, 1.5), perMinute(4),
			[]string{"", "", "────", ""}},
		{"zero is a line on the bottom row, never nothing",
			middles(0, 0, 0), perMinute(3),
			[]string{"", "", "", "───"}},
		{"the top of the axis is a line on the top row",
			middles(4, 4), perMinute(2),
			[]string{"──", "", "", ""}},
		{"a steep rise and fall connect through every row between",
			middles(0.5, 0.5, 3.5, 3.5, 0.5), perMinute(5),
			[]string{
				"  ╭─╮",
				"  │ │",
				"  │ │",
				"──╯ ╰",
			}},
		{"a sawtooth stays one stroke",
			middles(0.5, 3.5, 0.5, 3.5, 0.5), perMinute(5),
			[]string{
				" ╭╮╭╮",
				" ││││",
				" ││││",
				"─╯╰╯╰",
			}},
		{"a step of one row",
			middles(1.5, 1.5, 2.5, 2.5, 1.5), perMinute(5),
			[]string{
				"",
				"  ╭─╮",
				"──╯ ╰",
				"",
			}},
		{"a hole breaks the line: no stroke crosses it",
			middles(0.5, 0.5, nan, nan, nan, 3.5), perMinute(6),
			[]string{
				"     ─",
				"",
				"",
				"──",
			}},
		{"a single sample is one dash",
			middles(nan, 1.5, nan), perMinute(3),
			[]string{"", "", " ─", ""}},
		{"columns between two joined samples lie on the line between them",
			[]Point{{T: pat(0.5), V: 0.5}, {T: pat(4.5), V: 3.5}}, Frame{Start: p0, End: pat(8), Step: 4 * time.Minute, Width: 8},
			[]string{
				"    ╭",
				"  ╭─╯",
				" ╭╯",
				"─╯",
			}},
		{"a peak inside one column is drawn over the rows it reaches",
			middles(0.5, 0.5, 0.5, 0.5, 3.5, 0.5, 0.5, 0.5, 0.5), Frame{Start: p0, End: pat(9), Step: time.Minute, Width: 3},
			[]string{
				" │",
				" │",
				" │",
				"─┴─",
			}},
		{"a dip inside one column",
			middles(3.5, 3.5, 3.5, 3.5, 0.5, 3.5, 3.5, 3.5, 3.5), Frame{Start: p0, End: pat(9), Step: time.Minute, Width: 3},
			[]string{
				"─┬─",
				" │",
				" │",
				" │",
			}},
		{"one column wide: everything the line did is in it",
			middles(0.5, 3.5, 1.5), Frame{Start: p0, End: pat(3), Step: time.Minute, Width: 1},
			[]string{
				"│",
				"│",
				"├",
				"╯",
			}},
		{"two columns wide",
			middles(0.5, 3.5), perMinute(2),
			[]string{
				" ╭",
				" │",
				" │",
				"─╯",
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, steps(t, tt.points, tt.frame))
		})
	}
}

// Every column a stepped line crosses is connected to the next: the row it
// leaves a column in is the row it enters the next one in.
func TestStepsNeverLeaveAGapBetweenColumns(t *testing.T) {
	right := "─╭╰┴┬├┼"
	left := "─╮╯┴┬┤┼"
	values := make([]float64, 40)
	for i := range values {
		values[i] = float64((i*7)%5) * 0.9 // an irregular zigzag over every row
	}
	for _, width := range []int{40, 23, 11, 4} {
		f := Frame{Start: p0, End: pat(40), Step: time.Minute, Width: width}
		rows := steps(t, middles(values...), f)
		grid := make([][]rune, len(rows))
		for i, row := range rows {
			grid[i] = []rune(row + strings.Repeat(" ", width))
		}
		for col := 0; col+1 < width; col++ {
			exits, enters := -1, -1
			for row := range grid {
				if strings.ContainsRune(right, grid[row][col]) {
					require.Equal(t, -1, exits, "width %d column %d leaves in one row", width, col)
					exits = row
				}
				if strings.ContainsRune(left, grid[row][col+1]) {
					require.Equal(t, -1, enters, "width %d column %d is entered in one row", width, col+1)
					enters = row
				}
			}
			require.GreaterOrEqual(t, exits, 0, "width %d column %d is drawn", width, col)
			require.Equal(t, exits, enters, "width %d: columns %d and %d meet", width, col, col+1)
		}
	}
}

func TestStepsTellLinesApartWithoutColour(t *testing.T) {
	f := perMinute(5)
	a := Series{Color: "blue", Points: middles(1.5, 1.5, 1.5, 1.5, 1.5)}
	b := Series{Color: "red", Points: middles(1.5, 1.5, 3.5, 1.5, 1.5)}
	panel := Panel{Unit: Count, Ceiling: 4, Series: []Series{a, b}}
	// Where two lines run level in the same cells the first keeps them; a
	// turn of the second is drawn, so the place where it leaves is seen.
	assert.Equal(t, []string{
		"4 ┤  [red::]╭╮[-::]",
		"2 ┤  [red::]││[-::]",
		"  │[blue::]──[-::][red::]╯╰[-::][blue::]─[-::]",
		"0 ┤",
	}, panel.Render(f, 4, 1, Options{Cursor: -1}))

	// Each line ends in its letter, and both letters are there although
	// the lines end in the same cell.
	panel.Series[0].Key, panel.Series[1].Key = 'a', 'b'
	plain := panel.Render(f, 4, 1, Options{Cursor: -1})
	assert.Equal(t, "  │[blue::]──[-::][red::]╯b[-::][blue::]a[-::]", plain[2])

	// A marker repeats along the level stretches and never replaces a
	// turn, which would break the line.
	long := make([]float64, 60)
	for i := range long {
		long[i] = 0.5
		if i%12 >= 6 {
			long[i] = 3.5
		}
	}
	marked := Panel{Unit: Count, Ceiling: 4, Series: []Series{{Points: middles(long...), Marker: 'a', MarkerEvery: 6}, {}}}
	bare := Panel{Unit: Count, Ceiling: 4, Series: []Series{{Points: middles(long...)}, {}}}
	with, without := body(t, &marked, perMinute(60), 4, Options{Cursor: -1}), body(t, &bare, perMinute(60), 4, Options{Cursor: -1})
	letters := 0
	for row := range with {
		w, wo := []rune(with[row]), []rune(without[row])
		require.Len(t, w, len(wo))
		for col := range w {
			if w[col] == wo[col] {
				continue
			}
			require.Equal(t, 'a', w[col])
			require.Equal(t, '─', wo[col], "row %d column %d: a marker stands on a level stretch", row, col)
			letters++
		}
	}
	assert.GreaterOrEqual(t, letters, 8)
}

func TestStepsKeepDataOverLevelsAndTheCursorUnderEverything(t *testing.T) {
	f := perMinute(4)
	panel := Panel{Unit: Count, Ceiling: 4, Series: []Series{
		{Kind: Reference, Glyph: '┄', Points: middles(1.5, 1.5, 1.5, 1.5)},
		{Points: middles(0.5, 0.5, 1.5, 1.5)},
		{},
	}}
	assert.Equal(t, []string{
		"   ┆",
		"   ┆",
		"┄┄╭─",
		"──╯┆",
	}, body(t, &panel, f, 4, Options{Cursor: 3}), "the line is drawn over the level, the cursor only where nothing else is")
}

func TestFilledAndStyleChoices(t *testing.T) {
	line := func() Series { return Series{Points: middles(1, 1)} }
	ref := Series{Kind: Reference, Points: middles(2, 2)}
	off := Series{Kind: OffAxis, Points: middles(2, 2)}
	fill := line()
	fill.Fill = true
	tests := []struct {
		name   string
		series []Series
		want   int
	}{
		{"one line", []Series{line()}, 0},
		{"one line among levels and values off the axis", []Series{ref, off, line(), ref}, 2},
		{"a line without samples is still the only line", []Series{{}}, 0},
		{"two lines", []Series{line(), line()}, -1},
		{"a line that asks to be filled among others", []Series{line(), line(), fill}, 2},
		{"no line", []Series{ref}, -1},
		{"nothing", nil, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := Panel{Unit: Count, Series: tt.series}
			assert.Equal(t, tt.want, panel.Filled(Options{}))
			assert.Equal(t, -1, panel.Filled(Options{Style: Dots}), "braille dots fill nothing")
			assert.Equal(t, -1, panel.Filled(Options{ASCII: true}), "plain characters fill nothing")
		})
	}
	assert.Equal(t, Dots, Auto.Next())
	assert.Equal(t, Auto, Dots.Next(), "the style key cycles auto, dots, auto")
	assert.Equal(t, 1, runewidth.RuneWidth(FillGlyph))
}

// A panel with a filled median and its minimum and maximum as lines: the
// area ends where the median is, the maximum runs above it and the minimum
// is drawn over its body.
func TestAFilledLineAmongOthers(t *testing.T) {
	panel := Panel{Unit: Count, Ceiling: 4, Series: []Series{
		{Points: middles(3.5, 3.5, 3.5, 3.5)},
		{Points: middles(0.5, 0.5, 0.5, 0.5)},
		{Points: middles(2, 2, 2, 2), Fill: true},
	}}
	assert.Equal(t, []string{
		"────",
		"",
		"████",
		"────",
	}, body(t, &panel, perMinute(4), 4, Options{Cursor: -1}))

	// Above the area a line is a stepped line. Through the body of the
	// area it keeps only its level, one dash per column: the runs that
	// join the levels would cut the area open.
	stepped := Panel{Unit: Count, Ceiling: 4, Series: []Series{
		{Points: middles(3.5, 3.5, 3.5, 3.5, 3.5)},
		{Points: middles(0.5, 1.5, 0.5, 0.5, 2.5)},
		{Points: middles(3, 3, 3, 3, 1.5), Fill: true},
	}}
	assert.Equal(t, []string{
		"─────",
		"████╭",
		"█─██▄",
		"─█──█",
	}, body(t, &stepped, perMinute(5), 4, Options{Cursor: -1}))
	// In braille the same three series are three lines and nothing is
	// filled.
	for _, row := range body(t, &stepped, perMinute(5), 4, Options{Cursor: -1, Style: Dots}) {
		for _, r := range row {
			require.True(t, r == ' ' || (r >= 0x2800 && r <= 0x28ff), "%q", r)
		}
	}
}

// noIcons: the plain character form is the same whatever the style, and
// stays plain.
func TestPlainCharactersDoNotChangeWithTheStyle(t *testing.T) {
	f := frame(time.Hour, time.Minute, 30)
	one := Panel{Unit: Bytes, Series: []Series{
		{Key: 'a', Points: points(time.Minute, 0, 60, func(i int) float64 { return float64(i) * 4 * mi })},
		{Kind: Reference, Limit: true, Glyph: '─', ASCII: '=', Points: points(time.Minute, 0, 60, flat(256*mi))},
	}}
	several := Panel{Unit: Bytes, Series: []Series{
		{Key: 'a', Points: points(time.Minute, 0, 60, flat(200*mi))},
		{Key: 'b', Points: points(time.Minute, 0, 60, flat(90*mi))},
	}}
	for _, panel := range []*Panel{&one, &several} {
		auto := panel.Render(f, 5, 6, Options{ASCII: true, Cursor: 30, WarnColor: "amber"})
		assert.Equal(t, auto, panel.Render(f, 5, 6, Options{ASCII: true, Cursor: 30, Style: Dots}))
		for _, r := range strings.Join(auto, "\n") {
			require.Less(t, r, rune(128), "%q is not ASCII", r)
		}
		assert.Contains(t, strings.Join(auto, "\n"), "*")
		assert.NotContains(t, strings.Join(auto, "\n"), "amber", "the plain form has no area to colour")
	}
	g := gridOf(3)
	auto, _ := g.Render(p0, pat(10), time.Minute, 100, 8, Options{ASCII: true, Cursor: -1})
	dotted, _ := g.Render(p0, pat(10), time.Minute, 100, 8, Options{ASCII: true, Cursor: -1, Style: Dots})
	assert.Equal(t, auto, dotted)
}

// The three forms draw the same plot cells: the gutter, the axis and the
// width do not move when the style changes.
func TestEveryStyleKeepsTheSameCells(t *testing.T) {
	values := make([]float64, 61)
	for i := range values {
		values[i] = float64(i%20) * 12 * mi
	}
	one := Panel{Unit: Bytes, Series: []Series{
		{Kind: Reference, Limit: true, Glyph: '─', ASCII: '=', Points: points(time.Minute, 0, 60, flat(256*mi))},
		{Key: 'a', Points: points(time.Minute, 0, 60, func(i int) float64 { return values[i] })},
	}}
	several := Panel{Unit: Bytes, Series: []Series{
		{Key: 'a', Marker: 'a', Points: points(time.Minute, 0, 60, func(i int) float64 { return values[i] })},
		{Key: 'b', Marker: 'b', Points: points(time.Minute, 0, 60, func(i int) float64 { return values[60-i] })},
	}}
	for _, panel := range []*Panel{&one, &several} {
		for _, width := range []int{1, 2, 3, 7, 30, 61, 120} {
			f := frame(time.Hour, time.Minute, width)
			for _, height := range []int{1, 2, 3, 9} {
				for _, o := range []Options{{Cursor: 60}, {Cursor: 60, Style: Dots}, {Cursor: 60, ASCII: true}} {
					lines := panel.Render(f, height, 6, o)
					require.Len(t, lines, height)
					for _, line := range lines {
						require.LessOrEqual(t, runewidth.StringWidth(line), 6+2+width, "width %d height %d: %q", width, height, line)
						axis := []rune(line)[7]
						require.Contains(t, "┤│+|", string(axis), "the axis is in the same cell in every style")
					}
				}
			}
		}
	}
}
