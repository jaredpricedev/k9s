// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

func (v *changeSetView) bindChangeSetActions() {
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(changeSetTabs[index], func(e *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return e
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	for _, item := range []struct {
		key   tcell.Key
		label string
		run   func()
	}{
		{tcell.KeyTab, "Next change-set tab", func() { v.selectTab((v.activeTab + 1) % len(changeSetTabs)) }},
		{tcell.KeyBacktab, "Previous change-set tab", func() { v.selectTab((v.activeTab + len(changeSetTabs) - 1) % len(changeSetTabs)) }},
		{ui.KeyP, "Prepare explicit server dry-run", v.confirmPreparation},
		{ui.KeyA, "Apply selected reviewed targets", v.confirmApply},
		{ui.KeyM, "Select eligible targets", v.selectEligible},
		{ui.KeyR, "Refresh retained operation receipt", v.render},
		{ui.KeyX, "Cancel preparation or remaining writes", v.cancelRemaining},
		{ui.KeyG, "Review selected source/controller", func() { target := v.SelectedResource(); v.app.openGitOpsReview(&target, "") }},
		{ui.KeyO, "Read selected native controller outcome", func() { v.app.openRolloutReview(v.SelectedResource()) }},
		{tcell.KeyEnter, "Selected target changes", func() { v.selectTab(1) }},
	} {
		run := item.run
		v.actions.Add(item.key, ui.NewKeyAction(item.label, func(e *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				if e.Key() == tcell.KeyEnter {
					v.Details.filterCmd(e)
					v.render()
					return nil
				}
				return e
			}
			run()
			return nil
		}, true))
	}
}
func (v *changeSetView) tableKey(e *tcell.EventKey) *tcell.EventKey {
	if e.Key() == tcell.KeyRune && e.Rune() == ' ' && !v.cmdBuff.IsActive() {
		if v.plan != nil && v.focusedEntry >= 0 && v.focusedEntry < len(v.plan.Entries) {
			v.selected[v.focusedEntry] = !v.selected[v.focusedEntry]
			v.renderRows()
			v.renderChrome()
		}
		return nil
	}
	if action, ok := v.actions.Get(ui.AsKey(e)); ok {
		return action.Action(e)
	}
	return e
}
func (v *changeSetView) selectTab(tab int) {
	if tab < 0 || tab >= len(changeSetTabs) || tab == v.activeTab {
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
	if tab == 0 {
		v.app.SetFocus(v.table)
	} else {
		v.app.SetFocus(v.text)
	}
}
func (v *changeSetView) selectEligible() {
	if v.plan == nil {
		return
	}
	for index := range v.plan.Entries {
		_, eligible := v.plan.Target(index)
		v.selected[index] = eligible
	}
	v.render()
}
func (v *changeSetView) cancelRemaining() {
	if v.task != nil {
		v.task.cancelRemaining()
		v.notice = "Cancellation requested; accepted writes stay recorded in :operations"
	}
	if v.loading {
		v.stopPreparation()
		v.plan = nil
	}
	v.render()
}
func (v *changeSetView) confirmPreparation() {
	if err := v.eligible(); err != nil {
		v.app.Flash().Warn(err.Error())
		return
	}
	if v.loading || v.task != nil && v.task.receipt().Ended.IsZero() {
		v.app.Flash().Warn("Wait or cancel the current preparation/operation before preparing again")
		return
	}
	v.confirmation("Prepare reviewed change set", "Invokes strict dryRun=All admission for each eligible namespaced target. No resource is persisted. "+
		"Ownership is read through bounded named references. Secrets and cluster-scoped resources are excluded. "+
		"A separate Apply confirmation is required.", false, "Preview", v.prepare)
}
func (v *changeSetView) prepare() {
	if err := v.eligible(); err != nil || v.preparer == nil {
		v.app.Flash().Warn("Preparation unavailable or destination changed")
		return
	}
	v.stopPreparation()
	v.plan, v.task, v.selected = nil, nil, make(map[int]bool)
	ctx, cancel := context.WithTimeout(context.Background(), review.CollectionTimeout)
	v.cancel, v.loading = cancel, true
	generation, prepare := v.generation, v.preparer
	v.notice = "Explicit strict dry-run in progress; nothing persisted"
	v.render()
	go func() {
		defer cancel()
		plan, err := prepare(ctx)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		dispatch := v.enqueue
		if dispatch == nil {
			dispatch = v.app.QueueUpdateDraw
		}
		dispatch(func() {
			if !v.active || !v.destinationCurrent() || v.app.Content.Top() != v || v.generation != generation {
				return
			}
			v.loading, v.cancel = false, nil
			if err != nil || plan == nil {
				v.notice = "Preparation failed; no executable preview retained. Reload source or retry explicitly"
			} else {
				v.plan = plan
				v.notice = "Preview retained; Space selects · m selects eligible · a explicitly applies"
			}
			v.render()
		})
	}()
}
func (v *changeSetView) confirmApply() {
	if err := v.eligible(); err != nil {
		v.app.Flash().Warn(err.Error())
		return
	}
	if v.plan == nil || v.loading {
		v.app.Flash().Warn("Prepare an explicit server preview before applying")
		return
	}
	count, eligible := 0, 0
	for index := range v.plan.Entries {
		if v.selected[index] {
			count++
			if _, ok := v.plan.Target(index); ok {
				eligible++
			}
		}
	}
	if eligible == 0 {
		v.app.Flash().Warn("Select a current eligible target; blocked, expired or submitted rows cannot execute")
		return
	}
	plan := v.plan
	selection := v.selectionKey()
	message := fmt.Sprintf("Apply %d selected target(s), %d currently eligible?\nSource SHA256 %s\n"+
		"Each target is independent; this batch is not atomic. Earlier writes may succeed before a later denial, conflict or cancellation.\n"+
		"Complete authored objects include sensitive or omitted fields excluded from review. Secret objects are never submitted. "+
		"Created targets may need separate field-ownership review before a later apply. "+
		"No pruning, Force or automatic retry. Rechecks local source, UID/RV/generation, ownership and permission before every write.",
		count, eligible, v.source.Identity.SHA256)
	v.confirmation("Apply reviewed change set", message, true, "Apply", func() {
		if v.plan == plan && v.selectionKey() == selection {
			v.submitPlan(plan)
		} else {
			v.app.Flash().Warn("Plan or selected targets changed; confirm again")
		}
	})
}
func (v *changeSetView) selectionKey() string {
	key := ""
	if v.plan != nil {
		for index := range v.plan.Entries {
			if v.selected[index] {
				key += strconv.Itoa(index) + ","
			}
		}
	}
	return key
}
func (v *changeSetView) confirmation(title, message string, acknowledge bool, label string, accept func()) {
	v.dismissForm()
	styles := v.app.Styles.Dialog()
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).SetLabelColor(styles.LabelFgColor.Color()).
		SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color()).SetBackgroundColor(styles.BgColor.Color())
	var modal *ui.ModalForm
	dismiss := func() {
		if v.modal == modal {
			v.dismissForm()
		}
	}
	acknowledged := !acknowledge
	if acknowledge {
		form.AddCheckbox("Unreviewed fields / partial batch", false, func(_ string, checked bool) { acknowledged = checked })
	}
	form.AddButton("Cancel", dismiss)
	form.AddButton(label, func() {
		if v.modal != modal {
			return
		}
		if !acknowledged {
			v.app.Flash().Warn("Acknowledge unreviewed fields and partial batch behavior before execution")
			return
		}
		dismiss()
		accept()
	})
	form.SetCancelFunc(dismiss).SetFocus(form.GetFormItemCount())
	modal = ui.NewModalForm(title, form).SetText(tview.Escape(logstream.SafeText(message)))
	modal.SetContext(changeSetSingleLine("ctx " + v.contextName + " · namespaces " + strings.Join(v.scope.Namespaces, ",")))
	modal.SetDialogColors(&styles)
	v.modal, v.form = modal, form
	v.app.Content.Pages.AddPage(changeSetModalPage, modal, false, true)
	v.app.SetFocus(modal)
}
func (v *changeSetView) dismissForm() {
	v.app.Content.Pages.RemovePage(changeSetModalPage)
	v.modal, v.form = nil, nil
	if v.active && v.app.Content.Top() == v {
		if v.activeTab == 0 {
			v.app.SetFocus(v.table)
		} else {
			v.app.SetFocus(v.text)
		}
	}
}
func (v *changeSetView) submitPlan(plan *review.ChangeSetPlan) {
	if err := v.eligible(); err != nil || plan != v.plan {
		v.app.Flash().Warn("Captured destination or reviewed plan changed; reopen review")
		return
	}
	targets := []SelectedResourceTarget{}
	indices := map[string]int{}
	for index := range plan.Entries {
		if !v.selected[index] {
			continue
		}
		identity, eligible := plan.Target(index)
		if !eligible {
			identity = plan.Entries[index].Identity
		}
		target := changeSetTarget(&identity)
		if !eligible {
			target.UnavailableReason = "Blocked, expired or already submitted review target; no persistent write requested"
		}
		targets = append(targets, target)
		indices[review.IdentityKey(identity.GVR, identity.Namespace, identity.Name)] = index
	}
	session, owner := v.session, v.owner
	taskID := make(chan uint64, 1)
	task := session.submit("Apply reviewed change set (non-atomic)", targets, func(ctx context.Context, target SelectedResourceTarget) error {
		var id uint64
		select {
		case id = <-taskID:
			taskID <- id
		case <-ctx.Done():
			return ctx.Err()
		}
		index := indices[review.IdentityKey(target.GVR.GVR(), target.Namespace, target.Name)]
		verb := client.PatchVerb
		if plan.Entries[index].ExpectedAbsent {
			verb = client.CreateVerb
		}
		authorizationCtx, cancelAuthorization := context.WithTimeout(ctx, review.ReadTimeout)
		authorizationErr := session.authorize(authorizationCtx, &target, "", client.GetVerb, verb)
		cancelAuthorization()
		if authorizationErr != nil {
			return errors.New("Captured named read/write authorization denied or unavailable; target not submitted")
		}
		accepted, err := review.ApplyChangeSetTarget(ctx, session.dynamic, owner, plan, index,
			review.ChangeSetHooks{BeforeWrite: operationBeginWrite, Accepted: operationAcceptWrite})
		if accepted != nil {
			fmt.Fprintf(operationOutput(ctx), "Task #%d · API accepted %s · UID %s · RV %s · generation %d\n", id,
				accepted.AcceptedAt.UTC().Format(time.RFC3339Nano), accepted.Identity.UID, accepted.ResourceVersion, accepted.Generation)
			if accepted.Observed && err == nil {
				fmt.Fprintf(operationOutput(ctx), "OBSERVED %s · UID %s · RV %s · generation %d · source %s\n",
					accepted.ObservedAt.UTC().Format(time.RFC3339Nano), accepted.Identity.UID, accepted.ObservedResourceVersion, accepted.ObservedGeneration, accepted.ObservationSource)
				operationObserveWrite(ctx)
			}
		}
		if errors.Is(err, review.ErrChangeSetOutcomeUnknown) {
			return errors.Join(errExternalOperationOutcome, err)
		}
		return err
	}, nil)
	if task != nil {
		taskID <- task.receipt().ID
		v.task = task
		v.notice = "Submitted once; r refreshes receipts · x cancels remaining · :operations retains outcomes"
		v.selectTab(3)
		v.render()
	}
}
