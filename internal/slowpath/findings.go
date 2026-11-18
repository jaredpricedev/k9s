// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/impact"
)

// Relation says how the marks of a lane stand to the episodes of a report.
// It is computed from interval overlap only, with the same measure the
// summary uses (OverlapCount): it says when, never why.
type Relation int

const (
	// LaneQuiet is a lane that was read and has no mark inside the window.
	LaneQuiet Relation = iota
	// LaneSymptom is the lane the episodes are counted from.
	LaneSymptom
	// LaneEvery is a lane whose marks overlap every episode.
	LaneEvery
	// LaneSome is a lane whose marks overlap some episodes, not all.
	LaneSome
	// LaneOutside is a lane that is marked inside the window and overlaps no
	// episode: outside every one of them, or in a window without episodes.
	LaneOutside
	// LaneNotRead is a lane without data. It is never quiet.
	LaneNotRead
)

// Finding is one lane and how it stands to the episodes.
type Finding struct {
	Lane     *Lane
	Relation Relation
	// Marks are the marks of the lane inside the window.
	Marks []chart.Interval
	// Overlapped is the number of episodes the marks overlap.
	Overlapped int
	// Hops are the hops the lane stands for: one, or the dependencies that
	// share a lane. A lane of dated facts has none.
	Hops []*Hop
	// Signal is the one signal the lane's phrase names, or nil for a lane
	// of dated facts and for a lane that was not read.
	Signal *SignalRead
}

// Findings states every lane of the report against the episodes, in lane
// order. A lane is not read, or quiet, or stands in one of four ways to the
// episodes; the page that lays the lanes out by what lines up is built from
// this and decides nothing itself.
func (r *Report) Findings() []Finding {
	out := make([]Finding, 0, len(r.Lanes))
	own := map[string]bool{}
	for i := range r.Lanes {
		own[r.Lanes[i].ID] = true
	}
	for i := range r.Lanes {
		lane := &r.Lanes[i]
		finding := Finding{Lane: lane, Hops: r.laneHops(lane, own)}
		finding.Marks = Clip(lane.Marks, r.Start, r.End)
		if lane.Signal && len(lane.Names) > 0 && len(finding.Hops) == 1 {
			finding.Signal = finding.Hops[0].Signal(lane.Names[0])
			if lane.Hop == HopService {
				// The lane names what its marks mean, not the signal.
				finding.Signal = finding.Hops[0].Signal(SignalReady)
			}
		}
		finding.Overlapped = OverlapCount(r.Episodes, finding.Marks)
		switch {
		case lane.NotRead != "":
			finding.Relation, finding.Signal = LaneNotRead, nil
		case i == r.Symptom && len(r.Episodes) > 0:
			finding.Relation = LaneSymptom
		case len(finding.Marks) == 0:
			finding.Relation = LaneQuiet
		case finding.Overlapped == 0:
			finding.Relation = LaneOutside
		case finding.Overlapped == len(r.Episodes):
			finding.Relation = LaneEvery
		default:
			finding.Relation = LaneSome
		}
		out = append(out, finding)
	}
	return out
}

// PodSeries is the Pod's series of the signal: the worst one when the
// signal keeps several apart, nil when the Pod has none.
func (s *SignalRead) PodSeries(pod string) *Series { return s.seriesOf(pod) }

// laneHops lists the hops a lane stands for. own holds the lanes that have
// the identifier of their hop: a dependency without one shares a lane.
func (r *Report) laneHops(lane *Lane, own map[string]bool) []*Hop {
	switch {
	case lane.Hop == "":
		return nil
	case lane.Hop != HopDependencies:
		if hop := r.Hop(lane.Hop); hop != nil {
			return []*Hop{hop}
		}
		return nil
	}
	var hops []*Hop
	for i := range r.Hops {
		hop := &r.Hops[i]
		if !strings.HasPrefix(hop.Spec.ID, depPrefix) || own[hop.Spec.ID] {
			continue
		}
		if (hop.State == NotRead) == (lane.NotRead != "") {
			hops = append(hops, hop)
		}
	}
	return hops
}

// AnswerKind is what a read came to.
type AnswerKind int

const (
	// AnswerEpisodes is a window with episodes: the answer states them and
	// what lines up with them.
	AnswerEpisodes AnswerKind = iota
	// AnswerQuiet is a window in which no signal that was read crossed its
	// rule. It is not health.
	AnswerQuiet
	// AnswerNotRead is a read that has no latency evidence at all.
	AnswerNotRead
)

