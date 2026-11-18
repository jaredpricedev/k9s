// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twenty is twenty columns, one a minute, with the strips at column 12.
var (
	twenty       = chart.Frame{Start: minute(0), End: minute(20), Step: time.Minute, Width: 20}
	twentyLayout = Layout{Width: 40, StripCol: 12, Strip: 20}
	stallBands   = Bands{Label: "stalls", Episodes: []chart.Interval{span(3, 5), span(10, 11), {Start: minute(17)}}}
)

func TestMarkerRowAndAxisCarryTheBandsWithoutColour(t *testing.T) {
	for name, c := range map[string]struct {
		bands Bands
		opts  Options
		want  []string
	}{
		"episodes: a marker per column, heavy on the axis; a moment is one column": {stallBands, plainOpts, []string{
			"     stalls    ▼▼     ▼      ▼",
			"            ┬──━━─────┯──────━─┬",
			"  UTC     14:00     14:10    14:20",
		}},
		"plain characters with noIcons": {stallBands, asciiOpts, []string{
			"     stalls    vv     v      v",
			"            +--==-----#------=-+",
			"  UTC     14:00     14:10    14:20",
		}},
		"a labelled moment is its key on the axis, the cursor a caret": {
			Bands{Label: "stalls", Episodes: []chart.Interval{span(3, 5)}, Marks: []chart.Mark{{Key: 'R', T: minute(8), Label: "rollout"}}, Cursor: minute(15)}, plainOpts, []string{
				"     stalls    ▼▼   ┊     ┆",
				"            ┬──━━───R─┬───^────┬",
				"  UTC     14:00     14:10    14:20",
			}},
		"an episode that began before the window is clipped to it": {Bands{Label: "rollout", Episodes: []chart.Interval{span(-30, 2), span(18, 90)}}, plainOpts, []string{
			"    rollout ▼▼                ▼▼",
			"            ┯━────────┬───────━┯",
			"  UTC     14:00     14:10    14:20",
		}},
		"a long label is cut, never written over the strips": {Bands{Label: "fault windows", Episodes: []chart.Interval{span(0, 1)}}, plainOpts, []string{
			"  fault wi… ▼",
			"            ┯─────────┬────────┬",
			"  UTC     14:00     14:10    14:20",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got := append([]string{MarkerRow(c.bands, twenty, twentyLayout, c.opts)}, AxisLines(c.bands, twenty, twentyLayout, c.opts)...)
			assert.Equal(t, c.want, plainAll(got))
		})
	}
	assert.Empty(t, MarkerRow(stallBands, chart.Frame{}, twentyLayout, plainOpts))
	assert.Nil(t, AxisLines(stallBands, chart.Frame{}, twentyLayout, plainOpts))
	assert.True(t, stallBands.Any())
	assert.False(t, Bands{Marks: []chart.Mark{{Key: 'R', T: minute(1)}}}.Any(), "a moment is not an episode")
}

func TestAxisLabels(t *testing.T) {
	hour := hourFrame(60)
	for name, c := range map[string]struct {
		frame  chart.Frame
		layout Layout
		want   string
	}{
		"UTC once, at the start of the row": {hour, Layout{Width: 80, StripCol: 18, Strip: 60},
			"  UTC           14:00     14:10     14:20     14:30     14:40     14:50    15:00"},
		"a label may reach into the left margin": {hour, Layout{Width: 62, StripCol: 2, Strip: 60},
			"14:00     14:10     14:20     14:30     14:40     14:50 UTC"},
		"UTC after the labels when the start has no room": {twenty, Layout{Width: 30, StripCol: 4, Strip: 20},
			"  14:00     14:10    14:20 UTC"},
		"a label that would leave the screen is left out": {hour, Layout{Width: 78, StripCol: 18, Strip: 60},
			"  UTC           14:00     14:10     14:20     14:30     14:40     14:50"},
		// A tick a day stands at midnight UTC: the clock would say 00:00
		// under every one of them.
		"a window of days names the day under each tick": {chart.Frame{Start: minute(-5 * 24 * 60), End: minute(0), Width: 60}, Layout{Width: 80, StripCol: 18, Strip: 60},
			"  UTC                Nov13       Nov14       Nov15       Nov16       Nov17"},
		"a window within a day keeps the clock": {chart.Frame{Start: minute(-24 * 60), End: minute(0), Width: 60}, Layout{Width: 80, StripCol: 18, Strip: 60},
			"  UTC                     18:00          00:00          06:00          12:00"},
	} {
		t.Run(name, func(t *testing.T) {
			lines := AxisLines(Bands{}, c.frame, c.layout, plainOpts)
			require.Len(t, lines, 2)
			assert.Equal(t, c.want, plain(lines[1]))
			assert.LessOrEqual(t, width(lines[1]), c.layout.Width)
		})
	}
}

