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
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view"
	"github.com/derailed/tcell/v2"
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
		})
	}
}

func TestFluxCorePluginsPreserveNativeShortcuts(t *testing.T) {
	pp := loadFluxPlugins(t)
	supportedKeys := make(map[string]tcell.Key, len(tcell.KeyNames))
	for key, name := range tcell.KeyNames {
		supportedKeys[name] = key
	}
	for _, scope := range []string{
		"helmreleases", "kustomizations", "gitrepositories", "helmrepositories",
		"ocirepositories", "helmcharts", "buckets",
	} {
		t.Run(scope, func(t *testing.T) {
			claimedKeys := make(map[tcell.Key]string)
			for name, plugin := range pp.Plugins {
				for _, pluginScope := range plugin.Scopes {
					if pluginScope != scope && pluginScope != "all" {
						continue
					}
					key, ok := supportedKeys[plugin.ShortCut]
					require.True(t, ok, "%s must use a supported shortcut", name)
					assert.NotEqual(t, ui.KeyShiftR, key, "%s must preserve native reconcile", name)
					assert.NotEqual(t, ui.KeyShiftT, key, "%s must preserve native suspend/resume", name)
					previous, claimed := claimedKeys[key]
					assert.False(t, claimed, "%s and %s share %s", name, previous, plugin.ShortCut)
					claimedKeys[key] = name
					assert.Contains(t, plugin.Description, "CLI", "%s must identify its external command", name)
					if strings.HasPrefix(name, "reconcile-") {
						assert.Contains(t, plugin.Description, "wait", "%s must explain that CLI reconciliation waits", name)
					}
					break
				}
			}
		})
	}
}

func TestFluxOptionalCLIActions(t *testing.T) {
	requirePluginTestTools(t, "bash")
	pp := loadFluxPlugins(t)
	for _, tc := range []struct {
		name    string
		plugin  string
		command []string
		inputs  view.Env
		flags   []string
		state   string
	}{
		{name: "git", plugin: "reconcile-git", command: []string{"reconcile", "source", "git"}},
		{name: "helm repository", plugin: "reconcile-helm-repo", command: []string{"reconcile", "source", "helm"}},
		{name: "oci", plugin: "reconcile-oci-repo", command: []string{"reconcile", "source", "oci"}},
		{name: "helmrelease", plugin: "reconcile-hr", command: []string{"reconcile", "helmrelease"}},
		{name: "helmrelease force", plugin: "reconcile-hr", command: []string{"reconcile", "helmrelease"}, inputs: view.Env{"INPUT_FORCE": "true"}, flags: []string{"--force"}},
		{name: "helmrelease reset", plugin: "reconcile-hr", command: []string{"reconcile", "helmrelease"}, inputs: view.Env{"INPUT_RESET": "true"}, flags: []string{"--reset"}},
		{name: "helmrelease source", plugin: "reconcile-hr", command: []string{"reconcile", "helmrelease"}, inputs: view.Env{"INPUT_SOURCE": "true"}, flags: []string{"--with-source"}},
		{name: "kustomization", plugin: "reconcile-ks", command: []string{"reconcile", "kustomization"}},
		{name: "kustomization source", plugin: "reconcile-ks", command: []string{"reconcile", "kustomization"}, inputs: view.Env{"INPUT_SOURCE": "true"}, flags: []string{"--with-source"}},
		{name: "helmrelease suspend", plugin: "toggle-helmrelease", command: []string{"suspend", "helmrelease"}, state: "false"},
		{name: "helmrelease resume", plugin: "toggle-helmrelease", command: []string{"resume", "helmrelease"}, state: "true"},
		{name: "kustomization suspend", plugin: "toggle-kustomization", command: []string{"suspend", "kustomization"}, state: "false"},
		{name: "kustomization resume", plugin: "toggle-kustomization", command: []string{"resume", "kustomization"}, state: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin, ok := pp.Plugins[tc.plugin]
			require.True(t, ok)
			assert.False(t, plugin.Override, "optional CLI actions must not replace native actions")
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "flux.log")
			kubectlLogPath := filepath.Join(t.TempDir(), "kubectl.log")
			injectionMarker := filepath.Join(t.TempDir(), "injected")
			writeExecutable(t, binDir, "flux", `printf '%s\0' "${KUBECONFIG-}" "$@" >"$LOG_PATH"`)
			writeExecutable(t, binDir, "kubectl", `
printf '%s\0' "${KUBECONFIG-}" "$@" >"$KUBECTL_LOG_PATH"
printf '%s' "$SUSPEND_STATE"
`)
			writeExecutable(t, binDir, "less", "cat")
			env := view.Env{
				"CONTEXT":      "selected context; touch " + injectionMarker,
				"KUBECONFIG":   "/tmp/kube config; touch " + injectionMarker,
				"NAMESPACE":    "selected namespace; touch " + injectionMarker,
				"NAME":         "selected name; touch " + injectionMarker,
				"INPUT_FORCE":  "false",
				"INPUT_RESET":  "false",
				"INPUT_SOURCE": "false",
			}
			for name, value := range tc.inputs {
				env[name] = value
			}
			require.NoError(t, executePlugin(&plugin, env, append(os.Environ(),
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"LOG_PATH="+logPath,
				"KUBECTL_LOG_PATH="+kubectlLogPath,
				"SUSPEND_STATE="+tc.state,
			)))
			log, err := os.ReadFile(logPath)
			require.NoError(t, err)
			want := append([]string{env["KUBECONFIG"]}, tc.command...)
			want = append(want, "--context", env["CONTEXT"], "-n", env["NAMESPACE"], env["NAME"])
			want = append(want, tc.flags...)
			assert.Equal(t, want, strings.Split(strings.TrimSuffix(string(log), "\x00"), "\x00"))
			if tc.state != "" {
				log, err := os.ReadFile(kubectlLogPath)
				require.NoError(t, err)
				assert.Equal(t, []string{env["KUBECONFIG"], "--context", env["CONTEXT"], "get", tc.command[1], "-n", env["NAMESPACE"], env["NAME"], "-o", "jsonpath={.spec.suspend}"}, strings.Split(strings.TrimSuffix(string(log), "\x00"), "\x00"))
			}
			assert.NoFileExists(t, injectionMarker)
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
