// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package watch

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	di "k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/dynamic/fake"
)

func TestCachedGetUsesListNamespaceFactory(t *testing.T) {
	gvr := client.NewGVR("kustomize.toolkit.fluxcd.io/v1/kustomizations")
	tests := []struct {
		name      string
		namespace string
		fqn       string
		want      string
	}{
		{name: "namespace list", namespace: "team-a", fqn: "team-a/app", want: "namespace-cache"},
		{name: "all namespace list", namespace: "all", fqn: "team-a/app", want: "all-cache"},
		{name: "blank namespace list", namespace: "", fqn: "team-a/app", want: "all-cache"},
		{name: "concrete namespace within all", namespace: "all", fqn: "team-b/app", want: "other-namespace"},
		{name: "cluster scoped object", namespace: "-", fqn: "-/cluster-app", want: "cluster-object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, dyn := cachedGetFactory(t, gvr, map[string][]*unstructured.Unstructured{
				"team-a": {cachedGetObject("team-a", "app", "namespace-cache")},
				"": {
					cachedGetObject("team-a", "app", "all-cache"),
					cachedGetObject("team-b", "app", "other-namespace"),
					cachedGetObject("", "cluster-app", "cluster-object"),
				},
			})

			obj, err := f.CachedGet(gvr, tt.namespace, tt.fqn)

			require.NoError(t, err)
			require.IsType(t, &unstructured.Unstructured{}, obj)
			assert.Equal(t, tt.want, obj.(*unstructured.Unstructured).GetResourceVersion())
			assert.Empty(t, dyn.Actions(), "selection lookup must not make API requests")
		})
	}
}

func TestCachedGetUnavailableDoesNotFetchOrWait(t *testing.T) {
	gvr := client.NewGVR("kustomize.toolkit.fluxcd.io/v1/kustomizations")
	tests := []struct {
		name      string
		namespace string
		caches    map[string][]*unstructured.Unstructured
		notFound  bool
	}{
		{name: "no cache", namespace: "team-a"},
		{
			name: "no fallback to all namespace cache", namespace: "team-a",
			caches: map[string][]*unstructured.Unstructured{"": {cachedGetObject("team-a", "app", "1")}},
		},
		{
			name: "no fallback to concrete namespace cache", namespace: "all",
			caches: map[string][]*unstructured.Unstructured{"team-a": {cachedGetObject("team-a", "app", "1")}},
		},
		{
			name: "unsynced empty cache", namespace: "team-a", notFound: true,
			caches: map[string][]*unstructured.Unstructured{"team-a": nil},
		},
		{
			name: "missing object", namespace: "team-a", notFound: true,
			caches: map[string][]*unstructured.Unstructured{"team-a": {cachedGetObject("team-a", "other", "1")}},
		},
		{
			name: "empty cache does not fallback", namespace: "all", notFound: true,
			caches: map[string][]*unstructured.Unstructured{
				"": nil, "team-a": {cachedGetObject("team-a", "app", "1")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, dyn := cachedGetFactory(t, gvr, tt.caches)
			start := time.Now()

			obj, err := f.CachedGet(gvr, tt.namespace, "team-a/app")

			assert.Less(t, time.Since(start), defaultWaitTime/2, "selection lookup must not wait for cache sync")
			require.Error(t, err)
			assert.Nil(t, obj)
			assert.Contains(t, err.Error(), "refresh")
			assert.Equal(t, tt.notFound, apierrors.IsNotFound(err))
			assert.Empty(t, dyn.Actions(), "selection lookup must not make API requests")
		})
	}
}

func cachedGetFactory(t *testing.T, gvr *client.GVR, seeds map[string][]*unstructured.Unstructured) (*Factory, *fake.FakeDynamicClient) {
	t.Helper()
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	// A nil connection also ensures lookup cannot perform authorization or dial.
	f := NewFactory(nil)
	for namespace, objects := range seeds {
		fac := &cachedGetInformerFactory{DynamicSharedInformerFactory: di.NewFilteredDynamicSharedInformerFactory(dyn, 0, namespace, nil)}
		f.factories[namespace] = fac
		indexer := fac.ForResource(gvr.GVR()).Informer().GetIndexer()
		for _, obj := range objects {
			require.NoError(t, indexer.Add(obj))
		}
		t.Cleanup(func() {
			assert.False(t, fac.started, "selection lookup must not start informers")
			assert.False(t, fac.waited, "selection lookup must not wait for cache sync")
		})
	}
	return f, dyn
}

func cachedGetObject(namespace, name, resourceVersion string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata": map[string]any{
			"namespace": namespace, "name": name, "resourceVersion": resourceVersion,
		},
	}}
}

type cachedGetInformerFactory struct {
	di.DynamicSharedInformerFactory
	started, waited bool
}

func (f *cachedGetInformerFactory) Start(<-chan struct{}) {
	f.started = true
}

func (f *cachedGetInformerFactory) WaitForCacheSync(<-chan struct{}) map[schema.GroupVersionResource]bool {
	f.waited = true
	return nil
}
