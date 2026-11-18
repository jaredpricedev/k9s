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
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	dashValueRows = 4 // title and three lines of a value tile
	dashMaxPlot   = 10
	// dashLegendRows is the most lines a tile's title and legend take.
	dashLegendRows = 3
	dashNotDrawn   = "NOT DRAWN"
)

// dashColors are the tview colors of a page, from the semantic palette.
type dashColors struct {
	graphColors
	Good, Warn, Bad, Text string
}

func dashPalette(p config.SemanticPalette) dashColors {
	return dashColors{graphColors: graphPalette(p), Good: graphHex(p.Healthy), Warn: graphHex(p.Warning), Bad: graphHex(p.Failure), Text: graphHex(p.Text)}
}

func (c *dashColors) level(l dashboard.Level) string {
	switch l {
	case dashboard.LevelGood:
		return c.Good
	case dashboard.LevelWarn:
		return c.Warn
	case dashboard.LevelBad:
		return c.Bad
	}
	return c.Text
}

// dashPage is what the page needs beyond the snapshot. Selected, First and
// Lines are settled by the render, so that keys act on what is shown.
type dashPage struct {
	Width, Height int
	ASCII         bool
	Colors        dashColors
	// Selected indexes the dashboard's panels; First is the first grid
	// line shown.
	Selected, First int
	// Zoom shows the selected panel alone, with the cursor.
	Zoom   bool
	Cursor int
	Busy   string
	// Detail shows all the detail the page had before the calm layout: the
	// grid of every panel, or the page of one query with its expression.
	// Without Zoom and Detail the page is laid out the calm way.
	Detail bool
	// Calm is the palette of the calm layout.
	Calm calm.Palette
	// Lines is the grid for this width.
	Lines []dashboard.Line
	// Mains is settled by the calm layout: the panels that can be its main
	// chart, in the dashboard's order.
	Mains []int
	// Style is the chart style of the session.
	Style chart.Style
}

type dashRender struct {
	s     *dashSnapshot
	pg    *dashPage
	width int
	c     dashColors
	lines []string
	cache map[int]dashData
}

func (r *dashRender) words(text string) string { return graphWords(text, r.pg.ASCII) }

// dashFit cuts text to a width. With plain characters the cut is marked
// "...", and the width is measured after the page's signs were replaced, so
// that a line never grows past the width by being made plain.
func dashFit(text string, width int, ascii bool) string {
	if ascii {
		return runewidth.Truncate(graphWords(text, true), max(width, 0), "...")
	}
	return graphFit(text, width)
}

func (r *dashRender) fit(text string, width int) string { return dashFit(text, width, r.pg.ASCII) }

func (r *dashRender) plain(color, text string) {
	r.lines = append(r.lines, graphPaint(color, r.fit(text, r.width)))
}

func (r *dashRender) data(index int) dashData {
	if d, ok := r.cache[index]; ok {
		return d
	}
	d := r.s.data(index, r.pg.ASCII)
	r.cache[index] = d
	return d
}

func (s *dashSnapshot) title() string {
	switch {
	case s.Dash != nil && s.Dash.Title != "":
		return s.Dash.Title
	case s.Spec.Hint != "":
		return s.Spec.Hint
	case s.Spec.UID != "":
		return s.Spec.UID
	}
	return queryTyped
}

// headline is the first line: what is drawn, the window and the step.
func (s *dashSnapshot) headline(width int, ascii bool) string {
	end := s.End.UTC().Format("15:04Z")
	if s.End.Second() != 0 {
		end = graphClock(s.End)
	}
	tail := fmt.Sprintf(" · last %s to %s · step %s", graphSpan(s.Window), end, graphSpan(s.Step))
	if width < 72 {
		tail = fmt.Sprintf(" · %s to %s · step %s", graphSpan(s.Window), end, graphSpan(s.Step))
	}
	title, prefix := safeProviderText(s.title()), s.Spec.noun()+" · "
	if runewidth.StringWidth(prefix+title+tail) > width {
		prefix = ""
	}
	return prefix + dashFit(title, width-runewidth.StringWidth(prefix)-runewidth.StringWidth(tail), ascii) + tail
}

// failure describes a page on which nothing can be drawn, or returns nil.
func (s *dashSnapshot) failure() []string {
	point := "Open :sources to see the declaration and probe it once (p)."
	explain := func(name string, st observability.Status, what string) []string {
		reason := ""
		if st.Reason != "" {
			reason = " (" + st.Reason + ")"
		}
		at := ""
		if !st.ObservedAt.IsZero() {
			at = " at " + graphClock(st.ObservedAt)
		}
		const nothing = "Nothing is drawn: a read that failed is not a reading of zero and says nothing about the workload."
		verb := "was"
		if strings.HasPrefix(what, "all ") {
			verb = "were"
		}
		switch st.State {
		case observability.StateNotConfigured:
			return []string{"No " + name + " source is declared for this context, so nothing was asked.",
				"Open :sources to see what to declare; s there suggests in-cluster services."}
		case observability.StateDenied:
			return []string{name + " DENIED " + what + reason + at + ".", nothing, point}
		case observability.StateTimeout:
			return []string{name + " TIMEOUT: " + what + " " + verb + " not answered in time" + reason + at + ".", "Nothing is drawn. Press r to ask again, or open :sources."}
		case observability.StateAbsent:
			if name == "Grafana" {
				return []string{"Grafana ABSENT: it has no dashboard with this UID, or it is not visible to these credentials" + reason + at + ".",
					"Nothing is drawn. :dashboards lists what Grafana returns."}
			}
			return []string{name + " ABSENT: the declared source was not found" + reason + at + ".", "Nothing is drawn. " + point}
		case observability.StateIncomplete:
			return []string{name + " INCOMPLETE: " + what + " " + verb + " cut" + reason + at + ".",
				"A dashboard definition is read whole or not at all, so nothing is drawn. Shift-D opens it in Grafana."}
		}
		return []string{name + " " + strings.ToUpper(string(st.State)) + ": " + what + " failed" + reason + at + ".", "Nothing is drawn. " + point}
	}
	switch {
	case s.Grafana != nil && s.Grafana.State != observability.StateComplete:
		return explain("Grafana", *s.Grafana, "the read of the dashboard definition")
	case s.ParseErr != "":
		return []string{"Grafana answered, but " + s.ParseErr + ".", "Nothing is drawn. Shift-D opens the dashboard in Grafana."}
	case s.Dash == nil:
		return []string{"Nothing has been read yet."}
	case s.Prom != nil:
		lines := explain("Prometheus", *s.Prom, "the read")
		c := s.Plan.Count(s.Dash)
		return append(lines, fmt.Sprintf("The definition has %d panels; %d have Prometheus expressions that would be drawn.", c.Panels, c.Planned))
	}
	if t := s.tally(); t.Asked > 0 && t.Failed == t.Asked {
		what := fmt.Sprintf("all %d queries", t.Asked)
		if t.Asked == 1 {
			what = "the query"
		}
		const again = "Nothing is drawn. Press r to ask again, or open :sources."
		switch {
		case t.Mixed:
			// Reads that failed in different ways are counted, not summed up
			// under the name of one of them.
			return []string{fmt.Sprintf("Prometheus answered none of the %d queries: %s.", t.Asked, t.Failures),
				"Nothing is drawn: a read that failed is not a reading of zero and says nothing about the workload.", point}
		case t.Worst.State == "":
			unsent := fmt.Sprintf("none of the %d queries could be sent", t.Asked)
			if t.Asked == 1 {
				unsent = "the query could not be sent"
			}
			return []string{fmt.Sprintf("Prometheus NOT READ: %s before the page's %s deadline ended.", unsent, graphSpan(dashDeadline)), again}
		}
		return explain("Prometheus", t.Worst, what)
	}
	return nil
}

