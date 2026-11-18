// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/mattn/go-runewidth"
)

// The calm page is what :timeline opens on. It states what happened in
// order first, then every kind of entry as one row with its marks on one
// time axis: what changed, what went wrong after it, what was already
// wrong before it, and what else is in the window. The moment the answer
// starts from (the rollout, the first change, or the mark set with x) is a
// guide through every row. Under the axis the entries of the selected row,
// or of the column under the time cursor, are listed with their time,
// object and text. The list this view had before is the all-detail page
// (a), drawn by timeline_render.go, and s still shows sources and limits.
//
// Nothing here reads or decides: the entries, their order and what counts
// as a failing signal or a change are the timeline model's. A row draws
// what the API still holds, at the time it began; an empty column says
// that nothing retained began there, not that nothing happened.
const (
	timelineOrderCaveat   = "Order in time, not proof of cause."
	timelineNothingCaveat = "Nothing found is not proof of health."

	timelineChangedLabel = "CHANGED"
	timelineLaterLabel   = "CHANGED LATER"
	timelineThenLabel    = "THEN WENT WRONG"
	timelineWrongLabel   = "WENT WRONG"
	timelineBeforeLabel  = "ALREADY WRONG BEFORE IT"
	timelineAlsoLabel    = "ALSO IN THE WINDOW"
	timelineQuietLabel   = "QUIET"
	timelineNotReadLabel = "NOT READ"
	timelineNoTimeLabel  = "NO TIME"

	// timelineQuietWords is what a quiet line says of its kinds. A source
	// keeps its latest facts, not a history: nothing retained is not the
	// same as nothing happened.
	timelineQuietWords = "none retained"
	// timelineNowWords is what the page says of the kinds that are a state
	// read at the capture (an abnormal status, a failing node condition, a
	// firing alert) when none was read. Their sources hold what is the
	// case now and nothing of what ended before it: a Pod that was not
	// Ready ten minutes ago and is Ready now leaves no entry. The line
	// says that much and its strip is not drawn as quiet.
	timelineNowWords = "none now; what ended is not kept"
	timelineNowFold  = "none now"
	// timelineLeadWidth is the widest names the kit sets in the first
	// column of a quiet line (its maxLead). Longer names push the words
	// after them off the column the other lines write theirs in.
	timelineLeadWidth = 30

	// timelineEventRetention is how long Kubernetes commonly keeps an
	// event. An event row is not drawn as quiet further back than that,
	// unless an older event was read.
	timelineEventRetention = time.Hour
	// timelineWhatWidth is the widest the kit writes what a row is about.
	timelineWhatWidth = 30
)

// timelineClass is what the model says of an entry: a change to desired
// state or configuration, a failing signal, or neither.
type timelineClass int

const (
	timelineChanged timelineClass = iota
	timelineFailed
	timelineOther
)

// timelineKind is one kind of entry: one row of the calm page.
type timelineKind struct {
	key, name string
	source    timeline.Source
	class     timelineClass
	// holds says an entry of the kind is a state that was read at the
	// capture: it holds from when it began until then, and is a span.
	holds bool
	// quiet says the kind is named on the quiet line when it was read and
	// has no entry in the window.
	quiet bool
}

// timelineRowKinds are the rows in the order a group lists them when two
// began at the same time.
var timelineRowKinds = []timelineKind{
	{key: "rollout", name: "rollouts", source: timeline.SourceRollout, class: timelineChanged, quiet: true},
	{key: "config", name: "config", source: timeline.SourceConfig, class: timelineChanged, quiet: true},
	{key: "scaling", name: "scaling", source: timeline.SourceAutoscaler, class: timelineChanged, quiet: true},
	{key: "warning", name: "warnings", source: timeline.SourceEvent, class: timelineFailed, quiet: true},
	{key: "exit", name: "exits", source: timeline.SourceRestart, class: timelineFailed, quiet: true},
	{key: "status", name: "status", source: timeline.SourceCondition, class: timelineFailed, holds: true, quiet: true},
	{key: "autoscaler", name: "autoscaler", source: timeline.SourceAutoscaler, class: timelineFailed, holds: true},
	{key: "node", name: "nodes", source: timeline.SourceNode, class: timelineFailed, holds: true, quiet: true},
	{key: "alert", name: "alerts", source: timeline.SourceAlert, class: timelineFailed, holds: true, quiet: true},
	{key: "event", name: "events", source: timeline.SourceEvent, class: timelineOther},
	{key: "clean-exit", name: "clean exits", source: timeline.SourceRestart, class: timelineOther},
	{key: "autoscaler-ok", name: "autoscaler ok", source: timeline.SourceAutoscaler, class: timelineOther},
	{key: "node-ok", name: "node conditions", source: timeline.SourceNode, class: timelineOther},
	{key: "muted-alert", name: "muted alerts", source: timeline.SourceAlert, class: timelineOther, holds: true},
	{key: "annotation", name: "annotations", source: timeline.SourceAnnotation, class: timelineOther, quiet: true},
}

