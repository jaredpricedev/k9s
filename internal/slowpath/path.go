// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package slowpath gathers the evidence that locates an intermittent hang
// on the request path of one Service: which hop was raised, whether one Pod
// differs from its peers, and when. It resolves the path with the
// :network-review collector, plans one bounded batch of range reads from
// the request-path profiles, applies a documented rule per signal, turns
// crossings into intervals and states how the intervals overlap. It never
// names a cause: two things that happen in the same minutes overlap.
package slowpath

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/k9s/internal/observability"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Kinds a request path can be resolved from.
const (
	KindService     = "Service"
	KindIngress     = "Ingress"
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindPod         = "Pod"
)

// Read bounds of path resolution, on top of those of networkpath.Collect.
const (
	// ReadTimeout bounds each single Kubernetes read made here.
	ReadTimeout = 3 * time.Second
	// MaxServices bounds the one Service list that finds the Service behind
	// a workload or a Pod.
	MaxServices = 200
	// MaxDependencyPods bounds the Pods named for one dependency.
	MaxDependencyPods = 16
)

var (
	servicesResource  = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	podsResource      = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	ingressesResource = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	slicesResource    = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	workloadResources = map[string]schema.GroupVersionResource{
		KindDeployment:  {Group: "apps", Version: "v1", Resource: "deployments"},
		KindStatefulSet: {Group: "apps", Version: "v1", Resource: "statefulsets"},
		KindDaemonSet:   {Group: "apps", Version: "v1", Resource: "daemonsets"},
	}
)

// Selection is the object the user had selected when the view was opened.
type Selection struct {
	Context, Kind, Namespace, Name, UID string
}

// Label is "Service gitlab/gitlab-webservice-default".
func (s *Selection) Label() string { return s.Kind + " " + s.Namespace + "/" + s.Name }

// Pod is one backend of the Service, as the Kubernetes API reports it now.
type Pod struct {
	Name, UID, Node string
	// Ready is the Pod's Ready condition as reported ("True", "False").
	Ready string
	// ReadySince is when the Ready condition last changed; Created is when
	// the Pod was created. A zero time was not reported.
	ReadySince, Created time.Time
	Restarts            int64
	LastRestart         time.Time
	// Endpoint is "ready", "not ready" or "unknown" as an EndpointSlice of
	// the Service reports it, and empty when no slice names the Pod.
	Endpoint string
}

// Ingress is one Ingress whose backend is the Service.
type Ingress struct{ Name, Entry string }

// Event is one dated fact from the Kubernetes API, such as a rollout.
type Event struct {
	At    time.Time
	Label string
}

// Path is the configured request path of one Service, resolved once.
type Path struct {
	Selection                               Selection
	Context, Namespace, Service, ServiceUID string
	Ingresses                               []Ingress
	Pods                                    []Pod
	// Endpoints counts the EndpointSlice records of the Service and how
	// many of them are ready; EndpointsRead is false when the slices could
	// not be listed.
	Endpoints, EndpointsReady int
	EndpointsRead             bool
	// Dependencies maps a declared dependency Service (namespace/name) to
	// the Pods its EndpointSlices name.
	Dependencies map[string][]string
	// Workload controls the Pods, when it could be found; Rollouts are its
	// retained revisions, and RolloutsNote says why none were read.
	Workload     impact.Workload
	Rollouts     []Event
	RolloutsNote string
	// Focus is the selected Pod, when a Pod was selected.
	Focus string
	// Notes say what was assumed or could not be read.
	Notes  []string
	ReadAt time.Time
}

// Key is namespace/name of the Service: the key of its declaration.
func (p *Path) Key() string { return p.Namespace + "/" + p.Service }

// PodNames lists the backend Pods by name.
func (p *Path) PodNames() []string {
	names := make([]string, 0, len(p.Pods))
	for i := range p.Pods {
		names = append(names, p.Pods[i].Name)
	}
	return names
}

func (p *Path) notef(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	for _, existing := range p.Notes {
		if existing == text {
			return
		}
	}
	if len(p.Notes) < 16 {
		p.Notes = append(p.Notes, text)
	}
}

func readError(what string, err error) error {
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return errors.New(what + ": not permitted to read it")
	case apierrors.IsNotFound(err):
		return errors.New(what + " was not found")
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err):
		return errors.New(what + ": the read timed out")
	case errors.Is(err, context.Canceled):
		return errors.New(what + ": the read was canceled")
	}
	return errors.New(what + " could not be read")
}

