// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

const (
	connectionHealthFixtureActor       = "actor"
	connectionHealthFixtureVersionPath = "/version"
)

func connectionHealthConfigFixture(t *testing.T, server string, ca []byte) *client.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	raw := api.Config{
		CurrentContext: connectionHealthFixtureActor,
		Contexts:       map[string]*api.Context{connectionHealthFixtureActor: {Cluster: "cluster", AuthInfo: "user"}},
		Clusters:       map[string]*api.Cluster{"cluster": {Server: server, CertificateAuthorityData: ca}},
		AuthInfos:      map[string]*api.AuthInfo{"user": {Token: "configured-secret-token"}},
	}
	require.NoError(t, clientcmd.WriteToFile(raw, path))
	flags := genericclioptions.NewConfigFlags(true)
	flags.KubeConfig = &path
	cfg, err := client.NewConfig(flags).PinnedDiagnosticConfig(connectionHealthFixtureActor)
	require.NoError(t, err)
	return cfg
}

func TestConnectionHealthClassifiesSafeDistinctFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		state connectionHealthState
	}{
		{"readable", nil, connectionReadable},
		{"credentials rejected", apierrors.NewUnauthorized("Bearer SECRET"), connectionCredentials},
		{"credentials unavailable", errors.New("getting credentials: exec plugin stderr SECRET"), connectionCredentials},
		{"permission denied", apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "pod", errors.New("SECRET")), connectionDenied},
		{"untrusted certificate", fmt.Errorf("transport: %w", x509.UnknownAuthorityError{}), connectionTLS},
		{"hostname mismatch", x509.HostnameError{}, connectionTLS},
		{"expired certificate", x509.CertificateInvalidError{}, connectionTLS},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("SECRET")}, connectionUnreachable},
		{"DNS unavailable", &net.DNSError{Err: "not found", Name: "SECRET"}, connectionUnreachable},
		{"request deadline", context.DeadlineExceeded, connectionTimeout},
		{"server timeout", apierrors.NewTimeoutError("SECRET", 2), connectionTimeout},
		{"canceled", fmt.Errorf("wrapped: %w", context.Canceled), connectionCanceled},
		{"API missing", apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "SECRET"), connectionDiscovery},
		{"discovery failure", &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{{Version: "v1"}: errors.New("SECRET")}}, connectionDiscovery},
		{"unknown", errors.New("SECRET kubeconfig password"), connectionUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := classifyConnectionHealthError(test.err)
			require.Equal(t, test.state, state)
			snapshot := connectionHealthFailure(connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: "team"}, state, time.Now())
			require.NotContains(t, renderConnectionHealth(snapshot, connectionHealthSnapshot{}, time.Now(), false), "SECRET")
		})
	}
}

func TestConnectionHealthUsesOnlyBoundedCurrentNamespaceReads(t *testing.T) {
	var mu sync.Mutex
	var paths, limits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		limits = append(limits, r.URL.Query().Get("limit"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case connectionHealthFixtureVersionPath:
			fmt.Fprint(w, `{"gitVersion":"v1.35.3"}`)
		case "/api/v1/namespaces/team/pods":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"SECRET raw credential error","code":403}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	cfg := connectionHealthConfigFixture(t, server.URL, nil)
	request := connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: "team"}
	snapshot := collectConnectionHealth(t.Context(), request, cfg, nil)
	require.Len(t, snapshot.Checks, 2)
	require.Equal(t, connectionReadable, snapshot.Checks[0].State)
	require.Equal(t, connectionDenied, snapshot.Checks[1].State)
	mu.Lock()
	require.Equal(t, []string{connectionHealthFixtureVersionPath, "/api/v1/namespaces/team/pods"}, paths)
	require.Equal(t, []string{"", "1"}, limits)
	paths, limits = nil, nil
	mu.Unlock()
	require.NotContains(t, renderConnectionHealth(snapshot, connectionHealthSnapshot{}, time.Now(), false), "SECRET")
	require.NotContains(t, renderConnectionHealth(snapshot, connectionHealthSnapshot{}, time.Now(), false), "configured-secret-token")
	request.Namespace = client.NamespaceAll
	snapshot = collectConnectionHealth(t.Context(), request, cfg, nil)
	require.Equal(t, connectionSkipped, snapshot.Checks[1].State)
	mu.Lock()
	require.Equal(t, []string{connectionHealthFixtureVersionPath}, paths)
	mu.Unlock()
}

