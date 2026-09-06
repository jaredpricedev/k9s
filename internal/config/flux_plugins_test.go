// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/view"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func loadFluxPlugins(t *testing.T) config.Plugins {
	t.Helper()

	bb, err := os.ReadFile("../../plugins/flux.yaml")
	require.NoError(t, err)

	var pp config.Plugins
	require.NoError(t, yaml.Unmarshal(bb, &pp))
	return pp
}

func TestFluxMutationPluginsAreDangerous(t *testing.T) {
	pp := loadFluxPlugins(t)
	mutationPlugins := []string{
		"toggle-helmrelease",
		"toggle-kustomization",
		"reconcile-git",
		"reconcile-hr",
		"reconcile-helm-repo",
		"reconcile-oci-repo",
		"reconcile-ks",
		"reconcile-ir",
		"reconcile-iua",
		"toggle-rset",
		"toggle-inputprovider",
		"reconcile-rset",
		"reconcile-inputprovider",
		"reconcile-fluxinstance",
		"toggle-fluxinstance",
	}

	for _, name := range mutationPlugins {
		t.Run(name, func(t *testing.T) {
			plugin, ok := pp.Plugins[name]
			require.True(t, ok, "missing plugin %q", name)
			assert.True(t, plugin.Dangerous, "mutation plugin must be disabled in read-only mode")
			if plugin.ShortCut == "Shift-R" || plugin.ShortCut == "Shift-T" {
				assert.True(t, plugin.Override, "bundled plugin must explicitly override the native Flux action")
			}
		})
	}
}

func TestFluxInputProviderReconcileScope(t *testing.T) {
	plugin, ok := loadFluxPlugins(t).Plugins["reconcile-inputprovider"]
	require.True(t, ok)
	assert.Equal(t, []string{"resourcesetinputproviders"}, plugin.Scopes)
}

func TestFluxReadOnlyDiagnostics(t *testing.T) {
	pp := loadFluxPlugins(t)

	for _, name := range []string{"trace", "tree-kustomization", "controller-logs"} {
		t.Run(name, func(t *testing.T) {
			plugin, ok := pp.Plugins[name]
			require.True(t, ok, "missing plugin %q", name)
			assert.False(t, plugin.Dangerous)
		})
	}

	for _, name := range []string{"get-suspended-helmreleases", "get-suspended-kustomizations"} {
		t.Run(name, func(t *testing.T) {
			plugin, ok := pp.Plugins[name]
			require.True(t, ok, "missing plugin %q", name)
			assert.False(t, plugin.Dangerous)
			assert.True(t, plugin.Override, "bundled plugin must explicitly override Sort Status")
		})
	}
}

func TestFluxTraceTargetsSelectedCluster(t *testing.T) {
	requirePluginTestTools(t, "bash", "jq")

	pp := loadFluxPlugins(t)
	plugin := pp.Plugins["trace"]

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	injectionMarker := filepath.Join(t.TempDir(), "injected")
	writeExecutable(t, binDir, "kubectl", `
printf 'kubectl KUBECONFIG=<%s>' "${KUBECONFIG-}" >>"$LOG_PATH"
printf ' <%s>' "$@" >>"$LOG_PATH"
printf '\n' >>"$LOG_PATH"
printf '%s\n' '{"resources":[{"name":"deployments","kind":"Deployment","namespaced":true}]}'
`)
	writeExecutable(t, binDir, "flux", `
printf 'flux KUBECONFIG=<%s>' "${KUBECONFIG-}" >>"$LOG_PATH"
printf ' <%s>' "$@" >>"$LOG_PATH"
printf '\n' >>"$LOG_PATH"
`)
	writeExecutable(t, binDir, "less", "cat")

	env := view.Env{
		"CONTEXT":          "selected context; touch " + injectionMarker,
		"KUBECONFIG":       "/tmp/kube config; printf INJECTED",
		"NAMESPACE":        "selected-namespace",
		"NAME":             "selected-name",
		"RESOURCE_GROUP":   "apps",
		"RESOURCE_VERSION": "v1",
		"RESOURCE_NAME":    "deployments",
	}
	err := executePlugin(&plugin, env, append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"LOG_PATH="+logPath,
		"KUBECONFIG=",
	))
	require.NoError(t, err)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "kubectl KUBECONFIG=</tmp/kube config; printf INJECTED> <--context> <selected context; touch "+injectionMarker+">")
	assert.Contains(t, string(log), "flux KUBECONFIG=</tmp/kube config; printf INJECTED> <trace> <--context> <selected context; touch "+injectionMarker+">")
	assert.NoFileExists(t, injectionMarker)
}

