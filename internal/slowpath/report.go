// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
)

// Series is the samples of one answer series of a signal.
type Series struct {
	// Pod is the Pod the series belongs to when the signal was read per
	// Pod; Group holds the values of the signal's by labels (a drop reason).
	Pod, Group string
	Points     []chart.Point
	// Median is the series' own usual level over the window; Worst is its
	// largest sample (smallest for a signal that is bad when low).
	Median, Worst, Last float64
	WorstAt             time.Time
	// Crossed are the stretches in which the rule held.
	Crossed []chart.Interval
}

// Deviation is one Pod that differs from its peers on a signal.
type Deviation struct {
	Pod string
	// Value is the Pod's worst sample, or its median when Sustained.
	// Peers is the median of the same number over the other Pods.
	Value, Peers float64
	// Sustained is true when the Pod's usual level differs, false when
	// only its worst sample does.
	Sustained bool
}

// SignalRead is one signal of one hop after the batch: the samples, the
// rule, where the rule was crossed, or the reason nothing was read.
type SignalRead struct {
	Query                *Query
	Name, Title, Profile string
	Rule                 Rule
	// Read is false when the signal has no samples; Reason says why.
	Read   bool
	Reason string
	// Note qualifies a reading: counted as zero by absence, or cut by a
	// read limit.
	Note string
	// Text is the exact PromQL that was sent.
	Text    string
	Series  []Series
	Samples int
	// Crossed is the union of the stretches in which any series crossed.
	Crossed []chart.Interval
	// Worst is the worst sample of any series, at WorstAt.
	Worst   float64
	WorstAt time.Time
	// Share is Worst as a share of the total it is a part of (499 of all
	// requests), when the profile reads that total; ShareOf names it.
	Share    float64
	ShareOf  string
	Outliers []Deviation
	// PeerNote says why Pods could not be compared.
	PeerNote string

	worst    int
	answered bool
}

// look says how far back one sample of the signal looks.
func (s *SignalRead) look() Lookback {
	if s.Query == nil {
		return Lookback{}
	}
	return s.Query.Look
}

// Bad reports whether the signal has a direction that counts as bad.
func (s *SignalRead) Bad() bool { return s.Rule.Worse != WorseNone }

// Format writes a value in the signal's unit.
func (s *SignalRead) Format(v float64) string { return s.Rule.Unit.Format(v) }

// Shown is the number that stands for the signal in a row: its worst
// sample when it has a rule, its usual level when it is only context.
func (s *SignalRead) Shown() string {
	if !s.Read {
		return "not read"
	}
	if !s.Bad() {
		medians := make([]chart.Point, 0, len(s.Series))
		for i := range s.Series {
			medians = append(medians, chart.Point{V: s.Series[i].Median})
		}
		if s.Query != nil && s.Query.PerPod {
			// A per-Pod context signal is summed, as the whole hop sees it.
			total := 0.0
			for _, m := range medians {
				total += m.V
			}
			return s.Format(total)
		}
		return s.Format(Median(medians))
	}
	return s.Format(s.Worst)
}

// WorstSeries is the series that holds the worst sample, or nil.
func (s *SignalRead) WorstSeries() *Series {
	if s.worst < 0 || s.worst >= len(s.Series) {
		return nil
	}
	return &s.Series[s.worst]
}

// seriesOf returns the Pod's series of the signal: the worst one when the
// signal keeps several apart.
func (s *SignalRead) seriesOf(pod string) *Series {
	var best *Series
	for i := range s.Series {
		series := &s.Series[i]
		if series.Pod != pod {
			continue
		}
		if best == nil || (s.Rule.Worse == WorseLow && series.Worst < best.Worst) || (s.Rule.Worse != WorseLow && series.Worst > best.Worst) {
			best = series
		}
	}
	return best
}

// Envelope folds the series of a signal into one: the worst value at each
// sample time. It is what a hop's sparkline and cursor read.
func (s *SignalRead) Envelope() []chart.Point {
	if len(s.Series) == 1 {
		return s.Series[0].Points
	}
	at := map[int64]float64{}
	for i := range s.Series {
		for _, point := range s.Series[i].Points {
			key := point.T.UnixNano()
			current, seen := at[key]
			if !seen || (s.Rule.Worse == WorseLow && point.V < current) || (s.Rule.Worse != WorseLow && point.V > current) {
				at[key] = point.V
			}
		}
	}
	out := make([]chart.Point, 0, len(at))
	for key, value := range at {
		out = append(out, chart.Point{T: time.Unix(0, key).UTC(), V: value})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].T.Before(out[b].T) })
	return out
}

