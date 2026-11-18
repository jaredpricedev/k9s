// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/probe"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	// probeMarkWidth holds the selection mark, the flag and a blank.
	probeMarkWidth    = 3
	probePodMin       = 18
	probePodMax       = 48
	probeNumberWidth  = 7
	probeVerdictWidth = 10
	probeVerdictWide  = 34
	probeNodeMax      = 16
	probeZoneMax      = 14
)

// probeColumns are widths in terminal cells; zero hides a column. The Pod,
// its median and the verdict word always remain.
type probeColumns struct {
	pod, strip, fastest, slowest, over, codes, node, zone, verdict int
	// spaced puts one blank cell between two bars of a strip.
	spaced bool
}

// probeLayout spends the width in a fixed order: the strip, the slowest
// answer, the unanswered count and the node, then a blank between two bars
// of the strip so that each request stands alone, then the fastest answer
// and the status codes, and only then does a column grow. The selected
// Pod's line under the table states every time and every code whatever the
// columns leave out.
func probeLayout(width, attempts, podNatural, nodeNatural, zoneNatural int) probeColumns {
	podNatural = min(max(podNatural, len("previous 00:00:00Z")), probePodMax)
	columns := probeColumns{pod: min(podNatural, probePodMin), verdict: probeVerdictWidth}
	rest := width - (probeMarkWidth + columns.pod + 1 + probeNumberWidth + 1 + columns.verdict)
	take := func(cost int) bool {
		if cost <= 0 || rest < cost {
			return false
		}
		rest -= cost
		return true
	}
	if strip := max(attempts, 3); take(strip + 1) {
		columns.strip = strip
	}
	if take(probeNumberWidth + 1) {
		columns.slowest = probeNumberWidth
	}
	if take(probeNumberWidth + 1) {
		columns.over = probeNumberWidth
	}
	if node := min(max(nodeNatural, 4), probeNodeMax); nodeNatural > 0 && columns.over > 0 && take(node+1) {
		columns.node = node
	}
	if spaced := 2*attempts - 1; columns.strip > 0 && attempts > 1 && take(spaced-columns.strip) {
		columns.strip, columns.spaced = spaced, true
	}
	if take(probeNumberWidth + 1) {
		columns.fastest = probeNumberWidth
	}
	if take(probeNumberWidth + 1) {
		columns.codes = probeNumberWidth
	}
	grow := func(limit int) {
		if more := min(limit-columns.pod, rest); more > 0 {
			columns.pod += more
			rest -= more
		}
	}
	// The name grows a little, then the zone is placed, then the name
	// takes what it still needs: its end already tells the Pods apart.
	grow(min(podNatural, probePodMin+8))
	if zone := min(max(zoneNatural, 4), probeZoneMax); zoneNatural > 0 && columns.node > 0 && take(zone+1) {
		columns.zone = zone
	}
	grow(podNatural)
	if columns.strip > 0 {
		for _, label := range []int{len("EACH REQUEST"), len("EACH REQUEST, IN ORDER")} {
			if label > columns.strip && take(label-columns.strip) {
				columns.strip = label
			}
		}
	}
	if take(probeVerdictWide - columns.verdict) {
		columns.verdict = probeVerdictWide
	}
	return columns
}

func (v *probeView) noIcons() bool { return v.app.Config.K9s.UI.NoIcons }

func (v *probeView) viewWidth() int {
	if v.width <= 0 {
		return 80
	}
	return v.width
}

// probeTail keeps the end of a name, which is what tells sibling Pods
// apart, and marks what it cut at the front.
func probeTail(name string, width int, ascii bool) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(name) <= width {
		return runewidth.FillRight(name, width)
	}
	mark := "…"
	if ascii {
		mark = "~"
	}
	runes := []rune(name)
	cut := 0
	for cut < len(runes) && runewidth.StringWidth(string(runes[cut:]))+runewidth.StringWidth(mark) > width {
		cut++
	}
	// A name is cut at a dash when one is near, so that what remains starts
	// with a whole part ("…-6f9c7d5b84-hx4mt", not "…lt-6f9c7d5b84-hx4mt").
	if cut > 0 && cut < len(runes) && runes[cut] != '-' {
		for next := cut; next < len(runes) && next-cut <= 12; next++ {
			if runes[next] == '-' && len(runes)-next >= min(8, width-1) {
				cut = next
				break
			}
		}
	}
	return runewidth.FillRight(mark+string(runes[cut:]), width)
}

type probeGlyphs struct {
	timeout, failed, unsent string
	// waiting is a request in flight and abandoned one that was in flight
	// when the run was canceled. Both are plain ASCII in either mode.
	waiting, abandoned      string
	times, sep, arrow, full string
}

