// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package dependency

import (
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/stretchr/testify/require"
)

func dependencyFixture() *networkpath.Snapshot {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	id := inspect.ResourceIdentity{Context: "lab", GVR: networkpath.ServiceGVR, Namespace: "apps", Name: "api", UID: "svc-a"}
	service := networkpath.Source{Identity: id, Kind: "Service", CapturedAt: at}
	pod := networkpath.Source{Identity: inspect.ResourceIdentity{Context: "lab", GVR: networkpath.PodGVR, Namespace: "apps", Name: "api-a", UID: "pod-a"}, Kind: "Pod", CapturedAt: at,
		Owners: []networkpath.OwnerReference{{GVR: "apps/v1/replicasets", Kind: "ReplicaSet", Name: "api-rs", UID: "rs-a", Controller: true, ControllerReported: true}}}
	slice := networkpath.Source{Identity: inspect.ResourceIdentity{Context: "lab", GVR: "discovery.k8s.io/v1/endpointslices", Namespace: "apps", Name: "api-1", UID: "slice-a"}, Kind: "EndpointSlice", CapturedAt: at,
		Owners: []networkpath.OwnerReference{{GVR: networkpath.ServiceGVR, Kind: "Service", Name: "api", UID: "svc-a", Controller: true, ControllerReported: true}}}
	return &networkpath.Snapshot{Scope: networkpath.Scope{Service: id, RouteNamespaces: []string{"apps", "edge"}}, StartedAt: at, CapturedAt: at,
		Service: networkpath.Service{Source: service, Selector: map[string]string{"app": "api"}}, Pods: []networkpath.Pod{{Source: pod, Labels: map[string]string{"app": "api"}}},
		Items:    []networkpath.Item{{Source: slice, Group: networkpath.GroupBackends, Related: []inspect.ResourceIdentity{pod.Identity}}},
		Coverage: []networkpath.Coverage{{GVR: networkpath.PodGVR, Namespace: "apps", State: inspect.ObservationComplete}, {GVR: slice.Identity.GVR, Namespace: "apps", State: inspect.ObservationComplete}}}
}

func TestDependencyEdgesRetainDeclaredProvenanceWithoutCausalClaim(t *testing.T) {
	snapshot := dependencyFixture()
	review := Compose(snapshot)
	require.Len(t, review.Edges, 5)
	require.Equal(t, []string{"apps"}, review.Group.RouteNamespaces)
	raw := review.Evidence()
	for _, want := range []string{"Controlling owner reference", "Service selector matches obtained Pod", "Endpoint target reference", "current object unverified", "do not establish causal", "UID", "SourceTimeKind"} {
		require.Contains(t, raw, want)
	}
	for i := range review.Edges {
		edge := &review.Edges[i]
		if edge.Relation == "Controlling owner reference" {
			require.Equal(t, TypeOwnership, edge.Type)
		} else {
			require.Equal(t, TypeConfiguration, edge.Type)
		}
		require.Equal(t, snapshot.CapturedAt, edge.CapturedAt)
		require.Zero(t, edge.SourceAt, "local configuration capture is not an API modification time")
	}
}

func TestDependencyCompositionDetachesMetadataAndKeepsStableReferenceKeys(t *testing.T) {
	snapshot := dependencyFixture()
	review := Compose(snapshot)
	before := review.Items()
	snapshot.Pods[0].Source.Owners[0].Name = "mutated-owner"
	require.NotContains(t, review.Evidence(), "mutated-owner")
	snapshot = dependencyFixture()
	second := Compose(snapshot).Items()
	for i := range before {
		require.Equal(t, networkpath.ItemKey(&before[i]), networkpath.ItemKey(&second[i]))
	}
	snapshot.Service.Source.Identity.UID = "replacement"
	require.Empty(t, Compose(snapshot).Edges)
	require.Contains(t, Compose(snapshot).Evidence(), "replacement sources are not substituted")
}

func TestDependencySelectedTrafficKeepsAmbiguousUIDsAndPartialWindow(t *testing.T) {
	snapshot := dependencyFixture()
	event := hubble.Event{ID: 1, Time: snapshot.CapturedAt, Source: hubble.Peer{Cluster: "lab", Pod: "apps/api-a"}, Destination: hubble.Peer{Cluster: "lab", IP: "10.0.0.5", Names: "ambiguous-service.test"}, Protocol: "TCP", DestinationPort: 443}
	snapshot.Flows = networkpath.ComposeFlows(&networkpath.FlowSample{Context: "lab", StartedAt: snapshot.StartedAt, CapturedAt: snapshot.CapturedAt, Events: []hubble.Event{event}, Status: hubble.Status{Error: "Relay partial", Lost: 2}})
	require.NotContains(t, Compose(snapshot).Summary(), "1 flow")
	snapshot.Flows.Items[0].Pinned = true
	review := Compose(snapshot)
	require.Contains(t, review.Summary(), "1 flow")
	var traffic *Edge
	for i := range review.Edges {
		if review.Edges[i].Type == TypeTraffic {
			traffic = &review.Edges[i]
		}
	}
	require.NotNil(t, traffic)
	require.Empty(t, traffic.From.Identity.UID)
	require.Empty(t, traffic.To.Identity.UID)
	require.Equal(t, event.Time, traffic.SourceAt)
	require.Contains(t, review.Evidence(), "partial/unknown")
	require.Contains(t, review.Evidence(), "Service/Pod binding unverified")
	event.Destination.Cluster = "another"
	snapshot.Flows = networkpath.ComposeFlows(&networkpath.FlowSample{Context: "lab", Events: []hubble.Event{event}})
	snapshot.Flows.Items[0].Pinned = true
	review = Compose(snapshot)
	for i := range review.Edges {
		require.NotEqual(t, TypeTraffic, review.Edges[i].Type)
	}
	require.Contains(t, review.Evidence(), "cross-cluster")
}

func TestDependencyMissingTrafficDeniedProvidersAndBoundsStayExplicit(t *testing.T) {
	snapshot := dependencyFixture()
	snapshot.Coverage = append(snapshot.Coverage, networkpath.Coverage{GVR: "gateway.networking.k8s.io/v1/httproutes", Namespace: "apps", State: inspect.ObservationDenied})
	for i := range 300 {
		pod := snapshot.Pods[0]
		pod.Source.Identity.Name = fmt.Sprintf("pod-%d", i)
		pod.Source.Identity.UID = fmt.Sprintf("uid-%d", i)
		snapshot.Pods = append(snapshot.Pods, pod)
	}
	review := Compose(snapshot)
	require.Len(t, review.Edges, MaxEdges)
	require.Positive(t, review.Omitted)
	for _, want := range []string{"missing traffic is not a missing dependency", "query coverage incomplete", "projection limits omitted"} {
		require.Contains(t, review.Evidence(), want)
	}
}

func TestDependencyExternalAliasIsDeclaredWithoutDNSOrServiceInference(t *testing.T) {
	snapshot := dependencyFixture()
	snapshot.Service.Type = "ExternalName"
	snapshot.Service.ExternalName = "database.example.test"
	snapshot.Service.Selector = nil
	snapshot.Pods, snapshot.Items = nil, nil
	review := Compose(snapshot)
	require.Len(t, review.Edges, 1)
	require.Equal(t, "database.example.test", review.Edges[0].To.DeclaredAlias)
	require.Empty(t, review.Edges[0].To.Identity.UID)
	require.Contains(t, review.Edges[0].State, "runtime resolution unobserved")
	require.Contains(t, review.Evidence(), "missing traffic is not a missing dependency")
}
