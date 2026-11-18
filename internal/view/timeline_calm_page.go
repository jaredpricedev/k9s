// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// This file lays the calm page of :timeline out and draws it: the page of
// the story (timeline_calm.go), the guide of the moment the answer starts
// from, the two cursors, and under the axis the entries the cursor is on.
// The row cursor and the time cursor are put on after the page is planned,
// and the list under the axis has one height for a size of screen, so that
// no row and no column moves when a cursor does.

// bands are what runs through every row: the episodes of the view the
// timeline was opened from, and the moment the answer starts from. That
// moment is a guide with its marker, as the rollout is in :impact; beside
// episodes it is a labelled moment with its key on the axis, as a rollout
// is on a Pod's graph. labels are the words of the marker row, the fullest
// first.
func (st *timelineStory) bands(o calm.Options) (bands calm.Bands, labels []string) { //nolint:gocritic // Options are passed by value, as the kit takes them.
	if st.episodes != nil {
		bands.Label, bands.Episodes = st.episodes.Label, st.episodes.Intervals
	}
	if st.anchor < 0 {
		return bands, nil
	}
	e := &st.s.Entries[st.anchor]
	if e.At.Before(st.start) || e.At.After(st.end) {
		return bands, nil
	}
	_, words := st.anchorWords()
	if st.episodes == nil {
		bands.Guide, bands.Episodes = true, []chart.Interval{{Start: e.At, End: e.At}}
		return bands, words
	}
	key, legend := 'C', words[0]
	switch {
	case st.chosen:
		key, legend = 'x', legend+" · "+st.brief(e)
	case e.Source == timeline.SourceRollout:
		key = 'R'
		if _, changes, ok := strings.Cut(e.Summary, ": "); ok {
			legend += " · " + changes
		}
	default:
		legend += " · " + timelineHead(e)
	}
	bands.Marks = []chart.Mark{{Key: key, T: e.At, Label: impactArrow(legend, o)}}
	return bands, nil
}

// timelineCalmKeys is the second key line, in the order every incident
// view writes it. s is the sources and limits here, where the other views
// have their queries; a is on the next line.
func timelineCalmKeys(st *timelineStory) []string {
	keys := calmKeyLine{Window: calmKeyWindow + ", 0 all", Reread: true, Own: []string{calm.QueriesKey + " sources"}}
	if len(st.rows) > 0 {
		keys.Move, keys.Time = "↑↓ row", true
		keys.Own = append(keys.Own, "n next entry", "x mark")
	}
	keys.Own = append(keys.Own, "/ filter")
	return keys.parts()
}

// calmNext are the likely next steps, and only those that would run: what
// Enter opens for the entry the cursor is on, the list of every entry, the
// graph, and Grafana. a is the second of them: a short screen gives the
// second key line up, and a narrow one the last of these.
func (v *timelineView) calmNext(st *timelineStory) []calm.Action {
	var next []calm.Action
	graph := "graph this " + strings.ToLower(v.scope.Kind)
	if e := v.focusedEntry(); e != nil {
		switch {
		case e.Source == timeline.SourceAnnotation:
			if e.Ref.Name != "" {
				next = append(next, calm.Action{Key: "Enter", Label: "open its dashboard"})
			}
		case e.Ref.Resource != "" && e.Ref.Name != "":
			name := e.Ref.Short()
			if e.Ref.Kind == "Pod" {
				name = calm.ShortName(e.Ref.Name, st.pods)
			}
			next, graph = append(next, calm.Action{Key: "Enter", Label: "open " + name}), "graph it"
		}
	}
	next = append(next, calm.Action{Key: calm.DetailKey, Label: "full list"})
	if graphAvailability(v.app, v.SelectedResource()) == "" {
		next = append(next, calm.Action{Key: "m", Label: graph})
	}
	if grafanaUnavailableShort(v.app, v) == "" {
		next = append(next, calm.Action{Key: calmGrafanaKeyName, Label: calmNextGrafana})
	}
	return next
}