func TestMarkLegend(t *testing.T) {
	bands := Bands{Marks: []chart.Mark{
		{Key: 'R', T: minute(8), Label: "rollout to rev 2 at 14:08 · memory limit changed 512Mi → 256Mi"},
		{Key: 'S', T: minute(300), Label: "outside the window"},
		{T: minute(9), Label: "no key"},
	}}
	assert.Equal(t, []string{"  R rollout to rev 2 at 14:08 · memory limit changed 512Mi → 256Mi"}, plainAll(MarkLegend(bands, twenty, 80, plainOpts)))
	assert.Equal(t, []string{"  R rollout to rev 2 at 14:08 | memor..."}, plainAll(MarkLegend(bands, twenty, 40, asciiOpts)))
	row := draw(t, MarkLegend(bands, twenty, 80, colorOpts), 80)[0]
	assert.Equal(t, shown{r: 'R', fg: testPalette.Mark, bold: true}, row[2])
	assert.Nil(t, MarkLegend(Bands{}, twenty, 80, plainOpts))
}

// bandColumns are the columns of a drawn row whose background is the band.
func bandColumns(row []shown) []int {
	var out []int
	for col, c := range row {
		if c.bg == testPalette.Band {
			out = append(out, col)
		}
	}
	return out
}

func TestBandsShadeTheSameColumnsOfEveryLine(t *testing.T) {
	group := Group{Label: "LINES UP WITH THE STALLS", Color: testPalette.Warn, Rows: []Row{
		{Subject: "pod", What: "p99", Number: "9s", Color: testPalette.Fail, Strip: Levels(series(1, 1, 1, 9, 9, 1, 1, 1, 1, 1, 9, 1, 1, 1, 1, 1, 1, 1, 1, 1), over(4))},
		{Subject: "pod", What: "ready", Number: "0", Color: testPalette.Fail, Strip: Held(span(3, 5))},
		{Subject: "dns", What: "lookups", Number: "6ms", Strip: Quiet()},
	}}
	layout := twentyLayout
	layout.Subject, layout.What, layout.Number = 3, 0, 3
	panel := chart.Panel{Unit: chart.Count, Series: []chart.Series{{Name: "p99", Color: testPalette.Accent, Points: series(1, 1, 1, 9, 9, 1, 1, 1, 1, 1, 9, 1, 1, 1, 1, 1, 1, 1, 1, 1)}}}
	var lines []string
	lines = append(lines, MarkerRow(stallBands, twenty, layout, colorOpts), Shade("", stallBands, twenty, layout, colorOpts))
	lines = append(lines, GroupLines(group, 3, true, stallBands, twenty, layout, colorOpts)...)
	for _, line := range panel.Render(twenty, 3, layout.StripCol-2, chart.Options{Cursor: -1, AxisColor: testPalette.Muted}) {
		lines = append(lines, Shade(line, stallBands, twenty, layout, colorOpts))
	}
	lines = append(lines, Shade("  [#79c7d4::b]A HEADING THAT RUNS INTO THE STRIPS[-:-:-] and on", stallBands, twenty, layout, colorOpts))
	cells := draw(t, lines, 40)
	want := []int{15, 16, 22, 29} // minutes 3, 4, 10 and 17 from column 12
	for y, row := range cells {
		assert.Equal(t, want, bandColumns(row), "line %d: %q", y, text(row))
	}
	// The glyphs keep their own colour on the band.
	assert.Equal(t, shown{r: '▼', fg: testPalette.Warn, bg: testPalette.Band, bold: true}, cells[0][15])
	hot := cells[3][15]
	assert.Equal(t, shown{r: '█', fg: testPalette.Fail, bg: testPalette.Band}, hot)
	assert.Equal(t, shown{r: '▁', fg: testPalette.Muted, bg: testPalette.Band, dim: true}, cells[3][29], "a band over a quiet column is still a band")
	assert.Equal(t, shown{r: '▁', fg: testPalette.Muted, dim: true}, cells[3][14])

	// The axis is not shaded: it is drawn heavy there instead.
	axis := draw(t, AxisLines(stallBands, twenty, layout, colorOpts), 40)
	assert.Empty(t, bandColumns(axis[0]))
	assert.Equal(t, shown{r: '━', fg: testPalette.Warn}, axis[0][15])
	assert.Equal(t, shown{r: '─', fg: testPalette.Muted, dim: true}, axis[0][14])
}

