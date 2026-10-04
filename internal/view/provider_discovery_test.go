// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	providerTestContext   = "captured"
	providerTestPrivateNS = "private"
	providerTestNamespace = "apps"
	providerTestSelected  = "selected"
)

func TestProviderDiscoveryHelpAndSpecsDoNotExecuteOrScan(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	c := NewCommand(app)
	c.providerCommand("providers")
	assert.IsType(t, &Details{}, app.Content.Top())
	assert.Contains(t, app.Content.Top().(*Details).model.Peek(), "Provider checks")
	specs, err := explicitProviderSpecs([]string{"git", "git", "helm", "api:metrics"}, nil, capabilityRequest{Context: providerTestContext, Namespace: providerTestSelected})
	require.NoError(t, err)
	require.Len(t, specs, 3)
	assert.Equal(t, []string{"--version"}, specs[0].VersionArgs)
	assert.Equal(t, []string{providerVersionCommand, "--short"}, specs[1].VersionArgs)
	assert.NotNil(t, specs[2].Probe)
	_, err = explicitProviderSpecs([]string{"login", "kubectl;touch injected"}, nil, capabilityRequest{})
	require.Error(t, err)
	_, err = explicitProviderSpecs(nil, nil, capabilityRequest{})
	assert.NoError(t, err)
}

func TestProviderAPICheckExcludesSecretContentsAndRetainsCapturedScope(t *testing.T) {
	request := capabilityRequest{Task: capabilityTaskResource, Context: providerTestContext, Namespace: providerTestPrivateNS, Target: SelectedResourceTarget{GVR: client.NewGVR(client.SecGVR.String()), Name: "sensitive"}}
	// Nil config proves Secret checks stop before even creating a reader.
	_, err := providerAPIProbe(nil, request)(context.Background(), provider.Scope{Context: request.Context, Namespace: request.Namespace})
	require.ErrorContains(t, err, "Secret content excluded")
	result := provider.Discover(context.Background(), provider.Scope{Context: providerTestContext, Namespace: providerTestPrivateNS, Revision: 9}, provider.Spec{ID: "api:resource", Probe: providerAPIProbe(nil, request)})
	require.Len(t, result, 1)
	assert.Equal(t, provider.Unavailable, result[0].State)
	assert.Equal(t, uint64(9), result[0].Scope.Revision)
	assert.NotContains(t, renderProviderChecks(result[0].Scope, result, false), "sensitive")
}

func TestProviderChecksRenderDistinctStatesAndSafeUnicode(t *testing.T) {
	scope := provider.Scope{Context: "long-context", Namespace: providerTestNamespace}
	checks := []provider.Capability{}
	for _, state := range []provider.CapabilityState{provider.Ready, provider.Absent, provider.Denied, provider.Incompatible, provider.Unavailable} {
		checks = append(checks, provider.Capability{ID: string(state), Scope: scope, State: state, Version: "v2.3.4", ObservedAt: time.Unix(0, 0), Limits: provider.Limits{Timeout: 3 * time.Second}})
	}
	text := renderProviderChecks(scope, checks, true)
	for _, check := range checks {
		assert.Contains(t, text, string(check.State))
	}
	assert.Contains(t, text, "Retained observation")
	assert.Contains(t, text, "long-context")
	assert.Contains(t, text, "Namespace: apps")
	assert.True(t, utf8.ValidString(safeProviderText(strings.Repeat("界", 600))))
	assert.NotContains(t, safeProviderText("version\x1b[31m\n"), "\x1b")
}

func TestProviderDetailsGuardsGenerationAndDestinationRoundTrips(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	scope := provider.Scope{Context: app.Config.ActiveContextName(), Namespace: app.Config.CachedNamespace(), Revision: app.Config.DestinationRevision()}
	d := &providerDetails{Details: NewDetails(app, "Provider checks", "", contentInspection, true), scope: scope, started: true, generation: 3}
	app.Content.Push(d)
	assert.True(t, d.current(3))
	assert.False(t, d.current(2))
	d.scope.Revision++
	assert.False(t, d.current(3), "same context/namespace with changed revision is a different request destination")
	d.scope = scope
	d.Stop()
	assert.False(t, d.current(3))
}

func TestProviderDiscoverySimulationCancelsLateReplyAndReturnsToOwner(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	ctx := context.WithValue(context.Background(), internal.KeyApp, app)
	require.NoError(t, app.Content.Init(ctx))
	owner := NewDetails(app, "Owner", "retained", contentInspection, true).Update("Owner retained evidence")
	require.NoError(t, app.inject(owner, false))
	started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	scope := provider.Scope{Context: app.Config.ActiveContextName(), Namespace: app.Config.CachedNamespace(), Revision: app.Config.DestinationRevision()}
	d := &providerDetails{Details: NewDetails(app, "Provider checks", "", contentInspection, true), scope: scope,
		discover: func(ctx context.Context, captured provider.Scope, _ ...provider.Spec) []provider.Capability {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release // Deliberately late reply must not update the old page.
			return []provider.Capability{{ID: "late", Scope: captured, State: provider.Ready}}
		}}
	require.NoError(t, app.inject(d, false))
	<-started
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(60, 24)
	painted := make(chan struct{}, 1)
	app.SetScreen(screen).SetRoot(app.Content, true).SetAfterDrawFunc(func(tcell.Screen) {
		select {
		case painted <- struct{}{}:
		default:
		}
	})
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	t.Cleanup(func() { unblock(); d.Stop(); app.Stop(); <-finished })
	select {
	case <-painted:
	case <-time.After(time.Second):
		t.Fatal("provider view failed to draw at60columns")
	}
	app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Esc did not cancel explicit provider work")
	}
	unblock()
	observed := make(chan bool, 1)
	app.QueueUpdateDraw(func() { observed <- app.Content.Top() == owner && len(d.checks) == 0 })
	select {
	case ok := <-observed:
		assert.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("UI remained blocked after provider cancellation")
	}
}

func TestProviderDiscoveryFailedProbeDoesNotExposeStderr(t *testing.T) {
	check := provider.Capability{ID: "helm", State: provider.Unavailable, Detail: "check failed or timed out", Err: errors.New("SECRET raw stderr")}
	assert.NotContains(t, renderProviderChecks(provider.Scope{}, []provider.Capability{check}, false), "SECRET")
}
