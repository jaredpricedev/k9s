// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

func (c *collector) gateway(ctx context.Context, namespace, name string) *unstructured.Unstructured {
	key := namespace + "/" + name
	if object, found := c.gateways[key]; found {
		return object
	}
	if len(c.gateways) >= 16 {
		c.snapshot.Coverage = append(c.snapshot.Coverage, Coverage{GVR: "gateway.networking.k8s.io/v1/gateways", Namespace: namespace, Name: name,
			State: StateTruncated, Detail: "Only 16 explicit Gateway references read; additional attachment evidence unknown", CapturedAt: time.Now().UTC()})
		return nil
	}
	object := c.get(ctx, "gateway.networking.k8s.io/v1/gateways", namespace, name)
	c.gateways[key] = object
	return object
}

func (c *collector) listenerEvidence(ctx context.Context, gateway, route *unstructured.Unstructured, parent map[string]any) string {
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	reports := make([]string, 0)
	for _, raw := range listeners[:min(16, len(listeners))] {
		listener, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if section := text(parent, "sectionName"); section != "" && section != text(listener, "name") {
			continue
		}
		if port, found, _ := unstructured.NestedInt64(parent, "port"); found {
			listenerPort, reported, _ := unstructured.NestedInt64(listener, "port")
			if !reported || listenerPort != port {
				continue
			}
		}
		report := text(listener, "name") + " " + scalar(listener, "port") + "/" + scalar(listener, "protocol")
		report += "; allowedRoutes " + c.allowedRouteNamespace(ctx, gateway, route, listener)
		protocol := text(listener, "protocol")
		if protocol != "HTTP" && protocol != "HTTPS" {
			report += "; HTTPRoute protocol support unavailable"
		}
		kinds, _, _ := unstructured.NestedSlice(listener, "allowedRoutes", "kinds")
		if len(kinds) > 16 {
			report += "; additional allowed kinds omitted, support outside retained kinds unknown"
		}
		if !listenerAllowsHTTPRoute(listener) {
			report += "; HTTPRoute not included in explicit allowedRoutes kinds"
		}
		report += "; hostname " + hostnameEvidence(text(listener, "hostname"), stringsAt(route.Object, "spec", "hostnames"))
		report += "; Gateway conditions " + conditionEvidence(gateway.Object, gateway.GetGeneration(), "status", "conditions")
		report += "; listener conditions " + listenerConditions(gateway, text(listener, "name"))
		reports = append(reports, report)
	}
	if len(reports) == 0 {
		return "No matching obtained listener for sectionName/port; attachment unresolved"
	}
	if len(listeners) > 16 {
		c.snapshot.Omitted += len(listeners) - 16
		reports = append(reports, "Additional listeners omitted; attachment coverage incomplete")
	}
	return strings.Join(reports, " | ")
}

func listenerAllowsHTTPRoute(listener map[string]any) bool {
	kinds, found, _ := unstructured.NestedSlice(listener, "allowedRoutes", "kinds")
	if !found {
		return true
	}
	for _, raw := range kinds[:min(16, len(kinds))] {
		if k, ok := raw.(map[string]any); ok && text(k, "kind") == KindHTTPRoute &&
			(text(k, "group") == "" || text(k, "group") == GatewayGroup) {
			return true
		}
	}
	return false
}

func (c *collector) allowedRouteNamespace(ctx context.Context, gateway, route *unstructured.Unstructured, listener map[string]any) string {
	from := text(listener, "allowedRoutes", "namespaces", "from")
	switch from {
	case "", "Same":
		if gateway.GetNamespace() == route.GetNamespace() {
			return "Same namespace configured"
		}
		return "Same excludes this route namespace by configuration"
	case "All":
		return "All namespaces configured; still subject to kind/hostname/parent status limits"
	case "Selector":
		raw, found, _ := unstructured.NestedMap(listener, "allowedRoutes", "namespaces", "selector")
		if !found {
			return "Selector not reported; namespace authorization unknown"
		}
		var authored metav1.LabelSelector
		if runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &authored) != nil {
			return "Malformed namespace selector; unknown"
		}
		selector, err := metav1.LabelSelectorAsSelector(&authored)
		if err != nil {
			return "Invalid namespace selector; unknown"
		}
		namespace, found := c.namespaces[route.GetNamespace()]
		if !found {
			namespace = c.get(ctx, "v1/namespaces", "", route.GetNamespace())
			c.namespaces[route.GetNamespace()] = namespace
		}
		if namespace == nil {
			return "Namespace labels unavailable; selector match unknown"
		}
		return fmt.Sprintf("Selector currently matches=%t; Namespace UID=%s captured from exact reference",
			selector.Matches(labels.Set(namespace.GetLabels())), namespace.GetUID())
	default:
		return "Unsupported namespaces.from value; unknown"
	}
}

