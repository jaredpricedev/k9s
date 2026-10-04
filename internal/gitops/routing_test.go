// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func nativeRoutingSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	reader, request := graphFixture()
	reader.objects["apps/v1/deployments:team/web"].SetLabels(nil)
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, snapshot.Nodes, 3)
	return snapshot
}

func cloneRoutingSnapshot(t *testing.T, source *Snapshot) *Snapshot {
	t.Helper()
	encoded, err := json.Marshal(source)
	require.NoError(t, err)
	var result Snapshot
	require.NoError(t, json.Unmarshal(encoded, &result))
	return &result
}

func TestGitOpsRoutingNativeRequiresCompleteCapturedOwnerChain(t *testing.T) {
	snapshot := nativeRoutingSnapshot(t)
	policy := Routing(snapshot)
	require.Equal(t, RouteNative, policy.Route)
	require.Contains(t, policy.Reason, "unmanaged ownership is not proven")
	for _, scenario := range []string{"wrong-root-uid", "wrong-context", "unknown-collection", "denied-link", "unverified-link", "wrong-endpoint", "cycle", "unobserved-node", "missing-coverage", "wrong-coverage"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := cloneRoutingSnapshot(t, snapshot)
			switch scenario {
			case "wrong-root-uid":
				candidate.Request.Target.UID = "replacement-uid"
			case "wrong-context":
				candidate.Nodes[1].Identity.Context = "other"
			case "unknown-collection":
				candidate.CollectionState = ""
			case "denied-link":
				candidate.Links[0].State = Denied
			case "unverified-link":
				candidate.Links[0].Certainty = "unverified metadata"
			case "wrong-endpoint":
				candidate.Links[0].To = len(candidate.Nodes)
			case "cycle":
				candidate.Links = append(candidate.Links, Link{From: 2, To: 1, Relation: controllerRelation, Certainty: verifiedController, State: Complete})
			case "unobserved-node":
				candidate.Links = candidate.Links[:1]
			case "missing-coverage":
				candidate.Coverage = nil
			case "wrong-coverage":
				candidate.Coverage[0].Scope = "team/unobserved-resource"
			}
			require.Equal(t, RouteAmbiguous, Routing(candidate).Route)
		})
	}
	require.Equal(t, RouteAmbiguous, Routing(nil).Route)
	require.Equal(t, RouteAmbiguous, Routing(&Snapshot{}).Route)
}

func TestGitOpsRoutingCopiedAndIncompleteMetadataCannotUseNativeApply(t *testing.T) {
	for _, marker := range []struct{ location, key, value string }{
		{"labels", "app.kubernetes.io/managed-by", "Helm"},
		{"labels", "app.kubernetes.io/instance", "copied-app"},
		{"labels", "argocd.argoproj.io/instance", "copied-app"},
		{"annotations", "argocd.argoproj.io/tracking-id", "copied:/Pod:other/name"},
		{"labels", "kustomize.toolkit.fluxcd.io/name", "release-without-namespace"},
		{"labels", "helm.toolkit.fluxcd.io/namespace", "namespace-without-release"},
		{"annotations", "meta.helm.sh/release-name", "release-without-namespace"},
	} {
		t.Run(marker.key, func(t *testing.T) {
			reader, request := graphFixture()
			pod := reader.objects["v1/pods:team/web-pod"]
			pod.SetOwnerReferences(nil)
			require.NoError(t, unstructured.SetNestedField(pod.Object, marker.value, "metadata", marker.location, marker.key))
			snapshot, err := Collect(t.Context(), reader, request)
			require.NoError(t, err)
			policy := Routing(snapshot)
			require.Equal(t, RouteSource, policy.Route)
			require.Contains(t, policy.Reason, "not ownership proof")
			require.Len(t, reader.gets, 1)
		})
	}
}

