// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HopKind names one stretch of the path a request takes to a Service.
type HopKind string

const (
	// HopIngress is the ingress controller in front of the Service.
	HopIngress HopKind = "ingress"
	// HopApp is the Service's own Pods.
	HopApp HopKind = "app"
	// HopDependency is another Service the Pods call (a database, a cache).
	HopDependency HopKind = "dependency"
	// HopDNS is the cluster DNS the Pods resolve names with.
	HopDNS HopKind = "dns"
	// HopNetwork is the packet path to the Pods.
	HopNetwork HopKind = "network"
)

// HopKinds lists every hop kind in the order a request meets them.
var HopKinds = []HopKind{HopIngress, HopApp, HopDependency, HopDNS, HopNetwork}

// Signal kinds: the fixed query shape a signal fills.
const (
	// SignalRate is sum(rate(metric[w])): events per second.
	SignalRate = "rate"
	// SignalRatio is sum(rate(metric[w])) / sum(rate(divisor[w])): a share
	// such as errors per request, or a mean such as seconds per command.
	SignalRatio = "ratio"
	// SignalQuantile is histogram_quantile(q, sum by (le) (rate(metric[w]))).
	SignalQuantile = "quantile"
	// SignalGauge is aggregate(metric): a level such as queued connections.
	SignalGauge = "gauge"
)

// Signal units. A view maps them onto its own formatting.
const (
	UnitSeconds   = "seconds"
	UnitRatio     = "ratio"
	UnitPerSecond = "perSecond"
	UnitCount     = "count"
	UnitBytes     = "bytes"
	UnitCores     = "cores"
)

// Which direction of a signal is the bad one, for peer comparison.
const (
	WorseHigh = "high"
	WorseLow  = "low"
	WorseNone = "none"
)

// Scope fields: the Kubernetes names a profile can pin a query to.
const (
	ScopeNamespace = "namespace"
	ScopeService   = "service"
	ScopeIngress   = "ingress"
	ScopePod       = "pod"
	ScopeNode      = "node"
)

var scopeFields = []string{ScopeNamespace, ScopeService, ScopeIngress, ScopePod, ScopeNode}

// Gauge aggregations.
const (
	aggregateSum = "sum"
	aggregateMax = "max"
	aggregateMin = "min"
	aggregateAvg = "avg"
)

// ProfileNone in a hop's profile list means the hop is deliberately not read.
const ProfileNone = "none"

// ProfileRED is the generic HTTP profile built from the red: mapping.
const ProfileRED = "red"

// Limits on a request-path declaration and on what one resolution returns.
const (
	MaxProfiles       = 32
	MaxSignals        = 16
	MaxDependencies   = 16
	MaxHopProfiles    = 4
	MaxPathServices   = 64
	maxPodMatcherSize = 1000 // a Matcher value may hold 1024 bytes
	maxExtends        = 4
)

// Signal is one named reading of a hop: metric names and label matchers
// that fill a fixed query shape. It can never carry query text.
type Signal struct {
	// Name is the short identifier shown in a view, for example p99.
	Name string `json:"name" yaml:"name"`
	// Title says in plain words what is measured.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
	// Kind is rate, ratio, quantile or gauge.
	Kind string `json:"kind" yaml:"kind"`
	// Metric is the metric name; for a quantile, the _bucket series.
	Metric string `json:"metric" yaml:"metric"`
	// Match adds label matchers, each written label="value", label!="value",
	// label=~"regex" or label!~"regex".
	Match []string `json:"match,omitempty" yaml:"match,omitempty"`
	// Divisor is the denominator metric of a ratio. Empty means Metric,
	// which with DivisorMatch gives "the share of Metric that matches".
	Divisor string `json:"divisor,omitempty" yaml:"divisor,omitempty"`
	// DivisorMatch adds label matchers to the denominator only.
	DivisorMatch []string `json:"divisorMatch,omitempty" yaml:"divisorMatch,omitempty"`
	// Quantile is between 0 and 1; 0 means 0.99.
	Quantile float64 `json:"quantile,omitempty" yaml:"quantile,omitempty"`
	// Aggregate folds a gauge: sum (default), max, min or avg.
	Aggregate string `json:"aggregate,omitempty" yaml:"aggregate,omitempty"`
	// By keeps extra labels apart, for example reason on a drop counter.
	By []string `json:"by,omitempty" yaml:"by,omitempty"`
	// Unit is seconds, ratio, perSecond, count, bytes or cores.
	Unit string `json:"unit,omitempty" yaml:"unit,omitempty"`
	// Worse says which direction is the bad one: high (default), low or none.
	Worse string `json:"worse,omitempty" yaml:"worse,omitempty"`
}

// Profile maps the signals of one hop kind onto the metric and label names
// one exporter uses.
type Profile struct {
	// Extends names a profile to start from. Scope entries and Match are
	// added to it; Signals, when given, replace its signals.
	Extends string `json:"extends,omitempty" yaml:"extends,omitempty"`
	// Hop is the hop kind the profile reads.
	Hop HopKind `json:"hop,omitempty" yaml:"hop,omitempty"`
	// Scope says which label carries which Kubernetes name: keys are
	// namespace, service, ingress, pod and node; values are label names. A
	// profile without a scope reads cluster-wide series (cluster DNS).
	Scope map[string]string `json:"scope,omitempty" yaml:"scope,omitempty"`
	// Match adds label matchers to every signal.
	Match []string `json:"match,omitempty" yaml:"match,omitempty"`
	// Signals are the readings.
	Signals []Signal `json:"signals,omitempty" yaml:"signals,omitempty"`
	// About describes a built-in profile; it is not read from configuration.
	About string `json:"-" yaml:"-"`
}

