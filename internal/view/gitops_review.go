// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const gitopsCommandToken = "gitops"
const gitopsTitle = "GitOps review"

type gitopsView struct {
	*Details
	target                          SelectedResourceTarget
	request                         gitops.Request
	snapshot                        *gitops.Snapshot
	loader                          func(context.Context, *gitops.Request) (*gitops.Snapshot, error)
	enqueue                         func(func())
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	workspaceNamespace              string
	active, loading                 bool
	activeTab                       int
	tabStates                       [4]investigationTabState
	texts                           [4]string
	identityBar, tabsBar, footer    *tview.TextView
	width, height                   int
	refreshFailure                  string
}

func (c *Command) gitopsCommand(line string) {
	args := strings.Fields(line)
	if len(args) > 2 {
		c.app.Flash().Warn("Use :gitops [CONTROLLER_NAMESPACE] for the selected resource")
		return
	}
	namespace := ""
	if len(args) == 2 {
		namespace = args[1]
	}
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select an Application, Flux controller or Kubernetes resource first")
		return
	}
	target := actionTarget(owner, c.app.Config.ActiveContextName())
	c.app.openGitOpsReview(&target, namespace)
}

func (a *App) openGitOpsReview(target *SelectedResourceTarget, argoNamespace string) {
	if target == nil {
		a.Flash().Warn("Select a resource before GitOps review")
		return
	}
	if err := target.Err(); err != nil {
		a.Flash().Err(err)
		return
	}
	if target.UID == "" || target.GVR.R() == desiredReviewSecrets || target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Current captured resource UID required; Secret bodies are excluded")
		return
	}
	if argoNamespace != "" && !gitopsNamespaceDNS(argoNamespace) {
		a.Flash().Warn("Explicit Argo controller namespace is invalid")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	request := gitops.Request{Target: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(),
		Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, ArgoNamespace: argoNamespace}
	view := &gitopsView{Details: NewDetails(a, gitopsTitle, target.Path(), contentInspection, true), target: *target, request: request,
		destinationRevision: a.Config.DestinationRevision(), workspaceNamespace: a.Config.ActiveNamespace()}
	view.loader = func(ctx context.Context, request *gitops.Request) (*gitops.Snapshot, error) {
		dynamic, readErr := connection.DynDial()
		if readErr != nil {
			return nil, errors.New("Captured resource reader unavailable")
		}
		typed, readErr := connection.Dial()
		if readErr != nil {
			return nil, errors.New("Captured API discovery reader unavailable")
		}
		reader := &gitopsReader{dynamic: dynamic, discovery: typed.Discovery().RESTClient()}
		return gitops.Collect(ctx, reader, request)
	}
	if err := a.inject(view, false); err != nil {
		a.Flash().Err(err)
	}
}

func (v *gitopsView) SelectedResource() SelectedResourceTarget { return v.target }
func (*gitopsView) CompactWorkspace() bool                     { return true }

func (v *gitopsView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(gitops.Tabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next GitOps tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + 1) % len(gitops.Tabs))
		return nil
	}, true))
	v.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous GitOps tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + len(gitops.Tabs) - 1) % len(gitops.Tabs))
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh GitOps evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.render()
	return nil
}

