// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type graphReader struct {
	objects        map[string]*unstructured.Unstructured
	apis           map[string]ResolvedResource
	getErrors      map[string]error
	resolveError   error
	gets, resolves []string
	onGet          func()
}

func (r *graphReader) Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := gvrString(gvr) + ":" + namespace + "/" + name
	r.gets = append(r.gets, key)
	if r.onGet != nil {
		r.onGet()
	}
	if err := r.getErrors[key]; err != nil {
		return nil, err
	}
	if object := r.objects[key]; object != nil {
		return object.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
}
func (r *graphReader) Resolve(ctx context.Context, group, version, kind string) (ResolvedResource, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedResource{}, err
	}
	key := group + "/" + version + "/" + kind
	r.resolves = append(r.resolves, key)
	if r.resolveError != nil {
		return ResolvedResource{}, r.resolveError
	}
	if api, exists := r.apis[key]; exists {
		return api, nil
	}
	return ResolvedResource{}, ErrAPIAbsent
}

func graphObject(apiVersion, kind, namespace, name, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"namespace": namespace, "name": name, "uid": uid, "generation": int64(1), "resourceVersion": "17"}}}
}
func ownerReference(object *unstructured.Unstructured, apiVersion, kind, name, uid string) {
	controller := true
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid), Controller: &controller}})
}
func graphFixture() (*graphReader, *Request) {
	pod := graphObject("v1", "Pod", "team", "web-pod", "pod-uid")
	ownerReference(pod, "apps/v1", "ReplicaSet", "web-rs", "rs-uid")
	rs := graphObject("apps/v1", "ReplicaSet", "team", "web-rs", "rs-uid")
	ownerReference(rs, "apps/v1", "Deployment", "web", "deployment-uid")
	deployment := graphObject("apps/v1", "Deployment", "team", "web", "deployment-uid")
	deployment.SetLabels(map[string]string{"kustomize.toolkit.fluxcd.io/name": "web-release", "kustomize.toolkit.fluxcd.io/namespace": "ops"})
	kust := graphObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ops", "web-release", "kust-uid")
	kust.Object["spec"] = map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "namespace": "sources", "name": "repo"}}
	kust.Object["status"] = map[string]any{"lastAppliedRevision": "main@sha1:old"}
	repository := graphObject("source.toolkit.fluxcd.io/v1", "GitRepository", "sources", "repo", "repository-uid")
	repository.Object["status"] = map[string]any{"artifact": map[string]any{"revision": "main@sha1:new"}}
	r := &graphReader{objects: map[string]*unstructured.Unstructured{
		"v1/pods:team/web-pod": pod, "apps/v1/replicasets:team/web-rs": rs, "apps/v1/deployments:team/web": deployment,
		"kustomize.toolkit.fluxcd.io/v1/kustomizations:ops/web-release": kust, "source.toolkit.fluxcd.io/v1/gitrepositories:sources/repo": repository},
		apis: map[string]ResolvedResource{}, getErrors: map[string]error{}}
	for _, item := range []struct{ group, version, kind, resource string }{
		{"apps", "v1", "ReplicaSet", "replicasets"}, {"apps", "v1", "Deployment", "deployments"},
		{"kustomize.toolkit.fluxcd.io", "", "Kustomization", "kustomizations"}, {"source.toolkit.fluxcd.io", "", "GitRepository", "gitrepositories"},
	} {
		r.apis[item.group+"/"+item.version+"/"+item.kind] = ResolvedResource{GVR: schema.GroupVersionResource{Group: item.group, Version: "v1", Resource: item.resource}, Namespaced: true}
	}
	return r, &Request{Target: inspect.ResourceIdentity{Context: "captured", GVR: "v1/pods", Namespace: "team", Name: "web-pod", UID: "pod-uid"}}
}