func TestGitOpsRoutingVerifiedControllerAndMetadataAssociationStayDistinct(t *testing.T) {
	reader, request := graphFixture()
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	policy := Routing(snapshot)
	require.Equal(t, RouteSource, policy.Route)
	require.Contains(t, policy.Reason, "not verified ownership")
	pod := reader.objects["v1/pods:team/web-pod"]
	ownerReference(pod, "kustomize.toolkit.fluxcd.io/v1", kindKustomization, "workload-controller", "controller-uid")
	controller := graphObject("kustomize.toolkit.fluxcd.io/v1", kindKustomization, "team", "workload-controller", "controller-uid")
	reader.objects["kustomize.toolkit.fluxcd.io/v1/kustomizations:team/workload-controller"] = controller
	reader.apis["kustomize.toolkit.fluxcd.io/v1/Kustomization"] = ResolvedResource{GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Namespaced: true}
	snapshot, err = Collect(t.Context(), reader, request)
	require.NoError(t, err)
	policy = Routing(snapshot)
	require.Equal(t, RouteSource, policy.Route)
	require.Contains(t, policy.Reason, "UID-verified controlling-owner chain")
	request.Target = inspect.ResourceIdentity{Context: "captured", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "team", Name: "workload-controller", UID: "controller-uid"}
	snapshot, err = Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Contains(t, Routing(snapshot).Reason, "Selected supported controller")
}

func TestGitOpsRoutingUnavailableOwnerBlocksNativePath(t *testing.T) {
	reader, request := graphFixture()
	reader.objects["apps/v1/deployments:team/web"].SetLabels(nil)
	reader.getErrors["apps/v1/replicasets:team/web-rs"] = apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "replicasets"}, "web-rs", nil)
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Equal(t, RouteAmbiguous, Routing(snapshot).Route)
	require.NotEmpty(t, OwnershipFingerprint(snapshot), "Retained incomplete evidence can still be fingerprinted")
}

func TestGitOpsDeclaredRoutingBlocksManagementOwnerAndMalformedMetadata(t *testing.T) {
	object := graphObject("apps/v1", "Deployment", "team", "web", "deployment-uid")
	require.Equal(t, RouteNative, DeclaredRouting(object.Object).Route)
	for _, marker := range ownershipMarkerKeys {
		t.Run(marker, func(t *testing.T) {
			candidate := object.DeepCopy()
			candidate.SetLabels(map[string]string{marker: "copied"})
			require.Equal(t, RouteSource, DeclaredRouting(candidate.Object).Route)
			candidate.SetLabels(map[string]string{marker: ""})
			require.Equal(t, RouteSource, DeclaredRouting(candidate.Object).Route, "Empty declared management metadata does not become absence")
		})
	}
	candidate := object.DeepCopy()
	ownerReference(candidate, "apps/v1", "ReplicaSet", "web-rs", "rs-uid")
	require.Equal(t, RouteAmbiguous, DeclaredRouting(candidate.Object).Route)
	require.Contains(t, DeclaredRouting(candidate.Object).Reason, "creation establishes no ownership")
	require.Equal(t, RouteSource, DeclaredRouting(argoFixture().Object).Route)
	for _, malformed := range []any{nil, "invalid", map[string]any{"app.kubernetes.io/managed-by": int64(7)}} {
		malformedObject := object.DeepCopy()
		require.NoError(t, unstructured.SetNestedField(malformedObject.Object, malformed, "metadata", "labels"))
		require.Equal(t, RouteAmbiguous, DeclaredRouting(malformedObject.Object).Route)
		require.Empty(t, DeclaredOwnershipFingerprint(malformedObject.Object))
	}
	candidate = object.DeepCopy()
	require.NoError(t, unstructured.SetNestedSlice(candidate.Object, []any{map[string]any{"controller": "true"}}, "metadata", "ownerReferences"))
	require.Equal(t, RouteAmbiguous, DeclaredRouting(candidate.Object).Route)
	require.Empty(t, DeclaredOwnershipFingerprint(candidate.Object))
	require.Equal(t, RouteAmbiguous, DeclaredRouting(nil).Route)
}

