// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kind names one declared observability source.
type Kind string

const (
	KindPrometheus   Kind = "prometheus"
	KindAlertmanager Kind = "alertmanager"
	KindGrafana      Kind = "grafana"
)

// Kinds lists every source kind in display order.
var Kinds = []Kind{KindPrometheus, KindAlertmanager, KindGrafana}

// Dashboard kinds accepted in GrafanaSource.Dashboards.
const (
	DashboardPod       = "pod"
	DashboardWorkload  = "workload"
	DashboardNode      = "node"
	DashboardNamespace = "namespace"
)

// DashboardKinds lists the accepted dashboard keys in display order.
var DashboardKinds = []string{DashboardPod, DashboardWorkload, DashboardNode, DashboardNamespace}

// Sources is the per-context declaration stored under k9s.observability in
// the context configuration file. Declaring a source contacts nothing.
type Sources struct {
	Prometheus   *Source        `json:"prometheus,omitempty" yaml:"prometheus,omitempty"`
	Alertmanager *Source        `json:"alertmanager,omitempty" yaml:"alertmanager,omitempty"`
	Grafana      *GrafanaSource `json:"grafana,omitempty" yaml:"grafana,omitempty"`
	// RED optionally maps HTTP request, error and duration metric names.
	RED *RED `json:"red,omitempty" yaml:"red,omitempty"`
	// RequestPath optionally maps the hops of a Service's request path
	// (ingress, app, dependencies, DNS, network) onto metric names.
	RequestPath *RequestPath `json:"requestPath,omitempty" yaml:"requestPath,omitempty"`
}

// Source says how one source is reached. Exactly one of Service and URL is set.
type Source struct {
	// Service is namespace/name:port or namespace/https:name:port, reached
	// through the Kubernetes API server service proxy with kube credentials.
	Service string `json:"service,omitempty" yaml:"service,omitempty"`
	// URL is a direct HTTP(S) base URL without userinfo, query or fragment.
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
	// TokenEnv names an environment variable that holds a bearer token for a
	// URL source. The value is read per request and never stored.
	TokenEnv string `json:"tokenEnv,omitempty" yaml:"tokenEnv,omitempty"`
}

// GrafanaSource adds browser links and dashboard mapping to a Source.
type GrafanaSource struct {
	Source `yaml:",inline"`
	// PublicURL is the browser-facing base URL used only to build links.
	PublicURL string `json:"publicURL,omitempty" yaml:"publicURL,omitempty"`
	// Datasource is the UID of the Grafana Prometheus datasource.
	Datasource string `json:"datasource,omitempty" yaml:"datasource,omitempty"`
	// LokiDatasource is the UID of a Grafana Loki datasource. It is used
	// only to build Explore log links and is never queried.
	LokiDatasource string `json:"lokiDatasource,omitempty" yaml:"lokiDatasource,omitempty"`
	// Dashboards maps pod, workload, node and namespace to a dashboard.
	Dashboards map[string]Dashboard `json:"dashboards,omitempty" yaml:"dashboards,omitempty"`
}

// Dashboard names one Grafana dashboard and its template variables.
type Dashboard struct {
	UID          string `json:"uid" yaml:"uid"`
	NamespaceVar string `json:"namespaceVar,omitempty" yaml:"namespaceVar,omitempty"`
	PodVar       string `json:"podVar,omitempty" yaml:"podVar,omitempty"`
	WorkloadVar  string `json:"workloadVar,omitempty" yaml:"workloadVar,omitempty"`
	NodeVar      string `json:"nodeVar,omitempty" yaml:"nodeVar,omitempty"`
}

// LinkTarget carries the Kubernetes names substituted into a dashboard link.
type LinkTarget struct {
	Namespace, Pod, Workload, Node string
}

// ServiceRef is a parsed Source.Service.
type ServiceRef struct {
	Namespace, Scheme, Name, Port string
}

