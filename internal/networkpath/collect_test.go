// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func testObject(apiVersion, kind, namespace, name, uid string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
	o.SetNamespace(namespace)
	o.SetName(name)
	o.SetUID(types.UID(uid))
	o.SetGeneration(2)
	return o
}
func pathFixture(t *testing.T) (Scope, *fake.FakeDynamicClient) {
	t.Helper()
	svc := testObject(NativeAPI, KindService, "apps", "api", "svc-a")
	svc.Object["spec"] = map[string]any{"type": "ClusterIP", "clusterIP": "10.0.0.1", "selector": map[string]any{"app": "api"},
		"ports": []any{map[string]any{"name": "http", "port": int64(80), "targetPort": "http", "protocol": "TCP"}}}
	pod := testObject(NativeAPI, "Pod", "apps", "api-a", "pod-a")
	pod.SetLabels(map[string]string{"app": "api"})
	pod.Object["spec"] = map[string]any{"dnsPolicy": "ClusterFirst", "containers": []any{map[string]any{"name": "api", "env": []any{map[string]any{"name": "TOKEN", "value": "PRIVATE-ENV-SHOULD-NOT-APPEAR"}}}}}
	pod.Object["status"] = map[string]any{"phase": "Running", "podIP": "10.1.0.2", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	slice := testObject("discovery.k8s.io/v1", "EndpointSlice", "apps", "api-1", "slice-a")
	slice.SetLabels(map[string]string{"kubernetes.io/service-name": "api"})
	control := true
	slice.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: NativeAPI, Kind: KindService, Name: "api", UID: "svc-a", Controller: &control}})
	slice.Object["addressType"] = "IPv4"
	slice.Object["ports"] = []any{map[string]any{"name": "http", "port": int64(8080), "protocol": "TCP"}}
	slice.Object["endpoints"] = []any{map[string]any{"addresses": []any{"10.1.0.2"}, "conditions": map[string]any{"ready": true},
		"targetRef": map[string]any{"apiVersion": NativeAPI, "kind": "Pod", "namespace": "apps", "name": "api-a", "uid": "pod-a"}}}
	ingress := testObject("networking.k8s.io/v1", "Ingress", "apps", "api", "ingress-a")
	ingress.Object["spec"] = map[string]any{"rules": []any{map[string]any{"host": "api.example.test", "http": map[string]any{"paths": []any{map[string]any{
		"path": "/api", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": "api", "port": map[string]any{"number": int64(80)}}}}}}}}}
	policy := testObject("networking.k8s.io/v1", "NetworkPolicy", "apps", "api-policy", "policy-a")
	policy.Object["spec"] = map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}, "policyTypes": []any{"Ingress"}}
	kinds := map[schema.GroupVersionResource]string{}
	for resource, kind := range map[string]string{ServiceGVR: "ServiceList", PodGVR: "PodList", "discovery.k8s.io/v1/endpointslices": "EndpointSliceList",
		"networking.k8s.io/v1/ingresses": "IngressList", "networking.k8s.io/v1/networkpolicies": "NetworkPolicyList", "gateway.networking.k8s.io/v1/httproutes": "HTTPRouteList",
		"gateway.networking.k8s.io/v1/gateways": "GatewayList", "gateway.networking.k8s.io/v1beta1/referencegrants": "ReferenceGrantList", "v1/namespaces": "NamespaceList"} {
		kinds[gvr(resource)] = kind
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, svc, pod, slice, ingress, policy)
	return Scope{Service: inspect.ResourceIdentity{Context: "lab", GVR: ServiceGVR, Namespace: "apps", Name: "api", UID: "svc-a"}}, dyn
}

func TestNetworkPathJoinsOnlyExplicitNamespaceAndVerifiedSources(t *testing.T) {
	scope, dyn := pathFixture(t)
	s, err := Collect(t.Context(), dyn, scope)
	require.NoError(t, err)
	require.Len(t, s.Pods, 1)
	require.Equal(t, "pod-a", s.Pods[0].Source.Identity.UID)
	require.Len(t, s.TabItems(2), 1)
	require.Len(t, s.TabItems(3), 1)
	require.Len(t, s.TabItems(4), 1)
	require.Contains(t, s.Evidence(nil), "Verified target Pod")
	require.Contains(t, s.pathSummary(), "connectivity untested")
	require.NotContains(t, s.Evidence(nil), "PRIVATE-ENV-SHOULD-NOT-APPEAR")
	require.NotContains(t, s.Evidence(nil), "cluster.local assumption applied")
	for _, action := range dyn.Actions() {
		require.Equal(t, "apps", action.GetNamespace())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
}

func TestNetworkPathReplacedServiceAndSelectorlessScopeNeverBroadenPodRead(t *testing.T) {
	scope, dyn := pathFixture(t)
	scope.Service.UID = "replaced"
	_, err := Collect(t.Context(), dyn, scope)
	require.ErrorContains(t, err, "identity changed")
	require.Len(t, dyn.Actions(), 1)
	scope, dyn = pathFixture(t)
	svc, err := dyn.Resource(gvr(ServiceGVR)).Namespace("apps").Get(t.Context(), "api", metav1.GetOptions{})
	require.NoError(t, err)
	unstructured.RemoveNestedField(svc.Object, "spec", "selector")
	_, err = dyn.Resource(gvr(ServiceGVR)).Namespace("apps").Update(t.Context(), svc, metav1.UpdateOptions{})
	require.NoError(t, err)
	dyn.ClearActions()
	s, err := Collect(t.Context(), dyn, scope)
	require.NoError(t, err)
	require.Len(t, s.Pods, 1, "exact EndpointSlice UID target can be read without a Pod list")
	for _, action := range dyn.Actions() {
		require.False(t, action.GetVerb() == "list" && action.GetResource().Resource == "pods")
	}
}

func TestNetworkPathDeniedMissingGatewayEmptyEndpointsAndCanceledReads(t *testing.T) {
	scope, dyn := pathFixture(t)
	dyn.PrependReactor("list", "httproutes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: GatewayGroup, Resource: "httproutes"}, "")
	})
	dyn.PrependReactor("list", "endpointslices", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{}, nil
	})
	dyn.PrependReactor("list", "networkpolicies", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "networkpolicies"}, "", errors.New("denied"))
	})
	s, err := Collect(t.Context(), dyn, scope)
	require.NoError(t, err)
	require.Empty(t, s.TabItems(2))
	require.GreaterOrEqual(t, s.GapCount(), 3)
	require.Contains(t, s.Evidence(nil), "absent")
	require.Contains(t, s.Evidence(nil), "denied")
	dyn.ClearActions()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, err = Collect(ctx, dyn, scope)
	require.Error(t, err)
	require.Empty(t, dyn.Actions())
	require.Equal(t, StateCanceled, s.Coverage[0].State)
}

