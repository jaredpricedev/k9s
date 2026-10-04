// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/upgrade"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestUpgradeReadinessLayoutsKeepIdentityCoverageAndLimits(t *testing.T) {
	snapshot := upgrade.Snapshot{
		Context: "prod-west", Namespace: "payments", NamespaceUID: "1234567890abcdef", NamespaceState: "observed",
		ServerVersion: "v1.34.1", ServerVersionState: "observed", ObservedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Nodes:       upgrade.Section{State: "observed", Items: []upgrade.Fact{{Name: "node-1", Version: "v1.34.1"}}, Truncated: true},
		Deployments: upgrade.Section{State: "observed", Items: []upgrade.Fact{{Name: "api/web", Version: "registry.example/api:v2"}}},
	}
	for _, width := range []int{80, 60, 40} {
		text := renderUpgradeReadiness(&snapshot, "", width)
		for _, want := range []string{"prod-west", "payments", "UID:", "native Kubernetes API", "2026-10-04T12:00:00Z", "first 100", "facts", "registry"} {
			if !strings.Contains(text, want) {
				t.Errorf("width %d omitted %q:\n%s", width, want, text)
			}
		}
	}
	if got := renderUpgradeReadiness(&snapshot, "", 39); !strings.Contains(got, "Terminal too small") {
		t.Fatalf("minimum size state missing: %q", got)
	}
}

func TestUpgradeReadinessNeverClaimsCompatibilityOrExhaustiveUsage(t *testing.T) {
	text := renderUpgradeReadiness(&upgrade.Snapshot{Context: "ctx", Namespace: "ns", NamespaceUID: "uid", ObservedAt: time.Now()}, "", 80)
	for _, want := range []string{"unsupported", "do not establish compatibility", "does not exhaust deprecated API usage"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing caveat %q in %s", want, text)
		}
	}
}

func TestUpgradeReadinessDispatcherRefreshAndLifecycle(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	require.NoError(t, app.Content.Init(context.WithValue(context.Background(), internal.KeyApp, app)))
	request := connectionHealthRequest{Context: app.Config.ActiveContextName(), Namespace: app.Config.ActiveNamespace(), Revision: app.Config.DestinationRevision()}
	type work struct {
		ctx   context.Context
		reply chan upgrade.Snapshot
		done  chan struct{}
	}
	jobs := make(chan work, 10)
	d := &upgradeReadinessDetails{Details: NewDetails(app, "Upgrade readiness", "", contentInspection, true), request: request}
	d.loader = func(ctx context.Context, _ connectionHealthRequest) upgrade.Snapshot {
		job := work{ctx: ctx, reply: make(chan upgrade.Snapshot), done: make(chan struct{})}
		jobs <- job
		result := <-job.reply
		close(job.done)
		return result
	}
	dispatch := runUpgradeTestDispatcher(t, app)
	dispatch(func() { assert.NoError(t, app.inject(d, false)) })
	next := func() work {
		select {
		case job := <-jobs:
			return job
		case <-time.After(time.Second):
			t.Fatal("read did not start")
			return work{}
		}
	}
	evidence := func(version string) upgrade.Snapshot {
		return upgrade.Snapshot{Context: request.Context, Namespace: request.Namespace, ObservedAt: time.Now(), ServerVersionState: "observed", ServerVersion: version}
	}
	complete := func(job work, s upgrade.Snapshot) { job.reply <- s; <-job.done }
	first := next()
	complete(first, evidence(rolloutPodAPI))
	require.Eventually(t, func() bool {
		ok := false
		dispatch(func() { ok = d.snapshot.ServerVersion == rolloutPodAPI })
		return ok
	}, time.Second, time.Millisecond)
	dispatch(func() { d.refresh() })
	failed := next()
	failure := evidence("")
	failure.ServerVersionState = "denied"
	complete(failed, failure)
	require.Eventually(t, func() bool {
		ok := false
		dispatch(func() { ok = strings.Contains(d.status, "retained earlier") })
		return ok
	}, time.Second, time.Millisecond)
	dispatch(func() {
		assert.Equal(t, rolloutPodAPI, d.snapshot.ServerVersion)
		d.cmdBuff.SetActive(true)
		action, _ := d.actions.Get(ui.KeyR)
		event := tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone)
		assert.Same(t, event, action.Action(event))
		d.cmdBuff.SetActive(false)
		d.refresh()
	})
	old := next()
	dispatch(func() { d.refresh() })
	newest := next()
	require.Error(t, old.ctx.Err())
	complete(newest, evidence("v2"))
	complete(old, evidence("late"))
	require.Eventually(t, func() bool { ok := false; dispatch(func() { ok = d.snapshot.ServerVersion == "v2" }); return ok }, time.Second, time.Millisecond)
	dispatch(func() { d.refresh() })
	destination := next()
	dispatch(func() { assert.NoError(t, app.Config.SetActiveNamespace("other-upgrade-namespace")) })
	complete(destination, evidence("wrong-destination"))
	dispatch(func() {
		d.refresh()
		assert.Equal(t, "v2", d.snapshot.ServerVersion)
		assert.Contains(t, d.status, "Destination changed")
	})
	dispatch(func() {
		if err := app.Config.SetActiveNamespace(request.Namespace); err != nil {
			t.Error(err)
			return
		}
		d.request.Revision = app.Config.DestinationRevision()
		d.refresh()
	})
	stopped := next()
	dispatch(func() { d.Stop() })
	require.Error(t, stopped.ctx.Err())
	complete(stopped, evidence("stopped"))
	dispatch(func() {
		assert.Equal(t, "v2", d.snapshot.ServerVersion)
		d.started = true
		d.request.Revision++
		d.refresh()
		assert.Contains(t, d.status, "Destination changed")
		d.Stop()
	})
	select {
	case <-jobs:
		t.Fatal("query/destination guard launched an extra read")
	default:
	}
}