// order numbers the panels that are not row titles.
func (s *dashSnapshot) order(index int) (at, total int) {
	for i := range s.Dash.Panels {
		if s.Dash.Panels[i].Kind == dashboard.KindRow {
			continue
		}
		total++
		if i == index {
			at = total
		}
	}
	return at, total
}

// statusParts are the facts of the status line; level 1 uses fewer words.
func (s *dashSnapshot) statusParts(first, last, level int) []string {
	t := s.tally()
	c := s.Plan.Count(s.Dash)
	drawn := s.drawn()
	parts := []string{"read " + graphClock(s.ReadAt)}
	if c.Panels > 1 || s.Spec.UID != "" {
		panels := fmt.Sprintf("%d of %d panels drawn", drawn, c.Panels)
		if level > 0 {
			panels = fmt.Sprintf("%d/%d panels drawn", drawn, c.Panels)
		}
		if level > 1 {
			panels = fmt.Sprintf("%d/%d drawn", drawn, c.Panels)
		}
		if drawn < c.Panels && level == 0 {
			panels += fmt.Sprintf(", %d not drawn (s lists why)", c.Panels-drawn)
		}
		parts = append(parts, panels)
	}
	failed := t.Failed + t.Rejected
	answered := fmt.Sprintf("%d of %d queries answered", t.Asked-failed, t.Asked)
	if level > 0 {
		answered = fmt.Sprintf("%d/%d answered", t.Asked-failed, t.Asked)
	}
	if level > 1 {
		answered = fmt.Sprintf("%d/%d ok", t.Asked-failed, t.Asked)
	}
	if t.Rejected > 0 {
		answered += fmt.Sprintf(", %d rejected", t.Rejected)
	}
	if t.Failed > 0 {
		answered += ", " + t.Failures
	}
	parts = append(parts, answered)
	switch {
	case t.Empty > 0 && level > 1:
		parts = append(parts, fmt.Sprintf("%d empty", t.Empty))
	case t.Empty > 0:
		parts = append(parts, fmt.Sprintf("%d matched no series", t.Empty))
	}
	if t.Incomplete > 0 {
		parts = append(parts, fmt.Sprintf("%d incomplete", t.Incomplete))
	}
	// Panels a limit left out: beyond what a definition keeps, beyond the
	// panels of one read, or left without a query by the query limit.
	cut := s.Dash.PanelsCut
	for i := range s.Plan.Panels {
		if strings.HasPrefix(s.Plan.Panels[i].NotDrawn, "not read: ") {
			cut++
		}
	}
	if cut > 0 {
		if level > 0 {
			parts = append(parts, fmt.Sprintf("%d over limit", cut))
		} else {
			parts = append(parts, fmt.Sprintf("%d panels over the limit of one read", cut))
		}
	}
	if first > 0 && (first > 1 || last < c.Panels) {
		if level > 0 {
			parts = append(parts, fmt.Sprintf("%d-%d/%d ↑↓", first, last, c.Panels))
		} else {
			parts = append(parts, fmt.Sprintf("showing %d-%d of %d (↑/↓)", first, last, c.Panels))
		}
	}
	return parts
}

func dashFooter(width int, zoom, single, query, ascii bool, style chart.Style) string {
	var forms []string
	switch {
	case zoom && single:
		// The page of one query with its expression is its all-detail page:
		// a returns to the calm layout.
		forms = []string{
			"←/→ cursor · Home/End · 1 15m  2 1h  3 6h  4 24h · r re-read · s detail · Shift-D Grafana · e edit · a calm layout · Esc back",
			"←/→ cursor · 1 15m 2 1h 3 6h 4 24h · r re-read · s detail · e edit · a calm layout · Esc back",
			"←/→ cursor · 1-4 window · r re-read · s detail · e edit · a calm · Esc",
			"←/→ cursor · 1-4 window · r read · e edit · a calm",
		}
	case zoom:
		forms = []string{
			"←/→ cursor · ↑/↓ panel · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Shift-D Grafana · Esc grid",
			"←/→ cursor · ↑/↓ panel · 1-4 window · r re-read · s queries · Esc grid",
			"←/→ cursor · ↑/↓ panel · 1-4 window · r read · Esc grid",
			// Short enough for 60 columns when the arrows are written out.
			"←/→ cursor · ↑/↓ panel · 1-4 · r · Esc grid",
		}
	default:
		// The grid of every panel is the all-detail page of a dashboard: a
		// returns to the calm layout.
		forms = []string{
			"←/→ ↑/↓ panel · Enter zoom · 1 15m  2 1h  3 6h  4 24h · r re-read · s queries · Shift-D Grafana · a calm layout · Esc back",
			"←/→ ↑/↓ panel · Enter zoom · 1-4 window · r re-read · s queries · Shift-D Grafana · a calm layout · Esc",
			"←/→ ↑/↓ panel · Enter zoom · 1-4 window · r re-read · s queries · a calm · Esc",
			"←/→ panel · Enter zoom · 1-4 window · s queries · a calm",
			// Short enough for 60 columns when the arrows are written out.
			"Tab panel · Enter zoom · 1-4 window · s queries · a calm",
		}
	}
	if !query {
		for i := range forms {
			forms[i] = strings.Replace(forms[i], " · e edit", "", 1)
		}
	}
	return chartStyleFit(forms, style, width, ascii, "1-4 window · r read · Esc")
}

