// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// slowDepsFolded is the row that stands for every dependency when the
// screen is short and none of them is raised.
const slowDepsFolded = "dependencies:folded"

// slowPlain writes the few non-ASCII characters of the report in plain
// characters for the no-icons presentation.
var slowPlain = strings.NewReplacer("…", "...", "→", "->", "–", "-", "µ", "u", "×", "x", "↑↓", "Up/Down", "←→", "Left/Right")

// slowHardWrap breaks text into lines of at most width cells. It prefers a
// space and otherwise cuts inside a word: a query has few spaces.
func slowHardWrap(text string, width int) []string {
	width = max(width, 8)
	var lines []string
	for runewidth.StringWidth(text) > width {
		cut, cells, space := 0, 0, -1
		for index, r := range text {
			w := runewidth.RuneWidth(r)
			if cells+w > width {
				break
			}
			cells += w
			cut = index + len(string(r))
			if r == ' ' && cells > width/2 {
				space = cut
			}
		}
		if space > 0 {
			cut = space
		}
		lines = append(lines, strings.TrimRight(text[:cut], " "))
		text = text[cut:]
	}
	return append(lines, text)
}

func (v *slowView) plain(text string) string {
	if v.noIcons() {
		return slowPlain.Replace(text)
	}
	return text
}

func (v *slowView) ellipsis() string {
	if v.noIcons() {
		return "..."
	}
	return "…"
}

// fit truncates text to width terminal cells, marking the cut, and pads it.
func (v *slowView) fit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.FillRight(runewidth.Truncate(v.plain(text), width, v.ellipsis()), width)
}

// cut truncates without padding.
func (v *slowView) cut(text string, width int) string {
	return runewidth.Truncate(v.plain(text), max(width, 0), v.ellipsis())
}

// tail keeps the end of a name, which is the part that tells Pods apart.
func (v *slowView) tail(text string, width int) string {
	text = v.plain(text)
	if runewidth.StringWidth(text) <= width {
		return runewidth.FillRight(text, width)
	}
	mark := v.ellipsis()
	runes := []rune(text)
	for len(runes) > 0 && runewidth.StringWidth(mark+string(runes)) > width {
		runes = runes[1:]
	}
	return runewidth.FillRight(mark+string(runes), width)
}

// slowColumns are widths in terminal cells; zero hides a column. Hop, the
// key number and the state word always remain.
type slowColumns struct {
	hop, what, spark, key, state, evidence int
}

func slowLayout(width int) slowColumns {
	var c slowColumns
	switch {
	case width >= 120:
		c = slowColumns{hop: 19, what: 25, spark: 14, key: 14, state: 8}
	case width >= 76:
		c = slowColumns{hop: 18, spark: 10, key: 13, state: 8}
	default:
		c = slowColumns{hop: 18, key: 13, state: 8}
	}
	used := 2
	for _, w := range []int{c.hop, c.what, c.spark, c.key, c.state} {
		if w > 0 {
			used += w + 1
		}
	}
	if rest := width - used; rest >= 12 {
		c.evidence = rest
	}
	return c
}

func (v *slowView) stateColor(state slowpath.State) (config.Color, bool) {
	p := v.app.Styles.Semantic()
	switch state {
	case slowpath.Outlier:
		return p.Failure, true
	case slowpath.Elevated:
		return p.Warning, false
	case slowpath.NotRead:
		return p.Unknown, false
	}
	return p.Muted, false
}

// stateWord is the status word of a row. The outlier is capitalised so it
// stands out without color.
func slowStateWord(state slowpath.State) string {
	if state == slowpath.Outlier {
		return "OUTLIER"
	}
	return string(state)
}

// podsListed says whether the Pods of the app hop have their own rows.
func (v *slowView) podsListed() bool {
	if v.expanded != nil {
		return *v.expanded
	}
	report := v.report()
	if report == nil {
		return false
	}
	app := report.Hop(slowpath.HopApp)
	return app != nil && app.State == slowpath.Outlier && len(app.Pods) > 0 && len(app.Pods) <= 6 && v.height >= 30
}

// foldedDependencies returns the dependency hops when they are shown as
// one row: the screen is short and they share one state that is not raised.
func (v *slowView) foldedDependencies() []*slowpath.Hop {
	report := v.report()
	if report == nil || v.height <= 0 || v.height >= 28 {
		return nil
	}
	var deps []*slowpath.Hop
	for i := range report.Hops {
		hop := &report.Hops[i]
		if !strings.HasPrefix(hop.Spec.ID, "dep:") {
			continue
		}
		if hop.State != slowpath.Quiet && hop.State != slowpath.NotRead {
			return nil
		}
		if len(deps) > 0 && deps[0].State != hop.State {
			return nil
		}
		deps = append(deps, hop)
	}
	if len(deps) < 2 {
		return nil
	}
	return deps
}

func (v *slowView) buildRows() {
	v.rows = nil
	report := v.report()
	if report == nil {
		return
	}
	folded := v.foldedDependencies()
	listed := v.podsListed()
	for i := range report.Hops {
		hop := &report.Hops[i]
		if len(folded) > 0 && strings.HasPrefix(hop.Spec.ID, "dep:") {
			if hop == folded[0] {
				v.rows = append(v.rows, slowRow{hop: slowDepsFolded})
			}
			continue
		}
		v.rows = append(v.rows, slowRow{hop: hop.Spec.ID})
		if hop.Spec.ID == slowpath.HopApp && listed {
			for p := range hop.Pods {
				v.rows = append(v.rows, slowRow{hop: hop.Spec.ID, pod: hop.Pods[p].Pod.Name})
			}
		}
	}
	// The focus follows a row that folding or collapsing took away.
	found := false
	for _, row := range v.rows {
		found = found || row == v.focus
	}
	if !found && len(v.rows) > 0 {
		switch {
		case v.focus.pod != "":
			v.focus = slowRow{hop: v.focus.hop}
		case strings.HasPrefix(v.focus.hop, "dep:") && len(folded) > 0:
			v.focus = slowRow{hop: slowDepsFolded}
		case v.focus.hop == slowDepsFolded:
			for _, row := range v.rows {
				if strings.HasPrefix(row.hop, "dep:") {
					v.focus = row
					break
				}
			}
		default:
			v.focus = v.rows[0]
		}
	}
}

