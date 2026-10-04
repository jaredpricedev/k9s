// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dialog

import (
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
)

// ShowMessage reveals complete literal information with keyboard scrolling.
func ShowMessage(styles *config.Styles, pages *ui.Pages, title, message string) {
	modal := ui.NewMessageModal(styles, title+" · PgUp/PgDn scroll", message, func() { dismiss(pages) })
	pages.AddPage(dialogKey, modal, false, false)
	pages.SetPageCleanup(dialogKey, modal.Cleanup)
	pages.ShowPage(dialogKey)
}
