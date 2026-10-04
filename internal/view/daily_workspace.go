// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	dailyWorkspaceScopesMode       = "scopes"
	dailyWorkspaceQueueMode        = "queue"
	dailyWorkspaceHistoryMode      = "history"
	dailyWorkspacePinsMode         = "pins"
	dailyWorkspaceDeleteToken      = "delete"
	dailyWorkspaceFormPage         = "daily-workspace-form"
	dailyWorkspaceSearchLabel      = "Search"
	dailyWorkspaceBackLabel        = "Back"
	dailyWorkspaceCoverageMode     = "coverage"
	dailyWorkspaceCoverageComplete = "complete"
	dailyWorkspaceKindCol          = "KIND"
)

type dailyWorkspaceRow struct {
	cells     []string
	ref       *workspace.ResourceRef
	detail    string
	evidence  string
	scopeName string
	key       string
}

// dailyWorkspace owns a retained, explicitly refreshed observation. It neither
// changes the kubeconfig destination nor installs ordinary browser polling.
// Mutable view state belongs to the application draw goroutine.
type dailyWorkspace struct {
	*tview.Flex
	app                                    *App
	table                                  *tview.Table
	header, detail, footer                 *tview.TextView
	prompt                                 *tview.InputField
	actions                                *ui.KeyActions
	store                                  workspace.Store
	scope                                  workspace.Scope
	path, contextName, mode, query, notice string
	snapshot                               workspace.Snapshot
	observationWindow                      *workspace.QueueWindow
	jobSources                             map[string]*review.JobReviewSnapshot
	jobSourcesOmitted                      int
	coverage                               []workspace.Coverage
	rows                                   []dailyWorkspaceRow
	tabQueries, tabSelections              map[string]string
	prompting, started, formOpen           bool
	cancel                                 context.CancelFunc
	generation                             uint64
	reader                                 dynamic.Interface
	collect                                func(context.Context, dynamic.Interface, workspace.Scope, time.Time) workspace.Snapshot
	originalCapture                        func(*tcell.EventKey) *tcell.EventKey
	viewportWidth                          int
	displayColumns                         []int
}

var _ model.Component = (*dailyWorkspace)(nil)
var _ SelectedResource = (*dailyWorkspace)(nil)

func dailyWorkspacePath() string { return filepath.Join(config.AppConfigDir, "workspaces.yaml") }

func newDailyWorkspace(a *App, store workspace.Store, mode, query string) *dailyWorkspace {
	w := &dailyWorkspace{
		Flex: tview.NewFlex().SetDirection(tview.FlexRow), app: a,
		table: tview.NewTable(), header: tview.NewTextView(), detail: tview.NewTextView(), footer: tview.NewTextView(), prompt: tview.NewInputField(),
		store: store, path: dailyWorkspacePath(), contextName: a.Config.ActiveContextName(), mode: mode, query: query, collect: workspace.Collect,
	}
	for i := range store.Scopes {
		scope := &store.Scopes[i]
		if scope.Name == store.Active {
			w.scope = *scope
			break
		}
	}
	if w.scope.Name == "" {
		w.mode = dailyWorkspaceScopesMode
		w.query = ""
	}
	w.resetObservationWindow(time.Now())
	w.tabQueries = map[string]string{w.mode: w.query}
	w.tabSelections = make(map[string]string)
	w.table.SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	w.header.SetDynamicColors(true).SetWrap(false)
	w.detail.SetDynamicColors(true).SetWrap(true)
	w.footer.SetDynamicColors(true).SetWrap(false)
	w.prompt.SetLabel("Search (tokens; kind:/ns:/name:/status:): ").SetText(query)
	w.AddItem(w.header, 4, 0, false).AddItem(w.table, 0, 1, true).AddItem(w.detail, 3, 0, false).AddItem(w.footer, 1, 0, false)
	w.SetBorder(true).SetTitle(" Daily workspace ")
	w.table.SetInputCapture(w.key)
	w.table.SetSelectionChangedFunc(func(int, int) { w.renderDetail() })
	w.prompt.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter && !w.applyQuery(w.prompt.GetText()) {
			return
		}
		if key == tcell.KeyEnter || key == tcell.KeyEscape {
			w.prompting = false
			w.RemoveItem(w.prompt)
			w.app.SetFocus(w.table)
		}
	})
	w.actions = w.makeActions()
	return w
}

