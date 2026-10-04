// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"strings"

	"github.com/derailed/tview"
)

// A tview tag always starts with '['. Ordinary resource values need no regex
// replacement or copied string; possible tags still use tview's exact escape.
func escapeTableField(text string) string {
	if !strings.ContainsRune(text, '[') {
		return text
	}
	return tview.Escape(text)
}

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
