// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

const (
	sessionRenewedCredential  = "renewed-fixture-credential"
	sessionFixtureNamespace   = "team"
	sessionFixtureCredentials = "credentials"
	sessionFixturePodListPath = "/api/v1/namespaces/team/pods"
	sessionCertificateType    = "CERTIFICATE"
	sessionAlternateContext   = "elsewhere"
	sessionCoreAPIVersion     = "v1"
	sessionFixtureTargetPath  = sessionFixtureNamespace + "/" + investigationAppRole
)

func sessionFixtureServer(t *testing.T, failure string, authenticated *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer "+sessionRenewedCredential {
			authenticated.Add(1)
		}
		if failure == sessionFixtureCredentials {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","reason":"Unauthorized","code":401,"message":"PRIVATE_FIXTURE_VALUE"}`)
			return
		}
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"gitVersion":"v1.35.3"}`)
		case sessionFixturePodListPath:
			if failure == "permission" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","reason":"Forbidden","code":403,"message":"PRIVATE_FIXTURE_VALUE"}`)
				return
			}
			if r.URL.Query().Get("limit") != "1" {
				t.Error("namespace check must use a one-object limit")
			}
			fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","items":[]}`)
		case "/api":
			if failure == "discovery" {
				fmt.Fprint(w, `{"versions":[]}`)
				return
			}
			fmt.Fprint(w, `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`)
		case "/apis":
			fmt.Fprint(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`)
		default:
			t.Errorf("unexpected session setup request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestSessionRefreshLoadsRenewedActorAndFreezesPreparedHandles(t *testing.T) {
	var authenticated atomic.Int32
	server := sessionFixtureServer(t, "", &authenticated)
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: sessionCertificateType, Bytes: server.Certificate().Raw})
	actor := connectionHealthConfigFixture(t, server.URL, ca)
	oldREST, err := actor.RESTConfig()
	require.NoError(t, err)
	path := *actor.Flags().KubeConfig
	raw, err := clientcmd.LoadFromFile(path)
	require.NoError(t, err)
	raw.CurrentContext = sessionAlternateContext
	raw.Contexts[sessionAlternateContext] = &api.Context{Cluster: sessionAlternateContext, AuthInfo: sessionAlternateContext}
	raw.Clusters[sessionAlternateContext] = &api.Cluster{Server: "https://elsewhere.invalid"}
	raw.AuthInfos[sessionAlternateContext] = &api.AuthInfo{Token: "different-fixture-credential"}
	raw.AuthInfos["user"].Token = sessionRenewedCredential
	require.NoError(t, clientcmd.WriteToFile(*raw, path))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	private, err := actor.PinnedDiagnosticConfig(connectionHealthFixtureActor)
	require.NoError(t, err)
	request := connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: sessionFixtureNamespace, Revision: 7}
	result := prepareSessionRefresh(t.Context(), request, private, nil)
	require.NotNil(t, result.connection)
	defer closePreparedSession(result.connection)
	require.True(t, connectionHealthComplete(result.snapshot))
	require.EqualValues(t, 4, authenticated.Load())
	require.NotEqual(t, sessionRenewedCredential, oldREST.BearerToken)
	freshREST, err := result.connection.RestConfig()
	require.NoError(t, err)
	require.Equal(t, sessionRenewedCredential, freshREST.BearerToken)
	require.Equal(t, server.URL, freshREST.Host)
	require.Equal(t, oldREST.Timeout, freshREST.Timeout, "setup deadline must not shorten browsing/watch transports")
	require.Equal(t, sessionFixtureNamespace, *private.Flags().Namespace)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "session refresh must not modify global kubeconfig")
	// A later file edit affects the next explicit refresh, never prepared handles.
	raw.AuthInfos["user"].Token = "later-fixture-credential"
	require.NoError(t, clientcmd.WriteToFile(*raw, path))
	frozen, err := result.connection.RestConfig()
	require.NoError(t, err)
	require.Equal(t, sessionRenewedCredential, frozen.BearerToken)
	require.True(t, result.connection.CheckConnectivity())
	require.EqualValues(t, 5, authenticated.Load(), "periodic connectivity checks must use the committed actor")
}

func TestSessionRefreshFailureCategoriesKeepUncommittedHandlesPrivate(t *testing.T) {
	for _, test := range []struct {
		name  string
		state connectionHealthState
	}{
		{sessionFixtureCredentials, connectionCredentials}, {"permission", connectionDenied}, {"discovery", connectionDiscovery},
	} {
		t.Run(test.name, func(t *testing.T) {
			var authenticated atomic.Int32
			server := sessionFixtureServer(t, test.name, &authenticated)
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: sessionCertificateType, Bytes: server.Certificate().Raw})
			cfg := connectionHealthConfigFixture(t, server.URL, ca)
			result := prepareSessionRefresh(t.Context(), connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: sessionFixtureNamespace}, cfg, nil)
			require.Nil(t, result.connection)
			found := false
			for _, check := range result.snapshot.Checks {
				if check.State == test.state {
					found = true
				}
			}
			require.True(t, found)
			require.NotContains(t, renderConnectionHealth(result.snapshot, connectionHealthSnapshot{}, time.Now(), false), "PRIVATE_FIXTURE_VALUE")
		})
	}
}

type sessionFixtureConnection struct {
	client.Connection
	config *client.Config
	closed chan struct{}
	once   sync.Once
}

func (c *sessionFixtureConnection) Config() *client.Config { return c.config }
func (c *sessionFixtureConnection) CloseSession() {
	c.once.Do(func() {
		if c.closed != nil {
			close(c.closed)
		}
	})
}

func TestBoundedSessionRefreshCancelsSetupAndDisposesLateConnection(t *testing.T) {
	release, closed := make(chan struct{}), make(chan struct{})
	late := &sessionFixtureConnection{closed: closed}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	result := boundedSessionRefresh(ctx, connectionHealthRequest{}, nil, nil, func(context.Context, connectionHealthRequest, *client.Config, error) sessionRefreshResult {
		<-release
		return sessionRefreshResult{connection: late}
	})
	require.Nil(t, result.connection)
	require.Equal(t, connectionTimeout, result.snapshot.Checks[0].State)
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late prepared connection was not disposed")
	}
}

func sessionAppFixture(t *testing.T) (*App, connectionHealthRequest, *sessionFixtureConnection) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	flags := genericclioptions.NewConfigFlags(false)
	name := app.Config.ActiveContextName()
	flags.Context = &name
	conn := &sessionFixtureConnection{Connection: mock.NewMockConnection(), config: client.NewConfig(flags), closed: make(chan struct{})}
	app.Config.SetConnection(conn)
	app.factory = watch.NewFactory(conn)
	app.factory.Start(app.Config.ActiveNamespace())
	request := connectionHealthRequest{Context: name, Namespace: app.Config.ActiveNamespace(), Revision: app.Config.DestinationRevision()}
	t.Cleanup(app.Halt)
	return app, request, conn
}

func TestSessionRefreshPreservesRetainedStackAndAdvancesDestinationRevision(t *testing.T) {
	app, request, old := sessionAppFixture(t)
	owner := &discoveryOwner{Details: NewDetails(app, "saved workspace", "", contentTXT, false)}
	owner.text.SetText("retained evidence at original time").ScrollTo(3, 4)
	app.Content.Push(owner)
	connection := &connectionHealthDetails{Details: NewDetails(app, "Connection and session", request.Context, contentInspection, true), request: request,
		snapshot: connectionHealthFailure(request, connectionCredentials, time.Now())}
	app.Content.Push(connection)
	stopsBefore := owner.stops
	app.command = NewCommand(app)
	factory := app.factory
	flags := genericclioptions.NewConfigFlags(false)
	flags.Context = &request.Context
	fresh := &sessionFixtureConnection{Connection: mock.NewMockConnection(), config: client.NewConfig(flags)}
	require.NoError(t, app.replaceSession(request, fresh))
	require.Same(t, fresh, app.Conn())
	require.NotSame(t, factory, app.factory)
	require.Equal(t, request.Revision+1, app.Config.DestinationRevision())
	require.Same(t, owner, app.Content.Peek()[0])
	require.Same(t, connection, app.Content.Top())
	require.Equal(t, "retained evidence at original time", owner.text.GetText(true))
	row, col := owner.text.GetScrollOffset()
	require.Equal(t, 3, row)
	require.Equal(t, 4, col)
	require.Equal(t, stopsBefore+1, owner.stops)
	select {
	case <-old.closed:
	default:
		t.Fatal("old session transport was not released")
	}
	require.Equal(t, request.Revision, connection.snapshot.Request.Revision, "retained observations keep original identity")
}

func TestSessionRefreshFailureAndChangedDestinationRetainUsableState(t *testing.T) {
	app, request, old := sessionAppFixture(t)
	view := &connectionHealthDetails{Details: NewDetails(app, "Connection and session", request.Context, contentInspection, true), request: request}
	view.acceptSessionRefresh(sessionRefreshResult{snapshot: connectionHealthFailure(request, connectionCredentials, time.Now())})
	require.Same(t, old, app.Conn())
	require.Equal(t, request.Revision, app.Config.DestinationRevision())
	require.Contains(t, view.sessionNotice, "Existing session and retained workspace kept")
	head := strings.Split(view.renderHealth(false), "\n")
	require.Contains(t, head[2], "R reconnect session")
	require.Contains(t, head[3], "RECONNECT FAILED")
	flags := genericclioptions.NewConfigFlags(false)
	flags.Context = &request.Context
	fresh := &sessionFixtureConnection{Connection: mock.NewMockConnection(), config: client.NewConfig(flags)}
	request.Revision++
	factory := app.factory
	require.Error(t, app.replaceSession(request, fresh))
	require.Same(t, old, app.Conn())
	require.Same(t, factory, app.factory)
}

func TestBrowserSessionRejectsOldModelGeneration(t *testing.T) {
	app, _, _ := sessionAppFixture(t)
	browser := NewBrowser(client.PodGVR).(*Browser)
	browser.app = app
	old := browser.GetModel()
	old.SetNamespace(sessionFixtureNamespace)
	browser.SetInstance("team/original")
	old.SetRefreshRate(time.Second)
	listener := &browserWatchListener{browser: browser, model: old, generation: browser.watchGeneration.Load()}
	browser.rebindSession(SelectedResourceTarget{})
	app.SetRunning(true)
	require.NotSame(t, old, browser.GetModel())
	require.False(t, browser.watchCurrent(listener.generation, listener.model))
	require.Equal(t, sessionFixtureNamespace, browser.GetModel().GetNamespace())
	require.IsType(t, (*model.Table)(nil), browser.GetModel())
}

func TestXraySessionRejectsOldTreeGeneration(t *testing.T) {
	app, _, _ := sessionAppFixture(t)
	tree := NewXray(client.PodGVR).(*Xray)
	tree.app = app
	tree.model.SetNamespace(sessionFixtureNamespace)
	tree.SetSelectedItem("team/unverified")
	old := &xraySessionListener{view: tree, model: tree.model, generation: tree.generation.Load()}
	tree.Stop()
	tree.rebindSession()
	app.SetRunning(true)
	app.Content.Push(tree)
	require.NotSame(t, old.model, tree.model)
	require.False(t, tree.sessionCurrent(old.generation, old.model))
	require.Equal(t, sessionFixtureNamespace, tree.model.GetNamespace())
	require.Empty(t, tree.GetSelectedItem())
}

func TestPulseSessionRetainsMetricsOnlyAsStaleWithOriginalTime(t *testing.T) {
	app, _, _ := sessionAppFixture(t)
	pulse := NewPulse(client.PuGVR).(*Pulse)
	pulse.app = app
	original := pulse.model
	observed := time.Now().Add(-time.Second)
	pulse.metricsSample = client.NewMetricSample(client.ClusterMetrics{PercCPU: 13}, observed, client.PodMetricsSource, time.Now())
	pulse.rebindSession(sessionFixtureNamespace)
	require.NotSame(t, original, pulse.model)
	require.Equal(t, client.MetricsStale, pulse.metricsSample.State)
	require.Equal(t, observed, pulse.metricsSample.ObservedAt)
	require.Equal(t, 13, pulse.metricsSample.Values.PercCPU)
	require.False(t, pulse.metricsSample.Fresh())
}

func TestSessionRefreshExplicitCancellationReturnsNoPrivateHandles(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := boundedSessionRefresh(ctx, connectionHealthRequest{}, nil, nil, func(context.Context, connectionHealthRequest, *client.Config, error) sessionRefreshResult {
		return sessionRefreshResult{}
	})
	require.Nil(t, result.connection)
	require.Equal(t, connectionCanceled, result.snapshot.Checks[0].State)
}

type sessionSelectionConnection struct {
	client.Connection
	reader dynamic.Interface
}

func (c sessionSelectionConnection) DynDial() (dynamic.Interface, error) { return c.reader, nil }

func TestBrowserSessionRestoresOnlyMatchingResourceUID(t *testing.T) {
	metas := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = metas })
	for _, test := range []struct {
		name, uid   string
		selectedRow int
	}{
		{"same UID", "captured-uid", 2}, {nativeReplacement, "new-session-uid", 0}, {workspaceUnknown, "", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, request, _ := sessionAppFixture(t)
			obj := new(unstructured.Unstructured)
			obj.SetAPIVersion(sessionCoreAPIVersion)
			obj.SetKind(inspectionPodKind)
			obj.SetNamespace(sessionFixtureNamespace)
			obj.SetName(investigationAppRole)
			obj.SetUID(types.UID(test.uid))
			reader := fake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			factory := watch.NewFactory(sessionSelectionConnection{Connection: mock.NewMockConnection(), reader: reader})
			app.factory = factory
			factory.Start(sessionFixtureNamespace)
			t.Cleanup(factory.Terminate)
			inf, err := factory.ForResource(sessionFixtureNamespace, client.PodGVR)
			require.NoError(t, err)
			require.NoError(t, inf.Informer().GetStore().Add(obj))
			browser := NewBrowser(client.PodGVR).(*Browser)
			browser.app = app
			browser.GetModel().SetNamespace(sessionFixtureNamespace)
			browser.SetCell(0, 0, tview.NewTableCell("NAME"))
			browser.SetCell(1, 0, tview.NewTableCell("other").SetReference("team/other"))
			browser.SetCell(2, 0, tview.NewTableCell(investigationAppRole).SetReference(sessionFixtureTargetPath))
			browser.Select(1, 0)
			browser.sessionSelection = &SelectedResourceTarget{Context: request.Context, GVR: client.PodGVR, Namespace: sessionFixtureNamespace, Name: investigationAppRole, UID: "captured-uid"}
			browser.restoreSessionSelection()
			row, _ := browser.GetSelection()
			require.Equal(t, test.selectedRow, row)
			require.Nil(t, browser.sessionSelection)
		})
	}
}
