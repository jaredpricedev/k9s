// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	// graphCompareListWidth is the width below which the comparison is a
	// ranked list with one thin trend per row instead of small charts.
	graphCompareListWidth = 72
	// graphMiniRows is the plot height of a small chart that reads well;
	// height beyond it goes to the table of every signal first, and only
	// then to taller charts, up to graphMiniMaxRows.
	graphMiniRows    = 7
	graphMiniMaxRows = 11
	// graphMiniWidth is the narrowest plot of a small chart: wide enough
	// for a title with a name, the word OUTLIER and a number.
	graphMiniWidth   = 24
	graphOutlierWord = "OUTLIER"
	graphNoSamples   = "no samples"
	graphNotCause    = "a difference, not a cause"
)

// graphItem is one drawn unit of the comparison: a Pod, or a group.
type graphItem struct {
	// Name is the short name; Where is the Node of a Pod or the size of a
	// group; Count is the size alone, for the list.
	Name, Where, Count string
	// Word marks an item that differs and Mark is its short form; Long and
	// Short describe its value.
	Word, Mark, Long, Short string
	// Rank is the single number the list shows; Trend is drawn in the
	// list, Series in a chart.
	Rank    string
	Trend   []chart.Point
	Series  []chart.Series
	Outlier bool
	// Limit is the item's limit, read at the cursor.
	Limit []chart.Point
}

// graphLines collects the lines of a page; every line fits the width.
type graphLines struct {
	width int
	ascii bool
	out   []string
}

func (w *graphLines) fit(text string) string {
	if w.ascii {
		return runewidth.Truncate(graphWords(text, true), w.width, "...")
	}
	return graphFit(text, w.width)
}

func (w *graphLines) plain(color, text string) { w.out = append(w.out, graphPaint(color, w.fit(text))) }
func (w *graphLines) bold(color, text string)  { w.out = append(w.out, graphBold(color, w.fit(text))) }

func graphPlural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// graphRankWords name the number an item is ranked by.
func graphRankWords(sig *graphSignal, shares bool) (column, order string) {
	switch {
	case shares:
		return "OF LIMIT", "worst first by peak share of limit"
	case sig.Rank == graphRankLow:
		return "LOW", "lowest first"
	case sig.Rank == graphRankRise:
		return "RISE", "largest rise first"
	}
	return "PEAK", "worst first by peak"
}

// graphValueWords describe one ranked series: the long form for a chart
// title with room, the short one for a narrow title, and the bare rank
// number for the list.
func graphValueWords(sig *graphSignal, shares bool, value, peak, last, share float64) (long, short, rank string) {
	if math.IsNaN(value) {
		return graphNoSamples, graphNoSamples, graphNoSample
	}
	u := sig.Unit
	switch {
	case shares:
		pct := chart.Ratio.Format(share)
		return "peak " + u.Format(peak) + " · " + pct + " of limit", pct + " of limit", pct
	case sig.Rank == graphRankLow:
		return "low " + u.Format(value) + " · last " + u.Format(last), "low " + u.Format(value), u.Format(value)
	case sig.Rank == graphRankRise:
		return "+" + u.Format(value) + " in window · now " + u.Format(last), "+" + u.Format(value), "+" + u.Format(value)
	}
	return "peak " + u.Format(peak) + " · last " + u.Format(last), "peak " + u.Format(peak), u.Format(peak)
}

// graphPodItems are the Pods (or, by container, the containers) of a
// signal as drawable items, in rank order.
func graphPodItems(set *graphSeriesSet, colors graphColors) []graphItem {
	items := make([]graphItem, 0, len(set.Members))
	for i := range set.Members {
		m := &set.Members[i]
		item := graphItem{Name: m.Name, Where: m.Node, Count: m.Node, Trend: m.Points, Outlier: m.Outlier, Limit: m.Limit}
		item.Long, item.Short, item.Rank = graphValueWords(set.Signal, set.Shares, m.Value, m.Peak, m.Last, m.Share)
		color := ""
		if len(colors.Data) > 0 {
			color = colors.Data[0]
		}
		if m.Outlier {
			item.Word, item.Mark, color = graphOutlierWord, graphOutlierWord, colors.Failure
		}
		if len(m.Limit) > 0 {
			item.Series = append(item.Series, chart.Series{Kind: chart.Reference, Points: m.Limit, Glyph: '─', ASCII: '=', Color: colors.Ceiling, Limit: true})
		}
		item.Series = append(item.Series, chart.Series{Points: m.Points, Stroke: chart.Solid, Color: color})
		items = append(items, item)
	}
	return items
}

// graphGroupItems are groups as drawable items: the median as the area (in
// braille and plain characters, the line), the minimum and maximum as
// lines around it.
func graphGroupItems(set *graphSeriesSet, groups []graphGroup, by graphGrouping, colors graphColors) []graphItem {
	items := make([]graphItem, 0, len(groups))
	unit := "pod"
	if by == graphByContainer {
		unit = "series"
	}
	for i := range groups {
		g := &groups[i]
		size := graphPlural(g.Pods, "pod")
		if unit == "series" && len(g.Members) != g.Pods {
			size = fmt.Sprintf("%d series in %s", len(g.Members), graphPlural(g.Pods, "pod"))
		}
		item := graphItem{Name: g.Title, Where: size, Count: fmt.Sprintf("%d", len(g.Members)), Trend: g.Mid, Outlier: len(g.Outliers) > 0, Limit: g.Limit}
		item.Long, item.Short, item.Rank = graphValueWords(set.Signal, set.Shares, g.Value, g.Peak, graphLastOf(g.Mid), g.Value)
		// The ranking number is the worst member's; the latest value is the
		// group's median, and the title says so.
		if u, last := set.Signal.Unit, graphLastOf(g.Mid); !set.Shares && !math.IsNaN(g.Value) {
			switch set.Signal.Rank {
			case graphRankLow:
				item.Long = "lowest " + u.Format(g.Value) + " · median last " + u.Format(last)
			case graphRankRise:
				item.Long = "largest +" + u.Format(g.Value) + " in window · median now " + u.Format(last)
			default:
				item.Long = "peak " + u.Format(g.Peak) + " · median last " + u.Format(last)
			}
		}
		color := ""
		if len(colors.Data) > 0 {
			color = colors.Data[0]
		}
		// The word belongs to a Pod of the group, not to the group: the
		// long form names the Pod, the short one counts.
		switch n := len(g.Outliers); {
		case n == 0:
		case n == 1 && len(g.Members) == 1:
			item.Word, item.Mark = graphOutlierWord+" "+g.Outliers[0], graphOutlierWord
		case n == 1:
			item.Word, item.Mark = graphOutlierWord+" "+g.Outliers[0], "1 "+graphOutlierWord
		default:
			item.Word = fmt.Sprintf("%d %sS", n, graphOutlierWord)
			item.Mark = item.Word
		}
		if len(g.Limit) > 0 {
			item.Series = append(item.Series, chart.Series{Kind: chart.Reference, Points: g.Limit, Glyph: '─', ASCII: '=', Color: colors.Ceiling, Limit: true})
		}
		if len(g.Members) > 1 {
			item.Series = append(item.Series,
				chart.Series{Points: g.High, Color: colors.Axis},
				chart.Series{Points: g.Low, Color: colors.Axis})
		}
		// The median is the filled area of the chart; the minimum and the
		// maximum stay lines around it.
		item.Series = append(item.Series, chart.Series{Points: g.Mid, Stroke: chart.Solid, Color: color, Fill: true})
		items = append(items, item)
	}
	return items
}

