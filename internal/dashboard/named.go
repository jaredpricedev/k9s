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
	// Profile and Signal name the request-path signal a shipped query is:
	// the reading :slow makes of the same hop.
	Profile, Signal string
}

// builtin is a shipped query: one signal of a request-path profile, kept
// apart by the label of a scope field. It carries no query text. The text
// is written from the profile when the queries are listed, so that a
// shipped query and :slow read the same metric with the same matchers, and
// a context that declares other label names changes both.
type builtin struct {
	Name, Title string
	Hop         observability.HopKind
	// Profile is the built-in profile that has the signal. A profile the
	// context reads the hop with by default is preferred when it has a
	// signal of that name.
	Profile, Signal string
	// Split is the scope field whose label keeps the answer apart.
	Split string
	// Each says what one line of the answer is.
	Each string
}

// builtins are the queries shipped for looking into a request that hangs.
// Nothing checks that their metrics exist, and a query that matches
// nothing says so.
var builtins = []builtin{
	{Name: "throttled", Title: "CPU throttled share by pod", Hop: observability.HopApp, Profile: "container-cpu", Signal: "throttled",
		Split: observability.ScopePod, Each: "per Pod"},
	{Name: "ingress-499", Title: "499 per second at the ingress", Hop: observability.HopIngress, Profile: "ingress-nginx", Signal: "499",
		Split: observability.ScopeIngress, Each: "per Ingress of the namespace"},
	{Name: "puma-queue", Title: "Puma queued connections by pod", Hop: observability.HopApp, Profile: "gitlab-webservice", Signal: "queued",
		Split: observability.ScopePod, Each: "per Pod"},
	{Name: "dns-p99", Title: "DNS request duration p99", Hop: observability.HopDNS, Profile: "coredns", Signal: "p99",
		Each: "cluster-wide"},
	{Name: "hubble-drops", Title: "Hubble drops to the pods, by reason", Hop: observability.HopNetwork, Profile: "cilium-hubble", Signal: "drops",
		Split: observability.ScopePod, Each: "per Pod and reason"},
}

// named writes the shipped query from the profile the context reads its
// hop with: the first default profile of the hop that has the signal, else
// the profile the query names, else that profile as shipped.
func (b *builtin) named(s *observability.Sources) Named {
	var split []string
	if b.Split != "" {
		split = []string{b.Split}
	}
	out := Named{Name: b.Name, Builtin: true, NamedQuery: observability.NamedQuery{Title: b.Title}}
	candidates := append(s.HopProfiles(b.Hop, ""), b.Profile)
	var (
		template observability.SignalTemplate
		err      error
	)
	for _, name := range candidates {
		if template, err = s.SignalTemplate(name, b.Signal, split...); err == nil {
			break
		}
	}
	if err != nil {
		// The declaration cannot be used; the shipped profile still can.
		template, err = (*observability.Sources)(nil).SignalTemplate(b.Profile, b.Signal, split...)
	}
	if err != nil {
		out.About = "Not available: " + observability.Clean(err.Error(), 200) + "."
		return out
	}
	out.Profile, out.Signal = template.Profile, template.Signal.Name
	out.Expr, out.Unit = template.Text, template.Signal.Unit
	legend := make([]string, 0, len(template.By))
	for _, label := range template.By {
		legend = append(legend, "{{"+label+"}}")
	}
	out.Legend = strings.Join(legend, " ")
	if out.Legend == "" {
		out.Legend = template.Signal.Name
	}
	title := template.Signal.Title
	if title != "" {
		title = strings.ToUpper(title[:1]) + title[1:]
	}
	out.About = title + ", " + b.Each + ". Signal " + template.Signal.Name + " of the " + template.Profile +
		" request-path profile: the reading :slow makes."
	return out
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
	for i := range builtins {
		if !saved[builtins[i].Name] {
			out = append(out, builtins[i].named(s))
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
