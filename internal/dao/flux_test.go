// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestPatchFlux(t *testing.T) {
	gvr := client.NewGVR("kustomize.toolkit.fluxcd.io/v1/kustomizations")
	for _, action := range []string{"reconcile", "suspend", "resume"} {
		t.Run(action, func(t *testing.T) {
			o := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": gvr.GV().String(), "kind": "Kustomization",
				"metadata": map[string]any{"name": "apps", "namespace": "team", "resourceVersion": "12", "uid": "original", "annotations": map[string]any{"keep": "yes"}},
				"spec":     map[string]any{"suspend": false, "interval": "5m"},
			}}
			c := fake.NewSimpleDynamicClient(runtime.NewScheme(), o)
			c.PrependReactor("patch", "kustomizations", func(a ktesting.Action) (bool, runtime.Object, error) {
				p := a.(ktesting.PatchAction)
				assert.Equal(t, types.MergePatchType, p.GetPatchType())
				var patch map[string]any
				require.NoError(t, json.Unmarshal(p.GetPatch(), &patch))
				assert.Equal(t, "12", patch["metadata"].(map[string]any)["resourceVersion"])
				assert.Equal(t, "original", patch["metadata"].(map[string]any)["uid"])
				return false, nil, nil
			})
			now := time.Date(2026, 9, 6, 0, 0, 0, 123, time.UTC)
			err := patchFlux(context.Background(), c.Resource(gvr.GVR()).Namespace("team"), "apps", "original", action, now)
			require.NoError(t, err)
			got, err := c.Resource(gvr.GVR()).Namespace("team").Get(context.Background(), "apps", metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, "yes", got.GetAnnotations()["keep"])
			interval, _, _ := unstructured.NestedString(got.Object, "spec", "interval")
			assert.Equal(t, "5m", interval)
			if action == "reconcile" {
				assert.Equal(t, now.Format(time.RFC3339Nano), got.GetAnnotations()["reconcile.fluxcd.io/requestedAt"])
			} else {
				suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend")
				assert.Equal(t, action == "suspend", suspended)
			}
		})
	}
}

func TestReconcileReturnsOnAcceptanceAndDoesNotQueueDuplicate(t *testing.T) {
	gvr := client.NewGVR("helm.toolkit.fluxcd.io/v2/helmreleases")
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.GV().String(), "kind": "HelmRelease",
		"metadata": map[string]any{"name": "apps", "namespace": "team", "resourceVersion": "12", "uid": "original"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}},
	}}
	c := fake.NewSimpleDynamicClient(runtime.NewScheme(), o)
	resource := c.Resource(gvr.GVR()).Namespace("team")
	// This fake has no controller. A request must finish at API acceptance,
	// without polling or waiting for Ready, and preserve the old conditions.
	require.NoError(t, patchFlux(t.Context(), resource, "apps", "original", "reconcile", time.Now()))
	require.Len(t, c.Actions(), 2)
	assert.Equal(t, "get", c.Actions()[0].GetVerb())
	assert.Equal(t, "patch", c.Actions()[1].GetVerb())
	c.ClearActions()
	err := patchFlux(t.Context(), resource, "apps", "original", "reconcile", time.Now())
	require.ErrorContains(t, err, "already queued")
	require.Len(t, c.Actions(), 1)
	assert.Equal(t, "get", c.Actions()[0].GetVerb())
}

func TestFluxActionsRejectStaticOCIHelmRepository(t *testing.T) {
	gvr := client.NewGVR("source.toolkit.fluxcd.io/v1/helmrepositories")
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.GV().String(), "kind": "HelmRepository",
		"metadata": map[string]any{"name": "charts", "namespace": "team", "resourceVersion": "12", "uid": "original"},
		"spec":     map[string]any{"type": "oci", "url": "oci://example.test/charts"},
	}}
	for _, action := range []string{"reconcile", "suspend", "resume"} {
		t.Run(action, func(t *testing.T) {
			c := fake.NewSimpleDynamicClient(runtime.NewScheme(), o)
			err := patchFlux(t.Context(), c.Resource(gvr.GVR()).Namespace("team"), "charts", "original", action, time.Now())
			require.ErrorContains(t, err, "static")
			require.Len(t, c.Actions(), 1)
			assert.Equal(t, "get", c.Actions()[0].GetVerb())
		})
	}
}

