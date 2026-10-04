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
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/sahilm/fuzzy"
	"k8s.io/apimachinery/pkg/types"
)

const jobReviewCommandToken, jobReviewTitle = "job-review", "Job review"

var jobReviewTabs = []string{"Overview", "Runs", "Pods", "Schedule", "Evidence"}

type jobReviewView struct {
	*Details
	target               SelectedResourceTarget
	connection           client.Connection
	snapshot             *review.JobReviewSnapshot
	loader               func(context.Context, SelectedResourceTarget) (*review.JobReviewSnapshot, error)
	cancel               context.CancelFunc
	generation           uint64
	destinationRevision  uint64
	active, loading      bool
	activeTab            int
	tabStates            [5]investigationTabState
	selectedRunUID       string
	selectedPodUID       string
	identityBar, tabsBar *tview.TextView
	footer               *tview.TextView
	width                int
	bodyRows             int
	refreshFailure       string
	selectionNotice      string
	returnDestination    *inspectionReturnDestination
}

func (c *Command) jobReviewCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a native Job or CronJob to review outcomes")
		return
	}
	c.app.openJobReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // Capture one immutable destination before asynchronous reads.
func (a *App) openJobReview(target SelectedResourceTarget) {
	if err := jobReviewTargetError(target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen Job review")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	v := &jobReviewView{Details: NewDetails(a, jobReviewTitle, target.Path(), contentInspection, true), target: target,
		connection: connection, destinationRevision: a.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, selection SelectedResourceTarget) (*review.JobReviewSnapshot, error) {
		return loadJobReview(ctx, connection, selection)
	}
	v.Update("Loading bounded, read-only Job outcome evidence...")
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

//nolint:gocritic // Validate temporary immutable selection values without modifying them.
func jobReviewTargetError(target SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if target.GVR.GVR() != client.CjGVR.GVR() && target.GVR.GVR() != client.JobGVR.GVR() {
		return fmt.Errorf("Select a native batch/v1 Job or CronJob")
	}
	if !client.IsNamespaced(target.Namespace) || target.UID == "" {
		return fmt.Errorf("Job namespace/UID unavailable; refresh and select the source again")
	}
	return nil
}

func (*jobReviewView) CompactWorkspace() bool { return true }

func (v *jobReviewView) SelectedResource() SelectedResourceTarget {
	if v.snapshot != nil && v.activeTab == 1 && len(v.snapshot.Runs) > 0 {
		return jobIdentityTarget(v.snapshot.Runs[v.runIndex()].Identity, client.JobGVR)
	}
	if v.snapshot != nil && v.activeTab == 2 && len(v.snapshot.Pods) > 0 {
		return jobIdentityTarget(v.snapshot.Pods[v.podIndex()].Identity, client.PodGVR)
	}
	if v.activeTab == 1 || v.activeTab == 2 {
		return SelectedResourceTarget{Context: v.target.Context, UnavailableReason: "No verified retained run/Pod selected; inspect collection coverage"}
	}
	return v.target
}

//nolint:gocritic // Copy retained identity into an independently owned navigation target.
func jobIdentityTarget(identity inspect.ResourceIdentity, gvr *client.GVR) SelectedResourceTarget {
	return SelectedResourceTarget{Context: identity.Context, GVR: gvr, Namespace: identity.Namespace,
		Name: identity.Name, UID: types.UID(identity.UID)}
}

func (v *jobReviewView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.model.RemoveListener(v.Details)
	v.model.AddListener(v)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).
		AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	v.bindReviewKeys()
	v.render()
	return nil
}

func (v *jobReviewView) bindReviewKeys() {
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(jobReviewTabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	for _, item := range []struct {
		key   tcell.Key
		step  int
		label string
	}{{tcell.KeyTab, 1, "Next Job review tab"}, {tcell.KeyBacktab, -1, "Previous Job review tab"}} {
		v.actions.Add(item.key, ui.NewKeyAction(item.label, func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab((v.activeTab + item.step + len(jobReviewTabs)) % len(jobReviewTabs))
			return nil
		}, true))
	}
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh Job evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.actions.Add(ui.KeyJ, ui.NewKeyAction("Next retained run/Pod", func(event *tcell.EventKey) *tcell.EventKey { return v.moveSelection(event, 1) }, true))
	v.actions.Add(ui.KeyK, ui.NewKeyAction("Previous retained run/Pod", func(event *tcell.EventKey) *tcell.EventKey { return v.moveSelection(event, -1) }, true))
	v.actions.Add(tcell.KeyEnter, ui.NewSharedKeyAction("Inspect selected Job/Pod", v.inspectSelection, true))
	v.actions.Add(ui.KeyV, ui.NewKeyAction("Exact retained selected evidence", v.showSelectedEvidence, true))
	v.actions.Add(ui.KeyO, ui.NewKeyAction("Open native source actions", v.openSourceActions, true))
	logs := ui.NewKeyAction("Selected Pod logs", v.openPodLogs, true)
	logs.Availability = v.podLogsReason
	v.actions.Add(ui.KeyL, logs)
}

