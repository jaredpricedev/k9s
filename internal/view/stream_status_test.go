// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

const streamTestDrop = "DROPPED"

func TestNarrowLogStatusAndRetainedLaneDetail(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	frame := drawnText(t, w, 60, 18)
	for _, word := range []string{"FROZEN", "safe", "visible:3/3", "Loss", "S status"} {
		require.Contains(t, frame, word)
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 'S', tcell.ModNone))
	require.Contains(t, w.detail.GetText(true), "Repeats collapsed")
	require.Contains(t, w.detail.GetText(true), "Scope:")
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Equal(t, modeEntries, w.mode)
	w.showSources()
	w.chooseLane(1)
	w.chooseLane(2)
	require.Equal(t, modeLanes, w.mode)
	frame = drawnText(t, w, 60, 18)
	require.Contains(t, frame, "Lane A")
	require.Equal(t, 1, w.table.GetColumnCount())
	w.key(tcell.NewEventKey(tcell.KeyTAB, 0, tcell.ModNone))
	frame = drawnText(t, w, 60, 18)
	require.Contains(t, frame, "Lane B")
	before := append([]logstream.Entry(nil), w.laneEntries...)
	w.ingest([]logstream.Entry{logstream.Parse(w.sources[1], time.Now(), "new entry after snapshot")})
	w.flush()
	drawnText(t, w, 120, 24)
	drawnText(t, w, 60, 18)
	require.Equal(t, before, w.laneEntries, "resize must not adopt new collection")
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Equal(t, modeDetail, w.mode)
	require.NotContains(t, w.detail.GetText(true), "new entry after snapshot")
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Equal(t, modeLanes, w.mode)
	require.Equal(t, 1, w.focusedLane)
}

func TestNarrowHubbleUnknownCoverageAndStatusReturn(t *testing.T) {
	w := newHubbleView(hubble.Scope{Title: "demo / application"}, false)
	frame := drawnText(t, w, 60, 18)
	for _, word := range []string{"coverage:unknown", "Loss:unknown", "L7 redacted", "S error/status"} {
		require.Contains(t, frame, word)
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 'S', tcell.ModNone))
	require.Equal(t, hubbleStatusDetailMode, w.mode)
	require.Contains(t, w.detail.GetText(true), "Captured scope")
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Equal(t, hubblePeersMode, w.mode)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 4)
	a := hubble.Peer{Pod: "status-test/a"}
	b := hubble.Peer{IP: "192.0.2.1"}
	w.session.Store.Add(hubble.Event{Source: a, Destination: b, Verdict: streamTestDrop})
	w.render()
	w.inspect()
	frame = drawnText(t, w, 60, 18)
	require.Equal(t, 2, w.table.GetColumnCount())
	require.Contains(t, frame, streamTestDrop)
	require.Contains(t, frame, "Enter detail")
	w.inspect()
	require.Equal(t, modeDetail, w.mode)
	require.Contains(t, w.detail.GetText(true), streamTestDrop)
	require.NotEmpty(t, strings.TrimSpace(drawnText(t, w, 30, 8)))
}

func TestLogFullStatusReturnsToRetainedEntryAndHistory(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.table.Select(1, 0)
	w.expand()
	require.Equal(t, modeDetail, w.mode)
	original := w.detail.GetText(false)
	returnMode := w.detailReturnMode
	w.key(tcell.NewEventKey(tcell.KeyRune, 'S', tcell.ModNone))
	w.render()
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Equal(t, modeDetail, w.mode)
	require.Equal(t, original, w.detail.GetText(false))
	require.Equal(t, returnMode, w.detailReturnMode)
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Equal(t, modeEntries, w.mode)
	w.history = w.liveSnapshot()
	w.mode = modeHistory
	w.render()
	w.key(tcell.NewEventKey(tcell.KeyRune, 'S', tcell.ModNone))
	w.render()
	require.Contains(t, w.statusCompact, "HISTORY")
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Equal(t, modeHistory, w.mode)
	require.Contains(t, drawnText(t, w, 40, 12), "HISTORY")
	require.Contains(t, drawnText(t, w, 40, 8), "View too small")
}

func TestStreamResizeDrawDoesNotReenterApplicationFocus(t *testing.T) {
	for _, kind := range []string{"logs", "flows"} {
		t.Run(kind, func(t *testing.T) {
			application := tview.NewApplication()
			host := &App{App: &ui.App{Application: application, Configurator: ui.Configurator{Styles: config.NewStyles()}}}
			screen := tcell.NewSimulationScreen("")
			require.NoError(t, screen.Init())
			defer screen.Fini()
			screen.SetSize(80, 24)
			application.SetScreen(screen)
			var root tview.Primitive
			var focus tview.Primitive
			if kind == "logs" {
				w := testWorkbench(t)
				seedWorkbench(w)
				w.showSources()
				w.chooseLane(1)
				w.chooseLane(2)
				w.owner = &Log{app: host}
				root, focus = w, w.table
			} else {
				w := newHubbleView(hubble.Scope{Title: "native application"}, false)
				w.app = host
				root, focus = w, w.table
			}
			application.SetRoot(root, true).SetFocus(focus)
			for _, width := range []int{80, 60, 120, 40, 60} {
				screen.SetSize(width, 24)
				done := make(chan struct{})
				go func() { application.ForceDraw(); close(done) }()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("drawing a focused stream reentered the application lock")
				}
			}
		})
	}
}
