// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

type modalCheckboxFixture struct {
	screen              tcell.SimulationScreen
	drawable            tview.Primitive
	form                *tview.Form
	checkbox            *tview.Checkbox
	focus               func(tview.Primitive)
	palette             []tcell.Color
	changed, canceled   int
	submitted           bool
	changedLabel        string
	noColor, scrollable bool
}

func TestResponsiveModalCheckboxStatesAndNativeKeyboard(t *testing.T) {
	previous := tview.Styles
	t.Cleanup(func() { tview.Styles = previous })
	for _, skin := range []string{"default", "monochrome"} {
		for _, mode := range []string{"true-color", "256", "no-color"} {
			for _, size := range [][2]int{{80, 24}, {40, 16}} {
				for _, scrollable := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%dx%d/scroll-%t", skin, mode, size[0], size[1], scrollable), func(t *testing.T) {
						checkModalCheckboxKeyboard(t, newModalCheckboxFixture(t, skin, mode, size, scrollable))
					})
				}
			}
		}
	}
}

func TestResponsiveModalCheckboxLongLabelKeepsMarkerInsideInput(t *testing.T) {
	previous := tview.Styles
	t.Cleanup(func() { tview.Styles = previous })
	for _, size := range [][2]int{{80, 24}, {40, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			f := newModalCheckboxFixture(t, "default", "true-color", size, false)
			f.checkbox.SetLabel(strings.Repeat("Long acknowledgment label ", 5))
			f.assertState(t, "[ ]")
			require.Zero(t, f.changed)
			require.False(t, f.checkbox.IsChecked())
		})
	}
}

func newModalCheckboxFixture(t *testing.T, skin, mode string, size [2]int, scrollable bool) *modalCheckboxFixture {
	t.Helper()
	styles := config.NewStyles()
	if skin != "default" {
		require.NoError(t, styles.Load("../../skins/monochrome.yaml", false))
	}
	styles.Update()
	f := &modalCheckboxFixture{noColor: mode == "no-color", scrollable: scrollable}
	f.form = tview.NewForm().AddCheckbox("Acknowledge partial batch", false, func(label string, _ bool) {
		f.changed++
		f.changedLabel = label
	}).AddButton("Cancel", func() { f.canceled++ }).AddButton("Apply", func() { f.submitted = true })
	f.checkbox = f.form.GetFormItem(0).(*tview.Checkbox)
	guidance := "Review the selected target before proceeding."
	if scrollable {
		guidance = strings.Repeat("Retained preview requires explicit acknowledgment.\n", 8)
	}
	modal := NewModalForm("Review acknowledgment", f.form).SetContext("Context captured · UID source-id").SetText(guidance)
	colors := styles.Dialog()
	modal.SetDialogColors(&colors)
	f.form.SetFocus(f.form.GetFormItemCount())
	app := tview.NewApplication().SetRoot(modal, true).SetFocus(modal)
	f.focus = func(p tview.Primitive) { app.SetFocus(p) }
	f.screen = tcell.NewSimulationScreen("")
	require.NoError(t, f.screen.Init())
	t.Cleanup(f.screen.Fini)
	f.screen.SetSize(size[0], size[1])
	f.drawable = modal
	if f.noColor {
		f.drawable = &monochromeRoot{Primitive: modal, styles: styles}
	}
	if mode == "256" {
		f.palette = make([]tcell.Color, 256)
		for i := range f.palette {
			f.palette[i] = tcell.PaletteColor(i)
		}
	}
	return f
}

func checkModalCheckboxKeyboard(t *testing.T, f *modalCheckboxFixture) {
	t.Helper()
	f.assertState(t, "[ ]")
	require.True(t, f.form.GetButton(0).HasFocus(), "Cancel remains the initial focus")
	require.Zero(t, f.changed, "drawing must not acknowledge or invoke callbacks")
	f.form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone), f.focus)
	require.True(t, f.checkbox.HasFocus())
	f.assertState(t, "[ ]")
	f.checkbox.InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone), f.focus)
	f.assertState(t, "[x]")
	require.True(t, f.checkbox.IsChecked())
	require.Equal(t, 1, f.changed)
	require.Equal(t, "Acknowledge partial batch", f.changedLabel, "native callback receives the restored label")
	f.checkbox.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), f.focus)
	require.True(t, f.form.GetButton(0).HasFocus())
	f.assertState(t, "[x]")
	f.form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone), f.focus)
	f.checkbox.InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone), f.focus)
	f.assertState(t, "[ ]")
	require.False(t, f.checkbox.IsChecked())
	require.Equal(t, 2, f.changed)
	f.checkbox.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), f.focus)
	f.form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), f.focus)
	require.Equal(t, 1, f.canceled)
	require.False(t, f.submitted)
	require.Equal(t, 2, f.changed)
}

func (f *modalCheckboxFixture) assertState(t *testing.T, marker string) {
	t.Helper()
	f.drawable.Draw(f.screen)
	width, height := f.screen.Size()
	var frame strings.Builder
	x, y := -1, -1
	for row := range height {
		var line strings.Builder
		for column := range width {
			main, _, _, _ := f.screen.GetContent(column, row)
			line.WriteRune(main)
		}
		if position := strings.Index(line.String(), marker); position >= 0 {
			x, y = utf8.RuneCountInString(line.String()[:position]), row
		}
		frame.WriteString(line.String() + "\n")
	}
	require.GreaterOrEqual(t, x, 0, "native rendered marker %s missing:\n%s", marker, frame.String())
	require.Equal(t, 1, strings.Count(frame.String(), marker))
	require.Contains(t, frame.String(), "Space toggle")
	require.Contains(t, frame.String(), "Esc cancel")
	if f.scrollable {
		require.Contains(t, frame.String(), "PgUp/PgDn")
	}
	fieldX, fieldY, fieldWidth, fieldHeight := f.checkbox.GetInnerRect()
	require.Equal(t, fieldY, y)
	require.Positive(t, fieldHeight)
	require.GreaterOrEqual(t, x, fieldX)
	require.LessOrEqual(t, x+3, fieldX+fieldWidth, "all marker glyphs remain in the native input rectangle")
	f.assertMarkerStyle(t, x, y)
}

func (f *modalCheckboxFixture) assertMarkerStyle(t *testing.T, x, y int) {
	t.Helper()
	for column := x; column < x+3; column++ {
		_, _, style, _ := f.screen.GetContent(column, y)
		fg, bg, attrs := style.Decompose()
		if f.noColor {
			require.Equal(t, tcell.ColorDefault, fg)
			require.Equal(t, tcell.ColorDefault, bg)
			require.Equal(t, f.checkbox.HasFocus(), attrs&tcell.AttrReverse != 0)
			continue
		}
		if attrs&tcell.AttrReverse != 0 {
			fg, bg = bg, fg
		}
		if len(f.palette) > 0 {
			fg, bg = tcell.FindColor(fg, f.palette), tcell.FindColor(bg, f.palette)
		}
		require.GreaterOrEqual(t, config.ContrastRatio(fg, bg), 4.5)
	}
}