// renderDash lays the whole page out for a size. Every line fits Width.
// The calm layout is the default; Zoom shows one panel alone and Detail
// the page as it was before the calm layout.
func renderDash(s *dashSnapshot, pg *dashPage) []string {
	r := &dashRender{s: s, pg: pg, width: max(pg.Width, 20), c: pg.Colors, cache: map[int]dashData{}}
	if s == nil {
		return r.calmWords(calm.Title{View: dashViewTitle, Busy: "reading…"}, calm.Verdict{Sentences: []string{"Reading once…"}}, nil, []string{calmKeyBack})
	}
	if failure := s.failure(); failure != nil {
		return r.calmFailure(failure)
	}
	// What the keys chose, before the grid settles on a panel of its own.
	chosen := pg.Selected
	r.settle()
	if pg.Selected < 0 {
		return r.calmWords(r.calmTitle(), calm.Verdict{Kind: calm.NothingFound, Sentences: []string{"The dashboard definition has no panels."}},
			[]calm.Action{{Key: calmGrafanaKeyName, Label: calmNextGrafana}}, []string{calmKeyBack})
	}
	switch one := s.Spec.isQuery() && !s.Spec.Board; {
	case pg.Zoom, pg.Detail && one:
		r.lines = append(r.lines, graphBold(r.c.Title, r.fit(s.headline(r.width, pg.ASCII), r.width)))
		r.zoom()
	case pg.Detail:
		r.lines = append(r.lines, graphBold(r.c.Title, r.fit(s.headline(r.width, pg.ASCII), r.width)))
		r.grid()
	default:
		r.calm(chosen)
	}
	return r.lines
}

// source names where the page's reads went, for a page that drew nothing.
func (s *dashSnapshot) source() string {
	switch {
	case s.Grafana != nil && s.Grafana.State != observability.StateComplete && s.Grafana.Source != "":
		return "grafana via " + s.Grafana.Source
	case s.Grafana != nil && s.Grafana.State != observability.StateComplete:
		return "grafana"
	case s.Prom != nil:
		return "prometheus: " + s.Prom.Source
	}
	return "prometheus via " + s.Source
}

// reason is why a panel has no plot in this read: what the plan ruled out,
// or what its answers amounted to. Empty for a panel with a line.
func (r *dashRender) reason(index int) string {
	if text := r.s.Plan.Panels[index].NotDrawn; text != "" || r.s.Dash.Panels[index].Kind == dashboard.KindRow {
		return text
	}
	if data := r.data(index); len(data.Lines) == 0 {
		if data.Problem != "" {
			return data.Problem
		}
		return "nothing was read"
	}
	return ""
}

// settle lays the grid out and makes the selection a panel that exists and
// is not a row title. A panel whose queries drew nothing takes one line
// with the reason, like one the definition ruled out: an empty plot would
// only look like a reading.
func (r *dashRender) settle() {
	pg, d := r.pg, r.s.Dash
	shown := r.s.Plan
	shown.Panels = append([]dashboard.PanelPlan(nil), r.s.Plan.Panels...)
	for i := range shown.Panels {
		shown.Panels[i].NotDrawn = r.reason(i)
	}
	pg.Lines = dashboard.Flow(d, &shown, r.width)
	if pg.Selected < 0 || pg.Selected >= len(d.Panels) || d.Panels[pg.Selected].Kind == dashboard.KindRow {
		pg.Selected = dashboard.Advance(pg.Lines, -1, 0)
	}
}

func (r *dashRender) frame(width int) chart.Frame {
	return chart.Frame{Start: r.s.Start, End: r.s.End, Step: r.s.Step, Width: max(width, 8)}
}

// chartPanel builds the plot of a panel from its lines. Keys and colors
// are assigned here so the legend and the plot always agree.
func (r *dashRender) chartPanel(index int) chart.Panel {
	data := r.data(index)
	return r.chartPanelOf(index, &data)
}

// chartPanelOf is the plot of a panel from the given lines: all of them,
// or the ones the calm layout keeps for its main chart.
func (r *dashRender) chartPanelOf(index int, data *dashData) chart.Panel {
	p := &r.s.Dash.Panels[index]
	panel := chart.Panel{Unit: p.Unit.Chart, Empty: r.words(data.Problem)}
	for k := range data.Lines {
		series := chart.Series{Name: data.Lines[k].Name, Kind: chart.Line, Points: data.Lines[k].Points, Stroke: chart.Solid}
		if len(r.c.Data) > 0 {
			series.Color = r.c.Data[data.Lines[k].Tint%len(r.c.Data)]
		}
		if len(data.Lines) > 1 {
			// A line's letter ends it and repeats along it, so that lines are
			// told apart without color. The letter is the line's place among
			// all lines of its panel, so it is the same on every page.
			series.Key = rune('a' + data.Lines[k].Slot%26)
			series.Marker = series.Key
			series.MarkerEvery = max(20, 10*len(data.Lines))
		}
		panel.Series = append(panel.Series, series)
	}
	// A declared maximum is the top of the axis while the data stays under it.
	if top, ok := panel.Max(); ok && p.HasMax && p.Max*p.Unit.Scale >= top && p.Max > 0 {
		panel.Ceiling = p.Max * p.Unit.Scale
	}
	return panel
}

// legend names the lines of a panel with their values at a time; a zero
// time reads each line's last sample.
func (r *dashRender) legend(index int, panel *chart.Panel, f chart.Frame, at time.Time) []chart.Entry {
	data := r.data(index)
	return r.legendOf(index, &data, panel, f, at)
}

// legendOf is the legend of the plot chartPanelOf built from the same
// lines.
func (r *dashRender) legendOf(index int, data *dashData, panel *chart.Panel, f chart.Frame, at time.Time) []chart.Entry {
	p := &r.s.Dash.Panels[index]
	entries := make([]chart.Entry, 0, len(data.Lines)+1)
	if at.IsZero() {
		// A chart drawn from some of its queries says first which ones are
		// missing: a limit line alone would look like usage of nothing. The
		// zoomed panel states each in full.
		for _, state := range data.States {
			entries = append(entries, chart.Entry{Name: "query " + dashShortState(state)})
		}
		for _, state := range data.Incomplete {
			ref, _, _ := strings.Cut(state, ": ")
			entries = append(entries, chart.Entry{Name: "query " + ref + " incomplete"})
		}
	}
	for k := range data.Lines {
		series := &panel.Series[k]
		entry := chart.Entry{Name: data.Lines[k].Name, Color: series.Color}
		if series.Key != 0 {
			entry.Glyph = string(series.Key)
		}
		value, peak := r.lineValue(p, series.Points, f, at)
		entry.Value = strings.TrimSpace(value + " " + peak)
		entries = append(entries, entry)
	}
	if data.Cut > 0 {
		entries = append(entries, chart.Entry{Name: fmt.Sprintf("+%d series not drawn", data.Cut)})
	}
	return entries
}

// lineValue words one line at a time; a zero time reads its last sample.
// peak is what stands beside that value so that it does not hide a burst:
// "peak 47.9s", or empty.
func (r *dashRender) lineValue(p *dashboard.Panel, points []chart.Point, f chart.Frame, at time.Time) (value, peak string) {
	unit := p.Unit.Chart
	end := points[len(points)-1]
	switch sample, ok := f.ValueAt(&chart.Series{Points: points}, at); {
	case at.IsZero():
		// The grid reads each line where it ends. A line that stopped
		// before the window did says when, and one that stood well above
		// where it ends says so: the last sample alone would hide a burst.
		top := end.V
		for _, point := range points {
			top = math.Max(top, point.V)
		}
		value = unit.Format(end.V)
		if f.End.Sub(end.T) > f.Step+f.Step/2 {
			value = r.words(graphNoSample) + " last " + end.T.UTC().Format("15:04")
			if top > 0 {
				peak = "peak " + unit.Format(top)
			}
		} else if top > 2*end.V && unit.Format(top) != value {
			peak = "peak " + unit.Format(top)
		}
	case ok:
		value = unit.Format(sample)
	case end.T.Before(at):
		value = r.words(graphNoSample) + " last " + end.T.UTC().Format("15:04")
	case points[0].T.After(at):
		value = r.words(graphNoSample) + " first " + points[0].T.UTC().Format("15:04")
	default:
		value = r.words(graphNoSample)
	}
	return value, peak
}

