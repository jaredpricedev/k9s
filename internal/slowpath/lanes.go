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

// Lane is one row of the "when it happened" strip: the stretches in which
// something was the case. It states when, never why.
type Lane struct {
	ID string
	// Hop is the hop the lane belongs to; empty for a lane of dated facts
	// (restarts, rollouts, alerts).
	Hop string
	// Label names the lane on the strip; Short names it in a sentence.
	// Names are the raised signals the label lists, for a view that has to
	// shorten it.
	Label, Short string
	Names        []string
	// Phrase says what a mark means ("CPU throttling"); Where says where
	// ("on pod …-hx4mt", "at the ingress").
	Phrase, Where string
	// Pod is the one Pod every mark belongs to, or empty.
	Pod string
	// Marks are the stretches in which a rule was crossed, or the moments
	// of dated facts. Unknown are the stretches without data.
	Marks, Unknown []chart.Interval
	// NotRead says why the whole lane has no data.
	NotRead string
	// Values and Unit are what the cursor reads off the lane; Low is true
	// when the smallest value is the worst one (free threads).
	Values []chart.Point
	Unit   chart.Unit
	Low    bool
	// Events are the dated facts behind the marks of a fact lane.
	Events []Event
	// Signal is true when the marks are rule crossings of a signal.
	Signal bool
	// Lead is one step for a lane whose stretches come from samples: a
	// stretch begins one step before the first sample that was raised, and
	// that sample's time is what a sentence states. It is zero for dated
	// facts.
	Lead time.Duration
}

// ValueAt is the worst value the lane has between two times, for the
// cursor readout. ok is false when the lane has no sample there.
func (l *Lane) ValueAt(start, end time.Time) (value float64, ok bool) {
	for _, point := range l.Values {
		if point.T.Before(start) || !point.T.Before(end) {
			continue
		}
		if !ok || (l.Low && point.V < value) || (!l.Low && point.V > value) {
			value, ok = point.V, true
		}
	}
	return value, ok
}

// EventsAt lists the dated facts between two times.
func (l *Lane) EventsAt(start, end time.Time) []string {
	var out []string
	for _, event := range l.Events {
		if !event.At.Before(start) && event.At.Before(end) {
			out = append(out, event.Label)
		}
	}
	return out
}

func (r *Report) window() chart.Interval { return chart.Interval{Start: r.Start, End: r.End} }

// coverage returns the stretches of the window in which none of the given
// signals has a sample.
func (r *Report) unknown(signals ...*SignalRead) []chart.Interval {
	var covered []chart.Interval
	for _, sig := range signals {
		if sig == nil || !sig.Read {
			continue
		}
		for i := range sig.Series {
			covered = append(covered, Covered(sig.Series[i].Points, r.Step)...)
		}
	}
	return Complement(covered, r.Start, r.End)
}

// onePod returns the Pod all crossing series of the signals belong to, or
// "" when there is none or more than one.
func onePod(signals ...*SignalRead) string {
	pod, many := "", false
	for _, sig := range signals {
		for i := range sig.Series {
			series := &sig.Series[i]
			if len(series.Crossed) == 0 || series.Pod == "" {
				continue
			}
			if pod != "" && pod != series.Pod {
				many = true
			}
			pod = series.Pod
		}
	}
	if many {
		return ""
	}
	return pod
}

func hopWhere(spec *HopSpec) string {
	switch {
	case spec.ID == HopIngress:
		return "at the ingress"
	case spec.ID == HopService:
		return "on the endpoints"
	case spec.ID == HopApp:
		return "on the pods"
	case spec.ID == HopDNS:
		return "in DNS"
	case spec.ID == HopNetwork:
		return "on the network path"
	}
	return "on " + spec.Short
}