func get(ctx context.Context, objects dynamic.Interface, resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	return objects.Resource(resource).Namespace(namespace).Get(readCtx, name, metav1.GetOptions{})
}

// Resolve finds the Service whose request path the selection is on and
// reads its configured path once: the Service, its EndpointSlices and Pods
// and the Ingresses that route to it (networkpath.Collect), the Pods behind
// each declared dependency, and the retained revisions of the workload.
// Every read is bounded and nothing is changed. sources may be nil.
//
//nolint:gocritic // The selection is copied into the path, which outlives the call.
func Resolve(ctx context.Context, objects dynamic.Interface, sources *observability.Sources, sel Selection) (*Path, error) {
	if objects == nil {
		return nil, errors.New("no Kubernetes connection")
	}
	if sel.Context == "" || sel.Namespace == "" || sel.Name == "" {
		return nil, errors.New("select a Service, an Ingress, a Deployment, a StatefulSet or a Pod first")
	}
	path := &Path{Selection: sel, Context: sel.Context, Namespace: sel.Namespace, Dependencies: map[string][]string{}}
	if err := path.findService(ctx, objects, sources); err != nil {
		return nil, err
	}
	snapshot, err := networkpath.Collect(ctx, objects, networkpath.Scope{Service: inspect.ResourceIdentity{
		Context: sel.Context, GVR: networkpath.ServiceGVR, Namespace: path.Namespace, Name: path.Service, UID: path.ServiceUID}})
	if snapshot == nil || snapshot.Service.Source.Identity.UID == "" {
		if err == nil {
			err = errors.New("the Service could not be read")
		}
		return nil, fmt.Errorf("Service %s: %s", path.Key(), err.Error())
	}
	if err != nil {
		path.notef("the configured path is incomplete: %s", err.Error())
	}
	path.fromSnapshot(snapshot)
	path.readDependencies(ctx, objects, sources)
	path.readRollouts(ctx, objects)
	path.ReadAt = time.Now().UTC()
	return path, nil
}

// findService sets Service and ServiceUID from the selection.
func (p *Path) findService(ctx context.Context, objects dynamic.Interface, sources *observability.Sources) error {
	sel := p.Selection
	switch sel.Kind {
	case KindService:
		p.Service, p.ServiceUID = sel.Name, sel.UID
	case KindIngress:
		ingress, err := get(ctx, objects, ingressesResource, sel.Namespace, sel.Name)
		if err != nil {
			return readError(sel.Label(), err)
		}
		backends := ingressBackends(ingress)
		if len(backends) == 0 {
			return errors.New(sel.Label() + " names no backend Service, so there is no request path to read")
		}
		p.Service = preferred(backends, sel.Namespace, sources)
		if len(backends) > 1 {
			p.notef("%s routes to %d Services; this reads %s (others: %s)", sel.Label(), len(backends), p.Service, others(backends, p.Service))
		}
	case KindDeployment, KindStatefulSet, KindDaemonSet, KindPod:
		resource, what := podsResource, []string{"metadata", "labels"}
		if sel.Kind != KindPod {
			resource, what = workloadResources[sel.Kind], []string{"spec", "template", "metadata", "labels"}
			p.Workload = impact.Workload{Kind: sel.Kind, Namespace: sel.Namespace, Name: sel.Name}
		} else {
			p.Focus = sel.Name
		}
		object, err := get(ctx, objects, resource, sel.Namespace, sel.Name)
		if err != nil {
			return readError(sel.Label(), err)
		}
		podLabels, _, _ := unstructured.NestedStringMap(object.Object, what...)
		names, err := p.selectingServices(ctx, objects, podLabels)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("no Service in namespace %s selects the Pods of %s, so there is no request path to read; :graph shows its resource use",
				sel.Namespace, sel.Label())
		}
		p.Service = preferred(names, sel.Namespace, sources)
		if len(names) > 1 {
			p.notef("%d Services select these Pods; this reads %s (others: %s)", len(names), p.Service, others(names, p.Service))
		}
	default:
		return errors.New("select a Service, an Ingress, a Deployment, a StatefulSet or a Pod: a request path belongs to a Service")
	}
	if p.ServiceUID == "" {
		service, err := get(ctx, objects, servicesResource, p.Namespace, p.Service)
		if err != nil {
			return readError("Service "+p.Key(), err)
		}
		p.ServiceUID = string(service.GetUID())
	}
	if p.ServiceUID == "" {
		return errors.New("Service " + p.Key() + " reported no UID, so its path cannot be pinned to it")
	}
	return nil
}

