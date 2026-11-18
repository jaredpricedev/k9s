// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerdictLines(t *testing.T) {
	stalls := Verdict{
		Sentences: []string{
			"3 stalls: clients gave up waiting at 14:08, 14:27 and 14:53, for 2 to 4 minutes each.",
			"Each time, pod hx4mt was CPU-throttled and had no free threads. Nothing else lines up.",
		},
		Caveat: "Overlap in time, not proof of cause.",
	}
	short := Verdict{Sentences: []string{"Memory reached its 256Mi limit 6 times.", "The container restarted after each."},
		Caveat: "Order in time, not proof of cause."}
	for name, c := range map[string]struct {
		verdict Verdict
		width   int
		opts    Options
		want    []string
	}{
		"a following sentence that does not fit whole starts a line; the caveat is at the right of the last": {stalls, 132, plainOpts, []string{
			"  3 stalls: clients gave up waiting at 14:08, 14:27 and 14:53, for 2 to 4 minutes each.",
			right("  Each time, pod hx4mt was CPU-throttled and had no free threads. Nothing else lines up.", "Overlap in time, not proof of cause.", 130),
		}},
		"a following sentence that fits whole stays on the line": {short, 132, plainOpts, []string{
			right("  Memory reached its 256Mi limit 6 times. The container restarted after each.", "Order in time, not proof of cause.", 130),
		}},
		"the caveat has a line of its own when it does not fit": {short, 80, plainOpts, []string{
			"  Memory reached its 256Mi limit 6 times. The container restarted after each.",
			"  Order in time, not proof of cause.",
		}},
		"lines break between words": {stalls, 60, plainOpts, []string{
			"  3 stalls: clients gave up waiting at 14:08, 14:27 and",
			"  14:53, for 2 to 4 minutes each.",
			"  Each time, pod hx4mt was CPU-throttled and had no free",
			"  threads. Nothing else lines up.",
			"  Overlap in time, not proof of cause.",
		}},
		"width is measured in cells, not in runes": {Verdict{Sentences: []string{"节点 worker-f 内存 不足 压力 持续"}}, 20, plainOpts, []string{
			"  节点 worker-f 内存",
			"  不足 压力 持续",
		}},
		"a word wider than the line is cut, not lost": {Verdict{Sentences: []string{"pod checkout-api-7d9f8c6b5d-x2x9q restarted"}}, 20, plainOpts, []string{
			"  pod",
			"  checkout-api-7d9f8",
			"  c6b5d-x2x9q",
			"  restarted",
		}},
		"plain characters with noIcons": {Verdict{Sentences: []string{"Limit changed 512Mi → 256Mi."}, Caveat: "Order in time…"}, 60, asciiOpts, []string{
			right("  Limit changed 512Mi -> 256Mi.", "Order in time...", 60),
		}},
		"no sentence, no line": {Verdict{}, 80, plainOpts, nil},
	} {
		t.Run(name, func(t *testing.T) {
			lines := VerdictLines(c.verdict, c.width, c.opts)
			var got []string
			for _, line := range lines {
				got = append(got, plain(line))
				assert.LessOrEqual(t, width(line), c.width)
			}
			assert.Equal(t, c.want, got)
		})
	}
}

func TestVerdictIsBoldFirstThenPlainThenDim(t *testing.T) {
	v := Verdict{Sentences: []string{"First.", "Second."}, Caveat: "Caveat."}
	cells := draw(t, VerdictLines(v, 60, colorOpts), 60)
	row := cells[0]
	assert.Equal(t, right("  First. Second.", "Caveat.", 60), text(row))
	first, second, caveat := at(t, row, "First."), at(t, row, "Second."), at(t, row, "Caveat.")
	assert.True(t, row[first].bold, "the first sentence is bold")
	assert.Equal(t, testPalette.Text, row[first].fg)
	assert.False(t, row[second].bold, "the next one is plain")
	assert.False(t, row[second].dim)
	assert.Equal(t, testPalette.Text, row[second].fg)
	assert.True(t, row[caveat].dim, "the caveat is dim")
	assert.Equal(t, testPalette.Muted, row[caveat].fg)

	// Without a palette the attributes still carry the difference.
	cells = draw(t, VerdictLines(v, 60, plainOpts), 60)
	assert.True(t, cells[0][first].bold)
	assert.False(t, cells[0][second].bold)
	assert.True(t, cells[0][caveat].dim)
	assert.Empty(t, cells[0][first].fg)
}

