// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func gatewayFixture(t *testing.T) (route, gateway, namespace, grant *unstructured.Unstructured) {
	t.Helper()
	route = testObject("gateway.networking.k8s.io/v1", KindHTTPRoute, "edge", "api-route", "route-a")
	route.Object["spec"] = map[string]any{"hostnames": []any{"api.example.test"}, "parentRefs": []any{map[string]any{"name": "shared", "sectionName": "https"}},
		"rules": []any{map[string]any{"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/api"}}},
			"backendRefs": []any{map[string]any{"name": "api", "namespace": "apps", "port": int64(80)}}}}}
	route.Object["status"] = map[string]any{"parents": []any{map[string]any{"parentRef": map[string]any{"name": "shared", "sectionName": "https"}, "controllerName": "example/controller",
		"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(1)}}}}}
	gateway = testObject("gateway.networking.k8s.io/v1", "Gateway", "edge", "shared", "gateway-a")
	gateway.Object["spec"] = map[string]any{"listeners": []any{map[string]any{"name": "https", "protocol": "HTTPS", "port": int64(443), "hostname": "*.example.test",
		"allowedRoutes": map[string]any{"namespaces": map[string]any{"from": "Selector", "selector": map[string]any{"matchLabels": map[string]any{"team": "edge"}}}}}}}
	gateway.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "True"}}, "listeners": []any{map[string]any{"name": "https",
		"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(2)}}}}}
	namespace = testObject(NativeAPI, "Namespace", "", "edge", "ns-edge")
	namespace.SetLabels(map[string]string{"team": "edge"})
	grant = testObject("gateway.networking.k8s.io/v1beta1", "ReferenceGrant", "apps", "allow-edge", "grant-a")
	grant.Object["spec"] = map[string]any{"from": []any{map[string]any{"group": GatewayGroup, "kind": KindHTTPRoute, "namespace": "edge"}},
		"to": []any{map[string]any{"group": "", "kind": KindService, "name": "api"}}}
	return route, gateway, namespace, grant
}

func TestNetworkGatewayExplicitReferencesAndGenerationGaps(t *testing.T) {
	scope, dyn := pathFixture(t)
	scope.RouteNamespaces = []string{"edge"}
	route, gateway, namespace, grant := gatewayFixture(t)
	for resource, object := range map[string]*unstructured.Unstructured{"gateway.networking.k8s.io/v1/httproutes": route, "gateway.networking.k8s.io/v1/gateways": gateway,
		"v1/namespaces": namespace, "gateway.networking.k8s.io/v1beta1/referencegrants": grant} {
		_, err := dyn.Resource(gvr(resource)).Namespace(object.GetNamespace()).Create(t.Context(), object, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	dyn.ClearActions()
	s, err := Collect(t.Context(), dyn, scope)
	require.NoError(t, err)
	var selected *Item
	for _, item := range s.TabItems(3) {
		if item.Source.Kind == KindHTTPRoute {
			selected = item
		}
	}
	require.NotNil(t, selected)
	raw := s.Evidence(selected)
	for _, want := range []string{"grant-a", "gateway-a", "Selector currently matches=true", "stale/mismatching observed generation", "current generation unknown", "/api"} {
		require.Contains(t, raw, want)
	}
	require.NotEmpty(t, selected.Gaps)
	for _, action := range dyn.Actions() {
		if action.GetResource().Resource == "namespaces" {
			require.Equal(t, "get", action.GetVerb())
		} else {
			require.Contains(t, []string{"apps", "edge"}, action.GetNamespace())
			require.NotEmpty(t, action.GetNamespace())
		}
	}
}

func TestNetworkGatewayUnsupportedRefsAndHostLimitsStayExplicit(t *testing.T) {
	scope, dyn := pathFixture(t)
	scope.RouteNamespaces = []string{"edge"}
	route, _, _, _ := gatewayFixture(t)
	require.NoError(t, unstructured.SetNestedSlice(route.Object, []any{map[string]any{"name": "shared", "kind": KindService, "group": ""}}, "spec", "parentRefs"))
	_, err := dyn.Resource(gvr("gateway.networking.k8s.io/v1/httproutes")).Namespace("edge").Create(t.Context(), route, metav1.CreateOptions{})
	require.NoError(t, err)
	s, err := Collect(t.Context(), dyn, scope)
	require.NoError(t, err)
	require.Contains(t, s.Evidence(nil), "Unsupported parent group/kind")
	require.Contains(t, s.Evidence(nil), "No matching obtained ReferenceGrant")
	require.True(t, hostnameIntersects("*.example.test", "api.example.test"))
	require.False(t, hostnameIntersects("*.example.test", "example.test"))
	require.False(t, hostnameIntersects("api.example.test", "unrelated.test"))
	require.Contains(t, s.Evidence(nil), "authorization unknown")
}

func TestNetworkGatewayPortScopedStatusAndOmittedConditionsStayUnknown(t *testing.T) {
	route := testObject(GatewayGroup+"/v1", KindHTTPRoute, "edge", "api", "route-a")
	route.SetGeneration(2)
	condition := map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(2)}
	require.NoError(t, unstructured.SetNestedSlice(route.Object, []any{map[string]any{
		"parentRef": map[string]any{"name": "edge", "port": int64(443)}, "controllerName": "example/controller", "conditions": []any{condition}}}, "status", "parents"))
	require.Contains(t, routeParentConditions(route, map[string]any{"name": "edge", "port": int64(80)}, "edge"), "Unknown")
	require.Contains(t, routeParentConditions(route, map[string]any{"name": "edge", "port": int64(443)}, "edge"), "GatewayClass ownership unverified")
	conditions := make([]any, 17)
	for i := range conditions {
		conditions[i] = condition
	}
	require.Contains(t, conditionEvidence(map[string]any{"conditions": conditions}, 2, "conditions"), "Additional conditions omitted")
}
