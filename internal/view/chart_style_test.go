// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// The chart views draw a single series as a filled area and several as
// stepped lines; one key switches all of them to braille dots and back.

func hasBraille(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return r >= 0x2800 && r <= 0x28ff }) >= 0
}

func hasBlocks(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return r >= 0x2581 && r <= 0x2588 }) >= 0
}

func hasSteps(text string) bool { return strings.ContainsAny(text, "╭╮╰╯") }

func TestChartStyleKeyIsTheSameInEveryChartViewAndFreeElsewhere(t *testing.T) {
	slow := slowViewFixture(t)
	slow.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	workload, _, _, _ := compareViewFixture(t, hangTarget)
	pod, _, _, _ := compareViewFixture(t, graphPod)
	board := dashViewFixture(t, dashSpec{Board: true})
	query := dashViewFixture(t, querySpec(nil, "dns-p99"))
	probeV, _ := probeViewFixture(t)
	probeV.acceptPlan(probeTestPlan(), nil)
	impactV := impactViewFixture(t)

	require.Equal(t, ui.KeyB, chartStyleKey)
	charts := map[string]struct {
		actions *ui.KeyActions
		app     *App
	}{
		":slow":            {slow.actions, slow.app},
		":graph, workload": {workload.actions, workload.app},
		":graph, pod":      {pod.actions, pod.app},
		"drawn dashboard":  {board.actions, board.app},
		":query":           {query.actions, query.app},
	}
	for name, view := range charts {
		action, ok := view.actions.Get(chartStyleKey)
		require.True(t, ok, name)
		require.Equal(t, chartStyleLabel, action.Description, name)
		require.True(t, action.Opts.Visible, "%s: the key is named in the header menu and in help", name)
		// It cycles auto, dots, auto, for the whole session.
		require.Equal(t, chart.Auto, view.app.chartStyle(), name)
		require.Nil(t, action.Action(nil), name)
		require.Equal(t, chart.Dots, view.app.chartStyle(), name)
		require.Nil(t, action.Action(nil), name)
		require.Equal(t, chart.Auto, view.app.chartStyle(), name)
	}
	// The views of the walk that draw no chart with a style leave the
	// key alone: it never means something else next door.
	require.Empty(t, walkKey(probeV.actions, chartStyleKey), "probe")
	require.Empty(t, walkKey(impactV.actions, chartStyleKey), "impact")

	// The per-Pod page and its regrouping are modes of :graph: the key
	// is theirs too, and nothing they bind took it. n is named after what
	// the page steps through, the chart on the charts of a workload.
	for key, word := range map[tcell.Key]string{ui.KeyP: "Per pod", ui.KeyG: "Regroup", ui.KeyN: calmNextChartLabel, chartStyleKey: chartStyleLabel} {
		require.Equal(t, word, walkKey(workload.actions, key))
	}
}

// colourTags finds the colour tags the chart views write.
var colourTags = regexp.MustCompile(`\[(#[0-9a-fA-F]{6}|-)[^\]]*\]`)

