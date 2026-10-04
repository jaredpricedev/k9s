// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dao

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/port"
	gwebsocket "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	constants "k8s.io/apimachinery/pkg/util/portforward"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

const (
	forwardFixturePath        = "work/api"
	forwardFixtureUID         = "pod-captured-uid"
	forwardFixtureToken       = "fixture-captured-token"
	forwardFixtureUser        = "fixture-engineer"
	forwardFixtureFlag        = "KUBECTL_PORT_FORWARD_WEBSOCKETS"
	forwardFixtureActor       = "original"
	forwardFixtureActorHeader = "X-Captured-Actor"
	forwardFixtureFallback    = "fallback"
)

func capturedFixtureTunnel() port.PortTunnel {
	return port.NewPortTunnel("127.0.0.1", "api", "0", "8080")
}

type forwardFixture struct {
	server         *httptest.Server
	getPod         bool
	get, create    bool
	phase          v1.PodPhase
	uid            string
	absent         bool
	fallback       bool
	onUpgrade      func(http.ResponseWriter, *http.Request)
	onReview       func(*http.Request)
	methods        []string
	mx             sync.Mutex
	authMismatch   atomic.Bool
	streamFinished chan struct{}
}

func newForwardFixture(t *testing.T) *forwardFixture {
	t.Helper()
	f := &forwardFixture{getPod: true, get: true, create: true, phase: v1.PodRunning, uid: forwardFixtureUID,
		streamFinished: make(chan struct{}, 1)}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *forwardFixture) config() *rest.Config {
	return &rest.Config{Host: f.server.URL, BearerToken: forwardFixtureToken,
		ContentConfig:   rest.ContentConfig{ContentType: "application/json"},
		TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})},
		Impersonate:     rest.ImpersonationConfig{UserName: forwardFixtureUser},
		Proxy: func(request *http.Request) (*url.URL, error) {
			if request.Header.Get("Authorization") != "Bearer "+forwardFixtureToken || request.Header.Get(forwardFixtureActorHeader) != forwardFixtureActor {
				return nil, errors.New("proxy routing lost captured request identity")
			}
			return nil, nil
		},
		WrapTransport: func(next http.RoundTripper) http.RoundTripper { return forwardFixtureWrapper{next: next} }}
}

type forwardFixtureWrapper struct{ next http.RoundTripper }

func (r forwardFixtureWrapper) RoundTrip(request *http.Request) (*http.Response, error) {
	captured := request.Clone(request.Context())
	captured.Header.Set(forwardFixtureActorHeader, forwardFixtureActor)
	return r.next.RoundTrip(captured)
}

func (f *forwardFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+forwardFixtureToken ||
		r.Header.Get("Impersonate-User") != forwardFixtureUser || r.Header.Get(forwardFixtureActorHeader) != forwardFixtureActor {
		f.authMismatch.Store(true)
		http.Error(w, "actor mismatch", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews"):
		if f.onReview != nil {
			_, _ = io.Copy(io.Discard, r.Body)
			f.onReview(r)
			return
		}
		var review authorizationv1.SelfSubjectAccessReview
		if json.NewDecoder(r.Body).Decode(&review) != nil || review.Spec.ResourceAttributes == nil {
			http.Error(w, "invalid review", http.StatusBadRequest)
			return
		}
		attributes := review.Spec.ResourceAttributes
		allowed := f.getPod
		if attributes.Subresource == "portforward" {
			allowed = (attributes.Verb == "get" && f.get) || (attributes.Verb == "create" && f.create)
		}
		review.TypeMeta = metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}
		review.Status.Allowed = allowed
		_ = json.NewEncoder(w).Encode(&review)
	case strings.HasSuffix(r.URL.Path, "/portforward"):
		f.mx.Lock()
		f.methods = append(f.methods, r.Method)
		f.mx.Unlock()
		if f.onUpgrade != nil {
			f.onUpgrade(w, r)
			return
		}
		f.upgrade(w, r)
	case strings.HasSuffix(r.URL.Path, "/pods/api"):
		if f.absent {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
				Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound})
			return
		}
		_ = json.NewEncoder(w).Encode(&v1.Pod{TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Namespace: "work", Name: "api", UID: types.UID(f.uid)}, Status: v1.PodStatus{Phase: f.phase}})
	default:
		http.NotFound(w, r)
	}
}

