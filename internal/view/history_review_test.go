// SPDX-License-Identifier: Apache-2.0
package view

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func historyFixture(t *testing.T) *historyView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	v := &historyView{Details: NewDetails(app, "History review", "apps/api", contentInspection, true), request: observability.Request{Scope: provider.Scope{Context: app.Config.ActiveContextName(), Namespace: app.Config.CachedNamespace(), TargetNamespace: "apps", GVR: "v1/pods", Name: "api", UID: "pod-uid", Revision: app.Config.DestinationRevision()}, URL: "https://metrics.example.com", Start: time.Unix(1000, 0).UTC(), End: time.Unix(1060, 0).UTC(), Step: 15 * time.Second}}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.snapshot = &observability.Snapshot{Request: v.request, ObservedAt: time.Unix(1061, 0).UTC(), Evidence: []observability.Evidence{{State: "partial", Unit: "cumulative CPU seconds", Query: "container_cpu_usage_seconds_total{namespace=\"apps\",pod=\"api\"}"}}}
	v.render()
	return v
}
func TestHistoryNativeFramesAndDisconnectedRefreshCluster(t *testing.T) {
	v := historyFixture(t)
	a := v.app
	a.Content.Push(v)
	v.Start()
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	a.Config.SetConnection(conn)
	a.factory = watch.NewFactory(conn)
	a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
	a.Config.K9s.MaxConnRetry = 1
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v.cancel = cancel
	v.loading = true
	v.generation = 7
	for range 3 {
		require.NoError(t, a.refreshCluster(t.Context()))
	}
	require.Greater(t, atomic.LoadInt32(&a.conRetry), a.Config.K9s.MaxConnRetry)
	require.True(t, v.active)
	require.EqualValues(t, 7, v.generation)
	require.NoError(t, ctx.Err())
	for _, size := range [][2]int{{80, 24}, {40, 16}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "apps/api")
		require.Contains(t, frame, "pod-uid")
		require.Contains(t, frame, "unverified")
	}
	prior := v.snapshot
	v.accept(nil, errors.New("SECRET"))
	require.Same(t, prior, v.snapshot)
	require.NotContains(t, v.text.GetText(true), "SECRET")
	v.request.Scope.Revision++
	reads := 0
	v.loader = func(context.Context, observability.Request) (*observability.Snapshot, error) {
		reads++
		return nil, nil
	}
	v.refresh()
	require.Zero(t, reads)
}
func TestHistoryActualUIDispatcherAppliesSuccessAndRetainsFailure(t *testing.T) {
	v := historyFixture(t)
	a := v.app
	ctx := context.WithValue(t.Context(), internal.KeyApp, a)
	require.NoError(t, a.Content.Init(ctx))
	require.NoError(t, a.inject(v, false))
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	painted := make(chan struct{}, 1)
	a.SetRunning(true)
	a.SetScreen(screen).SetRoot(a.Content, true).SetAfterDrawFunc(func(tcell.Screen) {
		select {
		case painted <- struct{}{}:
		default:
		}
	})
	done := make(chan error, 1)
	go func() { done <- a.Application.Run() }()
	t.Cleanup(func() {
		// Lifecycle fields share the UI dispatcher with late provider callbacks.
		a.Application.QueueUpdateDraw(v.Stop)
		a.Stop()
		<-done
	})
	select {
	case <-painted:
	case <-time.After(time.Second):
		t.Fatal("no initial draw")
	}
	result := &observability.Snapshot{Request: v.request, ObservedAt: time.Now(), Evidence: []observability.Evidence{{State: "partial"}}}
	loaded := make(chan struct{}, 1)
	v.loader = func(context.Context, observability.Request) (*observability.Snapshot, error) {
		loaded <- struct{}{}
		return result, nil
	}
	a.Application.QueueUpdateDraw(func() { v.refresh() })
	<-loaded
	require.Eventually(t, func() bool {
		ok := make(chan bool, 1)
		a.Application.QueueUpdateDraw(func() { ok <- v.snapshot == result && !v.loading })
		return <-ok
	}, time.Second, 10*time.Millisecond)
	a.Application.QueueUpdateDraw(func() {
		v.loader = func(context.Context, observability.Request) (*observability.Snapshot, error) {
			loaded <- struct{}{}
			return nil, errors.New("SECRET provider payload")
		}
		v.refresh()
	})
	<-loaded
	require.Eventually(t, func() bool {
		ok := make(chan bool, 1)
		a.Application.QueueUpdateDraw(func() { ok <- v.snapshot == result && !v.loading && v.failure != "" })
		return <-ok
	}, time.Second, 10*time.Millisecond)
	a.Application.QueueUpdateDraw(func() { require.NotContains(t, v.text.GetText(true), "SECRET") })
	// Generation changes reject successful replies even when a provider ignores cancellation.
	release, entered := make(chan struct{}), make(chan struct{})
	a.Application.QueueUpdateDraw(func() {
		v.loader = func(context.Context, observability.Request) (*observability.Snapshot, error) {
			close(entered)
			<-release
			return &observability.Snapshot{}, nil
		}
		v.refresh()
	})
	<-entered
	a.Application.QueueUpdateDraw(func() { v.generation++; v.request.Scope.Revision++ })
	close(release)
	a.Application.QueueUpdateDraw(func() { require.Same(t, result, v.snapshot) })
}

func TestHistoryOpeningFormIsOfflineAndNumericInputsRemainInputs(t *testing.T) {
	v := historyFixture(t)
	a := v.app
	require.NoError(t, a.Content.Init(context.WithValue(t.Context(), internal.KeyApp, a)))
	reads := 0
	v.loader = func(context.Context, observability.Request) (*observability.Snapshot, error) {
		reads++
		return nil, nil
	}
	require.NoError(t, a.inject(v, false))
	v.form()
	require.True(t, a.Content.IsTopDialog())
	_, primitive := a.Content.Pages.GetFrontPage()
	modal, ok := primitive.(*ui.ModalForm)
	require.True(t, ok)
	// Global numeric/tab bindings cannot see the focused native form.
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyRune, '1', tcell.ModNone), func(p tview.Primitive) { a.SetFocus(p) })
	frame := drawnText(t, modal, 40, 24)
	require.Contains(t, frame, "URL")
	require.Zero(t, reads)
	a.Content.Pages.RemovePage("history-source-form")
	v.Stop()
	v.Start()
	require.Zero(t, reads)
}