func (s *SignalRead) summarize(step time.Duration) {
	s.Samples, s.Crossed, s.worst = 0, nil, -1
	var crossed []chart.Interval
	for i := range s.Series {
		series := &s.Series[i]
		series.Median = Median(series.Points)
		series.Crossed = Crossings(series.Points, s.Rule, step, s.look())
		crossed = append(crossed, series.Crossed...)
		for index, point := range series.Points {
			if index == 0 || (s.Rule.Worse == WorseLow && point.V < series.Worst) || (s.Rule.Worse != WorseLow && point.V > series.Worst) {
				series.Worst, series.WorstAt = point.V, point.T
			}
		}
		if n := len(series.Points); n > 0 {
			series.Last = series.Points[n-1].V
		}
		s.Samples += len(series.Points)
		if s.worst < 0 || (s.Rule.Worse == WorseLow && series.Worst < s.Worst) || (s.Rule.Worse != WorseLow && series.Worst > s.Worst) {
			s.worst, s.Worst, s.WorstAt = i, series.Worst, series.WorstAt
		}
	}
	s.Crossed = Normalize(crossed)
}

func readSignal(q *Query, res *observability.PromResult, step time.Duration) SignalRead {
	sig := SignalRead{Query: q, Name: q.Signal.Name, Title: q.Signal.Title, Profile: q.Profile, Rule: RuleFor(&q.Signal), Text: res.Query, worst: -1}
	if sig.Text == "" {
		sig.Text = q.Sent.String()
	}
	if missing, yes := q.Missing(res); yes {
		sig.Reason, sig.answered = missing.Reason, res.OK()
		return sig
	}
	sig.Read = true
	for i := range res.Series {
		in := &res.Series[i]
		if len(in.Samples) == 0 {
			continue
		}
		series := Series{Points: make([]chart.Point, 0, len(in.Samples))}
		if q.PerPod {
			series.Pod = in.Labels[q.PodLabel]
		}
		var groups []string
		for _, label := range q.Signal.By {
			if value := in.Labels[label]; value != "" {
				groups = append(groups, value)
			}
		}
		series.Group = strings.Join(groups, "/")
		for _, sample := range in.Samples {
			series.Points = append(series.Points, chart.Point{T: sample.Timestamp, V: sample.Value})
		}
		sig.Series = append(sig.Series, series)
	}
	sort.SliceStable(sig.Series, func(a, b int) bool {
		if sig.Series[a].Pod != sig.Series[b].Pod {
			return sig.Series[a].Pod < sig.Series[b].Pod
		}
		return sig.Series[a].Group < sig.Series[b].Group
	})
	sig.summarize(step)
	var notes []string
	if res.State == observability.StateIncomplete {
		notes = append(notes, "the answer was cut by a read limit: "+res.Reason)
	}
	if q.PodsOmitted > 0 {
		notes = append(notes, fmt.Sprintf("%d Pods were left out of the query to keep it bounded", q.PodsOmitted))
	}
	sig.Note = strings.Join(notes, "; ")
	return sig
}

// PodRow is one backend Pod of the app hop.
type PodRow struct {
	Pod      Pod
	State    State
	Key      string
	Evidence string
	// Trend is the Pod's series of the hop's main signal.
	Trend []chart.Point
}

// Hop is one row of the path after the batch.
type Hop struct {
	Spec  HopSpec
	State State
	// Reason says why the hop was not read.
	Reason string
	// Key is the number that justifies the state; Evidence adds to it,
	// and Briefs are shorter forms of it for a narrow column, longest
	// first.
	Key, Evidence string
	Briefs        []string
	Signals       []SignalRead
	// Main is the signal the sparkline and Key show, or -1.
	Main int
	// Trend is the main signal over the window, worst series at each time.
	Trend     []chart.Point
	TrendUnit chart.Unit
	Pods      []PodRow
}

