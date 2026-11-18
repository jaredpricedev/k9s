// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/chart"
)

// Chart is the one main chart of a page. The kit does not draw it: the
// view does, with chart.Panel, in the frame, gutter and height the plan
// gives it, so that its plot stands on the axis the strips share.
type Chart struct {
	// Head is the line above the plot, written and tagged by the view:
	// the name (HeadingText), the value now, the levels drawn.
	Head string
	// Gutter is the width of the Y labels (chart.Panel.GutterWidth).
	Gutter int
	// Min and Max are the fewest and the most plot rows; zero is 4 and 10.
	Min, Max int
	// Draw returns the plot: height lines whose plot starts at column
	// gutter+2 and is f.Width cells wide (chart.Panel.Render with these
	// three). f is SampleFrame of the page's frame: drawn in it, a sample
	// of the chart stands where the strips put theirs.
	Draw func(f chart.Frame, gutter, height int) []string
}

func (c *Chart) bounds() (least, most int) {
	least, most = c.Min, c.Max
	if least < 1 {
		least = 4
	}
	if most < least {
		most = max(10, least)
	}
	return least, most
}

// Page is everything a calm page shows, in the order it shows it.
type Page struct {
	Title   Title
	Verdict Verdict
	// Frame is the shared time axis: the window and the step. Its Width
	// is decided by the plan.
	Frame chart.Frame
	Bands Bands
	// Chart is the one main chart, nil on a page of rows.
	Chart *Chart
	// Groups are drawn in order. A folded group is one line under the
	// axis.
	Groups []Group
	// Next are the three or four likely actions, the likeliest first.
	Next []Action
	// Keys are the other keys, as words; the last is the way out.
	Keys []string
	// NumberAfter writes the number of a row after its strip.
	NumberAfter bool
	// Counted says the strips are sequences of counted things (Sequence),
	// not timelines. Frame is not used: the page has no time axis, a strip
	// is as wide as its cells need and no wider, and a caption row names
	// the two sequences of a row when there are two.
	Counted bool
}

// Plan is how a page is laid out in an area: the columns, what folds, and
// what the area has no room for.
type Plan struct {
	Width, Height int
	// Layout is the columns; Layout.Strip is the width of the strips.
	Layout Layout
	// Frame is the page's frame at that width.
	Frame chart.Frame
	// ChartHeight is the plot rows of the main chart; zero without one.
	ChartHeight int
	// Folded, Hidden and Rows say per group whether it is one line under
	// the axis, whether it is not drawn at all, and how many of its rows
	// are drawn.
	Folded, Hidden []bool
	Rows           []int
	// TextRows says the rows are written without strips: the width has no
	// room for a strip beside the text.
	TextRows bool
	// Dropped is everything this area gave up, in words, in the order it
	// was given up: blank rows first, then folds, then what is not shown.
	Dropped []string
	// Missing is the part of Dropped that is not on the screen at all. The
	// page says so in one line and names the key that shows it.
	Missing []string
	// TooSmall says not even the smallest form fits: the page then shows
	// the title, the verdict and the size it needs.
	TooSmall bool

	items []item
}

type itemKind int

const (
	itTitle itemKind = iota
	itBlank
	itVerdict
	itMarker
	itShadedBlank
	itChartHead
	itChart
	itHeading
	itRow
	itAxis
	itFolded
	itLegend
	itNote
	itNext
	itRest
	itTooSmall
	itCaption
)

// item is one line of a page: what it is and which group and row or which
// line of a part it shows.
type item struct {
	kind     itemKind
	group, n int
}

// drop is one thing a plan gave up. A trim is the rows left out of a
// primary group: its words follow the count.
type drop struct {
	words   string
	missing bool
	trim    bool
	group   int
}

// state is how far down the ladder a plan is.
type state struct {
	airy, spaced, gaps bool
	rest, next, loose  bool
	strips             bool
	folded, hidden     []bool
	rows               []int
	drops              []drop
}

