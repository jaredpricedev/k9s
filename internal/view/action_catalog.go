// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

type actionOwner interface {
	model.Component
	Actions() *ui.KeyActions
}

func actionTarget(owner actionOwner, contextName string) SelectedResourceTarget {
	if selected, ok := owner.(SelectedResource); ok {
		target := selected.SelectedResource()
		if target.Context == "" {
			target.Context = contextName
		} else if target.Context != contextName {
			target.UnavailableReason = "Context changed; reopen the selected resource in its saved workspace context"
		}
		return target
	}
	if resources, ok := owner.(ResourceViewer); ok {
		return resolveSelectedResource(resources, contextName)
	}
	return SelectedResourceTarget{Context: contextName, UnavailableReason: "This action needs a selected API resource"}
}

func actionCatalog(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	// Selection is a resource capability. Stream entry selection has its own
	// availability callbacks and never substitutes a pod name for an API target.
	actionContext := ui.ActionContext{ReadOnly: app.Config.IsReadOnly(), SelectionReason: target.UnavailableReason}
	// View and mode bindings own their labels and override the application
	// snapshot (Enter is delegated by the app; workbench help routes locally).
	actions := app.GetActions().Snapshot()
	maps.Copy(actions, owner.Actions().Snapshot())
	if resource, ok := owner.(ResourceViewer); ok && resource.GetTable() != nil {
		for key, action := range actions {
			switch strings.ToLower(action.Description) {
			case "view", "describe", "yaml", "logs", "previous logs", "copy", "edit", client.DeleteVerb, "scale", "restart", "shell", "exec", "port-forward":
				action.Opts.RequiresSelection = true
				actions[key] = action
			}
		}
	}
	result := ui.DescribeActions(ui.NewKeyActionsFromMap(actions), actionContext)
	if resource, ok := owner.(ResourceViewer); ok && resource.GetTable() != nil {
		for _, item := range []struct {
			key       tcell.Key
			id, label string
		}{
			{ui.KeyE, "resource.edit", "Edit"}, {tcell.KeyCtrlD, "resource.delete", "Delete"},
		} {
			if _, exists := actions[item.key]; exists {
				continue
			}
			reason := "This view does not expose the required change capability"
			if actionContext.ReadOnly {
				reason = "Read-only mode: changes are disabled"
			}
			result = append(result, ui.ActionDescriptor{ID: item.id, Label: item.label, Category: ui.ActionChange,
				Key: item.key, Shortcut: tcell.KeyNames[item.key], Discoverable: true, RequiresSelection: true,
				UnavailableReason: reason})
		}
	}
	if _, bound := actions[tcell.KeyCtrlO]; !bound {
		result = append(result, ui.ActionDescriptor{
			ID: "view.actions", Label: "Actions", Category: ui.ActionNavigate, Shortcut: "Ctrl-O",
			Discoverable: true, Visible: true, Handler: app.actionsCmd,
		})
	}
	if _, ok := owner.(ResourceViewer); ok || target.Err() == nil {
		for _, item := range []struct{ id, label, command string }{
			{"resource.troubleshoot", "Troubleshoot snapshot", troubleshootCommand},
			{"resource.tls", "TLS certificate inspection", tlsCommand},
		} {
			command := item.command
			reason := target.UnavailableReason
			if command == tlsCommand && reason == "" && !tlsEntryResource(target.GVR.R()) {
				reason = "Select a Secret, Certificate or Ingress to inspect TLS"
			}
			result = append(result, ui.ActionDescriptor{
				ID: item.id, Label: item.label, Category: ui.ActionInspect, Shortcut: ":" + command,
				Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
				Handler: func(*tcell.EventKey) *tcell.EventKey { app.openTargetInspection(target, command); return nil },
			})
		}
		result = append(result, ui.ActionDescriptor{
			ID: "resource.compare", Label: "Compare observations", Category: ui.ActionInspect, Shortcut: ":compare",
			Discoverable: true, RequiresSelection: true, UnavailableReason: target.UnavailableReason,
			Handler: func(*tcell.EventKey) *tcell.EventKey { app.openResourceComparison(target); return nil },
		})
	}
	result = append(result, investigationActions(owner, app)...)
	result = append(result, changeReviewActions(owner, app)...)
	result = append(result, maintenanceReviewActions(owner, app)...)
	result = append(result, configurationActions(owner, app)...)
	result = append(result, workspaceActions(app)...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return ui.ActionCategoryOrder(result[i].Category) < ui.ActionCategoryOrder(result[j].Category)
		}
		return result[i].Label < result[j].Label
	})
	return result
}

func changeReviewActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := target.UnavailableReason
	if reason == "" && rolloutTargetKind(target) == "" {
		reason = "Select a native Deployment, StatefulSet or DaemonSet"
	}
	if reason == "" && target.UID == "" {
		reason = "Reopen the workload to capture its identity before review"
	}
	return []ui.ActionDescriptor{
		{ID: "command.review", Label: "Review local manifest", Category: ui.ActionInspect, Shortcut: ":review", Discoverable: true,
			Handler: func(*tcell.EventKey) *tcell.EventKey { app.openDesiredReview(""); return nil }},
		{ID: "resource.rollout", Label: "Controller rollout review", Category: ui.ActionInspect, Shortcut: ":rollout",
			Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
			Handler: func(*tcell.EventKey) *tcell.EventKey { app.openRolloutReview(target); return nil }},
	}
}

func investigationActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	var result []ui.ActionDescriptor
	for _, item := range []struct {
		id, label, shortcut, category string
		run                           func()
	}{
		{"resource.pressure", "Resource pressure", ":pressure", ui.ActionInspect, func() { NewCommand(app).pressureCommand() }},
		{"resource.capacity", "Capacity and autoscaling review", ":capacity", ui.ActionInspect, func() { NewCommand(app).capacityCommand() }},
		{"resource.evidence", "Capture evidence preview", ":evidence", ui.ActionExport, func() { NewCommand(app).evidenceCommand("evidence") }},
	} {
		reason := target.UnavailableReason
		_, resourceView := owner.(ResourceViewer)
		_, selectedView := owner.(SelectedResource)
		if !resourceView && !selectedView {
			reason = "Open a resource list and select an API object first"
		}
		if item.id == "resource.capacity" && reason == "" {
			if err := capacityTargetError(&target); err != nil {
				reason = err.Error()
			}
		}
		if item.id == "resource.evidence" {
			switch owner.(type) {
			case *comparisonView, *inspectionDetails:
				_, err := retainedEvidenceBundle(owner)
				reason = ""
				if err != nil {
					reason = err.Error()
				}
			}
		}
		if item.id == "resource.pressure" && reason == "" {
			switch target.GVR.R() {
			case "pods", "deployments", "daemonsets", "statefulsets", "replicasets", "jobs":
			default:
				reason = "Select a Pod, Deployment, DaemonSet, StatefulSet, ReplicaSet or Job to inspect pressure"
			}
		}
		run := item.run
		result = append(result, ui.ActionDescriptor{ID: item.id, Label: item.label, Category: item.category, Shortcut: item.shortcut,
			Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
			Handler: func(*tcell.EventKey) *tcell.EventKey { run(); return nil }})
	}
	result = append(result, ui.ActionDescriptor{
		ID: "command.diagnostics", Label: "Capability diagnostics", Category: ui.ActionInspect,
		Shortcut: ":diagnostics", Discoverable: true,
		Handler: func(*tcell.EventKey) *tcell.EventKey { NewCommand(app).capabilityCommand("diagnostics"); return nil }}, ui.ActionDescriptor{
		ID: "command.providers", Label: "Provider checks", Category: ui.ActionInspect,
		Shortcut: ":providers", Discoverable: true,
		Handler: func(*tcell.EventKey) *tcell.EventKey { NewCommand(app).providerCommand("providers"); return nil }}, ui.ActionDescriptor{
		ID: "command.upgrade-readiness", Label: "Upgrade readiness evidence", Category: ui.ActionInspect, Shortcut: ":upgrade-readiness", Discoverable: true,
		UnavailableReason: func() string {
			if client.IsClusterWide(app.Config.ActiveNamespace()) {
				return "Select one current namespace before collecting upgrade evidence"
			}
			return ""
		}(),
		Handler: func(*tcell.EventKey) *tcell.EventKey {
			NewCommand(app).upgradeReadinessCommand("upgrade-readiness")
			return nil
		}})
	return result
}