func TestUpgradeReadinessPollerPreservesEvidenceBeyondRetryBudget(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	app.Config.SetConnection(conn)
	app.factory = watch.NewFactory(conn)
	app.clusterModel = model.NewClusterInfo(app.factory, "test", app.Config.K9s)
	app.Config.K9s.MaxConnRetry = 1
	d := &upgradeReadinessDetails{Details: NewDetails(app, "Upgrade readiness", "", contentInspection, true), snapshot: upgrade.Snapshot{ObservedAt: time.Now(), ServerVersion: "retained-version"}, status: "failed attempt; retained evidence"}
	app.Content.Push(d)
	for range 3 {
		require.NoError(t, app.refreshCluster(context.Background()))
		require.Same(t, d, app.Content.Top())
		require.Equal(t, "retained-version", d.snapshot.ServerVersion)
		require.Contains(t, d.status, "retained evidence")
	}
	require.Greater(t, atomic.LoadInt32(&app.conRetry), app.Config.K9s.MaxConnRetry)
}

func TestUpgradeReadinessVersionHTTPDeadlineAndInvalidScope(t *testing.T) {
	canceled := make(chan struct{}, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/version" {
			<-r.Context().Done()
			canceled <- struct{}{}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
	}))
	defer server.Close()
	typed, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	conn := &pinnedInspectionConnection{Connection: mock.NewMockConnection(), typedClient: typed}
	got := loadUpgradeReadiness(context.Background(), conn, connectionHealthRequest{Context: "ctx", Namespace: "all"})
	require.True(t, got.AllFactReadsFailed())
	require.Zero(t, calls.Load())
	got = loadUpgradeReadiness(context.Background(), conn, connectionHealthRequest{Context: strings.Repeat("x", 513), Namespace: "team-a"})
	require.True(t, got.AllFactReadsFailed())
	require.Len(t, []rune(got.Context), 512)
	require.Zero(t, calls.Load())
	started := time.Now()
	got = loadUpgradeReadiness(context.Background(), conn, connectionHealthRequest{Context: "ctx", Namespace: "team-a"})
	require.Less(t, time.Since(started), 4*time.Second)
	require.Equal(t, "canceled/timeout", got.ServerVersionState)
	require.Equal(t, "denied", got.Nodes.State)
	require.True(t, got.AllFactReadsFailed())
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("version transport was not canceled")
	}
}

func runUpgradeTestDispatcher(t *testing.T, app *App) func(func()) {
	t.Helper()
	// Start the actual tview dispatcher before injecting a refreshing page.
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	painted := make(chan struct{}, 1)
	app.SetScreen(screen).SetRoot(app.Content, true).SetAfterDrawFunc(func(tcell.Screen) {
		select {
		case painted <- struct{}{}:
		default:
		}
	})
	app.SetRunning(true)
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	t.Cleanup(func() { app.Stop(); app.SetRunning(false); require.NoError(t, <-finished) })
	select {
	case <-painted:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not start")
	}
	return func(fn func()) {
		done := make(chan struct{})
		app.QueueUpdateDraw(func() { fn(); close(done) })
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("dispatcher blocked")
		}
	}
}

func TestUpgradeReadinessCompactFailedRefreshKeepsAttemptGaps(t *testing.T) {
	d := &upgradeReadinessDetails{request: connectionHealthRequest{Context: "ctx", Namespace: "apps"}, snapshot: upgrade.Snapshot{Context: "ctx", Namespace: "apps", ObservedAt: time.Unix(1, 0), ServerVersion: "prior"}}
	failed := upgrade.Snapshot{Context: "ctx", Namespace: "apps", ObservedAt: time.Unix(2, 0), ServerVersionState: "denied", Nodes: upgrade.Section{State: "canceled/timeout"}}
	d.acceptSnapshot(&failed)
	text := renderUpgradeReadiness(&d.snapshot, d.status, 40)
	for _, want := range []string{"retained earlier evidence", "1970-01-01T00:00:02Z", "API:denied", "nodes:canceled/timeout"} {
		require.Contains(t, text, want)
	}
	require.Equal(t, "prior", d.snapshot.ServerVersion)
}
