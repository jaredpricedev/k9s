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
	"github.com/derailed/k9s/internal/dependency"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/types"
)

const networkReviewCommandToken = "network-review"

func networkReviewActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := target.UnavailableReason
	if reason == "" && (target.GVR == nil || target.GVR.GVR() != client.SvcGVR.GVR() || target.UID == "") {
		reason = "Select a native Service with captured UID"
	}
	return []ui.ActionDescriptor{{ID: "resource.network-path", Label: "Network path and flow evidence", Category: ui.ActionInspect,
		Shortcut: ":" + networkReviewCommandToken, Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openNetworkReview(target, nil); return nil }},
		{ID: "resource.dependency-evidence", Label: "Explicit dependency evidence", Category: ui.ActionInspect, Shortcut: ":" + dependencyReviewCommandToken,
			Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
			Handler: func(*tcell.EventKey) *tcell.EventKey { app.openNetworkReviewTab(target, nil, 7); return nil }}}
}

type networkReviewView struct {
	*Details
	target                          SelectedResourceTarget
	connection                      client.Connection
	scope                           networkpath.Scope
	snapshot                        *networkpath.Snapshot
	dependencies                    *dependency.Review
	edgeItems                       []networkpath.Item
	loader                          func(context.Context, networkpath.Scope) (*networkpath.Snapshot, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	activeTab                       int
	tabStates                       [8]investigationTabState
	selected                        [8]string
	identityBar, tabsBar, footer    *tview.TextView
	width, height                   int
	refreshFailure, selectionNotice string
	observed                        *HubbleView
	flowStartedAt                   time.Time
	flowPins                        []networkpath.FlowPin
}

func (c *Command) networkReviewCommand(line string) {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a native Service for network path review")
		return
	}
	var namespaces []string
	words := strings.Fields(line)
	if len(words) > 2 || len(words) == 2 && !strings.HasPrefix(words[1], "route-ns=") {
		c.app.Flash().Warn("Use :network-review [route-ns=apps,edge]; namespaces are explicit")
		return
	}
	if len(words) == 2 {
		namespaces = strings.Split(strings.TrimPrefix(words[1], "route-ns="), ",")
	}
	c.app.openNetworkReview(actionTarget(owner, c.app.Config.ActiveContextName()), namespaces)
}

//nolint:gocritic // Capture the destination independently before any background read.
func (a *App) openNetworkReview(target SelectedResourceTarget, namespaces []string) {
	a.openNetworkReviewTab(target, namespaces, 0)
}

