// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/ui"
)

// The calm page of :impact. The answer comes first, as a person would say
// it: how many signals got worse after the rollout and which kinds, then
// what the revision changed. It repeats no number of the rows under it.
// Every signal that moved is one row: its name, before -> after, the
// change, and a strip over both compared windows in which the rollout is
// one band through every row. Signals that did not move are one dim line
// of names; signals without a comparison are one line each and are never
// left out. The rule, the sample counts and the queries of the selected
// row and the full diff of the revision are under s, and the table this
// view had before, with the sentence that carries the numbers, is under a.
const (
	impactQueriesKey   = ui.KeyS
	impactQueriesLabel = "Queries, rules and samples"

	impactWorseLabel     = "WORSE AFTER"
	impactBetterLabel    = "BETTER AFTER"
	impactMovedLabel     = "HIGHER OR LOWER AFTER"
	impactUnchangedLabel = "UNCHANGED"
	impactNotReadLabel   = "NOT READ"
	impactMissingLabel   = "NOT AVAILABLE"
	impactTooFewLabel    = "TOO FEW SAMPLES"

	// impactTallPage is the height from which the block of s also states
	// the source and the windows of the whole comparison.
	impactTallPage = 28

	// impactNumberRoom is the widest number column the kit writes whole.
	// Before and after are set in two columns only where that fits it.
	impactNumberRoom = 16

	// impactTimelineKeyName is the key of the hand-off to :timeline, and
	// impactDetailWords what a shows, as the second key line says it.
	impactTimelineKeyName = "t"
	impactDetailWords     = "show every signal"
	// impactRolloutsKeys are the keys that step through the retained
	// rollouts, and impactRolloutsShort the same on a narrow line.
	impactRolloutsKeys  = "n/N later/earlier rollout"
	impactRolloutsShort = "n/N rollout"

	impactOrderCaveat  = "Order in time, not proof of cause."
	impactUnreadCaveat = "Not read is not zero and not healthy."
)

// impactClass is the group a signal belongs to on the calm page.
func impactClass(outcome *impact.Outcome) string {
	switch {
	case outcome.State == impact.NotRead:
		return impactNotReadLabel
	case outcome.State == impact.NotAvailable:
		return impactMissingLabel
	case outcome.State != impact.Compared:
		return impactTooFewLabel
	case outcome.Verdict == impact.Worse:
		return impactWorseLabel
	case outcome.Verdict == impact.Better:
		return impactBetterLabel
	case outcome.Verdict == impact.Unchanged:
		return impactUnchangedLabel
	}
	return impactMovedLabel
}

// impactRowLabels are the groups drawn as rows, in the order they are
// drawn; the cursor steps through their signals.
var impactRowLabels = []string{impactWorseLabel, impactBetterLabel, impactMovedLabel}

// calmOrder is the signals the cursor steps through on the calm page: the
// ones that are rows, top to bottom.
func (v *impactView) calmOrder() []string {
	if v.read == nil || v.read.report == nil {
		return nil
	}
	var out []string
	for _, label := range impactRowLabels {
		for index := range v.read.report.Outcomes {
			if outcome := &v.read.report.Outcomes[index]; impactClass(outcome) == label {
				out = append(out, outcome.Signal.ID)
			}
		}
	}
	return out
}

// moveFocus moves the cursor by step signals and stops at either end. On
// the calm page it steps through the rows; with all detail, through every
// signal of the table.
func (v *impactView) moveFocus(step int) {
	var order []string
	switch {
	case v.read == nil || v.read.report == nil:
		return
	case v.allDetail:
		for index := range v.read.report.Outcomes {
			order = append(order, v.read.report.Outcomes[index].Signal.ID)
		}
	default:
		order = v.calmOrder()
	}
	if len(order) == 0 {
		return
	}
	at := -1
	for index, id := range order {
		if id == v.focused {
			at = index
		}
	}
	next := min(max(at+step, 0), len(order)-1)
	if at < 0 {
		next = 0
	}
	if order[next] != v.focused {
		v.focused = order[next]
		v.render()
	}
}

