// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/chart"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangGroups are the groups of a hang page: what lines up, what differs,
// what is quiet and what was not read.
func hangGroups() []Group {
	stall := Levels(series(1, 1, 1, 9, 9, 1, 1, 1, 1, 1, 9, 1, 1, 1, 1, 1, 1, 1, 1, 1), over(4))
	return []Group{
		{Label: "LINES UP WITH THE STALLS", Color: testPalette.Warn, Rows: []Row{
			{Subject: "ingress", What: "clients gave up (499)", Number: "6.3%", Compare: "of requests", Color: testPalette.Warn, Strip: stall},
			{Subject: "pod hx4mt", What: "response time p99", Number: "47.9s", Compare: "peers 0.35s", Color: testPalette.Fail, Strip: stall, Selected: true},
			{Subject: "pod hx4mt", What: "CPU throttled", Number: "88%", Compare: "peers 1%", Color: testPalette.Fail, Strip: stall},
			{Subject: "pod hx4mt", What: "not ready", Number: "twice", Compare: "up to 3m", Color: testPalette.Fail, Strip: Held(span(3, 5), span(10, 11))},
		}},
		{Label: "DIFFERS, BUT NOT DURING A STALL", Role: Secondary, Rows: []Row{
			{Subject: "network", What: "packets dropped", Number: "0.4/s", Compare: "to pod 2bq7k", Color: testPalette.Info, Strip: Levels(series(0, 0, 0, 0, 0, 0, 0, 1), over(0.5))},
		}},
		{Label: "QUIET", Role: QuietRole, Rows: []Row{
			{Subject: "pods 2bq7k, 8wzpd", What: "response time p99", Number: "0.35s"},
			{Subject: "dns", What: "lookup time p99", Number: "6ms"},
		}},
		{Label: "NOT READ", Role: NotReadRole, Rows: []Row{
			{Subject: "postgresql", What: "no latency metric found", Strip: Quiet()},
		}},
	}
}

func TestMeasureGivesTheTextWayInOrder(t *testing.T) {
	groups := hangGroups()
	for name, c := range map[string]struct {
		width int
		want  Layout
	}{
		"wide: every column as wide as its text, the strip takes the rest": {132,
			Layout{Width: 132, Subject: 9, What: 21, Number: 5, Compare: 12, Lead: 17, Tail: 24, StripCol: 57, Strip: 73}},
		"80: the strips shorten first": {80,
			Layout{Width: 80, Subject: 9, What: 21, Number: 5, Compare: 12, Lead: 17, Tail: 24, StripCol: 57, Strip: 23}},
		"72: under a comfortable strip the comparison goes": {72,
			Layout{Width: 72, Subject: 9, What: 21, Number: 5, Lead: 17, Tail: 24, StripCol: 47, Strip: 25}},
		"60: then the text of the quiet lines and what is measured are shortened, as far as the strips need": {60,
			Layout{Width: 60, Subject: 9, What: 18, Number: 5, Lead: 17, Tail: 17, StripCol: 40, Strip: 20}},
		"52: down to their shortest": {52,
			Layout{Width: 52, Subject: 9, What: 10, Number: 5, Lead: 17, Tail: 9, StripCol: 32, Strip: 20}},
		"44: then the strips narrow to the least that still shows when": {44,
			Layout{Width: 44, Subject: 9, What: 10, Number: 5, Lead: 17, Tail: 9, StripCol: 32, Strip: 12}},
		"40: the subject gives last, and when that is not enough there are no strips": {40,
			Layout{Width: 40, Subject: 6, What: 10, Number: 5, Lead: 17, Tail: 6, StripCol: 29, Strip: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, Measure(groups, c.width, 0, false))
		})
	}
}

