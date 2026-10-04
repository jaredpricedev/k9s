// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/configreview"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/client-go/dynamic"
)

const configurationCommand = "config-review"

type configurationView struct {
	*Details
	target                          SelectedResourceTarget
	snapshot, previous              *configreview.Snapshot
	loader                          func(context.Context) (*configreview.Snapshot, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	activeTab, width, bodyRows      int
	tabStates                       [5]investigationTabState
	identityBar, tabsBar, footer    *tview.TextView
	refreshFailure                  string
}

func (c *Command) configurationCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a native workload, ConfigMap or Secret")
		return
	}
	c.app.openConfigurationReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // Capture an immutable selection before asynchronous reads.
func configurationScope(target SelectedResourceTarget) (*configreview.Scope, error) {
	if err := target.Err(); err != nil {
		return nil, err
	}
	scope := &configreview.Scope{Identity: configreview.Identity{ResourceIdentity: inspect.ResourceIdentity{
		Context: target.Context, GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}}}
	return scope, scope.Validate()
}

//nolint:gocritic // Keep the captured destination independent of later selections.
func (a *App) openConfigurationReview(target SelectedResourceTarget) {
	scope, err := configurationScope(target)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen configuration review")
		return
	}
	if a.Conn() == nil || a.Conn().Config() == nil {
		a.Flash().Warn("Select a configured context first")
		return
	}
	actor := a.Conn().Config().Snapshot(target.Context)
	cfg, err := actor.RESTConfig()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	objects, err := dynamic.NewForConfig(cfg)
	if err != nil {
		a.Flash().Err(errors.New("captured resource transport unavailable"))
		return
	}
	secrets, err := configreview.NewSecretMetadataReader(cfg)
	if err != nil {
		a.Flash().Err(err)
		return
	}
	readers := &configreview.Readers{Objects: objects, Secrets: secrets}
	v := &configurationView{Details: NewDetails(a, "Configuration review", target.Path(), contentInspection, true),
		target: target, destinationRevision: a.Config.DestinationRevision()}
	v.loader = func(ctx context.Context) (*configreview.Snapshot, error) {
		return configreview.Collect(ctx, readers, scope)
	}
	v.Update("Loading declared configuration references...")
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func (*configurationView) CompactWorkspace() bool                     { return true }
func (v *configurationView) SelectedResource() SelectedResourceTarget { return v.target }

func (v *configurationView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).
		AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(configreview.Tabs[tab], func(e *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return e
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	for _, binding := range []struct {
		key   tcell.Key
		step  int
		label string
	}{
		{tcell.KeyTab, 1, "Next configuration tab"}, {tcell.KeyBacktab, -1, "Previous configuration tab"},
	} {
		v.actions.Add(binding.key, ui.NewKeyAction(binding.label, func(e *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return e
			}
			v.selectTab((v.activeTab + binding.step + 5) % 5)
			return nil
		}, true))
	}
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh declared references", func(e *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return e
		}
		v.refresh()
		return nil
	}, true))
	v.model.RemoveListener(v.Details)
	v.model.AddListener(v)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.render()
	return nil
}

func (v *configurationView) Start() {
	v.active = true
	v.app.Prompt().SetModel(v.cmdBuff)
	v.app.Styles.AddListener(v)
	v.StylesChanged(v.app.Styles)
	if v.snapshot == nil && !v.loading && v.loader != nil {
		v.refresh()
	}
}
func (v *configurationView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *configurationView) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *configurationView) Hints() model.MenuHints              { return actionCatalogHints(v, v.app) }
func (*configurationView) ExtraHints() map[string]string {
	return map[string]string{"Configuration evidence": "Declared references are not runtime values. Secret key inventory is unknown. " +
		"5 shows source identities, times and bounded coverage; r explicitly refreshes."}
}
func (v *configurationView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	identityRows := 3
	if height < 16 {
		identityRows = 1
	}
	v.Flex.ResizeItem(v.identityBar, identityRows, 0)
	bodyRows := height - identityRows - 2
	if width != v.width || bodyRows != v.bodyRows {
		v.width, v.bodyRows = width, bodyRows
		v.render()
	}
	v.renderChrome()
	v.Flex.Draw(screen)
}
func (v *configurationView) selectTab(tab int) {
	if tab < 0 || tab >= 5 || tab == v.activeTab {
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
func (v *configurationView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}
func (v *configurationView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen review"
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
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	v.cancel, v.loading = cancel, true
	v.renderChrome()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, err)
			}
		})
	}()
}
func (v *configurationView) acceptSnapshot(snapshot *configreview.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = err.Error()
	} else {
		v.refreshFailure = ""
		v.previous, v.snapshot = v.snapshot, snapshot
	}
	v.render()
}
func (v *configurationView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "No observation yet. r explicitly reads this source."
	if v.snapshot != nil {
		switch v.activeTab {
		case 1:
			text = v.snapshot.ReferenceText()
		case 2:
			text = v.snapshot.ConsumerText()
		case 3:
			text = v.snapshot.Changes(v.previous)
		case 4:
			text = v.snapshot.Evidence()
		default:
			text = v.snapshot.Overview()
		}
	} else if v.refreshFailure != "" {
		text = "[?] Evidence unavailable: " + v.refreshFailure
	}
	v.text.SetWrap(v.activeTab != 0)
	if v.activeTab == 0 {
		lines := strings.Split(text, "\n")
		for i := range lines {
			lines[i] = fitInvestigation(lines[i], max(1, v.width))
		}
		text = strings.Join(lines, "\n")
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
	v.updateTitle()
}
func (v *configurationView) renderChrome() {
	if v.identityBar == nil {
		return
	}
	p := v.app.Styles.Semantic()
	for _, item := range []*tview.TextView{v.identityBar, v.tabsBar, v.footer} {
		item.SetBackgroundColor(p.Panel.Color())
		item.SetTextColor(p.Text.Color())
	}
	width := v.width
	if width <= 0 {
		width = 76
	}
	first := v.target.Context + " · " + v.target.Path()
	second, third := "No observation obtained yet", "READ ONLY · declared references"
	if v.snapshot != nil {
		second = fmt.Sprintf("UID %s · captured %s", v.target.UID, v.snapshot.CapturedAt.Format("15:04:05Z"))
		if v.bodyRows < 10 {
			first += " · " + v.snapshot.CapturedAt.Format("15:04Z")
		}
	}
	if v.loading {
		third = "[~] Refreshing; retained capture"
	} else if v.refreshFailure != "" {
		third = "[?] Refresh failed; retained · " + v.refreshFailure
	}
	if !v.destinationCurrent() {
		third = "[?] Destination changed; retained evidence"
	}
	if v.bodyRows < 10 && (v.refreshFailure != "" || !v.destinationCurrent()) {
		first = "[?] Retained · " + first
	}
	v.identityBar.SetText(detailStyled(p.Focus.String(), "b", fitInvestigation(first, width)) + "\n" +
		detailStyled(p.Muted.String(), "", fitInvestigation(second, width)) + "\n" + detailStyled(p.Warning.String(), "", fitInvestigation(third, width)))
	v.tabsBar.SetText(ui.TaskTabs(configreview.Tabs, v.activeTab, width))
	v.footer.SetText(detailStyled(p.Muted.String(), "", "Tab tabs · r refresh · Esc back · ? help"))
}
