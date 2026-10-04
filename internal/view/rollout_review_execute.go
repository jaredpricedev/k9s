// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const rolloutRecoveryFormPage = "rollout-recovery-confirm"
const rolloutRecoveryUnavailable = "Unavailable"

func (v *rolloutReviewView) prepareRecoveryCmd(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() || v.activeTab != rolloutRecoveryTab {
		return event
	}
	if err := v.recoveryEligibility(); err != nil {
		v.app.Flash().Warn(err.Error())
		return nil
	}
	v.stopRecoveryPreview()
	generation, uid := v.recoveryGeneration, v.recoveryRevisionUID
	message := "Prepare a server dry-run of selected revision " + uid + " in context " + v.target.Context +
		"? Requires named read and patch permission. Nothing persists until a separate Apply confirmation."
	v.recoveryConfirmation("Recovery server preview", message, false, "Preview", func() {
		if generation != v.recoveryGeneration || uid != v.recoveryRevisionUID || uid != v.selectedRevisionUID {
			return
		}
		v.prepareRecovery()
	})
	return nil
}

func (v *rolloutReviewView) recoveryEligibility() error {
	if !v.destinationCurrent() || v.app.Content.Top() != v || !v.active {
		return fmt.Errorf("Destination or originating review changed; reopen rollout review")
	}
	if v.app.Config.IsReadOnly() {
		return fmt.Errorf("Recovery execution unavailable in read-only mode; retained comparisons remain available")
	}
	if rolloutTargetKind(v.target) != rolloutDeployKind {
		return fmt.Errorf("Guarded recovery supports native Deployments; StatefulSet/DaemonSet revision comparison remains read-only")
	}
	if v.snapshot == nil || v.selectionInvalidated || v.selectedRevisionUID == "" || v.selectedRevisionUID != v.recoveryRevisionUID || v.revisionIndex() < 0 {
		return fmt.Errorf("Enter explicitly previews the pending exact revision before server preparation")
	}
	if v.recoverySession == nil {
		return fmt.Errorf("Captured recovery operation clients unavailable")
	}
	return nil
}

func (v *rolloutReviewView) prepareRecovery() {
	if err := v.recoveryEligibility(); err != nil {
		v.app.Flash().Warn(err.Error())
		return
	}
	v.stopRecoveryPreview()
	v.recoveryPlan = nil
	session, err := v.recoverySession()
	if err != nil || session == nil || session.dynamic == nil || session.typed == nil {
		v.recoveryNotice = "Captured recovery operation clients unavailable; previous server plan cleared"
		v.render()
		v.app.Flash().Warn("Captured recovery operation clients unavailable")
		return
	}
	generation, snapshot, uid, target := v.recoveryGeneration, v.snapshot, v.recoveryRevisionUID, v.target
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	v.recoveryCancel = cancel
	v.recoveryNotice = "Preparing explicit server dry-run; nothing persisted"
	v.render()
	go func() {
		defer cancel()
		var plan *review.RolloutRecoveryPlan
		err := session.authorize(ctx, &target, "", client.GetVerb, client.PatchVerb)
		if err == nil {
			plan, err = review.PrepareRolloutRecovery(ctx, session.dynamic, snapshot, uid)
		} else {
			err = fmt.Errorf("Recovery read/patch authorization unavailable or denied")
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if generation != v.recoveryGeneration || !session.current() || v.recoveryRevisionUID != uid || v.selectedRevisionUID != uid {
				return
			}
			v.recoveryCancel = nil
			if err != nil {
				v.recoveryNotice = err.Error()
			} else {
				v.recoveryPlan, v.recoveryNotice = plan, "Server preview retained; separate Apply confirmation required"
			}
			v.render()
		})
	}()
}

