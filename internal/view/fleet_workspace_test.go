// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/fleet"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

const (
	fleetActorPrimaryContext = "primary"
	fleetActorPeerContext    = "peer"
)

func fleetViewFixture(t *testing.T) *fleetWorkspace {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.DpGVR, Namespace: "apps", Name: "api", UID: "primary-uid"}
	scope := fleet.Scope{Contexts: [2]string{target.Context, fleetActorPeerContext}, GVR: target.GVR.GVR(), Namespace: target.Namespace, Name: target.Name, PrimaryUID: target.UID}
	v := &fleetWorkspace{Details: NewDetails(app, "Fleet facts", target.Path(), contentInspection, true), target: target, scope: scope, destinationRevision: app.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	now := time.Now()
	snapshot := &fleet.Snapshot{Scope: scope, CapturedAt: now}
	for i, name := range scope.Contexts {
		snapshot.Observations[i] = fleet.Observation{Context: name, Namespace: "apps", Name: "api", UID: "uid", ResourceVersion: "7", CapturedAt: now, State: "captured facts", Authority: "https://api.test", Facts: []fleet.Fact{{Category: "declared image (template)", Name: "api", Value: "api:v2"}}}
	}
	v.accept(snapshot, nil)
	return v
}
func TestFleetNativeFramesAndInput(t *testing.T) {
	v := fleetViewFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}, {40, 8}, {80, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		require.Contains(t, frame, "Fleet facts / apps/api")
		require.Contains(t, frame, "r refresh")
		require.Contains(t, frame, "Esc back")
		require.Contains(t, frame, "captured facts")
	}
	calls := 0
	v.loader = func(context.Context, fleet.Scope) (*fleet.Snapshot, error) { calls++; return nil, nil }
	v.cmdBuff.SetActive(true)
	action, _ := v.actions.Get(ui.KeyR)
	event := tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone)
	require.Equal(t, event, action.Action(event))
	require.Zero(t, calls)
}
func TestFleetRetainsIndependentFailedRefreshAndSearchScroll(t *testing.T) {
	v := fleetViewFixture(t)
	prior := v.snapshot
	v.BufferCompleted("api", "")
	v.text.ScrollTo(2, 0)
	v.accept(nil, errors.New("unavailable"))
	require.Same(t, prior, v.snapshot)
	require.Equal(t, "api", v.inspectionQuery)
	fresh := *prior
	fresh.Observations[1] = fleet.Observation{Context: fleetActorPeerContext, State: "denied"}
	v.accept(&fresh, nil)
	require.Contains(t, v.snapshot.Observations[1].State, "Retained; refresh denied")
	require.NotEmpty(t, v.snapshot.Observations[0].Facts)
	require.NotEmpty(t, v.snapshot.Observations[1].Facts)
}
func TestFleetDisconnectedRetentionAndStopCancel(t *testing.T) {
	v := fleetViewFixture(t)
	require.True(t, retainedDisconnectedWorkspace(v))
	ctx, cancel := context.WithCancel(t.Context())
	v.cancel = cancel
	generation := v.generation
	v.Stop()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Greater(t, v.generation, generation)
	require.False(t, v.active)
}

func TestFleetRealDisconnectedPollerRetainsWorkspace(t *testing.T) {
	v := fleetViewFixture(t)
	a := v.app
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	a.Config.SetConnection(conn)
	a.factory = watch.NewFactory(conn)
	a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
	a.Config.K9s.MaxConnRetry = 1
	a.Content.Push(v)
	prior := v.snapshot
	for range 3 {
		require.NoError(t, a.refreshCluster(t.Context()))
		require.Same(t, v, a.Content.Top())
		require.Same(t, prior, v.snapshot)
	}
	require.Greater(t, atomic.LoadInt32(&a.conRetry), a.Config.K9s.MaxConnRetry)
}

func TestFleetStaleDestinationAndGenerationGuards(t *testing.T) {
	v := fleetViewFixture(t)
	v.app.Content.Push(v)
	v.active = true
	v.generation = 3
	require.True(t, v.accepts(3))
	require.False(t, v.accepts(2))
	calls := 0
	v.loader = func(context.Context, fleet.Scope) (*fleet.Snapshot, error) { calls++; return nil, nil }
	v.destinationRevision++
	require.False(t, v.accepts(3))
	prior := v.snapshot
	v.refresh()
	require.Zero(t, calls)
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.failure, "Destination changed")
	v.destinationRevision--
	v.active = false
	require.False(t, v.accepts(3))
}

func TestFleetActorSnapshotsNeverSwitchGlobalContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	raw := api.NewConfig()
	raw.CurrentContext = fleetActorPrimaryContext
	raw.Clusters["first"] = &api.Cluster{Server: "https://primary.test"}
	raw.Clusters["second"] = &api.Cluster{Server: "https://peer.test"}
	raw.Contexts[fleetActorPrimaryContext] = &api.Context{Cluster: "first"}
	raw.Contexts[fleetActorPeerContext] = &api.Context{Cluster: "second"}
	require.NoError(t, clientcmd.WriteToFile(*raw, path))
	flags := genericclioptions.NewConfigFlags(false)
	*flags.KubeConfig = path
	*flags.Context = fleetActorPrimaryContext
	cfg := client.NewConfig(flags)
	scope := &fleet.Scope{Contexts: [2]string{fleetActorPrimaryContext, fleetActorPeerContext}}
	factory := fleetActorFactory(cfg, scope)
	for i, name := range scope.Contexts {
		actor, err := factory(t.Context(), name)
		require.NoError(t, err)
		require.NotNil(t, actor.Reader)
		require.Equal(t, []string{"https://primary.test", "https://peer.test"}[i], actor.Authority)
	}
	require.Equal(t, fleetActorPrimaryContext, *cfg.Flags().Context)
	reloaded, err := clientcmd.LoadFromFile(path)
	require.NoError(t, err)
	require.Equal(t, fleetActorPrimaryContext, reloaded.CurrentContext)
}