func TestConnectionHealthRealTLSFailureAndUnauthorizedAreDistinct(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","message":"SECRET","code":401}`)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	request := connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: "team"}
	untrusted := collectConnectionHealth(t.Context(), request, connectionHealthConfigFixture(t, server.URL, nil), nil)
	require.Equal(t, connectionTLS, untrusted.Checks[0].State)

	// Trust this fixture's server certificate; the identical endpoint now exposes
	// the auth rejection instead of incorrectly describing it as network failure.
	certificate := server.Certificate()
	trusted := connectionHealthConfigFixture(t, server.URL, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	authRejected := collectConnectionHealth(t.Context(), request, trusted, nil)
	require.Equal(t, connectionCredentials, authRejected.Checks[0].State)
	require.NotContains(t, renderConnectionHealth(authRejected, connectionHealthSnapshot{}, time.Now(), false), "SECRET")
}

func TestConnectionHealthDeadlineAndLateResultAreBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	completed := make(chan struct{})
	request := connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: "team"}
	probe := func(context.Context, connectionHealthRequest) connectionHealthSnapshot {
		<-release
		close(completed)
		return connectionHealthFailure(request, connectionReadable, time.Now())
	}
	snapshot := boundedConnectionHealth(ctx, request, probe)
	require.Equal(t, connectionTimeout, snapshot.Checks[0].State)
	close(release)
	<-completed
	require.Equal(t, connectionTimeout, snapshot.Checks[0].State, "late callback must not replace timeout observation")
}

func TestConnectionHealthViewGuardsAndPreservesPriorAge(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	details := &connectionHealthDetails{
		Details: NewDetails(app, "Connection", connectionHealthFixtureActor, contentInspection, true), started: true, generation: 5,
		request: connectionHealthRequest{Context: app.Config.ActiveContextName(), Namespace: app.Config.ActiveNamespace(), Revision: app.Config.DestinationRevision()},
	}
	app.Content.Push(details)
	require.True(t, details.current(5))
	require.False(t, details.current(4))
	details.started = false
	require.False(t, details.current(5))
	details.started = true
	previousNamespace := app.Config.ActiveNamespace()
	require.NoError(t, app.Config.SetActiveNamespace("other"))
	require.False(t, details.current(5))
	require.NoError(t, app.Config.SetActiveNamespace(previousNamespace))
	require.False(t, details.current(5), "a destination round trip does not accept a late result")
	details.request.Revision = app.Config.DestinationRevision()
	app.Content.Push(NewDetails(app, "Replacement", "", contentInspection, true))
	require.False(t, details.current(5))

	now := time.Now()
	old := connectionHealthFailure(details.request, connectionReadable, now.Add(-3*time.Minute))
	details.snapshot = old
	details.acceptSnapshot(connectionHealthFailure(details.request, connectionUnreachable, now))
	require.Equal(t, old.CheckedAt, details.previous.CheckedAt)
	rendered := renderConnectionHealth(details.snapshot, details.previous, now, false)
	require.Contains(t, rendered, "PREVIOUS READABLE OBSERVATION · 3m0s ago · retained, not current")
	details.acceptSnapshot(connectionHealthFailure(details.request, connectionCredentials, now.Add(time.Second)))
	require.Equal(t, old.CheckedAt, details.previous.CheckedAt, "success age must survive repeated failed retries")
}

func TestConnectionHealthUnavailableConfigStillRenders(t *testing.T) {
	request := connectionHealthRequest{Context: connectionHealthFixtureActor, Namespace: "team"}
	snapshot := collectConnectionHealth(t.Context(), request, nil, errors.New("SECRET config token"))
	rendered := renderConnectionHealth(snapshot, connectionHealthSnapshot{}, time.Now(), false)
	require.Contains(t, rendered, "UNKNOWN  Client configuration")
	require.Contains(t, rendered, "Context actor · namespace team")
	require.NotContains(t, rendered, "SECRET")
	require.Contains(t, rendered, "Retry reloads configuration")
}
