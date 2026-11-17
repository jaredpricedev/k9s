// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"testing"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
)

func nextCheckShortcuts(checks []ui.NextCheck) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.Shortcut)
	}
	return out
}

// The next-step line may only print what the action registry would run for
// the selected row in this view.
func TestNextStepChecksFollowTheActionRegistry(t *testing.T) {
	b, _, _, _ := nativeGuardFixture(t, nativeObserved)
	noop := func(*tcell.EventKey) *tcell.EventKey { return nil }
	b.Actions().Add(ui.KeyP, ui.NewKeyAction("Logs Previous", noop, true))
	b.Actions().Add(ui.KeyD, ui.NewKeyAction("Describe", noop, true))
	b.Actions().Add(ui.KeyX, ui.NewKeyActionWithOpts("Kill", noop, ui.ActionOpts{Visible: true, Dangerous: true}))

	checks := []ui.NextCheck{
		{Key: ui.KeyP, Shortcut: "p", Label: "previous logs"},
		{Key: ui.KeyD, Shortcut: "d", Label: "describe"},
		{Key: ui.KeyX, Shortcut: "x", Label: "a mutating action"},
		{Key: ui.KeyZ, Shortcut: "z", Label: "a key this view does not bind"},
		{Key: tcell.KeyCtrlD, Shortcut: "p", Label: "a shortcut name on another key"},
		{Shortcut: ":pressure"},
		{Shortcut: ":capacity"},
		{Shortcut: ":storage"},
		{Shortcut: ":config-review"},
		{Shortcut: ":rollout"},
		{Shortcut: ":no-such-command"},
	}
	assert.Equal(t, []string{"p", "d", "x", ":pressure", ":capacity", ":storage", ":config-review"},
		nextCheckShortcuts(b.runnableNextChecks(checks)), "a pod is not a rollout target, and unknown keys and commands are dropped")

	b.app.Config.K9s.ReadOnly = true
	assert.Equal(t, []string{"p", "d", ":pressure", ":capacity", ":storage", ":config-review"},
		nextCheckShortcuts(b.runnableNextChecks(checks)), "read-only mode never offers a mutating action")

	// Describe needs a selected row; with none, only selection-free actions stay.
	b.Select(0, 0)
	got := nextCheckShortcuts(b.runnableNextChecks(checks))
	assert.NotContains(t, got, "d")
	assert.NotContains(t, got, ":pressure")
	assert.NotContains(t, got, ":capacity")
}
