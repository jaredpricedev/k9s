// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package probe times one read-only GET to every Pod behind a Service or
// workload, through the Kubernetes API server Pod proxy.
//
// It is the only active request in the product, so its rules are narrow.
// The path and port are those of the Pod's own declared readinessProbe or
// livenessProbe httpGet and are never typed; the method is always GET, with
// no body and no added header; every request has a deadline and a byte cap,
// and the body is thrown away. Building a Plan contacts no Pod: requests
// leave only from Execute, which the caller runs after the user confirmed.
//
// The time measured is API server to Pod, through the API server's own Pod
// proxy: not through the ingress and not through the Service. It locates a
// slow Pod, not a slow network path, and a difference between peers is a
// difference, not a cause.
package probe

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Limits of one probe. They are constants on purpose: nothing a user types
// can widen them.
const (
	// MaxPods is the number of Pods contacted; the rest are counted.
	MaxPods = 16
	// DefaultAttempts and MaxAttempts bound the GETs sent to one Pod.
	DefaultAttempts = 5
	MaxAttempts     = 30
	// DefaultDeadline bounds one request, from sending it to the answer.
	DefaultDeadline = 5 * time.Second
	// MaxResponseBytes is how much of a response body is read before the
	// rest is dropped. The body is never kept or shown.
	MaxResponseBytes = 512
	// Concurrency is how many Pods are probed at once. One Pod is always
	// asked one request after another.
	Concurrency = 4
	// Pause separates two requests to the same Pod.
	Pause = 200 * time.Millisecond
	// RunDeadline bounds a whole run; what is unsent by then stays unsent.
	RunDeadline = 5 * time.Minute
	// maxPath bounds a declared probe path.
	maxPath = 1024
)

// AttemptChoices are the per-Pod request counts a user can step through.
var AttemptChoices = []int{1, 3, 5, 10, 20, 30}

// Kind names which declared probe supplies the request.
type Kind string

const (
	Readiness Kind = "readiness"
	Liveness  Kind = "liveness"
)

const schemeHTTPS = "https"

// Kinds is the order of preference.
var Kinds = []Kind{Readiness, Liveness}

// Field is the Pod spec field of the kind, as written in a manifest.
func (k Kind) Field() string { return string(k) + "Probe" }

// HTTPGet is one declared httpGet probe of one container. It holds only what
// is needed to repeat the kubelet's own request: no header value is read.
type HTTPGet struct {
	Kind      Kind
	Container string
	// Scheme is "http" or "https".
	Scheme string
	// Port is the container port number as text. PortName is the declared
	// name when the probe names its port.
	Port, PortName string
	// Path is the declared path, with a query when one is declared.
	Path string
	// Headers counts declared httpHeaders. They are never sent.
	Headers int
	// Period is the kubelet's own period for this probe.
	Period time.Duration
	// Skip says why the probe cannot be repeated through the Pod proxy.
	Skip string
}

// Pod is the projection of one Pod that a probe needs. It carries no env,
// volume, annotation or header value.
type Pod struct {
	Namespace, Name, UID, Node, Zone, Phase string
	Ready, Terminating                      bool
	// Probes are the declared httpGet readiness and liveness probes, in
	// container order.
	Probes []HTTPGet
	// Other names declared probes of another type, such as "readiness exec".
	Other []string
}

// Request is one GET through the API server Pod proxy.
type Request struct {
	Namespace, Pod, Scheme, Port, Path string
	Kind                               Kind
	Container                          string
}

// ProxyTarget is the Pod proxy name segment: [https:]name:port.
func (r *Request) ProxyTarget() string {
	target := r.Pod + ":" + r.Port
	if r.Scheme == schemeHTTPS {
		target = schemeHTTPS + ":" + target
	}
	return target
}

// APIPath is the API server path of the request, without the query.
func (r *Request) APIPath() string {
	path, _, _ := strings.Cut(r.Path, "?")
	return "/api/v1/namespaces/" + r.Namespace + "/pods/" + r.ProxyTarget() + "/proxy" + path
}

