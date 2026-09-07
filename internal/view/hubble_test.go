package view

import (
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/tcell/v2"
	"testing"
)

func TestHubbleFrozenConversationAndSelection(t *testing.T) {
	w := newHubbleView(hubble.Scope{Pods: []string{"ns/a"}}, false)
	w.session = hubble.NewSession(hubble.Config{}, w.scope, hubble.Query{}, 2)
	a := hubble.Peer{Pod: "ns/a", Kind: "pod"}
	b := hubble.Peer{IP: "1.1.1.1", Kind: "world"}
	w.session.Store.Add(hubble.Event{Source: a, Destination: b, Verdict: "FORWARDED"})
	w.render()
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for i := 0; i < 10000; i++ {
		w.session.Store.Add(hubble.Event{Source: b, Destination: a, Verdict: "DROPPED"})
	}
	w.render()
	if !w.frozen || len(w.rows) != 1 || w.rows[0].Verdict != "FORWARDED" {
		t.Fatal("inspection shifted")
	}
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if w.mode != "detail" || w.selected.ID != 1 {
		t.Fatal("detail lost selection")
	}
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if w.mode != "conversation" {
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
