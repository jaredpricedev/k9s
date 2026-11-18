// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

// graphLimitStory is the crash loop seen from one Pod, sampled so that the
// limit is caught: every two minutes the working set climbs to 254Mi of a
// 256Mi limit, holds it for two samples, and a container restarts with the
// next sample. Six times in the twelve minutes the Pod has existed.
func graphLimitStory() *graphFake {
	end := graphNow.Truncate(30 * time.Second)
	created := end.Add(-12 * time.Minute)
	story := graphStory()
	for i := range story.rules {
		switch rule := &story.rules[i]; rule.contains {
		case "container_memory_working_set_bytes":
			rule.value = func(_ string, at time.Time) (float64, bool) {
				switch at.Sub(created) % (2 * time.Minute) / (30 * time.Second) {
				case 0:
					return 150 * graphMi, !at.Before(created)
				case 1:
					return 205 * graphMi, !at.Before(created)
				}
				return 254 * graphMi, !at.Before(created)
			}
		case "kube_pod_container_status_restarts_total":
			rule.value = func(_ string, at time.Time) (float64, bool) {
				return float64(int(at.Sub(created) / (2 * time.Minute))), !at.Before(created)
			}
		}
	}
	return story
}

func calmGraphPage(width, height int) *graphPage {
	palette := config.DefaultSemanticPalette()
	return &graphPage{Width: width, Height: height, Colors: graphPalette(palette), Palette: calmPalette(palette),
		Offers: graphOffers{Timeline: true, Grafana: true, Probe: true}}
}

// calmLines are the lines of a plain page, without the trailing blanks.
func calmLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

// calmLine is the first line of a plain page that holds the words.
func calmLine(t *testing.T, text, words string) string {
	t.Helper()
	for _, line := range calmLines(text) {
		if strings.Contains(line, words) {
			return line
		}
	}
	require.Failf(t, "no such line", "%q is on no line of:\n%s", words, text)
	return ""
}

func TestGraphCalmPageAnswersFirstAndDrawsOneChartOverItsStrips(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphLimitStory(), graphPod, time.Hour, graphNow)
	snapshot.Marks = []graphMark{
		{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2"},
		{Key: 'X', T: snapshot.End.Add(-2 * time.Minute), Label: "last exit OOMKilled (137)"},
	}
	pg := calmGraphPage(132, 33)
	tagged := renderGraphCalm(snapshot, pg)
	page := graphPlainLines(t, tagged, 132)
	lines := calmLines(page)
	require.LessOrEqual(t, len(lines), 33)
	require.Empty(t, pg.Dropped, "132x36 gives nothing up")

	// The title, then the answer in a sentence with its caveat; everything
	// under it is evidence.
	require.Regexp(t, `^Graph  Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h +read 05:00 · 10 of 10 queries$`, lines[0])
	require.Len(t, []rune(lines[0]), 130, "the read status ends two cells before the edge")
	require.Empty(t, lines[1])
	require.Regexp(t, `^  Memory reached its 256Mi limit 6 times\. A container restarted after each\. +Order in time, not proof of cause\.$`, lines[2])
	require.Len(t, []rune(lines[2]), 130)

	// One chart at full size, chosen by the rule, with its legend on one
	// line: the value now, and the levels drawn.
	require.Equal(t, "memory", pg.Main)
	require.Contains(t, page, "  MEMORY  150Mi now  ─ limit 256Mi  ┄ request 128Mi")
	require.Equal(t, 1, strings.Count(page, "256Mi ┤"), "one chart has a Y axis")
	require.NotContains(t, page, "CPU cores")
	require.NotContains(t, page, "NETWORK")

	// Every other signal is one row on the axis of the chart: its name,
	// its strip, the one number. Restarts are marks on that axis.
	require.Contains(t, page, "\n  ALSO")
	restarts := calmLine(t, page, "  restarts  ")
	require.Regexp(t, `^  restarts  [×·▲┊]+  6$`, restarts)
	require.Equal(t, 6, strings.Count(restarts, "▲"))
	require.True(t, strings.HasPrefix(strings.TrimPrefix(restarts, "  restarts  "), "×"), "before the Pod existed nothing is known, and that is not quiet")
	require.Regexp(t, `^  CPU       ×+[▁-█]+  340m$`, calmLine(t, page, "  CPU   "))
	require.Regexp(t, `^  network   ×+[▁-█]+  9kB/s  tx$`, calmLine(t, page, "  network "))
	axis := calmLine(t, page, "┬")
	require.Equal(t, strings.Index(restarts, "×"), strings.IndexAny(axis, "─┬"), "the strips start where the axis does")
	plot := calmLine(t, page, "256Mi ┤")
	require.Equal(t, len([]rune(plot[:strings.Index(plot, "┤")]))+1, len([]rune(axis[:strings.IndexAny(axis, "─┬")])), "and where the plot of the chart does")

	// The moments read from the object are guides through the block, with
	// their letters on the axis and one line each. The first restart is
	// the first mark of the restarts row and has no letter of its own.
	require.Contains(t, axis, "R")
	require.Contains(t, axis, "X")
	require.Contains(t, page, "\n  R rollout rev 2 at 04:48\n  X last exit OOMKilled (137) at 04:58\n")
	require.NotContains(t, page, "first restart")
	require.GreaterOrEqual(t, strings.Count(page, "┊"), 2)

	// What matched nothing is named and is not a zero; what was refused
	// would be NOT READ.
	require.Contains(t, page, "\n  NO DATA  CPU limit · CPU throttled\n")
	require.NotContains(t, page, graphNotReadLabel)

	// Two lines of keys: the likely next steps in words, and the rest.
	require.Equal(t, "  next  n next chart (CPU)    a all charts    t what changed    D open in Grafana", lines[len(lines)-3])
	require.Equal(t, "  ←→ time · 1-4 window · s queries · b dots · r re-read · Esc back", lines[len(lines)-1])

	// The colours are the semantic palette's, and every tag names its
	// fields: no colour of the page's own.
	allowed := map[string]bool{}
	for _, color := range append(append([]string(nil), pg.Colors.Data...), pg.Colors.Ceiling, pg.Colors.Request, pg.Colors.Axis, pg.Colors.Cursor, pg.Colors.AtLimit,
		pg.Palette.Text, pg.Palette.Muted, pg.Palette.Accent, pg.Palette.Warn, pg.Palette.Fail, pg.Palette.Info, pg.Palette.Mark, pg.Palette.Band) {
		allowed[color] = true
	}
	for _, color := range regexp.MustCompile(`#[0-9a-fA-F]{6}`).FindAllString(strings.Join(tagged, "\n"), -1) {
		require.True(t, allowed[color], "%s is not a colour of the semantic palette", color)
	}
	require.NotRegexp(t, `\[#[0-9a-fA-F]{6}\]`, strings.Join(tagged, "\n"), "a colour tag has both colons")
}

