// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var p0 = time.Date(2026, 11, 17, 13, 30, 0, 0, time.UTC)

func pat(minutes float64) time.Time { return p0.Add(time.Duration(minutes * float64(time.Minute))) }

func samples(step time.Duration, values ...float64) []Point {
	out := make([]Point, 0, len(values))
	for i, v := range values {
		if !math.IsNaN(v) {
			out = append(out, Point{T: p0.Add(time.Duration(i) * step), V: v})
		}
	}
	return out
}

// dotColumns returns, per braille dot column, the lowest and highest dot
// level set in the plot (levels count from the bottom; -1 when empty).
func dotColumns(t *testing.T, lines []string, gutter, width int) (lo, hi []int) {
	t.Helper()
	lo, hi = make([]int, width*2), make([]int, width*2)
	for i := range lo {
		lo[i], hi[i] = -1, -1
	}
	height := len(lines)
	for row, line := range lines {
		cells := []rune(line)[gutter+2:]
		for col, r := range cells {
			if r < 0x2800 || r > 0x28ff {
				continue
			}
			for y := range 4 {
				for x := range 2 {
					if (r-0x2800)&brailleBits[3-y][x] == 0 {
						continue
					}
					level := (height-1-row)*4 + y
					dot := col*2 + x
					if lo[dot] < 0 || level < lo[dot] {
						lo[dot] = level
					}
					hi[dot] = max(hi[dot], level)
				}
			}
		}
	}
	return lo, hi
}

func TestSolidStrokeKeepsASteepSawtoothConnected(t *testing.T) {
	// One sample every four dot columns, jumping between the bottom and the
	// top: the worst case for a line drawn from sparse dots.
	f := Frame{Start: p0, End: pat(20), Step: time.Minute, Width: 40}
	values := make([]float64, 21)
	for i := range values {
		values[i] = float64((i % 2) * 100)
	}
	for _, tt := range []struct {
		name   string
		stroke Stroke
		dash   int
	}{
		{"solid", Solid, 0},
		{"solid ignores dash", Solid, 3},
		{"bold", Bold, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			panel := Panel{Unit: Count, Series: []Series{{Points: samples(time.Minute, values...), Stroke: tt.stroke, Dash: tt.dash}}}
			lines := panel.Render(f, 5, 3, dots(-1))
			lo, hi := dotColumns(t, lines, 3, f.Width)
			for x := range lo {
				if x >= len(lo)-2 {
					continue // the last sample sits on the final dot column
				}
				require.GreaterOrEqualf(t, lo[x], 0, "dot column %d is empty: the line is broken", x)
				if x > 0 && lo[x-1] >= 0 {
					assert.Truef(t, lo[x] <= hi[x-1]+1 && hi[x] >= lo[x-1]-1, "dot columns %d and %d do not meet (%d-%d, %d-%d)",
						x-1, x, lo[x-1], hi[x-1], lo[x], hi[x])
				}
			}
			if tt.stroke == Bold {
				for x := range lo[:len(lo)-2] {
					assert.GreaterOrEqual(t, hi[x]-lo[x], 1, "bold is at least two dots high")
				}
			}
		})
	}

	// A dashed thin line is what the solid stroke replaces: on a level
	// stretch it leaves dot columns empty, and a solid one does not.
	level := make([]float64, 21)
	for i := range level {
		level[i] = 50
	}
	count := func(s Series) (empty int) {
		panel := Panel{Unit: Count, Ceiling: 100, Series: []Series{s}}
		lo, _ := dotColumns(t, panel.Render(f, 5, 3, dots(-1)), 3, f.Width)
		for _, l := range lo[:len(lo)-2] {
			if l < 0 {
				empty++
			}
		}
		return empty
	}
	assert.Positive(t, count(Series{Points: samples(time.Minute, level...), Dash: 3}))
	assert.Zero(t, count(Series{Points: samples(time.Minute, level...), Dash: 3, Stroke: Solid}))
}