// Answer is what a read came to in plain sentences, for the top of a page.
// Every sentence is computed from the episodes and from interval overlap;
// none names a cause.
type Answer struct {
	Kind AnswerKind
	// Noun and Nouns name one episode and several: "stall" when the
	// episodes are client give-ups (HTTP 499 is a client that closed the
	// request while waiting), "episode" for any other signal.
	Noun, Nouns string
	// Lead states the episodes: how many, when they began and how long they
	// lasted. Brief is Lead without the times, for a page that draws them.
	// For a quiet window and a read without evidence, Lead says so.
	Lead, Brief string
	// Edge states an episode the window cuts: one whose first sample of the
	// window was already raised, or whose last was still raised. Its length
	// is then only what the window holds; of a single episode that was
	// already raised Lead says "at least". Empty when the window holds
	// every episode whole.
	Edge string
	// Lines states what lines up with every episode. Counted is the same
	// with the signals of one place counted instead of named, for a page
	// whose rows name them.
	Lines, Counted string
	// More states what lines up with some episodes, or that nothing else
	// does.
	More []string
	// Differs states the Pods whose usual level differs from their peers'
	// whether or not there is an episode: one sentence per Pod.
	Differs []string
	// Coarse is what a wide window cannot show; empty for a narrow one.
	Coarse string
	// Caveat is the evidential limit of the answer.
	Caveat string
}

// spoken writes a length of time in words when it is whole minutes under an
// hour, and compactly otherwise.
func spoken(d time.Duration) string {
	if d <= 0 || d >= time.Hour || d%time.Minute != 0 {
		return impact.Dur(d)
	}
	if d == time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", int(d/time.Minute))
}

// spokenRange writes "2 to 4 minutes".
func spokenRange(shortest, longest time.Duration) string {
	whole := func(d time.Duration) bool { return d > 0 && d < time.Hour && d%time.Minute == 0 }
	if whole(shortest) && whole(longest) {
		return fmt.Sprintf("%d to %s", int(shortest/time.Minute), spoken(longest))
	}
	return impact.Dur(shortest) + " to " + impact.Dur(longest)
}

// giveUps reports whether the episodes are counted from client give-ups at
// the ingress.
func giveUps(symptom *Lane) bool {
	return symptom.Hop == HopIngress && len(symptom.Names) > 0 && symptom.Names[0] == "499"
}

// coarse is what a window with wide samples cannot show.
func (r *Report) coarse() string {
	if r.Step < CoarseStep || r.Plan == nil {
		return ""
	}
	return fmt.Sprintf(
		"A sample here stands for %s and a rate for %s: a stall of a few minutes merges with the next one or disappears; 1h or 15m shows it.",
		impact.Dur(r.Step), impact.Dur(r.Plan.RateWindow))
}

// Answer states the read in plain sentences: the episodes and what lines up
// with them, or that the window was quiet for what was read, or that there
// was nothing to read it from.
func (r *Report) Answer() Answer {
	a := Answer{Noun: "episode", Nouns: "episodes", Coarse: r.coarse(), Differs: r.differs()}
	findings := r.Findings()
	unread := r.Hop(HopDependencies) != nil
	// read says a hop was read and stayed under its rules.
	read := false
	for i := range findings {
		unread = unread || findings[i].Relation == LaneNotRead
		read = read || (findings[i].Relation == LaneQuiet && findings[i].Lane.Hop != "")
	}
	if r.Symptom < 0 || len(r.Episodes) == 0 {
		switch {
		case r.Failure != "":
			a.Kind, a.Lead = AnswerNotRead, "Prometheus was not read ("+r.Failure+"), so no hop has latency evidence."
		case !read:
			a.Kind, a.Lead = AnswerNotRead, "No hop could be read for the last "+r.Span()+"."
		default:
			a.Kind, a.Lead = AnswerQuiet, "No signal that was read crossed its rule in the last "+r.Span()+"."
		}
		a.Brief = a.Lead
		if len(a.Differs) > 0 {
			a.Caveat = "A difference, not proof of cause."
		}
		return a
	}
	symptom := &r.Lanes[r.Symptom]
	if giveUps(symptom) {
		a.Noun, a.Nouns = "stall", "stalls"
	}
	a.Caveat = "Overlap in time, not proof of cause."
	a.Lead, a.Brief, a.Edge = r.lead(symptom, a.Noun, a.Nouns)
	r.linesUp(&a, findings, unread)
	return a
}