func (st *state) clone() state {
	c := *st
	c.folded = append([]bool(nil), st.folded...)
	c.hidden = append([]bool(nil), st.hidden...)
	c.rows = append([]int(nil), st.rows...)
	c.drops = append([]drop(nil), st.drops...)
	return c
}

// Lay plans a page for an area. It gives up room in a fixed order and only
// as far as the area needs:
//
//  1. the strips shorten, and the text beside them gives way as Measure
//     says;
//  2. the blank rows around the verdict and the keys go;
//  3. the quiet and the not-read groups fold to one line each;
//  4. the blank rows between subjects and before groups go; then the
//     secondary groups fold to one line each, then the second key line
//     goes;
//  5. the last blank rows go, under the verdict and over the keys; then
//     the last rows of the primary groups are left out and counted;
//  6. the folded lines of the quiet and secondary groups are left out;
//  7. the strips are left out and the rows are written as text;
//  8. the primary groups are left out, the last first: a page with a main
//     chart then shows the chart alone, a page of rows its first group;
//     the line of next steps is the last thing to go.
//
// A step is taken only if it helps what does not fit: a fold that does
// not widen the strips of a page that is only too narrow is not made. The
// folded line of a not-read group is never left out. Whatever is not on
// the screen is named in one line with the key that shows it.
func Lay(p *Page, width, height int, o Options) Plan {
	count := len(p.Groups)
	st := state{airy: true, spaced: true, gaps: true, rest: true, next: true, loose: true, strips: p.Counted || p.Frame.End.After(p.Frame.Start),
		folded: make([]bool, count), hidden: make([]bool, count), rows: make([]int, count)}
	for i := range p.Groups {
		st.rows[i] = len(p.Groups[i].Rows)
		// A group that has only a summary is one line from the start.
		st.folded[i] = len(p.Groups[i].Rows) == 0
	}
	label := func(i int) string {
		if p.Groups[i].Label != "" {
			return p.Groups[i].Label
		}
		return "group " + strconv.Itoa(i+1)
	}
	// last is the last group a step can still act on.
	last := func(ok func(i int) bool) int {
		for i := count - 1; i >= 0; i-- {
			if ok(i) {
				return i
			}
		}
		return -1
	}
	fold := func(role Role) func() bool {
		return func() bool {
			i := last(func(i int) bool { return p.Groups[i].Role == role && !st.folded[i] && !st.hidden[i] })
			if i < 0 {
				return false
			}
			st.folded[i] = true
			st.drops = append(st.drops, drop{words: label(i) + " folded to one line"})
			return true
		}
	}
	flag := func(on func() *bool, words string) func() bool {
		return func() bool {
			if !*on() {
				return false
			}
			*on() = false
			st.drops = append(st.drops, drop{words: words})
			return true
		}
	}
	hide := func(i int) bool {
		if i < 0 {
			return false
		}
		st.hidden[i] = true
		st.drops = append(st.drops, drop{words: label(i), missing: true})
		return true
	}
	// keep is the group a page of rows never gives up: its first primary
	// group. A page with a main chart keeps the chart.
	keep := -1
	if p.Chart == nil {
		for i := count - 1; i >= 0; i-- {
			if p.Groups[i].Role == Primary {
				keep = i
			}
		}
	}
	trim := func() bool {
		// The longest group gives a row first, the last of them when
		// several are as long: every group keeps its first rows, the ones
		// a view puts what stands out in.
		i := -1
		for g := range p.Groups {
			if p.Groups[g].Role == Primary && !st.hidden[g] && st.rows[g] > 1 && (i < 0 || st.rows[g] >= st.rows[i]) {
				i = g
			}
		}
		if i < 0 {
			return false
		}
		st.rows[i]--
		for at := range st.drops {
			if st.drops[at].trim && st.drops[at].group == i {
				return true
			}
		}
		st.drops = append(st.drops, drop{trim: true, group: i, missing: true})
		return true
	}
	steps := []struct {
		width, height bool
		// blank is a step that only takes blank rows: it is not reported
		// when there were none to take.
		blank bool
		apply func() bool
	}{
		{height: true, blank: true, apply: flag(func() *bool { return &st.airy }, "blank rows around the verdict and the keys")},
		{width: true, height: true, apply: fold(QuietRole)},
		{width: true, height: true, apply: fold(NotReadRole)},
		{height: true, blank: true, apply: flag(func() *bool { return &st.spaced }, "blank rows between subjects")},
		{height: true, blank: true, apply: flag(func() *bool { return &st.gaps }, "blank rows before groups")},
		{width: true, height: true, apply: fold(Secondary)},
		{height: true, apply: flag(func() *bool { return &st.rest }, "the second key line")},
		{height: true, blank: true, apply: flag(func() *bool { return &st.loose }, "the blank rows under the verdict and over the keys")},
		{height: true, apply: trim},
		{height: true, apply: func() bool {
			return hide(last(func(i int) bool { return st.folded[i] && !st.hidden[i] && p.Groups[i].Role != NotReadRole }))
		}},
		{width: true, height: true, apply: func() bool {
			if !st.strips || len(st.shown(p)) == 0 {
				return false
			}
			st.strips = false
			words := "the time strips"
			switch {
			case p.Chart != nil:
				words = "the strips beside the rows"
			case p.Counted:
				words = "the strips"
			}
			st.drops = append(st.drops, drop{words: words, missing: true})
			return true
		}},
		{width: true, height: true, apply: func() bool {
			return hide(last(func(i int) bool { return p.Groups[i].Role == Primary && !st.hidden[i] && i != keep }))
		}},
		{height: true, apply: flag(func() *bool { return &st.next }, "the next line")},
	}
	plan := st.build(p, width, height, o)
	// Each round takes the first step that helps what does not fit, so a
	// step the width had no use for is still there when the height needs
	// it. A step that was taken back is not tried again.
	spent := map[int]bool{}
	for {
		wide, tall := plan.fits()
		if wide && tall {
			break
		}
		taken := false
		for index, step := range steps {
			if spent[index] || !(step.width && !wide || step.height && !tall) {
				continue
			}
			before := st.clone()
			if !step.apply() {
				continue
			}
			next := st.build(p, width, height, o)
			if nextWide, _ := next.fits(); tall && !nextWide && next.Layout.Strip <= plan.Layout.Strip && next.TextRows == plan.TextRows {
				// Only too narrow, and the step did not widen the strips:
				// it is taken back.
				st, spent[index] = before, true
				continue
			}
			if step.blank && len(next.items) == len(plan.items) {
				st.drops = before.drops
				next = st.build(p, width, height, o)
			}
			plan, taken = next, true
			break
		}
		if !taken {
			break
		}
	}
	if wide, tall := plan.fits(); !wide || !tall {
		plan.TooSmall = true
		plan.Dropped = append(plan.Dropped, "everything under the verdict")
		plan.Missing = []string{"everything under the verdict"}
		plan.items = plan.items[:0]
		plan.items = append(plan.items, item{kind: itTitle})
		for n := range VerdictLines(p.Verdict, width, o) {
			if len(plan.items) < max(height-1, 1) {
				plan.items = append(plan.items, item{kind: itVerdict, n: n})
			}
		}
		plan.items = append(plan.items, item{kind: itTooSmall})
	}
	return plan
}

