// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

type collector struct {
	reader     dynamic.Interface
	snapshot   *Snapshot
	gateways   map[string]*unstructured.Unstructured
	namespaces map[string]*unstructured.Unstructured
	grants     map[string][]*unstructured.Unstructured
}

// Collect owns one captured destination and an explicit namespace subset. It
// reads configuration only; probes and flow collection require separate actions.
//
//nolint:gocritic // Keep the caller's scope immutable during asynchronous reads.
func Collect(ctx context.Context, reader dynamic.Interface, scope Scope) (*Snapshot, error) {
	scope, err := NormalizeScope(scope)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, fmt.Errorf("network path API reader unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	s := &Snapshot{Scope: scope, StartedAt: time.Now().UTC()}
	defer func() { s.CapturedAt = time.Now().UTC() }()
	c := collector{reader: reader, snapshot: s, gateways: make(map[string]*unstructured.Unstructured),
		namespaces: make(map[string]*unstructured.Unstructured), grants: make(map[string][]*unstructured.Unstructured)}
	object := c.get(ctx, ServiceGVR, scope.Service.Namespace, scope.Service.Name)
	if object == nil {
		return s, fmt.Errorf("captured Service read unavailable; inspect coverage")
	}
	if object.GetKind() != KindService || object.GetAPIVersion() != NativeAPI || string(object.GetUID()) != scope.Service.UID {
		s.Coverage = append(s.Coverage, Coverage{GVR: ServiceGVR, Namespace: scope.Service.Namespace, Name: scope.Service.Name,
			State: inspect.ObservationUnknown, Detail: "Service UID differs from captured target; replacement not used", CapturedAt: time.Now().UTC()})
		return s, fmt.Errorf("captured Service identity changed; reopen the current Service before reviewing its paths")
	}
	s.projectService(object, time.Now().UTC())
	if len(s.Service.Selector) > 0 {
		for _, pod := range c.list(ctx, PodGVR, scope.Service.Namespace, labels.SelectorFromSet(s.Service.Selector).String()) {
			if labels.SelectorFromSet(s.Service.Selector).Matches(labels.Set(pod.GetLabels())) {
				s.projectPod(pod, time.Now().UTC())
			}
		}
	} else {
		s.Coverage = append(s.Coverage, Coverage{GVR: PodGVR, Namespace: scope.Service.Namespace, State: inspect.ObservationUnknown,
			Detail: "Service has no selector; no broad Pod list attempted. Verified EndpointSlice target UIDs can supply bounded Pod reads", CapturedAt: time.Now().UTC()})
	}
	for _, slice := range c.list(ctx, "discovery.k8s.io/v1/endpointslices", scope.Service.Namespace,
		labels.SelectorFromSet(map[string]string{"kubernetes.io/service-name": scope.Service.Name}).String()) {
		if slice.GetLabels()["kubernetes.io/service-name"] == scope.Service.Name {
			s.projectEndpointSlice(slice, time.Now().UTC())
		}
	}
	c.readEndpointPods(ctx)
	for _, ns := range scope.RouteNamespaces {
		for _, ingress := range c.list(ctx, "networking.k8s.io/v1/ingresses", ns, "") {
			s.projectIngress(ingress, time.Now().UTC())
		}
		for _, route := range c.list(ctx, "gateway.networking.k8s.io/v1/httproutes", ns, "") {
			c.projectHTTPRoute(ctx, route)
		}
	}
	for _, policy := range c.list(ctx, "networking.k8s.io/v1/networkpolicies", scope.Service.Namespace, "") {
		s.projectPolicy(policy, time.Now().UTC())
	}
	s.CapturedAt = time.Now().UTC()
	return s, ctx.Err()
}

func gvr(value string) schema.GroupVersionResource {
	p := strings.Split(value, "/")
	if len(p) == 2 {
		return schema.GroupVersionResource{Version: p[0], Resource: p[1]}
	}
	return schema.GroupVersionResource{Group: p[0], Version: p[1], Resource: p[2]}
}

func coverageError(err error) string {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return inspect.ObservationDenied
	}
	if apierrors.IsNotFound(err) {
		return StateAbsent
	}
	return inspect.ObservationUnknown
}

func validObject(object *unstructured.Unstructured, resource, namespace string) bool {
	if object == nil || object.GetUID() == "" || object.GetName() == "" || object.GetNamespace() != namespace {
		return false
	}
	identity := gvr(resource)
	apiVersion := identity.Version
	if identity.Group != "" {
		apiVersion = identity.Group + "/" + identity.Version
	}
	kinds := map[string]string{"services": KindService, "pods": "Pod", "endpointslices": "EndpointSlice", "ingresses": "Ingress",
		"httproutes": KindHTTPRoute, "gateways": "Gateway", "referencegrants": "ReferenceGrant", "networkpolicies": "NetworkPolicy", "namespaces": "Namespace"}
	return object.GetAPIVersion() == apiVersion && object.GetKind() == kinds[identity.Resource]
}

