// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

// Actions uses the same shortcuts as the Hubble key router. These are view-local
// actions; peer/flow availability never depends on an API resource selection.
func (w *HubbleView) Actions() *ui.KeyActions {
	actions := ui.NewKeyActions()
	for _, item := range []struct {
		key                 tcell.Key
		id, label, category string
		visible             bool
	}{
		{tcell.KeyEnter, "hubble.inspect", "Inspect", ui.ActionInspect, true},
		{ui.KeyS, "hubble.freeze", "Freeze/Resume", ui.ActionFilter, true},
		{ui.KeySlash, "hubble.filter", "Filter", ui.ActionFilter, true},
		{ui.Key1, "hubble.source-pod", "Source pod", ui.ActionNavigate, false},
		{ui.Key2, "hubble.destination-pod", "Destination pod", ui.ActionNavigate, false},
		{ui.KeyR, "hubble.reconnect", "Reconnect", ui.ActionNavigate, true},
		{ui.KeyHelp, "hubble.help", "Help", ui.ActionInspect, true},
		{tcell.Key('S'), "hubble.status", "Full status", ui.ActionInspect, true},
		{tcell.KeyEscape, "hubble.back", "Back", ui.ActionNavigate, false},
	} {
		key := item.key
		action := ui.NewKeyAction(item.label, func(*tcell.EventKey) *tcell.EventKey { return w.key(actionEvent(key)) }, item.visible)
		action.ID, action.Category = item.id, item.category
		if key == tcell.KeyEnter || key == ui.Key1 || key == ui.Key2 {
			action.Availability = func() string {
				row, _ := w.table.GetSelection()
				if key == tcell.KeyEnter && w.mode == hubblePeersMode && row > 0 && row <= len(w.peers) {
					return ""
				}
				if w.mode == hubbleConversationMode && row > 0 && row <= len(w.rows) {
					return ""
				}
				if key != tcell.KeyEnter && w.mode == modeDetail {
					return ""
				}
				return "Select a retained peer or flow first"
			}
		}
		if key == ui.KeyS && w.statusOnly {
			action.Availability = func() string { return "Relay status has no flow snapshot to freeze" }
		}
		actions.Add(key, action)
	}
	actions.Add(tcell.KeyCtrlO, ui.NewKeyAction("Actions", func(e *tcell.EventKey) *tcell.EventKey {
		if w.app != nil {
			return w.app.actionsCmd(e)
		}
		return nil
	}, true))
	return actions
}
