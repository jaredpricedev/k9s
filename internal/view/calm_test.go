// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two keys of the calm layout are free in every incident view, or
// already mean what the layout says. A view that takes one of them for
// something else fails here.
func TestCalmKeysAreFreeOrMeanTheSameInEveryIncidentView(t *testing.T) {
	slow := slowViewFixture(t)
	slow.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	probeV, _ := probeViewFixture(t)
	probeV.acceptPlan(probeTestPlan(), nil)
	graph, _, _, _ := compareViewFixture(t, hangTarget)
	dash := dashViewFixture(t, dashSpec{Board: true})
	impactV := impactViewFixture(t)
	timelineV := timelineViewFixture(t)
	views := map[string]*ui.KeyActions{"slow": slow.actions, "probe": probeV.actions, "graph": graph.actions, "dash": dash.actions, "impact": impactV.actions,
		"timeline": timelineV.actions}

	require.Equal(t, ui.KeyA, calmDetailKey)
	require.Equal(t, ui.KeyN, calmNextChartKey)
	require.Equal(t, tcell.KeyTab, calmNextChartAltKey)
	require.Equal(t, calm.DetailKey, string(rune(calmDetailKey)), "the key the kit names on the page is the key the views bind")
	require.Equal(t, calm.NextChartKey, string(rune(calmNextChartKey)))
	require.Equal(t, calm.NextChartAltKey, tcell.KeyNames[calmNextChartAltKey])

	for name, actions := range views {
		// a: all detail, or nothing yet.
		if got := walkKey(actions, calmDetailKey); got != "" {
			assert.Equal(t, calmDetailLabel, got, "%s: a", name)
		}
		// Tab: the next chart or panel, or nothing yet.
		if got := walkKey(actions, calmNextChartAltKey); got != "" {
			assert.Contains(t, []string{calmNextChartLabel, "Next panel"}, got, "%s: Tab", name)
		}
	}
	// The timeline is laid out the calm way: a is bound there, not merely
	// free.
	require.Equal(t, calmDetailLabel, walkKey(views["timeline"], calmDetailKey))
	// n: the next of what the view steps through. That is the chart where
	// a view steps through nothing else, and stays the episode in :slow,
	// the rollout in :impact and the entry in :timeline.
	for name, want := range map[string][]string{
		"graph":    {calmNextChartLabel, "Next signal"},
		"dash":     {calmNextChartLabel, ""},
		"probe":    {""},
		"slow":     {"Cursor to next episode"},
		"impact":   {"Next (later) rollout"},
		"timeline": {"Cursor to next entry"},
	} {
		assert.Contains(t, want, walkKey(views[name], calmNextChartKey), "%s: n", name)
	}
}

func TestCalmPaletteIsTheSemanticPalette(t *testing.T) {
	stock := calmPalette(config.DefaultSemanticPalette())
	assert.Equal(t, calm.Palette{Text: "#e1e7e3", Muted: "#a5b0aa", Accent: "#79c7d4", Warn: "#e7bd73",
		Fail: "#ef8278", Info: "#91b8ec", Mark: "#b6a3df", Band: "#16202a"}, stock)

	// A colour the terminal owns adds no tag, and without a band colour
	// the marker row and the axis carry the bands alone.
	owned := config.DefaultSemanticPalette()
	owned.Text, owned.Band = config.DefaultColor, config.DefaultColor
	palette := calmPalette(owned)
	assert.Empty(t, palette.Text)
	assert.Empty(t, palette.Band)

	// The monochrome skin is one foreground; the band is still a surface.
	styles := config.NewStyles()
	require.NoError(t, styles.Load("../../skins/monochrome.yaml", false))
	mono := calmPalette(styles.Semantic())
	assert.Equal(t, mono.Text, mono.Fail)
	assert.Equal(t, mono.Text, mono.Warn)
	assert.Equal(t, "#1a1a1a", mono.Band)
}

