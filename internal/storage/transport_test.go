// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestRealSourcesRespectAllBoundsAndCancelStalledCSIWithoutLosingClaims(t *testing.T) {
	scope, objects := fixture(t)
	started, canceled := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var requests []string
	var failures []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		if r.URL.Query().Get("limit") != "101" {
			failures = append(failures, "unbounded query: "+r.URL.String())
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/csidrivers") {
			close(started)
			<-r.Context().Done()
			close(canceled)
			return
		}
		var source *sourceSpec
		for i := range sources {
			if strings.HasSuffix(r.URL.Path, "/"+sources[i].gvr.Resource) {
				source = &sources[i]
				break
			}
		}
		if source == nil {
			http.Error(w, "Unexpected fixture path", 404)
			return
		}
		kinds := map[string]string{SourcePods: "Pod", SourcePVCs: "PersistentVolumeClaim", SourcePVs: "PersistentVolume", SourceClasses: "StorageClass", SourceCSINodes: "CSINode", SourceAttachments: "VolumeAttachment", SourceEvents: "Event"}
		kind := kinds[source.name]
		items := []any{}
		for _, object := range objects {
			obj := object.(*unstructured.Unstructured)
			if obj.GetKind() == kind {
				items = append(items, obj.Object)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"apiVersion": source.gvr.GroupVersion().String(), "kind": kind + "List", "metadata": map[string]any{}, "items": items}); err != nil {
			t.Errorf("fixture reply: %v", err)
		}
	}))
	defer server.Close()
	reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	results := make(chan *Snapshot, 1)
	go func() { results <- Collect(ctx, reader, scope, time.Now()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("CSI read did not begin")
	}
	time.AfterFunc(100*time.Millisecond, cancel)
	var snapshot *Snapshot
	select {
	case snapshot = <-results:
	case <-time.After(time.Second):
		t.Fatal("Collection ignored cancellation")
	}
	require.Len(t, snapshot.PVCs, 1)
	require.Len(t, snapshot.PVs, 1)
	require.Equal(t, capacity.Unavailable, snapshot.Source(SourceDrivers).State)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Canceled CSI HTTP connection remains active")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, failures)
	require.Len(t, requests, 8)
	for _, path := range requests {
		require.NotContains(t, path, "secrets", fmt.Sprint(requests))
	}
}
