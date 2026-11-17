// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

const (
	mi = 1024 * 1024
	gi = 1024 * mi
)

var t0 = time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)

func frame(window, step time.Duration, width int) Frame {
	return Frame{Start: t0, End: t0.Add(window), Step: step, Width: width}
}

// points makes one sample per step from index first to last (inclusive).
func points(step time.Duration, first, last int, value func(int) float64) []Point {
	var out []Point
	for i := first; i <= last; i++ {
		out = append(out, Point{T: t0.Add(time.Duration(i) * step), V: value(i)})
	}
	return out
}

func flat(v float64) func(int) float64 { return func(int) float64 { return v } }

func plot(lines []string, gutter int) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		runes := []rune(line)
		if len(runes) > gutter+2 {
			out[i] = string(runes[gutter+2:])
		}
	}
	return out
}

func TestUnitFormat(t *testing.T) {
	for _, tc := range []struct {
		unit Unit
		v    float64
		want string
	}{
		{Bytes, 0, "0"}, {Bytes, 512, "512B"}, {Bytes, 254 * mi, "254Mi"}, {Bytes, 1.5 * gi, "1.5Gi"},
		{Bytes, 15.5 * gi, "15.5Gi"}, {Bytes, 1023.9 * mi, "1Gi"}, {Bytes, 96.4 * mi, "96.4Mi"},
		{Cores, 0.34, "340m"}, {Cores, 0.0005, "0.5m"}, {Cores, 1, "1"}, {Cores, 2.5, "2.5"}, {Cores, 0.9996, "1"},
		{BytesPerSecond, 2600, "2.6kB/s"}, {BytesPerSecond, 9, "9B/s"}, {BytesPerSecond, 3.4e6, "3.4MB/s"},
		{Count, 6, "6"}, {Count, 110, "110"}, {Count, 2.5, "2.5"},
		{Ratio, 0.41, "41%"}, {Ratio, 0.004, "0.4%"}, {Ratio, 1, "100%"},
	} {
		require.Equal(t, tc.want, tc.unit.Format(tc.v), "%v of unit %d", tc.v, tc.unit)
	}
}

func TestUnitCeilMakesRoundTicks(t *testing.T) {
	for _, tc := range []struct {
		unit Unit
		v    float64
		want float64
	}{
		{Bytes, 254 * mi, 256 * mi}, {Bytes, 256 * mi, 256 * mi}, {Bytes, 257 * mi, 320 * mi}, {Bytes, 512 * mi, 512 * mi},
		{Bytes, 100 * gi, 128 * gi}, {Bytes, 0, 1},
		{Cores, 0.34, 0.4}, {Cores, 4, 4}, {Cores, 0.11, 0.15}, {Count, 110, 150}, {Count, 6, 6}, {Count, 7, 8},
		{BytesPerSecond, 9000, 10000}, {Ratio, 0.41, 0.5}, {Ratio, 0.003, 0.01},
	} {
		require.InDelta(t, tc.want, tc.unit.Ceil(tc.v), tc.want*1e-6, "%v of unit %d", tc.v, tc.unit)
	}
}

func TestFrameMapsTimesToColumnsAndGridPoints(t *testing.T) {
	f := frame(time.Hour, 30*time.Second, 60)
	require.Equal(t, 120, f.Steps())
	require.Equal(t, t0.Add(time.Hour), f.TimeAt(120))
	require.Equal(t, t0.Add(time.Hour), f.TimeAt(500), "clamped to the last grid point")
	require.Equal(t, t0, f.TimeAt(-3))
	for _, tc := range []struct {
		offset time.Duration
		col    int
		ok     bool
	}{
		{0, 0, true}, {time.Minute, 1, true}, {30 * time.Minute, 30, true}, {time.Hour, 59, true},
		{-time.Second, 0, false}, {time.Hour + time.Second, 0, false},
	} {
		col, ok := f.Col(t0.Add(tc.offset))
		require.Equal(t, tc.ok, ok, tc.offset)
		require.Equal(t, tc.col, col, tc.offset)
	}
	// Narrower than the grid: neighbouring grid points share a column, and
	// every grid point still has one.
	narrow := frame(time.Hour, 30*time.Second, 40)
	a, _ := narrow.Col(narrow.TimeAt(60))
	b, _ := narrow.Col(narrow.TimeAt(61))
	require.Equal(t, a, b)
	_, ok := (Frame{}).Col(t0)
	require.False(t, ok)
}

