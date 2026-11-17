// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package data

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/derailed/k9s/internal/config/json"
	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/tools/clientcmd/api"
)

const declaredSources = `k9s:
  cluster: study
  observability:
    prometheus:
      service: monitoring/prometheus:9090
    alertmanager:
      url: https://alerts.example.test
      tokenEnv: ALERTMANAGER_TOKEN
    grafana:
      service: monitoring/grafana:3000
      publicURL: https://grafana.example.test
      datasource: prom-main
      dashboards:
        pod:
          uid: k8s-pod
          namespaceVar: namespace
          podVar: pod
        node:
          uid: k8s-node
          nodeVar: instance
`

func TestContextDeclaresObservabilitySourcesAndSavesKeepThem(t *testing.T) {
	require.NoError(t, json.NewValidator().Validate(json.ContextSchema, []byte(declaredSources)))
	require.Error(t, json.NewValidator().Validate(json.ContextSchema,
		[]byte("k9s:\n  observability:\n    prometheus:\n      token: SECRET\n")), "a literal token key is not part of the schema")
	require.Error(t, json.NewValidator().Validate(json.ContextSchema,
		[]byte("k9s:\n  observability:\n    grafana:\n      dashboards:\n        cluster:\n          uid: x\n")))

	path := filepath.Join(t.TempDir(), "study", "study-dev", MainConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(declaredSources), 0o600))
	dir := NewDir(t.TempDir())
	cfg, err := dir.loadConfig(path)
	require.NoError(t, err)
	sources := cfg.Context.Observability
	require.True(t, sources.Declared())
	for _, kind := range observability.Kinds {
		require.NoError(t, sources.Validate(kind), kind)
	}
	require.Equal(t, "ALERTMANAGER_TOKEN", sources.Alertmanager.TokenEnv)
	require.Equal(t, "instance", sources.Grafana.Dashboards[observability.DashboardNode].NodeVar)
	require.Equal(t, "monitoring/grafana:3000", sources.Grafana.Service)

	// A save from a context that was built fresh (not loaded) keeps the
	// hand-written declaration that is on disk.
	require.NoError(t, dir.Save(path, NewConfig(&api.Context{Cluster: "study", Namespace: "apps"})))
	again, err := dir.loadConfig(path)
	require.NoError(t, err)
	require.Equal(t, sources, again.Context.Observability)

	// A context without a declaration writes no observability key at all.
	plain, err := yaml.Marshal(NewConfig(&api.Context{Cluster: "study"}))
	require.NoError(t, err)
	require.NotContains(t, string(plain), "observability")
}