func timelineClassOf(e *timeline.Entry) timelineClass {
	switch {
	case e.Change:
		return timelineChanged
	case e.Failing:
		return timelineFailed
	}
	return timelineOther
}

// timelineKindOf is the row an entry belongs to: its source, and whether
// the model calls it a change, a failing signal or neither.
func timelineKindOf(e *timeline.Entry) *timelineKind {
	class := timelineClassOf(e)
	for i := range timelineRowKinds {
		if kind := &timelineRowKinds[i]; kind.source == e.Source && kind.class == class {
			return kind
		}
	}
	// A source this page has no row for is still a row, under its own name.
	name := strings.ToLower(string(e.Source))
	return &timelineKind{key: fmt.Sprintf("%s-%d", name, class), name: name, source: e.Source, class: class}
}

// reads are the reads the kind comes from, as the coverage names them.
func (k *timelineKind) reads() []string {
	if k.source == timeline.SourceAnnotation {
		return []string{timelineAnnotationsName}
	}
	return k.source.Reads()
}

// read reports whether every read the kind comes from was made (asked),
// and whether none of those that were made fell short (complete). A kind
// is quiet only when both hold: a read that was not asked, such as an
// Alertmanager that is not declared, is neither quiet nor a gap.
func (k *timelineKind) read(s *timeline.Snapshot) (asked, complete bool) {
	reads := k.reads()
	asked, complete = len(reads) > 0, true
	for _, name := range reads {
		found := false
		for i := range s.Coverage {
			if s.Coverage[i].Source == name {
				found = true
				complete = complete && s.Coverage[i].State == timeline.Complete
			}
		}
		asked = asked && found
	}
	return asked, complete
}

// timelineStoryRow is the entries of one kind in the window. A kind of
// failing signal is two rows when some of its entries began before the
// moment the answer starts from and some after it.
type timelineStoryRow struct {
	key     string
	kind    *timelineKind
	before  bool
	entries []int // oldest first
}

// timelineStory is what the calm page says of an observation in a window:
// the entries drawn, the moment the answer starts from, and what stands on
// each side of it. It is computed from the snapshot and nothing else.
type timelineStory struct {
	s          *timeline.Snapshot
	window     time.Duration
	query      string
	start, end time.Time
	// all are the entries of the window and shown those the filter keeps,
	// both oldest first.
	all, shown []int
	// anchor is the entry the answer starts from, or -1: the mark the user
	// set, else the last rollout before the first failing signal of the
	// window, else the first change before it.
	anchor int
	chosen bool
	// changes are the changes of the window; after and before the failing
	// signals from the anchor on and before it, without the anchor itself.
	// Without an anchor every failing signal is in after.
	changes, after, before []int
	rows                   []timelineStoryRow
	pods                   []string
	// episodes are the episodes of the view the timeline was opened from,
	// drawn as bands when one of them is in the window. partial says the
	// window reaches past the stretch they were looked for in.
	episodes *graphEpisodes
	partial  bool
	// partialQuiet are the rows of the quiet group that are quiet only as
	// far back as events are kept, and quietGroup the group they are in.
	// Its first quietNames rows are lists of names. nowRow is its row of
	// the kinds that are a state of the capture and have none, or -1: the
	// one column that is known of them is the last.
	partialQuiet []timelineCalmRow
	quietGroup   int
	quietNames   int
	nowRow       int
}

// oldestFirst turns entries, which the model keeps newest first, into the
// order they began. Entries of one second have no known order: they keep
// the order the model lists them in.
func (st *timelineStory) oldestFirst(indexes []int) []int {
	out := slices.Clone(indexes)
	sort.SliceStable(out, func(a, b int) bool { return st.s.Entries[out[a]].At.Before(st.s.Entries[out[b]].At) })
	return out
}