// toggleDetail is a: the table and detail pane this view had before the
// calm layout, and back.
func (v *impactView) toggleDetail() {
	v.allDetail = !v.allDetail
	v.focusRow()
	v.arrange()
	v.render()
	v.app.SetFocus(v)
}

// focusRow puts the cursor of the calm page on a row. A signal that is
// only one of the names of a folded line has no row to mark, so the cursor
// moves to the first row when it is on one of those: after a new read, and
// when coming back from the table, where it reaches every signal.
func (v *impactView) focusRow() {
	order := v.calmOrder()
	if v.allDetail || len(order) == 0 {
		return
	}
	for _, id := range order {
		if id == v.focused {
			return
		}
	}
	v.focused = order[0]
}

func (v *impactView) toggleQueries() {
	v.queries = !v.queries
	v.render()
}

// arrange puts the widgets of the current layout into the view: the one
// calm page, or the header, table, detail pane and key line.
func (v *impactView) arrange() {
	v.Flex.Clear()
	if v.allDetail {
		v.Flex.AddItem(v.header, v.headerHeight, 0, false).
			AddItem(v.table, 0, 1, true).
			AddItem(v.detail, v.detailHeight, 0, false).
			AddItem(v.footer, 1, 0, false)
		return
	}
	v.Flex.AddItem(v.calm, 0, 1, true)
}

// impactStrip is the strip of a compared signal over both windows: its
// value in every column, on one scale from zero, so a signal that fell is
// high before the rollout and low after it. A sample is in the row's
// colour where it is further from the before value than the signal's rule
// allows, in the direction the signal moved, and dim where it is not.
// Restarts are counted, not levelled: each is a mark where it happened.
func impactStrip(outcome *impact.Outcome, step time.Duration) calm.Strip {
	signal := &outcome.Signal
	if len(outcome.Series) == 0 {
		return calm.Strip{}
	}
	if signal.Reduce == impact.ReduceIncrease {
		var at []time.Time
		for _, point := range outcome.Series {
			if point.V > 0 {
				// A sample stands for the step that ends at it.
				at = append(at, point.T.Add(-step/2))
			}
		}
		return calm.Events(at...)
	}
	direction, before := 1.0, outcome.Before.Value
	if outcome.Delta() < 0 {
		direction = -1
	}
	rule := math.Max(signal.Floor, impact.RelativeThreshold*math.Abs(before))
	points := make([]chart.Point, 0, len(outcome.Series))
	for _, point := range outcome.Series {
		points = append(points, chart.Point{T: point.T, V: point.V})
	}
	return calm.Trend(points).Rule(func(v float64) bool { return direction*(v-before) >= rule-1e-12 })
}

// impactChange is the change of a compared signal: absolute and relative.
func impactChange(outcome *impact.Outcome) (delta, ratio string) {
	delta = outcome.Signal.FormatDelta(outcome.Delta())
	if outcome.Delta() == 0 {
		delta = "0"
	}
	return delta, impact.PercentChange(outcome.Before.Value, outcome.After.Value)
}

// impactFigures are the numbers of one row before they are set in columns.
type impactFigures struct {
	before, after, delta, ratio string
}

// impactColumnsOf sets the numbers of the rows in columns that every row
// of the page shares, so that one kind of number is read down one column:
// before ends where the arrow stands and after starts behind it, and the
// change and its ratio each start at one column. The word over the column
// is moved with the arrow. Where two columns would be wider than the kit
// writes a number, before -> after is written as one text, as it is read.
func impactColumnsOf(figures []impactFigures) (head string, number, compare []string) {
	head = "before -> after"
	var before, after, delta, ratio int
	for _, f := range figures {
		before, after = max(before, tviewCells(f.before)), max(after, tviewCells(f.after))
		delta, ratio = max(delta, tviewCells(f.delta)), max(ratio, tviewCells(f.ratio))
	}
	before = max(before, len("before"))
	aligned := before+len(" -> ")+after <= impactNumberRoom
	if aligned {
		head = strings.Repeat(" ", before-len("before")) + head
	}
	for _, f := range figures {
		text := f.before + " -> " + f.after
		if aligned {
			text = strings.Repeat(" ", before-tviewCells(f.before)) + text + strings.Repeat(" ", after-tviewCells(f.after))
		}
		number = append(number, text)
		// Every row is as wide as the widest, so the columns, and with
		// them the strips, stand where they are whichever groups are rows.
		compare = append(compare, f.delta+strings.Repeat(" ", delta-tviewCells(f.delta))+"  "+f.ratio+strings.Repeat(" ", ratio-tviewCells(f.ratio)))
	}
	return head, number, compare
}

