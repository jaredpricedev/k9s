// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/probe"
)

// The calm page of :probe. Before a run it is one block: nothing has been
// sent, what r would send, and the Pods it would go to. After a run the
// sentence that says which Pod differs comes first, and every Pod is one
// row: its name, where it runs, one cell per request on a scale shared by
// all rows, its median and the word for a Pod that stands out. Pods that
// stand out come first and in their colour; their peers are dim. The run
// before this one is a dimmer strip on the same row. What the time covers
// is one dim line; the table this view had before, with every time and
// status code, is under a. What is sent, and when, is not decided here.
const (
	probeSlowLabel     = "SLOWER THAN ITS PEERS"
	probeFailedLabel   = "NO ANSWER OR AN ERROR"
	probeAlikeLabel    = "LIKE THEIR PEERS"
	probeAnsweredLabel = "ANSWERED"
	probeOpenLabel     = "NO OUTCOME"
	probeSkippedLabel  = "NOT CONTACTED"
	probeListLabel     = "WOULD BE ASKED"
	probeBarredLabel   = "NOT ASKED"
	probeRunningLabel  = "ANSWERS SO FAR"
	probeGapsLabel     = "NOT READ"
	probeMeasuredLabel = "MEASURED"

	probeDifferenceCaveat = "A difference between Pods now, not proof of cause."
	probeNothingCaveat    = "Nothing found is not proof of health."
)

// probeCalmGroup is the Pods of one group of the calm page, as indexes
// into the plan's targets.
type probeCalmGroup struct {
	label   string
	role    calm.Role
	warn    bool
	targets []int
}

// ran reports whether the current run sent anything.
func (v *probeView) ran() bool { return v.run != nil && v.run.Sent() > 0 }

// calmGroups sorts the Pods into the groups of the calm page. Before a run
// they are the Pods a request would go to; while one is sending they stay
// in the order of the plan, so no row moves under the eye; afterwards the
// Pods that stand out come first.
func (v *probeView) calmGroups(summary *probe.Summary) []probeCalmGroup {
	if v.plan == nil {
		return nil
	}
	by := map[string][]int{}
	for i := range v.plan.Targets {
		label := probeListLabel
		switch verdict := summary.Pods[i].Verdict; {
		case v.plan.Targets[i].Request == nil:
			label = probeSkippedLabel
		case v.running:
			label = probeRunningLabel
		case !v.ran() && v.plan.Access == probe.AccessDenied:
			// r sends nothing here, so no Pod "would be asked".
			label = probeBarredLabel
		case !v.ran():
		case verdict == probe.Outlier || verdict == probe.Spikes:
			label = probeSlowLabel
		case verdict.Flagged():
			label = probeFailedLabel
		case verdict == probe.LikePeers:
			label = probeAlikeLabel
		case verdict == probe.NoPeers:
			label = probeAnsweredLabel
		default:
			label = probeOpenLabel
		}
		by[label] = append(by[label], i)
	}
	// The peers are rows like the Pod that stands out: a short screen leaves
	// the last of them out and counts them, it does not fold them away
	// while there is room for some.
	var out []probeCalmGroup
	for _, group := range []probeCalmGroup{
		{label: probeListLabel}, {label: probeBarredLabel}, {label: probeRunningLabel},
		{label: probeSlowLabel, warn: true}, {label: probeFailedLabel, warn: true},
		{label: probeAlikeLabel}, {label: probeAnsweredLabel},
		{label: probeOpenLabel, role: calm.Secondary}, {label: probeSkippedLabel, role: calm.NotReadRole},
	} {
		if group.targets = by[group.label]; len(group.targets) == 0 {
			continue
		}
		out = append(out, group)
	}
	return out
}

// calmOrder is the Pods in the order the calm page lists them.
func (v *probeView) calmOrder() []int {
	if v.plan == nil {
		return nil
	}
	summary := probe.Summarize(v.plan, v.run, v.previous)
	var out []int
	for _, group := range v.calmGroups(&summary) {
		out = append(out, group.targets...)
	}
	return out
}

// toggleDetail is a: the table and the result pane this view had before
// the calm layout, and back.
func (v *probeView) toggleDetail() {
	v.allDetail = !v.allDetail
	v.arrange()
	v.render()
	if v.confirm == nil {
		v.app.SetFocus(v)
	}
}

