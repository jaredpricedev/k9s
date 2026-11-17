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
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/tcell/v2"
)

// The calm page is what :slow opens on. It states the answer first, then
// every lane of the report as one row with its strip, grouped by how the
// lane stands to the episodes. The grouping is slowpath.Findings, which is
// interval overlap and nothing else; this file only lays it out. The hop
// table and the lanes strip the view had before are the all-detail page
// (a), drawn by slow_render.go.

// slowCalmRow is one row of the calm page that the row cursor can stand
// on, and what the keys act on while it does.
type slowCalmRow struct {
	// key tells the rows of a page apart: the lane, or a name of its own.
	key string
	// what is what the row measures, for the cursor readout in the title.
	what string
	// hops are the hops whose queries and rules s shows, and signal the one
	// it states first; facts are the lanes of dated facts it lists instead.
	hops   []*slowpath.Hop
	signal string
	facts  []*slowpath.Lane
	// lane is what the time cursor reads the row's value from, or nil.
	lane *slowpath.Lane
	// pod is the Pod Enter opens; outlier says it differs from its peers.
	pod     string
	outlier bool
	// group and index place the row on the page.
	group, index int
}

// hop is the hop the row belongs to, or empty for a row of dated facts.
func (r *slowCalmRow) hop() string {
	if len(r.hops) == 0 {
		return ""
	}
	return r.hops[0].Spec.ID
}

// slowCalmEntry is a row while the page is put together.
type slowCalmEntry struct {
	row  calm.Row
	meta slowCalmRow
	// symptom and overlapped order the rows that line up.
	symptom    bool
	overlapped int
	// fold is what the row is called when its group is one line; empty is
	// its subject.
	fold string
}

