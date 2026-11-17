// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

// The views that locate a hang were built apart and are used together:
// :slow, :probe, :graph, a drawn dashboard or query, and :impact. These
// tests hold them to one keyboard and one list of commands.

// walkKeys returns what a key is bound to in a view, or "" when it is not.
func walkKey(actions *ui.KeyActions, key tcell.Key) string {
	action, ok := actions.Get(key)
	if !ok {
		return ""
	}
	return action.Description
}

func TestHangViewsShareOneKeyboard(t *testing.T) {
	slow := slowViewFixture(t)
	slow.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	probeV, _ := probeViewFixture(t)
	probeV.acceptPlan(probeTestPlan(), nil)
	graph, _, _, _ := compareViewFixture(t, hangTarget)
	dash := dashViewFixture(t, dashSpec{Board: true})
	impactV := impactViewFixture(t)
	views := map[string]*ui.KeyActions{"slow": slow.actions, "probe": probeV.actions, "graph": graph.actions, "dash": dash.actions, "impact": impactV.actions}

	// The digits choose a window wherever there is one, the same four in
	// :slow, :graph and a drawn dashboard. A probe has no window, so a
	// digit does nothing there instead of meaning a count.
	for index, window := range graphWindows {
		key := ui.Key1 + tcell.Key(index)
		for _, name := range []string{"slow", "graph", "dash"} {
			require.Equal(t, "Window "+graphSpan(window), walkKey(views[name], key), name)
		}
		require.Empty(t, walkKey(views["probe"], key), "probe")
		require.True(t, strings.HasPrefix(walkKey(views["impact"], key), "Window "), "impact: a digit is a window there too")
	}

	// One key per destination, the same in each view that offers it and
	// the same as on a row of the resource list.
	for _, want := range []struct {
		key   tcell.Key
		word  string
		views []string
	}{
		{ui.KeyM, "raph", []string{"slow", "probe"}},
		{ui.KeyT, "imeline", []string{"slow", "probe", "impact"}},
		{grafanaKey, grafanaKeyLabel, []string{"slow", "probe", "graph", "dash", "impact"}},
		{probeKey, "Probe", []string{"slow", "graph"}},
		{slowKey, "Slow requests", []string{"probe", "graph"}},
		{ui.KeyR, "", []string{"slow", "probe", "graph", "dash", "impact"}},
		{ui.KeyS, "Queries", []string{"slow", "graph", "dash"}},
	} {
		for _, name := range want.views {
			got := walkKey(views[name], want.key)
			require.NotEmpty(t, got, "%s: %s", name, tcell.KeyNames[want.key])
			require.Contains(t, got, want.word, "%s: %s", name, tcell.KeyNames[want.key])
		}
	}
	require.Equal(t, ui.KeyShiftB, probeKey)
	require.Equal(t, ui.KeyShiftW, slowKey)

	// n and Shift-N step forward and back through what a view steps
	// through: episodes, signals, rollouts.
	for name, words := range map[string][2]string{"slow": {"next episode", "previous episode"}, "graph": {"Next signal", "Previous signal"},
		"impact": {"Next (later) rollout", "Previous (earlier) rollout"}} {
		require.Contains(t, walkKey(views[name], ui.KeyN), words[0], name)
		require.Contains(t, walkKey(views[name], ui.KeyShiftN), words[1], name)
	}

	// p is the per-Pod page of :graph and nothing else: not the probe in
	// :slow, not a second "previous" in :impact (which only says where
	// that went), not a count or a path in :probe.
	require.Equal(t, "Per pod", walkKey(views["graph"], ui.KeyP))
	require.Empty(t, walkKey(views["slow"], ui.KeyP))
	require.Empty(t, walkKey(views["probe"], ui.KeyP))
	require.Empty(t, walkKey(views["dash"], ui.KeyP))
	moved, ok := views["impact"].Get(ui.KeyP)
	require.True(t, ok)
	require.False(t, moved.Opts.Visible, "the pointer to Shift-N is not offered as a key")
	require.Contains(t, moved.Description, "Shift-N")

	// b switches the chart style, areas and solid lines or braille dots,
	// in every view that draws a chart or a trend with one, and is not
	// taken by anything in the two that do not.
	for _, name := range []string{"slow", "graph", "dash"} {
		require.Equal(t, chartStyleLabel, walkKey(views[name], chartStyleKey), name)
	}
	require.Equal(t, ui.KeyB, chartStyleKey)
	require.Empty(t, walkKey(views["probe"], chartStyleKey))
	require.Empty(t, walkKey(views["impact"], chartStyleKey))

	// w alone opens nothing: Shift-W is :slow, here as on a Service row.
	for name, actions := range views {
		require.Empty(t, walkKey(actions, ui.KeyW), name)
	}

	// The cursor moves with the arrows in every view that has one, and
	// Home and End reach the ends of the window.
	for _, name := range []string{"slow", "graph", "dash"} {
		for _, key := range []tcell.Key{tcell.KeyLeft, tcell.KeyRight, tcell.KeyHome, tcell.KeyEnd} {
			require.NotEmpty(t, walkKey(views[name], key), "%s: %s", name, tcell.KeyNames[key])
		}
	}
}

