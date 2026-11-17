// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDashboardJSONIsOneBoundedReadOfAValidUID(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/dashboards/uid/known":
			_, _ = w.Write([]byte(`{"dashboard":{"uid":"known","title":"Known"}}`))
		case "/api/dashboards/uid/huge":
			_, _ = w.Write([]byte(strings.Repeat("x", MaxBytes+10)))
		case "/api/dashboards/uid/secret":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	grafana, err := NewURLClient(KindGrafana, server.URL, "")
	require.NoError(t, err)

	body, st := grafana.DashboardJSON(t.Context(), "known")
	require.Equal(t, StateComplete, st.State)
	require.JSONEq(t, `{"dashboard":{"uid":"known","title":"Known"}}`, string(body))
	body, st = grafana.DashboardJSON(t.Context(), "missing")
	require.Equal(t, StateAbsent, st.State)
	require.Nil(t, body)
	_, st = grafana.DashboardJSON(t.Context(), "secret")
	require.Equal(t, StateDenied, st.State)
	body, st = grafana.DashboardJSON(t.Context(), "huge")
	require.Equal(t, StateIncomplete, st.State, "a definition over the byte limit is not used in part")
	require.Nil(t, body)
	require.Equal(t, []string{"GET /api/dashboards/uid/known", "GET /api/dashboards/uid/missing", "GET /api/dashboards/uid/secret", "GET /api/dashboards/uid/huge"}, paths)

	// A UID that could change the path is refused before anything is sent.
	for _, uid := range []string{"", "a/b", "../admin", "a?b", strings.Repeat("u", 65)} {
		_, st = grafana.DashboardJSON(t.Context(), uid)
		require.Equal(t, "dashboard UID refused", st.Reason, uid)
	}
	require.Len(t, paths, 4)
	prom, err := NewURLClient(KindPrometheus, server.URL, "")
	require.NoError(t, err)
	_, st = prom.DashboardJSON(t.Context(), "known")
	require.Equal(t, "not a grafana source", st.Reason)
}

func TestRejectedQueryKeepsTheSourcesOwnWordsAndNothingElse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch query := r.URL.Query().Get("query"); {
		case strings.Contains(query, "bad"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid parameter \"query\": 1:5: parse error:\n\tunexpected\u0007 <aggr:sum>"}`))
		case strings.Contains(query, "long"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"execution","error":"` + strings.Repeat("e", 3000) + `"}`))
		case strings.Contains(query, "plain"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html>Authorization: Bearer SECRET</html>"))
		case strings.Contains(query, "down"):
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"status":"error","error":"upstream SECRET"}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		}
	}))
	defer server.Close()
	prom, err := NewURLClient(KindPrometheus, server.URL, "")
	require.NoError(t, err)
	end := time.Now()
	ask := func(text string) PromResult {
		return prom.QueryRange(t.Context(), AuthoredExpr(text), end.Add(-time.Hour), end)
	}

	res := ask("bad(")
	require.Equal(t, StateUnavailable, res.State)
	require.Equal(t, "HTTP 400 request rejected", res.Reason)
	require.Equal(t, `bad_data: invalid parameter "query": 1:5: parse error: unexpected <aggr:sum>`, res.Error, "control characters and line breaks are removed")
	res = ask("long")
	require.LessOrEqual(t, len([]rune(res.Error)), maxPromError+len("execution: …"))
	require.True(t, strings.HasPrefix(res.Error, "execution: eee"))
	// Only a Prometheus error body of a rejected query is kept: any other
	// failed answer, and a body that is not that shape, leave nothing.
	require.Empty(t, ask("plain").Error)
	res = ask("down")
	require.Empty(t, res.Error)
	require.Equal(t, "HTTP 502", res.Reason)
	res = ask("up")
	require.Equal(t, StateComplete, res.State)
	require.Empty(t, res.Error)
	instant := prom.Query(t.Context(), AuthoredExpr("bad("), end)
	require.Contains(t, instant.Error, "parse error")
}

func TestDashboardVarsURLCarriesOnlyNamedVariables(t *testing.T) {
	g := &GrafanaSource{Source: Source{Service: "monitoring/grafana:3000"}, PublicURL: "https://grafana.example.test/g"}
	link, err := g.DashboardVarsURL("gitlab-webservice", [][2]string{{"namespace", "gitlab"}, {"pod", "a&b c"}, {"empty", ""}, {"bad name", "x"}},
		LinkRange{From: time.UnixMilli(1000), To: time.UnixMilli(2000), Live: true})
	require.NoError(t, err)
	require.Equal(t, "https://grafana.example.test/g/d/gitlab-webservice?from=1000&to=now&var-namespace=gitlab&var-pod=a%26b%20c", link)
	_, err = g.DashboardVarsURL("a/b", nil, LinkRange{})
	require.ErrorContains(t, err, "dashboard UID refused")
	_, err = (&GrafanaSource{}).DashboardVarsURL("x", nil, LinkRange{})
	require.ErrorIs(t, err, ErrNoPublicURL)
}

func TestSavedQueriesParseAndValidate(t *testing.T) {
	var s Sources
	require.NoError(t, yaml.Unmarshal([]byte(`
queries:
  slow-sql:
    title: Longest transaction
    expr: max by (datname) (pg_stat_activity_max_tx_duration{namespace="$namespace"})
    unit: seconds
    legend: "{{datname}}"
  up: {expr: up}
`), &s))
	require.NoError(t, s.ValidateQueries())
	require.Equal(t, []string{"slow-sql", "up"}, s.QueryNames())
	require.Equal(t, "{{datname}}", s.Queries["slow-sql"].Legend)
	require.NoError(t, (*Sources)(nil).ValidateQueries())
	require.True(t, QueryName("dns-p99"))
	for _, bad := range []string{"", "Up", "a b", "-a", "a-", strings.Repeat("a", 33), "up{}"} {
		require.False(t, QueryName(bad), bad)
	}
	many := Sources{Queries: map[string]NamedQuery{}}
	for i := range MaxNamedQueries + 1 {
		many.Queries["q"+strings.Repeat("x", i)] = NamedQuery{Expr: "up"}
	}
	require.ErrorContains(t, many.ValidateQueries(), "at most 32 queries")
	for name, q := range map[string]NamedQuery{"empty": {Expr: " "}, "long": {Expr: strings.Repeat("a", MaxAuthoredQuery+1)}, "unit": {Expr: "up", Unit: "parsecs"}} {
		bad := Sources{Queries: map[string]NamedQuery{name: q}}
		require.Error(t, bad.ValidateQueries(), name)
	}
}