// slowTimes writes a count of stretches as a word.
func slowTimes(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// slowCalmSubject names whose row a lane is: the one Pod its marks belong
// to when the lane is about the Pods themselves, the hop otherwise.
func slowCalmSubject(report *slowpath.Report, lane *slowpath.Lane) (subject string, onPod bool) {
	if lane.Pod != "" && (lane.Hop == slowpath.HopApp || lane.Hop == slowpath.HopService || lane.Hop == "") {
		return "pod " + report.ShortPod(lane.Pod), true
	}
	if lane.Hop == slowpath.HopDependencies {
		// Several dependencies on one lane are named by its label.
		return lane.Label, false
	}
	return lane.Short, false
}

// slowCalmStrip draws when a lane was marked. Moments are events;
// stretches are the columns in which the rule held, exactly as the overlap
// with the episodes was computed from them. What was not read is never
// drawn as quiet.
func slowCalmStrip(lane *slowpath.Lane, marks []chart.Interval) calm.Strip {
	moments := len(marks) > 0
	at := make([]time.Time, 0, len(marks))
	for _, mark := range marks {
		moments = moments && !mark.End.After(mark.Start)
		at = append(at, mark.Start)
	}
	if moments {
		return calm.Events(at...).Unknown(lane.Unknown...)
	}
	return calm.Held(marks...).Unknown(lane.Unknown...)
}

// slowCalmMarked is the row of a lane that has marks in the window: the one
// number that matters and what it is held against.
func slowCalmMarked(report *slowpath.Report, f *slowpath.Finding) slowCalmEntry {
	lane := f.Lane
	subject, onPod := slowCalmSubject(report, lane)
	entry := slowCalmEntry{symptom: f.Relation == slowpath.LaneSymptom, overlapped: f.Overlapped}
	entry.meta = slowCalmRow{key: lane.ID, hops: f.Hops, lane: lane}
	if onPod {
		entry.meta.pod = lane.Pod
	}
	if len(f.Hops) == 0 {
		entry.meta.facts = []*slowpath.Lane{lane}
	}
	marks := f.Marks
	if entry.symptom && lane.Named != nil {
		// The episodes are counted from the one signal the row names.
		marks = slowpath.Clip(lane.Named, report.Start, report.End)
	}
	row := calm.Row{Subject: subject, What: lane.Phrase, Strip: slowCalmStrip(lane, marks)}
	if extra := len(lane.Names) - 1; extra > 0 && f.Signal != nil {
		// A hop on one lane: its other raised signals are under s.
		row.What += fmt.Sprintf(" +%d", extra)
	}
	clock := func(mark chart.Interval) string {
		at := mark.Start
		if mark.End.After(mark.Start) {
			// A stretch of samples is stated by its first raised sample.
			at = at.Add(lane.Lead)
		}
		return report.Clock(at)
	}
	switch sig := f.Signal; {
	case sig == nil:
		slowCalmFacts(&row, lane, marks, onPod, clock)
	case lane.Hop == slowpath.HopService:
		longest := time.Duration(0)
		for _, mark := range marks {
			longest = max(longest, mark.End.Sub(mark.Start))
		}
		row.Number, row.Compare = slowTimes(len(marks)), "up to "+impact.Dur(longest)
		entry.meta.signal = sig.Name
	default:
		entry.meta.signal = sig.Name
		series, value := sig.WorstSeries(), sig.Worst
		if onPod {
			if own := sig.PodSeries(lane.Pod); own != nil {
				series, value = own, own.Worst
			}
		}
		row.Number = sig.Format(value)
		switch {
		case sig.Share > 0 && !onPod:
			row.Number, row.Compare = chart.Ratio.Format(sig.Share), "of "+sig.ShareOf
		case lane.Pod != "" && !onPod:
			join := "on "
			if lane.Hop == slowpath.HopNetwork {
				join = "to "
			}
			row.Compare = join + report.ShortPod(lane.Pod)
		case lane.Hop == slowpath.HopApp && !onPod:
			raised := 0
			for i := range sig.Series {
				if len(sig.Series[i].Crossed) > 0 && sig.Series[i].Pod != "" {
					raised++
				}
			}
			row.Compare = fmt.Sprintf("%d of %d pods", raised, len(report.Path.Pods))
		case series != nil:
			row.Compare = "usual " + sig.Format(series.Median)
		}
		for _, deviation := range sig.Outliers {
			if onPod && deviation.Pod == lane.Pod && !deviation.Sustained {
				row.Compare = "peers " + sig.Format(deviation.Peers)
			}
		}
	}
	entry.row, entry.meta.what = row, row.What
	return entry
}

// slowCalmFacts fills the row of a lane of dated facts: how often it
// happened, or for how long an alert fired, and when first.
func slowCalmFacts(row *calm.Row, lane *slowpath.Lane, marks []chart.Interval, onPod bool, clock func(chart.Interval) string) {
	if len(lane.Events) == 1 && !onPod {
		row.What = lane.Events[0].Label
	}
	if row.What == row.Subject {
		row.What = "more than one Pod"
	}
	row.Number = slowTimes(len(marks))
	if total := slowpath.Total(marks); total > 0 && !lane.Signal && lane.Lead == 0 {
		row.Number = impact.Dur(total)
	}
	if len(marks) == 0 {
		return
	}
	row.Compare = "at " + clock(marks[0])
	if len(marks) > 1 {
		row.Compare = "from " + clock(marks[0])
	}
}

// slowCalmPeers is the one quiet line of the Pods that stayed under their
// rules while another Pod of the Service did not.
func slowCalmPeers(report *slowpath.Report, app *slowpath.Hop) (slowCalmEntry, bool) {
	var names []string
	var values []float64
	main := (*slowpath.SignalRead)(nil)
	if app.Main >= 0 && app.Main < len(app.Signals) {
		main = &app.Signals[app.Main]
	}
	for i := range app.Pods {
		pod := &app.Pods[i]
		if pod.State != slowpath.Quiet {
			continue
		}
		names = append(names, report.ShortPod(pod.Pod.Name))
		if main == nil {
			continue
		}
		if series := main.PodSeries(pod.Pod.Name); series != nil {
			value := series.Worst
			if !main.Bad() {
				value = series.Median
			}
			values = append(values, value)
		}
	}
	if len(names) == 0 {
		return slowCalmEntry{}, false
	}
	row := calm.Row{Subject: "pods " + strings.Join(names, ", "), Strip: calm.Quiet()}
	if len(names) > 3 {
		row.Subject = fmt.Sprintf("%d other pods", len(names))
	}
	entry := slowCalmEntry{meta: slowCalmRow{key: slowpath.HopApp + ":peers", hops: []*slowpath.Hop{app}}}
	if main != nil && len(values) > 0 {
		low, high := values[0], values[0]
		for _, value := range values {
			low, high = min(low, value), max(high, value)
		}
		entry.meta.signal = main.Name
		switch {
		case main.Format(low) == main.Format(high):
			row.What = main.Name + " " + main.Format(high)
		case main.Rule.Worse == slowpath.WorseLow:
			row.What = main.Name + " at least " + main.Format(low)
		default:
			row.What = main.Name + " up to " + main.Format(high)
		}
	}
	entry.row, entry.meta.what = row, row.What
	return entry, true
}

// calmEvidence sorts the lanes of the report into the groups of the page.
// With episodes: what lines up with them, what is raised outside them or
// differs all window, what was quiet and what was not read. Without: a Pod
// that differs from its peers, what was over its rule or happened in the
// window, what was quiet and what was not read. Which of them a lane is in
// is slowpath.Findings; nothing is decided here.
//
//nolint:gocyclo,funlen,gocritic // One pass over the lanes, a case per way a lane stands to the episodes; options by value, as the kit takes them.
func (v *slowView) calmEvidence(report *slowpath.Report, answer *slowpath.Answer, o calm.Options) ([]calm.Group, [][]slowCalmRow) {
	p := o.Palette
	app := report.Hop(slowpath.HopApp)
	outliers := map[string]bool{}
	if app != nil {
		for i := range app.Pods {
			if app.Pods[i].State == slowpath.Outlier {
				outliers[app.Pods[i].Pod.Name] = true
			}
		}
	}
	var lines, outside, facts, usual, quiet, unread []slowCalmEntry
	var idle []*slowpath.Lane
	for _, u := range report.Usuals() {
		// A Pod whose usual level differs from its peers' never crossed a
		// rule for it: its strip is the quiet baseline, and the row says
		// what differs.
		sig, pod := u.Signal, u.Deviation.Pod
		strip := calm.Held()
		if series := sig.PodSeries(pod); series != nil {
			strip = strip.Unknown(slowpath.Complement(slowpath.Covered(series.Points, report.Step), report.Start, report.End)...)
		}
		row := calm.Row{Subject: "pod " + report.ShortPod(pod), What: slowpath.Phrase(sig.Name) + ", usually",
			Number: sig.Format(u.Deviation.Value), Compare: "peers " + sig.Format(u.Deviation.Peers), Strip: strip}
		usual = append(usual, slowCalmEntry{row: row, meta: slowCalmRow{key: "usual:" + sig.Name + ":" + pod, what: row.What,
			hops: []*slowpath.Hop{app}, signal: sig.Name, pod: pod, outlier: true}})
	}
	peers := false
	for _, f := range report.Findings() {
		lane := f.Lane
		subject, _ := slowCalmSubject(report, lane)
		switch f.Relation {
		case slowpath.LaneNotRead:
			entry := slowCalmEntry{row: calm.Row{Subject: subject, What: lane.NotRead},
				meta: slowCalmRow{key: lane.ID, hops: f.Hops, what: "not read"}}
			if len(f.Hops) == 0 {
				entry.meta.facts = []*slowpath.Lane{lane}
			}
			unread = append(unread, entry)
			continue
		case slowpath.LaneQuiet:
			if lane.Hop == "" {
				// Dated facts of which there were none share one line.
				idle = append(idle, lane)
				continue
			}
			if lane.Hop == slowpath.HopApp && len(usual) > 0 {
				// No Pod crossed a rule, and one differs all the same: the
				// quiet line is the Pods that do not.
				peers = true
				if line, ok := slowCalmPeers(report, app); ok {
					quiet = append(quiet, line)
				}
				continue
			}
			what := "no rule crossed"
			if len(f.Hops) == 1 && f.Hops[0].Key != "" {
				what = f.Hops[0].Key
			}
			// Read, and nothing to show; a stretch without samples is
			// still drawn as not read.
			quiet = append(quiet, slowCalmEntry{row: calm.Row{Subject: subject, What: what, Strip: calm.Events().Unknown(lane.Unknown...)},
				meta: slowCalmRow{key: lane.ID, hops: f.Hops, lane: lane, what: what}})
			continue
		}
		entry := slowCalmMarked(report, &f)
		entry.meta.outlier = entry.meta.pod != "" && outliers[entry.meta.pod]
		if lane.Hop == slowpath.HopApp && app != nil && !peers {
			// The Pods that did not cross a rule stand beside the one that did.
			peers = true
			if line, ok := slowCalmPeers(report, app); ok {
				quiet = append(quiet, line)
			}
		}
		switch {
		case f.Relation != slowpath.LaneOutside:
			lines = append(lines, entry)
		case len(report.Episodes) == 0 && !lane.Signal:
			facts = append(facts, entry)
		default:
			outside = append(outside, entry)
		}
	}
	if len(idle) > 0 {
		names := make([]string, 0, len(idle))
		for _, lane := range idle {
			names = append(names, lane.Short)
		}
		what := "none in the last " + report.Span()
		quiet = append(quiet, slowCalmEntry{row: calm.Row{Subject: strings.Join(names, " · "), What: what, Strip: calm.Quiet()},
			meta: slowCalmRow{key: "facts", facts: idle, what: what}})
	}
	if hop := report.Hop(slowpath.HopDependencies); hop != nil {
		unread = append(unread, slowCalmEntry{row: calm.Row{Subject: hop.Spec.Short, What: hop.Spec.What},
			meta: slowCalmRow{key: hop.Spec.ID, hops: []*slowpath.Hop{hop}, what: "not read"}})
	}
	for i := range report.Hops {
		// A hop that was read in part is quiet for what was read; the rest
		// is listed here, so that a missing signal is never taken for one
		// that stayed under its rule.
		hop := &report.Hops[i]
		if hop.State == slowpath.NotRead || hop.Spec.ID == slowpath.HopDependencies {
			continue
		}
		missing, reason, first := len(hop.Spec.NotRead), "", ""
		if missing > 0 {
			reason = hop.Spec.NotRead[0].Reason
		}
		for s := range hop.Signals {
			if sig := &hop.Signals[s]; !sig.Read {
				missing++
				if first == "" {
					first, reason = sig.Name, sig.Reason
				}
			}
		}
		if missing == 0 {
			continue
		}
		part := fmt.Sprintf("%d of %d signals", missing, len(hop.Signals)+len(hop.Spec.NotRead))
		what := part + ": " + reason
		unread = append(unread, slowCalmEntry{row: calm.Row{Subject: hop.Spec.Short, What: what}, fold: hop.Spec.Short + " (" + part + ")",
			meta: slowCalmRow{key: "unread:" + hop.Spec.ID, hops: []*slowpath.Hop{hop}, signal: first, what: "not read"}})
	}

	// What lines up: the lane the episodes are counted from first, then the
	// subjects by how many episodes they overlap, each with its rows
	// together and the ones that overlap most first.
	sort.SliceStable(lines, func(a, b int) bool {
		if lines[a].symptom != lines[b].symptom {
			return lines[a].symptom
		}
		return lines[a].overlapped > lines[b].overlapped
	})
	var order []string
	by := map[string][]slowCalmEntry{}
	for i := range lines {
		subject := lines[i].row.Subject
		if _, seen := by[subject]; !seen {
			order = append(order, subject)
		}
		by[subject] = append(by[subject], lines[i])
	}
	lines = lines[:0]
	for _, subject := range order {
		lines = append(lines, by[subject]...)
	}

	// One colour per subject on the whole page: the Pod that differs from
	// its peers, then what lines up, then what is raised without lining up.
	color := map[string]string{}
	paint := func(entries []slowCalmEntry, tone string) {
		for i := range entries {
			if _, seen := color[entries[i].row.Subject]; !seen {
				color[entries[i].row.Subject] = tone
			}
		}
	}
	if len(report.Episodes) > 0 {
		// It differs, and not during an episode: it differs all window.
		outside, usual = append(outside, usual...), nil
	}
	for _, entries := range [][]slowCalmEntry{lines, outside, facts, usual} {
		for i := range entries {
			if entries[i].meta.outlier {
				color[entries[i].row.Subject] = p.Fail
			}
		}
	}
	paint(lines, p.Warn)
	if len(report.Episodes) == 0 {
		paint(outside, p.Warn)
	}
	paint(outside, p.Info)
	paint(facts, p.Info)

	noun, nouns := strings.ToUpper(answer.Noun), strings.ToUpper(answer.Nouns)
	article := "A "
	if strings.HasPrefix(noun, "E") {
		article = "AN "
	}
	if len(report.Episodes) == 1 {
		nouns = noun
	}
	sets := []struct {
		group   calm.Group
		entries []slowCalmEntry
	}{
		{calm.Group{Label: "LINES UP WITH THE " + nouns, Color: p.Warn}, lines},
		{calm.Group{Label: "DIFFERS FROM ITS PEERS", Color: p.Warn}, usual},
		{calm.Group{Label: "DIFFERS, BUT NOT DURING " + article + noun, Role: calm.Secondary}, outside},
		{calm.Group{Label: "IN THE WINDOW", Role: calm.Secondary}, facts},
		{calm.Group{Label: "QUIET", Role: calm.QuietRole}, quiet},
		{calm.Group{Label: "NOT READ", Role: calm.NotReadRole, Color: p.Warn}, unread},
	}
	if len(report.Episodes) == 0 {
		// Without an episode nothing lines up: a signal that is marked in
		// the window is over its rule, and that is all that is said of it.
		sets[2].group = calm.Group{Label: "OVER ITS RULE", Color: p.Warn}
	}
	var groups []calm.Group
	var rows [][]slowCalmRow
	for i := range sets {
		set := &sets[i]
		if len(set.entries) == 0 {
			continue
		}
		group, metas := set.group, make([]slowCalmRow, 0, len(set.entries))
		named := false
		for e := range set.entries {
			named = named || set.entries[e].fold != ""
		}
		for e := range set.entries {
			row := set.entries[e].row
			if fold := set.entries[e].fold; fold != "" {
				group.Summary = append(group.Summary, v.plain(fold))
			} else if named {
				// Folded to one line, a hop that was read in part is named
				// with how much of it was not: it is not listed as not read.
				group.Summary = append(group.Summary, v.plain(row.Subject))
			}
			row.Color = color[row.Subject]
			row.Subject, row.What = v.plain(row.Subject), v.plain(row.What)
			row.Number, row.Compare = v.plain(row.Number), v.plain(row.Compare)
			group.Rows = append(group.Rows, row)
			metas = append(metas, set.entries[e].meta)
		}
		groups, rows = append(groups, group), append(rows, metas)
	}
	return groups, rows
}

// calmTitle names the Service, the window and how much of the batch was
// answered. The namespace is written when it is not the one the session
// is in, and the selection when the path was resolved from something else.
func (v *slowView) calmTitle() calm.Title {
	title := calm.Title{View: slowTitle, Object: v.selection.Label()}
	if path := v.path(); path != nil {
		title.Object = "Service " + path.Service
		if path.Namespace != v.app.Config.ActiveNamespace() {
			title.Object = "Service " + path.Key()
		}
		if v.selection.Kind != slowpath.KindService {
			title.Object += " (from " + v.selection.Kind + " " + v.selection.Name + ")"
		}
	}
	if report := v.report(); report != nil {
		title.Window = "last " + report.Span()
		title.Read, title.Done, title.Total = report.ReadAt, report.Answered, report.Asked
	}
	switch {
	case v.loading && v.read != nil:
		title.Busy = "reading again…"
	case v.loading:
		title.Busy = "reading…"
	}
	return title
}

// calmNotice is what is true of the page as a whole and is not evidence.
func (v *slowView) calmNotice(o calm.Options) (text, color string) { //nolint:gocritic // Options are passed by value, as the kit takes them.
	switch {
	case !v.destinationCurrent():
		return "Destination changed; this read is retained. Reopen :" + slowCommandToken + " for the current context.", o.Palette.Warn
	case !v.loading && v.read != nil && v.read.err != nil && v.read.report != nil:
		return "Re-read failed, the earlier read is shown: " + v.read.err.Error(), o.Palette.Warn
	}
	return "", ""
}

// calmNext are the likely next steps, and only those that would run: the
// probe, the Pods side by side, what Enter opens for the selected row, and
// Grafana.
func (v *slowView) calmNext(report *slowpath.Report, sel *slowCalmRow) []calm.Action {
	var next []calm.Action
	service, pods := v.serviceTarget(), len(report.Path.Pods)
	if probeAvailability(service) == "" && pods > 0 {
		next = append(next, calm.Action{Key: probeKeyName, Label: "probe the pods now"})
	}
	graphs := graphAvailability(v.app, service) == ""
	if graphs && pods > 1 {
		next = append(next, calm.Action{Key: "m", Label: "compare the pods"})
	}
	switch {
	case sel == nil:
	case sel.pod != "" && graphs:
		next = append(next, calm.Action{Key: "Enter", Label: "open " + v.plain(report.ShortPod(sel.pod))})
	default:
		next = append(next, calm.Action{Key: "Enter", Label: "its queries"})
	}
	if grafanaUnavailableShort(v.app, v) == "" {
		next = append(next, calm.Action{Key: tcell.KeyNames[grafanaKey], Label: "open in Grafana"})
	}
	return next
}

// calmKeys is the second key line: every other key, the way out last. A
// narrow line drops keys from the end, so the key for all detail and the
// one for the queries stand before the cursor and the windows.
func (v *slowView) calmKeys(report *slowpath.Report, answer *slowpath.Answer) []string {
	keys := []string{"↑↓ row", calm.DetailKey + " show every signal", "s queries", "←→ time", "1-4 window"}
	if len(report.Episodes) > 0 {
		keys = append(keys, "n next "+answer.Noun)
	}
	if _, _, err := timelineScope(v.workloadTarget(), v.app.Config.ActiveNamespace(), ""); err == nil {
		keys = append(keys, "t timeline")
	}
	if len(report.Path.Pods) > 0 {
		keys = append(keys, "h Hubble")
	}
	return append(keys, "r re-read", "Esc back")
}

// calmPage is the page of a report. brief leaves the times out of the
// answer when they do not fit on its first line: the marker row and the
// axis under it state them.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (v *slowView) calmPage(report *slowpath.Report, answer *slowpath.Answer, brief bool, width int, o calm.Options) (calm.Page, [][]slowCalmRow) {
	page := calm.Page{Title: v.calmTitle(), Frame: chart.Frame{Start: report.Start, End: report.End, Step: report.Step}}
	oneLine := func(sentence string) bool {
		return len(calm.VerdictLines(calm.Verdict{Sentences: []string{v.plain(sentence)}}, width, o)) <= 1
	}
	switch answer.Kind {
	case slowpath.AnswerEpisodes:
		lead, lines := answer.Lead, answer.Lines
		if brief && !oneLine(lead) {
			lead = answer.Brief
		}
		if !oneLine(lines) {
			// The rows under the answer name the signals it counts.
			lines = answer.Counted
		}
		page.Verdict = calm.Verdict{Sentences: append(append([]string{lead, lines}, answer.More...), answer.Differs...), Caveat: answer.Caveat}
		label := answer.Nouns
		if len(report.Episodes) == 1 {
			label = answer.Noun
		}
		page.Bands = calm.Bands{Label: label, Episodes: report.Episodes}
	case slowpath.AnswerQuiet:
		page.Verdict = calm.Nothing(answer.Lead, report.Answered, report.Asked)
		if len(answer.Differs) > 0 {
			// No episode, and a finding all the same: a Pod that differs.
			page.Verdict = calm.Verdict{Sentences: append(append([]string(nil), answer.Differs...), answer.Lead), Caveat: answer.Caveat}
		}
	default:
		page.Verdict = calm.Unread(answer.Lead, "")
	}
	if answer.Coarse != "" {
		page.Verdict.Sentences = append(page.Verdict.Sentences, answer.Coarse)
	}
	for i := range page.Verdict.Sentences {
		page.Verdict.Sentences[i] = v.plain(page.Verdict.Sentences[i])
	}
	groups, rows := v.calmEvidence(report, answer, o)
	page.Groups = groups
	page.Next, page.Keys = v.calmNext(report, &slowCalmRow{}), v.calmKeys(report, answer)
	return page, rows
}