// hopLane is the one lane of a hop that is not split by signal.
func (r *Report) hopLane(h *Hop) Lane {
	lane := Lane{ID: h.Spec.ID, Hop: h.Spec.ID, Label: h.Spec.Short, Short: h.Spec.Short, Where: hopWhere(&h.Spec), Signal: true, Lead: r.Step}
	if h.State == NotRead {
		lane.NotRead, lane.Unknown = h.Reason, []chart.Interval{r.window()}
		return lane
	}
	var (
		names   []string
		crossed []*SignalRead
		ruled   []*SignalRead
	)
	for i := range h.Signals {
		sig := &h.Signals[i]
		if !sig.Read || !sig.Bad() {
			continue
		}
		ruled = append(ruled, sig)
		if len(sig.Crossed) > 0 {
			crossed = append(crossed, sig)
			names = append(names, sig.Name)
			lane.Marks = append(lane.Marks, sig.Crossed...)
		}
	}
	lane.Marks, lane.Unknown = Normalize(lane.Marks), r.unknown(ruled...)
	if h.Main >= 0 {
		lane.Values, lane.Unit, lane.Low = h.Trend, h.TrendUnit, h.Signals[h.Main].Rule.Worse == WorseLow
	}
	if len(crossed) == 0 {
		return lane
	}
	lane.Phrase = Phrase(crossed[0].Name)
	if series := crossed[0].WorstSeries(); series != nil && series.Group != "" {
		lane.Phrase += " (" + series.Group + ")"
	}
	lane.Names = append([]string(nil), names...)
	if len(names) > 2 {
		names = append(names[:2], fmt.Sprintf("+%d", len(names)-2))
	}
	lane.Label = h.Spec.Short + ": " + strings.Join(names, ", ")
	if lane.Pod = onePod(crossed...); lane.Pod != "" {
		lane.Label += " " + r.ShortPod(lane.Pod)
		lane.Where = "on pod " + r.ShortPod(lane.Pod)
	}
	return lane
}

// maxAppLanes bounds the lanes the app hop is split into.
const maxAppLanes = 4

// appLanes gives every crossed signal of the app hop its own lane: slow
// answers, a queue and CPU throttling say different things.
func (r *Report) appLanes(h *Hop) []Lane {
	var crossed []*SignalRead
	for i := range h.Signals {
		sig := &h.Signals[i]
		if sig.Read && sig.Bad() && !sig.Rule.Counter && len(sig.Crossed) > 0 {
			crossed = append(crossed, sig)
		}
	}
	if h.State == NotRead || len(crossed) == 0 {
		return []Lane{r.hopLane(h)}
	}
	// The signals with an outlying Pod come first: they are the finding.
	sort.SliceStable(crossed, func(a, b int) bool { return len(crossed[a].Outliers) > 0 && len(crossed[b].Outliers) == 0 })
	var lanes []Lane
	for index, sig := range crossed {
		if index >= maxAppLanes {
			last := &lanes[len(lanes)-1]
			last.Label += fmt.Sprintf(" (+%d signals)", len(crossed)-maxAppLanes)
			break
		}
		lane := Lane{ID: h.Spec.ID + ":" + sig.Name, Hop: h.Spec.ID, Label: h.Spec.Short + ": " + sig.Name, Short: h.Spec.Short, Names: []string{sig.Name},
			Phrase: Phrase(sig.Name), Where: hopWhere(&h.Spec), Signal: true, Lead: r.Step, Marks: sig.Crossed, Unknown: r.unknown(sig),
			Values: sig.Envelope(), Unit: sig.Rule.Unit, Low: sig.Rule.Worse == WorseLow}
		if lane.Pod = onePod(sig); lane.Pod != "" {
			lane.Label += " " + r.ShortPod(lane.Pod)
			lane.Where = "on pod " + r.ShortPod(lane.Pod)
		}
		lanes = append(lanes, lane)
	}
	return lanes
}

// serviceLane marks when an endpoint was not ready. kube-state-metrics
// gives the history; without it only the last change the API still knows
// is marked, and the time before it is unknown.
func (r *Report) serviceLane(h *Hop) Lane {
	lane := Lane{ID: HopService, Hop: HopService, Label: h.Spec.Short, Short: h.Spec.Short, Phrase: Phrase(SignalReady),
		Where: hopWhere(&h.Spec), Signal: true, Lead: r.Step}
	if h.State == NotRead {
		lane.NotRead, lane.Unknown = h.Reason, []chart.Interval{r.window()}
		return lane
	}
	if ready := h.Signal(SignalReady); ready != nil && ready.Read {
		lane.Marks, lane.Unknown = ready.Crossed, r.unknown(ready)
		lane.Values, lane.Unit = h.Trend, chart.Count
		if len(lane.Marks) > 0 {
			lane.Label += ": not ready"
			lane.Names = []string{"not ready"}
			if lane.Pod = onePod(ready); lane.Pod != "" {
				lane.Label += " " + r.ShortPod(lane.Pod)
				lane.Where = "on pod " + r.ShortPod(lane.Pod)
			}
		}
		return lane
	}
	// What the API still knows is a dated fact, not a rule crossing: it
	// cannot be the symptom the episodes are counted from.
	lane.Phrase, lane.Signal, lane.Lead = "a Ready change", false, 0
	for i := range r.Path.Pods {
		pod := &r.Path.Pods[i]
		at := pod.ReadySince
		if !pod.Created.IsZero() && pod.Created.After(at) {
			at = pod.Created
		}
		if at.IsZero() || !at.After(r.Start) {
			continue
		}
		lane.Marks = append(lane.Marks, chart.Interval{Start: at, End: at})
		lane.Unknown = append(lane.Unknown, chart.Interval{Start: r.Start, End: at})
		lane.Events = append(lane.Events, Event{At: at, Label: r.ShortPod(pod.Name) + " Ready=" + pod.Ready})
	}
	lane.Marks, lane.Unknown = Normalize(lane.Marks), Normalize(lane.Unknown)
	if len(lane.Marks) > 0 {
		lane.Label += ": Ready changed"
		lane.Names = []string{"Ready changed"}
	}
	return lane
}

