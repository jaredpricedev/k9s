// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package render

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxHeaderUsesSameColumnsForNativeAndUnifiedViews(t *testing.T) {
	want := model1.Header{
		model1.HeaderColumn{Name: "NAMESPACE"},
		model1.HeaderColumn{Name: "NAME"},
		model1.HeaderColumn{Name: "KIND"},
		model1.HeaderColumn{Name: "STATUS"},
		model1.HeaderColumn{Name: "SUSPEND"},
		model1.HeaderColumn{Name: "REVISION"},
		model1.HeaderColumn{Name: "SOURCE"},
		model1.HeaderColumn{Name: "MESSAGE"},
		model1.HeaderColumn{Name: "AGE", Attrs: model1.Attrs{Time: true}},
		model1.HeaderColumn{Name: "LABELS", Attrs: model1.Attrs{Wide: true}},
	}
	assert.Equal(t, want, (Flux{}).Header("apps"))
	assert.Equal(t, want, (Flux{Unified: true}).Header("apps"))
}

func TestFluxRenderBuildsNativeAndUnifiedRowIDs(t *testing.T) {
	o := fluxObject()
	wantFields := model1.Fields{
		"apps",
		"frontend",
		"Kustomization",
		"Ready",
		"false",
		"main@sha1:abcd",
		"GitRepository:shared/manifests",
		"Applied revision main@sha1:abcd",
		UnknownValue,
		"app=frontend,team=platform",
	}

	for _, tt := range []struct {
		name   string
		render Flux
		wantID string
	}{
		{name: "native", render: Flux{}, wantID: "apps/frontend"},
		{name: "unified", render: Flux{Unified: true}, wantID: "kustomize.toolkit.fluxcd.io/v1/kustomizations|apps/frontend"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var row model1.Row
			require.NoError(t, tt.render.Render(o, "apps", &row))
			assert.Equal(t, tt.wantID, row.ID)
			assert.Equal(t, wantFields, row.Fields)
		})
	}
}

func TestFluxRenderRejectsWrongType(t *testing.T) {
	err := (Flux{}).Render("not an object", "apps", &model1.Row{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected Unstructured")
}

func TestFluxRenderRejectsNilUnstructured(t *testing.T) {
	var o *unstructured.Unstructured
	assert.NotPanics(t, func() {
		err := (Flux{}).Render(o, "apps", &model1.Row{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected Unstructured")
	})
}

func TestFluxRenderPreservesCustomColumnsAndComputedDefaults(t *testing.T) {
	renderer := Flux{Unified: true}
	renderer.SetViewSetting(&config.ViewSetting{Columns: []string{
		"NAME",
		"TEAM:.metadata.labels.team",
		"STATUS",
	}})

	header := renderer.Header("apps")
	assert.Equal(t, []string{
		"NAME", "TEAM", "STATUS", "NAMESPACE", "KIND", "SUSPEND", "REVISION", "SOURCE", "MESSAGE", "AGE", "LABELS",
	}, header.ColumnNames(true))

	var row model1.Row
	require.NoError(t, renderer.Render(fluxObject(), "apps", &row))
	assert.Equal(t, "kustomize.toolkit.fluxcd.io/v1/kustomizations|apps/frontend", row.ID)
	assert.Equal(t, model1.Fields{
		"frontend",
		"platform",
		"Ready",
		"apps",
		"Kustomization",
		"false",
		"main@sha1:abcd",
		"GitRepository:shared/manifests",
		"Applied revision main@sha1:abcd",
		UnknownValue,
		"app=frontend,team=platform",
	}, row.Fields)
	assert.False(t, header[3].Wide)
	assert.True(t, header[9].Time)
	assert.True(t, header[10].Wide)
}

func TestFluxRenderUsesMissingValueForAbsentRevisionAndSource(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata": map[string]any{
			"name":      "empty",
			"namespace": "apps",
		},
	}}
	var row model1.Row
	require.NoError(t, (Flux{}).Render(o, "apps", &row))
	assert.Equal(t, MissingValue, row.Fields[5])
	assert.Equal(t, MissingValue, row.Fields[6])
}

func fluxObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata": map[string]any{
			"name":       "frontend",
			"namespace":  "apps",
			"generation": int64(2),
			"labels": map[string]any{
				"team": "platform",
				"app":  "frontend",
			},
		},
		"spec": map[string]any{
			"sourceRef": map[string]any{
				"kind":      "GitRepository",
				"name":      "manifests",
				"namespace": "shared",
			},
		},
		"status": map[string]any{
			"lastAppliedRevision": "main@sha1:abcd",
			"conditions": []any{map[string]any{
				"type":               "Ready",
				"status":             "True",
				"observedGeneration": int64(2),
				"reason":             "ReconciliationSucceeded",
				"message":            "Applied revision main@sha1:abcd",
			}},
		},
	}}
}
