// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package probe

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/chart"
)

// Verdict is the one word a row carries. It compares a Pod with its peers
// in this run; it is never a statement about health or cause.
type Verdict int

const (
	// NotContacted means the plan sends nothing to the Pod.
	NotContacted Verdict = iota
	// Unprobed means nothing has been sent to the Pod yet.
	Unprobed
	// NoAnswer means requests left and none was answered.
	NoAnswer
	// Outlier means the Pod's median is far above its peers' medians.
	Outlier
	// Spikes means the median is like the peers' but its slowest answer is
	// far above theirs: slow some of the time.
	Spikes
	// Timeouts means some requests got no answer within the deadline and
	// nothing else singles the Pod out. It is a fact about this Pod, not a
	// comparison, so it is stated for one or two Pods as well, and when
	// every Pod has them.
	Timeouts
	// Errors means it answered, but not always with a 2xx or 3xx status.
	Errors
	// LikePeers means nothing separates it from its peers in this run.
	LikePeers
	// NoPeers means it answered and there are too few Pods to compare.
	NoPeers
	// Canceled means requests left for the Pod and the run ended before
	// any of them had an outcome.
	Canceled
)

// Word is the row word of a verdict.
func (v Verdict) Word() string {
	switch v {
	case NotContacted:
		return "not probed"
	case Unprobed:
		return "not sent"
	case NoAnswer:
		return "NO ANSWER"
	case Outlier:
		return "OUTLIER"
	case Spikes:
		return "SPIKES"
	case Timeouts:
		return "TIMEOUTS"
	case Errors:
		return "ERRORS"
	case LikePeers:
		return "like peers"
	case Canceled:
		return "canceled"
	}
	return "answered"
}

// Flagged says whether the verdict marks the row.
func (v Verdict) Flagged() bool {
	return v == NoAnswer || v == Outlier || v == Spikes || v == Timeouts || v == Errors
}

// Peer-comparison thresholds. A Pod is an outlier only when it is also this
// much slower than its peers in absolute terms, so that a few milliseconds
// of jitter between fast Pods never reads as a difference.
const (
	medianFloor  = 0.1 // seconds
	slowestFloor = 0.5 // seconds
)

// CodeCount is how often one HTTP status was answered.
type CodeCount struct {
	Code, Count int
}

// PodStats summarizes the attempts made against one Pod.
type PodStats struct {
	// Sent counts requests that left, Finished those of them with an
	// outcome. The difference is Waiting (in flight) plus Abandoned (the
	// run was canceled before the answer).
	Planned, Sent, Finished, Answered, TimedOut int
	Waiting, Abandoned                          int
	// Unanswered counts requests that left and got no answer from the Pod
	// for another reason (denied, unreachable, gone, failed).
	Unanswered int
	// Fastest, Median and Slowest are over answered requests only.
	Fastest, Median, Slowest time.Duration
	// Typical is the median in seconds with each timeout counted as the
	// deadline, and AtLeast says that it is therefore a lower bound. It is
	// NaN when nothing was answered or timed out.
	Typical float64
	AtLeast bool
	// Worst is the slowest request in seconds, a timeout counted as the
	// deadline; WorstAtLeast marks that lower bound.
	Worst        float64
	WorstAtLeast bool
	Codes        []CodeCount
	// Failure is the phrase of the first unanswered request.
	Failure string
	Verdict Verdict
	// Peers is the median of the other Pods' Typical (for Outlier) or
	// Worst (for Spikes), in seconds.
	Peers float64
}

// Summary is one run read as a whole.
type Summary struct {
	Pods []PodStats
	// Compared is the number of Pods with a timing; Enough says whether
	// that is enough to call one of them different.
	Compared int
	Enough   bool
	// Floor is the lowest per-Pod median, the overhead of the proxy path
	// that every reading includes; FloorPod is its target index, or -1.
	Floor    time.Duration
	FloorPod int
	// Top is the value drawn as a full bar, in seconds.
	Top float64
	// SlowLine is the time from which a single answer is drawn as slow.
	SlowLine float64
	// Denied says the API server refused pods/proxy.
	Denied bool
}

func seconds(d time.Duration) float64 { return d.Seconds() }

