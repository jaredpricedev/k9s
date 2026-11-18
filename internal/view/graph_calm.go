// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// The calm layout of :graph (docs/presentation.md, docs/graphs.md). The
// page says first what the series show, in a sentence; draws one chart at
// full size; and puts every other signal on one row under it, on the same
// time axis. It is built with the calm kit from the same snapshot the
// stacked charts are, and reads nothing of its own: a is the page with
// every chart, s the queries.

const (
	graphOrderCaveat   = "Order in time, not proof of cause."
	graphReadingCaveat = "A reading, not proof of cause."
	graphNeutralCaveat = "A reading, not a statement of health."
	graphAlsoLabel     = "ALSO"
	graphNotReadLabel  = "NOT READ"
	graphNoDataLabel   = "NO DATA"
	graphOutsideLabel  = "OUTSIDE THE WINDOW"
	graphMomentsLabel  = "MOMENTS"
	graphReadingWord   = "reading…"
	// graphFollow is how many steps after a stretch at the limit a restart
	// still counts as following it.
	graphFollow = 3
)

func (pg *graphPage) calmOptions() calm.Options {
	return calm.Options{ASCII: pg.ASCII, Palette: pg.Palette}
}

// graphCalmSpan is text in one colour on a line the view writes itself
// beside the kit's lines. The tag names the foreground, the background and
// the attributes, as every tag of a calm page does.
func graphCalmSpan(color string, bold bool, text string) string {
	if text == "" {
		return ""
	}
	fg, attrs := "-", "-"
	if color != "" {
		fg = color
	}
	if bold {
		attrs = "b"
	}
	if fg == "-" && attrs == "-" {
		return tview.Escape(text)
	}
	return "[" + fg + ":-:" + attrs + "]" + tview.Escape(text) + "[-:-:-]"
}

// graphCalmWhen says which sample a calm page reads: the one at the window
// end, or the one under a cursor that was moved.
type graphCalmWhen struct {
	at    time.Time
	moved bool
	// word follows a value ("254Mi now", "206Mi at 04:55:30").
	word string
}

func graphCalmAt(s *graphSnapshot, pg *graphPage) graphCalmWhen {
	when := graphCalmWhen{at: s.End, word: "now"}
	if !pg.Moved {
		return when
	}
	frame := chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: 1}
	when.at, when.moved = frame.TimeAt(min(max(pg.Cursor, 0), frame.Steps())), true
	when.word = "at " + graphCalmClock(s, when.at)
	return when
}

// graphCalmClock is a sample time as a calm page writes it: to the minute,
// and to the second where the samples are less than a minute apart. It is
// as wide for every time of a window.
func graphCalmClock(s *graphSnapshot, at time.Time) string {
	if s.Step < time.Minute {
		return at.UTC().Format("15:04:05")
	}
	return at.UTC().Format("15:04")
}

// graphPanelWords are how a chart is named in a sentence and on a strip.
var graphPanelWords = map[string][2]string{
	"memory": {"Memory", "memory"}, "cpu": {"CPU", "CPU"}, "network": {"Network", "network"},
	"disk": {"Disk /", "disk /"}, "pods": {"The Pod count", "pods"},
}

func graphPanelWord(spec *graphPanelSpec) (subject, strip string) {
	if words, ok := graphPanelWords[spec.ID]; ok {
		return words[0], words[1]
	}
	lower := strings.ToLower(spec.Title)
	if lower == "" {
		return "The reading", "reading"
	}
	first, size := utf8.DecodeRuneInString(lower)
	return string(unicode.ToUpper(first)) + lower[size:], lower
}

// graphSay writes a value for a sentence: as the charts write it, with the
// word a bare number of cores needs to be read as one.
func graphSay(unit chart.Unit, v float64) string {
	text := unit.Format(v)
	if unit != chart.Cores || strings.HasSuffix(text, "m") {
		return text
	}
	if text == "1" {
		return "1 core"
	}
	return text + " cores"
}