// timelineStoryOf sorts the entries of a window into the story of a page.
//
//nolint:gocyclo,funlen // One pass each for the window, the moment the answer starts from, the two sides of it, the rows and the episodes.
func timelineStoryOf(s *timeline.Snapshot, window time.Duration, query string, episodes *graphEpisodes) *timelineStory {
	st := &timelineStory{s: s, window: window, query: query, end: s.CapturedAt, start: s.CapturedAt.Add(-window), anchor: -1, nowRow: -1}
	// The window holds what began in it, as the list under a does, and what
	// began before it and still held at the capture: a state that is the
	// case all through the window belongs on its axis.
	held := func(query string) []int {
		began := s.Visible(window, query)
		if window <= 0 {
			return began
		}
		var out []int
		for _, i := range s.Visible(0, query) {
			e := &s.Entries[i]
			if slices.Contains(began, i) || timelineKindOf(e).holds && e.At.Before(st.start) {
				out = append(out, i)
			}
		}
		return out
	}
	st.all, st.shown = st.oldestFirst(held("")), st.oldestFirst(held(query))
	if window <= 0 {
		// Everything read: the axis starts at the oldest entry.
		st.start = s.CapturedAt.Add(-timeline.Windows[0])
		if len(st.all) > 0 && s.Entries[st.all[0]].At.Before(st.start) {
			st.start = s.Entries[st.all[0]].At
		}
	}
	seen := map[string]bool{}
	for i := range s.Entries {
		if ref := &s.Entries[i].Ref; ref.Kind == "Pod" && !seen[ref.Name] {
			seen[ref.Name] = true
			st.pods = append(st.pods, ref.Name)
		}
	}
	// firstFail is the first failing signal that began in the window. One
	// that began before it and still holds is not what a change of this
	// window is held against.
	firstFail := -1
	for _, i := range st.all {
		e := &s.Entries[i]
		if e.Change {
			st.changes = append(st.changes, i)
		}
		if e.Failing && firstFail < 0 && !e.At.Before(st.start) {
			firstFail = i
		}
	}
	if s.Chosen {
		if slices.Contains(st.all, s.Onset) {
			st.anchor, st.chosen = s.Onset, true
		}
	} else {
		first := -1
		for _, i := range st.changes {
			e := &s.Entries[i]
			if e.At.Before(st.start) || firstFail >= 0 && e.At.After(s.Entries[firstFail].At) {
				continue
			}
			if first < 0 {
				first = i
			}
			if e.Source == timeline.SourceRollout {
				// The changes are oldest first: the last rollout stays.
				st.anchor = i
			}
		}
		if st.anchor < 0 {
			st.anchor = first
		}
	}
	early := func(e *timeline.Entry) bool {
		return st.anchor >= 0 && e.At.Before(s.Entries[st.anchor].At)
	}
	for _, i := range st.all {
		switch e := &s.Entries[i]; {
		case !e.Failing || i == st.anchor:
			// A mark set on a failing signal is the moment itself, not one
			// of the signals that followed it.
		case early(e):
			st.before = append(st.before, i)
		default:
			st.after = append(st.after, i)
		}
	}
	at := map[string]int{}
	for _, i := range st.shown {
		e := &s.Entries[i]
		kind := timelineKindOf(e)
		key, before := kind.key, kind.class == timelineFailed && early(e)
		if before {
			key += ":before"
		}
		row, ok := at[key]
		if !ok {
			row, at[key] = len(st.rows), len(st.rows)
			st.rows = append(st.rows, timelineStoryRow{key: key, kind: kind, before: before})
		}
		st.rows[row].entries = append(st.rows[row].entries, i)
	}
	if episodes != nil {
		inside := false
		for _, episode := range episodes.Intervals {
			inside = inside || episode.End.After(st.start) && !episode.Start.After(st.end)
		}
		if inside {
			st.episodes = episodes
			slack := max(2*time.Minute, st.end.Sub(st.start)/30)
			st.partial = st.start.Before(episodes.Start.Add(-slack)) || st.end.After(episodes.End.Add(slack))
		}
	}
	return st
}

// lead is the entry a row is named after: the one the answer starts from
// when it is on the row, else the first of the window.
func (st *timelineStory) lead(row *timelineStoryRow) int {
	if slices.Contains(row.entries, st.anchor) {
		return st.anchor
	}
	return row.entries[0]
}

// rowOf is the key of the row an entry is drawn on, or empty.
func (st *timelineStory) rowOf(entry int) string {
	for r := range st.rows {
		if slices.Contains(st.rows[r].entries, entry) {
			return st.rows[r].key
		}
	}
	return ""
}

// windowWords names the window in a sentence.
func (st *timelineStory) windowWords() string {
	if st.window <= 0 {
		return "everything read"
	}
	return "the last " + timeline.Span(st.window)
}

// clock is a time as a row and a sentence write it: to the minute, with
// the day when it is not the day of the capture.
func (st *timelineStory) clock(t time.Time) string {
	t, end := t.UTC(), st.end.UTC()
	if t.Year() == end.Year() && t.YearDay() == end.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan02 15:04")
}

// object names what an entry is about: a Pod by its short name, as every
// incident view writes it, anything else as kind/name.
func (st *timelineStory) object(e *timeline.Entry) string {
	if e.Ref.Kind == "Pod" {
		return "pod " + calm.ShortName(e.Ref.Name, st.pods)
	}
	return e.Ref.Short()
}

// timelineHead is the start of an entry's text: what it is, without the
// message that follows it.
func timelineHead(e *timeline.Entry) string {
	text := e.Summary
	if cut := strings.Index(text, ": "); cut > 0 {
		text = text[:cut]
	}
	switch e.Source {
	case timeline.SourceEvent:
		// The type is the row's name, and the count of a repeated event is
		// written beside it.
		text = strings.TrimPrefix(strings.TrimPrefix(text, "Warning "), "Normal ")
		text = strings.Replace(text, fmt.Sprintf(" x%d", e.Count), "", 1)
	case timeline.SourceAlert:
		text = strings.TrimSuffix(strings.TrimSuffix(text, " firing"), " began")
	case timeline.SourceRestart:
		text = strings.TrimPrefix(text, "container ")
	}
	return text
}