func TestGitOpsOwnershipFingerprintPinsReferencesAndExcludesTransientState(t *testing.T) {
	snapshot := nativeRoutingSnapshot(t)
	fingerprint := OwnershipFingerprint(snapshot)
	require.Len(t, fingerprint, 64)
	candidate := cloneRoutingSnapshot(t, snapshot)
	candidate.CapturedAt = time.Now().Add(time.Hour)
	for index := range candidate.Nodes {
		node := &candidate.Nodes[index]
		node.ResourceVersion, node.State, node.Reason = "new-rv", "new-health", "new diagnostic"
		node.ObservedAt = candidate.CapturedAt
		generation := int64(99)
		node.Generation, node.ObservedGeneration = &generation, &generation
	}
	for index := range candidate.Coverage {
		candidate.Coverage[index].ObservedAt = candidate.CapturedAt
	}
	require.Equal(t, fingerprint, OwnershipFingerprint(candidate))
	for _, scenario := range []string{"owner-uid", "reference", "marker", "denied", "argo-namespace"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := cloneRoutingSnapshot(t, snapshot)
			switch scenario {
			case "owner-uid":
				candidate.Nodes[1].Identity.UID = "new-owner-uid"
			case "reference":
				candidate.Links[0].Reference += " changed"
			case "marker":
				candidate.Nodes[1].Markers = []string{"Possible copied metadata"}
			case "denied":
				candidate.Coverage[1].State = Denied
			case "argo-namespace":
				candidate.Request.ArgoNamespace = "control"
			}
			require.NotEqual(t, fingerprint, OwnershipFingerprint(candidate))
		})
	}
}

func TestGitOpsOwnershipFingerprintCanonicalizesGraphOrder(t *testing.T) {
	snapshot := nativeRoutingSnapshot(t)
	fingerprint := OwnershipFingerprint(snapshot)
	snapshot.Nodes[1], snapshot.Nodes[2] = snapshot.Nodes[2], snapshot.Nodes[1]
	snapshot.Links = []Link{snapshot.Links[1], snapshot.Links[0]}
	for index := range snapshot.Links {
		link := &snapshot.Links[index]
		if link.From > 0 {
			link.From = 3 - link.From
		}
		link.To = 3 - link.To
	}
	snapshot.Coverage[0], snapshot.Coverage[2] = snapshot.Coverage[2], snapshot.Coverage[0]
	require.Equal(t, fingerprint, OwnershipFingerprint(snapshot))
}

func TestGitOpsDeclaredOwnershipFingerprintPinsCompleteMarkerValuesOnly(t *testing.T) {
	object := graphObject("apps/v1", "Deployment", "team", "web", "deployment-uid")
	marker := strings.Repeat("copied-marker", 100)
	object.SetAnnotations(map[string]string{"app.kubernetes.io/managed-by": marker})
	ownerReference(object, "apps/v1", "ReplicaSet", "web-rs", "rs-uid")
	fingerprint := DeclaredOwnershipFingerprint(object.Object)
	require.Len(t, fingerprint, 64)
	require.NotContains(t, fingerprint, marker)
	candidate := object.DeepCopy()
	candidate.SetResourceVersion("new-rv")
	candidate.SetUID("other-root-uid")
	candidate.Object["spec"] = map[string]any{"private": "raw-confidential-value"}
	candidate.Object["status"] = map[string]any{"secret": "raw-secret-value"}
	require.Equal(t, fingerprint, DeclaredOwnershipFingerprint(candidate.Object), "Root UID is separately pinned by the graph")
	candidate.SetAnnotations(map[string]string{"app.kubernetes.io/managed-by": marker + "-new"})
	require.NotEqual(t, fingerprint, DeclaredOwnershipFingerprint(candidate.Object), "A marker change beyond the display limit must invalidate the digest")
	candidate = object.DeepCopy()
	ownerReference(candidate, "apps/v1", "ReplicaSet", "web-rs", "replacement-owner-uid")
	require.NotEqual(t, fingerprint, DeclaredOwnershipFingerprint(candidate.Object))
	candidate.SetAnnotations(map[string]string{"app.kubernetes.io/managed-by": strings.Repeat("x", maxOwnershipMetadataBytes+1)})
	require.Empty(t, DeclaredOwnershipFingerprint(candidate.Object))
	require.Equal(t, RouteAmbiguous, DeclaredRouting(candidate.Object).Route)
	candidate.SetKind("Secret")
	require.Empty(t, DeclaredOwnershipFingerprint(candidate.Object))
}
