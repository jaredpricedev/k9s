// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// timelineColumns are widths in terminal cells; zero hides a column.
type timelineColumns struct {
	clock, age, relation, resource, what int
	compactRelation                      bool
}

const (
	timelineKindWidth = 7
	timelineMinWhat   = 30
)

// timelineLayout budgets one row. Time, kind and the text always remain; the
// age goes first, then the resource name, and the before/after column shrinks
// to a signed span before anything hides the text.
func timelineLayout(width, clock, maxResource int, hasMoment bool) timelineColumns {
	columns := timelineColumns{clock: clock}
	rest := width - (2 + clock + 1 + timelineKindWidth + 1)
	if hasMoment {
		columns.relation, columns.compactRelation = 11, false
		if width < 72 {
			columns.relation, columns.compactRelation = 6, true
		}
		rest -= columns.relation + 1
	}
	if width >= 100 {
		columns.age = 6
		rest -= columns.age + 1
	}
	if room := rest - timelineMinWhat - 1; room >= 12 && maxResource > 0 {
		columns.resource = min(maxResource, 28, room)
		rest -= columns.resource + 1
	}
	columns.what = max(rest, 1)
	return columns
}

// timelineCell pads or truncates to an exact cell width, eliding with "…".
func timelineCell(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.FillRight(runewidth.Truncate(text, width, "…"), width)
}

// timelineName keeps both ends of a long resource name: the kind prefix and
// the generated suffix distinguish it better than its middle.
func timelineName(text string, width int) string {
	if runewidth.StringWidth(text) <= width || width < 8 {
		return timelineCell(text, width)
	}
	tail := (width - 1) / 3
	runes := []rune(text)
	return timelineCell(runewidth.Truncate(text, width-1-tail, "")+"…"+string(runes[len(runes)-tail:]), width)
}

func (v *timelineView) timelineMarks() (moment, failing, change, rule string) {
	if v.app.Config.K9s.UI.NoIcons {
		return ">", "!", "*", "-"
	}
	return "▶", "!", "◆", "─"
}

func timelineCompactRelation(s *timeline.Snapshot, index int) string {
	relation, delta := s.Relation(index)
	switch relation {
	case timeline.RelationOnset:
		if s.Chosen {
			return "REF"
		}
		return "FAULT"
	case timeline.RelationBefore:
		return "-" + timeline.Coarse(delta)
	case timeline.RelationAfter:
		return "+" + timeline.Coarse(delta)
	}
	return ""
}

