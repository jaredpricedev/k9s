// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package render

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxHeaderCompactAndWideColumns(t *testing.T) {
	native := (Flux{}).Header("apps")
	unified := (Flux{Unified: true}).Header("apps")
	assert.Equal(t, []string{"NAMESPACE", "NAME", "STATUS", "SUSPEND", "REVISION", "SOURCE", "AGE"}, native.ColumnNames(false))
	assert.Equal(t, []string{"NAMESPACE", "NAME", "KIND", "STATUS", "SUSPEND", "REVISION", "SOURCE", "AGE"}, unified.ColumnNames(false))
	assert.Equal(t, native.ColumnNames(true), unified.ColumnNames(true))
	assert.True(t, native[2].Wide)
	assert.False(t, unified[2].Wide)
	assert.True(t, native[7].Wide)
	assert.True(t, unified[7].Wide)
}

func TestFluxColorerUsesThemeSeverityAndPreservesEventSemantics(t *testing.T) {
	old := []tcell.Color{model1.ErrColor, model1.PendingColor, model1.HighlightColor, model1.CompletedColor, model1.StdColor, model1.KillColor, model1.AddColor, model1.ModColor}
	defer func() {
		model1.ErrColor, model1.PendingColor, model1.HighlightColor, model1.CompletedColor, model1.StdColor, model1.KillColor, model1.AddColor, model1.ModColor = old[0], old[1], old[2], old[3], old[4], old[5], old[6], old[7]
	}()
	model1.ErrColor, model1.PendingColor, model1.HighlightColor, model1.CompletedColor, model1.StdColor, model1.KillColor, model1.AddColor, model1.ModColor = tcell.ColorRed, tcell.ColorYellow, tcell.ColorBlue, tcell.ColorGreen, tcell.ColorWhite, tcell.ColorGray, tcell.ColorAqua, tcell.ColorPurple
	// STATUS is deliberately reordered as it can be in a custom view.
	header := model1.Header{{Name: "NAME"}, {Name: "VALID"}, {Name: "STATUS"}}
	renderer := Flux{}
	colorer := renderer.ColorerFunc()
	for _, tt := range []struct {
		status string
		want   tcell.Color
	}{
		{"Failed", model1.ErrColor}, {"Restricted", model1.ErrColor}, {"Unknown", model1.PendingColor}, {"Reconciling", model1.PendingColor}, {"Pending", model1.PendingColor}, {"Suspended", model1.HighlightColor}, {"Ready", model1.CompletedColor},
	} {
		t.Run(tt.status, func(t *testing.T) {
			row := model1.RowEvent{Kind: model1.EventUpdate, Row: model1.Row{Fields: model1.Fields{"test", "true", tt.status}}}
			assert.Equal(t, tt.want, colorer("apps", header, &row))
			row.Kind = model1.EventDelete
			assert.Equal(t, model1.KillColor, colorer("apps", header, &row))
			row.Kind = model1.EventUnchanged
			row.Row.Fields[1] = "false"
			assert.Equal(t, model1.ErrColor, colorer("apps", header, &row))
		})
	}
	row := model1.RowEvent{Kind: model1.EventAdd, Row: model1.Row{Fields: model1.Fields{"test"}}}
	assert.Equal(t, model1.AddColor, colorer("apps", header, &row))
	assert.Equal(t, model1.AddColor, colorer("apps", model1.Header{{Name: "NAME"}}, &row))
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

func TestFluxCompactColumnsCanBeShownByCustomSettings(t *testing.T) {
	renderer := Flux{}
	renderer.SetViewSetting(&config.ViewSetting{Columns: []string{"NAME", "KIND|S", "MESSAGE|S"}})
	header := renderer.Header("apps")
	for _, name := range []string{"KIND", "MESSAGE"} {
		index, ok := header.IndexOf(name, true)
		require.True(t, ok)
		assert.False(t, header[index].Wide)
	}
	var row model1.Row
	require.NoError(t, renderer.Render(fluxObject(), "apps", &row))
	assert.Equal(t, "Kustomization", row.Fields[1])
	assert.Equal(t, "Applied revision main@sha1:abcd", row.Fields[2])
}
