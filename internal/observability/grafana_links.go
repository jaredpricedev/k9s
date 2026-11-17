// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Link building contacts nothing. Every link is made from the declared
// publicURL, validated identifiers and escaped values; no token, header or
// userinfo can appear in one because none is ever an input.

var (
	// ErrNoPublicURL means Grafana is declared without a browser-facing URL.
	ErrNoPublicURL = errors.New("grafana publicURL is not declared")
	// ErrNoDashboard means no dashboard is declared for the requested kind.
	ErrNoDashboard = errors.New("no grafana dashboard is declared")
	// ErrNoDatasource means the datasource UID an Explore link needs is not declared.
	ErrNoDatasource = errors.New("grafana datasource UID is not declared")
)

// LinkRange is the time range a link opens with. A zero From leaves the
// range to the dashboard's own default. Live ends the range at Grafana's
// "now" so an open incident keeps moving; otherwise To must follow From.
type LinkRange struct {
	From, To time.Time
	Live     bool
}

// bounds returns Grafana's from and to values, or ok false for no range.
func (r LinkRange) bounds() (from, to string, ok bool) {
	if r.From.IsZero() {
		return "", "", false
	}
	from = strconv.FormatInt(r.From.UnixMilli(), 10)
	if r.Live {
		return from, "now", true
	}
	if r.To.IsZero() || !r.To.After(r.From) {
		return "", "", false
	}
	return from, strconv.FormatInt(r.To.UnixMilli(), 10), true
}

// encode writes a query string with %20 for spaces, which every Grafana
// version and every terminal link detector reads the same way.
func encode(q url.Values) string {
	return strings.ReplaceAll(q.Encode(), "+", "%20")
}

func (g *GrafanaSource) base() (*url.URL, error) {
	if g == nil {
		return nil, ErrNotConfigured
	}
	if g.PublicURL == "" {
		return nil, ErrNoPublicURL
	}
	u, err := parseBaseURL(g.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("publicURL: %w", err)
	}
	return u, nil
}

// DashboardURL builds the browser link of the dashboard declared for one
// kind, with its declared variables filled from t. Variables whose value is
// empty are left out, so the dashboard keeps its own default for them.
func (g *GrafanaSource) DashboardURL(kind string, t LinkTarget, r LinkRange) (string, error) {
	u, err := g.base()
	if err != nil {
		return "", err
	}
	d, ok := g.Dashboards[kind]
	if !ok {
		return "", fmt.Errorf("%w for %s", ErrNoDashboard, kind)
	}
	if err := g.Validate(); err != nil {
		return "", err
	}
	u.Path += "/d/" + d.UID
	q := url.Values{}
	for _, pair := range [][2]string{{d.NamespaceVar, t.Namespace}, {d.PodVar, t.Pod}, {d.WorkloadVar, t.Workload}, {d.NodeVar, t.Node}} {
		if pair[0] != "" && pair[1] != "" {
			q.Set("var-"+pair[0], pair[1])
		}
	}
	if from, to, ok := r.bounds(); ok {
		q.Set("from", from)
		q.Set("to", to)
	}
	u.RawQuery = encode(q)
	return u.String(), nil
}

// Filled names the declared variables a link for t would carry and the
// declared ones it cannot fill, in declaration order.
func (d Dashboard) Filled(t LinkTarget) (filled, empty []string) {
	for _, pair := range [][2]string{{d.NamespaceVar, t.Namespace}, {d.PodVar, t.Pod}, {d.WorkloadVar, t.Workload}, {d.NodeVar, t.Node}} {
		switch {
		case pair[0] == "":
		case pair[1] == "":
			empty = append(empty, pair[0])
		default:
			filled = append(filled, pair[0]+"="+pair[1])
		}
	}
	return filled, empty
}

// DashboardUIDURL links a dashboard by UID alone, for one that is not
// declared. Its variable names are unknown, so none is filled.
func (g *GrafanaSource) DashboardUIDURL(uid string, r LinkRange) (string, error) {
	u, err := g.base()
	if err != nil {
		return "", err
	}
	if !grafanaUID.MatchString(uid) {
		return "", errors.New("dashboard UID refused")
	}
	u.Path += "/d/" + uid
	q := url.Values{}
	if from, to, ok := r.bounds(); ok {
		q.Set("from", from)
		q.Set("to", to)
	}
	u.RawQuery = encode(q)
	return u.String(), nil
}