//nolint:gocritic // Capture an independent destination for the explicitly selected read-only task.
func (a *App) openNetworkReviewTab(target SelectedResourceTarget, namespaces []string, initialTab int) {
	if target.Err() != nil || target.GVR == nil || target.GVR.GVR() != client.SvcGVR.GVR() {
		a.Flash().Warn("Select a native v1 Service with captured UID")
		return
	}
	scope, err := networkpath.NormalizeScope(networkpath.Scope{Service: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(),
		Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, RouteNamespaces: namespaces})
	if err != nil {
		a.Flash().Err(err)
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen network path review")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	v := &networkReviewView{Details: NewDetails(a, "Network path", target.Path(), contentInspection, true), target: target, connection: connection,
		scope: scope, destinationRevision: a.Config.DestinationRevision(), activeTab: initialTab}
	v.loader = func(ctx context.Context, scope networkpath.Scope) (*networkpath.Snapshot, error) {
		reader, err := connection.DynDial()
		if err != nil {
			return nil, err
		}
		return networkpath.Collect(ctx, reader, scope)
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func (*networkReviewView) CompactWorkspace() bool { return true }
func (v *networkReviewView) SelectedResource() SelectedResourceTarget {
	if item := v.selectedItem(); item != nil {
		i := item.Source.Identity
		if i.GVR == "" || i.UID == "" {
			return SelectedResourceTarget{Context: v.target.Context, UnavailableReason: "Flow peers have no proven API resource UID; v shows retained source evidence"}
		}
		return SelectedResourceTarget{Context: i.Context, GVR: client.NewGVR(i.GVR), Namespace: i.Namespace, Name: i.Name, UID: types.UID(i.UID)}
	}
	if v.activeTab > 0 && v.activeTab != 6 {
		return SelectedResourceTarget{Context: v.target.Context, UnavailableReason: "No verified retained source selected; inspect Evidence coverage"}
	}
	return v.target
}

func (v *networkReviewView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).
		AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	v.bindNetworkKeys()
	v.bindExistingSessionsKey()
	v.render()
	return nil
}

func (v *networkReviewView) bindNetworkKeys() {
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(networkpath.Tabs[index], func(e *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return e
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next network review tab", func(e *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return e
		}
		v.selectTab((v.activeTab + 1) % len(networkpath.Tabs))
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh configured path evidence", func(e *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return e
		}
		v.refresh()
		return nil
	}, true))
	targetAction := ui.NewKeyAction("Inspect captured edge target reference", v.inspectEdgeTarget, true)
	targetAction.Availability = func() string { return v.edgeTarget().UnavailableReason }
	v.actions.Add(ui.KeyB, targetAction)
	v.actions.Add(ui.KeyD, ui.NewKeyAction("Explicit dependency evidence edges", func(e *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return e
		}
		v.selectTab(7)
		return nil
	}, true))
	v.actions.Add(ui.KeyJ, ui.NewKeyAction("Next retained path source", func(e *tcell.EventKey) *tcell.EventKey { return v.moveSelection(e, 1) }, true))
	v.actions.Add(ui.KeyK, ui.NewKeyAction("Previous retained path source", func(e *tcell.EventKey) *tcell.EventKey { return v.moveSelection(e, -1) }, true))
	v.actions.Add(tcell.KeyEnter, ui.NewSharedKeyAction("Inspect captured source identity", v.inspectSource, true))
	v.actions.Add(ui.KeyV, ui.NewKeyAction("Exact retained path evidence", v.showEvidence, true))
	v.actions.Add(ui.KeyH, ui.NewKeyAction("Explicit native Hubble observation / peer conversations", v.openFlows, true))
	v.actions.Add(ui.KeyP, ui.NewKeyAction("Pin reported source endpoint", func(e *tcell.EventKey) *tcell.EventKey { return v.pinFlow(e, networkpath.PinSource) }, true))
	v.actions.Add(ui.KeyT, ui.NewKeyAction("Pin reported destination endpoint", func(e *tcell.EventKey) *tcell.EventKey {
		return v.pinFlow(e, networkpath.PinDestination)
	}, true))
	v.actions.Add(ui.KeyShiftP, ui.NewKeyAction("Pin reported conversation", func(e *tcell.EventKey) *tcell.EventKey {
		return v.pinFlow(e, networkpath.PinConversation)
	}, true))
}

func (v *networkReviewView) Start() {
	v.active = true
	v.app.Prompt().SetModel(v.cmdBuff)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.StylesChanged(v.app.Styles)
	v.captureFlows()
	if v.snapshot == nil && !v.loading {
		v.refresh()
	} else {
		v.render()
	}
}
func (v *networkReviewView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *networkReviewView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.identityBar != nil {
		p := styles.Semantic()
		for _, bar := range []*tview.TextView{v.identityBar, v.tabsBar, v.footer} {
			bar.SetBackgroundColor(p.Canvas.Color())
			bar.SetTextColor(config.ReadableForeground(p.Text.Color(), p.Canvas.Color()))
		}
	}
	v.render()
}
func (v *networkReviewView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	v.Flex.Draw(screen)
}
func (v *networkReviewView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}

func (v *networkReviewView) selectTab(tab int) {
	if tab < 0 || tab >= len(networkpath.Tabs) || tab == v.activeTab {
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
func (v *networkReviewView) taskItems(tab int) []*networkpath.Item {
	if v.snapshot == nil {
		return nil
	}
	if tab != 7 {
		return v.snapshot.TabItems(tab)
	}
	items := make([]*networkpath.Item, 0, len(v.edgeItems))
	for i := range v.edgeItems {
		items = append(items, &v.edgeItems[i])
	}
	return items
}
func (v *networkReviewView) selectedIndex() int {
	if v.snapshot != nil {
		for i, item := range v.taskItems(v.activeTab) {
			if networkpath.ItemKey(item) == v.selected[v.activeTab] {
				return i
			}
		}
	}
	return 0
}
func (v *networkReviewView) selectedItem() *networkpath.Item {
	if v.snapshot == nil {
		return nil
	}
	items := v.taskItems(v.activeTab)
	if len(items) == 0 {
		return nil
	}
	return items[v.selectedIndex()]
}
func (v *networkReviewView) moveSelection(e *tcell.EventKey, step int) *tcell.EventKey {
	if v.cmdBuff.IsActive() || v.snapshot == nil {
		return e
	}
	items := v.taskItems(v.activeTab)
	if len(items) == 0 {
		return e
	}
	v.selected[v.activeTab] = networkpath.ItemKey(items[(v.selectedIndex()+step+len(items))%len(items)])
	v.text.ScrollToBeginning()
	v.render()
	return nil
}

func (v *networkReviewView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen network review"
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
	generation, scope := v.generation, v.scope
	ctx, cancel := context.WithTimeout(context.Background(), networkpath.CollectionTimeout)
	v.cancel, v.loading = cancel, true
	v.render()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, scope)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && generation == v.generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, err)
			}
		})
	}()
}
func (v *networkReviewView) acceptSnapshot(snapshot *networkpath.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = err.Error()
		if v.snapshot == nil {
			v.snapshot = snapshot
		}
	} else {
		v.refreshFailure = ""
		v.snapshot = snapshot
	}
	v.captureFlows()
	v.rebuildDependencies()
	v.selectionNotice = ""
	if v.snapshot != nil {
		for tab, key := range v.selected {
			if key == "" {
				continue
			}
			found := false
			for _, item := range v.taskItems(tab) {
				if networkpath.ItemKey(item) == key {
					found = true
				}
			}
			if !found {
				v.selected[tab] = ""
				v.selectionNotice = "Selected source no longer retained; inspect coverage before navigating"
			}
		}
	}
	v.render()
}