// Signal returns a signal of the hop by name, or nil.
func (h *Hop) Signal(name string) *SignalRead {
	for i := range h.Signals {
		if h.Signals[i].Name == name {
			return &h.Signals[i]
		}
	}
	return nil
}

// Alerts are the alerts of the namespace, read beside the batch when an
// Alertmanager is declared.
type Alerts struct {
	// Read is false when no Alertmanager was asked; Note says why, or how
	// it was reached.
	Read   bool
	Note   string
	Firing []AlertSpan
}

// AlertSpan is one alert and the time it has been firing.
type AlertSpan struct {
	Name       string
	Start, End time.Time
}

// Report is everything one read of the path produced.
type Report struct {
	Plan *Plan
	Path *Path
	// Source describes the Prometheus source; ReadAt is when the batch
	// finished.
	Source     string
	ReadAt     time.Time
	Start, End time.Time
	Step       time.Duration
	// Asked and Answered count range reads; Failure is set when none was
	// answered, with the source's reason.
	Asked, Answered int
	Failure         string
	Hops            []Hop
	Lanes           []Lane
	// Symptom is the lane whose marks are the episodes, or -1.
	Symptom  int
	Episodes []chart.Interval
	// Headline states the episodes, HeadlineShort without their times (the
	// lanes show them); Summary states what overlaps them.
	Headline      string
	HeadlineShort string
	Summary       []Sentence
	Alerts        *Alerts
}

// Hop returns a hop by identifier, or nil.
func (r *Report) Hop(id string) *Hop {
	for i := range r.Hops {
		if r.Hops[i].Spec.ID == id {
			return &r.Hops[i]
		}
	}
	return nil
}

// Clock writes a time of day in UTC, with seconds for a short window.
func (r *Report) Clock(t time.Time) string {
	if r.End.Sub(r.Start) <= 15*time.Minute {
		return t.UTC().Format("15:04:05")
	}
	return t.UTC().Format("15:04")
}

// Span names the window: "1h".
func (r *Report) Span() string { return impact.Dur(r.End.Sub(r.Start)) }

// ShortPod shortens a Pod name to its last dash-separated word when that
// tells it apart from the other Pods of the path: …-hx4mt.
func (r *Report) ShortPod(name string) string {
	return shortPod(name, r.Path.PodNames())
}

func shortPod(name string, all []string) string {
	cut := strings.LastIndex(name, "-")
	if cut <= 0 {
		return name
	}
	tail := name[cut:]
	for _, other := range all {
		if other != name && strings.HasSuffix(other, tail) {
			return name
		}
	}
	return "…" + tail
}

// Evaluate turns the answers of a batch into the report: a state, a key
// number and evidence per hop, the lanes, the episodes and the summary. It
// is a pure function of its arguments.
func Evaluate(plan *Plan, results []observability.PromResult, alerts *Alerts, readAt time.Time) *Report {
	report := &Report{Plan: plan, Path: plan.Path, ReadAt: readAt.UTC(), Start: plan.Start, End: plan.End, Step: plan.Step,
		Asked: len(plan.Queries), Symptom: -1, Alerts: alerts}
	report.Hops = make([]Hop, len(plan.Hops))
	for i := range plan.Hops {
		report.Hops[i] = Hop{Spec: plan.Hops[i], Main: -1}
	}
	failure, failed := "", 0
	for i := range plan.Queries {
		query := &plan.Queries[i]
		var result observability.PromResult
		if i < len(results) {
			result = results[i]
		}
		if result.OK() {
			report.Answered++
		} else {
			failed++
			if failure == "" {
				failure = string(result.State)
				if result.Reason != "" {
					failure += ": " + result.Reason
				}
				if result.State == "" {
					failure = "not asked"
				}
			}
		}
		hop := &report.Hops[query.HopIndex]
		hop.Signals = append(hop.Signals, readSignal(query, &result, plan.Step))
	}
	if failed == len(plan.Queries) && failed > 0 {
		report.Failure = failure
	}
	for i := range report.Hops {
		hop := &report.Hops[i]
		hop.zeroByAbsence(plan.Step)
		hop.shares()
		report.evaluate(hop)
	}
	report.buildLanes()
	report.summarize()
	return report
}