func TestNetworkPathHardCandidateCapsAndNamespaceValidation(t *testing.T) {
	scope, dyn := pathFixture(t)
	dyn.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		for i := range PageSize + 4 {
			pod := testObject(NativeAPI, "Pod", "apps", fmt.Sprintf("api-%d", i), fmt.Sprintf("pod-%d", i))
			pod.SetLabels(map[string]string{"app": "api"})
			list.Items = append(list.Items, *pod)
		}
		return true, list, nil
	})
	s, err := Collect(t.Context(), dyn, scope)
	require.NoError(t, err)
	require.LessOrEqual(t, len(s.Pods), PageSize+1)
	require.Contains(t, s.Evidence(nil), "Server ignored")
	scope.RouteNamespaces = []string{"*"}
	_, err = NormalizeScope(scope)
	require.Error(t, err)
}

func TestNetworkFlowEvidenceRetainsDNSDropsAndUnavailableRelayBoundaries(t *testing.T) {
	sample := FlowSample{Context: "lab", StartedAt: time.Now(), CapturedAt: time.Now(), Status: hubble.Status{Error: "Relay unavailable"}}
	w := ComposeFlows(&sample)
	require.Empty(t, w.Items)
	require.False(t, w.Status.CoverageKnown)
	s := Snapshot{Flows: w}
	require.Contains(t, s.Render(5, 0, 60, 12), "Relay unavailable")
	sample.Events = []hubble.Event{{ID: 1, Time: time.Now(), Source: hubble.Peer{Pod: "apps/api-a"}, Destination: hubble.Peer{Kind: "FQDN", Names: "api.example.test"},
		Verdict: "DROPPED", DropReason: "POLICY_DENIED", DNS: hubble.DNSReport{Reported: true, Query: "api.example.test", Answers: "10.0.0.1", RecordType: "RESPONSE", LatencyNs: 500000}}}
	w = ComposeFlows(&sample)
	require.Len(t, w.Items, 2)
	raw, err := json.Marshal(w)
	require.NoError(t, err)
	for _, want := range []string{"Reported DNS query", "api.example.test", "500000 ns reported", "not a proven Service binding", "Reported policy attribution"} {
		require.Contains(t, string(raw), want)
	}
	require.NotEqual(t, ItemKey(&w.Items[0]), ItemKey(&w.Items[1]))
}

