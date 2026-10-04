// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const tableReadyColumn, tableRestartsColumn = "READY", "RESTARTS"

// tablePresentation resolves expensive RGB/contrast work once per skin change.
// Ordinary refreshes and draws only look up the already resolved colors.
type tablePresentation struct {
	canvas, selected, text, healthy, warning, failure, progress, unknown tcell.Color
	focus                                                                tcell.Color
	marker, deltaPrefix                                                  string
	selectedColors                                                       map[tcell.Color]tcell.Color
}

func (t *Table) resolvePresentation(styles *config.Styles) {
	p := styles.Semantic()
	v := &t.presentation
	v.canvas, v.selected, v.focus = p.Canvas.Color(), p.Selected.Color(), p.Focus.Color()
	v.text = config.ReadableForeground(p.Text.Color(), v.canvas)
	v.healthy = config.ReadableForeground(p.Healthy.Color(), v.canvas)
	v.warning = config.ReadableForeground(p.Warning.Color(), v.canvas)
	v.failure = config.ReadableForeground(p.Failure.Color(), v.canvas)
	v.progress = config.ReadableForeground(p.Progress.Color(), v.canvas)
	v.unknown = config.ReadableForeground(p.Unknown.Color(), v.canvas)
	v.selectedColors = make(map[tcell.Color]tcell.Color, 6)
	for _, color := range []tcell.Color{v.text, v.healthy, v.warning, v.failure, v.progress, v.unknown} {
		v.selectedColors[color] = config.ReadableForeground(color, v.selected)
	}
	focus := config.ReadableForeground(v.focus, v.selected)
	focusName := p.Focus.String()
	if focus.Hex() >= 0 {
		focusName = fmt.Sprintf("#%06x", focus.Hex())
	}
	v.marker = fmt.Sprintf("[%s::b]> [-::]", focusName)
	v.deltaPrefix = fmt.Sprintf("[%s::b]", p.Progress.String())
}

func (t *Table) selectedForeground(color tcell.Color) tcell.Color {
	if resolved, ok := t.presentation.selectedColors[color]; ok {
		return resolved
	}
	resolved := config.ReadableForeground(color, t.presentation.selected)
	t.presentation.selectedColors[color] = resolved
	return resolved
}

// fitColumns reserves complete Pod diagnostics before optional columns. It
// changes only presentation; original fields and references stay in the model.
func (t *Table) fitColumns(data *model1.TableData, pads MaxyPad) {
	t.columnWidths = nil
	_, _, width, _ := t.GetInnerRect()
	t.layoutWidth = width
	if width <= 0 || t.wide {
		return
	}
	if t.gvr.GVR().Resource != "pods" {
		if width < 96 {
			t.fitStatusColumns(data, pads, width)
		}
		return
	}
	available := make(map[string]int)
	for i, h := range data.Header() {
		if !t.shouldExcludeColumn(h) {
			available[h.Name] = max(1, pads[i]+2)
		}
	}
	if available["NAME"] == 0 || available["STATUS"] == 0 {
		return
	}
	cols := make(map[string]int)
	remaining := width
	reserve := func(name string, size int) {
		if available[name] == 0 {
			return
		}
		cols[name] = size
		remaining -= size + 1
	}
	reserve("STATUS", available["STATUS"])
	reserve(tableReadyColumn, available[tableReadyColumn])
	reserve(tableRestartsColumn, available[tableRestartsColumn])
	// Namespace remains visible when viewing all namespaces. Names and scopes
	// elide before the state, with the full identity available in detail.
	if available["NAMESPACE"] > 0 {
		reserve("NAMESPACE", min(16, max(9, width/7)))
	}
	nameWidth := min(40, max(8, remaining-1))
	reserve("NAME", nameWidth)
	// At wider sizes shrink a very long name slightly to make room for useful
	// measurements. Do not add a column unless the complete label/value fits.
	for _, name := range []string{"CPU", "MEM", "AGE", "NODE", "PF", "IP", "%CPU/R", "%MEM/R", "%CPU/L", "%MEM/L", "VS"} {
		size := available[name]
		if size == 0 {
			continue
		}
		if name == "NODE" {
			size = min(size, 24)
		}
		if remaining < size+1 && width >= 96 && cols["NAME"] > 24 {
			give := min(cols["NAME"]-24, size+1-remaining)
			cols["NAME"] -= give
			remaining += give
		}
		if remaining >= size+1 {
			reserve(name, size)
		}
	}
	t.columnWidths = cols
}

// fitStatusColumns keeps native/provider status facts readable in a split.
// Secondary columns are omitted only when their complete values cannot fit;
// source rows remain intact for Describe, YAML, filter and export.
func (t *Table) fitStatusColumns(data *model1.TableData, pads MaxyPad, width int) {
	available := make(map[string]int)
	for index, column := range data.Header() {
		if !t.shouldExcludeColumn(column) {
			available[column.Name] = max(1, pads[index]+2)
		}
	}
	if available["NAME"] == 0 {
		return
	}
	critical := []string{"STATUS", "STATE", tableReadyColumn, "HEALTH", "HEALTHY", "PHASE", "VERDICT", "CONDITION"}
	hasStatus := false
	for _, name := range critical {
		hasStatus = hasStatus || available[name] > 0
	}
	if !hasStatus {
		return
	}
	cols := make(map[string]int)
	remaining := width
	for _, name := range critical {
		size := available[name]
		if size > 0 && size+10 <= remaining {
			cols[name] = size
			remaining -= size + 1
		}
	}
	if len(cols) == 0 {
		return
	}
	if available["NAMESPACE"] > 0 && width >= 60 && remaining > 22 {
		cols["NAMESPACE"] = min(12, available["NAMESPACE"])
		remaining -= cols["NAMESPACE"] + 1
	}
	cols["NAME"] = max(6, min(28, remaining-1))
	remaining -= cols["NAME"] + 1
	for _, name := range []string{tableRestartsColumn, "EXPIRES", "REVISION", "KIND", "SUSPEND", "AGE", "RENEWAL", "SOURCE", "ISSUER"} {
		size := available[name]
		if size > 0 && remaining >= size+1 {
			cols[name] = size
			remaining -= size + 1
		}
	}
	t.columnWidths = cols
}

