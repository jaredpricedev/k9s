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
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	graphMinRows      = 3
	graphGoodRows     = 4
	graphMaxRows      = 9
	graphNoData       = "no data"
	graphNoSample     = "–"
	graphRestartLabel = "restarts"
)

// graphColors are the tview colours of a page, from the semantic palette.
type graphColors struct {
	Data                            []string
	Ceiling, Request, Axis, Cursor  string
	Title, Muted, Failure, Restarts string
}

func graphHex(c config.Color) string {
	hex := c.Color().Hex()
	if hex < 0 {
		return ""
	}
	return fmt.Sprintf("#%06x", hex)
}

func graphPalette(p config.SemanticPalette) graphColors {
	return graphColors{
		Data:    []string{graphHex(p.Focus), graphHex(p.Category), graphHex(p.Progress), graphHex(p.Text)},
		Ceiling: graphHex(p.Failure), Request: graphHex(p.Warning), Axis: graphHex(p.Muted), Cursor: graphHex(p.Text),
		Title: graphHex(p.Focus), Muted: graphHex(p.Muted), Failure: graphHex(p.Failure), Restarts: graphHex(p.Warning),
	}
}

func graphPaint(color, text string) string {
	if color == "" || text == "" {
		return tview.Escape(text)
	}
	return "[" + color + "::]" + tview.Escape(text) + "[-::]"
}

func graphBold(color, text string) string {
	if color == "" {
		color = "-"
	}
	return "[" + color + "::b]" + tview.Escape(text) + "[-::-]"
}

// graphView is what the page needs beyond the snapshot.
type graphPage struct {
	Width, Height int
	ASCII         bool
	Colors        graphColors
	// Cursor is a sample-grid index; First is the first chart shown.
	Cursor, First int
	Busy          string
	// Signal and Grouping select what the comparison page shows; Shown is
	// written back by it: the number of charts or rows on one page.
	Signal   string
	Grouping graphGrouping
	Shown    int
}

// graphPanel is a panel ready to draw, with the state of each query.
type graphPanel struct {
	Spec   *graphPanelSpec
	Chart  chart.Panel
	Specs  []*graphSeriesSpec // parallel to Chart.Series
	States []string           // non-empty when a query drew nothing
}

func graphShortNames(names []string, prefix string) map[string]string {
	short := make(map[string]string, len(names))
	seen := make(map[string]int)
	for _, name := range names {
		tail := name[strings.LastIndex(name, "-")+1:]
		short[name] = tail
		seen[tail]++
	}
	for _, name := range names {
		if seen[short[name]] > 1 || short[name] == "" {
			short[name] = strings.TrimPrefix(name, prefix+"-")
		}
	}
	return short
}

func graphPoints(samples []observability.Sample) []chart.Point {
	points := make([]chart.Point, len(samples))
	for i, sample := range samples {
		points[i] = chart.Point{T: sample.Timestamp, V: sample.Value}
	}
	return points
}

// graphStateWord says why a query drew nothing. An answer that matched
// nothing is "no data"; it is never a zero.
func graphStateWord(res *observability.PromResult) string {
	switch {
	case res.State == "":
		return "not read"
	case !res.OK():
		return string(res.State)
	case graphSamples(res) == 0:
		return graphNoData
	}
	return ""
}