func (*dailyWorkspace) Name() string                           { return "daily workspace" }
func (*dailyWorkspace) CompactWorkspace() bool                 { return true }
func (*dailyWorkspace) SetCommand(*cmd.Interpreter)            {}
func (*dailyWorkspace) SetLabelSelector(labels.Selector, bool) {}
func (w *dailyWorkspace) SetFilter(query string, _ bool)       { w.applyQuery(query) }
func (w *dailyWorkspace) InCmdMode() bool                      { return w.prompting }
func (w *dailyWorkspace) Actions() *ui.KeyActions {
	if action, ok := w.actions.Get(tcell.KeyEnter); ok {
		action.Description = "Investigate"
		if w.mode == dailyWorkspaceScopesMode {
			action.Description = "Open scope"
		}
		if w.mode == dailyWorkspaceCoverageMode {
			action.Description = "Coverage details"
		}
		action.Opts.RequiresSelection = w.mode != dailyWorkspaceScopesMode && w.mode != dailyWorkspaceCoverageMode && w.mode != dailyWorkspaceHistoryMode
		if w.mode == dailyWorkspaceCoverageMode {
			action.Opts.RequiresSelection = false
		}
		w.actions.Add(tcell.KeyEnter, action)
	}
	return w.actions
}
func (w *dailyWorkspace) Hints() model.MenuHints { return actionCatalogHints(w, w.app) }
func (*dailyWorkspace) ExtraHints() map[string]string {
	return map[string]string{"Workspace": "1 Daily · 2 Inventory · 3 Coverage · 4 Pins · 5 Scopes · 6 History. " +
		"J reviews selected Job/CronJob; i investigates retained identity. / searches retained rows; r refreshes only saved namespaces."}
}
func (w *dailyWorkspace) Init(context.Context) error {
	w.StylesChanged(w.app.Styles)
	w.render()
	return nil
}
func (w *dailyWorkspace) Start() {
	if w.started {
		return
	}
	w.started = true
	if err := w.syncStore(); err != nil {
		w.notice = "Workspace metadata unavailable: " + err.Error()
	}
	w.app.Styles.RemoveListener(w)
	w.app.Styles.AddListener(w)
	w.StylesChanged(w.app.Styles)
	w.originalCapture = w.app.GetInputCapture()
	w.app.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if w.prompting || w.formOpen || w.app.Content.IsTopDialog() {
			return e
		}
		if !w.app.Prompt().InCmdMode() && (e.Rune() == '/' || e.Key() == tcell.KeyEscape) {
			return w.key(e)
		}
		if w.originalCapture != nil {
			return w.originalCapture(e)
		}
		return e
	})
	if w.snapshot.ObservedAt.IsZero() && w.scope.Name != "" && w.mode != dailyWorkspaceScopesMode {
		w.refresh()
	} else {
		w.render()
	}
}
func (w *dailyWorkspace) Stop() {
	w.generation++
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
		if strings.HasPrefix(w.notice, "Reading scope") {
			w.notice = "Observation canceled while away; retained snapshot. r refreshes."
			w.recordQueueGap(w.notice)
		}
	}
	if w.formOpen {
		w.app.Content.Pages.RemovePage(dailyWorkspaceFormPage)
		w.formOpen = false
	}
	if w.started {
		w.started = false
		w.app.Styles.RemoveListener(w)
		w.app.SetInputCapture(w.originalCapture)
	}
}
func (w *dailyWorkspace) Refresh() { w.refresh() }
func (w *dailyWorkspace) StylesChanged(styles *config.Styles) {
	p := styles.Semantic()
	canvas := p.Canvas.Color()
	text := config.ReadableForeground(p.Text.Color(), canvas)
	w.SetBackgroundColor(canvas).SetBorderColor(p.Muted.Color()).SetBorderFocusColor(p.Focus.Color()).SetTitleColor(p.Focus.Color())
	w.table.SetBackgroundColor(canvas)
	w.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(config.ReadableForeground(text, p.Selected.Color())).Bold(true))
	for _, v := range []*tview.TextView{w.header, w.detail, w.footer} {
		v.SetBackgroundColor(canvas)
		v.SetTextColor(text)
	}
	w.prompt.SetFieldBackgroundColor(p.Panel.Color()).SetFieldTextColor(text).SetLabelColor(p.Focus.Color())
	w.render()
}
func (w *dailyWorkspace) SelectedResource() SelectedResourceTarget {
	row, _ := w.table.GetSelection()
	if row < 1 || row > len(w.rows) || w.rows[row-1].ref == nil {
		return SelectedResourceTarget{Context: w.contextName, UnavailableReason: "Select an observed resource or finding first"}
	}
	ref := *w.rows[row-1].ref
	return SelectedResourceTarget{Context: w.scope.Context, GVR: client.NewGVR(ref.GVR), Namespace: ref.Namespace, Name: ref.Name, UID: types.UID(ref.UID)}
}
func (w *dailyWorkspace) makeActions() *ui.KeyActions {
	actions := ui.NewKeyActions()
	for _, item := range []struct {
		key     tcell.Key
		label   string
		visible bool
	}{
		{tcell.KeyEnter, "Investigate", true},
		{ui.KeyR, "Refresh", true},
		{ui.KeySlash, dailyWorkspaceSearchLabel, true},
		{ui.KeyN, "New scope", true},
		{ui.KeyE, "Edit scope", true},
		{ui.KeyP, "Pin / unpin", true},
		{ui.KeyS, "Save search", true},
		{ui.KeyShiftS, "Saved searches", true},
		{ui.KeyL, "Pod logs", true},
		{ui.KeyD, "Remove scope", false},
		{ui.Key1, "Daily", false},
		{ui.Key2, "Inventory", false},
		{ui.Key3, "Coverage", false},
		{ui.Key4, "Pins", false},
		{ui.Key5, "Scopes", true},
		{ui.Key6, "Observed history", true},
		{ui.KeyI, "Investigate captured identity", false},
		{ui.KeyShiftJ, "Job outcomes and schedule", true},
		{ui.KeyV, "Selected row details", true},
		{tcell.KeyTab, "Next workspace tab", true},
		{tcell.KeyEscape, dailyWorkspaceBackLabel, false},
	} {
		key := item.key
		action := ui.NewKeyAction(item.label, func(*tcell.EventKey) *tcell.EventKey { return w.key(actionEvent(key)) }, item.visible)
		action.ID = "workspace." + strings.ReplaceAll(strings.ToLower(item.label), " ", "-")
		action.Category = ui.ActionNavigate
		if key == ui.KeyD {
			action.Opts.Priority = -1
		}
		if key == ui.KeyP || key == ui.KeyL || key == ui.KeyShiftJ || key == ui.KeyI || key == tcell.KeyEnter {
			action.Opts.RequiresSelection = key != tcell.KeyEnter ||
				(w.mode != dailyWorkspaceScopesMode && w.mode != dailyWorkspaceCoverageMode && w.mode != dailyWorkspaceHistoryMode)
			action.Availability = func() string {
				if key == tcell.KeyEnter && (w.mode == dailyWorkspaceCoverageMode || w.mode == dailyWorkspaceHistoryMode) {
					row, _ := w.table.GetSelection()
					if row > 0 && row <= len(w.rows) {
						return ""
					}
					return "Select a retained source row first"
				}
				if key == tcell.KeyEnter && w.mode == dailyWorkspaceScopesMode {
					row, _ := w.table.GetSelection()
					if row > 0 && row <= len(w.rows) && w.rows[row-1].scopeName != "" {
						return ""
					}
					return "Select a saved scope first"
				}
				target := w.SelectedResource()
				if err := target.Err(); err != nil {
					return err.Error()
				}
				if target.Context != w.app.Config.ActiveContextName() {
					return "Context changed; reopen workspace"
				}
				if key == ui.KeyShiftJ {
					if err := jobReviewTargetError(target); err != nil {
						return err.Error()
					}
				}
				if key == ui.KeyL && (target.GVR == nil || target.GVR.R() != client.PodGVR.R()) {
					return "Select a Pod to open logs"
				}
				return ""
			}
		}
		actions.Add(key, action)
	}
	actions.Add(tcell.KeyCtrlO, ui.NewKeyAction("Actions", w.app.actionsCmd, true))
	return actions
}
func (w *dailyWorkspace) key(e *tcell.EventKey) *tcell.EventKey {
	if w.prompting || w.formOpen {
		return e
	}
	switch {
	case e.Key() == tcell.KeyEscape:
		return w.app.PrevCmd(e)
	case e.Key() == tcell.KeyEnter:
		if w.mode == dailyWorkspaceScopesMode {
			row, _ := w.table.GetSelection()
			if row > 0 && row <= len(w.rows) {
				w.useScope(w.rows[row-1].scopeName)
			}
			return nil
		}
		if w.mode == dailyWorkspaceCoverageMode || w.mode == dailyWorkspaceHistoryMode {
			w.showRowDetails()
			return nil
		}
		w.app.openTargetInspection(w.SelectedResource(), troubleshootCommand)
		return nil
	case e.Rune() == 'J':
		w.app.openJobReview(w.SelectedResource())
		return nil
	case e.Rune() == 'i':
		w.app.openTargetInspection(w.SelectedResource(), troubleshootCommand)
		return nil
	case e.Rune() == 'r':
		w.refresh()
		return nil
	case e.Key() == tcell.KeyTab:
		w.setMode(dailyWorkspaceModes[(w.tabIndex()+1)%len(dailyWorkspaceModes)])
		return nil
	case e.Rune() == 'v':
		w.showRowDetails()
		return nil
	case e.Rune() == '/':
		w.openQueryPrompt()
		return nil
	case e.Rune() == 'n':
		w.scopeForm(false)
		return nil
	case e.Rune() == 'e':
		w.scopeForm(true)
		return nil
	case e.Rune() == 'p':
		w.togglePin()
		return nil
	case e.Rune() == 's':
		w.searchForm()
		return nil
	case e.Rune() == 'S':
		w.savedSearchForm()
		return nil
	case e.Rune() == 'd':
		if w.mode == dailyWorkspaceScopesMode {
			w.deleteScopeForm()
			return nil
		}
	case e.Rune() == 'l':
		if err := w.app.dailyWorkspaceOpenLogs(w.SelectedResource()); err != nil {
			w.app.Flash().Err(err)
		}
		return nil
	case strings.ContainsRune("123456", e.Rune()) && e.Rune() != 0:
		w.setMode(map[rune]string{
			'1': dailyWorkspaceQueueMode, '2': inventoryCommand, '3': dailyWorkspaceCoverageMode,
			'4': dailyWorkspacePinsMode, '5': dailyWorkspaceScopesMode, '6': dailyWorkspaceHistoryMode,
		}[e.Rune()])
		return nil
	}
	return e
}
func (w *dailyWorkspace) openQueryPrompt() {
	w.prompting = true
	w.prompt.SetText(w.query)
	w.AddItem(w.prompt, 1, 0, true)
	w.app.SetFocus(w.prompt)
}
func (w *dailyWorkspace) setMode(mode string) {
	if w.tabQueries == nil {
		w.tabQueries = make(map[string]string)
	}
	if w.tabSelections == nil {
		w.tabSelections = make(map[string]string)
	}
	w.tabQueries[w.mode] = w.query
	w.tabSelections[w.mode] = w.selectedKey()
	w.mode = mode
	w.query = w.tabQueries[mode]
	if mode != dailyWorkspaceScopesMode && w.scope.Name != "" {
		if err := w.updateScope(func(scope *workspace.Scope) error { scope.Layout = mode; return nil }); err != nil {
			w.app.Flash().Err(err)
		}
	}
	w.table.Select(1, 0)
	w.render()
	if selected := w.tabSelections[mode]; selected != "" {
		for i, row := range w.rows {
			if dailyWorkspaceRowKey(&row) == selected {
				w.table.Select(i+1, 0)
				w.renderDetail()
				break
			}
		}
	}
	if w.snapshot.ObservedAt.IsZero() && mode != dailyWorkspaceScopesMode && w.scope.Name != "" {
		w.refresh()
	}
}
func dailyWorkspaceAccepts(generation, current uint64, contextName, currentContext string, top bool) bool {
	return generation == current && contextName == currentContext && top
}
func (w *dailyWorkspace) refresh() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
		w.generation++
	}
	if err := w.syncStore(); err != nil {
		w.notice = "Workspace metadata unavailable; observation retained: " + err.Error()
		w.recordQueueGap(w.notice)
		w.render()
		return
	}
	if w.scope.Name == "" {
		w.notice = "Create a scope with n, then refresh"
		w.render()
		return
	}
	if w.contextName != w.app.Config.ActiveContextName() || w.scope.Context != w.contextName {
		w.notice = "Scope context differs. Use :ctx to switch explicitly, then reopen :workspace."
		w.render()
		return
	}
	// Pin the client synchronously before the goroutine; context changes cannot
	// redirect an in-flight request to the new API server.
	connection := w.app.Conn()
	if connection == nil {
		w.notice = "Refresh unavailable: no Kubernetes connection; retained observation"
		w.recordQueueGap(w.notice)
		w.render()
		return
	}
	reader, err := connection.DynDial()
	if err != nil {
		w.notice = "Refresh failed; retained observation: " + err.Error()
		w.recordQueueGap(w.notice)
		w.render()
		return
	}
	w.reader = reader
	if w.cancel != nil {
		w.cancel()
	}
	w.generation++
	generation := w.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	w.cancel = cancel
	scope := w.scope
	w.notice = "Reading scope… 10 second deadline"
	w.render()
	go func() {
		defer cancel()
		snapshot := w.collect(ctx, reader, scope, time.Now())
		refreshErr := ctx.Err()
		w.app.QueueUpdateDraw(func() {
			if !dailyWorkspaceAccepts(generation, w.generation, w.contextName, w.app.Config.ActiveContextName(), w.app.Content.Top() == w) {
				return
			}
			w.cancel = nil
			w.acceptSnapshot(snapshot, refreshErr)
			w.render()
		})
	}()
}

