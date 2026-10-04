// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dialog

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorDialog(t *testing.T) {
	p := ui.NewPages()

	ShowError(new(config.Dialog), p, "Yo")

	d := p.GetPrimitive(dialogKey).(*ui.ModalForm)
	assert.NotNil(t, d)
	dismiss(p)
	assert.Nil(t, p.GetPrimitive(dialogKey))
}

func TestErrorRecoveryKeepsCauseReadableAndRunsOnlyExplicitAction(t *testing.T) {
	styles := config.NewStyles()
	require.NoError(t, styles.Load("../../../skins/monochrome.yaml", false))
	palette := styles.Dialog()
	pages := ui.NewPages()
	attempts := 0
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 16}} {
		ShowErrorRecovery(&palette, pages, "Denied API read: 京都 [untrusted] "+strings.Repeat("long diagnostic ", 60), ErrorRecovery{Title: "Read denied", Instruction: "Review access before retrying this retained scope.", ActionLabel: "Retry read", Action: func() { attempts++ }})
		modal := pages.GetPrimitive(dialogKey).(*ui.ModalForm)
		var form *tview.Form
		modal.Focus(func(p tview.Primitive) { form = p.(*tview.Form) })
		app := tview.NewApplication().SetRoot(pages, true).SetFocus(modal)
		screen := tcell.NewSimulationScreen("")
		require.NoError(t, screen.Init())
		screen.SetSize(size[0], size[1])
		modal.Draw(screen)
		var frame strings.Builder
		for y := range size[1] {
			for x := range size[0] {
				r, _, _, _ := screen.GetContent(x, y)
				frame.WriteRune(r)
			}
			frame.WriteByte('\n')
		}
		text := frame.String()
		require.Contains(t, text, "Denied API read")
		require.Contains(t, text, "Dismiss")
		require.Contains(t, text, "Retry read")
		require.NotContains(t, text, "Ruroh")
		require.NotContains(t, text, "^__^")
		form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
		require.Equal(t, 0, attempts)
		screen.Fini()
	}
	ShowErrorRecovery(&palette, pages, "read failed", ErrorRecovery{ActionLabel: "Retry read", Action: func() { attempts++ }})
	modal := pages.GetPrimitive(dialogKey).(*ui.ModalForm)
	var form *tview.Form
	modal.Focus(func(p tview.Primitive) { form = p.(*tview.Form) })
	app := tview.NewApplication().SetRoot(pages, true)
	form.SetFocus(1)
	app.SetFocus(modal)
	form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	require.Equal(t, 1, attempts)
	require.Nil(t, pages.GetPrimitive(dialogKey))
}