func TestValueAtReadsASampleAndNeverInterpolates(t *testing.T) {
	f := frame(time.Hour, time.Minute, 60)
	s := Series{Points: append(points(time.Minute, 0, 10, func(i int) float64 { return float64(i) }), points(time.Minute, 30, 40, flat(7))...)}
	v, ok := f.ValueAt(&s, f.TimeAt(4))
	require.True(t, ok)
	require.InDelta(t, 4.0, v, 0)
	_, ok = f.ValueAt(&s, f.TimeAt(20))
	require.False(t, ok, "a hole has no value")
	_, ok = f.ValueAt(&s, f.TimeAt(50))
	require.False(t, ok, "after the series ended there is no value")

	holes := f.Holes(&s)
	require.Equal(t, 22, holes.Samples)
	require.Equal(t, 1, holes.Gaps)
	require.False(t, holes.LateStart)
	require.True(t, holes.EarlyEnd)
	require.Equal(t, t0.Add(40*time.Minute), holes.Last)
	require.Equal(t, Holes{}, f.Holes(&Series{}))
}

func TestTicksLabelTopMiddleAndZero(t *testing.T) {
	p := Panel{Unit: Bytes, Series: []Series{{Points: points(time.Minute, 0, 5, flat(254*mi))}}}
	for _, tc := range []struct {
		height int
		want   []Tick
	}{
		{1, []Tick{{0, "256Mi"}}},
		{3, []Tick{{0, "256Mi"}, {2, "0"}}},
		{4, []Tick{{0, "256Mi"}, {1, "128Mi"}, {3, "0"}}},
		{7, []Tick{{0, "256Mi"}, {3, "128Mi"}, {6, "0"}}},
	} {
		require.Equal(t, tc.want, p.Ticks(tc.height), "height %d", tc.height)
	}
	require.Equal(t, 5, p.GutterWidth())
	require.Nil(t, p.Ticks(0))
}

func TestRenderScalesLinesAndReferencesToTheAxis(t *testing.T) {
	f := frame(time.Hour, time.Minute, 30)
	p := Panel{Unit: Bytes, Series: []Series{
		{Name: "usage", Key: 'a', Points: points(time.Minute, 0, 60, flat(128*mi))},
		{Name: "limit", Kind: Reference, Glyph: '─', ASCII: '=', Points: points(time.Minute, 0, 60, flat(256*mi))},
	}}
	lines := p.Render(f, 5, 6, Options{Cursor: -1})
	require.Len(t, lines, 5)
	require.Equal(t, " 256Mi ┤"+strings.Repeat("─", 30), lines[0], "the limit is the top of the axis")
	require.True(t, strings.HasPrefix(lines[2], " 128Mi ┤"), lines[2])
	require.True(t, strings.HasPrefix(lines[4], "     0 ┤"))
	body := plot(lines, 6)
	require.Equal(t, 30, runewidth.StringWidth(body[2]), "the line is on the 128Mi row")
	require.True(t, strings.HasSuffix(body[2], "a"), "the line ends in its key")
	for _, row := range []int{1, 3, 4} {
		require.Empty(t, strings.TrimSpace(body[row]), "row %d", row)
	}
	for _, line := range lines {
		require.LessOrEqual(t, runewidth.StringWidth(line), 6+2+30)
	}
}

func TestRenderLeavesHolesEmptyAndNeverDrawsMissingDataAsZero(t *testing.T) {
	f := frame(time.Hour, time.Minute, 30)
	holed := Series{Key: 'a', Points: append(points(time.Minute, 0, 20, flat(100)), points(time.Minute, 40, 60, flat(100))...)}
	p := Panel{Unit: Count, Series: []Series{holed}}
	body := plot(p.Render(f, 4, 4, Options{Cursor: -1}), 4)
	top := []rune(body[0])
	require.Len(t, top, 30)
	for col := 11; col < 20; col++ {
		require.Equal(t, ' ', top[col], "column %d lies inside the hole", col)
	}
	require.NotEqual(t, ' ', top[5])
	require.NotEqual(t, ' ', top[25])
	require.Empty(t, strings.TrimSpace(body[3]), "nothing is drawn on the zero row")

	// A series that ends early stops where its last sample is.
	ended := Panel{Unit: Count, Series: []Series{{Key: 'b', Points: points(time.Minute, 0, 30, flat(100))}}}
	row := []rune(plot(ended.Render(f, 4, 4, Options{Cursor: -1}), 4)[0])
	require.Equal(t, 'b', row[len(row)-1])
	require.Len(t, row, 16)

	// No samples at all: the plot says so and has no scale and no line.
	empty := Panel{Unit: Bytes, Empty: "no samples in this window", Series: []Series{{Name: "usage", Key: 'a'}}}
	lines := empty.Render(f, 4, 6, Options{Cursor: -1})
	text := strings.Join(lines, "\n")
	require.Contains(t, text, "no samples in this window")
	require.NotContains(t, text, "┤", "no tick is labelled without a scale")
	for _, r := range text {
		require.False(t, r >= 0x2800 && r <= 0x28ff, "no braille without samples")
	}
}