func TestGraphCalmPageSaysWhatASmallerScreenGivesUp(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphLimitStory(), graphPod, time.Hour, graphNow)
	snapshot.Marks = []graphMark{
		{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2"},
		{Key: 'X', T: snapshot.End.Add(-2 * time.Minute), Label: "last exit OOMKilled (137)"},
	}
	render := func(width, height int) (string, *graphPage) {
		pg := calmGraphPage(width, height)
		page := graphPlainLines(t, renderGraphCalm(snapshot, pg), width)
		require.LessOrEqual(t, len(calmLines(page)), height, "%dx%d", width, height)
		return page, pg
	}

	// 80x24 leaves 20 rows: the strips shorten and the blank rows go, and
	// the chart gives rows up before the rows under it fold. Nothing is
	// left out.
	page, pg := render(80, 20)
	require.Empty(t, pg.Missing)
	require.NotContains(t, strings.Join(pg.Dropped, "; "), "folded")
	require.Contains(t, page, "Graph  Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h")
	require.Contains(t, page, "05:00 · 10/10")
	require.Contains(t, page, "  Memory reached its 256Mi limit 6 times. A container restarted after each.\n  Order in time, not proof of cause.")
	require.Equal(t, 6, strings.Count(calmLine(t, page, "  restarts  "), "▲"))
	require.Contains(t, page, "256Mi ┤")
	require.Contains(t, page, "  next  n next chart (CPU)  a all charts  t what changed  D open in Grafana")

	// 60 columns: the rows under the chart fold to one line that keeps
	// every number, and the page says so in what it gave up.
	page, pg = render(60, 20)
	require.Empty(t, pg.Missing)
	require.Contains(t, pg.Dropped, "ALSO folded to one line")
	require.Contains(t, page, "\n  ALSO  restarts 6 · CPU 340m · network 9kB/s tx\n")
	require.Contains(t, page, "Graph  Pod apps/check…f8c6b5d-x2x9q · last 1h", "a Pod's name is shortened in the middle: its end tells it from its siblings")
	require.Contains(t, page, "256Mi ┤")
	require.Contains(t, page, "  NO DATA  CPU limit · CPU throttled")
	require.Contains(t, page, "  R rollout rev 2 at 04:48")

	// A short screen ends as the chart alone on its axis, and names what
	// is not shown with the key that shows it.
	page, pg = render(60, 12)
	require.Contains(t, page, "256Mi ┤")
	require.Contains(t, page, "04:30")
	require.NotContains(t, page, "\n  ALSO", "the rows under the chart are gone")
	require.Equal(t, []string{graphMomentsLabel, graphAlsoLabel}, pg.Missing, "and the guides of the moments with them")
	require.Contains(t, page, "not shown at this size (a shows all): MOMENTS, ALSO")
	require.Contains(t, page, "NO DATA", "what gave no reading is the last line a page gives up")

	// No size draws anything wider than itself or taller than it has.
	for _, size := range [][2]int{{200, 60}, {132, 33}, {100, 24}, {80, 20}, {72, 20}, {60, 20}, {50, 16}, {40, 12}, {30, 8}} {
		render(size[0], size[1])
	}
}

