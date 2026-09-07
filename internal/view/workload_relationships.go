// SPDX-License-Identifier: Apache-2.0
package view

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/certmanager"
	"github.com/derailed/k9s/internal/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	relationshipService = "Service"
	relationshipGateway = "Gateway"
	relationshipRoute   = "HTTPRoute"
	relationshipSlice   = "EndpointSlice"
	gatewayGroup        = "gateway.networking.k8s.io"
	serviceNameLabel    = "kubernetes.io/service-name"
)

func relationshipRef(group, kind, ns, name, reason string) inspectionReference {
	return inspectionReference{ref: certmanager.Reference{Group: group, Kind: kind, Namespace: ns, Name: name}, reason: reason}
}

// These are configuration references, not inferred traffic or attachment verdicts.
func networkReferences(o *unstructured.Unstructured) []inspectionReference {
	var refs []inspectionReference
	ns := o.GetNamespace()
	switch relationshipKind(o) {
	case "Ingress":
		add := func(backend map[string]any) {
			name := nestedText(backend, "service", "name")
			if name != "" {
				refs = append(refs, relationshipRef("", relationshipService, ns, name, "Ingress backend reference; routing not verified"))
			}
		}
		backend, _, _ := unstructured.NestedMap(o.Object, "spec", "defaultBackend")
		add(backend)
		for _, rule := range relationshipMaps(o.Object, "spec", "rules") {
			for _, path := range relationshipMaps(rule, "http", "paths") {
				b, _, _ := unstructured.NestedMap(path, "backend")
				add(b)
			}
		}
	case relationshipRoute:
		for _, parent := range relationshipMaps(o.Object, "spec", "parentRefs") {
			if ref, ok := routeReference(parent, ns, gatewayGroup, relationshipGateway, "parentRef; attachment not verified"); ok {
				refs = append(refs, ref)
			}
		}
		for _, rule := range relationshipMaps(o.Object, "spec", "rules") {
			for _, backend := range relationshipMaps(rule, "backendRefs") {
				if ref, ok := routeReference(backend, ns, "", relationshipService, "backendRef; routing and ReferenceGrant not verified"); ok {
					refs = append(refs, ref)
				}
			}
		}
	case relationshipSlice:
		if name := o.GetLabels()[serviceNameLabel]; name != "" {
			refs = append(refs, relationshipRef("", relationshipService, ns, name, "EndpointSlice service-name label"))
		}
		for _, endpoint := range relationshipMaps(o.Object, "endpoints") {
			addresses, _, _ := unstructured.NestedStringSlice(endpoint, "addresses")
			evidence := fmt.Sprintf("endpoint %s; ready=%s serving=%s terminating=%s",
				strings.Join(addresses, ", "), endpointCondition(endpoint, "ready"),
				endpointCondition(endpoint, "serving"), endpointCondition(endpoint, "terminating"))
			target, _, _ := unstructured.NestedMap(endpoint, "targetRef")
			targetNS := nestedText(target, "namespace")
			if targetNS == "" {
				targetNS = ns
			}
			api := nestedText(target, "apiVersion")
			if nestedText(target, "kind") == "Pod" && nestedText(target, "name") != "" && (api == "" || api == "v1") {
				refs = append(refs, relationshipRef("", "Pod", targetNS, nestedText(target, "name"), "targetRef; "+evidence))
			} else {
				refs = append(refs, inspectionReference{notice: evidence + "; no supported Pod targetRef"})
			}
		}
	}
	return refs
}
func endpointCondition(endpoint map[string]any, name string) string {
	value, found, _ := unstructured.NestedBool(endpoint, "conditions", name)
	if !found {
		return "not reported"
	}
	return fmt.Sprint(value)
}
func routeReference(raw map[string]any, ns, group, kind, reason string) (inspectionReference, bool) {
	if value, found, _ := unstructured.NestedString(raw, "group"); found && value != group {
		return inspectionReference{}, false
	}
	if value, found, _ := unstructured.NestedString(raw, "kind"); found && value != kind {
		return inspectionReference{}, false
	}
	name := nestedText(raw, "name")
	if name == "" {
		return inspectionReference{}, false
	}
	if value := nestedText(raw, "namespace"); value != "" && value != ns {
		ns = value
		reason = "cross-namespace " + reason
	}
	return relationshipRef(group, kind, ns, name, reason), true
}
func relationshipMaps(o map[string]any, fields ...string) []map[string]any {
	items, _, _ := unstructured.NestedSlice(o, fields...)
	var result []map[string]any
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}