// focusedHops returns the hops the focused row stands for.
func (v *slowView) focusedHops() []*slowpath.Hop {
	report := v.report()
	if report == nil {
		return nil
	}
	if v.focus.hop == slowDepsFolded {
		return v.foldedDependencies()
	}
	if hop := report.Hop(v.focus.hop); hop != nil {
		return []*slowpath.Hop{hop}
	}
	return nil
}

func (v *slowView) render() {
	if v.header == nil || v.rendering {
		return
	}
	v.rendering = true
	defer func() { v.rendering = false }()
	width, height := v.viewWidth(), v.height
	if height <= 0 {
		height = 20
	}
	header := v.headerLines(width)
	v.buildRows()
	tableHeight := 1 + len(v.rows)
	const minBody = 7
	if rest := height - len(header) - 1 - tableHeight; rest < minBody {
		tableHeight = max(min(tableHeight, 3), height-len(header)-1-minBody)
	}
	if len(header) != v.headerHeight {
		v.headerHeight = len(header)
		v.Flex.ResizeItem(v.header, v.headerHeight, 0)
	}
	if tableHeight != v.tableHeight {
		v.tableHeight = tableHeight
		v.Flex.ResizeItem(v.table, tableHeight, 0)
	}
	v.header.SetText(strings.Join(header, "\n"))
	v.renderRows(width)
	body := v.bodyLines(width, height-len(header)-1-tableHeight)
	v.body.SetText(strings.Join(body, "\n"))
	v.body.ScrollToBeginning()
	// The plain form of an arrow is a word, so a hint is measured as drawn.
	arrows, sides := v.plain("↑↓"), v.plain("←→")
	// The key that switches the trends between blocks and dots is named
	// wherever a form leaves room for it.
	forms := chartStyleForms([]string{
		arrows + " hop · Enter open · " + sides + " cursor · n/N episode · 1 15m  2 1h  3 6h  4 24h · s queries and rules · r re-read · Esc back",
		arrows + " hop · Enter open · " + sides + " cursor · n/N episode · 1-4 window · s queries · r re-read · Esc back",
		arrows + " hop · Enter open · " + sides + " cursor · 1-4 window · s queries · r read · Esc",
		"Enter open · " + sides + " cursor · 1-4 window · s queries · Esc",
		"Enter open · 1-4 window · s queries · Esc",
	}, v.app.chartStyle())
	v.footer.SetText(timelineTag(v.app.Styles.Semantic().Muted, false) + tview.Escape(impactFit(width, forms...)) + "[-:-:-]")
}

// titleLine names the Service, the window in clock time, when it was read
// and how much of the batch was answered, as far as the width allows.
func (v *slowView) titleLine(width int) string {
	title := slowTitle + " / " + v.selection.Label()
	short := slowTitle + " / " + v.selection.Name
	long := title
	report, path := v.report(), v.path()
	if path != nil {
		title = slowTitle + " / Service " + path.Key()
		short = slowTitle + " / " + path.Service
		long = title
		if v.selection.Kind != slowpath.KindService {
			long += " (from " + v.selection.Kind + " " + v.selection.Name + ")"
		}
	}
	if report == nil {
		return title
	}
	// A window that crosses midnight names both days.
	layout := "15:04"
	if report.Start.UTC().YearDay() != report.End.UTC().YearDay() {
		layout = "Jan02 15:04"
	}
	window := "last " + report.Span() + " " + report.Start.UTC().Format(layout) + "–" + report.End.UTC().Format(layout) + " UTC"
	read, step := "read "+report.ReadAt.Format("15:04:05"), "step "+impact.Dur(report.Step)
	source := "prometheus " + report.Source
	if report.Source == "" {
		source = "prometheus not read"
	}
	answered := fmt.Sprintf("%d/%d queries", report.Answered, report.Asked)
	return impactFit(width,
		impactJoin(long, window, read, step, source, answered),
		impactJoin(long, window, read, step, answered),
		impactJoin(long, window, read, answered),
		impactJoin(long, window, read),
		impactJoin(title, window, read, step, source, answered),
		impactJoin(title, window, read, step, answered),
		impactJoin(title, window, read, answered),
		impactJoin(title, window, read),
		impactJoin(short, window, read),
		impactJoin(short, "last "+report.Span(), read),
		impactJoin(short, report.Span()))
}