func TestShadeLeavesAlone(t *testing.T) {
	line := "  [#ef8278::b]pod[-::-]  text"
	for name, c := range map[string]struct {
		bands Bands
		frame chart.Frame
		opts  Options
	}{
		"a page without episodes":         {Bands{Label: "stalls"}, twenty, colorOpts},
		"a palette without a band colour": {stallBands, twenty, plainOpts},
		"episodes outside the window":     {Bands{Episodes: []chart.Interval{span(40, 50)}}, twenty, colorOpts},
		"a page without a frame":          {stallBands, chart.Frame{}, colorOpts},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, line, Shade(line, c.bands, c.frame, twentyLayout, c.opts))
		})
	}
}

// A rollout divides a window in two. One shaded column is not seen, so
// with Guide the moment runs down through every row, and the marker row
// and the axis mark the same column.
func TestAMomentWithAGuideDividesEveryRow(t *testing.T) {
	tenMinutes := chart.Frame{Start: minute(0), End: minute(10), Step: time.Minute, Width: 10}
	layout := Layout{Width: 30, StripCol: 12, Strip: 10}
	rollout := Bands{Label: "rev 2", Guide: true, Episodes: []chart.Interval{{Start: minute(4), End: minute(4)}}}
	levels := Levels(series(1, 1, 1, 1, 1, 9, 9, 9, 9, 9), over(4))
	unread := Levels(append(series(1, 1, 1, 1), chart.Point{T: minute(6), V: 9}), over(4))
	for name, c := range map[string]struct {
		strip       Strip
		want, ascii string
	}{
		"in place of the level of its column":   {levels, "▁▁▁▁┊█████", "____|#####"},
		"through a row where nothing happened":  {Events(minute(7)), "····┊··▲··", "....|..^.."},
		"never over an event":                   {Events(minute(4)), "····▲·····", "....^....."},
		"never over a column that was not read": {unread, "▁▁▁▁×█××××", "____x#xxxx"},
		"through a strip that draws nothing":    {Strip{}, "    ┊", "    |"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, plain(StripLine(c.strip, "", rollout, tenMinutes, plainOpts)))
			assert.Equal(t, c.ascii, plain(StripLine(c.strip, "", rollout, tenMinutes, asciiOpts)))
		})
	}

	// The marker, the guide of every line and the axis stand in one column.
	lines := []string{MarkerRow(rollout, tenMinutes, layout, colorOpts), Shade("", rollout, tenMinutes, layout, colorOpts),
		Shade("  WORSE AFTER", rollout, tenMinutes, layout, colorOpts),
		"            " + StripLine(levels, testPalette.Fail, rollout, tenMinutes, colorOpts)}
	lines = append(lines, AxisLines(rollout, tenMinutes, layout, colorOpts)...)
	cells := draw(t, lines, 30)
	assert.Equal(t, []string{
		"      rev 2     ▼",
		"                ┊",
		"  WORSE AFTER   ┊",
		"            ▁▁▁▁┊█████",
		"            ┬───━────┬",
		"  UTC     14:00    14:10",
	}, []string{text(cells[0]), text(cells[1]), text(cells[2]), text(cells[3]), text(cells[4]), text(cells[5])})
	assert.Equal(t, shown{r: '▼', fg: testPalette.Warn, bg: testPalette.Band, bold: true}, cells[0][16])
	assert.Equal(t, shown{r: '┊', fg: testPalette.Warn, bg: testPalette.Band}, cells[1][16], "the guide has the colour of its marker, on the band")
	assert.Equal(t, shown{r: '┊', fg: testPalette.Warn, bg: testPalette.Band}, cells[2][16])
	assert.Equal(t, shown{r: '┊', fg: testPalette.Warn}, cells[3][16], "a strip alone is not shaded")
	assert.Equal(t, shown{r: '━', fg: testPalette.Warn}, cells[4][16])

	// Without Guide a moment is its marker, its shaded column and its place
	// on the axis, as an episode is: nothing is drawn over a row.
	rollout.Guide = false
	assert.Equal(t, "▁▁▁▁▁█████", plain(StripLine(levels, "", rollout, tenMinutes, plainOpts)))
	assert.Empty(t, plain(Shade("", rollout, tenMinutes, layout, colorOpts)))
	// An episode that has a length is a band, never a guide.
	stall := Bands{Guide: true, Episodes: []chart.Interval{span(3, 5)}}
	assert.Equal(t, "▁▁▁▁▁█████", plain(StripLine(levels, "", stall, tenMinutes, plainOpts)))
}

