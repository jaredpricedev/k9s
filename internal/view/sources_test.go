// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
)

func declareStudySources(t *testing.T, app *App) {
	t.Helper()
	ct, err := app.Config.CurrentContext()
	require.NoError(t, err)
	ct.Observability = observability.Sources{
		Prometheus:   &observability.Source{Service: "monitoring/prometheus:9090"},
		Alertmanager: &observability.Source{URL: "https://alerts.example.test", TokenEnv: "ALERTMANAGER_TOKEN"},
		Grafana: &observability.GrafanaSource{Source: observability.Source{Service: "monitoring/grafana:3000"},
			PublicURL: "https://grafana.example.test", Datasource: "prom-main",
			Dashboards: map[string]observability.Dashboard{"pod": {UID: "k8s-pod", NamespaceVar: "namespace", PodVar: "pod"}}},
	}
}

func sourcesFixture(t *testing.T, declare bool) *sourcesView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	if declare {
		declareStudySources(t, app)
	}
	v := newSourcesView(app)
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	v.Start()
	return v
}

func TestSourcesShowsDeclarationWithoutContactingAnything(t *testing.T) {
	t.Setenv("ALERTMANAGER_TOKEN", "SECRET-TOKEN-VALUE")
	v := sourcesFixture(t, true)
	probed := 0
	v.probe = func(context.Context, observability.Kind) observability.Status {
		probed++
		return observability.Status{}
	}
	v.suggest = func(context.Context) sourceSuggestions { probed++; return sourceSuggestions{} }
	v.render()
	text := v.text.GetText(true)
	for _, want := range []string{
		"Nothing is contacted until p or s is pressed",
		"PROMETHEUS", "Reached by: service monitoring/prometheus:9090", "Kubernetes API service proxy, your kube credentials",
		"ALERTMANAGER", "Reached by: url https://alerts.example.test", "environment variable ALERTMANAGER_TOKEN (value never shown)",
		"GRAFANA", "Links open at: https://grafana.example.test", "Datasource UID: prom-main", "Dashboards: pod=k8s-pod",
		"prometheus    NOT PROBED (p probes once)", "SUGGESTIONS", "NOT REQUESTED",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "SECRET-TOKEN-VALUE")
	require.Zero(t, probed)
	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "prometheus    NOT PROBED")
		require.Contains(t, frame, "grafana       NOT PROBED")
		require.Contains(t, frame, "monitoring/prometheus:9090")
	}

	// Undeclared context: every kind says so and p refuses to probe.
	empty := sourcesFixture(t, false)
	require.Contains(t, empty.text.GetText(true), "prometheus    not declared\nalertmanager  not declared\ngrafana       not declared\n")
	empty.probe = func(context.Context, observability.Kind) observability.Status {
		probed++
		return observability.Status{}
	}
	empty.probeAll()
	require.Zero(t, probed)
	require.Empty(t, empty.busy)
}

func TestSourcesProbeStatesAreWordedDistinctlyWithTheirTime(t *testing.T) {
	at := time.Date(2026, 1, 2, 14, 2, 11, 0, time.UTC)
	v := sourcesFixture(t, true)
	v.probes = map[observability.Kind]observability.Status{
		observability.KindPrometheus:   {Kind: observability.KindPrometheus, State: observability.StateComplete, ObservedAt: at},
		observability.KindAlertmanager: {Kind: observability.KindAlertmanager, State: observability.StateUnavailable, Reason: "environment variable ALERTMANAGER_TOKEN is not set", ObservedAt: at},
		observability.KindGrafana:      {Kind: observability.KindGrafana, State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: at},
	}
	v.render()
	text := v.text.GetText(true)
	require.Contains(t, text, "prometheus    reachable at 14:02:11Z")
	require.Contains(t, text, "alertmanager  unavailable (environment variable ALERTMANAGER_TOKEN is not set) at 14:02:11Z")
	require.Contains(t, text, "grafana       denied (HTTP 403) at 14:02:11Z")
	require.NotContains(t, text, "NOT PROBED")
	require.Equal(t, "absent (HTTP 404)", probeWord(observability.Status{State: observability.StateAbsent, Reason: "HTTP 404"}))
	require.Equal(t, "timeout (no answer within the deadline)", probeWord(observability.Status{State: observability.StateTimeout, Reason: "no answer within the deadline"}))
}

func TestSourcesSuggestionsAreGuessesAndNeverWritten(t *testing.T) {
	at := time.Date(2026, 1, 2, 14, 2, 11, 0, time.UTC)
	v := sourcesFixture(t, false)
	v.suggestions = &sourceSuggestions{state: observability.StateComplete, at: at, items: observability.Suggest([]observability.ServicePort{
		{Namespace: "monitoring", Name: "prometheus-operated", Ports: []int32{9090}},
		{Namespace: "monitoring", Name: "prometheus", Ports: []int32{9090}},
		{Namespace: "monitoring", Name: "grafana", Ports: []int32{3000}},
	})}
	v.render()
	text := v.text.GetText(true)
	require.Contains(t, text, "A match is a guess")
	require.Contains(t, text, "nothing is written")
	require.Contains(t, text, "  k9s:\n    observability:\n      prometheus:\n        service: monitoring/prometheus-operated:9090\n")
	require.Contains(t, text, "      prometheus: # alternative; keep one\n        service: monitoring/prometheus:9090")
	require.Contains(t, text, "ct-1-1/config.yaml")
	require.Contains(t, text, "      grafana:\n        service: monitoring/grafana:3000")

	v.suggestions = &sourceSuggestions{state: observability.StateDenied, reason: "listing Services in all namespaces is not permitted", at: at}
	v.render()
	require.Contains(t, v.text.GetText(true), "Service list denied (listing Services in all namespaces is not permitted) at 14:02:11Z. No suggestion can be made.")
	v.suggestions = &sourceSuggestions{state: observability.StateComplete, at: at}
	v.render()
	require.Contains(t, v.text.GetText(true), "That does not show that none is installed.")
}

func TestSourcesDiscardsRepliesAfterTheViewClosed(t *testing.T) {
	v := sourcesFixture(t, true)
	release := make(chan struct{})
	done := make(chan struct{})
	v.probe = func(ctx context.Context, kind observability.Kind) observability.Status {
		<-release
		if kind == observability.KindGrafana {
			defer close(done)
		}
		return observability.Status{Kind: kind, State: observability.StateComplete, ObservedAt: time.Now()}
	}
	v.probeAll()
	require.Equal(t, "Probing declared sources once", v.busy)
	require.Contains(t, v.text.GetText(true), "Probing declared sources once; prior results retained.")
	v.Stop()
	close(release)
	<-done
	require.Empty(t, v.probes)
}

func TestHistoryReviewDefaultsToTheDeclaredPrometheusSource(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	client, st := observabilitySource(app, observability.KindPrometheus)
	require.Nil(t, client)
	require.Equal(t, observability.StateNotConfigured, st.State)
	declareStudySources(t, app)
	_, st = observabilitySource(app, observability.KindPrometheus)
	// The mock has no Kubernetes connection: the declaration is found and
	// the reason it cannot be used is stated, without any contact.
	require.Equal(t, "service monitoring/prometheus:9090", st.Source)
	client, st = observabilitySource(app, observability.KindAlertmanager)
	require.NotNil(t, client, st.Reason)
	require.Equal(t, observability.StateDeclared, st.State)
	require.Equal(t, "url https://alerts.example.test", client.Display())
}
