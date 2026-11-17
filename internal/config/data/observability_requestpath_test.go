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

// The shape the study fixture writes, plus a declared profile.
const declaredRequestPath = `k9s:
  cluster: study
  observability:
    prometheus:
      service: monitoring/prometheus:9090
    requestPath:
      defaults:
        network: [none]
      services:
        gitlab/gitlab-webservice-default:
          app: [gitlab-webservice, container-cpu]
          ingress: [edge]
          dependencies:
            - service: gitlab/gitlab-postgresql
              profile: postgres-exporter
              match: ['pod=~"gitlab-postgresql-.*"']
      profiles:
        edge:
          extends: ingress-nginx
          scope: {namespace: exported_namespace, service: exported_service}
        queue:
          hop: dependency
          scope: {namespace: namespace}
          signals:
            - {name: lag, kind: gauge, metric: queue_lag_seconds, aggregate: max, unit: seconds}
`

func TestContextDeclaresRequestPathHopsProfilesAndDependencies(t *testing.T) {
	validator := json.NewValidator()
	require.NoError(t, validator.Validate(json.ContextSchema, []byte(declaredRequestPath)))
	for name, text := range map[string]string{
		"a signal takes no query text": "k9s:\n  observability:\n    requestPath:\n      profiles:\n        x:\n          signals:\n            - {name: a, kind: rate, metric: m, query: up}\n",
		"a hop kind is one of five":    "k9s:\n  observability:\n    requestPath:\n      profiles:\n        x: {hop: storage}\n",
		"a dependency names a profile": "k9s:\n  observability:\n    requestPath:\n      services:\n        a/b:\n          dependencies:\n            - {service: a/c}\n",
		"unknown hop key":              "k9s:\n  observability:\n    requestPath:\n      services:\n        a/b: {storage: [x]}\n",
	} {
		require.Error(t, validator.Validate(json.ContextSchema, []byte(text)), name)
	}

	path := filepath.Join(t.TempDir(), "study", "study-dev", MainConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(declaredRequestPath), 0o600))
	cfg, err := NewDir(t.TempDir()).loadConfig(path)
	require.NoError(t, err)
	sources := &cfg.Context.Observability
	require.NotNil(t, sources.RequestPath)
	require.NoError(t, sources.ValidateRequestPath())

	const service = "gitlab/gitlab-webservice-default"
	require.Equal(t, []string{"gitlab-webservice", "container-cpu"}, sources.HopProfiles(observability.HopApp, service))
	require.Equal(t, []string{"edge"}, sources.HopProfiles(observability.HopIngress, service))
	require.Equal(t, []string{"coredns"}, sources.HopProfiles(observability.HopDNS, service), "an absent hop keeps its default")
	require.Empty(t, sources.HopProfiles(observability.HopNetwork, service), "defaults turn the network hop off")
	require.Equal(t, []string{observability.ProfileRED, "container-cpu"}, sources.HopProfiles(observability.HopApp, "apps/checkout-api"))
	require.Equal(t, []observability.Dependency{{Service: "gitlab/gitlab-postgresql", Profile: "postgres-exporter",
		Match: []string{`pod=~"gitlab-postgresql-.*"`}}}, sources.PathDependencies(service))

	// A save that declares no source keeps the hand-written declaration.
	fresh := NewContext()
	fresh.merge(cfg.Context)
	require.NotNil(t, fresh.Observability.RequestPath)
}
