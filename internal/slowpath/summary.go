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

// Sentence is one statement of the summary. Rank orders what stays when
// the screen is small: a lower rank is kept first.
type Sentence struct {
	Text string
	Rank int
}

// Ranks of summary sentences.
const (
	RankOverlap = iota // what overlaps every episode
	RankCaveat         // order, not cause; quiet is not healthy
	RankOutside        // what was raised outside every episode
	RankUnread         // what was not read
	RankPartial        // what overlaps some episodes
	RankNone           // what overlaps none
	RankContext        // shares and other numbers
)

const (
	// EpisodeGap is how many steps apart two crossings may be and still be
	// one episode.
	EpisodeGap = 2
	// BeforeHorizon is how long before an episode a dated fact is still
	// worth naming as "before" it. It is order in time, never a cause.
	BeforeHorizon = 15 * time.Minute
	// CoarseStep is the sample spacing from which a stall of a few minutes
	// can merge with the next one or fall between two samples.
	CoarseStep = 2 * time.Minute
	// maxEpisodeTimes bounds the start times the headline lists.
	maxEpisodeTimes = 6
)

// list joins words as "a, b and c".
func list(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// either joins words as "a, b or c".
func either(words []string) string {
	if len(words) < 2 {
		return list(words)
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

func capital(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// first is the time a stretch is stated by: for a stretch made of samples
// the sample that ends its first step, which is lead after the stretch
// begins; for a moment, the moment.
func first(iv chart.Interval, lead time.Duration) time.Time {
	if iv.End.After(iv.Start) {
		return iv.Start.Add(lead)
	}
	return iv.Start
}

// stretches writes when a lane was marked: "07:39 for 3m".
func (r *Report) stretches(lane *Lane, limit int) string {
	var parts []string
	for index, iv := range lane.Marks {
		if index >= limit {
			parts = append(parts, fmt.Sprintf("+%d more", len(lane.Marks)-limit))
			break
		}
		text := r.Clock(first(iv, lane.Lead))
		if iv.End.After(iv.Start) {
			text += " for " + impact.Dur(iv.End.Sub(iv.Start))
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", ")
}

// episodeClock is the time an episode is named by: its first raised sample.
func (r *Report) episodeClock(episode chart.Interval) string {
	return r.Clock(first(episode, r.Step))
}

// grouped joins lane phrases, naming a place once for the lanes that share
// it: "slow p99, queueing and CPU throttling on pod hx4mt".
func grouped(lanes []*Lane) string {
	var (
		order  []string
		groups = map[string][]string{}
	)
	for _, lane := range lanes {
		if _, seen := groups[lane.Where]; !seen {
			order = append(order, lane.Where)
		}
		groups[lane.Where] = append(groups[lane.Where], lane.Phrase)
	}
	parts := make([]string, 0, len(order))
	for _, where := range order {
		parts = append(parts, strings.TrimSpace(list(groups[where])+" "+where))
	}
	if len(parts) > 2 {
		// Several places: a semicolon keeps each "a and b on x" together.
		return strings.Join(parts, "; ")
	}
	return list(parts)
}

// named collects short names once each, in the order they are met.
type named struct {
	seen  map[string]bool
	names []string
}

func (n *named) add(short string) {
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	if !n.seen[short] {
		n.seen[short] = true
		n.names = append(n.names, short)
	}
}

func (r *Report) say(rank int, text string) {
	r.Summary = append(r.Summary, Sentence{Text: text, Rank: rank})
}

// summarize states the episodes and what overlaps them. It is computed
// from interval overlap only and names no cause.
func (r *Report) summarize() {
	r.Symptom, r.Episodes, r.Headline, r.HeadlineShort, r.Summary = -1, nil, "", "", nil
	for i := range r.Lanes {
		if r.Lanes[i].Signal && len(r.Lanes[i].Marks) > 0 {
			r.Symptom = i
			break
		}
	}
	var unread named
	if r.Hop(HopDependencies) != nil {
		unread.add("dependencies (none declared)")
	}
	if r.Symptom < 0 {
		r.summarizeQuiet(&unread)
	} else {
		r.summarizeEpisodes(&unread)
	}
	// What a wide window cannot show.
	if text := r.coarse(); text != "" {
		r.say(RankCaveat, text)
	}
}

// summarizeQuiet is the summary of a window in which no signal crossed
// its rule: quiet for what was read, and what was not read.
func (r *Report) summarizeQuiet(unread *named) {
	var (
		quiet         named
		facts, absent []string
	)
	for i := range r.Lanes {
		lane := &r.Lanes[i]
		switch {
		case lane.NotRead != "":
			unread.add(lane.Short)
		case len(lane.Marks) > 0:
			facts = append(facts, strings.TrimSpace(lane.Phrase+" "+lane.Where)+" at "+r.stretches(lane, 3))
		case lane.Hop != "":
			quiet.add(lane.Short)
		default:
			absent = append(absent, lane.Short)
		}
	}
	switch {
	case r.Failure != "":
		r.Headline = "Prometheus was not read (" + r.Failure + "), so no hop has latency evidence."
	case len(quiet.names) == 0:
		r.Headline = "No hop could be read for the last " + r.Span() + "."
	default:
		r.Headline = "No signal crossed its rule in the last " + r.Span() + "."
		r.say(RankOverlap, "Quiet for the hops that were read: "+list(quiet.names)+".")
	}
	if len(facts) > 0 {
		r.say(RankOutside, "In the window: "+strings.Join(facts, "; ")+".")
	}
	if len(absent) > 0 {
		r.say(RankNone, "No "+either(absent)+" in the window.")
	}
	if len(unread.names) > 0 {
		r.say(RankCaveat, "Not read: "+list(unread.names)+".")
	}
	if len(quiet.names) > 0 {
		r.say(RankCaveat, "Quiet is not healthy: a rule compares each signal with its own usual level in this window.")
	} else {
		r.say(RankCaveat, "Not read is not quiet and not healthy.")
	}
}

// headline counts the episodes of the symptom lane and states when they
// began and how long they lasted.
func (r *Report) headline(symptom *Lane) {
	marks := symptom.Marks
	if symptom.Named != nil {
		// The headline names one signal; its stretches are the episodes.
		marks = symptom.Named
	}
	r.Episodes = Clip(Join(marks, EpisodeGap*r.Step), r.Start, r.End)
	count := len(r.Episodes)
	noun := "episodes"
	if count == 1 {
		noun = "episode"
	}
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
	if count > maxEpisodeTimes {
		starts = append(starts, fmt.Sprintf("+%d more", count-maxEpisodeTimes))
	}
	lengths := impact.Dur(shortest) + " each"
	switch {
	case count == 1:
		lengths = "for " + impact.Dur(shortest)
	case shortest != longest:
		lengths = impact.Dur(shortest) + "-" + impact.Dur(longest) + " each"
	}
	r.Headline = fmt.Sprintf("%d %s of %s %s: %s UTC, %s.", count, noun, symptom.Phrase, symptom.Where, strings.Join(starts, ", "), lengths)
	r.HeadlineShort = fmt.Sprintf("%d %s of %s %s, %s.", count, noun, symptom.Phrase, symptom.Where, lengths)
}

// context states the symptom as a share, and whether errors rose with it:
// give-ups without 5xx are requests that waited, not requests refused.
func (r *Report) context(symptom *Lane) string {
	hop := r.Hop(symptom.Hop)
	if hop == nil || hop.Main < 0 {
		return ""
	}
	main := &hop.Signals[hop.Main]
	text := ""
	if main.Share > 0 {
		text = "At worst " + chart.Ratio.Format(main.Share) + " of " + main.ShareOf + "."
	}
	errors := hop.Signal("5xx")
	if errors == nil || !errors.Read || main.Name != "499" {
		return text
	}
	if len(errors.Crossed) == 0 {
		return strings.TrimSpace(text + " 5xx stayed at " + errors.Format(errors.Worst) + ": clients gave up waiting, they were not refused.")
	}
	return strings.TrimSpace(text + " 5xx reached " + errors.Format(errors.Worst) + ".")
}

// shortlyBefore says when a dated fact (a rollout, a restart) happened
// shortly before an episode began: "; 1m before the 07:47 one".
func (r *Report) shortlyBefore(lane *Lane) string {
	if lane.Signal {
		return ""
	}
	for _, mark := range lane.Marks {
		for _, episode := range r.Episodes {
			if ahead := first(episode, r.Step).Sub(first(mark, lane.Lead)); ahead > 0 && ahead <= BeforeHorizon && !Overlap(episode, mark) {
				return "; " + impact.Dur(ahead) + " before the " + r.episodeClock(episode) + " one"
			}
		}
	}
	return ""
}

// overlapping sorts the lanes beside the symptom by which episodes they
// overlap. Lanes that overlap the same episodes share a key, so that
// "2 of 3" never joins two lanes that overlap different ones.
type overlapping struct {
	all, outside []*Lane
	keys         []string
	partial      map[string][]*Lane
	count        map[string]int
	// none names what overlaps no episode: lanes without marks and lanes
	// whose marks are all outside.
	none named
}

func (r *Report) overlapping(unread *named) *overlapping {
	symptom := &r.Lanes[r.Symptom]
	o := &overlapping{partial: map[string][]*Lane{}, count: map[string]int{}}
	var quiet named
	// A hop named as overlapping is not also listed as overlapping none.
	taken := map[string]bool{symptom.Short: true}
	for i := range r.Lanes {
		lane := &r.Lanes[i]
		switch {
		case i == r.Symptom:
			continue
		case lane.NotRead != "":
			unread.add(lane.Short)
			continue
		case len(lane.Marks) == 0:
			quiet.add(lane.Short)
			continue
		}
		var which []string
		for _, episode := range r.Episodes {
			if OverlapsAny(episode, lane.Marks) {
				which = append(which, r.episodeClock(episode))
			}
		}
		switch {
		case len(which) == len(r.Episodes):
			o.all = append(o.all, lane)
			taken[lane.Short] = true
		case len(which) > 0:
			key := strings.Join(which, ", ")
			if _, seen := o.partial[key]; !seen {
				o.keys = append(o.keys, key)
			}
			o.partial[key], o.count[key] = append(o.partial[key], lane), len(which)
			taken[lane.Short] = true
		default:
			o.outside = append(o.outside, lane)
		}
	}
	for _, short := range quiet.names {
		if !taken[short] {
			o.none.add(short)
		}
	}
	for _, lane := range o.outside {
		if !taken[lane.Short] {
			o.none.add(lane.Short)
		}
	}
	sort.SliceStable(o.keys, func(a, b int) bool { return o.count[o.keys[a]] > o.count[o.keys[b]] })
	return o
}

// summarizeEpisodes states which lanes overlap the episodes of the symptom
// lane, which overlap none, and what was raised outside every episode.
func (r *Report) summarizeEpisodes(unread *named) {
	symptom := &r.Lanes[r.Symptom]
	r.headline(symptom)
	count := len(r.Episodes)
	o := r.overlapping(unread)
	every, no := fmt.Sprintf("All %d overlap", count), "None overlaps"
	switch count {
	case 1:
		every, no = "It overlaps", "It does not overlap"
	case 2:
		every, no = "Both overlap", "Neither overlaps"
	}
	if len(o.all) > 0 {
		r.say(RankOverlap, every+" "+grouped(o.all)+".")
	}
	for _, key := range o.keys {
		verb := "overlap"
		if o.count[key] == 1 {
			verb = "overlaps"
		}
		r.say(RankPartial, fmt.Sprintf("%d of %d (%s) %s %s.", o.count[key], count, key, verb, grouped(o.partial[key])))
	}
	if len(o.none.names) > 0 {
		r.say(RankNone, no+" "+either(o.none.names)+".")
	}
	for _, lane := range o.outside {
		r.say(RankOutside, capital(strings.TrimSpace(lane.Phrase+" "+lane.Where))+": "+r.stretches(lane, 3)+
			", outside every episode"+r.shortlyBefore(lane)+".")
	}
	if len(unread.names) > 0 {
		r.say(RankUnread, "Not read: "+list(unread.names)+".")
	}
	if context := r.context(symptom); context != "" {
		r.say(RankContext, context)
	}
	r.say(RankCaveat, "Overlap is order in time, not cause.")
}