// calmPage is the page of a story, before its strips and its cursors are
// drawn on it. metas are the rows the row cursor can stand on, per group.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (v *timelineView) calmPage(st *timelineStory, o calm.Options) (page calm.Page, metas [][]timelineCalmRow, labels []string) {
	page = calm.Page{Title: v.calmTitle(st), Verdict: st.verdict(), Frame: chart.Frame{Start: st.start, End: st.end}}
	page.Groups, metas = st.calmGroups(o)
	page.Bands, labels = st.bands(o)
	page.Keys, page.Next = timelineCalmKeys(st), v.calmNext(st)
	return page, metas, labels
}

// timelineDetailRows is the most rows the list under the axis takes.
func timelineDetailRows(height int) int {
	switch {
	case height >= 30:
		return 7
	case height >= 24:
		return 5
	}
	return 4
}

// timelineDetailFits reports whether a page planned with fewer rows, to
// leave room for the list under the axis, still shows what the page shows
// without it. The list may take the blank rows around the answer and the
// keys, and fold what is quiet to one line; it never costs a row, a
// strip, a group or a key line. With reasons it does not cost the reason
// a read fell short either: the not-read group stays as it is.
func timelineDetailFits(page *calm.Page, base, next *calm.Plan, reasons bool) bool {
	if next.TooSmall || next.TextRows != base.TextRows || (next.Layout.Strip > 0) != (base.Layout.Strip > 0) || len(next.Missing) > len(base.Missing) {
		return false
	}
	for g := range page.Groups {
		if next.Hidden[g] != base.Hidden[g] {
			return false
		}
		if role := page.Groups[g].Role; role == calm.QuietRole || role == calm.NotReadRole && !reasons {
			continue
		}
		if next.Folded[g] != base.Folded[g] || next.Rows[g] != base.Rows[g] {
			return false
		}
	}
	for _, what := range []string{"the next line", "the second key line", "blank rows before groups", "blank rows between subjects"} {
		if slices.Contains(next.Dropped, what) && !slices.Contains(base.Dropped, what) {
			return false
		}
	}
	return true
}

// timelineCalmPlan plans the page and the rows of the list under its axis.
// The list takes the rows it needs, as far as the page can spare them,
// with a blank row beside it where there is one. It leaves the reasons of
// the reads that fell short on the page when there is room for both, and
// folds them to one line, which still names each read and its state, when
// that is the only way to list anything. A page that can spare no row has
// no list, and a shows every entry.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func timelineCalmPlan(page *calm.Page, need, width, height int, o calm.Options) (plan calm.Plan, list int, gap bool) {
	base := calm.Lay(page, width, height, o)
	if need <= 0 || base.TooSmall {
		return base, 0, false
	}
	for _, reasons := range []bool{true, false} {
		for size := min(timelineDetailRows(height), need); size >= 1; size-- {
			for _, blank := range []bool{true, false} {
				room := height - size
				if blank {
					room--
				}
				if room < 3 {
					continue
				}
				if next := calm.Lay(page, width, room, o); timelineDetailFits(page, &base, &next, reasons) {
					return next, size, blank
				}
			}
		}
	}
	return base, 0, false
}

// detailNeed is the most rows the list under the axis has a use for: its
// heading, the entries of the longest row, and what it adds under the
// entry the keys act on. The list is that high whatever the cursors are
// on, so that no cursor moves the page.
func (st *timelineStory) detailNeed(width int, o calm.Options) int { //nolint:gocritic // Options are passed by value, as the kit takes them.
	need := 0
	for r := range st.rows {
		row := &st.rows[r]
		cols := st.detailCols(row.entries, false, width)
		for _, entry := range row.entries {
			_, rest := st.entryLine(entry, true, true, cols, o)
			need = max(need, 1+len(row.entries)+len(st.entryMore(entry, rest, cols, width, o)))
		}
	}
	return need
}