// buildLanes lays the lanes out in path order, then the dated facts.
func (r *Report) buildLanes() {
	r.Lanes = nil
	var quiet, unread []*Hop
	flush := func() {
		merge := func(hops []*Hop, notRead bool) {
			if len(hops) == 0 {
				return
			}
			if len(hops) == 1 {
				r.Lanes = append(r.Lanes, r.hopLane(hops[0]))
				return
			}
			names := make([]string, 0, len(hops))
			lane := Lane{ID: HopDependencies, Hop: HopDependencies, Short: "dependencies", Where: "on a dependency", Signal: true}
			if notRead {
				lane.ID, lane.NotRead, lane.Unknown = HopDependencies+":unread", hops[0].Reason, []chart.Interval{r.window()}
			}
			var unknown [][]chart.Interval
			for _, hop := range hops {
				names = append(names, hop.Spec.Short)
				if !notRead {
					unknown = append(unknown, r.hopLane(hop).Unknown)
				}
			}
			if !notRead {
				lane.Unknown = Union(unknown...)
			}
			lane.Label = strings.Join(names, " · ")
			if len(lane.Label) > 28 {
				lane.Label = fmt.Sprintf("%d dependencies", len(names))
			}
			r.Lanes = append(r.Lanes, lane)
		}
		merge(quiet, false)
		merge(unread, true)
		quiet, unread = nil, nil
	}
	for i := range r.Hops {
		hop := &r.Hops[i]
		dependency := strings.HasPrefix(hop.Spec.ID, depPrefix)
		if !dependency {
			flush()
		}
		switch {
		case hop.Spec.ID == HopDependencies:
			// Nothing is declared, so there is no lane to draw. The hop row
			// and the summary say so.
		case hop.Spec.ID == HopService:
			r.Lanes = append(r.Lanes, r.serviceLane(hop))
		case hop.Spec.ID == HopApp:
			r.Lanes = append(r.Lanes, r.appLanes(hop)...)
		case dependency && hop.State == NotRead:
			unread = append(unread, hop)
		case dependency && r.hopLane(hop).Marks == nil:
			quiet = append(quiet, hop)
		default:
			r.Lanes = append(r.Lanes, r.hopLane(hop))
		}
	}
	flush()
	r.Lanes = append(r.Lanes, r.factLanes()...)
}

// factLanes are the dated facts that are cheap to know: restarts, rollouts
// and new Pods from the Kubernetes API (and the restart counter when it is
// read), and firing alerts when an Alertmanager is declared.
func (r *Report) factLanes() []Lane {
	lanes := []Lane{r.restartsLane(), r.rolloutsLane()}
	if r.Alerts != nil {
		lanes = append(lanes, r.alertsLane())
	}
	return lanes
}