func ingressBackends(ingress *unstructured.Unstructured) []string {
	var names []string
	add := func(backend map[string]any) {
		name, _, _ := unstructured.NestedString(backend, "service", "name")
		for _, existing := range names {
			if existing == name {
				return
			}
		}
		if name != "" && len(names) < 32 {
			names = append(names, name)
		}
	}
	rules, _, _ := unstructured.NestedSlice(ingress.Object, "spec", "rules")
	for _, raw := range rules[:min(32, len(rules))] {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		paths, _, _ := unstructured.NestedSlice(rule, "http", "paths")
		for _, rawPath := range paths[:min(16, len(paths))] {
			if entry, ok := rawPath.(map[string]any); ok {
				backend, _, _ := unstructured.NestedMap(entry, "backend")
				add(backend)
			}
		}
	}
	if backend, found, _ := unstructured.NestedMap(ingress.Object, "spec", "defaultBackend"); found {
		add(backend)
	}
	return names
}

// selectingServices lists the Services of the namespace once and returns
// those whose selector matches the Pod labels, sorted by name.
func (p *Path) selectingServices(ctx context.Context, objects dynamic.Interface, podLabels map[string]string) ([]string, error) {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	list, err := objects.Resource(servicesResource).Namespace(p.Namespace).List(readCtx, metav1.ListOptions{Limit: MaxServices})
	if err != nil {
		return nil, readError("Services in namespace "+p.Namespace, err)
	}
	if list.GetContinue() != "" {
		p.notef("only the first %d Services of namespace %s were read to find the one that selects these Pods", len(list.Items), p.Namespace)
	}
	var names []string
	for i := range list.Items {
		selector, _, _ := unstructured.NestedStringMap(list.Items[i].Object, "spec", "selector")
		if len(selector) > 0 && labels.SelectorFromSet(selector).Matches(labels.Set(podLabels)) {
			names = append(names, list.Items[i].GetName())
		}
	}
	sort.Strings(names)
	return names, nil
}

// preferred picks the Service to read: the first one with a request-path
// declaration, else the first in the order given.
func preferred(names []string, namespace string, sources *observability.Sources) string {
	if sources != nil && sources.RequestPath != nil {
		for _, name := range names {
			if _, declared := sources.RequestPath.Services[namespace+"/"+name]; declared {
				return name
			}
		}
	}
	return names[0]
}

func others(names []string, chosen string) string {
	var rest []string
	for _, name := range names {
		if name != chosen {
			rest = append(rest, name)
		}
	}
	if len(rest) > 4 {
		rest = append(rest[:4], fmt.Sprintf("+%d more", len(rest)-4))
	}
	return strings.Join(rest, ", ")
}

func factOf(item *networkpath.Item, name string) string {
	for _, fact := range item.Facts {
		if fact.Name == name {
			return fact.Value
		}
	}
	return ""
}

