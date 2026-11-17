// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

// Limits shared by every read through a Client.
const (
	// DefaultTimeout bounds one GET, including reading the body.
	DefaultTimeout = 8 * time.Second
	// MaxWindow is the widest query_range window a Client accepts.
	MaxWindow = 7 * 24 * time.Hour
	// MaxPoints is the number of sample times requested per series.
	MaxPoints = 120
	// MaxAlerts, MaxAnnotations and MaxDashboards bound retained items.
	MaxAlerts      = 500
	MaxAnnotations = 200
	MaxDashboards  = 100
	maxLabels      = 32
	maxText        = 1024
)

// State is the outcome of one explicit read, or the reason none was made.
type State string

const (
	// StateDeclared means the source is declared and nothing was contacted.
	StateDeclared State = "declared"
	// StateComplete means the source answered and the reply fit every limit.
	// It says the read completed, not that the workload is healthy.
	StateComplete State = "complete"
	// StateIncomplete means the source answered but a byte, series, sample
	// or item limit cut the retained result.
	StateIncomplete    State = "incomplete"
	StateDenied        State = "denied"
	StateAbsent        State = "absent"
	StateTimeout       State = "timeout"
	StateUnavailable   State = "unavailable"
	StateNotConfigured State = "not-configured"
)

// ErrNotConfigured means the context declares no such source.
var ErrNotConfigured = errors.New("source is not declared for this context")

// Status describes one read. Reason never contains a body, header, token or
// URL userinfo.
type Status struct {
	Kind       Kind
	State      State
	Reason     string    // short plain text; empty for complete
	Source     string    // how it is reached, e.g. "service monitoring/prometheus:9090"
	ObservedAt time.Time // when the read finished (UTC); zero if nothing was contacted
	HTTPStatus int       // zero when no response was received
}

// OK reports whether the read produced data (complete or incomplete).
func (s Status) OK() bool { return s.State == StateComplete || s.State == StateIncomplete }

// String is a one-line form: "prometheus: denied (HTTP 403)".
func (s Status) String() string {
	out := string(s.Kind) + ": " + string(s.State)
	if s.Reason != "" {
		out += " (" + s.Reason + ")"
	}
	return out
}

// RESTConfigFunc returns the active Kubernetes REST configuration. It is
// called only when a service source is resolved.
type RESTConfigFunc func() (*rest.Config, error)

// Client reads one declared source. It is safe for concurrent use and holds
// no token: a tokenEnv value is read from the environment per request.
type Client struct {
	kind     Kind
	base     *url.URL
	display  string
	tokenEnv string
	http     *http.Client
	grafana  *GrafanaSource
	now      func() time.Time
}

// Kind returns the kind of source this client reads.
func (c *Client) Kind() Kind { return c.kind }

// Display describes how the source is reached, without credentials.
func (c *Client) Display() string { return c.display }

// Grafana returns the Grafana declaration (links, dashboards) when the
// client was resolved from one, or nil.
func (c *Client) Grafana() *GrafanaSource { return c.grafana }