// headerLines state what is read, over which window, and the episodes.
func (v *slowView) headerLines(width int) []string {
	p := v.app.Styles.Semantic()
	type line struct {
		text  string
		color config.Color
		bold  bool
	}
	var lines []line
	add := func(text string, color config.Color, bold bool) { lines = append(lines, line{text, color, bold}) }
	report, path := v.report(), v.path()
	title := v.titleLine(width)
	add(title, p.Focus, true)
	switch {
	case v.read == nil:
		add("Reading the configured path, then one bounded batch from the declared Prometheus.", p.Text, false)
	case report == nil:
		for _, part := range impactWrap("No request path: "+v.read.err.Error()+".", width) {
			add(part, p.Failure, true)
		}
	default:
		color := p.Text
		switch {
		case report.Failure != "":
			color = p.Failure
		case len(report.Episodes) > 0:
			color = p.Warning
		}
		headline := report.Headline
		if report.HeadlineShort != "" && runewidth.StringWidth(headline) > width {
			headline = report.HeadlineShort
		}
		parts := impactWrap(headline, width)
		for index, part := range parts {
			if index == 1 && len(parts) > 2 {
				add(v.cut(part, width-1)+v.ellipsis(), color, true)
				break
			}
			add(part, color, true)
		}
	}
	switch {
	case !v.destinationCurrent():
		add("Destination changed; this read is retained. Reopen :"+slowCommandToken+" for the current context.", p.Warning, false)
	case v.loading && v.read != nil:
		add("Reading again; what is shown is retained until the new read arrives.", p.Progress, false)
	case v.read != nil && v.read.err != nil && report != nil:
		add("Re-read failed, the earlier read is shown: "+v.read.err.Error(), p.Warning, false)
	case path != nil && len(path.Notes) > 0 && v.height >= 30:
		add("note: "+strings.Join(path.Notes, "; "), p.Muted, false)
	}
	out := make([]string, 0, len(lines))
	for _, item := range lines {
		out = append(out, timelineTag(item.color, item.bold)+tview.Escape(v.cut(item.text, width))+"[-:-:-]")
	}
	return out
}

// slowSparkTop is the value drawn at the top of a row's sparkline: the
// peak, and at least twice the usual level, so that a steady signal draws
// a low flat line and only a burst reaches the top.
func slowSparkTop(points []chart.Point) float64 {
	top := 2 * slowpath.Median(points)
	for _, point := range points {
		top = max(top, point.V)
	}
	return top
}

// keyCell keeps the number of a key ("p99 48s") whole and shortens the
// name before it when the cell is narrow.
func (v *slowView) keyCell(key string, width int) string {
	key = v.plain(key)
	if runewidth.StringWidth(key) <= width {
		return runewidth.FillRight(key, width)
	}
	cut := strings.LastIndex(key, " ")
	if cut <= 0 || strings.HasSuffix(key, " ready") || strings.HasSuffix(key, " read") {
		return v.fit(key, width)
	}
	name, value := key[:cut], key[cut:]
	room := width - runewidth.StringWidth(value)
	if room < 3 {
		return v.fit(key, width)
	}
	return runewidth.FillRight(runewidth.Truncate(name, room, v.ellipsis())+value, width)
}

func (v *slowView) renderRows(width int) {
	v.table.Clear()
	p := v.app.Styles.Semantic()
	report := v.report()
	if report == nil {
		text := "Nothing has been read yet."
		if v.read == nil {
			text = "Reading..."
		}
		v.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(v.fit(text, width))).SetSelectable(false).SetTextColor(p.Muted.Color()))
		return
	}
	columns := slowLayout(width)
	heading := "  " + v.fit("HOP", columns.hop) + " "
	if columns.what > 0 {
		heading += v.fit("WHAT", columns.what) + " "
	}
	if columns.spark > 0 {
		heading += v.fit("LAST "+report.Span(), columns.spark) + " "
	}
	heading += v.fit("WORST", columns.key) + " " + v.fit("STATE", columns.state) + " "
	if columns.evidence > 0 {
		heading += v.fit("EVIDENCE", columns.evidence)
	}
	v.table.SetCell(0, 0, tview.NewTableCell(timelineTag(p.Muted, true)+tview.Escape(strings.TrimRight(heading, " "))+"[-:-:-]").SetSelectable(false))
	frame := chart.Frame{Start: report.Start, End: report.End, Step: report.Step, Width: columns.spark}
	selected := 0
	podTop := 0.0
	if app := report.Hop(slowpath.HopApp); app != nil {
		for i := range app.Pods {
			podTop = max(podTop, slowSparkTop(app.Pods[i].Trend))
		}
	}
	folded := v.foldedDependencies()
	for index, row := range v.rows {
		line := slowRowText(report, row, folded, podTop)
		if row == v.focus {
			selected = index
		}
		cell := tview.NewTableCell(v.rowCell(&line, row, columns, frame)).SetExpansion(1)
		if row == v.focus {
			cell.SetBackgroundColor(p.Selected.Color())
		}
		v.table.SetCell(index+1, 0, cell)
	}
	// Keep the focused row on screen when the table is shorter than its rows.
	visible := max(v.tableHeight-1, 1)
	offset, _ := v.table.GetOffset()
	switch {
	case selected < offset:
		offset = selected
	case selected >= offset+visible:
		offset = selected - visible + 1
	}
	v.table.SetOffset(min(offset, max(len(v.rows)-visible, 0)), 0)
}

