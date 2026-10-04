// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/sahilm/fuzzy"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	uiAcceptanceNamespace             = "apps"
	uiAcceptanceCompactHeader         = "compact"
	actionDispatchAcceptanceOldQuery  = "alpha"
	actionDispatchAcceptanceNewQuery  = "beta"
	actionDispatchAcceptanceTextTitle = "Retained text"
)

func actionDispatchAcceptanceApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	cfg := mock.NewMockConfig(t)
	_, err := cfg.ActivateContext("ct-1-1")
	require.NoError(t, err)
	require.NoError(t, cfg.SetActiveNamespace(uiAcceptanceNamespace))
	cfg.K9s.ReadOnly = true
	cfg.K9s.UI.NoIcons, cfg.K9s.UI.Splashless = true, true
	cfg.K9s.UI.HeaderMode = uiAcceptanceCompactHeader
	app := NewApp(cfg)
	require.NoError(t, app.Init("acceptance", 0))
	t.Cleanup(func() { app.requestExit(0) })
	return app
}

// Resolve the descriptor from the current real owner, then check both native
// discovery surfaces before the scenario exercises that owner's actual key.
func actionDispatchAcceptanceAgreement(t *testing.T, app *App, key tcell.Key, label string, available bool) {
	t.Helper()
	owner, ok := app.Content.Top().(actionOwner)
	require.True(t, ok)
	var descriptor ui.ActionDescriptor
	found := false
	for _, item := range actionCatalog(owner, app) {
		if item.Key == key && item.Label == label {
			descriptor, found = item, true
			break
		}
	}
	require.True(t, found, "current owner must advertise %s", label)
	require.Equal(t, available, descriptor.Available(), label)
	help := NewHelp(app)
	require.NoError(t, help.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	defer help.Stop()
	var rows strings.Builder
	for row := range help.GetRowCount() {
		for column := range help.GetColumnCount() {
			rows.WriteString(help.GetCell(row, column).Text)
			rows.WriteByte(' ')
		}
		rows.WriteByte('\n')
	}
	require.Contains(t, rows.String(), label, "Help must retain the actual mode label")
	require.Contains(t, rows.String(), ui.ToMnemonic(descriptor.Shortcut))
	if !available {
		require.Contains(t, rows.String(), descriptor.UnavailableReason)
	}
	app.actionsCmd(nil)
	palette, ok := app.Content.GetPrimitive(actionsCommand).(*actionPalette)
	require.True(t, ok)
	found = false
	for _, entry := range palette.entries {
		if entry.action.ID != descriptor.ID || entry.action.Shortcut != descriptor.Shortcut {
			continue
		}
		found = true
		require.Equal(t, descriptor.Label, entry.action.Label)
		require.Equal(t, descriptor.UnavailableReason, entry.action.UnavailableReason)
		require.Contains(t, entry.label, descriptor.Label)
		break
	}
	require.True(t, found, "Actions must expose the same owned binding")
	palette.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) { app.SetFocus(primitive) })
	require.Equal(t, owner, app.Content.Top(), "discovery must preserve ownership")
}

func actionDispatchAcceptanceKey(app *App, key tcell.Key, character rune) {
	app.QueueEvent(tcell.NewEventKey(key, character, tcell.ModNone))
}

func actionDispatchAcceptancePromptQuestion(t *testing.T, app *App, unchanged func() bool) {
	t.Helper()
	for _, character := range ":pods?" {
		actionDispatchAcceptanceKey(app, tcell.KeyRune, character)
	}
	investigationAcceptanceAwait(t, app, func() bool { return app.CmdBuff().GetText() == "pods?" })
	var promptActive, ownerUnchanged bool
	investigationAcceptanceUpdate(t, app, func() {
		promptActive, ownerUnchanged = app.Prompt().InCmdMode(), unchanged()
	})
	require.True(t, promptActive)
	require.True(t, ownerUnchanged, "? typed in the application command must not open local or global Help")
	actionDispatchAcceptanceKey(app, tcell.KeyEscape, 0)
	investigationAcceptanceAwait(t, app, func() bool { return !app.Prompt().InCmdMode() })
}

