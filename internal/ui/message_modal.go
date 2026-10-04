// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"sync"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// MessageModal displays literal, scrollable information without interpreting
// context names or diagnostic reasons as terminal color markup.
type MessageModal struct {
	*tview.Flex
	text    *tview.TextView
	button  *tview.Button
	styles  *config.Styles
	cleanup sync.Once
}

func NewMessageModal(styles *config.Styles, title, message string, done func()) *MessageModal {
	m := &MessageModal{Flex: tview.NewFlex().SetDirection(tview.FlexRow), styles: styles}
	m.text = tview.NewTextView().SetText(message).SetDynamicColors(false).SetWrap(true).SetWordWrap(true).SetScrollable(true)
	m.button = tview.NewButton("Dismiss").SetSelectedFunc(done)
	m.SetBorder(true).SetTitle(title).SetBorderPadding(1, 1, 2, 2)
	m.AddItem(m.text, 0, 1, false).AddItem(m.button, 1, 0, true)
	m.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			done()
			return nil
		case tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyUp, tcell.KeyDown:
			m.text.InputHandler()(event, func(tview.Primitive) {})
			return nil
		}
		return event
	})
	m.StylesChanged(styles)
	styles.AddListener(m)
	return m
}

func (m *MessageModal) StylesChanged(styles *config.Styles) {
	d, palette := styles.Dialog(), styles.Semantic()
	m.SetBackgroundColor(d.BgColor.Color()).SetBorderColor(palette.Focus.Color()).SetTitleColor(palette.Focus.Color())
	m.text.SetBackgroundColor(d.BgColor.Color())
	m.text.SetTextColor(d.FgColor.Color())
	m.button.SetBackgroundColor(d.ButtonBgColor.Color())
	m.button.SetLabelColor(d.ButtonFgColor.Color())
	m.button.SetBackgroundColorActivated(d.ButtonFocusBgColor.Color())
	m.button.SetLabelColorActivated(d.ButtonFocusFgColor.Color())
}

func (m *MessageModal) Draw(screen tcell.Screen) {
	width, height := screen.Size()
	w, h := max(1, width-8), max(1, height-4)
	m.SetRect((width-w)/2, (height-h)/2, w, h)
	m.Flex.Draw(screen)
}

// Cleanup releases the skin listener when the transient page stops.
func (m *MessageModal) Cleanup() { m.cleanup.Do(func() { m.styles.RemoveListener(m) }) }