// restartsLane marks where the restart counter grew and the last restart
// the API reports for each Pod.
func (r *Report) restartsLane() Lane {
	lane := Lane{ID: "restarts", Label: "restarts", Short: "restarts", Phrase: "restarts"}
	var counter *SignalRead
	if app := r.Hop(HopApp); app != nil {
		counter = app.Signal(SignalRestarts)
	}
	history := counter != nil && counter.Read
	restarted := map[string]bool{}
	if history {
		lane.Lead = r.Step
		lane.Marks, lane.Unknown = counter.Crossed, r.unknown(counter)
		for i := range counter.Series {
			series := &counter.Series[i]
			for _, iv := range series.Crossed {
				restarted[series.Pod] = true
				lane.Pod = series.Pod
				lane.Events = append(lane.Events, Event{At: iv.Start, Label: r.ShortPod(series.Pod) + " restarted"})
			}
		}
	}
	for i := range r.Path.Pods {
		pod := &r.Path.Pods[i]
		if pod.LastRestart.IsZero() || !pod.LastRestart.After(r.Start) || pod.LastRestart.After(r.End) {
			continue
		}
		restarted[pod.Name] = true
		lane.Pod = pod.Name
		lane.Marks = append(lane.Marks, chart.Interval{Start: pod.LastRestart, End: pod.LastRestart})
		lane.Events = append(lane.Events, Event{At: pod.LastRestart, Label: r.ShortPod(pod.Name) + " last restart"})
		if !history && pod.Restarts > 1 {
			// The API keeps only the last restart of a container.
			lane.Unknown = append(lane.Unknown, chart.Interval{Start: r.Start, End: pod.LastRestart})
		}
	}
	lane.Marks, lane.Unknown = Normalize(lane.Marks), Normalize(lane.Unknown)
	if len(restarted) != 1 {
		lane.Pod = ""
	}
	if lane.Pod != "" {
		lane.Where = "on pod " + r.ShortPod(lane.Pod)
	}
	return lane
}

// rolloutsLane marks the creation of the workload's retained revisions and
// of Pods created inside the window.
func (r *Report) rolloutsLane() Lane {
	lane := Lane{ID: "rollouts", Label: "rollouts, new pods", Short: "rollouts", Phrase: "a rollout or new Pod"}
	for _, event := range r.Path.Rollouts {
		if event.At.After(r.Start) && !event.At.After(r.End) {
			lane.Marks = append(lane.Marks, chart.Interval{Start: event.At, End: event.At})
			lane.Events = append(lane.Events, Event{At: event.At, Label: event.Label + " rolled out"})
		}
	}
	for i := range r.Path.Pods {
		pod := &r.Path.Pods[i]
		if !pod.Created.IsZero() && pod.Created.After(r.Start) && !pod.Created.After(r.End) {
			lane.Marks = append(lane.Marks, chart.Interval{Start: pod.Created, End: pod.Created})
			lane.Events = append(lane.Events, Event{At: pod.Created, Label: r.ShortPod(pod.Name) + " created"})
		}
	}
	lane.Marks = Normalize(lane.Marks)
	if len(r.Path.Rollouts) == 0 && r.Path.RolloutsNote != "" && len(lane.Marks) == 0 {
		lane.Label, lane.NotRead, lane.Unknown = "rollouts", r.Path.RolloutsNote, []chart.Interval{r.window()}
	}
	return lane
}

// alertsLane marks the time each alert of the namespace has been firing.
func (r *Report) alertsLane() Lane {
	lane := Lane{ID: "alerts", Label: "alerts firing", Short: "alerts", Phrase: "a firing alert"}
	if !r.Alerts.Read {
		lane.NotRead, lane.Unknown = r.Alerts.Note, []chart.Interval{r.window()}
	}
	for _, alert := range r.Alerts.Firing {
		end := alert.End
		if end.IsZero() || end.After(r.End) {
			end = r.End
		}
		span := Clip([]chart.Interval{{Start: alert.Start, End: end}}, r.Start, r.End)
		if len(span) == 0 {
			continue
		}
		lane.Marks = append(lane.Marks, span...)
		lane.Events = append(lane.Events, Event{At: span[0].Start, Label: alert.Name})
	}
	lane.Marks = Normalize(lane.Marks)
	return lane
}

// CompactLanes is the strip for a small screen: the app hop on one lane
// instead of one per signal. The summary is unchanged.
func (r *Report) CompactLanes() []Lane {
	out := make([]Lane, 0, len(r.Lanes))
	merged := false
	for i := range r.Lanes {
		lane := r.Lanes[i]
		if lane.Hop != HopApp {
			out = append(out, lane)
			continue
		}
		if merged {
			continue
		}
		merged = true
		if app := r.Hop(HopApp); app != nil {
			lane = r.hopLane(app)
		}
		out = append(out, lane)
	}
	return out
}