// linesUp states what lines up with the episodes: with every one of them,
// with some, or that nothing else does. unread says a hop was not read, so
// "nothing" is said only of what was.
func (r *Report) linesUp(a *Answer, findings []Finding, unread bool) {
	count := len(r.Episodes)
	every, both, them := fmt.Sprintf("All %d line up with", count), fmt.Sprintf("all %d", count), "them"
	switch count {
	case 1:
		every, both, them = "It lines up with", "it", "it"
	case 2:
		every, both = "Both line up with", "both"
	}
	// Lanes that overlap the same episodes share a sentence, so that
	// "2 of 3" never joins two lanes that overlap different ones.
	var all []*Lane
	var keys []string
	some, hits := map[string][]*Lane{}, map[string]int{}
	for i := range findings {
		switch finding := &findings[i]; finding.Relation {
		case LaneEvery:
			all = append(all, finding.Lane)
		case LaneSome:
			key := ""
			for _, episode := range r.Episodes {
				if OverlapsAny(episode, finding.Marks) {
					key += "x"
				} else {
					key += "-"
				}
			}
			if _, seen := some[key]; !seen {
				keys = append(keys, key)
			}
			some[key], hits[key] = append(some[key], finding.Lane), finding.Overlapped
		}
	}
	sort.SliceStable(keys, func(a, b int) bool { return hits[keys[a]] > hits[keys[b]] })
	that := ""
	if unread {
		that = " that was read"
	}
	also := ""
	switch {
	case len(all) > 0:
		a.Lines = every + " " + grouped(all) + "."
		a.Counted, also = a.Lines, " also"
		if where, ok := onePlace(all); ok {
			a.Counted = fmt.Sprintf("%s %d signals %s.", every, len(all), where)
		}
	case len(keys) > 0:
		a.Lines = "Nothing" + that + " lines up with " + both + "."
		a.Counted = a.Lines
	default:
		a.Lines = "Nothing else" + that + " lines up with " + them + "."
		a.Counted = a.Lines
	}
	for _, key := range keys {
		verb := "line"
		if hits[key] == 1 {
			verb = "lines"
		}
		a.More = append(a.More, fmt.Sprintf("%d of %d%s %s up with %s.", hits[key], count, also, verb, grouped(some[key])))
	}
	if len(all) > 0 && len(keys) == 0 {
		a.More = append(a.More, "Nothing else"+that+" lines up.")
	}
}

// Usual is one Pod whose usual level differs from its peers' on a signal of
// the app hop. It is a difference over the whole window and says nothing
// about any episode: the Pod need never have crossed the signal's rule.
type Usual struct {
	Signal    *SignalRead
	Deviation Deviation
}

// Usuals lists the Pods of the app hop whose usual level differs from
// their peers', in the order of the hop's signals.
func (r *Report) Usuals() []Usual {
	app := r.Hop(HopApp)
	if app == nil {
		return nil
	}
	var out []Usual
	for i := range app.Signals {
		for _, deviation := range app.Signals[i].Outliers {
			if deviation.Sustained {
				out = append(out, Usual{Signal: &app.Signals[i], Deviation: deviation})
			}
		}
	}
	return out
}

// differs states each Pod whose usual level differs from its peers', with
// the signals it differs on: "Pod …-hx4mt usually differs from its peers:
// p99 2s (peers 305ms)."
func (r *Report) differs() []string {
	var pods []string
	parts := map[string][]string{}
	for _, usual := range r.Usuals() {
		pod, sig := usual.Deviation.Pod, usual.Signal
		if _, seen := parts[pod]; !seen {
			pods = append(pods, pod)
		}
		parts[pod] = append(parts[pod], sig.Name+" "+sig.Format(usual.Deviation.Value)+" (peers "+sig.Format(usual.Deviation.Peers)+")")
	}
	out := make([]string, 0, len(pods))
	for _, pod := range pods {
		out = append(out, "Pod "+r.ShortPod(pod)+" usually differs from its peers: "+list(parts[pod])+".")
	}
	return out
}

// onePlace reports the place several signal lanes share, when they all
// share one: their signals can then be counted instead of named.
func onePlace(lanes []*Lane) (where string, ok bool) {
	if len(lanes) < 2 {
		return "", false
	}
	for _, lane := range lanes {
		if !lane.Signal || lane.Where == "" || lane.Where != lanes[0].Where {
			return "", false
		}
	}
	return lanes[0].Where, true
}