func TestActionDispatchAcceptanceLogsLocalHelpAndCommandEditing(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	logs := NewLog(client.PodGVR, &dao.LogOptions{Path: "apps/api", Container: "api"})
	logs.sessionStale = true // Retained-only lifecycle; no Kubernetes log read.
	require.NoError(t, app.inject(logs, false))
	t.Cleanup(logs.Stop)
	seedWorkbench(logs.workbench)
	actionDispatchAcceptanceAgreement(t, app, ui.KeyHelp, "Help", true)
	actionDispatchAcceptanceAgreement(t, app, ui.KeyS, "Freeze/Resume", true)
	_, spaceBound := logs.Actions().Get(ui.KeySpace)
	require.False(t, spaceBound, "Logs must not invent Space as Freeze/Resume")
	screen := investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	var first string
	investigationAcceptanceUpdate(t, app, func() { first = investigationAcceptanceFrame(screen) })
	for _, text := range []string{"Actions", "Help", "Inspect entry"} {
		require.Contains(t, first, text, "first 80x24 Logs frame:\n%s", first)
	}
	var selected uint64
	investigationAcceptanceUpdate(t, app, func() { selected = logs.workbench.selected })
	actionDispatchAcceptanceKey(app, tcell.KeyRune, '?')
	investigationAcceptanceAwait(t, app, func() bool { return logs.workbench.mode == "help" })
	var localHelp string
	investigationAcceptanceUpdate(t, app, func() { localHelp = logs.workbench.detail.GetText(true) })
	require.Contains(t, localHelp, "Log workbench actions")
	require.Contains(t, localHelp, "Freeze/Resume")
	actionDispatchAcceptanceKey(app, tcell.KeyEscape, 0)
	investigationAcceptanceAwait(t, app, func() bool { return logs.workbench.mode == modeEntries })
	actionDispatchAcceptancePromptQuestion(t, app, func() bool {
		return app.Content.Top() == logs && logs.workbench.mode == modeEntries && logs.workbench.selected == selected
	})
	// s is the advertised real freeze binding. Help has already frozen this
	// snapshot, so s must resume the retained projection without a new source.
	actionDispatchAcceptanceKey(app, tcell.KeyRune, 's')
	investigationAcceptanceAwait(t, app, func() bool { return logs.workbench.follow })
}

func TestActionDispatchAcceptanceHubbleLocalHelpAndCommandEditing(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	flows := newHubbleView(hubble.Scope{Pods: []string{"apps/api"}}, false)
	flows.session = hubble.NewSession(hubble.Config{}, flows.scope, hubble.Query{}, 4)
	flows.session.Store.Add(hubble.Event{Source: hubble.Peer{Pod: "apps/api", Kind: "pod"}, Destination: hubble.Peer{IP: "1.1.1.1", Kind: "world"}, Verdict: "FORWARDED"})
	require.NoError(t, app.inject(flows, false))
	t.Cleanup(flows.Stop)
	actionDispatchAcceptanceAgreement(t, app, ui.KeyHelp, "Help", true)
	actionDispatchAcceptanceAgreement(t, app, tcell.KeyEnter, "Inspect", true)
	_, spaceBound := flows.Actions().Get(ui.KeySpace)
	require.False(t, spaceBound)
	screen := investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	var first string
	investigationAcceptanceUpdate(t, app, func() { first = investigationAcceptanceFrame(screen) })
	for _, text := range []string{"Actions", "Help", "Inspect"} {
		require.Contains(t, first, text, "first 80x24 Hubble frame:\n%s", first)
	}
	actionDispatchAcceptanceKey(app, tcell.KeyRune, '?')
	investigationAcceptanceAwait(t, app, func() bool { return flows.mode == hubbleHelpMode })
	actionDispatchAcceptanceKey(app, tcell.KeyEscape, 0)
	investigationAcceptanceAwait(t, app, func() bool { return flows.mode == hubblePeersMode })
	actionDispatchAcceptancePromptQuestion(t, app, func() bool { return app.Content.Top() == flows && flows.mode == hubblePeersMode })
	actionDispatchAcceptanceKey(app, tcell.KeyEnter, 0)
	investigationAcceptanceAwait(t, app, func() bool { return flows.mode == hubbleConversationMode && len(flows.rows) == 1 })
}