//nolint:gocritic // Accept a captured observation value before retaining it as view-owned evidence.
func (w *dailyWorkspace) acceptSnapshot(snapshot workspace.Snapshot, err error) {
	if !w.observeSnapshot(&snapshot, err) {
		w.notice = "Stale or undated refresh ignored; prior evidence retained. See History."
		w.coverage = []workspace.Coverage{{State: "stale", Detail: w.notice}}
		return
	}
	failed, successful := 0, 0
	for _, coverage := range snapshot.Coverage {
		switch coverage.State {
		case dailyWorkspaceCoverageComplete, "truncated":
			successful++
		case "absent":
		default:
			failed++
		}
	}
	w.coverage = snapshot.Coverage
	if err != nil || (failed > 0 && successful == 0) {
		if !w.snapshot.ObservedAt.IsZero() {
			w.notice = fmt.Sprintf("Refresh unavailable (%d failed reads); retained observation. See Coverage.", failed)
			if err != nil {
				w.notice = "Refresh failed; retained observation: " + err.Error()
			}
			return
		}
		w.notice = fmt.Sprintf("Partial observation: %d coverage gaps. See Coverage for reasons.", failed)
		if err != nil {
			w.notice = "Partial observation: " + err.Error()
		}
	} else if failed > 0 {
		w.notice = fmt.Sprintf("Partial observation: %d coverage gaps. Successful reads updated; see Coverage for reasons.", failed)
	} else {
		w.notice = "Explicit snapshot. r refreshes; no background polling."
	}
	w.snapshot = snapshot
}
func (w *dailyWorkspace) useScope(name string) {
	latest, err := workspace.LoadStore(w.path)
	if err != nil {
		w.app.Flash().Err(err)
		return
	}
	w.store = latest
	for i := range w.store.Scopes {
		scope := &w.store.Scopes[i]
		if scope.Name != name {
			continue
		}
		if scope.Context != w.app.Config.ActiveContextName() {
			w.notice = "Scope " + scope.Name + " belongs to context " + scope.Context + ". Use :ctx to switch explicitly, then reopen :workspace."
			w.render()
			return
		}
		candidate := w.store
		candidate.Active = name
		if err := workspace.SaveStore(w.path, candidate); err != nil {
			w.app.Flash().Err(err)
			return
		}
		if w.cancel != nil {
			w.cancel()
		}
		w.generation++
		w.store = candidate
		w.scope = *scope
		w.contextName = scope.Context
		w.snapshot = workspace.Snapshot{}
		w.resetObservationWindow(time.Now())
		w.coverage = nil
		w.query = ""
		w.tabQueries = make(map[string]string)
		w.tabSelections = make(map[string]string)
		w.mode = scope.Layout
		if w.mode == "" {
			w.mode = dailyWorkspaceQueueMode
		}
		w.table.Select(1, 0)
		w.refresh()
		return
	}
	if w.scope.Name == name {
		w.invalidateScope(workspace.Scope{}, "Saved scope was removed; choose another scope.")
	} else {
		w.notice = "Saved scope no longer exists; choose another scope."
	}
	w.render()
}