// dashShortState shortens "A: denied (HTTP 403): nothing was read" to
// "A denied" for a legend; the zoomed panel and the s page keep every word.
func dashShortState(state string) string {
	ref, rest, _ := strings.Cut(state, ": ")
	for _, stop := range []string{" (", ": "} {
		if at := strings.Index(rest, stop); at >= 0 {
			rest = rest[:at]
		}
	}
	return ref + " " + rest
}

func (r *dashRender) titleOf(index int) string {
	p := &r.s.Dash.Panels[index]
	title := safeProviderText(p.Title)
	if !p.Unit.Known && p.Unit.ID != "" {
		title += " · " + safeProviderText(p.Unit.Word())
	}
	return r.words(title)
}

func dashMarker(selected bool) string {
	if selected {
		return "> "
	}
	return "  "
}

// chartTile draws one line chart: legend lines, the plot and its time axis.
func (r *dashRender) chartTile(index, width, legendRows, plotRows, gutter int) []string {
	selected := index == r.pg.Selected
	panel := r.chartPanel(index)
	f := r.frame(width - gutter - 2)
	title := dashMarker(selected) + r.titleOf(index)
	legend := chart.Legend(title, r.legend(index, &panel, f, time.Time{}), width, legendRows, tview.Escape)
	color := r.c.Text
	if selected {
		color = r.c.Title
	}
	legend[0] = strings.Replace(legend[0], tview.Escape(runewidth.Truncate(title, width, "…")), graphBold(color, runewidth.Truncate(title, width, "…")), 1)
	for len(legend) < legendRows {
		legend = append(legend, "")
	}
	options := chart.Options{ASCII: r.pg.ASCII, Cursor: -1, AxisColor: r.c.Axis, CursorColor: r.c.Cursor, Style: r.pg.Style}
	legend = append(legend, panel.Render(f, plotRows, gutter, options)...)
	return append(legend, chart.Axis(f, gutter, nil, options)...)
}

func (r *dashRender) legendRows(index, width int) int {
	panel := r.chartPanel(index)
	title := dashMarker(false) + r.titleOf(index)
	return len(chart.Legend(title, r.legend(index, &panel, r.frame(width), time.Time{}), width, dashLegendRows, nil))
}

// bar draws a share of a width as a thin line: heavy for the part that is
// filled, light for the rest.
func (r *dashRender) bar(share float64, width int, color string) string {
	if width < 1 {
		return ""
	}
	full, rest := "━", "─"
	if r.pg.ASCII {
		full, rest = "=", "-"
	}
	filled := min(max(int(math.Round(share*float64(width))), 0), width)
	return graphPaint(color, strings.Repeat(full, filled)) + graphPaint(r.c.Axis, strings.Repeat(rest, width-filled))
}

// span is the range a gauge or bar measures against.
func (r *dashRender) span(index int) (low, high float64) {
	p := &r.s.Dash.Panels[index]
	data := r.data(index)
	if p.HasMin {
		low = p.Min * p.Unit.Scale
	}
	switch {
	case p.HasMax && p.Max*p.Unit.Scale > low:
		return low, p.Max * p.Unit.Scale
	case p.Unit.Chart == chart.Ratio && low < 1:
		return low, 1
	}
	for k := range data.Lines {
		if v, ok := dashValueOf(p, &data.Lines[k]); ok {
			high = math.Max(high, v.Value)
		}
	}
	if high <= low {
		high = low + 1
	}
	return low, high
}

// valueTile draws a stat, gauge or bar gauge in dashValueRows lines.
func (r *dashRender) valueTile(index, width int) []string {
	p := &r.s.Dash.Panels[index]
	data := r.data(index)
	selected := index == r.pg.Selected
	color := r.c.Text
	if selected {
		color = r.c.Title
	}
	out := []string{graphBold(color, r.fit(dashMarker(selected)+r.titleOf(index), width))}
	inner := width - 2
	pad := func() []string {
		for len(out) < dashValueRows {
			out = append(out, "")
		}
		return out[:dashValueRows]
	}
	if len(data.Lines) == 0 {
		for _, line := range graphWrap(r.words(data.Problem), inner, "") {
			out = append(out, "  "+graphPaint(r.c.Muted, r.fit(line, inner)))
		}
		return pad()
	}
	low, high := r.span(index)
	share := func(v float64) float64 { return (v - low) / (high - low) }
	if len(data.Lines) == 1 && p.Kind != dashboard.KindBarGauge {
		line := &data.Lines[0]
		v, _ := dashValueOf(p, line)
		value := p.Unit.Chart.Format(v.Value)
		tail := ""
		if v.Word != "" {
			tail += " " + v.Word
		}
		if v.HasStep {
			tail += "  " + dashStepText(p, v.Step)
		}
		level := r.c.level(v.Step.Level)
		// A gauge has no line for the window's minimum and maximum. When the
		// window held a value in another step than the one shown, the value
		// line says so: "3.8% green" alone would hide a burst into red.
		peak := ""
		if p.Kind == dashboard.KindGauge && v.HasMaxLvl {
			peak = " · max " + p.Unit.Chart.Format(v.Max) + " " + v.MaxStep.Word
			if runewidth.StringWidth(value+tail+peak) > inner {
				// The step's word matters more than where the step starts.
				tail = ""
				if v.Word != "" {
					tail = " " + v.Word
				}
				tail += " " + v.Step.Word
			}
		}
		used := runewidth.StringWidth(value)
		text := "  " + graphBold(level, r.fit(value, inner)) + graphPaint(level, r.fit(tail, inner-used))
		used += runewidth.StringWidth(r.fit(tail, inner-used))
		if peak != "" {
			text += graphPaint(r.c.level(v.MaxStep.Level), r.fit(peak, inner-used))
		}
		out = append(out, text)
		spark := chart.Spark(chart.SparkValues(line.Points, r.frame(inner)), chart.SparkOptions{ASCII: r.pg.ASCII, Style: chart.SparkStyleOf(r.pg.Style), Color: r.c.Data[0], GapColor: r.c.Axis})
		if p.Kind == dashboard.KindGauge {
			of := " of " + p.Unit.Chart.Format(high)
			out = append(out, "  "+r.bar(share(v.Value), inner-runewidth.StringWidth(of), level)+graphPaint(r.c.Muted, of), "  "+spark)
			return pad()
		}
		low := r.fit(fmt.Sprintf("min %s  ", p.Unit.Chart.Format(v.Min)), inner)
		high, highColor := "max "+p.Unit.Chart.Format(v.Max), r.c.Muted
		if v.HasMaxLvl {
			// The window held a value in another step than the one shown:
			// the word says which, and the color repeats it.
			high += " (" + v.MaxStep.Word + ")"
			highColor = r.c.level(v.MaxStep.Level)
		}
		out = append(out, "  "+spark, "  "+graphPaint(r.c.Muted, low)+graphPaint(highColor, r.fit(high, inner-runewidth.StringWidth(low))))
		return pad()
	}
	return append(out, r.valueRows(index, inner)...)[:dashValueRows]
}