func TestChartStyleSwitchesThePodGraphBetweenAreasAndDots(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphStory(), graphPod, time.Hour, graphNow)
	snapshot.Marks = []graphMark{{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2"}}
	last := int(snapshot.Window / snapshot.Step)
	page := func(style chart.Style, ascii bool) string {
		return graphPlainLines(t, renderGraph(snapshot, &graphPage{Width: 120, Height: 30, Cursor: last, Style: style, ASCII: ascii,
			Colors: graphPalette(config.DefaultSemanticPalette())}), 120)
	}
	plots := func(text, title string) string {
		// The plot rows of one chart: from its legend to the next legend.
		_, rest, ok := strings.Cut(text, title)
		require.True(t, ok, title)
		for _, next := range []string{"CPU cores", "NETWORK", "restarts │"} {
			if at := strings.Index(rest, "\n"+next); at >= 0 {
				rest = rest[:at]
			}
		}
		_, rows, ok := strings.Cut(rest, "\n")
		require.True(t, ok, title)
		return "\n" + rows
	}

	auto := page(chart.Auto, false)
	// One series against its limit: a filled area, named by a block.
	require.Contains(t, auto, "MEMORY  █ working set 60Mi  ─ limit 256Mi  ┄ request 128Mi")
	memory := plots(auto, "MEMORY")
	require.True(t, hasBlocks(memory), memory)
	require.False(t, hasBraille(memory))
	require.False(t, hasSteps(memory), "an area has no line")
	require.Contains(t, memory, "───", "the limit stays a line above the area")
	require.Contains(t, memory, "256Mi ┤")
	for _, row := range strings.Split(memory, "\n") {
		if strings.Contains(row, "128Mi ┤") {
			require.Contains(t, row, "┄┄┄┄", "the request stays a labelled line through the area")
			require.False(t, hasBlocks(row), "the body of the area gives way to the line: %q", row)
		}
	}
	// Two series share the network chart: stepped lines, each ending in
	// its letter.
	require.Contains(t, auto, "NETWORK  a rx 2.6kB/s  b tx 9kB/s")
	network := plots(auto, "NETWORK")
	require.False(t, hasBlocks(network), network)
	require.False(t, hasBraille(network))
	require.Contains(t, network, "──")
	for _, letter := range []string{"a\n", "b\n"} {
		require.Contains(t, network+"\n", letter, "each line ends in its letter")
	}
	require.Contains(t, auto, "b dots · Esc back", "the key line names the style key by what it switches to")
	require.False(t, hasBraille(auto), "no braille anywhere on the page")

	dotted := page(chart.Dots, false)
	require.Contains(t, dotted, "MEMORY  a working set 60Mi  ─ limit 256Mi  ┄ request 128Mi", "a braille line is named by its letter")
	require.True(t, hasBraille(plots(dotted, "MEMORY")))
	require.True(t, hasBraille(plots(dotted, "NETWORK")))
	require.False(t, hasBlocks(dotted))
	require.False(t, hasSteps(dotted))
	require.Contains(t, dotted, "b solid · Esc back")

	// The axis, the marks, the cursor line and the restarts strip are the
	// same lines in both styles: only the plots change.
	for _, line := range []string{"R rollout rev 2 04:48 · F first restart 04:50 · order, not cause", "Cursor 05:00:00Z (window end) · restarts 6, +6 in window"} {
		require.Contains(t, auto, line)
		require.Contains(t, dotted, line)
	}
	tail := func(text string) string {
		return text[strings.Index(text, "restarts │"):strings.LastIndex(text, "\n")]
	}
	require.Equal(t, tail(dotted), tail(auto))

	// noIcons draws plain characters whatever the style.
	require.Equal(t, strings.ReplaceAll(page(chart.Auto, true), "b dots", "b solid"), page(chart.Dots, true))
	for _, r := range page(chart.Auto, true) {
		require.True(t, r < 128 || r == '·', "%q is neither ASCII nor the shared separator", r)
	}
}

// An area that reaches its limit says so three ways: its top is drawn in
// the limit's row, its columns take the warning colour, and the legend says
// "at limit" in words at the cursor.
func TestChartAreaAtItsLimitSaysSoInWordsAndColour(t *testing.T) {
	end := graphNow.Truncate(30 * time.Second)
	near := end.Add(-10 * time.Minute)
	fake := &graphFake{rules: []graphRule{
		{contains: "container_memory_working_set_bytes", value: func(_ string, at time.Time) (float64, bool) {
			if at.Before(near) {
				return 100 * graphMi, true
			}
			return 250 * graphMi, true
		}},
		{contains: "kube_pod_container_resource_limits", value: func(string, time.Time) (float64, bool) { return 256 * graphMi, true }},
	}}
	snapshot := loadGraph(t.Context(), fake, graphPod, time.Hour, graphNow)
	colors := graphPalette(config.DefaultSemanticPalette())
	require.NotEmpty(t, colors.AtLimit)
	require.NotEqual(t, colors.AtLimit, colors.Data[0])
	last := int(snapshot.Window / snapshot.Step)
	render := func(cursor int, style chart.Style) (tagged []string, text string) {
		tagged = renderGraph(snapshot, &graphPage{Width: 100, Height: 30, Cursor: cursor, Style: style, Colors: colors})
		return tagged, graphPlainLines(t, tagged, 100)
	}

	tagged, text := render(last, chart.Auto)
	require.Contains(t, text, "MEMORY  █ working set 250Mi at limit  ─ limit 256Mi")
	warned, calm := 0, 0
	for _, line := range tagged {
		warned += strings.Count(line, "["+colors.AtLimit+"::]█")
		calm += strings.Count(line, "["+colors.Data[0]+"::]█")
	}
	require.Positive(t, warned, "the columns at the limit are drawn in the warning colour")
	require.Positive(t, calm, "the columns under it keep the colour of the series")
	for _, line := range tagged {
		// Every tag has both colons and is closed.
		for _, tag := range colourTags.FindAllString(line, -1) {
			require.Equal(t, 2, strings.Count(tag, ":"), "%q in %q", tag, line)
		}
	}
	// The row of the limit: the line where the area is under it, the top
	// of the area where it is at it.
	var limitRow string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "256Mi ┤") {
			limitRow = line
		}
	}
	require.NotEmpty(t, limitRow)
	require.Contains(t, limitRow, "────")
	require.True(t, hasBlocks(limitRow), "the area ends in the row of its limit: %q", limitRow)

	// With the cursor before the rise the value is not at the limit and
	// nothing claims it is.
	_, text = render(last-60, chart.Auto)
	require.Contains(t, text, "MEMORY  █ working set 100Mi  ─ limit 256Mi")
	require.NotContains(t, text, "at limit")

	// The words do not depend on the style; the colour of an area does.
	tagged, text = render(last, chart.Dots)
	require.Contains(t, text, "MEMORY  a working set 250Mi at limit  ─ limit 256Mi")
	require.NotContains(t, strings.Join(tagged, "\n"), "["+colors.AtLimit+"::]█")

	report := graphReport(snapshot)
	require.Contains(t, report, `"at limit" is a sample within 5% of its limit or above it`)
	require.Contains(t, report, "b draws braille dots instead")
}