func (v *networkReviewView) render() {
	if v.identityBar == nil {
		return
	}
	v.rebuildDependencies()
	// Retained source lists occupy fixed physical rows; full fields are in evidence.
	// The terminal widget's paragraph wrapper splits UTF-8 by bytes at narrow widths.
	v.text.SetWrap(v.activeTab == 0 || v.activeTab == 6)
	width, height := v.width, v.height
	if width == 0 {
		width = 78
	}
	if height == 0 {
		height = 20
	}
	status := "Configured only · flows not requested"
	body := "Loading bounded configured path evidence..."
	if v.snapshot != nil {
		status = fmt.Sprintf("Untested · %sZ · gaps %d · omitted %d", v.snapshot.CapturedAt.UTC().Format("15:04"), v.snapshot.GapCount(), v.snapshot.Omitted)
		body = v.snapshot.Render(v.activeTab, v.selectedIndex(), width, height-5)
		if v.activeTab == 7 {
			status = fmt.Sprintf("References/reports · %sZ · gaps %d", v.snapshot.CapturedAt.UTC().Format("15:04"), len(v.dependencies.Gaps))
			body = v.dependencies.Summary() + "\n\n" + v.renderEdges(width, height)
			if len(v.edgeItems) == 0 {
				body = v.dependencies.Summary() + "\n\n" + v.dependencies.Evidence()
			}
		}
	}
	if v.loading {
		status = "Reading captured scope · 15s deadline"
	}
	if v.refreshFailure != "" {
		status = "Retained evidence · refresh failed"
		body = "Refresh: " + v.refreshFailure + "\n\n" + body
	}
	if v.selectionNotice != "" {
		body = v.selectionNotice + "\n" + body
	}
	scopeLabel := "Routes: " + strings.Join(v.scope.RouteNamespaces, ", ")
	if v.activeTab == 7 {
		scopeLabel = "Edge group: " + v.scope.Service.Namespace + " · no federation"
	}
	v.identityBar.SetText(tview.Escape(fmt.Sprintf("%s · Service %s\n%s\n%s", v.target.Context, v.target.Path(), status, scopeLabel)))
	v.tabsBar.SetText(ui.TaskTabs(networkpath.Tabs, v.activeTab, width))
	v.footer.SetText("r refresh · h Hubble · v evidence · Enter inspect · ? help")
	if width < 80 {
		v.footer.SetText("r refresh · h flows · v evidence · ? help")
	}
	if width < 50 {
		v.footer.SetText("Enter inspect · v evidence · ? help")
	}
	if v.activeTab == 7 {
		v.footer.SetText("Enter source · b target · v evidence · ? help")
		if width < 50 {
			v.footer.SetText("Enter/b source/target · v · ? help")
		}
	}
	if v.activeTab == 5 {
		v.footer.SetText("p source · t dest · P conversation · v evidence · ? help")
		if width < 50 {
			v.footer.SetText("p/t endpoint · P pair · v · ? help")
		}
	}
	v.updateExistingSessionsFooter(width)
	row, col := v.text.GetScrollOffset()
	region := v.currentRegion
	v.Update(body)
	if v.inspectionQuery != "" {
		v.model.Filter(v.inspectionQuery)
		v.currentRegion = region
	}
	v.text.ScrollTo(row, col)
}