// Dependency is another Service the workload calls, named explicitly.
type Dependency struct {
	// Service is namespace/name.
	Service string `json:"service" yaml:"service"`
	// Profile names the profile that reads it.
	Profile string `json:"profile" yaml:"profile"`
	// Match adds label matchers, for a namespace that holds more than one
	// instance of the exporter.
	Match []string `json:"match,omitempty" yaml:"match,omitempty"`
}

// ServicePath says which profiles read each hop of one Service's request
// path. An absent hop keeps the default; the single entry none turns the
// hop off.
type ServicePath struct {
	Ingress      []string     `json:"ingress,omitempty" yaml:"ingress,omitempty"`
	App          []string     `json:"app,omitempty" yaml:"app,omitempty"`
	DNS          []string     `json:"dns,omitempty" yaml:"dns,omitempty"`
	Network      []string     `json:"network,omitempty" yaml:"network,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
}

// RequestPath is the optional per-context declaration stored under
// k9s.observability.requestPath. Declaring it contacts nothing.
type RequestPath struct {
	// Defaults applies to every Service that has no entry in Services.
	Defaults *ServicePath `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	// Services is keyed by namespace/name of the Service.
	Services map[string]ServicePath `json:"services,omitempty" yaml:"services,omitempty"`
	// Profiles adds profiles; a name that equals a built-in replaces it.
	Profiles map[string]Profile `json:"profiles,omitempty" yaml:"profiles,omitempty"`
}

// DefaultHopProfiles are the profiles a hop is read with when nothing is
// declared. A dependency has no default: it must be named.
var DefaultHopProfiles = map[HopKind][]string{
	HopIngress: {"ingress-nginx"},
	HopApp:     {ProfileRED, "container-cpu"},
	HopDNS:     {"coredns"},
	HopNetwork: {"cilium-hubble"},
}