func (v *jobReviewView) Start() {
	v.restoreSourceNamespace()
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

func (v *jobReviewView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *jobReviewView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	v.render()
}

func (v *jobReviewView) Hints() model.MenuHints { return actionCatalogHints(v, v.app) }
func (*jobReviewView) ExtraHints() map[string]string {
	return map[string]string{"Job evidence": "Schedule preview is calculated; Runs are observed API records. Missing retained Jobs do not prove missed runs.",
		"Source actions": "o opens the captured native source; CronJob t triggers, s suspends/resumes. :operations reviews guarded progress and accepted Job identity."}
}

func (v *jobReviewView) Draw(screen tcell.Screen) {
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

func (v *jobReviewView) TextChanged(lines []string) {
	v.text.SetText(rolloutMarkup(v.app, strings.Join(lines, "\n")))
}

func (v *jobReviewView) TextFiltered(lines []string, matches fuzzy.Matches) {
	v.Details.TextFiltered(lines, matches)
	v.text.SetText(enableRegion(rolloutMarkup(v.app, strings.Join(linesWithRegions(lines, matches), "\n"))))
}

func (v *jobReviewView) selectTab(tab int) {
	if tab < 0 || tab >= len(jobReviewTabs) || tab == v.activeTab {
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

func (v *jobReviewView) runIndex() int {
	if v.snapshot != nil {
		for index := range v.snapshot.Runs {
			if v.snapshot.Runs[index].Identity.UID == v.selectedRunUID {
				return index
			}
		}
	}
	return 0
}

func (v *jobReviewView) podIndex() int {
	if v.snapshot != nil {
		for index := range v.snapshot.Pods {
			if v.snapshot.Pods[index].Identity.UID == v.selectedPodUID {
				return index
			}
		}
	}
	return 0
}

func (v *jobReviewView) moveSelection(event *tcell.EventKey, step int) *tcell.EventKey {
	if v.cmdBuff.IsActive() || v.snapshot == nil {
		return event
	}
	if v.activeTab == 1 && len(v.snapshot.Runs) > 0 {
		index := (v.runIndex() + step + len(v.snapshot.Runs)) % len(v.snapshot.Runs)
		v.selectedRunUID = v.snapshot.Runs[index].Identity.UID
	} else if v.activeTab == 2 && len(v.snapshot.Pods) > 0 {
		index := (v.podIndex() + step + len(v.snapshot.Pods)) % len(v.snapshot.Pods)
		v.selectedPodUID = v.snapshot.Pods[index].Identity.UID
	} else {
		return event
	}
	v.render()
	return nil
}

func (v *jobReviewView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}

func (v *jobReviewView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen Job review"
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	v.cancel, v.loading = cancel, true
	v.renderChrome()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, target)
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

func (v *jobReviewView) acceptSnapshot(snapshot *review.JobReviewSnapshot, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = err.Error()
		if v.snapshot == nil {
			v.snapshot = snapshot
		}
	} else {
		v.refreshFailure, v.snapshot = "", snapshot
	}
	if v.snapshot != nil {
		v.selectionNotice = ""
		if v.selectedRunUID != "" && (len(v.snapshot.Runs) == 0 || v.snapshot.Runs[v.runIndex()].Identity.UID != v.selectedRunUID) {
			v.selectionNotice = "Selected Job UID no longer retained; inspect coverage"
			v.selectedRunUID = ""
		}
		if v.selectedPodUID != "" && (len(v.snapshot.Pods) == 0 || v.snapshot.Pods[v.podIndex()].Identity.UID != v.selectedPodUID) {
			v.selectionNotice = "Selected Pod UID no longer retained; inspect coverage"
			v.selectedPodUID = ""
		}
		if len(v.snapshot.Runs) > 0 && v.selectedRunUID == "" {
			v.selectedRunUID = v.snapshot.Runs[0].Identity.UID
		}
		if len(v.snapshot.Pods) > 0 && v.selectedPodUID == "" {
			v.selectedPodUID = v.snapshot.Pods[0].Identity.UID
		}
	}
	v.render()
}