// rowCell is the tagged text of one table row: hop, what is read, the
// trend, the key number, the state word and the evidence. The state word
// carries the meaning; its color only repeats it.
func (v *slowView) rowCell(line *slowLine, row slowRow, columns slowColumns, frame chart.Frame) string {
	p := v.app.Styles.Semantic()
	color, bold := v.stateColor(line.state)
	marker := "  "
	if row == v.focus {
		marker = "> "
	}
	nameColor := p.Text
	switch line.state {
	case slowpath.NotRead:
		nameColor = p.Muted
	case slowpath.Outlier:
		nameColor = color
	}
	var b strings.Builder
	b.WriteString(timelineTag(nameColor, bold))
	b.WriteString(tview.Escape(marker + v.fit(line.hop, columns.hop) + " "))
	if columns.what > 0 {
		what := v.fit(line.what, columns.what)
		if row.pod != "" {
			what = v.tail(line.what, columns.what)
		}
		b.WriteString(tview.Escape(what + " "))
	}
	if columns.spark > 0 {
		spark := strings.Repeat(" ", columns.spark)
		if len(line.trend) > 0 {
			sparkColor := p.Focus
			if line.state == slowpath.Outlier || line.state == slowpath.Elevated {
				sparkColor = color
			}
			spark = chart.Spark(chart.SparkValues(line.trend, frame), chart.SparkOptions{ASCII: v.noIcons(), Style: chart.SparkStyleOf(v.app.chartStyle()), Max: line.top,
				Color: graphColor(sparkColor), GapColor: graphColor(p.Muted)})
		}
		b.WriteString("[-:-:-]" + spark + " ")
	}
	b.WriteString(timelineTag(color, bold))
	b.WriteString(tview.Escape(v.keyCell(line.key, columns.key) + " " + v.fit(slowStateWord(line.state), columns.state) + " "))
	if columns.evidence > 0 {
		evidenceColor := p.Muted
		if line.state == slowpath.Outlier {
			evidenceColor = color
		}
		b.WriteString(timelineTag(evidenceColor, false))
		b.WriteString(tview.Escape(v.evidenceCell(line.evidence, line.briefs, columns.evidence)))
	}
	b.WriteString("[-:-:-]")
	return b.String()
}

// evidenceCell fits evidence into its column. It drops whole items from
// the end (they are separated by a middle dot) and marks that it did; when
// not even the first item fits it uses the longest short form that does,
// and cuts only then.
func (v *slowView) evidenceCell(evidence string, briefs []string, width int) string {
	evidence = v.plain(evidence)
	if runewidth.StringWidth(evidence) <= width {
		return evidence
	}
	const sep = " · "
	parts := strings.Split(evidence, sep)
	mark := " " + v.ellipsis()
	for keep := len(parts) - 1; keep >= 1; keep-- {
		if text := strings.Join(parts[:keep], sep) + mark; runewidth.StringWidth(text) <= width {
			return text
		}
	}
	for index, brief := range briefs {
		if brief = v.plain(brief); runewidth.StringWidth(brief) <= width || index == len(briefs)-1 {
			return v.cut(brief, width)
		}
	}
	return v.cut(evidence, width)
}

// graphColor is the tview color of a semantic token, or "" for the
// terminal default.
func graphColor(color config.Color) string {
	hex := color.Color().Hex()
	if hex < 0 {
		return ""
	}
	return fmt.Sprintf("#%06x", hex)
}

type slowLine struct {
	hop, what, key, evidence string
	briefs                   []string
	state                    slowpath.State
	trend                    []chart.Point
	top                      float64
}

// rowText is the content of one table row.
func slowRowText(report *slowpath.Report, row slowRow, folded []*slowpath.Hop, podTop float64) slowLine {
	if row.hop == slowDepsFolded && len(folded) > 0 {
		names := make([]string, 0, len(folded))
		for _, hop := range folded {
			names = append(names, hop.Spec.Short)
		}
		line := slowLine{hop: fmt.Sprintf("pods → %d deps", len(folded)), what: strings.Join(names, " · "), state: folded[0].State,
			key: fmt.Sprintf("%d read", len(folded)), evidence: "no rule crossed: " + strings.Join(names, ", ")}
		if line.state == slowpath.NotRead {
			line.key, line.evidence = "-", folded[0].Reason
		}
		return line
	}
	hop := report.Hop(row.hop)
	if hop == nil {
		return slowLine{hop: row.hop, state: slowpath.NotRead, key: "-"}
	}
	if row.pod != "" {
		for i := range hop.Pods {
			pod := &hop.Pods[i]
			if pod.Pod.Name != row.pod {
				continue
			}
			return slowLine{hop: "  " + report.ShortPod(pod.Pod.Name), what: pod.Pod.Name, key: pod.Key, evidence: pod.Evidence,
				state: pod.State, trend: pod.Trend, top: podTop}
		}
	}
	line := slowLine{hop: hop.Spec.Label(), what: hop.Spec.What, key: hop.Key, evidence: hop.Evidence, briefs: hop.Briefs, state: hop.State,
		trend: hop.Trend, top: slowSparkTop(hop.Trend)}
	if hop.State == slowpath.NotRead {
		line.key, line.trend = "-", nil
	}
	return line
}

// slowLaneColumns picks the plot width of the lanes strip: the most columns
// that fit, each a round stretch of time.
func slowLaneColumns(window time.Duration, room int) (columns int, each time.Duration) {
	ladder := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 20 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour}
	for _, each = range ladder {
		if window%each == 0 && int(window/each) <= room {
			return int(window / each), each
		}
	}
	return max(min(room, 12), 1), window / time.Duration(max(min(room, 12), 1))
}

// sentences returns the summary wrapped to the width in at most limit
// lines. When it does not fit, whole sentences are left out from the least
// important rank; the order of what remains is unchanged.
func (v *slowView) sentences(report *slowpath.Report, width, limit int) []string {
	order := make([]int, len(report.Summary))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return report.Summary[order[a]].Rank < report.Summary[order[b]].Rank })
	keep := make([]bool, len(report.Summary))
	wrap := func() []string {
		var parts []string
		for i, sentence := range report.Summary {
			if keep[i] {
				parts = append(parts, v.plain(sentence.Text))
			}
		}
		return impactWrap(strings.Join(parts, " "), width)
	}
	var best []string
	for _, index := range order {
		keep[index] = true
		lines := wrap()
		if len(lines) > limit {
			keep[index] = false
			continue
		}
		best = lines
	}
	if best == nil && len(order) > 0 && limit > 0 {
		// Not even the first sentence fits: show its start.
		keep[order[0]] = true
		best = wrap()[:limit]
		last := len(best) - 1
		best[last] = v.cut(best[last], width-1) + v.ellipsis()
	}
	return best
}