// impactOneLine is the first of the forms of a sentence that is one line
// of the answer at this width, and the last of them when none is: the
// fullest form comes first.
func impactOneLine(width int, o calm.Options, forms ...string) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	for _, form := range forms {
		if len(calm.VerdictLines(calm.Verdict{Sentences: []string{impactArrow(form, o)}}, width, o)) <= 1 {
			return form
		}
	}
	return forms[len(forms)-1]
}

// calmVerdict is the answer at the top: what the comparison came to, as a
// person would say it, then what the revision changed. Each sentence is
// one line where a form of it fits one: the kinds of signal are left out
// of the first when they do not fit beside it, because the rows name them;
// the two rates out of the sentence about a changed request rate, because
// its row or its folded line states them; and the values of the memory
// limit out of the last, because s states the whole diff. What is missing
// from the comparison is never left out.
func (v *impactView) calmVerdict(width int, o calm.Options) calm.Verdict { //nolint:gocritic // Options are passed by value, as the kit takes them.
	read := v.read
	// What the revision changed is a statement of its own: it starts a line.
	out := calm.Verdict{Apart: true}
	switch {
	case read == nil:
		return calm.Verdict{Sentences: []string{"Reading the rollout history, then the declared Prometheus source.",
			"Bounded, read-only, and only because this view was opened."}}
	case read.report == nil:
		out = calm.Unread("No comparison: "+read.err.Error()+".", "")
		out.Caveat, out.Apart = "No comparison is not a sign that nothing changed.", true
		if read.rollout != nil && read.rollout.Total > 1 {
			out.Sentences = append(out.Sentences, "n and N step to another retained rollout.")
		}
	default:
		report := read.report
		answer := report.Answer()
		out.Sentences = []string{impactOneLine(width, o, answer.Lead, answer.Brief)}
		if answer.Traffic != "" {
			// What limits the comparison follows the sentence it limits.
			out.Sentences = append(out.Sentences, impactOneLine(width, o, answer.Traffic, answer.TrafficBrief))
		}
		out.Sentences = append(out.Sentences, impactOneLine(width, o, answer.Changed, answer.ChangedBrief))
		worse, better, same, _ := report.Counts()
		out.Caveat = impactOrderCaveat
		switch {
		case report.Failure != "" || worse+better+same == 0:
			out.Kind, out.Caveat = calm.NotRead, impactUnreadCaveat
		case worse == 0:
			out.Kind = calm.NothingFound
		}
	}
	// What makes the page unsafe to read as it stands comes before it.
	var first []string
	if !v.contextCurrent() {
		first = append(first, "Context changed; this comparison is retained. Reopen :impact for the current context.")
	}
	if read.incomplete != "" {
		// A cut revision list can hide the real latest rollout.
		first = append(first, read.incomplete)
	}
	if len(first) > 0 {
		out.Kind = calm.NotRead
		out.Sentences = append(first, out.Sentences...)
	}
	return out
}

// calmTitle is what is compared and how much of it was read.
func (v *impactView) calmTitle() calm.Title {
	w := v.workload()
	title := calm.Title{View: impactTitle, Object: calmObject(v.app.Config.ActiveNamespace(), w.Kind, w.Namespace, w.Name)}
	read := v.read
	if read != nil && read.rollout != nil {
		r := read.rollout
		where := fmt.Sprintf("latest of %d", r.Total)
		if r.Back > 0 {
			where = fmt.Sprintf("%d before the latest of %d", r.Back, r.Total)
		}
		title.Window = r.Label() + " (" + where + ")"
	}
	switch {
	case v.loading && read != nil:
		title.Busy = "reading again; this comparison is retained"
	case v.loading || read == nil:
		title.Busy = "reading…"
	case read.report != nil && read.report.Asked > 0:
		title.Read, title.Done, title.Total = read.report.ReadAt, read.report.Answered, read.report.Asked
	}
	return title
}

