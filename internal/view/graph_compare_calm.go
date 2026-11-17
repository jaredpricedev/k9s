// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
)

// The calm layout of the per-Pod comparison (docs/graphs.md). The page
// says first which Pod differs from its peers and in how many signals;
// then shows one group per signal in which a Pod differs, one row per Pod
// on a shared time axis, the Pod that differs in its own colour; and names
// every other signal in one line each. It is built from the comparison
// read the page with a chart per Pod is built from, and reads nothing of
// its own.

const (
	graphCompareTitle  = "Compare pods"
	graphCompareCaveat = "A difference, not proof of cause."
	graphDiffersWord   = "differs"
	graphAlsoDiffers   = "ALSO DIFFERS"
	graphNotCompared   = "NOT COMPARED"
	graphPerPodOnly    = "PER POD ONLY"
	graphEpisodesWord  = "episodes"
	// graphCalmRows is how many rows a signal shows when it has more Pods
	// than that and one: the rest are counted in a row of their own and
	// reached page by page.
	graphCalmRows = 4
	// graphCalmGroups is the most signals a page shows as groups.
	graphCalmGroups = 6
)

// graphEpisodes are the episodes of the view a comparison was opened
// from, such as the stalls :slow found: when they were, and the window
// they were looked for in. They are drawn as bands only while the page
// shows a window inside that one; a window that reaches past it has parts
// in which nobody looked for an episode.
type graphEpisodes struct {
	Label      string
	Start, End time.Time
	Intervals  []chart.Interval
}

// graphEpisodeSource is a view that knows of episodes: :slow.
type graphEpisodeSource interface {
	graphEpisodes() *graphEpisodes
}

// graphEpisodes hands the episodes of the request path that was read to a
// comparison opened from :slow, with the window they were looked for in.
func (v *slowView) graphEpisodes() *graphEpisodes {
	report := v.report()
	if report == nil || len(report.Episodes) == 0 {
		return nil
	}
	return &graphEpisodes{Label: graphEpisodesWord, Start: report.Start, End: report.End,
		Intervals: append([]chart.Interval(nil), report.Episodes...)}
}

func (e *graphEpisodes) cover(s *graphSnapshot) bool {
	if e == nil || len(e.Intervals) == 0 {
		return false
	}
	slack := max(2*time.Minute, s.Step)
	return !s.Start.Before(e.Start.Add(-slack)) && !s.End.After(e.End.Add(slack))
}

// graphCalmSignal is one signal of the comparison as a calm page orders
// and shows it.
type graphCalmSignal struct {
	sig *graphSignal
	set graphSeriesSet
	// differs says a Pod (or, by container, a container) is far from its
	// peers in it; far says how far the furthest is.
	differs bool
	far     float64
	// compared is false when the signal had too few peers with samples.
	compared bool
}

// graphCalmSignals orders the signals of a comparison for a calm page:
// the ones in which a Pod differs first, the one whose Pod is furthest
// from the median of its peers before the others (as a share of the
// larger of the two values, so that signals in different units can be
// ordered), then the rest in the order they are read in. A signal that
// has nothing to compare is not in the order: it is named with its state
// in unread, or in perPod when it cannot be told apart by container.
func graphCalmSignals(c *graphCompare, by graphGrouping) (order []graphCalmSignal, unread, perPod []string) {
	for i := range c.Signals {
		sig := &c.Signals[i]
		entry := graphCalmSignal{sig: sig, set: c.series(sig, by == graphByContainer)}
		switch {
		case entry.set.State == "per Pod only":
			perPod = append(perPod, sig.Name)
			continue
		case entry.set.State != "":
			unread = append(unread, sig.Name+" ("+entry.set.State+")")
			continue
		}
		entry.compared = entry.set.Peers.Enough
		for j := range entry.set.Members {
			m := &entry.set.Members[j]
			if !m.Outlier || !entry.compared {
				continue
			}
			entry.differs = true
			d := m.Deviation
			if scale := math.Max(math.Abs(d.Value), math.Abs(d.Value-d.Delta)); scale > 0 {
				entry.far = math.Max(entry.far, math.Abs(d.Delta)/scale)
			}
		}
		order = append(order, entry)
	}
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].differs != order[b].differs {
			return order[a].differs
		}
		return order[a].differs && order[a].far > order[b].far
	})
	for _, n := range c.NotRead {
		if n.Signal != "" {
			unread = append(unread, n.Signal+" (not read per Pod)")
		}
	}
	if c.Base.PodsOmitted > 0 {
		unread = append(unread, graphPlural(c.Base.PodsOmitted, "Pod")+" beyond the matcher limit")
	}
	if t := c.tally(); t.Incomplete > 0 {
		unread = append(unread, graphPlural(t.Incomplete, "answer")+" cut at a limit of the source (s)")
	}
	return order, unread, perPod
}