func TestVerdictOfNothingFoundAndOfNotRead(t *testing.T) {
	for name, c := range map[string]struct {
		verdict Verdict
		want    []string
	}{
		"nothing found in a whole read says the read was whole": {Nothing("No stall in the last 1h.", 35, 35), []string{
			right("  No stall in the last 1h. All 35 queries answered.", "Nothing found is not proof of health.", 98),
		}},
		"nothing found in a partial read says what is not known": {Nothing("No stall in the last 1h in what was read.", 33, 35), []string{
			"  No stall in the last 1h in what was read.",
			"  2 of 35 queries were not answered; nothing is known about those.",
			"  Nothing found is not proof of health.",
		}},
		"nothing found without a count adds none": {Nothing("No restart in the last 15m.", 0, 0), []string{
			right("  No restart in the last 15m.", "Nothing found is not proof of health.", 98),
		}},
		"not read names the source and the reason": {Unread("Prometheus was not read.", "The query timed out after 10s."), []string{
			right("  Prometheus was not read. The query timed out after 10s.", "Not read is not healthy.", 98),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, plainAll(VerdictLines(c.verdict, 100, plainOpts)))
		})
	}
	// Not read is drawn in the warning colour; a finding is not.
	cells := draw(t, VerdictLines(Unread("Prometheus was not read.", ""), 80, colorOpts), 80)
	assert.Equal(t, testPalette.Warn, cells[0][2].fg)
	assert.True(t, cells[0][2].bold)
	assert.Equal(t, NothingFound, Nothing("x", 1, 1).Kind)
	assert.Equal(t, NotRead, Unread("x", "").Kind)
}

func TestTitleLine(t *testing.T) {
	title := Title{View: "Slow requests", Object: "Service gitlab-webservice-default", Window: "last 1h", Read: minute(57), Done: 35, Total: 35}
	partial := title
	partial.Done = 33
	for name, c := range map[string]struct {
		title Title
		width int
		opts  Options
		want  string
	}{
		"wide: everything, the read status at the right":    {title, 132, plainOpts, right("Slow requests  Service gitlab-webservice-default · last 1h", "read 14:57 · 35 of 35 queries", 130)},
		"the status shortens before the name":               {title, 80, plainOpts, right("Slow requests  Service gitlab-webservice-default · last 1h", "14:57 · 35/35", 80)},
		"then the name is cut and marked":                   {title, 60, plainOpts, "Slow requests  Service gitlab-webs… · last 1h  14:57 · 35/35"},
		"then the window goes":                              {title, 44, plainOpts, "Slow requests  Service gitla…  14:57 · 35/35"},
		"then the object":                                   {title, 28, plainOpts, right("Slow requests", "35/35", 28)},
		"plain characters with noIcons":                     {title, 80, asciiOpts, right("Slow requests  Service gitlab-webservice-default | last 1h", "14:57 | 35/35", 80)},
		"a partial read keeps its count":                    {partial, 28, plainOpts, right("Slow requests", "33/35", 28)},
		"a partial read keeps its count before the name":    {partial, 12, plainOpts, "Slow…  33/35"},
		"a read that did not happen says so":                {Title{View: "Graph", Object: "Pod x2x9q", Window: "last 15m"}, 60, plainOpts, right("Graph  Pod x2x9q · last 15m", "not read", 60)},
		"a read that is running says so":                    {Title{View: "Graph", Object: "Pod x2x9q", Window: "last 15m", Busy: "reading…"}, 60, plainOpts, right("Graph  Pod x2x9q · last 15m", "reading…", 60)},
		"a read without a count of queries has none":        {Title{View: "Probe", Object: "Service web", Read: minute(57)}, 60, plainOpts, right("Probe  Service web", "read 14:57", 60)},
		"the clock is UTC, like the axis":                   {Title{View: "Probe", Read: minute(57).In(fixedZone())}, 40, plainOpts, right("Probe", "read 14:57", 40)},
		"the name of the view alone when nothing else fits": {title, 9, plainOpts, "Slow req…"},
	} {
		t.Run(name, func(t *testing.T) {
			line := TitleLine(c.title, c.width, c.opts)
			assert.Equal(t, c.want, plain(line))
			assert.LessOrEqual(t, width(line), c.width)
		})
	}
}