// LogSelector renders a LogQL stream selector such as
// {namespace="apps",pod="api-0"}. Values are escaped exactly as in PromQL.
func LogSelector(ms ...Matcher) (string, error) {
	if len(ms) == 0 {
		return "", errors.New("a log selector needs at least one matcher")
	}
	text, err := matchers(ms)
	if err != nil {
		return "", err
	}
	return "{" + text + "}", nil
}

type exploreDatasource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type exploreQuery struct {
	RefID      string            `json:"refId"`
	Datasource exploreDatasource `json:"datasource"`
	Expr       string            `json:"expr"`
}

type exploreRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type explorePane struct {
	Datasource string         `json:"datasource"`
	Queries    []exploreQuery `json:"queries"`
	Range      exploreRange   `json:"range"`
}

// explore builds a Grafana Explore link in the panes format (schemaVersion
// 1, Grafana 10 and later). Without a range it opens on the last hour.
func (g *GrafanaSource) explore(kind, uid, expr string, r LinkRange) (string, error) {
	u, err := g.base()
	if err != nil {
		return "", err
	}
	if uid == "" {
		return "", ErrNoDatasource
	}
	if !grafanaUID.MatchString(uid) {
		return "", errors.New("datasource UID refused")
	}
	if expr == "" || len(expr) > 4096 {
		return "", errors.New("query refused")
	}
	pane := explorePane{Datasource: uid, Range: exploreRange{From: "now-1h", To: "now"},
		Queries: []exploreQuery{{RefID: "A", Datasource: exploreDatasource{Type: kind, UID: uid}, Expr: expr}}}
	if from, to, ok := r.bounds(); ok {
		pane.Range = exploreRange{From: from, To: to}
	}
	panes, err := json.Marshal(map[string]explorePane{"k9p": pane})
	if err != nil {
		return "", errors.New("query refused")
	}
	u.Path += "/explore"
	u.RawQuery = encode(url.Values{"schemaVersion": {"1"}, "panes": {string(panes)}})
	return u.String(), nil
}

// ExploreMetricsURL opens Grafana Explore on the declared Prometheus
// datasource with one query built by the typed builder.
func (g *GrafanaSource) ExploreMetricsURL(e Expr, r LinkRange) (string, error) {
	if g == nil {
		return "", ErrNotConfigured
	}
	if err := e.Err(); err != nil {
		return "", err
	}
	return g.explore("prometheus", g.Datasource, e.String(), r)
}

// ExploreLogsURL opens Grafana Explore on the declared Loki datasource with
// a stream selector. The Loki datasource itself is never queried from here.
func (g *GrafanaSource) ExploreLogsURL(selector []Matcher, r LinkRange) (string, error) {
	if g == nil {
		return "", ErrNotConfigured
	}
	if g.LokiDatasource == "" {
		return "", fmt.Errorf("%w: lokiDatasource", ErrNoDatasource)
	}
	text, err := LogSelector(selector...)
	if err != nil {
		return "", err
	}
	return g.explore("loki", g.LokiDatasource, text, r)
}

// conventionalVars are the variable names most Kubernetes dashboards use.
// A pin snippet proposes them; the dashboard's own variables decide.
var conventionalVars = map[string][][2]string{
	DashboardPod:       {{"namespaceVar", "namespace"}, {"podVar", "pod"}},
	DashboardWorkload:  {{"namespaceVar", "namespace"}, {"workloadVar", "workload"}},
	DashboardNode:      {{"nodeVar", "node"}},
	DashboardNamespace: {{"namespaceVar", "namespace"}},
}

// PinSnippet is the YAML that declares uid as the dashboard for kind, placed
// under k9s.observability in the context configuration. Nothing writes it.
func PinSnippet(kind, uid string) string {
	vars, known := conventionalVars[kind]
	if !known || !grafanaUID.MatchString(uid) {
		return ""
	}
	// One key per row: every row stays short, so a narrow dialog shows the
	// whole snippet instead of clipping a flow mapping at its edge.
	const indent = "\n          "
	out := "k9s:\n  observability:\n    grafana:\n      dashboards:\n        " + kind + ":" + indent + "uid: " + uid
	for _, v := range vars {
		out += indent + v[0] + ": " + v[1]
	}
	return out
}