// timelineFitNames writes names in a width: as many as fit, and the count
// of the rest.
func timelineFitNames(names []string, width int) string {
	const sep = " · "
	text := strings.Join(names, sep)
	for shown := len(names) - 1; shown >= 1 && tviewCells(text) > width; shown-- {
		text = strings.Join(names[:shown], sep) + fmt.Sprintf(" +%d more", len(names)-shown)
	}
	return text
}

// renderCalm draws the calm page into the area the view has.
func (v *timelineView) renderCalm() {
	o := calmOptions(v.app)
	width, height := v.viewWidth(), calmHeight(v.height)
	notice, color := v.calmNotice(o)
	if notice != "" {
		height--
	}
	var lines []string
	if v.snapshot == nil {
		page := v.calmBlank()
		plan := calm.Lay(&page, width, height, o)
		v.story, v.calmRows, v.frame, v.cursor, v.dropped = nil, nil, chart.Frame{}, -1, plan.Dropped
		lines = calm.Render(&page, &plan, o).Lines()
	} else {
		lines = v.calmObservation(width, height, o)
	}
	if notice != "" && len(lines) > 0 {
		// Under the title, where it is read before the answer.
		lines = append(lines[:1], append([]string{calm.NoticeLine(notice, color, width, o)}, lines[1:]...)...)
	}
	v.calm.SetText(strings.Join(lines, "\n"))
	v.calm.ScrollToBeginning()
}

// calmObservation lays the page of the retained observation out and draws
// it. Everything a cursor changes is put on after the page is planned.
func (v *timelineView) calmObservation(width, height int, o calm.Options) []string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	st := timelineStoryOf(v.snapshot, v.window, v.query, v.episodes)
	v.story = st
	page, metas, labels := v.calmPage(st, o)
	plan, list, gap := timelineCalmPlan(&page, st.detailNeed(width, o), width, height, o)
	strips := plan.Layout.Strip > 0 && !plan.TextRows && !plan.TooSmall
	v.frame = chart.Frame{}
	if strips {
		v.frame = plan.Frame
	} else {
		v.cursor = -1
	}
	for g := range metas {
		for r := range metas[g] {
			page.Groups[g].Rows[r].Strip = st.strip(metas[g][r].kind, metas[g][r].entries, plan.Frame)
		}
	}
	for _, row := range st.partialQuiet {
		page.Groups[st.quietGroup].Rows[row.index].Strip = st.strip(row.kind, nil, plan.Frame)
	}
	if strips {
		// The words beside the marker move no column, so they are chosen
		// once the columns are known; so are the names of the quiet line.
		for _, label := range labels {
			if page.Bands.Label = label; tviewCells(label) <= plan.Layout.StripCol-calm.Indent-1 {
				break
			}
		}
		// A line of names is cut between two names, and counts the rest.
		// The words beside the names are written when every line has room
		// for them whole: one line with them and one without reads as a
		// difference between the two.
		room, words := plan.Layout.StripCol-2-calm.Indent, true
		for r := range st.quietNames {
			row := &page.Groups[st.quietGroup].Rows[r]
			if tviewCells(row.Subject) > room {
				row.Subject = timelineFitNames(strings.Split(row.Subject, " · "), room)
			}
			words = words && tviewCells(row.Subject)+2+tviewCells(row.What) <= room
		}
		for r := 0; r < st.quietNames && !words; r++ {
			page.Groups[st.quietGroup].Rows[r].What = ""
		}
	}
	if line := calm.TitleLine(page.Title, width, o); !strings.Contains(line, page.Title.Object) || !strings.Contains(line, v.windowLabel()) {
		// A narrow title cuts the object and then leaves the window out.
		// The window is what the axis spans and the object what the page is
		// about, so the count of entries gives way before either.
		short := page.Title
		if short.Window = v.windowLabel(); strings.Contains(calm.TitleLine(short, width, o), short.Window) {
			page.Title = short
		}
	}
	v.calmRows = v.calmRows[:0]
	for g := range page.Groups {
		if plan.TooSmall || plan.Folded[g] || plan.Hidden[g] {
			continue
		}
		for r := range min(plan.Rows[g], len(metas[g])) {
			row := metas[g][r]
			row.group, row.index = g, r
			v.calmRows = append(v.calmRows, row)
		}
	}
	sel := v.calmSelect()
	entries := v.calmFocus(st, sel)
	if sel != nil {
		page.Groups[sel.group].Rows[sel.index].Selected = true
	}
	page.Next = v.calmNext(st)
	v.calmCursor(&page, st)
	v.dropped = plan.Dropped
	rendered := calm.Render(&page, &plan, o)
	return calmInsert(rendered, v.calmDetail(st, sel, entries, width, list, o), gap, plan.TooSmall)
}

