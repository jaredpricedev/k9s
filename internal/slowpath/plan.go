// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
)

// Hop identifiers, in the order a request meets them. A declared dependency
// has the identifier "dep:" followed by its namespace/name.
const (
	HopIngress      = "ingress"
	HopService      = "service"
	HopApp          = "app"
	HopDependencies = "dependencies"
	HopDNS          = "dns"
	HopNetwork      = "network"
	depPrefix       = "dep:"
)

// Bounds of one read.
const (
	// MaxQueries is the most range reads one batch sends.
	MaxQueries = 48
	// Concurrency is how many of them are in flight at once.
	Concurrency = 4
	// Deadline bounds the whole batch.
	Deadline = 45 * time.Second
	// DefaultWindow is the window the view opens on.
	DefaultWindow = time.Hour
)

// Profiles named for the readings :slow adds to the declared ones.
const (
	ProfileKubeState = "kube-state-metrics"
	ProfileCAdvisor  = "cadvisor"
)

// HopSpec is one row of the path: what it is and how it is read.
type HopSpec struct {
	ID string
	// Kind is the request-path hop kind; empty for the service hop, which
	// the Kubernetes API describes.
	Kind observability.HopKind
	// From and To name the stretch ("client", "ingress"); From is empty
	// for a hop that is not a stretch of the path (dns, network).
	From, To string
	// Short names the hop in a lane label and in the summary.
	Short string
	// What says what is read.
	What     string
	Profiles []string
	// NotRead lists what could not be planned, with the reason.
	NotRead []observability.NotRead
}

// Label is "client → ingress", or the plain name of a hop that is not a
// stretch.
func (h *HopSpec) Label() string {
	if h.From == "" {
		return h.To
	}
	return h.From + " → " + h.To
}

// Query is one range read of the batch.
type Query struct {
	observability.HopQuery
	// HopIndex is the row the answer belongs to.
	HopIndex int
	// Sent is the expression that is asked: per Pod when PerPod is set.
	Sent   observability.Expr
	PerPod bool
	// Look says how far back one sample of the answer looks: the rate
	// window for a rate, a share of rates or a quantile, nothing for a gauge.
	Look Lookback
}

// Plan is one bounded batch for one window. Building it contacts nothing.
type Plan struct {
	Path             *Path
	Window           time.Duration
	Start, End       time.Time
	Step, RateWindow time.Duration
	Hops             []HopSpec
	Queries          []Query
}

func (p *Plan) hop(id string) int {
	for i := range p.Hops {
		if p.Hops[i].ID == id {
			return i
		}
	}
	return -1
}

func (p *Plan) add(q *Query) {
	if len(p.Queries) >= MaxQueries {
		hop := &p.Hops[q.HopIndex]
		hop.NotRead = append(hop.NotRead, observability.NotRead{Hop: q.Hop, Profile: q.Profile, Signal: q.Signal.Name,
			Reason: fmt.Sprintf("over the limit of %d range reads per batch", MaxQueries)})
		return
	}
	p.Queries = append(p.Queries, *q)
}

// take adds the queries of one resolved hop to a row. perPod asks for the
// per-Pod form where the profile says which label carries the Pod name.
func (p *Plan) take(index int, resolved *observability.HopPlan, perPod bool) {
	hop := &p.Hops[index]
	hop.Profiles = append(hop.Profiles, resolved.Profiles...)
	hop.NotRead = append(hop.NotRead, resolved.NotRead...)
	for i := range resolved.Queries {
		q := Query{HopQuery: resolved.Queries[i], HopIndex: index, Sent: resolved.Queries[i].Expr}
		switch q.Signal.Kind {
		case observability.SignalRate, observability.SignalRatio:
			q.Look = Lookback{Window: p.RateWindow}
		case observability.SignalQuantile:
			q.Look = Lookback{Window: p.RateWindow, Quantile: true}
		}
		if perPod && q.PodLabel != "" && q.ByPod.Err() == nil {
			q.Sent, q.PerPod = q.ByPod, true
		}
		// A Hubble signal about DNS answers to these Pods is DNS evidence:
		// CoreDNS's own numbers are cluster-wide and cannot show it.
		if dns := p.hop(HopDNS); dns >= 0 && resolved.Hop == observability.HopNetwork && strings.HasPrefix(q.Signal.Name, "dns") {
			q.HopIndex = dns
		}
		p.add(&q)
	}
}

// fixed adds one reading with fixed kube-state-metrics or cAdvisor names.
func (p *Plan) fixed(index int, profile string, sig *observability.Signal, expr observability.Expr, scope string, omitted int) {
	p.add(&Query{HopIndex: index, Sent: expr, PerPod: true, HopQuery: observability.HopQuery{
		Hop: p.Hops[index].Kind, Profile: profile, Signal: *sig, Expr: expr, ByPod: expr, PodLabel: "pod", Scope: scope, PodsOmitted: omitted}})
}