func hostnameEvidence(listener string, routeHosts []string) string {
	if listener == "" {
		return "Listener is unrestricted; no DNS or TLS validation performed"
	}
	if len(routeHosts) == 0 {
		return listener + " configured; route hostnames absent, controller evidence still required"
	}
	for _, host := range routeHosts {
		if hostnameIntersects(listener, host) {
			return listener + " intersects a declared route hostname; configuration only"
		}
	}
	return listener + " does not intersect obtained route hostnames; attachment candidate unresolved"
}

func hostnameIntersects(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	if strings.HasPrefix(a, "*.") && strings.HasSuffix(b, a[1:]) && len(b) >= len(a) {
		return true
	}
	if strings.HasPrefix(b, "*.") && strings.HasSuffix(a, b[1:]) && len(a) >= len(b) {
		return true
	}
	return false
}

func conditionEvidence(object map[string]any, generation int64, path ...string) string {
	conditions, _, _ := unstructured.NestedSlice(object, path...)
	if len(conditions) == 0 {
		return "Unknown; conditions not reported"
	}
	parts := make([]string, 0)
	for _, raw := range conditions[:min(16, len(conditions))] {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		value := scalar(condition, "type") + "=" + scalar(condition, "status") + " " + text(condition, "reason")
		observed, found, _ := unstructured.NestedInt64(condition, "observedGeneration")
		switch {
		case !found || generation == 0:
			value += " (current generation unknown)"
		case observed != generation:
			value += " (stale/mismatching observed generation)"
		default:
			value += " (matches obtained generation)"
		}
		value += "; API transition " + scalar(condition, "lastTransitionTime")
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return "Unknown; condition projection unavailable"
	}
	if len(conditions) > 16 {
		parts = append(parts, "Additional conditions omitted; current status unknown outside retained fields")
	}
	return strings.Join(parts, "; ")
}

func listenerConditions(gateway *unstructured.Unstructured, name string) string {
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "status", "listeners")
	for _, raw := range listeners[:min(16, len(listeners))] {
		if listener, ok := raw.(map[string]any); ok && text(listener, "name") == name {
			return conditionEvidence(listener, gateway.GetGeneration(), "conditions")
		}
	}
	return "Unknown; listener status not reported"
}

func routeParentConditions(route *unstructured.Unstructured, parent map[string]any, namespace string) string {
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	reports := make([]string, 0)
	for _, raw := range parents[:min(16, len(parents))] {
		status, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref, _, _ := unstructured.NestedMap(status, "parentRef")
		ns := text(ref, "namespace")
		if ns == "" {
			ns = route.GetNamespace()
		}
		if ns != namespace || text(ref, "name") != text(parent, "name") || text(ref, "sectionName") != text(parent, "sectionName") {
			continue
		}
		parentPort, parentHasPort, _ := unstructured.NestedInt64(parent, "port")
		statusPort, statusHasPort, _ := unstructured.NestedInt64(ref, "port")
		if parentHasPort != statusHasPort || parentPort != statusPort {
			continue
		}
		if group := text(ref, "group"); group != "" && group != GatewayGroup {
			continue
		}
		if kind := text(ref, "kind"); kind != "" && kind != "Gateway" {
			continue
		}
		reports = append(reports, "Controller "+scalar(status, "controllerName")+" (GatewayClass ownership unverified): "+
			conditionEvidence(status, route.GetGeneration(), "conditions"))
	}
	if len(reports) == 0 {
		return "Unknown; no matching parent/controller condition obtained"
	}
	if len(parents) > 16 {
		reports = append(reports, "Additional controller parent statuses omitted; admission unknown outside retained reports")
	}
	return strings.Join(reports, " | ")
}

func (c *collector) referenceGrant(ctx context.Context, fromNamespace, toNamespace, serviceName string) string {
	grants, found := c.grants[toNamespace]
	if !found {
		grants = c.list(ctx, "gateway.networking.k8s.io/v1beta1/referencegrants", toNamespace, "")
		c.grants[toNamespace] = grants
	}
	for _, grant := range grants {
		froms, _, _ := unstructured.NestedSlice(grant.Object, "spec", "from")
		c.snapshot.Omitted += max(0, len(froms)-32)
		allowedFrom := false
		for _, raw := range froms[:min(32, len(froms))] {
			if from, ok := raw.(map[string]any); ok && text(from, "group") == GatewayGroup &&
				text(from, "kind") == KindHTTPRoute && text(from, "namespace") == fromNamespace {
				allowedFrom = true
			}
		}
		if !allowedFrom {
			continue
		}
		tos, _, _ := unstructured.NestedSlice(grant.Object, "spec", "to")
		c.snapshot.Omitted += max(0, len(tos)-32)
		for _, raw := range tos[:min(32, len(tos))] {
			if to, ok := raw.(map[string]any); ok && text(to, "group") == "" && text(to, "kind") == KindService &&
				(text(to, "name") == "" || text(to, "name") == serviceName) {
				return "Matching current ReferenceGrant " + toNamespace + "/" + grant.GetName() + " UID=" + string(grant.GetUID()) + "; configuration only"
			}
		}
	}
	return "No matching obtained ReferenceGrant; denied/absent/truncated read or unsupported API version leaves authorization unknown (see coverage)"
}
