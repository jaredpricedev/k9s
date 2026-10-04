// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dialog

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
)

// ShowError pops an error dialog.
func ShowError(styles *config.Dialog, pages *ui.Pages, msg string) {
	f := tview.NewForm()
	f.SetItemPadding(0)
	f.SetButtonsAlign(tview.AlignCenter)
	f.AddButton("Dismiss", func() {
		dismiss(pages)
	})
	f.SetFocus(0)
	modal := tview.NewModalForm("<error>", f)
	StyleForm(styles, f)
	modal.SetBackgroundColor(styles.BgColor.Color())
	modal.SetText(cowTalk(tview.Escape(msg)))
	modal.SetTextColor(styles.FgColor.Color())
	modal.SetDoneFunc(func(int, string) {
		dismiss(pages)
	})
	pages.AddPage(dialogKey, modal, false, false)
	bindPageForm(styles, pages, dialogKey, f, modal)
	pages.ShowPage(dialogKey)
}

func cowTalk(says string) string {
	msg := fmt.Sprintf("< Ruroh? %s >", strings.TrimSuffix(says, "\n"))
	buff := make([]string, 0, len(cow)+3)
	buff = append(buff, msg)
	buff = append(buff, cow...)

	return strings.Join(buff, "\n")
}

var cow = []string{
	`\   ^__^            `,
	` \  (oo)\_______    `,
	`    (__)\       )\/\`,
	`        ||----w |   `,
	`        ||     ||   `,
}