// panels turns the answers into drawable panels. Keys and colours are
// assigned here so the legend and the plot always agree.
func (s *graphSnapshot) panels(colors graphColors) []graphPanel {
	short := graphShortNames(s.Pods, s.Target.Name)
	out := make([]graphPanel, 0, len(s.Plan.Panels))
	for i := range s.Plan.Panels {
		spec := &s.Plan.Panels[i]
		panel := graphPanel{Spec: spec, Chart: chart.Panel{Unit: spec.Unit}}
		lines := 0
		add := func(sp *graphSeriesSpec, name string, points []chart.Point, state string) {
			series := chart.Series{Name: name, Kind: sp.Kind, Points: points, Glyph: sp.Glyph, ASCII: sp.ASCII, Unit: sp.Unit}
			switch {
			case sp.Kind == chart.Line:
				// Every line is one continuous stroke. Its letter ends it
				// and, when a chart has several lines, repeats along it.
				series.Key = rune('a' + lines%26)
				series.Stroke = chart.Solid
				if len(colors.Data) > 0 {
					series.Color = colors.Data[lines%len(colors.Data)]
				}
				lines++
			case sp.Role == graphRoleCeiling:
				series.Color = colors.Ceiling
			case sp.Role == graphRoleRequest:
				series.Color = colors.Request
			}
			panel.Chart.Series = append(panel.Chart.Series, series)
			panel.Specs = append(panel.Specs, sp)
			panel.States = append(panel.States, state)
		}
		for j := range spec.Series {
			sp := &spec.Series[j]
			res := s.Results[graphResultKey(spec.ID, sp.ID)]
			state := graphStateWord(&res)
			if state != "" || sp.By == "" {
				var points []chart.Point
				if state == "" {
					points = graphPoints(res.Series[0].Samples)
				}
				add(sp, sp.Name, points, state)
				continue
			}
			sort.SliceStable(res.Series, func(a, b int) bool { return res.Series[a].Labels[sp.By] < res.Series[b].Labels[sp.By] })
			answered := make(map[string]bool, len(res.Series))
			for k := range res.Series {
				name := res.Series[k].Labels[sp.By]
				answered[name] = true
				if sp.By == "pod" && short[name] != "" {
					name = short[name]
				}
				add(sp, name, graphPoints(res.Series[k].Samples), "")
			}
			if sp.By != "pod" {
				continue
			}
			// A Pod that was asked for and has no samples is named, so a
			// missing line is not read as a Pod that does not exist.
			asked := s.Pods[:len(s.Pods)-min(s.PodsOmitted, len(s.Pods))]
			for _, pod := range asked {
				if !answered[pod] {
					add(sp, short[pod], nil, graphNoData)
				}
			}
		}
		if lines > 1 {
			for k := range panel.Chart.Series {
				if series := &panel.Chart.Series[k]; series.Kind == chart.Line {
					series.Marker = series.Key
					// More lines, wider spacing: the letters stay readable
					// where several lines run together.
					series.MarkerEvery = max(12, 8*lines)
				}
			}
		}
		if _, drawn := panel.Chart.Max(); !drawn {
			panel.Chart.Empty = graphEmptyText(&panel)
		}
		out = append(out, panel)
	}
	return out
}

// graphEmptyText explains an empty plot in the plot itself.
func graphEmptyText(p *graphPanel) string {
	state := graphNoData
	for i, sp := range p.Specs {
		if sp.Kind == chart.Line {
			state = p.States[i]
			break
		}
	}
	switch state {
	case graphNoData:
		return "no samples in this window (not a zero)"
	case string(observability.StateDenied):
		return "denied: nothing was read · :sources"
	case string(observability.StateTimeout):
		return "timeout: no answer in time · r re-reads"
	}
	return state + ": nothing was read · :sources"
}

// legend builds the legend entries of a panel with the values at time at.
func (p *graphPanel) legend(f chart.Frame, at time.Time, ascii bool) []chart.Entry {
	entries := make([]chart.Entry, 0, len(p.Chart.Series))
	for i := range p.Chart.Series {
		series := &p.Chart.Series[i]
		unit := p.Chart.Unit
		if series.Unit != nil {
			unit = *series.Unit
		}
		entry := chart.Entry{Name: series.Name, Color: series.Color}
		switch series.Kind {
		case chart.Line:
			entry.Glyph = string(series.Key)
		case chart.Reference:
			entry.Glyph = string(series.Glyph)
			if ascii {
				entry.Glyph = string(series.ASCII)
			}
		}
		switch value, ok := f.ValueAt(series, at); {
		case p.States[i] != "":
			entry.Value = p.States[i]
		case ok:
			entry.Value = unit.Format(value)
		case len(series.Points) > 0 && series.Points[len(series.Points)-1].T.Before(at):
			// The series stopped: say when, rather than only "nothing here".
			entry.Value = graphNoSample + " last " + series.Points[len(series.Points)-1].T.UTC().Format("15:04")
		case len(series.Points) > 0 && series.Points[0].T.After(at):
			entry.Value = graphNoSample + " first " + series.Points[0].T.UTC().Format("15:04")
		default:
			entry.Value = graphNoSample
		}
		entries = append(entries, entry)
	}
	return entries
}

func graphClock(t time.Time) string { return t.UTC().Format("15:04:05Z") }