var (
	dnsLabel   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	portName   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,13}[a-z0-9])?$`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	grafanaUID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	variable   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// ParseServiceRef parses namespace/name:port or namespace/scheme:name:port.
func ParseServiceRef(s string) (ServiceRef, error) {
	bad := errors.New("service must be namespace/name:port or namespace/https:name:port")
	ns, rest, ok := strings.Cut(s, "/")
	if !ok || len(s) > 600 {
		return ServiceRef{}, bad
	}
	parts := strings.Split(rest, ":")
	ref := ServiceRef{Namespace: ns}
	switch len(parts) {
	case 2:
		ref.Name, ref.Port = parts[0], parts[1]
	case 3:
		ref.Scheme, ref.Name, ref.Port = parts[0], parts[1], parts[2]
	default:
		return ServiceRef{}, bad
	}
	if ref.Scheme != "" && ref.Scheme != "http" && ref.Scheme != "https" {
		return ServiceRef{}, bad
	}
	if !dnsLabel.MatchString(ref.Namespace) || !dnsLabel.MatchString(ref.Name) || !portName.MatchString(ref.Port) {
		return ServiceRef{}, bad
	}
	return ref, nil
}

// String renders the reference the way it is declared.
func (r ServiceRef) String() string {
	if r.Scheme != "" {
		return r.Namespace + "/" + r.Scheme + ":" + r.Name + ":" + r.Port
	}
	return r.Namespace + "/" + r.Name + ":" + r.Port
}

// ProxyPath is the API server service-proxy path prefix, without a trailing slash.
func (r ServiceRef) ProxyPath() string {
	target := r.Name + ":" + r.Port
	if r.Scheme != "" {
		target = r.Scheme + ":" + target
	}
	return "/api/v1/namespaces/" + r.Namespace + "/services/" + target + "/proxy"
}

func parseBaseURL(raw string) (*url.URL, error) {
	bad := errors.New("use an HTTP(S) base URL without credentials, query or fragment")
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, bad
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate reports why a declaration cannot be used, or nil.
func (s *Source) Validate() error {
	if s == nil {
		return ErrNotConfigured
	}
	switch {
	case s.Service != "" && s.URL != "":
		return errors.New("declare service or url, not both")
	case s.Service != "":
		if s.TokenEnv != "" {
			return errors.New("tokenEnv applies to url sources only; a service source uses kube credentials")
		}
		_, err := ParseServiceRef(s.Service)
		return err
	case s.URL != "":
		u, err := parseBaseURL(s.URL)
		if err != nil {
			return err
		}
		if s.TokenEnv != "" {
			if !envName.MatchString(s.TokenEnv) {
				return errors.New("tokenEnv must be an environment variable name")
			}
			if u.Scheme != "https" && !loopback(u.Hostname()) {
				return errors.New("tokenEnv requires an https url (plain http is accepted for loopback only)")
			}
		}
		return nil
	}
	return errors.New("declare service or url")
}

// Display describes how the source is reached without any credential.
func (s *Source) Display() string {
	if s == nil {
		return ""
	}
	if s.Service != "" {
		return "service " + Clean(s.Service, 300)
	}
	if u, err := parseBaseURL(s.URL); err == nil {
		return "url " + u.String()
	}
	return "url (invalid)"
}

// Validate reports why a Grafana declaration cannot be used, or nil.
func (g *GrafanaSource) Validate() error {
	if g == nil {
		return ErrNotConfigured
	}
	if err := g.Source.Validate(); err != nil {
		return err
	}
	if g.PublicURL != "" {
		if _, err := parseBaseURL(g.PublicURL); err != nil {
			return fmt.Errorf("publicURL: %w", err)
		}
	}
	if g.Datasource != "" && !grafanaUID.MatchString(g.Datasource) {
		return errors.New("datasource must be a Grafana UID")
	}
	if g.LokiDatasource != "" && !grafanaUID.MatchString(g.LokiDatasource) {
		return errors.New("lokiDatasource must be a Grafana UID")
	}
	for kind, d := range g.Dashboards {
		known := false
		for _, k := range DashboardKinds {
			known = known || k == kind
		}
		if !known {
			return errors.New("dashboards keys are pod, workload, node and namespace")
		}
		if !grafanaUID.MatchString(d.UID) {
			return fmt.Errorf("dashboards.%s.uid must be a Grafana UID", kind)
		}
		for _, v := range []string{d.NamespaceVar, d.PodVar, d.WorkloadVar, d.NodeVar} {
			if v != "" && !variable.MatchString(v) {
				return fmt.Errorf("dashboards.%s variable names must be identifiers", kind)
			}
		}
	}
	return nil
}

// DashboardLink builds a browser link for one dashboard kind. It needs
// publicURL and a declared dashboard; it contacts nothing. Zero times omit
// the time range.
func (g *GrafanaSource) DashboardLink(kind string, t LinkTarget, from, to time.Time) (string, error) {
	return g.DashboardURL(kind, t, LinkRange{From: from, To: to})
}

// Declared reports whether any source is declared.
func (s *Sources) Declared() bool {
	return s != nil && (s.Prometheus != nil || s.Alertmanager != nil || s.Grafana != nil)
}

// Source returns the access declaration for a kind, or nil.
func (s *Sources) Source(kind Kind) *Source {
	if s == nil {
		return nil
	}
	switch kind {
	case KindPrometheus:
		return s.Prometheus
	case KindAlertmanager:
		return s.Alertmanager
	case KindGrafana:
		if s.Grafana != nil {
			return &s.Grafana.Source
		}
	}
	return nil
}

// Validate reports why the declaration for a kind cannot be used, or nil.
// An undeclared kind returns ErrNotConfigured.
func (s *Sources) Validate(kind Kind) error {
	if s == nil {
		return ErrNotConfigured
	}
	if kind == KindGrafana {
		return s.Grafana.Validate()
	}
	return s.Source(kind).Validate()
}

// Suggestion is a likely in-cluster source found by one explicit Service list.
type Suggestion struct {
	Kind    Kind
	Service string // namespace/name:port
	Snippet string // YAML to add under k9s.observability
}

// ServicePort is the part of a Kubernetes Service that Suggest needs.
type ServicePort struct {
	Namespace, Name string
	Ports           []int32
}

var wellKnown = []struct {
	kind  Kind
	port  int32
	names []string
}{
	{KindPrometheus, 9090, []string{"prometheus-operated", "kube-prometheus-stack-prometheus", "prometheus-k8s", "prometheus-server", "prometheus"}},
	{KindAlertmanager, 9093, []string{"alertmanager-operated", "kube-prometheus-stack-alertmanager", "alertmanager-main", "alertmanager"}},
	{KindGrafana, 3000, []string{"kube-prometheus-stack-grafana", "grafana"}},
}

// Suggest matches Services against well-known names. It is a name match
// only: a suggestion is a guess to review, never a detected source.
func Suggest(services []ServicePort) []Suggestion {
	var out []Suggestion
	for _, svc := range services {
		for _, w := range wellKnown {
			matched := false
			for _, n := range w.names {
				matched = matched || svc.Name == n
			}
			if !matched || len(svc.Ports) == 0 || !dnsLabel.MatchString(svc.Namespace) || !dnsLabel.MatchString(svc.Name) {
				continue
			}
			port := svc.Ports[0]
			for _, p := range svc.Ports {
				if p == w.port {
					port = p
				}
			}
			ref := fmt.Sprintf("%s/%s:%d", svc.Namespace, svc.Name, port)
			out = append(out, Suggestion{Kind: w.kind, Service: ref, Snippet: fmt.Sprintf("%s:\n  service: %s", w.kind, ref)})
		}
	}
	rank := map[Kind]int{KindPrometheus: 0, KindAlertmanager: 1, KindGrafana: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// Clean removes control characters and bounds text taken from a source so it
// can be shown. Callers still escape it for tview.
func Clean(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= limit {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
