// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const tableReadyColumn, tableRestartsColumn, tableWhyColumn = "READY", "RESTARTS", "WHY"

// tablePresentation resolves expensive RGB/contrast work once per skin change.
// Ordinary refreshes and draws only look up the already resolved colors.
type tablePresentation struct {
	canvas, selected, text, healthy, warning, failure, progress, unknown tcell.Color
	focus                                                                tcell.Color
	marker, deltaPrefix, trendPrefix                                     string
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
	v.trendPrefix = "[::]"
	if muted := config.ReadableForeground(p.Muted.Color(), v.canvas); muted.Hex() >= 0 {
		v.trendPrefix = fmt.Sprintf("[#%06x::]", muted.Hex())
	}
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
	t.columnWidths, t.hideWhy = nil, false
	_, _, width, _ := t.GetInnerRect()
	t.layoutWidth = width
	if width <= 0 || t.wide {
		return
	}
	if t.gvr.GVR().Resource != "pods" {
		// The cause column exists only while some listed row reports a cause.
		why := t.whyWidth(data)
		t.hideWhy = why == 0
		// A split keeps the state columns; a list without any (Jobs,
		// Services) keeps its natural columns and still shows the cause.
		if (width >= 96 || !t.fitStatusColumns(data, pads, width, why)) && why > 0 {
			t.fitWhyColumns(data, pads, width, why)
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
	place := func(names ...string) {
		for _, name := range names {
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
	}
	// The cause column appears only when a row reports one. It first claims
	// a readable minimum, then age and usage are placed, then it grows toward
	// its full text before lower-value columns are considered.
	why := min(available[tableWhyColumn]-2, 60)
	if why <= len(tableWhyColumn) || width < 96 {
		why = 0
	} else {
		least := min(why, 28)
		if short := least + 1 - remaining; short > 0 && cols["NAME"] > 24 {
			give := min(cols["NAME"]-24, short)
			cols["NAME"] -= give
			remaining += give
		}
		if remaining < least+1 {
			why = 0
		} else {
			reserve(tableWhyColumn, least)
		}
	}
	delete(available, tableWhyColumn)
	place("AGE", "CPU", "MEM")
	// Budgets above are deliberately generous (padding and separator are both
	// counted), so a fully reserved column draws two cells narrower than its
	// budget. That slack is real width and is handed back below.
	spare := remaining - 2
	for name, size := range cols {
		if name != "NAME" && name != tableWhyColumn {
			spare += max(0, size-available[name]+2)
		}
	}
	if grow := min(spare, why-cols[tableWhyColumn]); why > 0 && grow > 0 {
		cols[tableWhyColumn] += grow
		spare -= grow
		remaining -= min(remaining, grow)
	}
	before := remaining
	place("NODE", "PF", "IP", "%CPU/R", "%MEM/R", "%CPU/L", "%MEM/L", "VS")
	spare -= before - remaining
	// A name is never elided while the row still has blank cells.
	if grow := min(spare, available["NAME"]-cols["NAME"]); grow > 0 {
		cols["NAME"] += grow
	}
	t.columnWidths = cols
}

// fitStatusColumns keeps native/provider status facts readable in a split.
// Secondary columns are omitted only when their complete values cannot fit;
// source rows remain intact for Describe, YAML, filter and export.
func (t *Table) fitStatusColumns(data *model1.TableData, pads MaxyPad, width, why int) bool {
	available := make(map[string]int)
	for index, column := range data.Header() {
		if !t.shouldExcludeColumn(column) {
			available[column.Name] = max(1, pads[index]+2)
		}
	}
	if available["NAME"] == 0 {
		return false
	}
	critical := []string{"STATUS", "STATE", tableReadyColumn, "HEALTH", "HEALTHY", "PHASE", "VERDICT", "CONDITION"}
	hasStatus := false
	for _, name := range critical {
		hasStatus = hasStatus || available[name] > 0
	}
	if !hasStatus {
		return false
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
		return false
	}
	if available["NAMESPACE"] > 0 && width >= 60 && remaining > 22 {
		cols["NAMESPACE"] = min(12, available["NAMESPACE"])
		remaining -= cols["NAMESPACE"] + 1
	}
	cols["NAME"] = max(6, min(28, remaining-1))
	if why > 0 {
		// A name gives up width it does not use, then elides down to a
		// recognizable stem, so the cause is readable beside the state. The
		// cause is left out, never the state, when even that is too narrow.
		cols["NAME"] = max(6, min(cols["NAME"], available["NAME"]))
		least := min(why, minWhyWidth)
		room := remaining - cols["NAME"] - 2
		if short := least - room; short > 0 && cols["NAME"]-short >= 16 {
			cols["NAME"] -= short
			room += short
		}
		if room >= least {
			size := min(why, maxWhyWidth, room)
			// Age keeps its place when the cause stays readable without it.
			if age := available["AGE"]; age > 0 && size == room && size-age-1 >= least {
				size -= age + 1
			}
			cols[tableWhyColumn] = size
			remaining -= size + 1
		}
	}
	remaining -= cols["NAME"] + 1
	for _, name := range []string{tableRestartsColumn, "EXPIRES", "REVISION", "KIND", "SUSPEND", "AGE", "RENEWAL", "SOURCE", "ISSUER"} {
		size := available[name]
		if size > 0 && remaining >= size+1 {
			cols[name] = size
			remaining -= size + 1
		}
	}
	// Budgets count padding and separator, so each complete column draws two
	// cells narrower than reserved. The cause grows into that real width.
	if size := cols[tableWhyColumn]; size > 0 {
		spare := remaining - 1
		for name, reserved := range cols {
			switch text := available[name] - 2; {
			case name == tableWhyColumn:
			case name != "NAME" && name != "NAMESPACE":
				spare += 2
			case text > displayWidth(name)+2:
				// Identity columns are drawn unpadded, as wide as their text.
				spare += reserved - min(reserved, text-1)
			}
		}
		cols[tableWhyColumn] += max(0, min(spare, min(why, maxWhyWidth)-size))
	}
	t.columnWidths = cols
	return true
}

// A cause narrower than minWhyWidth is not shown; optional columns give way
// until it has easyWhyWidth; it never takes more than maxWhyWidth.
const minWhyWidth, easyWhyWidth, maxWhyWidth = 18, 24, 80

// whyWidth is the widest cause any listed row reports, in terminal cells, or
// zero when the list has no visible WHY column or no row reports a cause.
func (t *Table) whyWidth(data *model1.TableData) int {
	index, ok := data.Header().IndexOf(tableWhyColumn, true)
	if !ok || t.shouldExcludeColumn(data.Header()[index]) {
		return 0
	}
	widest := 0
	data.RowsRange(func(_ int, re model1.RowEvent) bool {
		if index < len(re.Row.Fields) {
			widest = max(widest, displayWidth(strings.TrimSpace(re.Row.Fields[index])))
		}
		return true
	})
	return widest
}

// fitWhyColumns budgets a full-width, non-Pod list that reports a cause. All
// columns keep their natural width and the cause takes what is left. When
// that is too little to read, a long name is elided first and then optional
// columns are omitted from the right; identity, state and age always stay.
// When even that leaves too little, nothing is omitted and the cause is left
// out.
// The omitted columns return as soon as no row reports a cause, and Ctrl-W
// shows every column in full at any time.
func (t *Table) fitWhyColumns(data *model1.TableData, pads MaxyPad, width, why int) {
	var order []string
	available := make(map[string]int)
	for i, h := range data.Header() {
		if !t.shouldExcludeColumn(h) {
			order = append(order, h.Name)
			available[h.Name] = max(1, pads[i]+2)
		}
	}
	if available["NAME"] == 0 {
		return
	}
	cols := make(map[string]int, len(order))
	for _, name := range order {
		if name != tableWhyColumn {
			cols[name] = available[name]
		}
	}
	nameFloor := 24
	if width < 96 {
		nameFloor = 16
		if cols["NAMESPACE"] > 14 {
			cols["NAMESPACE"] = 14
		}
	}
	// Two cells hold the selection marker. A column draws its text, at most
	// two cells less than its generous budget, and one separator. Identity
	// columns are drawn unpadded once a budget exists.
	room := func() int {
		left := width - 2 - 1
		for name, size := range cols {
			text := available[name] - 2
			if (name == "NAME" || name == "NAMESPACE") && text > displayWidth(name)+2 {
				text--
			}
			left -= min(size, text) + 1
		}
		return left
	}
	least := min(why, minWhyWidth)
	fitName := func(floor int) {
		cols["NAME"] = available["NAME"]
		if short := least - room(); short > 0 && available["NAME"]-2 > floor {
			cols["NAME"] = max(floor, available["NAME"]-2-short)
		}
	}
	fitName(nameFloor)
	// Optional columns give way from the right until the cause reads easily,
	// never a Job's completions: a row counted as failing with no visible
	// evidence is worse than a missing duration.
	easy := min(why, easyWhyWidth)
	for i := len(order) - 1; i >= 0 && room() < easy; i-- {
		name := order[i]
		if name == "NAME" || name == "NAMESPACE" || name == "AGE" || name == "COMPLETIONS" ||
			name == tableWhyColumn || statusColumn(name) {
			continue
		}
		delete(cols, name)
	}
	// Last, as in a split list with a state column, a narrow list gives up
	// the namespace; Ctrl-W and Describe still show it.
	if _, ok := cols["NAMESPACE"]; ok && width < 96 && room() < least {
		delete(cols, "NAMESPACE")
		// The name now leads the row and shares its cells with the marker.
		fitName(nameFloor + 2)
	}
	if room() < least {
		t.hideWhy = true
		return
	}
	cols[tableWhyColumn] = min(why, maxWhyWidth, room())
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

// statusLevel orders what a status cell reports, from neutral to failing.
type statusLevel int

const (
	levelText statusLevel = iota
	levelHealthy
	levelUnknown
	levelProgress
	levelWarning
	levelFailure
)

// columnOrderOnly turns off the default problems-first ordering. It is a
// session-wide choice that follows the operator into every resource list.
var columnOrderOnly atomic.Bool

func problemsFirstEnabled() bool { return !columnOrderOnly.Load() }

// SetProblemsFirst sets the session ordering without refreshing any table.
func SetProblemsFirst(enabled bool) { columnOrderOnly.Store(!enabled) }

// tableHealth counts the rows whose status needs attention. It is derived
// from the same classification that colors the cells, so the summary and the
// rows can never disagree.
type tableHealth struct {
	failing, degraded, progressing int
}

func (t *Table) statusColor(name, value string, fallback tcell.Color) tcell.Color {
	return t.levelColor(statusLevelOf(name, value, fallback))
}

// rowLevel is the state a row reports now. A status word (CrashLoopBackOff,
// ContainerCreating, Pending) is more specific than a readiness count, so the
// count decides only when no status column speaks. Restart counts are history
// and the validation message is free text; neither is counted.
func rowLevel(h model1.Header, re *model1.RowEvent, fallback tcell.Color) statusLevel {
	worst, ready := levelText, levelText
	for c, field := range re.Row.Fields {
		if c >= len(h) {
			break
		}
		name := h[c].Name
		if !statusColumn(name) || name == tableRestartsColumn || name == "VALID" {
			continue
		}
		level := statusLevelOf(name, field, fallback)
		if level < levelProgress {
			continue
		}
		if name == tableReadyColumn {
			ready = level
		} else if level > worst {
			worst = level
		}
	}
	// A renderer that read the object's conditions knows more than a count:
	// its verdict replaces the readiness count, so the summary agrees with
	// WHY. It never lowers what a visible status word already reports.
	if own := rendererLevel(re.Row.Level); own != levelText {
		return max(own, worst)
	}
	if worst == levelText {
		return ready
	}
	return worst
}

func rendererLevel(level model1.Level) statusLevel {
	switch level {
	case model1.LevelFailing:
		return levelFailure
	case model1.LevelDegraded:
		return levelWarning
	case model1.LevelProgress:
		return levelProgress
	}
	return levelText
}

// levelColor is the semantic color of a level.
func (t *Table) levelColor(level statusLevel) tcell.Color {
	p := &t.presentation
	switch level {
	case levelFailure:
		return p.failure
	case levelWarning:
		return p.warning
	case levelProgress:
		return p.progress
	case levelUnknown:
		return p.unknown
	case levelHealthy:
		return p.healthy
	}
	return p.text
}

// ToggleProblemsFirst lists failing, degraded and progressing rows ahead of
// the rest. The chosen column order is kept inside each group.
func (t *Table) ToggleProblemsFirst() bool {
	enabled := !problemsFirstEnabled()
	SetProblemsFirst(enabled)
	t.Refresh()
	return enabled
}

// healthTitle summarizes attention-worthy rows beside the row count. Each
// part is a count and a word, so it reads the same without color; parts are
// dropped from the least severe when the frame is too narrow.
func (t *Table) healthTitle(used int) string {
	p := &t.presentation
	var out string
	add := func(count int, word string, color tcell.Color) {
		if count == 0 {
			return
		}
		text := fmt.Sprintf("%d %s", count, word)
		if used+len(text)+3 > t.layoutWidth-4 && t.layoutWidth > 0 {
			return
		}
		used += len(text) + 3
		tag := "[::b]"
		if color.Hex() >= 0 {
			tag = fmt.Sprintf("[#%06x::b]", color.Hex())
		}
		out += tag + text + "[-::-] "
	}
	add(t.health.failing, "failing", p.failure)
	add(t.health.degraded, "degraded", p.warning)
	add(t.health.progressing, "in progress", p.progress)
	if problemsFirstEnabled() && out != "" {
		out += "· problems first "
	}
	return out
}

func statusLevelOf(name, value string, fallback tcell.Color) statusLevel {
	v := strings.ToLower(strings.TrimSpace(value))
	if name == tableReadyColumn {
		counts := strings.Split(v, "/")
		if len(counts) == 2 {
			ready, e1 := strconv.Atoi(counts[0])
			total, e2 := strconv.Atoi(counts[1])
			if e1 == nil && e2 == nil {
				if total == 0 {
					return levelUnknown
				}
				if ready < total {
					return levelWarning
				}
				return levelHealthy
			}
		}
	}
	if name == tableRestartsColumn {
		if count, err := strconv.Atoi(v); err == nil {
			if count > 0 {
				return levelWarning
			}
			return levelText
		}
	}
	if v == "running" && fallback != tcell.ColorDefault && fallback == model1.ErrColor {
		return levelWarning
	}
	switch {
	case strings.Contains(v, "fail"), strings.Contains(v, "error"), strings.Contains(v, "backoff"),
		strings.Contains(v, "crash"), strings.Contains(v, "oom"), strings.Contains(v, "evict"),
		v == "false", v == "dropped", v == "denied":
		return levelFailure
	case strings.Contains(v, "unknown"), v == "n/a", v == "-", v == "<none>", v == "":
		return levelUnknown
	case v == "pending", strings.Contains(v, "notready"), v == "noready":
		return levelWarning
	case strings.Contains(v, "creating"), strings.Contains(v, "initializ"), strings.Contains(v, "reconcil"),
		strings.Contains(v, "progress"), strings.Contains(v, "connecting"):
		return levelProgress
	case v == "running", v == "ready", v == "healthy", v == "true", v == "succeeded", v == "completed", v == "forwarded":
		return levelHealthy
	}
	// Legacy renderers still supply resource-specific status classification.
	if fallback != tcell.ColorDefault && fallback == model1.ErrColor {
		return levelFailure
	}
	if fallback != tcell.ColorDefault && fallback == model1.PendingColor {
		return levelWarning
	}
	return levelText
}
