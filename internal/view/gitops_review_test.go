// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

const gitopsNamespaceCase = "namespace changed"

func gitopsViewFixture(t *testing.T) *gitopsView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	app.Config.K9s.ReadOnly = true
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.NewGVR("argoproj.io/v1alpha1/applications"), Namespace: "control", Name: "delivery", UID: "app-uid"}
	request := gitops.Request{Target: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, ArgoNamespace: "control"}
	v := &gitopsView{Details: NewDetails(app, gitopsTitle, target.Path(), contentInspection, true), target: target, request: request,
		destinationRevision: app.Config.DestinationRevision(), workspaceNamespace: app.Config.ActiveNamespace()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	snapshot := &gitops.Snapshot{Request: request, CapturedAt: time.Now(), Nodes: []gitops.Node{
		{Identity: request.Target, Kind: "Application", State: "reported outdated", ReportedSync: "OutOfSync", ReportedHealth: "Healthy",
			Reason: "Reported sync and health remain separate from workload readiness", Versions: []gitops.Version{{Kind: "reconciled source revision", Value: "main@sha1:captured", Source: "status.sync.revision"}}},
	}, Links: []gitops.Link{{From: 0, To: -1, Relation: "declared source reference", State: gitops.Denied, Reason: "Requested reference read denied"}}}
	v.acceptSnapshot(snapshot, gitopsTexts(snapshot), nil)
	return v
}

func gitopsTexts(snapshot *gitops.Snapshot) (texts [4]string) {
	for index := range texts {
		texts[index] = snapshot.Render(index)
	}
	return texts
}

func TestGitOpsNativeFramesRetainIdentityStateAndNavigation(t *testing.T) {
	v := gitopsViewFixture(t)
	for _, size := range []struct{ width, height int }{{120, 34}, {80, 24}, {60, 24}, {40, 16}, {40, 12}, {39, 12}, {40, 8}, {60, 24}} {
		frame := drawnText(t, v, size.width, size.height)
		if size.width < ui.MinTaskWidth || size.height < ui.MinTaskHeight {
			require.Contains(t, frame, "40x12")
			continue
		}
		for _, value := range []string{"control/delivery", "app-uid", "partial evidence", "Overview", "r refresh", "Esc back", "REPORTED OUTDATED"} {
			require.Contains(t, frame, value, "frame %dx%d:\n%s", size.width, size.height, frame)
		}
	}
	for index, heading := range []string{"GITOPS OBSERVATION", "OWNERSHIP / RECONCILIATION", "SOURCE / VERSION EVIDENCE", "RETAINED GITOPS EVIDENCE"} {
		action, ok := v.actions.Get([]tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4}[index])
		require.True(t, ok)
		require.Nil(t, action.Action(tcell.NewEventKey(tcell.KeyRune, rune('1'+index), tcell.ModNone)))
		require.Equal(t, index, v.activeTab)
		require.Contains(t, drawnText(t, v, 40, 16), heading)
	}
}

func TestGitOpsRetainsSearchScrollAndSafeFailureWithoutRefetch(t *testing.T) {
	v := gitopsViewFixture(t)
	reads := 0
	v.loader = func(context.Context, *gitops.Request) (*gitops.Snapshot, error) {
		reads++
		return nil, errors.New("unexpected fetch")
	}
	v.BufferCompleted("Reported", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(2)
	v.selectTab(0)
	require.Equal(t, "Reported", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, [4]string{}, errors.New("permission denied"))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.identityBar.GetText(true), "failed refresh")
	require.Contains(t, strings.Join(v.model.Peek(), "\n"), "Previous captured evidence retained")
	v.StylesChanged(v.app.Styles)
	v.Stop()
	v.Start()
	require.Same(t, prior, v.snapshot)
	require.Zero(t, reads)
	v.workspaceNamespace = "different-workspace"
	v.refresh()
	require.Zero(t, reads)
	require.Contains(t, v.identityBar.GetText(true), "destination changed")
}

func TestGitOpsRejectsDifferentSnapshotUIDAndExplicitNamespace(t *testing.T) {
	v := gitopsViewFixture(t)
	prior := v.snapshot
	for _, changed := range []string{"UID", gitopsNamespaceCase} {
		foreign := *prior
		if changed == "UID" {
			foreign.Request.Target.UID = nativeReplacement
		} else {
			foreign.Request.ArgoNamespace = "other-controller"
		}
		v.acceptSnapshot(&foreign, gitopsTexts(&foreign), nil)
		require.Same(t, prior, v.snapshot)
		require.Contains(t, v.refreshFailure, "captured selection")
	}
}

