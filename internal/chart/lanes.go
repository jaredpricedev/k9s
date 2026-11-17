// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// Interval is a stretch of time. An End that is not after Start marks the
// one moment Start.
type Interval struct {
	Start, End time.Time
}

// Lane is one labelled row of a lanes strip: when something was the case.
// It states when, never why: two lanes that line up overlap in time.
type Lane struct {
	Label string
	// Marks are the stretches in which the lane's condition held.
	Marks []Interval
	// Unknown are the stretches without data. They are drawn with the gap
	// glyph and are never shown as quiet.
	Unknown []Interval
	// Glyph and ASCII mark a cell; zero means a full block and '#'.
	Glyph, ASCII rune
	// Color is the tview colour of the marks; empty adds no tag.
	Color string
	// Note is written to the right of the row, for example "3 of 60 min".
	Note string
}

// LanesOptions are the presentation choices of a lanes strip.
type LanesOptions struct {
	// Options carries ASCII, the cursor and the axis colours. Cursor is a
	// grid index: with Frame.Step equal to one column it is the column.
	Options
	// LabelWidth is the width of the label column in cells.
	LabelWidth int
	// NoteWidth is the width of the note column; zero draws no notes.
	NoteWidth int
	// QuietColor is the tview colour of quiet cells and gap glyphs.
	QuietColor string
}

// ColumnSpan is the time one plot column covers: [start, end).
func (f Frame) ColumnSpan(col int) (start, end time.Time) {
	if !f.valid() {
		return time.Time{}, time.Time{}
	}
	col = min(max(col, 0), f.Width-1)
	window := f.End.Sub(f.Start)
	return f.Start.Add(window * time.Duration(col) / time.Duration(f.Width)),
		f.Start.Add(window * time.Duration(col+1) / time.Duration(f.Width))
}

// Marked reports, per plot column, whether any interval touches the
// column. An interval is clipped to the window; a moment marks the column
// it falls in.
func (f Frame) Marked(intervals []Interval) []bool {
	if !f.valid() {
		return nil
	}
	out := make([]bool, f.Width)
	for _, iv := range intervals {
		if !iv.End.After(iv.Start) {
			if col, ok := f.Col(iv.Start); ok {
				out[col] = true
			}
			continue
		}
		if !iv.End.After(f.Start) || !iv.Start.Before(f.End) {
			continue
		}
		first, _ := f.Col(maxTime(iv.Start, f.Start))
		// The end is exclusive: an interval that ends on a column edge
		// does not mark the next column.
		last, _ := f.Col(minTime(iv.End, f.End).Add(-time.Nanosecond))
		for col := first; col <= last; col++ {
			out[col] = true
		}
	}
	return out
}

// MarkedCount is the number of plot columns the intervals touch.
func (f Frame) MarkedCount(intervals []Interval) int {
	n := 0
	for _, hit := range f.Marked(intervals) {
		if hit {
			n++
		}
	}
	return n
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Lanes draws labelled rows on one shared time axis: one line per lane,
// then the tick row and the UTC clock labels of Axis. A marked cell holds
// the lane's glyph, a quiet cell a dot, a cell without data the gap glyph.
// The cursor column is drawn through quiet cells and on the tick row.
//
//nolint:gocritic // Options are passed by value, as in Panel.Render and Axis.
func Lanes(lanes []Lane, f Frame, o LanesOptions) []string {
	if !f.valid() {
		return nil
	}
	bar, quiet, gap, cursor := '│', '·', GapGlyph, '┆'
	if o.ASCII {
		bar, quiet, gap, cursor = '|', '.', GapASCII, ':'
	}
	cursorCol := -1
	if o.Cursor >= 0 {
		if col, ok := f.Col(f.TimeAt(o.Cursor)); ok {
			cursorCol = col
		}
	}
	labelWidth := max(o.LabelWidth, 0)
	out := make([]string, 0, len(lanes)+2)
	for i := range lanes {
		lane := &lanes[i]
		glyph := lane.Glyph
		if o.ASCII {
			glyph = lane.ASCII
		}
		if glyph == 0 {
			glyph = '█'
			if o.ASCII {
				glyph = '#'
			}
		}
		marked, unknown := f.Marked(lane.Marks), f.Marked(lane.Unknown)
		label := runewidth.Truncate(lane.Label, labelWidth, "…")
		var b strings.Builder
		b.WriteString(label + strings.Repeat(" ", labelWidth-runewidth.StringWidth(label)))
		b.WriteString(paint(o.AxisColor, " "+string(bar)))
		run, runColor := "", ""
		flush := func() {
			b.WriteString(paint(runColor, run))
			run = ""
		}
		for col := range f.Width {
			cell, color := quiet, o.QuietColor
			switch {
			case unknown[col]:
				// No data wins over a mark: a lane cannot be known to
				// hold where it was not read.
				cell = gap
			case marked[col]:
				cell, color = glyph, lane.Color
			case col == cursorCol:
				cell, color = cursor, o.CursorColor
			}
			if color != runColor {
				flush()
				runColor = color
			}
			run += string(cell)
		}
		flush()
		if o.NoteWidth > 0 && lane.Note != "" {
			b.WriteString("  " + runewidth.Truncate(lane.Note, o.NoteWidth, "…"))
		}
		out = append(out, b.String())
	}
	return append(out, Axis(f, labelWidth, nil, o.Options)...)
}
