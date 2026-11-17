// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
)

func kube(host string) RESTConfigFunc {
	return func() (*rest.Config, error) { return &rest.Config{Host: host, BearerToken: "KUBE-TOKEN"}, nil }
}

func TestDeclarationParsesValidatesAndNeverCarriesCredentials(t *testing.T) {
	var s Sources
	require.NoError(t, yaml.Unmarshal([]byte(`
prometheus:
  service: monitoring/prometheus:9090
alertmanager:
  url: https://alerts.example.test/am/
  tokenEnv: AM_TOKEN
grafana:
  service: monitoring/https:grafana:http
  publicURL: https://grafana.example.test/g
  datasource: prom-main
  dashboards:
    pod: {uid: k8s-pod, namespaceVar: namespace, podVar: pod}
`), &s))
	for _, kind := range Kinds {
		require.NoError(t, s.Validate(kind), kind)
	}
	require.Equal(t, "service monitoring/prometheus:9090", s.Prometheus.Display())
	require.Equal(t, "url https://alerts.example.test/am", s.Alertmanager.Display())
	ref, err := ParseServiceRef(s.Grafana.Service)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/namespaces/monitoring/services/https:grafana:http/proxy", ref.ProxyPath())

	link, err := s.Grafana.DashboardLink(DashboardPod, LinkTarget{Namespace: "apps", Pod: "a&b=c"}, time.UnixMilli(1000), time.UnixMilli(2000))
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/g/d/k8s-pod?from=1000&to=2000&var-namespace=apps&var-pod=a%26b%3Dc", link)
	_, err = s.Grafana.DashboardLink(DashboardNode, LinkTarget{Node: "n"}, time.Time{}, time.Time{})
	require.ErrorContains(t, err, "no grafana dashboard is declared for node")

	for name, bad := range map[string]Source{
		"both":            {Service: "a/b:1", URL: "https://x.test"},
		"neither":         {},
		"userinfo":        {URL: "https://user:SECRET@x.test"},
		"query":           {URL: "https://x.test?token=SECRET"},
		"scheme":          {URL: "file:///etc/passwd"},
		"service token":   {Service: "a/b:1", TokenEnv: "T"},
		"token over http": {URL: "http://x.test", TokenEnv: "T"},
		"token name":      {URL: "https://x.test", TokenEnv: "not a name"},
		"service shape":   {Service: "prometheus:9090"},
		"service path":    {Service: "a/../b:1"},
		"service scheme":  {Service: "a/ftp:b:1"},
	} {
		err := bad.Validate()
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), "SECRET", name)
	}
	require.NoError(t, (&Source{URL: "http://127.0.0.1:9090", TokenEnv: "T"}).Validate())
	require.ErrorIs(t, (&Sources{}).Validate(KindGrafana), ErrNotConfigured)
	require.Error(t, (&GrafanaSource{Source: Source{Service: "a/b:1"}, Dashboards: map[string]Dashboard{"cluster": {UID: "x"}}}).Validate())
	require.Error(t, (&GrafanaSource{Source: Source{Service: "a/b:1"}, Dashboards: map[string]Dashboard{"pod": {UID: "../x"}}}).Validate())
}

func TestResolveContactsNothingAndReportsDistinctReasons(t *testing.T) {
	var contacted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Add(1) }))
	defer server.Close()

	c, st := Resolve(nil, KindPrometheus, kube(server.URL))
	require.Nil(t, c)
	require.Equal(t, StateNotConfigured, st.State)
	c, st = Resolve(&Sources{Alertmanager: &Source{Service: "monitoring/am:9093"}}, KindPrometheus, kube(server.URL))
	require.Nil(t, c)
	require.Equal(t, StateNotConfigured, st.State)
	c, st = Resolve(&Sources{Prometheus: &Source{Service: "bad"}}, KindPrometheus, kube(server.URL))
	require.Nil(t, c)
	require.Equal(t, StateUnavailable, st.State)
	require.Contains(t, st.Reason, "namespace/name:port")
	c, st = Resolve(&Sources{Prometheus: &Source{Service: "monitoring/prometheus:9090"}}, KindPrometheus, nil)
	require.Nil(t, c)
	require.Equal(t, "no Kubernetes connection", st.Reason)
	c, st = Resolve(&Sources{Prometheus: &Source{Service: "monitoring/prometheus:9090"}}, KindPrometheus, kube(server.URL))
	require.NotNil(t, c)
	require.Equal(t, StateDeclared, st.State)
	require.True(t, st.ObservedAt.IsZero())
	require.Equal(t, "service monitoring/prometheus:9090", c.Display())
	require.Zero(t, contacted.Load())
}

