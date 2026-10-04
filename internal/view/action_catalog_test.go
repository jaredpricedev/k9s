// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type discoveryOwner struct {
	*Details
	starts, stops int
}

func (v *discoveryOwner) Start() { v.starts++ }
func (v *discoveryOwner) Stop()  { v.stops++ }

func TestActionOverlayPreservesOwnerAndDiscoversHiddenBindings(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	owner := &discoveryOwner{Details: NewDetails(app, "stream", "", contentTXT, false)}
	var executed bool
	action := ui.NewKeyAction("Retained entries", func(*tcell.EventKey) *tcell.EventKey { executed = true; return nil }, false)
	action.ID = "stream.export"
	action.Category = ui.ActionExport
	owner.actions.Add(ui.KeyE, action)
	owner.text.SetText("frozen snapshot").ScrollTo(3, 7)
	app.Content.Push(owner)
	app.actionsCmd(nil)
	palette, ok := app.Content.GetPrimitive(actionsCommand).(*actionPalette)
	require.True(t, ok)
	assert.Same(t, owner, app.Content.Top())
	assert.Zero(t, owner.stops, "discovery must not stop collectors or observers")
	palette.query = "export retained"
	palette.refresh()
	require.Len(t, palette.entries, 1)
	assert.Contains(t, palette.entries[0].label, ui.ActionExport, "category metadata must be searchable even when it is absent from the label")
	assert.Equal(t, action.ID, palette.entries[0].action.ID)
	palette.invoke(0)
	assert.True(t, executed)
	assert.Zero(t, owner.stops)
	assert.Zero(t, owner.starts, "closing discovery must not restart collection")
	assert.Equal(t, "frozen snapshot", owner.text.GetText(true))
	row, column := owner.text.GetScrollOffset()
	assert.Equal(t, 3, row)
	assert.Equal(t, 7, column)
}

func TestDisabledActionStaysSearchableAndCannotExecute(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	app.Config.K9s.ReadOnly = true
	owner := &discoveryOwner{Details: NewDetails(app, "test", "", contentTXT, false)}
	var executed bool
	action := ui.NewKeyActionWithOpts("Delete", func(*tcell.EventKey) *tcell.EventKey { executed = true; return nil }, ui.ActionOpts{Dangerous: true})
	owner.actions.Add(tcell.KeyCtrlD, action)
	app.Content.Push(owner)
	app.actionsCmd(nil)
	palette := app.Content.GetPrimitive(actionsCommand).(*actionPalette)
	palette.query = "delete"
	palette.refresh()
	require.Len(t, palette.entries, 1)
	assert.Contains(t, palette.entries[0].label, "Read-only")
	palette.invoke(0)
	assert.False(t, executed)
	assert.Same(t, palette, app.Content.GetPrimitive(actionsCommand))
	for _, hint := range actionCatalogHints(owner, app) {
		if hint.Mnemonic == tcell.KeyNames[tcell.KeyCtrlD] {
			assert.Contains(t, hint.Description, "Read-only")
		}
	}
}

type discoveryResourceOwner struct {
	*Browser
	target SelectedResourceTarget
}

func (v *discoveryResourceOwner) SelectedResource() SelectedResourceTarget { return v.target }

func TestInvestigationActionsReflectResourceAndRetainedEvidenceAvailability(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR,
		Namespace: "apps", Name: "api", UID: "fixture-pod"}
	action := func(owner actionOwner, id string) ui.ActionDescriptor {
		for _, descriptor := range actionCatalog(owner, app) {
			if descriptor.ID == id {
				return descriptor
			}
		}
		t.Fatalf("missing discoverable action %s", id)
		return ui.ActionDescriptor{}
	}
	resource := &discoveryResourceOwner{Browser: &Browser{Table: NewTable(client.PodGVR)}, target: target}
	descriptor := action(resource, "resource.pressure")
	assert.True(t, descriptor.Available())
	resource.target.GVR = client.NewGVR("v1/secrets")
	descriptor = action(resource, "resource.pressure")
	assert.False(t, descriptor.Available())
	assert.Contains(t, descriptor.UnavailableReason, "Select a Pod")
	assert.True(t, descriptor.Discoverable)

	comparison := &comparisonView{Details: NewDetails(app, "Resource comparison", target.Path(), contentInspection, true), target: target}
	descriptor = action(comparison, "resource.evidence")
	assert.False(t, descriptor.Available())
	assert.Contains(t, descriptor.UnavailableReason, "wait for retained comparison baseline A")
	a := inspect.NewObservation(inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(),
		Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, "Fixture API", time.Now(), map[string]any{"kind": "Pod"})
	comparison.baseline = &a
	descriptor = action(comparison, "resource.evidence")
	assert.True(t, descriptor.Available())

	inspector := &inspectionDetails{Details: NewDetails(app, "pressure", target.Path(), contentInspection, true), target: target}
	descriptor = action(inspector, "resource.evidence")
	assert.False(t, descriptor.Available())
	assert.Contains(t, descriptor.UnavailableReason, "wait for a retained inspection snapshot")
	inspector.snapshot = inspectionSnapshot{Text: "Fixture retained pressure evidence", UID: target.UID, CapturedAt: time.Now()}
	descriptor = action(inspector, "resource.evidence")
	assert.True(t, descriptor.Available())
}
