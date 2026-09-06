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

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/view"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCNPGMutationPluginsAreDangerous(t *testing.T) {
	plugins := loadCNPGPlugins(t)

	for _, name := range []string{
		"cnpg-backup",
		"cnpg-hibernate",
		"cnpg-hibernate-off",
		"cnpg-psql",
		"cnpg-reload",
		"cnpg-restart",
	} {
		t.Run(name, func(t *testing.T) {
			plugin, ok := plugins.Plugins[name]
			require.True(t, ok, "missing plugin %q", name)
			assert.True(t, plugin.Dangerous, "mutation plugin must be disabled in read-only mode")
		})
	}
}

func TestCNPGDisruptivePluginsRequireConfirmation(t *testing.T) {
	plugins := loadCNPGPlugins(t)

	for _, name := range []string{
		"cnpg-backup",
		"cnpg-hibernate",
		"cnpg-hibernate-off",
		"cnpg-reload",
		"cnpg-restart",
	} {
		t.Run(name, func(t *testing.T) {
			plugin, ok := plugins.Plugins[name]
			require.True(t, ok, "missing plugin %q", name)
			require.NotNil(t, plugin.Confirm)
			assert.True(t, *plugin.Confirm)
		})
	}
}

func TestCNPGHibernateOffUsesSelectedNamespaceName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable stub test")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required for this optional plugin test")
	}
	plugin, ok := loadCNPGPlugins(t).Plugins["cnpg-hibernate-off"]
	require.True(t, ok)
	assert.Equal(t, []string{"namespace"}, plugin.Scopes)

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "kubectl.log")
	writeCNPGExecutable(t, binDir, "kubectl", `printf '<%s>' "$@" >"$LOG_PATH"`)
	writeCNPGExecutable(t, binDir, "less", "cat")

	err := executeCNPGPlugin(&plugin, view.Env{
		"CONTEXT":   "selected-context",
		"NAMESPACE": client.ClusterScope,
		"NAME":      "selected-namespace",
	}, append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"LOG_PATH="+logPath,
	))
	require.NoError(t, err)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, "<cnpg><hibernate><off><selected-namespace><-n><selected-namespace><--context><selected-context>", string(log))
}

func loadCNPGPlugins(t *testing.T) config.Plugins {
	t.Helper()

	contents, err := os.ReadFile("../../plugins/cloudnative-pg.yaml")
	require.NoError(t, err)

	var plugins config.Plugins
	require.NoError(t, yaml.Unmarshal(contents, &plugins))
	return plugins
}

func executeCNPGPlugin(plugin *config.Plugin, env view.Env, processEnv []string) error {
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

func writeCNPGExecutable(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	contents := "#!/usr/bin/env bash\nset -euo pipefail\n" + strings.TrimSpace(body) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o700)) //nolint:gosec // Test stub must be executable; only its owner has access inside t.TempDir().
}
