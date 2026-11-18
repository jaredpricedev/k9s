// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// The calm layout of a drawn dashboard and of :query, built with the calm
// kit. A page says first what was read, then draws one panel at full size
// and every other panel as one row on the same time axis: its title, its
// strip and the one number that matters. What could not be drawn is one
// line each under the axis. Nothing here reads anything: the page is laid
// out from the snapshot the view already holds.
const (
	// dashCalmLines is the most lines the main chart draws: the ones with
	// the highest peak. The data colours are as many.
	dashCalmLines = 4
	// dashCalmFloor is the narrowest plot the main chart is drawn in beside
	// the rows. Below it the sections fold to one line each and the chart
	// takes the width. The sections fold as well when the kit would leave
	// out what stands beside a number: a row here never loses the word
	// that its window held a worse step, or its peak.
	dashCalmFloor = 20
	// dashCalmCompare is the width from which a row says what stands
	// beside its number: the peak, the window's maximum, how many series.
	dashCalmCompare = 120
	// dashCalmNumber is the width the kit gives the number of a row, and
	// dashCalmBeside the width it gives what stands beside the number.
	dashCalmNumber = 16
	dashCalmBeside = 20
	// dashCalmFirst is the first step the time cursor stands on: the first
	// sample the page has a column for. A sample is drawn over the step
	// that ends at it, and the one at the window's start ends before it.
	dashCalmFirst = 1
	// dashCalmCaveat keeps a page that only reads from sounding like a
	// finding. A threshold word on it is the dashboard author's rule.
	dashCalmCaveat = "A reading, not a finding."

	dashUnread   = "NOT READ"
	dashNoSeries = "NO SERIES MATCHED"
	dashAlso     = "ALSO"
)

func (r *dashRender) calmOpts() calm.Options {
	return calm.Options{ASCII: r.pg.ASCII, Palette: r.pg.Calm}
}

// calmText is plain text in one colour as a whole tview tag. The kit reads
// a tag as a whole, so a line the view writes beside the kit's names the
// foreground, the background and the attributes too.
func calmText(color, text string) string {
	if color == "" || text == "" {
		return tview.Escape(text)
	}
	return "[" + color + ":-:-]" + tview.Escape(text) + "[-:-:-]"
}

// calmTitle is the first line: the page, what it draws, the window, and
// how many of the queries were answered.
func (r *dashRender) calmTitle() calm.Title {
	s := r.s
	object := safeProviderText(s.title())
	if q := s.Spec.Query; q != nil && q.Name == "" && strings.TrimSpace(q.Expr) != "" {
		// A typed expression has no name: it is what the page draws.
		object = safeProviderText(q.Expr)
	}
	t := calm.Title{View: s.Spec.noun(), Object: object, Window: "last " + graphSpan(s.Window)}
	if s.Dash != nil && s.Prom == nil && s.ParseErr == "" {
		tally := s.tally()
		t.Read, t.Done, t.Total = s.ReadAt, tally.Asked-tally.Failed-tally.Rejected, tally.Asked
	}
	if r.pg.Busy != "" {
		t.Busy = "reading again…"
		if !t.Read.IsZero() {
			t.Busy = "reading again · shown: read " + t.Read.UTC().Format("15:04")
		}
	}
	return t
}

// calmWords is a page of words: a title, a verdict and the keys.
func (r *dashRender) calmWords(title calm.Title, v calm.Verdict, next []calm.Action, keys []string) []string { //nolint:gocritic // The kit takes its parts by value.
	o := r.calmOpts()
	page := calm.Page{Title: title, Verdict: v, Next: next, Keys: keys}
	plan := calm.Lay(&page, r.width, max(r.pg.Height, 1), o)
	return calm.Render(&page, &plan, o).Lines()
}

// calmFailure is a page on which nothing can be drawn: which source did
// not answer, in the words of the read, and what can be done.
func (r *dashRender) calmFailure(failure []string) []string {
	v := calm.Unread(failure[0], "")
	v.Sentences = append(append(v.Sentences, failure[1:]...), "Source: "+r.s.source())
	next := []calm.Action{{Key: "r", Label: calmNextReread}, {Key: calmGrafanaKeyName, Label: calmNextGrafana}}
	if r.s.Spec.isQuery() && !r.s.Spec.Board {
		next = append([]calm.Action{{Key: "e", Label: "edit the expression"}}, next...)
	}
	return r.calmWords(r.calmTitle(), v, next, (&calmKeyLine{Window: calmKeyWindow}).parts())
}

// drawnPanels are the panels with at least one line, in the dashboard's
// order.
func (r *dashRender) drawnPanels() []int {
	var out []int
	for i := range r.s.Dash.Panels {
		if r.s.Dash.Panels[i].Kind != dashboard.KindRow && r.reason(i) == "" {
			out = append(out, i)
		}
	}
	return out
}

