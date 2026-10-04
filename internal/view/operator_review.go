// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/operator"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const operatorCommandToken = "operator-review"
const operatorTitle = "Operator review"

type operatorView struct {
	*Details
	target                          SelectedResourceTarget
	snapshot                        *operator.Report
	loader                          func(context.Context, *SelectedResourceTarget) (*operator.Report, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	activeTab                       int
	tabStates                       [4]investigationTabState
	identityBar, tabsBar, footer    *tview.TextView
	width, height                   int
	refreshFailure                  string
}

func (c *Command) operatorCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a cert-manager.io/v1 Certificate; generic CRD browsing remains available")
		return
	}
	c.app.openOperatorReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openOperatorReview(target SelectedResourceTarget) {
	if err := operatorTargetError(&target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen operator review")
		return
	}
	conn, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	rule, customJump := a.CustomJumps().GetRule(target.GVR)
	if customJump {
		customJump = rule.ValidateRule() == nil
	}
	scope := provider.Scope{Context: target.Context, Namespace: client.CleanseNamespace(a.Config.ActiveNamespace()),
		TargetNamespace: target.Namespace, GVR: target.GVR.String(), Name: target.Name, UID: string(target.UID), Revision: a.Config.DestinationRevision()}
	v := &operatorView{Details: NewDetails(a, operatorTitle, target.Path(), contentInspection, true), target: target, destinationRevision: scope.Revision}
	v.loader = func(ctx context.Context, _ *SelectedResourceTarget) (*operator.Report, error) {
		reader, err := conn.DynDial()
		if err != nil {
			return nil, fmt.Errorf("Operator API reader unavailable")
		}
		return operator.Collect(ctx, reader, &scope, customJump, time.Now()), nil
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}
func operatorTargetError(target *SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if !operator.Supports(target.GVR.String()) {
		return fmt.Errorf("Unsupported operator semantics for %s; generic YAML/describe and configured custom jumps remain available", target.GVR.String())
	}
	scope := provider.Scope{Context: target.Context, GVR: target.GVR.String(), TargetNamespace: target.Namespace, Name: target.Name, UID: string(target.UID)}
	if !operator.ValidScope(&scope) {
		return fmt.Errorf("Certificate captured identity unavailable or unsafe; refresh source list and select again")
	}
	return nil
}
func (v *operatorView) SelectedResource() SelectedResourceTarget { return v.target }
func (*operatorView) CompactWorkspace() bool                     { return true }

func (v *operatorView) Init(ctx context.Context) error {
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
		v.actions.Add(key, ui.NewKeyAction(operator.Tabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next operator tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + 1) % len(operator.Tabs))
		return nil
	}, true))
	v.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous operator tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + len(operator.Tabs) - 1) % len(operator.Tabs))
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh operator evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.actions.Add(ui.KeyC, ui.NewKeyAction("Cancel collection", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		if v.cancel != nil {
			v.cancel()
		}
		v.generation++
		v.loading = false
		v.refreshFailure = "Canceled; prior evidence retained"
		v.render()
		return nil
	}, true))
	v.render()
	return nil
}

func (v *operatorView) Start() {
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
func (v *operatorView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *operatorView) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *operatorView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}

	v.renderChrome()
	v.Flex.Draw(screen)
}
func (v *operatorView) selectTab(tab int) {
	if tab < 0 || tab >= len(operator.Tabs) || tab == v.activeTab {
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
func (v *operatorView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}
func (v *operatorView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen operator review"
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
	generation, target := v.generation, v.target
	ctx, cancel := context.WithTimeout(v.app.sessionContext(), operator.CollectionTimeout)
	v.cancel, v.loading = cancel, true
	v.renderChrome()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, &target)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			v.applyRefreshResult(generation, snapshot, err)
		})
	}()
}
func (v *operatorView) applyRefreshResult(generation uint64, snapshot *operator.Report, err error) bool {
	if !v.active || v.generation != generation || !v.destinationCurrent() || v.app.Content.Top() != v {
		return false
	}
	v.acceptSnapshot(snapshot, err)
	return true
}
func (v *operatorView) acceptSnapshot(snapshot *operator.Report, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = "Collection failed or timed out"
	} else if snapshot == nil {
		v.refreshFailure = "No operator observation returned"
	} else if v.snapshot != nil && snapshot.CollectionFailed() {
		v.refreshFailure = "Refresh source " + snapshot.State() + "; previous evidence retained"
	} else {
		v.refreshFailure = ""
		v.snapshot = snapshot
	}
	v.render()
}
func (v *operatorView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading read-only operator evidence..."
	if v.snapshot != nil {
		text = v.snapshot.Render(v.activeTab)
	} else if v.refreshFailure != "" {
		text = "Operator evidence unavailable: " + v.refreshFailure
	}
	if v.snapshot != nil && v.refreshFailure != "" {
		text = "Refresh failed: " + v.refreshFailure + "\nPrevious captured evidence retained.\n\n" + text
	}
	query, region := v.inspectionQuery, v.currentRegion
	row, col := v.text.GetScrollOffset()
	v.Update(text)
	if query != "" {
		v.model.Filter(query)
		if region < v.maxRegions {
			v.currentRegion = region
			v.text.Highlight(fmt.Sprintf("search_%d", region))
		}
	}
	v.text.ScrollTo(row, col)
}
func (v *operatorView) renderChrome() {
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
	identity := operatorTitle + " / " + v.target.Path()
	source := retainedObservationPending
	state := retainedWaitingForReads
	if v.snapshot != nil {
		source = v.snapshot.CapturedAt.UTC().Format("15:04:05Z")
		state = "RO | controller report"
		if v.snapshot.State() != operator.ControllerReport {
			state = "RO | " + v.snapshot.State() + " | 4: sources"
		}
	}
	if v.loading {
		if v.snapshot != nil {
			state = retainedRefreshing
		}
	} else if v.refreshFailure != "" {
		state = "Unavailable | r retry | " + v.refreshFailure
		if v.snapshot != nil {
			state = "Retained / failed refresh"
		}
	}
	if !v.destinationCurrent() {
		state = retainedDestinationChanged
	}
	lines := []string{identity + " | " + v.target.Context, "Controller report != live TLS proof", source + " | " + state}
	for index, line := range lines {
		lines[index] = tview.Escape(ui.Truncate(line, width))
	}
	v.identityBar.SetText(strings.Join(lines, "\n"))
	v.tabsBar.SetText(ui.TaskTabs(operator.Tabs, v.activeTab, width))
	v.footer.SetText(tview.Escape(ui.Truncate("Tab next r refresh c cancel / Esc back", width)))
}