func graphLastOf(points []chart.Point) float64 {
	if len(points) == 0 {
		return math.NaN()
	}
	return points[len(points)-1].V
}

// graphTitleForms is the number of forms a chart title has, from the
// fullest to the shortest.
const graphTitleForms = 6

// graphItemTitle writes the title of a small chart in one of its forms:
// the name always, then the word that marks a difference, the value, and
// where the Pod runs or how large the group is. No form cuts a part in the
// middle.
func graphItemTitle(item *graphItem, form int) (title, note string) {
	with := func(word, value string) string {
		switch {
		case word == "":
			return value
		case value == "":
			return word
		}
		return word + " · " + value
	}
	where := item.Name
	if item.Where != "" {
		where += " · " + item.Where
	}
	short := item.Mark
	switch form {
	case 0:
		return where, with(item.Word, item.Long)
	case 1:
		return where, with(item.Word, item.Short)
	case 2:
		return where, with(short, item.Short)
	case 3:
		return where, with(short, item.Rank)
	case 4:
		return item.Name, with(short, item.Short)
	}
	return item.Name, with(short, item.Rank)
}

// graphTitleForm is the fullest form in which every title of a page fits
// its chart, so that the charts of one page are titled alike.
func graphTitleForm(items []graphItem, width int) int {
	for form := range graphTitleForms {
		fits := true
		for i := range items {
			title, note := graphItemTitle(&items[i], form)
			if runewidth.StringWidth(title)+2+runewidth.StringWidth(note) > width {
				fits = false
				break
			}
		}
		if fits {
			return form
		}
	}
	return graphTitleForms - 1
}

// graphSpark draws points as a thin trend exactly cells wide. Cells before
// the first and after the last sample stay empty (the series did not exist
// there); a hole between two samples is the gap glyph, never a level.
//
// A cell that holds several samples shows the worst of them: the largest,
// or with low the smallest, so that a short dip of a signal that is worse
// when low is not folded away any more than a short peak is.
func graphSpark(points []chart.Point, f chart.Frame, top float64, color, gap string, o chart.Options, low bool) string {
	values := chart.SparkValues(points, f)
	if low {
		mirrored := make([]chart.Point, len(points))
		for i, p := range points {
			mirrored[i] = chart.Point{T: p.T, V: -p.V}
		}
		values = chart.SparkValues(mirrored, f)
		for i := range values {
			values[i] = -values[i] // NaN stays NaN
		}
	}
	first, last := -1, -1
	for i, v := range values {
		if !math.IsNaN(v) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return strings.Repeat(" ", f.Width)
	}
	return strings.Repeat(" ", first) +
		chart.Spark(values[first:last+1], chart.SparkOptions{ASCII: o.ASCII, Style: chart.SparkStyleOf(o.Style), Max: top, Color: color, GapColor: gap}) +
		strings.Repeat(" ", f.Width-1-last)
}

// graphStandingLine says, across every signal read, which Pod differs from
// its peers. It never says why, and the caveat that it is a difference and
// not a cause is the last thing to be shortened.
func graphStandingLine(st *graphStanding, pods, width int) (text string, flagged bool) {
	switch {
	case pods < chart.DefaultMinPeers:
		return fmt.Sprintf("%s: too few peers to call one different (%d are needed)", graphPlural(pods, "Pod"), chart.DefaultMinPeers), false
	case st.Read == 0:
		return "No signal had samples for these Pods: nothing can be compared (missing data, not health)", false
	}
	read := graphPlural(st.Read, "signal") + " read"
	if st.Read < st.Total {
		read += fmt.Sprintf(" (%d of %d had no reading)", st.Total-st.Read, st.Total)
	}
	var variants []string
	switch len(st.Pods) {
	case 0:
		return "No Pod differs from its peers in the " + read, false
	case 1:
		pod := st.Pods[0]
		n, names := len(st.Signals[pod]), strings.Join(st.Signals[pod], ", ")
		variants = []string{
			fmt.Sprintf("* %s differs from its peers in %d of %s: %s · %s", pod, n, read, names, graphNotCause),
			fmt.Sprintf("* %s differs in %d of %d signals: %s · %s", pod, n, st.Read, names, graphNotCause),
			fmt.Sprintf("* %s differs in %d of %d signals · %s", pod, n, st.Read, graphNotCause),
			fmt.Sprintf("* %s differs in %d of %d · %s", pod, n, st.Read, graphNotCause),
			fmt.Sprintf("* %s differs in %d/%d · not a cause", pod, n, st.Read),
		}
	default:
		long := make([]string, 0, len(st.Pods))
		short := make([]string, 0, len(st.Pods))
		for _, pod := range st.Pods {
			long = append(long, fmt.Sprintf("%s in %s", pod, strings.Join(st.Signals[pod], ", ")))
			short = append(short, fmt.Sprintf("%s in %d", pod, len(st.Signals[pod])))
		}
		variants = []string{
			"* differ from their peers: " + strings.Join(long, "; ") + " · " + graphNotCause,
			fmt.Sprintf("* %d Pods differ from their peers: %s · %s", len(st.Pods), strings.Join(short, ", "), graphNotCause),
			fmt.Sprintf("* %d Pods differ from their peers · %s", len(st.Pods), graphNotCause),
			fmt.Sprintf("* %d Pods differ · not a cause", len(st.Pods)),
		}
	}
	for _, variant := range variants {
		if runewidth.StringWidth(variant) <= width {
			return variant, true
		}
	}
	return variants[len(variants)-1], true
}

