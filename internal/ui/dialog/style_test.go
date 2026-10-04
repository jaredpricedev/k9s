// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dialog

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFormSkinUpdatesPreserveEditingAndFocus(t *testing.T) {
	styles := config.NewStyles()
	form := tview.NewForm().AddInputField("Destination", "payments/api", 16, nil, nil).
		AddButton("Cancel", nil).AddButton("Save", nil)
	modal := ui.NewModalForm("Evidence", form)
	cleanup := BindFormStyles(styles, form, modal)
	defer cleanup()
	form.GetButton(0).Focus(func(tview.Primitive) {})
	input := form.GetFormItem(0).(*tview.InputField)
	input.SetText("edited/context/path")
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(80, 24)
	for _, skin := range []string{"", "../../../skins/high-contrast.yaml", "../../../skins/monochrome.yaml"} {
		styles.Reset(false)
		if skin != "" {
			require.NoError(t, styles.Load(skin, false))
		}
		styles.Update()
		assert.Equal(t, "edited/context/path", input.GetText())
		assert.True(t, form.GetButton(0).HasFocus())
		for index := range 2 {
			button := form.GetButton(index)
			button.SetRect(index*14, 0, 12, 1)
			button.Draw(screen)
			_, _, style, _ := screen.GetContent(index*14+4, 0)
			fg, bg, _ := style.Decompose()
			assert.GreaterOrEqual(t, config.ContrastRatio(fg, bg), 4.5, "skin %q button %d", skin, index)
		}
	}
	// A dismissed form does not retain a listener or change an edited value.
	cleanup()
	cleanup()
	color := form.GetButton(1).GetBackgroundColor()
	styles.Reset(false)
	styles.Update()
	assert.Equal(t, color, form.GetButton(1).GetBackgroundColor())
	assert.Equal(t, "edited/context/path", input.GetText())
}

func TestGenericDialogSkinBindingDismissalAndReplacement(t *testing.T) {
	styles := config.NewStyles()
	pages := ui.NewPages()
	dialogStyle := styles.Dialog()
	ShowConfirm(&dialogStyle, pages, "Destination", "Context: production", func() {}, func() {})
	modal := pages.GetPrimitive(dialogKey).(*ui.ModalForm)
	var form *tview.Form
	modal.Focus(func(p tview.Primitive) { form = p.(*tview.Form) })
	require.NotNil(t, form)
	form.GetButton(0).Focus(func(tview.Primitive) {})
	require.NoError(t, styles.Load("../../../skins/monochrome.yaml", false))
	styles.Update()
	assert.Equal(t, styles.Dialog().ButtonBgColor.Color(), form.GetButton(1).GetBackgroundColor())
	assert.True(t, form.GetButton(0).HasFocus(), "skin changes keep the user's focused choice")
	oldBackground := form.GetButton(1).GetBackgroundColor()
	// Replacing the dialog must detach the old listener, even without its callbacks.
	dialogStyle = styles.Dialog()
	ShowError(&dialogStyle, pages, "Failed [untrusted] operation")
	styles.Reset(false)
	styles.Update()
	assert.Equal(t, oldBackground, form.GetButton(1).GetBackgroundColor())
	errorModal := pages.GetPrimitive(dialogKey).(*ui.ModalForm)
	var errorForm *tview.Form
	errorModal.Focus(func(p tview.Primitive) { errorForm = p.(*tview.Form) })
	errorBackground := errorForm.GetButton(0).GetBackgroundColor()
	pages.ClearPageResources()
	assert.Nil(t, pages.GetPrimitive(dialogKey))
	require.NoError(t, styles.Load("../../../skins/high-contrast.yaml", false))
	styles.Update()
	assert.Equal(t, errorBackground, errorForm.GetButton(0).GetBackgroundColor(), "stopped pages release listeners")
}

func TestGenericDeleteDialogRetainsEditedChoicesOnSkinReload(t *testing.T) {
	styles := config.NewStyles()
	pages := ui.NewPages()
	dialogStyle := styles.Dialog()
	ShowDelete(&dialogStyle, pages, "Delete fixture?", func(*metav1.DeletionPropagation, bool) {}, func() {})
	modal := pages.GetPrimitive(dialogKey).(*ui.ModalForm)
	var form *tview.Form
	modal.Focus(func(p tview.Primitive) { form = p.(*tview.Form) })
	dropdown := form.GetFormItem(0).(*tview.DropDown)
	dropdown.SetCurrentOption(2)
	checkbox := form.GetFormItem(1).(*tview.Checkbox)
	checkbox.SetChecked(true)
	require.NoError(t, styles.Load("../../../skins/high-contrast.yaml", false))
	styles.Update()
	index, _ := dropdown.GetCurrentOption()
	assert.Equal(t, 2, index)
	assert.True(t, checkbox.IsChecked())
	dismiss(pages)
	background := form.GetButton(0).GetBackgroundColor()
	styles.Reset(false)
	styles.Update()
	assert.Equal(t, background, form.GetButton(0).GetBackgroundColor(), "dismissal releases live styling")
}
