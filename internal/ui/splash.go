// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tview"
)

// LogoWidth is the terminal cell width of the block wordmark.
const LogoWidth = 32

// LogoSmall draws [k9+] using terminal block characters, five rows high.
var LogoSmall = []string{
	`████ ██       ██████        ████`,
	`██   ██  ██   ██  ██   ██     ██`,
	`██   ████     ██████ ██████   ██`,
	`██   ██  ██       ██   ██     ██`,
	`████ ██    ██ ██████        ████`,
}

// LogoBig shares the wordmark so CLI, header and splash use the same identity.
var LogoBig = append([]string(nil), LogoSmall...)

// styledLogo accents the brackets and plus while keeping k9 in the body color.
func styledLogo(accent, foreground config.Color) string {
	var lines []string
	for _, row := range LogoSmall {
		cells := []rune(row)
		lines = append(lines, fmt.Sprintf("[%s::b]%s[%s::b]%s[%s::b]%s",
			accent, string(cells[:5]), foreground, string(cells[5:21]), accent, string(cells[21:])))
	}
	return strings.Join(lines, "\n")
}

// Splash represents a splash screen.
type Splash struct {
	*tview.Flex
}

// NewSplash instantiates a new splash screen with product and company info.
func NewSplash(styles *config.Styles, version string) *Splash {
	s := Splash{Flex: tview.NewFlex()}
	s.SetBackgroundColor(styles.BgColor())

	logo := tview.NewTextView()
	logo.SetDynamicColors(true)
	logo.SetTextAlign(tview.AlignCenter)
	s.layoutLogo(logo, styles)

	vers := tview.NewTextView()
	vers.SetDynamicColors(true)
	vers.SetTextAlign(tview.AlignCenter)
	s.layoutRev(vers, version, styles)

	s.SetDirection(tview.FlexRow)
	s.AddItem(logo, 10, 1, false)
	s.AddItem(vers, 1, 1, false)

	return &s
}

func (*Splash) layoutLogo(t *tview.TextView, styles *config.Styles) {
	_, _ = fmt.Fprintf(t, "\n\n%s\n", styledLogo(styles.Body().LogoColor, styles.Body().FgColor))
}

func (*Splash) layoutRev(t *tview.TextView, rev string, styles *config.Styles) {
	_, _ = fmt.Fprintf(t, "[%s::b]Revision [red::b]%s", styles.Body().FgColor, rev)
}