func TestServiceSourceUsesTheAPIServerProxyWithKubeCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/base/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/api/v1/status/buildinfo", r.URL.Path)
		assert.Equal(t, "Bearer KUBE-TOKEN", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"status":"success"}`)
	}))
	defer server.Close()
	c, _ := Resolve(&Sources{Prometheus: &Source{Service: "monitoring/prometheus:9090"}}, KindPrometheus, kube(server.URL+"/base/"))
	require.NotNil(t, c)
	st := c.Probe(t.Context())
	require.Equal(t, StateComplete, st.State, st.Reason)
	require.False(t, st.ObservedAt.IsZero())
	require.Equal(t, "prometheus: complete", st.String())
}

func TestURLSourceSendsOnlyTheNamedEnvironmentTokenAndRedactsIt(t *testing.T) {
	t.Setenv("K9PLUS_TEST_GRAFANA_TOKEN", "glsa_SECRET")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer glsa_SECRET", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "glsa_SECRET is not permitted")
	}))
	defer server.Close()
	s := &Sources{Grafana: &GrafanaSource{Source: Source{URL: server.URL, TokenEnv: "K9PLUS_TEST_GRAFANA_TOKEN"}}}
	c, st := Resolve(s, KindGrafana, nil)
	require.NotNil(t, c, st.Reason)
	defer c.Close()
	require.Same(t, s.Grafana, c.Grafana())
	body, st := c.Get(t.Context(), "/api/health", nil)
	require.Nil(t, body)
	require.Equal(t, StateDenied, st.State)
	require.Equal(t, "HTTP 403", st.Reason)
	require.NotContains(t, fmt.Sprintf("%+v %+v", st, *c), "glsa_SECRET")

	t.Setenv("K9PLUS_TEST_GRAFANA_TOKEN", "")
	_, st = c.Get(t.Context(), "/api/health", nil)
	require.Equal(t, StateUnavailable, st.State)
	require.Equal(t, "environment variable K9PLUS_TEST_GRAFANA_TOKEN is not set", st.Reason)
	require.Zero(t, st.HTTPStatus)
}

func TestGetStatesAreDistinctBoundedAndNeverFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "{}")
		case "/huge":
			fmt.Fprint(w, strings.Repeat("x", MaxBytes+1))
		case "/slow":
			<-r.Context().Done()
		case "/redirect":
			w.Header().Set("Location", other.URL)
			w.WriteHeader(http.StatusFound)
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/login":
			w.WriteHeader(http.StatusUnauthorized)
		case "/gateway":
			w.WriteHeader(http.StatusGatewayTimeout)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "SECRET stack trace")
		}
	}))
	defer server.Close()
	c, err := NewURLClient(KindPrometheus, server.URL, "")
	require.NoError(t, err)
	defer c.Close()
	for path, want := range map[string]State{"/ok": StateComplete, "/huge": StateIncomplete, "/redirect": StateUnavailable, "/gone": StateAbsent,
		"/login": StateDenied, "/gateway": StateTimeout, "/boom": StateUnavailable, "relative": StateUnavailable, "/a/../b": StateUnavailable} {
		body, st := c.Get(t.Context(), path, nil)
		require.Equal(t, want, st.State, path)
		require.Equal(t, want == StateComplete, body != nil, path)
		require.NotContains(t, st.Reason, "SECRET")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	_, st := c.Get(ctx, "/slow", nil)
	require.Equal(t, StateTimeout, st.State)
	ctx, stop := context.WithCancel(t.Context())
	stop()
	_, st = c.Get(ctx, "/ok", nil)
	require.Equal(t, StateUnavailable, st.State)
	require.Equal(t, "canceled", st.Reason)
	require.Zero(t, elsewhere.Load())
}

func TestExpressionsEscapeValuesAndRefuseRawText(t *testing.T) {
	hostile := `x"} or vector(1) #` + "\n"
	sel := Select("http_requests_total", Eq("namespace", "apps"), Eq("pod", hostile), Re("code", "5.."))
	require.NoError(t, sel.Err())
	require.Equal(t, `http_requests_total{namespace="apps",pod="x\"} or vector(1) #\n",code=~"5.."}`, sel.String())
	require.Equal(t, `checkout\.api-.*`, RegexQuote("checkout.api")+"-.*")

	ratio := Div(Sum(Rate(sel, 5*time.Minute)), Sum(Rate(Select("http_requests_total", Eq("namespace", "apps")), 5*time.Minute)))
	require.Equal(t, `(sum(rate(http_requests_total{namespace="apps",pod="x\"} or vector(1) #\n",code=~"5.."}[300s])) / sum(rate(http_requests_total{namespace="apps"}[300s])))`, ratio.String())
	p95 := HistogramQuantile(0.95, Sum(Rate(Select("http_request_duration_seconds_bucket", Eq("service", "checkout-api")), 5*time.Minute), "le", "pod"))
	require.Equal(t, `histogram_quantile(0.95, sum by (le,pod) (rate(http_request_duration_seconds_bucket{service="checkout-api"}[300s])))`, p95.String())
	require.Equal(t, `topk(5, max_over_time(up{}[60s]))`, TopK(5, OverTime("max", Select("up"), time.Minute)).String())
	require.Equal(t, `(up{} * 100)`, Binary("*", Select("up"), Scalar(100)).String())

	for name, bad := range map[string]Expr{
		"metric":        Select(`up{} or vector(1)`),
		"label":         Select("up", Eq(`a="b",c`, "d")),
		"operator":      Select("up", Matcher{Label: "a", Op: "=~~", Value: "b"}),
		"aggregation":   Aggregate("sum) or (vector(1)", Select("up")),
		"by label":      Sum(Select("up"), "a) (b"),
		"rate of rate":  Rate(Rate(Select("up"), time.Minute), time.Minute),
		"rate of sum":   Rate(Sum(Select("up")), time.Minute),
		"range":         Rate(Select("up"), 0),
		"binary":        Binary("or", Select("up"), Select("up")),
		"over time":     OverTime("quantile", Select("up"), time.Minute),
		"quantile":      HistogramQuantile(2, Select("up")),
		"topk":          TopK(1000, Select("up")),
		"zero value":    {},
		"carried error": Sum(Select("")),
	} {
		require.Error(t, bad.Err(), name)
		require.Empty(t, bad.String(), name)
	}
	require.Equal(t, 15*time.Second, StepFor(15*time.Minute))
	require.Equal(t, 30*time.Second, StepFor(time.Hour))
	require.Equal(t, 15*time.Minute, StepFor(24*time.Hour))
	require.Equal(t, 2*time.Hour, StepFor(MaxWindow))
}

