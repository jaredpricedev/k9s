// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const gitlabPath = "gitlab/gitlab-webservice-default"

func gitlabSources(t *testing.T) *Sources {
	t.Helper()
	var s Sources
	require.NoError(t, yaml.Unmarshal([]byte(`
prometheus:
  service: monitoring/prometheus:9090
red:
  workloadLabel: service
requestPath:
  services:
    gitlab/gitlab-webservice-default:
      app: [gitlab-webservice, container-cpu]
      dependencies:
        - service: gitlab/gitlab-postgresql
          profile: postgres-exporter
        - service: gitlab/gitlab-redis-master
          profile: redis-exporter
          match: ['pod=~"gitlab-redis-master-.*"']
`), &s))
	require.NoError(t, s.ValidateRequestPath())
	return &s
}

func queryNamed(t *testing.T, plan *HopPlan, profile, signal string) HopQuery {
	t.Helper()
	for i := range plan.Queries {
		if plan.Queries[i].Profile == profile && plan.Queries[i].Signal.Name == signal {
			return plan.Queries[i]
		}
	}
	require.Failf(t, "signal not resolved", "%s/%s in %+v", profile, signal, plan.NotRead)
	return HopQuery{}
}

func TestResolveHopFillsFixedTemplatesForTheObject(t *testing.T) {
	s := gitlabSources(t)
	pods := []string{"web-b", "web-a"}

	app := s.ResolveHop(HopApp, gitlabPath, HopTarget{Namespace: "gitlab", Service: "gitlab-webservice-default", Pods: pods}, 2*time.Minute)
	require.Empty(t, app.NotRead)
	require.Equal(t, []string{"gitlab-webservice", "container-cpu"}, app.Profiles)
	p99 := queryNamed(t, &app, "gitlab-webservice", "p99")
	assert.Equal(t, `histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{namespace="gitlab",pod=~"web-a|web-b"}[120s])))`, p99.Expr.String())
	assert.Equal(t, `histogram_quantile(0.99, sum by (le,pod) (rate(http_request_duration_seconds_bucket{namespace="gitlab",pod=~"web-a|web-b"}[120s])))`, p99.ByPod.String())
	assert.Equal(t, "pod", p99.PodLabel)
	assert.Equal(t, `namespace="gitlab",pod=~"web-a|web-b"`, p99.Scope)
	assert.Equal(t, UnitSeconds, p99.Signal.Unit)
	assert.Equal(t, WorseHigh, p99.Signal.Worse)
	assert.False(t, p99.ClusterWide)

	free := queryNamed(t, &app, "gitlab-webservice", "free-threads")
	assert.Equal(t, `sum by (pod) (puma_pool_capacity{namespace="gitlab",pod=~"web-a|web-b"})`, free.ByPod.String())
	assert.Equal(t, WorseLow, free.Signal.Worse, "a pool at zero is the bad direction")

	throttled := queryNamed(t, &app, "container-cpu", "throttled")
	assert.Equal(t, `(sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{namespace="gitlab",pod=~"web-a|web-b",container!="",container!="POD"}[120s]))`+
		` / sum by (pod) (rate(container_cpu_cfs_periods_total{namespace="gitlab",pod=~"web-a|web-b",container!="",container!="POD"}[120s])))`, throttled.ByPod.String())

	ingress := s.ResolveHop(HopIngress, gitlabPath, HopTarget{Namespace: "gitlab", Service: "gitlab-webservice-default", Ingress: "gitlab-webservice-default"}, 2*time.Minute)
	require.Empty(t, ingress.NotRead)
	gone := queryNamed(t, &ingress, "ingress-nginx", "499")
	assert.Equal(t, `sum(rate(nginx_ingress_controller_requests{namespace="gitlab",service="gitlab-webservice-default",ingress="gitlab-webservice-default",status="499"}[120s]))`, gone.Expr.String())
	require.Error(t, gone.ByPod.Err(), "the ingress profile has no Pod label")
	errs := queryNamed(t, &ingress, "ingress-nginx", "5xx")
	assert.Contains(t, errs.Expr.String(), `status=~"5.."}[120s])) / sum(rate(nginx_ingress_controller_requests{namespace="gitlab",service="gitlab-webservice-default",ingress="gitlab-webservice-default"}[120s])))`)

	// Without an Ingress name the profile still binds by Service and says
	// which field did not narrow the query.
	bare := s.ResolveHop(HopIngress, gitlabPath, HopTarget{Namespace: "gitlab", Service: "gitlab-webservice-default"}, 2*time.Minute)
	assert.Equal(t, []string{ScopeIngress}, queryNamed(t, &bare, "ingress-nginx", "requests").Unbound)

	dns := s.ResolveHop(HopDNS, gitlabPath, HopTarget{Namespace: "gitlab"}, 2*time.Minute)
	servfail := queryNamed(t, &dns, "coredns", "servfail")
	assert.True(t, servfail.ClusterWide, "CoreDNS is read for the cluster, and says so")
	assert.Empty(t, servfail.Scope)
	assert.Equal(t, `(sum(rate(coredns_dns_responses_total{rcode="SERVFAIL"}[120s])) / sum(rate(coredns_dns_responses_total{}[120s])))`, servfail.Expr.String())

	network := s.ResolveHop(HopNetwork, gitlabPath, HopTarget{Namespace: "gitlab", Pods: pods[:1]}, 2*time.Minute)
	drops := queryNamed(t, &network, "cilium-hubble", "drops")
	assert.Equal(t, `sum by (reason) (rate(hubble_drop_total{destination_namespace="gitlab",destination_pod="web-b"}[120s]))`, drops.Expr.String())
	assert.Equal(t, `sum by (destination_pod,reason) (rate(hubble_drop_total{destination_namespace="gitlab",destination_pod="web-b"}[120s]))`, drops.ByPod.String())
	rst := queryNamed(t, &network, "cilium-hubble", "rst")
	assert.Contains(t, rst.Expr.String(), `flag="RST"}[120s])) / sum(rate(hubble_tcp_flags_total{destination_namespace="gitlab",destination_pod="web-b",flag="SYN"}[120s])))`)
}