func TestMeasureOtherShapes(t *testing.T) {
	groups := hangGroups()
	// The number after the strip: the text before it is the subject and
	// what is measured only.
	after := Measure(groups[:2], 100, 0, true)
	assert.Equal(t, Layout{Width: 100, Subject: 9, What: 21, Number: 5, Compare: 12, NumberAfter: true, StripCol: 36, Strip: 41}, after)

	// A page with a main chart: the strips start where its plot does.
	chartRows := []Group{{Label: "ALSO", Rows: []Row{{What: "CPU", Number: "340m"}}}}
	assert.Equal(t, 12, Measure(chartRows, 80, 10, true).StripCol, "the gutter of the chart, its space and its axis")
	assert.Equal(t, 7, Measure(chartRows, 80, 2, true).StripCol, "the text of the rows when that is wider")
	assert.Equal(t, 7, Measure(nil, 80, 5, false).StripCol, "the chart alone")
	assert.Equal(t, 73, Measure(nil, 80, 5, false).Strip)

	// A tag is the last thing to go before the strips.
	tagged := []Group{{Rows: []Row{{Subject: "hx4mt", What: "worker-f", Number: "47.9s", Tag: "differs"}}}}
	assert.Equal(t, 7, Measure(tagged, 60, 0, true).Tag)
	assert.Equal(t, 0, Measure(tagged, 40, 0, true).Tag)
	assert.Equal(t, 14, Measure(tagged, 40, 0, true).Strip)

	// Long text is capped, not allowed to take the screen.
	long := []Group{{Rows: []Row{{Subject: strings.Repeat("s", 80), What: strings.Repeat("w", 80), Number: strings.Repeat("9", 80)}}}}
	l := Measure(long, 200, 0, false)
	assert.Equal(t, []int{maxSubject, maxWhat, maxNumber}, []int{l.Subject, l.What, l.Number})

	// Width is measured in cells.
	wide := Measure([]Group{{Rows: []Row{{Subject: "节点", What: "内存压力"}}}}, 80, 0, false)
	assert.Equal(t, []int{4, 8}, []int{wide.Subject, wide.What})
}

// stripStart is the column of the first strip glyph of a drawn row.
func stripStart(row string) int {
	index := strings.IndexAny(row, "▁▂▃▄▅▆▇█·×")
	if index < 0 {
		return -1
	}
	return runewidth.StringWidth(row[:index])
}

func TestEveryStripStartsAtTheSameColumn(t *testing.T) {
	groups := hangGroups()
	for _, width := range []int{132, 100, 80, 72, 60, 52, 46, 44} {
		layout := Measure(groups, width, 0, false)
		require.GreaterOrEqual(t, layout.Strip, MinStrip)
		frame := twenty
		frame.Width = layout.Strip
		for gi := range groups {
			lines := GroupLines(groups[gi], len(groups[gi].Rows), true, Bands{}, frame, layout, plainOpts)
			rows := 0
			for _, line := range lines[1:] {
				row := plain(line)
				if row == "" {
					continue
				}
				rows++
				assert.Equal(t, layout.StripCol, stripStart(row), "width %d: %q", width, row)
				assert.Equal(t, layout.StripCol+layout.Strip, runewidth.StringWidth(row), "width %d: %q", width, row)
				assert.LessOrEqual(t, runewidth.StringWidth(row), width)
			}
			assert.Equal(t, len(groups[gi].Rows), rows)
		}
	}
}

func TestGroupLines(t *testing.T) {
	groups := hangGroups()
	layout := Measure(groups, 100, 0, false)
	frame := twenty
	frame.Width = layout.Strip
	require.Equal(t, 41, layout.Strip)
	lines := func(g Group, spaced bool, o Options) []string {
		return plainAll(GroupLines(g, len(g.Rows), spaced, Bands{}, frame, layout, o))
	}
	// Twenty minutes in 41 columns. The samples taken at 14:04 and 14:05
	// stand for the minutes from 14:03 to 14:05, the one at 14:11 for the
	// minute from 14:10: the same columns as a condition that held then.
	stall := "▁▁▁▁▁▁█████▁▁▁▁▁▁▁▁▁███▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁"
	assert.Equal(t, []string{
		"  LINES UP WITH THE STALLS",
		"  ingress    clients gave up (499)  6.3%   of requests   " + stall,
		"",
		"> pod hx4mt  response time p99      47.9s  peers 0.35s   " + stall,
		"             CPU throttled          88%    peers 1%      " + stall,
		"             not ready              twice  up to 3m      " + stall,
	}, lines(groups[0], true, plainOpts), "the subject once per run; a blank row between a run and its neighbour; the selected row marked")
	assert.Len(t, lines(groups[0], false, plainOpts), 5, "without the blank row on a short screen")
	assert.Equal(t, []string{
		"  QUIET",
		"  pods 2bq7k, 8wzpd  response time p99  0.35s            " + strings.Repeat("·", 41),
		"  dns                lookup time p99  6ms                " + strings.Repeat("·", 41),
	}, lines(groups[2], true, plainOpts), "a quiet thing is one dim line: its subject, then what was measured and the number")
	assert.Equal(t, []string{
		"  NOT READ",
		"  postgresql         no latency metric found             " + strings.Repeat("×", 41),
	}, lines(groups[3], true, plainOpts), "what was not read is the gap glyph all along, whatever strip it was given")
	assert.Equal(t, []string{
		"  NOT READ",
		"  postgresql         no latency metric found             " + strings.Repeat("x", 41),
	}, lines(groups[3], true, asciiOpts))

	// The first rows only, when the plan leaves the last out.
	assert.Len(t, GroupLines(groups[0], 2, false, Bands{}, frame, layout, plainOpts), 3)
	assert.Len(t, GroupLines(groups[0], 99, false, Bands{}, frame, layout, plainOpts), 5)
}

