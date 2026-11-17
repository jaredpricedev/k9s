// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func linkGrafana() *GrafanaSource {
	return &GrafanaSource{Source: Source{URL: "https://grafana.internal.test", TokenEnv: "GRAFANA_LINK_TOKEN"},
		PublicURL: "https://grafana.example.test/team/", Datasource: "prom-main", LokiDatasource: "loki-main",
		Dashboards: map[string]Dashboard{
			DashboardPod:       {UID: "k8s-pod", NamespaceVar: "namespace", PodVar: "pod"},
			DashboardWorkload:  {UID: "k8s-workload", NamespaceVar: "namespace", WorkloadVar: "workload"},
			DashboardNode:      {UID: "k8s-node", NodeVar: "instance"},
			DashboardNamespace: {UID: "k8s-namespace", NamespaceVar: "namespace"},
		}}
}

func TestDashboardURLFillsVariablesEscapesValuesAndSetsTheRange(t *testing.T) {
	t.Setenv("GRAFANA_LINK_TOKEN", "SECRET-TOKEN-VALUE")
	g := linkGrafana()
	from, to := time.UnixMilli(1_700_000_000_000), time.UnixMilli(1_700_000_600_000)

	// Absolute range, base path kept, trailing slash dropped, keys sorted.
	link, err := g.DashboardURL(DashboardPod, LinkTarget{Namespace: "apps", Pod: "api-0"}, LinkRange{From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/team/d/k8s-pod?from=1700000000000&to=1700000600000&var-namespace=apps&var-pod=api-0", link)

	// A live range ends at Grafana's now; To is ignored.
	link, err = g.DashboardURL(DashboardWorkload, LinkTarget{Namespace: "apps", Workload: "checkout-api"}, LinkRange{From: from, To: to, Live: true})
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/team/d/k8s-workload?from=1700000000000&to=now&var-namespace=apps&var-workload=checkout-api", link)

	// Every value is escaped; none can add a parameter or a fragment.
	hostile := `a b&from=0#frag?x=1"'<>/é%`
	link, err = g.DashboardURL(DashboardNode, LinkTarget{Node: hostile}, LinkRange{From: from, Live: true})
	require.NoError(t, err)
	require.NotContains(t, link, " ")
	require.NotContains(t, link, "+", "spaces are %20 so every reader agrees")
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Empty(t, parsed.Fragment)
	require.Equal(t, "/team/d/k8s-node", parsed.Path)
	require.Equal(t, url.Values{"from": {"1700000000000"}, "to": {"now"}, "var-instance": {hostile}}, parsed.Query())

	// A variable without a value is left to the dashboard's default, and a
	// name the dashboard does not declare is never sent.
	link, err = g.DashboardURL(DashboardPod, LinkTarget{Namespace: "apps", Node: "worker-b", Workload: "w"}, LinkRange{})
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/team/d/k8s-pod?var-namespace=apps", link)

	// No usable range: none is sent rather than a wrong one.
	for name, r := range map[string]LinkRange{"zero": {}, "to only": {To: to}, "to before from": {From: to, To: from}, "from only": {From: from}} {
		link, err = g.DashboardURL(DashboardNamespace, LinkTarget{Namespace: "apps"}, r)
		require.NoError(t, err, name)
		require.Equal(t, "https://grafana.example.test/team/d/k8s-namespace?var-namespace=apps", link, name)
	}

	// The older entry point builds the same link.
	old, err := g.DashboardLink(DashboardPod, LinkTarget{Namespace: "apps", Pod: "api-0"}, from, to)
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/team/d/k8s-pod?from=1700000000000&to=1700000600000&var-namespace=apps&var-pod=api-0", old)

	filled, empty := g.Dashboards[DashboardPod].Filled(LinkTarget{Namespace: "apps"})
	require.Equal(t, []string{"namespace=apps"}, filled)
	require.Equal(t, []string{"pod"}, empty)
}

func TestLinksStateWhatIsMissingAndNeverCarryACredential(t *testing.T) {
	t.Setenv("GRAFANA_LINK_TOKEN", "SECRET-TOKEN-VALUE")
	g := linkGrafana()
	r := LinkRange{From: time.UnixMilli(1000), Live: true}

	var none *GrafanaSource
	_, err := none.DashboardURL(DashboardPod, LinkTarget{}, r)
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = none.ExploreMetricsURL(Select("up"), r)
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = none.ExploreLogsURL([]Matcher{Eq("namespace", "apps")}, r)
	require.ErrorIs(t, err, ErrNotConfigured)

	private := linkGrafana()
	private.PublicURL = ""
	_, err = private.DashboardURL(DashboardPod, LinkTarget{}, r)
	require.ErrorIs(t, err, ErrNoPublicURL)
	_, err = private.DashboardUIDURL("k8s-pod", r)
	require.ErrorIs(t, err, ErrNoPublicURL)
	_, err = private.ExploreMetricsURL(Select("up"), r)
	require.ErrorIs(t, err, ErrNoPublicURL)

	partial := linkGrafana()
	delete(partial.Dashboards, DashboardNode)
	partial.Datasource, partial.LokiDatasource = "", ""
	_, err = partial.DashboardURL(DashboardNode, LinkTarget{Node: "n"}, r)
	require.ErrorIs(t, err, ErrNoDashboard)
	require.ErrorContains(t, err, "no grafana dashboard is declared for node")
	_, err = partial.ExploreMetricsURL(Select("up"), r)
	require.ErrorIs(t, err, ErrNoDatasource)
	_, err = partial.ExploreLogsURL([]Matcher{Eq("namespace", "apps")}, r)
	require.ErrorIs(t, err, ErrNoDatasource)
	require.ErrorContains(t, err, "lokiDatasource")

	// A declaration that does not validate builds nothing.
	for name, bad := range map[string]func(*GrafanaSource){
		"userinfo in publicURL": func(g *GrafanaSource) { g.PublicURL = "https://user:SECRET@grafana.example.test" },
		"query in publicURL":    func(g *GrafanaSource) { g.PublicURL = "https://grafana.example.test?token=SECRET" },
		"variable name":         func(g *GrafanaSource) { g.Dashboards[DashboardPod] = Dashboard{UID: "k8s-pod", PodVar: "pod&x=1"} },
		"loki uid":              func(g *GrafanaSource) { g.LokiDatasource = "../loki" },
	} {
		broken := linkGrafana()
		bad(broken)
		link, err := broken.DashboardURL(DashboardPod, LinkTarget{Namespace: "apps", Pod: "p"}, r)
		require.Error(t, err, name)
		require.Empty(t, link, name)
		require.NotContains(t, err.Error(), "SECRET", name)
	}
	for _, uid := range []string{"", "../x", "a/b", "a b", "a?b", strings.Repeat("a", 65)} {
		_, err = g.DashboardUIDURL(uid, r)
		require.Error(t, err, uid)
	}
	link, err := g.DashboardUIDURL("checkout-slo", r)
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/team/d/checkout-slo?from=1000&to=now", link)

	// The source URL, its token variable and the token never reach a link.
	metrics, err := g.ExploreMetricsURL(Select("up"), r)
	require.NoError(t, err)
	logs, err := g.ExploreLogsURL([]Matcher{Eq("namespace", "apps")}, r)
	require.NoError(t, err)
	dashboard, err := g.DashboardURL(DashboardPod, LinkTarget{Namespace: "apps", Pod: "p"}, r)
	require.NoError(t, err)
	for _, link := range []string{metrics, logs, dashboard} {
		for _, secret := range []string{"SECRET-TOKEN-VALUE", "GRAFANA_LINK_TOKEN", "grafana.internal.test", "Authorization", "@"} {
			require.NotContains(t, link, secret)
		}
		require.True(t, strings.HasPrefix(link, "https://grafana.example.test/team/"), link)
	}
}

func explorePaneOf(t *testing.T, link string) (pane map[string]any, parsed *url.URL) {
	t.Helper()
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "1", parsed.Query().Get("schemaVersion"))
	var panes map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes))
	require.Len(t, panes, 1)
	for _, p := range panes {
		pane = p
	}
	return pane, parsed
}