func TestResolveHopDefaultsNeedNoDeclaration(t *testing.T) {
	for name, s := range map[string]*Sources{"nil": nil, "empty": {}} {
		plan := s.ResolveHop(HopApp, "apps/checkout-api", HopTarget{Namespace: "apps", Service: "checkout-api", Pods: []string{"checkout-api-0"}}, 5*time.Minute)
		require.Empty(t, plan.NotRead, name)
		require.Equal(t, []string{ProfileRED, "container-cpu"}, plan.Profiles, name)
		assert.Equal(t, `sum by (pod) (rate(http_requests_total{namespace="apps",pod="checkout-api-0"}[300s]))`,
			queryNamed(t, &plan, ProfileRED, "requests").ByPod.String(), name)
	}
	// The generic profile follows the red: mapping of the context.
	s := gitlabSources(t)
	plan := s.ResolveHop(HopApp, "apps/checkout-api", HopTarget{Namespace: "apps", Service: "checkout-api", Pods: []string{"checkout-api-0"}}, 5*time.Minute)
	assert.Equal(t, `(sum(rate(http_requests_total{namespace="apps",service="checkout-api",pod="checkout-api-0",code=~"5.."}[300s]))`+
		` / sum(rate(http_requests_total{namespace="apps",service="checkout-api",pod="checkout-api-0"}[300s])))`,
		queryNamed(t, &plan, ProfileRED, "errors").Expr.String())
	assert.Equal(t, 0.5, queryNamed(t, &plan, ProfileRED, "p50").Signal.Quantile)
}