func TestNumberAfterTheStripWithTagAndHead(t *testing.T) {
	quiet := Levels(series(1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1), over(4)).Top(9)
	group := Group{Label: "RESPONSE TIME p99", Color: testPalette.Accent, Head: "peak", Rows: []Row{
		{Subject: "hx4mt", What: "worker-f", Number: "47.9s", Tag: "differs", Color: testPalette.Fail,
			Strip: Levels(series(1, 1, 1, 9, 9, 1, 1, 1, 1, 1, 9, 1, 1, 1, 1, 1, 1, 1, 1, 1), over(4)).Top(9)},
		{Subject: "2bq7k", What: "worker-d", Number: "0.35s", Strip: quiet},
		{Subject: "8wzpd", What: "worker-e", Number: "0.35s", Strip: quiet},
	}}
	layout := Measure([]Group{group}, 55, 0, true)
	frame := twenty
	frame.Width = layout.Strip
	require.Equal(t, 20, layout.Strip)
	got := GroupLines(group, 3, true, Bands{}, frame, layout, colorOpts)
	assert.Equal(t, []string{
		"  RESPONSE TIME p99                      peak",
		"  hx4mt  worker-f  ▁▁▁██▁▁▁▁▁█▁▁▁▁▁▁▁▁▁  47.9s  differs",
		"  2bq7k  worker-d  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  0.35s",
		"  8wzpd  worker-e  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁  0.35s",
	}, plainAll(got), "rows with different subjects are not spaced apart")

	// One colour per subject: the subject, its number, its tag and what
	// is over its rule. A row without a colour is quiet.
	cells := draw(t, got, 55)
	odd, peer := cells[1], cells[2]
	fail := testPalette.Fail
	assert.Equal(t, shown{r: 'h', fg: fail, bold: true}, odd[2], "the subject")
	assert.Equal(t, shown{r: 'w', fg: testPalette.Text}, odd[9], "what is measured is plain")
	assert.Equal(t, shown{r: '█', fg: fail}, odd[22], "over its rule")
	assert.Equal(t, shown{r: '4', fg: fail}, odd[41], "the number")
	assert.Equal(t, shown{r: 'd', fg: fail}, odd[48], "the tag")
	assert.Equal(t, shown{r: '2', fg: testPalette.Muted}, peer[2])
	assert.Equal(t, shown{r: 'w', fg: testPalette.Muted, dim: true}, peer[9])
	assert.Equal(t, shown{r: '0', fg: testPalette.Muted}, peer[41])
	assert.Equal(t, shown{r: 'R', fg: testPalette.Accent, bold: true}, cells[0][2])
	assert.Equal(t, shown{r: 'p', fg: testPalette.Muted}, cells[0][41], "the word over the number column")
}

func TestRowsWithoutStrips(t *testing.T) {
	groups := hangGroups()
	layout := natural(groups, 44, false).textOnly()
	assert.Equal(t, []string{
		"  LINES UP WITH THE STALLS",
		"  ingress    clients gave up (499)  6.3%",
		"> pod hx4mt  response time p99      47.9s",
		"             CPU throttled          88%",
		"             not ready              twice",
	}, plainAll(GroupLines(groups[0], 4, false, Bands{Episodes: []chart.Interval{span(3, 5)}}, chart.Frame{}, layout, colorOpts)))
	assert.Equal(t, []string{
		"  QUIET",
		"  pods 2bq7k, 8wzpd  response time p99  0.3…",
		"  dns                lookup time p99  6ms",
	}, plainAll(GroupLines(groups[2], 2, false, Bands{}, chart.Frame{}, layout, plainOpts)))
}