func TestNetworkFlowKeysKeepReportedIdentityAndUnknownTimeBoundaries(t *testing.T) {
	events := []hubble.Event{{ID: 1, Source: hubble.Peer{Pod: "apps/api-a"}, Destination: hubble.Peer{IP: "10.0.0.1"}, Protocol: "TCP", SourcePort: 1000, DestinationPort: 443},
		{ID: 2, Source: hubble.Peer{Pod: "apps/api-a"}, Destination: hubble.Peer{IP: "10.0.0.1"}, Protocol: "TCP", SourcePort: 2000, DestinationPort: 443}}
	w := ComposeFlows(&FlowSample{Events: events})
	require.Zero(t, w.FirstReportedAt)
	require.Zero(t, w.LastReportedAt)
	require.NotEqual(t, w.Items[0].ConversationKey, w.Items[1].ConversationKey)
	require.Equal(t, w.Items[0].EndpointKeys, w.Items[1].EndpointKeys)
	raw, err := json.Marshal(w)
	require.NoError(t, err)
	require.Contains(t, string(raw), "unknown / no API timestamp reported")
	unknown := ComposeFlows(&FlowSample{Events: []hubble.Event{{ID: 3}}})
	require.Empty(t, unknown.Items[0].ConversationKey)
	require.Empty(t, unknown.Items[0].EndpointKeys[0])
}

func TestNetworkProjectionCapsRemainExplicit(t *testing.T) {
	scope, _ := pathFixture(t)
	snapshot := Snapshot{Scope: scope}
	ingress := testObject("networking.k8s.io/v1", "Ingress", "apps", "api", "ing-a")
	rules := make([]any, 33)
	for i := range rules {
		rules[i] = map[string]any{"host": "example.test"}
	}
	require.NoError(t, unstructured.SetNestedSlice(ingress.Object, rules, "spec", "rules"))
	snapshot.projectIngress(ingress, time.Now())
	require.Equal(t, 1, snapshot.Omitted)
	require.Positive(t, snapshot.GapCount())
	item := Item{}
	for i := range 65 {
		item.fact(fmt.Sprint(i), "value")
	}
	require.Len(t, item.Facts, 64)
	require.Contains(t, item.Gaps, "Additional fact fields omitted; retained evidence is incomplete")
}

func TestNetworkReadStateRequiresMatchingAPIStatusForAbsence(t *testing.T) {
	resource := schema.GroupResource{Resource: "services"}
	typed := apierrors.NewNotFound(resource, "api")
	require.Equal(t, StateAbsent, coverageError(typed, ServiceGVR, "api"))
	require.Equal(t, inspect.ObservationUnknown, coverageError(typed, ServiceGVR, "another"))
	require.Equal(t, inspect.ObservationUnknown, coverageError(typed, "apps/v1/services", "api"))
	plain := apierrors.NewGenericServerResponse(404, "get", resource, "api", "proxy404", 0, true)
	require.Equal(t, inspect.ObservationUnknown, coverageError(plain, ServiceGVR, "api"))
	missingDetails := &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound, Code: 404}}
	require.Equal(t, inspect.ObservationUnknown, coverageError(missingDetails, ServiceGVR, "api"))
	require.Equal(t, inspect.ObservationUnknown, coverageError(apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "api"), ServiceGVR, "api"))
}

func TestNetworkOwnerMetadataIsBoundedAndExcludesSecretTargets(t *testing.T) {
	object := testObject(NativeAPI, "Pod", "apps", "api-a", "pod-a")
	controller := true
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: "rs-a", Controller: &controller},
		{APIVersion: NativeAPI, Kind: "Secret", Name: "PRIVATE-SECRET-NAME", UID: "private-secret"}})
	retained := source(object, PodGVR, "lab", time.Now())
	require.Equal(t, "apps/v1/replicasets", retained.Owners[0].GVR)
	require.True(t, retained.Owners[0].Controller)
	raw, err := json.Marshal(retained)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE-SECRET-NAME")
	require.NotContains(t, string(raw), "private-secret")
	owners := object.GetOwnerReferences()
	for len(owners) < 18 {
		owners = append(owners, owners[0])
	}
	object.SetOwnerReferences(owners)
	retained = source(object, PodGVR, "lab", time.Now())
	require.Len(t, retained.Owners, 16)
	require.Equal(t, 2, retained.OwnerOmitted)
}

func TestNetworkFlowRowKeysDoNotRetargetResetLocalRecordIDs(t *testing.T) {
	first := hubble.Event{ID: 1, Source: hubble.Peer{Pod: "apps/api-a"}, Destination: hubble.Peer{IP: "10.0.0.1"}}
	second := first
	second.Source.Pod = "apps/replacement"
	window := ComposeFlows(&FlowSample{Events: []hubble.Event{first, second}})
	require.NotEqual(t, ItemKey(&window.Items[0]), ItemKey(&window.Items[1]), "native reconnect can reset local store IDs")
}