// calmSelect puts the row cursor on a row that is on the screen: the row
// it was on, else the row of the entry the keys act on (the first failing
// signal when the page opens), else the first row of what went wrong, else
// the first row.
func (v *timelineView) calmSelect() *timelineCalmRow {
	if len(v.calmRows) == 0 {
		v.calmKey = ""
		return nil
	}
	pick := func(ok func(row *timelineCalmRow) bool) *timelineCalmRow {
		for i := range v.calmRows {
			if ok(&v.calmRows[i]) {
				return &v.calmRows[i]
			}
		}
		return nil
	}
	row := pick(func(r *timelineCalmRow) bool { return v.calmKey != "" && r.key == v.calmKey })
	if row == nil && v.focused >= 0 {
		row = pick(func(r *timelineCalmRow) bool { return slices.Contains(r.entries, v.focused) })
	}
	if row == nil {
		row = pick(func(r *timelineCalmRow) bool {
			return r.kind.class == timelineFailed && !strings.HasSuffix(r.key, ":before")
		})
	}
	if row == nil {
		row = &v.calmRows[0]
	}
	v.calmKey = row.key
	return row
}

// span writes the time one strip column covers. A column is
// rarely a whole number of minutes, so the seconds are written unless both
// of its ends are on one; a day that is not the day of the capture is
// named.
func (st *timelineStory) span(from, to time.Time) string {
	layout := "15:04"
	if st.end.Sub(st.start) <= 15*time.Minute || from.Second() != 0 || to.Second() != 0 {
		layout = "15:04:05"
	}
	from, to, end := from.UTC(), to.UTC(), st.end.UTC()
	day := ""
	if from.YearDay() != end.YearDay() || from.Year() != end.Year() {
		day = from.Format("Jan02 ")
	}
	return day + from.Format(layout) + "–" + to.Format(layout)
}

// at are the entries drawn in one column: every entry whose mark touches
// it, those of the selected row first and the rest in the order they
// began.
func (st *timelineStory) at(f chart.Frame, col int, selected string) []int {
	var own, rest []int
	for r := range st.rows {
		row := &st.rows[r]
		for _, entry := range row.entries {
			if !f.Marked([]chart.Interval{st.mark(row.kind, entry)})[col] {
				continue
			}
			if row.key == selected {
				own = append(own, entry)
			} else {
				rest = append(rest, entry)
			}
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return st.s.Entries[rest[a]].At.Before(st.s.Entries[rest[b]].At) })
	return append(own, rest...)
}

// calmFocus settles which entries the list under the axis shows and which
// of them the keys act on: with the time cursor, what is drawn in its
// column; without it, the entries of the selected row. The entry the keys
// acted on stays when it is among them.
func (v *timelineView) calmFocus(st *timelineStory, sel *timelineCalmRow) []int {
	var entries []int
	timed := v.cursor >= 0 && v.frame.Width > 0
	switch {
	case timed:
		col := min(v.cursor, v.frame.Width-1)
		if at, ok := v.frame.Col(v.cursorAt); ok && !v.cursorAt.IsZero() {
			col = at
		}
		v.cursor = col
		entries = st.at(v.frame, col, v.calmKey)
	case sel != nil:
		entries = sel.entries
	}
	if !slices.Contains(entries, v.focused) {
		v.focused = -1
		switch {
		case len(entries) == 0:
		case timed:
			v.focused = entries[0]
		default:
			v.focused = sel.lead
		}
	}
	return entries
}

