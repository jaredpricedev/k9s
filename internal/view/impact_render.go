// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	impactSignalWidth  = 14
	impactValueWidth   = 8
	impactChangeWidth  = 9
	impactPercentWidth = 6
	impactVerdictWidth = 18
	impactMaxSummary   = 3
)

var (
	impactSparkGlyphs = []rune("▁▂▃▄▅▆▇█")
	impactSparkASCII  = []rune("_.:-=+*#")
)

// impactColumns are widths in terminal cells; zero hides a column. Signal,
// before, after and the verdict word always remain.
type impactColumns struct {
	change, percent, cells int
}

func impactLayout(width int) impactColumns {
	var columns impactColumns
	rest := width - (2 + impactSignalWidth + 1 + impactValueWidth + 1 + impactValueWidth + 1 + impactVerdictWidth)
	if rest > impactPercentWidth {
		columns.percent = impactPercentWidth
		rest -= impactPercentWidth + 1
	}
	if width >= 100 {
		columns.change = impactChangeWidth
		rest -= impactChangeWidth + 1
	}
	// The trend needs a separator, the rollout mark and equal halves.
	if cells := (rest - 2) / 2; cells >= 4 {
		columns.cells = min(cells, 12)
	}
	return columns
}

func (v *impactView) noIcons() bool { return v.app.Config.K9s.UI.NoIcons }

// impactSpark draws one half of a trend. A cell with no sample is blank.
func impactSpark(levels []int, ascii bool) string {
	glyphs := impactSparkGlyphs
	if ascii {
		glyphs = impactSparkASCII
	}
	var b strings.Builder
	for _, level := range levels {
		if level < 0 || level >= len(glyphs) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(glyphs[level])
	}
	return b.String()
}

func (v *impactView) rolloutMark() string {
	if v.noIcons() {
		return "|"
	}
	return "│"
}

func (v *impactView) render() {
	if v.header == nil {
		return
	}
	lines := v.headerLines(false)
	// When the full header would push signals off screen, the closing state
	// line folds into the source line; the rows still name every gap.
	if v.height > 0 && len(lines)+1+len(impact.Signals)+2 > v.height {
		lines = v.headerLines(true)
	}
	if len(lines) != v.headerHeight {
		v.headerHeight = len(lines)
		v.Flex.ResizeItem(v.header, v.headerHeight, 0)
	}
	v.header.SetText(strings.Join(lines, "\n"))
	// The table keeps every signal on screen when it can; the detail pane
	// takes what is left, and never less than its one-line statement.
	detail := 1
	if v.height > 0 {
		detail = min(max(v.height-v.headerHeight-1-(1+len(impact.Signals)), 1), 7)
	}
	if detail != v.detailHeight {
		v.detailHeight = detail
		v.Flex.ResizeItem(v.detail, detail, 0)
	}
	v.renderRows()
	v.renderDetail()
	width := v.viewWidth()
	enter := ""
	if v.graphAvailable() {
		enter = "Enter graph · "
	}
	v.footer.SetText(tview.Escape(impactFit(width,
		enter+"t timeline · 1 5m  2 15m  3 30m  4 1h  5 3h · p earlier  n later rollout · r re-read · Esc back",
		enter+"t timeline · 1-5 window · p/n rollout · r re-read · Esc back",
		"t timeline · 1-5 window · p/n rollout · r read · Esc")))
}

