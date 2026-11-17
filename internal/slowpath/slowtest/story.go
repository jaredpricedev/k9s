// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package slowtest is a synthetic request path for tests: an intermittent
// hang on one Pod of three, answered the way a declared Prometheus would
// answer the queries of a request-path batch. It contacts nothing.
package slowtest

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
)

// Names of the story.
const (
	Namespace = "gitlab"
	Service   = "gitlab-webservice-default"
	Ingress   = "gitlab-webservice-default"
	Host      = "gitlab.study.example.test"
	// Outlier is the Pod that stalls; Calm is the Pod the unrelated drop
	// blip is directed at.
	Outlier = "gitlab-webservice-default-6f9c7d5b84-hx4mt"
	Calm    = "gitlab-webservice-default-6f9c7d5b84-2bq7k"
	Third   = "gitlab-webservice-default-6f9c7d5b84-8wzpd"
)

// Now is the read time of the story. Everything is placed before it.
var Now = time.Date(2026, 11, 17, 8, 20, 0, 0, time.UTC)

// Stalls are the three stretches in which the outlier hangs, and Blip the
// unrelated burst of dropped packets towards another Pod, as offsets
// before Now.
var (
	Stalls = [][2]time.Duration{{52 * time.Minute, 49 * time.Minute}, {33 * time.Minute, 29 * time.Minute}, {7 * time.Minute, 5 * time.Minute}}
	Blip   = [2]time.Duration{41 * time.Minute, 40 * time.Minute}
)

// Story answers range reads. The zero value is the hang; Quiet removes the
// stalls and the blip.
type Story struct {
	// Quiet answers every signal at its usual level.
	Quiet bool
	// Deny, when its State is set, is the status of every read.
	Deny observability.Status
	// Absent lists query fragments that match no series, as a metric that
	// is not scraped does.
	Absent []string
	// Pods are the Pods series exist for; nil means the three of the story.
	Pods []string
	// Calls counts the reads.
	Calls atomic.Int64
}

func (s *Story) pods() []string {
	if s.Pods != nil {
		return s.Pods
	}
	return []string{Calm, Third, Outlier}
}

func between(t time.Time, span [2]time.Duration) bool {
	return !t.Before(Now.Add(-span[0])) && t.Before(Now.Add(-span[1]))
}

func (s *Story) stalled(t time.Time) bool {
	if s.Quiet {
		return false
	}
	for _, stall := range Stalls {
		if between(t, stall) {
			return true
		}
	}
	return false
}

// unready is true while the outlier fails its readiness probe: from 90
// seconds into each of the two longer stalls until 30 seconds after it.
func (s *Story) unready(t time.Time) bool {
	if s.Quiet {
		return false
	}
	for _, stall := range Stalls[:2] {
		if between(t, [2]time.Duration{stall[0] - 90*time.Second, stall[1] - 30*time.Second}) {
			return true
		}
	}
	return false
}

type answer struct {
	// has lists fragments the query must contain.
	has []string
	// label carries the Pod name; empty answers with one series.
	label string
	value func(s *Story, pod string, t time.Time) float64
}

func level(v float64) func(*Story, string, time.Time) float64 {
	return func(*Story, string, time.Time) float64 { return v }
}

// slow is the usual level, and another on the outlier (or, without a Pod,
// at the ingress) while it stalls.
func slow(usual, stalled float64) func(*Story, string, time.Time) float64 {
	return func(s *Story, pod string, t time.Time) float64 {
		if (pod == "" || pod == Outlier) && s.stalled(t) {
			return stalled
		}
		return usual
	}
}

