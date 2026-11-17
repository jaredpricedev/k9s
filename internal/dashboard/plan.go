// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dashboard

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/observability"
)

// Query is one target of a panel after its variables were filled in.
type Query struct {
	RefID, Legend string
	// Authored is the expression as the dashboard holds it.
	Authored string
	// Expr is what is sent; it is invalid when Skip says why nothing is.
	Expr observability.Expr
	// Skip is why this query is not sent: a hidden target, another
	// datasource, a variable without a value.
	Skip string
	// Assumed says the definition does not state the datasource type, so
	// the expression is sent to Prometheus on the strength of being one.
	Assumed bool
}

// Sent reports whether the query is to be run.
func (q *Query) Sent() bool { return q.Skip == "" }

// PanelPlan is what one read does with one panel.
type PanelPlan struct {
	Queries []Query
	// NotDrawn is why the panel draws nothing in this read, whatever a
	// source answers: its type, its datasource, an unresolved variable, or
	// a limit. Empty for a panel whose queries are sent, and for a row.
	NotDrawn string
	// Notes are things the reader should know about a panel that is
	// drawn: a query left out, an assumed datasource, a repeat.
	Notes []string
}

// Plan is the outcome of planning one read of a dashboard.
type Plan struct {
	Panels   []PanelPlan // parallel to Dashboard.Panels
	Bindings Bindings
	// Planned counts the panels whose queries are sent, Queries the
	// queries sent. CutPanels and CutQueries count what the limits of one
	// read left out.
	Planned, Queries      int
	CutPanels, CutQueries int
}

// planQueries fills the variables into the targets of one panel. room is
// how many more queries this read may send; cut counts targets it had no
// room for.
func planQueries(d *Dashboard, p *Panel, b *Bindings, room int) (queries []Query, sent, cut int, failed []string) {
	for j := range p.Targets {
		t := &p.Targets[j]
		q := Query{RefID: t.RefID, Legend: t.Legend, Authored: strings.Join(strings.Fields(t.Expr), " ")}
		kind := d.targetSource(p, t)
		switch {
		case strings.TrimSpace(t.Expr) == "":
			q.Skip = "it has no expression"
		case t.Hidden:
			q.Skip = "it is hidden in the dashboard"
		case kind != sourcePrometheus && kind != sourceUnstated:
			q.Skip = "its datasource is " + kind + ", not Prometheus"
		case sent >= room:
			q.Skip = fmt.Sprintf("not read: one read sends at most %d queries", MaxQueries)
			cut++
		default:
			text, err := b.Expand(t.Expr)
			if err == nil {
				q.Expr = observability.AuthoredExpr(text)
				err = q.Expr.Err()
			}
			if err != nil {
				q.Skip = err.Error()
				failed = append(failed, q.RefID+": "+q.Skip)
			} else {
				q.Assumed = kind == sourceUnstated
				sent++
			}
		}
		queries = append(queries, q)
	}
	return queries, sent, cut, failed
}

// notes are the things to know about a panel that is drawn.
func notes(p *Panel, queries []Query) []string {
	var out []string
	for j := range queries {
		q := &queries[j]
		switch {
		case !q.Sent():
			out = append(out, "query "+q.RefID+" is not sent: "+q.Skip)
		case q.Assumed:
			out = append(out, "query "+q.RefID+": the dashboard does not state the datasource type; it is sent to Prometheus")
		}
	}
	if p.TargetsCut > 0 {
		out = append(out, fmt.Sprintf("%d more queries are not kept: a panel keeps at most %d", p.TargetsCut, MaxTargets))
	}
	if p.Repeat != "" {
		out = append(out, "the panel repeats for $"+p.Repeat+" in Grafana; here it is drawn once")
	}
	return out
}

// BuildPlan fills the variables into every drawable panel's expressions and
// applies the limits of one read. Every panel gets queries or a reason.
func BuildPlan(d *Dashboard, b Bindings) Plan {
	plan := Plan{Panels: make([]PanelPlan, len(d.Panels)), Bindings: b}
	for i := range d.Panels {
		p := &d.Panels[i]
		pp := &plan.Panels[i]
		switch {
		case p.Kind == KindRow:
			continue
		case p.NotDrawn != "":
			pp.NotDrawn = p.NotDrawn
			continue
		case plan.Planned >= MaxDrawn:
			pp.NotDrawn = fmt.Sprintf("not read: one read draws at most %d panels", MaxDrawn)
			plan.CutPanels++
			continue
		}
		queries, sent, cut, failed := planQueries(d, p, &b, MaxQueries-plan.Queries)
		pp.Queries = queries
		plan.CutQueries += cut
		if sent > 0 {
			plan.Planned++
			plan.Queries += sent
			pp.Notes = notes(p, queries)
			continue
		}
		switch {
		case len(failed) > 1 || (len(failed) == 1 && len(queries) > 1):
			pp.NotDrawn = failed[0]
		case len(failed) == 1:
			pp.NotDrawn = queries[0].Skip
		case len(queries) > 0:
			pp.NotDrawn = queries[0].Skip
		default:
			pp.NotDrawn = "it has no Prometheus expression"
		}
	}
	return plan
}

// Counts are the totals a status line states.
type Counts struct {
	// Panels counts every panel that is not a row; Planned those whose
	// queries are sent; NotDrawn the rest.
	Panels, Planned, NotDrawn int
}

// Count totals the plan.
func (p *Plan) Count(d *Dashboard) Counts {
	var c Counts
	for i := range d.Panels {
		if d.Panels[i].Kind == KindRow {
			continue
		}
		c.Panels++
		if p.Panels[i].NotDrawn != "" {
			c.NotDrawn++
		} else {
			c.Planned++
		}
	}
	return c
}