func (v *probeView) glyphs() probeGlyphs {
	if v.noIcons() {
		return probeGlyphs{timeout: string(chart.GapASCII), failed: "!", unsent: " ", waiting: "~", abandoned: "?",
			times: "x", sep: " | ", arrow: "->", full: "#"}
	}
	return probeGlyphs{timeout: string(chart.GapGlyph), failed: "!", unsent: "·", waiting: "~", abandoned: "?",
		times: "×", sep: " · ", arrow: "→", full: "█"}
}

// probeStrip draws one cell per request, in the order they were sent, on a
// scale shared by every row: the height of a bar is the time to the first
// byte and top is a full bar. A request without an answer is a cross, one
// the API server could not deliver is "!", one in flight is "~", one that
// was in flight when the run was canceled is "?", and one never sent is a
// dot: none of them is drawn as a bar, because none of them is a time.
//
// Every cell carries a complete tag: in this terminal library a tag
// replaces the whole style, so the row's surface is repeated in each.
func (v *probeView) probeStrip(attempts []probe.Attempt, top, slowLine float64, spaced bool, normal, slow, dim config.Color, surface string) (text string, cells int) {
	g := v.glyphs()
	var b strings.Builder
	for i, attempt := range attempts {
		if i > 0 && spaced {
			b.WriteByte(' ')
			cells++
		}
		cells++
		color, glyph := slow, g.failed
		switch attempt.Outcome {
		case probe.NotSent:
			color, glyph = dim, g.unsent
		case probe.InFlight:
			color, glyph = dim, g.waiting
		case probe.Abandoned:
			color, glyph = dim, g.abandoned
		case probe.Answered:
			seconds := attempt.Took.Seconds()
			color = normal
			if slowLine > 0 && seconds >= slowLine {
				color = slow
			}
			glyph = chart.Spark([]float64{seconds}, chart.SparkOptions{ASCII: v.noIcons(), Style: chart.SparkBars, Max: top})
		case probe.TimedOut:
			glyph = g.timeout
		}
		b.WriteString(probeTag(color, false, surface) + tview.Escape(glyph))
	}
	return b.String(), cells
}

type probeLine struct {
	text  string
	color config.Color
	bold  bool
}

func probeTagged(lines []probeLine, width int) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, timelineTag(line.color, line.bold)+tview.Escape(ui.Truncate(line.text, width))+"[-:-:-]")
	}
	return strings.Join(out, "\n")
}

// requestWords describes what one run sends, long and short.
func (v *probeView) requestWords() (long, short string) {
	if v.plan == nil || v.plan.Sendable() == 0 {
		return "", ""
	}
	g := v.glyphs()
	requests, _ := v.plan.Requests()
	what := requests[0]
	if len(requests) > 1 {
		what = fmt.Sprintf("each pod's own %s GET (%d different)", v.plan.Kind.Field(), len(requests))
	}
	times := probePlural(v.attempts, "time", "times")
	if v.plan.Sendable() == 1 {
		return what + ", " + times + ", through the Kubernetes API proxy", fmt.Sprintf("%s %s%d", what, g.times, v.attempts)
	}
	if len(requests) > 1 {
		return what + ", " + times + ", through the Kubernetes API proxy", fmt.Sprintf("own %s GET %s%d per pod", v.plan.Kind.Field(), g.times, v.attempts)
	}
	return what + " on each pod, " + times + ", through the Kubernetes API proxy", fmt.Sprintf("%s %s%d per pod", what, g.times, v.attempts)
}

