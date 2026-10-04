// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

// Middleware exposes its inner transport without implementing idle closure.
type policyIdleWrapper struct{ http.RoundTripper }

func (w policyIdleWrapper) WrappedRoundTripper() http.RoundTripper { return w.RoundTripper }

func TestNativeWritePolicySessionClosesActualIdleConnections(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "plain"
		if nested {
			name = "nested wrappers"
		}
		t.Run(name, func(t *testing.T) {
			var accepted atomic.Int32
			idle, closed := make(chan struct{}, 4), make(chan struct{}, 4)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"policy-pod","namespace":"apps","uid":"policy-uid"}}`))
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				switch state {
				case http.StateNew:
					accepted.Add(1)
				case http.StateIdle:
					idle <- struct{}{}
				case http.StateClosed:
					closed <- struct{}{}
				}
			}
			server.Start()
			defer server.Close()
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			captured := &rest.Config{Host: server.URL, Transport: transport}
			if nested {
				captured.WrapTransport = func(inner http.RoundTripper) http.RoundTripper {
					return policyIdleWrapper{RoundTripper: policyIdleWrapper{RoundTripper: inner}}
				}
			}
			config := NewConfig(genericclioptions.NewConfigFlags(false))
			config.PrepareSessionREST(captured)
			connection, err := NewSessionConnection(config)
			require.NoError(t, err)
			defer connection.CloseSession()
			typed, err := connection.Dial()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = typed.CoreV1().Pods("apps").Get(ctx, policyPodName, metav1.GetOptions{})
			require.NoError(t, err)
			select {
			case <-idle:
			case <-ctx.Done():
				t.Fatal("successful request did not leave an idle connection")
			}
			connection.CloseSession()
			select {
			case <-closed:
			case <-ctx.Done():
				t.Fatal("session closure did not close the actual idle connection")
			}
			// The transport remains usable, but the old connection cannot be reused.
			_, err = typed.CoreV1().Pods("apps").Get(ctx, policyPodName, metav1.GetOptions{})
			require.NoError(t, err)
			require.EqualValues(t, 2, accepted.Load())
		})
	}
}
