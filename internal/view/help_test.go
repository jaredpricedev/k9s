// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view_test

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/view"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelp(t *testing.T) {
	ctx := makeCtx(t)

	app := ctx.Value(internal.KeyApp).(*view.App)
	po := view.NewPod(client.PodGVR)
	require.NoError(t, po.Init(ctx))
	app.Content.Push(po)

	v := view.NewHelp(app)

	require.NoError(t, v.Init(ctx))
	assert.Equal(t, "RESOURCE", strings.TrimSpace(v.GetCell(0, 0).Text))
	assert.Equal(t, "Actions", helpAction(t, v, "<ctrl-o>", "Actions"))
	assert.Contains(t, helpAction(t, v, "<a>", "Attach"), "Attach")
	hidden, ok := po.Actions().Get(tcell.KeyCtrlZ)
	require.True(t, ok)
	require.False(t, hidden.Opts.Visible, "fixture shortcut is omitted from the compact menu")
	assert.Equal(t, hidden.Description, helpAction(t, v, "<ctrl-z>", hidden.Description),
		"help must discover bindings hidden from the compact menu")
	assert.Contains(t, helpAction(t, v, "<:compare>", "Compare observations"), "Compare observations")
	assert.Contains(t, helpAction(t, v, "<:evidence>", "Capture evidence preview"), "Capture evidence preview")
}

func TestHelpExplainsDisabledActionsFromSharedRegistry(t *testing.T) {
	ctx := makeCtx(t)
	app := ctx.Value(internal.KeyApp).(*view.App)
	po := view.NewPod(client.PodGVR)
	require.NoError(t, po.Init(ctx))
	app.Content.Push(po)
	// Changing restrictions after binding must retain discoverability and derive
	// the current disabled reason rather than remove the existing action.
	app.Config.K9s.ReadOnly = true
	v := view.NewHelp(app)
	require.NoError(t, v.Init(ctx))
	assert.Contains(t, helpAction(t, v, "<a>", "Attach"), "Read-only mode: changes are disabled")
	assert.Contains(t, helpAction(t, v, "<l>", "Logs"), "Select a resource first")
}

func helpAction(t *testing.T, help *view.Help, shortcut, label string) string {
	t.Helper()
	for row := 1; row < help.GetRowCount(); row++ {
		key := strings.TrimSpace(help.GetCell(row, 0).Text)
		description := strings.TrimSpace(help.GetCell(row, 1).Text)
		if key == shortcut && strings.Contains(description, label) {
			return description
		}
	}
	t.Fatalf("help omitted discoverable action %s %s", shortcut, label)
	return ""
}
