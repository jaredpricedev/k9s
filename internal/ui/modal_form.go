// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// ModalForm retains native Form input and callbacks, but owns responsive
// geometry. Narrow fields stack under their labels and scroll with focus.
type ModalForm struct {
	*tview.Box
	form          *tview.Form
	text          string
	contextText   string
	colors        config.Dialog
	done          func(int, string)
	textOffset    int
	tooSmall      bool
	boundControls map[tview.Primitive]bool
}

func NewModalForm(title string, form *tview.Form) *ModalForm {
	m := &ModalForm{Box: tview.NewBox(), form: form, colors: config.NewStyles().Dialog()}
	m.SetBorder(true).SetTitle(title)
	for index := range form.GetFormItemCount() {
		m.bindDetailsScroll(form.GetFormItem(index))
	}
	for index := range form.GetButtonCount() {
		m.bindDetailsScroll(form.GetButton(index))
	}
	return m
}

func (m *ModalForm) bindDetailsScroll(primitive tview.Primitive) {
	if m.boundControls == nil {
		m.boundControls = make(map[tview.Primitive]bool)
	}
	if m.boundControls[primitive] {
		return
	}
	m.boundControls[primitive] = true
	if box, ok := primitive.(interface {
		GetInputCapture() func(*tcell.EventKey) *tcell.EventKey
		SetInputCapture(func(*tcell.EventKey) *tcell.EventKey) *tview.Box
	}); ok {
		previous := box.GetInputCapture()
		box.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if m.tooSmall && event.Key() != tcell.KeyEscape && event.Key() != tcell.KeyCtrlC {
				return nil
			}
			switch event.Key() {
			case tcell.KeyPgUp:
				m.textOffset = max(0, m.textOffset-3)
				return nil
			case tcell.KeyPgDn:
				m.textOffset += 3
				return nil
			}
			if previous != nil {
				return previous(event)
			}
			return event
		})
	}
}

func (m *ModalForm) SetText(text string) *ModalForm {
	if m.text != "" && strings.HasPrefix(text, m.text) {
		m.textOffset = 1 << 20
	}
	m.text = text
	return m
}

// SetContext pins the captured target and destination above scrollable guidance.
// Callers retain the full destination in their confirmation model.
func (m *ModalForm) SetContext(text string) *ModalForm {
	m.contextText = tview.Escape(logstream.SafeText(text))
	return m
}
func modalColor(color tcell.Color) config.Color {
	if color == tcell.ColorDefault {
		return config.DefaultColor
	}
	return config.NewColor(fmt.Sprintf("#%06x", color.Hex()))
}
func (m *ModalForm) SetTextColor(color tcell.Color) *ModalForm {
	m.colors.FgColor = modalColor(color)
	return m
}
func (m *ModalForm) SetBackgroundColor(color tcell.Color) *ModalForm {
	m.Box.SetBackgroundColor(color)
	m.form.SetBackgroundColor(color)
	m.colors.BgColor = modalColor(color)
	return m
}
func (m *ModalForm) SetDoneFunc(done func(int, string)) *ModalForm {
	m.done = done
	m.form.SetCancelFunc(func() {
		if done != nil {
			done(-1, "")
		}
	})
	return m
}
func (m *ModalForm) SetDialogColors(colors *config.Dialog) {
	m.colors = *colors
	m.Box.SetBackgroundColor(colors.BgColor.Color())
}
func (m *ModalForm) Focus(delegate func(tview.Primitive)) { delegate(m.form) }
func (m *ModalForm) HasFocus() bool                       { return m.form.HasFocus() }
func (m *ModalForm) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return m.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, focus func(tview.Primitive)) (bool, tview.Primitive) {
		if m.tooSmall {
			return false, nil
		}
		return m.form.MouseHandler()(action, event, focus)
	})
}
func (m *ModalForm) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return m.WrapInputHandler(func(event *tcell.EventKey, focus func(tview.Primitive)) {
		switch event.Key() {
		case tcell.KeyPgUp:
			m.textOffset = max(0, m.textOffset-3)
			return
		case tcell.KeyPgDn:
			m.textOffset += 3
			return
		}
		m.form.InputHandler()(event, focus)
	})
}

func (m *ModalForm) Draw(screen tcell.Screen) {
	for index := range m.form.GetFormItemCount() {
		m.bindDetailsScroll(m.form.GetFormItem(index))
	}
	for index := range m.form.GetButtonCount() {
		m.bindDetailsScroll(m.form.GetButton(index))
	}
	columns, rows := screen.Size()
	width, height := min(96, columns-2), min(rows-2, max(12, m.form.GetFormItemCount()*2+10))
	m.SetRect(max(0, (columns-width)/2), max(0, (rows-height)/2), max(1, width), max(1, height))
	m.tooSmall = columns < MinTaskWidth || rows < 16
	if m.tooSmall {
		m.drawSizeNotice(screen, columns, rows)
		return
	}
	m.Box.DrawForSubclass(screen, m)
	x, y, available, innerHeight := m.GetInnerRect()
	x++
	available -= 2
	contextLines := strings.Split(m.contextText, "\n")
	for _, line := range contextLines[:min(2, len(contextLines))] {
		if m.contextText == "" {
			break
		}
		tview.Print(screen, line, x, y, available, tview.AlignLeft, m.colors.FgColor.Color())
		y++
		innerHeight--
	}
	y, infoScrollable := m.drawGuidance(screen, x, y, available, innerHeight)
	bottom := (rows-height)/2 + height - 2
	buttonY := m.drawButtons(screen, x, bottom, available, infoScrollable)
	fieldHeight := max(1, buttonY-y-1)
	m.form.SetRect(x, y, available, buttonY-y+1)
	m.drawFields(screen, x, y, available, fieldHeight, columns < 80)
}