func (f *forwardFixture) upgrade(w http.ResponseWriter, r *http.Request) {
	var stream httpstream.Connection
	if r.Method == http.MethodGet {
		if f.fallback {
			http.Error(w, "websocket unsupported", http.StatusBadRequest)
			return
		}
		upgrader := gwebsocket.Upgrader{Subprotocols: []string{constants.WebsocketsSPDYTunnelingPrefix + portforward.PortForwardProtocolV1Name}}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		stream, err = streamspdy.NewServerConnection(portforward.NewTunnelingConnection("forward-fixture", connection), httpstream.NoOpNewStreamHandler)
		if err != nil {
			_ = connection.Close()
			return
		}
	} else {
		if _, err := httpstream.Handshake(r, w, []string{portforward.PortForwardProtocolV1Name}); err != nil {
			return
		}
		stream = streamspdy.NewResponseUpgrader().UpgradeResponse(w, r, httpstream.NoOpNewStreamHandler)
		if stream == nil {
			return
		}
	}
	<-stream.CloseChan()
	_ = stream.Close()
	f.streamFinished <- struct{}{}
}

func TestCapturedForwardPreflightRejectsUnavailableIdentityAndAccess(t *testing.T) {
	cases := []struct {
		name       string
		configure  func(*forwardFixture)
		want       error
		absent     bool
		phase      bool
		websockets string
	}{
		{name: "Pod read denied", configure: func(f *forwardFixture) { f.getPod = false }, want: ErrForwardEndpointDenied},
		{name: "same-name replacement", configure: func(f *forwardFixture) { f.uid = "replacement-uid" }, want: ErrForwardIdentityChanged},
		{name: "Pod absent", configure: func(f *forwardFixture) { f.absent = true }, absent: true},
		{name: "Pod not running", configure: func(f *forwardFixture) { f.phase = v1.PodPending }, phase: true},
		{name: "endpoint denied", configure: func(f *forwardFixture) { f.get, f.create = false, false }, want: ErrForwardEndpointDenied},
		{name: "get-only disabled", configure: func(f *forwardFixture) { f.create = false }, want: ErrForwardEndpointDenied, websockets: "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(forwardFixtureFlag, tc.websockets)
			f := newForwardFixture(t)
			tc.configure(f)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := NewPortForwarder(nil).StartCaptured(ctx, ctx, f.config(), forwardFixturePath, capturedFixtureTunnel(), forwardFixtureUID)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			if tc.absent {
				require.True(t, apierrors.IsNotFound(err))
			}
			if tc.phase {
				require.Contains(t, err.Error(), "not running")
			}
			require.False(t, f.authMismatch.Load())
			require.Empty(t, f.methods)
		})
	}
}

func TestCapturedForwardUpgradeCancellationClosesActualSocket(t *testing.T) {
	for _, websocket := range []bool{true, false} {
		for _, deadline := range []bool{false, true} {
			name := "SPDY"
			if websocket {
				name = "websocket"
			}
			if deadline {
				name += "/deadline"
			} else {
				name += "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv(forwardFixtureFlag, "true")
				f := newForwardFixture(t)
				f.get, f.create = websocket, !websocket
				entered, closed := make(chan struct{}), make(chan struct{})
				f.onUpgrade = hangingForwardUpgrade(entered, closed)
				setupTimeout := 5 * time.Second
				if deadline {
					setupTimeout = 250 * time.Millisecond
				}
				setup, cancelSetup := context.WithTimeout(context.Background(), setupTimeout)
				defer cancelSetup()
				lifetime, cancelLifetime := context.WithCancel(context.Background())
				defer cancelLifetime()
				owner := NewPortForwarder(nil)
				stream, err := owner.StartCaptured(setup, lifetime, f.config(), forwardFixturePath, capturedFixtureTunnel(), forwardFixtureUID)
				require.NoError(t, err)
				result := make(chan error, 1)
				go func() { result <- stream.ForwardPorts() }()
				awaitForwardFixture(t, entered)
				if !deadline {
					cancelLifetime()
				}
				select {
				case err = <-result:
					require.Error(t, err)
				case <-time.After(time.Second):
					t.Fatal("canceled upgrade worker did not finish")
				}
				awaitForwardFixture(t, closed)
				select {
				case <-owner.Ready():
					t.Fatal("a canceled handshake started a listener")
				default:
				}
			})
		}
	}
}