// graphStrip lays the signal names out on one line with the selected one
// in brackets and a star on every signal in which a Pod differs. Signals
// that do not fit are counted on either side, never dropped silently. The
// returned spans are the byte ranges of the selected token.
func graphStrip(c *graphCompare, st *graphStanding, selected, width int) (text string, from, to int) {
	tokens := make([]string, len(c.Signals))
	for i := range c.Signals {
		token := c.Signals[i].Name
		if st.Flagged[c.Signals[i].ID] {
			token += "*"
		}
		if state := st.States[c.Signals[i].ID]; state != "" {
			token += " (" + state + ")"
		}
		if i == selected {
			token = "[" + token + "]"
		}
		tokens[i] = token
	}
	const sep = "  "
	// earlier and more word the two counters; the short wording is for a
	// width at which the selected name alone nearly fills the line.
	earlier, more := " earlier", " more"
	build := func(start int) (line string, end int, a, b int) {
		if start > 0 {
			line = fmt.Sprintf("+%d%s%s", start, earlier, sep)
		}
		end = start
		for end < len(tokens) {
			next := line
			if end > start {
				next += sep
			}
			if end == selected {
				a = len(next)
			}
			next += tokens[end]
			rest := ""
			if end < len(tokens)-1 {
				rest = fmt.Sprintf("%s+%d%s", sep, len(tokens)-1-end, more)
			}
			if runewidth.StringWidth(next+rest) > width && end > start {
				break
			}
			line = next
			if end == selected {
				b = len(line)
			}
			end++
		}
		if end < len(tokens) {
			line += fmt.Sprintf("%s+%d%s", sep, len(tokens)-end, more)
		}
		return line, end, a, b
	}
	pick := func() (string, int, int) {
		for start := 0; start < len(tokens); start++ {
			line, end, a, b := build(start)
			if selected < end || start >= selected {
				return line, a, b
			}
		}
		return "", 0, 0
	}
	text, from, to = pick()
	if runewidth.StringWidth(text) > width {
		// The selected name with both counters is wider than the page: the
		// counters lose their words before the name loses anything.
		earlier, more = "", ""
		text, from, to = pick()
	}
	if runewidth.StringWidth(text) > width {
		return runewidth.Truncate(text, width, "…"), 0, 0
	}
	return text, from, to
}

// graphCompareStatus is the second line: when the per-Pod read began and
// how its queries went.
func graphCompareStatus(c *graphCompare, shown, first, total int, what string, level int) []string {
	t := c.tally()
	parts := []string{"read " + graphClock(c.ReadAt)}
	answered := fmt.Sprintf("%d of %d queries answered", t.Asked-t.Failed, t.Asked)
	if level > 0 {
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
		cut := fmt.Sprintf("%d cut at the series or sample limit", t.Incomplete)
		if level > 0 {
			cut = fmt.Sprintf("%d cut by limit", t.Incomplete)
		}
		parts = append(parts, cut)
	}
	pods := graphPlural(len(c.Base.Pods), "pod")
	if c.Base.PodsOmitted > 0 {
		pods += fmt.Sprintf(", %d NOT READ", c.Base.PodsOmitted)
	}
	parts = append(parts, pods)
	if shown < total {
		switch level {
		case 0:
			parts = append(parts, fmt.Sprintf("%ss %d-%d of %d (↑/↓ pages)", what, first+1, first+shown, total))
		case 1:
			parts = append(parts, fmt.Sprintf("%d-%d of %d %ss ↑↓", first+1, first+shown, total, what))
		default:
			parts = append(parts, fmt.Sprintf("%d-%d of %d ↑↓", first+1, first+shown, total))
		}
	}
	return parts
}

func graphCompareFooter(width int, ascii bool, style chart.Style) string {
	return chartStyleFit([]string{
		"←/→ cursor · n/N signal · g/G regroup · p overlaid lines · ↑/↓ page · 1-4 window · r re-read · s queries · Esc back",
		"←/→ cursor · n signal · g regroup · p overlaid · ↑/↓ page · 1-4 window · r re-read · s queries · Esc",
		"←/→ cursor · n signal · g regroup · p overlaid · ↑/↓ page · r read · s queries",
		"←/→ cursor · n signal · g regroup · p overlaid · r read · s queries",
		"n signal · g regroup · p overlaid · ↑/↓ page · r read · Esc",
	}, style, width, ascii, "n signal · g regroup · p overlaid")
}

// graphGroupingLine names the grouping, the size of every group and the
// Pods that could not be placed in one.
func graphGroupingLine(c *graphCompare, groups []graphGroup, by graphGrouping) string {
	source := map[graphGrouping]string{
		graphByContainer: "the container label of each series",
		graphByRevision:  "the owner in kube_pod_info",
		graphByNode:      "kube_pod_info",
		graphByZone:      "kube_node_labels",
	}[by]
	if by == graphByRevision && len(c.Hashes) > 0 {
		source = "kube_pod_info and kube_pod_labels"
	}
	var sizes []string
	var unknown []string
	for i := range groups {
		g := &groups[i]
		if g.Unknown {
			for j := range g.Members {
				unknown = append(unknown, g.Members[j].Name)
			}
			continue
		}
		size := fmt.Sprintf("%s %d", g.Title, len(g.Members))
		if len(sizes) == 0 {
			size = g.Title + " " + graphPlural(len(g.Members), "pod")
			if by == graphByContainer {
				size = g.Title + " in " + graphPlural(g.Pods, "pod")
			}
		}
		sizes = append(sizes, size)
	}
	text := "By " + by.String() + " (" + source + "): " + strings.Join(sizes, ", ")
	if len(sizes) == 0 {
		text = "By " + by.String() + " (" + source + "): no group could be determined"
	}
	if len(unknown) == 0 {
		return text + " · all placed"
	}
	why := map[graphGrouping]string{
		graphByContainer: "no series with a container label was returned for them",
		graphByRevision:  "no ReplicaSet owner in kube_pod_info and no controller-revision-hash in kube_pod_labels",
		graphByNode:      "kube_pod_info names no single Node for them",
		graphByZone:      "kube_node_labels names no zone for their Nodes",
	}[by]
	sort.Strings(unknown)
	return text + " · " + by.String() + " not known for " + strings.Join(unknown, ", ") + " (" + why + ")"
}

