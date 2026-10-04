// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/operator"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

func operatorReportFixture(target *SelectedResourceTarget) *operator.Report {
	generation := int64(5)
	return &operator.Report{Scope: provider.Scope{Context: target.Context, GVR: operator.CertificateGVR, TargetNamespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, CapturedAt: time.Now(),
		Identity:   operator.Identity{Context: target.Context, GVR: operator.CertificateGVR, Namespace: target.Namespace, Name: target.Name, UID: string(target.UID), ResourceVersion: "12", Generation: &generation},
		Conditions: []operator.Condition{{Type: "Ready", Status: "True", Meaning: "controller reports ready", ObservedGeneration: &generation}},
		Coverage:   []operator.Coverage{{Source: operator.CertificateGVR, State: operator.Complete, ReadAt: time.Now()}, {Source: "issuerRef", State: "denied", ReadAt: time.Now()}}}
}
func operatorViewFixture(t *testing.T) *operatorView {
	t.Helper()
	a := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: a.Config.ActiveContextName(), GVR: client.NewGVR(operator.CertificateGVR), Namespace: "apps", Name: "tls", UID: "cert-uid"}
	v := &operatorView{Details: NewDetails(a, operatorTitle, target.Path(), contentInspection, true), target: target, destinationRevision: a.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(operatorReportFixture(&target), nil)
	return v
}
func TestOperatorNativeFramesRetainIdentityMeaningAndControls(t *testing.T) {
	v := operatorViewFixture(t)
	for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "Operator review / apps/tls")
		require.Contains(t, frame, "Controller report != live TLS proof")
		require.Contains(t, frame, "partial relation")
		require.Contains(t, frame, "Overview")
		require.Contains(t, frame, "Ready=True")
		require.Contains(t, frame, "Esc back")
	}
	v.BufferCompleted("generation", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(3)
	v.selectTab(0)
	require.Equal(t, "generation", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("secret-sentinel"))
	require.Same(t, prior, v.snapshot)
	require.NotContains(t, v.text.GetText(true), "secret-sentinel")
	reads := 0
	v.loader = func(context.Context, *SelectedResourceTarget) (*operator.Report, error) { reads++; return nil, nil }
	v.Stop()
	v.Start()
	require.Zero(t, reads)
	v.destinationRevision++
	v.refresh()
	require.Zero(t, reads)
}
func TestOperatorLateCompletionAndFailedRefreshPreserveCapture(t *testing.T) {
	v := operatorViewFixture(t)
	v.app.Content.Push(v)
	v.active = true
	v.generation = 8
	prior := v.snapshot
	replacement := operatorReportFixture(&v.target)
	require.False(t, v.applyRefreshResult(7, replacement, nil))
	require.Same(t, prior, v.snapshot)
	v.destinationRevision++
	require.False(t, v.applyRefreshResult(8, replacement, nil))
	require.Same(t, prior, v.snapshot)
	v.destinationRevision--
	failed := &operator.Report{Coverage: []operator.Coverage{{Source: operator.CertificateGVR, State: operator.Unknown}}}
	require.True(t, v.applyRefreshResult(8, failed, nil))
	require.Same(t, prior, v.snapshot)
	v.Stop()
	require.False(t, v.applyRefreshResult(8, replacement, nil))
	require.Same(t, prior, v.snapshot)
}
func TestOperatorDisconnectRealPollerPreservesExplicitRequest(t *testing.T) {
	v := operatorViewFixture(t)
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
func TestOperatorUnsupportedSelectionRetainsGenericFallback(t *testing.T) {
	v := operatorViewFixture(t)
	for _, gvr := range []string{"example.io/v1/widgets", "cert-manager.io/v1beta1/certificates"} {
		target := v.target
		target.GVR = client.NewGVR(gvr)
		rule := config.JumpRule{TargetGVR: "example.io/v1/children", LabelSelector: "app={{.metadata.name}}"}
		v.app.CustomJumps().Jumps[gvr] = rule
		err := operatorTargetError(&target)
		require.ErrorContains(t, err, "Unsupported operator semantics")
		require.ErrorContains(t, err, "generic YAML/describe")
		require.ErrorContains(t, err, "custom jumps")
		v.app.Content.Push(v)
		v.app.openOperatorReview(target)
		require.Same(t, v, v.app.Content.Top())
		require.Equal(t, rule, v.app.CustomJumps().Jumps[gvr], "generic fallback must preserve custom-jump configuration")
	}
	for _, action := range investigationActions(v, v.app) {
		if action.ID == "command.operator-review" {
			require.True(t, action.Available())
			return
		}
	}
	t.Fatal("semantic operator action missing")
}

type operatorPendingRequest struct {
	ctx      context.Context
	release  chan struct{}
	finished chan struct{}
	once     sync.Once
	report   *operator.Report
}

func (r *operatorPendingRequest) unblock() { r.once.Do(func() { close(r.release) }) }
func takeOperatorRequest(t *testing.T, requests <-chan *operatorPendingRequest) *operatorPendingRequest {
	t.Helper()
	select {
	case request := <-requests:
		t.Cleanup(request.unblock)
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("operator worker did not start")
	}
	return nil
}
func operatorUI(t *testing.T, a *App, check func() bool) {
	t.Helper()
	observed := make(chan bool, 1)
	a.QueueUpdateDraw(func() { observed <- check() })
	select {
	case ok := <-observed:
		require.True(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("operator UI callback blocked")
	}
}
func TestOperatorActualAsyncUISuccessDestinationStopAndBack(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	ctx := context.WithValue(t.Context(), internal.KeyApp, a)
	require.NoError(t, a.Content.Init(ctx))
	owner := NewDetails(a, "Owner", "retained", contentInspection, true).Update("Owner retained evidence")
	require.NoError(t, a.inject(owner, false))
	target := SelectedResourceTarget{Context: a.Config.ActiveContextName(), GVR: client.NewGVR(operator.CertificateGVR), Namespace: "apps", Name: "tls", UID: "cert-uid"}
	requests := make(chan *operatorPendingRequest, 4)
	v := &operatorView{Details: NewDetails(a, operatorTitle, target.Path(), contentInspection, true), target: target, destinationRevision: a.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, _ *SelectedResourceTarget) (*operator.Report, error) {
		request := &operatorPendingRequest{ctx: ctx, release: make(chan struct{}), finished: make(chan struct{}), report: operatorReportFixture(&target)}
		requests <- request
		<-request.release
		close(request.finished)
		return request.report, nil
	}
	require.NoError(t, a.inject(v, false))
	first := takeOperatorRequest(t, requests)
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	painted := make(chan struct{}, 1)
	a.SetScreen(screen).SetRoot(a.Content, true).SetAfterDrawFunc(func(tcell.Screen) {
		select {
		case painted <- struct{}{}:
		default:
		}
	})
	finished := make(chan error, 1)
	a.SetRunning(true)
	go func() { finished <- a.Application.Run() }()
	t.Cleanup(func() {
		first.unblock()
		a.SetRunning(false)
		v.Stop()
		a.Application.Stop()
		select {
		case err := <-finished:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("operator UI did not stop")
		}
	})
	select {
	case <-painted:
	case <-time.After(2 * time.Second):
		t.Fatal("operator native view did not draw")
	}
	first.unblock()
	<-first.finished
	require.Eventually(t, func() bool {
		result := make(chan bool, 1)
		a.QueueUpdateDraw(func() { result <- v.snapshot == first.report && !v.loading })
		return <-result
	}, 2*time.Second, 10*time.Millisecond)
	prior := first.report
	operatorUI(t, a, func() bool { v.refresh(); return v.loading })
	second := takeOperatorRequest(t, requests)
	operatorUI(t, a, func() bool { v.generation++; return true })
	second.unblock()
	<-second.finished
	operatorUI(t, a, func() bool { return v.snapshot == prior })
	operatorUI(t, a, func() bool { v.refresh(); return true })
	third := takeOperatorRequest(t, requests)
	operatorUI(t, a, func() bool { v.destinationRevision++; return true })
	third.unblock()
	<-third.finished
	operatorUI(t, a, func() bool { return v.snapshot == prior })
	operatorUI(t, a, func() bool { v.destinationRevision--; v.refresh(); return true })
	fourth := takeOperatorRequest(t, requests)
	operatorUI(t, a, func() bool { v.Stop(); return true })
	require.ErrorIs(t, fourth.ctx.Err(), context.Canceled)
	fourth.unblock()
	<-fourth.finished
	operatorUI(t, a, func() bool { return v.snapshot == prior && !v.active })
	a.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.Eventually(t, func() bool {
		result := make(chan bool, 1)
		a.QueueUpdateDraw(func() { result <- a.Content.Top() == owner })
		return <-result
	}, 2*time.Second, 10*time.Millisecond)
}