// calmMain is the panel drawn at full size: the one the keys chose, or the
// first line chart, or the first panel that was drawn. -1 when none was.
func (r *dashRender) calmMain(chosen int, drawn []int) int {
	for _, i := range drawn {
		if i == chosen {
			return i
		}
	}
	for _, i := range drawn {
		if !r.s.Dash.Panels[i].Kind.Value() {
			return i
		}
	}
	if len(drawn) > 0 {
		return drawn[0]
	}
	return -1
}

func dashPeak(points []chart.Point) (peak float64, at time.Time) {
	peak = math.Inf(-1)
	for _, point := range points {
		if point.V > peak {
			peak, at = point.V, point.T
		}
	}
	return peak, at
}

// dashHighest is the line a one-row strip stands for: the one with the
// highest peak among the lines that move. A level that never moves (a
// limit) stands for the panel only when nothing else was read.
func dashHighest(lines []dashLine) int {
	best, bestMoves, bestPeak := 0, false, math.Inf(-1)
	for i := range lines {
		low, high := math.Inf(1), math.Inf(-1)
		for _, point := range lines[i].Points {
			low, high = math.Min(low, point.V), math.Max(high, point.V)
		}
		moves := high > low
		if moves && !bestMoves || moves == bestMoves && high > bestPeak {
			best, bestMoves, bestPeak = i, moves, high
		}
	}
	return best
}

// rowTitle is a panel's name on a row. A named query is known by its name.
func (r *dashRender) rowTitle(index int) string {
	title := safeProviderText(r.s.Dash.Panels[index].Title)
	if r.s.Spec.Board {
		title, _, _ = strings.Cut(title, " · ")
	}
	return r.words(title)
}

// calmLevel is the colour of a threshold level: the author's warning and
// failure steps, and the accent for everything else that was read.
func (r *dashRender) calmLevel(level dashboard.Level) string {
	switch level {
	case dashboard.LevelBad:
		return r.pg.Calm.Fail
	case dashboard.LevelWarn:
		return r.pg.Calm.Warn
	}
	return r.pg.Calm.Accent
}

// dashHasRule reports whether the author gave the panel a step that reads as
// a warning or a failure: the rule its strip is drawn against.
func dashHasRule(p *dashboard.Panel) bool {
	for _, threshold := range p.Thresholds {
		at := threshold.Value
		switch {
		case threshold.Base:
			at = math.Inf(-1)
		case p.Percentage:
			at = p.Min + threshold.Value/100*(p.Max-p.Min)*(1+1e-9)
		}
		if step, ok := p.StepFor(at); ok && step.Level >= dashboard.LevelWarn {
			return true
		}
	}
	return false
}

func dashScale(p *dashboard.Panel) float64 {
	if p.Unit.Scale == 0 {
		return 1
	}
	return p.Unit.Scale
}

// calmValue words a stat or gauge of one series: the value with the
// author's threshold word, what the window held beyond it, and the worst
// level the window held. At a time it reads the sample taken then. brief
// is compare without its number, for a page with no room for it: "346ms
// green" alone would hide the stall the window held.
func (r *dashRender) calmValue(p *dashboard.Panel, line *dashLine, f chart.Frame, when time.Time) (number, compare, brief string, worst dashboard.Level) {
	unit := p.Unit.Chart
	for _, sample := range line.Raw {
		if step, ok := p.StepFor(sample.Value); ok && step.Level > worst {
			worst = step.Level
		}
	}
	v, ok := dashValueOf(p, line)
	if !ok {
		return r.words(graphNoSample), "", "", worst
	}
	if v.HasMaxLvl {
		compare, brief = "max "+unit.Format(v.Max)+" "+v.MaxStep.Word, "max "+v.MaxStep.Word
	}
	if !when.IsZero() {
		sample, found := f.ValueAt(&chart.Series{Points: line.Points}, when)
		if !found {
			return r.words(graphNoSample), compare, brief, worst
		}
		return dashSample(p, sample), compare, brief, worst
	}
	number = unit.Format(v.Value)
	if v.Word != "" {
		number += " " + v.Word
	}
	if v.HasStep {
		number += " " + v.Step.Word
	}
	return number, compare, brief, worst
}

// dashSample words one sample of a stat or gauge: its value with the
// author's threshold word.
func dashSample(p *dashboard.Panel, sample float64) string {
	number := p.Unit.Chart.Format(sample)
	if step, has := p.StepFor(sample / dashScale(p)); has {
		number += " " + step.Word
	}
	return number
}

