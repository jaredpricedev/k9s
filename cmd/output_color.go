// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package cmd

import (
	"os"

	"github.com/mattn/go-isatty"
)

// Informational commands only color a terminal. NO_COLOR wins over skins;
// an empty value leaves automatic color detection enabled.
func outputColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}
