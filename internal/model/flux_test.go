// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model_test

import (
	"context"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestFluxTablesUseNativeRendererAndCachedList(t *testing.T) {
	gvr := client.NewGVR("source.toolkit.fluxcd.io/v1/ocirepositories")
	previous := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = previous })
	dao.MetaAccess.RegisterMeta(gvr.String(), &metav1.APIResource{Kind: "OCIRepository", Namespaced: true})
	f := makeTableFactory()
	f.rows = []runtime.Object{&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.GV().String(), "kind": "OCIRepository",
		"metadata": map[string]any{"name": "apps", "namespace": "team", "generation": int64(2)},
		"status":   map[string]any{"observedGeneration": int64(2), "artifact": map[string]any{"revision": "sha256:abc"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": "stored artifact"}}},
	}}}
	ctx := context.WithValue(context.Background(), internal.KeyFactory, f)
	for _, tableGVR := range []*client.GVR{gvr, client.FluxGVR} {
		t.Run(tableGVR.String(), func(t *testing.T) {
			table := model.NewTable(tableGVR)
			table.SetNamespace("team")
			require.NoError(t, table.Refresh(ctx))
			data := table.Peek()
			require.Equal(t, 1, data.RowCount())
			row, ok := data.RowAt(0)
			require.True(t, ok)
			expectedID := "team/apps"
			if tableGVR == client.FluxGVR {
				expectedID = gvr.String() + "|" + expectedID
			}
			assert.Equal(t, expectedID, row.Row.ID)
			assert.Contains(t, row.Row.Fields, "Ready")
			assert.Contains(t, row.Row.Fields, "sha256:abc")
		})
	}
}