// fact is what a row says its entry is: the text of the entry, with the
// object it is about where the text does not name one.
func (st *timelineStory) fact(e *timeline.Entry) string {
	head := timelineHead(e)
	switch e.Source {
	case timeline.SourceRollout:
		return e.Summary
	case timeline.SourceConfig:
		return e.Ref.Name + " " + strings.TrimPrefix(e.Summary, e.Ref.Kind+" ")
	case timeline.SourceAlert, timeline.SourceAnnotation:
		return head
	}
	if e.Ref.Name == "" || strings.Contains(head, " on ") {
		return head
	}
	return head + timelineObjectSep + st.object(e)
}

// brief names an entry in a sentence.
func (st *timelineStory) brief(e *timeline.Entry) string {
	head := timelineHead(e)
	switch {
	case e.Source == timeline.SourceAlert:
		return "alert " + head
	case e.Ref.Name == "" || strings.Contains(head, " on "):
		return head
	}
	return head + " on " + st.object(e)
}

// mark is when an entry is drawn: a moment, or a span for a repeated event
// (first to last occurrence), an annotated region, and a state that was
// read at the capture, which holds from when it began until then.
func (st *timelineStory) mark(kind *timelineKind, entry int) chart.Interval {
	e := &st.s.Entries[entry]
	end := e.At
	switch {
	case kind.holds:
		end = st.end
	case e.Last.After(e.At):
		end = e.Last
	}
	return chart.Interval{Start: e.At, End: end}
}

// eventHorizon is how far back an event row can be drawn as quiet: as far
// as Kubernetes commonly keeps events, or as far as the oldest event that
// was read when that is further.
func (st *timelineStory) eventHorizon() time.Time {
	horizon := st.end.Add(-timelineEventRetention)
	if st.s.EventsRead > 0 && !st.s.OldestEvent.IsZero() && st.s.OldestEvent.Before(horizon) {
		horizon = st.s.OldestEvent
	}
	return horizon
}

// strip draws the marks of a kind on the frame of the page. A column in
// which nothing is known is drawn as not read, never as quiet: all of the
// row when a read it comes from fell short, for events the stretch older
// than events are kept, and the stretch before an entry that counts
// earlier occurrences without a time (the restarts before the one exit a
// container retains: they happened there, at moments nothing states).
func (st *timelineStory) strip(kind *timelineKind, entries []int, f chart.Frame) calm.Strip {
	if f.Width < 1 || !f.End.After(f.Start) {
		return calm.Strip{}
	}
	marks := make([]chart.Interval, 0, len(entries))
	at := make([]time.Time, 0, len(entries))
	// A span no wider than one column is a moment at this scale: it is
	// drawn as one, so that the guide of the page never stands in its place.
	moments := true
	untimed := time.Time{}
	for _, entry := range entries {
		if e := &st.s.Entries[entry]; e.Earlier > 0 && e.At.After(untimed) {
			untimed = e.At
		}
		mark := st.mark(kind, entry)
		moments = moments && f.MarkedCount([]chart.Interval{mark}) <= 1
		marks = append(marks, mark)
		if mark.Start.Before(f.Start) {
			mark.Start = f.Start
		}
		at = append(at, mark.Start)
	}
	_, complete := kind.read(st.s)
	horizon := time.Time{}
	if kind.source == timeline.SourceEvent {
		horizon = st.eventHorizon()
	}
	// known reports whether a column can be quiet.
	known := func(from, to time.Time) bool {
		return complete && (horizon.IsZero() || to.After(horizon)) && !from.Before(untimed)
	}
	marked := f.Marked(marks)
	var unknown []chart.Interval
	for col := range f.Width {
		from, to := f.ColumnSpan(col)
		if marked[col] || known(from, to) {
			continue
		}
		// A run of columns is one stretch: its ends are column edges, so it
		// touches no column that holds a mark.
		if n := len(unknown); n > 0 && unknown[n-1].End.Equal(from) {
			unknown[n-1].End = to
		} else {
			unknown = append(unknown, chart.Interval{Start: from, End: to})
		}
	}
	if moments {
		return calm.Events(at...).Unknown(unknown...)
	}
	return calm.Held(marks...).Unknown(unknown...)
}

// timelineCalmRow is one row of the calm page that the row cursor can
// stand on.
type timelineCalmRow struct {
	key     string
	kind    *timelineKind
	entries []int
	lead    int
	// what is what the row is about, whole; the page cuts it to the column
	// it is given once the columns are known.
	what string
	// group and index place the row on the page.
	group, index int
}