// The production primitives and their key routers remain unchanged. Only the
// background observation lifecycle is replaced for these retained fixtures.
type actionDispatchAcceptancePulse struct{ *Pulse }

func (*actionDispatchAcceptancePulse) Start() {}

type actionDispatchAcceptanceBrowser struct{ *Browser }

func (b *actionDispatchAcceptanceBrowser) Init(ctx context.Context) error {
	if err := b.Table.Init(ctx); err != nil {
		return err
	}
	data := makeTableData()
	data.RowsRange(func(index int, event model1.RowEvent) bool {
		event.Row.ID = event.Row.Fields[1]
		data.SetRow(index, event)
		return true
	})
	b.SetModel(&retainedFilterModel{data: data})
	b.Refresh()
	return nil
}
func (*actionDispatchAcceptanceBrowser) Start()  {}
func (b *actionDispatchAcceptanceBrowser) Stop() { b.Table.Stop() }

func TestActionDispatchAcceptanceWorkspaceKeysAndPrompt(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	store := workspace.Store{Version: 1, Active: "application", Scopes: []workspace.Scope{{Name: "application", Context: app.Config.ActiveContextName(), Namespaces: []string{uiAcceptanceNamespace}, Kinds: []string{"pods"}}}}
	path := filepath.Join(t.TempDir(), "workspaces.yaml")
	require.NoError(t, workspace.SaveStore(path, store))
	w := newDailyWorkspace(app, store, inventoryCommand, "")
	w.path = path
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Resources: []workspace.Resource{{Ref: workspace.ResourceRef{GVR: "v1/pods", Namespace: uiAcceptanceNamespace, Name: "api", UID: "api-uid"}, Kind: "Pod", Summary: "Ready"}}, Coverage: []workspace.Coverage{{GVR: "v1/pods", Namespace: uiAcceptanceNamespace, State: "denied", Detail: "list permission unavailable"}}}
	w.coverage = w.snapshot.Coverage
	require.NoError(t, app.inject(w, false))
	t.Cleanup(w.Stop)
	actionDispatchAcceptanceAgreement(t, app, tcell.KeyTab, "Next workspace tab", true)
	screen := investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	actionDispatchAcceptanceKey(app, tcell.KeyTab, 0)
	investigationAcceptanceAwait(t, app, func() bool { return w.mode == dailyWorkspaceCoverageMode })
	// Discovery is reconstructed only on the UI thread after the mode change.
	var label string
	investigationAcceptanceUpdate(t, app, func() {
		for _, descriptor := range actionCatalog(w, app) {
			if descriptor.Key == tcell.KeyEnter {
				label = descriptor.Label
			}
		}
	})
	require.Equal(t, "Retained evidence", label)
	var first string
	investigationAcceptanceUpdate(t, app, func() { first = investigationAcceptanceFrame(screen) })
	for _, text := range []string{"Actions", "Help", "Retained evidence", "denied"} {
		require.Contains(t, first, text, "first Coverage viewport:\n%s", first)
	}
	actionDispatchAcceptanceKey(app, tcell.KeyEnter, 0)
	investigationAcceptanceAwait(t, app, func() bool { return app.Content.IsTopDialog() })
	actionDispatchAcceptanceKey(app, tcell.KeyEscape, 0)
	investigationAcceptanceAwait(t, app, func() bool { return !app.Content.IsTopDialog() && app.Content.Top() == w })
	for _, character := range "/api?" {
		actionDispatchAcceptanceKey(app, tcell.KeyRune, character)
	}
	investigationAcceptanceAwait(t, app, func() bool { return w.prompting && w.prompt.GetText() == "api?" })
	actionDispatchAcceptanceKey(app, tcell.KeyEscape, 0)
	investigationAcceptanceAwait(t, app, func() bool { return !w.prompting && w.query == "" && w.mode == dailyWorkspaceCoverageMode })
}