// DisplayPath is the declared path for the screen. Query values are masked,
// because a probe path can carry a token.
func DisplayPath(path string) string {
	base, query, found := strings.Cut(path, "?")
	if !found || query == "" {
		return base
	}
	parts := strings.Split(query, "&")
	for i, part := range parts {
		if name, _, has := strings.Cut(part, "="); has {
			parts[i] = name + "=…"
		}
	}
	return base + "?" + strings.Join(parts, "&")
}

// Display is "GET :8080/-/readiness", with query values masked.
func (r *Request) Display() string {
	scheme := ""
	if r.Scheme == schemeHTTPS {
		scheme = schemeHTTPS + " "
	}
	return "GET " + scheme + ":" + r.Port + DisplayPath(r.Path)
}

// Target is one Pod of a plan: what would be sent to it, or why nothing is.
type Target struct {
	Pod Pod
	// Request is nil when the Pod is not contacted; Skip then says why.
	Request *Request
	Skip    string
	// HeadersOmitted counts declared probe headers that are not sent.
	HeadersOmitted int
	// Period is the kubelet's period for the chosen probe.
	Period time.Duration
}

// Access is what a self subject access review said about pods/proxy.
type Access int

const (
	// AccessUnknown means no review answered; a request may still be tried.
	AccessUnknown Access = iota
	AccessAllowed
	AccessDenied
)

// Scope names what was selected.
type Scope struct {
	// Kind is Service, Deployment, StatefulSet, DaemonSet, ReplicaSet or Pod.
	Kind, Namespace, Name, UID string
}

// Label is "Service gitlab/gitlab-webservice-default".
func (s Scope) Label() string { return s.Kind + " " + s.Namespace + "/" + s.Name }

// Plan is what a probe would send. Building it reads Kubernetes objects and
// contacts no Pod.
type Plan struct {
	Scope Scope
	// Kind is the probe kind in use; Available lists the kinds that at
	// least one Pod declares as httpGet.
	Kind      Kind
	Available []Kind
	Targets   []Target
	// Found is the number of Pods behind the selection; LeftOut of them
	// are beyond MaxPods and not listed.
	Found, LeftOut int
	// Notes are gaps of the read, such as a cut list or unread zones.
	Notes []string
	// Access and AccessReason report the pods/proxy access review.
	Access       Access
	AccessReason string
	ReadAt       time.Time
}

// MissingAccess names the RBAC rule a probe needs, for a denied message.
func MissingAccess(namespace string) string {
	return fmt.Sprintf("RBAC verb \"get\" on resource \"pods/proxy\" (core API group) in namespace %q", namespace)
}

func nestedSlice(object map[string]any, fields ...string) []map[string]any {
	items, _, _ := unstructured.NestedSlice(object, fields...)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func text(object map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(object, fields...)
	return value
}

// number reads an integer that a JSON or typed decoder may have left as
// int64, float64 or int.
func number(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case float64:
		if v == float64(int64(v)) {
			return int64(v), true
		}
	}
	return 0, false
}

func validPort(n int64) bool { return n >= 1 && n <= 65535 }

// usablePath says whether a declared path can be sent as it is written.
func usablePath(path string) bool {
	if !strings.HasPrefix(path, "/") || len(path) > maxPath {
		return false
	}
	for _, r := range path {
		if r <= ' ' || r == 0x7f || r == '#' {
			return false
		}
	}
	parsed, err := url.Parse(path)
	return err == nil && parsed.Host == "" && parsed.Scheme == ""
}