func TestSolidStrokeNeverJoinsAcrossAHole(t *testing.T) {
	f := Frame{Start: p0, End: pat(20), Step: time.Minute, Width: 20}
	nan := math.NaN()
	panel := Panel{Unit: Count, Series: []Series{{Stroke: Solid,
		Points: samples(time.Minute, 10, 10, 10, 10, nan, nan, nan, nan, nan, nan, 90, 90, 90, 90)}}}
	lo, _ := dotColumns(t, panel.Render(f, 4, 3, dots(-1)), 3, f.Width)
	for x := 8; x < 20; x++ {
		assert.Equalf(t, -1, lo[x], "dot column %d lies in the hole and must stay empty", x)
	}
	assert.GreaterOrEqual(t, lo[2], 0)
	assert.GreaterOrEqual(t, lo[22], 0)
}

func TestMarkersTellLinesApartWithoutColourOrDashes(t *testing.T) {
	f := Frame{Start: p0, End: pat(60), Step: time.Minute, Width: 60}
	flat := func(v float64) []Point {
		values := make([]float64, 61)
		for i := range values {
			values[i] = v
		}
		return samples(time.Minute, values...)
	}
	panel := Panel{Unit: Count, Series: []Series{
		{Points: flat(100), Stroke: Solid, Marker: MarkerFor(0)},
		{Points: flat(100), Stroke: Solid, Marker: MarkerFor(1)},
		{Points: flat(0), Stroke: Solid, Marker: MarkerFor(2), MarkerEvery: 20},
	}}
	lines := panel.Render(f, 4, 3, Options{Cursor: -1})
	top, bottom := lines[0], lines[len(lines)-1]
	assert.Equal(t, 5, strings.Count(top, "●"), "one marker every 12 cells")
	assert.Equal(t, 5, strings.Count(top, "■"), "the second line on the same level keeps its own markers")
	assert.Equal(t, 3, strings.Count(bottom, "▲"))
	assert.NotEqual(t, strings.IndexRune(top, '●'), strings.IndexRune(top, '■'), "markers are staggered per line")
	assert.Equal(t, 3+2+60, runewidth.StringWidth(top), "markers are one cell wide")
	assert.Equal(t, 3+2+60, runewidth.StringWidth(bottom), "markers are one cell wide")

	// The plain character form already gives each line its own character.
	ascii := panel.Render(f, 4, 3, Options{ASCII: true, Cursor: -1})
	assert.NotContains(t, strings.Join(ascii, ""), "●")
	assert.Contains(t, ascii[0], "*")
	assert.Contains(t, ascii[3], "o")
	panel.Series[0].Marker, panel.Series[1].Marker = 'a', 'b'
	ascii = panel.Render(f, 4, 3, Options{ASCII: true, Cursor: -1})
	assert.Equal(t, 5, strings.Count(ascii[0], "a"), "a letter marker is plain and is drawn")
	assert.Equal(t, 5, strings.Count(ascii[0], "b"))

	distinct := map[rune]bool{}
	for i := range Markers {
		distinct[MarkerFor(i)] = true
		assert.Equal(t, 1, runewidth.RuneWidth(MarkerFor(i)))
	}
	assert.Len(t, distinct, len(Markers))
	assert.Equal(t, MarkerFor(0), MarkerFor(len(Markers)))
}

