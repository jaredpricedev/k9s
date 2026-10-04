// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui

import (
	"fmt"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// Task dimensions refer to the available view, excluding application chrome.
// A 40x16 terminal supplies the 40x12 task viewport in compact mode.
const MinTaskWidth, MinTaskHeight = 40, 12

// DrawTaskSizeNotice returns true when it replaces an unsupported layout.
// It does not mutate retained data, selection, query or input bindings.
func DrawTaskSizeNotice(screen tcell.Screen, box *tview.Box) bool {
	x, y, width, height := box.GetRect()
	if width >= MinTaskWidth && height >= MinTaskHeight {
		return false
	}
	box.Draw(screen)
	message := fmt.Sprintf("View too small: %dx%d\nNeed %dx%d; resize\nEsc back · Ctrl-C quit", width, height, MinTaskWidth, MinTaskHeight)
	for row, line := range tview.WordWrap(message, max(1, width-2)) {
		if row >= height-2 {
			break
		}
		tview.Print(screen, line, x+1, y+1+row, max(0, width-2), tview.AlignLeft, tcell.ColorDefault)
	}
	return true
}

// TaskTabs keeps the active label and a usable cycler visible at narrow widths.
func TaskTabs(labels []string, active, width int) string {
	var tabs []string
	for index, label := range labels {
		text := fmt.Sprintf("%d %s", index+1, label)
		if index == active {
			text = "[" + text + "]"
		}
		tabs = append(tabs, text)
	}
	line := strings.Join(tabs, "  ")
	if tview.TaggedStringWidth(line) <= width {
		return tview.Escape(line)
	}
	if active < 0 || active >= len(labels) {
		active = 0
	}
	line = fmt.Sprintf("[%d %s]  Tab next · 1-%d choose", active+1, labels[active], len(labels))
	if tview.TaggedStringWidth(line) > width {
		line = fmt.Sprintf("[%d %s]  Tab next", active+1, labels[active])
	}
	return tview.Escape(line)
}
