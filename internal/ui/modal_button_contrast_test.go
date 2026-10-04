// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func assertModalButtonContrast(t *testing.T, screen tcell.Screen, button *tview.Button, palette []tcell.Color, noColor bool) {
	t.Helper()
	x, y, width, _ := button.GetRect()
	label := []rune(button.GetLabel())
	start := x + (width-len(label))/2
	for offset, expected := range label {
		actual, _, style, _ := screen.GetContent(start+offset, y)
		require.Equal(t, expected, actual, "the label must exist in the actual native cells")
		fg, bg, attrs := style.Decompose()
		t.Logf("%s cell %d,%d fg=%06x bg=%06x attrs=%v", button.GetLabel(), start+offset, y, fg.Hex(), bg.Hex(), attrs)
		if noColor {
			require.Equal(t, tcell.ColorDefault, fg)
			require.Equal(t, tcell.ColorDefault, bg)
			require.Equal(t, button.HasFocus(), attrs&tcell.AttrReverse != 0, "NO_COLOR must retain a visible focused button")
			continue
		}
		if attrs&tcell.AttrReverse != 0 {
			fg, bg = bg, fg
		}
		if len(palette) > 0 {
			fg, bg = tcell.FindColor(fg, palette), tcell.FindColor(bg, palette)
		}
		require.GreaterOrEqual(t, config.ContrastRatio(fg, bg), 4.5, "rendered %s text must contrast with its background", button.GetLabel())
	}
}

func TestResponsiveModalButtonsKeepRenderedContrastAndCancelFocus(t *testing.T) {
	previous := tview.Styles
	t.Cleanup(func() { tview.Styles = previous })
	for _, skin := range []string{"default", "monochrome"} {
		for _, mode := range []string{"true-color", "256", "no-color"} {
			for _, size := range [][2]int{{80, 24}, {40, 16}} {
				t.Run(fmt.Sprintf("%s/%s/%dx%d", skin, mode, size[0], size[1]), func(t *testing.T) {
					checkResponsiveButtonFocus(t, skin, mode, size)
				})
			}
		}
	}
}

func checkResponsiveButtonFocus(t *testing.T, skin, mode string, size [2]int) {
	t.Helper()
	styles := config.NewStyles()
	if skin != "default" {
		require.NoError(t, styles.Load("../../skins/monochrome.yaml", false))
	}
	styles.Update()
	var palette []tcell.Color
	if mode == "256" {
		palette = make([]tcell.Color, 256)
		for i := range palette {
			palette[i] = tcell.PaletteColor(i)
		}
	}
	noColor := mode == "no-color"
	canceled, submitted := false, false
	form := tview.NewForm().AddCheckbox("Acknowledge partial batch", false, nil).
		AddButton("Cancel", func() { canceled = true }).AddButton("Apply", func() { submitted = true })
	modal := NewModalForm("Apply reviewed change set", form).SetText("Review the selected target and retained preview.")
	colors := styles.Dialog()
	modal.SetDialogColors(&colors)
	form.SetFocus(form.GetFormItemCount())
	app := tview.NewApplication().SetRoot(modal, true).SetFocus(modal)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	t.Cleanup(screen.Fini)
	screen.SetSize(size[0], size[1])
	var drawable tview.Primitive = modal
	if noColor {
		drawable = &monochromeRoot{Primitive: modal, styles: styles}
	}
	drawable.Draw(screen)
	require.True(t, form.GetButton(0).HasFocus(), "Cancel remains the initial focus")
	assertModalButtonContrast(t, screen, form.GetButton(0), palette, noColor)
	assertModalButtonContrast(t, screen, form.GetButton(1), palette, noColor)
	focus := func(p tview.Primitive) { app.SetFocus(p) }
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), focus)
	drawable.Draw(screen)
	require.True(t, form.GetButton(1).HasFocus())
	assertModalButtonContrast(t, screen, form.GetButton(0), palette, noColor)
	assertModalButtonContrast(t, screen, form.GetButton(1), palette, noColor)
	form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone), focus)
	drawable.Draw(screen)
	assertModalButtonContrast(t, screen, form.GetButton(0), palette, noColor)
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), focus)
	require.True(t, canceled)
	require.False(t, submitted)
	require.False(t, form.GetFormItem(0).(*tview.Checkbox).IsChecked(), "styling must not acknowledge the batch")
}
