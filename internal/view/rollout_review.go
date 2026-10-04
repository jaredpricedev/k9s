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
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/sahilm/fuzzy"
	"k8s.io/apimachinery/pkg/types"
)

const (
	rolloutReviewTitle = "Rollout review"
	rolloutEvidenceTab = 4
	rolloutRecoveryTab = 3
)

var rolloutReviewTabs = []string{"Overview", "Revisions", "Pods", "Recovery", "Evidence"}

// rolloutReviewView retains one explicitly obtained native controller observation.
// Its recovery view compares retained templates and never submits a write.
type rolloutReviewView struct {
	*Details
	target               SelectedResourceTarget
	snapshot             *review.RolloutSnapshot
	loader               func(context.Context, SelectedResourceTarget) (*review.RolloutSnapshot, error)
	cancel               context.CancelFunc
	generation           uint64
	destinationRevision  uint64
	active, loading      bool
	activeTab            int
	tabStates            [5]investigationTabState
	selectedRevisionUID  string
	recoveryRevisionUID  string
	identityBar, tabsBar *tview.TextView
	footer               *tview.TextView
	width, height        int
	selectionInvalidated bool
	ensureSelection      bool
	refreshFailure       string
	retainedText         string
	outcome              *review.RolloutOutcome
	follower             func(context.Context, *review.RolloutOutcomeRequest, func(*review.RolloutOutcome)) *review.RolloutOutcome
	followCancel         context.CancelFunc
	followGeneration     uint64
	following            bool
}

func (c *Command) rolloutReviewCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a native Deployment, StatefulSet or DaemonSet to review its rollout")
		return
	}
	c.app.openRolloutReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // A read-only review owns the captured selection independently of later navigation.