// calmCursor draws the time cursor on a planned page. The title states
// the time of its column; the list under the axis states what is drawn
// there. Nothing is measured again, so no column and no row moves.
func (v *timelineView) calmCursor(page *calm.Page, st *timelineStory) {
	if v.cursor < 0 || v.frame.Width <= 0 {
		return
	}
	from, to := v.frame.ColumnSpan(v.cursor)
	// The kit draws the cursor over the step that ends at its time, and a
	// frame without a sample step has one step a column.
	page.Bands.Cursor = to
	if page.Title.Busy == "" {
		page.Title.Busy = "cursor " + st.span(from, to) + " UTC"
	}
	if n := len(page.Keys); n > 0 {
		page.Keys[n-1] = "Esc hides the cursor"
	}
}

// timelineSpan is text in one colour on a line the view writes beside the
// kit's. The tag names the foreground, the background and the attributes,
// as every tag of a calm page does.
func timelineSpan(color, attrs, text string) string {
	if text == "" {
		return ""
	}
	fg := "-"
	if color != "" {
		fg = color
	}
	if attrs == "" {
		attrs = "-"
	}
	if fg == "-" && attrs == "-" {
		return tview.Escape(text)
	}
	return "[" + fg + ":-:" + attrs + "]" + tview.Escape(text) + "[-:-:-]"
}

// timelinePlainWords are the signs the view writes on its own lines, in
// plain characters for noIcons.
var timelinePlainWords = strings.NewReplacer(" · ", " | ", "…", "...", "→", "->", "–", "-")

func timelineWords(text string, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	if o.ASCII {
		return timelinePlainWords.Replace(text)
	}
	return text
}

// timelineClip cuts text to a width in cells and marks the cut.
func timelineClip(text string, width int, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	if width <= 0 {
		return ""
	}
	if tviewCells(text) <= width {
		return text
	}
	mark := timelineWords("…", o)
	if width <= tviewCells(mark) {
		return runewidth.Truncate(text, width, "")
	}
	return runewidth.Truncate(text, width, mark)
}

// timelineMiddle cuts a name to a width by leaving its middle out: the
// kind before it and the generated end tell it from its siblings.
func timelineMiddle(text string, width int, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	mark := timelineWords("…", o)
	if tviewCells(text) <= width || width < 8+tviewCells(mark) {
		return timelineClip(text, width, o)
	}
	runes := []rune(text)
	end, used := len(runes), 0
	for tail := (width - tviewCells(mark)) / 3; end > 0; end-- {
		w := runewidth.RuneWidth(runes[end-1])
		if used+w > tail {
			break
		}
		used += w
	}
	return runewidth.Truncate(text, width-tviewCells(mark)-used, "") + mark + string(runes[end:])
}

// timelineDetailCols are the columns of the list under the axis, in cells;
// zero leaves a column out.
type timelineDetailCols struct {
	clock, kind, object, text int
}

// detailCols budgets one line of the list. The time and the text always
// stay; the object gives way first, as it does in the list under a.
func (st *timelineStory) detailCols(entries []int, kinds bool, width int) timelineDetailCols {
	const minText = 28
	cols := timelineDetailCols{clock: 8}
	object := 0
	for _, i := range entries {
		e := &st.s.Entries[i]
		cols.clock = max(cols.clock, tviewCells(timeline.Clock(e.At, st.end)))
		object = max(object, tviewCells(e.Ref.Short()))
		if kinds {
			cols.kind = max(cols.kind, tviewCells(timelineKindOf(e).name))
		}
	}
	rest := width - calm.Indent - cols.clock - 2
	if cols.kind > 0 {
		rest -= cols.kind + 2
	}
	if room := rest - minText - 2; room >= 12 && object > 0 {
		cols.object = min(object, 36, room)
		rest -= cols.object + 2
	}
	cols.text = max(rest, 1)
	return cols
}