func (v *networkReviewView) inspectSource(e *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return v.Details.filterCmd(e)
	}
	if !v.destinationCurrent() {
		v.app.Flash().Warn("Destination changed; reopen before source navigation")
		return nil
	}
	target := v.SelectedResource()
	if err := target.Err(); err != nil {
		v.app.Flash().Warn(err.Error())
		return nil
	}
	v.app.openTargetInspection(target, troubleshootCommand)
	return nil
}
func (v *networkReviewView) showEvidence(e *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return e
	}
	if v.snapshot == nil {
		return nil
	}
	const page = "network-review-retained-evidence"
	done := func() { v.app.Content.Pages.RemovePage(page); v.app.SetFocus(v) }
	title, body := "Network evidence · retained", v.snapshot.Evidence(v.selectedItem())
	if v.activeTab == 7 {
		index := v.selectedIndex()
		if v.selectedItem() == nil {
			index = -1
		}
		title, body = "Dependency evidence · retained", v.dependencies.EvidenceFor(index)
	}
	modal := ui.NewMessageModal(v.app.Styles, title, body, done)
	v.app.Content.Pages.AddPage(page, modal, true, true)
	v.app.Content.Pages.SetPageCleanup(page, modal.Cleanup)
	v.app.SetFocus(modal)
	return nil
}

func (v *networkReviewView) openFlows(e *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return e
	}
	if !v.destinationCurrent() || v.snapshot == nil || len(v.snapshot.Pods) == 0 {
		v.app.Flash().Warn("Obtain scoped backend Pod identities first; empty evidence does not prove no traffic")
		return nil
	}
	if v.observed != nil {
		if err := v.app.inject(v.observed, false); err != nil {
			v.app.Flash().Err(err)
		}
		return nil
	}
	pods := make([]string, 0, len(v.snapshot.Pods))
	for i := range v.snapshot.Pods {
		id := v.snapshot.Pods[i].Source.Identity
		pods = append(pods, client.FQN(id.Namespace, id.Name))
	}
	v.observed = newHubbleView(hubble.Scope{Title: "Service " + v.target.Path() + " (captured backend names; flow UIDs unreported)", Pods: pods}, false)
	v.flowStartedAt = time.Now().UTC()
	if err := v.app.inject(v.observed, false); err != nil {
		v.app.Flash().Err(err)
	}
	return nil
}
func (v *networkReviewView) captureFlows() {
	if v.snapshot == nil || v.observed == nil || v.observed.session == nil {
		return
	}
	events, evicted := v.observed.session.Store.Snapshot()
	status := v.observed.session.Status()
	status.Phase = "Native observation stopped while away; retained window, h reopens / r in Hubble reconnects"
	v.snapshot.Flows = networkpath.ComposeFlows(&networkpath.FlowSample{Context: v.target.Context, StartedAt: v.flowStartedAt, CapturedAt: time.Now().UTC(),
		Events: events, Status: status, Evicted: evicted})
	v.applyFlowPins()
}

func (v *networkReviewView) pinFlow(e *tcell.EventKey, kind string) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return e
	}
	item := v.selectedItem()
	if v.activeTab != 5 || item == nil {
		v.app.Flash().Warn("Choose a retained flow source first; pins are reported identities, never API UID proof")
		return nil
	}
	key := item.ConversationKey
	if kind == networkpath.PinSource {
		key = item.EndpointKeys[0]
	}
	if kind == networkpath.PinDestination {
		key = item.EndpointKeys[1]
	}
	if key == "" {
		return nil
	}
	for i, pin := range v.flowPins {
		if pin.Kind == kind && pin.Key == key {
			v.flowPins = append(v.flowPins[:i], v.flowPins[i+1:]...)
			v.applyFlowPins()
			v.render()
			return nil
		}
	}
	if len(v.flowPins) >= 32 {
		v.app.Flash().Warn("At most 32 reported endpoint/conversation pins; unpin an entry first")
		return nil
	}
	v.flowPins = append(v.flowPins, networkpath.FlowPin{Kind: kind, Key: key, Context: v.target.Context, CreatedAt: time.Now().UTC()})
	v.applyFlowPins()
	v.render()
	return nil
}
func (v *networkReviewView) applyFlowPins() {
	if v.snapshot == nil || v.snapshot.Flows == nil {
		return
	}
	v.snapshot.Flows.Pins = append([]networkpath.FlowPin(nil), v.flowPins...)
	for i := range v.snapshot.Flows.Items {
		item := &v.snapshot.Flows.Items[i]
		item.Pinned = false
		for _, pin := range v.flowPins {
			if (pin.Kind == networkpath.PinConversation && pin.Key == item.ConversationKey) ||
				(pin.Kind == networkpath.PinSource && pin.Key == item.EndpointKeys[0]) || (pin.Kind == networkpath.PinDestination && pin.Key == item.EndpointKeys[1]) {
				item.Pinned = true
			}
		}
	}
}