var builtinProfiles = map[string]Profile{
	"ingress-nginx": {
		Hop:   HopIngress,
		About: "ingress-nginx controller metrics, selected by the Ingress and backend Service labels",
		Scope: map[string]string{ScopeNamespace: "namespace", ScopeIngress: "ingress", ScopeService: "service"},
		Signals: []Signal{
			{Name: "requests", Title: "requests through the ingress", Kind: SignalRate, Metric: "nginx_ingress_controller_requests", Unit: UnitPerSecond, Worse: WorseNone},
			{Name: "499", Title: "HTTP 499: the client closed the request while waiting", Kind: SignalRate, Metric: "nginx_ingress_controller_requests",
				Match: []string{`status="499"`}, Unit: UnitPerSecond},
			{Name: "5xx", Title: "share of requests answered with 5xx", Kind: SignalRatio, Metric: "nginx_ingress_controller_requests",
				Match: []string{`status=~"5.."`}, Unit: UnitRatio},
			{Name: "p99", Title: "request duration at the ingress, 99th percentile", Kind: SignalQuantile,
				Metric: "nginx_ingress_controller_request_duration_seconds_bucket", Unit: UnitSeconds},
			{Name: "upstream-p99", Title: "time the backend took to answer, 99th percentile", Kind: SignalQuantile,
				Metric: "nginx_ingress_controller_response_duration_seconds_bucket", Unit: UnitSeconds},
		},
	},
	"ingress-nginx-exported": {
		Extends: "ingress-nginx",
		About:   "ingress-nginx where Prometheus renamed the controller's own labels to exported_namespace and exported_service",
		Scope:   map[string]string{ScopeNamespace: "exported_namespace", ScopeService: "exported_service"},
	},
	"gitlab-webservice": {
		Hop:   HopApp,
		About: "GitLab webservice Pods: the Rails request histogram and the Puma thread pool",
		Scope: map[string]string{ScopeNamespace: "namespace", ScopePod: "pod"},
		Signals: []Signal{
			{Name: "p99", Title: "Rails request duration, 99th percentile", Kind: SignalQuantile, Metric: "http_request_duration_seconds_bucket", Unit: UnitSeconds},
			{Name: "requests", Title: "requests finished by Rails", Kind: SignalRate, Metric: "http_requests_total", Unit: UnitPerSecond, Worse: WorseNone},
			{Name: "5xx", Title: "share of requests Rails answered with 5xx", Kind: SignalRatio, Metric: "http_requests_total",
				Match: []string{`status=~"5.."`}, Unit: UnitRatio},
			{Name: "queued", Title: "connections waiting for a free Puma thread", Kind: SignalGauge, Metric: "puma_queued_connections", Unit: UnitCount},
			{Name: "free-threads", Title: "Puma threads free to take a request", Kind: SignalGauge, Metric: "puma_pool_capacity", Unit: UnitCount, Worse: WorseLow},
			{Name: "busy-threads", Title: "Puma threads working on a request", Kind: SignalGauge, Metric: "puma_active_connections", Unit: UnitCount},
			{Name: "threads", Title: "Puma threads configured", Kind: SignalGauge, Metric: "puma_max_threads", Unit: UnitCount, Worse: WorseNone},
		},
	},
	"container-cpu": {
		Hop:   HopApp,
		About: "cAdvisor CPU use and CFS throttling of the Pods' containers",
		Scope: map[string]string{ScopeNamespace: "namespace", ScopePod: "pod"},
		Match: []string{`container!=""`, `container!="POD"`},
		Signals: []Signal{
			{Name: "throttled", Title: "share of CPU periods in which a container was throttled at its limit", Kind: SignalRatio,
				Metric: "container_cpu_cfs_throttled_periods_total", Divisor: "container_cpu_cfs_periods_total", Unit: UnitRatio},
			{Name: "cpu", Title: "CPU cores used", Kind: SignalRate, Metric: "container_cpu_usage_seconds_total", Unit: UnitCores, Worse: WorseNone},
		},
	},
	"coredns": {
		Hop:   HopDNS,
		About: "CoreDNS server metrics for the whole cluster DNS, not for one client",
		Signals: []Signal{
			{Name: "p99", Title: "DNS request duration at CoreDNS, 99th percentile", Kind: SignalQuantile,
				Metric: "coredns_dns_request_duration_seconds_bucket", Unit: UnitSeconds},
			{Name: "servfail", Title: "share of DNS answers that were SERVFAIL", Kind: SignalRatio, Metric: "coredns_dns_responses_total",
				Match: []string{`rcode="SERVFAIL"`}, Unit: UnitRatio},
			{Name: "nxdomain", Title: "share of DNS answers that were NXDOMAIN", Kind: SignalRatio, Metric: "coredns_dns_responses_total",
				Match: []string{`rcode="NXDOMAIN"`}, Unit: UnitRatio, Worse: WorseNone},
			{Name: "requests", Title: "DNS requests answered", Kind: SignalRate, Metric: "coredns_dns_requests_total", Unit: UnitPerSecond, Worse: WorseNone},
		},
	},
	"cilium-hubble": {
		Hop: HopNetwork,
		About: "Hubble metrics with labelsContext=source_namespace,source_pod,destination_namespace,destination_pod, " +
			"selected by the destination Pod",
		Scope: map[string]string{ScopeNamespace: "destination_namespace", ScopePod: "destination_pod"},
		Signals: []Signal{
			{Name: "drops", Title: "packets dropped on the way to the Pods, by reason", Kind: SignalRate, Metric: "hubble_drop_total",
				By: []string{"reason"}, Unit: UnitPerSecond},
			{Name: "rst", Title: "TCP resets per new connection (RST per SYN)", Kind: SignalRatio, Metric: "hubble_tcp_flags_total",
				Match: []string{`flag="RST"`}, DivisorMatch: []string{`flag="SYN"`}, Unit: UnitRatio},
			{Name: "dns-errors", Title: "share of DNS answers to the Pods that were an error", Kind: SignalRatio, Metric: "hubble_dns_responses_total",
				Match: []string{`rcode!="No Error"`, `rcode!="Non-Existent Domain"`}, Unit: UnitRatio},
			{Name: "flows", Title: "flows Hubble processed towards the Pods", Kind: SignalRate, Metric: "hubble_flows_processed_total",
				Unit: UnitPerSecond, Worse: WorseNone},
		},
	},
	"postgres-exporter": {
		Hop:   HopDependency,
		About: "postgres_exporter: availability and connections in use",
		Scope: map[string]string{ScopeNamespace: "namespace", ScopePod: "pod"},
		Signals: []Signal{
			{Name: "up", Title: "the exporter reaches PostgreSQL (1 is yes)", Kind: SignalGauge, Metric: "pg_up", Aggregate: aggregateMin, Unit: UnitCount, Worse: WorseLow},
			{Name: "connections", Title: "connections open to PostgreSQL", Kind: SignalGauge, Metric: "pg_stat_activity_count", Unit: UnitCount},
			{Name: "active", Title: "connections running a statement", Kind: SignalGauge, Metric: "pg_stat_activity_count",
				Match: []string{`state="active"`}, Unit: UnitCount},
			{Name: "max-connections", Title: "connection limit of the server", Kind: SignalGauge, Metric: "pg_settings_max_connections",
				Aggregate: aggregateMin, Unit: UnitCount, Worse: WorseNone},
		},
	},
	"redis-exporter": {
		Hop:   HopDependency,
		About: "redis_exporter: availability, clients and mean command duration",
		Scope: map[string]string{ScopeNamespace: "namespace", ScopePod: "pod"},
		Signals: []Signal{
			{Name: "up", Title: "the exporter reaches Redis (1 is yes)", Kind: SignalGauge, Metric: "redis_up", Aggregate: aggregateMin, Unit: UnitCount, Worse: WorseLow},
			{Name: "latency", Title: "mean time Redis spent per command", Kind: SignalRatio, Metric: "redis_commands_duration_seconds_total",
				Divisor: "redis_commands_total", Unit: UnitSeconds},
			{Name: "clients", Title: "clients connected to Redis", Kind: SignalGauge, Metric: "redis_connected_clients", Unit: UnitCount},
		},
	},
	"grpc-server": {
		Hop:   HopDependency,
		About: "go-grpc-prometheus server metrics, as Gitaly exposes them",
		Scope: map[string]string{ScopeNamespace: "namespace", ScopePod: "pod"},
		Signals: []Signal{
			{Name: "p99", Title: "gRPC handling time, 99th percentile", Kind: SignalQuantile, Metric: "grpc_server_handling_seconds_bucket", Unit: UnitSeconds},
			{Name: "errors", Title: "share of gRPC calls that did not end OK", Kind: SignalRatio, Metric: "grpc_server_handled_total",
				Match: []string{`grpc_code!="OK"`}, Unit: UnitRatio},
			{Name: "requests", Title: "gRPC calls handled", Kind: SignalRate, Metric: "grpc_server_handled_total", Unit: UnitPerSecond, Worse: WorseNone},
		},
	},
}