// calmBlank is the page before a report exists: a read that is under way,
// or a selection that has no request path.
func (v *slowView) calmBlank() calm.Page {
	page := calm.Page{Title: v.calmTitle(), Keys: []string{"r re-read", "Esc back"}}
	if v.read == nil {
		page.Verdict = calm.Verdict{
			Sentences: []string{
				"Reading the configured path, then one bounded batch from the declared Prometheus.",
				"The path is read from the Kubernetes API as :network-review reads it: Service, EndpointSlices, Pods, Ingress. " +
					"Every hop is then read once from the declared Prometheus.",
			},
			Caveat: "Bounded, read-only, and only because this view was opened.",
		}
		return page
	}
	page.Verdict = calm.Unread("No request path: "+v.read.err.Error()+".",
		"Nothing has been read; r reads again, and :sources shows what is declared for this context.")
	return page
}

// calmLines draws the calm page into the area: the page of the report,
// or the queries and rules of its selected row while s is on.
func (v *slowView) calmLines(width, height int) []string {
	o := calmOptions(v.app)
	notice, color := v.calmNotice(o)
	if notice != "" {
		height--
	}
	var lines []string
	if report := v.report(); report != nil {
		lines = v.calmReport(report, width, height, o)
	} else {
		page := v.calmBlank()
		plan := calm.Lay(&page, width, height, o)
		v.calm, v.frame, v.cursor = nil, chart.Frame{}, -1
		lines = calm.Render(&page, &plan, o).Lines()
	}
	if notice != "" && len(lines) > 0 {
		// Under the title, where it is read before the answer.
		lines = append(lines[:1], append([]string{calm.NoticeLine(v.plain(notice), color, width, o)}, lines[1:]...)...)
	}
	return lines
}