// fromSnapshot takes Pods, endpoint readiness and Ingresses from what the
// :network-review collector read.
func (p *Path) fromSnapshot(snapshot *networkpath.Snapshot) {
	endpoint := map[string]string{}
	nodes := map[string]string{}
	seenIngress := map[string]bool{}
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		switch item.Group {
		case networkpath.GroupBackends:
			if !strings.HasPrefix(item.State, "ready ") {
				continue
			}
			p.Endpoints++
			state := "unknown"
			switch item.State {
			case "ready true":
				state = "ready"
				p.EndpointsReady++
			case "ready false":
				state = "not ready"
			}
			for _, ref := range item.Related {
				if ref.GVR == networkpath.PodGVR {
					endpoint[ref.Name] = state
					if node := factOf(item, "Node"); node != "" && !strings.HasPrefix(node, "unknown") {
						nodes[ref.Name] = node
					}
				}
			}
		case networkpath.GroupRoutes:
			name := item.Source.Identity.Name
			switch item.Source.Kind {
			case KindIngress:
				if !seenIngress[name] {
					seenIngress[name] = true
					p.Ingresses = append(p.Ingresses, Ingress{Name: name, Entry: strings.TrimSpace(factOf(item, "Configured entrypoint"))})
				}
			case networkpath.KindHTTPRoute:
				p.notef("HTTPRoute %s routes to this Service; no ingress profile reads a Gateway, so that entry is not read", name)
			}
		}
	}
	if p.Selection.Kind == KindIngress {
		// The selected Ingress is the one to read, first in the list.
		sort.SliceStable(p.Ingresses, func(a, b int) bool {
			return p.Ingresses[a].Name == p.Selection.Name && p.Ingresses[b].Name != p.Selection.Name
		})
	}
	for i := range snapshot.Pods {
		pod := &snapshot.Pods[i]
		name := pod.Source.Identity.Name
		node := pod.Node
		if node == "" {
			node = nodes[name]
		}
		p.Pods = append(p.Pods, Pod{Name: name, UID: pod.Source.Identity.UID, Node: node, Ready: pod.Ready, ReadySince: pod.ReadySince,
			Created: pod.Created, Restarts: pod.Restarts, LastRestart: pod.LastRestart, Endpoint: endpoint[name]})
	}
	sort.SliceStable(p.Pods, func(a, b int) bool { return p.Pods[a].Name < p.Pods[b].Name })
	for i := range snapshot.Coverage {
		coverage := &snapshot.Coverage[i]
		complete := coverage.State == inspect.ObservationComplete
		switch coverage.GVR {
		case "discovery.k8s.io/v1/endpointslices":
			p.EndpointsRead = p.EndpointsRead || complete
			if !complete {
				p.notef("EndpointSlices of %s: %s", p.Service, coverage.State)
			}
		case networkpath.PodGVR:
			if !complete && coverage.Name == "" {
				p.notef("Pods of %s: %s", p.Service, coverage.State)
			}
		case "networking.k8s.io/v1/ingresses":
			if !complete {
				p.notef("Ingresses in %s: %s", coverage.Namespace, coverage.State)
			}
		}
	}
	if snapshot.Omitted > 0 {
		p.notef("%d path records were over the read limit and are not counted", snapshot.Omitted)
	}
}

// readDependencies names the Pods behind each declared dependency from its
// EndpointSlices: one bounded list per dependency. A dependency whose Pods
// cannot be named is still read, for its whole namespace.
func (p *Path) readDependencies(ctx context.Context, objects dynamic.Interface, sources *observability.Sources) {
	for _, dep := range sources.PathDependencies(p.Key()) {
		namespace, name, ok := strings.Cut(dep.Service, "/")
		if !ok || namespace == "" || name == "" {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
		list, err := objects.Resource(slicesResource).Namespace(namespace).List(readCtx, metav1.ListOptions{
			Limit: 64, LabelSelector: labels.SelectorFromSet(map[string]string{"kubernetes.io/service-name": name}).String()})
		cancel()
		if err != nil {
			p.notef("%s", readError("EndpointSlices of dependency "+dep.Service, err).Error()+"; its profile reads the whole namespace")
			continue
		}
		seen := map[string]bool{}
		var pods []string
		for i := range list.Items {
			if list.Items[i].GetLabels()["kubernetes.io/service-name"] != name {
				continue
			}
			endpoints, _, _ := unstructured.NestedSlice(list.Items[i].Object, "endpoints")
			for _, raw := range endpoints {
				endpoint, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				kind, _, _ := unstructured.NestedString(endpoint, "targetRef", "kind")
				pod, _, _ := unstructured.NestedString(endpoint, "targetRef", "name")
				if kind == KindPod && pod != "" && !seen[pod] && len(pods) < MaxDependencyPods {
					seen[pod] = true
					pods = append(pods, pod)
				}
			}
		}
		sort.Strings(pods)
		p.Dependencies[dep.Service] = pods
	}
}

// readRollouts reads the retained revisions of the workload that controls
// the Pods: the reads :impact makes. Their creation times are the rollouts.
func (p *Path) readRollouts(ctx context.Context, objects dynamic.Interface) {
	if p.Workload.Name == "" {
		if len(p.Pods) == 0 {
			p.RolloutsNote = "no Pod is known, so no workload"
			return
		}
		workload, err := impact.Resolve(ctx, objects, impact.KindPod, p.Namespace, p.Pods[0].Name)
		if err != nil {
			p.RolloutsNote = err.Error()
			return
		}
		p.Workload = workload
	}
	history, err := impact.ReadHistory(ctx, objects, p.Workload)
	if err != nil {
		p.RolloutsNote = err.Error()
		return
	}
	for i := range history.Revisions {
		revision := &history.Revisions[i]
		if !revision.Created.IsZero() {
			p.Rollouts = append(p.Rollouts, Event{At: revision.Created.UTC(), Label: fmt.Sprintf("rev %d", revision.Number)})
		}
	}
	if history.Incomplete != "" {
		p.RolloutsNote = history.Incomplete
	}
}