// arrange puts the widgets of the current layout into the view: the one
// calm page, or the header, the table, the result pane and the key line.
func (v *probeView) arrange() {
	v.Flex.Clear()
	if v.allDetail {
		v.Flex.AddItem(v.header, v.headerHeight, 0, false).
			AddItem(v.body, 0, 1, true).
			AddItem(v.detail, v.detailHeight, 0, false).
			AddItem(v.footer, 1, 0, false)
		return
	}
	v.Flex.AddItem(v.calm, 0, 1, true)
}

// calmTicks are the requests of one run to one Pod as cells: the time to
// the first byte as a height, and a sign of its own for a request that has
// no time. A request without an answer and one the API server could not
// deliver stand out; one in flight, abandoned or never sent does not.
func probeCalmTicks(attempts []probe.Attempt, slowLine float64) []calm.Tick {
	out := make([]calm.Tick, 0, len(attempts))
	for _, attempt := range attempts {
		switch attempt.Outcome {
		case probe.NotSent:
			// A level glyph is a dot with plain characters, so an unsent
			// request is a blank there, as it was.
			out = append(out, calm.Tick{Glyph: '·', ASCII: ' '})
		case probe.InFlight:
			out = append(out, calm.Tick{Glyph: '~'})
		case probe.Abandoned:
			out = append(out, calm.Tick{Glyph: '?'})
		case probe.Answered:
			seconds := attempt.Took.Seconds()
			out = append(out, calm.Tick{V: seconds, Over: slowLine > 0 && seconds >= slowLine})
		case probe.TimedOut:
			out = append(out, calm.Tick{Glyph: chart.GapGlyph, ASCII: chart.GapASCII, Loud: true})
		default:
			out = append(out, calm.Tick{Glyph: '!', Loud: true})
		}
	}
	return out
}

// calmRow is the row of one Pod. strips says a run has answers to draw.
func (v *probeView) calmRow(index int, summary *probe.Summary, strips bool, o calm.Options) calm.Row {
	p := o.Palette
	target := &v.plan.Targets[index]
	stats := &summary.Pods[index]
	var where []string
	for _, part := range []string{target.Pod.Node, target.Pod.Zone} {
		if part != "" {
			where = append(where, part)
		}
	}
	if !target.Pod.Ready {
		where = append(where, "not Ready")
	}
	// Where a Pod runs is beside the point of its row: dim, also on the row
	// of a Pod that stands out.
	row := calm.Row{Subject: v.plan.ShortName(target.Pod.Name), What: strings.Join(where, " "), Aside: true, Selected: target.Pod.Name == v.selected}
	if target.Request == nil {
		row.What, row.Aside = target.Skip, false
		return row
	}
	if !strips {
		return row
	}
	switch stats.Verdict {
	case probe.Outlier:
		row.Color, row.Compare = p.Fail, "peers "+probe.Seconds(stats.Peers)
	case probe.NoAnswer:
		row.Color, row.Compare = p.Fail, stats.Failure
		if stats.TimedOut > 0 {
			row.Compare = "none within " + v.deadlineText()
		}
	case probe.Spikes:
		row.Color, row.Compare = p.Warn, "slowest "+stats.WorstText(o.ASCII)
	case probe.Timeouts:
		row.Color, row.Compare = p.Warn, fmt.Sprintf("%d of %d unanswered", stats.TimedOut, stats.Finished)
	case probe.Errors:
		row.Color, row.Compare = p.Warn, "HTTP "+stats.CodesText()
	}
	if stats.Finished > 0 {
		row.Number = stats.TypicalText(o.ASCII)
	}
	switch {
	case v.running && stats.Finished < stats.Planned:
		row.Tag = "probing"
	case stats.Verdict.Flagged() || stats.Verdict == probe.Canceled || stats.Verdict == probe.Unprobed:
		row.Tag = stats.Verdict.Word()
	}
	if v.run != nil && index < len(v.run.Pods) {
		row.Strip = calm.Sequence("this run", probeCalmTicks(v.run.Pods[index].Attempts, summary.SlowLine))
		if v.previous != nil && index < len(v.previous.Pods) && v.previous.Pods[index].Attempts != nil {
			row.Strip = row.Strip.Then("previous "+v.previousAt.Format("15:04:05Z"), probeCalmTicks(v.previous.Pods[index].Attempts, summary.SlowLine))
		}
		row.Strip = row.Strip.Top(summary.Top)
	}
	return row
}