// numberRoom is the width of the widest number a row writes while the
// cursor moves through its window. The kit gives a column the width of the
// widest text in it, and the strips and the plot what the columns leave
// them, so a number that changed its width under the cursor would move
// them. A group is given this width as the least its number column has
// (calm.Group.NumberWidth, as the pages of :graph do), and a sample stays
// in the cell it has. It counts every sample in the words of sample,
// and the other texts the row can write (the number the page opens with,
// the words for a time without a sample). It is no more than the kit gives
// a number.
func numberRoom(points []chart.Point, sample func(v float64) string, others ...string) int {
	room := 0
	for _, text := range others {
		room = max(room, runewidth.StringWidth(text))
	}
	for _, point := range points {
		room = max(room, runewidth.StringWidth(sample(point.V)))
	}
	return min(room, dashCalmNumber)
}

// lineRoom is numberRoom for a line read with lineValue: its samples, the
// value it ends with, and what it says before its first sample and after
// its last.
func (r *dashRender) lineRoom(p *dashboard.Panel, points []chart.Point, f chart.Frame) int {
	last, _ := r.lineValue(p, points, f, time.Time{})
	early, _ := r.lineValue(p, points, f, f.Start)
	late, _ := r.lineValue(p, points, f, f.End.Add(-f.Step))
	return numberRoom(points, p.Unit.Chart.Format, last, early, late, r.words(graphNoSample))
}

// calmRow is a panel as one row: its title, its strip and its number. A
// stat or gauge is its value with the author's threshold word, drawn
// against the author's rule. A panel of several series is its highest
// one, named beside the number. worst is the worst level the author's
// rule gave the window. brief is what the number alone would hide, in two
// words: that the window held another step ("max red"), or the peak of a
// line that ends low ("peak 40"). Without compare it is all the row says
// beside its number. Only the number follows the cursor. What stands
// beside it is the window's, and room is the widest the number is at any
// time of the window: the columns of the page are the same wherever the
// cursor stands.
func (r *dashRender) calmRow(index int, f chart.Frame, when time.Time, compare bool) (row calm.Row, brief string, worst dashboard.Level, room int) {
	p := &r.s.Dash.Panels[index]
	data := r.data(index)
	row = calm.Row{What: r.rowTitle(index), Color: r.pg.Calm.Accent}
	if len(data.States)+len(data.Incomplete) > 0 {
		// Drawn from some of its queries: the zoomed panel says which.
		row.Tag = "partial"
	}
	if p.Kind.Value() && len(data.Lines) == 1 {
		line := &data.Lines[0]
		number, beside, short, level := r.calmValue(p, line, f, when)
		last, _, _, _ := r.calmValue(p, line, f, time.Time{})
		room = numberRoom(line.Points, func(v float64) string { return dashSample(p, v) }, last, r.words(graphNoSample))
		row.Number, row.Color, row.Compare, brief, worst = number, r.calmLevel(level), short, short, level
		if compare {
			row.Compare = beside
		}
		if dashHasRule(p) {
			scale := dashScale(p)
			row.Strip = calm.Levels(line.Points, func(v float64) bool {
				step, _ := p.StepFor(v / scale)
				return step.Level >= dashboard.LevelWarn
			})
			return row, brief, worst, room
		}
		row.Strip = calm.Trend(line.Points)
		if p.Kind != dashboard.KindStat {
			// A gauge reads against its range.
			_, high := r.span(index)
			row.Strip = row.Strip.Top(high)
		}
		return row, brief, worst, room
	}
	line := &data.Lines[dashHighest(data.Lines)]
	value, _ := r.lineValue(p, line.Points, f, when)
	// The peak is the window's: it stays beside the sample at the cursor.
	_, peak := r.lineValue(p, line.Points, f, time.Time{})
	room = r.lineRoom(p, line.Points, f)
	name := ""
	if len(data.Lines) > 1 || line.Name != "value" {
		name = line.Name
	}
	row.Number = value
	if named := runewidth.StringWidth(name) + 1; name != "" && named+room <= dashCalmNumber {
		// The number of a panel of several series says whose it is, when
		// the widest number of the window leaves the name room.
		row.Number, name, room = name+" "+value, "", named+room
	}
	// A strip is drawn to its own highest sample, so without the peak it
	// has no scale: a page with no room for more still says it where the
	// number alone would hide a burst.
	row.Compare, brief = peak, peak
	if compare {
		parts := make([]string, 0, 3)
		if peak != "" {
			parts = append(parts, peak)
		}
		if others := len(data.Lines) + data.Cut - 1; others > 0 {
			// The row is one series of the panel, and says how many it is not.
			parts = append(parts, fmt.Sprintf("+%d more", others))
		}
		// A name too long for the number column takes the room the peak
		// and the count leave it: they are never cut for it. Of several
		// series the name says which one the row is, so it may be cut short;
		// the name of a panel's only series is written whole or not at all.
		rest := strings.Join(parts, " · ")
		left := dashCalmBeside - runewidth.StringWidth(rest) - 3
		switch {
		case name == "":
		case rest == "":
			parts = []string{name}
		case left >= runewidth.StringWidth(name), left >= 6 && len(data.Lines) > 1:
			parts = append([]string{dashFit(name, left, r.pg.ASCII)}, parts...)
		}
		row.Compare = strings.Join(parts, " · ")
	}
	row.Strip = calm.Trend(line.Points)
	return row, brief, worst, room
}

