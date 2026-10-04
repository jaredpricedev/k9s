// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestMaintenanceActualTransportCancellationStopsCollection(t *testing.T) {
	for _, stage := range []string{"node", testPodsResource} {
		t.Run(stage, func(t *testing.T) {
			started, release, disconnected := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if stage == testPodsResource && request.URL.Path == "/api/v1/nodes/"+testNode {
					writer.Header().Set("Content-Type", "application/json")
					node := fixtureNode()
					node.APIVersion, node.Kind = "v1", "Node"
					_ = json.NewEncoder(writer).Encode(node)
					return
				}
				close(started)
				select {
				case <-request.Context().Done():
					close(disconnected)
				case <-release:
				}
			}))
			defer func() { close(release); server.Close() }()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			var snapshot *Snapshot
			var collectErr error
			go func() { snapshot, collectErr = Collect(ctx, client, fixtureIdentity(), time.Now()); close(done) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("captured read did not start")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("collection outlived canceled operation")
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("actual HTTP read outlived captured context")
			}
			if stage == "node" {
				require.Nil(t, snapshot)
				require.ErrorIs(t, collectErr, context.Canceled)
			} else {
				require.NoError(t, collectErr)
				require.NotNil(t, snapshot)
				require.False(t, snapshot.PodsComplete())
				require.Contains(t, snapshot.Render(5), "canceled")
			}
		})
	}
}