// slowStep is one follow-up read: the key and what it opens, and a shorter
// form for a line that has no room for the whole of it.
type slowStep struct{ long, short string }

// nextSteps offers the follow-up reads as keys, and only those that would
// run. The line holds whole steps: later steps are shortened first, then
// left out, so that it never ends in the middle of one.
func (v *slowView) nextSteps(report *slowpath.Report, width int) string {
	var steps []slowStep
	if probeAvailability(v.serviceTarget()) == "" && len(report.Path.Pods) > 0 {
		steps = append(steps, slowStep{probeKeyName + " probe each pod now", probeKeyName + " probe pods"})
	}
	if app := report.Hop(slowpath.HopApp); app != nil && len(app.Pods) > 0 {
		outlier := ""
		for i := range app.Pods {
			if app.Pods[i].State == slowpath.Outlier && outlier == "" {
				outlier = report.ShortPod(app.Pods[i].Pod.Name)
			}
		}
		if graphAvailability(v.app, v.serviceTarget()) == "" {
			// m opens the comparison on the signal that set a Pod apart.
			if _, name := slowGraphSignal(report); name != "" && len(app.Pods) > 1 {
				steps = append(steps, slowStep{"m " + name + " of each pod side by side", "m " + name + " per pod"})
			} else if len(app.Pods) > 1 {
				steps = append(steps, slowStep{"m graphs of each pod side by side", "m per-pod graphs"})
			}
			switch {
			case v.focus.pod != "":
				short := report.ShortPod(v.focus.pod)
				steps = append(steps, slowStep{"Enter graphs " + short + " against its limits", "Enter graphs " + short})
			case outlier != "" && v.podsListed():
				steps = append(steps, slowStep{"Enter on " + outlier + " graphs it against its limits", "Enter on " + outlier + " graphs it"})
			case outlier != "":
				steps = append(steps, slowStep{"Enter lists the pods", "Enter lists pods"})
			}
		}
		steps = append(steps, slowStep{"h Hubble flows to the pods", "h Hubble"})
	}
	if grafanaUnavailableShort(v.app, v) == "" {
		steps = append(steps, slowStep{"Shift-D Grafana, this window", "Shift-D Grafana"})
	}
	if _, _, err := timelineScope(v.workloadTarget(), v.app.Config.ActiveNamespace(), ""); err == nil {
		steps = append(steps, slowStep{"t timeline", "t timeline"})
	}
	return v.fitSteps(steps, width)
}

// fitSteps joins the steps into one line of at most width cells.
func (v *slowView) fitSteps(steps []slowStep, width int) string {
	const lead = "next: "
	texts := make([]string, len(steps))
	for i := range steps {
		texts[i] = steps[i].long
	}
	line := func() string { return v.plain(lead + strings.Join(texts, " · ")) }
	for i := len(texts) - 1; i >= 0 && runewidth.StringWidth(line()) > width; i-- {
		texts[i] = steps[i].short
	}
	for len(texts) > 1 && runewidth.StringWidth(line()) > width {
		texts = texts[:len(texts)-1]
	}
	if len(texts) == 0 {
		return ""
	}
	return lead + strings.Join(texts, " · ")
}

func (v *slowView) bodyLines(width, rows int) []string {
	p := v.app.Styles.Semantic()
	paint := func(color config.Color, bold bool, text string) string {
		return timelineTag(color, bold) + tview.Escape(v.cut(text, width)) + "[-:-:-]"
	}
	report := v.report()
	if rows <= 0 {
		return nil
	}
	if report == nil {
		text := "r reads again. :sources shows what is declared for this context."
		if v.read == nil {
			text = "The path is read from the Kubernetes API as :network-review reads it: Service, EndpointSlices, Pods, Ingress. " +
				"Every hop is then read once from the declared Prometheus. Bounded, read-only, and only because this view was opened."
		}
		var out []string
		for _, part := range impactWrap(text, width) {
			out = append(out, paint(p.Muted, false, part))
		}
		return out[:min(len(out), rows)]
	}
	if v.detail {
		return v.detailLines(report, width, rows)
	}

	budget := v.shareRows(report, width, rows)
	var out []string
	for _, text := range v.focusLines(report, budget.detail) {
		out = append(out, paint(p.Muted, false, text))
	}
	out = append(out, v.lanesLines(report, budget.lanes, width, budget.title)...)
	if budget.folded != "" {
		out = append(out, paint(p.Muted, false, budget.folded))
	}
	if budget.gap {
		out = append(out, "")
	}
	if budget.heading {
		// Without an episode nothing lines up: the heading says what the
		// lines below are instead.
		text := "WHAT LINES UP"
		if len(report.Episodes) == 0 {
			text = "WHAT WAS READ"
		}
		out = append(out, paint(p.Focus, true, text))
	}
	for _, text := range budget.summary {
		out = append(out, paint(p.Text, false, text))
	}
	if budget.next != "" {
		out = append(out, paint(p.Warning, false, budget.next))
	}
	if len(out) > rows {
		out = out[:rows]
	}
	return out
}

// slowBudget is how the rows under the table are shared out.
type slowBudget struct {
	title, gap, heading bool
	detail              int
	summary             []string
	lanes               []slowpath.Lane
	// folded names the lanes that have no row; next is the next-steps line.
	folded, next string
}