// headerLines are the title with what a run sends, the state of the view,
// and what the plan left out.
func (v *probeView) headerLines(summary *probe.Summary) []string {
	width := v.viewWidth()
	p := v.app.Styles.Semantic()
	g := v.glyphs()
	title, short := probeTitle+" / "+v.scope.Label(), probeTitle+" / "+v.scope.Name
	long, brief := v.requestWords()
	first := timelineTag(p.Focus, true) + tview.Escape(ui.Truncate(short, width)) + "[-:-:-]"
	for _, pair := range [][2]string{{title, long}, {title, brief}, {short, brief}, {title, ""}, {short, ""}} {
		text := pair[0]
		if pair[1] != "" {
			text += "  " + pair[1]
		}
		if runewidth.StringWidth(text) > width {
			continue
		}
		first = timelineTag(p.Focus, true) + tview.Escape(pair[0]) + "[-:-:-]"
		if pair[1] != "" {
			first += "  " + timelineTag(p.Muted, false) + tview.Escape(pair[1]) + "[-:-:-]"
		}
		break
	}
	lines := []string{first}
	state, color := "", p.Muted
	switch {
	case !v.destinationCurrent():
		state, color = "Context changed; these answers are retained. Reopen :probe for the current context.", p.Warning
	case v.loading || v.plan == nil && v.planErr == "":
		state, color = "Reading the Pods and their declared probes. Nothing is sent to any Pod.", p.Progress
	case v.plan == nil:
		state, color = "Not read: "+v.planErr, p.Failure
	case v.running:
		done := fmt.Sprintf("Sending: %d of %d done", v.run.Finished(), v.run.Planned())
		state, color = impactFit(width,
			strings.Join([]string{done, probe.Seconds(v.elapsed.Seconds()), "Esc cancels; what was not sent stays unsent"}, g.sep),
			done+g.sep+"Esc cancels"), p.Progress
	case v.run != nil && v.run.Sent() > 0:
		sent := fmt.Sprintf("%d sent in %s", v.run.Sent(), probe.Seconds(v.elapsed.Seconds()))
		at := v.ranAt.Format("15:04:05Z")
		scale := "full bar " + g.full + " = " + probe.Seconds(summary.Top)
		state = impactFit(width,
			strings.Join([]string{"Read-only GET requests you asked for", sent, at, scale, "nothing is sent unless you press r"}, g.sep),
			strings.Join([]string{sent, at, scale, "nothing is sent unless you press r"}, g.sep),
			strings.Join([]string{sent, at, scale}, g.sep),
			sent+g.sep+scale)
	case v.plan.Sendable() == 0 || v.plan.Access == probe.AccessDenied:
		state = "Nothing has been sent, and nothing can be: see below."
	default:
		count := probePlural(v.plan.Sendable()*v.attempts, "read-only GET request", "read-only GET requests")
		state = impactFit(width,
			"Nothing has been sent. Opening this view read Kubernetes objects only; r sends "+count+" after you confirm.",
			"Nothing has been sent. r sends "+count+" after you confirm.",
			"Nothing sent. r sends "+probePlural(v.plan.Sendable()*v.attempts, "GET", "GETs")+" after you confirm.")
	}
	lines = append(lines, timelineTag(color, false)+tview.Escape(ui.Truncate(state, width))+"[-:-:-]")
	if v.plan != nil {
		var gaps []string
		if v.plan.LeftOut > 0 {
			gaps = append(gaps, fmt.Sprintf("%s beyond the limit of %d are not listed and not probed",
				probePlural(v.plan.LeftOut, "more Pod", "more Pods"), probe.MaxPods))
		}
		gaps = append(gaps, v.plan.Notes...)
		if len(gaps) > 0 {
			lines = append(lines, timelineTag(p.Warning, false)+tview.Escape(ui.Truncate(strings.Join(gaps, g.sep), width))+"[-:-:-]")
		}
	}
	return lines
}

func (v *probeView) naturalWidths() (pod, node, zone int) {
	if v.plan == nil {
		return 0, 0, 0
	}
	for i := range v.plan.Targets {
		t := &v.plan.Targets[i].Pod
		pod = max(pod, runewidth.StringWidth(t.Name))
		node = max(node, runewidth.StringWidth(t.Node))
		zone = max(zone, runewidth.StringWidth(t.Zone))
	}
	return pod, node, zone
}

func (v *probeView) deadlineText() string {
	deadline := probe.DefaultDeadline
	if v.run != nil && v.run.Deadline > 0 {
		deadline = v.run.Deadline
	}
	return probe.Seconds(deadline.Seconds())
}

// probeRowColor is the color and weight of a verdict. The word and the
// "!" flag carry the meaning; the color repeats it.
func probeRowColor(p *config.SemanticPalette, verdict probe.Verdict) (config.Color, bool) {
	switch verdict {
	case probe.Outlier, probe.NoAnswer:
		return p.Failure, true
	case probe.Spikes, probe.Timeouts, probe.Errors:
		return p.Warning, true
	case probe.NotContacted, probe.Unprobed, probe.Canceled:
		return p.Muted, false
	}
	return p.Text, false
}

// probeTag is a tview tag that also sets the background, so that the
// selected row keeps its own colors on the selection surface.
func probeTag(color config.Color, bold bool, background string) string {
	fg, attr := graphHex(color), "-"
	if fg == "" {
		fg = "-"
	}
	if background == "" {
		background = "-"
	}
	if bold {
		attr = "b"
	}
	return "[" + fg + ":" + background + ":" + attr + "]"
}

// probeRow accumulates one line and counts its cells.
type probeRow struct {
	text    strings.Builder
	used    int
	surface string
}

func (r *probeRow) tag(color config.Color, bold bool) {
	r.text.WriteString(probeTag(color, bold, r.surface))
}

func (r *probeRow) write(text string) {
	r.text.WriteString(tview.Escape(text))
	r.used += runewidth.StringWidth(text)
}

func (r *probeRow) right(text string, width int) {
	if width > 0 {
		r.write(impactRight(text, width) + " ")
	}
}

