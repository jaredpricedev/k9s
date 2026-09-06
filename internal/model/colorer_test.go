// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
)

func TestResourceColorerUsesDynamicAndStaticRenderers(t *testing.T) {
	oldErr, oldStd := model1.ErrColor, model1.StdColor
	t.Cleanup(func() { model1.ErrColor, model1.StdColor = oldErr, oldStd })
	model1.ErrColor, model1.StdColor = tcell.ColorRed, tcell.ColorWhite
	for _, tt := range []struct {
		gvr, state string
		want       tcell.Color
	}{
		{"cert-manager.io/v1/certificates", "Expired", tcell.ColorRed},
		{"kustomize.toolkit.fluxcd.io/v1/kustomizations", "Failed", tcell.ColorRed},
		{"source.toolkit.fluxcd.io/v1/ocirepositories", "Failed", tcell.ColorRed},
		{client.FluxGVR.String(), "Failed", tcell.ColorRed},
		{"example.io/v1/widgets", "Failed", tcell.ColorWhite},
	} {
		header := model1.Header{{Name: "STATUS"}}
		row := model1.NewRowEvent(model1.EventUnchanged, model1.Row{Fields: model1.Fields{tt.state}})
		assert.Equal(t, tt.want, ColorerFor(client.NewGVR(tt.gvr))("", header, &row), tt.gvr)
	}
}