func TestPrometheusQueriesKeepLimitsAndSayWhenTheyCut(t *testing.T) {
	var got string
	body := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"pod":"a","bad label":"x","note":"line\nbreak"},"values":[[1000,"1"],[1015,"NaN"],[900000,"3"],[1030,"2"],[1030,"9"]]}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path + "?" + r.URL.Query().Encode()
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	c, err := NewURLClient(KindPrometheus, server.URL, "")
	require.NoError(t, err)
	defer c.Close()
	start := time.Unix(1000, 0)
	res := c.QueryRange(t.Context(), Select("up", Eq("pod", "a")), start, start.Add(time.Hour))
	require.Equal(t, StateComplete, res.State, res.Reason)
	require.Equal(t, "/api/v1/query_range?end=4600.000&query=up%7Bpod%3D%22a%22%7D&start=1000.000&step=30", got)
	require.Equal(t, 30*time.Second, res.Step)
	require.Len(t, res.Series, 1)
	require.Equal(t, map[string]string{"pod": "a", "note": "line break"}, res.Series[0].Labels)
	require.Len(t, res.Series[0].Samples, 2)
	require.Equal(t, 3, res.Rejected)
	last, ok := res.Series[0].Last()
	require.True(t, ok)
	require.InDelta(t, 2, last.Value, 0)

	one := `{"metric":{"pod":"a"},"values":[[1000,"1"]]}`
	body = `{"status":"success","data":{"resultType":"matrix","result":[` + strings.TrimSuffix(strings.Repeat(one+",", MaxSeries+3), ",") + `]}}`
	res = c.QueryRange(t.Context(), Select("up"), start, start.Add(time.Hour))
	require.Equal(t, StateIncomplete, res.State)
	require.Len(t, res.Series, MaxSeries)
	require.Equal(t, 3, res.NotRetained)
	require.Contains(t, res.Reason, "3 series not retained")

	body = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"a"},"value":[1000,"0.25"]}]}}`
	instant := c.Query(t.Context(), Select("up"), start)
	require.Equal(t, StateComplete, instant.State, instant.Reason)
	require.Contains(t, got, "/api/v1/query?")
	require.InDelta(t, 0.25, instant.Series[0].Samples[0].Value, 0)
	body = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	empty := c.Query(t.Context(), Select("up"), start)
	require.Equal(t, StateComplete, empty.State)
	require.Empty(t, empty.Series)

	body = `{"status":"error","error":"SECRET"}`
	failed := c.Query(t.Context(), Select("up"), start)
	require.Equal(t, StateUnavailable, failed.State)
	require.NotContains(t, failed.Reason, "SECRET")

	got = ""
	for _, refused := range []PromResult{
		c.Query(t.Context(), Select("bad name"), start),
		c.QueryRange(t.Context(), Select("up"), start, start),
		c.QueryRange(t.Context(), Select("up"), start, start.Add(MaxWindow+time.Second)),
		c.Query(t.Context(), Expr{}, start),
	} {
		require.Equal(t, StateUnavailable, refused.State)
		require.NotEmpty(t, refused.Reason)
	}
	require.Empty(t, got, "a refused query must not reach the source")
	am, _ := NewURLClient(KindAlertmanager, server.URL, "")
	require.Equal(t, "not a prometheus source", am.Query(t.Context(), Select("up"), start).Reason)
}

func TestGrafanaDatasourceServesPrometheusWhenNoneIsDeclared(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/namespaces/monitoring/services/grafana:3000/proxy/api/datasources/proxy/uid/prom-main/api/v1/query", r.URL.Path)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"scalar","result":[1000,"1"]}}`)
	}))
	defer server.Close()
	c, st := Resolve(&Sources{Grafana: &GrafanaSource{Source: Source{Service: "monitoring/grafana:3000"}, Datasource: "prom-main"}}, KindPrometheus, kube(server.URL))
	require.NotNil(t, c, st.Reason)
	require.Equal(t, KindPrometheus, c.Kind())
	require.Equal(t, "service monitoring/grafana:3000 (grafana datasource prom-main)", c.Display())
	res := c.Query(t.Context(), Scalar(1), time.Unix(1000, 0))
	require.Equal(t, StateComplete, res.State, res.Reason)
	require.Len(t, res.Series, 1)
}