// BuiltinProfileNames lists the built-in profiles, including red, sorted.
func BuiltinProfileNames() []string {
	names := []string{ProfileRED}
	for name := range builtinProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// redProfile is the generic HTTP profile: the red: mapping of the context
// (or its defaults) read per Pod.
func redProfile(red *RED) Profile {
	m := red.WithDefaults()
	scope := map[string]string{ScopeNamespace: m.NamespaceLabel, ScopePod: m.PodLabel}
	if m.WorkloadLabel != "" {
		scope[ScopeService] = m.WorkloadLabel
	}
	return Profile{
		Hop:   HopApp,
		About: "generic HTTP request, error and duration series, named by the red: mapping",
		Scope: scope,
		Signals: []Signal{
			{Name: "p99", Title: "request duration, 99th percentile", Kind: SignalQuantile, Metric: m.DurationBuckets, Unit: UnitSeconds},
			{Name: "p50", Title: "request duration, median", Kind: SignalQuantile, Metric: m.DurationBuckets, Quantile: 0.5, Unit: UnitSeconds},
			{Name: "requests", Title: "requests answered", Kind: SignalRate, Metric: m.Requests, Unit: UnitPerSecond, Worse: WorseNone},
			{Name: "errors", Title: "share of requests answered with an error status", Kind: SignalRatio, Metric: m.Requests,
				Match: []string{m.CodeLabel + "=~" + strconv.Quote(m.ErrorCodes)}, Unit: UnitRatio},
		},
	}
}

var (
	profileName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$`)
	signalName  = regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9_.]{0,30}[a-zA-Z0-9])?$`)
	matcherText = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*(.*?)\s*$`)
	servicePath = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

// ParseMatcher parses label="value", label!="value", label=~"regex" or
// label!~"regex". The quotes may be left out for a value without spaces.
// A regex must compile and is fully anchored by Prometheus.
func ParseMatcher(text string) (Matcher, error) {
	bad := fmt.Errorf("matcher %q must be written label=\"value\" (operators = != =~ !~)", Clean(text, 80))
	if len(text) > 300 {
		return Matcher{}, bad
	}
	parts := matcherText.FindStringSubmatch(text)
	if parts == nil {
		return Matcher{}, bad
	}
	value := parts[3]
	if strings.HasPrefix(value, `"`) {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return Matcher{}, bad
		}
		value = unquoted
	} else if strings.ContainsAny(value, "\" \t,{}") {
		return Matcher{}, bad
	}
	m := Matcher{Label: parts[1], Op: MatchOp(parts[2]), Value: value}
	if m.Op == MatchRegex || m.Op == MatchNotRegex {
		if _, err := regexp.Compile(value); err != nil {
			return Matcher{}, fmt.Errorf("matcher %q: the value is not a regular expression", Clean(text, 80))
		}
	}
	return m, nil
}