func (a *App) openRolloutReview(target SelectedResourceTarget) {
	if err := rolloutTargetError(target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen rollout review")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	v := &rolloutReviewView{Details: NewDetails(a, rolloutReviewTitle, target.Path(), contentInspection, true), target: target,
		destinationRevision: a.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, target SelectedResourceTarget) (*review.RolloutSnapshot, error) {
		return loadRolloutReview(ctx, connection, target)
	}
	v.follower = func(ctx context.Context, request *review.RolloutOutcomeRequest, update func(*review.RolloutOutcome)) *review.RolloutOutcome {
		dyn, _ := connection.DynDial()
		return review.FollowRollout(ctx, dyn, request, update)
	}
	v.Update("Loading read-only controller rollout evidence...")
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

//nolint:gocritic // Validate the immutable selection without modifying it.
func rolloutTargetError(target SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if rolloutTargetKind(target) == "" {
		return fmt.Errorf("Select a native apps/v1 Deployment, StatefulSet or DaemonSet")
	}
	if !client.IsNamespaced(target.Namespace) {
		return fmt.Errorf("Select a namespaced rollout controller")
	}
	if target.UID == "" {
		return fmt.Errorf("Workload UID unavailable; refresh the source list and select the resource again")
	}
	return nil
}

func (v *rolloutReviewView) SelectedResource() SelectedResourceTarget { return v.target }
func (*rolloutReviewView) CompactWorkspace() bool                     { return true }

func (v *rolloutReviewView) Init(ctx context.Context) error {
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
	v.Flex.Clear().SetDirection(tview.FlexRow).
		AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).
		AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(rolloutReviewTabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next rollout tab", v.nextTab, true))
	v.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous rollout tab", v.previousTab, true))
	v.actions.Add(ui.KeyW, ui.NewKeyAction("Follow/stop captured rollout outcome", v.toggleOutcome, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh rollout evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.actions.Add(ui.KeyJ, ui.NewKeyAction("Next retained revision", func(event *tcell.EventKey) *tcell.EventKey {
		return v.moveRevision(event, 1)
	}, true))
	v.actions.Add(ui.KeyK, ui.NewKeyAction("Previous retained revision", func(event *tcell.EventKey) *tcell.EventKey {
		return v.moveRevision(event, -1)
	}, true))
	for _, item := range []struct {
		key   tcell.Key
		step  int
		label string
	}{
		{tcell.KeyDown, 1, "Next retained revision"}, {tcell.KeyUp, -1, "Previous retained revision"},
		{tcell.KeyPgDn, 0, "Next revision page"}, {tcell.KeyPgUp, 0, "Previous revision page"},
	} {
		key, step := item.key, item.step
		v.actions.Add(key, ui.NewKeyAction(item.label, func(event *tcell.EventKey) *tcell.EventKey {
			delta := step
			if delta == 0 {
				_, _, _, height := v.text.GetInnerRect()
				delta = max(1, height-4)
				if key == tcell.KeyPgUp {
					delta = -delta
				}
			}
			return v.moveRevision(event, delta)
		}, true))
	}
	v.actions.Add(tcell.KeyEnter, ui.NewSharedKeyAction("Review selected revision template", v.reviewSelectedRevision, true))
	v.render()
	return nil
}

func (v *rolloutReviewView) Start() {
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

func (v *rolloutReviewView) Stop() {
	v.stopOutcome()
	v.active = false
	v.loading = false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *rolloutReviewView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	v.render()
}

func (v *rolloutReviewView) Draw(screen tcell.Screen) {
	if v.following && !v.destinationCurrent() {
		v.stopOutcome()
		v.render()
	}
	_, _, width, height := v.GetInnerRect()
	if ui.DrawTaskSizeNotice(screen, v.Box) {
		return
	}
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.ensureSelection = true
		v.render()
	}
	v.renderChrome()
	v.keepRevisionVisible()
	v.Flex.Draw(screen)
}

func (v *rolloutReviewView) TextChanged(lines []string) {
	v.text.SetText(rolloutMarkup(v.app, strings.Join(lines, "\n")))
	v.text.ScrollToBeginning()
}

func (v *rolloutReviewView) TextFiltered(lines []string, matches fuzzy.Matches) {
	v.Details.TextFiltered(lines, matches)
	v.text.SetText(enableRegion(rolloutMarkup(v.app, strings.Join(linesWithRegions(lines, matches), "\n"))))
}

func (v *rolloutReviewView) nextTab(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	v.selectTab((v.activeTab + 1) % len(rolloutReviewTabs))
	return nil
}

func (v *rolloutReviewView) previousTab(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	v.selectTab((v.activeTab + len(rolloutReviewTabs) - 1) % len(rolloutReviewTabs))
	return nil
}

func (v *rolloutReviewView) selectTab(tab int) {
	if tab < 0 || tab >= len(rolloutReviewTabs) || tab == v.activeTab {
		return
	}
	row, col := v.text.GetScrollOffset()
	v.tabStates[v.activeTab] = investigationTabState{query: v.inspectionQuery, region: v.currentRegion, row: row, col: col}
	v.activeTab = tab
	v.ensureSelection = true
	state := v.tabStates[tab]
	v.inspectionQuery = state.query
	v.cmdBuff.SetText(state.query, "", true)
	v.currentRegion = state.region
	v.text.ScrollTo(state.row, state.col)
	v.render()
}

func (v *rolloutReviewView) revisionIndex() int {
	if v.snapshot != nil {
		for index := range v.snapshot.Revisions {
			if v.snapshot.Revisions[index].Identity.UID == v.selectedRevisionUID {
				return index
			}
		}
	}
	return -1
}

func (v *rolloutReviewView) moveRevision(event *tcell.EventKey, step int) *tcell.EventKey {
	if v.cmdBuff.IsActive() || (v.activeTab != 1 && v.activeTab != rolloutRecoveryTab) || v.snapshot == nil || len(v.snapshot.Revisions) == 0 {
		return event
	}
	index := v.revisionIndex()
	if index < 0 {
		// The first navigation after invalidation is an explicit new choice.
		index = 0
		if step < 0 {
			index = len(v.snapshot.Revisions) - 1
		}
	} else {
		index = (index + step%len(v.snapshot.Revisions) + len(v.snapshot.Revisions)) % len(v.snapshot.Revisions)
	}
	v.selectedRevisionUID = v.snapshot.Revisions[index].Identity.UID
	v.selectionInvalidated = false
	v.ensureSelection = true
	v.render()
	return nil
}

func (v *rolloutReviewView) reviewSelectedRevision(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return v.Details.filterCmd(event)
	}
	if (v.activeTab != 1 && v.activeTab != rolloutRecoveryTab) || v.snapshot == nil || len(v.snapshot.Revisions) == 0 {
		return event
	}
	index := v.revisionIndex()
	if index < 0 {
		v.selectionInvalidated = true
		v.renderChrome()
		return nil
	}
	v.recoveryRevisionUID = v.snapshot.Revisions[index].Identity.UID
	v.selectTab(rolloutRecoveryTab)
	v.render()
	return nil
}

func (v *rolloutReviewView) refresh() {
	v.stopOutcome()
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen rollout review"
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
	destinationRevision := v.app.Config.DestinationRevision()
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
			if v.active && v.generation == generation && v.target.Context == v.app.Config.ActiveContextName() &&
				v.app.Config.DestinationRevision() == destinationRevision && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, err)
			}
		})
	}()
}

func (v *rolloutReviewView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}

func (v *rolloutReviewView) acceptSnapshot(snapshot *review.RolloutSnapshot, err error) {
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
	if v.snapshot != nil && v.snapshot.Identity.UID != "" {
		v.target.UID = types.UID(v.snapshot.Identity.UID)
		if len(v.snapshot.Revisions) > 0 && v.selectedRevisionUID == "" && !v.selectionInvalidated {
			v.selectedRevisionUID = v.snapshot.Revisions[0].Identity.UID
		}
		v.selectionInvalidated = v.selectedRevisionUID != "" && v.revisionIndex() < 0
		v.ensureSelection = true
	}
	v.render()
}