// named finds a signal by its ID or by the name a caller from another
// view arrives with ("profile/name", or the name alone); nil when the
// comparison has no such signal.
func (c *graphCompare) named(id string) *graphSignal {
	if id == "" {
		return nil
	}
	for i := range c.Signals {
		if c.Signals[i].ID == id {
			return &c.Signals[i]
		}
	}
	if _, name, qualified := strings.Cut(id, "/"); qualified {
		id = name
	}
	for i := range c.Signals {
		if c.Signals[i].Name == id {
			return &c.Signals[i]
		}
	}
	return nil
}

// graphApart says, for every member that differs from its peers over the
// window, at which samples it was apart from them. Whether a Pod differs
// is decided over the whole window (chart.Outliers); this only says when,
// so that its row can be held against the others on the time axis. A
// sample is apart when, among the members that have a sample at that
// time, it is
//
//   - outside the range of the others, on a side the signal can be worse
//     on;
//   - at least the signal's noise floor from their median;
//   - and at least chart.DefaultPeerRatio times that median, or at most
//     the median divided by it, when the median is positive.
//
// One other member with a sample is enough: the question which of two is
// the odd one was answered over the window. A member that does not differ
// has no such samples: its row is the quiet baseline. A counter is
// compared by its rise at that sample, a signal ranked by share of its
// limit by that share, and a container with the same container in the
// other Pods.
func graphApart(set *graphSeriesSet) []map[int64]bool {
	sig := set.Signal
	type reading struct {
		member int
		v      float64
	}
	out := make([]map[int64]bool, len(set.Members))
	at := make(map[int64][]reading)
	for i := range set.Members {
		m := &set.Members[i]
		out[i] = make(map[int64]bool)
		limit := 0.0
		if n := len(m.Limit); set.Shares && n > 0 {
			limit = m.Limit[n-1].V
		}
		for j, p := range m.Points {
			v := p.V
			switch {
			case sig.Rank == graphRankRise:
				if j == 0 {
					continue
				}
				v = math.Max(p.V-m.Points[j-1].V, 0)
			case set.Shares:
				if limit <= 0 {
					continue
				}
				v = p.V / limit
			}
			key := p.T.UnixNano()
			at[key] = append(at[key], reading{member: i, v: v})
		}
	}
	floor := sig.Floor
	if set.Shares {
		floor = graphUnitFloor(chart.Ratio)
	}
	for key, readings := range at {
		for _, r := range readings {
			m := &set.Members[r.member]
			if !m.Outlier {
				continue
			}
			var others []float64
			for _, other := range readings {
				if other.member != r.member && set.Members[other.member].Container == m.Container {
					others = append(others, other.v)
				}
			}
			if len(others) == 0 {
				continue
			}
			sort.Float64s(others)
			n := len(others)
			median := others[n/2]
			if n%2 == 0 {
				median = (others[n/2-1] + others[n/2]) / 2
			}
			delta := r.v - median
			above := delta > 0
			switch {
			case math.Abs(delta) < floor || delta == 0:
			case above && (sig.Direction == chart.Below || r.v <= others[n-1]):
			case !above && (sig.Direction == chart.Above || r.v >= others[0]):
			case median > 0 && above && r.v < chart.DefaultPeerRatio*median:
			case median > 0 && !above && r.v > median/chart.DefaultPeerRatio:
			default:
				out[r.member][key] = true
			}
		}
	}
	return out
}

// graphRises are the times a counter rose between two neighbouring
// samples.
func graphRises(points []chart.Point, step time.Duration) []time.Time {
	var out []time.Time
	for j := 1; j < len(points); j++ {
		if points[j].V > points[j-1].V && points[j].T.Sub(points[j-1].T) <= step+step/2 {
			out = append(out, points[j].T)
		}
	}
	return out
}

