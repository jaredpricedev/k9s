// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func TestRecoveryForceDrawCancelsStaleConfirmationWithoutFocusDeadlock(t *testing.T) {
	v, dyn := recoveryViewFixture(t)
	v.recoveryPlan.Unreviewed = true
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	require.NotNil(t, v.recoveryModal)
	oldForm := v.recoveryForm
	preview, cancelPreview := context.WithCancel(t.Context())
	follow, cancelFollow := context.WithCancel(t.Context())
	v.recoveryCancel, v.followCancel, v.following = cancelPreview, cancelFollow, true
	originalNamespace := v.app.Config.ActiveNamespace()
	require.NoError(t, v.app.Config.SetActiveNamespace("changed-destination"))
	require.NoError(t, v.app.Config.SetActiveNamespace(originalNamespace))
	require.False(t, v.destinationCurrent(), "Returning to the same namespace must not revive the capture")
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	// The Pages owns modal focus, while the underlying view owns the stale draw.
	v.app.SetScreen(screen).SetRoot(v, true).SetFocus(v.app.Content.Pages)
	drawn := make(chan struct{})
	go func() { v.app.ForceDraw(); close(drawn) }()
	select {
	case <-drawn:
	case <-time.After(2 * time.Second):
		t.Fatal("ForceDraw deadlocked while invalidating the recovery confirmation")
	}
	require.ErrorIs(t, preview.Err(), context.Canceled)
	require.ErrorIs(t, follow.Err(), context.Canceled)
	require.NotNil(t, v.recoveryModal)
	require.Same(t, oldForm, v.recoveryForm)
	require.Contains(t, v.recoveryNotice, "Destination changed")
	require.Equal(t, rolloutRecoveryUnavailable, oldForm.GetButton(1).GetLabel())
	// A retained Apply callback still checks captured eligibility and cannot write.
	oldForm.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
	require.Nil(t, v.recoveryModal)
	// Cancel and Escape remain usable outside drawing.
	v.recoveryConfirmation("Retained review", "Unavailable", false, "Apply", func() { t.Error("Unexpected acceptance") })
	v.recoveryForm.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, 0), func(tview.Primitive) {})
	require.Nil(t, v.recoveryModal)
	require.False(t, v.app.Content.Pages.HasPage(rolloutRecoveryFormPage))
	screen.Fini()
}