func graphAgo(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		if d%time.Minute == 0 {
			return fmt.Sprintf("%dm", int(d.Minutes()))
		}
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func graphFit(text string, width int) string { return runewidth.Truncate(text, max(width, 0), "…") }

// graphMiddle shortens a name in the middle: generated suffixes tell Pods
// apart better than the middle does.
func graphMiddle(text string, width int) string {
	if runewidth.StringWidth(text) <= width || width < 8 {
		return graphFit(text, width)
	}
	runes := []rune(text)
	tail := (width - 1) / 2
	return runewidth.Truncate(text, width-1-tail, "") + "…" + string(runes[len(runes)-tail:])
}

// headline is the first line: what is drawn, the window and the step.
func (s *graphSnapshot) headline(width int) string {
	// Seconds are written when the aligned window end has any.
	end := s.End.UTC().Format("15:04Z")
	if s.End.Second() != 0 {
		end = graphClock(s.End)
	}
	tail := fmt.Sprintf(" · last %s to %s · step %s", graphSpan(s.Window), end, graphSpan(s.Step))
	if width < 72 {
		tail = fmt.Sprintf(" · %s to %s · step %s", graphSpan(s.Window), end, graphSpan(s.Step))
	}
	label, prefix := s.Target.Label(), "Graph · "
	if runewidth.StringWidth(prefix+label+tail) > width {
		// The breadcrumb already names the view; the object matters more.
		prefix = ""
	}
	return prefix + graphMiddle(label, width-runewidth.StringWidth(prefix)-runewidth.StringWidth(tail)) + tail
}

// failure describes a page on which nothing can be drawn, or returns nil.
func (s *graphSnapshot) failure() []string {
	point := "Open :sources to see the declaration and probe it once (p)."
	explain := func(st observability.Status, what string) []string {
		reason := ""
		if st.Reason != "" {
			reason = " (" + st.Reason + ")"
		}
		at := ""
		if !st.ObservedAt.IsZero() {
			at = " at " + graphClock(st.ObservedAt)
		}
		switch st.State {
		case observability.StateNotConfigured:
			return []string{"No Prometheus source is declared for this context, so nothing was asked.",
				"Open :sources to see what to declare; s there suggests in-cluster services."}
		case observability.StateDenied:
			return []string{"Prometheus DENIED " + what + reason + at + ".",
				"Nothing is drawn: a denied read is not a reading of zero and says nothing about the object.", point}
		case observability.StateTimeout:
			return []string{"Prometheus TIMEOUT: " + what + " was not answered in time" + reason + at + ".",
				"Nothing is drawn. Press r to ask again, or " + strings.ToLower(point[:1]) + point[1:]}
		case observability.StateAbsent:
			return []string{"Prometheus ABSENT: the declared source was not found" + reason + at + ".", "Nothing is drawn. " + point}
		}
		return []string{"Prometheus " + strings.ToUpper(string(st.State)) + ": " + what + " failed" + reason + at + ".", "Nothing is drawn. " + point}
	}
	switch {
	case s.Status != nil:
		return explain(*s.Status, "the read")
	case s.Target.Kind == graphKindService && (s.Members == nil || s.Members.Problem != "" || len(s.Members.Pods) == 0):
		// The Pods of a Service are resolved from the cluster. Without
		// them nothing was asked of Prometheus.
		lines := []string{"The Pods behind this Service were not resolved, so nothing was asked of Prometheus."}
		if s.Members != nil && s.Members.Problem != "" {
			lines[0] = s.Members.Problem
			lines = append(lines, "Nothing was asked of Prometheus. That is missing data, not a healthy reading.")
		}
		if s.Members != nil {
			lines = append(lines, s.Members.Notes...)
		}
		return append(lines, ":network-review on the Service shows its selector, EndpointSlices and what could not be read.")
	case s.Owner != nil && !s.Owner.OK():
		return explain(s.Owner.Status, "the lookup of this "+s.Target.Kind+"'s Pods")
	case s.Owner != nil && len(s.Pods) == 0:
		return []string{
			"Prometheus answered, but kube_pod_info names no Pod owned by this " + s.Target.Kind + " in the window.",
			"That is missing data, not a healthy reading: kube-state-metrics may not be scraped, or the workload had no Pods. " +
				"A graph of a workload needs kube_pod_info to find its Pods.",
			"Select one of its Pods and graph that instead, or check the source in :sources.",
		}
	}
	if t := s.tally(); t.Asked > 0 && t.Failed == t.Asked {
		return explain(t.Worst, fmt.Sprintf("all %d queries", t.Asked))
	}
	return nil
}

// statusLine is the second line: when, and how the reads went. The compact
// form keeps every fact and drops only words.
func (s *graphSnapshot) statusLine(shown, total int, compact bool) string {
	level := 0
	if compact {
		level = 1
	}
	return strings.Join(s.statusParts(shown, total, level), " · ")
}

// statusParts are the facts of the status line. Level 1 and 2 use fewer
// words for narrow pages; no level drops a fact.
func (s *graphSnapshot) statusParts(shown, total, level int) []string {
	compact := level > 0
	t := s.tally()
	parts := []string{"read " + graphClock(s.ReadAt)}
	answered := fmt.Sprintf("%d of %d queries answered", t.Asked-t.Failed, t.Asked)
	if compact {
		answered = fmt.Sprintf("%d/%d answered", t.Asked-t.Failed, t.Asked)
	}
	if level > 1 {
		answered = fmt.Sprintf("%d/%d ok", t.Asked-t.Failed, t.Asked)
	}
	if t.Failed > 0 {
		answered += fmt.Sprintf(", %d %s", t.Failed, t.Worst.State)
	}
	parts = append(parts, answered)
	if t.Empty > 0 {
		parts = append(parts, fmt.Sprintf("%d no data", t.Empty))
	}
	if t.Incomplete > 0 {
		parts = append(parts, fmt.Sprintf("%d incomplete", t.Incomplete))
	}
	if s.Target.workload() {
		pods := fmt.Sprintf("%d pods", len(s.Pods))
		if len(s.Pods) > graphLineCap {
			pods += " folded into min/avg/max"
			if compact {
				pods = fmt.Sprintf("%d pods folded", len(s.Pods))
			}
		}
		if s.PodsOmitted > 0 {
			pods += fmt.Sprintf(", %d NOT READ", s.PodsOmitted)
		}
		parts = append(parts, pods)
	}
	if shown < total {
		if compact {
			parts = append(parts, fmt.Sprintf("%d/%d charts ↑↓", shown, total))
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d charts (↑/↓)", shown, total))
		}
	}
	return parts
}

// graphJoinLines joins parts with a separator onto as few lines as fit the
// width. A part is never split between lines.
func graphJoinLines(parts []string, sep string, width int) []string {
	var lines []string
	for _, part := range parts {
		if n := len(lines); n > 0 && runewidth.StringWidth(lines[n-1]+sep+part) <= width {
			lines[n-1] += sep + part
			continue
		}
		lines = append(lines, part)
	}
	return lines
}

// graphWrap wraps plain text to the width at spaces. A word longer than a
// line (a PromQL text) is cut after a comma where it can be, else at the
// width; continuation lines carry the indent. It never interprets markup.
func graphWrap(text string, width int, indent string) []string {
	width = max(width, 20)
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		lead := paragraph[:len(paragraph)-len(strings.TrimLeft(paragraph, " "))]
		line := lead
		empty := true
		emit := func() {
			lines = append(lines, strings.TrimRight(line, " "))
			line, empty = lead+indent, true
		}
		for _, word := range strings.Fields(paragraph) {
			for {
				room := width - runewidth.StringWidth(line)
				if !empty {
					room--
				}
				if runewidth.StringWidth(word) <= room {
					if !empty {
						line += " "
					}
					line += word
					empty = false
					break
				}
				if !empty && runewidth.StringWidth(word) <= width-runewidth.StringWidth(lead+indent) {
					emit()
					continue
				}
				// The word is longer than any line: fill this one and cut.
				if !empty {
					if room < 8 {
						emit()
						continue
					}
					line += " "
				}
				head := runewidth.Truncate(word, max(room, 1), "")
				if head == "" {
					head = string([]rune(word)[:1])
				}
				// A matcher list breaks after a comma and a list of Pod names
				// after a bar, so that no name is cut in the middle.
				if cut := strings.LastIndexAny(head, ",|"); cut > 0 && len(head) < len(word) {
					head = head[:cut+1]
				} else if cut := strings.LastIndexAny(head, "{("); cut > 0 && len(head) < len(word) {
					head = head[:cut+1]
				}
				line += head
				word = word[len(head):]
				emit()
			}
		}
		lines = append(lines, strings.TrimRight(line, " "))
	}
	return lines
}

