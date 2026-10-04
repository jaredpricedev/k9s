// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dialog

import (
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
)

// ShowError pops an error dialog.
func ShowError(styles *config.Dialog, pages *ui.Pages, msg string) {
	ShowErrorRecovery(styles, pages, msg, ErrorRecovery{})
}

// ErrorRecovery lets a caller offer an explicit edit/help/retry action. The
// dialog never executes a suggestion, changes a query or retries on dismissal.
type ErrorRecovery struct {
	Title, Instruction, ActionLabel string
	Action                          func()
}

func ShowErrorRecovery(styles *config.Dialog, pages *ui.Pages, msg string, recovery ErrorRecovery) {
	if recovery.Title == "" {
		recovery.Title = "Error · retained view"
	}
	if recovery.Instruction == "" {
		recovery.Instruction = "Close to return to your view; revise input or retry explicitly.\n? shows actions and :diagnostics reviews available access."
	}
	f := tview.NewForm()
	f.SetItemPadding(0)
	f.SetButtonsAlign(tview.AlignCenter)
	f.AddButton("Dismiss", func() {
		dismiss(pages)
	})
	if recovery.Action != nil && recovery.ActionLabel != "" {
		f.AddButton(recovery.ActionLabel, func() { dismiss(pages); recovery.Action() })
	}
	f.SetFocus(0)
	modal := ui.NewModalForm(recovery.Title, f)
	StyleForm(styles, f)
	modal.SetBackgroundColor(styles.BgColor.Color())
	modal.SetText(tview.Escape(strings.TrimSpace(msg)) + "\n\n" + tview.Escape(recovery.Instruction))
	modal.SetTextColor(styles.FgColor.Color())
	modal.SetDoneFunc(func(int, string) {
		dismiss(pages)
	})
	pages.AddPage(dialogKey, modal, false, false)
	bindPageForm(styles, pages, dialogKey, f, modal)
	pages.ShowPage(dialogKey)
}
