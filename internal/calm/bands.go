// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"time"

	"github.com/derailed/k9s/internal/chart"
)

// Bands is what runs down through every row of a page: the episodes, the
// labelled moments and the time cursor. An episode says when something was
// the case, never why: what lines up with it overlaps it in time.
type Bands struct {
	// Label names the episodes on the marker row: "stalls".
	Label string
	// Episodes are shaded behind every row and marked on the marker row
	// and on the axis. They are stated as slowpath states them: a sample
	// that crossed its rule stands for the step that ends at it.
	Episodes []chart.Interval
	// Marks are labelled moments, such as a rollout: a guide through the
	// rows, the key on the axis and one legend line each.
	Marks []chart.Mark
	// Cursor is the time of the sample under the cursor; the zero time
	// draws none. It is drawn where that sample is: over the step that
	// ends at it.
	Cursor time.Time
}

// Any reports whether there is an episode to mark.
func (b Bands) Any() bool { return len(b.Episodes) > 0 }

// Columns reports, per column of the frame, whether an episode touches it.
func (b Bands) Columns(f chart.Frame) []bool { return f.Marked(b.Episodes) }

// MarkerRow is the row above a block that marks the episodes: the label at
// the left of the strip column and one marker per column an episode
// touches. With the axis it carries the bands where there is no colour.
func MarkerRow(b Bands, f chart.Frame, l Layout, o Options) string {
	if f.Width < 1 || !f.End.After(f.Start) {
		return ""
	}
	marker := bandGlyph
	if o.ASCII {
		marker = bandASCII
	}
	row := make([]cell, f.Width)
	for col, hit := range b.Columns(f) {
		row[col] = cell{r: ' '}
		if hit {
			row[col] = cell{r: marker, k: look{color: o.Palette.Warn, bold: true}}
		}
	}
	out := newLine(o)
	label := o.clip(o.words(b.Label), l.StripCol-Indent-1)
	out.to(l.StripCol - 1 - cells(label))
	out.put(o.muted(), label)
	out.to(l.StripCol)
	out.strip(row)
	out.row, _ = b.shade(out.row, f, l, o)
	return out.String()
}

// AxisLines is the shared time axis under a block, as two lines: the axis
// with its ticks, and the UTC clock labels under the ticks with "UTC" after
// the last one where there is room. An episode is drawn heavy on the axis,
// a labelled moment is its key, the cursor is a caret.
func AxisLines(b Bands, f chart.Frame, l Layout, o Options) []string {
	if f.Width < 1 || !f.End.After(f.Start) {
		return nil
	}
	rule, tick, heavy, heavyTick := '─', '┬', '━', '┯'
	if o.ASCII {
		rule, tick, heavy, heavyTick = '-', '+', '=', '#'
	}
	dim := o.dim()
	band := look{color: o.Palette.Warn}
	row := make([]cell, f.Width)
	for col, hit := range b.Columns(f) {
		row[col] = cell{r: rule, k: dim}
		if hit {
			row[col] = cell{r: heavy, k: band}
		}
	}
	const clock = 5 // "15:04"
	labels := newLine(o)
	zone := false
	for _, t := range f.TimeTicks() {
		col, ok := f.Col(t)
		if !ok {
			continue
		}
		if row[col].r == heavy {
			row[col].r = heavyTick
		} else {
			row[col].r = tick
		}
		// A label is centred under its tick and may reach into the left
		// margin, so the first tick of a window is labelled too.
		start := l.StripCol + col - clock/2
		if start < labels.col+1 && labels.col > 0 || start < 0 || start+clock > l.Width {
			continue
		}
		if !zone && labels.col == 0 && start >= Indent+4 {
			// The clock is UTC, and says so once, where the row starts.
			labels.to(Indent)
			labels.put(dim, "UTC")
			zone = true
		}
		labels.to(start)
		labels.put(o.muted(), t.UTC().Format("15:04"))
	}
	if !zone && labels.col > 0 && labels.col+4 <= l.Width {
		labels.to(labels.col + 1)
		labels.put(dim, "UTC")
	}
	for _, mark := range b.Marks {
		if col, ok := f.Col(mark.T); ok && mark.Key != 0 {
			row[col] = cell{r: mark.Key, k: look{color: o.Palette.Mark, bold: true}}
		}
	}
	if col, ok := b.cursorCol(f); ok {
		row[col] = cell{r: '^', k: o.text()}
	}
	axis := newLine(o)
	axis.to(l.StripCol)
	axis.strip(row)
	return []string{axis.String(), labels.String()}
}