func (r *probeRow) left(text string, width int) {
	if width > 0 {
		r.write(impactCell(text, width) + " ")
	}
}

// rowStrip writes the request strip of one run into the row.
func (v *probeView) rowStrip(row *probeRow, columns *probeColumns, attempts []probe.Attempt, summary *probe.Summary, dim bool) {
	if columns.strip == 0 {
		return
	}
	p := v.app.Styles.Semantic()
	limit := columns.strip
	if columns.spaced {
		limit = (columns.strip + 1) / 2
	}
	normal, slow := p.Focus, p.Failure
	if dim {
		normal, slow = p.Muted, p.Muted
	}
	strip, cells := v.probeStrip(attempts[:min(len(attempts), limit)], summary.Top, summary.SlowLine, columns.spaced, normal, slow, p.Muted, row.surface)
	row.text.WriteString(strip)
	row.used += cells
	row.tag(p.Text, false)
	row.write(strings.Repeat(" ", max(columns.strip-cells, 0)) + " ")
}

// rowNumbers writes the numbers of one run: fastest, median, slowest, the
// requests without an answer and the status codes. Nothing is written as
// a number before something was sent.
func (v *probeView) rowNumbers(row *probeRow, columns *probeColumns, stats *probe.PodStats) {
	ascii := v.noIcons()
	fastest, median, slowest, over, codes := "", "", "", "", ""
	// The counts are of requests with an outcome: one in flight or abandoned
	// was sent but says nothing about whether an answer came in time.
	if stats.Finished > 0 {
		fastest = "-"
		if stats.Answered > 0 {
			fastest = probe.Seconds(stats.Fastest.Seconds())
		}
		median, slowest = stats.TypicalText(ascii), stats.WorstText(ascii)
		over, codes = fmt.Sprintf("%d of %d", stats.TimedOut, stats.Finished), stats.CodesText()
	}
	row.right(fastest, columns.fastest)
	row.right(median, probeNumberWidth)
	row.right(slowest, columns.slowest)
	row.right(over, columns.over)
	row.right(codes, columns.codes)
}

// podRow is one line for a target. dim draws a previous run; selected puts
// the row on the selection surface and marks it with ">".
func (v *probeView) podRow(columns *probeColumns, label string, run *probe.Run, index int, stats *probe.PodStats, summary *probe.Summary, dim, selected bool) string {
	p := v.app.Styles.Semantic()
	color, bold := probeRowColor(&p, stats.Verdict)
	mark := "  "
	switch {
	case dim:
		color, bold = p.Muted, false
	case selected && stats.Verdict.Flagged():
		mark = ">!"
	case selected:
		mark = "> "
	case stats.Verdict.Flagged():
		mark = " !"
	}
	row := &probeRow{}
	if selected {
		row.surface = graphHex(p.Selected)
	}
	width := v.viewWidth()
	row.tag(color, bold || selected)
	row.write(mark + " " + label + " ")
	target := &v.plan.Targets[index]
	if target.Request == nil {
		row.tag(p.Muted, false)
		row.write(impactCell("not contacted: "+target.Skip, width-row.used))
		return row.text.String() + "[-:-:-]"
	}
	var attempts []probe.Attempt
	if run != nil && index < len(run.Pods) {
		attempts = run.Pods[index].Attempts
	}
	v.rowStrip(row, columns, attempts, summary, dim)
	row.tag(color, bold)
	v.rowNumbers(row, columns, stats)
	node, zone := target.Pod.Node, target.Pod.Zone
	if dim {
		node, zone = "", ""
	}
	row.left(node, columns.node)
	row.left(zone, columns.zone)
	word := stats.Verdict.Word()
	switch {
	case v.running && !dim && stats.Finished < stats.Planned:
		word = "probing"
	case columns.verdict >= probeVerdictWide && stats.Mark(v.noIcons()) != "":
		word += " " + stats.Mark(v.noIcons())
	}
	row.write(impactCell(word, columns.verdict))
	if selected && row.used < width {
		// The selection surface spans the row.
		row.write(strings.Repeat(" ", width-row.used))
	}
	return row.text.String() + "[-:-:-]"
}

func (v *probeView) tableHeading(columns *probeColumns) string {
	heading := "   " + impactCell("POD", columns.pod) + " "
	if columns.strip > 0 {
		label := "REQ"
		for _, candidate := range []string{"REQUESTS", "EACH REQUEST", "EACH REQUEST, IN ORDER", "EACH REQUEST, IN ORDER (height = time to first byte)"} {
			if len(candidate) <= columns.strip {
				label = candidate
			}
		}
		heading += impactCell(label, columns.strip) + " "
	}
	for _, column := range []struct {
		label string
		width int
	}{{"FASTEST", columns.fastest}, {"MEDIAN", probeNumberWidth}, {"SLOWEST", columns.slowest}, {"OVER " + v.deadlineText(), columns.over}, {"CODES", columns.codes}} {
		if column.width > 0 {
			heading += impactRight(column.label, column.width) + " "
		}
	}
	if columns.node > 0 {
		heading += impactCell("NODE", columns.node) + " "
	}
	if columns.zone > 0 {
		heading += impactCell("ZONE", columns.zone) + " "
	}
	return heading + impactCell("VS PEERS", columns.verdict)
}