func TestGuidesRunThroughBlankCellsOnly(t *testing.T) {
	bands := Bands{Marks: []chart.Mark{{Key: 'R', T: minute(4)}}, Cursor: minute(10)}
	for name, c := range map[string]struct {
		line, want string
	}{
		"an empty line is reached":              {"", "                ┊    ┆"},
		"a blank stretch":                       {"  ALSO                        end", "  ALSO          ┊    ┆        end"},
		"never between two words":               {"  MEMORY  254Mi now at limit", "  MEMORY  254Mi now at limit"},
		"never the last blank before a word":    {"  MEMORY         x", "  MEMORY         x   ┆"},
		"never over a glyph":                    {"            ████████████", "            ████████████"},
		"an escaped bracket counts as it shows": {"  pod [a[] xxxxxxxxxxxxxxxxxxx", "  pod [a] xxxxxxxxxxxxxxxxxxx"},
	} {
		t.Run(name, func(t *testing.T) {
			shaded := Shade(c.line, bands, twenty, twentyLayout, plainOpts)
			assert.Equal(t, c.want, text(draw(t, []string{shaded}, 40)[0]))
		})
	}
	ascii := Shade("", bands, twenty, twentyLayout, asciiOpts)
	assert.Equal(t, "                |    :", plain(ascii))
	row := draw(t, []string{Shade("", bands, twenty, twentyLayout, colorOpts)}, 40)[0]
	assert.Equal(t, shown{r: '┊', fg: testPalette.Mark}, row[16])

	// A cursor is drawn at every sample of the window, its first and its
	// last included; outside the window there is none.
	for at, want := range map[int]string{0: "            ┆", 1: "            ┆", 2: "             ┆", 20: "                               ┆", 21: "", -1: ""} {
		edge := Bands{Cursor: minute(at)}
		assert.Equal(t, want, text(draw(t, []string{Shade("", edge, twenty, twentyLayout, plainOpts)}, 40)[0]), "cursor at minute %d", at)
		axis := AxisLines(edge, twenty, twentyLayout, plainOpts)[0]
		assert.Equal(t, want != "", strings.Contains(axis, "^"), "cursor at minute %d: on the axis", at)
	}
}

func TestShadeSurvivesTagsThatResetTheBackground(t *testing.T) {
	bands := Bands{Episodes: []chart.Interval{span(0, 20)}}
	line := strings.Repeat(" ", 12) + "ab[#ef8278:-:b]cd[-:-:-]ef[red:blue:]gh[-:-:]ij"
	row := draw(t, []string{Shade(line, bands, twenty, twentyLayout, colorOpts)}, 40)[0]
	assert.Equal(t, "            abcdefghij", text(row))
	for col := 12; col < 32; col++ {
		if col == 18 || col == 19 {
			assert.Equal(t, "#0000ff", row[col].bg, "a background a line sets itself is kept")
			continue
		}
		assert.Equal(t, testPalette.Band, row[col].bg, "column %d", col)
	}
	assert.Empty(t, row[11].bg)
	assert.Empty(t, row[32].bg, "the band ends where the frame ends")
}
