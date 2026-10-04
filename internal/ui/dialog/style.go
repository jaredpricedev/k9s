// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dialog

import (
	"sync"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
)

// StyleForm applies both ordinary and focused button colors after buttons have
// been added. It preserves the current form focus and edited field values.
func StyleForm(styles *config.Dialog, form *tview.Form) {
	form.SetBackgroundColor(styles.BgColor.Color())
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).
		SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).
		SetFieldTextColor(styles.FieldFgColor.Color()).
		SetFieldBackgroundColor(styles.BgColor.Color())
	for index := range form.GetButtonCount() {
		button := form.GetButton(index)
		button.SetBackgroundColor(styles.ButtonBgColor.Color())
		button.SetLabelColor(styles.ButtonFgColor.Color())
		button.SetBackgroundColorActivated(styles.ButtonFocusBgColor.Color())
		button.SetLabelColorActivated(styles.ButtonFocusFgColor.Color())
	}
	for index := range form.GetFormItemCount() {
		if dropdown, ok := form.GetFormItem(index).(*tview.DropDown); ok {
			dropdown.SetListStyles(styles.FgColor.Color(), styles.BgColor.Color(),
				styles.ButtonFocusFgColor.Color(), styles.ButtonFocusBgColor.Color())
		}
	}
}

type formStyleListener struct {
	form  *tview.Form
	modal *tview.ModalForm
}

type listStyleListener struct {
	list  *tview.List
	modal *ui.ModalList
}

func (l *listStyleListener) StylesChanged(styles *config.Styles) {
	d := styles.Dialog()
	l.list.SetMainTextColor(d.FgColor.Color())
	l.list.SetSelectedTextColor(d.ButtonFocusFgColor.Color())
	l.list.SetSelectedBackgroundColor(d.ButtonFocusBgColor.Color())
	l.modal.SetDialogColors(d.BgColor.Color(), styles.Semantic().Focus.Color())
}

func bindPageList(styles *config.Dialog, pages *ui.Pages, key string, list *tview.List, modal *ui.ModalList) {
	list.SetMainTextColor(styles.FgColor.Color())
	modal.SetDialogColors(styles.BgColor.Color(), styles.LabelFgColor.Color())
	if source := styles.StyleSource(); source != nil {
		listener := &listStyleListener{list, modal}
		listener.StylesChanged(source)
		source.AddListener(listener)
		var cleanup sync.Once
		pages.SetPageCleanup(key, func() { cleanup.Do(func() { source.RemoveListener(listener) }) })
	}
}

func (l *formStyleListener) StylesChanged(styles *config.Styles) {
	d := styles.Dialog()
	StyleForm(&d, l.form)
	if l.modal != nil {
		l.modal.SetBackgroundColor(d.BgColor.Color())
		l.modal.SetTextColor(d.FgColor.Color())
	}
}

// BindFormStyles follows runtime skins until the returned idempotent cleanup is
// called. Call cleanup on dismissal and when the owning view stops.
func BindFormStyles(styles *config.Styles, form *tview.Form, modal *tview.ModalForm) func() {
	listener := &formStyleListener{form, modal}
	listener.StylesChanged(styles)
	styles.AddListener(listener)
	var cleanup sync.Once
	return func() {
		cleanup.Do(func() {
			styles.RemoveListener(listener)
		})
	}
}

// bindPageForm preserves the native ModalForm type and button contracts while
// following live skins until dismissal, replacement, or the owning view stops.
func bindPageForm(styles *config.Dialog, pages *ui.Pages, key string, form *tview.Form, modal *tview.ModalForm) {
	StyleForm(styles, form)
	modal.SetBackgroundColor(styles.BgColor.Color())
	modal.SetTextColor(styles.FgColor.Color())
	if source := styles.StyleSource(); source != nil {
		pages.SetPageCleanup(key, BindFormStyles(source, form, modal))
	}
}