// bodyRows builds the column heading and the rows: one per Pod, the
// previous run under it as a dimmer row, and a blank row between Pods when
// the height allows. It records the line of each Pod for scrolling.
func (v *probeView) bodyRows(summary, before *probe.Summary, spacers bool) (heading string, rows []string) {
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	v.rowOf = v.rowOf[:0]
	if v.plan == nil || len(v.plan.Targets) == 0 {
		text := "Reading..."
		switch {
		case v.planErr != "":
			text = "Nothing was read."
		case v.plan != nil:
			text = "No Pod is behind " + v.scope.Label() + "."
		}
		return "", []string{timelineTag(p.Muted, false) + tview.Escape(ui.Truncate(text, width)) + "[-:-:-]"}
	}
	podNatural, nodeNatural, zoneNatural := v.naturalWidths()
	attempts := v.attempts
	if v.run != nil {
		attempts = max(v.run.Attempts, 1)
	}
	if v.previous != nil {
		attempts = max(attempts, v.previous.Attempts)
	}
	columns := probeLayout(width, attempts, podNatural, nodeNatural, zoneNatural)
	heading = timelineTag(p.Focus, true) + tview.Escape(v.tableHeading(&columns)) + "[-:-:-]"
	if v.selectedTarget() == nil {
		v.selected = v.plan.Targets[0].Pod.Name
	}
	for i := range v.plan.Targets {
		name := v.plan.Targets[i].Pod.Name
		v.rowOf = append(v.rowOf, len(rows))
		rows = append(rows, v.podRow(&columns, probeTail(name, columns.pod, v.noIcons()), v.run, i, &summary.Pods[i], summary, false, name == v.selected))
		if v.previous != nil && before != nil && i < len(v.previous.Pods) && v.previous.Pods[i].Attempts != nil {
			label := "previous " + v.previousAt.Format("15:04:05Z")
			if columns.pod < runewidth.StringWidth(label) {
				label = "prev " + v.previousAt.Format("15:04:05")
			}
			rows = append(rows, v.podRow(&columns, impactCell(label, columns.pod), v.previous, i, &before.Pods[i], summary, true, false))
		}
		if spacers && i < len(v.plan.Targets)-1 {
			rows = append(rows, "")
		}
	}
	return heading, rows
}

// scrollToSelection keeps the selected Pod, and its previous-run row,
// inside the visible body lines.
func (v *probeView) scrollToSelection(perPod int) {
	visible := max(v.bodyHeight, 1)
	row := 0
	for i := range v.rowOf {
		if v.plan != nil && i < len(v.plan.Targets) && v.plan.Targets[i].Pod.Name == v.selected {
			row = v.rowOf[i]
		}
	}
	if row < v.offset {
		v.offset = row
	}
	if last := row + perPod - 1; last >= v.offset+visible {
		v.offset = last - visible + 1
	}
	v.offset = max(min(v.offset, v.bodyLines-visible), 0)
	v.body.ScrollTo(v.offset, 0)
}

// selectedLine states everything about the Pod under the cursor that the
// columns had to leave out: every time, the status codes, where it runs
// and which declared probe supplies the request.
func (v *probeView) selectedLine(summary *probe.Summary) string {
	target := v.selectedTarget()
	if target == nil {
		return ""
	}
	g := v.glyphs()
	ascii := v.noIcons()
	parts := []string{v.plan.ShortName(target.Pod.Name)}
	where := target.Pod.Node
	if where != "" && target.Pod.Zone != "" {
		where += " (" + target.Pod.Zone + ")"
	}
	if where != "" {
		parts[0] += " on " + where
	}
	if target.Request == nil {
		return parts[0] + ": not contacted - " + target.Skip
	}
	index := -1
	for i := range v.plan.Targets {
		if &v.plan.Targets[i] == target {
			index = i
		}
	}
	if v.run != nil && index >= 0 && index < len(v.run.Pods) && summary.Pods[index].Sent > 0 {
		var times []string
		for _, attempt := range v.run.Pods[index].Attempts {
			switch attempt.Outcome {
			case probe.NotSent:
			case probe.Answered:
				times = append(times, probe.Seconds(attempt.Took.Seconds()))
			case probe.TimedOut:
				times = append(times, "timeout")
			case probe.InFlight:
				times = append(times, "waiting")
			case probe.Abandoned:
				times = append(times, "abandoned")
			default:
				times = append(times, attempt.Detail)
			}
		}
		// The first twelve are written out; the strip shows every one.
		if shown := 12; len(times) > shown {
			times = append(times[:shown], fmt.Sprintf("... (%d more)", len(times)-shown))
		}
		parts = append(parts, strings.Join(times, " "))
		if codes := summary.Pods[index].CodesDetail(ascii); codes != "" {
			parts = append(parts, "HTTP "+codes)
		}
	} else if !target.Pod.Ready {
		parts = append(parts, "not Ready")
	}
	declared := target.Request.Kind.Field() + " of container " + target.Request.Container
	if target.Period > 0 {
		declared += fmt.Sprintf(", kubelet every %s", probe.Seconds(target.Period.Seconds()))
	}
	if target.HeadersOmitted > 0 {
		declared += fmt.Sprintf(", %s not sent", probePlural(target.HeadersOmitted, "declared header", "declared headers"))
	}
	parts = append(parts, declared)
	return strings.Join(parts, g.sep)
}