// Detail states one hop in full for the detail area: what was read, the
// worst sample, the rule, the sample counts and the exact queries, and for
// what was not read the reason.
func (r *Report) Detail(h *Hop) []string {
	head := h.Spec.Label() + " · " + h.Spec.What
	if len(h.Spec.Profiles) > 0 {
		head += " · profiles " + strings.Join(h.Spec.Profiles, ", ")
	}
	lines := []string{head + " · " + string(h.State)}
	if h.State == NotRead {
		lines = append(lines, "not read: "+h.Reason)
	}
	for _, missing := range h.Spec.NotRead {
		what := strings.TrimSpace(missing.Profile + " " + missing.Signal)
		if what == "" {
			what = "hop"
		}
		lines = append(lines, "not read ("+what+"): "+missing.Reason)
	}
	if h.Spec.ID == HopService {
		path := r.Path
		lines = append(lines, fmt.Sprintf("Kubernetes API at %s: %d of %d EndpointSlice records ready, %d Pods", r.Clock(path.ReadAt),
			path.EndpointsReady, path.Endpoints, len(path.Pods)),
			"rule: raised when an endpoint is not ready now, a Pod was created or changed Ready inside the window, "+
				"or kube_pod_status_ready was 0")
	}
	// The raised signals come first, then the ones with a rule, then
	// context and what was not read; the declared order is kept within each.
	order := make([]int, len(h.Signals))
	for i := range order {
		order[i] = i
	}
	rank := func(sig *SignalRead) int {
		switch {
		case len(sig.Outliers) > 0:
			return 0
		case len(sig.Crossed) > 0:
			return 1
		case sig.Read && sig.Bad():
			return 2
		case sig.Read:
			return 3
		}
		return 4
	}
	sort.SliceStable(order, func(a, b int) bool { return rank(&h.Signals[order[a]]) < rank(&h.Signals[order[b]]) })
	for _, i := range order {
		sig := &h.Signals[i]
		head := sig.Name + " [" + sig.Profile + "] " + sig.Title
		if !sig.Read {
			lines = append(lines, head, "  not read: "+sig.Reason, "  query: "+sig.Text)
			continue
		}
		lines = append(lines, head, "  "+r.SignalStat(sig), "  rule: "+sig.Rule.Text(), "  query: "+sig.Text)
		for _, deviation := range sig.Outliers {
			what := "worst sample"
			if deviation.Sustained {
				what = "usual level"
			}
			lines = append(lines, fmt.Sprintf("  differs from peers: %s %s %s vs peers %s", r.ShortPod(deviation.Pod), what, sig.Format(deviation.Value),
				sig.Format(deviation.Peers)))
		}
		if sig.PeerNote != "" {
			lines = append(lines, "  peers: "+sig.PeerNote)
		}
		if sig.Note != "" {
			lines = append(lines, "  note: "+sig.Note)
		}
	}
	if h.Spec.ID == HopApp {
		lines = append(lines, "peer rule: a Pod differs when its worst sample deviates from its peers' (modified z-score 3.5, twice their median, "+
			"past the rule's floor) and crossed the signal's rule, or when its usual level deviates the same way; fewer than 3 Pods are not compared")
	}
	return lines
}

// SignalStat is one line of numbers for a signal: the worst sample and
// when, the usual level, how long the rule was crossed and the sample
// count.
func (r *Report) SignalStat(sig *SignalRead) string {
	if !sig.Read {
		return "not read: " + sig.Reason
	}
	parts := []string{"worst " + sig.Format(sig.Worst) + " at " + r.Clock(sig.WorstAt)}
	if series := sig.WorstSeries(); series != nil {
		pod := series.Pod
		if pod != "" {
			pod = r.ShortPod(pod)
		}
		if where := strings.TrimSpace(series.Group + " " + pod); where != "" {
			parts[0] += " (" + where + ")"
		}
		parts = append(parts, "usual "+sig.Format(series.Median))
	}
	if sig.Bad() {
		if len(sig.Crossed) > 0 {
			parts = append(parts, fmt.Sprintf("raised %s of %s in %d stretches", impact.Dur(Total(sig.Crossed)), r.Span(), len(sig.Crossed)))
		} else {
			parts = append(parts, "not raised")
		}
	}
	parts = append(parts, fmt.Sprintf("%d samples in %d series, step %s", sig.Samples, len(sig.Series), impact.Dur(r.Step)))
	return strings.Join(parts, " · ")
}