// impactRows is the signals of a comparison as the page groups them.
type impactRows struct {
	// rows are the rows of a group, short what a group of rows says when
	// it is folded to one line, and names the signals of a group.
	rows         map[string][]calm.Row
	short, names map[string][]string
	// head is the word over the column of before and after.
	head string
	// subject is the width of the widest name of a row, in cells.
	subject int
	// reason is why signals were not read, when one reason holds for all
	// of them.
	reason string
}

// calmRows sorts the signals of a report into the groups of the page and
// makes a row of each that moved. The numbers of the rows are set in
// columns once all of them are known.
func (v *impactView) calmRows(report *impact.Report, p *calm.Palette) impactRows {
	set := impactRows{rows: map[string][]calm.Row{}, short: map[string][]string{}, names: map[string][]string{}}
	type place struct {
		class string
		row   int
	}
	var (
		figures []impactFigures
		places  []place
	)
	reasons := map[string]bool{}
	for index := range report.Outcomes {
		outcome := &report.Outcomes[index]
		signal := &outcome.Signal
		class := impactClass(outcome)
		set.names[class] = append(set.names[class], signal.Label)
		if class == impactNotReadLabel {
			reasons[outcome.Reason], set.reason = true, outcome.Reason
		}
		if outcome.State != impact.Compared || outcome.Verdict == impact.Unchanged {
			continue
		}
		color := p.Accent
		switch class {
		case impactWorseLabel:
			color = p.Fail
		case impactBetterLabel:
			color = p.Info
		}
		figure := impactFigures{before: signal.Format(outcome.Before.Value), after: signal.Format(outcome.After.Value)}
		figure.delta, figure.ratio = impactChange(outcome)
		figures = append(figures, figure)
		places = append(places, place{class: class, row: len(set.rows[class])})
		set.short[class] = append(set.short[class], signal.Label+" "+figure.before+" -> "+figure.after)
		set.rows[class] = append(set.rows[class], calm.Row{
			Subject: signal.Label, Color: color, Strip: impactStrip(outcome, report.Step), Selected: signal.ID == v.focused,
		})
	}
	if len(reasons) != 1 {
		set.reason = ""
	}
	head, number, compare := impactColumnsOf(figures)
	set.head = head
	for index, at := range places {
		row := &set.rows[at.class][at.row]
		row.Number, row.Compare = number[index], compare[index]
		set.subject = max(set.subject, tviewCells(row.Subject))
	}
	return set
}

// calmPage builds the page from the retained comparison.
func (v *impactView) calmPage(width int, o calm.Options) calm.Page { //nolint:gocritic // Options are passed by value, as the kit takes them.
	p := o.Palette
	// The rows of this page are many: a single row to spare sets the
	// answer apart from them before it sets the keys apart.
	page := calm.Page{Title: v.calmTitle(), Verdict: v.calmVerdict(width, o), AnswerApart: true}
	read := v.read
	page.Keys = v.calmRest(width, o, nil)
	if read == nil || read.report == nil {
		return page
	}
	report := read.report
	win := &report.Windows
	if report.Step > 0 && win.After.End.After(win.Before.Start) {
		page.Frame = chart.Frame{Start: win.Before.Start, End: win.After.End, Step: report.Step}
		// The rollout is a moment: one column at the split of the windows,
		// and a guide down through every row.
		page.Bands = calm.Bands{Label: report.Rollout.Label(), Guide: true,
			Episodes: []chart.Interval{{Start: win.Rollout, End: win.Rollout}}}
	}
	set := v.calmRows(report, &o.Palette)
	rows := set.rows
	headed := false
	for _, label := range impactRowLabels {
		if len(rows[label]) == 0 {
			continue
		}
		// The names are one column as wide as the widest of all rows: a
		// group that opens under the cursor, or a row a short page turns
		// back in, then moves no strip.
		group := calm.Group{Label: label, Rows: rows[label], Summary: set.short[label], SubjectWidth: set.subject}
		switch {
		case label == impactWorseLabel:
			group.Color = p.Warn
		case label == impactMovedLabel || len(rows[impactWorseLabel]) > 0:
			// What the sentence does not rest on folds first, and what
			// is only higher or lower before the page gives a blank row
			// up: the rows that are worse read better apart than beside
			// two more.
			group.Role, group.FoldFirst = calm.Secondary, label == impactMovedLabel
		}
		if !headed {
			group.Head, headed = set.head, true
		}
		for index := range group.Rows {
			if group.Rows[index].Selected {
				// The group the cursor is in never folds: its rows are
				// what the keys act on.
				group.Role = calm.Primary
			}
		}
		page.Groups = append(page.Groups, group)
	}
	if list := set.names[impactUnchangedLabel]; len(list) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: impactUnchangedLabel, Role: calm.QuietRole, Summary: list})
	}
	if list := set.names[impactNotReadLabel]; len(list) > 0 {
		// One reason is said once, before the names it holds for.
		if set.reason != "" {
			list = append([]string{set.reason + ": " + list[0]}, list[1:]...)
		}
		page.Groups = append(page.Groups, calm.Group{Label: impactNotReadLabel, Color: p.Warn, Role: calm.NotReadRole, Summary: list})
	}
	for _, label := range []string{impactMissingLabel, impactTooFewLabel} {
		if list := set.names[label]; len(list) > 0 {
			page.Groups = append(page.Groups, calm.Group{Label: label, Role: calm.NotReadRole, Summary: list})
		}
	}
	page.Next = v.calmNext(width, o)
	page.Keys = v.calmRest(width, o, page.Next)
	impactArrows(&page, o)
	return page
}