// renderGraphCompare lays the comparison page out for a size. Every line
// fits Width. It reads pg.Signal, pg.Grouping and pg.First (the first item
// of the page shown) and writes back pg.First and pg.Shown.
func renderGraphCompare(s *graphSnapshot, c *graphCompare, pg *graphPage) []string {
	width := max(pg.Width, 20)
	col := pg.Colors
	w := &graphLines{width: width, ascii: pg.ASCII}
	if s == nil || s.failure() != nil {
		return renderGraph(s, pg)
	}
	w.bold(col.Title, s.headline(width))
	if c == nil || c.Base != s {
		busy := pg.Busy
		if busy == "" {
			busy = "The per-Pod series of this window have not been read. r reads again."
		}
		w.plain(col.Muted, busy)
		w.out = append(w.out, "")
		w.plain(col.Muted, graphCompareFooter(width, pg.ASCII, pg.Style))
		return w.out
	}
	standing := c.standing()
	if pg.Signal == "" {
		// Nothing chosen yet: open on the first signal in which a Pod
		// differs, else on the first that has samples.
		pg.Signal = standing.first(c)
	}
	sig, index := c.signal(pg.Signal)
	if sig == nil {
		w.plain(col.Muted, "No Pod was asked for, so there is nothing to compare.")
		w.plain(col.Muted, graphCompareFooter(width, pg.ASCII, pg.Style))
		return w.out
	}
	pg.Signal = sig.ID
	by := pg.Grouping
	set := c.series(sig, by == graphByContainer)
	var (
		items  []graphItem
		groups []graphGroup
		what   = "pod"
	)
	if set.State == "" {
		if by == graphByPod {
			items = graphPodItems(&set, col)
		} else {
			groups = c.groups(&set, by)
			items = graphGroupItems(&set, groups, by, col)
			what = "group"
		}
	}

	frameOf := func(cells int) chart.Frame {
		return chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: max(cells, 1)}
	}
	grid := frameOf(1)
	cursor := min(max(pg.Cursor, 0), grid.Steps())
	at := grid.TimeAt(cursor)
	valueAt := func(points []chart.Point) (float64, bool) {
		return grid.ValueAt(&chart.Series{Points: points}, at)
	}

	// The lines under the charts are built first: their height decides how
	// many charts fit.
	list := width < graphCompareListWidth
	readout := "Cursor " + graphClock(at)
	switch behind := s.End.Sub(at); {
	case behind <= 0:
		readout += " (window end)"
	case width < 100:
		readout += ", -" + graphAgo(behind)
	default:
		readout += ", " + graphAgo(behind) + " before window end"
	}
	values := func() []string {
		parts := []string{readout}
		for i := range items {
			text := items[i].Name + " " + graphNoSample
			if v, ok := valueAt(items[i].Trend); ok {
				text = items[i].Name + " " + sig.Unit.Format(v)
				// In words what the warning colour of an area means.
				if limit, ok := valueAt(items[i].Limit); ok && chart.Near(v, limit) {
					text += " at limit"
				}
			}
			parts = append(parts, text)
		}
		if by != graphByPod && len(parts) > 1 {
			parts[1] = "medians: " + parts[1]
		}
		return parts
	}
	limitNote := func() string {
		lowest, found := math.Inf(1), false
		for i := range items {
			if v, ok := valueAt(items[i].Limit); ok {
				lowest, found = math.Min(lowest, v), true
			}
		}
		if !found {
			return ""
		}
		return "lowest limit " + sig.Unit.Format(lowest)
	}
	var under []string
	build := func(list bool) {
		parts := []string{readout}
		if !list {
			parts = values()
		}
		if note := limitNote(); note != "" {
			parts = append(parts, note)
		}
		for i := range parts {
			parts[i] = graphWords(parts[i], pg.ASCII)
		}
		lines := graphJoinLines(parts, " · ", width)
		maxLines := 2
		if pg.Height >= 30 {
			maxLines = 3
		}
		if len(lines) > maxLines {
			// The values that do not fit are counted; the list shows them all.
			kept := strings.Count(strings.Join(lines[:maxLines], " · "), " · ") + 1
			more := fmt.Sprintf("+%d more", len(parts)-kept)
			lines = lines[:maxLines]
			for runewidth.StringWidth(lines[maxLines-1]+" · "+more) > width && strings.Contains(lines[maxLines-1], " · ") {
				lines[maxLines-1] = lines[maxLines-1][:strings.LastIndex(lines[maxLines-1], " · ")]
				kept--
				more = fmt.Sprintf("+%d more", len(parts)-kept)
			}
			lines[maxLines-1] += " · " + more
		}
		under = lines
		if by != graphByPod && set.State == "" {
			// On the shortest pages the grouping keeps to one line, cut
			// visibly, so that the key line below it is never pushed out.
			most := 2
			if pg.Height < 16 {
				most = 1
			}
			under = append(under, graphWrapMax(graphGroupingLine(c, groups, by), width, most, pg.ASCII)...)
		}
	}
	build(list)

	// statusFor words the status line for a page; it takes a second line
	// rather than lose the count of what is not shown.
	statusFor := func(shown, first int) []string {
		var two, parts []string
		for level := range 3 {
			parts = graphCompareStatus(c, shown, first, len(items), what, level)
			if pg.Busy != "" {
				parts[0] = pg.Busy + " · shown: " + parts[0]
			}
			for i := range parts {
				parts[i] = graphWords(parts[i], pg.ASCII)
			}
			status := graphJoinLines(parts, " · ", width)
			if len(status) == 1 {
				return status
			}
			// No wording fits one line: two lines of the fullest wording
			// that fits them say more than two of the shortest.
			if len(status) == 2 && two == nil {
				two = status
			}
		}
		if two != nil {
			return two
		}
		// Not even the shortest wording fits two lines. The middle is cut
		// visibly; the count of what is not shown is the last part and stays.
		rest, last := strings.Join(parts[1:], " · "), ""
		if shown < len(items) && len(parts) > 2 {
			rest, last = strings.Join(parts[1:len(parts)-1], " · "), parts[len(parts)-1]
		}
		if last == "" {
			return []string{parts[0], rest}
		}
		cut := graphWords("…", pg.ASCII)
		if room := width - runewidth.StringWidth(last) - 3; room >= 8 {
			return []string{parts[0], runewidth.Truncate(rest, room, cut) + " · " + last}
		}
		return []string{parts[0], last}
	}
	// Headline, status, signal strip, standing, signal title. The status
	// is measured for the widest page numbers it can hold.
	top := 4 + len(statusFor(1, max(len(items)-1, 0)))
	body := pg.Height - top - len(under) - 1
	var (
		drawn  []string
		shown  int
		layout chart.GridLayout
		strip  = true
	)
	first := min(max(pg.First, 0), max(len(items)-1, 0))
	scale := chart.Grid{Unit: sig.Unit, MinWidth: graphMiniWidth}
	for i := range items {
		scale.Minis = append(scale.Minis, chart.Mini{Series: items[i].Series})
	}
	ceiling := scale.Top()
	options := pg.options(cursor)
	if len(items) > 0 && !list {
		// The page size is that of the whole set, so that paging steps by
		// whole pages; a page holds what fits at a readable size.
		page := scale.Layout(width, body).Shown
		if page > 0 {
			first -= first % page
			pageItems := items[first:min(first+page, len(items))]
			g := chart.Grid{Unit: sig.Unit, MinWidth: graphMiniWidth, Ceiling: ceiling, Empty: graphNoSamples, Escape: tview.Escape}
			g.Minis = make([]chart.Mini, len(pageItems))
			for i := range pageItems {
				g.Minis[i].Series = pageItems[i].Series
			}
			layout = g.Layout(width, body)
			// Plots stop growing at a height that reads well; what is left
			// of a tall page shows every signal, and the charts take the
			// rest up to their largest height.
			table := 0
			if by == graphByPod {
				table = len(c.Signals) + 3
			}
			height := min(body, layout.Rows*(1+graphMiniRows)+2)
			if body-height < table {
				table = 0
			}
			height = min(body-table, layout.Rows*(1+graphMiniMaxRows)+2)
			layout = g.Layout(width, height)
			form := graphTitleForm(pageItems, layout.PlotWidth)
			for i := range pageItems {
				title, note := graphItemTitle(&pageItems[i], form)
				title, note = graphWords(title, pg.ASCII), graphWords(note, pg.ASCII)
				if room := layout.PlotWidth - runewidth.StringWidth(note) - 1; runewidth.StringWidth(title) > room {
					// Not even the shortest form fits: the name is cut, visibly.
					title = (&graphLines{width: max(room, 1), ascii: pg.ASCII}).fit(title)
				}
				g.Minis[i].Title, g.Minis[i].Note = title, note
				if pageItems[i].Outlier {
					g.Minis[i].TitleColor = col.Failure
				}
			}
			drawn, layout = g.Render(s.Start, s.End, s.Step, width, height, options)
			shown = layout.Shown
			if table > 0 && body-len(drawn) >= table {
				drawn = append(drawn, "")
				drawn = append(drawn, graphSignalTable(c, &standing, &set, sig.ID, s, width, pg)...)
			}
		}
	}
	if len(items) > 0 && shown == 0 {
		// Too narrow or too short for charts: the ranked list.
		list = true
		build(true)
		body = pg.Height - top - len(under) - 1
		rows := max(body-1, 1)
		first -= first % rows
		drawn, shown = graphCompareList(items[first:min(first+rows, len(items))], sig, &set, by, frameOf, ceiling, at, valueAt, width, pg)
		switch spare := body - len(drawn); {
		case by != graphByPod:
		case spare >= len(c.Signals)+2:
			drawn = append(drawn, "")
			drawn = append(drawn, graphSignalRanks(c, &standing, index, width, spare-1, pg)...)
		case spare+1 >= 5:
			// The table names the signals, marks the selected one and stars
			// the ones in which a Pod differs: it stands in for the strip
			// when there is room for only one of them, and counts the
			// signals it has no row for.
			strip = false
			drawn = append(drawn, graphSignalRanks(c, &standing, index, width, spare+1, pg)...)
		}
	}
	pg.First, pg.Shown = first, max(shown, 1)

	// Status, strip, standing, title.
	for _, line := range statusFor(shown, first) {
		w.plain(col.Muted, line)
	}
	if names, from, to := graphStrip(c, &standing, index, width); !strip {
		// The table of every signal below carries the same names.
	} else if to > from && to <= len(names) {
		w.out = append(w.out, graphPaint(col.Muted, names[:from])+graphBold(col.Title, names[from:to])+graphPaint(col.Muted, names[to:]))
	} else {
		w.plain(col.Muted, names)
	}
	text, flagged := graphStandingLine(&standing, len(c.pods()), width)
	color := ""
	if flagged {
		color = col.Request
	}
	w.plain(color, text)

	_, order := graphRankWords(sig, set.Shares)
	head := sig.Title + " by " + by.String()
	rest := " · " + order + " · " + sig.About
	if by != graphByPod {
		rest = " · line median, thin lines min and max" + rest
		if !pg.ASCII && pg.Style == chart.Auto {
			rest = strings.Replace(rest, "line median, thin lines", "area median, lines", 1)
		}
	}
	if set.Incomplete {
		rest = " · CUT at the source's series or sample limit" + rest
	}
	if set.State != "" {
		rest = " · " + set.State
	}
	head = w.fit(head)
	w.out = append(w.out, graphBold(col.Title, head)+graphPaint(col.Muted, (&graphLines{width: width - runewidth.StringWidth(head), ascii: pg.ASCII}).fit(rest)))

	switch {
	case set.State != "":
		reason := set.Reason
		if pg.Busy != "" && set.State == "not read" {
			reason = pg.Busy
		}
		// The reason comes first and the key line stays: on a short page the
		// blank line and the hint give way, and a reason that still does not
		// fit is cut visibly.
		room := max(pg.Height-len(w.out)-1, 1)
		why := graphWrap(reason, width, "")
		if len(why) > room {
			why = append(why[:room-1], w.fit(strings.Join(why[room-1:], " ")))
		}
		hint := graphWrap("n shows the next signal, g the next grouping; s lists every query and its answer.", width, "")
		if len(why)+1+len(hint) <= room {
			w.out = append(w.out, "")
		}
		for _, line := range why {
			w.plain("", line)
		}
		if len(why)+len(hint) <= room {
			for _, line := range hint {
				w.plain(col.Muted, line)
			}
		}
		w.plain(col.Muted, graphCompareFooter(width, pg.ASCII, pg.Style))
		return w.out
	case len(items) == 0:
		w.plain("", "No Pod was asked for, so there is nothing to compare.")
	}
	w.out = append(w.out, drawn...)
	for _, line := range under {
		w.plain("", line)
	}
	w.plain(col.Muted, graphCompareFooter(width, pg.ASCII, pg.Style))
	return w.out
}