// valueRows draws one row per series of a value tile: name, bar and value.
func (r *dashRender) valueRows(index, inner int) []string {
	p := &r.s.Dash.Panels[index]
	data := r.data(index)
	low, high := r.span(index)
	share := func(v float64) float64 { return (v - low) / (high - low) }
	var out []string
	rows := dashValueRows - 1
	shown := len(data.Lines)
	if shown > rows {
		shown = rows - 1
	}
	nameWidth, valueWidth := 0, 0
	values := make([]dashValue, shown)
	for k := range shown {
		values[k], _ = dashValueOf(p, &data.Lines[k])
		nameWidth = max(nameWidth, runewidth.StringWidth(data.Lines[k].Name))
		valueWidth = max(valueWidth, runewidth.StringWidth(p.Unit.Chart.Format(values[k].Value)))
	}
	nameWidth = min(nameWidth, max(inner/3, 6))
	barWidth := inner - nameWidth - valueWidth - 2
	for k := range shown {
		level := r.c.level(values[k].Step.Level)
		row := "  " + tview.Escape(timelineCell(data.Lines[k].Name, nameWidth)) + " "
		if barWidth >= 6 {
			row += r.bar(share(values[k].Value), barWidth, level) + " "
		}
		value := p.Unit.Chart.Format(values[k].Value)
		out = append(out, row+graphPaint(level, strings.Repeat(" ", valueWidth-runewidth.StringWidth(value))+value))
	}
	if more := len(data.Lines) - shown + data.Cut; more > 0 {
		out = append(out, "  "+graphPaint(r.c.Muted, r.fit(fmt.Sprintf("+%d more · Enter shows all", more), inner)))
	}
	for len(out) < dashValueRows {
		out = append(out, "")
	}
	return out
}

func (r *dashRender) noteLine(index int) string {
	selected := index == r.pg.Selected
	head := dashMarker(selected) + r.titleOf(index) + " · "
	head = r.fit(head, r.width-len(dashNotDrawn)-2)
	reason := r.fit(": "+safeProviderText(r.reason(index)), r.width-runewidth.StringWidth(head)-len(dashNotDrawn))
	color := r.c.Muted
	if selected {
		color = r.c.Title
	}
	return graphPaint(color, head) + graphPaint(r.c.Request, dashNotDrawn) + graphPaint(r.c.Muted, reason)
}

func (r *dashRender) sectionLine(index int) string {
	rule := "─"
	if r.pg.ASCII {
		rule = "-"
	}
	title := r.fit(safeProviderText(r.s.Dash.Panels[index].Title), r.width-8)
	text := strings.Repeat(rule, 2) + " " + title + " "
	return graphPaint(r.c.Axis, text+strings.Repeat(rule, max(r.width-runewidth.StringWidth(text), 0)))
}

// dashJoin sets tiles side by side at their slots' columns.
func dashJoin(tiles [][]string, slots []dashboard.Slot) []string {
	rows := 0
	for _, tile := range tiles {
		rows = max(rows, len(tile))
	}
	out := make([]string, rows)
	for row := range rows {
		var b strings.Builder
		col := 0
		for k, tile := range tiles {
			if row >= len(tile) || tile[row] == "" {
				continue
			}
			b.WriteString(strings.Repeat(" ", max(slots[k].X-col, 0)))
			b.WriteString(tile[row])
			col = max(slots[k].X, col) + tview.TaggedStringWidth(tile[row])
		}
		out[row] = b.String()
	}
	return out
}

// dashGrid is the sizing of one page of the grid: the label gutter every
// chart shares, the legend and plot rows of each line, and which lines are
// shown.
type dashGrid struct {
	gutter                  int
	legends, plots, heights []int
	start, count            int
}

// measure sizes every line of the grid at the height it would like.
func (r *dashRender) measure(plotMin int) *dashGrid {
	lines := r.pg.Lines
	g := &dashGrid{gutter: 1, legends: make([]int, len(lines)), plots: make([]int, len(lines)), heights: make([]int, len(lines))}
	for i := range lines {
		switch lines[i].Kind {
		case dashboard.TileValue:
			g.heights[i] = dashValueRows
		case dashboard.TileChart:
			for _, slot := range lines[i].Slots {
				// One gutter for every chart, so that all plots start in one
				// column.
				panel := r.chartPanel(slot.Panel)
				g.gutter = max(g.gutter, panel.GutterWidth())
				g.legends[i] = max(g.legends[i], r.legendRows(slot.Panel, slot.Width))
			}
			g.heights[i] = g.legends[i] + plotMin + 2
		default:
			g.heights[i] = 1
		}
	}
	return g
}

// place chooses the lines that are shown and gives every row of the page
// to a chart.
func (r *dashRender) place(g *dashGrid, budget, plotMin int) {
	lines, pg := r.pg.Lines, r.pg
	selected, _, _ := dashboard.LineOf(lines, pg.Selected)
	// A row title is shown with the first line under it.
	first := pg.First
	if selected > 0 && lines[selected-1].Kind == dashboard.TileSection && first >= selected {
		first = selected - 1
	}
	g.start, g.count = dashboard.Visible(g.heights, first, selected, budget)
	if g.start > 0 && g.start == selected && lines[g.start-1].Kind == dashboard.TileSection && g.heights[g.start]+1 <= budget {
		g.start, g.count = dashboard.Visible(g.heights, g.start-1, selected, budget)
	}
	pg.First = g.start
	used, charts := 0, 0
	for i := g.start; i < g.start+g.count; i++ {
		used += g.heights[i]
		if lines[i].Kind == dashboard.TileChart {
			g.plots[i] = plotMin
			charts++
		}
	}
	// squeeze fits a chart line into fewer rows than it would like: the
	// legend gives way first (it then counts what it leaves out), then
	// the plot.
	squeeze := func(i, room int) int {
		g.legends[i] = min(g.legends[i], max(room-4, 1))
		g.plots[i] = min(max(room-g.legends[i]-2, 1), dashMaxPlot)
		return g.legends[i] + g.plots[i] + 2
	}
	switch next := g.start + g.count; {
	case used > budget && g.count == 1 && lines[g.start].Kind == dashboard.TileChart:
		// A page shorter than one chart still shows it whole.
		used = squeeze(g.start, budget)
	case next < len(lines) && lines[next].Kind == dashboard.TileChart && budget-used >= 5:
		// Room that is left under the last whole line is given to the next
		// chart rather than left blank.
		used += squeeze(next, budget-used)
		g.count++
		charts++
	}
	for spare := budget - used; spare > 0 && charts > 0; {
		grew := false
		for i := g.start; i < g.start+g.count && spare > 0; i++ {
			if lines[i].Kind == dashboard.TileChart && g.plots[i] < dashMaxPlot {
				g.plots[i]++
				spare--
				grew = true
			}
		}
		if !grew {
			break
		}
	}
}