// slowCalmPlan plans the page for the area. The kit gives the second key
// line up before the last blank rows of a page. That line names the key
// for all detail, so when leaving it out left blank rows on the page, the
// page is planned without it and one row shorter, and under says the line
// is written under the page instead. Nothing else is given up for it.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func slowCalmPlan(page *calm.Page, width, height int, o calm.Options) (plan calm.Plan, under bool) {
	plan = calm.Lay(page, width, height, o)
	if plan.TooSmall || len(page.Next) == 0 || calm.RestLine(page.Keys, width, o) == "" || len(calm.Render(page, &plan, o).Keys) > 1 {
		return plan, false
	}
	keys := page.Keys
	page.Keys = nil
	shorter := calm.Lay(page, width, height-1, o)
	page.Keys = keys
	same := !shorter.TooSmall && shorter.TextRows == plan.TextRows && shorter.Layout == plan.Layout && len(shorter.Missing) == len(plan.Missing) &&
		slices.Equal(shorter.Folded, plan.Folded) && slices.Equal(shorter.Hidden, plan.Hidden) && slices.Equal(shorter.Rows, plan.Rows)
	if !same {
		return plan, false
	}
	// The line is on the screen: it is not among what this size gave up.
	shorter.Dropped = slices.DeleteFunc(shorter.Dropped, func(what string) bool { return what == "the second key line" })
	return shorter, true
}