func (v *rolloutReviewView) applyRecoveryCmd(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() || v.activeTab != rolloutRecoveryTab {
		return event
	}
	if err := v.recoveryEligibility(); err != nil {
		v.app.Flash().Warn(err.Error())
		return nil
	}
	plan := v.recoveryPlan
	if plan == nil || plan.RevisionIdentity.UID != v.recoveryRevisionUID {
		v.app.Flash().Warn("Prepare the exact selected server preview with x first")
		return nil
	}
	message := "PERSISTENT WRITE"
	if len(plan.OwnershipMarkers) > 0 {
		message += "\nOwnership metadata is unverified.\nAnother controller may reconcile this template back. See Evidence for markers."
	}
	message += "\nTarget UID " + plan.Identity.UID +
		"\nSelected " + plan.RevisionIdentity.Name + " · UID " + plan.RevisionIdentity.UID +
		"\nSource template SHA256 " + plan.SourceTemplateSHA256[:min(12, len(plan.SourceTemplateSHA256))] +
		"\nReplaces the complete Pod template with this exact selected source. Sensitive fields may be excluded from review." +
		"\nConfigMap/Secret contents and Deployment annotations are not restored. Acceptance is separate from controller completion."
	v.recoveryConfirmation("Apply selected template", message, plan.Unreviewed, "Apply", func() { v.submitRecovery(plan) })
	return nil
}

func (v *rolloutReviewView) recoveryConfirmation(title, message string, unreviewed bool, label string, accept func()) {
	v.dismissRecoveryForm()
	styles := v.app.Styles.Dialog()
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).SetLabelColor(styles.LabelFgColor.Color()).
		SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color()).SetBackgroundColor(styles.BgColor.Color())
	var modal *ui.ModalForm
	dismiss := func() {
		if v.recoveryModal == modal {
			v.dismissRecoveryForm()
		}
	}
	acknowledged := !unreviewed
	if unreviewed {
		form.AddCheckbox("Unreviewed fields", false, func(_ string, checked bool) { acknowledged = checked })
	}
	form.AddButton("Cancel", dismiss)
	form.AddButton(label, func() {
		if v.recoveryModal != modal {
			return
		}
		if err := v.recoveryEligibility(); err != nil {
			dismiss()
			v.app.Flash().Warn(err.Error())
			return
		}
		if !acknowledged {
			v.app.Flash().Warn("Acknowledge excluded sensitive/command fields before applying the complete template")
			return
		}
		dismiss()
		accept()
	})
	form.SetCancelFunc(dismiss)
	form.SetFocus(form.GetFormItemCount())
	modal = ui.NewModalForm(title, form).SetText(tview.Escape(message))
	modal.SetContext("ctx " + v.target.Context + " · " + v.target.Namespace + "/" + v.target.Name)
	modal.SetDialogColors(&styles)
	v.recoveryModal, v.recoveryForm = modal, form
	v.app.Content.Pages.AddPage(rolloutRecoveryFormPage, modal, false, true)
	v.app.SetFocus(modal)
}

func (v *rolloutReviewView) dismissRecoveryForm() {
	v.app.Content.Pages.RemovePage(rolloutRecoveryFormPage)
	v.recoveryModal, v.recoveryForm = nil, nil
	if v.active && v.app.Content.Top() == v {
		v.app.SetFocus(v)
	}
}

func (v *rolloutReviewView) stopRecoveryPreview() {
	v.recoveryGeneration++
	if v.recoveryCancel != nil {
		v.recoveryCancel()
		v.recoveryCancel = nil
	}
}