// A strip puts a sample where the request-path views say its rule held:
// over the step that ends at it. So what slowpath.Crossings calls an
// episode and what a strip draws over its rule are the same columns, at
// any width.
func TestCalmStripsLineUpWithTheEpisodesOfSlowpath(t *testing.T) {
	start := time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)
	step := time.Minute
	var points []chart.Point
	for minute := 1; minute <= 60; minute++ {
		v := 0.3
		if minute >= 9 && minute <= 11 || minute >= 28 && minute <= 31 || minute == 54 {
			v = 40
		}
		points = append(points, chart.Point{T: start.Add(time.Duration(minute) * step), V: v})
	}
	rule := slowpath.Rule{Worse: slowpath.WorseHigh, Always: 1}
	episodes := slowpath.Crossings(points, rule, step, slowpath.Lookback{})
	require.Equal(t, []chart.Interval{
		{Start: start.Add(8 * step), End: start.Add(11 * step)},
		{Start: start.Add(27 * step), End: start.Add(31 * step)},
		{Start: start.Add(53 * step), End: start.Add(54 * step)},
	}, episodes, "a raised sample stands for the step that ends at it")

	bands := calm.Bands{Label: "stalls", Episodes: episodes}
	strip := calm.Levels(points, func(v float64) bool { return rule.Crosses(v, 0.3) })
	for _, width := range []int{12, 24, 60, 73, 120, 200} {
		frame := chart.Frame{Start: start, End: start.Add(time.Hour), Step: step, Width: width}
		line := []rune(calm.StripLine(strip, "", bands, frame, calm.Options{}))
		line = []rune(strings.NewReplacer("[-:-:d]", "", "[-:-:-]", "").Replace(string(line)))
		require.Len(t, line, width)
		for col, in := range bands.Columns(frame) {
			assert.Equal(t, in, line[col] != '▁', "width %d, column %d: %q", width, col, string(line))
		}
	}
}

// The second key line is written in one order in every incident view:
// what the arrows move, the window, all detail, the queries, the view's
// own keys, reading again and the way out. The kit keeps the way out, all
// detail and the queries longest on a narrow line.
func TestCalmKeyLineIsWrittenInOneOrderInEveryIncidentView(t *testing.T) {
	full := calmKeyLine{Move: "↑↓ row", Time: true, Window: calmKeyWindow, Detail: "show every signal", Queries: true,
		Own: []string{"n next stall", "", "t timeline"}, Reread: true}
	assert.Equal(t, []string{"↑↓ row", "←→ time", "1-4 window", "a show every signal", "s queries", "n next stall", "t timeline", "r re-read", "Esc back"},
		full.parts(), "an empty key of the view's own is left out")
	assert.Equal(t, []string{"Esc back"}, (&calmKeyLine{}).parts(), "the way out is always there, and last")
	assert.Equal(t, []string{"1-5 window", "D Grafana", "Esc back"},
		(&calmKeyLine{Window: "1-5 window", Own: []string{calmGrafanaKeyName + " Grafana"}}).parts(), "a key on the next line is not named again")

	// Without a palette the line carries only the dim tag and its reset.
	rest := func(width int) string {
		return strings.NewReplacer("[-:-:d]", "", "[-:-:-]", "").Replace(calm.RestLine(full.parts(), width, calm.Options{}))
	}
	assert.Equal(t, "  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · n next stall · t timeline · r re-read · Esc back", rest(132))
	assert.Equal(t, "  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · Esc back", rest(80),
		"the view's own keys and reading again go first")
	assert.Equal(t, "  ↑↓ row · a show every signal · s queries · Esc back", rest(60),
		"then the cursor and the window; all detail and the queries stay")

	// The words of the hand-offs are the ones the approved pages write.
	assert.Equal(t, "B", calmProbeKeyName)
	assert.Equal(t, "W", calmSlowKeyName)
	assert.Equal(t, "D", calmGrafanaKeyName)
	assert.Equal(t, calm.DetailKey, string(rune(calmDetailKey)))
}