func workspaceActions(app *App) []ui.ActionDescriptor {
	var result []ui.ActionDescriptor
	for _, item := range []struct{ id, label, command string }{
		{"command.workspace", "Saved workspaces", "workspace"},
		{"command.daily", "Daily findings queue", dailyCommand},
		{"command.inventory", "Scoped inventory", inventoryCommand},
		{"command.connection", "Connection health", connectionCommand},
	} {
		command := item.command
		result = append(result, ui.ActionDescriptor{
			ID: item.id, Label: item.label, Category: ui.ActionNavigate, Shortcut: ":" + command,
			Discoverable: true, Handler: func(*tcell.EventKey) *tcell.EventKey {
				c := NewCommand(app)
				if command == connectionCommand {
					c.connectionHealthCommand(command)
				} else {
					c.dailyWorkspaceCommand(command)
				}
				return nil
			}})
	}
	return result
}

func actionCatalogHints(owner actionOwner, app *App) model.MenuHints {
	var hints model.MenuHints
	for _, item := range actionCatalog(owner, app) {
		if !item.Discoverable {
			continue
		}
		label := item.Label
		if item.UnavailableReason != "" {
			label += " · " + item.UnavailableReason
		}
		hints = append(hints, model.MenuHint{Mnemonic: item.Shortcut, Description: label, Visible: item.Visible, Priority: item.Priority})
	}
	return hints
}

func (a *App) actionsCmd(event *tcell.EventKey) *tcell.EventKey {
	if event != nil && a.Prompt().InCmdMode() {
		return event
	}
	if event == nil && a.CmdBuff().IsActive() {
		a.ResetCmd()
	}
	owner, ok := a.Content.Top().(actionOwner)
	if !ok {
		a.Flash().Err(fmt.Errorf("Actions are unavailable in this view"))
		return nil
	}
	p := &actionPalette{Picker: NewPicker(), owner: owner, app: a, target: actionTarget(owner, a.Config.ActiveContextName())}
	p.path = p.target.Path()
	ctx := context.WithValue(context.Background(), internal.KeyApp, a)
	if err := p.Init(ctx); err != nil {
		a.Flash().Err(err)
		return nil
	}
	// Action discovery is an overlay: opening it never stops stream collection
	// or destroys the owner's frozen snapshot, query or scroll position.
	p.originalCapture = a.GetInputCapture()
	a.Content.AddPage(actionsCommand, p, true, true)
	a.SetInputCapture(p.keyboard)
	a.SetFocus(p)
	return nil
}

// sharedActionHelp keeps the compact action reference synchronized with owned
// registry labels. A view may append its query grammar and source caveats.
func sharedActionHelp(actions *ui.KeyActions, actionContext ui.ActionContext) string {
	var text strings.Builder
	category := ""
	for _, item := range ui.DescribeActions(actions, actionContext) {
		if !item.Discoverable {
			continue
		}
		if item.Category != category {
			category = item.Category
			fmt.Fprintf(&text, "\n%s\n", category)
		}
		fmt.Fprintf(&text, "%-10s %s", item.Shortcut, item.Label)
		if item.UnavailableReason != "" {
			fmt.Fprintf(&text, " · %s", item.UnavailableReason)
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func (p *actionPalette) close() {
	p.app.SetInputCapture(p.originalCapture)
	p.app.Content.RemovePage(actionsCommand)
	if top := p.app.Content.Top(); top != nil {
		p.app.SetFocus(top)
	}
}

func configurationActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := ""
	if _, err := configurationScope(target); err != nil {
		reason = err.Error()
	}
	return []ui.ActionDescriptor{{
		ID: "resource.configuration-review", Label: "Declared configuration references", Category: ui.ActionInspect,
		Shortcut: ":" + configurationCommand, Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openConfigurationReview(target); return nil },
	}}
}
