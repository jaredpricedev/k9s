// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/view"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func loadCertManagerPlugins(t *testing.T) config.Plugins {
	t.Helper()
	bb, err := os.ReadFile("../../plugins/cert-manager.yaml")
	require.NoError(t, err)
	var pp config.Plugins
	require.NoError(t, yaml.Unmarshal(bb, &pp))
	return pp
}

func TestCertManagerPluginSafety(t *testing.T) {
	pp := loadCertManagerPlugins(t)
	renew, ok := pp.Plugins["cert-renew"]
	require.True(t, ok)
	assert.True(t, renew.Dangerous, "renewal must be disabled in read-only mode")
	require.NotNil(t, renew.Confirm)
	assert.True(t, renew.ShouldConfirm(), "renewal must require confirmation")
	require.GreaterOrEqual(t, len(renew.Args), 3)
	label, err := (view.Env{
		"CONTEXT": "production", "NAMESPACE": "payments", "NAME": "public-tls",
	}).Substitute(renew.Args[2])
	require.NoError(t, err)
	assert.Contains(t, label, "Renew certificate payments/public-tls")
	assert.Contains(t, label, "context production")

	for _, name := range []string{"cert-status", "secret-inspect"} {
		plugin, ok := pp.Plugins[name]
		require.True(t, ok)
		assert.False(t, plugin.Dangerous, "%s must remain available in read-only mode", name)
		assert.False(t, plugin.ShouldConfirm())
		if plugin.ShortCut == "Shift-S" {
			assert.True(t, plugin.Override, "status must explicitly override SortStatus")
		}
	}
}

func TestCertManagerPluginsTargetSelectedClusterSafely(t *testing.T) {
	requirePluginTestTools(t, "bash")
	pp := loadCertManagerPlugins(t)
	for _, tc := range []struct {
		name string
		verb []string
	}{
		{"cert-status", []string{"status", "certificate"}},
		{"cert-renew", []string{"renew"}},
		{"secret-inspect", []string{"inspect", "secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin := pp.Plugins[tc.name]
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "command.log")
			pagerPath := filepath.Join(t.TempDir(), "pager.log")
			marker := filepath.Join(t.TempDir(), "injected")
			writeExecutable(t, binDir, "cmctl", `
printf '%s\0' "${KUBECONFIG-}" "$@" >"$LOG_PATH"
printf 'diagnostic output\n'
printf 'diagnostic error\n' >&2
`)
			writeExecutable(t, binDir, "less", `cat >"$PAGER_PATH"`)
			env := view.Env{
				"CONTEXT":    "selected context; touch " + marker,
				"KUBECONFIG": "/tmp/kube config $(touch " + marker + ")",
				"NAMESPACE":  "namespace `touch " + marker + "`",
				"NAME":       "certificate'\"; touch " + marker + "\n#",
			}
			err := executePlugin(&plugin, env, append(os.Environ(),
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"LOG_PATH="+logPath, "PAGER_PATH="+pagerPath,
				"KUBECONFIG=/tmp/wrong-kubeconfig",
			))
			require.NoError(t, err)
			log, err := os.ReadFile(logPath)
			require.NoError(t, err)
			want := append([]string{env["KUBECONFIG"]}, tc.verb...)
			want = append(want, "--context", env["CONTEXT"], "-n", env["NAMESPACE"], env["NAME"])
			assert.Equal(t, want, strings.Split(strings.TrimSuffix(string(log), "\x00"), "\x00"))
			assert.NoFileExists(t, marker, "resource values must never become shell code")
			paged, err := os.ReadFile(pagerPath)
			require.NoError(t, err)
			assert.Equal(t, "diagnostic output\ndiagnostic error\n", string(paged))
		})
	}
}

func TestCertManagerPluginsPropagatePipelineFailures(t *testing.T) {
	requirePluginTestTools(t, "bash")
	pp := loadCertManagerPlugins(t)
	for _, name := range []string{"cert-status", "cert-renew", "secret-inspect"} {
		for _, tc := range []struct {
			name, cmctl, pager, exit string
		}{
			{"cmctl", "exit 37", "cat", "exit status 37"},
			{"pager", ":", "cat; exit 41", "exit status 41"},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				plugin := pp.Plugins[name]
				binDir := t.TempDir()
				writeExecutable(t, binDir, "cmctl", tc.cmctl)
				writeExecutable(t, binDir, "less", tc.pager)
				err := executePlugin(&plugin, view.Env{
					"CONTEXT": "selected-context", "KUBECONFIG": "/tmp/kubeconfig",
					"NAMESPACE": "selected-namespace", "NAME": "selected-name",
				}, append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH")))
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.exit)
			})
		}
	}
}