// httpGet projects one declared probe. It returns false when the container
// declares no probe of the kind, and names another probe type in other.
func httpGet(container map[string]any, kind Kind) (probe HTTPGet, other string, ok bool) {
	declared, found, _ := unstructured.NestedMap(container, kind.Field())
	if !found || declared == nil {
		return HTTPGet{}, "", false
	}
	get, found, _ := unstructured.NestedMap(declared, "httpGet")
	if !found || get == nil {
		for _, name := range []string{"exec", "tcpSocket", "grpc"} {
			if _, has := declared[name]; has {
				return HTTPGet{}, string(kind) + " " + name, false
			}
		}
		return HTTPGet{}, "", false
	}
	probe = HTTPGet{Kind: kind, Container: text(container, "name"), Scheme: "http", Path: text(get, "path")}
	if strings.EqualFold(text(get, "scheme"), "HTTPS") {
		probe.Scheme = schemeHTTPS
	}
	if probe.Path == "" {
		probe.Path = "/"
	}
	if headers, _, _ := unstructured.NestedSlice(get, "httpHeaders"); len(headers) > 0 {
		probe.Headers = len(headers)
	}
	if period, isNumber := number(declared["periodSeconds"]); isNumber && period > 0 {
		probe.Period = time.Duration(period) * time.Second
	} else {
		probe.Period = 10 * time.Second // the Kubernetes default
	}
	switch port := get["port"].(type) {
	case string:
		probe.PortName = port
		if n, err := strconv.ParseInt(port, 10, 32); err == nil {
			probe.PortName = ""
			if validPort(n) {
				probe.Port = strconv.FormatInt(n, 10)
			}
			break
		}
		for _, declaredPort := range nestedSlice(container, "ports") {
			if text(declaredPort, "name") != port {
				continue
			}
			if n, isNumber := number(declaredPort["containerPort"]); isNumber && validPort(n) {
				probe.Port = strconv.FormatInt(n, 10)
			}
		}
	default:
		if n, isNumber := number(port); isNumber && validPort(n) {
			probe.Port = strconv.FormatInt(n, 10)
		}
	}
	switch {
	case text(get, "host") != "":
		probe.Skip = "its " + kind.Field() + " names its own host, which the Pod proxy cannot reach"
	case probe.Port == "" && probe.PortName != "":
		probe.Skip = "its " + kind.Field() + " port " + strconv.Quote(probe.PortName) + " is not a port the container declares"
	case probe.Port == "":
		probe.Skip = "its " + kind.Field() + " declares no usable port"
	case !usablePath(probe.Path):
		probe.Skip = "its " + kind.Field() + " path cannot be sent as written"
	}
	return probe, "", true
}

// PodFromObject projects a Pod object. Init and ephemeral containers are
// ignored: only containers that serve have readiness and liveness probes.
func PodFromObject(object map[string]any) Pod {
	pod := Pod{
		Namespace: text(object, "metadata", "namespace"), Name: text(object, "metadata", "name"),
		UID: text(object, "metadata", "uid"), Node: text(object, "spec", "nodeName"), Phase: text(object, "status", "phase"),
		Terminating: text(object, "metadata", "deletionTimestamp") != "",
	}
	for _, condition := range nestedSlice(object, "status", "conditions") {
		if text(condition, "type") == "Ready" {
			pod.Ready = text(condition, "status") == "True"
		}
	}
	for _, container := range nestedSlice(object, "spec", "containers") {
		for _, kind := range Kinds {
			probe, other, ok := httpGet(container, kind)
			switch {
			case ok:
				pod.Probes = append(pod.Probes, probe)
			case other != "":
				pod.Other = append(pod.Other, other)
			}
		}
	}
	return pod
}

// probeOf is the Pod's first declared httpGet probe of the kind.
func (p *Pod) probeOf(kind Kind) *HTTPGet {
	for i := range p.Probes {
		if p.Probes[i].Kind == kind {
			return &p.Probes[i]
		}
	}
	return nil
}