func TestGitOpsQueuedRefreshRejectsExitReplacementAndDestinationChanges(t *testing.T) {
	for _, scenario := range []string{"exit", "generation", gitopsNamespaceCase, "covered"} {
		t.Run(scenario, func(t *testing.T) {
			v := gitopsViewFixture(t)
			v.active = true
			v.app.Content.Push(v)
			v.app.SetRunning(true)
			t.Cleanup(func() { v.app.SetRunning(false) })
			prior := v.snapshot
			queued := make(chan func(), 1)
			v.enqueue = func(callback func()) { queued <- callback }
			v.loader = func(_ context.Context, request *gitops.Request) (*gitops.Snapshot, error) {
				captured := *prior
				captured.Request = *request
				captured.CapturedAt = prior.CapturedAt.Add(time.Minute)
				return &captured, nil
			}
			v.refresh()
			var callback func()
			select {
			case callback = <-queued:
			case <-time.After(time.Second):
				t.Fatal("native review did not queue its captured result")
			}
			switch scenario {
			case "exit":
				v.Stop()
			case "generation":
				v.generation++
			case gitopsNamespaceCase:
				v.workspaceNamespace = "new-workspace"
			case "covered":
				v.app.Content.Push(NewDetails(v.app, "other", "", contentTXT, false))
			}
			callback()
			require.Same(t, prior, v.snapshot, "late result replaced captured evidence after %s", scenario)
		})
	}
}

func TestGitOpsStopAndDestinationDrawCancelWorkers(t *testing.T) {
	v := gitopsViewFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	v.cancel, v.loading = cancel, true
	v.generation = 7
	v.workspaceNamespace = "changed"
	frame := drawnText(t, v, 80, 24)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, uint64(8), v.generation)
	require.False(t, v.loading)
	require.Contains(t, frame, "destination changed")
	ctx, cancel = context.WithCancel(t.Context())
	v.cancel, v.loading = cancel, true
	v.Stop()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, uint64(9), v.generation)
}

func TestGitOpsDisconnectedWorkspaceRetainsActiveWorkerAndEvidence(t *testing.T) {
	v := gitopsViewFixture(t)
	app := v.app
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	app.Config.SetConnection(conn)
	app.factory = watch.NewFactory(conn)
	app.clusterModel = model.NewClusterInfo(app.factory, "test", app.Config.K9s)
	app.Config.K9s.MaxConnRetry = 1
	app.Content.Push(v)
	v.active, v.loading, v.generation = true, true, 9
	ctx, cancel := context.WithCancel(t.Context())
	v.cancel = cancel
	v.selectTab(2)
	prior := v.snapshot
	for range 3 {
		require.NoError(t, app.refreshCluster(t.Context()))
	}
	app.connectivityComponent(v, true)
	require.Greater(t, atomic.LoadInt32(&app.conRetry), app.Config.K9s.MaxConnRetry)
	require.True(t, v.active)
	require.True(t, v.loading)
	require.Equal(t, uint64(9), v.generation)
	require.NoError(t, ctx.Err())
	require.Same(t, prior, v.snapshot)
	require.Equal(t, 2, v.activeTab)
	require.Contains(t, drawnText(t, v, 80, 24), "SOURCE / VERSION EVIDENCE")
}

func TestGitOpsChromeSanitizesControlSequencesBidiAndInjectedRows(t *testing.T) {
	v := gitopsViewFixture(t)
	original := v.target.Context
	v.target.Context += "\x1b[31m\u202e\nINJECTED\tROW"
	v.render()
	header := v.identityBar.GetText(true)
	require.Len(t, strings.Split(header, "\n"), 3)
	for _, control := range []string{"\x1b", "\u202e", "\t"} {
		require.NotContains(t, header, control)
	}
	v.target.Context = original
	v.refreshFailure = "denied\x1b[31m\u2066\nINJECTED\tROW"
	v.render()
	header = v.identityBar.GetText(true)
	require.Len(t, strings.Split(header, "\n"), 3)
	for _, control := range []string{"\x1b", "\u2066", "\t"} {
		require.NotContains(t, header, control)
		if control != "\t" {
			require.NotContains(t, v.text.GetText(true), control)
		}
	}
	require.Contains(t, drawnText(t, v, 40, 16), "Esc back")
}