// timelineWhat is what a row is about in a column of a width, with the
// count of the other entries of the row. The count is never what gives
// way: a row that names one alert of five says "+4" at every width, and
// with noIcons too. A text that does not fit ends where the object it is
// about begins, where that keeps a third of it, or between two words,
// where that keeps half; the cut is marked.
func timelineWhat(text string, more, width int, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	suffix := ""
	if more > 0 {
		suffix = fmt.Sprintf(" +%d", more)
	}
	text = timelineWords(text, o)
	room := width - tviewCells(suffix)
	if tviewCells(text) <= room {
		return text + suffix
	}
	mark := timelineWords("…", o)
	if room <= tviewCells(mark) {
		// No room for both: the count says more than a letter of the text.
		return runewidth.Truncate(strings.TrimSpace(suffix), width, "")
	}
	head := runewidth.Truncate(text, room-tviewCells(mark), "")
	keeps := func(at, part int) bool { return at > 0 && part*tviewCells(head[:at]) >= tviewCells(head) }
	switch object := strings.LastIndex(head, timelineWords(timelineObjectSep, o)); {
	case keeps(object, 3):
		// A part of the name of the object says less than none of it.
		head = head[:object]
	case timelineWordEnds(text[len(head):]):
		// The cut is between two words already.
	default:
		if at := strings.LastIndex(head, " "); keeps(at, 2) {
			head = head[:at]
		}
	}
	return strings.TrimRight(head, " ,;:(|·") + mark + suffix
}

// timelineWordEnds reports whether what follows a cut does not continue
// a word: a space or a sign follows, not a letter or a digit. A text that
// was cut and lost the sign after its last word is cut at the same place
// again in a column of exactly that width.
func timelineWordEnds(rest string) bool {
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// timelineObjectSep stands between what an entry is and the object it is
// about, on a row.
const timelineObjectSep = " · "

// timelineRestartCount is the restart count in the text of a container
// exit. A row that counts the exits beside its text does not write it twice.
var timelineRestartCount = regexp.MustCompile(`, restart count \d+`)

// calmRow is a row of the story as the kit draws it: the kind, what its
// lead entry is, when that began, and how often or for how long. The strip
// is drawn once the page is planned and its frame is known.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (st *timelineStory) calmRow(row *timelineStoryRow, o calm.Options) (calm.Row, timelineCalmRow) {
	s := st.s
	lead := st.lead(row)
	e := &s.Entries[lead]
	what := impactArrow(st.fact(e), o)
	out := calm.Row{Subject: row.kind.name, Number: st.clock(e.At)}
	switch {
	case row.kind.holds:
		// How long the first of them has held, to the minute.
		held := st.end.Sub(s.Entries[row.entries[0]].At)
		if held >= time.Minute {
			held = held.Truncate(time.Minute)
		}
		out.Compare = "for " + timeline.Span(held)
	case row.kind.source == timeline.SourceEvent:
		// A repeated event is one entry: its occurrences are counted.
		times := 0
		for _, entry := range row.entries {
			times += max(s.Entries[entry].Count, 1)
		}
		if times > 1 {
			out.Compare = slowTimes(times)
		}
	case row.kind.source == timeline.SourceRestart:
		// A container retains its last exit only. The restarts before it
		// are counted here and have no mark: the strip draws the stretch
		// before the retained exit as not read.
		times := 0
		for _, entry := range row.entries {
			times += 1 + s.Entries[entry].Earlier
		}
		if times > len(row.entries) {
			out.Compare = slowTimes(times)
			what = timelineRestartCount.ReplaceAllString(what, "")
		}
	}
	out.What = timelineWhat(what, len(row.entries)-1, timelineWhatWidth, o)
	switch row.kind.class {
	case timelineChanged:
		out.Color = o.Palette.Info
	case timelineFailed:
		out.Color = o.Palette.Fail
	}
	return out, timelineCalmRow{key: row.key, kind: row.kind, entries: row.entries, lead: lead, what: what}
}

// timelineFold is what a row is called when its group is one line.
func timelineFold(row *calm.Row) string {
	return strings.Join(strings.Fields(row.Subject+" "+row.Number+" "+row.Compare), " ")
}

// timelinePlural writes a count with its noun.
func timelinePlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// anchorWords says what the answer starts from, as a sentence without its
// full stop, and as the labels of its guide from the fullest to the
// shortest.
func (st *timelineStory) anchorWords() (sentence string, labels []string) {
	e := &st.s.Entries[st.anchor]
	at := st.clock(e.At)
	head := timelineHead(e)
	switch {
	case st.chosen:
		return "Your mark is at " + at + ": " + st.brief(e), []string{"your mark " + at, "mark " + at, "mark"}
	case e.Source == timeline.SourceRollout:
		rev, _, _ := strings.Cut(head, " (")
		return strings.ToUpper(rev[:1]) + rev[1:] + " rolled out at " + at, []string{rev + " rolled out " + at, rev + " " + at, rev}
	case e.Source == timeline.SourceConfig:
		verb := strings.TrimPrefix(e.Summary, e.Ref.Kind+" ")
		return e.Ref.Kind + " " + e.Ref.Name + " was " + verb + " at " + at, []string{e.Ref.Short() + " changed " + at, "change " + at, "change"}
	}
	return e.Ref.Short() + " " + head + " at " + at, []string{e.Ref.Short() + " changed " + at, "change " + at, "change"}
}

