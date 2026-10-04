// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const capacityCommandToken = "capacity"
const capacityTitle = "Capacity review"

type capacityView struct {
	*Details
	target                          SelectedResourceTarget
	snapshot                        *capacity.Snapshot
	loader                          func(context.Context, *SelectedResourceTarget) (*capacity.Snapshot, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	activeTab                       int
	tabStates                       [6]investigationTabState
	identityBar, tabsBar, footer    *tview.TextView
	width, height                   int
	refreshFailure                  string
	metricStateKey                  string
}

func (c *Command) capacityCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a Pod, workload, Namespace or Node for capacity review")
		return
	}
	c.app.openCapacityReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openCapacityReview(target SelectedResourceTarget) {
	if err := capacityTargetError(&target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen capacity review")
		return
	}
	conn, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	namespace := client.CleanseNamespace(a.Config.ActiveNamespace())
	v := &capacityView{Details: NewDetails(a, capacityTitle, target.Path(), contentInspection, true), target: target, destinationRevision: a.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, target *SelectedResourceTarget) (*capacity.Snapshot, error) {
		return loadCapacityReview(ctx, conn, target, namespace, time.Now())
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func capacityTargetError(target *SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	switch target.GVR.String() {
	case client.PodGVR.String(), client.DpGVR.String(), client.StsGVR.String(), client.DsGVR.String(),
		client.RsGVR.String(), client.JobGVR.String(), client.NsGVR.String(), client.NodeGVR.String():
	default:
		return fmt.Errorf("Select a native Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, Namespace or Node")
	}
	if target.UID == "" {
		return fmt.Errorf("Resource UID unavailable; refresh the source list and select the resource again")
	}
	return nil
}

func loadCapacityReview(ctx context.Context, conn client.Connection, target *SelectedResourceTarget, namespace string, now time.Time) (*capacity.Snapshot, error) {
	if err := capacityTargetError(target); err != nil {
		return nil, err
	}
	reader, err := conn.DynDial()
	if err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithTimeout(ctx, capacity.ReadTimeout)
	obj, err := reader.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(readCtx, target.Name, metav1.GetOptions{})
	cancel()
	if err != nil {
		return nil, err
	}
	if identityErr := verifySelectedIdentity(*target, obj); identityErr != nil {
		return nil, identityErr
	}
	scope, err := capacityScope(target, obj, namespace)
	if err != nil {
		return nil, err
	}
	return capacity.Collect(ctx, reader, scope, now), nil
}

func capacityScope(target *SelectedResourceTarget, obj *unstructured.Unstructured, namespace string) (*capacity.Scope, error) {
	scope := &capacity.Scope{Identity: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(),
		Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID())}, Kind: obj.GetKind(), Namespace: obj.GetNamespace()}
	switch target.GVR.String() {
	case client.PodGVR.String():
		scope.PodName = obj.GetName()
	case client.NsGVR.String():
		scope.Namespace = obj.GetName()
	case client.NodeGVR.String():
		scope.Node = obj.GetName()
		scope.Namespace = namespace
	default:
		raw, found, err := unstructured.NestedMap(obj.Object, "spec", "selector")
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("Workload has no reported Pod selector")
		}
		var selector metav1.LabelSelector
		if conversionErr := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); conversionErr != nil {
			return nil, conversionErr
		}
		parsed, err := metav1.LabelSelectorAsSelector(&selector)
		if err != nil {
			return nil, err
		}
		if parsed.Empty() {
			return nil, fmt.Errorf("Workload selector is empty; refusing an unrelated namespace-wide Pod read")
		}
		scope.Selector = parsed.String()
	}
	return scope, nil
}

func (v *capacityView) SelectedResource() SelectedResourceTarget { return v.target }
func (*capacityView) CompactWorkspace() bool                     { return true }

func (v *capacityView) Init(ctx context.Context) error {
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
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(capacity.Tabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next capacity tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + 1) % len(capacity.Tabs))
		return nil
	}, true))
	v.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous capacity tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + len(capacity.Tabs) - 1) % len(capacity.Tabs))
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh capacity evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.render()
	return nil
}

func (v *capacityView) Start() {
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
func (v *capacityView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *capacityView) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *capacityView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	if v.snapshot != nil && v.snapshot.MetricStateKey(time.Now()) != v.metricStateKey {
		v.render()
	}
	v.renderChrome()
	v.Flex.Draw(screen)
}
func (v *capacityView) selectTab(tab int) {
	if tab < 0 || tab >= len(capacity.Tabs) || tab == v.activeTab {
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
func (v *capacityView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}
func (v *capacityView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen capacity review"
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
func (v *capacityView) acceptSnapshot(snapshot *capacity.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = err.Error()
	} else if snapshot == nil {
		v.refreshFailure = "No capacity observation returned"
	} else {
		v.refreshFailure = ""
		v.snapshot = snapshot
		v.target.UID = types.UID(snapshot.Scope.Identity.UID)
	}
	v.render()
}
func (v *capacityView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading read-only capacity evidence..."
	if v.snapshot != nil {
		text = v.snapshot.Render(v.activeTab)
		v.metricStateKey = v.snapshot.MetricStateKey(time.Now())
	} else if v.refreshFailure != "" {
		text = "Capacity evidence unavailable: " + v.refreshFailure
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
func (v *capacityView) renderChrome() {
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
	identity := capacityTitle + " / " + v.target.Path()
	source := retainedObservationPending
	state := retainedWaitingForReads
	if v.snapshot != nil {
		source = "Captured " + v.snapshot.CapturedAt.UTC().Format("15:04:05Z") + " | " + v.target.Context
		state = "Read only | complete visible page"
		if v.snapshot.Partial() {
			state = "Read only | partial evidence | 6: sources"
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
	v.tabsBar.SetText(ui.TaskTabs(capacity.Tabs, v.activeTab, width))
	v.footer.SetText(tview.Escape(ui.Truncate("Tab next  r refresh  / search  Esc back", width)))
}