func (c *collector) get(ctx context.Context, resource, namespace, name string) *unstructured.Unstructured {
	coverage := Coverage{GVR: resource, Namespace: namespace, Name: name, CapturedAt: time.Now().UTC()}
	defer func() { c.snapshot.Coverage = append(c.snapshot.Coverage, coverage) }()
	if len(validation.IsDNS1123Subdomain(name)) != 0 || resource != "v1/namespaces" && len(validation.IsDNS1123Label(namespace)) != 0 {
		coverage.State, coverage.Detail = inspect.ObservationUnknown, "Invalid explicit resource reference; no wildcard or all-namespace read attempted"
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		coverage.State, coverage.Detail = StateCanceled, safe(err.Error())
		return nil
	}
	object, err := c.reader.Resource(gvr(resource)).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	coverage.CapturedAt = time.Now().UTC()
	if ctx.Err() != nil {
		coverage.State, coverage.Detail = StateCanceled, safe(ctx.Err().Error())
		return nil
	}
	if err != nil {
		coverage.State, coverage.Detail = coverageError(err), safe(err.Error())
		return nil
	}
	if !validObject(object, resource, namespace) || object.GetName() != name {
		coverage.State, coverage.Detail = inspect.ObservationUnknown, "Returned API/kind/namespace/name/UID does not match the explicit reference"
		return nil
	}
	coverage.State, coverage.Detail = inspect.ObservationComplete, "Exact referenced current API object obtained; configuration/status is not a connectivity test"
	return object
}

func (c *collector) list(ctx context.Context, resource, namespace, selector string) []*unstructured.Unstructured {
	coverage := Coverage{GVR: resource, Namespace: namespace, CapturedAt: time.Now().UTC()}
	defer func() { c.snapshot.Coverage = append(c.snapshot.Coverage, coverage) }()
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	objects := make([]*unstructured.Unstructured, 0)
	continuation, invalid := "", 0
	for range MaxPages {
		if err := ctx.Err(); err != nil {
			coverage.State, coverage.Detail = StateCanceled, safe(err.Error())
			return objects
		}
		page, err := c.reader.Resource(gvr(resource)).Namespace(namespace).List(ctx, metav1.ListOptions{
			Limit: PageSize, Continue: continuation, LabelSelector: selector})
		coverage.CapturedAt = time.Now().UTC()
		if ctx.Err() != nil {
			coverage.State, coverage.Detail = StateCanceled, safe(ctx.Err().Error())
			return objects
		}
		if err != nil {
			coverage.State, coverage.Detail = coverageError(err), safe(err.Error())
			return objects
		}
		if page == nil {
			coverage.State, coverage.Detail = inspect.ObservationUnknown, "API returned no list response"
			return objects
		}
		for i := range page.Items[:min(PageSize, len(page.Items))] {
			object := &page.Items[i]
			if validObject(object, resource, namespace) {
				objects = append(objects, object.DeepCopy())
			} else {
				invalid++
			}
		}
		if len(page.Items) > PageSize {
			coverage.State, coverage.Detail = StateTruncated, "Server ignored the 64-candidate page limit"
			return objects
		}
		continuation = page.GetContinue()
		if continuation == "" {
			coverage.State, coverage.Detail = inspect.ObservationComplete, fmt.Sprintf("%d current records obtained in explicit namespace", len(objects))
			if invalid > 0 {
				coverage.State, coverage.Detail = inspect.ObservationUnknown, fmt.Sprintf("%d records missing matching identity excluded", invalid)
			}
			return objects
		}
	}
	coverage.State, coverage.Detail = StateTruncated, "Two-page namespace query limit reached; additional records not scanned"
	return objects
}

func (c *collector) readEndpointPods(ctx context.Context) {
	known := make(map[string]string)
	for i := range c.snapshot.Pods {
		p := &c.snapshot.Pods[i]
		known[p.Source.Identity.Name] = p.Source.Identity.UID
	}
	targets := make(map[string]inspect.ResourceIdentity)
	for i := range c.snapshot.Items {
		for _, ref := range c.snapshot.Items[i].Related {
			if ref.GVR == PodGVR && known[ref.Name] != ref.UID {
				targets[ref.Name+"/"+ref.UID] = ref
			}
		}
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 16 {
		c.snapshot.Omitted += len(keys) - 16
	}
	for _, key := range keys[:min(16, len(keys))] {
		ref := targets[key]
		object := c.get(ctx, PodGVR, ref.Namespace, ref.Name)
		if object != nil && string(object.GetUID()) == ref.UID {
			c.snapshot.projectPod(object, time.Now().UTC())
			known[ref.Name] = ref.UID
		} else {
			c.snapshot.Coverage = append(c.snapshot.Coverage, Coverage{GVR: PodGVR, Namespace: ref.Namespace, Name: ref.Name,
				State: inspect.ObservationUnknown, Detail: "Endpoint target UID not verified; replacement/unavailable Pod cannot stand in for captured endpoint",
				CapturedAt: time.Now().UTC()})
		}
	}
	for i := range c.snapshot.Items {
		item := &c.snapshot.Items[i]
		for _, ref := range item.Related {
			if ref.GVR != PodGVR {
				continue
			}
			verified := false
			for j := range c.snapshot.Pods {
				p := &c.snapshot.Pods[j]
				if p.Source.Identity.UID == ref.UID && p.Source.Identity.Name == ref.Name {
					item.fact("Verified target Pod", ref.Name+" UID="+ref.UID+" Ready="+p.Ready+" phase="+p.Phase)
					verified = true
				}
			}
			if !verified {
				item.gap("Endpoint target Pod identity/readiness unavailable; retained target reference does not prove current Pod membership")
			}
		}
	}
}