// Every incident view names its object the same way: the namespace is
// written only when it is not the one the session is in.
func TestCalmObjectNamesTheNamespaceOnlyWhenItIsAnotherOne(t *testing.T) {
	assert.Equal(t, "Service gitlab-webservice-default", calmObject("gitlab", "Service", "gitlab", "gitlab-webservice-default"))
	assert.Equal(t, "Service gitlab/gitlab-webservice-default", calmObject("apps", "Service", "gitlab", "gitlab-webservice-default"))
	assert.Equal(t, "Pod apps/checkout-api-x2x9q", calmObject("", "Pod", "apps", "checkout-api-x2x9q"), "a list of every namespace names it")
	assert.Equal(t, "Node worker-f", calmObject("apps", "Node", "", "worker-f"), "an object without a namespace has none to name")

	target := graphTarget{Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}
	assert.Equal(t, "Deployment checkout-api", graphCalmObject(&target, &graphPage{Namespace: "apps"}))
	assert.Equal(t, "Deployment apps/checkout-api", graphCalmObject(&target, &graphPage{Namespace: "gitlab"}))
}

// :impact and :timeline tell the two halves of one rollout: what was
// measured before and after it, and what else happened around it, in
// order. One opens the other, so the two are read as one page after
// another: the title, the answer and its caveat, the marker of the rollout
// and the two key lines are written the same way on both.
func TestImpactAndTimelineReadAsOneProduct(t *testing.T) {
	impactV := impactViewFixture(t)
	impactV.accept(impactStoryRead(t, impactStory()))
	timelineV := timelineIncidentView(t)

	// One sentence for the same limit, from one place; a Pod's graph has it
	// too.
	require.Equal(t, calmOrderCaveat, impactOrderCaveat)
	require.Equal(t, calmOrderCaveat, timelineOrderCaveat)
	require.Equal(t, calmOrderCaveat, graphOrderCaveat)

	for _, size := range [][2]int{{132, 33}, {80, 21}, {80, 20}} {
		for name, frame := range map[string]string{
			"impact":   drawnText(t, impactV, size[0], size[1]),
			"timeline": drawnText(t, timelineV, size[0], size[1]),
		} {
			at := fmt.Sprintf("%s at %dx%d\n%s", name, size[0], size[1], frame)
			lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
			// The title: the view, the object as calmObject writes it, and at
			// the right how much was read and when.
			assert.Regexp(t, `^(Impact|Timeline)  Deployment apps/checkout-api · .*  (read \d\d:\d\d · \d+ of \d+ (queries|sources)|\d\d:\d\d · \d+/\d+)$`, lines[0], at)
			// The answer is directly under it: the rollout by its revision and
			// its minute, bold first sentence, then the caveat.
			answer := strings.Join(lines[1:6], "\n")
			assert.Regexp(t, `(?i)rev 2 rolled out at \d\d:\d\d`, answer, at)
			assert.Contains(t, answer, calmOrderCaveat, at)
			if size[0] == 132 {
				assert.Regexp(t, `(?m)^  \S.*\. {3,}`+regexp.QuoteMeta(calmOrderCaveat)+`$`, answer, "the caveat is at the right of the last line: %s", at)
			}
			// The rollout is the marker over the strips, in the same words.
			assert.Regexp(t, `(?m)^ +rev 2 rolled out \d\d:\d\d.* ▼$`, frame, at)
			// A change is before → after.
			assert.Regexp(t, `\S+ → \S+`, frame, at)
			assert.NotContains(t, frame, "->", at)
			// The first key line: what Enter does, then the key for all detail.
			next, _ := timelineLineWith(t, frame, "  next  ")
			assert.Regexp(t, `^  next  Enter \S.*  a (all 11 signals|full list)\b`, next, at)
			if size[0] == 132 {
				// The second: what the arrows move first, reading again and
				// the way out last.
				rest := lines[len(lines)-1]
				assert.Regexp(t, `^  ↑↓ (signal|row) · .* · r re-read · Esc back$`, rest, at)
				assert.NotContains(t, rest, " a ", "a is on the first line, once: %s", at)
			}
		}
	}
	// The hand-off from one to the other is named by the name the page it
	// opens has in its title.
	frame := drawnText(t, impactV, 132, 33)
	assert.Contains(t, frame, "    "+impactTimelineKeyName+" "+strings.ToLower(timelineTitle)+"\n")
	assert.True(t, strings.HasPrefix(drawnText(t, timelineV, 132, 33), timelineTitle+"  "))
}
