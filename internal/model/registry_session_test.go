// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package model

import (
	"context"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/render"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

type sessionModelFactory struct {
	dao.Factory
	uid string
}

func (f sessionModelFactory) Get(*client.GVR, string, bool, labels.Selector) (runtime.Object, error) {
	return &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"uid": f.uid}}}, nil
}

func TestSessionModelCollectorsOwnFactoryAndRendererState(t *testing.T) {
	firstContext := context.WithValue(t.Context(), internal.KeyFactory, sessionModelFactory{uid: "old-collector"})
	freshContext := context.WithValue(t.Context(), internal.KeyFactory, sessionModelFactory{uid: "fresh-collector"})
	first, err := getMeta(firstContext, client.PodGVR)
	require.NoError(t, err)
	fresh, err := getMeta(freshContext, client.PodGVR)
	require.NoError(t, err)
	first.DAO.Init(sessionModelFactory{uid: "late-old-collector"}, client.PodGVR)
	obj, err := fresh.DAO.Get(t.Context(), "team/app")
	require.NoError(t, err)
	require.Equal(t, "fresh-collector", string(obj.(*render.PodWithMetrics).Raw.GetUID()))
	require.NotSame(t, first.Renderer, fresh.Renderer)
	// Tree renderers such as xray.Pod are stateless zero-size values, whose
	// allocations may share an address. Actor and mutable renderer ownership
	// are the isolation guarantees exercised here.
}