// graphMarkLines names the axis marks on one or two lines. The wording
// states order only, and that caveat is never the part that is cut.
func graphMarkLines(marks []graphMark, f chart.Frame, width int) []string {
	if len(marks) == 0 {
		return nil
	}
	parts := make([]string, 0, len(marks)+1)
	for _, mark := range marks {
		text := fmt.Sprintf("%c %s %s", mark.Key, mark.Label, mark.T.UTC().Format("15:04"))
		if _, ok := f.Col(mark.T); !ok {
			if mark.T.Before(f.Start) {
				text += " (before window)"
			} else {
				text += " (after window)"
			}
		}
		parts = append(parts, text)
	}
	parts = append(parts, "order, not cause")
	const sep = " · "
	lines := []string{""}
	for _, part := range parts {
		last := &lines[len(lines)-1]
		switch {
		case *last == "":
			*last = part
		case runewidth.StringWidth(*last)+runewidth.StringWidth(sep+part) <= width || len(lines) == 2:
			*last += sep + part
		default:
			lines = append(lines, part)
		}
	}
	if len(lines) == 2 && runewidth.StringWidth(lines[1]) > width {
		// Too many marks even for two lines: shorten the marks, keep the caveat.
		lines[1] = graphFit(strings.TrimSuffix(lines[1], sep+"order, not cause"), width-runewidth.StringWidth(sep+"order, not cause")) + sep + "order, not cause"
	}
	return lines
}