// zeroByAbsence reads a signal as zero when the source answered with no
// series for its extra matcher while the same metric, without the matcher,
// was read in the same profile: a counter has no status="499" series until
// the first 499. It is stated on the signal.
func (h *Hop) zeroByAbsence(step time.Duration) {
	for i := range h.Signals {
		sig := &h.Signals[i]
		if sig.Read || !sig.answered || len(sig.Query.Signal.Match) == 0 || sig.Query.Signal.Divisor != "" {
			continue
		}
		for j := range h.Signals {
			total := &h.Signals[j]
			if j == i || !total.Read || total.Profile != sig.Profile || total.Query.Signal.Metric != sig.Query.Signal.Metric ||
				len(total.Query.Signal.Match) > 0 || total.Query.PerPod != sig.Query.PerPod {
				continue
			}
			for k := range total.Series {
				series := Series{Pod: total.Series[k].Pod, Points: make([]chart.Point, 0, len(total.Series[k].Points))}
				for _, point := range total.Series[k].Points {
					series.Points = append(series.Points, chart.Point{T: point.T})
				}
				sig.Series = append(sig.Series, series)
			}
			sig.Read, sig.Reason = true, ""
			sig.Note = "no series matched " + strings.Join(sig.Query.Signal.Match, ",") + "; read as 0 because " +
				sig.Query.Signal.Metric + " itself was read"
			sig.summarize(step)
			break
		}
	}
}

// shares states a raised rate as a share of the total it is part of.
func (h *Hop) shares() {
	for i := range h.Signals {
		sig := &h.Signals[i]
		if !sig.Read || !sig.Bad() || sig.Query.Signal.Kind != observability.SignalRate || len(sig.Query.Signal.Match) == 0 || sig.Worst <= 0 {
			continue
		}
		for j := range h.Signals {
			total := &h.Signals[j]
			if j == i || !total.Read || total.Profile != sig.Profile || total.Query.Signal.Kind != observability.SignalRate ||
				total.Query.Signal.Metric != sig.Query.Signal.Metric || len(total.Query.Signal.Match) > 0 {
				continue
			}
			worst := sig.WorstSeries()
			if worst == nil {
				break
			}
			sum := 0.0
			for k := range total.Series {
				if total.Query.PerPod && total.Series[k].Pod != worst.Pod {
					continue
				}
				for _, point := range total.Series[k].Points {
					if point.T.Equal(sig.WorstAt) {
						sum += point.V
					}
				}
			}
			if sum > 0 && sig.Worst <= sum {
				sig.Share, sig.ShareOf = sig.Worst/sum, total.Name
			}
			break
		}
	}
}

// peers compares the Pods of the app hop on every signal that has a bad
// direction. A Pod differs when its worst sample deviates from its peers'
// worst samples and that sample crossed the signal's own rule, or when its
// usual level deviates from its peers' usual levels.
func (r *Report) peers(h *Hop) {
	pods := r.Path.PodNames()
	for i := range h.Signals {
		sig := &h.Signals[i]
		if !sig.Read || !sig.Bad() || sig.Rule.Counter || !sig.Query.PerPod {
			continue
		}
		worst, usual := make([]float64, len(pods)), make([]float64, len(pods))
		crossed := make([]bool, len(pods))
		for p, pod := range pods {
			worst[p], usual[p] = math.NaN(), math.NaN()
			if series := sig.seriesOf(pod); series != nil {
				worst[p], usual[p], crossed[p] = series.Worst, series.Median, len(series.Crossed) > 0
			}
		}
		direction := chart.Above
		if sig.Rule.Worse == WorseLow {
			direction = chart.Below
		}
		options := chart.PeerOptions{Direction: direction, MinDelta: sig.Rule.Floor}
		byWorst := chart.Outliers(worst, options)
		if !byWorst.Enough {
			sig.PeerNote = fmt.Sprintf("%s (%d with samples)", byWorst.Reason, byWorst.Peers)
			continue
		}
		seen := map[int]bool{}
		for _, deviation := range byWorst.Outliers {
			if crossed[deviation.Index] {
				seen[deviation.Index] = true
				sig.Outliers = append(sig.Outliers, Deviation{Pod: pods[deviation.Index], Value: deviation.Value, Peers: peerMedian(worst, deviation.Index)})
			}
		}
		for _, deviation := range chart.Outliers(usual, options).Outliers {
			if !seen[deviation.Index] {
				sig.Outliers = append(sig.Outliers, Deviation{Pod: pods[deviation.Index], Value: deviation.Value, Peers: peerMedian(usual, deviation.Index),
					Sustained: true})
			}
		}
	}
}

