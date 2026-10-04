// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

const (
	diagnosticFixtureActor    = "actor"
	diagnosticFixtureOther    = "other"
	diagnosticFixtureMutation = "mutated"
)

func TestPinnedDiagnosticConfigReloadsActorWithoutGlobalContextSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	raw := api.Config{
		CurrentContext: diagnosticFixtureActor,
		Contexts: map[string]*api.Context{
			diagnosticFixtureActor: {Cluster: "actor-cluster", AuthInfo: "actor-user"},
			diagnosticFixtureOther: {Cluster: "other-cluster", AuthInfo: "other-user"},
		},
		Clusters: map[string]*api.Cluster{
			"actor-cluster": {Server: "https://actor.example"},
			"other-cluster": {Server: "https://other.example"},
		},
		AuthInfos: map[string]*api.AuthInfo{
			"actor-user": {Token: "old-token"}, "other-user": {Token: "other-token"},
		},
	}
	require.NoError(t, clientcmd.WriteToFile(raw, path))
	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path
	actor := NewConfig(flags)
	loaded, err := actor.RESTConfig()
	require.NoError(t, err)
	require.Equal(t, "old-token", loaded.BearerToken)

	// The global file now points elsewhere and has refreshed actor credentials.
	raw.CurrentContext = diagnosticFixtureOther
	raw.AuthInfos["actor-user"].Token = "new-token"
	require.NoError(t, clientcmd.WriteToFile(raw, path))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	pinned, err := actor.PinnedDiagnosticConfig(diagnosticFixtureActor)
	require.NoError(t, err)
	refreshed, err := pinned.RESTConfig()
	require.NoError(t, err)
	require.Equal(t, "https://actor.example", refreshed.Host)
	require.Equal(t, "new-token", refreshed.BearerToken)
	unchanged, err := actor.RESTConfig()
	require.NoError(t, err)
	require.Equal(t, "old-token", unchanged.BearerToken, "running actor handles must remain untouched")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "diagnostics must not write global kubeconfig")
}

func TestPinnedDiagnosticConfigPreservesOverridesWithoutSharedPointers(t *testing.T) {
	flags := genericclioptions.NewConfigFlags(true)
	//nolint:gosec // Synthetic credential overrides verify that diagnostic copies preserve actor settings without sharing pointers.
	token, server, actor, impersonate, uid, username, password := "explicit-token", "https://override.example", diagnosticFixtureActor, "reviewer", "42", "user", "pass"
	flags.BearerToken, flags.APIServer, flags.Context = &token, &server, &actor
	flags.Impersonate, flags.ImpersonateUID = &impersonate, &uid
	flags.Username, flags.Password = &username, &password
	groups, extras := []string{"ops"}, []string{"scope=read"}
	flags.ImpersonateGroup, flags.ImpersonateUserExtra = &groups, &extras
	pinned, err := NewConfig(flags).PinnedDiagnosticConfig(actor)
	require.NoError(t, err)
	//nolint:gosec // Mutating a synthetic fixture token verifies that a previously captured diagnostic config keeps its own value.
	token, server, actor = diagnosticFixtureMutation, "https://other.example", diagnosticFixtureOther
	groups[0], extras[0] = diagnosticFixtureMutation, diagnosticFixtureMutation
	require.Equal(t, "explicit-token", *pinned.flags.BearerToken)
	require.Equal(t, "https://override.example", *pinned.flags.APIServer)
	require.Equal(t, diagnosticFixtureActor, *pinned.flags.Context)
	require.Equal(t, "reviewer", *pinned.flags.Impersonate)
	require.Equal(t, "42", *pinned.flags.ImpersonateUID)
	require.Equal(t, []string{"ops"}, *pinned.flags.ImpersonateGroup)
	require.Equal(t, []string{"scope=read"}, *pinned.flags.ImpersonateUserExtra)
	require.Equal(t, "user", *pinned.flags.Username)
	require.Equal(t, "pass", *pinned.flags.Password)
}

func TestPinnedDiagnosticConfigUnavailableDoesNotPanic(t *testing.T) {
	var cfg *Config
	_, err := cfg.PinnedDiagnosticConfig(diagnosticFixtureActor)
	require.Error(t, err)
	_, err = NewConfig(nil).PinnedDiagnosticConfig(diagnosticFixtureActor)
	require.Error(t, err)
	_, err = NewConfig(genericclioptions.NewConfigFlags(true)).PinnedDiagnosticConfig("")
	require.Error(t, err)
}
