// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/observability"
)

// Ranger is the one read a comparison makes. *observability.Client is one.
type Ranger interface {
	QueryRange(ctx context.Context, e observability.Expr, start, end time.Time) observability.PromResult
}

// Concurrency bounds how many range reads are in flight at once.
const Concurrency = 3

// Request is one comparison to make.
type Request struct {
	Workload Workload
	Rollout  Rollout
	Windows  Windows
	// RED maps the HTTP metric and label names; nil keeps the defaults.
	RED *observability.RED
	// Source describes the Prometheus source for the report header.
	Source string
}

// Report is one retained comparison.
type Report struct {
	Workload Workload
	Rollout  Rollout
	Windows  Windows
	Source   string
	ReadAt   time.Time
	// Step is the sample spacing; RateWindow the range every rate covers.
	Step, RateWindow time.Duration
	// HTTPSelector says how HTTP series were matched to the workload.
	HTTPSelector string
	Outcomes     []Outcome
	// Changes lists what the revision changed against the one before it;
	// names only, never values.
	Changes []string
	// MemoryLimit is "512Mi -> 256Mi" when the revision changed it.
	MemoryLimit string
	// Traffic is set when the request rate moved by TrafficShift or more.
	Traffic string
	// Failure is set when no query was answered at all.
	Failure string
	Notes   []string
}

const (
	qRequests  = "requests"
	qErrors    = "errors"
	qBuckets   = "buckets"
	qRestarts  = "restarts"
	qMemory    = "memory"
	qMemLimit  = "memlimit"
	qCPU       = "cpu"
	qThrottled = "throttled"
	qReady     = "ready"
)

var queryOrder = []string{qRequests, qErrors, qBuckets, qRestarts, qMemory, qMemLimit, qCPU, qThrottled, qReady}

// RateWindowFor is the range each rate covers for a given sample spacing:
// four steps, and at least MinRateWindow.
func RateWindowFor(step time.Duration) time.Duration {
	return max(4*step, MinRateWindow)
}

// Queries builds the fixed query templates for a workload. Every name and
// value goes through the typed builder, which validates names and writes
// values as escaped literals.
func Queries(w Workload, red *observability.RED, rate time.Duration) (map[string]observability.Expr, string) {
	m := red.WithDefaults()
	eq, re, sel := observability.Eq, observability.Re, observability.Select
	sum, rated := observability.Sum, observability.Rate
	http := []observability.Matcher{eq(m.NamespaceLabel, w.Namespace)}
	how := m.PodLabel + "=~" + strconv.Quote(w.PodPattern())
	if m.WorkloadLabel != "" {
		http = append(http, eq(m.WorkloadLabel, w.Name))
		how = m.WorkloadLabel + "=" + strconv.Quote(w.Name)
	} else {
		http = append(http, re(m.PodLabel, w.PodPattern()))
	}
	with := func(base []observability.Matcher, extra ...observability.Matcher) []observability.Matcher {
		return append(append([]observability.Matcher(nil), base...), extra...)
	}
	// Resource series come from cAdvisor and kube-state-metrics, whose
	// label names are fixed. container!="" drops the pod-level cgroup.
	pods := []observability.Matcher{eq("namespace", w.Namespace), re("pod", w.PodPattern())}
	containers := with(pods, observability.Matcher{Label: "container", Op: observability.MatchNotEqual, Value: ""},
		observability.Matcher{Label: "container", Op: observability.MatchNotEqual, Value: "POD"})
	perContainer := func(e observability.Expr) observability.Expr {
		return observability.Aggregate("max", e, "pod", "container")
	}
	readyMetric, readyLabel := "kube_deployment_status_replicas_available", "deployment"
	switch w.Kind {
	case KindStatefulSet:
		readyMetric, readyLabel = "kube_statefulset_status_replicas_ready", "statefulset"
	case KindDaemonSet:
		readyMetric, readyLabel = "kube_daemonset_status_number_ready", "daemonset"
	}
	return map[string]observability.Expr{
		qRequests: sum(rated(sel(m.Requests, http...), rate)),
		qErrors:   sum(rated(sel(m.Requests, with(http, re(m.CodeLabel, m.ErrorCodes))...), rate)),
		qBuckets:  sum(rated(sel(m.DurationBuckets, http...), rate), "le"),
		qRestarts: sum(sel("kube_pod_container_status_restarts_total", pods...)),
		qMemory:   observability.Aggregate("max", sel("container_memory_working_set_bytes", containers...)),
		qMemLimit: observability.Aggregate("max", observability.Div(
			perContainer(sel("container_memory_working_set_bytes", containers...)),
			perContainer(sel("kube_pod_container_resource_limits", with(pods, eq("resource", "memory"))...)))),
		qCPU: sum(rated(sel("container_cpu_usage_seconds_total", containers...), rate)),
		qThrottled: observability.Div(
			sum(rated(sel("container_cpu_cfs_throttled_periods_total", containers...), rate)),
			sum(rated(sel("container_cpu_cfs_periods_total", containers...), rate))),
		qReady: observability.Aggregate("max", sel(readyMetric, eq("namespace", w.Namespace), eq(readyLabel, w.Name))),
	}, how
}