// peerMedian is the median of the finite values except the one at skip.
func peerMedian(values []float64, skip int) float64 {
	rest := make([]chart.Point, 0, len(values))
	for i, v := range values {
		if i != skip && !math.IsNaN(v) {
			rest = append(rest, chart.Point{V: v})
		}
	}
	return Median(rest)
}

func (h *Hop) pickMain() {
	h.Main = -1
	rank := func(sig *SignalRead) int {
		switch {
		case !sig.Read:
			return 0
		case len(sig.Outliers) > 0:
			return 6
		case len(sig.Crossed) > 0:
			return 5
		case sig.Bad() && sig.Rule.Unit == chart.Seconds:
			return 4
		case sig.Bad() && sig.Rule.Worse == WorseHigh && !sig.Rule.Counter && sig.Name != SignalMemory:
			return 3
		case sig.Bad():
			return 2
		}
		return 1
	}
	best := 0
	for i := range h.Signals {
		if score := rank(&h.Signals[i]); score > best {
			best, h.Main = score, i
		}
	}
	if h.Main >= 0 {
		h.Trend, h.TrendUnit = h.Signals[h.Main].Envelope(), h.Signals[h.Main].Rule.Unit
	}
}

func (r *Report) evaluate(h *Hop) {
	if h.Spec.ID == HopService {
		r.evaluateService(h)
		return
	}
	read := 0
	for i := range h.Signals {
		if h.Signals[i].Read {
			read++
		}
	}
	if read == 0 {
		h.State = NotRead
		switch {
		case len(h.Spec.NotRead) > 0:
			h.Reason = h.Spec.NotRead[0].Reason
		case len(h.Signals) > 0:
			h.Reason = h.Signals[0].Reason
		default:
			h.Reason = "no signal is mapped for this hop"
		}
		h.Evidence = h.Reason
		if h.Spec.ID == HopApp {
			r.podRows(h)
		}
		return
	}
	if h.Spec.ID == HopApp {
		r.peers(h)
	}
	h.State = Quiet
	for i := range h.Signals {
		sig := &h.Signals[i]
		switch {
		case len(sig.Outliers) > 0:
			h.State = Outlier
		case len(sig.Crossed) > 0 && h.State != Outlier:
			h.State = Elevated
		}
	}
	h.pickMain()
	main := &h.Signals[h.Main]
	h.Key = main.Name + " " + main.Shown()
	if h.Spec.ID == HopApp {
		r.podRows(h)
		h.Evidence = r.appEvidence(h)
		return
	}
	var parts []string
	if main.Share > 0 {
		parts = append(parts, chart.Ratio.Format(main.Share)+" of "+main.ShareOf)
	}
	if where := r.where(h, main); where != "" && len(main.Crossed) > 0 {
		parts = append(parts, where)
	}
	parts = append(parts, h.otherSignals("")...)
	h.Evidence = strings.Join(parts, " · ")
}

// where names the series that holds the worst sample of a signal: the drop
// reason, and the Pod when the signal was read per Pod.
func (r *Report) where(h *Hop, sig *SignalRead) string {
	series := sig.WorstSeries()
	if series == nil {
		return ""
	}
	text := series.Group
	if series.Pod != "" {
		join := "on"
		if h.Spec.ID == HopNetwork {
			join = "to"
		}
		text = strings.TrimSpace(text + " " + join + " " + r.ShortPod(series.Pod))
	}
	return text
}

// otherSignals lists every signal but the main one as "name value": the
// crossed ones first, then the ones with a rule, then context. For a Pod
// it reads that Pod's series.
func (h *Hop) otherSignals(pod string) []string {
	var crossed, ruled, context []string
	unread := 0
	for i := range h.Signals {
		sig := &h.Signals[i]
		if i == h.Main {
			continue
		}
		if !sig.Read {
			unread++
			continue
		}
		text, raised := sig.Name+" "+sig.Shown(), len(sig.Crossed) > 0
		if pod != "" {
			series := sig.seriesOf(pod)
			if series == nil {
				continue
			}
			value := series.Worst
			if !sig.Bad() {
				value = series.Median
			}
			text, raised = sig.Name+" "+sig.Format(value), len(series.Crossed) > 0
		}
		switch {
		case raised:
			crossed = append(crossed, text)
		case sig.Bad():
			ruled = append(ruled, text)
		default:
			context = append(context, text)
		}
	}
	out := append(append(crossed, ruled...), context...)
	if unread > 0 && pod == "" {
		out = append(out, fmt.Sprintf("%d of %d signals not read", unread, len(h.Signals)))
	}
	return out
}