// measuredNote says what path the time covers, and the floor every reading
// includes. It is never dropped: without it a number here reads as the
// time a user waits.
func (v *probeView) measuredNote(summary *probe.Summary, short bool) string {
	g := v.glyphs()
	floor, brief := "", ""
	switch {
	case summary.FloorPod >= 0 && v.plan != nil && summary.Compared > 1:
		name := v.plan.ShortName(v.plan.Targets[summary.FloorPod].Pod.Name)
		floor = fmt.Sprintf(" The proxy adds its own time to every answer: the fastest Pod here, %s, has a median of %s, which is the floor.",
			name, probe.Seconds(summary.Floor.Seconds()))
		brief = " Floor: " + probe.Seconds(summary.Floor.Seconds()) + "."
	case summary.FloorPod >= 0:
		floor = " The proxy adds its own time to every answer, and one Pod has no faster peer to show how much."
		brief = " Includes proxy time."
	}
	if short {
		return "API server " + g.arrow + " Pod, not ingress or Service: finds a slow Pod, not a slow path." + brief
	}
	return "Time is measured from the API server through its Pod proxy to the Pod, not through the ingress or the Service. " +
		"It locates a slow Pod, not a slow network path." + floor
}

// nextLine proposes the checks that exist in this view, by their real keys.
func (v *probeView) nextLine(summary *probe.Summary) string {
	if v.plan == nil || v.running || !v.destinationCurrent() {
		return ""
	}
	g := v.glyphs()
	var parts []string
	flagged := -1
	for i := range summary.Pods {
		if summary.Pods[i].Verdict.Flagged() && (flagged < 0 || v.plan.Targets[i].Pod.Name == v.selected) {
			flagged = i
		}
	}
	ran := v.run != nil && v.run.Sent() > 0
	switch {
	case ran && flagged >= 0:
		name := v.plan.ShortName(v.plan.Targets[flagged].Pod.Name)
		if v.plan.Targets[flagged].Pod.Name != v.selected {
			parts = append(parts, "select "+name)
		}
		parts = append(parts, "Enter opens "+name+" in the Pod list")
		// A check is proposed only when its key would act: the graph
		// needs a declared Prometheus source.
		pod := SelectedResourceTarget{Context: v.contextName, GVR: client.PodGVR, Namespace: v.plan.Targets[flagged].Pod.Namespace,
			Name: v.plan.Targets[flagged].Pod.Name}
		if graphAvailability(v.app, pod) == "" {
			parts = append(parts, "m graphs its CPU against its limit")
		}
		if v.runUnavailable() == "" {
			parts = append(parts, "r runs again to see whether it repeats")
		}
	case ran:
		if v.runUnavailable() == "" {
			parts = append(parts, "r runs again (this run stays as a dimmer strip)")
			for _, count := range probe.AttemptChoices {
				if count > v.attempts {
					parts = append(parts, fmt.Sprintf("+ sends %d per pod", count))
					break
				}
			}
		}
	case v.runUnavailable() == "":
		parts = append(parts, "r sends the requests after you confirm")
	}
	if _, err := slowTarget(v.scopeTarget()); err == nil && ran {
		parts = append(parts, slowKeyName+" slow requests by hop")
	}
	if len(parts) == 0 {
		return ""
	}
	width := v.viewWidth()
	for len(parts) > 1 && runewidth.StringWidth("next: "+strings.Join(parts, g.sep)) > width {
		parts = parts[:len(parts)-1]
	}
	return "next: " + strings.Join(parts, g.sep)
}