// calmReport lays the page of a report out and draws it. The row cursor
// and the time cursor are put on after the page is planned, so that
// neither moves a column or a row of it.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (v *slowView) calmReport(report *slowpath.Report, width, height int, o calm.Options) []string {
	answer := report.Answer()
	page, rows := v.calmPage(report, &answer, true, width, o)
	plan, under := slowCalmPlan(&page, width, height, o)
	strips := func() bool { return plan.Layout.Strip > 0 && !plan.TextRows && !plan.TooSmall }
	if !strips() && answer.Kind == slowpath.AnswerEpisodes {
		// Without strips nothing else on the page says when: the times
		// stay in the answer however long it gets.
		page, rows = v.calmPage(report, &answer, false, width, o)
		plan, under = slowCalmPlan(&page, width, height, o)
	}
	v.frame = chart.Frame{}
	if strips() {
		v.frame = plan.Frame
	} else {
		v.cursor = -1
	}
	v.calm = v.calm[:0]
	for g := range page.Groups {
		if plan.TooSmall || plan.Folded[g] || plan.Hidden[g] {
			continue
		}
		for r := range plan.Rows[g] {
			row := rows[g][r]
			row.group, row.index = g, r
			v.calm = append(v.calm, row)
		}
	}
	sel := v.calmSelect()
	if v.detail {
		return v.calmQueries(report, &page, width, height, o)
	}
	if sel != nil {
		page.Groups[sel.group].Rows[sel.index].Selected = true
	}
	page.Next = v.calmNext(report, sel)
	v.calmCursor(report, &page, &plan, sel, o)
	lines := calm.Render(&page, &plan, o).Lines()
	if under {
		lines = append(lines, calm.RestLine(page.Keys, width, o))
	}
	return lines
}