func TestFluxTraceClusterScopedResourceOmitsNamespace(t *testing.T) {
	requirePluginTestTools(t, "bash", "jq")

	plugin := loadFluxPlugins(t).Plugins["trace"]
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	writeExecutable(t, binDir, "kubectl", `
printf '%s\n' '{"resources":[{"name":"namespaces","kind":"Namespace","namespaced":false}]}'
`)
	writeExecutable(t, binDir, "flux", `
printf '<%s>' "$@" >"$LOG_PATH"
`)
	writeExecutable(t, binDir, "less", "cat")

	err := executePlugin(&plugin, view.Env{
		"CONTEXT":          "selected-context",
		"KUBECONFIG":       "/tmp/kubeconfig",
		"NAMESPACE":        "-",
		"NAME":             "selected-name",
		"RESOURCE_GROUP":   "",
		"RESOURCE_VERSION": "v1",
		"RESOURCE_NAME":    "namespaces",
	}, append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"LOG_PATH="+logPath,
	))
	require.NoError(t, err)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "<--kind><Namespace><--api-version><v1><selected-name>")
	assert.NotContains(t, string(log), "<--namespace>")
}

func TestFluxTraceRejectsMalformedDiscovery(t *testing.T) {
	requirePluginTestTools(t, "bash", "jq")

	plugin := loadFluxPlugins(t).Plugins["trace"]
	binDir := t.TempDir()
	fluxMarker := filepath.Join(t.TempDir(), "flux-ran")
	writeExecutable(t, binDir, "kubectl", `
printf '%s\n' '{"resources":[{"name":"namespaces","kind":"Namespace"}]}'
`)
	writeExecutable(t, binDir, "flux", `
touch "$FLUX_MARKER"
`)
	writeExecutable(t, binDir, "less", "cat")

	err := executePlugin(&plugin, view.Env{
		"CONTEXT":          "selected-context",
		"KUBECONFIG":       "/tmp/kubeconfig",
		"NAMESPACE":        "-",
		"NAME":             "selected-name",
		"RESOURCE_GROUP":   "",
		"RESOURCE_VERSION": "v1",
		"RESOURCE_NAME":    "namespaces",
	}, append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"FLUX_MARKER="+fluxMarker,
	))
	require.Error(t, err)
	assert.NoFileExists(t, fluxMarker)
}

func TestFluxPipelinePropagatesCommandFailure(t *testing.T) {
	requirePluginTestTools(t, "bash")

	plugin := loadFluxPlugins(t).Plugins["reconcile-oci-repo"]

	binDir := t.TempDir()
	writeExecutable(t, binDir, "flux", "exit 37")
	writeExecutable(t, binDir, "less", "cat")

	err := executePlugin(&plugin, view.Env{
		"CONTEXT":    "selected-context",
		"KUBECONFIG": "/tmp/kubeconfig",
		"NAMESPACE":  "selected-namespace",
		"NAME":       "selected-name",
	}, append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 37")
}

func executePlugin(plugin *config.Plugin, env view.Env, processEnv []string) error {
	args := make([]string, len(plugin.Args))
	for i, arg := range plugin.Args {
		var err error
		args[i], err = env.Substitute(arg)
		if err != nil {
			return fmt.Errorf("substitute argument %d: %w", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, plugin.Command, args...)
	cmd.Env = processEnv
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func writeExecutable(t *testing.T, dir, name, body string) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable stub test")
	}
	t.Helper()
	path := filepath.Join(dir, name)
	contents := "#!/usr/bin/env bash\nset -euo pipefail\n" + strings.TrimSpace(body) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o700)) //nolint:gosec // Test stub must be executable; only its owner has access inside t.TempDir().
}

func requirePluginTestTools(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is required to execute Flux plugin regression tests", name)
		}
	}
}