func TestRenderStepsAreJoinedAndDashedLinesAreSparser(t *testing.T) {
	f := frame(time.Hour, time.Minute, 30)
	dots := func(dash int) int {
		p := Panel{Unit: Count, Series: []Series{{Dash: dash, Points: points(time.Minute, 0, 60, func(i int) float64 { return float64(i) })}}}
		count := 0
		for _, r := range strings.Join(p.Render(f, 6, 3, Options{Cursor: -1}), "") {
			if r >= 0x2800 && r <= 0x28ff {
				for bits := r - 0x2800; bits > 0; bits &= bits - 1 {
					count++
				}
			}
		}
		return count
	}
	solid, dashed := dots(1), dots(3)
	require.Greater(t, solid, 55, "a rising line touches every dot column")
	require.Less(t, dashed, solid/2)
	require.Positive(t, dashed)
}

func TestRenderASCIIUsesPlainCharactersOnly(t *testing.T) {
	f := frame(time.Hour, time.Minute, 30)
	p := Panel{Unit: Bytes, Series: []Series{
		{Key: 'a', Points: points(time.Minute, 0, 60, flat(200*mi))},
		{Key: 'b', Points: points(time.Minute, 0, 60, flat(90*mi))},
		{Kind: Reference, Glyph: '─', ASCII: '=', Points: points(time.Minute, 0, 60, flat(256*mi))},
		{Kind: Reference, Glyph: '┄', ASCII: '.', Points: points(time.Minute, 0, 60, flat(128*mi))},
	}}
	o := Options{ASCII: true, Cursor: 30}
	text := strings.Join(p.Render(f, 5, 6, o), "\n") + "\n" + strings.Join(Axis(f, 6, []Mark{{Key: 'R', T: t0.Add(20 * time.Minute)}}, o), "\n") +
		"\n" + Strip("restarts", []time.Time{t0.Add(25 * time.Minute)}, f, 8, "", o)
	for _, r := range text {
		require.Less(t, r, rune(128), "%q is not ASCII", r)
	}
	for _, want := range []string{"*", "+", "=", ".", "|", ":", "^", "R"} {
		require.Contains(t, text, want)
	}
}

func TestRenderCursorAndColoursAndNarrowWidths(t *testing.T) {
	series := []Series{
		{Key: 'a', Color: "#79c7d4", Points: points(time.Minute, 0, 60, flat(3))},
		{Kind: Reference, Glyph: '─', Color: "#ef8278", Points: points(time.Minute, 0, 60, flat(4))},
	}
	p := Panel{Unit: Count, Series: series}
	f := frame(time.Hour, time.Minute, 30)
	lines := p.Render(f, 4, 3, Options{Cursor: 30, AxisColor: "#a5b0aa", CursorColor: "#e1e7e3"})
	text := strings.Join(lines, "\n")
	require.Contains(t, text, "[#79c7d4::]")
	require.Contains(t, text, "[#ef8278::]")
	require.Contains(t, text, "[#e1e7e3::]┆[-::]", "the cursor is drawn where nothing else is")
	require.Equal(t, strings.Count(text, "["), strings.Count(text, "[-::]")*2, "every colour tag is closed")

	series[0].Color, series[1].Color = "", ""
	for _, width := range []int{1, 2, 8, 20} {
		narrow := frame(time.Hour, time.Minute, width)
		for _, line := range p.Render(narrow, 3, 3, Options{Cursor: 60}) {
			require.LessOrEqual(t, runewidth.StringWidth(line), 3+2+width, "width %d", width)
		}
		for _, line := range Axis(narrow, 3, nil, Options{Cursor: 60}) {
			require.LessOrEqual(t, runewidth.StringWidth(line), 3+2+width, "width %d", width)
		}
	}
	require.Nil(t, p.Render(Frame{}, 4, 3, Options{}))
	require.Nil(t, p.Render(f, 0, 3, Options{}))
}