func TestSpark(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name   string
		values []float64
		opts   SparkOptions
		want   string
	}{
		// A trend is eighth blocks, one cell high: the height is the value.
		{"a trend is one eighth block per value", []float64{0, 1, 2, 3, 4, 5, 6, 7}, SparkOptions{}, "▁▂▃▄▅▆▇█"},
		{"a jump is two heights side by side", []float64{0, 0, 3, 3, 0}, SparkOptions{}, "▁▁██▁"},
		{"a flat series is a baseline, not blank", []float64{0, 0, 0}, SparkOptions{}, "▁▁▁"},
		{"a missing sample is a gap, never a low value", []float64{1, nan, nan, 1}, SparkOptions{}, "█××█"},
		{"a gap between a low and a high value", []float64{0, nan, 3}, SparkOptions{}, "▁×█"},
		{"a fixed maximum keeps rows comparable", []float64{1, 1, 1}, SparkOptions{Max: 100}, "▁▁▁"},
		{"values above the maximum stay on the top level", []float64{50, 500}, SparkOptions{Max: 100}, "▅█"},
		{"a single value", []float64{2}, SparkOptions{Max: 4}, "▅"},
		// The braille line is the trend of the Dots style, as it was.
		{"dots: thin line joins a jump with a vertical run", []float64{0, 0, 3, 3, 0}, SparkOptions{Style: SparkDots}, "⣀⣸⠉⢹⣀"},
		{"dots: a flat series is a baseline, not blank", []float64{0, 0, 0}, SparkOptions{Style: SparkDots}, "⣀⣀⣀"},
		{"dots: a missing sample is a gap, never a low value", []float64{1, nan, nan, 1}, SparkOptions{Style: SparkDots}, "⠉××⠉"},
		{"dots: a jump is not joined across a gap", []float64{0, nan, 3}, SparkOptions{Style: SparkDots}, "⣀×⠉"},
		{"dots: a fixed maximum keeps rows comparable", []float64{1, 1, 1}, SparkOptions{Style: SparkDots, Max: 100}, "⣀⣀⣀"},
		{"dots: values above the maximum stay on the top level", []float64{50, 500}, SparkOptions{Style: SparkDots, Max: 100}, "⠚⠉"},
		{"dots: plain characters do not change with the style", []float64{0, 1, 2, 3, 4, nan}, SparkOptions{ASCII: true, Style: SparkDots}, "_.-~^x"},
		{"bars for separate measurements", []float64{0, 1, 2, 3, 4, 5, 6, 7}, SparkOptions{Style: SparkBars}, "▁▂▃▄▅▆▇█"},
		{"bars with a gap", []float64{7, nan, 0}, SparkOptions{Style: SparkBars}, "█×▁"},
		{"plain line", []float64{0, 1, 2, 3, 4, nan}, SparkOptions{ASCII: true}, "_.-~^x"},
		{"plain bars", []float64{0, 7, nan}, SparkOptions{ASCII: true, Style: SparkBars}, "_#x"},
		{"colour runs share one tag and gaps keep their own", []float64{1, 1, nan, 1}, SparkOptions{Color: "#79c7d4", GapColor: "#5c6370"},
			"[#79c7d4::]██[-::][#5c6370::]×[-::][#79c7d4::]█[-::]"},
		{"dots: colour runs share one tag and gaps keep their own", []float64{1, 1, nan, 1}, SparkOptions{Style: SparkDots, Color: "#79c7d4", GapColor: "#5c6370"},
			"[#79c7d4::]⠉⠉[-::][#5c6370::]×[-::][#79c7d4::]⠉[-::]"},
		{"nothing to draw", nil, SparkOptions{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Spark(tt.values, tt.opts)
			assert.Equal(t, tt.want, got)
			if tt.opts.Color == "" {
				assert.Equal(t, len(tt.values), runewidth.StringWidth(got), "one cell per value")
			}
		})
	}

	slow := Spark([]float64{0, 9, 0}, SparkOptions{Style: SparkBars, Color: "blue", ColorAt: func(_ int, v float64) string {
		if v > 5 {
			return "red"
		}
		return "blue"
	}})
	assert.Equal(t, "[blue::]▁[-::][red::]█[-::][blue::]▁[-::]", slow)
	assert.Equal(t, SparkLine, SparkStyleOf(Auto))
	assert.Equal(t, SparkDots, SparkStyleOf(Dots))
}

func TestSparkValuesKeepPeaksAndHoles(t *testing.T) {
	// Four samples per cell: the cell shows the largest, so a short peak
	// survives.
	f := Frame{Start: p0, End: pat(8), Step: 30 * time.Second, Width: 4}
	values := SparkValues(samples(30*time.Second, 1, 1, 9, 1, 1, 1, 1, 1 /* cell 2 has no sample */, math.NaN(), math.NaN(), math.NaN(), math.NaN(), 2, 2, 2, 2), f)
	require.Len(t, values, 4)
	assert.InDelta(t, 9.0, values[0], 1e-9)
	assert.InDelta(t, 1.0, values[1], 1e-9)
	assert.True(t, math.IsNaN(values[2]), "a cell without a sample is missing, not zero")
	assert.InDelta(t, 2.0, values[3], 1e-9)

	// Fewer samples than cells: cells between two joined samples repeat
	// the nearer one, and a real hole stays a hole.
	wide := Frame{Start: p0, End: pat(10), Step: 2 * time.Minute, Width: 20}
	values = SparkValues([]Point{{T: pat(0), V: 1}, {T: pat(2), V: 1}, {T: pat(8), V: 5}}, wide)
	assert.InDelta(t, 1.0, values[1], 1e-9)
	assert.InDelta(t, 1.0, values[3], 1e-9)
	assert.True(t, math.IsNaN(values[10]), "the four-minute hole is not filled")
	assert.InDelta(t, 5.0, values[16], 1e-9)
	assert.Nil(t, SparkValues(nil, Frame{}))
}