func TestChartStyleReachesMinisTrendsAndDashboards(t *testing.T) {
	// The per-Pod page: every mini chart is an area, the trends are
	// blocks; in dots both are braille.
	base, c := hangCompare(t, hangStory())
	compare := func(style chart.Style, width, height int, by graphGrouping) string {
		page := comparePage(width, height)
		page.Style, page.Grouping = style, by
		return graphPlainLines(t, renderGraphCompare(base, c, page), width)
	}
	wide := compare(chart.Auto, 132, 33, graphByPod)
	require.True(t, hasBlocks(wide))
	require.False(t, hasBraille(wide), wide)
	require.False(t, hasSteps(wide), "one Pod per chart: each is an area")
	require.Contains(t, wide, "b dots · Esc back")
	dotted := compare(chart.Dots, 132, 33, graphByPod)
	require.True(t, hasBraille(dotted))
	require.False(t, hasBlocks(dotted))
	require.Contains(t, dotted, "b solid · Esc back")
	// A group is the median as the area, with its minimum and maximum as
	// lines; the title says which is which in each style.
	require.Contains(t, compare(chart.Auto, 132, 33, graphByNode), "by node · area median, lines min and max")
	require.Contains(t, compare(chart.Dots, 132, 33, graphByNode), "by node · line median, thin lines min and max")
	// The ranked list of a narrow page draws its trends the same way.
	list := compare(chart.Auto, 60, 20, graphByPod)
	require.Contains(t, list, "POD ")
	require.True(t, hasBlocks(list))
	require.False(t, hasBraille(list))
	require.True(t, hasBraille(compare(chart.Dots, 60, 20, graphByPod)))

	// A drawn dashboard in the calm layout: the main chart follows the
	// style, and the key line names what b switches to. The strips of the
	// rows are the kit's eighth blocks in either style.
	s := dashStorySnapshot(t)
	calmPage := func(style chart.Style) string {
		page := dashPageOf(132, 33)
		page.Style = style
		return dashText(t, s, page)
	}
	require.True(t, hasSteps(calmPage(chart.Auto)), "several series: stepped lines")
	require.False(t, hasBraille(calmPage(chart.Auto)))
	require.Contains(t, calmPage(chart.Auto), "s queries · b dots · r re-read · Esc back")
	require.True(t, hasBraille(calmPage(chart.Dots)), "the main chart follows the style")
	require.False(t, hasSteps(calmPage(chart.Dots)))
	require.Contains(t, calmPage(chart.Dots), "s queries · b solid · r re-read · Esc back")

	// Under the all-detail key: stat trends are blocks, a chart of several
	// lines is stepped lines; zoomed, a panel of one series is an area.
	dash := func(style chart.Style, zoom bool, selected int) string {
		page := dashDetailOf(132, 33)
		page.Style, page.Zoom = style, zoom
		if zoom {
			page.Selected, page.Cursor = selected, int(s.Window/s.Step)
		}
		return dashText(t, s, page)
	}
	grid := dash(chart.Auto, false, 0)
	require.True(t, hasBlocks(grid))
	require.True(t, hasSteps(grid))
	require.False(t, hasBraille(grid))
	require.Contains(t, grid, "Shift-D Grafana · a calm layout · b dots · Esc back")
	require.True(t, hasBraille(dash(chart.Dots, false, 0)))
	require.False(t, hasBlocks(dash(chart.Dots, false, 0)))
	stat := dash(chart.Auto, true, 2)
	require.Contains(t, stat, "Panel 2 of 11 · stat · Slowest pod p99 · seconds")
	require.True(t, hasBlocks(stat))
	require.False(t, hasSteps(stat), "one series: an area")
	lines := dash(chart.Auto, true, 5)
	require.Contains(t, lines, "Panel 5 of 11 · timeseries · Request duration p99 by pod · seconds")
	require.True(t, hasSteps(lines))
	require.False(t, hasBlocks(lines), "several series: lines, no area")
	require.Contains(t, lines, "b dots · Esc grid")
}