func TestActionDispatchAcceptanceDesiredReviewKeys(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	w := newDesiredReviewView(app, review.Scope{Context: app.Config.ActiveContextName(), Namespaces: []string{uiAcceptanceNamespace}, DefaultNamespace: uiAcceptanceNamespace}, filepath.Join(t.TempDir(), "retained.yaml"))
	at := time.Now().UTC()
	w.source = review.Source{Identity: review.SourceIdentity{Path: w.path, SHA256: "retained-source-sha", LoadedAt: at, Documents: 1}}
	entry := desiredReviewFixtureEntry("api", review.StateChanged, at)
	entry.Identity.Context, entry.Identity.Namespace = app.Config.ActiveContextName(), uiAcceptanceNamespace
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at, entry), nil)
	require.NoError(t, app.inject(w, false))
	t.Cleanup(w.Stop)
	actionDispatchAcceptanceAgreement(t, app, tcell.KeyEnter, "Review detail", true)
	screen := investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	var first string
	investigationAcceptanceUpdate(t, app, func() { first = investigationAcceptanceFrame(screen) })
	for _, text := range []string{"Actions", "Help", "Review detail", "api"} {
		require.Contains(t, first, text, first)
	}
	actionDispatchAcceptanceKey(app, tcell.KeyEnter, 0)
	investigationAcceptanceAwait(t, app, func() bool { return w.detailOpen })
	var detail string
	investigationAcceptanceUpdate(t, app, func() { detail = w.detail.GetText(true) })
	require.Contains(t, detail, "example/api:v1")
	require.Contains(t, detail, "example/api:v2")
	actionDispatchAcceptanceKey(app, tcell.KeyEscape, 0)
	investigationAcceptanceAwait(t, app, func() bool { return !w.detailOpen && app.Content.Top() == w })
	var retained review.Snapshot
	investigationAcceptanceUpdate(t, app, func() { retained = w.snapshot })
	require.Equal(t, entry.Identity.UID, retained.Entries[0].Identity.UID)
	require.Equal(t, at, retained.Entries[0].ObservedAt)
}

func TestActionDispatchAcceptancePulseTabs(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	p := &actionDispatchAcceptancePulse{Pulse: NewPulse(client.PuGVR).(*Pulse)}
	require.NoError(t, app.inject(p, false))
	t.Cleanup(p.Stop)
	actionDispatchAcceptanceAgreement(t, app, tcell.KeyTab, "Next", true)
	actionDispatchAcceptanceAgreement(t, app, tcell.KeyEnter, "Goto", true)
	screen := investigationAcceptanceScreen(t, app)
	app.SetFocus(p.charts[p.chartGVRs[p.selectedIndex]])
	investigationAcceptanceDraw(t, app)
	first := investigationAcceptanceFrame(screen)
	for _, text := range []string{"Actions", "Help", "Enter browse", "Selected: pods"} {
		require.Contains(t, first, text, first)
	}
	prior := p.selectedIndex
	p.charts[p.chartGVRs[prior]].InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), func(primitive tview.Primitive) { app.SetFocus(primitive) })
	require.NotEqual(t, prior, p.selectedIndex)
	require.Equal(t, p.charts[p.chartGVRs[p.selectedIndex]], app.GetFocus())
	p.charts[p.chartGVRs[p.selectedIndex]].InputHandler()(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone), func(primitive tview.Primitive) { app.SetFocus(primitive) })
	require.Equal(t, prior, p.selectedIndex, "the native Backtab override must reverse Tab")
	// Browser discovery/collection is deliberately outside this bounded
	// retained fixture: this scenario establishes Tab, not Goto completion.
}

