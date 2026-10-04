// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/access"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const accessFixtureUID = "selected-uid"

func accessViewFixture(t *testing.T) *accessView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	if _, err := app.Config.ActivateContext("ct-1-1"); err != nil {
		t.Fatal(err)
	}
	v := &accessView{Details: NewDetails(app, "Access decision", "question", contentInspection, true), question: access.Question{Context: app.Config.ActiveContextName(), GVR: podCmd, Namespace: investigationAppRole, Name: testWorkspaceAPIName, User: access.Self, Verb: client.GetVerb, TargetUID: accessFixtureUID}, revision: app.Config.DestinationRevision()}
	app.Content.Push(v)
	return v
}
func accessFormForTest(t *testing.T, v *accessView) *tview.Form {
	t.Helper()
	var form *tview.Form
	v.modal.Focus(func(p tview.Primitive) { form, _ = p.(*tview.Form) })
	if form == nil {
		t.Fatal("missing native access form")
	}
	return form
}
func TestAccessCanceledAndAbandonedFormsDoNotReview(t *testing.T) {
	v := accessViewFixture(t)
	v.editQuestion()
	form := accessFormForTest(t, v)
	next := NewDetails(v.app, "next", "", contentTXT, false)
	v.app.Content.Push(next)
	v.app.SetFocus(next)
	focus := v.app.GetFocus()
	form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { v.app.SetFocus(p) })
	if v.modal != nil || v.cancel != nil || v.snapshot != nil || v.app.GetFocus() != focus {
		t.Fatal("abandoned form acted")
	}
	v.app.Content.Pop()
	v.editQuestion()
	form = accessFormForTest(t, v)
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { v.app.SetFocus(p) })
	if v.modal != nil || v.cancel != nil || v.app.Content.Top() != v {
		t.Fatal("cancel lost owner or launched review")
	}
}
func TestAccessLateCallbacksRejectDestinationRoundTripAndCancel(t *testing.T) {
	v := accessViewFixture(t)
	generation := v.generation
	previous := v.app.Config.ActiveNamespace()
	if !v.current(generation) {
		t.Fatal("current question rejected")
	}
	if err := v.app.Config.SetActiveNamespace("other"); err != nil {
		t.Fatal(err)
	}
	if err := v.app.Config.SetActiveNamespace(previous); err != nil {
		t.Fatal(err)
	}
	if v.current(generation) {
		t.Fatal("namespace round trip revived stale destination")
	}
	ctx, cancel := context.WithCancel(t.Context())
	v.cancel = cancel
	v.Stop()
	if ctx.Err() == nil || v.current(generation) {
		t.Fatal("late callback survived cancel")
	}
}
func TestAccessNativeFormAndDecisionAtSupportedSizes(t *testing.T) {
	v := accessViewFixture(t)
	v.model.AddListener(v.Details)
	v.Update("ACCESS DECISION: unavailable\nSubject: self\nReview request: denied\nVisible RBAC explanation: incomplete\nNo grants inferred.\ne: edit · r: review · Esc: return")
	for _, size := range [][2]int{{80, 24}, {60, 18}, {40, 16}} {
		screen := tcell.NewSimulationScreen("UTF-8")
		if err := screen.Init(); err != nil {
			t.Fatal(err)
		}
		screen.SetSize(size[0], size[1])
		v.SetRect(0, 0, size[0], size[1])
		v.Draw(screen)
		text := accessScreenText(screen)
		if !strings.Contains(text, "ACCESS DECISION") || !strings.Contains(text, "incomplete") {
			t.Fatal(size, text)
		}
		v.editQuestion()
		v.modal.Draw(screen)
		text = accessScreenText(screen)
		if !strings.Contains(text, "Explicit access question") || !strings.Contains(text, "Cancel") || !strings.Contains(text, "Review") {
			t.Fatal(size, text)
		}
		v.dismissAccessForm(false)
		screen.Fini()
	}
}

func TestAccessDisconnectRetainsCaptureAndNativeControlsBeyondRetryBudget(t *testing.T) {
	v := accessViewFixture(t)
	a := v.app
	if err := v.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	a.Config.SetConnection(conn)
	a.factory = watch.NewFactory(conn)
	a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
	a.Config.K9s.MaxConnRetry = 1
	snapshot := &access.Snapshot{Question: v.question, Decision: "unknown", At: time.Now()}
	v.snapshot = snapshot
	v.Update(snapshot.Text() + "\nNo questioned resource was read or changed.\ne: edit · r: explicit review · Esc: return")
	v.BufferCompleted("Subject", "")
	v.text.ScrollTo(2, 1)
	pending, cancel := context.WithCancel(t.Context())
	v.cancel, v.generation = cancel, 7
	defer cancel()
	for range 3 {
		if err := a.refreshCluster(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&a.conRetry) <= a.Config.K9s.MaxConnRetry || a.Content.Top() != v ||
		v.snapshot != snapshot || v.generation != 7 || pending.Err() != nil || v.inspectionQuery != "Subject" {
		t.Fatal("disconnect changed retained access review state")
	}
	row, col := v.text.GetScrollOffset()
	if row != 2 || col != 1 {
		t.Fatalf("scroll offset changed: %d,%d", row, col)
	}
	a.connectivityComponent(v, true)
	if v.generation != 7 || pending.Err() != nil {
		t.Fatal("recovery canceled explicit access review")
	}
	event := tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone)
	v.InputHandler()(event, func(p tview.Primitive) { a.SetFocus(p) })
	if v.modal == nil {
		t.Fatal("native access question control unavailable after disconnect")
	}
	form := accessFormForTest(t, v)
	modalFrame := drawnText(t, v.modal, 80, 24)
	if !strings.Contains(modalFrame, "Explicit access question") || !strings.Contains(modalFrame, "Cancel") || !strings.Contains(modalFrame, "Review") {
		t.Fatal("native access controls missing after disconnect", modalFrame)
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { a.SetFocus(p) })
	frame := drawnText(t, v, 80, 24)
	if !strings.Contains(frame, "ACCESS DECISION") || !strings.Contains(frame, "Subject: self") {
		t.Fatal("retained access evidence or controls missing", frame)
	}
}
func accessScreenText(screen tcell.Screen) string {
	var b strings.Builder
	width, height := screen.Size()
	for y := range height {
		for x := range width {
			r, _, _, _ := screen.GetContent(x, y)
			b.WriteRune(r)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
