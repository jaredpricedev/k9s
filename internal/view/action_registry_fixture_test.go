// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view_test

import (
	"testing"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

type actionRegistryViewer interface {
	Actions() *ui.KeyActions
	Hints() model.MenuHints
}

func assertActionRegistry(t *testing.T, viewer actionRegistryViewer) {
	t.Helper()
	action, registered := viewer.Actions().Get(tcell.KeyCtrlO)
	require.True(t, registered, "every view exposes the action registry through Ctrl-O")
	require.True(t, action.Opts.Visible)
	require.Equal(t, "Actions", action.Description)
	require.NotNil(t, action.Action)
	for _, hint := range viewer.Hints() {
		if hint.Mnemonic == "Ctrl-O" {
			require.True(t, hint.Visible)
			return
		}
	}
	t.Fatal("the registered Ctrl-O action must also be visible in the view's hints")
}

func assertAppDestinationAndRegistry(t *testing.T, app *view.App) {
	t.Helper()
	for _, key := range []tcell.Key{tcell.KeyCtrlO, tcell.KeyF2} {
		action, registered := app.GetActions().Get(key)
		require.True(t, registered)
		require.True(t, action.Opts.Visible)
		require.NotNil(t, action.Action)
	}
}