// target decides what, if anything, is sent to one Pod.
func target(pod *Pod, kind Kind) Target {
	t := Target{Pod: *pod}
	switch {
	case pod.Terminating:
		t.Skip = "terminating; not contacted"
		return t
	case pod.Phase != "Running":
		phase := pod.Phase
		if phase == "" {
			phase = "unknown"
		}
		t.Skip = "phase " + phase + "; only a running Pod is contacted"
		return t
	}
	probe := pod.probeOf(kind)
	if probe == nil {
		t.Skip = "no declared HTTP probe"
		switch {
		case len(pod.Probes) > 0:
			t.Skip = "no " + kind.Field() + " httpGet; it declares a " + pod.Probes[0].Kind.Field()
		case len(pod.Other) > 0:
			t.Skip = "no declared HTTP probe (" + strings.Join(pod.Other, ", ") + " only)"
		}
		return t
	}
	if probe.Skip != "" {
		t.Skip = probe.Skip
		return t
	}
	t.Request = &Request{Namespace: pod.Namespace, Pod: pod.Name, Scheme: probe.Scheme, Port: probe.Port, Path: probe.Path,
		Kind: kind, Container: probe.Container}
	t.HeadersOmitted, t.Period = probe.Headers, probe.Period
	return t
}

// NewPlan chooses up to MaxPods Pods in name order and the first probe kind
// any of them declares. found is the number of Pods behind the selection,
// which can be more than were read.
func NewPlan(scope Scope, pods []Pod, found int) *Plan {
	sort.SliceStable(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	plan := &Plan{Scope: scope, Found: max(found, len(pods))}
	if len(pods) > MaxPods {
		pods = pods[:MaxPods]
	}
	plan.LeftOut = plan.Found - len(pods)
	for _, kind := range Kinds {
		for i := range pods {
			if probe := pods[i].probeOf(kind); probe != nil && probe.Skip == "" {
				plan.Available = append(plan.Available, kind)
				break
			}
		}
	}
	plan.Targets = make([]Target, len(pods))
	for i := range pods {
		plan.Targets[i].Pod = pods[i]
	}
	kind := Readiness
	if len(plan.Available) > 0 {
		kind = plan.Available[0]
	}
	plan.Select(kind)
	return plan
}

// Select switches the plan to another declared probe kind.
func (p *Plan) Select(kind Kind) {
	p.Kind = kind
	for i := range p.Targets {
		p.Targets[i] = target(&p.Targets[i].Pod, kind)
	}
}

// NextKind is the kind after the current one among those available, or the
// current kind when there is no other.
func (p *Plan) NextKind() Kind {
	for i, kind := range p.Available {
		if kind == p.Kind {
			return p.Available[(i+1)%len(p.Available)]
		}
	}
	if len(p.Available) > 0 {
		return p.Available[0]
	}
	return p.Kind
}

// Sendable is the number of Pods that would be contacted.
func (p *Plan) Sendable() int {
	count := 0
	for i := range p.Targets {
		if p.Targets[i].Request != nil {
			count++
		}
	}
	return count
}

// Requests lists the distinct requests of the plan as "GET :8080/path", in
// first-use order, with the number of Pods each goes to.
func (p *Plan) Requests() (requests []string, pods []int) {
	index := map[string]int{}
	for i := range p.Targets {
		request := p.Targets[i].Request
		if request == nil {
			continue
		}
		display := request.Display()
		at, seen := index[display]
		if !seen {
			at = len(requests)
			index[display] = at
			requests, pods = append(requests, display), append(pods, 0)
		}
		pods[at]++
	}
	return requests, pods
}

// Signature identifies exactly what a run would send: every request and the
// count. A confirmation given for one signature does not cover another.
func (p *Plan) Signature(attempts int) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(attempts))
	for i := range p.Targets {
		if request := p.Targets[i].Request; request != nil {
			b.WriteString("|" + p.Targets[i].Pod.UID + " " + request.Scheme + " " + request.ProxyTarget() + request.Path)
		}
	}
	return b.String()
}

// ClampAttempts keeps a count inside the allowed range.
func ClampAttempts(n int) int {
	if n < 1 {
		return DefaultAttempts
	}
	return min(n, MaxAttempts)
}