// calmRow is the row the cursor stands on, or nil.
func (v *slowView) calmRow() *slowCalmRow {
	for i := range v.calm {
		if v.calm[i].key == v.calmKey {
			return &v.calm[i]
		}
	}
	return nil
}

// calmSelect puts the row cursor on a row that is on the screen: the row
// it was on, else a row of the Pod that was selected, else the strongest
// finding (a row of the Pod that differs from its peers), else the first
// row. What the shared actions act on follows it.
func (v *slowView) calmSelect() *slowCalmRow {
	if len(v.calm) == 0 {
		return nil
	}
	pick := func(ok func(row *slowCalmRow) bool) *slowCalmRow {
		for i := range v.calm {
			if ok(&v.calm[i]) {
				return &v.calm[i]
			}
		}
		return nil
	}
	row := v.calmRow()
	if row == nil && v.focus.pod != "" {
		row = pick(func(r *slowCalmRow) bool { return r.pod == v.focus.pod })
	}
	if row == nil && v.calmFromTable && v.focus.hop != "" {
		// Back from the hop table: the row of the hop that was selected.
		row = pick(func(r *slowCalmRow) bool { return r.pod == "" && r.hop() == v.focus.hop })
	}
	if row == nil {
		row = pick(func(r *slowCalmRow) bool { return r.outlier })
	}
	if row == nil {
		row = &v.calm[0]
	}
	v.calmFromTable = false
	v.calmKey, v.focus = row.key, slowRow{hop: row.hop(), pod: row.pod}
	return row
}