func TestAxisHasRoundClockLabelsMarksAndCursor(t *testing.T) {
	for _, tc := range []struct {
		window   time.Duration
		width    int
		interval time.Duration
	}{
		{15 * time.Minute, 70, 2 * time.Minute}, {time.Hour, 70, 10 * time.Minute}, {time.Hour, 108, 5 * time.Minute},
		{6 * time.Hour, 70, time.Hour}, {24 * time.Hour, 70, 3 * time.Hour}, {24 * time.Hour, 48, 6 * time.Hour}, {time.Hour, 10, time.Hour},
	} {
		require.Equal(t, tc.interval, frame(tc.window, time.Minute, tc.width).TickInterval(), "%s in %d", tc.window, tc.width)
	}
	f := Frame{Start: t0.Add(7 * time.Minute), End: t0.Add(67 * time.Minute), Step: 30 * time.Second, Width: 60}
	lines := Axis(f, 5, []Mark{{Key: 'R', T: t0.Add(48 * time.Minute), Label: "rollout"}, {Key: 'Z', T: t0.Add(-time.Hour)}}, Options{Cursor: 120})
	require.Len(t, lines, 2)
	axis := []rune(lines[0])
	require.Equal(t, '└', axis[6])
	require.Equal(t, 'R', axis[7+41], "the mark sits in the column of its time")
	require.Equal(t, '^', axis[len(axis)-1], "the cursor is at the last grid point")
	require.NotContains(t, lines[0], "Z", "a mark outside the window is not placed")
	require.Equal(t, 6, strings.Count(lines[0], "┬"), "ticks at 04:10 … 05:00")
	labels := lines[1]
	for _, clock := range []string{"04:10", "04:20", "04:30", "04:40", "04:50", "05:00"} {
		at := strings.Index(labels, clock)
		require.GreaterOrEqual(t, at, 0, clock)
		tick, _ := f.Col(time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC).Add(time.Duration(10*(1+strings.Index("04:10 04:20 04:30 04:40 04:50 05:00", clock)/6)) * time.Minute))
		require.Equal(t, 7+tick-2, at, "%s is centred under its tick", clock)
	}
	require.Nil(t, Axis(Frame{}, 5, nil, Options{}))
}

func TestStripMarksEventsOnTheSharedAxis(t *testing.T) {
	f := frame(time.Hour, time.Minute, 30)
	row := Strip("restarts", []time.Time{t0.Add(20 * time.Minute), t0.Add(20*time.Minute + 10*time.Second), t0.Add(50 * time.Minute), t0.Add(2 * time.Hour)},
		f, 8, "", Options{Cursor: -1})
	require.True(t, strings.HasPrefix(row, "restarts │"))
	body := []rune(strings.TrimPrefix(row, "restarts │"))
	require.Equal(t, '▲', body[10])
	require.Equal(t, '▲', body[25])
	require.Equal(t, 2, strings.Count(row, "▲"), "two events in one column are one glyph; one is outside the window")
	require.Equal(t, "restarts │", Strip("restarts", nil, f, 8, "", Options{Cursor: -1}), "no event, no glyph")
}

func TestLegendWrapsAndCountsWhatDoesNotFit(t *testing.T) {
	entries := []Entry{
		{Glyph: "a", Name: "k4m2t", Value: "185Mi", Color: "#79c7d4"}, {Glyph: "b", Name: "x2x9q", Value: "254Mi"},
		{Glyph: "─", Name: "limit", Value: "256Mi"}, {Glyph: "┄", Name: "request", Value: "128Mi"},
	}
	one := Legend("MEMORY", entries, 80, 2, nil)
	require.Equal(t, []string{"MEMORY  [#79c7d4::]a[-::] k4m2t 185Mi  b x2x9q 254Mi  ─ limit 256Mi  ┄ request 128Mi"}, one)

	two := Legend("MEMORY", entries, 50, 2, nil)
	require.Len(t, two, 2)
	require.Contains(t, two[1], "request 128Mi")

	cut := Legend("MEMORY", entries, 50, 1, nil)
	require.Len(t, cut, 1)
	require.True(t, strings.HasSuffix(cut[0], "+2 more"), cut[0])

	for _, width := range []int{12, 20, 30, 44, 60} {
		for _, line := range Legend("MEMORY", entries, width, 2, nil) {
			plain := strings.NewReplacer("[#79c7d4::]", "", "[-::]", "").Replace(line)
			require.LessOrEqual(t, runewidth.StringWidth(plain), width, "width %d: %q", width, plain)
		}
	}
	escaped := Legend("T", []Entry{{Glyph: "a", Name: "[red]", Value: "1"}}, 40, 1, func(s string) string { return strings.ReplaceAll(s, "]", "[]") })
	require.Equal(t, []string{"T  a [red[] 1"}, escaped)
	require.Nil(t, Legend("T", entries, 0, 2, nil))
}

func TestAxisMarksInOneColumnKeepTheirOrderInTime(t *testing.T) {
	// At 24h one column is many minutes wide: a rollout and a later exit
	// share it, and the earlier one must stay to the left.
	f := frame(24*time.Hour, 15*time.Minute, 48)
	end := t0.Add(24 * time.Hour)
	rollout, exit := Mark{Key: 'R', T: end.Add(-12 * time.Minute)}, Mark{Key: 'X', T: end.Add(-2 * time.Minute)}
	for _, marks := range [][]Mark{{rollout, exit}, {exit, rollout}} {
		axis := Axis(f, 3, marks, Options{Cursor: -1})[0]
		require.True(t, strings.HasSuffix(axis, "RX"), axis)
	}
}