func TestExploreLinksCarryTheEscapedQueryDatasourceAndRange(t *testing.T) {
	g := linkGrafana()
	hostile := `api"}[5m]) or vector(1) # \`
	expr := Sum(Rate(Select("http_requests_total", Eq("namespace", "apps"), Eq("pod", hostile)), 5*time.Minute), "pod")
	link, err := g.ExploreMetricsURL(expr, LinkRange{From: time.UnixMilli(1000), To: time.UnixMilli(2000)})
	require.NoError(t, err)
	require.NotContains(t, link, " ")
	require.NotContains(t, link, "+")
	require.NotContains(t, link, `"`)
	pane, parsed := explorePaneOf(t, link)
	require.Equal(t, "/team/explore", parsed.Path)
	require.Equal(t, "prom-main", pane["datasource"])
	require.Equal(t, map[string]any{"from": "1000", "to": "2000"}, pane["range"])
	query := pane["queries"].([]any)[0].(map[string]any)
	require.Equal(t, expr.String(), query["expr"], "the query survives the URL exactly as the typed builder wrote it")
	require.Contains(t, query["expr"], `pod="api\"}[5m]) or vector(1) # \\"`)
	require.Equal(t, map[string]any{"type": "prometheus", "uid": "prom-main"}, query["datasource"])
	require.Equal(t, "A", query["refId"])

	// Live and missing ranges.
	link, err = g.ExploreMetricsURL(Select("up"), LinkRange{From: time.UnixMilli(1000), Live: true})
	require.NoError(t, err)
	pane, _ = explorePaneOf(t, link)
	require.Equal(t, map[string]any{"from": "1000", "to": "now"}, pane["range"])
	link, err = g.ExploreMetricsURL(Select("up"), LinkRange{})
	require.NoError(t, err)
	pane, _ = explorePaneOf(t, link)
	require.Equal(t, map[string]any{"from": "now-1h", "to": "now"}, pane["range"])

	// An expression the builder refused is not linked.
	_, err = g.ExploreMetricsURL(Select("not a metric"), LinkRange{})
	require.Error(t, err)

	// LogQL selector with the same escaping, on the Loki datasource.
	link, err = g.ExploreLogsURL([]Matcher{Eq("namespace", "apps"), Re("pod", RegexQuote("checkout.api")+"-.+")}, LinkRange{From: time.UnixMilli(1000), Live: true})
	require.NoError(t, err)
	pane, _ = explorePaneOf(t, link)
	query = pane["queries"].([]any)[0].(map[string]any)
	require.Equal(t, `{namespace="apps",pod=~"checkout\\.api-.+"}`, query["expr"])
	require.Equal(t, map[string]any{"type": "loki", "uid": "loki-main"}, query["datasource"])
	require.Equal(t, "loki-main", pane["datasource"])

	selector, err := LogSelector(Eq("namespace", `a"b`))
	require.NoError(t, err)
	require.Equal(t, `{namespace="a\"b"}`, selector)
	_, err = LogSelector()
	require.Error(t, err)
	_, err = LogSelector(Eq("bad label", "x"))
	require.Error(t, err)
	_, err = g.ExploreLogsURL(nil, LinkRange{})
	require.Error(t, err)
}

