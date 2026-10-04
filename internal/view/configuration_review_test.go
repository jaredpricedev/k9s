// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/configreview"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func configurationViewFixture(t *testing.T) *configurationView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR,
		Namespace: "apps", Name: "api", UID: "pod-uid"}
	v := &configurationView{Details: NewDetails(app, "Configuration review", target.Path(), contentInspection, true),
		target: target, destinationRevision: app.Config.DestinationRevision(), width: 76, bodyRows: 18}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	consumer := configreview.Identity{Kind: "Pod", ResourceIdentity: inspect.ResourceIdentity{
		Context: target.Context, GVR: "v1/pods", Namespace: "apps", Name: "api", UID: "pod-uid"}}
	v.acceptSnapshot(&configreview.Snapshot{Identity: consumer, CapturedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		References: []configreview.Reference{{Consumer: consumer, Kind: configreview.ConfigMapKind,
			Name: "settings", Key: "url", Variable: "URL", Container: "api", Use: "env"}},
		Objects: []configreview.Object{{Identity: configreview.Identity{Kind: configreview.ConfigMapKind,
			ResourceIdentity: inspect.ResourceIdentity{Namespace: "apps", Name: "settings", UID: "cm-uid"}},
			State: configreview.Present, KeysKnown: true, KeyNames: []string{"url"}, ResourceVersion: "1"}},
		Coverage: []configreview.Coverage{{Source: "Consumers", State: inspect.ObservationDenied, Detail: "Read denied"}},
	}, nil)
	return v
}

func TestConfigurationNativeFramesRetainScopeTabsActionsAndUncertainty(t *testing.T) {
	v := configurationViewFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 18}, {40, 12}, {40, 8}, {80, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("configuration-%dx%d", size[0], size[1]), frame)
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		require.Contains(t, frame, "apps/api")
		require.Contains(t, frame, "Overview")
		require.Contains(t, frame, "READ ONLY")
		require.Contains(t, frame, "r refresh")
		require.Contains(t, frame, "Esc back")
	}
	v.selectTab(4)
	frame := drawnText(t, v, 40, 16)
	require.Contains(t, frame, "Evidence")
	require.Contains(t, frame, "CAPTURED SOURCE")
	require.Equal(t, "pod-uid", string(v.SelectedResource().UID))
}

func TestConfigurationRetainsSearchScrollAndCaptureAfterFailedRefresh(t *testing.T) {
	v := configurationViewFixture(t)
	v.selectTab(1)
	v.BufferCompleted("declared key", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(4)
	v.selectTab(1)
	require.Equal(t, "declared key", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, errors.New("source replaced"))
	require.Same(t, prior, v.snapshot)
	require.Nil(t, v.previous)
	require.Contains(t, v.identityBar.GetText(true), "retained")
	require.Contains(t, v.identityBar.GetText(true), "12:00:00Z")
	reads := 0
	v.loader = func(context.Context) (*configreview.Snapshot, error) {
		reads++
		return nil, errors.New("unexpected read")
	}
	v.Stop()
	v.Start()
	v.StylesChanged(v.app.Styles)
	require.Zero(t, reads, "Back and style changes must reuse retained evidence")
	v.destinationRevision++
	v.refresh()
	require.Zero(t, reads, "destination changes must block captured reads")
	require.Contains(t, v.refreshFailure, "Destination changed")
}

func TestConfigurationScopeRejectsSyntheticClusterWideAndUnknownUIDTargets(t *testing.T) {
	valid := SelectedResourceTarget{Context: "demo", GVR: client.PodGVR, Namespace: "apps", Name: "api", UID: "uid"}
	_, err := configurationScope(valid)
	require.NoError(t, err)
	for _, target := range []SelectedResourceTarget{
		{Context: "demo", GVR: client.NodeGVR, Name: "worker", UID: "uid"},
		{Context: "demo", GVR: client.PodGVR, Namespace: "", Name: "api", UID: "uid"},
		{Context: "demo", GVR: client.PodGVR, Namespace: "apps", Name: "api"},
		{UnavailableReason: "synthetic view"},
	} {
		_, err := configurationScope(target)
		require.Error(t, err)
	}
}

func TestConfigurationDisconnectRetainsCaptureAndNativeControlsBeyondRetryBudget(t *testing.T) {
	v := configurationViewFixture(t)
	a := v.app
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	a.Config.SetConnection(conn)
	a.factory = watch.NewFactory(conn)
	a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
	a.Config.K9s.MaxConnRetry = 1
	a.Content.Push(v)
	v.Start()
	v.selectTab(1)
	v.BufferCompleted("declared key", "")
	v.text.ScrollTo(2, 1)
	snapshot := v.snapshot
	pending, cancel := context.WithCancel(t.Context())
	v.cancel, v.loading, v.generation = cancel, true, 7
	defer cancel()
	for range 3 {
		require.NoError(t, a.refreshCluster(t.Context()))
	}
	require.Greater(t, atomic.LoadInt32(&a.conRetry), a.Config.K9s.MaxConnRetry)
	require.Same(t, v, a.Content.Top())
	require.Same(t, snapshot, v.snapshot)
	require.True(t, v.active)
	require.True(t, v.loading)
	require.EqualValues(t, 7, v.generation, "connectivity polling must not call view Stop/Start")
	require.NoError(t, pending.Err(), "the background poller must not cancel explicit collection")
	require.Equal(t, "declared key", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	a.connectivityComponent(v, true)
	require.EqualValues(t, 7, v.generation)
	v.InputHandler()(tcell.NewEventKey(tcell.KeyRune, '5', tcell.ModNone), func(p tview.Primitive) { a.SetFocus(p) })
	require.Equal(t, 4, v.activeTab)
	frame := drawnText(t, v, 80, 24)
	require.Contains(t, frame, "CAPTURED SOURCE")
	require.Contains(t, frame, "pod-uid")
	require.Contains(t, frame, "12:00:00Z")
	require.Contains(t, frame, "Esc back")
	frame = drawnText(t, v, 40, 16)
	require.Contains(t, frame, "Evidence")
	require.Contains(t, frame, "CAPTURED SOURCE")
}