func (v *rolloutReviewView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	width := max(1, v.width)
	v.text.SetWrap(v.activeTab != 1)
	if v.width == 0 {
		width = 76
	}
	text := "Loading read-only controller rollout evidence..."
	if v.snapshot != nil {
		v.retainedText = rolloutEvidence(v.snapshot)
		text = rolloutTabText(v.snapshot, v.activeTab, width, v.selectedRevisionUID, v.recoveryRevisionUID)
		if v.outcome != nil {
			if v.activeTab == 0 {
				text = rolloutOutcomeText(v.outcome, width) + "\n" + strings.Replace(text, "ROLLOUT OBSERVATION", "ORIGINAL RETAINED OBSERVATION", 1)
			}
			if v.activeTab == rolloutEvidenceTab {
				text = rolloutOutcomeEvidence(v.outcome) + "\n" + text
			}
		}
	} else if v.refreshFailure != "" {
		text = "[?] Rollout evidence unavailable: " + v.refreshFailure
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

func (v *rolloutReviewView) renderChrome() {
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
	first := rolloutTargetKind(v.target) + " " + v.target.Path() + " · " + v.target.Context
	second := "Kubernetes API · observation not yet obtained"
	third := "READ ONLY · native controller review"
	if v.snapshot != nil {
		s := v.snapshot
		uid := s.Identity.UID
		if len(uid) > 20 {
			uid = uid[:8] + "…" + uid[len(uid)-8:]
		}
		second = fmt.Sprintf("UID %s · captured %s · age %s", uid, s.CapturedAt.UTC().Format("15:04:05Z"), investigationAge(s.CapturedAt, time.Now()))
		third = rolloutCoverageLine(s.Coverage, width)
	}
	if v.loading {
		third = "[~] Refreshing; retained evidence stays visible"
	} else if v.refreshFailure != "" {
		third = "[~] Refresh failed; source/time retained · " + v.refreshFailure
	}
	if v.activeTab == 1 || v.activeTab == rolloutRecoveryTab {
		index := v.revisionIndex()
		if index >= 0 {
			r := &v.snapshot.Revisions[index]
			second = "Selected " + r.Identity.Name + " · UID " + r.Identity.UID
		} else if v.selectedRevisionUID != "" {
			second = "Prior selection UID " + v.selectedRevisionUID
		}
		if v.selectionInvalidated {
			third = "[!] Selection removed; j/k choose again before Enter"
		}
	}
	if !v.destinationCurrent() {
		third = "[~] Destination changed; original source retained; reopen to refresh"
	}
	v.identityBar.SetText(detailStyled(p.Focus.String(), "b", fitInvestigation(first, width)) + "\n" +
		detailStyled(p.Muted.String(), "", fitInvestigation(second, width)) + "\n" +
		detailStyled(p.Warning.String(), "", fitInvestigation(third, width)))
	v.tabsBar.SetText(ui.TaskTabs(rolloutReviewTabs, v.activeTab, width))
	footer := "READ ONLY · w follow · r refresh · / search · Esc back"
	if width < 60 {
		footer = "w follow · r refresh · Esc back"
	}
	if v.following {
		footer = "w stop following · Esc back"
	}
	if v.activeTab == 1 || v.activeTab == rolloutRecoveryTab {
		footer = "READ ONLY · j/k choose · Enter review · Esc back"
		if width < 48 {
			footer = "j/k choose · Enter review · Esc back"
		}
	}
	v.footer.SetText(detailStyled(p.Muted.String(), "", fitInvestigation(footer, width)))
}

// Selection owns the revision list viewport; free-text search keeps its own
// highlights and tab state. Only navigation, resize and refreshed evidence
// request a viewport adjustment, so scrolling other tabs remains independent.
func (v *rolloutReviewView) keepRevisionVisible() {
	if !v.ensureSelection || v.activeTab != 1 {
		return
	}
	v.ensureSelection = false
	// Search highlights may request a different scroll on the next native draw.
	// Choosing a row takes priority; n/N can resume independent match navigation.
	v.text.Highlight()
	line := -1
	for index, text := range v.model.Peek() {
		if strings.HasPrefix(text, ">") {
			line = index
			break
		}
	}
	if line < 0 {
		return
	}
	height := max(1, v.height-5)
	row, _ := v.text.GetScrollOffset()
	if line < row {
		row = line
	}
	if line >= row+height {
		row = line - height + 1
	}
	v.text.ScrollTo(max(0, row), 0)
}

func (*rolloutReviewView) ExtraHints() map[string]string {
	return map[string]string{
		"Following": "w explicitly starts/stops bounded named GETs for the captured generation/template. Esc and refresh stop observing; accepted changes are not rolled back.",
		"Revisions": "j/k or arrows choose UID; Page keys move selection. Enter reviews the exact visible choice. Refresh removal requires a new choice.",
		"Search":    "Case-insensitive regex; -f text for fuzzy search. n/N move matches without choosing a revision; j/k returns to the chosen row.",
	}
}

//nolint:gocritic // Captured identities are validated without mutation.
func rolloutTargetKind(target SelectedResourceTarget) string {
	if target.GVR == nil {
		return ""
	}
	switch target.GVR.String() {
	case client.DpGVR.String():
		return "Deployment"
	case client.StsGVR.String():
		return "StatefulSet"
	case client.DsGVR.String():
		return "DaemonSet"
	default:
		return ""
	}
}