// graphCalmEvents is the strip of a counter: a mark where it rose in any
// of the series, and the gap glyph before the first and after the last
// sample of them, where a rise cannot be known not to have happened.
func graphCalmEvents(series [][]chart.Point, s *graphSnapshot) calm.Strip {
	var (
		events      []time.Time
		first, last time.Time
	)
	for _, points := range series {
		if len(points) == 0 {
			continue
		}
		events = append(events, graphRises(points, s.Step)...)
		if first.IsZero() || points[0].T.Before(first) {
			first = points[0].T
		}
		if points[len(points)-1].T.After(last) {
			last = points[len(points)-1].T
		}
	}
	if first.IsZero() {
		return calm.UnreadStrip()
	}
	strip := calm.Events(events...)
	gap := s.Step + s.Step/2
	if first.Sub(s.Start) > gap {
		strip = strip.Unknown(chart.Interval{Start: s.Start, End: first.Add(-s.Step)})
	}
	if s.End.Sub(last) > gap {
		strip = strip.Unknown(chart.Interval{Start: last, End: s.End})
	}
	return strip
}

// graphCalmStripOf is the strip of one row of the comparison. A sample at
// which the row is apart from its peers is drawn as a level in the row's
// colour, every other sample as the quiet baseline, a time without a
// sample as the gap glyph. For a signal that is worse when low the level
// is the shortfall from the highest value of the group, so that a dip
// stands up.
func graphCalmStripOf(sig *graphSignal, points []chart.Point, apart map[int64]bool, top float64) calm.Strip {
	if len(points) == 0 {
		return calm.UnreadStrip()
	}
	// A sample that is not apart is written below zero: it is sampled, and
	// under the rule.
	marked := make([]chart.Point, len(points))
	for j, p := range points {
		v := -1.0
		if apart[p.T.UnixNano()] {
			v = p.V
			if sig.Rank == graphRankLow {
				v = top - p.V
			}
			v = math.Max(v, 0)
		}
		marked[j] = chart.Point{T: p.T, V: v}
	}
	return calm.Levels(marked, func(v float64) bool { return v >= 0 }).Top(top)
}

// graphCalmRow is one row of a signal with what the page needs beside it.
type graphCalmRow struct {
	row     calm.Row
	differs bool
	// pod is the Pod Enter opens from the row: the Pod itself, or the Pod
	// of a group that differs; short is its short name.
	pod, short string
	// apart are the sample times at which the row is apart from its peers.
	apart map[int64]bool
}

