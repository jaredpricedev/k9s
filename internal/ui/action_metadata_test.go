// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui_test

import (
	"testing"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionDiscoverySeparatesVisibilityAndAvailability(t *testing.T) {
	actions := ui.NewKeyActions()
	filter := ui.NewKeyAction("Filter", func(e *tcell.EventKey) *tcell.EventKey { return e }, false)
	filter.ID = "resource.filter"
	actions.Add(ui.KeySlash, filter)
	change := ui.NewKeyActionWithOpts("Delete", filter.Action, ui.ActionOpts{Dangerous: true, Visible: true})
	actions.Add(tcell.KeyCtrlD, change)
	descriptions := ui.DescribeActions(actions, ui.ActionContext{ReadOnly: true, SelectionReason: "Select a resource first"})
	require.Len(t, descriptions, 2)
	for _, item := range descriptions {
		require.True(t, item.Discoverable)
		if item.ID == filter.ID {
			assert.False(t, item.Visible)
			assert.True(t, item.Available(), "view-local filtering never needs an API object")
			assert.Equal(t, ui.ActionFilter, item.Category)
		} else {
			assert.False(t, item.Available())
			assert.Contains(t, item.UnavailableReason, "Read-only")
		}
	}
	for _, item := range ui.DescribeActions(actions, ui.ActionContext{SelectionReason: "Select a resource first"}) {
		if item.Label == "Delete" {
			assert.Equal(t, "Select a resource first", item.UnavailableReason)
		}
	}
}

func TestActionAvailabilityCallbacksRunOutsideLock(t *testing.T) {
	actions := ui.NewKeyActions()
	action := ui.NewKeyAction("Inspect", func(e *tcell.EventKey) *tcell.EventKey { return e }, true)
	action.Availability = func() string {
		actions.Add(ui.KeyA, ui.NewKeyAction("added", nil, false))
		return "No retained entry selected"
	}
	actions.Add(tcell.KeyEnter, action)
	descriptions := ui.DescribeActions(actions, ui.ActionContext{})
	require.Len(t, descriptions, 1, "callbacks cannot mutate the owned discovery snapshot")
	assert.Equal(t, "No retained entry selected", descriptions[0].UnavailableReason)
	assert.False(t, descriptions[0].Available())
	assert.Equal(t, 2, actions.Len())
}