func TestGraphCalmPagePromotesTheNextChartAndReadsTheCursor(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphLimitStory(), graphPod, time.Hour, graphNow)

	// n makes the next strip the chart at full size; the chart it replaces
	// becomes a strip, and the answer at the top stays the one of the read.
	pg := calmGraphPage(132, 33)
	pg.Main = "cpu"
	page := graphPlainLines(t, renderGraphCalm(snapshot, pg), 132)
	require.Equal(t, "cpu", pg.Main)
	require.Contains(t, page, "  CPU CORES  340m now  ─ limit no data  ┄ request 100m  throttled no data")
	require.Contains(t, page, "  Memory reached its 256Mi limit 6 times. A container restarted after each.")
	require.Regexp(t, `^  memory    ×+[▁-█]+  150Mi$`, calmLine(t, page, "  memory  "))
	require.NotContains(t, page, "\n  CPU   ")
	require.Contains(t, page, "  next  n next chart (network)")
	// A chart that does not exist changes nothing: the rule chooses.
	pg = calmGraphPage(132, 33)
	pg.Main = "disk"
	renderGraphCalm(snapshot, pg)
	require.Equal(t, "memory", pg.Main)

	// A cursor that was moved is drawn, and every number is the sample
	// under it: no interpolation, and the page says which time it reads.
	pg = calmGraphPage(132, 33)
	pg.Moved, pg.Cursor = true, int(snapshot.Window/snapshot.Step)-2
	page = graphPlainLines(t, renderGraphCalm(snapshot, pg), 132)
	require.Contains(t, page, "  MEMORY  254Mi at 04:59:00  at limit  ─ limit 256Mi  ┄ request 128Mi")
	require.Equal(t, 1, strings.Count(page, "04:59:00"), "the time the cursor reads is said once, over the chart")
	require.Contains(t, calmLine(t, page, "┬"), "^")
	require.Contains(t, page, "┆")
	// At the window end there is no cursor to draw.
	page = graphPlainLines(t, renderGraphCalm(snapshot, calmGraphPage(132, 33)), 132)
	require.NotContains(t, page, "┆")
	require.NotContains(t, calmLine(t, page, "┬"), "^")
}

// calmAxisSpan is where the time axis of a plain page starts and how wide
// it is, in cells: every strip of the page stands on it.
func calmAxisSpan(t *testing.T, page string) (start, width int) {
	t.Helper()
	for _, line := range calmLines(page) {
		rule := strings.TrimLeft(line, " ")
		if strings.ContainsAny(rule, "┬┯+") && strings.Trim(rule, "─┬━┯-+=#^RXF") == "" {
			return len([]rune(line)) - len([]rune(rule)), len([]rune(rule))
		}
	}
	require.Failf(t, "no axis", "no time axis on:\n%s", page)
	return 0, 0
}

// The numbers of the rows are read under the cursor, and their widths
// differ from sample to sample (9kB/s, 2.6kB/s; a name, a name and "at
// limit"). The columns are held as wide as the widest of them, so that
// moving the cursor moves the cursor and nothing else: not a strip, not a
// mark, not the axis.
func TestGraphCalmCursorMovesNothingButItself(t *testing.T) {
	pod := loadGraph(t.Context(), graphLimitStory(), graphPod, time.Hour, graphNow)
	pod.Marks = []graphMark{{Key: 'R', T: pod.End.Add(-12 * time.Minute), Label: "rollout rev 2"}}
	// Two Pods, one of which is at its limit every fourth minute: the row
	// of memory then names another Pod and adds "at min limit".
	pods := []string{"checkout-api-6c8d7f9b5-k4m2t", "checkout-api-7d9f8c6b5d-x2x9q"}
	workload := loadGraph(t.Context(), &graphFake{pods: pods, rules: []graphRule{
		{contains: "container_memory_working_set_bytes", by: pods, value: func(pod string, at time.Time) (float64, bool) {
			if strings.HasSuffix(pod, "x2x9q") {
				return map[bool]float64{true: 250, false: 9.5}[at.Minute()%4 == 0] * graphMi, true
			}
			return 100 * graphMi, true
		}},
		{contains: "kube_pod_container_resource_limits", value: graphConst(256 * graphMi)},
		{contains: "container_cpu_usage_seconds_total", by: pods, value: func(string, time.Time) (float64, bool) { return 0.34, true }},
		{contains: "kube_pod_container_status_restarts_total", by: pods, value: func(string, time.Time) (float64, bool) { return 0, true }},
	}}, &graphTarget{Context: "ct-1-1", Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}, time.Hour, graphNow)
	require.Nil(t, workload.failure())
	for name, snapshot := range map[string]*graphSnapshot{"pod": pod, "workload": workload} {
		steps := int(snapshot.Window / snapshot.Step)
		for _, size := range [][2]int{{132, 33}, {80, 20}} {
			for _, main := range []string{"", "cpu", "network"} {
				still := calmGraphPage(size[0], size[1])
				still.Main, still.ASCII = main, name == "workload"
				page := graphPlainLines(t, renderGraphCalm(snapshot, still), size[0])
				require.Empty(t, still.Missing, "%s %dx%d main %q", name, size[0], size[1], main)
				start, width := calmAxisSpan(t, page)
				var marks, rule []int
				if name == "pod" {
					marks = calmColumns(calmLine(t, page, "  restarts  "), "▲")
					rule = calmColumns(calmLine(t, page, "┬"), "R")
					require.Len(t, marks, 6)
					require.Len(t, rule, 1)
				}
				for _, cursor := range []int{0, 1, steps / 3, steps / 2, steps - 30, steps - 3, steps - 1, steps} {
					moved := calmGraphPage(size[0], size[1])
					moved.Main, moved.ASCII, moved.Moved, moved.Cursor = main, still.ASCII, true, cursor
					page := graphPlainLines(t, renderGraphCalm(snapshot, moved), size[0])
					gotStart, gotWidth := calmAxisSpan(t, page)
					require.Equal(t, [2]int{start, width}, [2]int{gotStart, gotWidth}, "%s %dx%d main %q cursor %d: the axis\n%s", name, size[0], size[1], main, cursor, page)
					if name == "pod" {
						require.Equal(t, marks, calmColumns(calmLine(t, page, "  restarts  "), "▲"), "%s cursor %d: the restart marks", name, cursor)
						require.Equal(t, rule, calmColumns(calmLine(t, page, "┬"), "R"), "%s cursor %d: the rollout", name, cursor)
					}
				}
			}
		}
	}
}

