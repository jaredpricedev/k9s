package view

import (
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