// calmUnsent says, before anything was sent, what r would send or why it
// can send nothing.
func (v *probeView) calmUnsent() calm.Verdict {
	switch {
	case v.plan.Sendable() == 0:
		who := fmt.Sprintf("None of the %d listed Pods declares", len(v.plan.Targets))
		if len(v.plan.Targets) == 1 {
			who = "The listed Pod does not declare"
		}
		return calm.Verdict{Kind: calm.NotRead, Apart: true, Caveat: "Nothing has been sent.", Sentences: []string{"Nothing can be sent.",
			fmt.Sprintf("%s an httpGet %s or %s, and this command sends only a request a Pod declares for itself. The path is never typed.",
				who, probe.Readiness.Field(), probe.Liveness.Field())}}
	case v.plan.Access == probe.AccessDenied:
		return calm.Verdict{Kind: calm.NotRead, Apart: true, Sentences: []string{"Not allowed.",
			"The API server says this user is " + v.plan.AccessReason + ". Nothing was sent and r will send nothing."}}
	}
	// Two sentences and one dim line: nothing has left, what r would send,
	// and what the application will notice. How the requests are paced and
	// what else was read are in the question r asks and under a.
	long, _ := v.requestWords()
	own := "It is each Pod's own " + v.plan.Kind.Field()
	if v.plan.Sendable() == 1 {
		own = "It is the Pod's own " + v.plan.Kind.Field()
	}
	out := calm.Verdict{Apart: true, Caveat: own + "; the application will see it in its logs.", Sentences: []string{
		"Nothing has been sent.",
		fmt.Sprintf("r sends %s: %s in all. You are asked first.", long, probePlural(v.plan.Sendable()*v.attempts, "request", "requests")),
	}}
	if v.plan.Access == probe.AccessUnknown && v.plan.AccessReason != "" {
		out.Sentences = append(out.Sentences, "Whether this user may use pods/proxy is not known: "+v.plan.AccessReason+".")
	}
	return out
}

// calmResult is the answer of a run: the first sentence of the result and
// the one that qualifies it most. Why the run ended early comes before how
// the other Pods answered. Every sentence is under a.
func (v *probeView) calmResult(summary *probe.Summary, ascii bool) calm.Verdict {
	lines := probe.Result(v.plan, v.run, summary, ascii)
	var out calm.Verdict
	if v.running {
		out.Sentences = append(out.Sentences, fmt.Sprintf("Still sending: %d of %d requests have an outcome. What follows is not final.",
			v.run.Finished(), v.run.Planned()))
	}
	if len(lines) > 0 {
		out.Sentences = append(out.Sentences, lines[0])
	}
	second := ""
	if len(lines) > 1 {
		second = lines[1]
		if last := lines[len(lines)-1]; !summary.Denied && v.run.Stopped != "" && len(lines) > 2 {
			second = last
		}
	}
	if second != "" && len(out.Sentences) < 2 {
		out.Sentences = append(out.Sentences, second)
	}
	flagged := false
	for i := range summary.Pods {
		flagged = flagged || summary.Pods[i].Verdict.Flagged()
	}
	switch {
	case summary.Denied:
		out.Kind, out.Caveat = calm.NotRead, "Not measured is not healthy."
	case flagged:
		out.Caveat = probeDifferenceCaveat
	default:
		out.Kind, out.Caveat = calm.NothingFound, probeNothingCaveat
	}
	return out
}

func (v *probeView) calmVerdict(summary *probe.Summary, ascii bool) calm.Verdict {
	var out calm.Verdict
	switch {
	case v.planErr != "":
		out = calm.Unread("Nothing was read: "+v.planErr+".", "No request was sent to any Pod. Esc goes back; open :probe again to read again.")
	case v.plan == nil:
		out = calm.Verdict{Apart: true, Sentences: []string{
			"Reading the Pods behind " + v.scope.Label() + " and their declared readiness and liveness probes.",
			"These are bounded Kubernetes reads. No request goes to any Pod until you press r and confirm.",
		}}
	case len(v.plan.Targets) == 0:
		out = calm.Verdict{Kind: calm.NotRead, Caveat: "Nothing has been sent.",
			Sentences: []string{"No Pod is behind " + v.scope.Label() + ", so there is nothing to probe."}}
	case v.ran() || v.running:
		out = v.calmResult(summary, ascii)
	default:
		out = v.calmUnsent()
	}
	if !v.destinationCurrent() {
		out.Kind = calm.NotRead
		out.Sentences = append([]string{"Context changed; these answers are retained. Reopen :probe for the current context."}, out.Sentences...)
	}
	return out
}