// appEvidence states whether one Pod differs: "1 of 3 pods: …-hx4mt p99
// 48s vs peers 350ms".
func (r *Report) appEvidence(h *Hop) string {
	main := &h.Signals[h.Main]
	total := len(r.Path.Pods)
	if len(main.Outliers) > 0 {
		var named []string
		for _, deviation := range main.Outliers {
			text := r.ShortPod(deviation.Pod) + " " + main.Name + " " + main.Format(deviation.Value)
			if deviation.Sustained {
				text += " usually"
			}
			named = append(named, text)
		}
		text := fmt.Sprintf("%d of %d pods: %s vs peers %s", len(main.Outliers), total, strings.Join(named, ", "), main.Format(main.Outliers[0].Peers))
		// The key column already holds the Pod's own number.
		pod, peers := r.ShortPod(main.Outliers[0].Pod), main.Format(main.Outliers[0].Peers)
		h.Briefs = []string{
			fmt.Sprintf("%d of %d pods: %s vs peers %s", len(main.Outliers), total, pod, peers),
			fmt.Sprintf("%d of %d: %s vs %s", len(main.Outliers), total, pod, peers),
		}
		var also []string
		for i := range h.Signals {
			if i != h.Main && len(h.Signals[i].Outliers) > 0 {
				also = append(also, h.Signals[i].Name)
			}
		}
		if len(also) > 0 {
			text += " · also " + strings.Join(also, ", ")
		}
		return text
	}
	if len(main.Crossed) > 0 {
		var raised []string
		for i := range main.Series {
			if len(main.Series[i].Crossed) > 0 && main.Series[i].Pod != "" {
				raised = append(raised, r.ShortPod(main.Series[i].Pod))
			}
		}
		text := fmt.Sprintf("%d of %d pods raised", len(raised), total)
		if len(raised) > 0 && len(raised) <= 3 {
			text += ": " + strings.Join(raised, ", ")
		}
		if main.PeerNote != "" {
			text += " · " + main.PeerNote
		} else {
			text += " · none differs from its peers"
		}
		return text
	}
	parts := []string{fmt.Sprintf("%d pods, none differs", total)}
	if main.PeerNote != "" {
		parts[0] = fmt.Sprintf("%d pods, %s", total, main.PeerNote)
	}
	return strings.Join(append(parts, h.otherSignals("")...), " · ")
}

// podRows states each backend Pod of the app hop on its own row.
func (r *Report) podRows(h *Hop) {
	h.Pods = nil
	for p := range r.Path.Pods {
		pod := r.Path.Pods[p]
		row := PodRow{Pod: pod, State: NotRead, Key: "not read"}
		read := false
		for i := range h.Signals {
			sig := &h.Signals[i]
			series := sig.seriesOf(pod.Name)
			if series == nil {
				continue
			}
			if !read {
				read, row.State = true, Quiet
			}
			if len(series.Crossed) > 0 && row.State != Outlier {
				row.State = Elevated
			}
			for _, deviation := range sig.Outliers {
				if deviation.Pod == pod.Name {
					row.State = Outlier
				}
			}
		}
		var parts []string
		if h.Main >= 0 {
			main := &h.Signals[h.Main]
			if series := main.seriesOf(pod.Name); series != nil {
				value := series.Worst
				if !main.Bad() {
					value = series.Median
				}
				row.Key, row.Trend = main.Name+" "+main.Format(value), series.Points
			} else if read {
				row.Key = main.Name + " not read"
			}
			parts = h.otherSignals(pod.Name)
		}
		if !read {
			parts = append(parts, "no series for this Pod")
		}
		if pod.Node != "" {
			parts = append(parts, "node "+pod.Node)
		}
		row.Evidence = strings.Join(parts, " · ")
		h.Pods = append(h.Pods, row)
	}
}