// shareRows decides what the rows under the table show. Lanes and the
// summary always remain; the title, the next steps, a heading and the
// focused row's rule are added as the height allows.
func (v *slowView) shareRows(report *slowpath.Report, width, rows int) slowBudget {
	b := slowBudget{title: rows >= 13, gap: rows >= 24, heading: rows >= 16}
	summaryCap := 3
	switch {
	case rows >= 22:
		b.detail, summaryCap = 2, 4
	case rows >= 17:
		summaryCap = 4
	case rows < 9:
		summaryCap = 2
	}
	if rows >= 13 {
		b.next = v.nextSteps(report, width)
	}
	count := func(flags ...bool) int {
		n := 0
		for _, flag := range flags {
			if flag {
				n++
			}
		}
		return n
	}
	fixed := 2 + count(b.title, b.next != "", b.gap, b.heading) + b.detail
	b.summary = v.sentences(report, width, summaryCap)
	if b.gap && rows-fixed-len(b.summary) < len(report.Lanes) {
		// The blank line goes before a lane does.
		b.gap = false
		fixed--
	}
	b.lanes, b.folded = slowPickLanes(report, rows-fixed-len(b.summary))
	if left := rows - fixed - len(b.lanes) - count(b.folded != ""); len(b.summary) > left {
		// Two lanes always remain; the summary then keeps the whole
		// sentences that fit beside them and is never cut in the middle.
		b.summary = v.sentences(report, width, max(left, 0))
	}
	if spare := rows - fixed - len(b.summary) - len(b.lanes) - count(b.folded != ""); spare > 0 {
		// Rows the lanes did not need go to the summary, then the detail.
		if more := v.sentences(report, width, summaryCap+spare); len(more) > len(b.summary) {
			spare -= len(more) - len(b.summary)
			b.summary = more
		}
		if spare > 0 && rows >= 17 {
			b.detail += min(spare, 3-b.detail)
		}
	}
	return b
}

// slowPickLanes chooses the lanes that fit in budget rows: all of them when
// there is room; else the lanes with marks, as many of the others as fit,
// and one line that names the rest. The app hop keeps one lane per signal
// while the marked lanes fit, and one lane otherwise. When not even the
// marked lanes fit, the lane the episodes are counted from stays, then the
// lanes that overlap every episode, then those that overlap some: what the
// summary speaks about is what remains drawn. The lanes and the line that
// names the rest never take more than budget rows (two at least).
func slowPickLanes(report *slowpath.Report, budget int) (lanes []slowpath.Lane, folded string) {
	budget = max(budget, 2)
	if len(report.Lanes) <= budget {
		return report.Lanes, ""
	}
	candidates := [][]slowpath.Lane{report.Lanes, report.CompactLanes()}
	for index, candidate := range candidates {
		if len(candidate) <= budget {
			return candidate, ""
		}
		var marked []int
		for i := range candidate {
			if len(candidate[i].Marks) > 0 {
				marked = append(marked, i)
			}
		}
		if len(marked)+1 > budget && index < len(candidates)-1 {
			continue
		}
		// The line that names what is left out gives way when exactly the
		// marked lanes fit without it.
		limit := budget - 1
		if len(marked) == budget {
			limit = budget
		}
		rank := func(i int) int {
			lane := &candidate[i]
			switch overlaps := slowpath.OverlapCount(report.Episodes, lane.Marks); {
			case report.Symptom >= 0 && lane.ID == report.Lanes[report.Symptom].ID:
				return 0
			case len(report.Episodes) > 0 && overlaps == len(report.Episodes):
				return 1
			case overlaps > 0:
				return 2
			}
			return 3
		}
		sort.SliceStable(marked, func(a, b int) bool { return rank(marked[a]) < rank(marked[b]) })
		keep := map[int]bool{}
		for _, i := range marked[:min(limit, len(marked))] {
			keep[i] = true
		}
		room := limit - len(keep)
		var quiet, unread []string
		more := 0
		for i := range candidate {
			lane := candidate[i]
			switch {
			case keep[i]:
				lanes = append(lanes, lane)
			case len(lane.Marks) > 0:
				more++
			case room > 0:
				lanes = append(lanes, lane)
				room--
			case lane.NotRead != "":
				unread = append(unread, lane.Short)
			default:
				quiet = append(quiet, lane.Short)
			}
		}
		if limit == budget {
			return lanes, ""
		}
		// Never one: a single lane more would have taken this line's row.
		var parts []string
		if more > 0 {
			parts = append(parts, fmt.Sprintf("%d more lanes with marks do not fit", more))
		}
		if len(quiet) > 0 {
			parts = append(parts, "no marks: "+strings.Join(quiet, ", "))
		}
		if len(unread) > 0 {
			parts = append(parts, "not read: "+strings.Join(unread, ", "))
		}
		return lanes, strings.Join(parts, " · ")
	}
	return report.Lanes, ""
}

