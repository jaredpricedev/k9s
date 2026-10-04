// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package configreview

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
)

func TestSecretMetadataNegotiatesStrictlyAndDiscardsAnnotationValues(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/namespaces/apps/secrets/auth", r.URL.Path)
		assert.Equal(t, metadataAccept, r.Header.Get("Accept"))
		assert.Empty(t, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata",`+
			`"metadata":{"name":"auth","namespace":"apps","uid":"secret-uid","resourceVersion":"3",`+
			`"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"PRIVATE-CONTENT"}}}`)
	}))
	defer server.Close()
	reader, err := NewSecretMetadataReader(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	meta, err := reader(t.Context(), "apps", "auth")
	require.NoError(t, err)
	require.Equal(t, "secret-uid", string(meta.UID))
	require.Equal(t, "3", meta.ResourceVersion)
	require.Equal(t, map[string]string{"kubectl.kubernetes.io/last-applied-configuration": ""}, meta.Annotations)
	require.NotContains(t, fmt.Sprintf("%+v", meta), "PRIVATE-CONTENT")
	require.EqualValues(t, 1, requests.Load())
}

func TestSecretMetadataNeverFallsBackAndDoesNotExposeResponseBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unsupported", http.StatusNotAcceptable, "PRIVATE-CONTENT"},
		{"denied", http.StatusForbidden, "PRIVATE-CONTENT"},
		{"missing", http.StatusNotFound, `{"apiVersion":"v1","kind":"Status","code":404,"reason":"NotFound","details":{"name":"auth","kind":"secrets"},"message":"PRIVATE-CONTENT"}`},
		{"ambiguous-404", http.StatusNotFound, "PRIVATE-CONTENT"},
		{"full-object", http.StatusOK, `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"auth","namespace":"apps","uid":"uid"},"data":{"token":"PRIVATE-CONTENT"}}`},
		{"wrong-identity", http.StatusOK, `{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"other","namespace":"apps","uid":"uid"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, metadataAccept, r.Header.Get("Accept"))
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			reader, err := NewSecretMetadataReader(&rest.Config{Host: server.URL})
			require.NoError(t, err)
			meta, err := reader(t.Context(), "apps", "auth")
			require.Error(t, err)
			require.Nil(t, meta)
			require.NotContains(t, err.Error(), "PRIVATE-CONTENT")
			require.Equal(t, tc.status == http.StatusForbidden, apierrors.IsForbidden(err))
			require.Equal(t, tc.name == "missing", apierrors.IsNotFound(err))
			require.EqualValues(t, 1, requests.Load(), "no retry with ordinary JSON or full-object GET")
		})
	}
}

func TestSecretMetadataCancellationBoundsActualHTTPRead(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	reader, err := NewSecretMetadataReader(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := reader(ctx, "apps", "auth"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("metadata read ignored cancellation")
	}
}