// Discovery is bounded and namespace-scoped. No background watches or API reads
// occur until the user opens the related-resource picker.
func networkRelationships(ctx context.Context, conn client.Connection, o *unstructured.Unstructured) []inspectionReference {
	var refs []inspectionReference
	list := func(group, resource, kind, selector string) []*unstructured.Unstructured {
		dyn, err := conn.DynDial()
		if err != nil {
			refs = append(refs, inspectionReference{notice: kind + " visibility unavailable: " + err.Error()})
			return nil
		}
		objects, err := dyn.Resource(schema.GroupVersionResource{Group: group, Version: "v1", Resource: resource}).
			Namespace(o.GetNamespace()).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 200})
		if err != nil {
			refs = append(refs, inspectionReference{notice: kind + " visibility unavailable: " + err.Error()})
			return nil
		}
		if objects.GetContinue() != "" {
			refs = append(refs, inspectionReference{notice: kind + " results truncated at 200; relationships may be missing"})
		}
		var result []*unstructured.Unstructured
		for i := range objects.Items {
			result = append(result, &objects.Items[i])
		}
		return result
	}
	add := func(obj *unstructured.Unstructured, group, kind, reason string) {
		refs = append(refs, relationshipRef(group, kind, obj.GetNamespace(), obj.GetName(), reason))
	}
	switch relationshipKind(o) {
	case "Pod", "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet", "Job":
		podLabels := o.GetLabels()
		reason := "Service selector matches Pod labels; endpoints not verified"
		if o.GetKind() != "Pod" {
			podLabels, _, _ = unstructured.NestedStringMap(o.Object, "spec", "template", "metadata", "labels")
			reason = "Service selector matches pod template; current endpoints not verified"
		}
		for _, svc := range list("", "services", relationshipService, "") {
			selector, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
			if len(selector) > 0 && labels.SelectorFromSet(selector).Matches(labels.Set(podLabels)) {
				add(svc, "", relationshipService, reason)
			}
		}
	case relationshipService:
		selector, _, _ := unstructured.NestedStringMap(o.Object, "spec", "selector")
		if len(selector) > 0 {
			for _, pod := range list("", "pods", "Pod", labels.SelectorFromSet(selector).String()) {
				add(pod, "", "Pod", "Service selector matches; endpoint readiness not established")
			}
		} else {
			refs = append(refs, inspectionReference{notice: "Service has no selector; pod membership is not inferred. Inspect EndpointSlices for explicit targets."})
		}
		for _, slice := range list("discovery.k8s.io", "endpointslices", relationshipSlice, labels.Set{serviceNameLabel: o.GetName()}.String()) {
			add(slice, "discovery.k8s.io", relationshipSlice, "service-name label; inspect targets and reported readiness")
		}
		for _, ingress := range list("networking.k8s.io", "ingresses", "Ingress", "") {
			if referencesObject(networkReferences(ingress), o, relationshipService) {
				add(ingress, "networking.k8s.io", "Ingress", "Ingress backend reference; routing not verified")
			}
		}
		for _, route := range list(gatewayGroup, "httproutes", relationshipRoute, "") {
			if referencesObject(networkReferences(route), o, relationshipService) {
				add(route, gatewayGroup, relationshipRoute, "HTTPRoute backendRef; routing not verified")
			}
		}
		refs = append(refs, inspectionReference{notice: "Reverse route lookup is same-namespace only; cross-namespace consumers are not scanned."})
	case relationshipGateway:
		for _, route := range list(gatewayGroup, "httproutes", relationshipRoute, "") {
			if referencesObject(networkReferences(route), o, relationshipGateway) {
				add(route, gatewayGroup, relationshipRoute, "HTTPRoute parentRef; attachment not verified")
			}
		}
		refs = append(refs, inspectionReference{notice: "Same-namespace HTTPRoutes only; cross-namespace attachments are not scanned."})
	}
	return refs
}
func referencesObject(refs []inspectionReference, o *unstructured.Unstructured, kind string) bool {
	for _, r := range refs {
		if r.ref.Group == o.GroupVersionKind().Group && r.ref.Kind == kind && r.ref.Namespace == o.GetNamespace() && r.ref.Name == o.GetName() {
			return true
		}
	}
	return false
}
func stableRelationships(refs []inspectionReference) []inspectionReference {
	seen := map[inspectionReference]bool{}
	targets := map[certmanager.Reference]int{}
	result := make([]inspectionReference, 0, len(refs))
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		if r.notice == "" {
			if index, ok := targets[r.ref]; ok {
				if r.reason != "" {
					if result[index].reason != "" {
						result[index].reason += " | "
					}
					result[index].reason += r.reason
				}
				continue
			}
			targets[r.ref] = len(result)
		}
		result = append(result, r)
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if (a.notice == "") != (b.notice == "") {
			return a.notice == ""
		}
		return a.ref.Kind+a.ref.Namespace+a.ref.Name+a.reason+a.notice < b.ref.Kind+b.ref.Namespace+b.ref.Name+b.reason+b.notice
	})
	return result
}

// Kind names alone are ambiguous across API groups (for example Istio Gateway).
func relationshipKind(o *unstructured.Unstructured) string {
	group := o.GroupVersionKind().Group
	expected := map[string]string{
		"Pod": "", relationshipService: "", "Deployment": "apps", "DaemonSet": "apps",
		"StatefulSet": "apps", "ReplicaSet": "apps", "Job": "batch", "Ingress": "networking.k8s.io",
		relationshipSlice: "discovery.k8s.io", relationshipRoute: gatewayGroup, relationshipGateway: gatewayGroup,
	}
	if want, ok := expected[o.GetKind()]; ok && group == want {
		return o.GetKind()
	}
	return ""
}