// timelineEntryColor is the colour of an entry: the colour of its row.
func timelineEntryColor(e *timeline.Entry, o calm.Options) string { //nolint:gocritic // Options are passed by value, as the kit takes them.
	switch timelineClassOf(e) {
	case timelineChanged:
		return o.Palette.Info
	case timelineFailed:
		return o.Palette.Fail
	}
	return ""
}

// entryLine is one entry of the list: when it began, what it is about and
// its text. focused marks the entry the keys act on. A text that does not
// fit is cut and the cut is marked; with wrap it ends between two words
// instead and rest is what follows, for a line of its own.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (st *timelineStory) entryLine(entry int, focused, wrap bool, cols timelineDetailCols, o calm.Options) (line, rest string) {
	p := o.Palette
	e := &st.s.Entries[entry]
	var b strings.Builder
	quiet := "d"
	if focused {
		b.WriteString(timelineSpan(p.Accent, "b", ">") + " ")
		quiet = ""
	} else {
		b.WriteString(strings.Repeat(" ", calm.Indent))
	}
	b.WriteString(timelineSpan(p.Muted, quiet, runewidth.FillRight(timeline.Clock(e.At, st.end), cols.clock)) + "  ")
	if cols.kind > 0 {
		b.WriteString(timelineSpan(p.Muted, quiet, runewidth.FillRight(timelineKindOf(e).name, cols.kind)) + "  ")
	}
	if cols.object > 0 {
		attrs := ""
		if focused {
			attrs = "b"
		}
		color := timelineEntryColor(e, o)
		if color == "" {
			color = p.Text
		}
		b.WriteString(timelineSpan(color, attrs, runewidth.FillRight(timelineMiddle(e.Ref.Short(), cols.object, o), cols.object)) + "  ")
	}
	text := timelineWords(impactArrow(e.Summary, o), o)
	shown := timelineClip(text, cols.text, o)
	if wrap && shown != text {
		head := runewidth.Truncate(text, cols.text, "")
		if at := strings.LastIndex(head, " "); at > cols.text/2 {
			head = head[:at]
		}
		shown, rest = head, strings.TrimSpace(strings.TrimPrefix(text, head))
	}
	color := p.Text
	if !focused {
		color = p.Muted
	}
	b.WriteString(timelineSpan(color, "", shown))
	return b.String(), rest
}

// entryMore is what the list adds under the entry the keys act on, a line
// each: the rest of a text that did not fit, how the entry stands to the
// first failing signal or to the mark, the detail the model carries, and
// where its time came from.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (st *timelineStory) entryMore(entry int, rest string, cols timelineDetailCols, width int, o calm.Options) []string {
	p := o.Palette
	s := st.s
	e := &s.Entries[entry]
	indent := calm.Indent + cols.clock + 2
	room := width - indent
	if room < 12 {
		return nil
	}
	var out []string
	add := func(color, attrs, text string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, strings.Repeat(" ", indent)+timelineSpan(color, attrs, timelineClip(timelineWords(text, o), room, o)))
		}
	}
	add(p.Text, "", rest)
	if s.OnsetEntry() != nil {
		relation, delta := s.Relation(entry)
		sentence := s.RelationSentence(entry)
		if relation == timeline.RelationBefore && e.Change && tviewCells(sentence) > room {
			moment := "the first failing signal"
			if s.Chosen {
				moment = "your mark"
			}
			sentence = "Preceded " + moment + " by " + timeline.Span(delta) + "; order, not cause."
		}
		add(p.Muted, "", sentence)
	}
	var details []string
	for _, item := range e.Detail {
		if item != "" {
			details = append(details, item)
		}
	}
	add(p.Muted, "d", strings.Join(details, " | "))
	if e.Origin != "" {
		add(p.Muted, "d", "Time from: "+e.Origin)
	}
	return out
}