// cut reports whether the window cuts the episodes: open says the first
// was already raised at the first sample of the window, ongoing that the
// last was still raised at the last one. A stretch keeps such an end where
// the window puts it (see unsmear), so what is stated of it is only the
// part the window holds.
func (r *Report) cut() (open, ongoing bool) {
	if len(r.Episodes) == 0 {
		return false, false
	}
	first, last := r.Episodes[0], r.Episodes[len(r.Episodes)-1]
	// The last sample is less than a step before the end of the window.
	return !first.Start.After(r.Start), last.End.Add(r.Step).After(r.End)
}

// edge says in a sentence which episode the window cuts, and what that
// leaves unknown. Of a single episode that was already raised the lead
// says "at least", so the sentence does not say it again.
func (r *Report) edge() string {
	open, ongoing := r.cut()
	const began, lasts = "was already raised when the window began", "still raised at the last sample"
	if len(r.Episodes) == 1 {
		switch {
		case open && ongoing:
			return "It " + began + " and " + lasts + ": it may not be over."
		case open:
			return "It " + began + "."
		case ongoing:
			return "It was " + lasts + ": it may not be over."
		}
		return ""
	}
	switch {
	case open && ongoing:
		return "The first " + began + " and may be longer; the last was " + lasts + " and may not be over."
	case open:
		return "The first " + began + ": it may be longer."
	case ongoing:
		return "The last was " + lasts + ": it may not be over."
	}
	return ""
}

// lead states the episodes of the symptom lane: with the times they began,
// and without; and edge, the episode the window cuts.
func (r *Report) lead(symptom *Lane, noun, nouns string) (lead, brief, edge string) {
	count := len(r.Episodes)
	shortest, longest := time.Duration(0), time.Duration(0)
	starts := make([]string, 0, count)
	for index, episode := range r.Episodes {
		length := episode.End.Sub(episode.Start)
		if index == 0 || length < shortest {
			shortest = length
		}
		longest = max(longest, length)
		if index < maxEpisodeTimes {
			starts = append(starts, r.episodeClock(episode))
		}
	}
	when := list(starts) + " UTC"
	if count > maxEpisodeTimes {
		when = fmt.Sprintf("%s UTC and %d more times", strings.Join(starts, ", "), count-maxEpisodeTimes)
	}
	lengths := "for " + spoken(shortest) + " each"
	switch {
	case count == 1:
		lengths = "for " + spoken(shortest)
		nouns = noun
		if open, _ := r.cut(); open {
			// Only the part inside the window was seen. One that is still
			// raised at the end keeps its length: a rate stays raised while
			// a burst leaves its window, so it may as well be shorter.
			lengths = "for at least " + spoken(shortest)
		}
	case shortest != longest:
		lengths = "for " + spokenRange(shortest, longest) + " each"
	}
	what := fmt.Sprintf("%d %s of %s", count, nouns, strings.TrimSpace(symptom.Phrase+" "+symptom.Where))
	if giveUps(symptom) {
		// HTTP 499 is a client that closed the request while it waited.
		what = fmt.Sprintf("%d %s: clients gave up waiting", count, nouns)
	}
	return what + " at " + when + ", " + lengths + ".", what + ", " + lengths + ".", r.edge()
}

// LaneDetail states a lane of dated facts in full for the detail area: what
// it is read from and every fact with its time, or why it was not read.
func (r *Report) LaneDetail(lane *Lane) []string {
	from := map[string]string{
		"restarts": "the restart counter when it is read, and the last restart the Kubernetes API reports per Pod",
		"rollouts": "retained revisions of the workload and Pods created in the window, from the Kubernetes API",
		"alerts":   "the alerts of the namespace, from the declared Alertmanager",
	}
	head := lane.Label
	if text := from[lane.ID]; text != "" {
		head += " · " + text
	}
	lines := []string{head}
	switch {
	case lane.NotRead != "":
		return append(lines, "  not read: "+lane.NotRead)
	case len(lane.Events) == 0:
		lines = append(lines, "  none in the last "+r.Span())
	}
	events := append([]Event(nil), lane.Events...)
	sort.SliceStable(events, func(a, b int) bool { return events[a].At.Before(events[b].At) })
	for _, event := range events {
		lines = append(lines, "  "+r.Clock(event.At)+" UTC  "+event.Label)
	}
	if unknown := Total(Clip(lane.Unknown, r.Start, r.End)); unknown > 0 {
		lines = append(lines, "  no data for "+impact.Dur(unknown)+" of "+r.Span()+": nothing is known about that time")
	}
	return lines
}