func TestPatchFluxRejectsChangedIdentityAndUnknownAction(t *testing.T) {
	gvr := client.NewGVR("source.toolkit.fluxcd.io/v1/ocirepositories")
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(gvr.GV().String())
	o.SetKind("OCIRepository")
	o.SetNamespace("team")
	o.SetName("apps")
	o.SetUID("replacement")
	o.SetResourceVersion("2")
	for _, action := range []string{"reconcile", "delete"} {
		c := fake.NewSimpleDynamicClient(runtime.NewScheme(), o)
		err := patchFlux(context.Background(), c.Resource(gvr.GVR()).Namespace("team"), "apps", "original", action, time.Now())
		require.Error(t, err)
		for _, a := range c.Actions() {
			assert.NotEqual(t, "patch", a.GetVerb())
		}
	}
}

func TestFluxNativeActions(t *testing.T) {
	assert.True(t, FluxNativeActions(client.NewGVR("source.toolkit.fluxcd.io/v1/ocirepositories")))
	assert.True(t, FluxNativeActions(client.NewGVR("helm.toolkit.fluxcd.io/v2/helmreleases")))
	assert.False(t, FluxNativeActions(client.NewGVR("example.com/v1/helmreleases")))
	assert.False(t, FluxNativeActions(client.NewGVR("fluxcd.controlplane.io/v1/resourcesets")))
}

func TestFluxDashboardCannotEditOrDeleteSyntheticRows(t *testing.T) {
	metas := make(ResourceMetas)
	loadK9s(metas)
	meta := metas[client.FluxGVR]
	require.NotNil(t, meta)
	assert.False(t, client.Can(meta.Verbs, "edit"))
	assert.False(t, client.Can(meta.Verbs, "delete"))
}

func TestFluxResourceVersions(t *testing.T) {
	metas := NewMeta()
	for _, v := range []string{"v1beta2", "v1", "v1beta1"} {
		metas.RegisterMeta("source.toolkit.fluxcd.io/"+v+"/ocirepositories", &metav1.APIResource{Kind: "OCIRepository", Namespaced: true})
	}
	metas.RegisterMeta("apps/v1/deployments", &metav1.APIResource{Kind: "Deployment"})
	got := fluxResourceVersions(metas)
	require.Len(t, got, 1)
	assert.Equal(t, "source.toolkit.fluxcd.io/v1/ocirepositories", got[0].String())
}

type fluxListFactory struct {
	Factory
	calls []string
}

func (f *fluxListFactory) List(gvr *client.GVR, ns string, wait bool, sel labels.Selector) ([]runtime.Object, error) {
	f.calls = append(f.calls, gvr.String()+"|"+ns+"|"+sel.String())
	if wait {
		panic("Flux dashboard must not block on cache synchronization")
	}
	if gvr.R() == "helmreleases" {
		return nil, errors.New("list access denied")
	}
	o := &unstructured.Unstructured{}
	o.SetName("apps")
	return []runtime.Object{o}, nil
}

func TestListFluxRetainsAccessibleResources(t *testing.T) {
	metas := NewMeta()
	metas.RegisterMeta("source.toolkit.fluxcd.io/v1/ocirepositories", &metav1.APIResource{Kind: "OCIRepository"})
	metas.RegisterMeta("helm.toolkit.fluxcd.io/v2/helmreleases", &metav1.APIResource{Kind: "HelmRelease"})
	f := &fluxListFactory{}
	got, err := listFlux(context.Background(), f, metas, "team")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "<restricted>", got[0].(*unstructured.Unstructured).GetName())
	assert.Equal(t, "list access denied", got[0].(*unstructured.Unstructured).GetAnnotations()["k9scli.io/flux-list-error"])
	assert.Equal(t, "apps", got[1].(*unstructured.Unstructured).GetName())
	assert.Equal(t, []string{"helm.toolkit.fluxcd.io/v2/helmreleases|team|", "source.toolkit.fluxcd.io/v1/ocirepositories|team|"}, f.calls)
}