// headerLines states what is compared, the two windows in clock time, the
// source and read time, what the revision changed, the summary sentence and
// what could not be compared.
func (v *impactView) headerLines(tight bool) []string {
	width := v.viewWidth()
	p := v.app.Styles.Semantic()
	w := v.workload()
	type line struct {
		text  string
		color config.Color
		bold  bool
	}
	title := impactTitle + " / " + w.Label()
	lines := []line{{text: title, color: p.Focus, bold: true}}
	add := func(text string, color config.Color, bold bool) { lines = append(lines, line{text, color, bold}) }
	wrapped := func(text string, color config.Color, bold bool) {
		parts := impactWrap(text, width)
		for index, part := range parts {
			if index == impactMaxSummary-1 && len(parts) > impactMaxSummary {
				add(ui.Truncate(part, width-1)+"…", color, bold)
				break
			}
			add(part, color, bold)
		}
	}
	read := v.read
	if read != nil && read.rollout != nil {
		r := read.rollout
		where, short := "latest", "latest"
		if r.Back > 0 {
			where, short = fmt.Sprintf("%d before latest", r.Back), fmt.Sprintf("latest-%d", r.Back)
		}
		at := r.Revision.Created
		stamp := at.UTC().Format("15:04:05Z")
		if read.report != nil {
			stamp = impactClock(at, read.report.ReadAt, true) + "Z"
			lines[0].text = impactFit(width,
				fmt.Sprintf("%s · %s (%s; %d retained) · rolled out %s, %s before this read", title, r.Label(), where, r.Total, stamp,
					impact.Dur(read.report.ReadAt.Sub(at))),
				fmt.Sprintf("%s · %s (%s) · rolled out %s", title, r.Label(), where, stamp),
				fmt.Sprintf("%s / %s · %s (%s) · %s", impactTitle, w.Name, r.Label(), short, stamp))
		} else {
			lines[0].text = impactFit(width, fmt.Sprintf("%s · %s (%s; %d retained)", title, r.Label(), where, r.Total),
				fmt.Sprintf("%s / %s · %s (%s)", impactTitle, w.Name, r.Label(), short))
		}
	}
	if read != nil && read.incomplete != "" {
		// A cut revision list can hide the real latest rollout; say so
		// before any number is read.
		add(read.incomplete, p.Warning, true)
	}
	switch {
	case read == nil:
		add("Reading the rollout history, then the declared Prometheus source.", p.Text, false)
		add("Bounded, read-only, and only because this view was opened.", p.Muted, false)
	case read.report == nil:
		wrapped("No comparison: "+read.err.Error()+".", p.Failure, true)
		if read.rollout != nil && read.rollout.Total > 1 {
			add("p and n step to another retained rollout.", p.Muted, false)
		}
	default:
		report := read.report
		win := &report.Windows
		span := func(seconds bool) string {
			// The date is written once per window, on its start.
			return fmt.Sprintf("before %s-%sZ %s after %s-%sZ", impactClock(win.Before.Start, report.ReadAt, seconds),
				impactClock(win.Before.End, win.Before.Start, seconds), v.rolloutMark(),
				impactClock(win.After.Start, report.ReadAt, seconds), impactClock(win.After.End, win.After.Start, seconds))
		}
		each := impact.Dur(win.Width) + " each"
		if win.Clipped != "" {
			add(impactFit(width,
				fmt.Sprintf("%s · %s (asked %s; %s)", span(true), each, impact.Dur(win.Asked), win.Clipped),
				fmt.Sprintf("%s · %s, asked %s", span(true), each, impact.Dur(win.Asked)),
				fmt.Sprintf("%s · %s, asked %s", span(false), each, impact.Dur(win.Asked)),
				fmt.Sprintf("%s · %s (asked %s)", span(false), impact.Dur(win.Width), impact.Dur(win.Asked))), p.Text, false)
		} else {
			add(impactFit(width, span(true)+" · "+each, span(false)+" · "+each), p.Text, false)
		}
		if report.Step > 0 {
			readAt := "read " + report.ReadAt.Format("15:04:05Z")
			pace := fmt.Sprintf("step %s · rates over %s", impact.Dur(report.Step), impact.Dur(report.RateWindow))
			step, gaps, short := "step "+impact.Dur(report.Step), "", ""
			if _, _, _, uncompared := report.Counts(); tight && uncompared > 0 {
				gaps = fmt.Sprintf("%d of %d not compared", uncompared, len(report.Outcomes))
				short = fmt.Sprintf("%d not compared", uncompared)
			}
			add(impactFit(width,
				impactJoin("prometheus "+report.Source, readAt, pace, "http: "+report.HTTPSelector, gaps),
				impactJoin("prometheus "+report.Source, readAt, pace, gaps),
				impactJoin(report.Source, readAt, pace, gaps),
				impactJoin(report.Source, readAt, step, gaps),
				impactJoin(report.Source, readAt, step, short),
				impactJoin(readAt, step, gaps)), p.Muted, false)
		} else {
			add(impactFit(width, "prometheus: not read · "+report.ReadAt.Format("15:04:05Z")), p.Muted, false)
		}
		switch {
		case len(report.Changes) > 0:
			add(ui.Truncate(report.Rollout.Label()+" changed: "+strings.Join(report.Changes, "; "), width), p.Text, false)
		case report.Rollout.Previous == nil:
			add(ui.Truncate(report.Rollout.Label()+" is the earliest retained revision; there is no earlier one to diff.", width), p.Muted, false)
		default:
			add(ui.Truncate(report.Rollout.Label()+" changed: nothing in images, env keys, resources or commands.", width), p.Muted, false)
		}
		worse, _, _, _ := report.Counts()
		color := p.Text
		if worse > 0 || report.Failure != "" {
			color = p.Failure
		}
		wrapped(report.Summary(), color, true)
	}
	// The last line is the state of the read and what is missing from it.
	state, color, urgent := "", p.Muted, true
	switch {
	case !v.contextCurrent():
		state, color = "Context changed; this comparison is retained. Reopen :impact for the current context.", p.Warning
	case v.loading && read != nil:
		state, color = "Reading again; the comparison below is retained until the new one arrives.", p.Progress
	case v.loading:
		state, color = "Reading...", p.Progress
	case read != nil && read.report != nil && read.report.Failure == "":
		urgent = false
		if gaps := read.report.Uncompared(); gaps != "" {
			state, color = "Gaps, not zeros - "+gaps, p.Warning
		} else {
			state = fmt.Sprintf("All %d signals compared. \"After\" is order in time, not cause.", len(read.report.Outcomes))
		}
	case read != nil && read.report != nil:
		state, color = "Not read is not zero and not healthy. r reads again; :sources shows and probes the declaration.", p.Warning
	case read != nil:
		state = "r reads again."
	}
	if urgent || !tight {
		add(ui.Truncate(state, width), color, false)
	}
	out := make([]string, 0, len(lines))
	for _, item := range lines {
		out = append(out, timelineTag(item.color, item.bold)+tview.Escape(ui.Truncate(item.text, width))+"[-:-:-]")
	}
	return out
}