// Every command of the walk is in the palette and in help once, under
// Inspect, with the key that opens it named on the same entry.
func TestHangCommandsAreListedOnceWithTheirKey(t *testing.T) {
	slow := slowViewFixture(t)
	slow.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	probeV, _ := probeViewFixture(t)
	probeV.acceptPlan(probeTestPlan(), nil)
	graph, _, _, _ := compareViewFixture(t, hangTarget)

	labels := func(owner actionOwner, app *App) map[string][]string {
		out := map[string][]string{}
		for _, item := range actionCatalog(owner, app) {
			if strings.HasPrefix(item.Shortcut, ":") {
				require.NotEmpty(t, item.Label)
				out[item.Shortcut] = append(out[item.Shortcut], string(item.Category)+" / "+item.Label)
			}
		}
		return out
	}
	for name, got := range map[string]map[string][]string{"slow": labels(slow, slow.app), "probe": labels(probeV, probeV.app), "graph": labels(graph, graph.app)} {
		for _, command := range []string{":slow", ":probe", ":graph", ":graph pods", ":query"} {
			require.Len(t, got[command], 1, "%s lists %s once", name, command)
			require.True(t, strings.HasPrefix(got[command][0], string(ui.ActionInspect)+" / "), "%s: %s is under Inspect: %s", name, command, got[command][0])
		}
	}
	// A key that opens a listed command is named on that command; the
	// binding is not listed a second time under its own words.
	inSlow := labels(slow, slow.app)
	require.Equal(t, "Inspect / Probe each Pod: timed GET (asks first) (Shift-B)", inSlow[":probe"][0])
	require.Equal(t, "Inspect / Per-Pod graphs: which Pod differs (m)", inSlow[":graph pods"][0], "m opens the Pods side by side from :slow")
	require.Equal(t, "Inspect / Graph: usage vs limits over time", inSlow[":graph"][0])
	inProbe := labels(probeV, probeV.app)
	require.Equal(t, "Inspect / Slow requests: which hop (Shift-W)", inProbe[":slow"][0])
	require.Equal(t, "Inspect / Graph: usage vs limits over time (m)", inProbe[":graph"][0], "m graphs the Pod under the cursor from :probe")
	inGraph := labels(graph, graph.app)
	require.Equal(t, "Inspect / Per-Pod graphs: which Pod differs (p)", inGraph[":graph pods"][0])
	for _, owner := range []struct {
		name  string
		owner actionOwner
		app   *App
	}{{"slow", slow, slow.app}, {"probe", probeV, probeV.app}, {"graph", graph, graph.app}} {
		for _, item := range actionCatalog(owner.owner, owner.app) {
			if item.Shortcut != "" && !strings.HasPrefix(item.Shortcut, ":") {
				for _, word := range []string{"Probe pods", "Slow requests", "Per pod", graphPodsLabel} {
					require.False(t, strings.HasPrefix(item.Label, word), "%s: %q is listed again as %s", owner.name, item.Label, item.Shortcut)
				}
			}
		}
	}

	// From :slow the entry runs what m runs: the Pods behind the Service,
	// also while one Pod row is focused, where the typed command alone
	// would have no peers to compare.
	drawnText(t, slow, 132, 32)
	for range 3 {
		slowPress(t, slow, tcell.KeyDown)
	}
	require.Equal(t, slowtest.Outlier, slow.focus.pod)
	ran := false
	for _, item := range actionCatalog(slow, slow.app) {
		if item.ID == "resource.graph-pods" {
			require.Empty(t, item.UnavailableReason)
			item.Handler(nil)
			ran = true
		}
	}
	require.True(t, ran)
	opened, ok := slow.app.Content.Top().(*graphView)
	require.True(t, ok)
	t.Cleanup(opened.Stop)
	require.True(t, opened.comparing)
	require.Equal(t, graphKindService, opened.target.Kind)
	require.Equal(t, "gitlab-webservice/p99", opened.signal)
	slow.app.Content.Pop()

	// The shipped queries of :query are signals of the request-path
	// profiles :slow reads.
	for _, q := range dashboard.BuiltinQueries() {
		require.NotEmpty(t, q.Profile, q.Name)
		require.NotEmpty(t, q.Signal, q.Name)
	}
}