// The legend over the chart is not part of the plot: a guide starts under
// it, and never floats in the blank end of that line. The letter of a
// line that is not filled is in the plot, so the legend names it.
func TestGraphCalmLegendRowCarriesNoGuideAndNamesTheLetterOfALine(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphLimitStory(), graphPod, time.Hour, graphNow)
	snapshot.Marks = []graphMark{
		{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2"},
		{Key: 'X', T: snapshot.End.Add(-2 * time.Minute), Label: "last exit OOMKilled (137)"},
	}
	pg := calmGraphPage(132, 33)
	pg.Moved, pg.Cursor = true, int(snapshot.Window/snapshot.Step)-20
	page := graphPlainLines(t, renderGraphCalm(snapshot, pg), 132)
	require.Equal(t, "  MEMORY  150Mi at 04:50:00  ─ limit 256Mi  ┄ request 128Mi", calmLine(t, page, "  MEMORY"))
	require.Contains(t, calmLine(t, page, "  ALSO"), "┊", "the guides run through the rows under the plot")
	require.Contains(t, calmLine(t, page, "  ALSO"), "┆", "and so does the cursor")

	// Dots and noIcons draw the line with its letter at its end.
	for axis, pg := range map[string]*graphPage{
		"256Mi ┤": func() *graphPage { pg := calmGraphPage(132, 33); pg.Style = chart.Dots; return pg }(),
		"256Mi +": func() *graphPage { pg := calmGraphPage(132, 33); pg.ASCII = true; return pg }(),
	} {
		page := graphPlainLines(t, renderGraphCalm(snapshot, pg), 132)
		require.Regexp(t, `^  MEMORY  a 150Mi now  `, calmLine(t, page, "  MEMORY"))
		require.Equal(t, 1, strings.Count(page[strings.Index(page, axis):strings.Index(page, "ALSO")], "a"), "the letter the legend names is in the plot:\n%s", page)
	}
	// A filled area needs no letter.
	require.Regexp(t, `^  MEMORY  150Mi now  `, calmLine(t, graphPlainLines(t, renderGraphCalm(snapshot, calmGraphPage(132, 33)), 132), "  MEMORY"))
}

func TestGraphCalmNoIconsPageIsASCII(t *testing.T) {
	snapshot := loadGraph(t.Context(), graphLimitStory(), graphPod, time.Hour, graphNow)
	snapshot.Marks = []graphMark{{Key: 'R', T: snapshot.End.Add(-12 * time.Minute), Label: "rollout rev 2"}}
	for _, size := range [][2]int{{132, 33}, {80, 20}, {60, 20}} {
		pg := calmGraphPage(size[0], size[1])
		pg.ASCII, pg.Moved, pg.Cursor = true, true, 100
		page := graphPlainLines(t, renderGraphCalm(snapshot, pg), size[0])
		for _, r := range page {
			require.Less(t, r, rune(128), "%q is not ASCII at %dx%d:\n%s", r, size[0], size[1], page)
		}
		require.Contains(t, page, "Memory reached its 256Mi limit 6 times.")
		require.Contains(t, page, "= limit 256Mi")
		require.Contains(t, page, "R rollout rev 2 at 04:48")
		require.Contains(t, page, "Left/Right time")
	}
	page := graphPlainLines(t, renderGraphCalm(snapshot, func() *graphPage { pg := calmGraphPage(132, 33); pg.ASCII = true; return pg }()), 132)
	require.Regexp(t, `^  restarts  x+[.^|]+  6$`, calmLine(t, page, "  restarts  "), "a restart is ^, a time without a sample x, a quiet one a dot")
	require.Contains(t, calmLine(t, page, "+-"), "R")
}