func TestActionDispatchAcceptanceTableSpaceAndFilter(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	b := &actionDispatchAcceptanceBrowser{Browser: &Browser{Table: NewTable(client.NewGVR("test")), meta: &metav1.APIResource{Kind: "Test"}, cancelFn: func() {}}}
	require.NoError(t, app.inject(b, false))
	t.Cleanup(b.Stop)
	actionDispatchAcceptanceAgreement(t, app, ui.KeySpace, "Mark", true)
	actionDispatchAcceptanceAgreement(t, app, ui.KeySlash, "Filter Mode", true)
	investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	var original string
	investigationAcceptanceUpdate(t, app, func() { original = b.GetSelectedItem() })
	require.NotEmpty(t, original)
	actionDispatchAcceptanceKey(app, tcell.KeyRune, ' ')
	investigationAcceptanceAwait(t, app, func() bool { return b.IsMarked(original) })
	for _, character := range "/r2 " {
		actionDispatchAcceptanceKey(app, tcell.KeyRune, character)
	}
	investigationAcceptanceAwait(t, app, func() bool { return b.CmdBuff().IsActive() && b.CmdBuff().GetText() == "r2 " })
	var markRetained bool
	investigationAcceptanceUpdate(t, app, func() { markRetained = b.IsMarked(original) })
	require.True(t, markRetained, "Space while editing must extend the query, not toggle the selected mark")
	actionDispatchAcceptanceKey(app, tcell.KeyEnter, 0)
	investigationAcceptanceAwait(t, app, func() bool { return !b.CmdBuff().IsActive() && b.GetSelectedItem() == "r2" })
}

func TestActionDispatchAcceptanceComparisonWaitsForAAndPaletteCapturesB(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR, Namespace: uiAcceptanceNamespace, Name: "api", UID: "chosen-a"}
	v := &comparisonView{Details: NewDetails(app, "Resource comparison", target.Path(), contentInspection, true), target: target, normalize: true}
	at := time.Now().UTC()
	identity := inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}
	baseline := inspect.NewObservation(identity, "retained fixture", at, map[string]any{"spec": map[string]any{"replicas": int64(1)}})
	identity.UID = "replacement-b"
	other := inspect.NewObservation(identity, "retained fixture", at.Add(time.Minute), map[string]any{"spec": map[string]any{"replicas": int64(2)}})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var aReads, bReads atomic.Int32
	v.loader = func(ctx context.Context, captureA bool) inspect.Observation {
		if !captureA {
			bReads.Add(1)
			return other
		}
		aReads.Add(1)
		select {
		case <-release:
			return baseline
		case <-ctx.Done():
			return inspect.Observation{}
		}
	}
	require.NoError(t, app.inject(v, false))
	t.Cleanup(v.Stop)
	actionDispatchAcceptanceAgreement(t, app, ui.KeyR, "Capture B (keep A)", false)
	screen := investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	var noBaseline bool
	investigationAcceptanceUpdate(t, app, func() {
		v.text.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone), func(primitive tview.Primitive) { app.SetFocus(primitive) })
		noBaseline = v.baseline == nil
	})
	require.True(t, noBaseline)
	require.Zero(t, bReads.Load(), "r cannot capture B before chosen A completes")
	unblock()
	investigationAcceptanceAwait(t, app, func() bool { return v.baseline != nil })
	var first string
	investigationAcceptanceUpdate(t, app, func() { first = investigationAcceptanceFrame(screen) })
	for _, text := range []string{"Actions", "Help", "A captured; r capture B"} {
		require.Contains(t, first, text, first)
	}
	var primary string
	investigationAcceptanceUpdate(t, app, func() { primary = app.Menu().GetCell(0, 0).Text })
	require.Contains(t, primary, "Capture B", "the next staged action must be discoverable in compact chrome")
	actionDispatchAcceptanceKey(app, tcell.KeyCtrlO, 0)
	investigationAcceptanceAwait(t, app, func() bool { return app.Content.GetPrimitive(actionsCommand) != nil })
	var chosen bool
	investigationAcceptanceUpdate(t, app, func() {
		palette := app.Content.GetPrimitive(actionsCommand).(*actionPalette)
		for index, entry := range palette.entries {
			if entry.action.Key == ui.KeyR && entry.action.Label == "Capture B (keep A)" && entry.action.Available() {
				palette.SetCurrentItem(index)
				chosen = true
				break
			}
		}
	})
	require.True(t, chosen)
	actionDispatchAcceptanceKey(app, tcell.KeyEnter, 0)
	investigationAcceptanceAwait(t, app, func() bool { return v.other.Identity.UID == "replacement-b" })
	var retainedA inspect.Observation
	investigationAcceptanceUpdate(t, app, func() { retainedA = *v.baseline })
	require.Equal(t, baseline, retainedA)
	require.EqualValues(t, 1, aReads.Load())
	require.EqualValues(t, 1, bReads.Load())
}

