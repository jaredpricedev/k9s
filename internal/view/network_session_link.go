// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strings"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

func (v *networkReviewView) bindExistingSessionsKey() {
	action := ui.NewKeyAction("Existing local sessions", v.openExistingSessions, false)
	action.ID, action.Category = "network.sessions", ui.ActionNavigate
	action.Opts.RequiresSelection = true
	action.Availability = v.existingSessionsUnavailable
	v.actions.Add(ui.KeyS, action)
}

func (v *networkReviewView) existingSessionsUnavailable() string {
	if v.cmdBuff.IsActive() {
		return "Finish retained-evidence search before session navigation"
	}
	if !v.destinationCurrent() {
		return "Destination changed; reopen network review before session navigation"
	}
	if !v.active || v.app.Content.Top() != v {
		return "Network review is no longer the current workspace"
	}
	target := v.SelectedResource()
	if err := target.Err(); err != nil {
		return err.Error()
	}
	if target.UID == "" {
		return "Selected evidence has no captured API resource UID"
	}
	if !v.app.hasLocalSessionsForTarget(&target) {
		return "No app-owned local session matches this captured resource identity"
	}
	return ""
}

func (v *networkReviewView) openExistingSessions(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	if reason := v.existingSessionsUnavailable(); reason != "" {
		v.app.Flash().Warn(reason)
		return nil
	}
	target := v.SelectedResource()
	v.app.openLocalSessionsForTarget(&target)
	return nil
}

func (v *networkReviewView) updateExistingSessionsFooter(width int) {
	if v.existingSessionsUnavailable() != "" {
		return
	}
	if width < 60 {
		v.footer.SetText("s sessions · v evidence · ? help")
		return
	}
	v.footer.SetText(strings.Replace(v.footer.GetText(false), " · ? help", " · s sessions · ? help", 1))
}

func (v *networkReviewView) ExtraHints() map[string]string {
	hints := v.Details.ExtraHints()
	if hints == nil {
		hints = make(map[string]string)
	}
	hints["Local sessions"] = "s reviews existing app-owned lifecycle records matched to the selected captured context, resource kind and UID. " +
		"Launch remains explicit; local session state and observed connectivity evidence remain separate."
	return hints
}
