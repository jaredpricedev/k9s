// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const (
	investigationAcceptanceUID   = "acceptance-crash-pod-uid"
	investigationAcceptanceQuery = "full"
)

type investigationAcceptanceCompletionProbe struct{ completed chan string }

func (p *investigationAcceptanceCompletionProbe) BufferCompleted(text, _ string) {
	select {
	case p.completed <- text:
	default:
	}
}
func (*investigationAcceptanceCompletionProbe) BufferChanged(string, string)        {}
func (*investigationAcceptanceCompletionProbe) BufferActive(bool, model.BufferKind) {}

// Collect through the same selected-resource loader as a real investigation.
// Subsequent fake-client action counts distinguish presentation from new reads.
func investigationAcceptanceFixture(t *testing.T) (*inspectionDetails, *dynamicfake.FakeDynamicClient, *kubefake.Clientset) {
	t.Helper()
	previousMetadata := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = previousMetadata })
	object := &unstructured.Unstructured{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"apiVersion":"v1","kind":"Pod",
		"metadata":{"name":"api","namespace":"apps","uid":"acceptance-crash-pod-uid"},
		"spec":{"nodeName":"worker-02","containers":[{"name":"api","resources":{"requests":{"memory":"128Mi"},"limits":{"memory":"256Mi"}}},{"name":"proxy"}]},
		"status":{"phase":"Running",
			"conditions":[{"type":"Ready","status":"False","reason":"ContainersNotReady","message":"full readiness message\nreadiness continuation"}],
			"containerStatuses":[
				{"name":"api","ready":false,"restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff","message":"full current fault [red] is literal\nsecond full source line"}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"finishedAt":"2026-10-04T08:00:00Z","message":"full previous termination evidence\nprevious continuation"}}},
				{"name":"proxy","ready":true,"restartCount":0,"state":{"running":{}}}
			]}
	}`), &object.Object))
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	eventAt := time.Now().UTC().Add(-6 * time.Minute)
	typed := kubefake.NewSimpleClientset(&corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "old-backoff", Namespace: uiAcceptanceNamespace},
		InvolvedObject: corev1.ObjectReference{UID: investigationAcceptanceUID},
		Reason:         "BackOff", Type: "Warning", Count: 2, LastTimestamp: metav1.NewTime(eventAt),
		Message: "full retained event message\nevent continuation",
	})
	connection := inspectionConnection{dynamic: dynamic, typed: typed}
	cfg := mock.NewMockConfig(t)
	_, err := cfg.ActivateContext("ct-1-1")
	require.NoError(t, err)
	require.NoError(t, cfg.SetActiveNamespace(uiAcceptanceNamespace))
	cfg.K9s.ReadOnly = true
	cfg.K9s.UI.NoIcons, cfg.K9s.UI.Splashless = true, true
	cfg.K9s.UI.HeaderMode = uiAcceptanceCompactHeader
	app := NewApp(cfg)
	require.NoError(t, app.Init("acceptance", 0))
	app.factory = watch.NewFactory(connection)
	target := SelectedResourceTarget{Context: cfg.ActiveContextName(), GVR: client.PodGVR, Namespace: uiAcceptanceNamespace, Name: "api", UID: investigationAcceptanceUID}
	snapshot, err := loadTargetInspectionSnapshot(t.Context(), connection, target, troubleshootCommand)
	require.NoError(t, err)
	d := &inspectionDetails{Details: NewDetails(app, troubleshootCommand, target.Path(), contentInspection, true), target: target, connection: connection,
		related: func(context.Context, SelectedResourceTarget) ([]inspectionReference, error) {
			return []inspectionReference{relationshipRef("", inspectionPodKind, "ops", "child", "fixture relationship")}, nil
		}}
	require.NoError(t, app.inject(d, false))
	d.acceptSnapshot(snapshot, nil)
	t.Cleanup(func() { app.requestExit(0); d.Stop() })
	dynamic.ClearActions()
	typed.ClearActions()
	return d, dynamic, typed
}

func investigationAcceptanceScreen(t *testing.T, app *App) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	app.SetScreen(screen)
	// Calling the ui.App wrapper is essential: Application.SetRoot bypasses
	// the real NO_COLOR drawing boundary.
	app.App.SetRoot(app.Main, true)
	// The production App.Run shows this page after splash and connection
	// startup. Retained fixtures bypass those collectors, then use the same
	// native application root and keyboard dispatcher.
	app.Main.SwitchToPage("main")
	app.SetFocus(app.Content.Top())
	// Application.Stop owns screen finalization after a dispatcher run. Calling
	// SimulationScreen.Fini again closes the same channel twice.
	t.Cleanup(app.Application.Stop)
	return screen
}

func investigationAcceptanceFrame(screen tcell.Screen) string {
	width, height := screen.Size()
	var frame strings.Builder
	for y := range height {
		var line strings.Builder
		for x := range width {
			ch, combining, _, _ := screen.GetContent(x, y)
			line.WriteRune(ch)
			for _, mark := range combining {
				line.WriteRune(mark)
			}
		}
		frame.WriteString(strings.TrimRight(line.String(), " ") + "\n")
	}
	return frame.String()
}

func investigationAcceptanceDraw(t *testing.T, app *App) {
	t.Helper()
	drawn := make(chan struct{})
	go func() { app.Application.ForceDraw(); close(drawn) }()
	select {
	case <-drawn:
	case <-time.After(2 * time.Second):
		t.Fatal("full application investigation draw did not complete")
	}
}

func TestInvestigationAcceptanceFirstViewportAcrossColorModes(t *testing.T) {
	for _, mode := range []string{"stock", "monochrome", "NO_COLOR"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			if mode == "NO_COLOR" {
				t.Setenv("NO_COLOR", "1")
			}
			d, dynamic, typed := investigationAcceptanceFixture(t)
			if mode == "monochrome" {
				require.NoError(t, d.app.Styles.Load("../../skins/monochrome.yaml", false))
				d.app.Styles.Update()
			}
			screen := investigationAcceptanceScreen(t, d.app)
			investigationAcceptanceDraw(t, d.app)
			frame := investigationAcceptanceFrame(screen)
			for _, want := range []string{"[RO]", "ctx:ct-1-1", "ns:apps", "CURRENT FINDINGS", "READY RESTART",
				"Previous termination: OOMKilled · exit 137 · api", "VISIBILITY GAPS", "[?] logs: not collected", "[?] metrics: not collected",
				"NEXT CHECKS · proposed, cause unconfirmed", "l Pod logs · 2 containers · 3 events", "5 evidence · :pressure budgets · 6 compare",
				"Previous exit is historical; current cause unknown."} {
				require.Contains(t, frame, want, "first 80x24 application frame:\n%s", frame)
			}
			require.Regexp(t, `api\s+CrashLoopBackOff\s+no\s+7`, frame)
			require.Regexp(t, `proxy\s+running\s+yes\s+0`, frame)
			require.Equal(t, 1, strings.Count(frame, "CrashLoopBackOff"))
			require.NotContains(t, frame, "second full source line")
			row, column := d.text.GetScrollOffset()
			require.Zero(t, row)
			require.Zero(t, column)
			logs, ok := d.Actions().Get(ui.KeyL)
			require.True(t, ok)
			require.NotNil(t, logs.Action)
			require.Empty(t, logs.Availability(), "the displayed Pod logs action must be usable for this captured Pod")
			// Exercise the native widget's advertised tab controls, not private
			// rendering helpers. These are evidence views over the same capture.
			for _, tab := range []struct {
				key   rune
				index int
			}{{'2', 1}, {'3', 2}, {'5', 4}, {'1', 0}} {
				d.InputHandler()(tcell.NewEventKey(tcell.KeyRune, tab.key, tcell.ModNone), func(p tview.Primitive) { d.app.SetFocus(p) })
				require.Equal(t, tab.index, d.activeTab)
			}
			require.Empty(t, dynamic.Actions(), "drawing and opening evidence tabs must not fetch newer objects")
			require.Empty(t, typed.Actions(), "opening the Events tab must use retained records")
			if mode == "NO_COLOR" {
				for y := range 24 {
					for x := range 80 {
						_, _, style, _ := screen.GetContent(x, y)
						foreground, background, _ := style.Decompose()
						require.Equal(t, tcell.ColorDefault, foreground)
						require.Equal(t, tcell.ColorDefault, background)
					}
				}
			}
		})
	}
}

func investigationAcceptanceExport(t *testing.T, d *inspectionDetails) (encoded, markdown []byte) {
	t.Helper()
	bundle, err := retainedEvidenceBundle(d)
	require.NoError(t, err)
	// Export creation time is intentionally new on each invocation; source
	// identity, observation times, payload and limits must remain identical.
	bundle.CreatedAt = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	encoded, err = inspect.EncodeBundle(bundle, false)
	require.NoError(t, err)
	markdown, err = inspect.EncodeBundle(bundle, true)
	require.NoError(t, err)
	return encoded, markdown
}

func investigationAcceptanceUpdate(t *testing.T, app *App, update func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { app.Application.QueueUpdateDraw(update); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("investigation UI dispatcher did not complete the update")
	}
}

func investigationAcceptanceAwait(t *testing.T, app *App, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var complete bool
		investigationAcceptanceUpdate(t, app, func() { complete = ready() })
		if complete {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("investigation dispatcher never reached the requested state")
		}
	}
}

func investigationAcceptanceRun(t *testing.T, app *App) {
	t.Helper()
	painted := make(chan struct{}, 1)
	app.SetAfterDrawFunc(func(tcell.Screen) {
		select {
		case painted <- struct{}{}:
		default:
		}
	})
	app.SetRunning(true)
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	t.Cleanup(func() {
		app.requestExit(0)
		app.Application.Stop()
		app.SetRunning(false)
		select {
		case err := <-finished:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("investigation dispatcher did not stop")
		}
	})
	select {
	case <-painted:
	case <-time.After(2 * time.Second):
		t.Fatal("investigation dispatcher did not paint its first frame")
	}
}

func TestInvestigationAcceptanceRetainedExportSurvivesPresentationAndFailedRefresh(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	d, dynamic, typed := investigationAcceptanceFixture(t)
	original := d.snapshot
	payload, err := json.Marshal(original.Investigation)
	require.NoError(t, err)
	encoded, markdown := investigationAcceptanceExport(t, d)
	for _, full := range []string{"second full source line", "readiness continuation", "previous continuation", "event continuation", string(original.UID), original.CapturedAt.UTC().Format(time.RFC3339)} {
		require.Contains(t, string(encoded), full)
	}
	screen := investigationAcceptanceScreen(t, d.app)
	investigationAcceptanceDraw(t, d.app)
	investigationAcceptanceRun(t, d.app)
	var attempts atomic.Int32
	investigationAcceptanceUpdate(t, d.app, func() {
		d.snapshotLoader = func(context.Context, SelectedResourceTarget) (inspectionSnapshot, error) {
			attempts.Add(1)
			return inspectionSnapshot{}, errors.New("fixture permission denied")
		}
	})
	for _, key := range []rune{'2', '3', '5'} {
		d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
	}
	investigationAcceptanceAwait(t, d.app, func() bool { return d.activeTab == investigationEvidenceTab })
	investigationAcceptanceAcceptSearch(t, d, investigationAcceptanceQuery)
	investigationAcceptanceUpdate(t, d.app, func() { d.text.ScrollTo(4, 0) })
	var styleError error
	investigationAcceptanceUpdate(t, d.app, func() {
		styleError = d.app.Styles.Load("../../skins/monochrome.yaml", false)
		d.app.Styles.Update()
	})
	require.NoError(t, styleError)
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.refreshFailure != "" })
	investigationAcceptanceUpdate(t, d.app, func() {})
	require.EqualValues(t, 1, attempts.Load(), "only the explicit r key may start a new observation")
	require.Empty(t, dynamic.Actions())
	require.Empty(t, typed.Actions())
	require.Equal(t, original, d.snapshot)
	require.Equal(t, original.UID, d.target.UID)
	require.Equal(t, investigationAcceptanceQuery, d.inspectionQuery)
	require.Contains(t, d.identityBar.GetText(true), "Refresh failed")
	require.Contains(t, d.text.GetText(true), "RETAINED SNAPSHOT")
	require.Contains(t, d.text.GetText(true), "event continuation")
	require.Contains(t, investigationAcceptanceFrame(screen), "Refresh failed")
	actualPayload, err := json.Marshal(d.snapshot.Investigation)
	require.NoError(t, err)
	require.True(t, bytes.Equal(payload, actualPayload), "display changes must not mutate retained typed evidence")
	actualEncoded, actualMarkdown := investigationAcceptanceExport(t, d)
	require.Equal(t, encoded, actualEncoded, "JSON export must retain complete original evidence, identity, source, time and limits")
	require.Equal(t, markdown, actualMarkdown, "Markdown export must retain the same original evidence")
}

func investigationAcceptanceAcceptSearch(t *testing.T, d *inspectionDetails, query string) {
	t.Helper()
	for _, character := range "/" + query {
		d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, character, tcell.ModNone))
	}
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool {
		return d.inspectionQuery == query && !d.cmdBuff.IsActive()
	})
}

func investigationAcceptanceNativeSearch(t *testing.T, d *inspectionDetails) {
	t.Helper()
	probe := &investigationAcceptanceCompletionProbe{completed: make(chan string, 16)}
	d.cmdBuff.AddListener(probe)
	defer d.cmdBuff.RemoveListener(probe)
	var capturedPayload []byte
	var beforeRow, beforeColumn int
	var err error
	investigationAcceptanceUpdate(t, d.app, func() {
		capturedPayload, err = json.Marshal(d.snapshot)
		beforeRow, beforeColumn = d.text.GetScrollOffset()
	})
	require.NoError(t, err)
	// Type through the real prompt, then wait for its actual delayed completion.
	// Editing a draft must not change the previously accepted per-tab query.
	for _, key := range []rune{'/', 'f', 'u', 'l', 'l'} {
		d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
	}
	investigationAcceptanceAwait(t, d.app, func() bool { return d.cmdBuff.IsActive() && d.cmdBuff.GetText() == investigationAcceptanceQuery })
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	completed := false
	for !completed {
		select {
		case text := <-probe.completed:
			completed = text == investigationAcceptanceQuery
		case <-deadline.C:
			t.Fatal("native inspection draft never produced its delayed completion")
		}
	}
	var draftQuery string
	var draftPayload []byte
	var draftRow, draftColumn int
	investigationAcceptanceUpdate(t, d.app, func() {
		draftRow, draftColumn = d.text.GetScrollOffset()
		draftQuery = d.inspectionQuery
		draftPayload, err = json.Marshal(d.snapshot)
	})
	require.NoError(t, err)
	require.Empty(t, draftQuery, "delayed completion must not commit an investigation draft")
	require.Equal(t, beforeRow, draftRow, "a draft must retain the accepted vertical viewport")
	require.Equal(t, beforeColumn, draftColumn)
	require.Equal(t, capturedPayload, draftPayload, "typing and delayed completion must retain the captured payload")
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.inspectionQuery == investigationAcceptanceQuery && !d.cmdBuff.IsActive() })
	investigationAcceptanceInvalidSearch(t, d, capturedPayload)
}

func investigationAcceptanceInvalidSearch(t *testing.T, d *inspectionDetails, capturedPayload []byte) {
	t.Helper()
	var entered atomic.Int32
	var capture func(*tcell.EventKey) *tcell.EventKey
	investigationAcceptanceUpdate(t, d.app, func() {
		capture = d.app.GetInputCapture()
		d.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEnter {
				entered.Add(1)
			}
			if capture != nil {
				return capture(event)
			}
			return event
		})
	})
	defer investigationAcceptanceUpdate(t, d.app, func() { d.app.SetInputCapture(capture) })
	var draftPayload []byte
	var err error
	// An invalid new draft stays editable and keeps the accepted search and
	// captured evidence. Correct it through native keys before continuing.
	for _, key := range []rune{'/', '['} {
		d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
	}
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool {
		return entered.Load() >= 1 && d.cmdBuff.IsActive() && d.cmdBuff.GetText() == "["
	})
	var accepted, promptText string
	investigationAcceptanceUpdate(t, d.app, func() {
		accepted, promptText = d.inspectionQuery, d.app.Prompt().GetText(true)
		draftPayload, err = json.Marshal(d.snapshot)
	})
	require.NoError(t, err)
	require.Equal(t, investigationAcceptanceQuery, accepted)
	require.Contains(t, promptText, "[", "invalid input must remain visible for correction")
	require.Equal(t, capturedPayload, draftPayload, "invalid Enter must retain the original payload")
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModNone))
	for _, key := range []rune{'f', 'u', 'l', 'l'} {
		d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
	}
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.inspectionQuery == investigationAcceptanceQuery && !d.cmdBuff.IsActive() })
}

func TestInvestigationAcceptanceTypedRelatedEscRetainsTabSearchAndViewport(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	d, dynamic, typed := investigationAcceptanceFixture(t)
	screen := investigationAcceptanceScreen(t, d.app)
	investigationAcceptanceRun(t, d.app)
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, '5', tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.activeTab == investigationEvidenceTab })
	investigationAcceptanceNativeSearch(t, d)
	investigationAcceptanceUpdate(t, d.app, func() { d.text.ScrollTo(4, 0) })
	original := d.snapshot
	row, column := d.text.GetScrollOffset()
	highlights := append([]string(nil), d.text.GetHighlights()...)
	region := d.currentRegion
	require.Positive(t, row)
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.activeTab == 1 && d.inspectionQuery == "" })
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, '5', tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool {
		return d.activeTab == investigationEvidenceTab && d.inspectionQuery == investigationAcceptanceQuery && d.cmdBuff.GetText() == investigationAcceptanceQuery
	})
	var tabRow, tabColumn int
	investigationAcceptanceUpdate(t, d.app, func() { tabRow, tabColumn = d.text.GetScrollOffset() })
	require.Equal(t, row, tabRow, "returning to Evidence must retain its accepted viewport")
	require.Equal(t, column, tabColumn)
	// The actual g action opens the retained source's related picker. Esc must
	// return to the typed tab and accepted query rather than rebuilding Overview.
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'g', tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { _, ok := d.app.Content.Top().(*relatedPicker); return ok })
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.app.Content.Top() == d })
	// Model an already accepted cross-namespace jump using the existing return
	// ownership seam; target read/UID rejection is covered by identity tests.
	var jumpError error
	investigationAcceptanceUpdate(t, d.app, func() {
		jumpError = d.app.switchNS("ops")
		if jumpError != nil {
			return
		}
		d.rememberRelatedDestination(uiAcceptanceNamespace, nil)
		jumpError = d.app.inject(NewDetails(d.app, "Pod", "ops/child", contentInspection, true).Update("Retained related Pod"), false)
	})
	require.NoError(t, jumpError)
	require.Contains(t, investigationAcceptanceFrame(screen), "ns:ops")
	var styleError error
	investigationAcceptanceUpdate(t, d.app, func() {
		styleError = d.app.Styles.Load("../../skins/monochrome.yaml", false)
		d.app.Styles.Update()
	})
	require.NoError(t, styleError)
	d.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	investigationAcceptanceAwait(t, d.app, func() bool { return d.app.Content.Top() == d })
	investigationAcceptanceUpdate(t, d.app, func() {})
	require.Equal(t, uiAcceptanceNamespace, d.app.Config.ActiveNamespace())
	require.Contains(t, investigationAcceptanceFrame(screen), "ns:apps")
	require.Equal(t, investigationEvidenceTab, d.activeTab)
	require.Equal(t, investigationAcceptanceQuery, d.inspectionQuery)
	require.Equal(t, investigationAcceptanceQuery, d.cmdBuff.GetText())
	require.Equal(t, region, d.currentRegion)
	require.Equal(t, highlights, d.text.GetHighlights())
	actualRow, actualColumn := d.text.GetScrollOffset()
	require.Equal(t, row, actualRow)
	require.Equal(t, column, actualColumn)
	require.Equal(t, original, d.snapshot)
	require.Equal(t, original.UID, d.target.UID)
	require.Empty(t, dynamic.Actions(), "Esc and return must not recapture source evidence")
	require.Empty(t, typed.Actions(), "related return must not recollect source events")
}
