// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/tcell/v2"
)

func TestHubbleFrozenConversationAndSelection(t *testing.T) {
	w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 2)
	a := hubble.Peer{Pod: "ns/a", Kind: "pod"}
	b := hubble.Peer{IP: "1.1.1.1", Kind: "world"}
	w.session.Store.Add(hubble.Event{Source: a, Destination: b, Verdict: "FORWARDED"})
	w.render()
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for range 10000 {
		w.session.Store.Add(hubble.Event{Source: b, Destination: a, Verdict: "DROPPED"})
	}
	w.render()
	if !w.frozen || len(w.rows) != 1 || w.rows[0].Verdict != "FORWARDED" {
		t.Fatal("inspection shifted")
	}
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if w.mode != modeDetail || w.selected.ID != 1 {
		t.Fatal("detail lost selection")
	}
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if w.mode != hubbleConversationMode {
		t.Fatal(w.mode)
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
	if w.frozen || len(w.rows) != 2 {
		t.Fatal("resume failed")
	}
}

func TestHubbleFrozenTicksRetainRowsAndUpdateEvictions(t *testing.T) {
	w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 2)
	a := hubble.Peer{Pod: "ns/a", Kind: "pod"}
	b := hubble.Peer{IP: "1.1.1.1", Kind: "world"}
	w.session.Store.Add(hubble.Event{Source: a, Destination: b, Verdict: "FORWARDED"})
	w.render()
	w.inspect()
	cell := w.table.GetCell(1, 0)
	displayed := &w.displayed[0]
	rows := &w.rows[0]
	w.table.SetOffset(1, 2)
	for range 10000 {
		w.session.Store.Add(hubble.Event{Source: b, Destination: a, Verdict: "DROPPED"})
	}
	w.render()
	if w.table.GetCell(1, 0) != cell || &w.displayed[0] != displayed || &w.rows[0] != rows {
		t.Fatal("unchanged frozen tick rebuilt rows or cloned snapshot")
	}
	if row, col := w.table.GetOffset(); row != 1 || col != 2 {
		t.Fatalf("tick moved viewport: %d %d", row, col)
	}
	if !strings.Contains(w.status.GetText(false), "local evictions: 9999") {
		t.Fatal("loss counters stopped updating", w.status.GetText(false))
	}
	w.applyFilter("dropped")
	if len(w.rows) != 0 || len(w.displayed) != 1 || w.displayed[0].ID != 1 {
		t.Fatal("filter moved frozen dataset")
	}
	w.applyFilter("forwarded")
	if len(w.rows) != 1 || w.rows[0].ID != 1 {
		t.Fatal("filter did not invalidate rows")
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
	if w.frozen || len(w.displayed) != 2 || w.displayed[0].ID != 10000 || len(w.rows) != 0 {
		t.Fatal("resume did not use current live dataset")
	}
}

func TestHubbleUnchangedLiveTicksRetainTable(t *testing.T) {
	w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 2)
	w.session.Store.Add(hubble.Event{Source: hubble.Peer{Pod: "ns/a"}, Destination: hubble.Peer{IP: "1.1.1.1"}})
	w.render()
	cell, displayed := w.table.GetCell(1, 0), &w.displayed[0]
	w.render()
	if w.table.GetCell(1, 0) != cell || &w.displayed[0] != displayed {
		t.Fatal("unchanged live dataset copied or rerendered")
	}
	w.session.Store.Add(hubble.Event{Source: hubble.Peer{Pod: "ns/a"}, Destination: hubble.Peer{IP: "1.1.1.1"}})
	w.render()
	if len(w.displayed) != 2 || w.peers[0].Count != 2 {
		t.Fatal("changed live revision did not rerender")
	}
}

// These benchmarks measure fixture view assembly, without a Relay or terminal
// paint. Live changes one event per render; frozen retains the exact 10k IDs.
func BenchmarkHubbleConversationRender(b *testing.B) {
	for _, frozen := range []bool{true, false} {
		b.Run(fmt.Sprintf("frozen=%t/retained=10000", frozen), func(b *testing.B) {
			w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
			w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 10000)
			e := hubble.Event{Source: hubble.Peer{Pod: "ns/a", Kind: "pod"}, Destination: hubble.Peer{IP: "1.1.1.1", Kind: "world"}, Protocol: "TCP", Verdict: "FORWARDED", Origin: "live"}
			for range 10000 {
				w.session.Store.Add(e)
			}
			w.render()
			w.inspect()
			w.frozen = frozen
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w.session.Store.Add(e)
				w.render()
			}
		})
	}
}
func TestHubbleMalformedFilterPreservesView(t *testing.T) {
	w := newHubbleView(hubble.Scope{}, false)
	w.applyFilter("hello")
	w.applyFilter("verdict=invalid")
	if w.query.Text != "hello" || w.expression != "hello" || w.notice == "" {
		t.Fatal("previous filter lost")
	}
}