// graphCalmRowsOf builds the rows of one signal: one per Pod (or, by
// container, per container), or one per group when the series are
// regrouped. The number is what ranks the row, or under a moved cursor
// its value there.
func graphCalmRowsOf(c *graphCompare, entry *graphCalmSignal, by graphGrouping, when graphCalmWhen, pg *graphPage) []graphCalmRow {
	s, sig, set := c.Base, entry.sig, &entry.set
	pal := pg.Palette
	apart := graphApart(set)
	top := 0.0
	for i := range set.Members {
		for _, p := range set.Members[i].Points {
			top = math.Max(top, p.V)
		}
	}
	grid := chart.Frame{Start: s.Start, End: s.End, Step: s.Step, Width: 1}
	number := func(rank string, points []chart.Point) string {
		if !when.moved {
			return graphWords(rank, pg.ASCII)
		}
		if v, ok := grid.ValueAt(&chart.Series{Points: points}, when.at); ok {
			return sig.Unit.Format(v)
		}
		return graphWords(graphNoSample, pg.ASCII)
	}
	var rows []graphCalmRow
	if by == graphByPod || by == graphByContainer {
		for i := range set.Members {
			m := &set.Members[i]
			_, _, rank := graphValueWords(sig, set.Shares, m.Value, m.Peak, m.Last, m.Share)
			r := graphCalmRow{pod: m.Pod, short: strings.TrimSuffix(m.Name, "/"+m.Container), apart: apart[i], differs: m.Outlier && entry.compared,
				row: calm.Row{Subject: m.Name, What: m.Node, Number: number(rank, m.Points), Strip: graphCalmStripOf(sig, m.Points, apart[i], top)}}
			if sig.Rank == graphRankRise {
				r.row.Strip = graphCalmEvents([][]chart.Point{m.Points}, s)
			}
			if !m.sampled() {
				r.row.Number = graphNoSamples
			}
			if r.differs {
				r.row.Color, r.row.Tag = pal.Fail, graphDiffersWord
			}
			rows = append(rows, r)
		}
	} else {
		index := make(map[string]int, len(set.Members))
		for i := range set.Members {
			index[set.Members[i].Pod] = i
		}
		for _, g := range c.groups(set, by) {
			_, _, rank := graphValueWords(sig, set.Shares, g.Value, g.Peak, graphLastOf(g.Mid), g.Value)
			// A group is drawn by the member furthest out at each time: its
			// highest, or for a signal that is worse when low its lowest.
			points, union := g.High, make(map[int64]bool)
			if sig.Rank == graphRankLow {
				points = g.Low
			}
			// Under a moved cursor the number is read off the series that is
			// drawn: the member furthest out at that sample.
			r := graphCalmRow{apart: union, row: calm.Row{Subject: g.Title, What: graphPlural(g.Pods, "pod"), Number: number(rank, points)}}
			series := make([][]chart.Point, 0, len(g.Members))
			for j := range g.Members {
				m := &g.Members[j]
				series = append(series, m.Points)
				for key := range apart[index[m.Pod]] {
					union[key] = true
				}
				if m.Outlier && entry.compared && r.pod == "" {
					r.pod, r.short = m.Pod, m.Name
					if len(g.Outliers) == 1 {
						// One Pod of the group differs: the number is its own,
						// the one the word beside it is about.
						_, _, own := graphValueWords(sig, set.Shares, m.Value, m.Peak, m.Last, m.Share)
						r.row.Number = number(own, m.Points)
					}
				}
			}
			r.row.Strip = graphCalmStripOf(sig, points, union, top)
			if sig.Rank == graphRankRise {
				// The rises of every Pod of the group are its events.
				r.row.Strip = graphCalmEvents(series, s)
			}
			if math.IsNaN(g.Value) {
				r.row.Number = graphNoSamples
			}
			if n := len(g.Outliers); n > 0 && entry.compared {
				r.differs = true
				r.row.Color, r.row.Tag, r.row.Compare = pal.Fail, graphDiffersWord, g.Outliers[0]
				if n > 1 {
					r.row.Tag, r.row.Compare = "differ", fmt.Sprintf("%d pods", n)
				}
			}
			rows = append(rows, r)
		}
	}
	// The rows that differ first, worst first; the others by name, so that
	// a peer is on the same row in every signal.
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].differs != rows[b].differs {
			return rows[a].differs
		}
		return !rows[a].differs && rows[a].row.Subject < rows[b].row.Subject
	})
	return rows
}

// graphCalmPaged cuts the rows of a signal to what a calm page shows of
// it. Up to graphCalmRows and one all are shown. Beyond that the rows
// that differ stay (three at most), the others take turns page by page,
// and one row counts what is not shown.
func graphCalmPaged(rows []graphCalmRow, page int, rest string) (shown []graphCalmRow, pages int) {
	if len(rows) <= graphCalmRows+1 {
		return rows, 1
	}
	fixed := 0
	for fixed < len(rows) && fixed < graphCalmRows-1 && rows[fixed].differs {
		fixed++
	}
	size := graphCalmRows - fixed
	tail := rows[fixed:]
	pages = (len(tail) + size - 1) / size
	page = min(max(page, 0), pages-1)
	turn := tail[page*size : min((page+1)*size, len(tail))]
	shown = append(append(shown, rows[:fixed]...), turn...)
	hidden, differ := len(tail)-len(turn), 0
	for i := range tail {
		if tail[i].differs && (i < page*size || i >= (page+1)*size) {
			differ++
		}
	}
	count := calm.Row{Subject: fmt.Sprintf("+%d more", hidden), What: rest}
	if differ > 0 {
		count.What = fmt.Sprintf("%d of them differ", differ)
	}
	return append(shown, graphCalmRow{row: count}), pages
}