// Draw recalculates the column budget after resize and renders selection as a
// surface plus an ASCII marker. Each selected cell retains its own severity.
func (t *Table) Draw(screen tcell.Screen) {
	selectRows, selectColumns := t.GetSelectable()
	_, _, width, _ := t.GetInnerRect()
	if t.styles != nil && width != t.layoutWidth && t.model != nil && t.model.Peek().HeaderCount() > 0 {
		t.Refresh()
		row, _ := t.GetOffset()
		t.SetOffset(row, 0)
	}
	if !t.semanticSelection || t.styles == nil || !selectRows {
		t.SelectTable.Table.Draw(screen)
		return
	}
	selected, _ := t.GetSelection()
	_, _, _, height := t.GetInnerRect()
	rowOffset, colOffset := t.GetOffset()
	if selected < rowOffset+1 {
		rowOffset = max(0, selected-1)
	}
	if selected >= rowOffset+height {
		rowOffset = max(0, selected-height+1)
	}
	t.SetOffset(rowOffset, colOffset)
	p := &t.presentation
	t.SetSelectable(false, false)
	// Prefix the first visible field only for drawing, so raw selected field
	// access and clipboard/export operations keep the original identity.
	type savedCell struct {
		cell              *tview.TableCell
		text              string
		color, background tcell.Color
		attrs             tcell.AttrMask
	}
	saved := make([]savedCell, 0, height+t.GetColumnCount())
	first, last := max(1, rowOffset+1), min(t.GetRowCount(), rowOffset+height)
	for row := first; row < last; row++ {
		columns := 1
		if row == selected {
			columns = t.GetColumnCount()
		}
		for col := range columns {
			cell := t.GetCell(row, col)
			if cell == nil {
				continue
			}
			saved = append(saved, savedCell{cell, cell.Text, cell.Color, cell.BackgroundColor, cell.Attributes})
			if col == 0 {
				marker := "  "
				if row == selected {
					marker = p.marker
				} else if id, ok := t.GetRowID(row); ok && t.IsMarked(id) {
					marker = "* "
				}
				cell.Text = marker + cell.Text
			}
			if row == selected {
				cell.SetBackgroundColor(p.selected)
				cell.SetTextColor(t.selectedForeground(cell.Color))
				cell.SetAttributes(cell.Attributes | tcell.AttrBold)
			}
		}
	}
	t.SelectTable.Table.Draw(screen)
	for _, old := range saved {
		old.cell.Text, old.cell.Color, old.cell.BackgroundColor, old.cell.Attributes = old.text, old.color, old.background, old.attrs
	}
	t.SetSelectable(selectRows, selectColumns)
}

func statusColumn(name string) bool {
	switch name {
	case "STATUS", "STATE", tableReadyColumn, "HEALTH", "HEALTHY", "PHASE", "VERDICT", "CONDITION", tableRestartsColumn, "VALID":
		return true
	default:
		return false
	}
}

func (t *Table) statusColor(name, value string, fallback tcell.Color) tcell.Color {
	p := &t.presentation
	v := strings.ToLower(strings.TrimSpace(value))
	if name == tableReadyColumn {
		counts := strings.Split(v, "/")
		if len(counts) == 2 {
			ready, e1 := strconv.Atoi(counts[0])
			total, e2 := strconv.Atoi(counts[1])
			if e1 == nil && e2 == nil {
				if total == 0 {
					return p.unknown
				}
				if ready < total {
					return p.warning
				}
				return p.healthy
			}
		}
	}
	if name == tableRestartsColumn {
		if count, err := strconv.Atoi(v); err == nil {
			if count > 0 {
				return p.warning
			}
			return p.text
		}
	}
	if v == "running" && fallback != tcell.ColorDefault && fallback == model1.ErrColor {
		return p.warning
	}
	switch {
	case strings.Contains(v, "fail"), strings.Contains(v, "error"), strings.Contains(v, "backoff"),
		strings.Contains(v, "crash"), strings.Contains(v, "oom"), strings.Contains(v, "evict"),
		v == "false", v == "dropped", v == "denied":
		return p.failure
	case strings.Contains(v, "unknown"), v == "n/a", v == "-", v == "<none>", v == "":
		return p.unknown
	case v == "pending", strings.Contains(v, "notready"), v == "noready":
		return p.warning
	case strings.Contains(v, "creating"), strings.Contains(v, "initializ"), strings.Contains(v, "reconcil"),
		strings.Contains(v, "progress"), strings.Contains(v, "connecting"):
		return p.progress
	case v == "running", v == "ready", v == "healthy", v == "true", v == "succeeded", v == "completed", v == "forwarded":
		return p.healthy
	}
	// Legacy renderers still supply resource-specific status classification.
	if fallback != tcell.ColorDefault && fallback == model1.ErrColor {
		return p.failure
	}
	if fallback != tcell.ColorDefault && fallback == model1.PendingColor {
		return p.warning
	}
	return p.text
}