// impactWrap breaks text at spaces so that no line is wider than width
// terminal cells. A single word wider than the line is left to truncation.
func impactWrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case runewidth.StringWidth(line)+1+runewidth.StringWidth(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func impactCell(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.FillRight(runewidth.Truncate(text, width, "…"), width)
}

func impactRight(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.FillLeft(runewidth.Truncate(text, width, "…"), width)
}

func impactSide(signal *impact.Signal, side impact.Side) string {
	if !side.Valid || side.Samples == 0 {
		return "-"
	}
	return signal.Format(side.Value)
}

func (v *impactView) renderRows() {
	if v.table == nil {
		return
	}
	v.table.Clear()
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	if v.read == nil || v.read.report == nil || len(v.read.report.Outcomes) == 0 {
		text := "Nothing was compared."
		if v.read == nil {
			text = "Reading..."
		}
		v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(impactCell(text, width))).SetSelectable(false).SetTextColor(p.Muted.Color()))
		return
	}
	report := v.read.report
	columns := impactLayout(width)
	mark := v.rolloutMark()
	heading := "  " + impactCell("SIGNAL", impactSignalWidth) + " " + impactRight("BEFORE", impactValueWidth) + " " + impactRight("AFTER", impactValueWidth) + " "
	if columns.change > 0 {
		heading += impactRight("CHANGE", columns.change) + " "
	}
	if columns.percent > 0 {
		heading += impactRight("%", columns.percent) + " "
	}
	heading += impactCell("VERDICT", impactVerdictWidth)
	if columns.cells > 0 {
		heading += " " + impactRight("before", columns.cells) + mark + impactCell("after", columns.cells)
	}
	v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(heading)).SetSelectable(false).SetTextColor(p.Focus.Color()).SetAttributes(tcell.AttrBold))
	selected := 1
	for index := range report.Outcomes {
		outcome := &report.Outcomes[index]
		signal := &outcome.Signal
		row := index + 1
		if signal.ID == v.focused {
			selected = row
		}
		flag, color, bold := " ", p.Text, false
		switch {
		case outcome.State != impact.Compared:
			color = p.Warning
		case outcome.Verdict == impact.Worse:
			flag, color, bold = "!", p.Failure, true
		case outcome.Verdict == impact.Better:
			color = p.Healthy
		case outcome.Verdict == impact.Unchanged:
			color = p.Muted
		}
		var line strings.Builder
		line.WriteString(timelineTag(p.Text, bold))
		line.WriteString(tview.Escape(flag + " " + impactCell(signal.Label, impactSignalWidth) + " " +
			impactRight(impactSide(signal, outcome.Before), impactValueWidth) + " " +
			impactRight(impactSide(signal, outcome.After), impactValueWidth) + " "))
		change, percent := "", ""
		if outcome.State == impact.Compared {
			change, percent = signal.FormatDelta(outcome.Delta()), impact.PercentChange(outcome.Before.Value, outcome.After.Value)
			if outcome.Delta() == 0 {
				change = "0"
			}
		}
		if columns.change > 0 {
			line.WriteString(tview.Escape(impactRight(change, columns.change) + " "))
		}
		if columns.percent > 0 {
			line.WriteString(tview.Escape(impactRight(percent, columns.percent) + " "))
		}
		line.WriteString(timelineTag(color, bold))
		line.WriteString(tview.Escape(impactCell(outcome.Word(), impactVerdictWidth)))
		if columns.cells > 0 {
			before, after := impact.Spark(outcome.Series, &report.Windows, columns.cells, signal.Reduce == impact.ReduceMax)
			if len(outcome.Series) == 0 {
				before, after = make([]int, columns.cells), make([]int, columns.cells)
				for cell := range before {
					before[cell], after[cell] = -1, -1
				}
			}
			line.WriteString(timelineTag(p.Muted, false) + " " + tview.Escape(impactSpark(before, v.noIcons())))
			line.WriteString(timelineTag(p.Focus, false) + mark)
			line.WriteString(timelineTag(color, false) + tview.Escape(impactSpark(after, v.noIcons())))
		}
		line.WriteString("[-:-:-]")
		v.table.SetCell(row, 0, tview.NewTableCell(line.String()).SetExpansion(1))
	}
	v.table.Select(selected, 0)
	v.table.SetOffset(v.table.GetOffset())
}