func parseMatchers(texts []string) ([]Matcher, error) {
	if len(texts) > maxLabels {
		return nil, fmt.Errorf("more than %d matchers", maxLabels)
	}
	out := make([]Matcher, 0, len(texts))
	for _, text := range texts {
		m, err := ParseMatcher(text)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// profile returns a named profile with extends resolved: a declared profile
// first, then the red profile, then a built-in.
func (s *Sources) profile(name string, depth int) (Profile, error) {
	if depth > maxExtends {
		return Profile{}, fmt.Errorf("profile %q: extends is nested more than %d deep or forms a loop", Clean(name, 64), maxExtends)
	}
	var (
		p     Profile
		found bool
	)
	if s != nil && s.RequestPath != nil {
		p, found = s.RequestPath.Profiles[name]
	}
	if !found && name == ProfileRED {
		var red *RED
		if s != nil {
			red = s.RED
		}
		if err := red.Validate(); err != nil {
			return Profile{}, err
		}
		return redProfile(red), nil
	}
	if !found {
		p, found = builtinProfiles[name]
	}
	if !found {
		return Profile{}, fmt.Errorf("profile %q is not declared and is not a built-in (%s)", Clean(name, 64), strings.Join(BuiltinProfileNames(), ", "))
	}
	if p.Extends == "" {
		return p, nil
	}
	if p.Extends == name {
		// A declared profile may extend the built-in it replaces.
		base, ok := builtinProfiles[name]
		if !ok {
			return Profile{}, fmt.Errorf("profile %q extends itself", Clean(name, 64))
		}
		return mergeProfile(&base, &p), nil
	}
	base, err := s.profile(p.Extends, depth+1)
	if err != nil {
		return Profile{}, err
	}
	return mergeProfile(&base, &p), nil
}

func mergeProfile(base, over *Profile) Profile {
	out := Profile{Hop: base.Hop, About: base.About, Signals: base.Signals}
	if over.Hop != "" {
		out.Hop = over.Hop
	}
	if over.About != "" {
		out.About = over.About
	}
	out.Scope = make(map[string]string, len(base.Scope)+len(over.Scope))
	for k, v := range base.Scope {
		out.Scope[k] = v
	}
	for k, v := range over.Scope {
		out.Scope[k] = v
	}
	out.Match = append(append([]string(nil), base.Match...), over.Match...)
	if len(over.Signals) > 0 {
		out.Signals = over.Signals
	}
	return out
}

// Profile returns a profile by name with extends resolved, or why it cannot
// be used. It contacts nothing.
func (s *Sources) Profile(name string) (Profile, error) {
	p, err := s.profile(name, 0)
	if err != nil {
		return Profile{}, err
	}
	if err := validateProfile(name, &p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func validHop(h HopKind) bool {
	for _, k := range HopKinds {
		if k == h {
			return true
		}
	}
	return false
}

func validateProfile(name string, p *Profile) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("requestPath profile %q: %s", Clean(name, 64), fmt.Sprintf(format, args...))
	}
	if !validHop(p.Hop) {
		return fail("hop must be ingress, app, dependency, dns or network")
	}
	for field, label := range p.Scope {
		known := false
		for _, f := range scopeFields {
			known = known || f == field
		}
		if !known {
			return fail("scope keys are namespace, service, ingress, pod and node")
		}
		if !labelName.MatchString(label) {
			return fail("scope %s: invalid label name %q", field, Clean(label, 80))
		}
	}
	if _, err := parseMatchers(p.Match); err != nil {
		return fail("%s", err.Error())
	}
	if len(p.Signals) == 0 {
		return fail("declare at least one signal")
	}
	if len(p.Signals) > MaxSignals {
		return fail("more than %d signals", MaxSignals)
	}
	seen := make(map[string]bool, len(p.Signals))
	for i := range p.Signals {
		sig := &p.Signals[i]
		if !signalName.MatchString(sig.Name) {
			return fail("signal name %q must be a short identifier", Clean(sig.Name, 40))
		}
		if seen[sig.Name] {
			return fail("signal %q is declared twice", sig.Name)
		}
		seen[sig.Name] = true
		if err := validateSignal(sig); err != nil {
			return fail("signal %q: %s", sig.Name, err.Error())
		}
	}
	return nil
}

func validateSignal(sig *Signal) error {
	switch sig.Kind {
	case SignalRate, SignalRatio, SignalQuantile, SignalGauge:
	default:
		return errors.New("kind must be rate, ratio, quantile or gauge")
	}
	if !metricName.MatchString(sig.Metric) {
		return fmt.Errorf("invalid metric name %q", Clean(sig.Metric, 80))
	}
	if sig.Divisor != "" && !metricName.MatchString(sig.Divisor) {
		return fmt.Errorf("invalid divisor metric name %q", Clean(sig.Divisor, 80))
	}
	if sig.Kind != SignalRatio && (sig.Divisor != "" || len(sig.DivisorMatch) > 0) {
		return errors.New("divisor and divisorMatch apply to a ratio only")
	}
	if sig.Kind == SignalRatio && sig.Divisor == "" && len(sig.Match) == 0 && len(sig.DivisorMatch) == 0 {
		return errors.New("a ratio needs a divisor metric or matchers that select a part of the metric")
	}
	if sig.Quantile != 0 && (sig.Kind != SignalQuantile || sig.Quantile < 0 || sig.Quantile > 1) {
		return errors.New("quantile is between 0 and 1 and applies to a quantile only")
	}
	switch sig.Aggregate {
	case "", aggregateSum, aggregateMax, aggregateMin, aggregateAvg:
	default:
		return errors.New("aggregate must be sum, max, min or avg")
	}
	if sig.Aggregate != "" && sig.Kind != SignalGauge {
		return errors.New("aggregate applies to a gauge only")
	}
	switch sig.Unit {
	case "", UnitSeconds, UnitRatio, UnitPerSecond, UnitCount, UnitBytes, UnitCores:
	default:
		return errors.New("unit must be seconds, ratio, perSecond, count, bytes or cores")
	}
	switch sig.Worse {
	case "", WorseHigh, WorseLow, WorseNone:
	default:
		return errors.New("worse must be high, low or none")
	}
	if len(sig.By) > 4 {
		return errors.New("by lists at most four labels")
	}
	for _, l := range sig.By {
		if !labelName.MatchString(l) || l == "le" {
			return fmt.Errorf("by: invalid label name %q", Clean(l, 80))
		}
	}
	if len(sig.Title) > 200 {
		return errors.New("title exceeds 200 characters")
	}
	if _, err := parseMatchers(sig.Match); err != nil {
		return err
	}
	_, err := parseMatchers(sig.DivisorMatch)
	return err
}

// ValidateRequestPath reports why the request-path declaration cannot be
// used, or nil. A context without one is valid.
func (s *Sources) ValidateRequestPath() error {
	if s == nil || s.RequestPath == nil {
		return nil
	}
	r := s.RequestPath
	if len(r.Profiles) > MaxProfiles {
		return fmt.Errorf("requestPath: more than %d profiles", MaxProfiles)
	}
	if len(r.Services) > MaxPathServices {
		return fmt.Errorf("requestPath: more than %d services", MaxPathServices)
	}
	names := make([]string, 0, len(r.Profiles))
	for name := range r.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !profileName.MatchString(name) || name == ProfileNone {
			return fmt.Errorf("requestPath: profile name %q must be lower-case letters, digits and dashes", Clean(name, 64))
		}
		if _, err := s.Profile(name); err != nil {
			return err
		}
	}
	check := func(where string, p *ServicePath) error {
		for _, hop := range []struct {
			kind  HopKind
			names []string
		}{{HopIngress, p.Ingress}, {HopApp, p.App}, {HopDNS, p.DNS}, {HopNetwork, p.Network}} {
			if len(hop.names) > MaxHopProfiles {
				return fmt.Errorf("requestPath %s: more than %d profiles for the %s hop", where, MaxHopProfiles, hop.kind)
			}
			for _, name := range hop.names {
				if name == ProfileNone {
					if len(hop.names) != 1 {
						return fmt.Errorf("requestPath %s: none must be the only entry of the %s hop", where, hop.kind)
					}
					continue
				}
				profile, err := s.Profile(name)
				if err != nil {
					return fmt.Errorf("requestPath %s: %w", where, err)
				}
				if profile.Hop != hop.kind {
					return fmt.Errorf("requestPath %s: profile %q reads the %s hop, not %s", where, Clean(name, 64), profile.Hop, hop.kind)
				}
			}
		}
		if len(p.Dependencies) > MaxDependencies {
			return fmt.Errorf("requestPath %s: more than %d dependencies", where, MaxDependencies)
		}
		for _, dep := range p.Dependencies {
			if !servicePath.MatchString(dep.Service) {
				return fmt.Errorf("requestPath %s: dependency service %q must be namespace/name", where, Clean(dep.Service, 80))
			}
			profile, err := s.Profile(dep.Profile)
			if err != nil {
				return fmt.Errorf("requestPath %s: dependency %s: %w", where, dep.Service, err)
			}
			if profile.Hop != HopDependency {
				return fmt.Errorf("requestPath %s: dependency %s: profile %q reads the %s hop, not dependency", where, dep.Service, Clean(dep.Profile, 64), profile.Hop)
			}
			if _, err := parseMatchers(dep.Match); err != nil {
				return fmt.Errorf("requestPath %s: dependency %s: %w", where, dep.Service, err)
			}
		}
		return nil
	}
	if r.Defaults != nil {
		if len(r.Defaults.Dependencies) > 0 {
			return errors.New("requestPath defaults: dependencies belong to one service, not to the defaults")
		}
		if err := check("defaults", r.Defaults); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(r.Services))
	for key := range r.Services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !servicePath.MatchString(key) {
			return fmt.Errorf("requestPath: services key %q must be namespace/name", Clean(key, 80))
		}
		p := r.Services[key]
		if err := check("services."+key, &p); err != nil {
			return err
		}
	}
	return nil
}