// calmRest is the second key line of the page, for the first key line it
// is drawn under. All detail, the queries and the timeline are on the
// first, because a short page gives the second one up; a first line too
// narrow for one of them leaves it to the second, in the words and the
// place every calm page has for it. A key that works stays named.
func (v *impactView) calmRest(width int, o calm.Options, next []calm.Action) []string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	read := v.read
	keys := calmKeyLine{}
	if read == nil {
		return keys.parts()
	}
	// A key that has nothing to step through is not named.
	rollouts := ""
	if read.rollout != nil && read.rollout.Total > 1 {
		rollouts = impactRolloutsKeys
	}
	keys.Own, keys.Reread = []string{rollouts}, true
	if read.report == nil {
		return keys.parts()
	}
	// Reading again is the first of the next actions when nothing was
	// read, and is named once. The first line then names the declaration
	// instead of the queries; s still has the diff of the revision, which
	// the answer only counts, so this line names it.
	failed := read.report.Failure != ""
	keys.Reread, keys.Queries = !failed, failed
	// The digits are the width compared on each side of the rollout here.
	keys.Window = "1-5 window"
	if len(v.calmOrder()) > 0 {
		keys.Move = "↑↓ signal"
	}
	timeline := ""
	for _, action := range next[calm.NextShown(next, width, o):] {
		switch action.Key {
		case calm.DetailKey:
			keys.Detail = impactDetailWords
		case string(rune(impactQueriesKey)):
			keys.Queries = true
		case impactTimelineKeyName:
			timeline = impactTimelineKeyName + " timeline"
		}
	}
	keys.Own = []string{rollouts, timeline, calmGrafanaKeyName + " Grafana"}
	if rollouts != "" && !strings.Contains(calm.RestLine(keys.parts(), width, o), rollouts) {
		// The line drops from the right, and a part too long for it takes
		// what stands behind it along: the keys of the rollouts are then
		// said in the words the table under a has for them.
		keys.Own[0] = impactRolloutsShort
	}
	return keys.parts()
}

// impactArrow writes "before -> after" the way every calm page writes a
// change: with an arrow, and in plain characters with noIcons. The words
// of the comparison are written with "->" where they are made; the page
// is given them in the form it draws, because its columns are measured
// from the text.
func impactArrow(text string, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	if o.ASCII {
		return text
	}
	return strings.ReplaceAll(text, " -> ", " → ")
}

