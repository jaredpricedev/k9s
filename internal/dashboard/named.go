// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dashboard

import (
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/observability"
)

// Named is a query that can be recalled by name in the :query bar.
type Named struct {
	Name string
	observability.NamedQuery
	// Builtin is false for a query saved in the context configuration.
	Builtin bool
	// About says in a sentence what the query shows and what it assumes.
	About string
}

// builtins are the queries shipped for looking into a request that hangs.
// Metric and label names are the defaults of cAdvisor, ingress-nginx, the
// GitLab Puma exporter, CoreDNS and Hubble; nothing checks that they exist,
// and a query that matches nothing says so.
var builtins = []Named{
	{Name: "throttled", About: "Share of CPU periods in which each Pod's containers were throttled at their limit (cAdvisor).",
		NamedQuery: observability.NamedQuery{Title: "CPU throttled share by pod", Unit: observability.UnitRatio, Legend: "{{pod}}",
			Expr: `sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{namespace="$namespace", pod=~"$pod"}[$__rate_interval])) / ` +
				`sum by (pod) (rate(container_cpu_cfs_periods_total{namespace="$namespace", pod=~"$pod"}[$__rate_interval]))`}},
	{Name: "ingress-499", About: "Share of requests the client gave up on (HTTP 499) at ingress-nginx, per Ingress of the namespace.",
		NamedQuery: observability.NamedQuery{Title: "499 share at ingress", Unit: observability.UnitRatio, Legend: "{{ingress}}",
			Expr: `sum by (ingress) (rate(nginx_ingress_controller_requests{namespace="$namespace", status="499"}[$__rate_interval])) / ` +
				`sum by (ingress) (rate(nginx_ingress_controller_requests{namespace="$namespace"}[$__rate_interval]))`}},
	{Name: "puma-queue", About: "Connections waiting for a free Puma thread, per Pod (GitLab webservice exporter).",
		NamedQuery: observability.NamedQuery{Title: "Puma queued connections by pod", Unit: observability.UnitCount, Legend: "{{pod}}",
			Expr: `sum by (pod) (puma_queued_connections{namespace="$namespace", pod=~"$pod"})`}},
	{Name: "dns-p99", About: "99th percentile of CoreDNS request duration, cluster-wide.",
		NamedQuery: observability.NamedQuery{Title: "DNS request duration p99", Unit: observability.UnitSeconds, Legend: "p99",
			Expr: `histogram_quantile(0.99, sum by (le) (rate(coredns_dns_request_duration_seconds_bucket[$__rate_interval])))`}},
	{Name: "hubble-drops", About: "Packets Cilium dropped per second by reason, cluster-wide (Hubble metrics).",
		NamedQuery: observability.NamedQuery{Title: "Hubble drops by reason", Unit: observability.UnitPerSecond, Legend: "{{reason}}",
			Expr: `sum by (reason) (rate(hubble_drop_total[$__rate_interval]))`}},
}

// BuiltinQueries lists the shipped queries in their display order.
func BuiltinQueries() []Named { return NamedQueries(nil) }

// NamedQueries lists the shipped queries followed by the saved ones in name
// order. A saved query with the name of a shipped one replaces it.
func NamedQueries(s *observability.Sources) []Named {
	var out []Named
	saved := map[string]bool{}
	if s != nil {
		for name := range s.Queries {
			saved[name] = true
		}
	}
	for _, q := range builtins {
		if !saved[q.Name] {
			q.Builtin = true
			out = append(out, q)
		}
	}
	names := make([]string, 0, len(saved))
	for name := range saved {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, Named{Name: name, NamedQuery: s.Queries[name], About: "Saved under k9s.observability.queries in this context's configuration."})
	}
	return out
}

// FindQuery looks a query up by name.
func FindQuery(s *observability.Sources, name string) (Named, bool) {
	for _, q := range NamedQueries(s) {
		if q.Name == name {
			return q, true
		}
	}
	return Named{}, false
}

var prometheusSource = Datasource{Type: sourcePrometheus}

func queryPanel(id int, q *Named) Panel {
	title := q.Title
	if title == "" {
		title = q.Name
	}
	if title == "" {
		// A typed expression is its own title.
		title = q.Expr
	}
	return Panel{ID: id, Type: "timeseries", Kind: KindTimeseries, Title: clean(title, 120), Description: q.About, Band: 1,
		Unit: UnitNamed(q.Unit), Datasource: prometheusSource,
		Targets: []Target{{RefID: "A", Expr: q.Expr, Legend: q.Legend, Datasource: prometheusSource}}}
}

// QueryBoard is the page :query shows without an argument: every named
// query as one chart.
func QueryBoard(named []Named) *Dashboard {
	d := &Dashboard{Title: "Named queries"}
	for i := range named {
		if len(d.Panels) >= MaxPanels {
			d.PanelsCut++
			continue
		}
		p := queryPanel(i+1, &named[i])
		p.Title = named[i].Name + " · " + p.Title
		d.Panels = append(d.Panels, p)
	}
	return d
}

// QueryPage is the page of one query: a named one, or an expression typed
// into the bar.
func QueryPage(q *Named) *Dashboard {
	title := q.Name
	if title == "" {
		title = "typed expression"
	}
	return &Dashboard{Title: title, Panels: []Panel{queryPanel(1, q)}}
}

// SaveSnippet is the YAML that saves an expression under a name. Nothing
// writes it; the page shows it for the person to copy.
func SaveSnippet(name, expr string) string {
	if name == "" {
		name = "my-query"
	}
	return "k9s:\n  observability:\n    queries:\n      " + name + ":\n        expr: '" + strings.ReplaceAll(strings.Join(strings.Fields(expr), " "), "'", "''") + "'"
}