// fits reports whether the plan fits the area: the strips are wide enough
// to read, and the lines are no more than the height.
func (plan *Plan) fits() (wide, tall bool) {
	wide = plan.Layout.Strip >= MinStrip || plan.TextRows && plan.ChartHeight == 0
	return wide, len(plan.items) <= plan.Height
}

// shown is the groups whose rows are drawn, cut to the rows that are.
func (st *state) shown(p *Page) []Group {
	var out []Group
	for i := range p.Groups {
		if st.folded[i] || st.hidden[i] {
			continue
		}
		g := p.Groups[i]
		g.Rows = g.Rows[:st.rows[i]]
		out = append(out, g)
	}
	return out
}

// build lays the page out at one rung of the ladder.
func (st *state) build(p *Page, width, height int, o Options) Plan {
	plan := Plan{Width: width, Height: height, Folded: append([]bool(nil), st.folded...),
		Hidden: append([]bool(nil), st.hidden...), Rows: append([]int(nil), st.rows...)}
	gutter := 0
	if p.Chart != nil {
		gutter = max(p.Chart.Gutter, 1)
	}
	rows := st.shown(p)
	switch {
	case st.strips && p.Counted:
		plan.Layout = measureCounted(rows, width, gutter, p.NumberAfter)
	case st.strips:
		plan.Layout = Measure(rows, width, gutter, p.NumberAfter)
	default:
		// Without strips beside the rows the axis belongs to the chart
		// alone, and a page of rows has none.
		plan.TextRows = true
		plan.Layout = Layout{Width: width, StripCol: width}
		if p.Chart != nil {
			plan.Layout = Measure(nil, width, gutter, p.NumberAfter)
		}
	}
	plan.Frame = p.Frame
	plan.Frame.Width = plan.Layout.Strip
	if p.Counted {
		plan.Frame = countedFrame(plan.Layout.Strip)
	}
	axis := !p.Counted && plan.Layout.Strip > 0 && (p.Chart != nil || len(rows) > 0 && st.strips)

	add := func(kind itemKind, group, n int) {
		plan.items = append(plan.items, item{kind: kind, group: group, n: n})
	}
	blank := func(kind itemKind) {
		if n := len(plan.items); n > 0 && plan.items[n-1].kind != itBlank && plan.items[n-1].kind != itShadedBlank {
			add(kind, 0, 0)
		}
	}
	add(itTitle, 0, 0)
	if st.airy {
		blank(itBlank)
	}
	for n := range VerdictLines(p.Verdict, width, o) {
		add(itVerdict, 0, n)
	}
	if st.loose {
		blank(itBlank)
	}
	if axis && p.Bands.Any() {
		add(itMarker, 0, 0)
		if st.airy {
			blank(itShadedBlank)
		}
	}
	if p.Counted && st.strips && plan.Layout.Strip > 0 && len(rows) > 0 && plan.Layout.seq.captioned() {
		add(itCaption, 0, 0)
	}
	chartAt := -1
	if p.Chart != nil {
		if p.Chart.Head != "" {
			add(itChartHead, 0, 0)
		}
		chartAt = len(plan.items)
	}
	for i := range p.Groups {
		if st.folded[i] || st.hidden[i] {
			continue
		}
		switch {
		case !st.gaps:
		case chartAt == len(plan.items):
			// The plot is put in at chartAt once its height is known.
			add(itShadedBlank, 0, 0)
		case plan.items[len(plan.items)-1].kind == itCaption:
			// The caption row is the head of the rows under it.
		default:
			blank(itShadedBlank)
		}
		add(itHeading, i, 0)
		_, gap := p.Groups[i].runs()
		for r := range st.rows[i] {
			if st.spaced && gap[r] {
				add(itShadedBlank, 0, 0)
			}
			add(itRow, i, r)
		}
	}
	if axis {
		add(itAxis, 0, 0)
		add(itAxis, 0, 1)
	}
	folded := false
	for i := range p.Groups {
		if st.folded[i] && !st.hidden[i] {
			if !folded && st.gaps {
				blank(itBlank)
			}
			folded = true
			add(itFolded, i, 0)
		}
	}
	if axis {
		for n := range MarkLegend(p.Bands, plan.Frame, width, o) {
			if n == 0 && st.airy {
				blank(itBlank)
			}
			add(itLegend, 0, n)
		}
	}
	for _, d := range st.drops {
		words := d.words
		if d.trim {
			if st.hidden[d.group] {
				continue
			}
			all := len(p.Groups[d.group].Rows)
			words = strconv.Itoa(all-st.rows[d.group]) + " of " + strconv.Itoa(all) + " rows of " + p.Groups[d.group].Label
		}
		plan.Dropped = append(plan.Dropped, words)
		if d.missing {
			plan.Missing = append(plan.Missing, words)
		}
	}
	if p.Counted && st.strips {
		// What a narrow strip leaves out of its sequences is not on the
		// screen at all, and is named with the rest.
		words := plan.Layout.seq.missing(plan.Layout.Strip)
		plan.Dropped = append(plan.Dropped, words...)
		plan.Missing = append(plan.Missing, words...)
	}
	if st.strips && plan.Layout.Compare == 0 && natural(rows, width, p.NumberAfter).Compare > 0 {
		// The number stays; what it is held against is context, and is
		// reported without taking a row for it.
		plan.Dropped = append(plan.Dropped, "what each number is compared with")
	}
	if len(plan.Missing) > 0 {
		add(itNote, 0, 0)
	}
	next := st.next && len(p.Next) > 0
	if next {
		if st.loose {
			blank(itBlank)
		}
		add(itNext, 0, 0)
	}
	if st.rest && RestLine(p.Keys, width, o) != "" {
		if st.airy || !next && st.loose {
			blank(itBlank)
		}
		add(itRest, 0, 0)
	}
	for n := len(plan.items); n > 0 && plan.items[n-1].kind == itBlank; n-- {
		plan.items = plan.items[:n-1]
	}
	if p.Chart != nil {
		// The chart takes the rows that are left, between its bounds.
		least, most := p.Chart.bounds()
		plan.ChartHeight = min(max(height-len(plan.items), least), most)
		rest := append([]item(nil), plan.items[chartAt:]...)
		plan.items = plan.items[:chartAt]
		for n := range plan.ChartHeight {
			add(itChart, 0, n)
		}
		plan.items = append(plan.items, rest...)
	}
	return plan
}