func TestGraphMainChartIsChosenByRule(t *testing.T) {
	at := func(minutes int) time.Time { return graphNow.Add(time.Duration(minutes) * time.Minute) }
	level := func(v float64) []chart.Point {
		return []chart.Point{{T: at(0), V: v}, {T: at(1), V: v}, {T: at(2), V: v}}
	}
	panel := func(id string, usage, limit float64, more ...chart.Series) graphPanel {
		p := graphPanel{Spec: &graphPanelSpec{ID: id, Title: strings.ToUpper(id)}, Chart: chart.Panel{Series: []chart.Series{
			{Name: "usage", Points: level(usage)}, {Name: "limit", Kind: chart.Reference, Limit: true, Points: level(limit)},
		}}, Specs: []*graphSeriesSpec{{ID: "usage"}, {ID: "limit", Kind: chart.Reference}}, States: []string{"", ""}}
		for i := range more {
			p.Chart.Series = append(p.Chart.Series, more[i])
			p.Specs = append(p.Specs, &graphSeriesSpec{ID: more[i].Name, Kind: more[i].Kind})
			p.States = append(p.States, "")
		}
		return p
	}
	throttled := func(share float64) chart.Series {
		return chart.Series{Name: "throttled", Kind: chart.OffAxis, Points: level(share)}
	}
	oom := []graphMark{{Key: 'X', T: at(1), Label: "last exit OOMKilled (137)"}}
	for _, tc := range []struct {
		name   string
		panels []graphPanel
		marks  []graphMark
		want   string
		why    string
	}{
		{"memory at its limit", []graphPanel{panel("memory", 250, 256), panel("cpu", 1, 1)}, nil, "memory", "memory was at its limit"},
		{"the last exit was OOMKilled", []graphPanel{panel("memory", 60, 256), panel("cpu", 0.2, 1)}, oom, "memory", "OOMKilled"},
		{"another exit is not memory's", []graphPanel{panel("memory", 60, 256), panel("cpu", 0.99, 1)}, []graphMark{{Key: 'X', T: at(1), Label: "last exit Error (1)"}}, "cpu", "CPU was at its limit"},
		{"CPU throttled", []graphPanel{panel("memory", 60, 256), panel("cpu", 0.2, 1, throttled(0.4))}, nil, "cpu", "throttled in 40% of its periods"},
		{"throttling under the floor does not count", []graphPanel{panel("memory", 60, 256), panel("cpu", 0.2, 1, throttled(0.1))}, nil, "memory", "nothing was at a limit"},
		{"CPU at its limit", []graphPanel{panel("memory", 60, 256), panel("cpu", 0.96, 1)}, nil, "cpu", "CPU was at its limit"},
		{"a Node's disk at its size", []graphPanel{panel("cpu", 1, 8), panel("memory", 10, 64), panel("disk", 99, 100), panel("pods", 20, 110)}, nil, "disk", "disk / was at its limit"},
		{"nothing at a limit: memory", []graphPanel{panel("cpu", 1, 8), panel("memory", 10, 64), panel("disk", 9, 100)}, nil, "memory", "nothing was at a limit"},
		{"no memory chart: the first", []graphPanel{panel("cpu", 1, 8), panel("disk", 9, 100)}, nil, "cpu", "the first chart"},
	} {
		index, why := graphMainPanel(tc.panels, tc.marks, time.Minute)
		require.Equal(t, tc.want, tc.panels[index].Spec.ID, tc.name)
		require.Contains(t, why, tc.why, tc.name)
	}

	// A limit is reached once per stretch of neighbouring samples near it,
	// and only against the limit read at that time.
	line := []chart.Point{{T: at(0), V: 100}, {T: at(1), V: 250}, {T: at(2), V: 254}, {T: at(3), V: 120}, {T: at(4), V: 255}, {T: at(9), V: 255}}
	limit := []chart.Point{{T: at(0), V: 256}, {T: at(1), V: 256}, {T: at(2), V: 256}, {T: at(3), V: 256}, {T: at(4), V: 256}, {T: at(9), V: 256}}
	require.Equal(t, []chart.Interval{{Start: at(1), End: at(2)}, {Start: at(4), End: at(4)}, {Start: at(9), End: at(9)}}, graphReached(line, limit, time.Minute),
		"a hole between two samples ends a stretch")
	require.Empty(t, graphReached(line, nil, time.Minute), "without a limit nothing is at it")
	require.Empty(t, graphReached(line, []chart.Point{{T: at(20), V: 256}}, time.Minute), "a limit read later says nothing about earlier samples")
}