func TestTitleColoursSayWhetherTheReadIsWhole(t *testing.T) {
	title := Title{View: "Slow requests", Object: "Service web", Window: "last 1h", Read: minute(57), Done: 35, Total: 35}
	row := draw(t, []string{TitleLine(title, 80, colorOpts)}, 80)[0]
	assert.Equal(t, testPalette.Accent, row[0].fg, "the view name is in the accent colour")
	assert.True(t, row[0].bold)
	assert.Equal(t, testPalette.Text, row[at(t, row, "Service")].fg)
	assert.True(t, row[79].dim, "a whole read is dim")

	title.Done = 30
	row = draw(t, []string{TitleLine(title, 80, colorOpts)}, 80)[0]
	assert.Equal(t, testPalette.Warn, row[79].fg, "a partial read is not")
	assert.False(t, row[79].dim)
	assert.Contains(t, text(row), "30 of 35 queries")
}

func TestKeyLines(t *testing.T) {
	next := []Action{{"B", "probe the pods now"}, {"m", "compare the pods"}, {"Enter", "open hx4mt"}, {"D", "open in Grafana"}}
	keys := []string{"↑↓ row", "←→ time", "1-4 window", "a show every signal", "s queries", "Esc back"}
	for name, c := range map[string]struct {
		width      int
		opts       Options
		next, rest string
	}{
		"wide": {132, plainOpts,
			"  next  B probe the pods now    m compare the pods    Enter open hx4mt    D open in Grafana",
			"  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · Esc back"},
		"the actions draw closer before one goes": {86, plainOpts,
			"  next  B probe the pods now  m compare the pods  Enter open hx4mt  D open in Grafana",
			"  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · Esc back"},
		"actions go from the right; all detail, the queries and the way out stay": {60, plainOpts,
			"  next  B probe the pods now  m compare the pods",
			"  ↑↓ row · a show every signal · s queries · Esc back"},
		"narrower, the queries go before all detail": {34, plainOpts,
			"  next  B probe the pods now",
			"  a show every signal · Esc back"},
		"the first action is always named": {20, plainOpts,
			"  next  B probe the…",
			"  Esc back"},
		"only the way out": {12, plainOpts,
			"  next  B p…",
			"  Esc back"},
		"plain characters with noIcons": {80, asciiOpts,
			"  next  B probe the pods now  m compare the pods  Enter open hx4mt",
			"  Up/Down row | Left/Right time | a show every signal | s queries | Esc back"},
	} {
		t.Run(name, func(t *testing.T) {
			first, second := NextLine(next, c.width, c.opts), RestLine(keys, c.width, c.opts)
			assert.Equal(t, c.next, plain(first))
			assert.Equal(t, c.rest, plain(second))
			assert.LessOrEqual(t, width(first), c.width)
			assert.LessOrEqual(t, width(second), c.width)
		})
	}
	assert.Empty(t, NextLine(nil, 80, plainOpts))
	assert.Empty(t, RestLine(nil, 80, plainOpts))
	// A view that must keep a key named asks how many the line names.
	for width, shown := range map[int]int{132: 4, 85: 4, 84: 3, 60: 2, 34: 1, 12: 1} {
		assert.Equal(t, shown, NextShown(next, width, plainOpts), "%d columns", width)
		for _, action := range next[:shown] {
			if width > 34 {
				assert.Contains(t, plain(NextLine(next, width, plainOpts)), action.Key+" "+action.Label, "%d columns", width)
			}
		}
		for _, action := range next[shown:] {
			assert.NotContains(t, plain(NextLine(next, width, plainOpts)), action.Label, "%d columns", width)
		}
	}
	assert.Zero(t, NextShown(nil, 80, plainOpts))
	assert.Empty(t, RestLine([]string{"", ""}, 80, plainOpts))

	rows := draw(t, []string{NextLine(next, 132, colorOpts), RestLine(keys, 132, colorOpts)}, 132)
	assert.Equal(t, testPalette.Warn, rows[0][2].fg, `"next" is in the warning colour`)
	assert.True(t, rows[0][2].bold)
	assert.Equal(t, testPalette.Text, rows[0][8].fg)
	assert.True(t, rows[1][2].dim, "the second line is dim")
}