func graphTimes(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// graphReached is when a line was at a limit: the stretches of
// neighbouring samples that are chart.Near the limit read at the same
// time. A stretch of one sample starts and ends at that sample.
func graphReached(line, limit []chart.Point, step time.Duration) []chart.Interval {
	var out []chart.Interval
	gap := step + step/2
	at, open := 0, false
	var prev time.Time
	for _, p := range line {
		for at+1 < len(limit) && !limit[at+1].T.After(p.T) {
			at++
		}
		near := false
		if at < len(limit) {
			if d := p.T.Sub(limit[at].T); d >= 0 && d <= gap {
				near = chart.Near(p.V, limit[at].V)
			}
		}
		switch {
		case near && open && p.T.Sub(prev) <= gap:
			out[len(out)-1].End = p.T
		case near:
			out = append(out, chart.Interval{Start: p.T, End: p.T})
		}
		open, prev = near, p.T
	}
	return out
}

// graphLine is what one drawn line of a panel did in the window.
type graphLine struct {
	series     *chart.Series
	last, peak chart.Point
	// limit is the panel's limit at the line's latest sample; zero when
	// none was read by then.
	limit float64
	// reached are the stretches in which the line was at the limit.
	reached []chart.Interval
}

// limitSeries is the level the panel's lines are held under, or nil.
func (p *graphPanel) limitSeries() *chart.Series {
	for i := range p.Chart.Series {
		if s := &p.Chart.Series[i]; s.Limit && s.Kind == chart.Reference {
			return s
		}
	}
	return nil
}

// lines are the drawn lines of the panel that have samples.
func (p *graphPanel) lines(step time.Duration) []graphLine {
	var limit []chart.Point
	if s := p.limitSeries(); s != nil {
		limit = s.Points
	}
	var out []graphLine
	for i := range p.Chart.Series {
		s := &p.Chart.Series[i]
		if s.Kind != chart.Line || len(s.Points) == 0 {
			continue
		}
		line := graphLine{series: s, last: s.Points[len(s.Points)-1], peak: s.Points[0], reached: graphReached(s.Points, limit, step)}
		for _, pt := range s.Points {
			if pt.V > line.peak.V {
				line.peak = pt
			}
		}
		for _, pt := range limit {
			if !pt.T.After(line.last.T) {
				line.limit = pt.V
			}
		}
		out = append(out, line)
	}
	return out
}

// throttled is the largest share of CPU periods throttled in the window.
func (p *graphPanel) throttled() (peak float64, ok bool) {
	for i, spec := range p.Specs {
		if spec.ID != "throttled" {
			continue
		}
		for _, pt := range p.Chart.Series[i].Points {
			if !ok || pt.V > peak {
				peak, ok = pt.V, true
			}
		}
	}
	return peak, ok
}

func graphAnyReached(lines []graphLine) bool {
	for i := range lines {
		if len(lines[i].reached) > 0 {
			return true
		}
	}
	return false
}

// graphMainPanel chooses the one chart a calm page draws at full size:
//
//  1. memory, when it was at its limit in the window or the last recorded
//     exit of a container was OOMKilled;
//  2. else CPU, when it was throttled in at least slowpath.ThrottleFloor of
//     its periods or was at its limit;
//  3. else the first chart that was at its limit (a Node's disk, its Pods);
//  4. else memory, and where there is no memory chart the first one.
//
// why says which of these it was, for the s page.
func graphMainPanel(panels []graphPanel, marks []graphMark, step time.Duration) (index int, why string) {
	find := func(id string) int {
		for i := range panels {
			if panels[i].Spec.ID == id {
				return i
			}
		}
		return -1
	}
	memory, cpu := find("memory"), find("cpu")
	if memory >= 0 {
		if graphAnyReached(panels[memory].lines(step)) {
			return memory, "memory was at its limit in the window"
		}
		for _, mark := range marks {
			if mark.Key == 'X' && strings.Contains(mark.Label, "OOMKilled") {
				return memory, "the last recorded exit was OOMKilled"
			}
		}
	}
	if cpu >= 0 {
		if peak, ok := panels[cpu].throttled(); ok && peak >= slowpath.ThrottleFloor {
			return cpu, fmt.Sprintf("CPU was throttled in %s of its periods (%s and more counts)", chart.Ratio.Format(peak), chart.Ratio.Format(slowpath.ThrottleFloor))
		}
		if graphAnyReached(panels[cpu].lines(step)) {
			return cpu, "CPU was at its limit in the window"
		}
	}
	for i := range panels {
		if graphAnyReached(panels[i].lines(step)) {
			_, word := graphPanelWord(panels[i].Spec)
			return i, word + " was at its limit in the window"
		}
	}
	if memory >= 0 {
		return memory, "nothing was at a limit, so memory"
	}
	return 0, "nothing was at a limit, so the first chart"
}

// graphRestarts is what the restart counter showed in the window.
type graphRestarts struct {
	// shown is false for an object without the counter (a Node).
	shown bool
	// state says why the counter was not read; empty when it was.
	state  string
	events []time.Time
	rise   float64
	// measured is false when no series had two samples: one sample cannot
	// show a rise, and that is not "no restart".
	measured bool
	// first and last are the first and the last sample of the counter.
	first, last time.Time
}

func (s *graphSnapshot) restarts() graphRestarts {
	res, ok := s.Results[graphResultKey("", "restarts")]
	if !ok || s.Plan.Restarts == nil {
		return graphRestarts{}
	}
	r := graphRestarts{shown: true, state: graphStateWord(&res)}
	r.events, r.rise, r.measured = graphRestartEvents(&res, s.Step)
	for i := range res.Series {
		samples := res.Series[i].Samples
		if len(samples) == 0 {
			continue
		}
		if r.first.IsZero() || samples[0].Timestamp.Before(r.first) {
			r.first = samples[0].Timestamp
		}
		if samples[len(samples)-1].Timestamp.After(r.last) {
			r.last = samples[len(samples)-1].Timestamp
		}
	}
	return r
}

// followed counts the stretches that a restart follows: a rise of the
// counter from the start of the stretch to graphFollow steps after its
// end. A restart is counted for one stretch only.
func (r *graphRestarts) followed(stretches []chart.Interval, step time.Duration) int {
	n, next := 0, 0
	for _, stretch := range stretches {
		for next < len(r.events) && r.events[next].Before(stretch.Start) {
			next++
		}
		if next < len(r.events) && !r.events[next].After(stretch.End.Add(graphFollow*step)) {
			n++
			next++
		}
	}
	return n
}

// sentence is what the counter adds to a neutral verdict, or "".
func (r *graphRestarts) sentence(window time.Duration) string {
	switch {
	case !r.shown || r.state != "" || !r.measured:
		return ""
	case r.rise > 0:
		noun := "restarts"
		if r.rise == 1 {
			noun = "restart"
		}
		return fmt.Sprintf("%s %s in the last %s.", chart.Count.Format(r.rise), noun, graphSpan(window))
	}
	return "No restart was recorded in the last " + graphSpan(window) + "."
}

// graphCalmVerdict is the answer of a calm page, derived from the chart
// the rule chose. It states what the samples show and nothing about why:
//
//   - a line that was at its limit: how often, and for a single object's
//     memory whether a restart followed each time;
//   - CPU that was throttled: the largest share, and the peak against the
//     limit;
//   - otherwise the latest value against its limit, and whether the
//     restart counter rose.
//
// A chart without a sample says that it has none.
func graphCalmVerdict(s *graphSnapshot, p *graphPanel, restarts *graphRestarts) calm.Verdict {
	subject, word := graphPanelWord(p.Spec)
	unit := p.Chart.Unit
	window := "the last " + graphSpan(s.Window)
	lines := p.lines(s.Step)
	if len(lines) == 0 {
		state := graphNoData
		for i, spec := range p.Specs {
			if spec.Kind == chart.Line {
				state = p.States[i]
				break
			}
		}
		if state == graphNoData || state == "" {
			return calm.Unread(subject+" has no samples in "+window+".", "The query matched nothing: that is missing data, not a zero.")
		}
		return calm.Unread(subject+" was not read ("+state+").", "r reads again; :sources shows the declaration.")
	}
	drawn := 0
	for i := range p.Chart.Series {
		if p.Chart.Series[i].Kind == chart.Line {
			drawn++
		}
	}
	single := drawn == 1
	limitName := "limit"
	if ls := p.limitSeries(); ls != nil && ls.Name != "" {
		limitName = ls.Name
	}
	// against is "a 256Mi limit" for a line, or says that none was read.
	against := func(l *graphLine) string {
		if l.limit > 0 {
			return graphSay(unit, l.limit) + " " + limitName
		}
		return ""
	}

	var hit []*graphLine
	for i := range lines {
		if len(lines[i].reached) > 0 {
			hit = append(hit, &lines[i])
		}
	}
	if peak, ok := p.throttled(); ok && peak >= slowpath.ThrottleFloor {
		top := &lines[0]
		for i := range lines {
			if lines[i].peak.V > top.peak.V {
				top = &lines[i]
			}
		}
		usage := "Usage peaked at " + graphSay(unit, top.peak.V)
		if !single {
			usage += " on " + top.series.Name
		}
		if limit := against(top); limit != "" {
			usage += " against a " + limit + "."
		} else {
			usage += "; no CPU limit was read."
		}
		return calm.Verdict{Sentences: []string{
			subject + " was throttled in up to " + chart.Ratio.Format(peak) + " of its periods.", usage,
		}, Caveat: graphReadingCaveat}
	}
	if len(hit) > 0 {
		v := calm.Verdict{Caveat: graphReadingCaveat}
		switch {
		case single:
			v.Sentences = []string{fmt.Sprintf("%s reached its %s %s.", subject, against(hit[0]), graphTimes(len(hit[0].reached)))}
		case len(hit) == 1:
			v.Sentences = []string{fmt.Sprintf("%s of %s reached the %s %s.", subject, hit[0].series.Name, against(hit[0]), graphTimes(len(hit[0].reached)))}
		default:
			names := make([]string, 0, len(hit))
			for _, l := range hit {
				names = append(names, l.series.Name)
			}
			list := strings.Join(names, ", ")
			if len(names) > 3 {
				list = fmt.Sprintf("%s and %d more", strings.Join(names[:3], ", "), len(names)-3)
			}
			v.Sentences = []string{fmt.Sprintf("%s reached the %s on %d of %d lines: %s.", subject, against(hit[0]), len(hit), len(lines), list)}
		}
		switch {
		case p.Spec.ID != "memory" || !restarts.shown || restarts.state != "" || !restarts.measured:
		case !single:
			// The counter of a workload is not told apart by Pod here.
			if text := restarts.sentence(s.Window); text != "" && restarts.rise > 0 {
				v.Sentences = append(v.Sentences, text)
			}
		default:
			total := len(hit[0].reached)
			switch n := restarts.followed(hit[0].reached, s.Step); {
			case n == total && total == 1:
				v.Sentences, v.Caveat = append(v.Sentences, "A container restarted after it."), graphOrderCaveat
			case n == total:
				v.Sentences, v.Caveat = append(v.Sentences, "A container restarted after each."), graphOrderCaveat
			case n > 0:
				v.Sentences, v.Caveat = append(v.Sentences, fmt.Sprintf("A container restarted after %d of them.", n)), graphOrderCaveat
			case restarts.rise > 0:
				v.Sentences = append(v.Sentences, strings.TrimSuffix(restarts.sentence(s.Window), ".")+", none right after.")
			default:
				v.Sentences = append(v.Sentences, restarts.sentence(s.Window))
			}
		}
		return v
	}

	// Nothing was at a limit: the latest value, against the limit.
	top := &lines[0]
	for i := range lines {
		if lines[i].last.V > top.last.V {
			top = &lines[i]
		}
	}
	value := graphSay(unit, top.last.V) + " now"
	if s.End.Sub(top.last.T) > s.Step+s.Step/2 {
		value = graphSay(unit, top.last.V) + " at " + top.last.T.UTC().Format("15:04") + ", its last sample"
	}
	text := subject + " is " + value
	if !single {
		text = subject + " is highest on " + top.series.Name + ": " + value
	}
	switch limit := against(top); {
	case limit != "" && single:
		text += ", " + chart.Ratio.Format(top.last.V/top.limit) + " of its " + limit + "."
	case limit != "":
		text += ", " + chart.Ratio.Format(top.last.V/top.limit) + " of the " + limit + "."
	case p.limitSeries() != nil:
		text += "; no " + word + " " + limitName + " was read."
	default:
		text += "."
	}
	v := calm.Verdict{Sentences: []string{text}, Caveat: graphNeutralCaveat}
	more := restarts.sentence(s.Window)
	if p.Spec.ID == "memory" {
		// An exit the object records as OOMKilled is said with the memory
		// it is about, whether or not a sample caught the limit. It takes
		// the place of the count of restarts, which their row has.
		for _, mark := range s.Marks {
			if mark.Key == 'X' && strings.Contains(mark.Label, "OOMKilled") {
				more = "The last exit was OOMKilled, at " + mark.T.UTC().Format("15:04") + "."
				break
			}
		}
	}
	if more != "" {
		v.Sentences = append(v.Sentences, more)
	}
	return v
}

// graphCalmHead is the line over the main chart: its name, the value of
// its line now (or under the cursor) with the words "at limit" where that
// is so, and the levels drawn. A chart with several lines names each by
// its letter. Entries that do not fit are counted.
func graphCalmHead(p *graphPanel, f chart.Frame, when graphCalmWhen, pg *graphPage, width int) string {
	o := pg.calmOptions()
	pal := o.Palette
	// A part is one entry of the legend, as it reads and as it is drawn.
	type part struct{ plain, tagged string }
	add := func(parts []part, pieces ...part) []part {
		joined := part{}
		for _, piece := range pieces {
			joined.plain += piece.plain
			joined.tagged += piece.tagged
		}
		return append(parts, joined)
	}
	span := func(color string, bold bool, text string) part {
		text = graphWords(text, pg.ASCII)
		return part{plain: text, tagged: graphCalmSpan(color, bold, text)}
	}
	entries := p.legend(f, when.at, pg.options(-1))
	drawn := 0
	for i := range p.Chart.Series {
		if p.Chart.Series[i].Kind == chart.Line {
			drawn++
		}
	}
	var parts []part
	if drawn != 1 {
		parts = add(parts, span(pal.Muted, false, when.word))
	}
	for i := range entries {
		entry, series := &entries[i], &p.Chart.Series[i]
		switch {
		case series.Kind == chart.Line && drawn == 1:
			// "254Mi at limit" is the value and the words for its colour.
			value, flag := entry.Value, ""
			if cut := strings.LastIndex(value, " at "); cut > 0 && p.States[i] == "" {
				value, flag = value[:cut], value[cut+1:]
			}
			if _, sampled := f.ValueAt(series, when.at); sampled {
				value += " " + when.word
			}
			if entry.Glyph != string(chart.FillGlyph) {
				// A line that is not filled (dots, noIcons) carries its
				// letter in the plot, so the legend names it.
				parts = add(parts, span(series.Color, true, entry.Glyph), span(pal.Text, false, " "+value))
			} else {
				parts = add(parts, span(pal.Text, false, value))
			}
			if flag != "" {
				parts = add(parts, span(pal.Warn, true, flag))
			}
		case series.Kind == chart.Line:
			parts = add(parts, span(series.Color, true, entry.Glyph), span(pal.Text, false, " "+strings.TrimSpace(entry.Name+" "+entry.Value)))
		case series.Kind == chart.Reference:
			parts = add(parts, span(series.Color, false, strings.TrimSpace(entry.Glyph+" "+entry.Name+" "+entry.Value)))
		default:
			parts = add(parts, span(pal.Muted, false, strings.TrimSpace(entry.Name+" "+entry.Value)))
		}
	}
	// Two cells between entries: a guide of the page runs only through a
	// blank cell whose neighbours are blank, so it never cuts the legend.
	const gap = "  "
	title := strings.ToUpper(p.Spec.Title)
	head := strings.Repeat(" ", calm.Indent) + calm.HeadingText(title, pal.Accent, o)
	used := calm.Indent + runewidth.StringWidth(graphWords(title, pg.ASCII))
	for i, piece := range parts {
		more := ""
		if left := len(parts) - i - 1; left > 0 {
			more = fmt.Sprintf("%s+%d more", gap, left)
		}
		if used+len(gap)+runewidth.StringWidth(piece.plain)+len(more) > width {
			if left := len(parts) - i; left > 0 && used+len(gap)+len(fmt.Sprintf("+%d more", left)) <= width {
				head += gap + graphCalmSpan(pal.Muted, false, fmt.Sprintf("+%d more", left))
			}
			break
		}
		head += gap + piece.tagged
		used += len(gap) + runewidth.StringWidth(piece.plain)
	}
	// The legend is over the plot, not part of it: a guide of the page
	// starts under it. The kit runs a guide through blank cells in the
	// default style only, so the rest of the row is blank cells that carry
	// an attribute; an episode still shades them.
	if rest := width - tview.TaggedStringWidth(head); rest > 0 {
		head += "[-:-:d]" + strings.Repeat(" ", rest) + "[-:-:-]"
	}
	return head
}

// graphHighest folds the lines of a panel into the highest value at each
// sample time, which is what its one-row strip draws.
func graphHighest(p *graphPanel) []chart.Point {
	var series [][]chart.Point
	for i := range p.Chart.Series {
		if s := &p.Chart.Series[i]; s.Kind == chart.Line && len(s.Points) > 0 {
			series = append(series, s.Points)
		}
	}
	if len(series) == 1 {
		return series[0]
	}
	_, _, high := graphBand(series, false)
	return high
}

// graphCalmStrip is a chart that is not the main one, as one row: its
// lines folded into the highest of them, on the scale the chart itself
// would have, with the value now (or under the cursor) after it.
func graphCalmStrip(p *graphPanel, f chart.Frame, when graphCalmWhen, pg *graphPage) calm.Row {
	_, word := graphPanelWord(p.Spec)
	row := calm.Row{What: word, Color: pg.Palette.Accent}
	points := graphHighest(p)
	if len(points) == 0 {
		// Nothing was read: the gap glyph all along, and the reason.
		row.Number, row.Strip, row.Color = graphNoData, calm.UnreadStrip(), ""
		for i, spec := range p.Specs {
			if spec.Kind == chart.Line && p.States[i] != "" {
				row.Number = p.States[i]
				break
			}
		}
		return row
	}
	row.Strip = calm.Trend(points).Top(p.Chart.Top())
	row.Number = graphWords(graphNoSample, pg.ASCII)
	best, lines := -1, 0
	var value float64
	for i := range p.Chart.Series {
		s := &p.Chart.Series[i]
		if s.Kind != chart.Line {
			continue
		}
		lines++
		if v, ok := f.ValueAt(s, when.at); ok && (best < 0 || v > value) {
			best, value = i, v
		}
	}
	if best < 0 {
		return row
	}
	row.Number = p.Chart.Unit.Format(value)
	var notes []string
	if lines > 1 {
		notes = append(notes, p.Chart.Series[best].Name)
	}
	if flag := strings.TrimSpace(p.atLimit(f, when.at, value)); flag != "" {
		notes = append(notes, flag)
	}
	row.Compare = strings.Join(notes, " ")
	return row
}

// graphCalmReserve is the widest number and the widest note a strip of the
// panel writes at any sample of the window, in cells. The row reads the
// sample under the cursor: the highest of the lines there, the name of
// that line and whether it is at a limit. Columns held this wide are the
// same wherever the cursor is, so no strip moves when it does.
func graphCalmReserve(p *graphPanel, f chart.Frame, pg *graphPage) (number, compare int) {
	lines, limited, name := 0, false, 0
	for i := range p.Chart.Series {
		s := &p.Chart.Series[i]
		if s.Kind == chart.Line {
			lines++
		}
		limited = limited || s.Limit && len(s.Points) > 0
	}
	for i := range p.Chart.Series {
		if s := &p.Chart.Series[i]; s.Kind == chart.Line && lines > 1 && len(s.Points) > 0 {
			name = max(name, runewidth.StringWidth(s.Name))
		}
	}
	number = runewidth.StringWidth(graphWords(graphNoSample, pg.ASCII))
	flag := 0
	for _, pt := range graphHighest(p) {
		number = max(number, runewidth.StringWidth(p.Chart.Unit.Format(pt.V)))
		if limited {
			flag = max(flag, runewidth.StringWidth(strings.TrimSpace(p.atLimit(f, pt.T, pt.V))))
		}
	}
	compare = name + flag
	if name > 0 && flag > 0 {
		compare++
	}
	return number, compare
}

// graphCalmTitle is the title line of a calm page of :graph.
func graphCalmTitle(view string, s *graphSnapshot, pg *graphPage) calm.Title {
	t := s.tally()
	title := calm.Title{View: view, Object: s.Target.Label(), Window: "last " + graphSpan(s.Window), KeepEnd: true}
	if s.Target.workload() && len(s.Pods) > 0 {
		title.Window = graphPlural(len(s.Pods), "pod") + " · " + title.Window
	}
	if t.Asked > 0 {
		title.Read, title.Done, title.Total = s.ReadAt, t.Asked-t.Failed, t.Asked
	}
	if pg.Busy != "" {
		title.Busy = graphReadingWord
	}
	return title
}

// graphCalmLines lays a calm page out for the size of the view and writes
// back what the size gave up.
//
// A page with a chart is laid out twice when the first layout folds the
// rows under the chart to one line: a chart of three rows still shows a
// level against its limit, and on an 80x24 terminal it is what keeps the
// restarts on the axis of the chart they are held against. The shorter
// chart is taken only when it keeps those rows.
//
// A screen that has no room for the chart under its verdict gives up the
// guides of the moments first: they become one line (MOMENTS) that the
// layout may leave out and then names, so that the smallest page is the
// chart alone on its axis and not a page without one.
func graphCalmLines(page *calm.Page, pg *graphPage) []string {
	o := pg.calmOptions()
	width, height := max(pg.Width, 20), max(pg.Height, 3)
	plan := calm.Lay(page, width, height, o)
	folds := func(plan *calm.Plan) bool {
		for i := range page.Groups {
			if len(page.Groups[i].Rows) > 0 && (plan.Folded[i] || plan.Hidden[i]) {
				return true
			}
		}
		return plan.TooSmall
	}
	if page.Chart != nil && page.Chart.Min == 0 && folds(&plan) {
		page.Chart.Min = graphMinRows
		if short := calm.Lay(page, width, height, o); !folds(&short) {
			plan = short
		} else {
			page.Chart.Min = 0
		}
	}
	if page.Chart != nil && plan.TooSmall {
		compact := *page
		chart := *page.Chart
		chart.Min = graphMinRows
		compact.Chart = &chart
		var moments []string
		for _, mark := range page.Bands.Marks {
			moments = append(moments, mark.Label)
		}
		compact.Bands.Marks = nil
		compact.Groups = append([]calm.Group(nil), page.Groups...)
		if len(moments) > 0 {
			compact.Groups = append(compact.Groups, calm.Group{Label: graphMomentsLabel, Role: calm.QuietRole, Summary: moments})
		}
		if small := calm.Lay(&compact, width, height, o); !small.TooSmall {
			page, plan = &compact, small
		}
	}
	pg.Dropped, pg.Missing = plan.Dropped, plan.Missing
	return calm.Render(page, &plan, o).Lines()
}

// graphCalmFailure is the page of a read on which nothing can be drawn:
// the reason as the answer, in the warning colour, and the keys that read
// again or show what was asked.
func graphCalmFailure(view string, s *graphSnapshot, failure []string, pg *graphPage) calm.Page {
	title := graphCalmTitle(view, s, pg)
	sentences := append([]string(nil), failure...)
	if s.Source != "" {
		sentences = append(sentences, "Source: prometheus via "+s.Source+".")
	}
	return calm.Page{
		Title:   title,
		Verdict: calm.Verdict{Kind: calm.NotRead, Sentences: sentences, Caveat: "Not read is not healthy."},
		Next:    []calm.Action{{Key: "r", Label: "read again"}, {Key: "s", Label: "what was asked"}},
		Keys:    []string{"1 15m  2 1h  3 6h  4 24h", "Esc back"},
	}
}

// graphCalmWaiting is the page shown while the first read of a window
// runs: the window's name is never put over the charts of another one.
func graphCalmWaiting(view, object string, window time.Duration, sentence string) calm.Page {
	return calm.Page{
		Title:   calm.Title{View: view, Object: object, Window: "last " + graphSpan(window), Busy: graphReadingWord, KeepEnd: true},
		Verdict: calm.Verdict{Sentences: []string{sentence, "Esc cancels and goes back."}},
		Keys:    []string{"Esc back"},
	}
}

// graphCalmMarks are the moments of a page as the kit draws them: a guide
// through every row, the letter on the axis and one line each. A moment
// outside the window has no guide; it is named in one folded line. The
// rollout says what the limit of the main chart was before it, when the
// samples on both sides show that it changed.
func graphCalmMarks(marks []graphMark, f chart.Frame, main *graphPanel) (inside []chart.Mark, outside []string) {
	for _, mark := range marks {
		clock := mark.T.UTC().Format("15:04")
		if _, ok := f.Col(mark.T); !ok {
			where := "after it"
			if mark.T.Before(f.Start) {
				where = "before it"
			}
			outside = append(outside, mark.Label+" at "+clock+" ("+where+")")
			continue
		}
		label := mark.Label + " at " + clock
		if ls := main.limitSeries(); mark.Key == 'R' && ls != nil {
			var before, after float64
			for _, pt := range ls.Points {
				if pt.T.Before(mark.T) {
					before = pt.V
				}
				after = pt.V
			}
			if before > 0 && after > 0 && (before > after*1.01 || after > before*1.01) {
				unit := main.Chart.Unit
				_, word := graphPanelWord(main.Spec)
				label += " · " + word + " " + ls.Name + " " + unit.Format(before) + " before it, " + unit.Format(after) + " now"
			}
		}
		inside = append(inside, chart.Mark{Key: mark.Key, T: mark.T, Label: label})
	}
	return inside, outside
}

// graphCalmPage builds the calm page of the charts of one object: the
// verdict, the main chart, every other signal as one row, the moments read
// from the object as guides, and what was not read.
func graphCalmPage(s *graphSnapshot, pg *graphPage) calm.Page {
	if failure := s.failure(); failure != nil {
		return graphCalmFailure(graphTitle, s, failure, pg)
	}
	o := pg.calmOptions()
	pal := o.Palette
	colors := pg.Colors
	// The warning colour is the area at its limit; a request is quiet.
	colors.Request = colors.Muted
	panels := s.panels(colors)
	if len(panels) == 0 {
		return graphCalmFailure(graphTitle, s, []string{"Nothing was asked for this object, so there is nothing to draw."}, pg)
	}
	restarts := s.restarts()
	// The moments read from the object. The first restart has no guide of
	// its own here: it is the first mark of the restarts row, on the same
	// axis. The page with every chart still marks it F.
	marks := append([]graphMark(nil), s.Marks...)
	sort.SliceStable(marks, func(a, b int) bool { return marks[a].T.Before(marks[b].T) })

	chosen, _ := graphMainPanel(panels, s.Marks, s.Step)
	main := chosen
	for i := range panels {
		if panels[i].Spec.ID == pg.Main && pg.Main != "" {
			main = i
		}
	}
	pg.Main = panels[main].Spec.ID
	panel := &panels[main]
	frame := chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: 1}
	when := graphCalmAt(s, pg)

	page := calm.Page{
		Title:       graphCalmTitle(graphTitle, s, pg),
		Verdict:     graphCalmVerdict(s, &panels[chosen], &restarts),
		Frame:       chart.Frame{Start: s.Start, End: s.End, Step: s.Step},
		NumberAfter: true,
	}
	var outside []string
	page.Bands.Marks, outside = graphCalmMarks(marks, frame, panel)
	if when.moved {
		page.Bands.Cursor = when.at
	}
	page.Chart = &calm.Chart{
		Head:   graphCalmHead(panel, frame, when, pg, max(pg.Width, 20)),
		Gutter: panel.Chart.GutterWidth(),
		Draw: func(f chart.Frame, gutter, height int) []string {
			return panel.Chart.Render(f, height, gutter, pg.options(-1))
		},
	}

	// The time a moved cursor reads is on the line over the chart, once.
	also := calm.Group{Label: graphAlsoLabel, Role: calm.Secondary}
	if restarts.shown {
		row := calm.Row{What: graphRestartLabel, Color: pal.Warn}
		switch {
		case restarts.state != "":
			row.Number, row.Strip, row.Color = restarts.state, calm.UnreadStrip(), ""
		case !restarts.measured:
			// One sample cannot show a rise; that is not "none".
			row.Number, row.Compare, row.Strip = graphWords(graphNoSample, pg.ASCII), "1 sample", calm.UnreadStrip()
		default:
			row.Number = chart.Count.Format(restarts.rise)
			strip := calm.Events(restarts.events...)
			if gap := s.Step + s.Step/2; restarts.first.Sub(s.Start) > gap {
				strip = strip.Unknown(chart.Interval{Start: s.Start, End: restarts.first.Add(-s.Step)})
			}
			if gap := s.Step + s.Step/2; s.End.Sub(restarts.last) > gap {
				strip = strip.Unknown(chart.Interval{Start: restarts.last, End: s.End})
			}
			row.Strip = strip
			if restarts.rise == 0 {
				row.Color = ""
			}
		}
		also.Rows = append(also.Rows, row)
	}
	for i := range panels {
		if i != main {
			also.Rows = append(also.Rows, graphCalmStrip(&panels[i], frame, when, pg))
			number, compare := graphCalmReserve(&panels[i], frame, pg)
			also.NumberWidth, also.CompareWidth = max(also.NumberWidth, number), max(also.CompareWidth, compare)
		}
	}
	if len(also.Rows) > 0 {
		page.Groups = append(page.Groups, also)
	}

	// What gave no reading is named, never left out. A query that was
	// answered and matched nothing (a limit that is not set, a metric that
	// is not scraped) is "no data"; one that was refused or timed out, and
	// Pods beyond the matcher limit, are "not read".
	var unread, empty []string
	for i := range panels {
		_, word := graphPanelWord(panels[i].Spec)
		lines, failed := 0, 0
		for j, spec := range panels[i].Specs {
			if spec.Kind == chart.Line {
				lines++
				if panels[i].States[j] != "" {
					failed++
				}
			}
		}
		for j, spec := range panels[i].Specs {
			state := panels[i].States[j]
			if state == "" || spec.Kind == chart.Line && i != main && failed == lines {
				// Its row already says so, with the gap glyph all along.
				continue
			}
			name := word + " " + panels[i].Chart.Series[j].Name
			if spec.Kind == chart.Line && lines == 1 {
				name = word
			}
			if state == graphNoData {
				empty = append(empty, name)
				continue
			}
			unread = append(unread, name+" ("+state+")")
		}
	}
	if s.PodsOmitted > 0 {
		unread = append(unread, fmt.Sprintf("%s beyond the matcher limit", graphPlural(s.PodsOmitted, "Pod")))
	}
	if t := s.tally(); t.Incomplete > 0 {
		unread = append(unread, fmt.Sprintf("%s cut at a limit of the source (s)", graphPlural(t.Incomplete, "answer")))
	}
	if len(empty) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphNoDataLabel, Role: calm.NotReadRole, Summary: empty})
	}
	if len(unread) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphNotReadLabel, Role: calm.NotReadRole, Color: pal.Warn, Summary: unread})
	}
	if len(outside) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphOutsideLabel, Role: calm.QuietRole, Summary: outside})
	}

	if s.Target.workload() {
		page.Next = append(page.Next, calm.Action{Key: "p", Label: "compare the pods"})
	}
	if len(panels) > 1 {
		_, next := graphPanelWord(panels[(main+1)%len(panels)].Spec)
		page.Next = append(page.Next, calm.Action{Key: calm.NextChartKey, Label: "next chart (" + next + ")"})
	}
	page.Next = append(page.Next, calm.Action{Key: calm.DetailKey, Label: "all charts"})
	if pg.Offers.Timeline {
		page.Next = append(page.Next, calm.Action{Key: "t", Label: "what changed"})
	}
	if pg.Offers.Grafana {
		page.Next = append(page.Next, calm.Action{Key: "D", Label: "open in Grafana"})
	}
	page.Keys = []string{"←→ time", "1 15m  2 1h  3 6h  4 24h", "r re-read", "s queries"}
	if !pg.ASCII {
		page.Keys = append(page.Keys, chartStyleHint(pg.Style))
	}
	page.Keys = append(page.Keys, "Esc back")
	return page
}

