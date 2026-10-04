// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/types"
)

const (
	testWorkspaceName            = "application"
	testWorkspaceNamespace       = "backend"
	testWorkspaceCrashLoopReason = "CrashLoopBackOff"
	testWorkspaceAPIName         = "api"
)

func dailyWorkspaceFixture() *dailyWorkspace {
	app := &App{App: &ui.App{Configurator: ui.Configurator{Styles: config.NewStyles()}}}
	return &dailyWorkspace{
		Flex: tview.NewFlex(), app: app,
		table:  tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		header: tview.NewTextView().SetDynamicColors(true), detail: tview.NewTextView().SetDynamicColors(true),
		footer: tview.NewTextView().SetDynamicColors(true), prompt: tview.NewInputField(),
		store:       workspace.Store{Version: 1, Active: testWorkspaceName},
		scope:       workspace.Scope{Name: testWorkspaceName, Context: "development", Namespaces: []string{"frontend", testWorkspaceNamespace}, Kinds: []string{"pods"}},
		contextName: "development", mode: "inventory",
	}
}
func workspaceFixtureRef(name string) workspace.ResourceRef {
	return workspace.ResourceRef{GVR: "v1/pods", Namespace: testWorkspaceNamespace, Name: name, UID: name + "-uid"}
}

func TestDailyWorkspaceSearchStaysLocalAndRejectsInvalidFields(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: testWorkspaceCrashLoopReason}, {Ref: workspaceFixtureRef("worker"), Kind: "Pod", Summary: "Ready"}}}
	if !w.applyQuery("kind:POD ns:backend status:crash") || len(w.rows) != 1 || w.rows[0].ref.Name != testWorkspaceAPIName {
		t.Fatalf("search: %#v", w.rows)
	}
	if w.applyQuery("namespace:frontend") {
		t.Fatal("invalid structured query accepted")
	}
	if w.query != "kind:POD ns:backend status:crash" || len(w.rows) != 1 || w.rows[0].ref.Name != testWorkspaceAPIName {
		t.Fatal("invalid search widened prior result")
	}
	if !strings.Contains(w.notice, "prior search retained") {
		t.Fatal(w.notice)
	}
	for _, query := range []string{"ns:", "kind:", "status:", "name:", "unknown:x"} {
		if _, err := parseDailyWorkspaceQuery(query); err == nil {
			t.Fatalf("invalid query accepted: %s", query)
		}
	}
}
func TestDailyWorkspaceRefreshRetainsObservationAndTimestampOnFailure(t *testing.T) {
	w := dailyWorkspaceFixture()
	observed := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	w.snapshot = workspace.Snapshot{ObservedAt: observed, Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: "Ready"}}}
	w.acceptSnapshot(workspace.Snapshot{ObservedAt: observed.Add(time.Minute), Coverage: []workspace.Coverage{{GVR: "v1/pods", Namespace: testWorkspaceNamespace, State: "denied", Detail: "list forbidden"}}}, nil)
	if !w.snapshot.ObservedAt.Equal(observed) || len(w.snapshot.Resources) != 1 {
		t.Fatal("failed refresh replaced retained observation")
	}
	if len(w.coverage) != 1 || w.coverage[0].State != "denied" || !strings.Contains(w.notice, "retained") {
		t.Fatal("latest coverage failure was hidden")
	}
	w.acceptSnapshot(workspace.Snapshot{ObservedAt: observed.Add(2 * time.Minute)}, errors.New("deadline exceeded"))
	if !w.snapshot.ObservedAt.Equal(observed) || !strings.Contains(w.notice, "deadline exceeded") {
		t.Fatal("deadline lost snapshot or failure")
	}
	w.acceptSnapshot(workspace.Snapshot{ObservedAt: observed.Add(3 * time.Minute), Coverage: []workspace.Coverage{{State: dailyWorkspaceCoverageComplete}}}, nil)
	if !w.snapshot.ObservedAt.Equal(observed.Add(3 * time.Minute)) {
		t.Fatal("successful refresh was rejected")
	}
}
func TestDailyWorkspacePartialRefreshUpdatesSuccessfulReadsWithPersistentRBACGap(t *testing.T) {
	w := dailyWorkspaceFixture()
	observed := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	w.snapshot = workspace.Snapshot{ObservedAt: observed, Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: testWorkspaceCrashLoopReason}}}
	fresh := workspace.Snapshot{ObservedAt: observed.Add(time.Minute), Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: "Ready"}}, Coverage: []workspace.Coverage{{GVR: "v1/pods", Namespace: testWorkspaceNamespace, State: dailyWorkspaceCoverageComplete}, {GVR: "networking.k8s.io/v1/ingresses", Namespace: testWorkspaceNamespace, State: "denied"}}}
	w.acceptSnapshot(fresh, nil)
	if !w.snapshot.ObservedAt.Equal(fresh.ObservedAt) || w.snapshot.Resources[0].Summary != "Ready" || !strings.Contains(w.notice, "Partial observation") {
		t.Fatal("successful reads stayed stale behind unrelated denial")
	}
	if len(w.coverage) != 2 || w.coverage[1].State != "denied" {
		t.Fatal("coverage gap disappeared")
	}
}
func TestDailyWorkspaceLifecycleRejectsLateAndWrongContextUpdates(t *testing.T) {
	cases := []struct {
		generation, current     uint64
		context, currentContext string
		top, want               bool
	}{{2, 2, "dev", "dev", true, true}, {1, 2, "dev", "dev", true, false}, {2, 2, "dev", "prod", true, false}, {2, 2, "dev", "dev", false, false}}
	for _, tc := range cases {
		if got := dailyWorkspaceAccepts(tc.generation, tc.current, tc.context, tc.currentContext, tc.top); got != tc.want {
			t.Fatalf("guard accepted stale observation: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &dailyWorkspace{cancel: cancel, generation: 3}
	w.Stop()
	if ctx.Err() != context.Canceled || w.cancel != nil || w.generation != 4 {
		t.Fatal("leaving workspace did not cancel and invalidate refresh")
	}
}
func TestDailyWorkspaceSelectionRetainsCapturedIdentityAcrossRefresh(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: "Ready"}, {Ref: workspaceFixtureRef("worker"), Kind: "Pod", Summary: "Ready"}}}
	w.render()
	w.table.Select(2, 0)
	// A reordered observation and changed status preserve the identity selected by
	// the engineer, rather than preserving its old row number.
	w.snapshot.Resources = []workspace.Resource{{Ref: workspaceFixtureRef("worker"), Kind: "Pod", Summary: "Unready"}, {Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: "Ready"}}
	w.render()
	target := w.SelectedResource()
	if target.Name != "worker" || target.UID != types.UID("worker-uid") || target.Context != "development" || target.Namespace != testWorkspaceNamespace {
		t.Fatalf("selected identity changed: %+v", target)
	}
	// Selection captures the scope's context, even when the surrounding app is
	// changed later. The caller must reject it rather than retargeting its request.
	w.scope.Context = "prior-context"
	if w.SelectedResource().Context != "prior-context" {
		t.Fatal("selected resource was silently retargeted")
	}
}
func TestDailyWorkspacePinsKeepPriorUIDAndCoverageIsNotAnObject(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.scope.Pins = []workspace.ResourceRef{workspaceFixtureRef(testWorkspaceAPIName)}
	w.mode = "pins"
	replacement := workspaceFixtureRef(testWorkspaceAPIName)
	replacement.UID = "replacement-uid"
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Resources: []workspace.Resource{{Ref: replacement, Kind: "Pod", Summary: "Ready"}}}
	w.render()
	if !strings.Contains(w.rows[0].cells[3], "Identity changed") || w.SelectedResource().UID != "api-uid" {
		t.Fatal("pin silently followed replacement")
	}
	w.mode = dailyWorkspaceCoverageMode
	w.coverage = []workspace.Coverage{{GVR: "v1/pods", Namespace: testWorkspaceNamespace, State: "denied", Detail: "forbidden"}}
	w.render()
	if w.SelectedResource().Err() == nil {
		t.Fatal("coverage row was exposed as API object")
	}
}
func TestDailyWorkspaceScopeMutationWritesOnlyLocalStore(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.path = filepath.Join(t.TempDir(), "workspaces.yaml")
	w.store.Scopes = []workspace.Scope{w.scope}
	if err := workspace.SaveStore(w.path, w.store); err != nil {
		t.Fatal(err)
	}
	updated := w.scope
	updated.Searches = []workspace.SavedSearch{{Name: "crashes", Query: "status:crash"}}
	if err := w.persistScope(updated); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.LoadStore(w.path)
	if err != nil || len(store.Scopes) != 1 || len(store.Scopes[0].Searches) != 1 {
		t.Fatalf("saved search: %+v %v", store, err)
	}
	invalid := updated
	invalid.Namespaces = []string{AllScopes}
	if err := w.persistScope(invalid); err == nil {
		t.Fatal("widened invalid scope accepted")
	}
	retained, _ := workspace.LoadStore(w.path)
	if retained.Scopes[0].Namespaces[0] != "frontend" {
		t.Fatal("invalid edit replaced saved scope")
	}
}
func TestDailyWorkspaceDrawEscapesReportedMarkupAndShowsUnknownCoverage(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.mode = "queue"
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Findings: []workspace.Finding{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Severity: "warning", Reason: "[red]reported reason", Detail: "[blue]reported detail"}}}
	w.coverage = []workspace.Coverage{{GVR: "v1/pods", Namespace: "frontend", State: "denied"}}
	w.render()
	header := drawnText(t, w.header, 140, 5)
	if !strings.Contains(header, "1 coverage gaps") {
		t.Fatal(header)
	}
	detail := drawnText(t, w.detail, 100, 5)
	if !strings.Contains(detail, "[blue]reported detail") {
		t.Fatal("reported markup was interpreted: " + detail)
	}
}