// renderDetail states the focused signal in full: the numbers, the rule
// that produced the verdict, how many samples stood behind it, and the
// exact query. With one line it keeps the statement and the reason.
func (v *impactView) renderDetail() {
	if v.detail == nil {
		return
	}
	outcome := v.focusedOutcome()
	if outcome == nil {
		text := ""
		if v.read != nil && v.read.report != nil && v.read.report.Failure != "" {
			text = "Nothing was read, so nothing is shown as a value. That is not a zero and not a sign of health."
		}
		v.detail.SetText(tview.Escape(ui.Truncate(text, v.viewWidth())))
		return
	}
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	signal := &outcome.Signal
	report := v.read.report
	samples := fmt.Sprintf("samples: %d before, %d after", outcome.Before.Samples, outcome.After.Samples)
	if outcome.Skipped > 0 {
		samples += fmt.Sprintf("; %d just after the rollout left out (their %s rate window spans it)", outcome.Skipped, impact.Dur(report.RateWindow))
	}
	var head string
	color := p.Text
	if outcome.State == impact.Compared {
		head = fmt.Sprintf("%s: %s -> %s · %s · %s · %s", signal.Label, signal.Format(outcome.Before.Value), signal.Format(outcome.After.Value),
			signal.FormatDelta(outcome.Delta()), impact.PercentChange(outcome.Before.Value, outcome.After.Value), outcome.Verdict)
		if outcome.Verdict == impact.Worse {
			color = p.Failure
		}
		if v.detailHeight == 1 {
			head = impactFit(width, head+fmt.Sprintf(" · %d|%d samples", outcome.Before.Samples, outcome.After.Samples), head)
		}
	} else {
		head = signal.Label + ": " + outcome.State + " - " + outcome.Reason
		color = p.Warning
	}
	lines := []string{timelineTag(color, true) + tview.Escape(ui.Truncate(head, width)) + "[-:-:-]"}
	plain := func(text string) {
		lines = append(lines, timelineTag(p.Muted, false)+tview.Escape(ui.Truncate(text, width))+"[-:-:-]")
	}
	if outcome.State != impact.NotRead && outcome.State != impact.NotAvailable {
		plain("rule: " + signal.Rule())
		plain(samples)
	}
	if len(outcome.Notes) > 0 {
		plain("note: " + strings.Join(outcome.Notes, "; "))
	}
	for _, query := range outcome.Queries {
		if query != "" {
			plain("query: " + query)
		}
	}
	if len(lines) > v.detailHeight {
		lines = lines[:v.detailHeight]
	}
	v.detail.SetText(strings.Join(lines, "\n"))
	v.detail.ScrollToBeginning()
}