func TestCapturedForwardLifetimeCancellationAlsoStopsPreflight(t *testing.T) {
	f := newForwardFixture(t)
	entered, closed := make(chan struct{}), make(chan struct{})
	f.onReview = func(request *http.Request) {
		close(entered)
		<-request.Context().Done()
		close(closed)
		// A canceled request must abort the synthetic exchange. Returning
		// normally would let net/http emit an empty 200 racing client cancel.
		panic(http.ErrAbortHandler)
	}
	setup, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	result := make(chan error, 1)
	go func() {
		_, err := NewPortForwarder(nil).StartCaptured(setup, lifetime, f.config(), forwardFixturePath, capturedFixtureTunnel(), forwardFixtureUID)
		result <- err
	}()
	awaitForwardFixture(t, entered)
	cancelLifetime()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled session retained a preflight worker")
	}
	awaitForwardFixture(t, closed)
	require.Empty(t, f.methods)
}

func hangingForwardUpgrade(entered, closed chan struct{}) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close(); close(closed) }()
		close(entered)
		_, _ = io.Copy(io.Discard, connection)
	}
}

func awaitForwardFixture(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("forward fixture did not finish its lifecycle step")
	}
}

func TestCapturedForwardUsesCapturedActorAndRetainsEstablishedLifetime(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, forwardFixtureFallback} {
		t.Run(method, func(t *testing.T) {
			t.Setenv(forwardFixtureFlag, "true")
			f := newForwardFixture(t)
			f.get, f.create = method != http.MethodPost, method != http.MethodGet
			f.fallback = method == forwardFixtureFallback
			setup, cancelSetup := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancelSetup()
			lifetime, cancelLifetime := context.WithCancel(context.Background())
			defer cancelLifetime()
			owner := NewPortForwarder(nil)
			cfg := f.config()
			var dialCalls atomic.Int32
			cfg.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				dialCalls.Add(1)
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			stream, err := owner.StartCaptured(setup, lifetime, cfg, forwardFixturePath, capturedFixtureTunnel(), forwardFixtureUID)
			require.NoError(t, err)
			dialCalls.Store(0)
			cfg.Host, cfg.BearerToken, cfg.Impersonate.UserName = "https://changed.invalid", "changed", "changed"
			cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("changed dialer must not run")
			}
			result := make(chan error, 1)
			go func() { result <- stream.ForwardPorts() }()
			awaitForwardFixture(t, owner.Ready())
			ports, err := stream.GetPorts()
			require.NoError(t, err)
			require.NotZero(t, ports[0].Local)
			<-setup.Done()
			select {
			case err = <-result:
				t.Fatalf("established session inherited setup deadline: %v", err)
			default:
			}
			require.False(t, f.authMismatch.Load())
			require.Positive(t, dialCalls.Load(), "captured socket dialer was discarded")
			f.mx.Lock()
			methods := append([]string(nil), f.methods...)
			f.mx.Unlock()
			if method == forwardFixtureFallback {
				require.Equal(t, []string{http.MethodGet, http.MethodPost}, methods)
			} else {
				require.Equal(t, []string{method}, methods)
			}
			cancelLifetime()
			select {
			case <-result:
			case <-time.After(time.Second):
				t.Fatal("lifetime cancellation did not release forward")
			}
			awaitForwardFixture(t, f.streamFinished)
		})
	}
}

func TestCapturedForwardHangingProxyCONNECTIsCanceled(t *testing.T) {
	entered, closed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(hangingForwardUpgrade(entered, closed)))
	defer server.Close()
	proxyURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	endpoint, err := url.Parse("http://captured.invalid/api/v1/namespaces/work/pods/api/portforward")
	require.NoError(t, err)
	setup, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	owner := NewPortForwarder(nil)
	stream, err := owner.forwardCapturedTransport(setup, lifetime, &rest.Config{Proxy: http.ProxyURL(proxyURL)}, endpoint, capturedFixtureTunnel(), false, true)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { result <- stream.ForwardPorts() }()
	awaitForwardFixture(t, entered)
	cancelLifetime()
	select {
	case err = <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("proxy CONNECT retained a canceled socket")
	}
	awaitForwardFixture(t, closed)
}

func TestCapturedForwardRefusesOpaqueHTTPTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, err := url.Parse("https://captured.invalid")
	require.NoError(t, err)
	_, err = NewPortForwarder(nil).forwardCapturedTransport(ctx, ctx, &rest.Config{Transport: http.DefaultTransport}, endpoint, capturedFixtureTunnel(), false, true)
	require.ErrorContains(t, err, "custom HTTP transport")
}