func TestGitOpsChainUsesExplicitNamedReferencesAndOwnerUIDs(t *testing.T) {
	reader, request := graphFixture()
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, snapshot.Nodes, 5)
	require.Len(t, snapshot.Links, 4)
	require.Equal(t, "controller UID verified", snapshot.Links[0].Certainty)
	require.Equal(t, "controller UID verified", snapshot.Links[1].Certainty)
	require.Equal(t, "unverified metadata", snapshot.Links[2].Certainty)
	require.Equal(t, Complete, snapshot.Links[2].State, "Readable marker target is still unverified ownership")
	require.Equal(t, "sources", snapshot.Nodes[4].Identity.Namespace)
	require.Len(t, reader.gets, 5)
	for _, request := range reader.gets {
		require.NotContains(t, request, "secrets")
	}
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "main@sha1:old")
	require.Contains(t, string(encoded), "main@sha1:new")
	require.Contains(t, snapshot.Render(0), "applied revision differs")
}

func TestGitOpsReferenceReplacementPermissionAndAbsentRemainUnknown(t *testing.T) {
	for _, scenario := range []string{"replaced", "denied", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			reader, request := graphFixture()
			switch scenario {
			case "replaced":
				reader.objects["apps/v1/replicasets:team/web-rs"].SetUID("replacement")
			case "denied":
				reader.getErrors["apps/v1/replicasets:team/web-rs"] = apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "replicasets"}, "web-rs", fmt.Errorf("raw-private-diagnostic"))
			case "absent":
				reader.resolveError = ErrAPIAbsent
			}
			snapshot, err := Collect(t.Context(), reader, request)
			require.NoError(t, err)
			require.Len(t, snapshot.Nodes, 1)
			require.Len(t, snapshot.Links, 1)
			require.NotEqual(t, Complete, snapshot.Links[0].State)
			encoded, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "raw-private-diagnostic")
		})
	}
}

func TestArgoTrackingRequiresExplicitNamespaceAndSelfResource(t *testing.T) {
	reader, request := graphFixture()
	pod := reader.objects["v1/pods:team/web-pod"]
	pod.SetOwnerReferences(nil)
	pod.SetAnnotations(map[string]string{"argocd.argoproj.io/tracking-id": "delivery:/Pod:team/web-pod"})
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, reader.gets, 1)
	require.Empty(t, reader.resolves)
	require.Contains(t, snapshot.Links[0].Reason, "namespace unknown")
	request.ArgoNamespace = "control"
	reader.objects["argoproj.io/v1alpha1/applications:control/delivery"] = argoFixture()
	reader.apis["argoproj.io/v1alpha1/Application"] = ResolvedResource{GVR: schema.GroupVersionResource{Group: argoGroup, Version: "v1alpha1", Resource: "applications"}, Namespaced: true}
	snapshot, err = Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, snapshot.Nodes, 2)
	require.Equal(t, "unverified metadata", snapshot.Links[0].Certainty)
	pod.SetAnnotations(map[string]string{"argocd.argoproj.io/tracking-id": "delivery:/Pod:team/another-pod"})
	reader.gets = nil
	snapshot, err = Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, reader.gets, 1)
	require.Contains(t, snapshot.Links[0].Reason, "another resource")
}

func TestGitOpsCanceledOrSecretSelectionMakesNoRead(t *testing.T) {
	reader, request := graphFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Collect(ctx, reader, request)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, reader.gets)
	request.Target.GVR = "v1/secrets"
	_, err = Collect(t.Context(), reader, request)
	require.Error(t, err)
	require.Empty(t, reader.gets)
	require.Empty(t, reader.resolves)
}

func TestGitOpsCycleRemainsPartialAndSharedSourceIsReadOnce(t *testing.T) {
	reader, request := graphFixture()
	ownerReference(reader.objects["apps/v1/deployments:team/web"], "apps/v1", "ReplicaSet", "web-rs", "rs-uid")
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.True(t, snapshot.Partial())
	require.Contains(t, snapshot.Render(1), "Reference cycle detected")
	require.Len(t, reader.gets, 5)
	reader, request = graphFixture()
	kust := reader.objects["kustomize.toolkit.fluxcd.io/v1/kustomizations:ops/web-release"]
	require.NoError(t, unstructured.SetNestedSlice(kust.Object, []any{map[string]any{"name": "sibling"}}, "spec", "dependsOn"))
	sibling := graphObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ops", "sibling", "sibling-uid")
	sibling.Object["spec"] = map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "namespace": "sources", "name": "repo"}}
	reader.objects["kustomize.toolkit.fluxcd.io/v1/kustomizations:ops/sibling"] = sibling
	snapshot, err = Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.False(t, snapshot.Partial())
	require.Len(t, snapshot.Nodes, 6)
	require.Len(t, reader.gets, 6)
	require.NotContains(t, snapshot.Render(1), "cycle")
}