// sections are the panels beside the main chart, grouped as the dashboard
// groups them: a row title is the label of a group, and panels under no
// row title stand under ALSO. folded writes each group as one line. The
// one line of a group, folded here or by the kit on a short page, names
// first the panel whose window held the worst step of its author's rule,
// and of each panel first what its number alone would hide: a line that
// is cut short or counts the rest never loses that.
func (r *dashRender) sections(main int, drawn []int, f chart.Frame, when time.Time, compare, folded bool) []calm.Group {
	var groups []calm.Group
	// levels holds, per group, the worst level of each panel.
	var levels [][]dashboard.Level
	for _, index := range drawn {
		if index == main {
			continue
		}
		label := dashAlso
		if section := safeProviderText(r.s.Dash.Panels[index].Section); section != "" {
			label = strings.ToUpper(r.words(section))
		}
		if n := len(groups); n == 0 || groups[n-1].Label != label {
			// The first group is the top of the dashboard: it keeps its rows
			// on a short screen, where the groups under it fold to one line.
			role := calm.Secondary
			if n == 0 && !folded {
				role = calm.Primary
			}
			groups = append(groups, calm.Group{Label: label, Role: role})
			levels = append(levels, nil)
		}
		g := &groups[len(groups)-1]
		row, brief, worst, room := r.calmRow(index, f, when, compare)
		item := row.What + " " + row.Number
		if brief != "" {
			item = row.What + " " + brief + ", " + row.Number
		}
		g.Summary = append(g.Summary, r.words(item))
		levels[len(levels)-1] = append(levels[len(levels)-1], worst)
		if !folded {
			g.NumberWidth = max(g.NumberWidth, room)
			g.Rows = append(g.Rows, row)
		}
	}
	for i := range groups {
		// The dashboard's order among panels of one level.
		summary, level := groups[i].Summary, levels[i]
		order := make([]int, len(summary))
		for k := range order {
			order[k] = k
		}
		sort.SliceStable(order, func(a, b int) bool { return level[order[a]] > level[order[b]] })
		sorted := make([]string, len(summary))
		for k, from := range order {
			sorted[k] = summary[from]
		}
		groups[i].Summary = sorted
	}
	return groups
}

// dashBrief shortens the reason a panel was not drawn to a few words for a
// folded line; the s page and the zoomed panel keep every word.
func dashBrief(reason string) string {
	if kind, ok := strings.CutPrefix(reason, `panel type "`); ok {
		kind, _, _ = strings.Cut(kind, `"`)
		return kind
	}
	if rest, ok := strings.CutPrefix(reason, "variable "); ok {
		name, what, _ := strings.Cut(rest, " ")
		what, _, _ = strings.Cut(strings.TrimPrefix(what, "is "), ":")
		return name + " " + what
	}
	if strings.HasPrefix(reason, "not read: one read") {
		return "over the limit of one read"
	}
	for _, stop := range []string{": ", " (", ", "} {
		if at := strings.Index(reason, stop); at > 0 {
			reason = reason[:at]
		}
	}
	return reason
}

// calmCounts is how the read of a page went, panel by panel.
type calmCounts struct {
	total, drawn int
	// ruled counts panels the definition or the selection ruled out, limit
	// those a limit of one read left out, failed those whose queries were
	// not answered, empty those whose queries matched nothing, partial the
	// drawn panels that miss one of their queries.
	ruled, limit, failed, empty, partial int
	// notDrawn, notRead and noSeries name them for the folded lines.
	notDrawn, notRead, noSeries []string
}

func (r *dashRender) calmCount() calmCounts {
	var c calmCounts
	for i := range r.s.Dash.Panels {
		p, pp := &r.s.Dash.Panels[i], &r.s.Plan.Panels[i]
		if p.Kind == dashboard.KindRow {
			continue
		}
		c.total++
		title := r.rowTitle(i)
		switch reason := r.reason(i); {
		case reason == "":
			c.drawn++
			if data := r.data(i); len(data.States)+len(data.Incomplete) > 0 {
				c.partial++
			}
		case strings.HasPrefix(pp.NotDrawn, "not read: "):
			c.limit++
			c.notDrawn = append(c.notDrawn, title+" ("+dashBrief(reason)+")")
		case pp.NotDrawn != "":
			c.ruled++
			c.notDrawn = append(c.notDrawn, title+" ("+dashBrief(safeProviderText(reason))+")")
		case strings.HasPrefix(reason, "no series matched"):
			c.empty++
			c.noSeries = append(c.noSeries, title)
		default:
			c.failed++
			c.notRead = append(c.notRead, title+" ("+dashBrief(reason)+")")
		}
	}
	if cut := r.s.Dash.PanelsCut; cut > 0 {
		c.limit += cut
		c.notDrawn = append(c.notDrawn, fmt.Sprintf("%d more panels of the definition (limit %d)", cut, dashboard.MaxPanels))
	}
	return c
}