type actionDispatchAcceptanceFilterProbe struct{ filtered chan []string }

func (*actionDispatchAcceptanceFilterProbe) TextChanged([]string) {}
func (p *actionDispatchAcceptanceFilterProbe) TextFiltered(_ []string, matches fuzzy.Matches) {
	lines := make([]string, len(matches))
	for index, match := range matches {
		lines[index] = match.Str
	}
	select {
	case p.filtered <- lines:
	default:
	}
}

func actionDispatchAcceptanceNoMatch(t *testing.T, probe *actionDispatchAcceptanceFilterProbe, unwanted string) {
	t.Helper()
	deadline := time.NewTimer(150 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case lines := <-probe.filtered:
			require.NotContains(t, lines, unwanted, "a delayed completion must not mutate this retained text model")
		case <-deadline.C:
			return
		}
	}
}

func actionDispatchAcceptanceMatch(t *testing.T, probe *actionDispatchAcceptanceFilterProbe, expected string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case lines := <-probe.filtered:
			if len(lines) > 0 {
				require.Equal(t, []string{expected}, lines)
				return
			}
		case <-deadline.C:
			t.Fatal("native delayed search never reached the current text model")
		}
	}
}

// Pause only the UI dispatcher after native typing. The real key-entry worker
// still completes, making off-dispatch filtering and stale replay observable.
func actionDispatchAcceptanceDelayedSearch(t *testing.T, app *App, d *Details, mutate func()) *actionDispatchAcceptanceFilterProbe {
	t.Helper()
	probe := &actionDispatchAcceptanceFilterProbe{filtered: make(chan []string, 16)}
	d.model.AddListener(probe)
	completion := &investigationAcceptanceCompletionProbe{completed: make(chan string, 16)}
	d.cmdBuff.AddListener(completion)
	t.Cleanup(func() { d.cmdBuff.RemoveListener(completion) })
	actionDispatchAcceptanceKey(app, tcell.KeyRune, '/')
	investigationAcceptanceAwait(t, app, func() bool { return d.cmdBuff.IsActive() })
	release, finished := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		app.Application.QueueUpdateDraw(func() {
			for _, character := range actionDispatchAcceptanceOldQuery {
				app.Prompt().InputHandler()(tcell.NewEventKey(tcell.KeyRune, character, tcell.ModNone), func(tview.Primitive) {})
			}
			<-release
			if mutate != nil {
				mutate()
			}
		})
		close(finished)
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	completed := false
	for !completed {
		select {
		case text := <-completion.completed:
			completed = text == actionDispatchAcceptanceOldQuery
		case <-deadline.C:
			t.Fatal("native search did not produce its real delayed completion")
		}
	}
	actionDispatchAcceptanceNoMatch(t, probe, actionDispatchAcceptanceOldQuery)
	unblock()
	select {
	case <-finished:
	case <-deadline.C:
		t.Fatal("native UI dispatcher did not resume")
	}
	return probe
}

