// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
	"github.com/derailed/k9s/internal/config/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolatedLocations(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(xdg.Reload)
	for _, kind := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+kind+"_HOME", filepath.Join(root, kind))
	}
	t.Setenv("K9PLUS_CONFIG_DIR", "")
	t.Setenv("K9PLUS_LOGS_DIR", "")
	xdg.Reload()
	for _, location := range []*string{&AppConfigDir, &AppSkinsDir, &AppBenchmarksDir, &AppDumpsDir, &AppContextsDir, &AppConfigFile, &AppLogFile, &AppViewsFile, &AppJumpsFile, &AppAliasesFile, &AppPluginsFile, &AppHotKeysFile} {
		previous := *location
		t.Cleanup(func() { *location = previous })
	}
	return root
}

func TestK9PlusLocationsIgnoreLegacyOverrides(t *testing.T) {
	root := isolatedLocations(t)
	legacyConfig := filepath.Join(root, "legacy-config")
	legacyLogs := filepath.Join(root, "legacy-logs")
	t.Setenv("K9S_CONFIG_DIR", legacyConfig)
	t.Setenv("K9S_LOGS_DIR", legacyLogs)

	require.NoError(t, InitLocs())
	require.NoError(t, InitLogLoc())
	assert.Equal(t, filepath.Join(root, "CONFIG", "k9plus"), AppConfigDir)
	assert.Equal(t, filepath.Join(root, "DATA", "k9plus", "clusters"), AppContextsDir)
	assert.Equal(t, filepath.Join(root, "STATE", "k9plus", "screen-dumps"), AppDumpsDir)
	assert.Equal(t, filepath.Join(root, "STATE", "k9plus", "benchmarks"), AppBenchmarksDir)
	assert.Equal(t, filepath.Join(root, "STATE", "k9plus", "k9plus.log"), AppLogFile)
	assert.NoDirExists(t, legacyConfig)
	assert.NoDirExists(t, legacyLogs)
	tmp, err := UserTmpDir()
	require.NoError(t, err)
	assert.Equal(t, "k9plus", filepath.Base(tmp))
}

func TestK9PlusExplicitLocations(t *testing.T) {
	root := isolatedLocations(t)
	configDir := filepath.Join(root, "chosen-config")
	logsDir := filepath.Join(root, "chosen-logs")
	t.Setenv("K9PLUS_CONFIG_DIR", configDir)
	t.Setenv("K9PLUS_LOGS_DIR", logsDir)
	t.Setenv("K9S_CONFIG_DIR", filepath.Join(root, "legacy-config"))
	t.Setenv("K9S_LOGS_DIR", filepath.Join(root, "legacy-logs"))

	require.NoError(t, InitLocs())
	require.NoError(t, InitLogLoc())
	assert.Equal(t, configDir, AppConfigDir)
	assert.Equal(t, filepath.Join(configDir, "clusters"), AppContextsDir)
	assert.Equal(t, filepath.Join(configDir, "screen-dumps"), AppDumpsDir)
	assert.Equal(t, filepath.Join(configDir, "benchmarks"), AppBenchmarksDir)
	assert.Equal(t, filepath.Join(logsDir, "k9plus.log"), AppLogFile)
}

func TestK9PlusDoesNotMigrateLegacyConfig(t *testing.T) {
	root := isolatedLocations(t)
	legacyDir := filepath.Join(root, "CONFIG", "k9s")
	require.NoError(t, os.MkdirAll(legacyDir, 0700))
	legacyFile := filepath.Join(legacyDir, "config.yaml")
	legacy := []byte("k9s:\n  refreshRate: 7\n  readOnly: true\n")
	require.NoError(t, os.WriteFile(legacyFile, legacy, 0600))
	t.Setenv("K9S_CONFIG_DIR", legacyDir)

	require.NoError(t, InitLocs())
	assert.NoFileExists(t, AppConfigFile)
	assert.Equal(t, filepath.Join(root, "CONFIG", "k9plus", "config.yaml"), AppConfigFile)
	// An explicit copy is compatible; startup does not import or rewrite it.
	require.NoError(t, os.WriteFile(AppConfigFile, legacy, 0600))
	cfg := NewConfig(nil)
	require.NoError(t, cfg.Load(AppConfigFile, false))
	assert.InDelta(t, 7, cfg.K9s.RefreshRate, 0.001)
	assert.True(t, cfg.K9s.ReadOnly)
	require.NoError(t, cfg.SaveFile(AppConfigFile))
	updated, err := os.ReadFile(AppConfigFile)
	require.NoError(t, err)
	assert.Contains(t, string(updated), "k9s:\n")
	untouched, err := os.ReadFile(legacyFile)
	require.NoError(t, err)
	assert.Equal(t, legacy, untouched)
	entries, err := os.ReadDir(legacyDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestK9PlusRuntimeOptions(t *testing.T) {
	t.Setenv("K9S_DEFAULT_PF_ADDRESS", "legacy-address")
	t.Setenv("K9S_FEATURE_GATE_NODE_SHELL", "true")
	t.Setenv("K9PLUS_DEFAULT_PF_ADDRESS", "")
	t.Setenv("K9PLUS_FEATURE_GATE_NODE_SHELL", "")
	assert.Equal(t, "localhost", defaultPFAddress())
	ct := data.NewContext()
	ct.Validate(nil, "test", "test")
	assert.False(t, ct.FeatureGates.NodeShell)
	t.Setenv("K9PLUS_DEFAULT_PF_ADDRESS", "127.0.0.2")
	t.Setenv("K9PLUS_FEATURE_GATE_NODE_SHELL", "true")
	assert.Equal(t, "127.0.0.2", defaultPFAddress())
	ct.Validate(nil, "test", "test")
	assert.True(t, ct.FeatureGates.NodeShell)
}

func TestK9PlusPluginDiscoveryIsIsolated(t *testing.T) {
	root := isolatedLocations(t)
	t.Setenv("XDG_DATA_DIRS", filepath.Join(root, "shared-data"))
	xdg.Reload()
	require.NoError(t, InitLocs())
	for _, dir := range []string{filepath.Join(root, "DATA"), filepath.Join(root, "CONFIG"), filepath.Join(root, "shared-data")} {
		for _, app := range []string{"k9s", "k9plus"} {
			pluginDir := filepath.Join(dir, app, "plugins")
			require.NoError(t, os.MkdirAll(pluginDir, 0700))
			plugin := []byte("shortCut: Ctrl-L\ndescription: fixture\ncommand: echo\nscopes: [pods]\n")
			require.NoError(t, os.WriteFile(filepath.Join(pluginDir, app+"-"+filepath.Base(dir)+".yaml"), plugin, 0600))
		}
	}
	p := NewPlugins()
	require.NoError(t, p.Load(filepath.Join(root, "context-plugins.yaml"), true))
	assert.Len(t, p.Plugins, 3)
	assert.Contains(t, p.Plugins, "k9plus-DATA")
	assert.Contains(t, p.Plugins, "k9plus-CONFIG")
	assert.Contains(t, p.Plugins, "k9plus-shared-data")
	assert.NotContains(t, p.Plugins, "k9s-DATA")
	assert.NotContains(t, p.Plugins, "k9s-CONFIG")
	assert.NotContains(t, p.Plugins, "k9s-shared-data")
}