func TestGraphCalmVerdictStatesOnlyWhatTheSamplesShow(t *testing.T) {
	verdict := func(fake *graphFake, target *graphTarget, marks ...graphMark) calm.Verdict {
		t.Helper()
		snapshot := loadGraph(t.Context(), fake, target, time.Hour, graphNow)
		snapshot.Marks = marks
		panels := snapshot.panels(graphColors{})
		chosen, _ := graphMainPanel(panels, marks, snapshot.Step)
		restarts := snapshot.restarts()
		return graphCalmVerdict(snapshot, &panels[chosen], &restarts)
	}
	end := graphNow.Truncate(30 * time.Second)

	// At the limit, and a restart after each time: order, not cause.
	v := verdict(graphLimitStory(), graphPod)
	require.Equal(t, []string{"Memory reached its 256Mi limit 6 times.", "A container restarted after each."}, v.Sentences)
	require.Equal(t, "Order in time, not proof of cause.", v.Caveat)
	require.Equal(t, calm.Found, v.Kind)

	// The same memory with a counter that never rose: no restart is
	// claimed, and the caveat no longer speaks of order.
	quiet := graphLimitStory()
	for i := range quiet.rules {
		if quiet.rules[i].contains == "kube_pod_container_status_restarts_total" {
			quiet.rules[i].value = graphConst(2)
		}
	}
	v = verdict(quiet, graphPod)
	require.Equal(t, []string{"Memory reached its 256Mi limit 6 times.", "No restart was recorded in the last 1h."}, v.Sentences)
	require.Equal(t, "A reading, not proof of cause.", v.Caveat)

	// Restarts that do not follow the limit are counted, not attributed.
	late := graphLimitStory()
	for i := range late.rules {
		if late.rules[i].contains == "kube_pod_container_status_restarts_total" {
			late.rules[i].value = func(_ string, at time.Time) (float64, bool) {
				if at.Before(end.Add(-11*time.Minute - 30*time.Second)) {
					return 0, !at.Before(end.Add(-12 * time.Minute))
				}
				return 3, true
			}
		}
	}
	v = verdict(late, graphPod)
	require.Equal(t, []string{"Memory reached its 256Mi limit 6 times.", "3 restarts in the last 1h, none right after."}, v.Sentences)

	// Nothing at a limit: the latest value against its limit, and whether
	// the counter rose. It is a reading, not health.
	v = verdict(graphStory(), graphPod)
	require.Equal(t, []string{"Memory is 60Mi now, 23.4% of its 256Mi limit.", "6 restarts in the last 1h."}, v.Sentences)
	require.Equal(t, "A reading, not a statement of health.", v.Caveat)
	// An exit the object records as OOMKilled is said with it.
	v = verdict(graphStory(), graphPod, graphMark{Key: 'X', T: end.Add(-2 * time.Minute), Label: "last exit OOMKilled (137)"})
	require.Equal(t, []string{"Memory is 60Mi now, 23.4% of its 256Mi limit.", "The last exit was OOMKilled, at 04:58."}, v.Sentences)

	// No limit read: the page does not invent one.
	bare := &graphFake{rules: []graphRule{{contains: "container_memory_working_set_bytes", value: graphConst(60 * graphMi)}}}
	v = verdict(bare, graphPod)
	require.Equal(t, []string{"Memory is 60Mi now; no memory limit was read."}, v.Sentences)

	// CPU that was throttled: the share, and the peak against the limit.
	throttled := &graphFake{rules: []graphRule{
		{contains: "container_memory_working_set_bytes", value: graphConst(60 * graphMi)},
		{contains: "container_cpu_cfs", value: graphConst(0.88)},
		{contains: "container_cpu_usage_seconds_total", value: graphConst(1.5)},
		{contains: `resource="cpu"`, value: graphConst(1.5)},
	}}
	v = verdict(throttled, graphPod)
	require.Equal(t, []string{"CPU was throttled in up to 88% of its periods.", "Usage peaked at 1.5 cores against a 1.5 cores limit."}, v.Sentences)
	require.Equal(t, "A reading, not proof of cause.", v.Caveat)

	// No sample is missing data, said as such and never as a level.
	v = verdict(&graphFake{}, graphPod)
	require.Equal(t, calm.NotRead, v.Kind)
	require.Equal(t, []string{"Memory has no samples in the last 1h.", "The query matched nothing: that is missing data, not a zero."}, v.Sentences)
	require.Equal(t, "Not read is not healthy.", v.Caveat)

	// Several Pods: the line that reached the limit is named, and the
	// restarts of the workload are counted, not given to a Pod.
	dp := &graphTarget{Context: "ct-1-1", Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}
	pods := []string{"checkout-api-6c8d7f9b5-k4m2t", "checkout-api-7d9f8c6b5d-x2x9q"}
	workload := &graphFake{pods: pods, rules: []graphRule{
		{contains: "container_memory_working_set_bytes", by: pods, value: func(pod string, at time.Time) (float64, bool) {
			if strings.HasSuffix(pod, "x2x9q") && at.Minute()%4 == 0 {
				return 250 * graphMi, true
			}
			return 100 * graphMi, true
		}},
		{contains: "kube_pod_container_resource_limits", value: graphConst(256 * graphMi)},
		{contains: "kube_pod_container_status_restarts_total", by: pods, value: func(_ string, at time.Time) (float64, bool) {
			return float64(at.Sub(end.Add(-time.Hour)) / (20 * time.Minute)), true
		}},
	}}
	v = verdict(workload, dp)
	require.Equal(t, "Memory of x2x9q reached the 256Mi min limit 16 times.", v.Sentences[0])
	require.Equal(t, "6 restarts in the last 1h.", v.Sentences[1])
	require.Equal(t, "A reading, not proof of cause.", v.Caveat)
}