// answers are matched in order: the more specific fragment first.
var answers = []answer{
	{has: []string{"nginx_ingress_controller_requests", `status="499"`}, value: slow(0.008, 2.2)},
	{has: []string{"nginx_ingress_controller_requests", `status=~"5.."`}, value: level(0.00003)},
	{has: []string{"nginx_ingress_controller_requests"}, value: slow(39.6, 35)},
	{has: []string{"nginx_ingress_controller_request_duration_seconds_bucket"}, value: slow(0.44, 10)},
	{has: []string{"nginx_ingress_controller_response_duration_seconds_bucket"}, value: slow(0.43, 10)},
	{has: []string{"histogram_quantile(0.5", "http_request_duration_seconds_bucket"}, label: "pod", value: slow(0.12, 20)},
	{has: []string{"http_request_duration_seconds_bucket"}, label: "pod", value: slow(0.35, 48)},
	{has: []string{"http_requests_total", `status=~"5.."`}, label: "pod", value: level(0.0003)},
	{has: []string{"http_requests_total", `code=~"5.."`}, label: "pod", value: level(0.0003)},
	{has: []string{"http_requests_total"}, label: "pod", value: slow(12, 1.5)},
	{has: []string{"puma_queued_connections"}, label: "pod", value: slow(0, 30)},
	{has: []string{"puma_pool_capacity"}, label: "pod", value: slow(5, 0)},
	{has: []string{"puma_active_connections"}, label: "pod", value: slow(3, 8)},
	{has: []string{"puma_max_threads"}, label: "pod", value: level(8)},
	{has: []string{"container_cpu_cfs_throttled_periods_total"}, label: "pod", value: func(s *Story, pod string, t time.Time) float64 {
		switch {
		case pod == Outlier && s.stalled(t):
			return 0.86
		case pod == Outlier:
			return 0.04
		}
		return 0.012
	}},
	{has: []string{"container_cpu_usage_seconds_total"}, label: "pod", value: slow(0.35, 1.47)},
	{has: []string{"kube_pod_status_ready"}, label: "pod", value: func(s *Story, pod string, t time.Time) float64 {
		if pod == Outlier && s.unready(t) {
			return 0
		}
		return 1
	}},
	{has: []string{"container_memory_working_set_bytes"}, label: "pod", value: level(0.62)},
	{has: []string{"kube_pod_container_status_restarts_total"}, label: "pod", value: level(0)},
	{has: []string{"pg_up"}, value: level(1)},
	{has: []string{"pg_stat_activity_count", `state="active"`}, value: level(6)},
	{has: []string{"pg_stat_activity_count"}, value: level(41)},
	{has: []string{"pg_settings_max_connections"}, value: level(100)},
	{has: []string{"redis_up"}, value: level(1)},
	{has: []string{"redis_commands_duration_seconds_total"}, value: level(0.00012)},
	{has: []string{"redis_connected_clients"}, value: level(42)},
	{has: []string{"grpc_server_handling_seconds_bucket"}, value: level(0.1)},
	{has: []string{"grpc_server_handled_total", `grpc_code!="OK"`}, value: level(0)},
	{has: []string{"grpc_server_handled_total"}, value: level(25)},
	{has: []string{"coredns_dns_request_duration_seconds_bucket"}, value: level(0.006)},
	{has: []string{"coredns_dns_responses_total", "SERVFAIL"}, value: level(0)},
	{has: []string{"coredns_dns_responses_total", "NXDOMAIN"}, value: level(0.15)},
	{has: []string{"coredns_dns_requests_total"}, value: level(40)},
	{has: []string{"hubble_tcp_flags_total"}, label: "destination_pod", value: level(0.0017)},
	{has: []string{"hubble_dns_responses_total"}, label: "destination_pod", value: level(0)},
	{has: []string{"hubble_flows_processed_total"}, label: "destination_pod", value: level(120)},
}

func samples(start, end time.Time, step time.Duration, value func(t time.Time) (float64, bool)) []observability.Sample {
	var out []observability.Sample
	for t := start; !t.After(end); t = t.Add(step) {
		if v, ok := value(t); ok {
			out = append(out, observability.Sample{Timestamp: t, Value: v})
		}
	}
	return out
}