func TestFoldedGroupNamesItsRows(t *testing.T) {
	groups := hangGroups()
	assert.Equal(t, []string{"network packets dropped 0.4/s to pod 2bq7k"}, groups[1].items(), "a row of evidence: its subject, what, the number")
	assert.Equal(t, []string{"pods 2bq7k, 8wzpd", "dns"}, groups[2].items(), "a quiet row: its subject")
	assert.Equal(t, []string{"postgresql"}, groups[3].items())
	assert.Equal(t, []string{"memory", "restarts"}, (&Group{Summary: []string{"memory", "restarts"}, Rows: groups[2].Rows}).items(), "a summary of its own wins")
	assert.Equal(t, []string{"CPU 340m", "restarts 6"}, (&Group{Role: Secondary, Rows: []Row{{What: "CPU", Number: "340m"}, {What: "restarts", Number: "6"}, {What: "CPU", Number: "340m"}}}).items(), "named once")
	assert.Equal(t, []string{"lookup time"}, (&Group{Role: QuietRole, Rows: []Row{{What: "lookup time"}}}).items(), "what is measured when there is no subject")
}

// A view whose numbers are read under a cursor reserves the widest it can
// write: the columns, and with them every strip, then stay where they are
// whatever the cursor reads.
func TestMeasureKeepsTheColumnsOfAGroupThatReservesThem(t *testing.T) {
	group := func(number, compare string) []Group {
		return []Group{{Label: "ALSO", Role: Secondary, NumberWidth: 7, CompareWidth: 14, Rows: []Row{
			{What: "CPU", Number: number, Compare: compare},
			{What: "network", Number: "9kB/s"},
		}}}
	}
	at := Measure(group("340m", ""), 132, 10, true)
	assert.Equal(t, 7, at.Number)
	assert.Equal(t, 14, at.Compare)
	for _, other := range [][]Group{group("1.5", "x2x9q at limit"), group("–", ""), group("907kB/s", "hx4mt")} {
		assert.Equal(t, at, Measure(other, 132, 10, true))
	}
	// Text wider than what was reserved still gets its room.
	assert.Equal(t, 9, Measure(group("1021.5MiB", ""), 132, 10, true).Number)
	// A group without rows is one line under the axis and holds no column.
	assert.Zero(t, Measure([]Group{{Label: "QUIET", Role: QuietRole, NumberWidth: 9, Summary: []string{"dns"}}}, 132, 0, false).Number)
}

// A view whose row cursor opens a folded group, or turns a row back in
// that a short page left out, reserves the widest subject of all its rows:
// the columns are measured from the rows that are drawn, and stay where
// they are whichever of them that is.
func TestMeasureKeepsTheSubjectColumnOfAGroupThatReservesIt(t *testing.T) {
	rows := []Row{
		{Subject: "Error ratio", Number: "0.2% -> 22%", Strip: Trend(nil)},
		{Subject: "Memory/limit", Number: "37% -> 99%", Strip: Trend(nil)},
	}
	group := func(least, shown int) []Group {
		return []Group{{Label: "WORSE AFTER", SubjectWidth: least, Rows: rows[:shown]}}
	}
	all := Measure(group(0, 2), 80, 0, false)
	assert.Equal(t, 12, all.Subject)
	short := Measure(group(0, 1), 80, 0, false)
	assert.Equal(t, 11, short.Subject, "measured from the rows that are drawn")
	assert.NotEqual(t, all.StripCol, short.StripCol, "so a strip moves when a row is left out")
	assert.Equal(t, all, Measure(group(12, 1), 80, 0, false), "unless the group reserves the column")
	// A subject wider than what was reserved still gets its room, and the
	// column still gives way where the strips have none.
	assert.Equal(t, 12, Measure(group(4, 2), 80, 0, false).Subject)
	assert.Less(t, Measure(group(24, 2), 44, 0, false).Subject, 24)
	// A group without rows is one line under the axis and holds no column.
	assert.Zero(t, Measure([]Group{{Label: "QUIET", Role: QuietRole, SubjectWidth: 9, Summary: []string{"dns"}}}, 132, 0, false).Subject)
}