// unsentLines says, before anything was sent, what r would send or why it
// can send nothing.
func (v *probeView) unsentLines(add func(text string, color config.Color, bold bool)) {
	p := v.app.Styles.Semantic()
	switch {
	case v.plan.Sendable() == 0:
		add("Nothing can be sent.", p.Warning, true)
		who := fmt.Sprintf("None of the %d listed Pods declares", len(v.plan.Targets))
		if len(v.plan.Targets) == 1 {
			who = "The listed Pod does not declare"
		}
		add(fmt.Sprintf("%s an httpGet %s or %s, and this command sends only a request a Pod declares for itself. "+
			"The path is never typed.", who, probe.Readiness.Field(), probe.Liveness.Field()), p.Text, false)
	case v.plan.Access == probe.AccessDenied:
		add("Not allowed.", p.Failure, true)
		add("The API server says this user is "+v.plan.AccessReason+". Nothing was sent and r will send nothing.", p.Text, false)
	default:
		add("Nothing has been sent.", p.Focus, true)
		long, _ := v.requestWords()
		pace := "one after another"
		if v.plan.Sendable() > 1 {
			pace = fmt.Sprintf("one after another per Pod, at most %d Pods at a time", probe.Concurrency)
		}
		add(fmt.Sprintf("r sends %s: %s in all, %s. You are asked first.",
			long, probePlural(v.plan.Sendable()*v.attempts, "request", "requests"), pace), p.Text, false)
		add(fmt.Sprintf("Path and port are those of each Pod's own %s, the request the kubelet already makes. "+
			"The application handles it and will see it in its logs.", v.plan.Kind.Field()), p.Text, false)
		if v.plan.Access == probe.AccessUnknown && v.plan.AccessReason != "" {
			add("Whether this user may use pods/proxy is not known: "+v.plan.AccessReason+".", p.Muted, false)
		}
	}
}

// detailLines is the pane under the table. level trades air and wording for
// height: 0 has blank lines, 1 has none, 2 shortens the measured note, 3
// also drops the selected-Pod line. The result and the note always remain.
func (v *probeView) detailLines(summary *probe.Summary, level int) []probeLine {
	p := v.app.Styles.Semantic()
	width := v.viewWidth()
	ascii := v.noIcons()
	var out []probeLine
	add := func(text string, color config.Color, bold bool) {
		for _, part := range impactWrap(text, width) {
			out = append(out, probeLine{part, color, bold})
		}
	}
	blank := func() {
		if level == 0 && len(out) > 0 && out[len(out)-1].text != "" {
			out = append(out, probeLine{})
		}
	}
	switch {
	case v.planErr != "":
		add("Nothing was read: "+v.planErr+".", p.Failure, true)
		add("No request was sent to any Pod. Esc goes back; open :probe again to read again.", p.Muted, false)
		return out
	case v.plan == nil:
		add("Reading the Pods behind "+v.scope.Label()+" and their declared readiness and liveness probes.", p.Text, false)
		add("These are bounded Kubernetes reads. No request goes to any Pod until you press r and confirm.", p.Muted, false)
		return out
	case len(v.plan.Targets) == 0:
		add("No Pod is behind "+v.scope.Label()+", so there is nothing to probe.", p.Warning, true)
		return out
	}
	if line := v.selectedLine(summary); line != "" && level < 3 {
		add(line, p.Muted, false)
	}
	blank()
	ran := v.run != nil && v.run.Sent() > 0
	switch {
	case ran || v.running:
		add("Result", p.Focus, true)
		for i, line := range probe.Result(v.plan, v.run, summary, ascii) {
			color := p.Text
			if i == 0 && (summary.Denied || strings.HasPrefix(line, "Only ") || strings.Contains(line, "slower than their peers")) {
				color = p.Failure
			}
			if i == 0 && strings.Contains(line, "gave no answer") {
				color = p.Warning
			}
			add(line, color, i == 0)
		}
		if v.running {
			add("Still sending: the numbers and words above are not final.", p.Progress, false)
		}
	default:
		v.unsentLines(add)
	}
	blank()
	add(v.measuredNote(summary, level >= 2), p.Muted, false)
	if next := v.nextLine(summary); next != "" {
		blank()
		out = append(out, probeLine{next, p.Warning, false})
	}
	return out
}