func medianOf(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func podStats(attempts []Attempt, deadline time.Duration) PodStats {
	stats := PodStats{Planned: len(attempts), Typical: math.NaN(), Worst: math.NaN()}
	var answered, timed []float64
	codes := map[int]int{}
	for _, attempt := range attempts {
		if !attempt.Sent() {
			continue
		}
		stats.Sent++
		if attempt.Finished() {
			stats.Finished++
		}
		switch attempt.Outcome {
		case InFlight:
			stats.Waiting++
		case Abandoned:
			stats.Abandoned++
		case Answered:
			stats.Answered++
			answered = append(answered, seconds(attempt.Took))
			codes[attempt.Code]++
		case TimedOut:
			stats.TimedOut++
			timed = append(timed, seconds(deadline))
		default:
			stats.Unanswered++
			if stats.Failure == "" {
				stats.Failure = attempt.Detail
			}
		}
	}
	for code, count := range codes {
		stats.Codes = append(stats.Codes, CodeCount{code, count})
	}
	sort.Slice(stats.Codes, func(i, j int) bool {
		if stats.Codes[i].Count != stats.Codes[j].Count {
			return stats.Codes[i].Count > stats.Codes[j].Count
		}
		return stats.Codes[i].Code < stats.Codes[j].Code
	})
	if len(answered) > 0 {
		sort.Float64s(answered)
		stats.Fastest = time.Duration(answered[0] * float64(time.Second))
		stats.Slowest = time.Duration(answered[len(answered)-1] * float64(time.Second))
		stats.Median = time.Duration(medianOf(answered) * float64(time.Second))
	}
	timings := make([]float64, 0, len(answered)+len(timed))
	timings = append(append(timings, answered...), timed...)
	if len(timings) > 0 {
		stats.Typical = medianOf(timings)
		stats.AtLeast = len(timed) > 0 && stats.Typical >= seconds(deadline)
		stats.Worst = timings[0]
		for _, v := range timings {
			stats.Worst = math.Max(stats.Worst, v)
		}
		stats.WorstAtLeast = len(timed) > 0
	}
	return stats
}

// errorsAnswered says whether any answer was not a 2xx or 3xx status.
func (s *PodStats) errorsAnswered() bool {
	for _, code := range s.Codes {
		if code.Code < 200 || code.Code >= 400 {
			return true
		}
	}
	return s.Unanswered > 0
}

// peersOf is the median of every finite value except the one at index.
func peersOf(values []float64, index int) float64 {
	var others []float64
	for i, v := range values {
		if i != index && !math.IsNaN(v) {
			others = append(others, v)
		}
	}
	return medianOf(others)
}

// read fills the per-Pod numbers of a run and returns, per target, the
// median and the slowest request (NaN without a timing), and every answered
// time for the bar scale.
func (s *Summary) read(plan *Plan, run *Run) (typical, worst, every []float64) {
	typical, worst = make([]float64, len(plan.Targets)), make([]float64, len(plan.Targets))
	for i := range plan.Targets {
		typical[i], worst[i] = math.NaN(), math.NaN()
		stats := &s.Pods[i]
		stats.Typical, stats.Worst = math.NaN(), math.NaN()
		switch {
		case plan.Targets[i].Request == nil:
			stats.Verdict = NotContacted
			continue
		case run == nil || i >= len(run.Pods) || run.Pods[i].Attempts == nil:
			stats.Verdict = Unprobed
			continue
		}
		*stats = podStats(run.Pods[i].Attempts, run.Deadline)
		typical[i], worst[i] = stats.Typical, stats.Worst
		for _, attempt := range run.Pods[i].Attempts {
			switch attempt.Outcome {
			case Answered:
				every = append(every, seconds(attempt.Took))
			case Denied:
				s.Denied = true
			}
		}
		switch {
		case stats.Sent == 0:
			stats.Verdict = Unprobed
		case stats.Finished == 0 && stats.Abandoned > 0:
			stats.Verdict = Canceled
		case stats.Finished == 0:
			// The first request is in flight: nothing can be said yet.
			stats.Verdict = Unprobed
		case stats.Answered == 0:
			stats.Verdict = NoAnswer
			// A Pod that only timed out still has a lower-bound timing.
			if !math.IsNaN(stats.Typical) {
				s.Compared++
			}
		default:
			stats.Verdict = NoPeers
			s.Compared++
			if s.FloorPod < 0 || stats.Median < s.Floor {
				s.Floor, s.FloorPod = stats.Median, i
			}
		}
	}
	return typical, worst, every
}

// compare gives each Pod with a timing its word against its peers.
func (s *Summary) compare(typical, worst []float64) {
	medians := chart.Outliers(typical, chart.PeerOptions{Direction: chart.Above, MinDelta: medianFloor})
	spikes := chart.Outliers(worst, chart.PeerOptions{Direction: chart.Above, MinDelta: slowestFloor})
	s.Enough = medians.Enough
	if medians.Enough {
		for i := range s.Pods {
			if s.Pods[i].Verdict == NoPeers {
				s.Pods[i].Verdict = LikePeers
			}
		}
		for _, deviation := range spikes.Outliers {
			if stats := &s.Pods[deviation.Index]; stats.Verdict == LikePeers {
				stats.Verdict, stats.Peers = Spikes, peersOf(worst, deviation.Index)
			}
		}
		for _, deviation := range medians.Outliers {
			if stats := &s.Pods[deviation.Index]; stats.Verdict == LikePeers || stats.Verdict == Spikes {
				stats.Verdict, stats.Peers = Outlier, peersOf(typical, deviation.Index)
			}
		}
	}
	for i := range s.Pods {
		stats := &s.Pods[i]
		if stats.Verdict != LikePeers && stats.Verdict != NoPeers {
			continue
		}
		// A request without an answer is a fact about this Pod whatever its
		// peers did: it is never folded into "like peers" or "answered".
		switch {
		case stats.TimedOut > 0:
			stats.Verdict = Timeouts
		case stats.errorsAnswered():
			stats.Verdict = Errors
		}
	}
}

// scale sets the shared bar scale: a full bar is the slowest answer, but
// never less than five times the usual answer (up to a quarter of a
// second), so ordinary jitter between fast Pods stays low on the strip.
func (s *Summary) scale(every []float64, previous *Run) {
	if previous != nil {
		for i := range previous.Pods {
			for _, attempt := range previous.Pods[i].Attempts {
				if attempt.Outcome == Answered {
					every = append(every, seconds(attempt.Took))
				}
			}
		}
	}
	usual := medianOf(every)
	top := 0.05
	for _, v := range every {
		top = math.Max(top, v)
	}
	if !math.IsNaN(usual) {
		top = math.Max(top, math.Min(5*usual, 0.25))
		s.SlowLine = math.Max(4*usual, usual+medianFloor)
	}
	s.Top = chart.Seconds.Ceil(top)
}

// Summarize reads a run: per-Pod numbers, which Pods differ from their
// peers, the floor of the proxy path and the shared bar scale. previous,
// when given, only widens the scale so both strips share it.
func Summarize(plan *Plan, run, previous *Run) Summary {
	summary := Summary{Pods: make([]PodStats, len(plan.Targets)), FloorPod: -1}
	typical, worst, every := summary.read(plan, run)
	summary.compare(typical, worst)
	summary.scale(every, previous)
	return summary
}

// Seconds writes a duration the way the charts do: 22ms, 3.04s.
func Seconds(v float64) string { return chart.Seconds.Format(v) }

// Typical writes a Pod's median, as a lower bound when timeouts decided it.
func (s *PodStats) TypicalText(ascii bool) string {
	if math.IsNaN(s.Typical) {
		return "-"
	}
	if s.AtLeast {
		return atLeast(ascii) + Seconds(s.Typical)
	}
	return Seconds(s.Typical)
}

// WorstText writes a Pod's slowest request, as a lower bound after a timeout.
func (s *PodStats) WorstText(ascii bool) string {
	if math.IsNaN(s.Worst) {
		return "-"
	}
	if s.WorstAtLeast {
		return atLeast(ascii) + Seconds(s.Worst)
	}
	return Seconds(s.Worst)
}

func atLeast(ascii bool) string {
	if ascii {
		return ">="
	}
	return "≥"
}

// CodesText is "200", "200 503" or "-": the statuses the Pod answered with,
// most frequent first.
func (s *PodStats) CodesText() string {
	if len(s.Codes) == 0 {
		if s.Failure != "" {
			return "none"
		}
		return "-"
	}
	parts := make([]string, 0, len(s.Codes))
	for _, code := range s.Codes {
		parts = append(parts, strconv.Itoa(code.Code))
	}
	return strings.Join(parts, " ")
}

// CodesDetail is "200 ×5" or "200 ×4, 503 ×1".
func (s *PodStats) CodesDetail(ascii bool) string {
	times := "×"
	if ascii {
		times = "x"
	}
	parts := make([]string, 0, len(s.Codes))
	for _, code := range s.Codes {
		parts = append(parts, fmt.Sprintf("%d %s%d", code.Code, times, code.Count))
	}
	return strings.Join(parts, ", ")
}

// Mark is the outlier phrase of a row: "3.1s median vs peers 22ms".
func (s *PodStats) Mark(ascii bool) string {
	switch s.Verdict {
	case Outlier:
		return s.TypicalText(ascii) + " median vs peers " + Seconds(s.Peers)
	case Spikes:
		return "slowest " + s.WorstText(ascii) + " vs peers " + Seconds(s.Peers)
	}
	return ""
}

// ShortName is the part of a Pod name that tells it from its siblings: the
// last dash-separated segment, written "…-hx4mt". A short name stays whole.
func ShortName(name string, ascii bool) string {
	cut := strings.LastIndex(name, "-")
	if cut <= 0 || cut == len(name)-1 || len(name) <= 20 {
		return name
	}
	if ascii {
		return "..." + name[cut:]
	}
	return "…" + name[cut:]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// report builds the sentences of one result.
type report struct {
	plan    *Plan
	run     *Run
	summary *Summary
	ascii   bool
	lines   []string
}

func (r *report) addf(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *report) name(i int) string { return ShortName(r.plan.Targets[i].Pod.Name, r.ascii) }

func (r *report) timeouts(stats *PodStats) string {
	if stats.TimedOut == 0 {
		return ""
	}
	return fmt.Sprintf("; %d of %d had no answer within %s", stats.TimedOut, stats.Finished, Seconds(seconds(r.run.Deadline)))
}

// abandoned says how many of the sent requests were in flight when the run
// ended. They left, so they are counted as sent, and the application may
// still handle them.
func (r *report) abandoned() string {
	n := r.run.Abandoned()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %s still in flight and abandoned without an answer (the application may still handle %s)",
		plural(n, "of them", "of them"), map[bool]string{true: "it", false: "them"}[n == 1])
}

// slow states which Pods answered slower than their peers.
func (r *report) slow(slow []int) {
	switch {
	case len(slow) == 1:
		stats := &r.summary.Pods[slow[0]]
		peers := plural(r.summary.Compared-1, "peer", "peers")
		if stats.Verdict == Outlier {
			r.addf("Only %s answered slowly: median %s against %s for its %s%s.",
				r.name(slow[0]), stats.TypicalText(r.ascii), Seconds(stats.Peers), peers, r.timeouts(stats))
			return
		}
		r.addf("Only %s was slow some of the time: slowest %s against %s for its %s, median %s%s.",
			r.name(slow[0]), stats.WorstText(r.ascii), Seconds(stats.Peers), peers, stats.TypicalText(r.ascii), r.timeouts(stats))
	case len(slow) > 1:
		parts := make([]string, 0, len(slow))
		for _, i := range slow {
			stats := &r.summary.Pods[i]
			parts = append(parts, r.name(i)+" ("+stats.TypicalText(r.ascii)+", slowest "+stats.WorstText(r.ascii)+")")
		}
		r.addf("%d of %d Pods answered slower than their peers: %s.", len(slow), r.summary.Compared, strings.Join(parts, ", "))
	}
}

// failed states which Pods gave no answer or not always a success.
func (r *report) failed(flagged []int) {
	for _, i := range flagged {
		stats := &r.summary.Pods[i]
		switch stats.Verdict {
		case NoAnswer:
			why := stats.Failure
			if stats.TimedOut > 0 {
				why = "no answer within " + Seconds(seconds(r.run.Deadline))
			}
			r.addf("%s gave no answer to %s (%s).", r.name(i), plural(stats.Finished, "request", "requests"), why)
		case Timeouts:
			// Stated on its own terms: a request without an answer needs no
			// peer to be worth reading.
			r.addf("%s gave no answer within %s to %d of %s; its %s took %s.", r.name(i), Seconds(seconds(r.run.Deadline)),
				stats.TimedOut, plural(stats.Finished, "request", "requests"), plural(stats.Answered, "answer", "answers"),
				span(seconds(stats.Fastest), seconds(stats.Slowest)))
		case Errors:
			detail := stats.CodesDetail(r.ascii)
			if stats.Unanswered > 0 {
				detail = strings.TrimPrefix(detail+", "+fmt.Sprintf("%d unanswered (%s)", stats.Unanswered, stats.Failure), ", ")
			}
			r.addf("%s did not always answer with success: %s.", r.name(i), detail)
		}
	}
}

// steady states how the Pods that do not stand out answered.
func (r *report) steady(steady []int, others bool) {
	if len(steady) == 0 {
		return
	}
	requests, low, high, slowest := 0, math.Inf(1), math.Inf(-1), 0.0
	for _, i := range steady {
		stats := &r.summary.Pods[i]
		requests += stats.Answered
		low, high = math.Min(low, seconds(stats.Median)), math.Max(high, seconds(stats.Median))
		slowest = math.Max(slowest, seconds(stats.Slowest))
	}
	switch {
	case others:
		who := "The other " + plural(len(steady), "Pod", "Pods")
		if len(steady) == 1 {
			who = "The other Pod"
		}
		r.addf("%s answered all %s in %s or less.", who, plural(requests, "request", "requests"), Seconds(slowest))
	case r.summary.Compared == 1:
		r.addf("%s answered: median %s, slowest %s.", r.name(steady[0]), span(low, high), Seconds(slowest))
		r.addf("One Pod has no peers here. Probe its Service or workload to see whether it differs from the others.")
	case !r.summary.Enough:
		r.addf("%s answered: median %s, slowest %s. Fewer than %d Pods is too few to call one of them different.",
			plural(len(steady), "Pod", "Pods"), span(low, high), Seconds(slowest), chart.DefaultMinPeers)
	case high >= 2*low && high-low >= medianFloor:
		// The peer rule compares each Pod with the median of the group, so
		// it cannot single one out when about half of them are slow. That
		// is not the same as answering alike, and it is not said to be.
		r.addf("The %d Pods did not answer alike: medians %s, slowest answer %s. No single Pod differs from the rest by the peer rule, "+
			"which cannot name one when several are slow; compare the rows.", len(steady), span(low, high), Seconds(slowest))
	default:
		r.addf("All %d Pods answered alike: medians %s, slowest answer %s. No Pod stands out in this run.", len(steady), span(low, high), Seconds(slowest))
		r.addf("An intermittent stall can fall between %s per Pod: run again, or send more.", plural(r.run.Attempts, "request", "requests"))
	}
}

// Result states what the run showed, in sentences. It reports differences
// between Pods at the time of the run and what the request did not pass
// through; it does not name a cause.
func Result(plan *Plan, run *Run, summary *Summary, ascii bool) []string {
	if run == nil || run.Sent() == 0 {
		return nil
	}
	if summary.Denied {
		answered := 0
		for i := range summary.Pods {
			answered += summary.Pods[i].Answered
		}
		lines := []string{"Not allowed: the API server refused the request. This user is missing " + MissingAccess(plan.Scope.Namespace) + "."}
		if answered == 0 {
			return append(lines, "No request reached a Pod, so nothing is known about how fast any of them answers.")
		}
		// A rule limited to some Pods by name refuses the others.
		return append(lines, fmt.Sprintf("%s answered before the refusal ended the run. The rows show them; the run is incomplete and compares nothing.",
			plural(answered, "request was", "requests were")))
	}
	var flagged, slow, steady []int
	for i := range summary.Pods {
		switch summary.Pods[i].Verdict {
		case Outlier, Spikes:
			slow = append(slow, i)
			flagged = append(flagged, i)
		case NoAnswer, Timeouts, Errors:
			flagged = append(flagged, i)
		case LikePeers, NoPeers:
			steady = append(steady, i)
		}
	}
	r := report{plan: plan, run: run, summary: summary, ascii: ascii}
	r.slow(slow)
	r.failed(flagged)
	r.steady(steady, len(flagged) > 0)
	if len(slow) > 0 && len(steady) > 0 {
		r.addf("The difference is between Pods, now. The request did not pass the ingress or the Service, so neither slowed it.")
	}
	switch run.Stopped {
	case StoppedCanceled:
		r.addf("Canceled: %d of %d requests were sent%s; the rest were not.", run.Sent(), run.Planned(), r.abandoned())
	case StoppedDeadline:
		r.addf("The run reached its %s limit: %d of %d requests were sent%s.", Seconds(RunDeadline.Seconds()), run.Sent(), run.Planned(), r.abandoned())
	}
	return r.lines
}

func span(low, high float64) string {
	if Seconds(low) == Seconds(high) {
		return Seconds(low)
	}
	return Seconds(low) + " to " + Seconds(high)
}