func TestResolveHopEscapesNamesFromTheCluster(t *testing.T) {
	var s *Sources
	hostile := `x"} or up{a="`
	plan := s.ResolveHop(HopApp, "apps/x", HopTarget{Namespace: hostile, Pods: []string{"a.b|c", `p"q`}}, time.Minute)
	q := queryNamed(t, &plan, ProfileRED, "requests")
	assert.Equal(t, `sum(rate(http_requests_total{namespace="x\"} or up{a=\"",pod=~"a\\.b\\|c|p\"q"}[60s]))`, q.Expr.String())

	// More Pod names than one matcher may hold are left out and counted.
	many := make([]string, 0, 80)
	for i := range 80 {
		many = append(many, "gitlab-webservice-default-6f9c7d5b84-"+strings.Repeat("x", 4)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	plan = s.ResolveHop(HopApp, "gitlab/x", HopTarget{Namespace: "gitlab", Pods: many}, time.Minute)
	q = queryNamed(t, &plan, ProfileRED, "requests")
	require.NoError(t, q.Expr.Err())
	assert.Positive(t, q.PodsOmitted)
	assert.Less(t, q.PodsOmitted, 80)
}

func TestHopsThatCannotBeReadSayWhy(t *testing.T) {
	s := gitlabSources(t)
	reasons := func(plan HopPlan) string {
		var out []string
		for _, n := range plan.NotRead {
			out = append(out, n.Profile+"/"+n.Signal+": "+n.Reason)
		}
		return strings.Join(out, "\n")
	}
	tests := []struct {
		name string
		plan HopPlan
		want string
	}{
		{"app hop without Pods", s.ResolveHop(HopApp, gitlabPath, HopTarget{Namespace: "gitlab"}, time.Minute), "no Pod is known for this object"},
		{"nothing binds the scope", s.ResolveHop(HopIngress, gitlabPath, HopTarget{}, time.Minute), "the object has none of them"},
		{"dependency through ResolveHop", s.ResolveHop(HopDependency, gitlabPath, HopTarget{}, time.Minute), "ResolveDependency"},
		{"unknown hop", s.ResolveHop("storage", gitlabPath, HopTarget{}, time.Minute), "unknown hop kind"},
		{"no rate window", s.ResolveHop(HopDNS, gitlabPath, HopTarget{}, 0), "range must be between one second and seven days"},
		{"no dependency declared", s.ResolveDependencies("apps/checkout-api", nil, time.Minute)[0], "no dependency is declared for apps/checkout-api"},
		{"dependency with an unknown profile", s.ResolveDependency(Dependency{Service: "a/b", Profile: "nope"}, nil, time.Minute), `profile "nope" is not declared`},
		{"dependency with an app profile", s.ResolveDependency(Dependency{Service: "a/b", Profile: "coredns"}, nil, time.Minute), "reads the dns hop, not dependency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, tt.plan.Queries)
			assert.Contains(t, reasons(tt.plan), tt.want)
		})
	}

	off := &Sources{RequestPath: &RequestPath{Defaults: &ServicePath{Network: []string{ProfileNone}}}}
	plan := off.ResolveHop(HopNetwork, gitlabPath, HopTarget{Namespace: "gitlab", Pods: []string{"a"}}, time.Minute)
	assert.Contains(t, reasons(plan), "turned off (none)")

	broken := &Sources{RequestPath: &RequestPath{Services: map[string]ServicePath{gitlabPath: {App: []string{"missing"}}}}}
	plan = broken.ResolveHop(HopApp, gitlabPath, HopTarget{Namespace: "gitlab", Pods: []string{"a"}}, time.Minute)
	assert.Contains(t, reasons(plan), `declaration: requestPath services.gitlab/gitlab-webservice-default: profile "missing" is not declared`)
}

func TestResolveDependenciesAreExplicitServices(t *testing.T) {
	s := gitlabSources(t)
	plans := s.ResolveDependencies(gitlabPath, map[string][]string{"gitlab/gitlab-postgresql": {"gitlab-postgresql-0"}}, 2*time.Minute)
	require.Len(t, plans, 2)

	pg := plans[0]
	require.Empty(t, pg.NotRead)
	assert.Equal(t, HopTarget{Namespace: "gitlab", Service: "gitlab-postgresql", Pods: []string{"gitlab-postgresql-0"}}, pg.Target)
	assert.Equal(t, `min(pg_up{namespace="gitlab",pod="gitlab-postgresql-0"})`, queryNamed(t, &pg, "postgres-exporter", "up").Expr.String())
	assert.Equal(t, `sum(pg_stat_activity_count{namespace="gitlab",pod="gitlab-postgresql-0",state="active"})`,
		queryNamed(t, &pg, "postgres-exporter", "active").Expr.String())

	redis := plans[1]
	latency := queryNamed(t, &redis, "redis-exporter", "latency")
	assert.Equal(t, `(sum(rate(redis_commands_duration_seconds_total{namespace="gitlab",pod=~"gitlab-redis-master-.*"}[120s]))`+
		` / sum(rate(redis_commands_total{namespace="gitlab",pod=~"gitlab-redis-master-.*"}[120s])))`, latency.Expr.String())
	assert.Equal(t, []string{ScopePod}, latency.Unbound, "no Pod names were given: the namespace and the declared matcher select")
	assert.Equal(t, `namespace="gitlab",pod=~"gitlab-redis-master-.*"`, latency.Scope)
	assert.Equal(t, UnitSeconds, latency.Signal.Unit)
}

func TestProfilesExtendAndReplaceBuiltins(t *testing.T) {
	var s Sources
	require.NoError(t, yaml.Unmarshal([]byte(`
requestPath:
  defaults:
    ingress: [ingress-nginx-exported]
    app: [rails]
  profiles:
    coredns:
      extends: coredns
      match: ['job="kube-dns"']
    rails:
      extends: gitlab-webservice
      scope: {pod: kubernetes_pod_name}
      signals:
        - {name: p95, kind: quantile, metric: rails_request_seconds_bucket, quantile: 0.95}
`), &s))
	require.NoError(t, s.ValidateRequestPath())

	ingress := s.ResolveHop(HopIngress, "a/b", HopTarget{Namespace: "a", Service: "b", Ingress: "b"}, time.Minute)
	assert.Equal(t, `sum(rate(nginx_ingress_controller_requests{exported_namespace="a",exported_service="b",ingress="b"}[60s]))`,
		queryNamed(t, &ingress, "ingress-nginx-exported", "requests").Expr.String())

	dns := s.ResolveHop(HopDNS, "a/b", HopTarget{}, time.Minute)
	assert.Contains(t, queryNamed(t, &dns, "coredns", "requests").Expr.String(), `coredns_dns_requests_total{job="kube-dns"}`)

	app := s.ResolveHop(HopApp, "a/b", HopTarget{Namespace: "a", Pods: []string{"p"}}, time.Minute)
	require.Len(t, app.Queries, 1, "declared signals replace the extended ones")
	assert.Equal(t, `histogram_quantile(0.95, sum by (le,kubernetes_pod_name) (rate(rails_request_seconds_bucket{namespace="a",kubernetes_pod_name="p"}[60s])))`,
		app.Queries[0].ByPod.String())

	for _, name := range BuiltinProfileNames() {
		p, err := (*Sources)(nil).Profile(name)
		require.NoError(t, err, name)
		assert.NotEmpty(t, p.About, name)
		assert.NotEmpty(t, p.Signals, name)
	}
	assert.Equal(t, []string{"cilium-hubble", "container-cpu", "coredns", "gitlab-webservice", "grpc-server", "ingress-nginx",
		"ingress-nginx-exported", "postgres-exporter", "red", "redis-exporter"}, BuiltinProfileNames())
}

func TestRequestPathValidationRefusesWhatCannotFillATemplate(t *testing.T) {
	tests := map[string]struct{ yaml, want string }{
		"metric name":      {`profiles: {x: {hop: app, signals: [{name: a, kind: rate, metric: "up{}"}]}}`, "invalid metric name"},
		"kind":             {`profiles: {x: {hop: app, signals: [{name: a, kind: query, metric: up}]}}`, "kind must be rate, ratio, quantile or gauge"},
		"hop":              {`profiles: {x: {signals: [{name: a, kind: rate, metric: up}]}}`, "hop must be"},
		"scope key":        {`profiles: {x: {hop: app, scope: {cluster: c}, signals: [{name: a, kind: rate, metric: up}]}}`, "scope keys are"},
		"scope label":      {`profiles: {x: {hop: app, scope: {pod: "pod}"}, signals: [{name: a, kind: rate, metric: up}]}}`, "invalid label name"},
		"matcher":          {`profiles: {x: {hop: app, match: ["up or vector(1)"], signals: [{name: a, kind: rate, metric: up}]}}`, "must be written label"},
		"regex":            {`profiles: {x: {hop: app, signals: [{name: a, kind: rate, metric: up, match: ['code=~"5(("']}]}}`, "not a regular expression"},
		"ratio of itself":  {`profiles: {x: {hop: app, signals: [{name: a, kind: ratio, metric: up}]}}`, "a ratio needs a divisor"},
		"quantile range":   {`profiles: {x: {hop: app, signals: [{name: a, kind: quantile, metric: up, quantile: 2}]}}`, "quantile is between 0 and 1"},
		"duplicate signal": {`profiles: {x: {hop: app, signals: [{name: a, kind: rate, metric: up}, {name: a, kind: rate, metric: up}]}}`, "declared twice"},
		"no signal":        {`profiles: {x: {hop: app}}`, "at least one signal"},
		"extends loop":     {`profiles: {x: {extends: y}, y: {extends: x}}`, "forms a loop"},
		"profile name":     {`profiles: {"X Y": {hop: app, signals: [{name: a, kind: rate, metric: up}]}}`, "profile name"},
		"service key":      {`services: {webservice: {app: [red]}}`, "must be namespace/name"},
		"wrong hop":        {`services: {a/b: {ingress: [coredns]}}`, `reads the dns hop, not ingress`},
		"none with others": {`services: {a/b: {dns: [none, coredns]}}`, "none must be the only entry"},
		"dependency ref":   {`services: {a/b: {dependencies: [{service: postgres, profile: postgres-exporter}]}}`, "must be namespace/name"},
		"default deps":     {`defaults: {dependencies: [{service: a/b, profile: postgres-exporter}]}`, "belong to one service"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var r RequestPath
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &r))
			err := (&Sources{RequestPath: &r}).ValidateRequestPath()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	require.NoError(t, (*Sources)(nil).ValidateRequestPath())
	require.NoError(t, (&Sources{}).ValidateRequestPath())
}

func TestParseMatcher(t *testing.T) {
	tests := []struct {
		in   string
		want Matcher
		bad  bool
	}{
		{in: `status="499"`, want: Matcher{Label: "status", Op: MatchEqual, Value: "499"}},
		{in: `status=~"5.."`, want: Matcher{Label: "status", Op: MatchRegex, Value: "5.."}},
		{in: ` rcode != "No Error" `, want: Matcher{Label: "rcode", Op: MatchNotEqual, Value: "No Error"}},
		{in: `container!=""`, want: Matcher{Label: "container", Op: MatchNotEqual, Value: ""}},
		{in: `job=kube-dns`, want: Matcher{Label: "job", Op: MatchEqual, Value: "kube-dns"}},
		{in: `path!~"/-/.*"`, want: Matcher{Label: "path", Op: MatchNotRegex, Value: "/-/.*"}},
		{in: `q="a\"b"`, want: Matcher{Label: "q", Op: MatchEqual, Value: `a"b`}},
		{in: `status`, bad: true},
		{in: `status="499`, bad: true},
		{in: `status=4 9`, bad: true},
		{in: `1status="499"`, bad: true},
		{in: `status=~"("`, bad: true},
		{in: `a="b",c="d"`, bad: true},
	}
	for _, tt := range tests {
		got, err := ParseMatcher(tt.in)
		if tt.bad {
			require.Error(t, err, tt.in)
			continue
		}
		require.NoError(t, err, tt.in)
		assert.Equal(t, tt.want, got, tt.in)
	}
}

func TestMissingSeparatesNoAnswerFromNoSeries(t *testing.T) {
	plan := (*Sources)(nil).ResolveHop(HopNetwork, "gitlab/x", HopTarget{Namespace: "gitlab", Pods: []string{"a"}}, time.Minute)
	q := queryNamed(t, &plan, "cilium-hubble", "drops")

	n, missing := q.Missing(nil)
	require.True(t, missing)
	assert.Equal(t, NotRead{Hop: HopNetwork, Profile: "cilium-hubble", Signal: "drops", Reason: "not asked"}, n)

	n, missing = q.Missing(&PromResult{Status: Status{Kind: KindPrometheus, State: StateDenied, Reason: "HTTP 403"}})
	require.True(t, missing)
	assert.Equal(t, "prometheus: denied (HTTP 403)", n.Reason)

	n, missing = q.Missing(&PromResult{Status: Status{Kind: KindPrometheus, State: StateComplete}})
	require.True(t, missing, "an answer without series is not a healthy reading")
	assert.Equal(t, `no series matched hubble_drop_total with destination_namespace="gitlab",destination_pod="a": the metric is not scraped, or its labels differ from the cilium-hubble profile`, n.Reason)

	_, missing = q.Missing(&PromResult{Status: Status{Kind: KindPrometheus, State: StateComplete},
		Series: []PromSeries{{Samples: []Sample{{Value: 0}}}}})
	assert.False(t, missing, "a zero sample is a reading")
}

func TestAuthoredExprIsBoundedAndRefusesLeftoverVariables(t *testing.T) {
	ok := AuthoredExpr(` sum by (pod) (rate(http_requests_total{namespace="gitlab"}[2m])) `)
	require.NoError(t, ok.Err())
	assert.Equal(t, `sum by (pod) (rate(http_requests_total{namespace="gitlab"}[2m]))`, ok.String())
	for name, text := range map[string]string{
		"empty":    "  ",
		"variable": `rate(x{namespace="$namespace"}[$__rate_interval])`,
		"control":  "up\x00",
		"too long": strings.Repeat("a", MaxAuthoredQuery+1),
	} {
		require.Error(t, AuthoredExpr(text).Err(), name)
		assert.Empty(t, AuthoredExpr(text).String(), name)
	}
}