// statusLine says how the read went and which panels are on the page, with
// fewer words on a narrower page and never fewer facts.
func (r *dashRender) statusLine(g *dashGrid) string {
	firstPanel, lastPanel := 0, 0
	for i := g.start; i < g.start+g.count; i++ {
		if r.pg.Lines[i].Kind == dashboard.TileSection {
			continue
		}
		for _, slot := range r.pg.Lines[i].Slots {
			at, _ := r.s.order(slot.Panel)
			if firstPanel == 0 {
				firstPanel = at
			}
			lastPanel = at
		}
	}
	var status []string
	for level := range 3 {
		parts := r.s.statusParts(firstPanel, lastPanel, level)
		if r.pg.Busy != "" {
			parts[0] = r.pg.Busy + " · shown: " + parts[0]
		}
		for i := range parts {
			parts[i] = r.words(parts[i])
		}
		if status = graphJoinLines(parts, " · ", r.width); len(status) == 1 {
			break
		}
	}
	return strings.Join(status, " · ")
}

// variableLine names the selection and what the variables stand for. The
// variables matter more than the words around them.
func (r *dashRender) variableLine(vars []string) string {
	text := strings.Join(vars, " · ")
	if text == "" {
		return "for " + r.s.Label + " · no expression on this page uses a variable"
	}
	for _, lead := range []string{"for " + r.s.Label + " · ", r.s.Label + " · "} {
		if r.s.Label != "" && runewidth.StringWidth(lead+text) <= r.width {
			return lead + text
		}
	}
	return text
}

// tiles draws one line of the grid.
func (r *dashRender) tiles(g *dashGrid, i int) []string {
	line := &r.pg.Lines[i]
	switch line.Kind {
	case dashboard.TileSection:
		return []string{r.sectionLine(line.Slots[0].Panel)}
	case dashboard.TileNote:
		return []string{r.noteLine(line.Slots[0].Panel)}
	}
	tiles := make([][]string, len(line.Slots))
	for k, slot := range line.Slots {
		if line.Kind == dashboard.TileValue {
			tiles[k] = r.valueTile(slot.Panel, slot.Width)
		} else {
			tiles[k] = r.chartTile(slot.Panel, slot.Width, g.legends[i], g.plots[i], g.gutter)
		}
	}
	return dashJoin(tiles, line.Slots)
}

// grid draws the scrolling page of tiles.
func (r *dashRender) grid() {
	s, pg := r.s, r.pg
	vars := s.variableParts()
	header := 2
	if pg.Height >= 24 && (len(vars) > 0 || s.Label != "") {
		header = 3
	}
	budget := pg.Height - header - 2
	plotMin := 4
	if budget >= 22 {
		plotMin = 6
	}
	g := r.measure(plotMin)
	r.place(g, budget, plotMin)
	r.plain(r.c.Muted, r.statusLine(g))
	if header == 3 {
		r.plain(r.c.Muted, r.variableLine(vars))
	}
	for i := g.start; i < g.start+g.count; i++ {
		r.lines = append(r.lines, r.tiles(g, i)...)
	}
	// The readout and the keys are never pushed off a very short page.
	if len(r.lines) > pg.Height-2 {
		r.lines = r.lines[:max(pg.Height-2, 1)]
	}
	for len(r.lines) < pg.Height-2 {
		r.lines = append(r.lines, "")
	}
	r.plain("", r.readout())
	r.plain(r.c.Muted, dashFooter(r.width, false, len(s.Dash.Panels) == 1, s.Spec.isQuery(), pg.ASCII, pg.Style))
}

// readout says in one line what the selected panel is and asks.
func (r *dashRender) readout() string {
	index := r.pg.Selected
	p, pp := &r.s.Dash.Panels[index], &r.s.Plan.Panels[index]
	at, total := r.s.order(index)
	text := fmt.Sprintf("Panel %d of %d · %s", at, total, safeProviderText(p.Type))
	if reason := r.reason(index); reason != "" {
		return text + " · not drawn: " + safeProviderText(reason)
	}
	for j := range pp.Queries {
		if pp.Queries[j].Sent() {
			return text + " · " + pp.Queries[j].RefID + " " + strings.Join(strings.Fields(pp.Queries[j].Expr.String()), " ")
		}
	}
	return text
}

// queryDetail lists what a panel asked and what came back, as plain lines.
func (s *dashSnapshot) queryDetail(index int) []string {
	p, pp := &s.Dash.Panels[index], &s.Plan.Panels[index]
	var out []string
	for j := range pp.Queries {
		q := &pp.Queries[j]
		if !q.Sent() {
			out = append(out, q.RefID+"  "+safeProviderText(q.Authored), "   not sent: "+safeProviderText(q.Skip))
			continue
		}
		res := s.Results[dashKey(index, q.RefID)]
		out = append(out, q.RefID+"  "+strings.Join(strings.Fields(q.Expr.String()), " "))
		state := string(res.State)
		if res.State == "" {
			state = dashNotRead
		}
		if res.Reason != "" {
			state += " (" + res.Reason + ")"
		}
		line := fmt.Sprintf("   %s · %d series · %d samples", state, len(res.Series), graphSamples(&res))
		if res.Error != "" {
			line = "   " + state + " · Prometheus said: " + safeProviderText(res.Error)
		} else if res.OK() && graphSamples(&res) == 0 {
			line += " · no series matched: missing data, not a zero"
		}
		if q.Legend != "" {
			line += " · legend " + safeProviderText(q.Legend)
		}
		out = append(out, line)
	}
	if len(pp.Queries) == 0 {
		for j := range p.Targets {
			if expr := strings.Join(strings.Fields(p.Targets[j].Expr), " "); expr != "" {
				out = append(out, p.Targets[j].RefID+"  "+safeProviderText(expr), "   not sent")
			}
		}
	}
	for _, note := range pp.Notes {
		out = append(out, "Note: "+safeProviderText(note))
	}
	if p.Description != "" {
		out = append(out, "About: "+safeProviderText(p.Description))
	}
	return out
}

// zoomDetail lists the queries, notes and variables of the zoomed panel,
// wrapped to the page.
func (r *dashRender) zoomDetail(index int) []string {
	var detail []string
	for _, line := range r.s.queryDetail(index) {
		detail = append(detail, graphWrap(r.words(line), r.width, "   ")...)
	}
	if vars := r.s.variableParts(); len(vars) > 0 {
		detail = append(detail, graphWrap(r.words("Variables: "+strings.Join(vars, " · ")), r.width, "   ")...)
	}
	return detail
}