func TestLanes(t *testing.T) {
	f := Frame{Start: p0, End: pat(20), Step: time.Minute, Width: 20}
	lanes := []Lane{
		{Label: "slow requests", Marks: []Interval{{pat(3), pat(6)}, {pat(15), pat(15)}}, Note: "4 of 20 min"},
		{Label: "restarts, any pod", Note: "none in window"},
		{Label: "node, a long label that is cut", Unknown: []Interval{{pat(0), pat(10)}}, Marks: []Interval{{pat(8), pat(12)}}, Glyph: '▇', ASCII: '=', Note: "10 min not read"},
	}
	tests := []struct {
		name string
		opts LanesOptions
		want []string
	}{
		{"unicode with a cursor", LanesOptions{Options: Options{Cursor: 9}, LabelWidth: 18, NoteWidth: 14}, []string{
			"slow requests      │···███···┆·····█····  4 of 20 min",
			"restarts, any pod  │·········┆··········  none in window",
			"node, a long labe… │××××××××××▇▇········  10 min not re…",
			"                   └┬────────^┬────────┬",
			"                            13:40",
		}},
		{"plain characters, no notes, no cursor", LanesOptions{Options: Options{ASCII: true, Cursor: -1}, LabelWidth: 18}, []string{
			"slow requests      |...###.........#....",
			"restarts, any pod  |....................",
			"node, a long labe… |xxxxxxxxxx==........",
			"                   ++---------+--------+",
			"                            13:40",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lanes(lanes, f, tt.opts)
			assert.Equal(t, tt.want, got)
			for _, line := range got[:len(lanes)] {
				assert.Equal(t, '│', []rune(strings.ReplaceAll(line, "|", "│"))[tt.opts.LabelWidth+1], "the plot starts in the same cell on every row")
			}
		})
	}

	lanes[0].Color = "red"
	colored := Lanes(lanes[:1], f, LanesOptions{Options: Options{Cursor: -1, AxisColor: "gray"}, LabelWidth: 13, QuietColor: "gray"})
	assert.Equal(t, "slow requests[gray::] │[-::][gray::]···[-::][red::]███[-::][gray::]·········[-::][red::]█[-::][gray::]····[-::]", colored[0])
	assert.Nil(t, Lanes(lanes, Frame{}, LanesOptions{}))
}

func TestMarkedClipsIntervalsToColumns(t *testing.T) {
	f := Frame{Start: p0, End: pat(10), Step: time.Minute, Width: 10}
	cells := func(marked []bool) string {
		var b strings.Builder
		for _, hit := range marked {
			if hit {
				b.WriteByte('#')
			} else {
				b.WriteByte('.')
			}
		}
		return b.String()
	}
	tests := []struct {
		name      string
		intervals []Interval
		want      string
	}{
		{"an interval ending on a column edge does not mark the next column", []Interval{{pat(2), pat(4)}}, "..##......"},
		{"a moment marks its column", []Interval{{pat(5.5), time.Time{}}}, ".....#...."},
		{"an interval shorter than a column still shows", []Interval{{pat(7.2), pat(7.4)}}, ".......#.."},
		{"clipped to the window on both sides", []Interval{{pat(-5), pat(1.5)}, {pat(9.5), pat(30)}}, "##.......#"},
		{"outside the window", []Interval{{pat(-5), pat(-1)}, {pat(10), pat(12)}, {pat(40), time.Time{}}}, ".........."},
		{"overlapping intervals count once", []Interval{{pat(1), pat(3)}, {pat(2), pat(4)}}, ".###......"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cells(f.Marked(tt.intervals)))
			assert.Equal(t, strings.Count(tt.want, "#"), f.MarkedCount(tt.intervals))
		})
	}
	start, end := f.ColumnSpan(3)
	assert.Equal(t, pat(3), start)
	assert.Equal(t, pat(4), end)
	start, _ = f.ColumnSpan(99)
	assert.Equal(t, pat(9), start, "a column outside the plot is clamped")
}