// QueryRange answers one range read of the batch.
func (s *Story) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	s.Calls.Add(1)
	query := e.String()
	step := observability.StepFor(end.Sub(start))
	res := observability.PromResult{Query: query, Start: start, End: end, Step: step, Status: observability.Status{
		Kind: observability.KindPrometheus, State: observability.StateComplete, Source: "service monitoring/prometheus:9090"}}
	if s.Deny.State != "" {
		res.Status = s.Deny
		res.Kind = observability.KindPrometheus
		return res
	}
	for _, fragment := range s.Absent {
		if strings.Contains(query, fragment) {
			return res
		}
	}
	if strings.Contains(query, "hubble_drop_total") {
		// A stale-address trickle towards every Pod, and one policy burst
		// towards the calm Pod whose series only exists from the burst on.
		for _, pod := range s.pods() {
			res.Series = append(res.Series, observability.PromSeries{
				Labels:  map[string]string{"destination_pod": pod, "reason": "STALE_OR_UNROUTABLE_IP"},
				Samples: samples(start, end, step, func(time.Time) (float64, bool) { return 0.002, true })})
		}
		if !s.Quiet {
			res.Series = append(res.Series, observability.PromSeries{
				Labels: map[string]string{"destination_pod": Calm, "reason": "POLICY_DENIED"},
				Samples: samples(start, end, step, func(t time.Time) (float64, bool) {
					switch {
					case t.Before(Now.Add(-Blip[0])):
						return 0, false
					case between(t, Blip):
						return 0.8, true
					}
					return 0, true
				})})
		}
		return res
	}
	for i := range answers {
		found := true
		for _, fragment := range answers[i].has {
			found = found && strings.Contains(query, fragment)
		}
		if !found {
			continue
		}
		value := answers[i].value
		if answers[i].label == "" {
			res.Series = []observability.PromSeries{{Labels: map[string]string{},
				Samples: samples(start, end, step, func(t time.Time) (float64, bool) { return value(s, "", t), true })}}
			return res
		}
		for _, pod := range s.pods() {
			res.Series = append(res.Series, observability.PromSeries{Labels: map[string]string{answers[i].label: pod},
				Samples: samples(start, end, step, func(t time.Time) (float64, bool) { return value(s, pod, t), true })})
		}
		return res
	}
	return res
}

// Sources is the declaration of the story's context: the request path of
// the Service with its app profiles and three dependencies.
func Sources() *observability.Sources {
	return &observability.Sources{
		Prometheus: &observability.Source{Service: "monitoring/prometheus:9090"},
		RequestPath: &observability.RequestPath{Services: map[string]observability.ServicePath{
			Namespace + "/" + Service: {
				App: []string{"gitlab-webservice", "container-cpu"},
				Dependencies: []observability.Dependency{
					{Service: Namespace + "/gitlab-postgresql", Profile: "postgres-exporter"},
					{Service: Namespace + "/gitlab-redis-master", Profile: "redis-exporter"},
					{Service: Namespace + "/gitlab-gitaly", Profile: "grpc-server"},
				},
			},
		}},
	}
}

// Path is the resolved path of the story: one Ingress, three ready
// endpoints, three Pods on three nodes and the Pods of each dependency.
func Path() *slowpath.Path {
	pod := func(name, node string) slowpath.Pod {
		return slowpath.Pod{Name: name, UID: "uid-" + name, Node: node, Ready: "True", ReadySince: Now.Add(-6 * 24 * time.Hour),
			Created: Now.Add(-6 * 24 * time.Hour), Endpoint: "ready"}
	}
	outlier := pod(Outlier, "worker-f")
	outlier.ReadySince = Now.Add(-28*time.Minute - 30*time.Second)
	return &slowpath.Path{
		Selection: slowpath.Selection{Context: "ct-1-1", Kind: slowpath.KindService, Namespace: Namespace, Name: Service, UID: "uid-" + Service},
		Context:   "ct-1-1", Namespace: Namespace, Service: Service, ServiceUID: "uid-" + Service,
		Ingresses: []slowpath.Ingress{{Name: Ingress, Entry: Host + " / (Prefix)"}},
		Pods:      []slowpath.Pod{pod(Calm, "worker-d"), pod(Third, "worker-e"), outlier},
		Endpoints: 3, EndpointsReady: 3, EndpointsRead: true,
		Dependencies: map[string][]string{
			Namespace + "/gitlab-postgresql":   {"gitlab-postgresql-0"},
			Namespace + "/gitlab-redis-master": {"gitlab-redis-master-0"},
			Namespace + "/gitlab-gitaly":       {"gitlab-gitaly-0"},
		},
		Workload: impact.Workload{Kind: impact.KindDeployment, Namespace: Namespace, Name: Service},
		Rollouts: []slowpath.Event{{At: Now.Add(-6 * 24 * time.Hour), Label: "rev 1"}},
		ReadAt:   Now,
	}
}

// Report runs the story through a plan, the batch and the evaluation for
// one window that ends at Now.
func Report(story *Story, sources *observability.Sources, path *slowpath.Path, window time.Duration) *slowpath.Report {
	plan := slowpath.BuildPlan(sources, path, window, Now)
	report := slowpath.Evaluate(plan, slowpath.Collect(context.Background(), story, plan), nil, Now.Add(12*time.Second))
	report.Source = "service monitoring/prometheus:9090"
	return report
}