func TestGraphCalmPageNeverReadsMissingDataAsHealth(t *testing.T) {
	// Answered, and nothing matched: the answer says so in the warning
	// form, every strip is the gap glyph, and nothing is plotted.
	empty := loadGraph(t.Context(), &graphFake{}, graphPod, 15*time.Minute, graphNow)
	page := graphPlainLines(t, renderGraphCalm(empty, calmGraphPage(100, 30)), 100)
	require.Contains(t, page, "\n  Memory has no samples in the last 15m.\n  The query matched nothing: that is missing data, not a zero.")
	require.Contains(t, page, "Not read is not healthy.")
	require.Contains(t, page, "no samples in this window (not a zero)")
	require.Regexp(t, `^  restarts  ×+  no data$`, calmLine(t, page, "  restarts  "))
	require.Regexp(t, `^  CPU       ×+  no data$`, calmLine(t, page, "  CPU   "))
	require.Contains(t, page, "  NO DATA  memory · memory limit")
	for _, r := range page {
		require.False(t, r >= 0x2581 && r <= 0x2588, "nothing is drawn without samples")
	}

	// A refused read is NOT READ, with its state, and is counted in the
	// title in the warning colour.
	partial := loadGraph(t.Context(), graphStory(), graphPod, time.Hour, graphNow)
	denied := partial.Results["network/rx"]
	denied.Status, denied.Series = observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}, nil
	partial.Results["network/rx"] = denied
	pg := calmGraphPage(132, 33)
	tagged := renderGraphCalm(partial, pg)
	page = graphPlainLines(t, tagged, 132)
	require.Contains(t, page, "read 05:00 · 9 of 10 queries")
	require.Contains(t, tagged[0], "["+pg.Palette.Warn+":-:-]read 05:00 · 9 of 10 queries")
	require.Contains(t, page, "\n  NOT READ  network rx (denied)\n")
	require.Regexp(t, `  9kB/s  tx$`, calmLine(t, page, "  network "), "the line that answered is still drawn")

	// Nothing answered: the reason is the answer, there is no chart, and
	// the keys read again or show what was asked.
	st := observability.Status{State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: graphNow}
	refused := loadGraph(t.Context(), &graphFake{status: &st}, graphPod, time.Hour, graphNow)
	for _, width := range []int{132, 80, 60} {
		page = graphPlainLines(t, renderGraphCalm(refused, calmGraphPage(width, 20)), width)
		flat := strings.Join(strings.Fields(page), " ")
		for _, want := range []string{"0/10", "Prometheus DENIED all 10 queries (HTTP 403) at 05:00:07Z.", "a denied read is not a reading of zero",
			":sources", "Not read is not healthy.", "next r read again s what was asked"} {
			if want == "0/10" && width == 132 {
				want = "0 of 10 queries"
			}
			require.Contains(t, flat, want, "width %d", width)
		}
		require.NotContains(t, page, "┤")
	}
	none := observability.Status{State: observability.StateNotConfigured}
	undeclared := &graphSnapshot{Target: *graphPod, Window: time.Hour, Step: 30 * time.Second, Start: graphNow.Add(-time.Hour), End: graphNow, Status: &none, Source: "no source"}
	page = graphPlainLines(t, renderGraphCalm(undeclared, calmGraphPage(80, 20)), 80)
	require.Contains(t, page, "not read")
	require.Contains(t, page, "No Prometheus source is declared for this context, so nothing was asked.")
}