// servicePathFor returns the declaration for a Service (namespace/name):
// its own entry with absent hops filled from Defaults.
func (s *Sources) servicePathFor(path string) ServicePath {
	var out ServicePath
	if s == nil || s.RequestPath == nil {
		return out
	}
	if own, ok := s.RequestPath.Services[path]; ok {
		out = own
	}
	if d := s.RequestPath.Defaults; d != nil {
		if out.Ingress == nil {
			out.Ingress = d.Ingress
		}
		if out.App == nil {
			out.App = d.App
		}
		if out.DNS == nil {
			out.DNS = d.DNS
		}
		if out.Network == nil {
			out.Network = d.Network
		}
	}
	return out
}

// HopProfiles returns the profile names that read one hop of the request
// path of a Service (path is namespace/name): the declaration, else the
// defaults. It is empty for a hop turned off with none and for the
// dependency hop, which is read per declared Dependency.
func (s *Sources) HopProfiles(hop HopKind, path string) []string {
	p := s.servicePathFor(path)
	var names []string
	switch hop {
	case HopIngress:
		names = p.Ingress
	case HopApp:
		names = p.App
	case HopDNS:
		names = p.DNS
	case HopNetwork:
		names = p.Network
	default:
		return nil
	}
	if len(names) == 0 {
		names = DefaultHopProfiles[hop]
	}
	if len(names) == 1 && names[0] == ProfileNone {
		return nil
	}
	return append([]string(nil), names...)
}

// PathDependencies returns the dependencies declared for a Service.
func (s *Sources) PathDependencies(path string) []Dependency {
	deps := s.servicePathFor(path).Dependencies
	if len(deps) > MaxDependencies {
		deps = deps[:MaxDependencies]
	}
	return append([]Dependency(nil), deps...)
}

// HopTarget names the Kubernetes objects one hop is read for. An empty
// field is unknown and is never guessed.
type HopTarget struct {
	Namespace string
	Service   string
	Ingress   string
	Pods      []string
	Node      string
}

// HopQuery is one signal of one hop, resolved for one object: fixed query
// templates filled with escaped matchers. Nothing has been asked yet.
type HopQuery struct {
	Hop     HopKind
	Profile string
	// Signal is the declaration with Quantile, Aggregate, Unit and Worse
	// defaults filled in.
	Signal Signal
	// Expr answers for the whole target: one series, or one per
	// combination of the Signal.By labels.
	Expr Expr
	// ByPod answers per Pod (and Signal.By). ByPod.Err() is not nil when
	// the profile does not say which label carries the Pod name.
	ByPod Expr
	// PodLabel is the label that carries the Pod name in ByPod answers.
	PodLabel string
	// Scope is the matcher text that pins the query to the target, for
	// display: namespace="gitlab",pod=~"a|b". Empty when ClusterWide.
	Scope string
	// Unbound lists scope fields the profile declares for which the target
	// has no name; they did not narrow the query.
	Unbound []string
	// ClusterWide is true when nothing pins the query to the target.
	ClusterWide bool
	// PodsOmitted counts Pod names left out to keep the matcher bounded.
	PodsOmitted int
}