func TestPinSnippetDeclaresOneDashboardAndRefusesABadUID(t *testing.T) {
	require.Equal(t, "k9s:\n  observability:\n    grafana:\n      dashboards:\n        pod: {uid: checkout-slo, namespaceVar: namespace, podVar: pod}",
		PinSnippet(DashboardPod, "checkout-slo"))
	require.Contains(t, PinSnippet(DashboardWorkload, "w"), "workload: {uid: w, namespaceVar: namespace, workloadVar: workload}")
	require.Contains(t, PinSnippet(DashboardNode, "n"), "node: {uid: n, nodeVar: node}")
	require.Contains(t, PinSnippet(DashboardNamespace, "n"), "namespace: {uid: n, namespaceVar: namespace}")
	require.Empty(t, PinSnippet("cluster", "x"))
	require.Empty(t, PinSnippet(DashboardPod, "x: y}\nk9s"))

	// What the snippet proposes validates as a declaration.
	g := &GrafanaSource{Source: Source{Service: "monitoring/grafana:3000"}, PublicURL: "https://g.example.test", LokiDatasource: "loki-main",
		Dashboards: map[string]Dashboard{DashboardPod: {UID: "checkout-slo", NamespaceVar: "namespace", PodVar: "pod"}}}
	require.NoError(t, g.Validate())
	g.LokiDatasource = "not a uid"
	require.ErrorContains(t, g.Validate(), "lokiDatasource")
}