func TestGraphCalmWorkloadKeepsSolidLinesInItsMainChart(t *testing.T) {
	dp := &graphTarget{Context: "ct-1-1", Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}
	pods := []string{"checkout-api-6c8d7f9b5-k4m2t", "checkout-api-7d9f8c6b5d-p8wm", "checkout-api-7d9f8c6b5d-x2x9q"}
	end := graphNow.Truncate(30 * time.Second)
	fake := &graphFake{pods: pods, rules: []graphRule{
		{contains: "container_memory_working_set_bytes", by: []string{pods[0], pods[2]}, value: func(pod string, at time.Time) (float64, bool) {
			if strings.HasSuffix(pod, "k4m2t") {
				return 185 * graphMi, true
			}
			return 250 * graphMi, !at.Before(end.Add(-12 * time.Minute))
		}},
		{contains: "kube_pod_container_resource_limits", value: func(_ string, at time.Time) (float64, bool) {
			if at.Before(end.Add(-12 * time.Minute)) {
				return 512 * graphMi, true
			}
			return 256 * graphMi, true
		}},
		{contains: "container_cpu_usage_seconds_total", by: pods, value: graphConst(0.1)},
	}}
	snapshot := loadGraph(t.Context(), fake, dp, time.Hour, graphNow)
	snapshot.Marks = []graphMark{{Key: 'R', T: end.Add(-12 * time.Minute), Label: "rollout rev 2"}}
	pg := calmGraphPage(132, 33)
	page := graphPlainLines(t, renderGraphCalm(snapshot, pg), 132)

	require.Contains(t, page, "Graph  Deployment apps/checkout-api · 3 pods · last 1h")
	require.Contains(t, page, "  Memory of x2x9q reached the 256Mi min limit once.")
	// Every line has its letter in the legend and in the plot, and a Pod
	// without samples is named.
	head := calmLine(t, page, "  MEMORY  ")
	require.Contains(t, head, "MEMORY  now  a k4m2t 185Mi  b x2x9q 250Mi at min limit  c p8wm no data  ─ min limit 256Mi")
	plot := page[strings.Index(page, head)+len(head) : strings.Index(page, "  ALSO")]
	require.Contains(t, plot, "a")
	require.Contains(t, plot, "b")
	require.Contains(t, plot, "─", "lines are solid strokes, not dots")
	// The other chart is one row: the highest of its lines, and whose.
	require.Regexp(t, `^  CPU       [▁-█]+  100m +(k4m2t|p8wm|x2x9q)$`, calmLine(t, page, "  CPU   "))
	// The rollout says what the limit was before it: the samples show it.
	require.Contains(t, page, "  R rollout rev 2 at 04:48 · memory min limit 512Mi before it, 256Mi now")
	require.Contains(t, page, "NO DATA  memory p8wm")
	require.Equal(t, "  next  p compare the pods    n next chart (CPU)    a all charts    t what changed    D open in Grafana", calmLine(t, page, "  next  "))
}

func TestGraphViewCalmIsTheDefaultAndKeysStepItWithoutReading(t *testing.T) {
	v := graphViewFixture(t)
	fake := graphLimitStory()
	reads := 0
	v.load = func(ctx context.Context, window time.Duration) *graphSnapshot {
		reads++
		return loadGraph(ctx, fake, &v.target, window, graphNow)
	}
	v.active = true
	v.accept(v.load(t.Context(), v.window))
	press := func(key tcell.Key) {
		t.Helper()
		action, ok := v.actions.Get(key)
		require.True(t, ok, tcell.KeyNames[key])
		action.Action(tcell.NewEventKey(key, 0, tcell.ModNone))
	}
	label := func(key tcell.Key) string {
		action, _ := v.actions.Get(key)
		return action.Description
	}

	// The calm page is what opens.
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, frame, "Memory reached its 256Mi limit 6 times. A container restarted after each.")
	require.Contains(t, frame, "  MEMORY  150Mi now")
	require.Equal(t, "memory", v.shown)
	require.Empty(t, v.dropped)

	// n, Tab and Down make the next chart the main one; Shift-N, Shift-Tab
	// and Up the previous. They wrap around.
	require.Equal(t, calmNextChartLabel, label(calmNextChartKey))
	require.Equal(t, calmNextChartLabel, label(calmNextChartAltKey))
	press(calmNextChartKey)
	require.Contains(t, drawnText(t, v, 132, 33), "  CPU CORES  340m now")
	press(calmNextChartAltKey)
	require.Contains(t, drawnText(t, v, 132, 33), "  NETWORK  now  a rx 2.6kB/s  b tx 9kB/s")
	press(tcell.KeyDown)
	require.Contains(t, drawnText(t, v, 132, 33), "  MEMORY  150Mi now")
	press(ui.KeyShiftN)
	require.Equal(t, "network", v.main)
	press(tcell.KeyBacktab)
	press(tcell.KeyUp)
	require.Contains(t, drawnText(t, v, 132, 33), "  MEMORY  150Mi now")

	// a shows all the detail the page had before, with the way back
	// named; a again returns. Nothing is lost.
	require.Equal(t, calmDetailLabel, label(calmDetailKey))
	press(calmDetailKey)
	detail := drawnText(t, v, 132, 33)
	for _, want := range []string{
		"Graph · Pod apps/checkout-api-7d9f8c6b5d-x2x9q · last 1h to 05:00Z · step 30s  ·  a short page",
		"read 05:00:07Z · 10 of 10 queries answered · 2 no data",
		"MEMORY  █ working set 150Mi", "CPU cores  █ usage 340m", "NETWORK  a rx 2.6kB/s  b tx 9kB/s",
		"F first restart 04:50 · order, not cause", "Cursor 05:00:00Z (window end) · restarts 6, +6 in window",
	} {
		require.Contains(t, detail, want)
	}
	press(calmDetailKey)
	require.Contains(t, drawnText(t, v, 132, 33), "Memory reached its 256Mi limit 6 times.")

	// t opens what changed around the same object.
	require.Equal(t, "Timeline", label(ui.KeyT))
	// One Pod has no peers: Enter and the page keys of the comparison are
	// not bound here.
	_, ok := v.actions.Get(tcell.KeyEnter)
	require.False(t, ok)
	require.Equal(t, 1, reads, "no key of the page reads anything")
}