// graphSignalTable is the dense table under the charts of a tall page: one
// row per signal, one thin trend per Pod, each row on its own scale with
// the ranking number beside it. The Pods keep the order of the charts
// above, so the one that differs there is the first column in every row. A
// star marks the number of a Pod that differs from its peers in that row.
func graphSignalTable(c *graphCompare, st *graphStanding, shown *graphSeriesSet, selected string, s *graphSnapshot, width int, pg *graphPage) []string {
	col := pg.Colors
	const gap = 2
	labelW, valueW := 0, 5
	sets := make([]graphSeriesSet, len(c.Signals))
	for i := range c.Signals {
		labelW = max(labelW, runewidth.StringWidth(c.Signals[i].Name))
		sets[i] = c.series(&c.Signals[i], false)
		for j := range sets[i].Members {
			m := &sets[i].Members[j]
			_, _, value := graphValueWords(&c.Signals[i], sets[i].Shares, m.Value, m.Peak, m.Last, m.Share)
			// One more cell for the star of a Pod that differs.
			valueW = max(valueW, runewidth.StringWidth(graphWords(value, pg.ASCII))+1)
		}
	}
	valueW = min(valueW, 12)
	labelW += 4 // "> " in front, "*" and a space behind
	pods := make([]string, 0, len(shown.Members))
	names := make(map[string]string)
	for i := range shown.Members {
		pods = append(pods, shown.Members[i].Pod)
		names[shown.Members[i].Pod] = shown.Members[i].Name
		if shown.Members[i].Node != "" {
			names[shown.Members[i].Pod] += " · " + shown.Members[i].Node
		}
	}
	columns := len(pods)
	for columns > 1 && (width-labelW)/columns-gap-valueW-1 < 8 {
		columns--
	}
	cellW := (width-labelW)/max(columns, 1) - gap
	sparkW := min(max(cellW-valueW-1, 4), 40)
	cellW = sparkW + 1 + valueW
	frame := chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: sparkW}
	pad := func(text string, cells int) string {
		text = (&graphLines{width: cells, ascii: pg.ASCII}).fit(text)
		return text + strings.Repeat(" ", max(cells-runewidth.StringWidth(text), 0))
	}
	head := "EVERY SIGNAL"
	rest := " · one row per signal, each on its own scale · the number is what ranks the Pod · * differs from its peers"
	if columns < len(pods) {
		rest = fmt.Sprintf(" · first %d of %d pods", columns, len(pods)) + rest
	}
	lines := []string{graphBold(col.Title, head) + graphPaint(col.Muted, (&graphLines{width: width - len(head), ascii: pg.ASCII}).fit(rest))}
	header := strings.Repeat(" ", labelW)
	for _, pod := range pods[:columns] {
		header += pad(names[pod], cellW) + strings.Repeat(" ", gap)
	}
	lines = append(lines, graphPaint(col.Muted, strings.TrimRight(header, " ")))
	for i := range c.Signals {
		sig := &c.Signals[i]
		mark, labelColor := "  ", col.Muted
		if sig.ID == selected {
			mark, labelColor = "> ", col.Title
		}
		label := mark + sig.Name
		if st.Flagged[sig.ID] {
			label += "*"
		}
		row := graphPaint(labelColor, pad(label, labelW))
		if sig.ID == selected {
			row = graphBold(labelColor, pad(label, labelW))
		}
		set := &sets[i]
		if set.State != "" {
			lines = append(lines, row+graphPaint(col.Muted, pad(set.State+": not compared (missing data, not a reading of zero)", width-labelW)))
			continue
		}
		byPod := make(map[string]*graphMember, len(set.Members))
		scale := chart.Panel{Unit: sig.Unit}
		for j := range set.Members {
			byPod[set.Members[j].Pod] = &set.Members[j]
			scale.Series = append(scale.Series, chart.Series{Points: set.Members[j].Points})
		}
		for _, pod := range pods[:columns] {
			m := byPod[pod]
			if m == nil || !m.sampled() {
				row += graphPaint(col.Muted, pad(graphNoSamples, cellW)) + strings.Repeat(" ", gap)
				continue
			}
			_, _, value := graphValueWords(sig, set.Shares, m.Value, m.Peak, m.Last, m.Share)
			value = graphWords(value, pg.ASCII)
			color, valueColor := "", ""
			if len(col.Data) > 0 {
				color = col.Data[0]
			}
			if m.Outlier {
				value += "*"
				color, valueColor = col.Failure, col.Failure
			}
			row += graphSpark(m.Points, frame, scale.Top(), color, col.Muted, pg.options(-1), sig.Rank == graphRankLow) + " " +
				graphPaint(valueColor, pad(value, valueW)) + strings.Repeat(" ", gap)
		}
		lines = append(lines, strings.TrimRight(row, " "))
	}
	return lines
}

