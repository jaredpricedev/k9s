// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import "github.com/derailed/tview"

// displayWidth avoids grapheme and markup parsing for ordinary table values.
// Controls, Unicode and potential tags retain tview's exact width semantics.
func displayWidth(text string) int {
	for i := range len(text) {
		if text[i] < ' ' || text[i] > '~' || text[i] == '[' {
			return tview.TaggedStringWidth(text)
		}
	}
	return len(text)
}