// graphCalmReport is the part of the s page that says how the short page
// is derived from the same answers: which chart is at full size and why,
// what its sentence rests on, and what a row under the chart draws.
func graphCalmReport(s *graphSnapshot) string {
	var b strings.Builder
	b.WriteString("\nTHE SHORT PAGE (the default; a shows every chart, a again returns)\n")
	b.WriteString("  One chart is drawn at full size: memory when it was at its limit in the window or the last recorded exit was OOMKilled; " +
		"else CPU when it was throttled in at least " + chart.Ratio.Format(slowpath.ThrottleFloor) + " of its periods or was at its limit; " +
		"else the first chart that was at its limit; else memory. n and Tab make the next chart the one at full size.\n")
	if s.failure() == nil {
		if panels := s.panels(graphColors{}); len(panels) > 0 {
			chosen, why := graphMainPanel(panels, s.Marks, s.Step)
			fmt.Fprintf(&b, "  Here: %s, because %s.\n", panels[chosen].Spec.Title, why)
		}
	}
	fmt.Fprintf(&b, "  The sentence at the top is read off that chart. \"Reached its limit\" counts the stretches of neighbouring samples within %s of the limit or above it. "+
		"\"Restarted after\" counts the stretches with a rise of the restart counter between their first sample and %d steps after their last. "+
		"It states order in time; it does not say that one caused the other.\n", chart.Ratio.Format(1-chart.NearLimit), graphFollow)
	b.WriteString("  A row under ALSO is another chart on one row: the highest of its lines at each time, on the scale that chart has at full size. " +
		"The restarts row marks every rise of the counter; its first mark is the first restart.\n")
	b.WriteString("  Under a moved cursor every number is the sample at the cursor, and the line over the chart says its time. " +
		"The columns after the strips are as wide as the widest of those samples, so the strips stay where they are when the cursor moves.\n")
	b.WriteString("  × is a time without a sample. NO DATA names a query that was answered and matched nothing; NOT READ one that was not answered.\n")
	return b.String()
}

// renderGraphCalm is the calm page of :graph for a size: the default page
// of the view. Every line fits Width. It reads pg.Main and writes back the
// chart it drew at full size.
func renderGraphCalm(s *graphSnapshot, pg *graphPage) []string {
	if s == nil {
		page := graphCalmWaiting(graphTitle, "", graphDefaultWindow, "Reading the declared Prometheus source once…")
		return graphCalmLines(&page, pg)
	}
	page := graphCalmPage(s, pg)
	return graphCalmLines(&page, pg)
}