// slowCalmSpan writes the time one strip column covers. A column is
// rarely a whole number of minutes, so the seconds are written unless both
// of its ends are on one.
func slowCalmSpan(report *slowpath.Report, from, to time.Time) string {
	layout := "15:04"
	if report.End.Sub(report.Start) <= 15*time.Minute || from.Second() != 0 || to.Second() != 0 {
		layout = "15:04:05"
	}
	return from.UTC().Format(layout) + "–" + to.UTC().Format(layout)
}

// slowCalmCursorNote is what a lane reads in a strip column: the dated
// facts that fall into it, or the worst value of the samples that stand
// for it. A sample stands for the step that ends at it, as the strip draws
// it.
func slowCalmCursorNote(lane *slowpath.Lane, from, to time.Time, step time.Duration) string {
	if events := lane.EventsAt(from, to); len(events) > 0 {
		return strings.Join(events, ", ")
	}
	if value, ok := lane.ValueAt(from.Add(time.Nanosecond), to.Add(step)); ok {
		return lane.Unit.Format(value)
	}
	return "-"
}

// calmCursor draws the time cursor on a planned page. The title states its
// time and the value of the selected row there; each row states its own
// where the page has a column for what a number is held against. Nothing
// is measured again, so no column and no row moves.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (v *slowView) calmCursor(report *slowpath.Report, page *calm.Page, plan *calm.Plan, sel *slowCalmRow, o calm.Options) {
	if v.cursor < 0 || v.frame.Width <= 0 {
		return
	}
	col := min(v.cursor, v.frame.Width-1)
	if at, ok := v.frame.Col(v.cursorAt); ok && !v.cursorAt.IsZero() {
		col = at
	}
	v.cursor = col
	from, to := v.frame.ColumnSpan(col)
	// The kit draws the cursor over the step that ends at its time.
	page.Bands.Cursor = from.Add(to.Sub(from) / 2).Add(report.Step / 2)
	note := func(row *slowCalmRow) string {
		if row.lane == nil {
			return ""
		}
		return v.plain(slowCalmCursorNote(row.lane, from, to, report.Step))
	}
	mark := "┆ "
	if o.ASCII {
		mark = ": "
	}
	for i := range v.calm {
		row := &v.calm[i]
		group := &page.Groups[row.group]
		if plan.Layout.Compare == 0 || group.Role != calm.Primary && group.Role != calm.Secondary {
			continue
		}
		group.Rows[row.index].Compare = ""
		if text := note(row); text != "" {
			group.Rows[row.index].Compare = mark + text
		}
	}
	at := "cursor " + slowCalmSpan(report, from, to) + " UTC"
	if sel != nil {
		if text := note(sel); text != "" && text != "-" {
			at += " · " + v.plain(sel.what) + " " + text
		}
	}
	if page.Title.Busy == "" {
		page.Title.Busy = at
	}
	if n := len(page.Keys); n > 0 {
		page.Keys[n-1] = "Esc hides the cursor"
	}
}

// calmQueries is the page s shows in place of the evidence: the title,
// then every query, sample count and rule behind the selected row.
//
//nolint:gocritic // Options are passed by value, as the kit takes them.
func (v *slowView) calmQueries(report *slowpath.Report, page *calm.Page, width, height int, o calm.Options) []string {
	lines := []string{calm.TitleLine(page.Title, width, o)}
	rows, airy := height-2, height >= 16
	if airy {
		lines = append(lines, "")
		rows -= 2
	}
	indent := strings.Repeat(" ", calm.Indent)
	for _, line := range v.detailLines(report, width-calm.Indent, max(rows, 2)) {
		lines = append(lines, indent+line)
	}
	if airy {
		lines = append(lines, "")
	}
	keys := []string{"↑↓ row", "PgDn PgUp page", "s or Enter back to the page", "Esc back"}
	return append(lines, calm.RestLine(keys, width, o))
}