func gridOf(n int) *Grid {
	g := &Grid{Unit: Seconds, Empty: "no data"}
	for i := range n {
		v := 0.3
		if i == 2 {
			v = 40
		}
		g.Minis = append(g.Minis, Mini{Title: "pod-" + string(rune('a'+i)), Note: "p99 " + Seconds.Format(v),
			Series: []Series{{Points: samples(time.Minute, v, v, v, v, v, v, v, v, v, v, v), Stroke: Bold}}})
	}
	return g
}

func TestGridLayout(t *testing.T) {
	tests := []struct {
		name          string
		minis         int
		width, height int
		want          GridLayout
	}{
		{"three pods side by side on a wide screen", 3, 132, 12, GridLayout{Columns: 3, Rows: 1, PlotWidth: 40, PlotHeight: 9, Gutter: 3, Shown: 3}},
		{"four fit two by two at 80 columns", 4, 80, 14, GridLayout{Columns: 2, Rows: 2, PlotWidth: 36, PlotHeight: 5, Gutter: 3, Shown: 4}},
		{"a low screen takes narrower plots instead of dropping rows", 4, 80, 7, GridLayout{Columns: 4, Rows: 1, PlotWidth: 16, PlotHeight: 4, Gutter: 3, Shown: 4}},
		{"eight are evened out", 8, 132, 20, GridLayout{Columns: 4, Rows: 2, PlotWidth: 29, PlotHeight: 8, Gutter: 3, Shown: 8}},
		{"what does not fit is counted", 4, 60, 8, GridLayout{Columns: 3, Rows: 1, PlotWidth: 16, PlotHeight: 5, Gutter: 3, Shown: 3, NotShown: 1}},
		{"seven at 60 columns and 24 rows", 7, 60, 24, GridLayout{Columns: 2, Rows: 4, PlotWidth: 26, PlotHeight: 4, Gutter: 3, Shown: 7}},
		{"too narrow for one", 3, 18, 20, GridLayout{Gutter: 3, NotShown: 3}},
		{"too low for one", 3, 80, 5, GridLayout{Gutter: 3, NotShown: 3}},
		{"nothing to lay out", 0, 80, 20, GridLayout{Gutter: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gridOf(tt.minis)
			got := g.Layout(tt.width, tt.height)
			assert.Equal(t, tt.want, got)
			for _, o := range []Options{{Cursor: -1}, dots(-1), {Cursor: -1, ASCII: true}} {
				lines, layout := g.Render(p0, pat(10), time.Minute, tt.width, tt.height, o)
				assert.Equal(t, got, layout)
				assert.LessOrEqual(t, len(lines), tt.height)
				for _, line := range lines {
					assert.LessOrEqual(t, cellWidth(line), tt.width, "%q", line)
				}
				if got.Shown == 0 {
					assert.Empty(t, lines)
				}
			}
		})
	}
}

