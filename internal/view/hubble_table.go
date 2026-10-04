// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// Hubble uses the same selection surface as resource tables while retaining
// individual verdict colors. Only visible selection styling changes at paint.
type hubbleTable struct {
	*tview.Table
	palette func() config.SemanticPalette
}

type hubbleRowColors struct {
	canvas, text, category, healthy, failure, unknown tcell.Color
}

func (t *hubbleTable) Draw(screen tcell.Screen) {
	row, _ := t.GetSelection()
	if row < 1 || row >= t.GetRowCount() {
		t.Table.Draw(screen)
		return
	}
	// Keep native selection scrolling before disabling its color inversion.
	_, _, _, height := t.GetInnerRect()
	offRow, offCol := t.GetOffset()
	if row < offRow+1 {
		offRow = max(0, row-1)
	}
	if height > 1 && row >= offRow+height {
		offRow = max(0, row-height+1)
	}
	t.SetOffset(offRow, offCol)
	p := t.palette()
	t.SetSelectable(false, false)
	type savedCell struct {
		cell              *tview.TableCell
		text              string
		color, background tcell.Color
		attrs             tcell.AttrMask
	}
	var saved [8]savedCell // Current Hubble schemas have at most six columns.
	count := min(t.GetColumnCount(), len(saved))
	for col := range saved {
		if col >= count {
			break
		}
		cell := t.GetCell(row, col)
		saved[col] = savedCell{cell: cell, text: cell.Text, color: cell.Color, background: cell.BackgroundColor, attrs: cell.Attributes}
		cell.SetBackgroundColor(p.Selected.Color()).SetTextColor(config.ReadableForeground(cell.Color, p.Selected.Color())).SetAttributes(cell.Attributes | tcell.AttrBold)
		if col == 0 && len(cell.Text) >= 2 {
			cell.Text = "> " + cell.Text[2:]
		}
	}
	t.Table.Draw(screen)
	for _, old := range saved[:count] {
		old.cell.Text, old.cell.Color, old.cell.BackgroundColor, old.cell.Attributes = old.text, old.color, old.background, old.attrs
	}
	t.SetSelectable(true, false)
}

func (w *HubbleView) semanticPalette() config.SemanticPalette {
	return w.palette
}

func (w *HubbleView) StylesChanged(styles *config.Styles) {
	p := styles.Semantic()
	changed := w.palette != p
	w.palette = p
	canvas := p.Canvas.Color()
	readable := func(c config.Color) tcell.Color { return config.ReadableForeground(c.Color(), canvas) }
	w.rowColors = hubbleRowColors{
		canvas: canvas, text: readable(p.Text), category: readable(p.Category),
		healthy: readable(p.Healthy), failure: readable(p.Failure), unknown: readable(p.Unknown),
	}
	w.SetBackgroundColor(p.Canvas.Color()).SetBorderColor(p.Muted.Color())
	w.SetBorderFocusColor(p.Focus.Color()).SetTitleColor(w.rowColors.category)
	w.table.SetBackgroundColor(p.Canvas.Color())
	w.status.SetBackgroundColor(p.Panel.Color())
	w.status.SetTextColor(p.Text.Color())
	w.detail.SetBackgroundColor(p.Canvas.Color())
	w.detail.SetTextColor(p.Text.Color())
	w.prompt.SetFieldBackgroundColor(p.Panel.Color()).SetFieldTextColor(p.Text.Color()).SetLabelColor(p.Focus.Color())
	if changed {
		w.styleGeneration++
	}
	if w.started {
		w.render()
	}
}
