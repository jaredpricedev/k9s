// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

func maintenanceReviewActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	if _, review := owner.(*maintenanceView); review {
		return nil
	}
	if action, native := owner.Actions().Snapshot()[ui.KeyO]; native && action.Description == "Maintenance preview" {
		return nil
	}
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := target.UnavailableReason
	if reason == "" && (target.GVR == nil || target.GVR.GVR() != client.NodeGVR.GVR()) {
		reason = "Select a native Node to preview maintenance"
	}
	if reason == "" && target.UID == "" {
		reason = "Refresh the Node list to capture its UID before maintenance review"
	}
	if _, native := owner.(ResourceViewer); !native {
		if _, review := owner.(*maintenanceView); !review {
			reason = "Select a native Node in a resource list"
		}
	}
	return []ui.ActionDescriptor{{ID: "resource.maintenance", Label: "Node maintenance preview", Category: ui.ActionInspect, Shortcut: ":maintenance",
		Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { NewCommand(app).maintenanceCommand(); return nil }}}
}