// graphApartBands are the stretches in which a row was apart from its
// peers, stated as episodes are: a sample stands for the step that ends
// at it.
func graphApartBands(apart map[int64]bool, step time.Duration) []chart.Interval {
	keys := make([]int64, 0, len(apart))
	for key := range apart {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	var out []chart.Interval
	for _, key := range keys {
		t := time.Unix(0, key).UTC()
		if n := len(out); n > 0 && !t.Add(-step).After(out[n-1].End) {
			out[n-1].End = t
			continue
		}
		out = append(out, chart.Interval{Start: t.Add(-step), End: t})
	}
	return out
}

func graphOthers(n int) string {
	switch n {
	case 1:
		return "the other one"
	case 2:
		return "the other two"
	}
	return fmt.Sprintf("the other %d", n)
}

// graphCompareVerdict is the answer of the comparison: which Pod differs
// from its peers and in how many of the signals that were read. It says
// nothing about why, and it says when there were too few Pods or too few
// readings to tell.
func graphCompareVerdict(c *graphCompare, st *graphStanding) calm.Verdict {
	pods := len(c.pods())
	t := c.tally()
	unread := ""
	if st.Read < st.Total {
		unread = fmt.Sprintf("%d of %d signals had no reading.", st.Total-st.Read, st.Total)
	}
	switch {
	case pods < chart.DefaultMinPeers:
		return calm.Verdict{Kind: calm.NothingFound, Caveat: "Nothing found is not proof of health.", Sentences: []string{
			fmt.Sprintf("%s: too few to call one different.", graphPlural(pods, "Pod")),
			fmt.Sprintf("%d are needed.", chart.DefaultMinPeers),
		}}
	case st.Read == 0:
		return calm.Unread("No signal had samples for these Pods.", "Nothing can be compared: that is missing data, not health.")
	case len(st.Pods) == 0:
		v := calm.Nothing(fmt.Sprintf("No Pod differs from its peers in the %s read.", graphPlural(st.Read, "signal")), t.Asked-t.Failed, t.Asked)
		if unread != "" {
			v.Sentences = append(v.Sentences[:1], unread)
		}
		return v
	case len(st.Pods) == 1:
		pod := st.Pods[0]
		v := calm.Verdict{Caveat: graphCompareCaveat, Sentences: []string{
			fmt.Sprintf("Pod %s differs from %s in %d of %s.", pod, graphOthers(pods-1), len(st.Signals[pod]), graphPlural(st.Read, "signal")),
			"No other Pod differs.",
		}}
		if unread != "" {
			v.Sentences[1] += " " + unread
		}
		return v
	}
	parts := make([]string, 0, len(st.Pods))
	for i, pod := range st.Pods {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("%d more", len(st.Pods)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%s in %d", pod, len(st.Signals[pod])))
	}
	v := calm.Verdict{Caveat: graphCompareCaveat, Sentences: []string{
		fmt.Sprintf("%d of %d Pods differ from their peers: %s of %s.", len(st.Pods), pods, strings.Join(parts, ", "), graphPlural(st.Read, "signal")),
	}}
	if unread != "" {
		v.Sentences = append(v.Sentences, unread)
	}
	return v
}

// graphCalmLabel heads the group of a signal: its title, and for a signal
// of a request-path profile what the profile says it measures.
func graphCalmLabel(sig *graphSignal) string {
	label := strings.ToUpper(sig.Title)
	if sig.Profile == "" {
		return label
	}
	about := strings.TrimSuffix(sig.About, ", "+sig.Profile+" profile")
	if about == "" || strings.EqualFold(about, sig.Title) {
		return label
	}
	return label + " · " + about
}

// graphCalmAlso names a signal in which a Pod differs and that has no
// group on the page: the Pod, its number and the median of its peers.
func graphCalmAlso(entry *graphCalmSignal) string {
	sig, set := entry.sig, &entry.set
	for i := range set.Members {
		m := &set.Members[i]
		if !m.Outlier {
			continue
		}
		_, _, rank := graphValueWords(sig, set.Shares, m.Value, m.Peak, m.Last, m.Share)
		text := sig.Name + " (" + strings.TrimSuffix(m.Name, "/"+m.Container) + " " + rank
		if set.Peers.Enough && !math.IsNaN(set.Peers.Median) {
			unit := sig.Unit
			if set.Shares {
				unit = chart.Ratio
			}
			text += ", peers " + unit.Format(set.Peers.Median)
		}
		return text + ")"
	}
	return sig.Name
}

// graphCompareCalmPage builds the calm page of the comparison with count
// signals shown as groups. It reads pg.Signal, pg.Grouping and pg.Peers
// and writes back the signal it settled on, the page of rows shown, how
// many such pages there are, and the Pod that Enter opens.
func graphCompareCalmPage(s *graphSnapshot, c *graphCompare, pg *graphPage, count int) (page calm.Page, groups int) {
	o := pg.calmOptions()
	pal := o.Palette
	by := pg.Grouping
	standing := c.standing()
	when := graphCalmAt(s, pg)
	t := c.tally()
	pods := len(c.pods())

	title := calm.Title{View: graphCompareTitle, Object: s.Target.Label(), Window: graphPlural(pods, "pod") + " · last " + graphSpan(s.Window), KeepEnd: true,
		Read: c.ReadAt, Done: t.Asked - t.Failed, Total: t.Asked}
	if by != graphByPod {
		title.Window = graphPlural(pods, "pod") + " · by " + by.String() + " · last " + graphSpan(s.Window)
	}
	if pg.Busy != "" {
		title.Busy = graphReadingWord
	}
	page = calm.Page{Title: title, Verdict: graphCompareVerdict(c, &standing), Frame: chart.Frame{Start: s.Start, End: s.End, Step: s.Step}, NumberAfter: true}
	if when.moved {
		page.Bands.Cursor = when.at
	}
	keys := []string{"↑↓ signal", "←→ time", "1-4 window", calm.DetailKey + " show every signal"}
	finish := func() {
		page.Next = append(page.Next, calm.Action{Key: "g", Label: "group by " + graphNextGroupings(by)})
		if pg.Offers.Probe {
			page.Next = append(page.Next, calm.Action{Key: "B", Label: "probe the pods now"})
		}
		page.Keys = append(keys, "p overlaid lines", "r re-read", "s queries", "Esc back")
	}

	if by == graphByContainer && !c.Containers {
		// The per-container series are read the first time they are asked
		// for; until then there is nothing to group.
		sentence := pg.Busy
		if sentence == "" {
			sentence = "The per-container series have not been read. r reads again."
		}
		page.Verdict = calm.Verdict{Sentences: []string{sentence}}
		finish()
		return page, 0
	}
	order, unread, perPod := graphCalmSignals(c, by)
	differing := 0
	for i := range order {
		if order[i].differs {
			differing++
		}
	}
	selected := 0
	if sig := c.named(pg.Signal); sig != nil {
		for i := range order {
			if order[i].sig.ID == sig.ID {
				selected = i
			}
		}
	}
	// The page shows the signals in which a Pod differs, as many as it was
	// asked for, scrolled so that the marked one is among them. A marked
	// signal in which no Pod differs is shown after them, in the place of
	// the last: the page stays about what differs.
	count = max(count, 1)
	var shownAt []int
	switch {
	case len(order) == 0:
	case selected < differing:
		first := max(selected-count+1, 0)
		for i := first; i < min(first+count, differing); i++ {
			shownAt = append(shownAt, i)
		}
	default:
		for i := range min(count-1, differing) {
			shownAt = append(shownAt, i)
		}
		shownAt = append(shownAt, selected)
	}
	onPage := make(map[int]bool, len(shownAt))
	pg.PeerPages, pg.Open = 1, ""
	rest := "like their peers"
	if by != graphByPod && by != graphByContainer {
		rest = "groups"
	}
	var open string
	head := ""
	for _, i := range shownAt {
		onPage[i] = true
		entry := &order[i]
		all := graphCalmRowsOf(c, entry, by, when, pg)
		_, pages := graphCalmPaged(all, 0, rest)
		pg.PeerPages = max(pg.PeerPages, pages)
		shown, _ := graphCalmPaged(all, min(pg.Peers, pg.PeerPages-1), rest)
		group := calm.Group{Label: graphCalmLabel(entry.sig), Color: pal.Accent}
		column, _ := graphRankWords(entry.sig, entry.set.Shares)
		word := strings.ToLower(column)
		if when.moved {
			word = when.word
		}
		if word != head {
			// The word over the numbers is written where it changes.
			group.Head, head = word, word
		}
		for j := range shown {
			group.Rows = append(group.Rows, shown[j].row)
		}
		if i == selected && len(shown) > 0 {
			group.Rows[0].Selected = true
			pg.Open, open = shown[0].pod, shown[0].short
			if len(shown[0].apart) > 0 && shown[0].differs {
				page.Bands.Label = shown[0].short + " differs"
				page.Bands.Episodes = graphApartBands(shown[0].apart, s.Step)
			}
		}
		page.Groups = append(page.Groups, group)
		groups++
	}
	if pg.Episodes.cover(s) {
		// The episodes of the view the page was opened from are what the
		// rows are held against.
		page.Bands.Label, page.Bands.Episodes = pg.Episodes.Label, pg.Episodes.Intervals
	}
	pg.Peers = min(max(pg.Peers, 0), pg.PeerPages-1)
	if len(order) > 0 {
		pg.Signal = order[selected].sig.ID
	}

	var also, alike, uncompared []string
	for i := range order {
		if onPage[i] {
			continue
		}
		switch entry := &order[i]; {
		case entry.differs:
			also = append(also, graphCalmAlso(entry))
		case entry.compared:
			alike = append(alike, entry.sig.Name)
		default:
			uncompared = append(uncompared, entry.sig.Name)
		}
	}
	if len(also) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphAlsoDiffers, Role: calm.Secondary, Summary: also})
	}
	if len(alike) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: fmt.Sprintf("ALIKE ON ALL %d", pods), Role: calm.QuietRole, Summary: alike})
	}
	if len(perPod) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphPerPodOnly, Role: calm.QuietRole, Summary: perPod})
	}
	if len(uncompared) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphNotCompared, Role: calm.NotReadRole, Color: pal.Warn, Summary: uncompared})
	}
	if len(unread) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: graphNotReadLabel, Role: calm.NotReadRole, Color: pal.Warn, Summary: unread})
	}

	if pg.Open != "" {
		page.Next = append(page.Next, calm.Action{Key: "Enter", Label: "open " + open})
	}
	if pg.PeerPages > 1 {
		keys = append(keys, fmt.Sprintf("PgUp/PgDn pods (page %d of %d)", pg.Peers+1, pg.PeerPages))
	}
	finish()
	return page, groups
}