// graphSignalRanks is the narrow form of the table of every signal: per
// signal the Pod that ranks worst, its number and the median of all Pods.
func graphSignalRanks(c *graphCompare, st *graphStanding, selected, width, height int, pg *graphPage) []string {
	col := pg.Colors
	type row struct {
		label, pod, value, median, state string
		outlier, selected                bool
	}
	rows := make([]row, 0, len(c.Signals))
	labelW, podW, valueW := 0, len("WORST POD"), len("VALUE")
	for i := range c.Signals {
		sig := &c.Signals[i]
		r := row{label: "  " + sig.Name, selected: i == selected}
		if r.selected {
			r.label = "> " + sig.Name
		}
		if st.Flagged[sig.ID] {
			r.label += "*"
		}
		set := c.series(sig, false)
		switch {
		case set.State != "":
			r.state = set.State
		case len(set.Members) > 0 && set.Members[0].sampled():
			m := &set.Members[0]
			_, _, r.value = graphValueWords(sig, set.Shares, m.Value, m.Peak, m.Last, m.Share)
			r.pod, r.outlier = m.Name, m.Outlier
			// Where every Pod has the same number none of them is worst.
			alike := len(set.Members) > 1
			for j := range set.Members {
				alike = alike && set.Members[j].Value == m.Value
			}
			if alike {
				r.pod = "all alike"
			}
			unit := sig.Unit
			if set.Shares {
				unit = chart.Ratio
			}
			r.median = graphNoSample
			if set.Peers.Enough {
				r.median = unit.Format(set.Peers.Median)
			}
		default:
			r.state = graphNoData
		}
		r.value, r.median = graphWords(r.value, pg.ASCII), graphWords(r.median, pg.ASCII)
		labelW, podW, valueW = max(labelW, runewidth.StringWidth(r.label)), max(podW, runewidth.StringWidth(r.pod)), max(valueW, runewidth.StringWidth(r.value))
		rows = append(rows, r)
	}
	podW = min(podW, max(width-labelW-valueW-24, 6))
	pad := func(text string, cells int) string {
		text = (&graphLines{width: cells, ascii: pg.ASCII}).fit(text)
		return text + strings.Repeat(" ", max(cells-runewidth.StringWidth(text), 0))
	}
	w := &graphLines{width: width, ascii: pg.ASCII}
	rest := &graphLines{width: max(width-labelW-1, 1), ascii: pg.ASCII}
	lines := []string{graphBold(col.Title, pad("EVERY SIGNAL", labelW+1)) + graphPaint(col.Muted, rest.fit(pad("WORST POD", podW)+" "+pad("VALUE", valueW)+" MEDIAN OF ALL"))}
	// When the rows do not all fit, the ones around the selected signal are
	// shown and the rest are counted.
	from, to := 0, len(rows)
	if len(rows) > height-1 {
		keep := max(height-2, 1)
		from = min(max(selected-keep/2, 0), len(rows)-keep)
		to = from + keep
	}
	for i := from; i < to; i++ {
		r := &rows[i]
		labelColor := col.Muted
		if r.selected {
			labelColor = col.Title
		}
		label := graphPaint(labelColor, pad(r.label, labelW+1))
		if r.state != "" {
			lines = append(lines, label+graphPaint(col.Muted, (&graphLines{width: width - labelW - 1, ascii: pg.ASCII}).fit(r.state+": not compared")))
			continue
		}
		rest := pad(r.pod, podW) + " " + pad(r.value, valueW) + " " + r.median
		color := ""
		if r.outlier {
			rest = pad(r.pod, podW) + " " + pad(r.value, valueW) + " " + pad(r.median, 7) + " " + graphOutlierWord
			color = col.Failure
		}
		lines = append(lines, label+graphPaint(color, (&graphLines{width: width - labelW - 1, ascii: pg.ASCII}).fit(strings.TrimRight(rest, " "))))
	}
	if hidden := len(rows) - (to - from); hidden > 0 {
		lines = append(lines, graphPaint(col.Muted, w.fit(fmt.Sprintf("  +%d more signals: n and N step through all %d", hidden, len(rows)))))
	}
	return lines
}