func (v *gitopsView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.snapshot == nil && !v.loading && v.loader != nil {
		v.refresh()
	}
}
func (v *gitopsView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *gitopsView) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *gitopsView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	if v.loading && !v.destinationCurrent() {
		v.generation++
		if v.cancel != nil {
			v.cancel()
		}
		v.cancel, v.loading = nil, false
	}
	v.renderChrome()
	v.Flex.Draw(screen)
}
func (v *gitopsView) selectTab(tab int) {
	if tab < 0 || tab >= len(gitops.Tabs) || tab == v.activeTab {
		return
	}
	row, col := v.text.GetScrollOffset()
	v.tabStates[v.activeTab] = investigationTabState{query: v.inspectionQuery, region: v.currentRegion, row: row, col: col}
	v.activeTab = tab
	state := v.tabStates[tab]
	v.inspectionQuery, v.currentRegion = state.query, state.region
	v.cmdBuff.SetText(state.query, "", true)
	v.text.ScrollTo(state.row, state.col)
	v.render()
}
func (v *gitopsView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision() &&
		v.workspaceNamespace == v.app.Config.ActiveNamespace()
}
func (v *gitopsView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen GitOps review"
		v.render()
		return
	}
	if v.loader == nil {
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, request := v.generation, v.request
	ctx, cancel := context.WithTimeout(context.Background(), gitops.CollectTimeout)
	v.cancel, v.loading = cancel, true
	v.renderChrome()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, &request)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		var texts [4]string
		if snapshot != nil {
			for tab := range texts {
				texts[tab] = snapshot.Render(tab)
			}
		}
		dispatch := v.enqueue
		if dispatch == nil {
			dispatch = v.app.QueueUpdateDraw
		}
		dispatch(func() {
			if v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, texts, err)
			}
		})
	}()
}
func (v *gitopsView) acceptSnapshot(snapshot *gitops.Snapshot, texts [4]string, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = logstream.SafeText(err.Error())
	} else if snapshot == nil {
		v.refreshFailure = "No GitOps observation returned"
	} else if snapshot.Request != v.request {
		v.refreshFailure = "Snapshot does not match the captured selection and source namespace"
	} else {
		v.refreshFailure = ""
		v.snapshot, v.texts = snapshot, texts
	}
	v.render()
}
func (v *gitopsView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading read-only GitOps evidence..."
	if v.snapshot != nil {
		text = v.texts[v.activeTab]
	} else if v.refreshFailure != "" {
		text = "GitOps evidence unavailable: " + v.refreshFailure
	}
	if v.snapshot != nil && v.refreshFailure != "" {
		text = "Refresh failed: " + v.refreshFailure + "\nPrevious captured evidence retained.\n\n" + text
	}
	query, region := v.inspectionQuery, v.currentRegion
	row, col := v.text.GetScrollOffset()
	v.Update(logstream.SafeText(text))
	if query != "" {
		v.model.Filter(query)
		if region < v.maxRegions {
			v.currentRegion = region
			v.text.Highlight(fmt.Sprintf("search_%d", region))
		}
	}
	v.text.ScrollTo(row, col)
}
func (v *gitopsView) renderChrome() {
	if v.identityBar == nil {
		return
	}
	width := v.width
	if width <= 0 {
		width = 80
	}
	styles := v.app.Styles.Semantic()
	for _, item := range []*tview.TextView{v.identityBar, v.tabsBar, v.footer} {
		item.SetBackgroundColor(styles.Panel.Color())
		item.SetTextColor(styles.Text.Color())
	}
	identity := "ctx " + v.target.Context + " · " + v.target.Path()
	source := retainedObservationPending
	state := retainedWaitingForReads
	if v.snapshot != nil {
		source = "UID " + string(v.target.UID)[:min(12, len(v.target.UID))] + " · captured " + v.snapshot.CapturedAt.UTC().Format("15:04:05Z")
		state = "Read only | independent named observations"
		if v.snapshot.Partial() {
			state = "Read only | partial evidence | 4 Evidence"
		}
	}
	if v.loading {
		state = retainedRefreshing
	} else if v.refreshFailure != "" {
		state = "Unavailable | r retry | " + v.refreshFailure
		if v.snapshot != nil {
			state = "Retained / failed refresh | " + v.refreshFailure
		}
	}
	if !v.destinationCurrent() {
		state = retainedDestinationChanged
	}
	lines := []string{identity, source, state}
	for index, line := range lines {
		line = strings.NewReplacer("\n", " ", "\t", " ").Replace(logstream.SafeText(line))
		lines[index] = tview.Escape(ui.Truncate(line, width))
	}
	v.identityBar.SetText(strings.Join(lines, "\n"))
	v.tabsBar.SetText(ui.TaskTabs(gitops.Tabs, v.activeTab, width))
	footer := "Tab next · r refresh · / search · Esc back"
	if width < 48 {
		footer = "Tab · r refresh · / search · Esc back"
	}
	v.footer.SetText(tview.Escape(ui.Truncate(footer, width)))
}