func TestGitOpsBudgetsRemainVisibleWithoutUnboundedTraversal(t *testing.T) {
	reader, request := graphFixture()
	request.Target = inspect.ResourceIdentity{Context: "captured", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "ops", Name: "root", UID: "root-uid"}
	root := graphObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ops", "root", "root-uid")
	var dependencies []any
	for child := range 8 {
		name := fmt.Sprintf("child-%d", child)
		dependencies = append(dependencies, map[string]any{"name": name})
		object := graphObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ops", name, name+"-uid")
		var grandchildren []any
		for grandchild := range 8 {
			grandname := fmt.Sprintf("grand-%d-%d", child, grandchild)
			grandchildren = append(grandchildren, map[string]any{"name": grandname})
			reader.objects["kustomize.toolkit.fluxcd.io/v1/kustomizations:ops/"+grandname] = graphObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ops", grandname, grandname+"-uid")
		}
		object.Object["spec"] = map[string]any{"dependsOn": grandchildren}
		reader.objects["kustomize.toolkit.fluxcd.io/v1/kustomizations:ops/"+name] = object
	}
	root.Object["spec"] = map[string]any{"dependsOn": dependencies}
	reader.objects["kustomize.toolkit.fluxcd.io/v1/kustomizations:ops/root"] = root
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, reader.gets, MaxNodes)
	require.Len(t, snapshot.Nodes, MaxNodes)
	require.True(t, snapshot.Partial())
	require.Contains(t, snapshot.Render(0), "budget reached")
}

func TestGitOpsSelectedMissingKindCannotBeginReferenceTraversal(t *testing.T) {
	reader, request := graphFixture()
	reader.objects["v1/pods:team/web-pod"].SetKind("")
	snapshot, err := Collect(t.Context(), reader, request)
	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "identity changed or unavailable")
	require.Len(t, reader.gets, 1)
	require.Empty(t, reader.resolves)
}

func TestGitOpsCollectionCopiesCapturedRequestBeforeNativeRead(t *testing.T) {
	reader, request := graphFixture()
	captured := *request
	reader.onGet = func() {
		request.Target.Context = "changed-context"
		request.Target.Namespace = "changed-namespace"
		request.Target.UID = "changed-uid"
		request.ArgoNamespace = "changed-controller"
	}
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Equal(t, captured, snapshot.Request)
	require.Len(t, snapshot.Nodes, 5)
	for _, node := range snapshot.Nodes {
		require.Equal(t, captured.Target.Context, node.Identity.Context)
	}
	require.Equal(t, captured.Target, snapshot.Nodes[0].Identity)
}

func TestGitOpsSelectedFluxSourceShowsReportedProgressInOverview(t *testing.T) {
	reader, request := graphFixture()
	request.Target = inspect.ResourceIdentity{Context: "captured", GVR: "source.toolkit.fluxcd.io/v1/gitrepositories", Namespace: "sources", Name: "repo", UID: "repository-uid"}
	source := reader.objects["source.toolkit.fluxcd.io/v1/gitrepositories:sources/repo"]
	require.NoError(t, unstructured.SetNestedField(source.Object, int64(1), "status", "observedGeneration"))
	require.NoError(t, unstructured.SetNestedSlice(source.Object, []any{map[string]any{"type": conditionReady, "status": "True", "reason": "Succeeded"}}, "status", "conditions"))
	snapshot, err := Collect(t.Context(), reader, request)
	require.NoError(t, err)
	require.Len(t, snapshot.Nodes, 1)
	require.Equal(t, ProviderFlux, snapshot.Nodes[0].Provider)
	require.Contains(t, snapshot.Render(0), "REPORTED READY")
	require.Contains(t, snapshot.Render(0), "GitRepository sources/repo")
}
