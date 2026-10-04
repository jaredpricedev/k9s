// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

func (v *jobReviewView) inspectSelection(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return v.Details.filterCmd(event)
	}
	if !v.destinationCurrent() {
		v.app.Flash().Warn("Destination changed; reopen Job review before navigating")
		return nil
	}
	target := v.SelectedResource()
	if v.snapshot == nil || (v.activeTab != 1 && v.activeTab != 2) {
		v.selectTab(1)
		return nil
	}
	if err := target.Err(); err != nil || target.GVR == client.CjGVR {
		v.app.Flash().Warn("Choose an observed Job or Pod first")
		return nil
	}
	v.app.openTargetInspection(target, troubleshootCommand)
	return nil
}

func (v *jobReviewView) podLogsReason() string {
	if !v.destinationCurrent() {
		return "Destination changed; reopen Job review"
	}
	if v.activeTab != 2 || v.snapshot == nil || len(v.snapshot.Pods) == 0 {
		return "Open 3 Pods and choose a retained UID-owned Pod; missing Pods do not imply no logs"
	}
	return ""
}

func (v *jobReviewView) openPodLogs(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	if reason := v.podLogsReason(); reason != "" {
		v.app.Flash().Warn(reason)
		return nil
	}
	if err := v.app.dailyWorkspaceOpenLogs(v.SelectedResource()); err != nil {
		v.app.Flash().Err(err)
	}
	return nil
}

func (v *jobReviewView) showSelectedEvidence(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	if v.snapshot == nil {
		return nil
	}
	text := jobReviewEvidence(v.snapshot)
	if v.activeTab == 1 && len(v.snapshot.Runs) > 0 {
		text = jobRunEvidence(&v.snapshot.Runs[v.runIndex()], v.snapshot.CapturedAt)
	} else if v.activeTab == 2 && len(v.snapshot.Pods) > 0 {
		text = jobPodEvidence(&v.snapshot.Pods[v.podIndex()], v.snapshot.CapturedAt)
	}
	const page = "job-review-retained-detail"
	done := func() { v.app.Content.Pages.RemovePage(page); v.app.SetFocus(v) }
	modal := ui.NewMessageModal(v.app.Styles, "Job evidence · retained", text, done)
	v.app.Content.Pages.AddPage(page, modal, true, true)
	v.app.Content.Pages.SetPageCleanup(page, modal.Cleanup)
	v.app.SetFocus(modal)
	return nil
}

// Open the exact source's existing native actions, never submit a hidden retry
// or bypass its confirmations. Native CronJob t/s handlers own the guarded
// writes, operation receipts and accepted Job identity; native Job l opens logs.
func (v *jobReviewView) openSourceActions(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	if !v.destinationCurrent() || v.connection == nil {
		v.app.Flash().Warn("Destination/client changed; reopen Job review before source actions")
		return nil
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.loading = false
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	v.cancel = cancel
	v.app.Flash().Info("Checking captured source identity before native actions...")
	go func() {
		defer cancel()
		observed, err := loadRelatedTarget(ctx, v.connection, v.target)
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if !v.active || generation != v.generation || !v.destinationCurrent() || v.app.Content.Top() != v {
				return
			}
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if err != nil {
				v.app.Flash().Err(err)
				return
			}
			v.applySourceJump(observed)
		})
	}()
	return nil
}

//nolint:gocritic // Navigation owns the immutable source identity obtained by the preceding read.
func (v *jobReviewView) applySourceJump(target SelectedResourceTarget) {
	sourceNamespace := v.app.Config.ActiveNamespace()
	v.app.gotoResource(target.GVR.String()+" "+target.Namespace, target.Path(), false, true)
	if native, ok := v.app.Content.Top().(ResourceViewer); ok && native.GVR() == target.GVR {
		native.GetTable().expectedTarget = &target
		v.returnDestination = &inspectionReturnDestination{sourceNamespace: sourceNamespace,
			destinationNamespace: v.app.Config.ActiveNamespace(), revision: v.app.Config.DestinationRevision()}
		message := "Native Job actions: l logs · Esc returns to retained Job review"
		if target.GVR == client.CjGVR {
			message = "CronJob actions: t trigger · s suspend/resume · :operations progress · Esc returns"
		}
		v.app.Flash().Info(message)
	} else if v.app.Content.Top() == v {
		v.returnDestination = &inspectionReturnDestination{sourceNamespace: sourceNamespace,
			destinationNamespace: v.app.Config.ActiveNamespace(), revision: v.app.Config.DestinationRevision()}
		v.restoreSourceNamespace()
	}
}

func (v *jobReviewView) restoreSourceNamespace() {
	destination := v.returnDestination
	v.returnDestination = nil
	if destination == nil || v.app.Content.Top() != v || v.target.Context != v.app.Config.ActiveContextName() ||
		v.app.Config.ActiveNamespace() != destination.destinationNamespace || v.app.Config.DestinationRevision() != destination.revision {
		return
	}
	if v.app.Config.ActiveNamespace() != destination.sourceNamespace {
		if err := v.app.switchNS(destination.sourceNamespace); err != nil {
			v.app.Flash().Err(fmt.Errorf("Return namespace unavailable: %w", err))
			return
		}
	}
	v.destinationRevision = v.app.Config.DestinationRevision()
}
