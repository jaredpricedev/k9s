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

// The calm page of :impact. The sentence that says what was measured after
// the rollout comes first, with what the revision changed directly under
// it. Every signal that moved is one row: its name, before -> after, the
// change, and a strip over both compared windows in which the rollout is
// one band through every row. Signals that did not move are one dim line
// of names; signals without a comparison are one line each and are never
// left out. The rule, the sample counts and the queries of the selected
// row are under s, and the table this view had before is under a.
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
func impactChange(outcome *impact.Outcome) string {
	change := outcome.Signal.FormatDelta(outcome.Delta())
	if outcome.Delta() == 0 {
		change = "0"
	}
	return change + "  " + impact.PercentChange(outcome.Before.Value, outcome.After.Value)
}

// calmVerdict is the answer at the top: the sentence the comparison comes
// to, then what the revision changed.
func (v *impactView) calmVerdict() calm.Verdict {
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
		summary := report.Summary()
		if report.Traffic != "" && strings.HasSuffix(summary, " "+report.Traffic) {
			out.Sentences = []string{strings.TrimSuffix(summary, " "+report.Traffic), report.Traffic}
		} else {
			out.Sentences = []string{summary}
		}
		worse, better, same, _ := report.Counts()
		out.Caveat = impactOrderCaveat
		switch {
		case report.Failure != "" || worse+better+same == 0:
			out.Kind, out.Caveat = calm.NotRead, impactUnreadCaveat
		case worse == 0:
			out.Kind = calm.NothingFound
		}
		switch {
		case len(report.Changes) > 0:
			out.Sentences = append(out.Sentences, report.Rollout.Label()+" changed: "+strings.Join(report.Changes, "; "))
		case report.Rollout.Previous == nil:
			out.Sentences = append(out.Sentences, report.Rollout.Label()+" is the earliest retained revision; there is no earlier one to diff.")
		default:
			out.Sentences = append(out.Sentences, report.Rollout.Label()+" changed: nothing in images, env keys, resources or commands.")
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

// calmPage builds the page from the retained comparison.
func (v *impactView) calmPage(o calm.Options) calm.Page {
	p := o.Palette
	page := calm.Page{Title: v.calmTitle(), Verdict: v.calmVerdict()}
	read := v.read
	keys := calmKeyLine{}
	page.Keys = keys.parts()
	if read == nil {
		return page
	}
	// A key that has nothing to step through is not named.
	if read.rollout != nil && read.rollout.Total > 1 {
		keys.Own = append(keys.Own, "n/N later/earlier rollout")
	}
	keys.Reread = true
	page.Keys = keys.parts()
	if read.report == nil {
		return page
	}
	keys.Own = append(keys.Own, calmGrafanaKeyName+" Grafana")
	report := read.report
	// Reading again is the first of the next actions when nothing was
	// read, and is named once.
	keys.Reread = report.Failure == ""
	page.Keys = keys.parts()
	win := &report.Windows
	if report.Step > 0 && win.After.End.After(win.Before.Start) {
		page.Frame = chart.Frame{Start: win.Before.Start, End: win.After.End, Step: report.Step}
		// The rollout is a moment: one column at the split of the windows,
		// and a guide down through every row.
		page.Bands = calm.Bands{Label: report.Rollout.Label(), Guide: true,
			Episodes: []chart.Interval{{Start: win.Rollout, End: win.Rollout}}}
	}
	rows := map[string][]calm.Row{}
	// names are the signals of a group, short what a group of rows says
	// when it is folded to one line.
	names, short := map[string][]string{}, map[string][]string{}
	reasons := map[string]bool{}
	reason := ""
	for index := range report.Outcomes {
		outcome := &report.Outcomes[index]
		signal := &outcome.Signal
		class := impactClass(outcome)
		names[class] = append(names[class], signal.Label)
		if class == impactNotReadLabel {
			reasons[outcome.Reason], reason = true, outcome.Reason
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
		short[class] = append(short[class], signal.Label+" "+signal.Format(outcome.Before.Value)+" -> "+signal.Format(outcome.After.Value))
		rows[class] = append(rows[class], calm.Row{
			Subject: signal.Label, Number: signal.Format(outcome.Before.Value) + " -> " + signal.Format(outcome.After.Value),
			Compare: impactChange(outcome), Color: color, Strip: impactStrip(outcome, report.Step), Selected: signal.ID == v.focused,
		})
	}
	headed := false
	for _, label := range impactRowLabels {
		if len(rows[label]) == 0 {
			continue
		}
		group := calm.Group{Label: label, Rows: rows[label], Summary: short[label]}
		switch {
		case label == impactWorseLabel:
			group.Color = p.Warn
		case label == impactMovedLabel || len(rows[impactWorseLabel]) > 0:
			// What the sentence does not rest on folds first.
			group.Role = calm.Secondary
		}
		if !headed {
			group.Head, headed = "before -> after", true
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
	if list := names[impactUnchangedLabel]; len(list) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: impactUnchangedLabel, Role: calm.QuietRole, Summary: list})
	}
	if list := names[impactNotReadLabel]; len(list) > 0 {
		// One reason is said once, before the names it holds for.
		if len(reasons) == 1 && reason != "" {
			list = append([]string{reason + ": " + list[0]}, list[1:]...)
		}
		page.Groups = append(page.Groups, calm.Group{Label: impactNotReadLabel, Color: p.Warn, Role: calm.NotReadRole, Summary: list})
	}
	for _, label := range []string{impactMissingLabel, impactTooFewLabel} {
		if list := names[label]; len(list) > 0 {
			page.Groups = append(page.Groups, calm.Group{Label: label, Role: calm.NotReadRole, Summary: list})
		}
	}
	// The digits are the width compared on each side of the rollout here.
	keys.Window = "1-5 window"
	if len(v.calmOrder()) > 0 {
		keys.Move = "↑↓ signal"
	}
	page.Keys = keys.parts()
	page.Next = v.calmNext()
	impactArrows(&page, o)
	return page
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
func (v *impactView) calmNext() []calm.Action {
	if v.read == nil || v.read.report == nil {
		return nil
	}
	report := v.read.report
	// a is on the first key line: a short screen gives the second one up.
	all := calm.Action{Key: calm.DetailKey, Label: fmt.Sprintf("all %d signals", len(impact.Signals))}
	queries := calm.Action{Key: string(rune(impactQueriesKey)), Label: "query and rule"}
	timeline := calm.Action{Key: "t", Label: "timeline"}
	if report.Failure != "" {
		// Nothing was read: reading again and the declaration come first.
		return []calm.Action{{Key: "r", Label: calmNextReread}, {Key: ":sources", Label: "show and probe the declaration"}, timeline, all}
	}
	graph := calm.Action{Key: "Enter", Label: "graph this workload"}
	if outcome := v.focusedOutcome(); outcome != nil && impactGraphPanels[v.focused] != "" {
		graph.Label = "graph " + outcome.Signal.Phrase
	}
	return []calm.Action{graph, all, queries, timeline}
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

// calmQueries is the block s shows on the calm page: the selected signal
// in full, the rule behind its verdict, how many samples stood behind it,
// its exact queries, and the source and windows of the whole comparison.
// whole writes a query that is wider than the line on as many lines as it
// needs; without it a query is one line, cut where the line ends.
func (v *impactView) calmQueries(width int, short, whole bool, o calm.Options) []string {
	if v.read == nil || v.read.report == nil {
		return nil
	}
	p := o.Palette
	report := v.read.report
	var lines []string
	plain := func(label, text string) {
		lines = append(lines, calm.RestLine([]string{fmt.Sprintf("%-8s %s", label, text)}, width, o))
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
			switch {
			case query == "":
			case whole:
				// A query is read to be run again: it is not cut.
				for index, part := range impactQueryLines(query, width-calm.Indent-9) {
					label := ""
					if index == 0 {
						label = "query"
					}
					plain(label, part)
				}
			default:
				plain("query", query)
			}
		}
	} else if report.Failure != "" {
		lines = append(lines, calm.RestLine([]string{"Nothing was read, so nothing is shown as a value. That is not a zero and not a sign of health."}, width, o))
	}
	if short {
		// A short screen keeps the rule, the samples and the queries, and
		// leaves what holds for every signal to a taller one.
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
		page = v.calmPage(o)
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
		// A query is read to be run again, so it is written whole where
		// that costs the page under it neither its strips nor its next
		// line: first with a blank row beside the block, then without.
		// Where it would, each query is one line, cut where the line ends.
		short := all < impactTallPage
		cut, whole := v.calmQueries(width, short, false, o), v.calmQueries(width, short, true, o)
		strips, next := lay(cut, true)
		if len(whole) > len(cut) {
			fits := false
			for _, blank := range []bool{true, false} {
				if s, n := lay(whole, blank); !plan.TooSmall && (s || !strips) && (n || !next) {
					fits = true
					break
				}
			}
			if !fits {
				lay(cut, true)
			}
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
