// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

func (s *Snapshot) projectIngress(object *unstructured.Unstructured, at time.Time) {
	if object.GetNamespace() != s.Scope.Service.Namespace {
		return
	}
	base := Item{Source: source(object, "networking.k8s.io/v1/ingresses", s.Scope.Service.Context, at), Group: GroupRoutes, State: StateConfigured}
	base.fact("Ingress class", scalar(object.Object, "spec", "ingressClassName"))
	base.fact("Boundary", "Configured route; controller admission, TLS, DNS and live connectivity are separate evidence")
	if backend, found, _ := unstructured.NestedMap(object.Object, "spec", "defaultBackend"); found {
		s.addIngressBackend(&base, backend, "default backend")
	}
	rules, _, _ := unstructured.NestedSlice(object.Object, "spec", "rules")
	s.Omitted += max(0, len(rules)-32)
	for _, raw := range rules[:min(32, len(rules))] {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		paths, _, _ := unstructured.NestedSlice(rule, "http", "paths")
		s.Omitted += max(0, len(paths)-16)
		for _, rawPath := range paths[:min(16, len(paths))] {
			p, ok := rawPath.(map[string]any)
			if !ok {
				continue
			}
			backend, _, _ := unstructured.NestedMap(p, "backend")
			s.addIngressBackend(&base, backend, text(rule, "host")+" "+scalar(p, "path")+" ("+scalar(p, "pathType")+")")
		}
	}
}

func (s *Snapshot) addIngressBackend(base *Item, backend map[string]any, entrypoint string) {
	if text(backend, "service", "name") != s.Scope.Service.Name {
		return
	}
	item := *base
	item.Facts = append([]Fact(nil), base.Facts...)
	item.Summary = entrypoint + " → Service " + s.Scope.Service.Name
	item.Related = []inspect.ResourceIdentity{s.Scope.Service}
	item.fact("Configured entrypoint", entrypoint)
	port := text(backend, "service", "port", "name")
	if port == "" {
		port = scalar(backend, "service", "port", "number")
	}
	item.fact("Backend Service port", port+" · "+s.servicePortEvidence(port))
	s.add(&item)
}

func (s *Snapshot) servicePortEvidence(value string) string {
	for _, p := range s.Service.Ports {
		if value == p.Name || value == fmt.Sprint(p.Number) {
			return "matches obtained Service declaration; targetPort " + p.Target
		}
	}
	return "not matched to an obtained Service port; configuration is unresolved"
}

func (c *collector) projectHTTPRoute(ctx context.Context, object *unstructured.Unstructured) {
	rules, _, _ := unstructured.NestedSlice(object.Object, "spec", "rules")
	c.snapshot.Omitted += max(0, len(rules)-16)
	for _, raw := range rules[:min(16, len(rules))] {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		backends, _, _ := unstructured.NestedSlice(rule, "backendRefs")
		c.snapshot.Omitted += max(0, len(backends)-16)
		for _, rawBackend := range backends[:min(16, len(backends))] {
			backend, ok := rawBackend.(map[string]any)
			if !ok {
				continue
			}
			ns := text(backend, "namespace")
			if ns == "" {
				ns = object.GetNamespace()
			}
			if text(backend, "name") != c.snapshot.Scope.Service.Name || ns != c.snapshot.Scope.Service.Namespace {
				continue
			}
			item := Item{Source: source(object, "gateway.networking.k8s.io/v1/httproutes", c.snapshot.Scope.Service.Context, time.Now().UTC()),
				Group: GroupRoutes, State: StateConfigured, Summary: object.GetName() + " → Service " + ns + "/" + text(backend, "name"),
				Related: []inspect.ResourceIdentity{c.snapshot.Scope.Service}}
			item.fact("Hostnames", strings.Join(stringsAt(object.Object, "spec", "hostnames"), ", "))
			matches, _, _ := unstructured.NestedSlice(rule, "matches")
			if len(matches) > 8 {
				c.snapshot.Omitted += len(matches) - 8
				item.gap("Additional HTTP matches omitted; route projection is incomplete")
			}
			for _, rawMatch := range matches[:min(8, len(matches))] {
				if match, ok := rawMatch.(map[string]any); ok {
					item.fact("Configured HTTP match", scalar(match, "path", "type")+" "+scalar(match, "path", "value")+" method "+scalar(match, "method"))
				}
			}
			item.fact("Backend port", scalar(backend, "port")+" · "+c.snapshot.servicePortEvidence(scalar(backend, "port")))
			item.fact("Configured weight", scalar(backend, "weight")+"; weight 0 does not send requests to this backend")
			kind, group := text(backend, "kind"), text(backend, "group")
			if (kind != "" && kind != KindService) || group != "" {
				item.State = inspect.ObservationUnknown
				item.gap("Unsupported backend group/kind; Service reference not established")
				item.fact("Backend kind", "Unsupported "+group+"/"+kind+"; no Service binding established")
			} else if ns != object.GetNamespace() {
				grant := c.referenceGrant(ctx, object.GetNamespace(), ns, text(backend, "name"))
				item.fact("Cross-namespace backend authorization", grant)
				if !strings.HasPrefix(grant, "Matching current") {
					item.gap(grant)
				}
			} else {
				item.fact("Backend namespace", "Same namespace; configuration link only")
			}
			c.routeParents(ctx, object, &item)
			item.fact("Boundary", "Parent conditions, allowedRoutes and ReferenceGrant configuration do not prove a successful request")
			c.snapshot.add(&item)
		}
	}
}