func TestHeadingAndFoldedLine(t *testing.T) {
	assert.Equal(t, "[#a5b0aa:-:b]QUIET[-:-:-]", HeadingText("QUIET", "", colorOpts), "a label without a colour is muted")
	assert.Equal(t, "[#79c7d4:-:b]RESPONSE TIME p99[-:-:-]", HeadingText("RESPONSE TIME p99", testPalette.Accent, colorOpts), "written as given")
	assert.Equal(t, "[-:-:b]QUIET[-:-:-]", HeadingText("QUIET", "", plainOpts), "bold without a palette")

	items := []string{"memory", "restarts", "errors", "network"}
	for name, c := range map[string]struct {
		items []string
		width int
		opts  Options
		want  string
	}{
		"all items":                          {items, 80, plainOpts, "  SAME ON ALL 3  memory · restarts · errors · network"},
		"items that do not fit are counted":  {items, 42, plainOpts, "  SAME ON ALL 3  memory · restarts +2 more"},
		"down to one":                        {items, 32, plainOpts, "  SAME ON ALL 3  memory +3 more"},
		"the first is cut, the rest counted": {[]string{"postgresql · redis · gitaly", "dns"}, 34, plainOpts, "  SAME ON ALL 3  postgres… +1 more"},
		"one item is cut":                    {[]string{"CPU usage (1.5 cores, peers 0.4)"}, 36, plainOpts, "  SAME ON ALL 3  CPU usage (1.5 cor…"},
		"plain characters with noIcons":      {items, 80, asciiOpts, "  SAME ON ALL 3  memory | restarts | errors | network"},
		"no room for a cut one: the count":   {items, 24, plainOpts, "  SAME ON ALL 3  (4)"},
		"no item, only the label":            {nil, 80, plainOpts, "  SAME ON ALL 3"},
	} {
		t.Run(name, func(t *testing.T) {
			line := FoldedLine("SAME ON ALL 3", "", c.items, true, c.width, c.opts)
			assert.Equal(t, c.want, plain(line))
			assert.LessOrEqual(t, width(line), c.width)
		})
	}
	row := draw(t, []string{FoldedLine("ALSO DIFFERS", "", []string{"CPU usage"}, false, 60, colorOpts), FoldedLine("QUIET", "", []string{"dns"}, true, 60, colorOpts)}, 60)
	assert.True(t, row[0][2].bold)
	assert.Equal(t, testPalette.Text, row[0][at(t, row[0], "CPU usage")].fg, "what a group says is plain")
	assert.True(t, row[1][at(t, row[1], "dns")].dim, "what a quiet group says is dim")
}

func TestTextGivenToTheKitIsEscaped(t *testing.T) {
	// A name with brackets would otherwise be read by tview as a tag.
	row := Row{Subject: "pod [red]", What: "a[b]c", Number: "1"}
	group := Group{Label: "L[x]", Rows: []Row{row}}
	layout := Measure([]Group{group}, 80, 0, false)
	lines := GroupLines(group, 1, false, Bands{}, hourFrame(layout.Strip), layout, colorOpts)
	cells := draw(t, lines, 80)
	assert.Equal(t, "  L[x]", text(cells[0]))
	assert.True(t, strings.HasPrefix(text(cells[1]), "  pod [red]  a[b]c  1"), text(cells[1]))
	require.Len(t, VerdictLines(Verdict{Sentences: []string{"[red]x"}}, 40, colorOpts), 1)
	assert.Equal(t, "  [red]x", text(draw(t, VerdictLines(Verdict{Sentences: []string{"[red]x"}}, 40, colorOpts), 40)[0]))
}
