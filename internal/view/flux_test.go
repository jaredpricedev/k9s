// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/flux"
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