// calmDetail is the list under the axis, exactly rows lines of it: a
// heading that says what is listed, the entries with their time, object
// and text, and under the entry the keys act on what the list under a
// states of it. One row is that entry alone.
//
//nolint:funlen,gocritic // The heading, then one of three lists: one entry, an empty column, the entries that fit; options by value, as the kit takes them.
func (v *timelineView) calmDetail(st *timelineStory, sel *timelineCalmRow, entries []int, width, rows int, o calm.Options) []string {
	if rows <= 0 {
		return nil
	}
	timed := v.cursor >= 0 && v.frame.Width > 0
	label, color := "", ""
	switch {
	case timed:
		from, to := v.frame.ColumnSpan(v.cursor)
		label = "AT " + st.span(from, to)
	case sel != nil:
		label = strings.ToUpper(sel.kind.name)
		if e := v.focusedEntry(); e != nil {
			color = timelineEntryColor(e, o)
		}
	}
	nothing := "nothing retained began or held here"
	if rows == 1 {
		if v.focused < 0 {
			return []string{calm.FoldedLine(label, color, []string{nothing}, true, width, o)}
		}
		line, _ := st.entryLine(v.focused, true, false, st.detailCols([]int{v.focused}, true, width), o)
		return []string{line}
	}
	if timed && len(entries) == 0 {
		// An empty column: the entries that began last before it and next
		// after it are named, so that the list says where n and Shift-N go.
		from, to := v.frame.ColumnSpan(v.cursor)
		at := sort.Search(len(st.shown), func(i int) bool { return !st.s.Entries[st.shown[i]].At.Before(to) })
		before := sort.Search(len(st.shown), func(i int) bool { return !st.s.Entries[st.shown[i]].At.Before(from) }) - 1
		var near []int
		if before >= 0 {
			near = append(near, st.shown[before])
		}
		if at < len(st.shown) {
			near = append(near, st.shown[at])
		}
		out := []string{calm.FoldedLine(label, color, []string{nothing + "; the entries before and after it:"}, true, width, o)}
		cols := st.detailCols(near, true, width)
		for _, entry := range near[:min(len(near), rows-1)] {
			line, _ := st.entryLine(entry, false, false, cols, o)
			out = append(out, line)
		}
		for len(out) < rows {
			out = append(out, "")
		}
		return out
	}
	// The entries that fit, and among them the one the keys act on.
	room, first := rows-1, 0
	if len(entries) > room {
		first = min(max(slices.Index(entries, v.focused)-room+1, 0), len(entries)-room)
	}
	shown := entries[first:min(first+room, len(entries))]
	count := timelinePlural(len(entries), "entry", "entries")
	switch {
	case len(entries) == 0:
		count = nothing
	case len(shown) < len(entries):
		count += ", " + timelinePlural(len(shown), "shown", "shown") + "; n steps through them"
	case !timed:
		count += " in " + st.windowWords()
	}
	out := []string{calm.FoldedLine(label, color, []string{count}, len(entries) == 0, width, o)}
	cols := st.detailCols(shown, timed, width)
	spare := room - len(shown)
	for _, entry := range shown {
		focused := entry == v.focused
		line, rest := st.entryLine(entry, focused, focused && spare > 0, cols, o)
		out = append(out, line)
		if !focused || spare <= 0 {
			continue
		}
		more := st.entryMore(entry, rest, cols, width, o)
		more = more[:min(len(more), spare)]
		out, spare = append(out, more...), spare-len(more)
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return out
}

// calmShown reports whether the calm page takes the keys. They are handed
// on everywhere else: to the filter prompt while it is open, and to the
// list under a and the sources text, which scroll with the same keys.
func (v *timelineView) calmShown() bool {
	return !v.allDetail && !v.showSources && !v.cmdBuff.IsActive()
}

// toggleDetail is a: the list and the pane this view had before the calm
// layout, and back. The entry the keys act on stays the same on both.
func (v *timelineView) toggleDetail() {
	if v.showSources {
		return
	}
	v.allDetail = !v.allDetail
	v.cursor, v.calmKey = -1, ""
	v.arrange()
	v.render()
	if v.active {
		v.app.SetFocus(v)
	}
}

// moveRow moves the row cursor over the rows that are on the screen.
func (v *timelineView) moveRow(delta int) {
	if len(v.calmRows) == 0 {
		return
	}
	index := 0
	for i := range v.calmRows {
		if v.calmRows[i].key == v.calmKey {
			index = i
		}
	}
	row := &v.calmRows[min(max(index+delta, 0), len(v.calmRows)-1)]
	if row.key == v.calmKey {
		return
	}
	// The entry the keys act on follows the row: its entry under the time
	// cursor, else the one it is named after.
	v.calmKey, v.focused = row.key, -1
	v.render()
}

// moveCursor shows the time cursor and moves it by delta columns.
func (v *timelineView) moveCursor(delta int) {
	if v.story == nil || v.frame.Width <= 0 {
		return
	}
	switch {
	case v.cursor < 0 && delta < 0:
		v.cursor = v.frame.Width - 1
	case v.cursor < 0:
		v.cursor = 0
	default:
		v.cursor = min(max(v.cursor+delta, 0), v.frame.Width-1)
	}
	v.placeCursor()
}

// cursorEdge puts the time cursor on the first or the last column.
func (v *timelineView) cursorEdge(direction int) {
	if v.story == nil || v.frame.Width <= 0 {
		return
	}
	v.cursor = 0
	if direction > 0 {
		v.cursor = v.frame.Width - 1
	}
	v.placeCursor()
}

// placeCursor remembers the time under the cursor and redraws. The time
// is the middle of the column, so that it is found again in the same
// column whatever width the strips are redrawn with.
func (v *timelineView) placeCursor() {
	from, to := v.frame.ColumnSpan(v.cursor)
	v.cursorAt = from.Add(to.Sub(from) / 2)
	v.focused = -1
	v.render()
}

// stepEntry moves both cursors to the next or the previous entry in the
// order they began: its row, and the column it began in. Every entry of
// the window is reached this way, also one whose group is folded.
func (v *timelineView) stepEntry(direction int) {
	st := v.story
	if st == nil || len(st.shown) == 0 {
		return
	}
	s := st.s
	at := slices.Index(st.shown, v.focused)
	switch {
	case at >= 0:
		at = min(max(at+direction, 0), len(st.shown)-1)
	case v.cursor >= 0 && v.frame.Width > 0:
		// From an empty column: the entry that began next after it, or last
		// before it.
		from, to := v.frame.ColumnSpan(v.cursor)
		at = sort.Search(len(st.shown), func(i int) bool { return !s.Entries[st.shown[i]].At.Before(to) })
		if direction < 0 {
			at = sort.Search(len(st.shown), func(i int) bool { return !s.Entries[st.shown[i]].At.Before(from) }) - 1
		}
		at = min(max(at, 0), len(st.shown)-1)
	case direction < 0:
		at = len(st.shown) - 1
	default:
		at = 0
	}
	entry := st.shown[at]
	v.focused = entry
	if key := st.rowOf(entry); key != "" {
		v.calmKey = key
	}
	if v.frame.Width > 0 {
		when := s.Entries[entry].At
		col, ok := v.frame.Col(when)
		switch {
		case ok:
		case when.Before(v.frame.Start):
			col = 0
		default:
			col = v.frame.Width - 1
		}
		v.cursor = col
		from, to := v.frame.ColumnSpan(col)
		v.cursorAt = from.Add(to.Sub(from) / 2)
	}
	v.render()
}