func TestAlertsKeepSuppressionAndBoundText(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/alerts", r.URL.Path)
		query = r.URL.RawQuery
		fmt.Fprintf(w, `[{"fingerprint":"f1","labels":{"alertname":"KubePodCrashLooping","severity":"warning","namespace":"apps","pod":"p"},
"annotations":{"summary":"Pod is crash\u001b[31m looping","runbook_url":"https://runbooks.example.test/x","description":%q},
"startsAt":"2026-01-01T00:00:00Z","endsAt":"2026-01-01T00:10:00Z","updatedAt":"2026-01-01T00:05:00Z","generatorURL":"http://user:SECRET@prom/graph",
"receivers":[{"name":"oncall"}],"status":{"state":"active","silencedBy":[],"inhibitedBy":[]}},
{"fingerprint":"f2","labels":{"alertname":"Watchdog"},"annotations":{},"status":{"state":"suppressed","silencedBy":["s1"],"inhibitedBy":["f1"]}}]`,
			strings.Repeat("d", 5000))
	}))
	defer server.Close()
	c, err := NewURLClient(KindAlertmanager, server.URL, "")
	require.NoError(t, err)
	defer c.Close()
	res := c.Alerts(t.Context(), AlertQuery{Matchers: []Matcher{Eq("namespace", `ap"ps`)}, HideInhibited: true})
	require.Equal(t, StateComplete, res.State, res.Reason)
	require.Equal(t, "active=true&filter=namespace%3D%22ap%5C%22ps%22&inhibited=false&silenced=true&unprocessed=false", query)
	require.Len(t, res.Alerts, 2)
	a := res.Alerts[0]
	require.Equal(t, "KubePodCrashLooping", a.Name())
	require.Equal(t, AlertActive, a.State)
	require.Equal(t, "Pod is crash [31m looping", a.Annotations["summary"])
	require.LessOrEqual(t, len([]rune(a.Annotations["description"])), maxText+1)
	require.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), a.StartsAt)
	require.Equal(t, []string{"oncall"}, a.Receivers)
	require.NotContains(t, fmt.Sprintf("%+v", res), "SECRET")
	require.True(t, res.Alerts[1].Silenced())
	require.True(t, res.Alerts[1].Inhibited())
	require.False(t, a.Silenced())

	refused := c.Alerts(t.Context(), AlertQuery{Matchers: []Matcher{{Label: "bad label", Value: "x"}}})
	require.Equal(t, StateUnavailable, refused.State)
	require.Contains(t, refused.Reason, "filter refused")
}

