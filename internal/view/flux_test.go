// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/flux"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFluxPath(t *testing.T) {
	gvr, fqn, ok := parseFluxPath("source.toolkit.fluxcd.io/v1/ocirepositories|team/apps")
	require.True(t, ok)
	assert.Equal(t, "ocirepositories", gvr.R())
	assert.Equal(t, "team/apps", fqn)
	for _, path := range []string{"", "apps", "apps|", "apps|team/apps", "source.toolkit.fluxcd.io/v1/ocirepositories|team/<restricted>"} {
		_, _, ok := parseFluxPath(path)
		assert.False(t, ok, path)
	}
}

func TestFluxBindingsRespectReadOnly(t *testing.T) {
	for _, ro := range []bool{false, true} {
		v := NewFlux(client.NewGVR("source.toolkit.fluxcd.io/v1/ocirepositories")).(*Flux)
		v.GetTable().app = &App{App: &ui.App{Configurator: ui.Configurator{Config: &config.Config{K9s: &config.K9s{ReadOnly: ro}}}}}
		aa := ui.NewKeyActions()
		v.bindKeys(aa)
		for _, key := range []tcell.Key{ui.KeyShiftR, ui.KeyShiftT} {
			a, ok := aa.Get(key)
			assert.Equal(t, !ro, ok)
			if ok {
				assert.True(t, a.Opts.Dangerous)
			}
		}
		_, ok := aa.Get(ui.KeyG)
		assert.True(t, ok)
	}
}

func TestFluxDashboardOffersInlineActions(t *testing.T) {
	for _, ro := range []bool{false, true} {
		v := NewFlux(client.FluxGVR).(*Flux)
		v.GetTable().app = &App{App: &ui.App{Configurator: ui.Configurator{Config: &config.Config{K9s: &config.K9s{ReadOnly: ro}}}}}
		aa := ui.NewKeyActions()
		v.bindKeys(aa)
		for _, key := range []tcell.Key{ui.KeyShiftR, ui.KeyShiftT} {
			action, ok := aa.Get(key)
			assert.Equal(t, !ro, ok)
			if ok {
				assert.True(t, action.Opts.Dangerous)
			}
		}
		_, ok := aa.Get(ui.KeyG)
		assert.True(t, ok)
	}
}

func TestLegacyFluxPluginsCannotReplaceInlineActions(t *testing.T) {
	for _, resource := range []string{"helm.toolkit.fluxcd.io/v2/helmreleases", "kustomize.toolkit.fluxcd.io/v1/kustomizations", "apps/v1/deployments"} {
		b := NewBrowser(client.NewGVR(resource)).(*Browser)
		for _, key := range []tcell.Key{ui.KeyShiftR, ui.KeyShiftT, ui.KeyShiftZ} {
			aa := ui.NewKeyActions()
			aa.Add(key, ui.NewKeyAction("Native", func(e *tcell.EventKey) *tcell.EventKey { return e }, true))
			p := &config.Plugin{Override: true, Description: "Legacy CLI", Command: "false"}
			require.NoError(t, bindPluginAction(b, aa, "legacy-flux", key, p))
			action, _ := aa.Get(key)
			protected := resource != "apps/v1/deployments" && key != ui.KeyShiftZ
			assert.Equal(t, !protected, action.Opts.Plugin)
		}
	}
}

func TestFluxReferenceResolutionUsesGroupAndStableVersion(t *testing.T) {
	m := dao.NewMeta()
	m.RegisterMeta("source.toolkit.fluxcd.io/v1beta2/ocirepositories", &metav1.APIResource{Kind: "OCIRepository"})
	m.RegisterMeta("source.toolkit.fluxcd.io/v1/ocirepositories", &metav1.APIResource{Kind: "OCIRepository"})
	m.RegisterMeta("other.io/v1/ocirepositories", &metav1.APIResource{Kind: "OCIRepository"})
	gvr, err := resolveFluxReference(m, flux.Reference{Group: "source.toolkit.fluxcd.io", Kind: "OCIRepository", Namespace: "team", Name: "apps"})
	require.NoError(t, err)
	assert.Equal(t, "source.toolkit.fluxcd.io/v1/ocirepositories", gvr.String())
}

func TestSyntheticBrowserDoesNotAuthorizeOrWatchSyntheticResource(t *testing.T) {
	b := &Browser{meta: &metav1.APIResource{Categories: []string{"k9s"}}}
	// No Kubernetes connection exists: synthetic views must authorize their
	// constituent resources in their DAO instead of asking for a fake CRD.
	require.NoError(t, b.canSwitchNamespace("team"))
	synced, err := b.cacheSynced()
	require.NoError(t, err)
	assert.True(t, synced)
}

func TestFluxStatusBindingAvailableInAllModes(t *testing.T) {
	for _, gvr := range []*client.GVR{client.FluxGVR, client.NewGVR("source.toolkit.fluxcd.io/v1/gitrepositories")} {
		v := NewFlux(gvr).(*Flux)
		v.GetTable().app = &App{App: &ui.App{Configurator: ui.Configurator{Config: &config.Config{K9s: &config.K9s{ReadOnly: true}}}}}
		aa := ui.NewKeyActions()
		v.bindKeys(aa)
		action, ok := aa.Get(ui.KeyI)
		require.True(t, ok)
		assert.True(t, action.Opts.Visible)
		assert.False(t, action.Opts.Dangerous)
	}
}

func TestFluxStatusDetailsPreservesFullMessageAndCustomColumnOrder(t *testing.T) {
	message := "[red]dependency not ready\n" + strings.Repeat("retry after source reconciliation ", 100)
	header := model1.Header{{Name: "MESSAGE", Attrs: model1.Attrs{Wide: true}}, {Name: "PRIVATE"}, {Name: "STATUS"}, {Name: "NAME"}, {Name: "KIND"}}
	row := &model1.Row{ID: "source.toolkit.fluxcd.io/v1/gitrepositories|apps/<restricted>", Fields: model1.Fields{message, "do not include custom data", "Restricted", "<restricted>", "GitRepository"}}
	details := fluxStatusDetails(header, row)
	assert.Contains(t, details, "STATUS: Restricted")
	assert.Contains(t, details, "[red[]dependency not ready\n"+strings.Repeat("retry after source reconciliation ", 100))
	assert.NotContains(t, details, "do not include custom data")
	assert.Empty(t, fluxStatusDetails(header, nil))
	assert.NotPanics(t, func() { fluxStatusDetails(header, &model1.Row{}) })
}