// evaluateService states the endpoints: what the Kubernetes API reports
// now, which Pods changed Ready inside the window, and, when
// kube-state-metrics is read, when a Pod was not Ready.
func (r *Report) evaluateService(h *Hop) {
	path := r.Path
	ready := h.Signal(SignalReady)
	history := ready != nil && ready.Read
	if !path.EndpointsRead && !history {
		h.State = NotRead
		h.Reason = "the EndpointSlices of the Service were not read"
		if ready != nil && ready.Reason != "" {
			h.Reason += "; " + ready.Reason
		}
		h.Evidence = h.Reason
		return
	}
	h.State = Quiet
	now := fmt.Sprintf("%d of %d ready", path.EndpointsReady, path.Endpoints)
	h.Key = now
	var parts []string
	if path.EndpointsRead && path.Endpoints == 0 {
		h.State = Elevated
		parts = append(parts, "no EndpointSlice of the Service lists an endpoint")
	}
	if path.EndpointsRead && path.EndpointsReady < path.Endpoints {
		h.State = Elevated
		var down []string
		for i := range path.Pods {
			if path.Pods[i].Endpoint != "" && path.Pods[i].Endpoint != "ready" {
				down = append(down, r.ShortPod(path.Pods[i].Name))
			}
		}
		if len(down) > 0 {
			parts = append(parts, "not ready now: "+strings.Join(down, ", "))
		}
	}
	if history {
		parts = append(parts, r.readyHistory(h, ready, now)...)
	}
	// What the API still knows: the last change of each Pod's Ready
	// condition, and Pods created inside the window.
	for i := range path.Pods {
		pod := &path.Pods[i]
		switch {
		case !pod.Created.IsZero() && pod.Created.After(r.Start):
			h.State = Elevated
			parts = append(parts, r.ShortPod(pod.Name)+" created "+r.Clock(pod.Created))
		case !pod.ReadySince.IsZero() && pod.ReadySince.After(r.Start):
			h.State = Elevated
			parts = append(parts, fmt.Sprintf("%s Ready=%s since %s", r.ShortPod(pod.Name), pod.Ready, r.Clock(pod.ReadySince)))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "no Pod changed Ready in the window")
	}
	if !history {
		why := "kube_pod_status_ready not read"
		if ready == nil {
			why = "no Pod is known"
		}
		parts = append(parts, "API only: "+why)
	}
	h.Evidence = strings.Join(parts, " · ")
}

// readyHistory reads kube_pod_status_ready: the hop's trend is the number
// of Pods not Ready at each sample time, and its key number the fewest that
// were Ready at once.
func (r *Report) readyHistory(h *Hop, ready *SignalRead, now string) []string {
	h.Main = 0
	for i := range h.Signals {
		if h.Signals[i].Name == SignalReady {
			h.Main = i
		}
	}
	down := map[int64]float64{}
	for i := range ready.Series {
		for _, point := range ready.Series[i].Points {
			key := point.T.UnixNano()
			if _, seen := down[key]; !seen {
				down[key] = 0
			}
			if point.V < 1 {
				down[key]++
			}
		}
	}
	most := 0.0
	for key, value := range down {
		h.Trend = append(h.Trend, chart.Point{T: time.Unix(0, key).UTC(), V: value})
		most = math.Max(most, value)
	}
	sort.Slice(h.Trend, func(a, b int) bool { return h.Trend[a].T.Before(h.Trend[b].T) })
	h.TrendUnit = chart.Count
	if most == 0 {
		return nil
	}
	h.State = Elevated
	total := len(ready.Series)
	h.Key = fmt.Sprintf("%d of %d ready", total-int(most), total)
	var parts []string
	for i := range ready.Series {
		series := &ready.Series[i]
		if len(series.Crossed) == 0 {
			continue
		}
		longest := time.Duration(0)
		for _, iv := range series.Crossed {
			longest = max(longest, iv.End.Sub(iv.Start))
		}
		parts = append(parts, fmt.Sprintf("%s not Ready %s, %s longest", r.ShortPod(series.Pod), times(len(series.Crossed)), impact.Dur(longest)))
	}
	return append(parts, "now "+now)
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}
