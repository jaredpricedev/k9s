// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package dao

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHelmOperationTransportCancelsWaitingRequest(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		close(entered)
		<-req.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cl := &http.Client{Transport: helmOperationTransport{ctx: ctx, base: http.DefaultTransport}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		response, requestErr := cl.Do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- requestErr
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	select {
	case requestErr := <-done:
		if !errors.Is(requestErr, context.DeadlineExceeded) {
			t.Fatal(requestErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Helm request exceeded operation deadline")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("server request did not receive cancellation")
	}
	response, err := cl.Do(req)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired operation sent a new request", err)
	}
}
