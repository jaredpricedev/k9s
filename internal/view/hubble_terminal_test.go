// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// TestHubblePTYFixture is an opt-in real terminal fixture. Its footer lets a
// PTY reader identify completed frames after a key. It is separate from the
// allocation benchmarks: native terminal drawing occurs, but no Relay is used.
func TestHubblePTYFixture(t *testing.T) {
	if os.Getenv("K9PLUS_HUBBLE_TTY_FIXTURE") != "1" {
		t.Skip("opt-in PTY input-to-paint fixture")
	}
	w := newHubbleView(hubble.Scope{Title: "Synthetic 10,000-event latency fixture", Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 10000)
	for i := range 10000 {
		w.session.Store.Add(hubble.Event{Time: time.Unix(1770000000, int64(i)*int64(time.Millisecond)), Source: hubble.Peer{Pod: "ns/a", Kind: "pod"}, Destination: hubble.Peer{IP: "1.1.1.1", Kind: "world"}, Protocol: "TCP", Verdict: "FORWARDED", Origin: "fixture"})
	}
	w.render()
	w.inspect() // Start on the chosen frozen conversation; IDs are exactly 1..10k.
	app := tview.NewApplication().SetRoot(w, true).SetFocus(w.table)
	route := func(e *tcell.EventKey) *tcell.EventKey {
		if e.Rune() == 'X' {
			app.Stop()
			return nil
		}
		if e.Rune() == 'B' {
			for range 10000 {
				w.session.Store.Add(hubble.Event{Source: hubble.Peer{Pod: "ns/a", Kind: "pod"}, Destination: hubble.Peer{IP: "1.1.1.1", Kind: "world"}, Protocol: "TCP", Verdict: "DROPPED", Origin: "fixture burst"})
			}
			w.render()
			return nil
		}
		result := w.key(e)
		if w.mode == modeDetail || w.mode == hubbleHelpMode {
			app.SetFocus(w.detail)
		} else {
			app.SetFocus(w.table)
		}
		return result
	}
	w.table.SetInputCapture(route)
	w.detail.SetInputCapture(route)
	app.SetAfterDrawFunc(func(screen tcell.Screen) {
		width, height := screen.Size()
		row, _ := w.table.GetSelection()
		id := w.selected.ID
		if w.mode == hubbleConversationMode && row > 0 && row <= len(w.rows) {
			id = w.rows[row-1].ID
		}
		footer := fmt.Sprintf("FRAME mode=%s event=%d frozen=%t evicted=%d (B bursts, X exits)", w.mode, id, w.frozen, w.session.Store.Stats().Evicted)
		for col := range width {
			r := ' '
			if col < len(footer) {
				r = rune(footer[col])
			}
			screen.SetContent(col, height-1, r, nil, tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack))
		}
	})
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
}