// graphPlainWords replaces the few non-ASCII signs of the page for noIcons.
var graphPlainWords = strings.NewReplacer("←/→", "Left/Right", "↑/↓", "Up/Down", "↑↓", "Up/Down", "–", "-", "…", "...")

func graphWords(text string, ascii bool) string {
	if !ascii {
		return text
	}
	return graphPlainWords.Replace(text)
}

// graphFooter is the key line. For a workload or Service it names the two
// keys that answer "which Pod is different?" before the window keys.
func graphFooter(width int, charts, ascii, pods bool) string {
	texts := []string{
		"←/→ cursor · Home/End · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · ↑/↓ charts · Esc back",
		"←/→ cursor · 1 15m 2 1h 3 6h 4 24h · r re-read · s queries · Esc back",
		"←/→ cursor · 1-4 window · r re-read · s queries · Esc",
		"←/→ cursor · 1-4 window · r read · s queries",
	}
	if pods {
		texts = []string{
			"←/→ cursor · p one chart per pod · g regroup · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · ↑/↓ charts · Esc back",
			"←/→ cursor · p per pod · g regroup · 1-4 window · r re-read · s queries · Esc back",
			"←/→ cursor · p per pod · g regroup · 1-4 window · r read · s queries",
			"p per pod · g regroup · 1-4 window · r read · s queries",
		}
	}
	for _, text := range texts {
		if !charts {
			text = strings.TrimPrefix(text, "←/→ cursor · ")
		}
		text = graphWords(text, ascii)
		if runewidth.StringWidth(text) <= width {
			return text
		}
	}
	return "1-4 window · r read · Esc"
}