// verdict is the answer at the top: what the window holds, in order. It
// counts and orders what the model read and claims nothing else.
//
//nolint:funlen // One case per way a window stands: nothing read, nothing in it, a moment to start from, failing signals without one, neither.
func (st *timelineStory) verdict() calm.Verdict {
	s := st.s
	out := calm.Verdict{Caveat: timelineOrderCaveat}
	say := func(format string, args ...any) { out.Sentences = append(out.Sentences, fmt.Sprintf(format, args...)) }
	failing := func(n int) string { return timelinePlural(n, "failing signal", "failing signals") }
	switch {
	case len(s.Entries) == 0:
		out.Kind, out.Caveat = calm.NothingFound, timelineNothingCaveat
		say("No timed entries were read.")
		say("%s", s.EventHorizon())
	case len(st.all) == 0:
		out.Kind, out.Caveat = calm.NothingFound, timelineNothingCaveat
		say("Nothing that was read began in %s.", st.windowWords())
		say("0 shows all %s read.", timelinePlural(len(s.Entries), "entry", "entries"))
	case st.anchor >= 0:
		sentence, _ := st.anchorWords()
		anchor := &s.Entries[st.anchor]
		earlier := 0
		for _, i := range st.changes {
			if i != st.anchor && s.Entries[i].At.Before(anchor.At) {
				earlier++
			}
		}
		if earlier > 0 && !st.chosen {
			sentence += ", after " + timelinePlural(earlier, "other change", "other changes")
		}
		say("%s.", sentence)
		// Entries of one second have no order that is known: what began in
		// the second of the moment did not follow it, it began with it.
		switch n := len(st.after); {
		case n == 0:
			out.Caveat = timelineNothingCaveat
			say("No failing signal was read after it.")
		case s.Entries[st.after[n-1]].At.Sub(anchor.At) < time.Second && n == 1:
			say("1 failing signal began in the same second: %s.", st.brief(&s.Entries[st.after[0]]))
		case s.Entries[st.after[n-1]].At.Sub(anchor.At) < time.Second:
			say("%d failing signals began in the same second, among them %s.", n, st.brief(&s.Entries[st.after[0]]))
		case n == 1:
			say("1 failing signal followed after %s: %s.", timeline.Span(s.Entries[st.after[0]].At.Sub(anchor.At)), st.brief(&s.Entries[st.after[0]]))
		default:
			first, last := &s.Entries[st.after[0]], &s.Entries[st.after[n-1]]
			say("%d failing signals followed within %s, the first %s: %s.", n, timeline.Span(last.At.Sub(anchor.At)),
				timelineAfter(first.At.Sub(anchor.At)), st.brief(first))
		}
		if n := len(st.before); n > 0 {
			say("%s had begun before it.", failing(n))
		}
	case len(st.after) > 0:
		first := &s.Entries[st.after[0]]
		when := "at " + st.clock(first.At)
		if first.At.Before(st.start) {
			when = "before this window, at " + st.clock(first.At)
		}
		if n := len(st.after); n == 1 {
			say("1 failing signal began %s: %s.", when, st.brief(first))
		} else {
			say("%d failing signals began in %s, the first %s: %s.", n, st.windowWords(), when, st.brief(first))
		}
		if n := len(st.changes); n > 0 {
			say("No change was read before it in this window; %s came later.", timelinePlural(n, "change", "changes"))
		} else {
			say("No change was read in this window.")
		}
	default:
		out.Kind, out.Caveat = calm.NothingFound, timelineNothingCaveat
		say("No change and no failing signal began in %s.", st.windowWords())
		say("%s read: neither a change nor a failing signal.", timelinePlural(len(st.all), "other entry was", "other entries were"))
	}
	if s.Chosen && !st.chosen {
		if mark := s.OnsetEntry(); mark != nil {
			say("Your mark at %s is outside this window.", st.clock(mark.At))
		}
	}
	if onset := s.OnsetEntry(); onset != nil && !s.Chosen && len(st.all) > 0 && !slices.Contains(st.all, s.Onset) {
		say("The first failing signal read began earlier, at %s; 0 shows everything read.", st.clock(onset.At))
	}
	if st.episodes != nil {
		n := 0
		for _, episode := range st.episodes.Intervals {
			if episode.End.After(st.start) && !episode.Start.After(st.end) {
				n++
			}
		}
		verb := "are"
		if n == 1 {
			verb = "is"
		}
		say("The %d %s :slow found %s shaded.", n, st.episodes.Label, verb)
	}
	if gaps := s.Gaps(); gaps != "" {
		say("Not read: %s.", gaps)
	}
	if st.query != "" {
		say("Filter %q: %d of %s in this window drawn; Esc clears it.", st.query, len(st.shown), timelinePlural(len(st.all), "entry", "entries"))
	}
	return out
}