// Collect makes the bounded range reads for one comparison and reduces them.
// It always returns a report; what could not be read is stated per signal.
func Collect(ctx context.Context, source Ranger, req *Request) *Report {
	span := req.Windows.After.End.Sub(req.Windows.Before.Start)
	step := observability.StepFor(span)
	report := &Report{Workload: req.Workload, Rollout: req.Rollout, Windows: req.Windows, Source: req.Source,
		Step: step, RateWindow: RateWindowFor(step)}
	report.Changes, report.MemoryLimit = req.Rollout.Changes()
	queries, how := Queries(req.Workload, req.RED, report.RateWindow)
	report.HTTPSelector = how

	results := make(map[string]observability.PromResult, len(queries))
	var (
		mu    sync.Mutex
		group sync.WaitGroup
	)
	slots := make(chan struct{}, Concurrency)
	for _, key := range queryOrder {
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				mu.Lock()
				results[key] = observability.PromResult{Status: observability.Status{State: observability.StateUnavailable, Reason: "canceled"}}
				mu.Unlock()
				return
			}
			result := source.QueryRange(ctx, queries[key], req.Windows.Before.Start, req.Windows.After.End)
			mu.Lock()
			results[key] = result
			mu.Unlock()
		}()
	}
	group.Wait()
	report.ReadAt = time.Now().UTC()
	report.reduce(results)
	return report
}

func points(result *observability.PromResult) []Point {
	if len(result.Series) == 0 {
		return nil
	}
	out := make([]Point, 0, len(result.Series[0].Samples))
	for _, sample := range result.Series[0].Samples {
		out = append(out, Point{T: sample.Timestamp, V: sample.Value})
	}
	return out
}

// unread explains a read that produced no usable answer, or returns "".
func unread(result *observability.PromResult) string {
	if result.State == observability.StateComplete || (result.State == observability.StateIncomplete && len(result.Series) > 0) {
		return ""
	}
	text := string(result.State)
	if result.HTTPStatus != 0 {
		text += fmt.Sprintf(" (HTTP %d)", result.HTTPStatus)
	}
	if result.Reason != "" && !strings.Contains(text, result.Reason) {
		text += ": " + result.Reason
	}
	return text
}

