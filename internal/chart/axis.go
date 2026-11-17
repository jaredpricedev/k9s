// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// Mark is a labelled moment on the time axis. It states when something
// happened; it does not say that it caused anything.
type Mark struct {
	Key   rune
	T     time.Time
	Label string
}

var tickLadder = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

const clockWidth = 5 // "15:04"

// TickInterval is the smallest round interval whose clock labels fit.
func (f Frame) TickInterval() time.Duration {
	if !f.valid() {
		return 0
	}
	window := f.End.Sub(f.Start)
	for _, interval := range tickLadder {
		if int(window/interval)*(clockWidth+3) <= f.Width {
			return interval
		}
	}
	return tickLadder[len(tickLadder)-1]
}

// TimeTicks are the round UTC clock times inside the window.
func (f Frame) TimeTicks() []time.Time {
	interval := f.TickInterval()
	if interval <= 0 {
		return nil
	}
	var ticks []time.Time
	for t := f.Start.UTC().Truncate(interval); !t.After(f.End); t = t.Add(interval) {
		if !t.Before(f.Start) {
			ticks = append(ticks, t)
		}
	}
	return ticks
}

// Axis draws the shared time axis as two lines: the axis with ticks, marks
// and the cursor, and UTC clock labels under the ticks.
func Axis(f Frame, gutter int, marks []Mark, o Options) []string {
	if !f.valid() {
		return nil
	}
	corner, line, tick := '└', '─', '┬'
	if o.ASCII {
		corner, line, tick = '+', '-', '+'
	}
	const cursor = '^'
	axis := make([]rune, f.Width)
	for i := range axis {
		axis[i] = line
	}
	labels := []rune(strings.Repeat(" ", f.Width))
	free := 0
	for _, t := range f.TimeTicks() {
		col, ok := f.Col(t)
		if !ok {
			continue
		}
		axis[col] = tick
		start := col - clockWidth/2
		if start < free || start+clockWidth > f.Width {
			continue
		}
		copy(labels[start:], []rune(t.UTC().Format("15:04")))
		free = start + clockWidth + 1
	}
	for _, mark := range marks {
		if col, ok := f.Col(mark.T); ok && mark.Key != 0 {
			// Two marks in one column sit side by side, earlier to the left.
			for col > 0 && axis[col] != line && axis[col] != tick {
				col--
			}
			axis[col] = mark.Key
		}
	}
	pad := strings.Repeat(" ", gutter+1)
	var b strings.Builder
	b.WriteString(paint(o.AxisColor, pad+string(corner)))
	cursorCol := -1
	if o.Cursor >= 0 {
		if col, ok := f.Col(f.TimeAt(o.Cursor)); ok {
			cursorCol = col
		}
	}
	run := ""
	flush := func() {
		b.WriteString(paint(o.AxisColor, run))
		run = ""
	}
	for col, r := range axis {
		switch {
		case r != line && r != tick:
			flush()
			b.WriteRune(r)
		case col == cursorCol:
			flush()
			b.WriteString(paint(o.CursorColor, string(cursor)))
		default:
			run += string(r)
		}
	}
	flush()
	return []string{b.String(), paint(o.AxisColor, strings.TrimRight(pad+" "+string(labels), " "))}
}

// Strip draws one row of event marks, such as restarts, on the shared axis.
// More than one event in a column is still one glyph: the count belongs in
// a readout, not in the picture.
func Strip(label string, events []time.Time, f Frame, gutter int, color string, o Options) string {
	if !f.valid() {
		return ""
	}
	bar, glyph, cursor := "│", '▲', '┆'
	if o.ASCII {
		bar, glyph, cursor = "|", '^', ':'
	}
	row := []rune(strings.Repeat(" ", f.Width))
	hit := make([]bool, f.Width)
	if o.Cursor >= 0 {
		if col, ok := f.Col(f.TimeAt(o.Cursor)); ok {
			row[col] = cursor
		}
	}
	for _, t := range events {
		if col, ok := f.Col(t); ok {
			row[col], hit[col] = glyph, true
		}
	}
	label = runewidth.Truncate(label, gutter, "")
	var b strings.Builder
	b.WriteString(paint(o.AxisColor, strings.Repeat(" ", gutter-runewidth.StringWidth(label))+label+" "+bar))
	end := len(row)
	for end > 0 && row[end-1] == ' ' {
		end--
	}
	for col := range end {
		switch {
		case hit[col]:
			b.WriteString(paint(color, string(row[col])))
		case row[col] != ' ':
			b.WriteString(paint(o.CursorColor, string(row[col])))
		default:
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// Entry is one legend item: the glyph that identifies the series in the
// plot, its name and its value at the cursor.
type Entry struct {
	Glyph, Name, Value, Color string
}

func (e Entry) rest() string { return strings.TrimSpace(e.Name + " " + e.Value) }

func (e Entry) plain() string { return strings.TrimSpace(e.Glyph + " " + e.rest()) }

// Legend lays a title and entries out on at most maxLines lines of the
// given width. Entries that do not fit are counted, never silently dropped.
// escape protects text from the caller's markup (nil leaves it alone).
func Legend(title string, entries []Entry, width, maxLines int, escape func(string) string) []string {
	if escape == nil {
		escape = func(s string) string { return s }
	}
	if maxLines < 1 || width < 1 {
		return nil
	}
	const sep = "  "
	title = runewidth.Truncate(title, width, "…")
	lines := []string{escape(title)}
	used := runewidth.StringWidth(title)
	for index, entry := range entries {
		need := runewidth.StringWidth(entry.plain())
		// Room is kept for the count of what is left out.
		reserve := 0
		if index < len(entries)-1 {
			reserve = len(sep) + len("+99 more")
		}
		fits := func(used int) bool { return used+len(sep)+need+reserve <= width }
		if !fits(used) {
			if len(lines) >= maxLines || !fits(0) {
				more := "+" + strconv.Itoa(len(entries)-index) + " more"
				if used+len(sep)+len(more) <= width {
					lines[len(lines)-1] += sep + more
				}
				break
			}
			lines = append(lines, "")
			used = 0
		}
		colored := paint(entry.Color, escape(entry.Glyph))
		if rest := entry.rest(); rest != "" {
			if entry.Glyph != "" {
				colored += " "
			}
			colored += escape(rest)
		}
		lines[len(lines)-1] += sep + colored
		used += len(sep) + need
	}
	return lines
}