// renderGraph lays the whole page out for a size. Every line fits Width.
func renderGraph(s *graphSnapshot, pg *graphPage) []string {
	width := max(pg.Width, 20)
	c := pg.Colors
	var lines []string
	fit := func(text string) string {
		if pg.ASCII {
			return runewidth.Truncate(graphWords(text, true), width, "...")
		}
		return graphFit(text, width)
	}
	head := func(text string) { lines = append(lines, graphBold(c.Title, fit(text))) }
	plain := func(color, text string) { lines = append(lines, graphPaint(color, fit(text))) }
	if s == nil {
		head("Graph")
		plain("", "Reading the declared Prometheus source once…")
		return lines
	}
	head(s.headline(width))
	if failure := s.failure(); failure != nil {
		if pg.Busy != "" {
			plain(c.Muted, pg.Busy)
		}
		lines = append(lines, "")
		for i, line := range failure {
			color := ""
			if i == 0 {
				color = c.Failure
			}
			for _, wrapped := range graphWrap(line, width, "") {
				plain(color, wrapped)
			}
		}
		lines = append(lines, "", graphPaint(c.Muted, graphFit("Source: prometheus via "+s.Source, width)),
			"", graphPaint(c.Muted, graphFooter(width, false, pg.ASCII, false)))
		return lines
	}

	panels := s.panels(c)
	first := 0
	restarts, hasStrip := s.Results[graphResultKey("", "restarts")]
	hasStrip = hasStrip && s.Plan.Restarts != nil
	events, rise, measured := graphRestartEvents(&restarts, s.Step)
	marks := append([]graphMark(nil), s.Marks...)
	if len(events) > 0 {
		marks = append(marks, graphMark{Key: 'F', T: events[0], Label: "first restart", Origin: "first rise of kube_pod_container_status_restarts_total in the window"})
	}
	sort.SliceStable(marks, func(a, b int) bool { return marks[a].T.Before(marks[b].T) })

	// The gutter is shared so that every plot starts in the same column.
	gutter := 1
	if hasStrip {
		gutter = len(graphRestartLabel)
	}
	for i := range panels {
		gutter = max(gutter, panels[i].Chart.GutterWidth())
	}
	frame := chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: max(width-gutter-2, 8)}
	cursor := min(max(pg.Cursor, 0), frame.Steps())
	at := frame.TimeAt(cursor)
	options := chart.Options{ASCII: pg.ASCII, Cursor: cursor, AxisColor: c.Axis, CursorColor: c.Cursor}

	legends := make([][]string, len(panels))
	for i := range panels {
		title := panels[i].Spec.Title
		entries := panels[i].legend(frame, at, pg.ASCII)
		for j := range entries {
			entries[j].Value = graphWords(entries[j].Value, pg.ASCII)
		}
		legends[i] = chart.Legend(title, entries, width, 2, tview.Escape)
		if len(legends[i]) > 0 {
			legends[i][0] = strings.Replace(legends[i][0], tview.Escape(title), graphBold(c.Title, title), 1)
		}
	}
	markLines := graphMarkLines(marks, frame, width)
	fixed := 1 + 2 + 2 // headline, axis, cursor line and keys
	if hasStrip {
		fixed++
	}
	fixed += len(markLines)
	// fill reports how many charts fit from a first chart on. Another chart
	// is added only while each keeps a readable height; a single chart may
	// shrink to the minimum.
	fill := func(first, budget int) (shown, used int) {
		for i := first; i < len(panels); i++ {
			if shown > 0 && used+len(legends[i])+graphMinRows > budget-(shown+1)*(graphGoodRows-graphMinRows) {
				break
			}
			used += len(legends[i]) + graphMinRows
			shown++
		}
		return shown, used
	}
	var (
		status      []string
		shown, used int
		budget      int
	)
	// The status line names how many charts are shown, and its own height
	// decides how many fit: lay out for one line first, then for two.
	for statusRows := 1; statusRows <= 2; statusRows++ {
		budget = pg.Height - fixed - statusRows
		first = min(max(pg.First, 0), len(panels)-1)
		shown, used = fill(first, budget)
		// Scrolling never hides a chart that would still fit above.
		for first > 0 && first+shown == len(panels) {
			if more, moreUsed := fill(first-1, budget); more == shown+1 {
				first, shown, used = first-1, more, moreUsed
				continue
			}
			break
		}
		for level := range 3 {
			parts := s.statusParts(shown, len(panels), level)
			if pg.Busy != "" {
				parts[0] = pg.Busy + " · shown: " + parts[0]
			}
			for i := range parts {
				parts[i] = graphWords(parts[i], pg.ASCII)
			}
			if status = graphJoinLines(parts, " · ", width); len(status) == 1 {
				break
			}
		}
		if len(status) <= statusRows {
			break
		}
	}
	if len(status) > 2 {
		// Past two lines the tail is cut, visibly.
		status = append(status[:1], strings.Join(status[1:], " · "))
	}
	pg.First = first
	rows := make([]int, shown)
	spare := budget - used
	for i := range rows {
		rows[i] = graphMinRows
	}
	for grew := true; spare > 0 && grew; {
		grew = false
		for i := range rows {
			if spare > 0 && rows[i] < graphMaxRows {
				rows[i]++
				spare--
				grew = true
			}
		}
	}
	for _, line := range status {
		plain(c.Muted, line)
	}
	for i := range shown {
		panel := &panels[first+i]
		lines = append(lines, legends[first+i]...)
		lines = append(lines, panel.Chart.Render(frame, rows[i], gutter, options)...)
	}
	if hasStrip {
		label := graphRestartLabel
		if state := graphStateWord(&restarts); state != "" && state != graphNoData {
			label = "rst " + state
		}
		lines = append(lines, chart.Strip(label, events, frame, gutter, c.Restarts, options))
	}
	chartMarks := make([]chart.Mark, len(marks))
	for i, mark := range marks {
		chartMarks[i] = chart.Mark{Key: mark.Key, T: mark.T, Label: mark.Label}
	}
	lines = append(lines, chart.Axis(frame, gutter, chartMarks, options)...)
	for _, line := range markLines {
		plain("", line)
	}
	readout := "Cursor " + graphClock(at)
	switch behind := s.End.Sub(at); {
	case behind <= 0:
		readout += " (window end)"
	case width < 72:
		readout += ", -" + graphAgo(behind)
	default:
		readout += ", " + graphAgo(behind) + " before window end"
	}
	if hasStrip {
		switch state := graphStateWord(&restarts); {
		case state != "":
			readout += " · restarts " + state
		default:
			total, found := 0.0, false
			for i := range restarts.Series {
				series := chart.Series{Points: graphPoints(restarts.Series[i].Samples)}
				if v, ok := frame.ValueAt(&series, at); ok {
					total, found = total+v, true
				}
			}
			count := graphNoSample
			if found {
				count = chart.Count.Format(total)
			}
			if measured {
				readout += fmt.Sprintf(" · restarts %s, +%s in window", count, chart.Count.Format(rise))
			} else {
				// One sample cannot show a rise; that is not "+0".
				readout += fmt.Sprintf(" · restarts %s, rise not known (1 sample)", count)
			}
		}
	}
	plain("", readout)
	plain(c.Muted, graphFooter(width, true, pg.ASCII, s.Target.workload()))
	return lines
}

