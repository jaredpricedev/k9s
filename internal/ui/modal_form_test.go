// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func modalFrame(t *testing.T, modal tview.Primitive, width, height int) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(width, height)
	modal.SetRect(0, 0, width, height)
	modal.Draw(screen)
	var text strings.Builder
	for y := range height {
		for x := range width {
			r, _, _, _ := screen.GetContent(x, y)
			text.WriteRune(r)
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func TestResponsiveFormKeepsEveryFocusedFieldAndButtonsVisible(t *testing.T) {
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			form := tview.NewForm().SetItemPadding(0)
			for _, label := range []string{"Name", "Context", "Namespaces (comma-separated)", "Label selector (optional)", "Kinds (optional, comma-separated)"} {
				form.AddInputField(label, "api", 48, nil, nil)
			}
			form.AddCheckbox("Ignore DaemonSets", true, nil).AddButton("Cancel", nil).AddButton("Save", nil)
			modal := ui.NewModalForm("Create workspace", form).SetText("Saved locally. Explicit namespaces only. No context switching.")
			styles := config.NewStyles()
			require.NoError(t, styles.Load("../../skins/monochrome.yaml", false))
			palette := styles.Dialog()
			modal.SetDialogColors(&palette)
			app := tview.NewApplication().SetRoot(modal, true)
			for index := range form.GetFormItemCount() {
				form.SetFocus(index)
				app.SetFocus(modal)
				frame := modalFrame(t, modal, size[0], size[1])
				require.Contains(t, frame, "Cancel")
				require.Contains(t, frame, "Save")
				require.Contains(t, frame, "Esc cancel")
				item := form.GetFormItem(index)
				_, y, _, h := item.GetRect()
				require.Positive(t, h)
				require.GreaterOrEqual(t, y, 0)
				require.Less(t, y, size[1])
				require.True(t, item.HasFocus())
			}
			require.Equal(t, "api", form.GetFormItem(0).(*tview.InputField).GetText())
		})
	}
}

func TestResponsiveFormBlocksInvisibleSubmissionAndReturnsWithoutLosingInput(t *testing.T) {
	submitted, canceled := false, false
	form := tview.NewForm().AddInputField("Name", "京都e\u0301", 48, nil, nil).AddButton("Save", func() { submitted = true })
	modal := ui.NewModalForm("Edit", form).SetDoneFunc(func(int, string) { canceled = true })
	app := tview.NewApplication().SetRoot(modal, true)
	form.SetFocus(1)
	app.SetFocus(modal)
	frame := modalFrame(t, modal, 40, 15)
	require.Contains(t, frame, "Need 40x16")
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	require.False(t, submitted)
	require.Equal(t, "京都e\u0301", form.GetFormItem(0).(*tview.InputField).GetText())
	frame = modalFrame(t, modal, 60, 24)
	require.Contains(t, frame, "Save")
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	require.True(t, canceled)
}

func TestResponsiveFormStacksCompleteRecoveryButtonsAtMinimum(t *testing.T) {
	form := tview.NewForm().AddButton("Cancel", nil).AddButton("Save previewed evidence", nil)
	modal := ui.NewModalForm("Export evidence", form).SetText("Only previewed retained evidence is saved.")
	frame := modalFrame(t, modal, 40, 16)
	require.Contains(t, frame, "Cancel")
	require.Contains(t, frame, "Save previewed evidence")
	_, cancelY, _, _ := form.GetButton(0).GetRect()
	_, saveY, _, _ := form.GetButton(1).GetRect()
	require.Greater(t, saveY, cancelY)
}
