// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestCollectRealTransportCancellationKeepsIndependentSuccessfulSources(t *testing.T) {
	now := time.Now()
	pod := object(t, testPod(now))
	nodeStarted := make(chan struct{})
	nodeCanceled := make(chan struct{})
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/v1/nodes" {
			close(nodeStarted)
			<-r.Context().Done()
			close(nodeCanceled)
			return
		}
		var items []any
		kind, version := "List", "v1"
		if r.URL.Path == "/api/v1/namespaces/apps/pods" {
			items = []any{pod.Object}
			kind = "PodList"
		}
		if r.URL.Path == "/apis/metrics.k8s.io/v1beta1/namespaces/apps/pods" {
			kind = "PodMetricsList"
			version = "metrics.k8s.io/v1beta1"
		}
		w.Header().Set("Content-Type", "application/json")
		if items == nil {
			items = []any{}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{}, "items": items}); err != nil {
			t.Errorf("fixture response: %v", err)
		}
	}))
	defer server.Close()
	reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan *Snapshot, 1)
	go func() { result <- Collect(ctx, reader, scope(), now) }()
	select {
	case <-nodeStarted:
	case <-time.After(time.Second):
		t.Fatal("node read did not begin")
	}
	// Independent requests can complete while the node response is blocked.
	time.AfterFunc(100*time.Millisecond, cancel)
	var snapshot *Snapshot
	select {
	case snapshot = <-result:
	case <-time.After(time.Second):
		t.Fatal("collection did not respect cancellation")
	}
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, Unavailable, snapshot.Source("Nodes").State)
	require.True(t, snapshot.Partial())
	select {
	case <-nodeCanceled:
	case <-time.After(time.Second):
		t.Fatal("canceled read still retained its HTTP connection")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, paths, 7)
	for _, path := range paths {
		require.NotContains(t, path, "secrets")
	}
}