func TestActionDispatchAcceptanceDetailsDebounceUsesCurrentDispatcherOwner(t *testing.T) {
	for _, kind := range []string{"ordinary text", "comparison wrapper"} {
		t.Run(kind, func(t *testing.T) {
			app := actionDispatchAcceptanceApp(t)
			d := NewDetails(app, actionDispatchAcceptanceTextTitle, "", contentTXT, true)
			if kind == "comparison wrapper" {
				target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR, Namespace: uiAcceptanceNamespace, Name: "api", UID: "chosen-a"}
				baseline := inspect.Observation{}
				v := &comparisonView{Details: d, target: target, baseline: &baseline}
				require.NoError(t, app.inject(v, false))
				t.Cleanup(v.Stop)
			} else {
				require.NoError(t, app.inject(d, false))
			}
			d.Update("alpha\nbeta")
			investigationAcceptanceScreen(t, app)
			investigationAcceptanceRun(t, app)
			probe := actionDispatchAcceptanceDelayedSearch(t, app, d, nil)
			actionDispatchAcceptanceMatch(t, probe, actionDispatchAcceptanceOldQuery)
			investigationAcceptanceAwait(t, app, func() bool { return d.maxRegions == 1 })
		})
	}
}

func TestActionDispatchAcceptanceDetailsRejectsDelayedStaleText(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	d := NewDetails(app, actionDispatchAcceptanceTextTitle, "", contentTXT, true)
	require.NoError(t, app.inject(d, false))
	d.Update("alpha\nbeta")
	investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	probe := actionDispatchAcceptanceDelayedSearch(t, app, d, func() {
		handler := app.Prompt().InputHandler()
		handler(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone), func(tview.Primitive) {})
		for _, character := range actionDispatchAcceptanceNewQuery {
			handler(tcell.NewEventKey(tcell.KeyRune, character, tcell.ModNone), func(tview.Primitive) {})
		}
		handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
	})
	actionDispatchAcceptanceMatch(t, probe, actionDispatchAcceptanceNewQuery)
	actionDispatchAcceptanceNoMatch(t, probe, actionDispatchAcceptanceOldQuery)
	investigationAcceptanceAwait(t, app, func() bool { return d.cmdBuff.GetText() == actionDispatchAcceptanceNewQuery && !d.cmdBuff.IsActive() })
}

func TestActionDispatchAcceptanceDetailsRejectsDelayedPreviousOwner(t *testing.T) {
	app := actionDispatchAcceptanceApp(t)
	d := NewDetails(app, "Previous retained text", "", contentTXT, true)
	require.NoError(t, app.inject(d, false))
	d.Update("alpha\nbeta")
	investigationAcceptanceScreen(t, app)
	investigationAcceptanceRun(t, app)
	next := NewDetails(app, "Current retained text", "", contentTXT, true)
	var navigationErr error
	probe := actionDispatchAcceptanceDelayedSearch(t, app, d, func() {
		navigationErr = app.inject(next, false)
		next.Update("new owner evidence")
	})
	require.NoError(t, navigationErr)
	actionDispatchAcceptanceNoMatch(t, probe, actionDispatchAcceptanceOldQuery)
	var regions int
	var current bool
	investigationAcceptanceUpdate(t, app, func() {
		current = app.Content.Top() == next
		regions = d.maxRegions
	})
	require.True(t, current)
	require.Zero(t, regions, "a stopped previous owner must retain its unfiltered projection")
	// The old buffer still contains alpha: this specifically needs an owner
	// guard, independently of the stale-text guard exercised in the other case.
	require.Equal(t, actionDispatchAcceptanceOldQuery, d.cmdBuff.GetText())
}