// clip keeps as many detail lines as there is room for and says where the
// rest is.
func (r *dashRender) clip(detail []string, room int) []string {
	if len(detail) <= room {
		return detail
	}
	room = max(room, 1)
	return append(append([]string(nil), detail[:room-1]...), r.words("… s shows every query and note"))
}

// finish pads the page and writes the key line last.
func (r *dashRender) finish(footer string) {
	for len(r.lines) < r.pg.Height-1 {
		r.lines = append(r.lines, "")
	}
	r.plain(r.c.Muted, footer)
}

// zoomWords is the zoomed page of a panel without a line: the reason and
// what can be done, not an empty plot.
func (r *dashRender) zoomWords(problem, advice string, detail []string, footer string) {
	// Without a plot there is no cursor to move.
	for _, keys := range []string{"←/→ cursor · Home/End · ", "←/→ cursor · "} {
		footer = strings.TrimPrefix(footer, r.words(keys))
	}
	r.lines = append(r.lines, "")
	for _, line := range graphWrap(r.words(dashNotDrawn+": "+problem+"."), r.width, "") {
		r.plain(r.c.Request, line)
	}
	for _, line := range graphWrap(r.words(advice), r.width, "") {
		r.plain("", line)
	}
	r.lines = append(r.lines, "")
	for _, line := range r.clip(detail, r.pg.Height-len(r.lines)-2) {
		r.plain(r.c.Muted, line)
	}
	r.finish(footer)
}

// zoomTop is what stands above the plot of a zoomed panel: the value of a
// stat or gauge in words, the legend at the cursor, and what a query of the
// panel did not return.
func (r *dashRender) zoomTop(index int, panel *chart.Panel, f chart.Frame, when time.Time) []string {
	p := &r.s.Dash.Panels[index]
	data := r.data(index)
	var top []string
	if p.Kind.Value() && len(data.Lines) == 1 {
		if v, ok := dashValueOf(p, &data.Lines[0]); ok {
			word := "last"
			if v.Word != "" {
				word = v.Word
			}
			text := word + " " + p.Unit.Chart.Format(v.Value)
			if v.HasStep {
				text += " · threshold " + dashStepText(p, v.Step)
			}
			text += fmt.Sprintf(" · window min %s max %s", p.Unit.Chart.Format(v.Min), p.Unit.Chart.Format(v.Max))
			if v.HasMaxLvl {
				text += " (" + v.MaxStep.Word + ")"
			}
			top = append(top, graphPaint(r.c.level(v.Step.Level), r.fit(text, r.width)))
		}
	}
	if entries := r.legend(index, panel, f, when); len(entries) > 0 {
		legend := chart.Legend("", entries, r.width, dashLegendRows, tview.Escape)
		for i := range legend {
			legend[i] = strings.TrimPrefix(legend[i], "  ")
		}
		top = append(top, legend...)
	}
	for _, text := range data.States {
		top = append(top, graphPaint(r.c.Request, r.fit("query "+text, r.width)))
	}
	for _, text := range data.Incomplete {
		top = append(top, graphPaint(r.c.Request, r.fit("INCOMPLETE "+text, r.width)))
	}
	return top
}

// zoom draws the selected panel alone, with the cursor and its queries.
func (r *dashRender) zoom() {
	s, pg := r.s, r.pg
	index := pg.Selected
	p := &s.Dash.Panels[index]
	at, total := s.order(index)
	single := total == 1
	title := r.titleOf(index)
	if !single {
		title = fmt.Sprintf("Panel %d of %d · %s · ", at, total, safeProviderText(p.Type)) + title
	}
	if word := p.Unit.Word(); word != "" && p.Unit.Known {
		title += " · " + word
	}
	if pg.Busy != "" {
		title = pg.Busy + " · shown: " + title
	}
	r.lines = append(r.lines, graphBold(r.c.Text, r.fit(title, r.width)))
	footer := dashFooter(r.width, true, single, s.Spec.isQuery(), pg.ASCII, pg.Style)
	if !pg.Detail {
		// Zoomed from the calm layout, Esc returns to it: there is no grid.
		footer = strings.Replace(footer, "Esc grid", "Esc back", 1)
	}
	detail := r.zoomDetail(index)
	data := r.data(index)
	if problem, advice := r.undrawn(index, &data); problem != "" {
		r.zoomWords(problem, advice, detail, footer)
		return
	}

	panel := r.chartPanel(index)
	gutter := panel.GutterWidth()
	f := r.frame(r.width - gutter - 2)
	cursor := min(max(pg.Cursor, 0), f.Steps())
	when := f.TimeAt(cursor)
	options := chart.Options{ASCII: pg.ASCII, Cursor: cursor, AxisColor: r.c.Axis, CursorColor: r.c.Cursor, Style: pg.Style}
	top := r.zoomTop(index, &panel, f, when)
	r.lines = append(r.lines, top...)
	// headline, panel line, what is above the plot, axis, cursor, keys.
	// The plot keeps at least half of the room; the detail takes the rest
	// and says when it was cut.
	room := pg.Height - 2 - len(top) - 2 - 1 - 1
	plotMin := max(4, room/2)
	detail = r.clip(detail, max(room-plotMin, 2))
	rows := min(max(room-len(detail), 3), 16)
	r.lines = append(r.lines, panel.Render(f, rows, gutter, options)...)
	r.lines = append(r.lines, chart.Axis(f, gutter, nil, options)...)
	readout := "Cursor " + graphClock(when)
	switch behind := s.End.Sub(when); {
	case behind <= 0:
		readout += " (window end)"
	case r.width < 72:
		readout += ", -" + graphAgo(behind)
	default:
		readout += ", " + graphAgo(behind) + " before window end"
	}
	// The reminder is dropped whole on a narrow page, never cut.
	if note := " · a value is the sample at the cursor, not an interpolation"; runewidth.StringWidth(readout+note) <= r.width {
		readout += note
	}
	r.plain("", readout)
	for _, line := range detail {
		r.plain(r.c.Muted, line)
	}
	r.finish(footer)
}