// Close releases idle connections of a direct URL client.
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// NewURLClient builds a client for a direct base URL. It does not follow
// redirects and does not inherit a proxy from the environment.
func NewURLClient(kind Kind, baseURL, tokenEnv string) (*Client, error) {
	src := Source{URL: baseURL, TokenEnv: tokenEnv}
	if err := src.Validate(); err != nil {
		return nil, err
	}
	u, _ := parseBaseURL(baseURL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Do not inherit proxy credentials or an unrelated endpoint mapping.
	return &Client{kind: kind, base: u, display: src.Display(), tokenEnv: tokenEnv, now: time.Now,
		http: &http.Client{Transport: transport, CheckRedirect: noRedirect}}, nil
}

func newServiceClient(kind Kind, ref ServiceRef, restConfig RESTConfigFunc) (*Client, error) {
	if restConfig == nil {
		return nil, errors.New("no Kubernetes connection")
	}
	cfg, err := restConfig()
	if err != nil || cfg == nil {
		return nil, errors.New("no Kubernetes connection")
	}
	host := cfg.Host
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("Kubernetes API server address is unusable")
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, errors.New("Kubernetes credentials are unusable")
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	u.Path = strings.TrimRight(u.Path, "/") + ref.ProxyPath()
	return &Client{kind: kind, base: u, display: "service " + ref.String(), now: time.Now,
		http: &http.Client{Transport: transport, CheckRedirect: noRedirect}}, nil
}

// Resolve builds the client for a declared source without contacting it.
// The returned Status is StateDeclared with a usable client, or
// StateNotConfigured / StateUnavailable with a nil client and a Reason.
//
// A context that declares no prometheus source but declares
// grafana.datasource gets a Prometheus client that reads through Grafana's
// datasource proxy.
func Resolve(s *Sources, kind Kind, restConfig RESTConfigFunc) (*Client, Status) {
	st := Status{Kind: kind, State: StateNotConfigured, Reason: "no " + string(kind) + " source is declared for this context"}
	if s == nil {
		return nil, st
	}
	src, prefix := s.Source(kind), ""
	if kind == KindPrometheus && src == nil && s.Grafana != nil && s.Grafana.Datasource != "" {
		if err := s.Grafana.Validate(); err != nil {
			st.State, st.Reason = StateUnavailable, "grafana declaration: "+err.Error()
			return nil, st
		}
		src, prefix = &s.Grafana.Source, "/api/datasources/proxy/uid/"+s.Grafana.Datasource
	}
	if src == nil {
		return nil, st
	}
	st.Source = src.Display()
	if err := s.Validate(kind); err != nil && prefix == "" {
		st.State, st.Reason = StateUnavailable, "declaration: "+err.Error()
		return nil, st
	}
	var (
		c   *Client
		err error
	)
	if src.Service != "" {
		ref, _ := ParseServiceRef(src.Service)
		c, err = newServiceClient(kind, ref, restConfig)
	} else {
		c, err = NewURLClient(kind, src.URL, src.TokenEnv)
	}
	if err != nil {
		st.State, st.Reason = StateUnavailable, err.Error()
		return nil, st
	}
	if prefix != "" {
		c.base.Path += prefix
		c.display += " (grafana datasource " + s.Grafana.Datasource + ")"
	}
	if kind == KindGrafana {
		c.grafana = s.Grafana
	}
	st.State, st.Reason, st.Source = StateDeclared, "", c.display
	return c, st
}

// Get performs one bounded GET of path (which must start with "/") below the
// source base. It returns the body only for StateComplete. The deadline is
// DefaultTimeout or the context's, whichever is sooner; the body limit is
// MaxBytes; redirects are refused.
func (c *Client) Get(ctx context.Context, path string, query url.Values) ([]byte, Status) {
	st := Status{Kind: c.kind, Source: c.display, State: StateUnavailable}
	finish := func(state State, reason string) ([]byte, Status) {
		st.State, st.Reason, st.ObservedAt = state, reason, c.now().UTC()
		return nil, st
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "?#\r\n\x00") {
		return finish(StateUnavailable, "request path refused")
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return finish(StateUnavailable, "request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	if c.tokenEnv != "" {
		token := os.Getenv(c.tokenEnv)
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return finish(StateUnavailable, "environment variable "+c.tokenEnv+" is not set")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	failed := func() ([]byte, Status) {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return finish(StateTimeout, "no answer within the deadline")
		case ctx.Err() != nil:
			return finish(StateUnavailable, "canceled")
		}
		return finish(StateUnavailable, "no response")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return failed()
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	_ = resp.Body.Close()
	st.HTTPStatus = resp.StatusCode
	if ctx.Err() != nil || readErr != nil {
		return failed()
	}
	code := fmt.Sprintf("HTTP %d", resp.StatusCode)
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return finish(StateDenied, code)
	case resp.StatusCode == http.StatusNotFound:
		return finish(StateAbsent, code)
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusGatewayTimeout:
		return finish(StateTimeout, code)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return finish(StateUnavailable, code+" redirect refused")
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity:
		return finish(StateUnavailable, code+" request rejected")
	default:
		return finish(StateUnavailable, code)
	}
	if len(body) > MaxBytes {
		return finish(StateIncomplete, "response exceeds the 1 MiB limit")
	}
	st.State, st.ObservedAt = StateComplete, c.now().UTC()
	return body, st
}

// Probe makes one small read that shows whether the source answers:
// Prometheus /api/v1/status/buildinfo, Alertmanager /api/v2/status,
// Grafana /api/health. The body is discarded.
func (c *Client) Probe(ctx context.Context) Status {
	path := map[Kind]string{KindPrometheus: "/api/v1/status/buildinfo", KindAlertmanager: "/api/v2/status", KindGrafana: "/api/health"}[c.kind]
	if path == "" {
		return Status{Kind: c.kind, Source: c.display, State: StateUnavailable, Reason: "unknown source kind"}
	}
	_, st := c.Get(ctx, path, nil)
	return st
}

func cleanLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		if labelName.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxLabels {
		keys = keys[:maxLabels]
	}
	for _, k := range keys {
		out[k] = Clean(in[k], 256)
	}
	return out
}