func TestHubbleDetailRefreshPreservesScroll(t *testing.T) {
	w := newHubbleView(hubble.Scope{}, false)
	w.mode = modeDetail
	w.frozen = true
	w.render()
	w.detail.ScrollTo(4, 0)
	w.render()
	row, _ := w.detail.GetScrollOffset()
	if row != 4 {
		t.Fatalf("detail refresh reset scroll: %d", row)
	}
}

func TestHubbleHelpReturnsToStatus(t *testing.T) {
	w := newHubbleView(hubble.Scope{}, true)
	w.key(tcell.NewEventKey(tcell.KeyRune, '?', tcell.ModNone))
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if w.mode != hubbleStatusMode {
		t.Fatal("status screen lost after help", w.mode)
	}
}

func TestHubbleStopCancelsCollectorWhileLifecycleStopped(t *testing.T) {
	w := newHubbleView(hubble.Scope{}, false)
	called := false
	w.cancel = func() { called = true }
	w.Stop()
	if !called || w.cancel != nil {
		t.Fatal("collector leaked after second stop")
	}
}

func TestHubbleDetailEscapesReportedMarkup(t *testing.T) {
	w := newHubbleView(hubble.Scope{}, false)
	w.mode = modeDetail
	w.selected = hubble.Event{Verdict: "DROPPED", DropReason: "POLICY_DENIED", Node: "[red]untrusted", Policy: "[::b]reported-policy"}
	w.render()
	plain := drawnText(t, w.detail, 110, 28)
	for _, text := range []string{"VERDICT", "ENDPOINTS", "OBSERVATION", "VISIBILITY & POLICY", "[red]untrusted", "[::b]reported-policy"} {
		if !strings.Contains(plain, text) {
			t.Fatalf("missing literal detail %q in %s", text, plain)
		}
	}
	if !strings.Contains(w.detail.GetText(false), "["+w.hubblePalette().failure+"::b]DROPPED") {
		t.Fatal("drop verdict lacks severity styling")
	}
}

func TestHubbleSelectedVerdictKeepsSeverityAndLiteralMarkup(t *testing.T) {
	w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 2)
	w.session.Store.Add(hubble.Event{Source: hubble.Peer{Pod: "ns/a", Kind: "pod"}, Destination: hubble.Peer{IP: "[red]reported", Kind: "world"}, Verdict: "DROPPED"})
	w.render()
	w.inspect()
	cell := w.table.GetCell(1, 4)
	oldText, oldColor, oldBackground := cell.Text, cell.Color, cell.BackgroundColor
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 8)
	w.table.SetRect(0, 0, 120, 8)
	w.table.Draw(screen)
	p := w.semanticPalette()
	var text strings.Builder
	for x := range 120 {
		r, _, style, _ := screen.GetContent(x, 1)
		text.WriteRune(r)
		if r == 'D' {
			fg, bg, _ := style.Decompose()
			if fg != p.Failure.Color() || bg != p.Selected.Color() {
				t.Fatalf("selected drop lost severity: %v/%v", fg, bg)
			}
		}
	}
	if !strings.Contains(text.String(), "> ") || !strings.Contains(text.String(), "[red]reported") || !strings.Contains(text.String(), "DROPPED") {
		t.Fatal("selection or literal reported text missing", text.String())
	}
	if cell.Text != oldText || cell.Color != oldColor || cell.BackgroundColor != oldBackground {
		t.Fatal("paint mutated retained table cell")
	}
}

func TestHubbleSelectionPaintScrollsToSelectedEvent(t *testing.T) {
	w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 100)
	for range 100 {
		w.session.Store.Add(hubble.Event{Source: hubble.Peer{Pod: "ns/a"}, Destination: hubble.Peer{IP: "world"}, Verdict: "FORWARDED"})
	}
	w.render()
	w.inspect()
	w.table.Select(100, 0)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 8)
	w.table.SetRect(0, 0, 120, 8)
	w.table.Draw(screen)
	row, _ := w.table.GetOffset()
	if row != 93 {
		t.Fatal("selected event remained outside viewport", row)
	}
	r, _, _, _ := screen.GetContent(0, 7)
	if r != '>' {
		t.Fatal("selected marker missing at end of viewport", r)
	}
}
