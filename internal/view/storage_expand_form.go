// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"

	"github.com/derailed/k9s/internal/storage"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
)

const storageExpansionPage = "storage-expansion-preview"

type storageExpansionModal struct{ *ui.ModalForm }

func (*storageExpansionModal) IsDialog() bool { return true }
func (v *storageView) closeExpansionForm() {
	v.formOpen = false
	v.modal = nil
	v.app.Content.Pages.RemovePage(storageExpansionPage)
	if v.active && v.app.Content.Top() == v {
		v.app.SetFocus(v.text)
	}
}
func (v *storageView) expansionForm() {
	if !v.active || !v.destinationCurrent() || v.snapshot == nil {
		v.app.Flash().Warn("Wait for a current captured storage snapshot before expansion preview")
		return
	}
	if len(v.snapshot.PVCs) == 0 {
		v.app.Flash().Warn("No PVC is retained in this storage scope; inspect source coverage")
		return
	}
	snapshot, generation := v.snapshot, v.generation
	options := make([]string, len(snapshot.PVCs))
	for i := range snapshot.PVCs {
		p := &snapshot.PVCs[i]
		options[i] = p.Identity.Namespace + "/" + p.Identity.Name
	}
	form := v.expansionFields()
	form.AddDropDown("PVC", options, 0, nil).AddInputField("New requested storage", "", 24, nil, nil)
	form.AddButton("Cancel", v.closeExpansionForm).AddButton("Preview", func() {
		if !v.expansionCurrent(snapshot, generation) {
			v.app.Flash().Warn("Storage snapshot/destination changed; reopen expansion preview")
			return
		}
		index, _ := form.GetFormItem(0).(*tview.DropDown).GetCurrentOption()
		if index < 0 || index >= len(snapshot.PVCs) {
			v.app.Flash().Warn("Select a retained PVC")
			return
		}
		pvc := &snapshot.PVCs[index]
		quantity := form.GetFormItem(1).(*tview.InputField).GetText()
		plan, err := snapshot.Expansion(pvc.Identity.Namespace, pvc.Identity.Name, quantity)
		if err != nil {
			v.modal.SetText(tview.Escape(err.Error() + "\n\n" + v.expansionGuidance()))
			return
		}
		v.confirmExpansion(snapshot, generation, plan)
	})
	v.showExpansionForm("PVC expansion preview", v.target.Context+" / "+v.target.Path(), v.expansionGuidance(), form)
}
func (v *storageView) expansionCurrent(snapshot *storage.Snapshot, generation uint64) bool {
	return v.active && v.formOpen && v.app.Content.Top() == v && v.destinationCurrent() && v.snapshot == snapshot && v.generation == generation
}
func (*storageView) expansionGuidance() string {
	return "Enter the new requested size, such as 20Gi. It must exceed the current spec request. Preview checks captured " +
		"PVC/PV binding and explicit StorageClass expansion permission. No write occurs yet. Usage/free space is " +
		"unavailable. Read-only mode permits preview and blocks submission."
}
func (v *storageView) confirmExpansion(snapshot *storage.Snapshot, generation uint64, plan *storage.ExpansionPlan) {
	form := v.expansionFields()
	form.AddButton("Cancel", v.closeExpansionForm).AddButton("Submit request", func() {
		if !v.expansionCurrent(snapshot, generation) {
			v.app.Flash().Warn("Storage snapshot/destination changed; reopen expansion preview")
			return
		}
		if v.app.Config.IsReadOnly() {
			v.modal.SetText(tview.Escape("Read-only mode blocks submission.\n\n" + plan.Preview()))
			return
		}
		if v.expandSubmit == nil {
			v.modal.SetText("Guarded operations unavailable; reopen the storage review")
			return
		}
		v.closeExpansionForm()
		v.expandSubmit(plan)
	})
	v.showExpansionForm("Confirm PVC expansion", fmt.Sprintf("Context %s\nPVC %s/%s", plan.PVC.Context, plan.PVC.Namespace, plan.PVC.Name), plan.Preview(), form)
}
func (v *storageView) expansionFields() *tview.Form {
	colors := v.app.Styles.Dialog()
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	form.SetBackgroundColor(colors.BgColor.Color())
	form.SetButtonBackgroundColor(colors.ButtonBgColor.Color()).SetButtonTextColor(colors.ButtonFgColor.Color()).
		SetLabelColor(colors.LabelFgColor.Color()).SetFieldTextColor(colors.FieldFgColor.Color()).SetFieldBackgroundColor(colors.BgColor.Color())
	form.SetCancelFunc(v.closeExpansionForm)
	return form
}
func (v *storageView) showExpansionForm(title, contextText, message string, form *tview.Form) {
	host := ui.NewModalForm(title, form).SetContext(contextText).SetText(tview.Escape(message))
	colors := v.app.Styles.Dialog()
	host.SetDialogColors(&colors)
	host.SetDoneFunc(func(_ int, _ string) { v.closeExpansionForm() })
	v.modal = &storageExpansionModal{ModalForm: host}
	v.formOpen = true
	v.app.Content.Pages.AddPage(storageExpansionPage, v.modal, false, true)
	v.app.SetFocus(v.modal)
}