func (v *probeView) footerText() string {
	width := v.viewWidth()
	if v.running {
		return impactFit(width, "Esc cancels the probe; answers so far stay on screen", "Esc cancels the probe")
	}
	g := v.glyphs()
	count := fmt.Sprintf("%s%d", g.times, v.attempts)
	// Six forms, longest first; each names the run, the count, the other
	// probe, the sibling views and the way back to the calm page (a) as
	// far as the width allows.
	forms := [][]string{
		{"r run", "+ more  _ fewer per pod (" + count + ")", "u " + v.otherKind() + " probe", "Enter open pod", "m graph", "t timeline", slowKeyName + " slow", "a less detail", "Esc back"},
		{"r run", "+ more  _ fewer per pod (" + count + ")", "u " + v.otherKind() + " probe", "Enter open pod", "m graph", "t timeline", slowKeyName + " slow", "a less", "Esc back"},
		{"r run", "+/_ " + count + " per pod", "u probe", "Enter pod", "m graph", "t timeline", slowKeyName + " slow", "a less", "Esc back"},
		{"r run", "+/_ " + count, "u probe", "Enter pod", "m graph", slowKeyName + " slow", "a less", "Esc"},
		{"r run", "+/_ " + count, "Enter pod", "m graph", slowKeyName + " slow", "a less", "Esc"},
		{"r run", "+/_ " + count, "Enter pod", "m graph", "a less", "Esc"},
	}
	runnable := v.plan == nil || v.runUnavailable() == ""
	several := v.plan != nil && len(v.plan.Available) > 1
	texts := make([]string, 0, len(forms))
	for _, form := range forms {
		var keys []string
		for _, key := range form {
			switch {
			// A key that would send nothing is not offered; the pane says why.
			case !runnable && (strings.HasPrefix(key, "r run") || strings.HasPrefix(key, "+")):
			case !several && strings.HasPrefix(key, "u "):
			default:
				keys = append(keys, key)
			}
		}
		texts = append(texts, strings.Join(keys, g.sep))
	}
	return impactFit(width, texts...)
}

// otherKind names the declared probe that u switches to.
func (v *probeView) otherKind() string {
	if v.plan == nil {
		return ""
	}
	return string(v.plan.NextKind())
}

// render draws the calm page or, with all detail, lays the view out as the
// header, the table, the result pane and the key line. The table keeps at
// least three Pods in view; the pane gives up air and wording before it
// loses a line of the result.
func (v *probeView) render() {
	if v.header == nil || v.rendering {
		return
	}
	v.rendering = true
	defer func() { v.rendering = false }()
	var summary, before probe.Summary
	if v.plan != nil {
		summary = probe.Summarize(v.plan, v.run, v.previous)
		if v.previous != nil {
			before = probe.Summarize(v.plan, v.previous, nil)
		}
	}
	if !v.allDetail {
		v.renderCalm(&summary)
		return
	}
	width := v.viewWidth()
	lines := v.headerLines(&summary)
	pods, perPod := 0, 1
	if v.plan != nil {
		pods = len(v.plan.Targets)
	}
	if v.previous != nil {
		perPod = 2
	}
	tableWant := 1 + max(pods, 1)*perPod
	tableMin := 1 + min(max(pods, 1), 3)*perPod
	available := 1 << 20
	if v.height > 0 {
		available = max(v.height-len(lines)-1, 2)
	}
	detail := v.detailLines(&summary, 0)
	// With room to spare the view breathes like a page: a blank line under
	// the header, between Pods and above the result, which then follows
	// the table directly. Without it the table scrolls and the pane keeps
	// its place above the key line.
	airy := v.height > 0 && tableWant+max(pods-1, 0)+2+len(detail) <= available
	for level := 1; !airy && level <= 3 && tableWant+len(detail) > available; level++ {
		detail = v.detailLines(&summary, level)
	}
	if airy {
		lines = append(lines, "")
		detail = append([]probeLine{{}}, detail...)
	}
	heading, rows := v.bodyRows(&summary, &before, airy)
	if heading != "" {
		lines = append(lines, heading)
	}
	if len(lines) != v.headerHeight {
		v.headerHeight = len(lines)
		v.Flex.ResizeItem(v.header, v.headerHeight, 0)
	}
	v.header.SetText(strings.Join(lines, "\n"))
	// The column heading is the last header line, so the body holds Pods
	// only and scrolls under it.
	bodyWant, bodyMin := tableWant-1, tableMin-1
	height := min(len(detail), max(available-1-bodyMin, 1))
	v.bodyLines = len(rows)
	if airy {
		v.Flex.ResizeItem(v.body, len(rows), 0)
		v.Flex.ResizeItem(v.detail, 0, 1)
		height, v.bodyHeight = len(detail), len(rows)
	} else {
		v.Flex.ResizeItem(v.body, 0, 1)
		v.Flex.ResizeItem(v.detail, height, 0)
		v.bodyHeight = min(max(available-1-height, 1), bodyWant)
	}
	v.detailHeight = height
	v.body.SetText(strings.Join(rows, "\n"))
	v.scrollToSelection(perPod)
	v.detail.SetText(probeTagged(detail[:min(len(detail), max(height, 0))], width))
	v.detail.ScrollToBeginning()
	v.footer.SetText(timelineTag(v.app.Styles.Semantic().Muted, false) + tview.Escape(v.footerText()) + "[-:-:-]")
}
