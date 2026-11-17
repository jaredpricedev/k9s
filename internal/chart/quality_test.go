// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var qualityStart = time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)

func qualityFrame(width int) Frame {
	return Frame{Start: qualityStart, End: qualityStart.Add(time.Hour), Step: 30 * time.Second, Width: width}
}

func qualityFlat(v float64) []Point {
	points := make([]Point, 0, 121)
	for i := range 121 {
		points = append(points, Point{T: qualityStart.Add(time.Duration(i) * 30 * time.Second), V: v})
	}
	return points
}

func TestCountAndDurationAxesEndOnRoundNumbers(t *testing.T) {
	for _, tc := range []struct {
		unit      Unit
		v         float64
		top, half string
	}{
		// A count is whole at the top and at half of it.
		{Count, 1, "2", "1"}, {Count, 3, "4", "2"}, {Count, 5, "6", "3"}, {Count, 7, "8", "4"}, {Count, 9, "10", "5"},
		{Count, 12, "20", "10"}, {Count, 25, "30", "15"}, {Count, 45, "50", "25"}, {Count, 110, "150", "75"},
		// A fraction of a count (an average) keeps the general steps.
		{Count, 0.3, "0.3", "0.15"},
		// Up to a minute a duration rounds like any number; past it the
		// axis ends on whole minutes or hours, and so does its half.
		{Seconds, 0.35, "400ms", "200ms"}, {Seconds, 48, "50s", "25s"}, {Seconds, 61, "2m", "60s"}, {Seconds, 100, "2m", "60s"},
		{Seconds, 130, "4m", "2m"}, {Seconds, 400, "10m", "5m"}, {Seconds, 3000, "60m", "30m"}, {Seconds, 5000, "2h", "60m"},
	} {
		top := tc.unit.Ceil(tc.v)
		require.GreaterOrEqual(t, top, tc.v)
		require.Equal(t, tc.top, tc.unit.Format(top), "top for %v of unit %d", tc.v, tc.unit)
		require.Equal(t, tc.half, tc.unit.Format(top/2), "middle tick for %v of unit %d", tc.v, tc.unit)
	}
	restarts := Panel{Unit: Count, Series: []Series{{Points: qualityFlat(1)}}}
	require.Equal(t, []Tick{{Row: 0, Label: "2"}, {Row: 1, Label: "1"}, {Row: 3, Label: "0"}}, restarts.Ticks(4))
}

func TestARowWithALevelLineIsLabelledWithThatLevel(t *testing.T) {
	usage := qualityFlat(0.4)
	usage[60].V = 1.52 // one sample just above the limit
	panel := Panel{Unit: Cores, Series: []Series{
		{Points: usage, Stroke: Solid, Key: 'a'},
		{Kind: Reference, Points: qualityFlat(1.5), Glyph: '─', ASCII: '='},
		{Kind: Reference, Points: qualityFlat(1), Glyph: '┄', ASCII: '.'},
	}}
	// Four rows: the limit of 1.5 falls into the second row. That row is
	// labelled 1.5, not with the middle of the axis.
	require.Equal(t, []Tick{{Row: 0, Label: "2"}, {Row: 1, Label: "1.5"}, {Row: 3, Label: "0"}}, panel.Ticks(4))
	lines := panel.Render(qualityFrame(40), 4, panel.GutterWidth(), Options{Cursor: -1})
	require.True(t, strings.HasPrefix(lines[1], "1.5 ┤─"), lines[1])
	for _, line := range lines {
		require.NotContains(t, line, "  1 ┤─", "a line never sits on a row that names another number")
	}
	// With room the request has its own row and its own label; the top and
	// the bottom keep the scale.
	require.Equal(t, []Tick{{Row: 0, Label: "2"}, {Row: 2, Label: "1.5"}, {Row: 3, Label: "1"}, {Row: 7, Label: "0"}}, panel.Ticks(8))
	tall := panel.Render(qualityFrame(40), 8, panel.GutterWidth(), Options{Cursor: -1})
	require.True(t, strings.HasPrefix(tall[2], "1.5 ┤─"), tall[2])
	require.True(t, strings.HasPrefix(tall[3], "  1 ┤┄"), tall[3])
	// The plain character form places the lines by its own rows.
	plain := panel.Render(qualityFrame(40), 4, panel.GutterWidth(), Options{ASCII: true, Cursor: -1})
	for _, line := range plain {
		if strings.Contains(line, "=") {
			require.True(t, strings.HasPrefix(line, "1.5 +"), line)
		}
	}
	// The gutter is wide enough for a level's label at any height.
	wide := Panel{Unit: Bytes, Series: []Series{{Points: qualityFlat(100 * 1024 * 1024)}, {Kind: Reference, Points: qualityFlat(2.6 * 1024 * 1024 * 1024)}}}
	require.Equal(t, len("2.6Gi"), wide.GutterWidth())
}