func (v *probeView) calmTitle() calm.Title {
	title := calm.Title{View: probeTitle, Object: calmObject(v.app.Config.ActiveNamespace(), v.scope.Kind, v.scope.Namespace, v.scope.Name),
		Verb: "sent", Unit: "requests"}
	_, title.Window = v.requestWords()
	switch {
	case !v.destinationCurrent():
		title.Busy = "context changed"
	case v.loading || v.plan == nil && v.planErr == "":
		title.Busy = "reading the Pods…"
	case v.plan == nil:
	case v.running:
		title.Busy = fmt.Sprintf("sending… %d of %d done", v.run.Finished(), v.run.Planned())
	case v.ran():
		title.Read, title.Done, title.Total = v.ranAt, v.run.Sent(), v.run.Planned()
	default:
		title.Busy = "nothing sent"
	}
	return title
}

// calmMeasured is the one line that says what the times cover: the path
// the request took, the scale of the strips and the floor every reading
// includes. It is written as a line that is never left out, because a
// number here reads as the time a user waits without it. The long form of
// it is under a.
func (v *probeView) calmMeasured(summary *probe.Summary, width int) calm.Group {
	g := v.glyphs()
	path := "API server " + g.arrow + " Pod, not ingress or Service"
	meaning := path + ": a slow Pod, not a slow path"
	long := "API server " + g.arrow + " Pod proxy " + g.arrow + " Pod, not through the ingress or the Service: it locates a slow Pod, not a slow path"
	scale, bar := "", ""
	if v.ran() || v.running {
		scale, bar = "full bar "+g.full+" = "+probe.Seconds(summary.Top), g.full+" = "+probe.Seconds(summary.Top)
	}
	// The floor is the median of the fastest Pod: every reading includes
	// it. The line says whose time it is as long as it has room to.
	floor, named, brief := "", "", ""
	switch {
	case summary.FloorPod >= 0 && summary.Compared > 1:
		brief = "floor " + probe.Seconds(summary.Floor.Seconds())
		floor, named = brief+", the fastest Pod's median", brief+" (fastest Pod)"
	case summary.FloorPod >= 0:
		floor, named, brief = "includes proxy time", "includes proxy time", "includes proxy time"
	}
	room := width - calm.Indent - tviewCells(probeMeasuredLabel) - 4
	var items []string
	for _, form := range [][]string{
		{long, scale, floor}, {meaning, scale, floor}, {meaning, scale, named}, {meaning, scale, brief}, {meaning, bar, brief},
		{path, bar, named}, {path, bar, brief}, {path, bar}, {path},
	} {
		items = items[:0]
		for _, item := range form {
			if item != "" {
				items = append(items, item)
			}
		}
		if tviewCells(strings.Join(items, g.sep)) <= room {
			break
		}
	}
	return calm.Group{Label: probeMeasuredLabel, Role: calm.NotReadRole, Summary: append([]string(nil), items...)}
}

// calmNext is the likely actions, each with the key that really runs it.
func (v *probeView) calmNext(summary *probe.Summary, ascii bool) []calm.Action {
	if v.plan == nil {
		return nil
	}
	if v.running {
		return []calm.Action{{Key: "Esc", Label: "cancel; what was not sent stays unsent"}}
	}
	if !v.destinationCurrent() {
		return nil
	}
	var next []calm.Action
	runnable := v.runUnavailable() == ""
	target := v.selectedTarget()
	open := func() {
		if target != nil {
			next = append(next, calm.Action{Key: "Enter", Label: "open " + v.plan.ShortName(target.Pod.Name)})
		}
	}
	more := func() {
		for _, count := range probe.AttemptChoices {
			if count > v.attempts && runnable {
				next = append(next, calm.Action{Key: "+", Label: fmt.Sprintf("%d per pod", count)})
				break
			}
		}
	}
	if !v.ran() {
		if runnable {
			next = append(next, calm.Action{Key: "r", Label: "send " + probePlural(v.plan.Sendable()*v.attempts, "request", "requests") + " (asks first)"})
		}
		more()
		if len(v.plan.Available) > 1 {
			next = append(next, calm.Action{Key: "u", Label: v.otherKind() + " probe instead"})
		}
		open()
		return next
	}
	flagged := false
	for i := range summary.Pods {
		flagged = flagged || summary.Pods[i].Verdict.Flagged()
	}
	if flagged {
		open()
		// A check is proposed only when its key would act: the graph needs
		// a declared Prometheus source.
		if target != nil {
			pod := SelectedResourceTarget{Context: v.contextName, GVR: client.PodGVR, Namespace: target.Pod.Namespace, Name: target.Pod.Name}
			if graphAvailability(v.app, pod) == "" {
				next = append(next, calm.Action{Key: "m", Label: "graph its CPU"})
			}
		}
		if runnable {
			next = append(next, calm.Action{Key: "r", Label: "run again"})
		}
	} else {
		if runnable {
			next = append(next, calm.Action{Key: "r", Label: "run again (this run stays as the dimmer strip)"})
		}
		more()
	}
	if _, err := slowTarget(v.scopeTarget()); err == nil {
		next = append(next, calm.Action{Key: calmSlowKeyName, Label: "slow requests by hop"})
	}
	return next
}