func TestDailyWorkspaceContextMismatchDoesNotActivateOrReplaceScope(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.app.Config = &config.Config{K9s: &config.K9s{}}
	other := workspace.Scope{Name: "production", Context: "production-context", Namespaces: []string{"prod"}, Kinds: []string{"pods"}}
	w.store.Scopes = []workspace.Scope{w.scope, other}
	w.path = filepath.Join(t.TempDir(), "workspaces.yaml")
	if err := workspace.SaveStore(w.path, w.store); err != nil {
		t.Fatal(err)
	}
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now()}
	observed := w.snapshot.ObservedAt
	w.useScope("production")
	if w.store.Active != testWorkspaceName || w.scope.Name != testWorkspaceName || !w.snapshot.ObservedAt.Equal(observed) {
		t.Fatal("mismatched scope changed destination or evidence")
	}
	if w.app.Config.ActiveContextName() != "" || !strings.Contains(w.notice, "switch explicitly") {
		t.Fatal("scope switched context or gave no useful next action")
	}
}
func TestDailyWorkspaceFormFitsLaptopWidthAndSupportsTabEscape(t *testing.T) {
	canceled := false
	form := tview.NewForm().SetItemPadding(0)
	form.AddInputField("Name", testWorkspaceName, 36, nil, nil).AddInputField("Namespaces (comma-separated)", "backend,frontend", 48, nil, nil).AddButton("Save", func() {})
	form.SetCancelFunc(func() { canceled = true })
	frame := tview.NewFrame(form).SetBorders(0, 0, 1, 0, 0, 0)
	frame.SetBorder(true).SetBorderPadding(1, 1, 1, 1)
	modal := &dailyWorkspaceModal{Frame: frame, form: form, message: "Choose explicit namespaces.", color: config.DefaultSemanticPalette().Text.Color()}
	text := drawnText(t, modal, 80, 24)
	if !strings.Contains(text, "backend,frontend") || !strings.Contains(text, "Namespaces") {
		t.Fatalf("workspace form clipped namespace input:\n%s", text)
	}
	app := tview.NewApplication().SetRoot(modal, true).SetFocus(modal)
	first := form.GetFormItem(0).(*tview.InputField)
	second := form.GetFormItem(1).(*tview.InputField)
	first.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	if !second.HasFocus() {
		t.Fatal("Tab did not focus next workspace field")
	}
	second.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	if !canceled {
		t.Fatal("Escape did not cancel workspace form")
	}
}