// timelineAfter says how long after a moment something began.
func timelineAfter(d time.Duration) string {
	if d < time.Second {
		return "in the same second"
	}
	return "after " + timeline.Span(d)
}

// calmTitle names what the timeline follows, the window, how many of the
// entries read are in it, and how many of the reads were complete.
func (v *timelineView) calmTitle(st *timelineStory) calm.Title {
	object := "Namespace " + v.scope.Namespace
	if v.scope.Kind != "" {
		object = calmObject(v.app.Config.ActiveNamespace(), v.scope.Kind, v.scope.Namespace, v.scope.Name)
	}
	if v.scopeNote != "" {
		object += " (" + v.scopeNote + ")"
	}
	title := calm.Title{View: timelineTitle, Object: object, KeepEnd: v.scope.Kind == "Pod", Unit: "sources"}
	if st != nil {
		s := st.s
		title.Window = v.windowLabel()
		if st.query != "" {
			title.Window += " · filter " + st.query
		}
		if v.cursor < 0 || v.allDetail {
			// The readout of the time cursor takes the room of the count.
			title.Window += fmt.Sprintf(" · %d of %s", len(st.shown), timelinePlural(len(s.Entries), "entry", "entries"))
		}
		title.Read, title.Total = s.CapturedAt, len(s.Coverage)
		for i := range s.Coverage {
			if s.Coverage[i].State == timeline.Complete {
				title.Done++
			}
		}
	}
	switch {
	case v.loading && v.snapshot != nil:
		title.Busy = "reading again…"
	case v.loading:
		title.Busy = "reading…"
	}
	return title
}

// calmNotice is what is true of the page as a whole and is not evidence.
func (v *timelineView) calmNotice(o calm.Options) (text, color string) { //nolint:gocritic // Options are passed by value, as the kit takes them.
	switch {
	case !v.contextCurrent():
		return "Context changed; this observation is retained. Reopen the timeline for the current context.", o.Palette.Warn
	case !v.loading && v.refreshFailure != "" && v.snapshot != nil:
		return "Re-read failed, the earlier observation is shown: " + v.refreshFailure, o.Palette.Warn
	}
	return "", ""
}

// calmBlank is the page before an observation exists: a read that is
// under way, or one that failed.
func (v *timelineView) calmBlank() calm.Page {
	page := calm.Page{Title: v.calmTitle(nil), Keys: (&calmKeyLine{}).parts()}
	if v.refreshFailure == "" || v.loading {
		page.Verdict = calm.Verdict{
			Sentences: []string{"Reading events, rollout history, restarts, configuration metadata, autoscalers and nodes.",
				"One bounded, read-only observation, made because this view was opened."},
		}
		return page
	}
	page.Verdict = calm.Unread("Nothing was read: "+v.refreshFailure+".", "")
	page.Next = []calm.Action{{Key: "r", Label: calmNextReread}}
	return page
}

