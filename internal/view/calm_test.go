// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
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