// MarkLegend names the labelled moments, one line each: the key in the
// mark colour and the label as given. A mark outside the window is left
// out: its key is not on the axis.
func MarkLegend(b Bands, f chart.Frame, width int, o Options) []string {
	var out []string
	for _, mark := range b.Marks {
		if _, ok := f.Col(mark.T); !ok || mark.Key == 0 {
			continue
		}
		l := newLine(o)
		l.to(Indent)
		l.put(look{color: o.Palette.Mark, bold: true}, string(mark.Key))
		l.to(l.col + 1)
		l.put(o.text(), o.clip(o.words(mark.Label), width-l.col-margin(width)))
		out = append(out, l.String())
	}
	return out
}

// Shade runs the bands through a line: the band background behind the
// cells an episode touches, and the guide of a labelled moment and the
// cursor through the cells that are blank. The line may carry colour tags:
// a chart.Panel line, a line of the view's own, an empty line. Column
// l.StripCol of the line is column 0 of the frame; a line that ends before
// a band or a guide is padded to reach it. A cell that has a background of
// its own keeps it. A line the bands do not touch is returned as it is.
func Shade(text string, b Bands, f chart.Frame, l Layout, o Options) string {
	row, touched := b.shade(parse(text), f, l, o)
	if !touched {
		return text
	}
	return tagged(row)
}

// shade is Shade on the cells of a line; touched is false when the bands
// have nothing to draw on this frame.
func (b Bands) shade(row []glyph, f chart.Frame, l Layout, o Options) (out []glyph, touched bool) {
	if f.Width < 1 || !f.End.After(f.Start) {
		return row, false
	}
	band := o.Palette.Band
	cols := b.Columns(f)
	last := -1
	if band != "" {
		for col, hit := range cols {
			if hit {
				last = col
			}
		}
	}
	guides := map[int]cell{}
	guide, cursor := guideGlyph, cursorGlyph
	if o.ASCII {
		guide, cursor = guideASCII, cursorASCII
	}
	for _, mark := range b.Marks {
		if col, ok := f.Col(mark.T); ok {
			guides[col] = cell{r: guide, k: look{color: o.Palette.Mark}}
			last = max(last, col)
		}
	}
	if col, ok := b.cursorCol(f); ok {
		guides[col] = cell{r: cursor, k: o.text()}
		last = max(last, col)
	}
	if last < 0 {
		return row, false
	}
	width := 0
	for _, g := range row {
		width += g.width
	}
	for ; width <= l.StripCol+last; width++ {
		row = append(row, glyph{text: " ", width: 1})
	}
	col := 0
	for i := range row {
		g := &row[i]
		at := col - l.StripCol
		col += g.width
		if at < 0 || at >= len(cols) {
			continue
		}
		// A guide runs through a blank cell whose neighbours are blank
		// too, so it never joins two words.
		if mark, ok := guides[at]; ok && g.blank() && g.attrs == "" &&
			(i == 0 || row[i-1].blank()) && (i == len(row)-1 || row[i+1].blank()) {
			g.text, g.fg, g.attrs = string(mark.r), mark.k.style().fg, mark.k.style().attrs
		}
		if band != "" && cols[at] && g.bg == "" {
			g.bg = band
		}
	}
	return row, true
}