func (m *ModalForm) drawSizeNotice(screen tcell.Screen, columns, rows int) {
	m.Box.DrawForSubclass(screen, m)
	x, y, width, height := m.GetInnerRect()
	message := fmt.Sprintf("Terminal too small\nNeed 40x16; current %dx%d\nResize or Esc cancel", columns, rows)
	for index, line := range tview.WordWrap(message, max(1, width)) {
		if index >= height {
			break
		}
		tview.Print(screen, line, x, y+index, width, tview.AlignLeft, m.colors.FgColor.Color())
	}
}

func (m *ModalForm) drawGuidance(screen tcell.Screen, x, y, width, height int) (int, bool) {
	lines := tview.WordWrap(m.text, max(1, width))
	// Reserve controls before guidance; PgUp/PgDn reaches the complete text.
	messageRows := min(len(lines), max(0, min(3, height-5)))
	m.textOffset = min(m.textOffset, max(0, len(lines)-messageRows))
	for row := range messageRows {
		tview.Print(screen, lines[m.textOffset+row], x, y+row, width, tview.AlignLeft, m.colors.FgColor.Color())
	}
	y += messageRows
	if messageRows > 0 {
		y++
	}
	return y, len(lines) > messageRows
}

func (m *ModalForm) drawButtons(screen tcell.Screen, x, bottom, width int, infoScrollable bool) int {
	hint := "Tab next · Shift-Tab previous · Esc cancel"
	if width < 48 {
		hint = "Tab next · Esc cancel"
	}
	if infoScrollable {
		hint = "PgUp/PgDn info · Tab · Esc cancel"
	}
	tview.Print(screen, hint, x, bottom, width, tview.AlignLeft, m.colors.FgColor.Color())
	buttonY := bottom - 1
	buttonsWidth := 0
	for index := range m.form.GetButtonCount() {
		buttonsWidth += tview.TaggedStringWidth(m.form.GetButton(index).GetLabel()) + 5
	}
	stacked := buttonsWidth > width
	if stacked {
		buttonY = bottom - m.form.GetButtonCount()
	}
	buttonX := x + max(0, (width-buttonsWidth)/2)
	for index := range m.form.GetButtonCount() {
		button := m.form.GetButton(index)
		buttonWidth := min(width, tview.TaggedStringWidth(button.GetLabel())+4)
		row := buttonY
		if stacked {
			buttonX = x + max(0, (width-buttonWidth)/2)
			row += index
		}
		button.SetRect(buttonX, row, buttonWidth, 1)
		button.Draw(screen)
		buttonX += buttonWidth + 1
	}
	return buttonY
}

func (m *ModalForm) drawFields(screen tcell.Screen, x, y, available, fieldHeight int, stacked bool) {
	step := 1
	labelWidth := 0
	var focusedDraw func()
	for index := range m.form.GetFormItemCount() {
		labelWidth = max(labelWidth, tview.TaggedStringWidth(m.form.GetFormItem(index).GetLabel())+1)
	}
	if stacked {
		step = 2
		labelWidth = 0
	}
	focused, _ := m.form.GetFocusedItemIndex()
	offset := 0
	if focused >= 0 {
		offset = max(0, (focused+1)*step-fieldHeight)
	}
	for index := range m.form.GetFormItemCount() {
		item := m.form.GetFormItem(index)
		row := y + index*step - offset
		if stacked && row >= y && row < y+fieldHeight {
			tview.Print(screen, tview.Escape(item.GetLabel()), x, row, available, tview.AlignLeft, m.colors.LabelFgColor.Color())
		}
		if stacked {
			row++
		}
		if row < y || row >= y+fieldHeight {
			item.SetRect(0, 0, 0, 0)
			continue
		}
		item.SetFormAttributes(labelWidth, m.colors.LabelFgColor.Color(), m.colors.BgColor.Color(), m.colors.FieldFgColor.Color(), m.colors.BgColor.Color())
		item.SetRect(x, row, available, 1)
		draw := func() { drawModalField(screen, item, stacked) }
		if item.HasFocus() {
			focusedDraw = draw
		} else {
			draw()
		}
	}
	if focusedDraw != nil {
		focusedDraw()
	}
}

func drawModalField(screen tcell.Screen, item tview.FormItem, stacked bool) {
	restore := func() {}
	if stacked {
		label := item.GetLabel()
		switch field := item.(type) {
		case *tview.InputField:
			field.SetLabel("")
			restore = func() { field.SetLabel(label) }
		case *tview.DropDown:
			field.SetLabel("")
			restore = func() { field.SetLabel(label) }
		case *tview.Checkbox:
			field.SetLabel("")
			restore = func() { field.SetLabel(label) }
		}
	}
	item.Draw(screen)
	restore()
}