// shortName drops the dash-separated words a dependency shares with the
// Service at the start of its name: gitlab-postgresql beside
// gitlab-webservice-default is postgresql.
func shortName(name, service string) string {
	words, other := strings.Split(name, "-"), strings.Split(service, "-")
	skip := 0
	for skip < len(words)-1 && skip < len(other) && words[skip] == other[skip] {
		skip++
	}
	return strings.Join(words[skip:], "-")
}

func entryHost(entry string) string {
	host, _, _ := strings.Cut(strings.TrimSpace(entry), " ")
	if host == "" || strings.HasPrefix(host, "/") {
		return ""
	}
	return host
}

// BuildPlan resolves every hop of the path into one bounded batch of range
// reads for the window that ends now. It contacts nothing; what cannot be
// resolved is listed per hop with the reason.
func BuildPlan(sources *observability.Sources, path *Path, window time.Duration, now time.Time) *Plan {
	if window <= 0 {
		window = DefaultWindow
	}
	step := observability.StepFor(window)
	// The window ends on a whole step, and on a whole minute when times are
	// written without seconds: a column of the lanes strip and the cursor
	// then begin and end at the minute they are labeled with.
	align := step
	if window > 15*time.Minute {
		align = max(step, time.Minute)
	}
	end := now.UTC().Truncate(align)
	plan := &Plan{Path: path, Window: window, Start: end.Add(-window), End: end, Step: step, RateWindow: impact.RateWindowFor(step)}
	key, pods, rate := path.Key(), path.PodNames(), plan.RateWindow
	add := func(spec *HopSpec) int {
		plan.Hops = append(plan.Hops, *spec)
		return len(plan.Hops) - 1
	}

	// Ingress.
	index := add(&HopSpec{ID: HopIngress, Kind: observability.HopIngress, From: "client", To: "ingress", Short: "ingress"})
	if len(path.Ingresses) == 0 {
		plan.Hops[index].What = "no Ingress routes here"
		plan.Hops[index].NotRead = append(plan.Hops[index].NotRead, observability.NotRead{Hop: observability.HopIngress,
			Reason: "no Ingress in namespace " + path.Namespace + " routes to Service " + path.Service +
				"; HTTPRoutes and Ingresses in other namespaces are not read here"})
	} else {
		target := observability.HopTarget{Namespace: path.Namespace, Service: path.Service}
		what := fmt.Sprintf("%d Ingresses", len(path.Ingresses))
		if len(path.Ingresses) == 1 || path.Selection.Kind == KindIngress {
			target.Ingress = path.Ingresses[0].Name
			what = path.Ingresses[0].Name
			if host := entryHost(path.Ingresses[0].Entry); host != "" {
				what = host
			}
		}
		resolved := sources.ResolveHop(observability.HopIngress, key, target, rate)
		plan.take(index, &resolved, false)
		plan.Hops[index].What = what
	}

	// Service: the Kubernetes API says what is ready now; kube-state-metrics
	// says when a Pod was not.
	from := "ingress"
	if len(path.Ingresses) == 0 {
		from = "client"
	}
	index = add(&HopSpec{ID: HopService, From: from, To: "service", Short: "endpoints", What: fmt.Sprintf("%d endpoints", path.Endpoints)})
	namespace := observability.Eq("namespace", path.Namespace)
	// The same matcher the profiles of the hop are pinned with.
	matcher, omitted := observability.PodMatcher("pod", pods)
	scope := fmt.Sprintf("namespace=%q,pod%s%q", path.Namespace, string(matcher.Op), matcher.Value)
	if len(pods) > 0 {
		plan.fixed(index, ProfileKubeState,
			&observability.Signal{Name: SignalReady, Title: "Pod Ready condition (1 is Ready)", Kind: observability.SignalGauge,
				Metric: "kube_pod_status_ready", Unit: observability.UnitCount, Worse: WorseLow, Aggregate: "min"},
			observability.Aggregate("min", observability.Select("kube_pod_status_ready", namespace, matcher, observability.Eq("condition", "true")), "pod"),
			scope, omitted)
	}

	// App.
	index = add(&HopSpec{ID: HopApp, Kind: observability.HopApp, From: "service", To: "pods", Short: "pods"})
	resolved := sources.ResolveHop(observability.HopApp, key, observability.HopTarget{Namespace: path.Namespace, Service: path.Service, Pods: pods}, rate)
	plan.take(index, &resolved, true)
	plan.Hops[index].What = fmt.Sprintf("%d pods", len(pods))
	if len(pods) > 0 {
		containers := []observability.Matcher{namespace, matcher,
			{Label: "container", Op: observability.MatchNotEqual, Value: ""}, {Label: "container", Op: observability.MatchNotEqual, Value: "POD"}}
		perContainer := func(e observability.Expr) observability.Expr {
			return observability.Aggregate("max", e, "pod", "container")
		}
		plan.fixed(index, ProfileCAdvisor,
			&observability.Signal{Name: SignalMemory, Title: "memory in use against the limit, fullest container", Kind: observability.SignalRatio,
				Metric: "container_memory_working_set_bytes", Divisor: "kube_pod_container_resource_limits", Unit: observability.UnitRatio, Worse: WorseHigh},
			observability.Aggregate("max", observability.Div(
				perContainer(observability.Select("container_memory_working_set_bytes", containers...)),
				perContainer(observability.Select("kube_pod_container_resource_limits", namespace, matcher, observability.Eq("resource", "memory")))), "pod"),
			scope, omitted)
		plan.fixed(index, ProfileKubeState,
			&observability.Signal{Name: SignalRestarts, Title: "container restarts", Kind: observability.SignalGauge,
				Metric: "kube_pod_container_status_restarts_total", Unit: observability.UnitCount, Worse: WorseHigh},
			observability.Sum(observability.Select("kube_pod_container_status_restarts_total", namespace, matcher), "pod"),
			scope, omitted)
	}

	// Dependencies, each declared by name.
	dependencies := sources.ResolveDependencies(key, path.Dependencies, rate)
	for i := range dependencies {
		dependency := &dependencies[i]
		if dependency.Target.Service == "" {
			add(&HopSpec{ID: HopDependencies, Kind: observability.HopDependency, From: "pods", To: "dependencies", Short: "dependencies",
				What: "none declared", NotRead: dependency.NotRead})
			continue
		}
		short := shortName(dependency.Target.Service, path.Service)
		index = add(&HopSpec{ID: depPrefix + dependency.Target.Namespace + "/" + dependency.Target.Service, Kind: observability.HopDependency,
			From: "pods", To: short, Short: short, What: dependency.Target.Service})
		plan.take(index, dependency, false)
	}

	// DNS, then the network: a Hubble DNS signal lands on the DNS row.
	index = add(&HopSpec{ID: HopDNS, Kind: observability.HopDNS, To: "dns", Short: "dns"})
	resolved = sources.ResolveHop(observability.HopDNS, key, observability.HopTarget{Namespace: path.Namespace, Service: path.Service, Pods: pods}, rate)
	plan.take(index, &resolved, false)
	what := strings.Join(resolved.Profiles, ", ")
	if len(resolved.Queries) > 0 && resolved.Queries[0].ClusterWide {
		what += ", whole cluster"
	}
	plan.Hops[index].What = what

	index = add(&HopSpec{ID: HopNetwork, Kind: observability.HopNetwork, To: "network", Short: "network"})
	resolved = sources.ResolveHop(observability.HopNetwork, key, observability.HopTarget{Namespace: path.Namespace, Pods: pods}, rate)
	plan.take(index, &resolved, true)
	plan.Hops[index].What = strings.Join(resolved.Profiles, ", ") + ", to pods"
	return plan
}

