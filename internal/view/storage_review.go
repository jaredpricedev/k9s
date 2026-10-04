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
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/storage"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const storageCommandToken = "storage"
const storageTitle = "Storage diagnosis"

type storageView struct {
	*Details
	target                          SelectedResourceTarget
	snapshot                        *storage.Snapshot
	loader                          func(context.Context, *SelectedResourceTarget) (*storage.Snapshot, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	activeTab                       int
	tabStates                       [5]investigationTabState
	identityBar, tabsBar, footer    *tview.TextView
	width, height                   int
	refreshFailure                  string
	formOpen                        bool
	modal                           *storageExpansionModal
	connection                      client.Connection
	expandSubmit                    func(*storage.ExpansionPlan)
}

func (c *Command) storageCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a Pod, PVC, PV or StorageClass for storage diagnosis")
		return
	}
	c.app.openStorageReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openStorageReview(target SelectedResourceTarget) {
	if err := storageTargetError(&target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen storage diagnosis")
		return
	}
	conn, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	namespace := client.CleanseNamespace(a.Config.ActiveNamespace())
	v := &storageView{Details: NewDetails(a, storageTitle, target.Path(), contentInspection, true), target: target,
		connection: conn, destinationRevision: a.Config.DestinationRevision()}
	v.expandSubmit = v.submitExpansion
	v.loader = func(ctx context.Context, target *SelectedResourceTarget) (*storage.Snapshot, error) {
		return loadStorageReview(ctx, conn, target, namespace, time.Now())
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func storageTargetError(target *SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	switch target.GVR.String() {
	case client.PodGVR.String(), client.PvcGVR.String(), client.PvGVR.String(), client.ScGVR.String():
	default:
		return fmt.Errorf("Select a native Pod, PVC, PV or StorageClass")
	}
	if target.UID == "" {
		return fmt.Errorf("Resource UID unavailable; refresh the source list and select the resource again")
	}
	return nil
}

func loadStorageReview(ctx context.Context, conn client.Connection, target *SelectedResourceTarget, namespace string, now time.Time) (*storage.Snapshot, error) {
	if err := storageTargetError(target); err != nil {
		return nil, err
	}
	reader, err := conn.DynDial()
	if err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithTimeout(ctx, storage.ReadTimeout)
	obj, err := reader.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(readCtx, target.Name, metav1.GetOptions{})
	cancel()
	if err != nil {
		return nil, err
	}
	if identityErr := verifySelectedIdentity(*target, obj); identityErr != nil {
		return nil, identityErr
	}
	scope := storageScope(target, obj, namespace)
	return storage.Collect(ctx, reader, scope, now), nil
}

func storageScope(target *SelectedResourceTarget, obj *unstructured.Unstructured, namespace string) *storage.Scope {
	scope := &storage.Scope{Identity: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(),
		Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID())}, Kind: obj.GetKind(), Namespace: obj.GetNamespace()}
	switch target.GVR.String() {
	case client.PodGVR.String():
		scope.PodName = obj.GetName()
		scope.Node, _, _ = unstructured.NestedString(obj.Object, "spec", "nodeName")
	case client.PvcGVR.String():
		scope.PVCName = obj.GetName()
		scope.PVName, _, _ = unstructured.NestedString(obj.Object, "spec", "volumeName")
		scope.ClassName, _, _ = unstructured.NestedString(obj.Object, "spec", "storageClassName")
	case client.PvGVR.String():
		scope.PVName = obj.GetName()
		scope.Namespace = namespace
		claimNamespace, found, _ := unstructured.NestedString(obj.Object, "spec", "claimRef", "namespace")
		if found && claimNamespace != "" {
			scope.Namespace = claimNamespace
		}
		scope.PVCName, _, _ = unstructured.NestedString(obj.Object, "spec", "claimRef", "name")
		scope.PVCUID, _, _ = unstructured.NestedString(obj.Object, "spec", "claimRef", "uid")
		scope.ClassName, _, _ = unstructured.NestedString(obj.Object, "spec", "storageClassName")
	case client.ScGVR.String():
		scope.ClassName = obj.GetName()
		scope.Namespace = namespace
	}
	return scope
}

func (v *storageView) SelectedResource() SelectedResourceTarget { return v.target }
func (*storageView) CompactWorkspace() bool                     { return true }

func (v *storageView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).
		AddItem(v.tabsBar, 1, 0, false).AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(storage.Tabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next storage tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + 1) % len(storage.Tabs))
		return nil
	}, true))
	v.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous storage tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + len(storage.Tabs) - 1) % len(storage.Tabs))
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh storage evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.actions.Add(ui.KeyE, ui.NewKeyAction("Preview PVC expansion", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.expansionForm()
		return nil
	}, true))
	v.render()
	return nil
}

func (v *storageView) Start() {
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
func (v *storageView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.closeExpansionForm()
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *storageView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	v.render()
	if v.modal != nil {
		colors := styles.Dialog()
		v.modal.SetDialogColors(&colors)
	}
}
func (v *storageView) Draw(screen tcell.Screen) {
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
func (v *storageView) selectTab(tab int) {
	if tab < 0 || tab >= len(storage.Tabs) || tab == v.activeTab {
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
func (v *storageView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}
func (v *storageView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen storage diagnosis"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
			if v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, err)
			}
		})
	}()
}
func (v *storageView) acceptSnapshot(snapshot *storage.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = err.Error()
	} else if snapshot == nil {
		v.refreshFailure = "No storage observation returned"
	} else {
		v.refreshFailure = ""
		v.snapshot = snapshot
		v.target.UID = types.UID(snapshot.Scope.Identity.UID)
	}
	v.render()
}
func (v *storageView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading read-only storage evidence..."
	if v.snapshot != nil {
		text = v.snapshot.Render(v.activeTab)
	} else if v.refreshFailure != "" {
		text = "Storage evidence unavailable: " + v.refreshFailure
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
func (v *storageView) renderChrome() {
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
	identity := storageTitle + " / " + v.target.Path()
	source := retainedObservationPending
	state := retainedWaitingForReads
	if v.snapshot != nil {
		source = "Captured " + v.snapshot.CapturedAt.UTC().Format("15:04:05Z") + " | " + v.target.Context
		state = "Read only | complete visible page"
		if v.snapshot.Partial() {
			state = "Read only | partial evidence | 5: sources"
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
		lines[index] = tview.Escape(ui.Truncate(line, width))
	}
	v.identityBar.SetText(strings.Join(lines, "\n"))
	v.tabsBar.SetText(ui.TaskTabs(storage.Tabs, v.activeTab, width))
	v.footer.SetText(tview.Escape(ui.Truncate("e expand  r refresh  / search  Esc back", width)))
}