func (r *Report) reduce(results map[string]observability.PromResult) {
	w := &r.Windows
	failed, firstFailure := 0, ""
	for _, key := range queryOrder {
		result := results[key]
		if why := unread(&result); why != "" {
			failed++
			if firstFailure == "" {
				firstFailure = why
			}
		}
	}
	if failed == len(queryOrder) {
		r.Failure = firstFailure
	}
	for index := range Signals {
		signal := Signals[index]
		outcome := Outcome{Signal: signal}
		guard := time.Duration(0)
		if signal.Rate {
			guard = r.RateWindow
		}
		use := func(keys ...string) bool {
			for _, key := range keys {
				result := results[key]
				outcome.Queries = append(outcome.Queries, result.Query)
				if why := unread(&result); why != "" && outcome.State == "" {
					outcome.State, outcome.Reason = NotRead, why
				}
				if result.State == observability.StateIncomplete && len(result.Series) > 0 {
					outcome.Notes = append(outcome.Notes, "the source answer was cut by a read limit")
				}
			}
			if outcome.State != "" {
				return false
			}
			first := results[keys[0]]
			if len(first.Series) == 0 {
				outcome.State, outcome.Reason = NotAvailable, "no series matched (the source answered)"
				return false
			}
			return true
		}
		var reduced reduction
		switch signal.ID {
		case SigErrors:
			if !use(qRequests, qErrors) {
				break
			}
			requests, errors := results[qRequests], results[qErrors]
			if len(errors.Series) == 0 {
				outcome.Notes = append(outcome.Notes, "no error series matched; counted as zero errors because request series exist")
			}
			reduced = reduceRatio(points(&errors), points(&requests), w, guard)
		case SigP50, SigP95, SigP99:
			if !use(qBuckets) {
				break
			}
			buckets := results[qBuckets]
			if buckets.NotRetained > 0 {
				outcome.State = NotRead
				outcome.Reason = fmt.Sprintf("%d bucket series were over the read limit of %d", buckets.NotRetained, observability.MaxSeries)
				break
			}
			byBound := map[float64][]Point{}
			for _, series := range buckets.Series {
				bound, err := strconv.ParseFloat(series.Labels["le"], 64)
				if err != nil {
					continue
				}
				for _, sample := range series.Samples {
					byBound[bound] = append(byBound[bound], Point{T: sample.Timestamp, V: sample.Value})
				}
			}
			if _, ok := byBound[math.Inf(1)]; !ok {
				outcome.State, outcome.Reason = NotAvailable, "the histogram has no +Inf bucket"
				break
			}
			reduced = reduceQuantile(signal.Quantile, byBound, w, guard)
		case SigRestarts:
			if use(qRestarts) {
				result := results[qRestarts]
				reduced = reduceIncrease(points(&result), w)
			}
		default:
			if use(signal.ID) {
				result := results[signal.ID]
				reduced = reduceGauge(points(&result), w, guard, signal.Reduce)
			}
		}
		if outcome.State == "" {
			outcome.Before, outcome.After, outcome.Skipped, outcome.Series = reduced.before, reduced.after, reduced.skipped, reduced.series
			outcome.State = Compared
			lacking := func(name string, side Side) string {
				switch {
				case side.Samples < MinSamples:
					return fmt.Sprintf("%s has %d of the %d samples needed", name, side.Samples, MinSamples)
				case !side.Valid:
					return name + " has no value (nothing was counted)"
				}
				return ""
			}
			var short []string
			for _, text := range []string{lacking("before", outcome.Before), lacking("after", outcome.After)} {
				if text != "" {
					short = append(short, text)
				}
			}
			if len(short) > 0 {
				outcome.State, outcome.Reason = TooFewSamples, strings.Join(short, "; ")
			} else {
				outcome.Verdict = compare(&signal, outcome.Before.Value, outcome.After.Value)
			}
		}
		r.Outcomes = append(r.Outcomes, outcome)
	}
	if requests := r.Outcome(SigRequests); requests != nil && requests.State == Compared {
		before, after := requests.Before.Value, requests.After.Value
		if before > 0 && (after >= before*TrafficShift || after <= before/TrafficShift) {
			r.Traffic = fmt.Sprintf("Request rate %s -> %s (%s): error ratio and latency are not like-for-like.",
				requests.Signal.Format(before), requests.Signal.Format(after), PercentChange(before, after))
			for _, id := range []string{SigErrors, SigP50, SigP95, SigP99} {
				if outcome := r.Outcome(id); outcome != nil && outcome.State == Compared {
					outcome.Notes = append(outcome.Notes, "request volume differs by 2x or more between the windows; not like-for-like")
				}
			}
		}
	}
}

// Outcome returns one signal's comparison, or nil.
func (r *Report) Outcome(id string) *Outcome {
	for index := range r.Outcomes {
		if r.Outcomes[index].Signal.ID == id {
			return &r.Outcomes[index]
		}
	}
	return nil
}