func TestGridSharesOneScaleAndOneAxis(t *testing.T) {
	g := gridOf(3)
	g.Minis = append(g.Minis, Mini{Title: "pod-d"})
	assert.Equal(t, 40.0, g.Top(), "the scale is the maximum over every mini chart")

	// Every mini chart is a filled area: a flat series near zero is a
	// baseline of one eighth, never blank, and the chart without samples
	// has no fill at all.
	lines, layout := g.Render(p0, pat(10), time.Minute, 100, 8, Options{Cursor: -1})
	require.Equal(t, GridLayout{Columns: 4, Rows: 1, PlotWidth: 21, PlotHeight: 5, Gutter: 3, Shown: 4}, layout)
	assert.Equal(t, []string{
		"     pod-a       p99 300ms   pod-b       p99 300ms   pod-c         p99 40s   pod-d",
		"40s ┤                       │                       │█████████████████████  │",
		"    │                       │                       │█████████████████████  │",
		"20s ┤                       │                       │█████████████████████  │ no data",
		"    │                       │                       │█████████████████████  │",
		"  0 ┤▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  │▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  │█████████████████████  │",
		"    └┬─────────┬─────────┬  └┬─────────┬─────────┬  └┬─────────┬─────────┬  └┬─────────┬─────────┬",
		"             13:35                   13:35                   13:35                   13:35",
	}, lines)

	// The braille style keeps the lines it always drew.
	lines, layout = g.Render(p0, pat(10), time.Minute, 100, 8, dots(-1))
	require.Equal(t, GridLayout{Columns: 4, Rows: 1, PlotWidth: 21, PlotHeight: 5, Gutter: 3, Shown: 4}, layout)
	want := []string{
		"     pod-a       p99 300ms   pod-b       p99 300ms   pod-c         p99 40s   pod-d",
		"40s ┤                       │                       │⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛  │",
		"    │                       │                       │                       │",
		"20s ┤                       │                       │                       │ no data",
		"    │                       │                       │                       │",
		"  0 ┤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤  │⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤  │                       │",
		"    └┬─────────┬─────────┬  └┬─────────┬─────────┬  └┬─────────┬─────────┬  └┬─────────┬─────────┬",
		"             13:35                   13:35                   13:35                   13:35",
	}
	assert.Equal(t, want, lines)
	assert.Equal(t, 1, strings.Count(strings.Join(lines, "\n"), "20s"), "the Y labels are written once")

	// The plain character form keeps the same cells.
	ascii, _ := g.Render(p0, pat(10), time.Minute, 100, 8, Options{ASCII: true, Cursor: -1})
	require.Len(t, ascii, len(lines))
	for i := range lines {
		assert.Equal(t, cellWidth(lines[i]), cellWidth(ascii[i]), "line %d", i)
	}
	assert.Contains(t, ascii[1], "|*********************  |")

	// Titles and notes go through the caller's escape, and a colour tag
	// does not shift the columns.
	g.Escape = func(s string) string { return strings.ReplaceAll(s, "pod-", "POD-") }
	g.Minis[2].TitleColor = "#e06c75"
	tagged, _ := g.Render(p0, pat(10), time.Minute, 100, 8, Options{Cursor: -1, AxisColor: "#5c6370", Style: Dots})
	assert.Contains(t, tagged[0], "[#e06c75::]POD-c         p99 40s[-::]")
	assert.Equal(t, cellWidth(lines[1]), cellWidth(tagged[1]))
}

