// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const probeConfirmPage = "probe-confirm"

// probeConfirm asks before the first active request. Unlike the shared
// confirmation form, which shows three lines and scrolls the rest, it sizes
// itself to its text: what is about to be sent has to be readable in full
// before the button that sends it can be pressed. Cancel has the focus.
type probeConfirm struct {
	*tview.Box
	app     *App
	message string
	labels  [2]string
	focus   int
	offset  int
	done    func(send bool)
}

func newProbeConfirm(app *App, title, message, send string, done func(bool)) *probeConfirm {
	m := &probeConfirm{Box: tview.NewBox(), app: app, message: message, labels: [2]string{"Cancel", send}, done: done}
	m.SetBorder(true).SetTitle(" " + title + " ")
	return m
}

// IsDialog keeps application keys from acting underneath the question.
func (*probeConfirm) IsDialog() bool { return true }

func (m *probeConfirm) lines(width int) []string {
	var out []string
	for _, paragraph := range strings.Split(m.message, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		out = append(out, impactWrap(paragraph, width)...)
	}
	return out
}

// button renders one button. The focused one is marked with angle brackets
// as well as the focus colors.
func (m *probeConfirm) button(index int) (text string, cells int) {
	d := m.app.Styles.Dialog()
	label := "  " + m.labels[index] + "  "
	fg, bg := d.ButtonFgColor, d.ButtonBgColor
	if index == m.focus {
		label = "> " + m.labels[index] + " <"
		fg, bg = d.ButtonFocusFgColor, d.ButtonFocusBgColor
	}
	return "[" + graphHex(fg) + ":" + graphHex(bg) + ":b]" + tview.Escape(label) + "[-:-:-]", runewidth.StringWidth(label)
}

func (m *probeConfirm) Draw(screen tcell.Screen) {
	d, p := m.app.Styles.Dialog(), m.app.Styles.Semantic()
	columns, rows := screen.Size()
	width := max(min(96, columns-2), 20)
	inner := width - 4
	lines := m.lines(inner)
	height := max(min(rows-2, len(lines)+5), 6)
	m.SetRect(max(0, (columns-width)/2), max(0, (rows-height)/2), width, height)
	m.SetBackgroundColor(d.BgColor.Color())
	m.SetBorderColor(p.Focus.Color()).SetTitleColor(p.Focus.Color())
	m.Box.DrawForSubclass(screen, m)
	x, y, _, innerHeight := m.GetInnerRect()
	x++
	visible := max(innerHeight-3, 1)
	m.offset = max(min(m.offset, len(lines)-visible), 0)
	for row := 0; row < visible && m.offset+row < len(lines); row++ {
		tview.Print(screen, tview.Escape(lines[m.offset+row]), x, y+row, inner, tview.AlignLeft, d.FgColor.Color())
	}
	if len(lines) > visible {
		tview.Print(screen, "PgUp/PgDn shows the rest", x, y+innerHeight-3, inner, tview.AlignRight, p.Muted.Color())
	}
	cancel, cancelCells := m.button(0)
	send, sendCells := m.button(1)
	start := x + max((inner-cancelCells-3-sendCells)/2, 0)
	tview.Print(screen, cancel, start, y+innerHeight-2, cancelCells, tview.AlignLeft, d.ButtonFgColor.Color())
	tview.Print(screen, send, start+cancelCells+3, y+innerHeight-2, sendCells, tview.AlignLeft, d.ButtonFgColor.Color())
	tview.Print(screen, "Tab switches · Enter chooses · Esc cancels", x, y+innerHeight-1, inner, tview.AlignCenter, p.Muted.Color())
}

func (m *probeConfirm) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return m.WrapInputHandler(func(event *tcell.EventKey, _ func(tview.Primitive)) {
		switch event.Key() {
		case tcell.KeyEscape:
			m.done(false)
		case tcell.KeyEnter:
			m.done(m.focus == 1)
		case tcell.KeyTab, tcell.KeyBacktab, tcell.KeyLeft, tcell.KeyRight:
			m.focus ^= 1
		case tcell.KeyPgDn:
			m.offset += 3
		case tcell.KeyPgUp:
			m.offset = max(m.offset-3, 0)
		}
	})
}
