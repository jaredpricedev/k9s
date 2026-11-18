// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strings"

	"github.com/derailed/k9s/internal/chart"
)

// Row is one thing and its timeline on one line: the subject, what is
// measured, the one number that matters, and the strip.
type Row struct {
	// Subject is whose row it is: "pod hx4mt", "ingress". It is written
	// once for a run of consecutive rows with the same subject.
	Subject string
	// What is measured: "response time p99".
	What string
	// Number is the one number that matters: "47.9s". It is never
	// shortened.
	Number string
	// Compare is what the number is held against: "peers 0.35s", "of 4".
	// It is a column of its own, and the first a narrow screen leaves out.
	Compare string
	// Tag is a short word after the strip, such as "differs".
	Tag string
	// Strip is the timeline.
	Strip Strip
	// Color is the subject's colour, the same on the whole screen: the
	// subject, the number, the tag and what is over its rule in the strip.
	// A row without a colour is a quiet row: muted, with a dim strip.
	Color string
	// Selected marks the row the keys act on with ">".
	Selected bool
	// Aside says What is where the subject is, not what is measured: the
	// node and zone of a Pod. It is then dim on a coloured row too, so
	// the colour of a row is its subject and its numbers.
	Aside bool
}

// Role is how much room a group may claim.
type Role int

const (
	// Primary is the evidence the verdict rests on. It never folds; on a
	// short screen its last rows are left out and counted.
	Primary Role = iota
	// Secondary folds to one line when the screen is short or narrow.
	Secondary
	// QuietRole is things that were read and showed nothing: one dim line
	// each. It folds first.
	QuietRole
	// NotReadRole is things that were not read: one dim line each with the
	// gap glyph all along. Its folded line is the last thing a page gives
	// up: missing data is never health.
	NotReadRole
)

// Group is rows that mean the same thing, under a short label in capitals.
type Group struct {
	// Label is written as given: "LINES UP WITH THE STALLS".
	Label string
	// Color is the colour of the label; empty is muted.
	Color string
	// Head is a word over the number column on the label's row: "peak".
	Head string
	Role Role
	Rows []Row
	// Summary is what the group says when folded to one line; empty names
	// its rows.
	Summary []string
	// NumberWidth and CompareWidth are the least widths of the number and
	// the comparison column, in cells. A view whose numbers are read under
	// a cursor gives the widest it can write there, so that the columns are
	// the same wherever the cursor is and no strip moves when it does.
	NumberWidth, CompareWidth int
}

// items is what a folded group names: its summary, or its rows. A quiet
// row is named by its subject; any other by what it measures and the
// number.
func (g *Group) items() []string {
	if len(g.Summary) > 0 {
		return g.Summary
	}
	var out []string
	seen := map[string]bool{}
	for i := range g.Rows {
		r := &g.Rows[i]
		text := r.Subject
		if g.Role == Primary || g.Role == Secondary {
			text = strings.Join(nonEmpty(r.Subject, r.What, r.Number, r.Compare), " ")
		}
		if text == "" {
			text = r.What
		}
		if text = strings.Join(strings.Fields(text), " "); text != "" && !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}
	return out
}