func impactArrows(page *calm.Page, o calm.Options) { //nolint:gocritic // Options are passed by value, as the kit takes them.
	for i := range page.Verdict.Sentences {
		page.Verdict.Sentences[i] = impactArrow(page.Verdict.Sentences[i], o)
	}
	for g := range page.Groups {
		group := &page.Groups[g]
		group.Head = impactArrow(group.Head, o)
		for i := range group.Summary {
			group.Summary[i] = impactArrow(group.Summary[i], o)
		}
		for r := range group.Rows {
			group.Rows[r].Number = impactArrow(group.Rows[r].Number, o)
		}
	}
}

// calmNext is the likely actions, each with the key that really runs it.
// What Enter graphs is the first thing a narrow line gives up: the marked
// row says it, and the keys behind it are then still named.
func (v *impactView) calmNext(width int, o calm.Options) []calm.Action { //nolint:gocritic // Options are passed by value, as the kit takes them.
	if v.read == nil || v.read.report == nil {
		return nil
	}
	report := v.read.report
	// a is on the first key line: a short screen gives the second one up.
	all := calm.Action{Key: calm.DetailKey, Label: fmt.Sprintf("all %d signals", len(impact.Signals))}
	queries := calm.Action{Key: string(rune(impactQueriesKey)), Label: "query and rule"}
	timeline := calm.Action{Key: impactTimelineKeyName, Label: "timeline"}
	if report.Failure != "" {
		// Nothing was read: reading again and the declaration come first.
		return []calm.Action{{Key: "r", Label: calmNextReread}, {Key: ":sources", Label: "show and probe the declaration"}, timeline, all}
	}
	graph := calm.Action{Key: "Enter", Label: "graph the workload"}
	if outcome := v.focusedOutcome(); outcome != nil && impactGraphPanels[v.focused] != "" {
		graph.Label = "graph " + outcome.Signal.Phrase
	}
	actions := []calm.Action{graph, all, queries, timeline}
	if short := append([]calm.Action{{Key: graph.Key, Label: "graph"}}, actions[1:]...); calm.NextShown(short, width, o) > calm.NextShown(actions, width, o) {
		return short
	}
	return actions
}

// impactBandLabels are the words beside the rollout marker, the fullest
// first: the marker row takes the first that fits beside the strips.
func impactBandLabels(report *impact.Report) []string {
	win := &report.Windows
	rev := report.Rollout.Label()
	at := impactClock(win.Rollout, report.ReadAt, true) + "Z"
	each := impact.Dur(win.Width) + " each side"
	labels := []string{rev + " rolled out " + at + " · " + each, rev + " at " + at + " · " + each, rev + " at " + at, rev}
	if win.Clipped != "" {
		labels = append([]string{rev + " rolled out " + at + " · " + each + " (asked " + impact.Dur(win.Asked) + ")"}, labels...)
	}
	return labels
}

// impactQueryLines cuts text into lines of at most room cells. A line ends after
// a comma, a blank or a bracket where one stands in its second half, so a
// query breaks between its parts when it can.
func impactQueryLines(text string, room int) []string {
	if room < 8 {
		return []string{text}
	}
	var out []string
	for tviewCells(text) > room {
		cut, used, soft := 0, 0, 0
		for index, r := range text {
			w := tviewCells(string(r))
			if used+w > room {
				break
			}
			used, cut = used+w, index+len(string(r))
			if strings.ContainsRune(", {(", r) && used >= room/2 {
				soft = cut
			}
		}
		if soft > 0 {
			cut = soft
		}
		out = append(out, strings.TrimRight(text[:cut], " "))
		text = strings.TrimLeft(text[cut:], " ")
	}
	return append(out, text)
}

// impactDiff is what the revision changed, as the list the comparison of
// the two pod templates wrote.
func impactDiff(report *impact.Report, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	switch {
	case len(report.Changes) > 0:
		return impactArrow(strings.Join(report.Changes, "; "), o)
	case report.Rollout.Previous == nil:
		return "not known: " + report.Rollout.Label() + " is the earliest retained revision"
	}
	return "nothing in images, env keys, resources or commands"
}

// impactBlock is how the block of s is written: which of its lines are
// whole. short leaves the lines that hold for every signal (what a strip
// draws, the source, the windows) to a taller screen.
type impactBlock struct {
	short bool
	// queries, diff and rest write the queries, the diff of the revision
	// and every other line whole.
	queries, diff, rest bool
}