// graphNextGroupings names the groupings g steps to from one.
func graphNextGroupings(by graphGrouping) string {
	names := make([]string, 0, int(graphGroupings)-1)
	for step := 1; step < int(graphGroupings); step++ {
		names = append(names, ((by + graphGrouping(step)) % graphGroupings).String())
	}
	return strings.Join(names, " / ")
}

// renderGraphCompareCalm is the calm page of the comparison for a size:
// the default page of the per-Pod view. Every line fits Width. It shows as
// many signals as groups as the size has room for without leaving a row
// out, and names the others in one line.
func renderGraphCompareCalm(s *graphSnapshot, c *graphCompare, pg *graphPage) []string {
	if s == nil {
		return renderGraphCalm(s, pg)
	}
	if failure := s.failure(); failure != nil {
		page := graphCalmFailure(graphCompareTitle, s, failure, pg)
		return graphCalmLines(&page, pg)
	}
	if c == nil || c.Base != s {
		sentence := pg.Busy
		if sentence == "" {
			sentence = "The per-Pod series of this window have not been read. r reads again."
		}
		page := graphCalmWaiting(graphCompareTitle, s.Target.Label(), s.Window, sentence)
		if pg.Busy == "" {
			page.Title.Busy = ""
		}
		page.Keys = []string{"p overlaid lines", "r re-read", "Esc back"}
		return graphCalmLines(&page, pg)
	}
	o := pg.calmOptions()
	width, height := max(pg.Width, 20), max(pg.Height, 3)
	var (
		page calm.Page
		plan calm.Plan
	)
	// As many signals are groups as the size holds without giving anything
	// up: the blank rows between them are what keeps them apart. A second
	// group is worth the blank rows around the verdict and the keys; no
	// group is worth a row that is left out.
	blanksOnly := func(dropped []string) bool {
		for _, what := range dropped {
			if !strings.Contains(what, "blank rows") {
				return false
			}
		}
		return true
	}
	for count := graphCalmGroups; ; {
		var groups int
		page, groups = graphCompareCalmPage(s, c, pg, count)
		plan = calm.Lay(&page, width, height, o)
		fits := len(plan.Missing) == 0 && !plan.TooSmall
		if groups <= 1 || fits && len(plan.Dropped) == 0 || fits && groups == 2 && blanksOnly(plan.Dropped) {
			break
		}
		// One signal fewer as a group; it is named in one line instead.
		count = groups - 1
	}
	pg.Dropped, pg.Missing = plan.Dropped, plan.Missing
	return calm.Render(&page, &plan, o).Lines()
}