// Counts reports how many signals fell in each group.
func (r *Report) Counts() (worse, better, same, uncompared int) {
	for index := range r.Outcomes {
		outcome := &r.Outcomes[index]
		switch {
		case outcome.State != Compared:
			uncompared++
		case outcome.Verdict == Worse:
			worse++
		case outcome.Verdict == Better:
			better++
		default:
			same++
		}
	}
	return
}

// Uncompared lists the signals that have no verdict, grouped by state, for
// one header line: "not available: CPU throttled · not read: ...".
func (r *Report) Uncompared() string {
	var groups []string
	for _, state := range []string{NotRead, NotAvailable, TooFewSamples} {
		var names []string
		for index := range r.Outcomes {
			if r.Outcomes[index].State == state {
				names = append(names, r.Outcomes[index].Signal.Label)
			}
		}
		if len(names) > 0 {
			groups = append(groups, state+": "+strings.Join(names, ", "))
		}
	}
	return strings.Join(groups, " · ")
}

func (o *Outcome) arrow() string {
	return o.Signal.Phrase + " " + o.Signal.Format(o.Before.Value) + " -> " + o.Signal.Format(o.After.Value)
}

// Summary is the one-sentence statement of what was measured after the
// rollout. It reports measurements in time order and names no cause.
func (r *Report) Summary() string {
	at, layout := r.Windows.Rollout.UTC(), "15:04"
	if read := r.ReadAt.UTC(); !r.ReadAt.IsZero() && (read.Year() != at.Year() || read.YearDay() != at.YearDay()) {
		layout = "Jan02 15:04"
	}
	head := fmt.Sprintf("After %s (%s): ", r.Rollout.Label(), at.Format(layout))
	if r.Failure != "" {
		return head + "nothing was read from the source: " + r.Failure + ". No comparison was made."
	}
	worse, better, same, _ := r.Counts()
	compared := worse + better + same
	if compared == 0 {
		return head + "no signal could be compared. That is missing data, not a sign that nothing changed."
	}
	limit := ""
	if r.MemoryLimit != "" {
		limit = " (limit " + r.MemoryLimit + ")"
	}
	var parts []string
	list := func(verdict string) {
		latency := false
		for _, id := range []string{SigErrors, SigP99, SigP95, SigP50, SigRestarts, SigMemLimit, SigThrottled, SigReady} {
			outcome := r.Outcome(id)
			if outcome == nil || outcome.State != Compared || outcome.Verdict != verdict {
				continue
			}
			// One latency figure is enough for the sentence: the highest
			// percentile that moved.
			if id == SigP99 || id == SigP95 || id == SigP50 {
				if latency {
					continue
				}
				latency = true
			}
			switch {
			case id == SigMemLimit && verdict == Worse && outcome.After.Value >= AtLimit:
				parts = append(parts, "memory at limit"+limit)
			case id == SigMemLimit:
				parts = append(parts, outcome.arrow()+limit)
			default:
				parts = append(parts, outcome.arrow())
			}
		}
	}
	list(Worse)
	text := head
	if worse == 0 {
		text += fmt.Sprintf("no compared signal is worse by the stated thresholds (%d compared", compared)
		if better > 0 {
			text += fmt.Sprintf(", %d better", better)
		}
		text += ")"
		list(Better)
		if len(parts) > 0 {
			text += "; better: " + strings.Join(parts, ", ")
		}
	} else {
		text += strings.Join(parts, ", ")
	}
	text += "."
	if r.Traffic != "" {
		text += " " + r.Traffic
	}
	return text
}

// Unread is the report for a comparison whose source could not be used at
// all. It still states what the revision changed, which needs no source.
func Unread(req *Request, why string) *Report {
	report := &Report{Workload: req.Workload, Rollout: req.Rollout, Windows: req.Windows, Source: req.Source,
		ReadAt: time.Now().UTC(), Failure: why}
	report.Changes, report.MemoryLimit = req.Rollout.Changes()
	return report
}