// impactBlocks are the forms of the block, the fullest first: what a page
// without the rows for all of it gives up, in order. The rule, the
// samples, the source and the windows are one line each before a query or
// the diff is cut; then the diff is one line, because s is the key of the
// query and the rule and the answer still says what kinds of thing
// changed; then, where whole queries do not fit either, each query is one
// line and the diff is whole again.
func impactBlocks(short bool) []impactBlock {
	return []impactBlock{
		{short: short, queries: true, diff: true, rest: true},
		{short: short, queries: true, diff: true},
		{short: short, queries: true},
		{short: short, diff: true},
	}
}

// calmQueries is the block s shows on the calm page: the selected signal
// in full, the rule behind its verdict, how many samples stood behind it,
// its exact queries, the full diff of the revision, and the source and
// windows of the whole comparison.
// form says which of its lines are written whole, on as many lines as
// each needs; a line that is not is one line, cut where the line ends and
// marked.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (v *impactView) calmQueries(width int, form impactBlock, o calm.Options) []string {
	if v.read == nil || v.read.report == nil {
		return nil
	}
	p := o.Palette
	report := v.read.report
	short := form.short
	var lines []string
	// wrapped writes a text that is read to its end: on as many lines as
	// it needs when it is written whole, and on one line, cut where the
	// line ends, when it is not. The lines after the first stand under
	// the text, not under its label.
	write := func(label string, parts []string) {
		for index, part := range parts {
			if index > 0 {
				label = ""
			}
			lines = append(lines, calm.RestLine([]string{fmt.Sprintf("%-8s %s", label, part)}, width, o))
		}
	}
	room := width - calm.Indent - 9
	// A query and the diff break between their parts where they can.
	wrapped := func(label, text string, whole bool) {
		if whole {
			write(label, impactQueryLines(text, room))
			return
		}
		write(label, []string{text})
	}
	// A sentence breaks between its words.
	plain := func(label, text string) {
		if form.rest && room >= 8 {
			write(label, impactWrap(text, room))
			return
		}
		write(label, []string{text})
	}
	if outcome := v.focusedOutcome(); outcome != nil {
		signal := &outcome.Signal
		if outcome.State == impact.Compared {
			color := ""
			switch outcome.Verdict {
			case impact.Worse:
				color = p.Fail
			case impact.Better:
				color = p.Info
			}
			lines = append(lines, calm.FoldedLine(strings.ToUpper(signal.Label), color, []string{
				impactArrow(signal.Format(outcome.Before.Value)+" -> "+signal.Format(outcome.After.Value), o), signal.FormatDelta(outcome.Delta()),
				impact.PercentChange(outcome.Before.Value, outcome.After.Value), outcome.Verdict,
			}, false, width, o))
			if strip := "the value from zero; in colour where a sample is further from the before value than the rule allows"; !short {
				if signal.Reduce == impact.ReduceIncrease {
					strip = "a mark where the count rose"
				}
				plain("strip", strip)
			}
		} else {
			lines = append(lines, calm.FoldedLine(strings.ToUpper(signal.Label), p.Warn, []string{outcome.State + " - " + outcome.Reason}, false, width, o))
		}
		if outcome.State != impact.NotRead && outcome.State != impact.NotAvailable {
			plain("rule", signal.Rule())
			samples := fmt.Sprintf("%d before, %d after", outcome.Before.Samples, outcome.After.Samples)
			if outcome.Skipped > 0 {
				samples += fmt.Sprintf("; %d just after the rollout left out (their %s rate window spans it)", outcome.Skipped, impact.Dur(report.RateWindow))
			}
			plain("samples", samples)
		}
		if len(outcome.Notes) > 0 {
			plain("note", strings.Join(outcome.Notes, "; "))
		}
		for _, query := range outcome.Queries {
			if query != "" {
				// A query is read to be run again: it is not cut.
				wrapped("query", query, form.queries)
			}
		}
	} else if report.Failure != "" {
		const nothing = "Nothing was read, so nothing is shown as a value. That is not a zero and not a sign of health."
		parts := []string{nothing}
		if form.rest && width-calm.Indent >= 8 {
			parts = impactWrap(nothing, width-calm.Indent)
		}
		for _, part := range parts {
			lines = append(lines, calm.RestLine([]string{part}, width, o))
		}
	}
	// The answer says what kinds of thing the revision changed; this is
	// the list itself, for a reader who asks which image and which keys.
	wrapped("changed", impactDiff(report, o), form.diff)
	if short {
		// A short screen keeps the rule, the samples, the queries and
		// the diff, and leaves the source and the windows to a taller one.
		return lines
	}
	if report.Step > 0 {
		plain("source", impactJoin("prometheus "+report.Source, "step "+impact.Dur(report.Step), "rates over "+impact.Dur(report.RateWindow),
			"http: "+report.HTTPSelector))
	} else {
		plain("source", "prometheus: not read")
	}
	win := &report.Windows
	windows := fmt.Sprintf("before %s-%sZ | after %s-%sZ · %s each", impactClock(win.Before.Start, report.ReadAt, true),
		impactClock(win.Before.End, win.Before.Start, true), impactClock(win.After.Start, report.ReadAt, true),
		impactClock(win.After.End, win.After.Start, true), impact.Dur(win.Width))
	if win.Clipped != "" {
		windows += fmt.Sprintf(" (asked %s; %s)", impact.Dur(win.Asked), win.Clipped)
	}
	plain("windows", windows)
	return lines
}

