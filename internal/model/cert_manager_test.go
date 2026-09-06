// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model_test

import (
	"context"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestCertificateTableUsesCachedNativeRenderer(t *testing.T) {
	f := makeTableFactory()
	f.rows = []runtime.Object{&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": "expired", "namespace": "apps"},
		"status":   map[string]any{"notAfter": "2000-01-01T00:00:00Z"},
	}}}
	table := model.NewTable(client.NewGVR("cert-manager.io/v1/certificates"))
	table.SetNamespace("apps")
	ctx := context.WithValue(context.Background(), internal.KeyFactory, f)
	require.NoError(t, table.Refresh(ctx))
	data := table.Peek()
	require.Equal(t, 1, data.RowCount())
	row, ok := data.RowAt(0)
	require.True(t, ok)
	assert.Equal(t, "apps/expired", row.Row.ID)
	assert.Contains(t, row.Row.Fields, "Expired")
	assert.Contains(t, data.Header().ColumnNames(true), "EXPIRES")
}
