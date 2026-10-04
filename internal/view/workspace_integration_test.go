// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/stretchr/testify/require"
)

type workspaceDiscoveryOwner struct {
	*Details
	target SelectedResourceTarget
}

type disconnectedWorkspaceConnection struct{ client.Connection }

func (*disconnectedWorkspaceConnection) CheckConnectivity() bool { return false }

func TestDisconnectedWorkspaceRemainsUsableBeyondBackgroundRetryBudget(t *testing.T) {
	for _, mode := range []string{"workspace", "connection"} {
		t.Run(mode, func(t *testing.T) {
			a := NewApp(mock.NewMockConfig(t))
			conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
			a.Config.SetConnection(conn)
			a.factory = watch.NewFactory(conn)
			a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
			a.Config.K9s.MaxConnRetry = 1
			if mode == "workspace" {
				a.Content.Push(newDailyWorkspace(a, workspace.Store{Version: 1}, "scopes", ""))
			} else {
				a.Content.Push(&connectionHealthDetails{Details: NewDetails(a, "Connection", "", contentInspection, true)})
			}
			owner := a.Content.Top()
			for range 3 {
				require.NoError(t, a.refreshCluster(context.Background()))
				require.Same(t, owner, a.Content.Top())
			}
			require.Greater(t, atomic.LoadInt32(&a.conRetry), a.Config.K9s.MaxConnRetry)
		})
	}
}

func (w *workspaceDiscoveryOwner) SelectedResource() SelectedResourceTarget { return w.target }

func TestWorkspaceActionsPreserveCapturedContextAcrossNavigation(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	w := &workspaceDiscoveryOwner{Details: NewDetails(a, "workspace", "", contentInspection, true),
		target: SelectedResourceTarget{Context: "original-context", GVR: client.PodGVR, Namespace: "apps", Name: testWorkspaceAPIName, UID: "original-uid"}}
	target := actionTarget(w, "different-context")
	require.Equal(t, "original-context", target.Context)
	require.Equal(t, w.target.UID, target.UID)
	require.ErrorContains(t, target.Err(), "Context changed")
	for _, action := range actionCatalog(w, a) {
		if action.ID == "resource.troubleshoot" || action.ID == "resource.compare" || action.ID == "resource.pressure" || action.ID == "resource.evidence" {
			require.Contains(t, action.UnavailableReason, "Context changed", action.ID)
		}
	}
}