func TestGrafanaAnnotationsAndSearchAreBoundedAndOrdered(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Path + "?" + r.URL.RawQuery
		switch r.URL.Path {
		case "/api/annotations":
			fmt.Fprint(w, `[{"id":2,"time":2000,"alertName":"High","newState":"alerting","prevState":"ok","text":"High alerting","tags":["alert"],"login":"someone","email":"a@b.test"},
{"id":1,"time":1000,"timeEnd":1500,"text":"Deploy v2","tags":["deploy"],"dashboardUID":"k8s-pod","panelId":3}]`)
		case "/api/search":
			fmt.Fprint(w, `[{"uid":"k8s-pod","title":"Pod","url":"/d/k8s-pod/pod","folderTitle":"Kubernetes","tags":["k8s"]},{"uid":"bad uid/..","title":"x"}]`)
		}
	}))
	defer server.Close()
	c, err := NewURLClient(KindGrafana, server.URL, "")
	require.NoError(t, err)
	defer c.Close()
	res := c.Annotations(t.Context(), AnnotationQuery{From: time.UnixMilli(500), To: time.UnixMilli(3000), Tags: []string{"deploy", "alert"}})
	require.Equal(t, StateComplete, res.State, res.Reason)
	require.Equal(t, "/api/annotations?from=500&limit=200&matchAny=true&tags=deploy&tags=alert&to=3000", query)
	require.Len(t, res.Annotations, 2)
	require.Equal(t, "Deploy v2", res.Annotations[0].Text)
	require.Equal(t, time.UnixMilli(1500).UTC(), res.Annotations[0].TimeEnd)
	require.Equal(t, "alerting", res.Annotations[1].NewState)
	require.True(t, res.Annotations[1].TimeEnd.IsZero())
	require.NotContains(t, fmt.Sprintf("%+v", res), "a@b.test")

	limited := c.Annotations(t.Context(), AnnotationQuery{From: time.UnixMilli(500), To: time.UnixMilli(3000), Limit: 2})
	require.Equal(t, StateIncomplete, limited.State)
	require.Equal(t, StateUnavailable, c.Annotations(t.Context(), AnnotationQuery{}).State)

	hits := c.SearchDashboards(t.Context(), "pod", []string{"k8s"}, 0)
	require.Equal(t, StateComplete, hits.State, hits.Reason)
	require.Equal(t, "/api/search?limit=100&query=pod&tag=k8s&type=dash-db", query)
	require.Equal(t, []DashboardHit{{UID: "k8s-pod", Title: "Pod", FolderTitle: "Kubernetes", Path: "/d/k8s-pod/pod", Tags: []string{"k8s"}}}, hits.Dashboards)
}

func TestSuggestionsAreNameMatchesWithTheExactSnippet(t *testing.T) {
	got := Suggest([]ServicePort{
		{Namespace: "monitoring", Name: "grafana", Ports: []int32{80, 3000}},
		{Namespace: "apps", Name: "checkout-api", Ports: []int32{8080}},
		{Namespace: "monitoring", Name: "alertmanager-operated", Ports: []int32{9094, 9093}},
		{Namespace: "monitoring", Name: "kube-prometheus-stack-prometheus", Ports: []int32{8080}},
		{Namespace: "monitoring", Name: "prometheus-operated"},
	})
	require.Equal(t, []Suggestion{
		{Kind: KindPrometheus, Service: "monitoring/kube-prometheus-stack-prometheus:8080", Snippet: "prometheus:\n  service: monitoring/kube-prometheus-stack-prometheus:8080"},
		{Kind: KindAlertmanager, Service: "monitoring/alertmanager-operated:9093", Snippet: "alertmanager:\n  service: monitoring/alertmanager-operated:9093"},
		{Kind: KindGrafana, Service: "monitoring/grafana:3000", Snippet: "grafana:\n  service: monitoring/grafana:3000"},
	}, got)
	for _, s := range got {
		var parsed map[string]Source
		require.NoError(t, yaml.Unmarshal([]byte(s.Snippet), &parsed))
		src := parsed[string(s.Kind)]
		require.NoError(t, src.Validate())
	}
}

func TestHistoryCollectReadsThroughADeclaredSource(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/api/v1/query_range", r.URL.Path)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"namespace":"apps","pod":"api","container":"web"},"values":[[1000,"1"],[1015,"2"]]}]}}`)
	}))
	defer server.Close()
	c, _ := Resolve(&Sources{Prometheus: &Source{Service: "monitoring/prometheus:9090"}}, KindPrometheus, kube(server.URL))
	require.NotNil(t, c)
	r := request("")
	r.Source = c
	s, err := Collect(t.Context(), r)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "service monitoring/prometheus:9090", s.Request.URL)
	require.Len(t, s.Evidence[0].Series[0].Samples, 2)
	am, _ := NewURLClient(KindAlertmanager, server.URL, "")
	r.Source = am
	require.ErrorContains(t, Validate(&r), "prometheus source")
}