// calmGroups sorts the rows of the story into the groups of the page, in
// the order the answer tells them. The second value is the rows the cursor
// can stand on, per group.
//
//nolint:funlen,gocritic // One pass over the rows and one over the kinds and the reads; options by value, as the kit takes them.
func (st *timelineStory) calmGroups(o calm.Options) ([]calm.Group, [][]timelineCalmRow) {
	p := o.Palette
	s := st.s
	type set struct {
		group calm.Group
		rows  []timelineCalmRow
	}
	wrong, changed := timelineWrongLabel, timelineChangedLabel
	if st.anchor >= 0 {
		wrong = timelineThenLabel
	} else if len(st.after) > 0 {
		changed = timelineLaterLabel
	}
	sets := map[string]*set{
		changed:             {group: calm.Group{Label: changed}},
		wrong:               {group: calm.Group{Label: wrong, Color: p.Warn}},
		timelineBeforeLabel: {group: calm.Group{Label: timelineBeforeLabel, Role: calm.Secondary}},
		timelineAlsoLabel:   {group: calm.Group{Label: timelineAlsoLabel, Role: calm.Secondary}},
	}
	order := []string{changed, wrong, timelineBeforeLabel, timelineAlsoLabel}
	if st.anchor < 0 {
		order = []string{wrong, changed, timelineAlsoLabel}
		sets[changed].group.Role = calm.Secondary
	}
	// The rows of a group are in the order they began; two that began
	// together keep the order of the kinds.
	rows := make([]*timelineStoryRow, 0, len(st.rows))
	for r := range st.rows {
		rows = append(rows, &st.rows[r])
	}
	rank := func(row *timelineStoryRow) int {
		for i := range timelineRowKinds {
			if &timelineRowKinds[i] == row.kind {
				return i
			}
		}
		return len(timelineRowKinds)
	}
	sort.SliceStable(rows, func(a, b int) bool {
		first, second := s.Entries[rows[a].entries[0]].At, s.Entries[rows[b].entries[0]].At
		if !first.Equal(second) {
			return first.Before(second)
		}
		return rank(rows[a]) < rank(rows[b])
	})
	for _, row := range rows {
		label := timelineAlsoLabel
		switch {
		case row.kind.class == timelineChanged:
			label = changed
		case row.before:
			label = timelineBeforeLabel
		case row.kind.class == timelineFailed:
			label = wrong
		}
		drawn, meta := st.calmRow(row, o)
		into := sets[label]
		into.group.Rows, into.rows = append(into.group.Rows, drawn), append(into.rows, meta)
		into.group.Summary = append(into.group.Summary, timelineFold(&drawn))
	}
	var groups []calm.Group
	var metas [][]timelineCalmRow
	for _, label := range order {
		if into := sets[label]; len(into.rows) > 0 {
			groups, metas = append(groups, into.group), append(metas, into.rows)
		}
	}

	// What was read and has nothing in the window is a dim line of names:
	// the changes, the failing signals and the rest, one line each. A kind
	// whose read fell short is never one of them, and neither is a kind
	// that is a state of the capture: of those the page knows that there is
	// none now, and nothing of the window before that.
	var quiet, unread calm.Group
	quiet = calm.Group{Label: timelineQuietLabel, Role: calm.QuietRole}
	unread = calm.Group{Label: timelineNotReadLabel, Role: calm.NotReadRole, Color: p.Warn}
	names := map[timelineClass][]string{}
	var partial []timelineCalmRow
	var now []string
	horizon := st.eventHorizon()
	for i := range timelineRowKinds {
		kind := &timelineRowKinds[i]
		if !kind.quiet || st.query != "" || slices.ContainsFunc(st.rows, func(row timelineStoryRow) bool { return row.kind == kind }) {
			continue
		}
		switch asked, complete := kind.read(s); {
		case !asked || !complete:
		case kind.source == timeline.SourceEvent && st.start.Before(horizon):
			// Quiet as far back as events are kept, and unknown before: the
			// row draws both and says where one ends.
			quiet.Rows = append(quiet.Rows, calm.Row{Subject: kind.name, What: "none since " + st.clock(horizon) + "; older expired"})
			quiet.Summary = append(quiet.Summary, kind.name+" since "+st.clock(horizon))
			// what is the same in fewer words, for a column too narrow.
			partial = append(partial, timelineCalmRow{key: "quiet:" + kind.key, kind: kind, index: len(quiet.Rows) - 1, what: "since " + st.clock(horizon)})
		case kind.holds:
			now = append(now, kind.name)
		default:
			names[kind.class] = append(names[kind.class], kind.name)
		}
	}
	var named []calm.Row
	var summary []string
	for _, class := range []timelineClass{timelineChanged, timelineFailed, timelineOther} {
		if list := names[class]; len(list) > 0 {
			named = append(named, calm.Row{Subject: strings.Join(list, " · "), What: timelineQuietWords, Strip: calm.Quiet()})
			summary = append(summary, list...)
		}
	}
	lines := len(named)
	if len(now) > 0 {
		// Its strip is drawn once the frame is known: not read all along,
		// but for the column of the capture.
		st.nowRow = len(named)
		named = append(named, calm.Row{Subject: strings.Join(now, " · "), What: timelineNowWords})
		summary = append(summary, strings.Join(now, ", ")+": "+timelineNowFold)
	}
	quiet.Rows, quiet.Summary = append(named, quiet.Rows...), append(summary, quiet.Summary...)
	for i := range partial {
		partial[i].index += len(named)
	}
	for i := range s.Coverage {
		if c := &s.Coverage[i]; c.State != timeline.Complete {
			name := strings.ToLower(c.Source)
			unread.Rows = append(unread.Rows, calm.Row{Subject: name, What: strings.TrimSuffix(c.State+": "+c.Detail, ": ")})
			unread.Summary = append(unread.Summary, name+" "+c.State)
		}
	}
	if st.episodes != nil && st.partial {
		looked := st.clock(st.episodes.Start) + "–" + st.clock(st.episodes.End)
		unread.Rows = append(unread.Rows, calm.Row{Subject: st.episodes.Label, What: ":slow looked for them " + looked + " only"})
		unread.Summary = append(unread.Summary, st.episodes.Label+" outside "+looked)
	}
	if len(quiet.Rows) > 0 {
		groups, metas = append(groups, quiet), append(metas, nil)
		// The rows of event kinds that are quiet only in part are not rows
		// the cursor stands on, but their strips are drawn on the frame.
		st.partialQuiet, st.quietGroup, st.quietNames = partial, len(groups)-1, lines
	}
	if len(unread.Rows) > 0 {
		groups, metas = append(groups, unread), append(metas, nil)
	}
	if n := len(s.Untimed); n > 0 {
		words := fmt.Sprintf("%s no time and cannot be placed; s lists them", timelinePlural(n, "failing status carries", "failing statuses carry"))
		groups, metas = append(groups, calm.Group{Label: timelineNoTimeLabel, Role: calm.NotReadRole, Color: p.Warn, Summary: []string{words}}), append(metas, nil)
	}
	return groups, metas
}