func (v *rolloutReviewView) submitRecovery(plan *review.RolloutRecoveryPlan) {
	if err := v.recoveryEligibility(); err != nil {
		v.app.Flash().Warn(err.Error())
		return
	}
	if v.recoveryPlan != plan || plan.RevisionIdentity.UID != v.selectedRevisionUID {
		v.app.Flash().Warn("Recovery plan or selection changed; prepare again")
		return
	}
	session, err := v.recoverySession()
	if err != nil || session == nil || session.dynamic == nil || session.typed == nil {
		v.app.Flash().Warn("Captured operation clients unavailable")
		return
	}

	target := v.target
	v.stopOutcome()
	generation, destinationRevision := v.followGeneration, v.destinationRevision
	session.timeout = 3 * time.Minute // Accepted controller observation owns a separate bounded stage.
	taskID := make(chan uint64, 1)
	task := session.submit("Apply selected Deployment template", []SelectedResourceTarget{target}, func(ctx context.Context, _ SelectedResourceTarget) error {
		var id uint64
		select {
		case id = <-taskID:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := session.authorize(ctx, &target, "", client.GetVerb, client.PatchVerb); err != nil {
			return fmt.Errorf("Recovery read/patch authorization unavailable or denied")
		}
		accepted, err := review.ApplyRolloutRecovery(ctx, session.dynamic, plan, review.RolloutRecoveryHooks{BeforeWrite: operationBeginWrite, Accepted: operationAcceptWrite})
		output := operationOutput(ctx)
		if accepted != nil {
			fmt.Fprintf(output, "%s · UID %s · generation %d · template SHA256 %s · accepted %s\n",
				accepted.State, accepted.Identity.UID, accepted.Generation, accepted.TemplateSHA256, accepted.AcceptedAt.UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			return err
		}
		request := &review.RolloutOutcomeRequest{Identity: accepted.Identity, Generation: accepted.Generation,
			TemplateSHA256: accepted.TemplateSHA256, AcceptedAt: accepted.AcceptedAt, OperationID: strconv.FormatUint(id, 10)}
		publish := func(outcome *review.RolloutOutcome) {
			session.dispatch(func() {
				if generation != v.followGeneration || destinationRevision != v.destinationRevision {
					return
				}
				v.following = outcome.FinishedAt.IsZero()
				v.followCancel = func() {
					for _, task := range v.app.operations.list() {
						if task.receipt().ID == id {
							task.cancelRemaining()
							break
						}
					}
				}
				v.selectTab(0)
				v.acceptOutcome(generation, destinationRevision, outcome)
			})
		}
		outcome := review.FollowRollout(ctx, session.dynamic, request, publish)
		fmt.Fprintf(output, "CONTROLLER OUTCOME: %s · %s · %d named reads\n", outcome.State, outcome.Reason, outcome.Reads)
		publish(outcome)
		// Acceptance remains recorded if observing is canceled after the write.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}, nil)
	if task != nil {
		taskID <- task.receipt().ID
		v.recoveryPlan = nil
		v.recoveryNotice = "Recovery submitted once; :operations retains acceptance and controller outcome receipts"
		v.render()
	}
}

func rolloutPreparedRecovery(plan *review.RolloutRecoveryPlan, width int) string {
	var b strings.Builder
	b.WriteString("SERVER RECOVERY PREVIEW · NOT EXECUTED\n")
	fmt.Fprintln(&b, fitInvestigation(plan.RevisionIdentity.Name+" · UID "+plan.RevisionIdentity.UID, width))
	fmt.Fprintln(&b, "Complete Pod-template replacement; Deployment metadata preserved")
	for _, change := range plan.Comparison.Changes[:min(8, len(plan.Comparison.Changes))] {
		fmt.Fprintln(&b, fitInvestigation(change.Kind+" "+change.Path, width))
		before, after := comparisonValues(change.Before, change.After)
		fmt.Fprintln(&b, fitInvestigation("  A "+before, width))
		fmt.Fprintln(&b, fitInvestigation("  B "+after, width))
	}
	if plan.Unreviewed {
		b.WriteString("[?] Sensitive/command fields excluded; Apply requires acknowledgment\n")
	}
	if len(plan.OwnershipMarkers) > 0 {
		b.WriteString("[?] Ownership metadata unverified; reconciliation may undo recovery\n")
	}
	b.WriteString("a explicit Apply confirmation · 5 full evidence · x prepare again\n")
	return b.String()
}

func rolloutPreparedEvidence(plan *review.RolloutRecoveryPlan) string {
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "[?] Prepared recovery evidence unavailable"
	}
	text := "SERVER RECOVERY PREVIEW EVIDENCE · no persistent write\n" + string(encoded)
	if len(text) > inspect.MaxComparisonText {
		text = text[:inspect.MaxComparisonText] + "\n[~] Evidence capped; full projection unavailable here.\n"
	}
	return text
}