func TestDailyWorkspaceActiveContextEditInvalidatesPriorObservationBeforeRejection(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.app.Config = &config.Config{K9s: &config.K9s{}}
	w.path = filepath.Join(t.TempDir(), "workspaces.yaml")
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: "Ready"}}}
	w.render()
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	edited := w.scope
	edited.Context = "production-context"
	w.store.Scopes = []workspace.Scope{edited}
	if err := workspace.SaveStore(w.path, w.store); err != nil {
		t.Fatal(err)
	}
	w.acceptEditedScope(edited)
	if ctx.Err() != context.Canceled || w.scope.Context != "production-context" || !w.snapshot.ObservedAt.IsZero() || w.SelectedResource().Err() == nil {
		t.Fatal("edited scope retained old destination/target")
	}
	w.setMode("inventory")
	if w.SelectedResource().Err() == nil {
		t.Fatal("tab change reused old context target")
	}
	saved, err := workspace.LoadStore(w.path)
	if err != nil || saved.Scopes[0].Context != "production-context" {
		t.Fatalf("layout saved obsolete scope: %+v %v", saved, err)
	}
	scope := w.scope
	scope.Searches = []workspace.SavedSearch{{Name: "pending", Query: "status:pending"}}
	if err := w.persistScope(scope); err != nil {
		t.Fatal(err)
	}
	saved, _ = workspace.LoadStore(w.path)
	if saved.Scopes[0].Context != "production-context" {
		t.Fatal("search update overwrote accepted context edit")
	}
}

