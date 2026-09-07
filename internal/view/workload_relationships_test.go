// SPDX-License-Identifier: Apache-2.0
package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func relationshipObject(t *testing.T, raw string) *unstructured.Unstructured {
	t.Helper()
	o := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(raw), &o.Object); err != nil {
		t.Fatal(err)
	}
	return o
}
func relationshipHas(refs []inspectionReference, kind, ns, name string) bool {
	for _, r := range refs {
		if r.ref.Kind == kind && r.ref.Namespace == ns && r.ref.Name == name {
			return true
		}
	}
	return false
}
func TestRouteReferencesKeepDestinationNamespace(t *testing.T) {
	o := relationshipObject(t, `{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"namespace":"app","name":"route"},"spec":{"parentRefs":[{"name":"edge","namespace":"infra"}],"rules":[{"backendRefs":[{"name":"api","namespace":"backend"},{"name":"ignored","kind":"ConfigMap"}]}]}}`)
	refs := objectReferences(o)
	if !relationshipHas(refs, "Gateway", "infra", "edge") || !relationshipHas(refs, "Service", "backend", "api") || relationshipHas(refs, "Service", "app", "ignored") {
		t.Fatalf("wrong refs: %+v", refs)
	}
}
func TestIngressIncludesDefaultAndRuleBackends(t *testing.T) {
	o := relationshipObject(t, `{"apiVersion":"networking.k8s.io/v1","kind":"Ingress","metadata":{"namespace":"app"},"spec":{"defaultBackend":{"service":{"name":"fallback"}},"rules":[{"http":{"paths":[{"backend":{"service":{"name":"api"}}}]}}]}}`)
	refs := objectReferences(o)
	if !relationshipHas(refs, "Service", "app", "fallback") || !relationshipHas(refs, "Service", "app", "api") {
		t.Fatalf("missing backends: %+v", refs)
	}
}
func TestEndpointSliceUsesExplicitTargetsNotAddressGuessing(t *testing.T) {
	o := relationshipObject(t, `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":{"namespace":"app","labels":{"kubernetes.io/service-name":"api"}},"endpoints":[{"addresses":["10.0.0.4"],"targetRef":{"kind":"Pod","namespace":"app","name":"api-1"},"conditions":{"ready":false}},{"addresses":["1.1.1.1"]}]}`)
	refs := objectReferences(o)
	if !relationshipHas(refs, "Service", "app", "api") || !relationshipHas(refs, "Pod", "app", "api-1") {
		t.Fatalf("missing explicit targets: %+v", refs)
	}
	for _, r := range refs {
		if r.ref.Name == "1.1.1.1" {
			t.Fatal("invented pod from IP")
		}
	}
}
func TestServiceRelationshipsScopeListsAndSurfaceGaps(t *testing.T) {
	svc := relationshipObject(t, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"app","name":"api"},"spec":{"selector":{"app":"api"}}}`)
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                                           "PodList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}:      "EndpointSliceList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}:          "IngressList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
	}, svc)
	dyn.PrependReactor("list", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "app" {
			t.Fatal("namespace broadened", a)
		}
		act := a.(ktesting.ListAction)
		switch a.GetResource().Resource {
		case "pods":
			if act.GetListRestrictions().Labels.String() != "app=api" {
				t.Fatal("wrong pod selector", act)
			}
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*relationshipObject(t, `{"kind":"Pod","metadata":{"namespace":"app","name":"api-1","labels":{"app":"api"}}}`)}}, nil
		case "endpointslices":
			if act.GetListRestrictions().Labels.String() != "kubernetes.io/service-name=api" {
				t.Fatal("wrong service selector", act)
			}
			return true, &unstructured.UnstructuredList{Object: map[string]any{"metadata": map[string]any{"continue": "more"}}}, nil
		default:
			return true, nil, fmt.Errorf("forbidden")
		}
	})
	refs, err := loadInspectionReferences(t.Context(), inspectionConnection{dynamic: dyn}, client.NewGVR("v1/services"), "app/api", troubleshootCommand)
	if err != nil || !relationshipHas(refs, "Pod", "app", "api-1") {
		t.Fatal(refs, err)
	}
	text := fmt.Sprint(refs)
	if !strings.Contains(text, "truncated") || !strings.Contains(text, "forbidden") {
		t.Fatal("visibility gap hidden", text)
	}
}

func TestPodRelationshipsRejectEmptyAndPartialServiceSelectors(t *testing.T) {
	pod := relationshipObject(t, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"app","name":"p","labels":{"app":"api","tier":"web"}}}`)
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "services"}: "ServiceList"}, pod,
		relationshipObject(t, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"app","name":"right"},"spec":{"selector":{"app":"api","tier":"web"}}}`),
		relationshipObject(t, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"app","name":"partial"},"spec":{"selector":{"app":"api","tier":"db"}}}`),
		relationshipObject(t, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"app","name":"empty"}}`),
		relationshipObject(t, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"other","name":"remote"},"spec":{"selector":{"app":"api"}}}`))
	refs := networkRelationships(t.Context(), inspectionConnection{dynamic: dyn}, pod)
	if len(refs) != 1 || !relationshipHas(refs, "Service", "app", "right") {
		t.Fatalf("false relationship: %+v", refs)
	}
}
func TestSelectorlessServiceNeverListsAllPods(t *testing.T) {
	svc := relationshipObject(t, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"app","name":"external"}}`)
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}:      "EndpointSliceList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}:          "IngressList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
	})
	dyn.PrependReactor("list", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetResource().Resource == "pods" {
			t.Fatal("selectorless Service scanned pods")
		}
		return true, &unstructured.UnstructuredList{}, nil
	})
	refs := networkRelationships(t.Context(), inspectionConnection{dynamic: dyn}, svc)
	if !strings.Contains(fmt.Sprint(refs), "no selector") {
		t.Fatal("missing selector not explained", refs)
	}
}
func TestReverseRouteMatchingDoesNotConfuseRemoteService(t *testing.T) {
	route := relationshipObject(t, `{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"namespace":"app","name":"r"},"spec":{"rules":[{"backendRefs":[{"name":"api","namespace":"other"}]}]}}`)
	svc := relationshipObject(t, `{"kind":"Service","metadata":{"namespace":"app","name":"api"}}`)
	if referencesObject(networkReferences(route), svc, "Service") {
		t.Fatal("remote backend attributed to local Service")
	}
}

func TestRelationshipsDoNotConfuseIstioAndGatewayAPI(t *testing.T) {
	o := relationshipObject(t, `{"apiVersion":"networking.istio.io/v1","kind":"Gateway","metadata":{"namespace":"app","name":"edge"}}`)
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList"})
	refs := networkRelationships(t.Context(), inspectionConnection{dynamic: dyn}, o)
	if len(refs) != 0 || len(dyn.Actions()) != 0 {
		t.Fatal("unrelated API group scanned", refs, dyn.Actions())
	}
	route := relationshipObject(t, `{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"namespace":"app"},"spec":{"parentRefs":[{"name":"edge"}]}}`)
	if referencesObject(networkReferences(route), o, "Gateway") {
		t.Fatal("Gateway group collision")
	}
}

func TestRelationshipsCombineEvidenceForSameTarget(t *testing.T) {
	refs := stableRelationships([]inspectionReference{
		relationshipRef("", "Service", "app", "api", "ownerReference"),
		relationshipRef("", "Service", "app", "api", "EndpointSlice service-name label"),
	})
	if len(refs) != 1 || !strings.Contains(refs[0].reason, "ownerReference") || !strings.Contains(refs[0].reason, "service-name label") {
		t.Fatal("duplicate target or lost evidence", refs)
	}
}