// NotRead says why a hop, a profile or one signal was not read. It is the
// honest alternative to drawing nothing: missing data is not health.
type NotRead struct {
	Hop     HopKind
	Profile string // empty when the whole hop was not read
	Signal  string // empty when the whole profile was not read
	Reason  string
}

// HopPlan is everything resolved for one hop of one object.
type HopPlan struct {
	Hop      HopKind
	Target   HopTarget
	Profiles []string
	Queries  []HopQuery
	NotRead  []NotRead

	rate time.Duration
}

// ResolveHop returns the query templates that apply to one hop (ingress,
// app, dns or network) for one object. path is the namespace/name of the
// Service whose request path is read and selects the declaration; target
// names the objects of this hop. rateWindow is the range rates are taken
// over. Resolving contacts nothing; what cannot be resolved is listed in
// NotRead with the reason. For dependencies use ResolveDependency.
//
//nolint:gocritic // The target is copied into the plan, which outlives the call.
func (s *Sources) ResolveHop(hop HopKind, path string, target HopTarget, rateWindow time.Duration) HopPlan {
	plan := HopPlan{Hop: hop, Target: target, rate: rateWindow}
	if hop == HopDependency {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: hop, Reason: "a dependency is read per declared Service; see ResolveDependency"})
		return plan
	}
	if !validHop(hop) {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: hop, Reason: "unknown hop kind"})
		return plan
	}
	if err := s.ValidateRequestPath(); err != nil {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: hop, Reason: "declaration: " + err.Error()})
		return plan
	}
	plan.Profiles = s.HopProfiles(hop, path)
	if len(plan.Profiles) == 0 {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: hop, Reason: "the " + string(hop) + " hop is turned off (none) for " + Clean(path, 300)})
		return plan
	}
	for _, name := range plan.Profiles {
		s.resolveProfile(&plan, name, nil)
	}
	return plan
}

// ResolveDependency returns the query templates for one declared
// dependency. pods optionally names the Pods behind the dependency Service
// (for example from its EndpointSlices); without them the profile reads
// every matching series in the dependency's namespace, and says so in
// HopQuery.Unbound.
func (s *Sources) ResolveDependency(dep Dependency, pods []string, rateWindow time.Duration) HopPlan {
	ns, name, _ := strings.Cut(dep.Service, "/")
	plan := HopPlan{Hop: HopDependency, Target: HopTarget{Namespace: ns, Service: name, Pods: pods}, Profiles: []string{dep.Profile}, rate: rateWindow}
	if !servicePath.MatchString(dep.Service) {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: HopDependency, Reason: "dependency service must be namespace/name"})
		return plan
	}
	extra, err := parseMatchers(dep.Match)
	if err != nil {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: HopDependency, Profile: dep.Profile, Reason: "declaration: " + err.Error()})
		return plan
	}
	s.resolveProfile(&plan, dep.Profile, extra)
	return plan
}

// ResolveDependencies resolves every dependency declared for a Service
// (path is namespace/name). pods optionally maps a dependency's
// namespace/name to its Pod names. A Service without a declared dependency
// yields one plan whose NotRead says so: a dependency is never guessed.
func (s *Sources) ResolveDependencies(path string, pods map[string][]string, rateWindow time.Duration) []HopPlan {
	if err := s.ValidateRequestPath(); err != nil {
		return []HopPlan{{Hop: HopDependency, NotRead: []NotRead{{Hop: HopDependency, Reason: "declaration: " + err.Error()}}}}
	}
	deps := s.PathDependencies(path)
	if len(deps) == 0 {
		return []HopPlan{{Hop: HopDependency, NotRead: []NotRead{{Hop: HopDependency,
			Reason: "no dependency is declared for " + Clean(path, 300) + " (requestPath.services.<namespace/name>.dependencies)"}}}}
	}
	out := make([]HopPlan, 0, len(deps))
	for _, dep := range deps {
		out = append(out, s.ResolveDependency(dep, pods[dep.Service], rateWindow))
	}
	return out
}

func targetValue(t *HopTarget, field string) string {
	switch field {
	case ScopeNamespace:
		return t.Namespace
	case ScopeService:
		return t.Service
	case ScopeIngress:
		return t.Ingress
	case ScopeNode:
		return t.Node
	}
	return ""
}

// podMatcher matches exactly the named Pods and reports how many names
// were left out to keep the matcher within the builder's value limit.
func podMatcher(label string, pods []string) (m Matcher, omitted int) {
	names := append([]string(nil), pods...)
	sort.Strings(names)
	var parts []string
	size := 0
	for _, pod := range names {
		quoted := RegexQuote(pod)
		if size+len(quoted)+1 > maxPodMatcherSize {
			break
		}
		parts = append(parts, quoted)
		size += len(quoted) + 1
	}
	if len(parts) == 1 {
		return Eq(label, names[0]), len(names) - 1
	}
	return Re(label, strings.Join(parts, "|")), len(names) - len(parts)
}