func (v *timelineView) renderRows() {
	if v.table == nil {
		return
	}
	v.table.Clear()
	v.rows = v.rows[:0]
	v.visibleCount = 0
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	message := func(text string) {
		v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(timelineCell(text, width))).SetSelectable(false).SetTextColor(p.Muted.Color()))
	}
	s := v.snapshot
	if s == nil {
		switch {
		case v.refreshFailure != "":
			message("Timeline unavailable: " + v.refreshFailure)
		default:
			message("Reading the timeline...")
		}
		return
	}
	visible := s.Visible(v.window, v.query)
	v.visibleCount = len(visible)
	clock, maxResource := 8, 0
	for _, index := range visible {
		entry := &s.Entries[index]
		clock = max(clock, runewidth.StringWidth(timeline.Clock(entry.At, s.CapturedAt)))
		maxResource = max(maxResource, runewidth.StringWidth(entry.Ref.Short()))
	}
	columns := timelineLayout(width, clock, maxResource, s.OnsetEntry() != nil)
	momentMark, failingMark, changeMark, rule := v.timelineMarks()

	heading := "  " + timelineCell("UTC TIME", columns.clock) + " " + timelineCell("KIND", timelineKindWidth) + " "
	if columns.relation > 0 {
		label := "VS FAULT"
		if s.Chosen {
			label = "VS REF"
		}
		if columns.compactRelation {
			label = "+/-"
		}
		heading += timelineCell(label, columns.relation) + " "
	}
	if columns.age > 0 {
		heading += timelineCell("AGE", columns.age) + " "
	}
	if columns.resource > 0 {
		heading += timelineCell("RESOURCE", columns.resource) + " "
	}
	heading += timelineCell("WHAT", columns.what)
	v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(heading)).SetSelectable(false).SetTextColor(p.Focus.Color()).SetAttributes(tcell.AttrBold))

	if len(visible) == 0 {
		text := "No timed entries were read."
		switch {
		case v.query != "":
			text = fmt.Sprintf("No entry matches the filter in window %s. Esc clears the filter.", v.windowLabel())
		case v.window != 0 && len(s.Entries) > 0:
			text = fmt.Sprintf("Nothing began in the %s. 0 shows all %d entries read.", v.windowLabel(), len(s.Entries))
		}
		v.table.SetCell(1, 0, tview.NewTableCell(tview.Escape(timelineCell(text, width))).SetSelectable(false).SetTextColor(p.Muted.Color()))
		v.focused = -1
		return
	}

	selectedRow, firstRow, ruled := 0, 0, false
	for _, index := range visible {
		entry := &s.Entries[index]
		relation, _ := s.Relation(index)
		if relation == timeline.RelationBefore && !ruled {
			ruled = true
			label := " below: preceded " + s.Moment() + "; order, not cause "
			if runewidth.StringWidth(label)+4 > width {
				label = " below: preceded it; not cause "
			}
			fill := max(width-runewidth.StringWidth(label), 2)
			text := strings.Repeat(rule, fill/2) + label + strings.Repeat(rule, fill-fill/2)
			v.rows = append(v.rows, -1)
			v.table.SetCell(len(v.rows), 0, tview.NewTableCell(tview.Escape(timelineCell(text, width))).SetSelectable(false).SetTextColor(p.Muted.Color()))
		}
		v.rows = append(v.rows, index)
		row := len(v.rows)
		if firstRow == 0 {
			firstRow = row
		}
		if index == v.focused {
			selectedRow = row
		}
		mark, color, bold := " ", p.Text, false
		switch {
		case relation == timeline.RelationOnset:
			mark, color, bold = momentMark, p.Failure, true
			if s.Chosen {
				color = p.Focus
			}
		case entry.Failing:
			mark, color = failingMark, p.Failure
		case entry.Change:
			mark, color = changeMark, p.Progress
			if relation == timeline.RelationBefore {
				color, bold = p.Warning, true
			}
		}
		var line strings.Builder
		line.WriteString(timelineTag(color, bold))
		line.WriteString(tview.Escape(mark + " " + timelineCell(timeline.Clock(entry.At, s.CapturedAt), columns.clock) + " " +
			timelineCell(entry.Source.Tag(), timelineKindWidth) + " "))
		if columns.relation > 0 {
			label := s.RelationLabel(index)
			if columns.compactRelation {
				label = timelineCompactRelation(s, index)
			}
			line.WriteString(tview.Escape(timelineCell(label, columns.relation) + " "))
		}
		if !bold {
			line.WriteString(timelineTag(p.Text, false))
		}
		if columns.age > 0 {
			line.WriteString(tview.Escape(timelineCell(timeline.Span(s.CapturedAt.Sub(entry.At)), columns.age) + " "))
		}
		if columns.resource > 0 {
			line.WriteString(tview.Escape(timelineName(entry.Ref.Short(), columns.resource) + " "))
		}
		line.WriteString(tview.Escape(timelineCell(entry.Summary, columns.what)))
		line.WriteString("[-:-:-]")
		v.table.SetCell(row, 0, tview.NewTableCell(line.String()).SetExpansion(1))
	}
	if selectedRow == 0 {
		selectedRow = firstRow
		v.focused = v.rows[firstRow-1]
	}
	v.table.Select(selectedRow, 0)
	// A table that once showed every row keeps following its end; restating
	// the offset lets the selection decide the scroll position after a resize.
	v.table.SetOffset(v.table.GetOffset())
}
