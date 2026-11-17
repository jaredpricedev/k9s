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
)

const declaredRED = `k9s:
  cluster: study
  observability:
    prometheus:
      service: monitoring/prometheus:9090
    red:
      requests: nginx_ingress_controller_requests
      codeLabel: status
      errorCodes: 5..|429
      workloadLabel: service
`

func TestContextMapsREDMetricNamesAndKeepsDefaultsForTheRest(t *testing.T) {
	require.NoError(t, json.NewValidator().Validate(json.ContextSchema, []byte(declaredRED)))
	require.Error(t, json.NewValidator().Validate(json.ContextSchema,
		[]byte("k9s:\n  observability:\n    red:\n      query: up\n")), "the mapping names metrics and labels; it takes no query text")

	path := filepath.Join(t.TempDir(), "study", "study-dev", MainConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(declaredRED), 0o600))
	cfg, err := NewDir(t.TempDir()).loadConfig(path)
	require.NoError(t, err)
	red := cfg.Context.Observability.RED
	require.NotNil(t, red)
	require.NoError(t, red.Validate())
	require.Equal(t, observability.RED{
		Requests: "nginx_ingress_controller_requests", CodeLabel: "status", ErrorCodes: "5..|429", WorkloadLabel: "service",
		DurationBuckets: observability.DefaultDurationBuckets, NamespaceLabel: observability.DefaultNamespaceLabel, PodLabel: observability.DefaultPodLabel,
	}, red.WithDefaults())
	require.Equal(t, observability.DefaultRequests, (*observability.RED)(nil).WithDefaults().Requests)
}