func (s *Sources) resolveProfile(plan *HopPlan, name string, extra []Matcher) {
	skip := func(signal, reason string) {
		plan.NotRead = append(plan.NotRead, NotRead{Hop: plan.Hop, Profile: name, Signal: signal, Reason: reason})
	}
	profile, err := s.Profile(name)
	if err != nil {
		skip("", err.Error())
		return
	}
	if profile.Hop != plan.Hop {
		skip("", fmt.Sprintf("profile reads the %s hop, not %s", profile.Hop, plan.Hop))
		return
	}
	var (
		scope    []Matcher
		unbound  []string
		omitted  int
		podLabel = profile.Scope[ScopePod]
	)
	for _, field := range scopeFields {
		label, declared := profile.Scope[field]
		if !declared {
			continue
		}
		if field == ScopePod {
			if len(plan.Target.Pods) == 0 {
				unbound = append(unbound, field)
				continue
			}
			var m Matcher
			m, omitted = podMatcher(label, plan.Target.Pods)
			scope = append(scope, m)
			continue
		}
		if value := targetValue(&plan.Target, field); value != "" {
			scope = append(scope, Eq(label, value))
		} else {
			unbound = append(unbound, field)
		}
	}
	switch {
	case len(profile.Scope) > 0 && len(scope) == 0:
		skip("", "the profile selects by "+strings.Join(unbound, ", ")+" and the object has none of them; reading every series would not be about this object")
		return
	case podLabel != "" && len(plan.Target.Pods) == 0 && (plan.Hop == HopApp || plan.Hop == HopNetwork):
		skip("", "no Pod is known for this object, and the "+string(plan.Hop)+" hop is read per Pod")
		return
	}
	static, err := parseMatchers(profile.Match)
	if err != nil {
		skip("", err.Error())
		return
	}
	base := append(append(append([]Matcher(nil), scope...), static...), extra...)
	scopeText, _ := matchers(append(append([]Matcher(nil), scope...), extra...))
	rate := plan.rate
	for i := range profile.Signals {
		sig := profile.Signals[i]
		fillSignal(&sig)
		q := HopQuery{Hop: plan.Hop, Profile: name, Signal: sig, PodLabel: podLabel, Scope: scopeText,
			Unbound: unbound, ClusterWide: len(scope) == 0 && len(extra) == 0, PodsOmitted: omitted}
		q.Expr, err = signalExpr(&sig, base, rate, sig.By)
		if err != nil {
			skip(sig.Name, err.Error())
			continue
		}
		if podLabel == "" {
			q.ByPod = invalid("profile %q does not say which label carries the Pod name", name)
		} else if q.ByPod, err = signalExpr(&sig, base, rate, append([]string{podLabel}, sig.By...)); err != nil {
			skip(sig.Name, err.Error())
			continue
		}
		plan.Queries = append(plan.Queries, q)
	}
}

func fillSignal(sig *Signal) {
	if sig.Kind == SignalQuantile && sig.Quantile == 0 {
		sig.Quantile = 0.99
	}
	if sig.Kind == SignalGauge && sig.Aggregate == "" {
		sig.Aggregate = aggregateSum
	}
	if sig.Worse == "" {
		sig.Worse = WorseHigh
	}
	if sig.Unit == "" {
		switch sig.Kind {
		case SignalRate:
			sig.Unit = UnitPerSecond
		case SignalRatio:
			sig.Unit = UnitRatio
		case SignalQuantile:
			sig.Unit = UnitSeconds
		default:
			sig.Unit = UnitCount
		}
	}
	if sig.Title == "" {
		sig.Title = sig.Name
	}
}

// signalExpr fills the fixed template of a signal kind. Every matcher value
// goes through the builder's string escaping.
func signalExpr(sig *Signal, base []Matcher, rate time.Duration, by []string) (Expr, error) {
	own, err := parseMatchers(sig.Match)
	if err != nil {
		return Expr{}, err
	}
	all := append(append([]Matcher(nil), base...), own...)
	var e Expr
	switch sig.Kind {
	case SignalRate:
		e = Sum(Rate(Select(sig.Metric, all...), rate), by...)
	case SignalGauge:
		e = Aggregate(sig.Aggregate, Select(sig.Metric, all...), by...)
	case SignalQuantile:
		e = HistogramQuantile(sig.Quantile, Sum(Rate(Select(sig.Metric, all...), rate), append([]string{"le"}, by...)...))
	case SignalRatio:
		divisorOwn, err := parseMatchers(sig.DivisorMatch)
		if err != nil {
			return Expr{}, err
		}
		divisor := sig.Divisor
		if divisor == "" {
			divisor = sig.Metric
		}
		below := append(append([]Matcher(nil), base...), divisorOwn...)
		e = Div(Sum(Rate(Select(sig.Metric, all...), rate), by...), Sum(Rate(Select(divisor, below...), rate), by...))
	default:
		return Expr{}, errors.New("kind must be rate, ratio, quantile or gauge")
	}
	if err := e.Err(); err != nil {
		return Expr{}, err
	}
	return e, nil
}

// Missing reports why an answer to this query is not a reading: the source
// did not answer, or it answered and matched no series. It returns false
// when the answer holds at least one sample.
func (q *HopQuery) Missing(res *PromResult) (NotRead, bool) {
	out := NotRead{Hop: q.Hop, Profile: q.Profile, Signal: q.Signal.Name}
	switch {
	case res == nil || res.State == "":
		out.Reason = "not asked"
	case !res.OK():
		out.Reason = res.Status.String()
	default:
		for i := range res.Series {
			if len(res.Series[i].Samples) > 0 {
				return NotRead{}, false
			}
		}
		where := "cluster-wide"
		if q.Scope != "" {
			where = "with " + q.Scope
		}
		out.Reason = "no series matched " + q.Signal.Metric + " " + where + ": the metric is not scraped, or its labels differ from the " + q.Profile + " profile"
	}
	return out, true
}