func nonEmpty(texts ...string) []string {
	var out []string
	for _, text := range texts {
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

// quiet reports whether the rows of the group are one dim line each.
func (g *Group) quiet() bool { return g.Role == QuietRole || g.Role == NotReadRole }

// Layout is the columns of a page, computed once for the whole screen so
// that every strip starts at the same column.
type Layout struct {
	// Width is the width of the screen in cells.
	Width int
	// Subject, What, Number, Compare and Tag are the column widths in
	// cells; zero is a column no row uses or that the width has no room
	// for.
	Subject, What, Number, Compare, Tag int
	// Lead and Tail are the two columns of a quiet line: its subject, and
	// what was measured with the number. They lie over the text columns
	// and never push the strips to the right of where the rows put them,
	// unless the page has quiet lines only.
	Lead, Tail int
	// NumberAfter writes the number after the strip instead of before it.
	NumberAfter bool
	// StripCol is the column every strip starts at.
	StripCol int
	// Strip is the width of every strip, and of the Frame; zero is a
	// screen too narrow for strips.
	Strip int

	// seq is how the sequences of a counted page are drawn in the strips.
	seq sequenceForm
}

const (
	columnGap = 2
	// MinStrip is the narrowest strip that still shows when: below it the
	// strips are left out and the page says so.
	MinStrip = 12
	// goodStrip is the width the strips keep while the text can still
	// give.
	goodStrip = 20

	maxSubject, maxWhat, maxNumber, maxCompare, maxTag = 24, 30, 16, 20, 12
	minSubject, minWhat                                = 6, 10
	maxLead, maxTail, minLead                          = 30, 34, 8
)

// quietTail is what a quiet line writes after its subject: what was
// measured and, where the number is not written after the strip, the
// number.
func quietTail(r *Row, numberAfter bool, o Options) string {
	if numberAfter {
		return o.words(r.What)
	}
	return strings.Join(nonEmpty(o.words(r.What), o.words(strings.Join(nonEmpty(r.Number, r.Compare), " "))), "  ")
}

// natural is the columns as wide as the rows of the groups need them, each
// up to its cap.
func natural(groups []Group, width int, numberAfter bool) Layout {
	l := Layout{Width: width, NumberAfter: numberAfter}
	for gi := range groups {
		g := &groups[gi]
		for ri := range g.Rows {
			r := &g.Rows[ri]
			if g.quiet() {
				l.Lead = max(l.Lead, cells(r.Subject))
				l.Tail = max(l.Tail, cells(quietTail(r, numberAfter, Options{})))
				if numberAfter {
					l.Number = max(l.Number, cells(r.Number))
					l.Compare = max(l.Compare, cells(r.Compare))
				}
				continue
			}
			l.Subject = max(l.Subject, cells(r.Subject))
			l.What = max(l.What, cells(r.What))
			l.Number = max(l.Number, cells(r.Number))
			l.Compare = max(l.Compare, cells(r.Compare))
			l.Tag = max(l.Tag, cells(r.Tag))
		}
		if g.Head != "" {
			l.Number = max(l.Number, cells(g.Head))
		}
		if len(g.Rows) > 0 {
			l.Number, l.Compare = max(l.Number, g.NumberWidth), max(l.Compare, g.CompareWidth)
		}
	}
	l.Subject, l.What = min(l.Subject, maxSubject), min(l.What, maxWhat)
	l.Number, l.Compare, l.Tag = min(l.Number, maxNumber), min(l.Compare, maxCompare), min(l.Tag, maxTag)
	l.Lead, l.Tail = min(l.Lead, maxLead), min(l.Tail, maxTail)
	return l
}

// Measure computes the columns for a width from the rows of the groups.
// gutter is the Y label width of the page's main chart, zero without one:
// the strips then start where its plot does. numberAfter writes the number
// after the strip. The strip takes what the text leaves. When that is less
// than a comfortable strip, the text gives way in this order: the text of
// the quiet lines is shortened, the Compare column is left out, What is
// shortened. The strip then narrows to MinStrip; below that Subject is
// shortened and Tag left out, and a Layout with Strip zero has no room
// for strips at all.
func Measure(groups []Group, width, gutter int, numberAfter bool) Layout {
	return measure(groups, width, gutter, numberAfter, goodStrip)
}

// measure is Measure with the strip width the text gives way for: the
// comfortable width of a timeline, or what the sequences of a counted page
// need.
func measure(groups []Group, width, gutter int, numberAfter bool, want int) Layout {
	l := natural(groups, width, numberAfter)
	// rows and quiet are how far left the rows and the quiet lines reach;
	// the strips start at the larger, or where the chart's plot does.
	rows, quiet := 0, 0
	fit := func() {
		rows, quiet = Indent, Indent
		after := 0
		for _, w := range []int{l.Subject, l.What} {
			if w > 0 {
				rows += w + columnGap
			}
		}
		for _, w := range []int{l.Number, l.Compare} {
			switch {
			case w == 0:
			case numberAfter:
				after += columnGap + w
			default:
				rows += w + columnGap
			}
		}
		if l.Tag > 0 {
			after += columnGap + l.Tag
		}
		for _, w := range []int{l.Lead, l.Tail} {
			if w > 0 {
				quiet += w + columnGap
			}
		}
		left := max(rows, quiet)
		if gutter > 0 {
			left = max(left, gutter+2)
		}
		l.StripCol, l.Strip = left, width-left-after-margin(width)
	}
	// give takes one thing from the text and reports whether there was
	// one to take. Only what holds the strips back gives: the quiet lines
	// when they reach further than the rows, the rows otherwise, and what
	// is written after the strip when the chart's gutter decides.
	give := func(narrow bool) bool {
		fixed := gutter > 0 && gutter+2 >= max(rows, quiet)
		switch {
		case !fixed && quiet > rows && l.Tail > 0:
			l.Tail--
		case !fixed && quiet > rows && l.Lead > minLead:
			l.Lead--
		case !fixed && quiet > rows:
			return false
		case l.Compare > 0 && (!fixed || numberAfter):
			l.Compare = 0
		case !fixed && l.What > minWhat:
			l.What--
		case narrow && !fixed && l.Subject > minSubject:
			l.Subject--
		case narrow && l.Tag > 0:
			l.Tag = 0
		default:
			return false
		}
		fit()
		return true
	}
	fit()
	for l.Strip < want && give(false) {
	}
	for l.Strip < MinStrip && give(true) {
	}
	if l.Strip < MinStrip {
		l.Strip = 0
	}
	return l
}

// textOnly is the layout of rows without strips: the text takes the width.
func (l Layout) textOnly() Layout {
	l.Strip, l.Tag, l.Tail, l.NumberAfter = 0, 0, 0, false
	room := l.Width - Indent - margin(l.Width)
	if l.Number > 0 {
		room -= l.Number + columnGap
	}
	if l.Subject > 0 {
		l.Subject = min(l.Subject, max(room/3, minSubject))
		room -= l.Subject + columnGap
	}
	l.What = max(min(l.What, room), 0)
	if room -= l.What + columnGap; l.Compare > room {
		l.Compare = 0
	}
	l.StripCol = l.Width
	return l
}

// left is the columns before the strip: where the subject, what, the
// number and its comparison start. A column that is not there starts
// where the next one does.
func (l Layout) left() (subject, what, number, compare int) {
	subject = Indent
	what = subject
	if l.Subject > 0 {
		what += l.Subject + columnGap
	}
	number = what
	if l.What > 0 {
		number += l.What + columnGap
	}
	compare = number
	if l.Number > 0 {
		compare += l.Number + columnGap
	}
	return subject, what, number, compare
}

// after is the columns behind the strip: where the number and its
// comparison (when they are written there) and the tag start.
func (l Layout) after() (number, compare, tag int) {
	number = l.StripCol + l.Strip + columnGap
	compare, tag = number, number
	if l.NumberAfter {
		if l.Number > 0 {
			compare += l.Number + columnGap
		}
		tag = compare
		if l.Compare > 0 {
			tag += l.Compare + columnGap
		}
	}
	return number, compare, tag
}

// blank is an empty line of a block: the bands run through it.
func blank(b Bands, f chart.Frame, l Layout, o Options) string {
	if l.Strip == 0 {
		return ""
	}
	return Shade("", b, f, l, o)
}

// headingLine is the label row of a group.
func headingLine(g *Group, b Bands, f chart.Frame, l Layout, o Options) string {
	out := newLine(o)
	out.to(Indent)
	label := o.clip(o.words(g.Label), l.Width-Indent)
	out.put(headingLook(g.Color, o), label)
	if g.Head != "" && l.Number > 0 {
		_, _, at, _ := l.left()
		if l.NumberAfter {
			at, _, _ = l.after()
		}
		// The word is written only where its column is free and on the
		// screen.
		if at >= out.col+columnGap && at+cells(g.Head) <= l.Width {
			out.to(at)
			out.field(o.muted(), o.words(g.Head), l.Number)
		}
	}
	if l.Strip > 0 {
		out.row, _ = b.shade(out.row, f, l, o)
	}
	return out.String()
}

// rowLine is one row. first says the subject is written: it starts a run.
func rowLine(g *Group, r *Row, first bool, b Bands, f chart.Frame, l Layout, o Options) string {
	out := newLine(o)
	if r.Selected {
		out.put(look{color: o.Palette.Accent, bold: true}, ">")
	}
	subjectCol, whatCol, numberCol, compareCol := l.left()
	subject, what, number := look{color: r.Color, bold: true}, o.text(), look{color: r.Color}
	tag := number
	quiet := g.quiet()
	if r.Color == "" || quiet {
		subject, what, number, tag = o.muted(), o.dim(), o.muted(), o.dim()
	}
	if r.Aside {
		what = o.dim()
	}
	strip := r.Strip
	switch {
	case g.Role == NotReadRole:
		strip = UnreadStrip()
	case g.Role == QuietRole && strip.kind == stripNone:
		strip = Quiet()
	}
	end := l.StripCol - columnGap
	if l.Strip == 0 {
		end = l.Width - margin(l.Width)
	}
	out.to(subjectCol)
	if quiet {
		// One dim line: the subject, then what was measured and the
		// number as one text, in a column of their own when the subjects
		// leave one.
		lead, tail := o.words(r.Subject), quietTail(r, l.NumberAfter && l.Strip > 0, o)
		if lead == "" {
			lead, tail = tail, ""
		}
		out.put(subject, o.clip(lead, end-out.col))
		if tail != "" && end-out.col > columnGap {
			at := out.col + columnGap
			if column := subjectCol + l.Lead + columnGap; l.Lead > 0 && column >= at && column+cells(tail) <= end {
				at = column
			}
			out.to(at)
			out.put(o.dim(), o.clip(tail, end-out.col))
		}
	} else {
		if l.Subject > 0 {
			text := ""
			if first {
				text = o.words(r.Subject)
			}
			out.field(subject, text, l.Subject)
		}
		if l.What > 0 {
			out.to(whatCol)
			out.field(what, o.words(r.What), l.What)
		}
		if before := !(l.NumberAfter && l.Strip > 0); before && l.Number > 0 {
			out.to(numberCol)
			out.field(number, o.words(r.Number), l.Number)
		}
		if before := !(l.NumberAfter && l.Strip > 0); before && l.Compare > 0 {
			out.to(compareCol)
			out.field(number, o.words(r.Compare), l.Compare)
		}
	}
	if l.Strip == 0 {
		return out.String()
	}
	out.to(l.StripCol)
	color := r.Color
	if quiet {
		color = ""
	}
	row := strip.draw(f, color, o)
	if strip.kind == stripSequence && l.seq.cells > 0 {
		// Every row of a counted page is drawn in the page's one form.
		row = strip.sequence(l.seq, f.Width, color, o)
	}
	b.through(row, f, o)
	out.strip(row)
	numberAt, compareAt, tagCol := l.after()
	if l.NumberAfter && l.Number > 0 {
		out.to(numberAt)
		out.field(number, o.words(r.Number), l.Number)
	}
	if l.NumberAfter && l.Compare > 0 {
		out.to(compareAt)
		out.field(number, o.words(r.Compare), l.Compare)
	}
	if l.Tag > 0 && r.Tag != "" {
		out.to(tagCol)
		out.field(tag, o.words(r.Tag), l.Tag)
	}
	out.row, _ = b.shade(out.row, f, l, o)
	return out.String()
}

// runs reports, for each row of a group, whether it starts a run of rows
// with one subject, and whether a blank row belongs before it: between two
// runs of which one has more than one row.
func (g *Group) runs() (first, spaced []bool) {
	first, spaced = make([]bool, len(g.Rows)), make([]bool, len(g.Rows))
	length := make([]int, len(g.Rows)) // of the run a row is in
	start := 0
	for i := range g.Rows {
		if i == 0 || g.Rows[i].Subject != g.Rows[i-1].Subject {
			first[i], start = true, i
		}
		for j := start; j <= i; j++ {
			length[j] = i - start + 1
		}
	}
	for i := 1; i < len(g.Rows); i++ {
		if first[i] && !g.quiet() && (length[i] > 1 || length[i-1] > 1) {
			spaced[i] = true
		}
	}
	return first, spaced
}

// GroupLines is a group as it is written in a block: its label, then its
// rows, the first rows of them. spaced puts a blank row between two
// subjects when one of them has several rows. Every line is shaded where
// an episode touches it.
func GroupLines(g Group, rows int, spaced bool, b Bands, f chart.Frame, l Layout, o Options) []string {
	rows = min(max(rows, 0), len(g.Rows))
	out := []string{headingLine(&g, b, f, l, o)}
	first, gap := g.runs()
	for i := range g.Rows[:rows] {
		if spaced && gap[i] {
			out = append(out, blank(b, f, l, o))
		}
		out = append(out, rowLine(&g, &g.Rows[i], first[i], b, f, l, o))
	}
	return out
}
