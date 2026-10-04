// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dao

import (
	"errors"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMetadataSessionRetainsLookupDuringPendingDiscoveryAndRejectsLatePublish(t *testing.T) {
	meta := NewMeta()
	meta.RegisterMeta(client.PodGVR.String(), &metav1.APIResource{Name: "retained-pods"})
	started, release, complete := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		complete <- meta.collectResources(func(resources ResourceMetas) error {
			resources[client.PodGVR] = &metav1.APIResource{Name: "old-session-pods"}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	lookup := make(chan string, 1)
	go func() {
		resource, err := meta.MetaFor(client.PodGVR)
		if err == nil {
			lookup <- resource.Name
		}
	}()
	select {
	case name := <-lookup:
		require.Equal(t, "retained-pods", name)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("pending discovery blocked retained metadata lookup")
	}
	meta.InvalidateRequests()
	require.NoError(t, meta.collectResources(func(resources ResourceMetas) error {
		resources[client.PodGVR] = &metav1.APIResource{Name: "fresh-session-pods"}
		return nil
	}))
	close(release)
	require.NoError(t, <-complete)
	resource, err := meta.MetaFor(client.PodGVR)
	require.NoError(t, err)
	require.Equal(t, "fresh-session-pods", resource.Name)
}

func TestMetadataSessionFailedDiscoveryKeepsUsableMetadata(t *testing.T) {
	meta := NewMeta()
	meta.RegisterMeta(client.PodGVR.String(), &metav1.APIResource{Name: "retained-pods"})
	require.Error(t, meta.collectResources(func(ResourceMetas) error { return errors.New("fixture discovery failed") }))
	resource, err := meta.MetaFor(client.PodGVR)
	require.NoError(t, err)
	require.Equal(t, "retained-pods", resource.Name)
}