// calmKeys is every other key, in the order a narrow line keeps them.
func (v *probeView) calmKeys() []string {
	if v.plan == nil {
		return (&calmKeyLine{}).parts()
	}
	if v.running {
		// Esc is on the first key line while a probe runs: it cancels.
		return []string{"↑↓ pod", calm.DetailKey + " every time and status code"}
	}
	g := v.glyphs()
	keys := calmKeyLine{Move: "↑↓ pod", Detail: "all detail"}
	if v.ran() {
		keys.Detail = "every time and status code"
	}
	if v.runUnavailable() == "" {
		keys.Own = append(keys.Own, fmt.Sprintf("+/_ %s%d per pod", g.times, v.attempts))
		if v.ran() && len(v.plan.Available) > 1 {
			keys.Own = append(keys.Own, "u "+v.otherKind()+" probe")
		}
	}
	keys.Own = append(keys.Own, "t timeline", "m graph", calmGrafanaKeyName+" Grafana")
	return keys.parts()
}

// calmPage builds the page from the plan and the runs.
func (v *probeView) calmPage(summary *probe.Summary, width int, o calm.Options) calm.Page {
	p := o.Palette
	page := calm.Page{Title: v.calmTitle(), Verdict: v.calmVerdict(summary, o.ASCII), NumberAfter: true}
	page.Keys = v.calmKeys()
	if v.plan == nil || len(v.plan.Targets) == 0 {
		return page
	}
	if v.selectedTarget() == nil {
		v.selected = v.plan.Targets[0].Pod.Name
	}
	strips := v.ran() || v.running
	page.Counted = strips
	headed := false
	for _, group := range v.calmGroups(summary) {
		out := calm.Group{Label: group.label, Role: group.role}
		if group.warn {
			out.Color = p.Warn
		}
		for _, index := range group.targets {
			row := v.calmRow(index, summary, strips, o)
			out.Rows = append(out.Rows, row)
			if group.role == calm.Secondary {
				out.Summary = append(out.Summary, strings.TrimSpace(row.Subject+" "+row.Number))
			}
			if row.Selected && group.role == calm.Secondary {
				// The group the cursor is in never folds: its rows are
				// what the keys act on.
				out.Role = calm.Primary
			}
		}
		if strips && !headed && group.role != calm.NotReadRole {
			out.Head, headed = "median", true
		}
		page.Groups = append(page.Groups, out)
	}
	var gaps []string
	if v.plan.LeftOut > 0 {
		gaps = append(gaps, fmt.Sprintf("%s beyond the limit of %d are not listed and not probed",
			probePlural(v.plan.LeftOut, "more Pod", "more Pods"), probe.MaxPods))
	}
	if gaps = append(gaps, v.plan.Notes...); len(gaps) > 0 {
		page.Groups = append(page.Groups, calm.Group{Label: probeGapsLabel, Color: p.Warn, Role: calm.NotReadRole, Summary: gaps})
	}
	if strips {
		page.Groups = append(page.Groups, v.calmMeasured(summary, width))
	}
	page.Next = v.calmNext(summary, o.ASCII)
	return page
}

// renderCalm draws the calm page into the area the view has.
func (v *probeView) renderCalm(summary *probe.Summary) {
	o := calmOptions(v.app)
	width, height := v.viewWidth(), calmHeight(v.height)
	page := v.calmPage(summary, width, o)
	plan := calm.Lay(&page, width, height, o)
	if calmKeepSelected(&page, &plan) {
		plan = calm.Lay(&page, width, height, o)
	}
	v.dropped = plan.Dropped
	v.calm.SetText(strings.Join(calm.Render(&page, &plan, o).Lines(), "\n"))
	v.calm.ScrollToBeginning()
}