// focusLines state the focused row in up to three lines: the rule of its
// main signal, the numbers behind it, and the query.
func (v *slowView) focusLines(report *slowpath.Report, limit int) []string {
	if limit <= 0 {
		return nil
	}
	hops := v.focusedHops()
	if len(hops) == 0 {
		return nil
	}
	hop := hops[0]
	var lines []string
	switch {
	case v.focus.hop == slowDepsFolded:
		lines = append(lines, fmt.Sprintf("%d dependencies on one row; Enter or s lists their queries and rules", len(hops)))
	case hop.State == slowpath.NotRead:
		lines = append(lines, hop.Spec.Label()+" not read: "+hop.Reason)
	case hop.Spec.ID == slowpath.HopService:
		lines = append(lines, "rule (endpoints): raised when an endpoint is not ready now, a Pod was created or changed Ready in the window, or kube_pod_status_ready was 0")
		if ready := hop.Signal(slowpath.SignalReady); ready != nil {
			lines = append(lines, "ready: "+report.SignalStat(ready), "query: "+ready.Text)
		}
	case hop.Main >= 0:
		main := &hop.Signals[hop.Main]
		lines = append(lines, "rule ("+main.Name+"): "+main.Rule.Text(), main.Name+": "+report.SignalStat(main), "query: "+main.Text)
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	if limit == 1 || len(lines) == 1 {
		lines[0] += " · s shows every query"
	}
	return lines
}

// detailLines replace the lanes with everything behind the focused row:
// every signal with its numbers, rule and exact query. PgDn and PgUp page.
func (v *slowView) detailLines(report *slowpath.Report, width, rows int) []string {
	p := v.app.Styles.Semantic()
	var out []string
	add := func(color config.Color, bold bool, text string) {
		out = append(out, timelineTag(color, bold)+tview.Escape(v.cut(text, width))+"[-:-:-]")
	}
	// Nothing on this page is cut: the numbers, the rule and the query are
	// the evidence behind a status word. Words wrap at spaces; a query has
	// few and is broken where the line ends.
	wrapped := func(color config.Color, bold bool, text string, wrap func(string, int) []string) {
		body := strings.TrimPrefix(text, "  ")
		first, rest := "", "  "
		if body != text {
			first, rest = "  ", "    "
		}
		for index, part := range wrap(body, width-len(rest)) {
			indent := first
			if index > 0 {
				indent = rest
			}
			add(color, bold, indent+part)
		}
	}
	for _, hop := range v.focusedHops() {
		for _, text := range report.Detail(hop) {
			text = v.plain(text)
			switch {
			case !strings.HasPrefix(text, " "):
				wrapped(p.Text, true, text, impactWrap)
			case strings.HasPrefix(text, "  query:"):
				wrapped(p.Muted, false, text, slowHardWrap)
			case strings.HasPrefix(text, "  not read"):
				wrapped(p.Warning, false, text, slowHardWrap)
			default:
				wrapped(p.Muted, false, text, impactWrap)
			}
		}
	}
	if path := report.Path; len(path.Notes) > 0 {
		for _, part := range slowHardWrap("path notes: "+v.plain(strings.Join(path.Notes, "; ")), width) {
			add(p.Muted, false, part)
		}
	}
	page := max(rows-1, 1)
	v.detailPage = page
	v.detailTop = min(v.detailTop, max(len(out)-page, 0))
	// The paging keys stay in the heading at every width: they are the only
	// way to the lines below.
	head := impactFit(width, "QUERIES, SAMPLES AND RULES · s or Enter returns to the lanes", "QUERIES AND RULES · s returns to the lanes", "QUERIES AND RULES · s returns")
	if len(out) > page {
		from, to := v.detailTop+1, min(v.detailTop+page, len(out))
		head = impactFit(width,
			fmt.Sprintf("QUERIES, SAMPLES AND RULES · s or Enter returns to the lanes · lines %d-%d of %d · PgDn PgUp", from, to, len(out)),
			fmt.Sprintf("QUERIES AND RULES · s returns · lines %d-%d of %d · PgDn PgUp", from, to, len(out)),
			fmt.Sprintf("QUERIES · s returns · %d-%d of %d · PgDn PgUp", from, to, len(out)))
	}
	lines := []string{timelineTag(p.Focus, true) + tview.Escape(v.cut(head, width)) + "[-:-:-]"}
	return append(lines, out[v.detailTop:min(v.detailTop+page, len(out))]...)
}

// laneLabel fits a lane's label: the raised signals are shortened before
// the Pod name is, because the Pod is what tells two lanes apart.
func (v *slowView) laneLabel(report *slowpath.Report, lane *slowpath.Lane, width int) string {
	if runewidth.StringWidth(v.plain(lane.Label)) <= width || lane.Pod == "" || len(lane.Names) == 0 {
		return v.cut(lane.Label, width)
	}
	pod := report.ShortPod(lane.Pod)
	more := ""
	if len(lane.Names) > 1 {
		more = fmt.Sprintf(" +%d", len(lane.Names)-1)
	}
	// What the mark means goes before where the hop is: "not ready …-hx4mt"
	// says more than "endpoints …-hx4mt".
	for _, text := range []string{lane.Short + " " + pod + ": " + lane.Names[0] + more, lane.Names[0] + more + " " + pod, lane.Short + " " + pod} {
		if runewidth.StringWidth(v.plain(text)) <= width {
			return v.plain(text)
		}
	}
	return v.cut(lane.Short+" "+pod, width)
}

// slowSnap moves the edges of each stretch to the nearest column edge, so
// that the cells a lane fills add up to the time written beside it: a
// stretch that straddles two columns by half a sample is not drawn a
// column wider at each end. A stretch shorter than half a column keeps the
// column its middle falls in, and a moment is left where it is.
func slowSnap(marks []chart.Interval, frame chart.Frame) []chart.Interval {
	if frame.Width <= 0 || !frame.End.After(frame.Start) {
		return marks
	}
	column := frame.End.Sub(frame.Start) / time.Duration(frame.Width)
	edge := func(t time.Time) time.Time { return frame.Start.Add(t.Sub(frame.Start).Round(column)) }
	out := make([]chart.Interval, 0, len(marks))
	for _, mark := range marks {
		if !mark.End.After(mark.Start) {
			out = append(out, mark)
			continue
		}
		start, end := edge(mark.Start), edge(mark.End)
		if !end.After(start) {
			middle := mark.Start.Add(mark.End.Sub(mark.Start) / 2)
			start = frame.Start.Add(middle.Sub(frame.Start).Truncate(column))
			end = start.Add(column)
		}
		out = append(out, chart.Interval{Start: start, End: end})
	}
	return out
}

// slowLaneNote is what stands to the right of a lane: for how long it was
// over its rule, or that it was not read.
func slowLaneNote(report *slowpath.Report, lane *slowpath.Lane, width int) string {
	switch {
	case lane.NotRead != "":
		return "not read"
	case len(lane.Marks) == 0 && len(lane.Unknown) > 0:
		return "none; gaps"
	case len(lane.Marks) == 0:
		return "none in " + report.Span()
	case slowpath.Total(lane.Marks) == 0:
		return fmt.Sprintf("%d in %s", len(lane.Marks), report.Span())
	}
	note := impact.Dur(slowpath.Total(slowpath.Clip(lane.Marks, report.Start, report.End)))
	if runewidth.StringWidth(note+" of "+report.Span()) <= width {
		note += " of " + report.Span()
	}
	return note
}

// slowCursorNote is what a lane reads at the cursor: its worst value in
// that column, or the dated facts that fall into it.
func slowCursorNote(lane *slowpath.Lane, from, to time.Time) string {
	if events := lane.EventsAt(from, to); len(events) > 0 {
		return strings.Join(events, ", ")
	}
	if value, ok := lane.ValueAt(from, to); ok {
		return lane.Unit.Format(value)
	}
	return "-"
}

// slowDrawnUnknown is the unknown part of a lane as it is drawn. A dated
// fact is drawn in its column even when the time before it is unknown:
// the fact itself was read.
func slowDrawnUnknown(lane *slowpath.Lane, frame chart.Frame) []chart.Interval {
	if len(lane.Events) == 0 || len(lane.Unknown) == 0 {
		return lane.Unknown
	}
	marked, hidden := frame.Marked(lane.Marks), frame.Marked(lane.Unknown)
	var unknown []chart.Interval
	for col := range hidden {
		if hidden[col] && !marked[col] {
			start, end := frame.ColumnSpan(col)
			unknown = append(unknown, chart.Interval{Start: start, End: end})
		}
	}
	return unknown
}

// lanesLines draw the strip: one lane per row on a shared time axis, the
// time under each lane's marks, and the cursor readout.
func (v *slowView) lanesLines(report *slowpath.Report, lanes []slowpath.Lane, width int, title bool) []string {
	p := v.app.Styles.Semantic()
	labelWidth, noteWidth := 17, 9
	switch {
	case width >= 120:
		labelWidth, noteWidth = 30, 18
	case width >= 76:
		labelWidth, noteWidth = 24, 11
	}
	window := report.End.Sub(report.Start)
	columns, each := slowLaneColumns(window, width-labelWidth-2-noteWidth-2)
	frame := chart.Frame{Start: report.Start, End: report.End, Step: window / time.Duration(columns), Width: columns}
	v.frame = frame
	cursor := -1
	if v.cursor >= 0 {
		cursor = min(v.cursor, columns-1)
		if col, ok := frame.Col(v.cursorAt); ok && !v.cursorAt.IsZero() {
			cursor = col
		}
		v.cursor = cursor
	}
	var from, to time.Time
	if cursor >= 0 {
		from, to = frame.ColumnSpan(cursor)
	}
	drawn := make([]chart.Lane, 0, len(lanes))
	for i := range lanes {
		lane := &lanes[i]
		color, _ := v.stateColor(slowpath.Elevated)
		if hop := report.Hop(lane.Hop); hop != nil && hop.State == slowpath.Outlier && lane.Pod != "" {
			color, _ = v.stateColor(slowpath.Outlier)
		}
		if !lane.Signal {
			color = p.Progress
		}
		var note string
		if cursor >= 0 && lane.NotRead == "" {
			note = slowCursorNote(lane, from, to)
		} else {
			note = slowLaneNote(report, lane, noteWidth)
		}
		unknown := slowDrawnUnknown(lane, frame)
		drawn = append(drawn, chart.Lane{Label: v.laneLabel(report, lane, labelWidth), Marks: slowSnap(lane.Marks, frame), Unknown: unknown,
			Color: graphColor(color), Note: v.cut(note, noteWidth)})
	}
	lines := chart.Lanes(drawn, frame, chart.LanesOptions{
		Options:    chart.Options{ASCII: v.noIcons(), Cursor: cursor, AxisColor: graphColor(p.Muted), CursorColor: graphColor(p.Focus)},
		LabelWidth: labelWidth, NoteWidth: noteWidth, QuietColor: graphColor(p.Muted)})
	mark, gapGlyph := "█", "×"
	if v.noIcons() {
		mark, gapGlyph = "#", "x"
	}
	legend := "1 column = " + impact.Dur(each)
	at := ""
	if cursor >= 0 {
		at = "cursor " + report.Clock(from) + "–" + report.Clock(to)
	}
	var out []string
	if title {
		head := timelineTag(p.Focus, true) + "WHEN IT HAPPENED" + "[-:-:-]"
		rest := " · " + legend + " · " + mark + " over its rule · " + gapGlyph + " no data · UTC"
		if at != "" {
			rest = " · " + at + " UTC · the value of each lane at the cursor is on the right · Esc hides it"
		}
		out = append(out, head+timelineTag(p.Muted, false)+tview.Escape(v.cut(rest, width-16))+"[-:-:-]")
	}
	// Without a title row the axis gutter carries the legend and the cursor.
	if n := len(lines); n >= 2 && !title {
		pad := strings.Repeat(" ", labelWidth+1)
		first, second := "when, UTC", legend
		if at != "" {
			first = strings.Replace(at, "cursor", "at", 1)
		}
		lines[n-2] = strings.Replace(lines[n-2], pad, v.fit(first, labelWidth+1), 1)
		if strings.Contains(lines[n-1], pad) {
			lines[n-1] = strings.Replace(lines[n-1], pad, v.fit(second, labelWidth+1), 1)
		}
	}
	return append(out, lines...)
}