// graphWrapMax wraps text to at most lines lines; what does not fit is cut
// visibly at the end of the last line.
func graphWrapMax(text string, width, lines int, ascii bool) []string {
	wrapped := graphWrap(text, width, "  ")
	if len(wrapped) <= lines {
		return wrapped
	}
	rest := strings.Join(wrapped[lines-1:], " ")
	return append(wrapped[:lines-1], (&graphLines{width: width, ascii: ascii}).fit(strings.Join(strings.Fields(rest), " ")))
}

// graphCompareList is the narrow form of the comparison: one row per Pod
// or group, ranked, with a thin trend on the shared scale, the ranking
// number, the value at the cursor and the word that marks a difference.
func graphCompareList(items []graphItem, sig *graphSignal, set *graphSeriesSet, by graphGrouping, frameOf func(int) chart.Frame,
	ceiling float64, at time.Time, valueAt func([]chart.Point) (float64, bool), width int, pg *graphPage) (lines []string, shown int) {
	col := pg.Colors
	rankHead, _ := graphRankWords(sig, set.Shares)
	nameHead, whereHead := "POD", "NODE"
	if by != graphByPod {
		nameHead, whereHead = strings.ToUpper(by.String()), "N"
	}
	atHead := "AT " + at.UTC().Format("15:04")
	if by != graphByPod {
		// The value of a group at the cursor is the median of its members.
		atHead = "MEDIAN " + at.UTC().Format("15:04")
	}
	nameW, whereW, rankW, atW, wordW := runewidth.StringWidth(nameHead), runewidth.StringWidth(whereHead), runewidth.StringWidth(rankHead), runewidth.StringWidth(atHead), 0
	cursorValues := make([]string, len(items))
	for i := range items {
		cursorValues[i] = graphWords(graphNoSample, pg.ASCII)
		if v, ok := valueAt(items[i].Trend); ok {
			cursorValues[i] = sig.Unit.Format(v)
		}
		nameW = max(nameW, runewidth.StringWidth(items[i].Name))
		whereW = max(whereW, runewidth.StringWidth(items[i].Count))
		rankW = max(rankW, runewidth.StringWidth(graphWords(items[i].Rank, pg.ASCII)))
		atW = max(atW, runewidth.StringWidth(cursorValues[i]))
		wordW = max(wordW, runewidth.StringWidth(items[i].Mark))
	}
	nameW, whereW = min(nameW, 18), min(whereW, 12)
	fixed := func() int {
		total := nameW + 1 + rankW
		for _, column := range []int{whereW, atW, wordW} {
			if column > 0 {
				total += column + 1
			}
		}
		return total
	}
	// The trend keeps at least twelve cells. The Node column goes first,
	// then the value at the cursor (the header no longer names it), and
	// only then is the name shortened: the name says which row is which.
	room := func() int { return width - fixed() - 1 }
	if room() < 12 {
		whereW = 0
	}
	if room() < 12 {
		atW = 0
	}
	if short := 12 - room(); short > 0 {
		nameW = max(nameW-short, 6)
	}
	trendW := min(max(room(), 4), 48)
	frame := frameOf(trendW)
	pad := func(text string, cells int) string {
		text = (&graphLines{width: cells, ascii: pg.ASCII}).fit(text)
		return text + strings.Repeat(" ", max(cells-runewidth.StringWidth(text), 0))
	}
	right := func(text string, cells int) string {
		return strings.Repeat(" ", max(cells-runewidth.StringWidth(text), 0)) + text
	}
	trendHead := "LAST " + graphSpan(frame.End.Sub(frame.Start))
	header := pad(nameHead, nameW) + " "
	if whereW > 0 {
		header += pad(whereHead, whereW) + " "
	}
	header += pad(trendHead, trendW) + " " + right(rankHead, rankW)
	if atW > 0 {
		header += " " + right(atHead, atW)
	}
	lines = append(lines, graphPaint(col.Muted, strings.TrimRight(header, " ")))
	for i := range items {
		item := &items[i]
		color, lineColor := "", ""
		if len(col.Data) > 0 {
			lineColor = col.Data[0]
		}
		if item.Outlier {
			color, lineColor = col.Failure, col.Failure
		}
		lead := pad(item.Name, nameW) + " "
		if whereW > 0 {
			lead += pad(item.Count, whereW) + " "
		}
		tail := " " + right(graphWords(item.Rank, pg.ASCII), rankW)
		if atW > 0 {
			tail += " " + right(cursorValues[i], atW)
		}
		if item.Mark != "" {
			tail += " " + item.Mark
		}
		trend := graphSpark(item.Trend, frame, ceiling, lineColor, col.Muted, pg.options(-1), sig.Rank == graphRankLow)
		if len(item.Trend) == 0 {
			trend = graphPaint(col.Muted, pad(graphNoSamples, trendW))
		}
		lines = append(lines, graphPaint(color, lead)+trend+graphPaint(color, tail))
	}
	return lines, len(items)
}