func TestChartStyleKeyRedrawsTheViewAndSaysWhatItDid(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	// The trends are on the page of all detail; the calm page draws when a
	// rule held, which has one form.
	slowAllDetail(t, v)
	action, ok := v.actions.Get(chartStyleKey)
	require.True(t, ok)
	frame := drawnText(t, v, 132, 36)
	require.True(t, hasBlocks(frame))
	require.Contains(t, frame, "▁▃█▁▁▁██▁▁▁▁▇▄", "the trend of a hop is eighth blocks")
	require.Contains(t, frame, "r re-read · b dots · Esc back")

	action.Action(nil)
	frame = drawnText(t, v, 132, 36)
	require.True(t, hasBraille(frame), "the trends follow the style")
	require.Contains(t, frame, "r re-read · b solid · Esc back")
	action.Action(nil)
	require.Contains(t, drawnText(t, v, 132, 36), "▁▃█▁▁▁██▁▁▁▁▇▄")

	// With noIcons the trends are plain characters in either style. The
	// key still answers, in words (a flash): a key that seems dead is a
	// broken key.
	v.app.Config.K9s.UI.NoIcons = true
	v.render()
	before := drawnText(t, v, 132, 36)
	require.Contains(t, before, "_.^___^^____^-")
	action.Action(nil)
	require.Equal(t, chart.Dots, v.app.chartStyle())
	after := drawnText(t, v, 132, 36)
	require.Equal(t, strings.ReplaceAll(before, "b dots", "b solid"), after)
	require.False(t, hasBraille(after))
	require.False(t, hasBlocks(after))
}

func TestChartStyleHintTakesRoomOnlyWhereThereIsSome(t *testing.T) {
	forms := []string{
		"←/→ cursor · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Esc back",
		"←/→ cursor · 1-4 window · r re-read · Esc",
		"1-4 window · r read",
	}
	for _, tc := range []struct {
		width int
		style chart.Style
		want  string
	}{
		{120, chart.Auto, "←/→ cursor · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · b dots · Esc back"},
		{120, chart.Dots, "←/→ cursor · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · b solid · Esc back"},
		// No room for the hint: the same wording without it, before any
		// shorter wording.
		{76, chart.Auto, "←/→ cursor · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Esc back"},
		{60, chart.Auto, "←/→ cursor · 1-4 window · r re-read · b dots · Esc"},
		{45, chart.Auto, "←/→ cursor · 1-4 window · r re-read · Esc"},
		// A form without a way out names the key last.
		{30, chart.Auto, "1-4 window · r read · b dots"},
		{20, chart.Auto, "1-4 window · r read"},
		{10, chart.Auto, "Esc"},
	} {
		got := chartStyleFit(forms, tc.style, tc.width, false, "Esc")
		require.Equal(t, tc.want, got, "width %d", tc.width)
		require.LessOrEqual(t, runewidth.StringWidth(got), tc.width)
	}
	require.Equal(t, "Left/Right cursor · 1-4 window · r re-read · b dots · Esc", chartStyleFit(forms, chart.Auto, 60, true, "Esc"), "the plain words are measured as drawn")

	// Every key line of the chart views fits its width at every width, in
	// both styles.
	for width := 20; width <= 140; width++ {
		for _, style := range []chart.Style{chart.Auto, chart.Dots} {
			for _, ascii := range []bool{false, true} {
				for name, line := range map[string]string{
					"graph":       graphFooter(width, true, ascii, false, style),
					"graph pods":  graphFooter(width, true, ascii, true, style),
					"compare":     graphCompareFooter(width, ascii, style),
					"dash":        dashFooter(width, false, false, false, ascii, style),
					"dash zoom":   dashFooter(width, true, false, false, ascii, style),
					"query":       dashFooter(width, true, true, true, ascii, style),
					"graph words": graphFooter(width, false, ascii, false, style),
				} {
					if width >= 40 {
						require.LessOrEqual(t, tview.TaggedStringWidth(tview.Escape(line)), width, "%s at %d: %q", name, width, line)
					}
					if name == "graph words" {
						require.NotContains(t, line, chartStyleHint(style), "a page without a chart does not offer a chart style")
					}
				}
			}
		}
	}
}