func TestALevelLineNeverOverdrawsData(t *testing.T) {
	// The usage runs exactly on the limit for a while and crosses the
	// request: every dot of the line is still there.
	usage := qualityFlat(0.4)
	for i := 40; i < 80; i++ {
		usage[i].V = 1.5
	}
	line := Series{Points: usage, Stroke: Solid}
	alone := Panel{Unit: Cores, Ceiling: 2, Series: []Series{line}}
	with := Panel{Unit: Cores, Ceiling: 2, Series: []Series{
		{Kind: Reference, Points: qualityFlat(1.5), Glyph: '─', ASCII: '='},
		{Kind: Reference, Points: qualityFlat(1), Glyph: '┄', ASCII: '.'},
		line,
	}}
	for _, ascii := range []bool{false, true} {
		options := Options{ASCII: ascii, Cursor: -1}
		bare := alone.Render(qualityFrame(60), 6, 4, options)
		drawn := with.Render(qualityFrame(60), 6, 4, options)
		require.Len(t, drawn, len(bare))
		levels := 0
		for row := range bare {
			a, b := []rune(bare[row]), []rune(drawn[row])
			for col := 6; col < len(a) && col < len(b); col++ {
				if a[col] != ' ' {
					require.Equal(t, string(a[col]), string(b[col]), "row %d column %d: data is drawn over the level, never under it", row, col)
				}
			}
			levels += strings.Count(drawn[row], "─") + strings.Count(drawn[row], "=")
		}
		require.Positive(t, levels, "the level is still drawn where there is no data")
	}
}

func TestGridCeilingKeepsOneScaleFromPageToPage(t *testing.T) {
	minis := []Mini{{Title: "a", Series: []Series{{Points: qualityFlat(0.86)}}}, {Title: "b", Series: []Series{{Points: qualityFlat(0.012)}}}}
	all := Grid{Unit: Ratio, Minis: minis}
	require.InDelta(t, 1.0, all.Top(), 1e-9)
	// The second page holds only the quiet chart; alone it would rescale.
	alone := Grid{Unit: Ratio, Minis: minis[1:]}
	require.InDelta(t, 0.02, alone.Top(), 1e-9)
	page := Grid{Unit: Ratio, Minis: minis[1:], Ceiling: all.Top()}
	require.InDelta(t, 1.0, page.Top(), 1e-9)
	lines, layout := page.Render(qualityStart, qualityStart.Add(time.Hour), 30*time.Second, 60, 9, Options{Cursor: -1})
	require.Equal(t, 1, layout.Shown)
	require.Contains(t, strings.Join(lines, "\n"), "100% ┤")
	require.Equal(t, all.Layout(60, 9).Gutter, layout.Gutter, "the label gutter does not move either")

	// A level line in a mini chart is labelled with its own value; the
	// gutter of the grid makes room for it, so every plot row starts in
	// the same column.
	limited := Grid{Unit: Cores, Minis: []Mini{
		{Title: "a", Series: []Series{{Kind: Reference, Points: qualityFlat(1.5), Glyph: '─', ASCII: '='}, {Points: qualityFlat(1.52), Stroke: Solid}}},
		{Title: "b", Series: []Series{{Kind: Reference, Points: qualityFlat(1.5), Glyph: '─', ASCII: '='}, {Points: qualityFlat(0.4), Stroke: Solid}}},
	}}
	lines, layout = limited.Render(qualityStart, qualityStart.Add(time.Hour), 30*time.Second, 80, 7, Options{Cursor: -1})
	require.Equal(t, 2, layout.Columns)
	require.Equal(t, len("1.5"), layout.Gutter)
	axis := -1
	for _, line := range lines[1:5] {
		at := strings.IndexAny(line, "┤│")
		require.GreaterOrEqual(t, at, 0, line)
		column := cellWidth(line[:at])
		if axis < 0 {
			axis = column
		}
		require.Equal(t, axis, column, "the axis is in one column on every row: %q", line)
	}
	// The first chart's data runs on its limit and is drawn over it; the
	// second shows the level on the same row.
	require.Contains(t, strings.Join(lines, "\n"), "1.5 ┤⠉")
	require.Contains(t, strings.Join(lines, "\n"), "│────")
}

func TestLegendNeverDropsAnEntryWithoutCountingIt(t *testing.T) {
	entries := []Entry{
		{Glyph: "a", Name: "2bq7k", Value: "362m"}, {Glyph: "b", Name: "8wzpd", Value: "344m"}, {Glyph: "c", Name: "hx4mt", Value: "527m"},
		{Glyph: "─", Name: "min limit", Value: "1.5"}, {Glyph: "┄", Name: "min request", Value: "1"}, {Name: "throttled", Value: "2.15%"},
	}
	more := regexp.MustCompile(`\+(\d+) more`)
	for width := 24; width <= 140; width++ {
		for _, maxLines := range []int{1, 2, 3} {
			lines := Legend("CPU cores", entries, width, maxLines, nil)
			require.LessOrEqual(t, len(lines), maxLines)
			text := strings.Join(lines, "\n")
			shown := 0
			for _, entry := range entries {
				if strings.Contains(text, entry.rest()) {
					shown++
				}
			}
			counted := 0
			if match := more.FindStringSubmatch(text); match != nil {
				counted, _ = strconv.Atoi(match[1])
			}
			require.Equal(t, len(entries), shown+counted, "width %d, %d lines: %q", width, maxLines, text)
			for _, line := range lines {
				require.LessOrEqual(t, cellWidth(line), width, line)
			}
		}
	}
}