// renderCalm draws the calm page into the area the view has. The block of
// s stands between the evidence and the keys, and the page is planned for
// the rows that are left.
func (v *impactView) renderCalm() {
	o := calmOptions(v.app)
	width, all := v.viewWidth(), calmHeight(v.height)
	var (
		page  calm.Page
		plan  calm.Plan
		block []string
		gap   bool
	)
	// lay plans the page for the rows a block leaves it, and reports
	// whether the page still has its strips and its next line.
	lay := func(lines []string, blank bool) (strips, next bool) {
		block, gap = lines, blank
		height := all
		if len(block) > 0 {
			height = max(all-len(block), 1)
			if gap {
				height = max(height-1, 1)
			}
		}
		page = v.calmPage(width, o)
		plan = calm.Lay(&page, width, height, o)
		if calmKeepSelected(&page, &plan) {
			plan = calm.Lay(&page, width, height, o)
		}
		next = true
		for _, words := range plan.Dropped {
			next = next && words != "the next line"
		}
		return !plan.TooSmall && plan.Layout.Strip > 0, !plan.TooSmall && next
	}
	if v.queries {
		// A query is read to be run again and the diff to be read to its
		// end, and so is every other line of the block: each is written
		// whole where that costs the page under it neither its strips nor
		// its next line, first with a blank row beside the block, then
		// without. Where the page has not the rows for all of it, it
		// takes the fullest form that fits (impactBlocks); a page that
		// would lose its strips or its next line to any of them has one
		// line of each, with every cut marked.
		short := all < impactTallPage
		least := v.calmQueries(width, impactBlock{short: short}, o)
		strips, next := lay(least, true)
		fits := false
		for _, form := range impactBlocks(short) {
			more := v.calmQueries(width, form, o)
			if len(more) == len(least) {
				continue
			}
			for _, blank := range []bool{true, false} {
				if s, n := lay(more, blank); !plan.TooSmall && (s || !strips) && (n || !next) {
					fits = true
					break
				}
			}
			if fits {
				break
			}
		}
		if !fits {
			lay(least, true)
		}
	} else {
		lay(nil, false)
	}
	if v.read != nil && v.read.report != nil && page.Bands.Any() && plan.Layout.Strip > 0 {
		// The label does not move a column, so it is chosen once the columns
		// are known.
		for _, label := range impactBandLabels(v.read.report) {
			if page.Bands.Label = label; tviewCells(label) <= plan.Layout.StripCol-calm.Indent-1 {
				break
			}
		}
	}
	v.dropped = plan.Dropped
	rendered := calm.Render(&page, &plan, o)
	v.calm.SetText(strings.Join(calmInsert(rendered, block, gap, plan.TooSmall), "\n"))
	v.calm.ScrollToBeginning()
}
