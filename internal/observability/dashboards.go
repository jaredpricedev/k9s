// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// NamedQuery is one PromQL expression saved under a name for the :query
// bar, under k9s.observability.queries in the context configuration.
type NamedQuery struct {
	// Title is shown above the chart; empty uses the name.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
	// Expr is the PromQL text. It may use $namespace, $pod, $workload and
	// $node, and Grafana's $__rate_interval, $__interval and $__range.
	Expr string `json:"expr" yaml:"expr"`
	// Unit is one of seconds, ratio, perSecond, count, bytes and cores;
	// empty draws plain numbers.
	Unit string `json:"unit,omitempty" yaml:"unit,omitempty"`
	// Legend names each line, with {{label}} replaced by a label's value;
	// empty names a line by the labels that differ.
	Legend string `json:"legend,omitempty" yaml:"legend,omitempty"`
}

// MaxNamedQueries bounds the saved queries of one context.
const MaxNamedQueries = 32

var queryName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// QueryName reports whether text can name a saved query: lower-case
// letters, digits and dashes, at most 32 characters.
func QueryName(text string) bool { return queryName.MatchString(text) }

// QueryNames lists the saved query names in order.
func (s *Sources) QueryNames() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.Queries))
	for name := range s.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateQueries reports why the saved queries cannot be used, or nil.
func (s *Sources) ValidateQueries() error {
	if s == nil {
		return nil
	}
	if len(s.Queries) > MaxNamedQueries {
		return fmt.Errorf("at most %d queries can be saved", MaxNamedQueries)
	}
	for _, name := range s.QueryNames() {
		q := s.Queries[name]
		switch {
		case !QueryName(name):
			return fmt.Errorf("queries.%s: a name is lower-case letters, digits and dashes", Clean(name, 40))
		case strings.TrimSpace(q.Expr) == "":
			return fmt.Errorf("queries.%s: expr is empty", name)
		case len(q.Expr) > MaxAuthoredQuery:
			return fmt.Errorf("queries.%s: expr exceeds %d bytes", name, MaxAuthoredQuery)
		case len(q.Title) > 120 || len(q.Legend) > 200:
			return fmt.Errorf("queries.%s: title or legend is too long", name)
		}
		switch q.Unit {
		case "", UnitSeconds, UnitRatio, UnitPerSecond, UnitCount, UnitBytes, UnitCores:
		default:
			return fmt.Errorf("queries.%s: unit is one of seconds, ratio, perSecond, count, bytes, cores", name)
		}
	}
	return nil
}

// DashboardJSON reads GET /api/dashboards/uid/<uid> once: the definition of
// one dashboard, bounded like every read (one deadline, 1 MiB). The body is
// returned as Grafana sent it; nothing in it is trusted or shown before it
// is parsed and cleaned.
func (c *Client) DashboardJSON(ctx context.Context, uid string) ([]byte, Status) {
	st := Status{Kind: c.kind, Source: c.display, State: StateUnavailable}
	if c.kind != KindGrafana {
		st.Reason = "not a grafana source"
		return nil, st
	}
	if !grafanaUID.MatchString(uid) {
		st.Reason = "dashboard UID refused"
		return nil, st
	}
	return c.Get(ctx, "/api/dashboards/uid/"+uid, nil)
}

// DashboardVarsURL links a dashboard by UID with the given variables set,
// for a dashboard whose variable names were read from its definition.
// Each pair is a variable name and a value; a pair with an empty value or a
// name that is not an identifier is left out.
func (g *GrafanaSource) DashboardVarsURL(uid string, vars [][2]string, r LinkRange) (string, error) {
	u, err := g.base()
	if err != nil {
		return "", err
	}
	if !grafanaUID.MatchString(uid) {
		return "", errors.New("dashboard UID refused")
	}
	u.Path += "/d/" + uid
	q := url.Values{}
	for _, pair := range vars {
		if variable.MatchString(pair[0]) && pair[1] != "" && len(pair[1]) <= 256 {
			q.Add("var-"+pair[0], pair[1])
		}
	}
	if from, to, ok := r.bounds(); ok {
		q.Set("from", from)
		q.Set("to", to)
	}
	u.RawQuery = encode(q)
	return u.String(), nil
}