// graphReport is the s page: exactly what was asked and what came back.
func graphReport(s *graphSnapshot) string {
	var b strings.Builder
	if s == nil {
		return "Nothing has been read yet."
	}
	fmt.Fprintf(&b, "Graph of %s · READ ONLY · %s\n", s.Target.Label(), s.Target.Context)
	fmt.Fprintf(&b, "Window: %s to %s (last %s, UTC)\n", s.Start.UTC().Format("2006-01-02 15:04:05Z"), s.End.UTC().Format("2006-01-02 15:04:05Z"), graphSpan(s.Window))
	fmt.Fprintf(&b, "Step: %s, so at most %d points per series. The window ends at most one minute before the read.\n", graphSpan(s.Step), int(s.Window/s.Step)+1)
	fmt.Fprintf(&b, "Read began: %s · Source: prometheus via %s\n", graphClock(s.ReadAt), s.Source)
	fmt.Fprintf(&b, "Each query is one bounded GET (%s, %d series, %d samples). Nothing is polled; r reads again.\n",
		graphSpan(observability.DefaultTimeout), observability.MaxSeries, observability.MaxSamples)
	frame := chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: 1}
	describe := func(name string, res *observability.PromResult) {
		fmt.Fprintf(&b, "\n%s\n  %s\n", name, res.Query)
		state := string(res.State)
		if res.State == "" {
			state = "not read"
		}
		if res.Reason != "" {
			state += " (" + res.Reason + ")"
		}
		fmt.Fprintf(&b, "  %s · %d series · %d samples", state, len(res.Series), graphSamples(res))
		if res.Rejected > 0 {
			fmt.Fprintf(&b, " · %d samples rejected", res.Rejected)
		}
		if res.OK() && graphSamples(res) == 0 {
			b.WriteString(" · answered, matched nothing: missing data, not zero")
		}
		b.WriteString("\n")
		for i := range res.Series {
			if res.Step == 0 {
				break // an instant query has one sample by design
			}
			series := chart.Series{Points: graphPoints(res.Series[i].Samples)}
			holes := frame.Holes(&series)
			if holes.Samples == 0 || (holes.Gaps == 0 && !holes.LateStart && !holes.EarlyEnd) {
				continue
			}
			var notes []string
			if holes.LateStart {
				notes = append(notes, "begins "+graphClock(holes.First))
			}
			if holes.Gaps > 0 {
				notes = append(notes, fmt.Sprintf("%d gap(s) inside", holes.Gaps))
			}
			if holes.EarlyEnd {
				notes = append(notes, "ends "+graphClock(holes.Last))
			}
			label := "series"
			for _, key := range []string{"pod", "node"} {
				if v := res.Series[i].Labels[key]; v != "" {
					label = v
				}
			}
			fmt.Fprintf(&b, "  holes: %s %s (drawn as gaps)\n", label, strings.Join(notes, ", "))
		}
	}
	if s.Status != nil {
		fmt.Fprintf(&b, "\nNothing was asked: %s", s.Status.State)
		if s.Status.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Status.Reason)
		}
		b.WriteString("\n")
	}
	if s.Members != nil {
		b.WriteString("\nPODS BEHIND THE SERVICE (Kubernetes API, your credentials; the Pods it selects now)\n")
		if s.Members.Origin != "" {
			fmt.Fprintf(&b, "  %s\n", s.Members.Origin)
		}
		if s.Members.Problem != "" {
			fmt.Fprintf(&b, "  %s\n", s.Members.Problem)
		}
		for _, note := range s.Members.Notes {
			fmt.Fprintf(&b, "  %s\n", note)
		}
		if len(s.Pods) > 0 {
			fmt.Fprintf(&b, "  Pods found: %s\n", strings.Join(s.Pods, ", "))
		}
		b.WriteString("  A Pod the Service selected earlier in the window and no longer selects is not drawn.\n")
	}
	if s.PodInfo != nil {
		describe("NODE AND OWNER OF EACH POD (instant query at the window end)", s.PodInfo)
	}
	if s.Target.workload() && s.Owner == nil && s.PodsOmitted > 0 {
		fmt.Fprintf(&b, "  NOT READ: %d Pods beyond the matcher limit (%d names). The charts cover only the first %d by name.\n",
			s.PodsOmitted, graphPodNameLimit, len(s.Pods)-s.PodsOmitted)
	}
	if s.Owner != nil {
		describe("PODS OF THE WORKLOAD (instant query at the window end)", s.Owner)
		if len(s.Pods) > 0 {
			fmt.Fprintf(&b, "  Pods found: %s\n", strings.Join(s.Pods, ", "))
		}
		if s.PodsOmitted > 0 {
			fmt.Fprintf(&b, "  NOT READ: %d Pods beyond the matcher limit (%d names). The charts cover only the first %d by name.\n",
				s.PodsOmitted, graphPodNameLimit, len(s.Pods)-s.PodsOmitted)
		}
		if len(s.Pods) > graphLineCap {
			fmt.Fprintf(&b, "  More than %d Pods: drawn as minimum, average and maximum across Pods, not one line each.\n", graphLineCap)
		}
	}
	for i := range s.Plan.Panels {
		panel := &s.Plan.Panels[i]
		for j := range panel.Series {
			res := s.Results[graphResultKey(panel.ID, panel.Series[j].ID)]
			describe(fmt.Sprintf("%s · %s (%s)", panel.Title, panel.Series[j].Name, panel.Note), &res)
		}
	}
	if s.Plan.Restarts != nil {
		res := s.Results[graphResultKey("", s.Plan.Restarts.ID)]
		describe("RESTARTS · counter; a mark is drawn where it rose between two samples", &res)
	}
	b.WriteString("\nAXIS MARKS (times read from the objects; they state order, not cause)\n")
	if len(s.Marks) == 0 {
		b.WriteString("  none read from the object\n")
	}
	for _, mark := range s.Marks {
		fmt.Fprintf(&b, "  %c %s at %s · from %s\n", mark.Key, mark.Label, mark.T.UTC().Format("2006-01-02 15:04:05Z"), mark.Origin)
	}
	if s.MarkNote != "" {
		fmt.Fprintf(&b, "  %s\n", s.MarkNote)
	}
	b.WriteString("  F, when shown, is the first rise of the restart counter inside the window.\n")
	b.WriteString("\nREADING THE PAGE\n")
	b.WriteString("  A value in the legend is the sample at the cursor, not an interpolation.\n")
	b.WriteString("  – means no sample at the cursor; no data means the query matched nothing in the window.\n")
	b.WriteString("  Lines are not joined across more than one and a half steps.\n")
	return b.String()
}