// undrawn says why the zoomed panel has no plot and what can be done, or
// returns "" for a panel with at least one line.
func (r *dashRender) undrawn(index int, data *dashData) (problem, advice string) {
	p, pp := &r.s.Dash.Panels[index], &r.s.Plan.Panels[index]
	edit := ""
	if r.s.Spec.isQuery() {
		edit = " Press e to edit the expression."
	}
	switch {
	case pp.NotDrawn != "" && p.Kind == dashboard.KindUnsupported:
		return safeProviderText(pp.NotDrawn), "Drawn here: " + dashboard.DrawnTypes + ", when their query is a Prometheus expression. Shift-D opens the dashboard in Grafana."
	case pp.NotDrawn != "":
		return safeProviderText(pp.NotDrawn), "Nothing was sent for this panel." + edit
	case len(data.Lines) > 0:
		return "", ""
	case strings.HasPrefix(data.Problem, "expression failed"):
		return data.Problem, "Prometheus rejected the expression as it was sent, in its own words above. Nothing was read." + edit
	case strings.HasPrefix(data.Problem, "no series matched"):
		advice = "The query answered and matched nothing. That is missing data, not a zero: the metric may not be scraped here, or a label may have another name."
		if q := r.s.Spec.Query; q != nil && q.Name == "" && observability.QueryName(strings.TrimSpace(q.Expr)) {
			var names []string
			for _, named := range dashboard.BuiltinQueries() {
				names = append(names, named.Name)
			}
			advice += " If a named query was meant: " + strings.Join(names, ", ") + "."
		}
		return data.Problem, advice + edit
	}
	return data.Problem, "Nothing is drawn: a read that failed is not a reading of zero. :sources shows the declaration; r reads again."
}

// dashReport is the s page: every panel, what was asked and what came
// back, and everything that was not drawn with its reason.
func dashReport(s *dashSnapshot) string {
	if s == nil || s.Dash == nil {
		return "Nothing has been read yet."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s · READ ONLY\n", s.Spec.noun(), safeProviderText(s.title()))
	if s.Spec.UID != "" {
		origin := "found by search: variables are matched by the names namespace, pod, workload and node"
		if s.Spec.Declared {
			origin = "declared for " + s.Spec.For + ": variables are filled as declared"
		}
		fmt.Fprintf(&b, "Grafana dashboard uid %s, schema version %d · %s\n", s.Spec.UID, s.Dash.SchemaVersion, origin)
		fmt.Fprintf(&b, "The definition was read from the declared Grafana source once; its queries go to the declared Prometheus source, not through Grafana.\n")
	}
	fmt.Fprintf(&b, "Window: %s to %s (last %s, UTC) · step %s\n", s.Start.UTC().Format("2006-01-02 15:04:05Z"), s.End.UTC().Format("2006-01-02 15:04:05Z"),
		graphSpan(s.Window), graphSpan(s.Step))
	fmt.Fprintf(&b, "Read began: %s · Source: prometheus via %s\n", graphClock(s.ReadAt), s.Source)
	fmt.Fprintf(&b, "Each query is one bounded GET (%s, %d series, %d samples). One read draws at most %d panels with %d queries. Nothing is polled; r reads again.\n",
		graphSpan(observability.DefaultTimeout), observability.MaxSeries, observability.MaxSamples, dashboard.MaxDrawn, dashboard.MaxQueries)

	b.WriteString("\nVARIABLES\n")
	if s.Label != "" {
		fmt.Fprintf(&b, "  Selection: %s\n", s.Label)
	}
	for i := range s.Plan.Bindings.List {
		binding := &s.Plan.Bindings.List[i]
		if binding.Unresolved != "" {
			fmt.Fprintf(&b, "  $%s NOT RESOLVED: %s\n", binding.Name, safeProviderText(binding.Unresolved))
			continue
		}
		fmt.Fprintf(&b, "  $%s\n", safeProviderText(binding.Display()))
	}
	for _, name := range s.Plan.Bindings.Missing {
		if s.Spec.Declared {
			fmt.Fprintf(&b, "  $%s is declared for this dashboard, but the dashboard has no such variable; nothing was filled from it.\n", name)
		}
	}
	fmt.Fprintf(&b, "  $__interval is the step, %s. $__rate_interval is %s: four steps and at least 2m, because the scrape interval is not read.\n",
		graphSpan(s.Step), graphSpan(s.Plan.Bindings.Rate))
	if s.Dash.VariablesCut > 0 {
		fmt.Fprintf(&b, "  %d more variables were not kept (limit %d).\n", s.Dash.VariablesCut, dashboard.MaxVariables)
	}

	var skipped []string
	b.WriteString("\nPANELS\n")
	for i := range s.Dash.Panels {
		p, pp := &s.Dash.Panels[i], &s.Plan.Panels[i]
		if p.Kind == dashboard.KindRow {
			fmt.Fprintf(&b, "\n-- %s --\n", safeProviderText(p.Title))
			continue
		}
		at, total := s.order(i)
		fmt.Fprintf(&b, "\n%d of %d · %s · %s\n", at, total, safeProviderText(p.Type), safeProviderText(p.Title))
		if pp.NotDrawn != "" {
			fmt.Fprintf(&b, "  %s: %s\n", dashNotDrawn, safeProviderText(pp.NotDrawn))
			skipped = append(skipped, fmt.Sprintf("%s: %s", safeProviderText(p.Title), safeProviderText(pp.NotDrawn)))
		} else if data := s.data(i, false); len(data.Lines) == 0 {
			fmt.Fprintf(&b, "  %s: %s\n", dashNotDrawn, data.Problem)
			skipped = append(skipped, fmt.Sprintf("%s: %s", safeProviderText(p.Title), data.Problem))
		}
		if word := p.Unit.Word(); word != "" {
			fmt.Fprintf(&b, "  unit: %s\n", safeProviderText(word))
		}
		for _, line := range s.queryDetail(i) {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	b.WriteString("\nNOT DRAWN\n")
	if len(skipped) == 0 {
		b.WriteString("  Every panel was drawn.\n")
	}
	for _, line := range skipped {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if s.Dash.PanelsCut > 0 {
		fmt.Fprintf(&b, "  %d more panels of the definition were not kept (limit %d).\n", s.Dash.PanelsCut, dashboard.MaxPanels)
	}
	if s.Spec.isQuery() && s.Spec.Query != nil {
		b.WriteString("\nSAVE THIS QUERY (nothing writes it; add it to the context configuration)\n")
		for _, line := range strings.Split(dashboard.SaveSnippet(s.Spec.Query.Name, s.Spec.Query.Expr), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	b.WriteString("\nREADING THE PAGE\n")
	b.WriteString("  A panel is drawn when its type is " + dashboard.DrawnTypes + " and its query is a Prometheus expression.\n")
	b.WriteString("  A value in a legend is a sample, not an interpolation. No series matched means the query answered and matched nothing: missing data, not a zero.\n")
	fmt.Fprintf(&b, "  One sample is read per step (%s here). Something shorter than the step can fall between two samples and not show, and a rate over $__rate_interval (%s here) flattens a short burst: a flat line in a long window is not proof that nothing happened. A shorter window (1, 2) has a shorter step.\n",
		graphSpan(s.Step), graphSpan(s.Plan.Bindings.Rate))
	b.WriteString("  A threshold word (green, orange, red) is the name the dashboard author gave the step; it is the author's rule, not a finding.\n")
	b.WriteString("  A row is one panel on the time axis of the chart above it. The strip of a stat or gauge whose author gave it a warning or failure step stands over its baseline where the value was in such a step. Any other strip is the series with the highest peak, drawn from zero to that series' own highest sample: the peak beside the number is its scale. A cell without a sample is marked, never drawn as a low value.\n")
	return b.String()
}