// graphCompareReport is the part of the s page that lists the per-Pod
// reads: which Service chose the profiles, every query and its answer, the
// label lookups, and the rule by which a Pod is called different.
func graphCompareReport(c *graphCompare) string {
	var b strings.Builder
	b.WriteString("\nPER-POD COMPARISON (p) · one series per Pod and signal, same window and step\n")
	fmt.Fprintf(&b, "Read began: %s\n", graphClock(c.ReadAt))
	if c.Reused > 0 {
		fmt.Fprintf(&b, "%d of the answers below are the ones the charts above already read for this window; they were not asked twice.\n", c.Reused)
	}
	if c.Service.Path != "" {
		fmt.Fprintf(&b, "Request path of Service %s", c.Service.Path)
	} else {
		b.WriteString("Request path: no Service")
	}
	if c.Service.Note != "" {
		fmt.Fprintf(&b, " · %s", c.Service.Note)
	}
	b.WriteString("\n")
	if len(c.Profiles) > 0 {
		fmt.Fprintf(&b, "App-hop profiles that supplied signals: %s (docs/request-path.md)\n", strings.Join(c.Profiles, ", "))
	} else {
		b.WriteString("No app-hop profile supplied a signal; only the cAdvisor and kube-state-metrics signals are compared.\n")
	}
	for _, n := range c.NotRead {
		name := n.Profile
		if n.Signal != "" {
			name += "/" + n.Signal
		}
		fmt.Fprintf(&b, "  not read: %s %s\n", strings.TrimSpace(name), n.Reason)
	}
	describe := func(name, key string) {
		res, ok := c.Results[key]
		if !ok {
			return
		}
		state := string(res.State)
		if res.State == "" {
			state = "not read"
		}
		if res.Reason != "" {
			state += " (" + res.Reason + ")"
		}
		fmt.Fprintf(&b, "\n%s\n  %s\n  %s · %d series · %d samples", name, res.Query, state, len(res.Series), graphSamples(&res))
		if res.State == observability.StateIncomplete {
			b.WriteString(" · CUT: the source's series or sample limit folded this answer; Pods may be missing")
		}
		if res.OK() && graphSamples(&res) == 0 {
			b.WriteString(" · answered, matched nothing: missing data, not zero")
		}
		b.WriteString("\n")
	}
	for i := range c.Signals {
		sig := &c.Signals[i]
		describe(sig.Title+" per Pod · "+sig.About, graphCompareKey(sig.ID, graphPartPod))
		describe(sig.Title+" · limit per Pod", graphCompareKey(sig.ID, graphPartLimit))
		describe(sig.Title+" per container", graphCompareKey(sig.ID, graphPartContainer))
	}
	describe("ZONE OF EACH NODE (instant query at the window end)", graphLookupZones)
	describe("CONTROLLER REVISION OF EACH POD (instant query at the window end)", graphLookupHashes)
	b.WriteString("\nHOW A POD IS CALLED AN OUTLIER\n")
	b.WriteString("  Each Pod is reduced to one number per signal over the window: its peak, the lowest value for a signal that is worse when low, or the rise of a counter.\n")
	fmt.Fprintf(&b, "  A Pod is an OUTLIER when that number is far from the median of all Pods: a modified z-score of at least %.1f on the median absolute deviation, "+
		"at least %.0f times the median or at most 1/%.0f of it, and further from it than the signal's noise floor.\n",
		chart.DefaultPeerScore, chart.DefaultPeerRatio, chart.DefaultPeerRatio)
	fmt.Fprintf(&b, "  Fewer than %d Pods are never compared.\n", chart.DefaultMinPeers)
	b.WriteString("  It states a difference between peers over the same window. It does not say why, and a Pod that is not marked is not thereby healthy.\n")
	b.WriteString("\nGROUPS (g)\n")
	b.WriteString("  node and revision come from the kube_pod_info labels node and created_by_name; zone from kube_node_labels; container from the series.\n")
	b.WriteString("  A group is drawn as the median of its Pods with thin lines for their minimum and maximum, computed here from the per-Pod series.\n")
	b.WriteString("  A Pod whose label is missing is drawn in a group of its own named \"not known\", never left out.\n")
	b.WriteString("\nTHE SHORT PAGE (the default; a shows a chart per Pod and every signal, a again returns)\n")
	b.WriteString("  Each signal in which a Pod is an OUTLIER by the rule above is a group of rows, one row per Pod (or per group, with g). " +
		"The signal whose Pod is furthest from the median of its peers, as a share of the larger of the two values, comes first. " +
		"Signals that have no room as a group are named in one line: ALSO DIFFERS, or ALIKE when no Pod is an OUTLIER in them. ALIKE is not \"identical\", " +
		"and it is said of the Pods that have samples in the signal: where some have none the line counts those that have (3 of 4 pods sampled), " +
		"and the answer says \"its peers\" instead of \"the other 3\".\n")
	fmt.Fprintf(&b, "  The row of a Pod that differs is drawn in its colour at the samples at which it is outside the range of the other Pods, "+
		"at least the signal's noise floor from their median, and at least %.0f times that median or at most 1/%.0f of it. Every other sample is the baseline; × is a time without a sample.\n",
		chart.DefaultPeerRatio, chart.DefaultPeerRatio)
	b.WriteString("  The ▼ columns and the shading behind them are the episodes of the view the page was opened from (:slow), while the window shown lies inside the one that view read. " +
		"Otherwise they are the samples at which the marked row (>) is apart from the others; ↑ and ↓ mark another signal. They state when, not why.\n")
	b.WriteString("  Under a moved cursor the number of each row is its sample at the cursor, and the word over the numbers is that time. " +
		"The number column is as wide as the widest of those samples, so the strips stay where they are when the cursor moves.\n")
	return b.String()
}