// verdict says what was read, and how much of the page that is. A
// dashboard shows; it finds nothing, and the sentence does not either.
func (c *calmCounts) verdict(noun, nouns string) calm.Verdict {
	first := fmt.Sprintf("%d of %d %s read.", c.drawn, c.total, nouns)
	switch {
	case c.total == 1 && c.drawn == 1:
		first = "The " + noun + " was read."
	case c.drawn == c.total:
		first = fmt.Sprintf("All %d %s read.", c.total, nouns)
	}
	say := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	if c.partial > 0 {
		// Drawn from some of its queries: the line over the chart and the
		// zoomed panel name the query that is missing.
		first = strings.TrimSuffix(first, ".") + fmt.Sprintf(", %d only in part.", c.partial)
	}
	// One sentence for everything that is not on the page as a chart or a
	// row; the lines under the axis name each panel.
	var rest []string
	if c.ruled > 0 {
		rest = append(rest, fmt.Sprintf("%d cannot be drawn here", c.ruled))
	}
	if c.limit > 0 {
		rest = append(rest, say(c.limit, "is", "are")+" over the limit of one read")
	}
	if c.failed > 0 {
		rest = append(rest, say(c.failed, "was", "were")+" not answered")
	}
	if c.empty > 0 {
		rest = append(rest, fmt.Sprintf("%d matched no series (not a zero)", c.empty))
	}
	v := calm.Verdict{Sentences: []string{first}, Caveat: dashCalmCaveat}
	if len(rest) > 0 {
		v.Sentences = append(v.Sentences, strings.Join(rest, ", ")+".")
	}
	return v
}

// groups are the folded lines of what is not on the page as a row. They
// are never left out: a panel that was not read is not a quiet panel.
func (c *calmCounts) groups(warn string) []calm.Group {
	var out []calm.Group
	if len(c.notDrawn) > 0 {
		out = append(out, calm.Group{Label: dashNotDrawn, Role: calm.NotReadRole, Summary: c.notDrawn})
	}
	if len(c.notRead) > 0 {
		out = append(out, calm.Group{Label: dashUnread, Role: calm.NotReadRole, Color: warn, Summary: c.notRead})
	}
	if len(c.noSeries) > 0 {
		out = append(out, calm.Group{Label: dashNoSeries, Role: calm.NotReadRole, Color: warn, Summary: c.noSeries})
	}
	return out
}

// calmLines are the lines of the main chart. keep are the ones it draws:
// all of them, or the dashCalmLines with the highest peak, each in one of
// the data colours. ranked lists every line, the highest peak first.
func (r *dashRender) calmLines(index int) (keep dashData, ranked []dashLine) {
	keep = r.data(index)
	keep.Lines = append([]dashLine(nil), keep.Lines...)
	ranked = append([]dashLine(nil), keep.Lines...)
	peak := func(line *dashLine) float64 {
		top, _ := dashPeak(line.Points)
		return top
	}
	sort.SliceStable(ranked, func(a, b int) bool { return peak(&ranked[a]) > peak(&ranked[b]) })
	if len(keep.Lines) <= dashCalmLines {
		return keep, ranked
	}
	chosen := map[int]bool{}
	for i := range ranked[:dashCalmLines] {
		chosen[ranked[i].Slot] = true
	}
	lines := keep.Lines[:0]
	for i := range keep.Lines {
		if chosen[keep.Lines[i].Slot] {
			keep.Lines[i].Tint = len(lines)
			lines = append(lines, keep.Lines[i])
		}
	}
	keep.Lines = lines
	for i := range ranked {
		ranked[i].Tint = -1
		for k := range keep.Lines {
			if keep.Lines[k].Slot == ranked[i].Slot {
				ranked[i].Tint = keep.Lines[k].Tint
			}
		}
	}
	return keep, ranked
}