// Ranger is the one read the batch makes. *observability.Client is one.
type Ranger interface {
	QueryRange(ctx context.Context, e observability.Expr, start, end time.Time) observability.PromResult
}

// Collect sends the batch: every query once, a few at a time, each with
// the limits of the declared source's client, and the whole batch within
// Deadline. It returns one result per query, in order. Canceling the
// context stops what has not been sent.
func Collect(ctx context.Context, source Ranger, plan *Plan) []observability.PromResult {
	ctx, cancel := context.WithTimeout(ctx, Deadline)
	defer cancel()
	results := make([]observability.PromResult, len(plan.Queries))
	var group sync.WaitGroup
	slots := make(chan struct{}, Concurrency)
	for i := range plan.Queries {
		group.Add(1)
		go func() {
			defer group.Done()
			canceled := func() {
				status := observability.Status{Kind: observability.KindPrometheus, State: observability.StateUnavailable, Reason: "canceled"}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					status.State, status.Reason = observability.StateTimeout, "not sent: the batch deadline of "+impact.Dur(Deadline)+" passed"
				}
				results[i] = observability.PromResult{Query: plan.Queries[i].Sent.String(), Status: status}
			}
			// A read that was canceled before its turn is never sent.
			if ctx.Err() != nil {
				canceled()
				return
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				canceled()
				return
			}
			if ctx.Err() != nil {
				canceled()
				return
			}
			results[i] = source.QueryRange(ctx, plan.Queries[i].Sent, plan.Start, plan.End)
		}()
	}
	group.Wait()
	return results
}

// Unread is the result set of a batch that was never sent, with the reason
// on every query: no source is declared, or it could not be resolved.
func Unread(plan *Plan, status observability.Status) []observability.PromResult { //nolint:gocritic // A small value, copied per query.
	results := make([]observability.PromResult, len(plan.Queries))
	for i := range results {
		results[i] = observability.PromResult{Status: status, Query: plan.Queries[i].Sent.String()}
	}
	return results
}