func TestDailyWorkspaceInventorySearchDoesNotHideCoverageAndReturnsWithSelection(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.path = filepath.Join(t.TempDir(), "workspaces.yaml")
	w.store.Scopes = []workspace.Scope{w.scope}
	if err := workspace.SaveStore(w.path, w.store); err != nil {
		t.Fatal(err)
	}
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Resources: []workspace.Resource{{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: "Pod", Summary: testWorkspaceCrashLoopReason}, {Ref: workspaceFixtureRef("worker"), Kind: "Pod", Summary: testWorkspaceCrashLoopReason}}}
	w.coverage = []workspace.Coverage{{GVR: "v1/pods", Namespace: "frontend", State: "denied", Detail: "forbidden"}}
	w.applyQuery("status:crash")
	w.table.Select(2, 0)
	w.setMode(dailyWorkspaceCoverageMode)
	if w.query != "" || len(w.rows) != 1 || w.rows[0].cells[0] != "denied" {
		t.Fatal("inventory search hid coverage gap")
	}
	w.setMode("inventory")
	if w.query != "status:crash" || w.SelectedResource().Name != "worker" {
		t.Fatal("returning to inventory lost search or selected identity")
	}
}

func TestDailyWorkspacePaletteRequiresCapturedSelectionForResourceActions(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.actions = w.makeActions()
	for _, key := range []tcell.Key{tcell.KeyEnter, ui.KeyP, ui.KeyL} {
		action, found := w.Actions().Get(key)
		if !found || !action.Opts.RequiresSelection {
			t.Fatalf("workspace resource action lost captured selection guard: %v", key)
		}
	}
	w.mode = "scopes"
	action, _ := w.Actions().Get(tcell.KeyEnter)
	if action.Opts.RequiresSelection {
		t.Fatal("scope navigation incorrectly requires API resource")
	}
}
func TestDailyWorkspaceOldViewMetadataPatchPreservesNewerScopesPinsAndSearches(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.path = filepath.Join(t.TempDir(), "workspaces.yaml")
	w.store.Scopes = []workspace.Scope{w.scope}
	latest := w.store
	latest.Scopes = slices.Clone(latest.Scopes)
	latest.Scopes[0].Pins = []workspace.ResourceRef{workspaceFixtureRef(testWorkspaceAPIName)}
	latest.Scopes[0].Searches = []workspace.SavedSearch{{Name: "newer-search", Query: "name:api"}}
	latest.Scopes = append(latest.Scopes, workspace.Scope{Name: "newer-scope", Context: "development", Namespaces: []string{"frontend"}, Kinds: []string{"pods"}})
	latest.Active = "newer-scope"
	if err := workspace.SaveStore(w.path, latest); err != nil {
		t.Fatal(err)
	}
	// Old A was covered by B. A patches its tab and another pin against the latest
	// store rather than saving the stale complete Store it retained before B.
	if err := w.updateScope(func(scope *workspace.Scope) error {
		scope.Layout = "pins"
		scope.Pins = append(scope.Pins, workspaceFixtureRef("worker"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := workspace.LoadStore(w.path)
	if err != nil || len(saved.Scopes) != 2 || saved.Active != "newer-scope" || len(saved.Scopes[0].Pins) != 2 || len(saved.Scopes[0].Searches) != 1 {
		t.Fatalf("older view lost newer metadata: %+v %v", saved, err)
	}
	changed := saved.Scopes[0]
	changed.Context = "production"
	saved.Scopes[0] = changed
	if err := workspace.SaveStore(w.path, saved); err != nil {
		t.Fatal(err)
	}
	if err := w.updateScope(func(scope *workspace.Scope) error { scope.Layout = "inventory"; return nil }); err == nil {
		t.Fatal("old view edited changed scope identity")
	}
	retained, _ := workspace.LoadStore(w.path)
	if retained.Scopes[0].Context != "production" {
		t.Fatal("old view restored obsolete context")
	}
	retained.Scopes = slices.DeleteFunc(retained.Scopes, func(scope workspace.Scope) bool { return scope.Name == testWorkspaceName })
	if err := workspace.SaveStore(w.path, retained); err != nil {
		t.Fatal(err)
	}
	if err := w.updateScope(func(scope *workspace.Scope) error { scope.Layout = "inventory"; return nil }); err == nil {
		t.Fatal("old view recreated removed scope")
	}
	final, _ := workspace.LoadStore(w.path)
	if len(final.Scopes) != 1 {
		t.Fatal("removed scope was recreated")
	}
}

func TestDailyWorkspaceEmptyStatePaintUsesFullWidthInEveryTab(t *testing.T) {
	messages := map[string]string{
		"scopes":                   "No saved scopes. Press n to create your daily workspace.",
		"queue":                    "No findings observed. Coverage shows checks and unknowns.",
		"inventory":                "No resources observed. r refreshes; Coverage shows gaps.",
		"pins":                     "No pins. Select a resource in Daily or Inventory and press p.",
		dailyWorkspaceCoverageMode: "No observation yet. r reads the saved namespaces.",
	}
	for _, width := range []int{80, 120} {
		for mode, message := range messages {
			t.Run(fmt.Sprintf("%s-%d", mode, width), func(t *testing.T) {
				w := dailyWorkspaceFixture()
				w.mode = mode
				w.snapshot.ObservedAt = time.Now()
				w.Flex.SetDirection(tview.FlexRow).AddItem(w.table, 0, 1, true)
				w.render()
				paint := drawnText(t, w, width, 12)
				if !strings.Contains(paint, message) {
					t.Fatalf("empty message clipped at %d columns:\n%s", width, paint)
				}
				if w.SelectedResource().Err() == nil {
					t.Fatal("empty message exposed as selected API object")
				}
			})
		}
	}
}

func TestDailyWorkspaceAllFormButtonsRemainVisibleAtLaptopAndDeskSizes(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 34}} {
		for _, kind := range []string{"scope", "search"} {
			t.Run(fmt.Sprintf("%s-%d", kind, size[0]), func(t *testing.T) {
				form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
				form.SetBorderPadding(0, 0, 0, 0)
				var message, button string
				if kind == "scope" {
					form.AddInputField("Name", testWorkspaceName, 36, nil, nil).
						AddInputField("Context", "development", 36, nil, nil).
						AddInputField("Namespaces (comma-separated)", "backend,frontend", 48, nil, nil).
						AddInputField("Label selector (optional)", "app=api", 48, nil, nil).
						AddInputField("Kinds (optional, comma-separated)", "pods,deployments", 48, nil, nil)
					message = "Saved locally. Pick explicit namespaces (maximum 16). Empty kinds use workload defaults. " +
						"Enter/Tab moves through fields. Saving never switches your Kubernetes context."
					button = "Save workspace"
				} else {
					form.AddDropDown(dailyWorkspaceSearchLabel, []string{"faults"}, 0, nil)
					message = "Applies the saved literal query to the Inventory tab. It keeps the same context, namespaces and label selector."
					button = "Open inventory"
				}
				form.AddButton("Cancel", func() {}).AddButton(button, func() {})
				frame := tview.NewFrame(form).SetBorders(0, 0, 1, 0, 0, 0)
				frame.SetBorder(true).SetBorderPadding(1, 1, 1, 1)
				modal := &dailyWorkspaceModal{Frame: frame, form: form, message: message, color: config.DefaultSemanticPalette().Text.Color()}
				paint := drawnText(t, modal, size[0], size[1])
				for _, visible := range []string{"Cancel", button} {
					if !strings.Contains(paint, visible) {
						t.Fatalf("%s form clipped %s at %dx%d:\n%s", kind, visible, size[0], size[1], paint)
					}
				}
			})
		}
	}
}