func TestOutliers(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name    string
		values  []float64
		opts    PeerOptions
		enough  bool
		indexes []int
	}{
		{"one pod of three is slow", []float64{0.31, 0.29, 41}, PeerOptions{}, true, []int{2}},
		{"one pod of four is slow", []float64{0.31, 0.29, 41, 0.33}, PeerOptions{}, true, []int{2}},
		{"a tight group has no outlier although the score is large", []float64{0.300, 0.301, 0.310}, PeerOptions{}, true, nil},
		{"all equal", []float64{2, 2, 2, 2}, PeerOptions{}, true, nil},
		{"two peers cannot say which one is odd", []float64{0.3, 41}, PeerOptions{}, false, nil},
		{"a peer without a reading does not count", []float64{0.3, nan, 41}, PeerOptions{}, false, nil},
		{"more than half share one value (MAD is zero)", []float64{0, 0, 40}, PeerOptions{MinDelta: 1}, true, []int{2}},
		{"below the noise floor", []float64{0, 0, 0.002}, PeerOptions{MinDelta: 0.01}, true, nil},
		{"free threads over a window: low is the bad side", []float64{5.6, 5.4, 0, 5.8}, PeerOptions{Direction: Below, MinDelta: 1}, true, []int{2}},
		{"direction above ignores a low value", []float64{5.6, 5.4, 0, 5.8}, PeerOptions{Direction: Above, MinDelta: 1}, true, nil},
		{"a noisy group at one instant singles nobody out", []float64{7, 4, 0, 6}, PeerOptions{Direction: Below}, true, nil},
		{"throttled share", []float64{0.012, 0.011, 0.85}, PeerOptions{Direction: Above, MinDelta: 0.05}, true, []int{2}},
		{"two of five differ, largest first", []float64{1, 1.1, 30, 0.9, 12}, PeerOptions{}, true, []int{2, 4}},
		{"half of the group is not an outlier pair", []float64{1, 1, 30, 30}, PeerOptions{}, true, nil},
		{"a stricter ratio", []float64{1, 1.1, 2.6, 0.9}, PeerOptions{Ratio: 3}, true, nil},
		{"the default ratio", []float64{1, 1.1, 2.6, 0.9}, PeerOptions{}, true, []int{2}},
		{"a larger minimum group", []float64{0.3, 0.3, 41}, PeerOptions{MinPeers: 4}, false, nil},
		{"no values", nil, PeerOptions{}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Outliers(tt.values, tt.opts)
			assert.Equal(t, tt.enough, got.Enough)
			if !tt.enough {
				assert.Equal(t, "not enough peers", got.Reason)
				assert.Empty(t, got.Outliers)
				return
			}
			var indexes []int
			for _, d := range got.Outliers {
				indexes = append(indexes, d.Index)
				assert.Equal(t, tt.values[d.Index], d.Value)
				assert.Equal(t, d.Delta > 0, d.Above)
			}
			assert.Equal(t, tt.indexes, indexes)
		})
	}

	got := Outliers([]float64{0.31, 0.29, 41, nan}, PeerOptions{})
	require.Len(t, got.Outliers, 1)
	d := got.Outliers[0]
	assert.Equal(t, 3, got.Peers)
	assert.Equal(t, []int{3}, got.Skipped)
	assert.InDelta(t, 0.31, got.Median, 1e-9)
	assert.InDelta(t, 0.02, got.MAD, 1e-9)
	assert.InDelta(t, 40.69, d.Delta, 1e-9)
	assert.InDelta(t, 132.26, d.Ratio, 0.01)
	assert.InDelta(t, 1372.3, d.Score, 0.1)

	zero := Outliers([]float64{0, 0, 5}, PeerOptions{})
	require.Len(t, zero.Outliers, 1)
	assert.True(t, math.IsInf(zero.Outliers[0].Score, 1))
	assert.True(t, math.IsInf(zero.Outliers[0].Ratio, 1), "a ratio against a zero median says nothing")
	low := Outliers([]float64{5.6, 5.4, 0, 5.8}, PeerOptions{Direction: Below})
	require.Len(t, low.Outliers, 1)
	assert.Negative(t, low.Outliers[0].Score)
	assert.False(t, low.Outliers[0].Above)
}

func TestSecondsAndPerSecondUnits(t *testing.T) {
	tests := []struct {
		unit Unit
		v    float64
		want string
	}{
		{Seconds, 0, "0"}, {Seconds, 0.00025, "250µs"}, {Seconds, 0.0057, "5.7ms"}, {Seconds, 0.31, "310ms"},
		{Seconds, 0.9996, "1s"}, {Seconds, 2.5, "2.5s"}, {Seconds, 41, "41s"}, {Seconds, 99.9, "99.9s"},
		{Seconds, 150, "2.5m"}, {Seconds, 7200, "2h"}, {Seconds, math.NaN(), "?"},
		{PerSecond, 12, "12/s"}, {PerSecond, 0.004, "0/s"}, {PerSecond, 1.25, "1.25/s"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.unit.Format(tt.v), "%v", tt.v)
	}
	assert.Equal(t, 50.0, Seconds.Ceil(41))
	assert.Equal(t, 0.4, Seconds.Ceil(0.31))
	for name, want := range map[string]Unit{"seconds": Seconds, "ratio": Ratio, "perSecond": PerSecond, "count": Count, "bytes": Bytes, "cores": Cores} {
		got, ok := UnitNamed(name)
		assert.True(t, ok, name)
		assert.Equal(t, want, got, name)
	}
	_, ok := UnitNamed("furlongs")
	assert.False(t, ok)
}