// calmHead is the line over the main chart: the panel's name, the time at
// the cursor, and the legend on one line, the highest line first. What
// does not fit is counted.
func (r *dashRender) calmHead(index int, keep *dashData, ranked []dashLine, f chart.Frame, when time.Time, one, rows bool) string {
	p := &r.s.Dash.Panels[index]
	o, pal := r.calmOpts(), r.pg.Calm
	room := r.width
	if room >= 100 {
		room -= calm.Indent
	}
	label := strings.ToUpper(r.words(safeProviderText(p.Title)))
	if q := r.s.Spec.Query; q != nil && q.Name == "" && !r.s.Spec.Board {
		// The expression is in the title line; capitals would change it.
		label = "TYPED EXPRESSION"
	}
	// A piece is text in colours: [colour, text] pairs written without a
	// space between them.
	type piece [][2]string
	width := func(pc piece) int {
		total := 0
		for _, part := range pc {
			total += runewidth.StringWidth(part[1])
		}
		return total
	}
	// notes are what is said of the chart before its legend; each is
	// written whole or left out, the first ones first.
	var notes, legend []piece
	if word := p.Unit.Word(); word != "" && (one || !p.Unit.Known) {
		notes = append(notes, piece{{pal.Muted, r.words(safeProviderText(word))}})
	}
	if !when.IsZero() {
		notes = append(notes, piece{{pal.Text, "at " + graphClock(when)}})
	}
	if len(ranked) > len(keep.Lines) {
		// The chart draws the highest; the count says so in words.
		notes = append(notes, piece{{pal.Muted, fmt.Sprintf("%d highest of %d series", len(keep.Lines), len(ranked))}})
	}
	if keep.Cut > 0 {
		notes = append(notes, piece{{pal.Muted, fmt.Sprintf("+%d series not drawn", keep.Cut)}})
	}
	for _, state := range keep.States {
		notes = append(notes, piece{{pal.Warn, "query " + dashShortState(state)}})
	}
	for _, state := range keep.Incomplete {
		ref, _, _ := strings.Cut(state, ": ")
		notes = append(notes, piece{{pal.Warn, "query " + ref + " incomplete"}})
	}
	switch {
	case p.Kind.Value() && len(ranked) == 1:
		number, beside, _, worst := r.calmValue(p, &ranked[0], f, when)
		legend = append(legend, piece{{r.calmLevel(worst), strings.TrimSpace(number + "  " + beside)}})
	case rows:
		// The rows under the chart are its legend.
	default:
		for i := range ranked {
			line := &ranked[i]
			if line.Tint < 0 {
				continue
			}
			value, peak := r.lineValue(p, line.Points, f, when)
			text := strings.TrimSpace(line.Name + " " + strings.TrimSpace(value+" "+peak))
			if len(keep.Lines) < 2 {
				legend = append(legend, piece{{pal.Text, text}})
				continue
			}
			color := ""
			if len(r.c.Data) > 0 {
				color = r.c.Data[line.Tint%len(r.c.Data)]
			}
			legend = append(legend, piece{{color, string(rune('a' + line.Slot%26))}, {pal.Text, " " + text}})
		}
	}
	// Two cells between the parts, never three: the kit runs the cursor
	// through a blank cell that stands between two blank cells, and a line
	// of words is no place for it.
	const noteGap, entryGap = 2, 2
	more := func(left int) piece { return piece{{pal.Muted, fmt.Sprintf("+%d more", left)}} }
	// The name gives way to the notes and to the first line of the legend:
	// which line stands highest says more than the end of a title.
	need := 0
	for _, note := range notes {
		need += noteGap + width(note)
	}
	if len(legend) > 0 {
		need += entryGap + width(legend[0])
		if len(legend) > 1 {
			need += entryGap + width(more(len(legend)-1))
		}
	}
	label = dashFit(label, max(min(room-calm.Indent-need, room-calm.Indent), min(12, room-calm.Indent)), r.pg.ASCII)
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", calm.Indent) + calm.HeadingText(label, pal.Accent, o))
	used := calm.Indent + runewidth.StringWidth(label)
	add := func(gap int, pc piece) bool {
		if used+gap+width(pc) > room {
			return false
		}
		b.WriteString(strings.Repeat(" ", gap))
		for _, part := range pc {
			b.WriteString(calmText(part[0], part[1]))
		}
		used += gap + width(pc)
		return true
	}
	for _, note := range notes {
		add(noteGap, note)
	}
	for i, entry := range legend {
		// Room is kept for the count of what follows.
		left := len(legend) - i - 1
		reserve := 0
		if left > 0 {
			reserve = entryGap + width(more(left))
		}
		if used+entryGap+width(entry)+reserve > room || !add(entryGap, entry) {
			add(entryGap, more(left+1))
			break
		}
	}
	return b.String()
}

// seriesGroup lists the series of a query under its chart, the highest
// peak first: the letter the chart ends its line with, the name, the last
// value, the peak, and the strip, all on the chart's scale. A series the
// chart has no room for is a row without a letter.
func (r *dashRender) seriesGroup(index int, ranked []dashLine, f chart.Frame, when time.Time) calm.Group {
	p := &r.s.Dash.Panels[index]
	// The word over the numbers is as wide with the cursor as without it,
	// and each number as wide as the widest of its window: the strips do
	// not move when the cursor does. The line over the chart says the time;
	// the caret is the one on the axis.
	g := calm.Group{Label: "SERIES BY PEAK", Head: "last", Role: calm.Primary}
	if !when.IsZero() {
		g.Head = "at ^"
	}
	top := 0.0
	for i := range ranked {
		peak, _ := dashPeak(ranked[i].Points)
		top = math.Max(top, peak)
	}
	for i := range ranked {
		line := &ranked[i]
		value, _ := r.lineValue(p, line.Points, f, when)
		peak, _ := dashPeak(line.Points)
		row := calm.Row{What: line.Name, Number: value, Compare: "peak " + p.Unit.Chart.Format(peak),
			Strip: calm.Trend(line.Points).Top(top)}
		g.NumberWidth = max(g.NumberWidth, r.lineRoom(p, line.Points, f))
		if line.Tint >= 0 {
			row.Subject = string(rune('a' + line.Slot%26))
			if len(r.c.Data) > 0 {
				row.Color = r.c.Data[line.Tint%len(r.c.Data)]
			}
		}
		g.Rows = append(g.Rows, row)
	}
	return g
}

