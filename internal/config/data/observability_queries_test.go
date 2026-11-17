// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package data

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/derailed/k9s/internal/config/json"
	"github.com/stretchr/testify/require"
)

const savedQueries = `k9s:
  cluster: study
  observability:
    prometheus:
      service: monitoring/prometheus:9090
    queries:
      slow-sql:
        title: Longest transaction
        expr: 'max by (datname) (pg_stat_activity_max_tx_duration{namespace="$namespace"})'
        unit: seconds
        legend: '{{datname}}'
      ready: {expr: 'sum(kube_pod_status_ready{namespace="$namespace", condition="true"})'}
`

func TestContextSavesNamedQueriesForTheQueryBar(t *testing.T) {
	validator := json.NewValidator()
	require.NoError(t, validator.Validate(json.ContextSchema, []byte(savedQueries)))
	for name, text := range map[string]string{
		"a query needs an expression": "k9s:\n  observability:\n    queries:\n      a: {title: x}\n",
		"a unit is one of six":        "k9s:\n  observability:\n    queries:\n      a: {expr: up, unit: furlongs}\n",
		"unknown key":                 "k9s:\n  observability:\n    queries:\n      a: {expr: up, datasource: other}\n",
		"an expression is not empty":  "k9s:\n  observability:\n    queries:\n      a: {expr: ''}\n",
	} {
		require.Error(t, validator.Validate(json.ContextSchema, []byte(text)), name)
	}

	path := filepath.Join(t.TempDir(), "study", "study-dev", MainConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(savedQueries), 0o600))
	cfg, err := NewDir(t.TempDir()).loadConfig(path)
	require.NoError(t, err)
	sources := &cfg.Context.Observability
	require.NoError(t, sources.ValidateQueries())
	require.Equal(t, []string{"ready", "slow-sql"}, sources.QueryNames())
	require.Equal(t, "seconds", sources.Queries["slow-sql"].Unit)
	require.Equal(t, `max by (datname) (pg_stat_activity_max_tx_duration{namespace="$namespace"})`, sources.Queries["slow-sql"].Expr)

	// A save keeps the hand-written queries.
	fresh := NewContext()
	fresh.merge(cfg.Context)
	require.Len(t, fresh.Observability.Queries, 2)
}