func (c *collector) routeParents(ctx context.Context, route *unstructured.Unstructured, item *Item) {
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if len(parents) == 0 {
		item.fact("Gateway parents", "No parent reference reported; attachment unavailable")
		item.gap("Gateway parent reference not reported")
	}
	for _, raw := range parents[:min(8, len(parents))] {
		parent, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ns := text(parent, "namespace")
		if ns == "" {
			ns = route.GetNamespace()
		}
		name := text(parent, "name")
		group, kind := text(parent, "group"), text(parent, "kind")
		if (group != "" && group != GatewayGroup) || (kind != "" && kind != "Gateway") {
			item.fact("Parent "+ns+"/"+name, "Unsupported parent group/kind; attachment unknown")
			item.gap("Unsupported parent group/kind")
			continue
		}
		gateway := c.gateway(ctx, ns, name)
		if gateway == nil {
			item.fact("Parent "+ns+"/"+name, "Gateway unavailable; admission/listener/allowedRoutes unknown")
			item.gap("Gateway " + ns + "/" + name + " unavailable; admission/listener/allowedRoutes unknown")
			continue
		}
		item.Related = append(item.Related, source(gateway, "gateway.networking.k8s.io/v1/gateways", c.snapshot.Scope.Service.Context, time.Now().UTC()).Identity)
		listener := c.listenerEvidence(ctx, gateway, route, parent)
		admission := routeParentConditions(route, parent, ns)
		item.gap("GatewayClass controller ownership is unverified; controller conditions are reported source evidence")
		item.fact("Parent "+ns+"/"+name, listener)
		item.fact("Route admission "+ns+"/"+name, admission)
		if strings.Contains(strings.ToLower(listener), "unknown") || strings.Contains(listener, "unavailable") ||
			strings.Contains(listener, "unresolved") || strings.Contains(listener, "stale/") {
			item.gap("Gateway attachment has missing/unsupported/stale source evidence; inspect listener facts")
		}
		if strings.Contains(strings.ToLower(admission), "unknown") || strings.Contains(admission, "stale/") {
			item.gap("Route admission generation or controller condition unconfirmed")
		}
	}
	if len(parents) > 8 {
		c.snapshot.Omitted += len(parents) - 8
		item.fact("Parent coverage", "Only first 8 explicit parent references projected; additional parents unknown")
		item.gap("Additional parent references omitted; attachment coverage incomplete")
	}
}

func (s *Snapshot) projectPolicy(object *unstructured.Unstructured, at time.Time) {
	raw, found, _ := unstructured.NestedMap(object.Object, "spec", "podSelector")
	if !found {
		return
	}
	var authored metav1.LabelSelector
	if runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &authored) != nil {
		return
	}
	selector, err := metav1.LabelSelectorAsSelector(&authored)
	if err != nil {
		return
	}
	matched := make([]inspect.ResourceIdentity, 0)
	for i := range s.Pods {
		p := &s.Pods[i]
		if selector.Matches(labels.Set(p.Labels)) {
			matched = append(matched, p.Source.Identity)
		}
	}
	if len(matched) == 0 {
		return
	}
	item := Item{Source: source(object, "networking.k8s.io/v1/networkpolicies", s.Scope.Service.Context, at), Group: GroupPolicies, State: StateCandidate,
		Summary: fmt.Sprintf("%s selects %d obtained backend Pods; configured candidate", object.GetName(), len(matched)), Related: matched}
	item.fact("Authored Pod selector", selector.String())
	item.fact("Reported policyTypes", strings.Join(stringsAt(object.Object, "spec", "policyTypes"), ", "))
	for _, direction := range []string{"ingress", "egress"} {
		rules, _, _ := unstructured.NestedSlice(object.Object, "spec", direction)
		item.fact(direction+" rules", fmt.Sprintf("%d reported; peer/rule order does not establish authorization", len(rules)))
	}
	item.fact("Peer selectors/IPBlocks", "Not evaluated against external peers or namespace labels; their configuration does not prove a live policy verdict")
	item.fact("Boundary", "This candidate is not a deny/allow verdict; union/defaulting, CNI implementation and observed traffic must be assessed separately")
	s.add(&item)
}