// queryVerdict is the neutral sentence of a query page: how many series
// came back, which stands highest and when. It reads; it does not judge.
func (r *dashRender) queryVerdict(index int, keep *dashData, ranked []dashLine) calm.Verdict {
	unit := r.s.Dash.Panels[index].Unit.Chart
	line := &ranked[0]
	peak, at := dashPeak(line.Points)
	low := peak
	for _, point := range line.Points {
		low = math.Min(low, point.V)
	}
	what := "peak " + unit.Format(peak) + " at " + at.UTC().Format("15:04")
	if low == peak {
		what = unit.Format(peak) + " throughout"
	}
	first := fmt.Sprintf("%d series; highest %s, %s.", len(ranked), line.Name, what)
	switch {
	case len(ranked) == 1 && line.Name == "value":
		first = "1 series, " + what + "."
	case len(ranked) == 1:
		first = "1 series: " + line.Name + ", " + what + "."
	}
	v := calm.Verdict{Sentences: []string{first}, Caveat: dashCalmCaveat}
	var rest []string
	if keep.Cut > 0 {
		rest = append(rest, fmt.Sprintf("%d more series are not drawn (limit %d).", keep.Cut, dashboard.MaxLines))
	}
	for _, state := range keep.Incomplete {
		rest = append(rest, "The answer was cut ("+state+").")
	}
	if len(rest) > 0 {
		v.Sentences = append(v.Sentences, strings.Join(rest, " "))
	}
	return v
}

// calmProblem is the page of one query that drew nothing: the reason is
// the verdict, in the words of the source, and never an empty plot.
func (r *dashRender) calmProblem() []string {
	index := r.pg.Selected
	data := r.data(index)
	problem, advice := r.undrawn(index, &data)
	v := calm.Verdict{Kind: calm.NotRead, Sentences: []string{dashNotDrawn + ": " + problem + "."}, Caveat: "Not read is not healthy."}
	if r.s.Plan.Panels[index].NotDrawn == "" && strings.HasPrefix(data.Problem, "no series matched") {
		v = calm.Verdict{Kind: calm.NothingFound, Sentences: []string{"No series matched: missing data, not a zero."}, Caveat: "Nothing found is not proof of health."}
		advice = strings.TrimPrefix(advice, "The query answered and matched nothing. That is missing data, not a zero: ")
		if first, size := utf8.DecodeRuneInString(advice); size > 0 {
			advice = string(unicode.ToUpper(first)) + advice[size:]
		}
	}
	v.Sentences = append(v.Sentences, advice)
	next := []calm.Action{{Key: "e", Label: "edit the expression"}, {Key: calm.QueriesKey, Label: "the query as it was sent"},
		{Key: calmGrafanaKeyName, Label: calmNextGrafana}}
	return r.calmWords(r.calmTitle(), v, next, (&calmKeyLine{Window: calmKeyWindow, Detail: "all detail", Reread: true}).parts())
}

// calmKeys are the two key lines: the likely next steps in words, and the
// rest in one dim line that ends with the way out.
func (r *dashRender) calmKeys(main int, drawn []int, one bool) (next []calm.Action, keys []string) {
	if one {
		next = []calm.Action{{Key: "e", Label: "edit the expression"}, {Key: calm.QueriesKey, Label: "queries, and how to save this one"},
			{Key: calmGrafanaKeyName, Label: calmNextGrafana}}
		line := calmKeyLine{Time: true, Window: calmKeyWindow, Detail: "all detail", Own: []string{chartStyleHint(r.pg.Style)}, Reread: true}
		return next, line.parts()
	}
	if len(drawn) > 1 && main >= 0 {
		at := 0
		for i, index := range drawn {
			if index == main {
				at = i
			}
		}
		name := dashFit(r.rowTitle(drawn[(at+1)%len(drawn)]), 28, r.pg.ASCII)
		next = append(next, calm.Action{Key: calm.NextChartKey, Label: "next chart (" + name + ")"})
	}
	if _, total := r.s.order(0); total > 1 {
		next = append(next, calm.Action{Key: "Enter", Label: "zoom"})
	}
	all := "all panels"
	if r.s.Spec.Board {
		all = "all charts"
	}
	next = append(next, calm.Action{Key: calm.DetailKey, Label: all}, calm.Action{Key: calmGrafanaKeyName, Label: calmNextGrafana})
	line := calmKeyLine{Time: main >= 0, Window: calmKeyWindow, Queries: true, Reread: true}
	if len(drawn) > 1 {
		line.Move = "↑↓ chart"
	}
	if main >= 0 {
		line.Own = append(line.Own, chartStyleHint(r.pg.Style))
	}
	return next, line.parts()
}

