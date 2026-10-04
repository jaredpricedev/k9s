package view

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/backup"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/watch"
	"github.com/stretchr/testify/require"
)

func backupViewFixture(t *testing.T) *backupView {
	t.Helper()
	a := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: a.Config.ActiveContextName(), Namespace: "apps", Name: "velero"}
	v := &backupView{Details: NewDetails(a, backupTitle, "apps", contentInspection, true), target: target, destinationRevision: a.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(&backup.Snapshot{Scope: backup.Scope{Context: target.Context, Namespace: "apps", ControllerNamespace: "velero"}, CapturedAt: time.Now(), Coverage: []backup.Coverage{{Source: "Backup", State: "denied"}}}, nil)
	return v
}
func TestBackupNativeFramesAndRetainedNavigation(t *testing.T) {
	v := backupViewFixture(t)
	for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "Backup success != recoverability")
		require.Contains(t, frame, "Overview")
		require.Contains(t, frame, "partial")
		require.Contains(t, frame, "Esc back")
	}
	v.BufferCompleted("namespace", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(4)
	v.selectTab(0)
	require.Equal(t, "namespace", v.inspectionQuery)
	prior := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("sentinel-secret"))
	require.Same(t, prior, v.snapshot)
	require.NotContains(t, v.text.GetText(true), "sentinel-secret")
	reads := 0
	v.loader = func(context.Context, *SelectedResourceTarget) (*backup.Snapshot, error) { reads++; return nil, nil }
	v.Stop()
	v.Start()
	require.Zero(t, reads)
	v.destinationRevision++
	v.refresh()
	require.Zero(t, reads)
}
func TestBackupDisconnectKeepsRealPollerFromStoppingCollection(t *testing.T) {
	v := backupViewFixture(t)
	a := v.app
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	a.Config.SetConnection(conn)
	a.factory = watch.NewFactory(conn)
	a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
	a.Config.K9s.MaxConnRetry = 1
	a.Content.Push(v)
	v.Start()
	pending, cancel := context.WithCancel(t.Context())
	defer cancel()
	v.cancel, v.loading, v.generation = cancel, true, 7
	for range 3 {
		require.NoError(t, a.refreshCluster(t.Context()))
	}
	require.Greater(t, atomic.LoadInt32(&a.conRetry), a.Config.K9s.MaxConnRetry)
	require.Same(t, v, a.Content.Top())
	require.True(t, v.active)
	require.NoError(t, pending.Err())
	require.EqualValues(t, 7, v.generation)
}

func TestBackupLateCompletionAndFailedRefreshPreservePriorCapture(t *testing.T) {
	v := backupViewFixture(t)
	v.app.Content.Push(v)
	v.active = true
	v.generation = 8
	prior := v.snapshot
	replacement := &backup.Snapshot{Coverage: []backup.Coverage{{Source: "Backup", State: "empty"}}}
	require.False(t, v.applyRefreshResult(7, replacement, nil))
	require.Same(t, prior, v.snapshot)
	v.destinationRevision++
	require.False(t, v.applyRefreshResult(8, replacement, nil))
	require.Same(t, prior, v.snapshot)
	v.destinationRevision--
	failed := &backup.Snapshot{Coverage: []backup.Coverage{{Source: "Backup", State: "unknown / unavailable"}}}
	require.True(t, v.applyRefreshResult(8, failed, nil))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.refreshFailure, "denied or unavailable")
	v.Stop()
	require.False(t, v.applyRefreshResult(8, replacement, nil))
	require.Same(t, prior, v.snapshot)
}

func TestBackupActionOpensExplicitNamespaceCommandPrompt(t *testing.T) {
	v := backupViewFixture(t)
	found := false
	for _, action := range investigationActions(v, v.app) {
		if action.ID == "command.backup-review" {
			found = true
			action.Handler(nil)
		}
	}
	require.True(t, found)
	require.True(t, v.app.CmdBuff().IsActive())
	require.Equal(t, "backup-review ", v.app.CmdBuff().GetText())
	require.True(t, v.app.Prompt().InCmdMode())
}