// Note is the line that names the key that shows all detail and what the
// area has no room for; empty when everything is on the screen. The key
// comes first, so a narrow line cuts the list and not the key.
func (plan *Plan) Note() string {
	if len(plan.Missing) == 0 {
		return ""
	}
	return "not shown at this size (" + DetailKey + " shows all): " + strings.Join(plan.Missing, ", ")
}

// noteIn is the note for a line with room cells: Note where it fits, and
// with fewer words before the list where it does not, so that a narrow
// line names what is missing before it cuts anything.
func (plan *Plan) noteIn(room int) string {
	note := plan.Note()
	if note == "" || cells(note) <= room {
		return note
	}
	return "not shown (" + DetailKey + " shows all): " + strings.Join(plan.Missing, ", ")
}

// Rendered is a page as lines of tview-tagged text.
type Rendered struct {
	// Title, Verdict, Body and Keys are the parts of the page without the
	// blank rows between them.
	Title   string
	Verdict []string
	Body    []string
	Keys    []string
	lines   []string
}

// Lines is the whole page, top to bottom, with the blank rows the plan
// kept. It is no longer than the height it was planned for; a page that
// is too small for its evidence still has its title and one line that
// names the key for all detail.
func (r Rendered) Lines() []string { return r.lines }

// Render draws a page as the plan laid it out. It takes the page the plan
// was made for and the options it was made with.
func Render(p *Page, plan *Plan, o Options) Rendered {
	var out Rendered
	l, f, b := plan.Layout, plan.Frame, p.Bands
	if !b.Any() || plan.Layout.Strip == 0 {
		b.Episodes = nil
	}
	rowLayout := l
	if plan.TextRows {
		rowLayout = natural(plan.shownGroups(p), plan.Width, false).textOnly()
	}
	var plot []string
	if p.Chart != nil && p.Chart.Draw != nil && plan.ChartHeight > 0 && !plan.TooSmall {
		plot = p.Chart.Draw(SampleFrame(f), l.StripCol-2, plan.ChartHeight)
	}
	verdict := VerdictLines(p.Verdict, plan.Width, o)
	var axis, legend []string
	firsts := map[int][]bool{}
	for _, it := range plan.items {
		text := ""
		part := &out.Body
		switch it.kind {
		case itTitle:
			out.Title = TitleLine(p.Title, plan.Width, o)
			text, part = out.Title, nil
		case itBlank:
			part = nil
		case itVerdict:
			text, part = verdict[it.n], &out.Verdict
		case itMarker:
			text = MarkerRow(b, f, l, o)
		case itCaption:
			text = captionRow(l, o)
		case itShadedBlank:
			text = blank(b, f, l, o)
			part = nil
		case itChartHead:
			text = Shade(p.Chart.Head, b, f, l, o)
		case itChart:
			if it.n < len(plot) {
				text = Shade(plot[it.n], b, f, l, o)
			} else {
				text = blank(b, f, l, o)
			}
		case itHeading:
			text = headingLine(&p.Groups[it.group], b, f, rowLayout, o)
		case itRow:
			g := &p.Groups[it.group]
			if firsts[it.group] == nil {
				firsts[it.group], _ = g.runs()
			}
			text = rowLine(g, &g.Rows[it.n], firsts[it.group][it.n], b, f, rowLayout, o)
		case itAxis:
			if axis == nil {
				axis = AxisLines(b, f, l, o)
			}
			// A page whose window is not a stretch of time has no axis.
			if it.n < len(axis) {
				text = axis[it.n]
			}
		case itFolded:
			g := &p.Groups[it.group]
			text = FoldedLine(g.Label, g.Color, g.items(), g.quiet(), plan.Width, o)
		case itLegend:
			if legend == nil {
				legend = MarkLegend(b, f, plan.Width, o)
			}
			text = legend[it.n]
		case itNote:
			note := newLine(o)
			note.to(Indent)
			note.put(o.muted(), o.clip(plan.noteIn(plan.Width-Indent), plan.Width-Indent))
			text = note.String()
		case itNext:
			text, part = NextLine(p.Next, plan.Width, o), &out.Keys
		case itRest:
			text, part = RestLine(p.Keys, plan.Width, o), &out.Keys
		case itTooSmall:
			note := newLine(o)
			words := DetailKey + " shows all detail: no room for the evidence in " + strconv.Itoa(plan.Width) + "x" + strconv.Itoa(plan.Height)
			note.put(look{color: o.Palette.Warn}, o.clip(words, plan.Width))
			text = note.String()
		}
		// A line a view wrote itself may be wider than the area.
		text = fitted(text, plan.Width)
		if part != nil {
			*part = append(*part, text)
		}
		out.lines = append(out.lines, text)
	}
	return out
}

// WhatWidth is the width, in cells, of the column the rows of the page are
// written what they are about in: the Layout's where the rows have strips,
// and what the text takes of the screen where they are written without. A
// view that cuts that text itself, to keep something at its end that a cut
// from the right would lose, cuts it to this width. It is measured from the
// rows as they are, so a view cuts once the page is planned and never to
// more than the text it planned with.
func (plan *Plan) WhatWidth(p *Page) int {
	if plan.TextRows {
		return natural(plan.shownGroups(p), plan.Width, false).textOnly().What
	}
	return plan.Layout.What
}

// shownGroups is the groups whose rows the plan draws, cut to those rows.
func (plan *Plan) shownGroups(p *Page) []Group {
	var out []Group
	for i := range p.Groups {
		if plan.Folded[i] || plan.Hidden[i] {
			continue
		}
		g := p.Groups[i]
		g.Rows = g.Rows[:plan.Rows[i]]
		out = append(out, g)
	}
	return out
}

// fitted is a tagged line cut to a width in cells.
func fitted(text string, width int) string {
	row := parse(text)
	used := 0
	for i, g := range row {
		if used+g.width > width {
			return tagged(row[:i])
		}
		used += g.width
	}
	return text
}