// calm lays the page out the calm way: what was read, one panel at full
// size, every other panel as one row on the same axis, and what was not
// drawn as one line each. chosen is the panel the keys made the main
// chart. The page settles Selected on the main chart, so Enter zooms it,
// and Mains on the panels the next-chart key steps through.
func (r *dashRender) calm(chosen int) {
	s, pg, o := r.s, r.pg, r.calmOpts()
	one := s.Spec.isQuery() && !s.Spec.Board
	drawn := r.drawnPanels()
	main := r.calmMain(chosen, drawn)
	pg.Mains = drawn
	if main >= 0 {
		pg.Selected = main
	}
	if one && main < 0 {
		r.lines = r.calmProblem()
		return
	}
	frame := chart.Frame{Start: s.Start, End: s.End, Step: s.Step}
	// The cursor at the window's end is not drawn: the page then reads
	// each line where it ends, and says when a line stood well above that.
	// It never stands on the window's start, the one sample with no column
	// on this page: a cursor left there by a zoomed panel reads the first
	// sample that has one.
	var when time.Time
	if steps := int(s.Window / s.Step); pg.Cursor >= 0 && pg.Cursor < steps {
		when = s.Start.Add(time.Duration(max(pg.Cursor, dashCalmFirst)) * s.Step)
	}
	counts := r.calmCount()
	noun, nouns := "panel", "panels"
	if s.Spec.isQuery() {
		noun, nouns = "named query", "named queries"
	}
	build := func(compare, folded bool) calm.Page {
		page := calm.Page{Title: r.calmTitle(), Verdict: counts.verdict(noun, nouns), Frame: frame, Bands: calm.Bands{Cursor: when}, NumberAfter: true}
		if main >= 0 {
			keep, ranked := r.calmLines(main)
			rows := one && len(ranked) > 1
			panel := r.chartPanelOf(main, &keep)
			options := chart.Options{ASCII: pg.ASCII, Cursor: -1, AxisColor: r.c.Axis, CursorColor: r.c.Cursor, Style: pg.Style}
			page.Chart = &calm.Chart{Head: r.calmHead(main, &keep, ranked, frame, when, one, rows), Gutter: panel.GutterWidth(), Max: 12,
				Draw: func(f chart.Frame, gutter, height int) []string { return panel.Render(f, height, gutter, options) }}
			if one {
				// The chart is what a query page is for: it keeps six rows
				// before the blank rows around it go.
				page.Verdict, page.Chart.Min, page.Chart.Max = r.queryVerdict(main, &keep, ranked), 6, dashMaxPlot+6
			}
			if rows {
				page.Groups = append(page.Groups, r.seriesGroup(main, ranked, frame, when))
			}
			if folded {
				// Alone on the page, the chart takes the rows the blank rows
				// would.
				page.Chart.Min = 6
			}
		}
		if !one {
			page.Groups = append(page.Groups, r.sections(main, drawn, frame, when, compare, folded)...)
			page.Groups = append(page.Groups, counts.groups(pg.Calm.Warn)...)
		}
		page.Next, page.Keys = r.calmKeys(main, drawn, one)
		return page
	}
	page := build(r.width >= dashCalmCompare, false)
	plan := calm.Lay(&page, r.width, max(pg.Height, 1), o)
	// lost says a row on the page has no room for what stands beside its
	// number.
	lost := false
	for i := range page.Groups {
		if plan.Folded[i] || plan.Hidden[i] {
			continue
		}
		for k := range page.Groups[i].Rows[:plan.Rows[i]] {
			lost = lost || page.Groups[i].Rows[k].Compare != ""
		}
	}
	lost = lost && plan.Layout.Compare == 0
	if !one && page.Chart != nil && (plan.Layout.Strip < dashCalmFloor || lost) {
		// Too narrow for a chart beside the rows, or for what a row must
		// say beside its number: the sections fold to lines that say it in
		// words, and the chart takes the width. A page too short for that
		// form keeps the one it has.
		folded := build(false, true)
		if lines := calm.Lay(&folded, r.width, max(pg.Height, 1), o); !lines.TooSmall || plan.TooSmall {
			page, plan = folded, lines
		}
	}
	r.lines = calm.Render(&page, &plan, o).Lines()
}