// An accepted edit changes the identity of the saved scope even when its new
// context cannot be opened here. Invalidate the prior observation before the
// context check so subsequent local layout/search changes cannot save old scope
// fields over the accepted edit or expose resources from the old destination.
//
//nolint:gocritic // Capture the accepted scope value before invalidating prior observation state.
func (w *dailyWorkspace) acceptEditedScope(scope workspace.Scope) {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.generation++
	w.scope = scope
	w.snapshot = workspace.Snapshot{}
	w.resetObservationWindow(time.Now())
	w.coverage = nil
	w.rows = nil
	w.reader = nil
	w.query = ""
	w.tabQueries = make(map[string]string)
	w.tabSelections = make(map[string]string)
	w.table.Clear()
	w.table.Select(1, 0)
	if scope.Context != w.app.Config.ActiveContextName() {
		w.mode = dailyWorkspaceScopesMode
		w.notice = "Scope " + scope.Name + " belongs to context " + scope.Context + ". Use :ctx to switch explicitly, then reopen :workspace."
		w.render()
		return
	}
	w.contextName = scope.Context
	w.mode = scope.Layout
	if w.mode == "" {
		w.mode = dailyWorkspaceQueueMode
	}
	w.refresh()
}

func (w *dailyWorkspace) togglePin() {
	target := w.SelectedResource()
	if err := target.Err(); err != nil {
		w.app.Flash().Err(err)
		return
	}
	if target.Context != w.app.Config.ActiveContextName() {
		w.app.Flash().Warn("Context changed; reopen workspace")
		return
	}
	ref := workspace.ResourceRef{GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}
	sameIdentity := func(pin workspace.ResourceRef) bool {
		return pin.GVR == ref.GVR && pin.Namespace == ref.Namespace && pin.Name == ref.Name
	}
	expectedIndex := slices.IndexFunc(w.scope.Pins, sameIdentity)
	var expected workspace.ResourceRef
	if expectedIndex >= 0 {
		expected = w.scope.Pins[expectedIndex]
	}
	err := w.updateScope(func(scope *workspace.Scope) error {
		scope.Pins = slices.Clone(scope.Pins)
		index := slices.IndexFunc(scope.Pins, sameIdentity)
		if expectedIndex >= 0 {
			if index < 0 {
				return nil
			}
			if scope.Pins[index] != expected {
				return fmt.Errorf("this pin changed while away; reopen workspace")
			}
			scope.Pins = slices.Delete(scope.Pins, index, index+1)
			return nil
		}
		if index >= 0 {
			if scope.Pins[index] == ref {
				return nil
			}
			return fmt.Errorf("this pin changed while away; reopen workspace")
		}
		scope.Pins = append(scope.Pins, ref)
		return nil
	})
	if err != nil {
		w.app.Flash().Err(err)
		return
	}
	w.render()
}
