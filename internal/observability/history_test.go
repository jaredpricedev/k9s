// SPDX-License-Identifier: Apache-2.0
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func request(url string) Request {
	return Request{Scope: provider.Scope{Context: "selected", GVR: "v1/pods", TargetNamespace: "apps", Name: "api", UID: "selected-uid"}, URL: url, Start: time.Unix(1000, 0).UTC(), End: time.Unix(1060, 0).UTC()}
}
func TestRangeRequestsRetainOnlyTypedScopedFiniteHistory(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/api/v1/query_range", r.URL.Path)
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Query().Get("query"), `{namespace="apps",pod="api"}`)
		assert.Equal(t, "15", r.URL.Query().Get("step"))
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"namespace":"apps","pod":"api","container":"web","credential":"SECRET"},"values":[[1000,"1"],[1015,"NaN"],[2000,"7"],[1060,"2"]]},{"metric":{"namespace":"other","pod":"api"},"values":[[1000,"3"]]}]}}`)
	}))
	defer server.Close()
	s, err := Collect(t.Context(), request(server.URL))
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Len(t, s.Evidence, 2)
	require.Len(t, s.Evidence[0].Series, 1)
	require.Len(t, s.Evidence[0].Series[0].Samples, 2)
	require.Contains(t, s.Evidence[0].State, "partial")
	require.True(t, s.Evidence[0].WindowMismatch)
	require.True(t, s.Evidence[0].TargetMismatch)
	require.Greater(t, s.Evidence[0].Gaps, 2)
	encoded, err := json.Marshal(s)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SECRET")
	require.NotContains(t, string(encoded), "credential")
}
func TestURLScopeWindowAndLiteralBounds(t *testing.T) {
	for _, url := range []string{"https://user:secret@example.com", "https://example.com?token=secret", "https://example.com#secret", "https://example.com?", "file:///tmp/secret", strings.Repeat("x", 2049)} {
		r := request(url)
		require.Error(t, Validate(&r))
	}
	r := request("https://example.com/prom")
	r.Scope.GVR = "v1/secrets"
	require.Error(t, Validate(&r))
	r = request("https://example.com")
	r.End = r.Start.Add(25 * time.Hour)
	require.Error(t, Validate(&r))
	r = request("https://example.com")
	r.Start = time.Date(2600, 1, 1, 0, 0, 0, 0, time.UTC)
	r.End = r.Start.Add(time.Hour)
	require.ErrorContains(t, Validate(&r), "timestamp range")
	r = request("https://example.com")
	r.Scope.Name = `a"b\c`
	require.Contains(t, Queries(&r)[0], `pod="a\"b\\c"`)
}
func TestProviderStatesAndNoRedirectContact(t *testing.T) {
	contacted := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted++ }))
	defer destination.Close()
	for _, status := range []int{401, 403, 404, 302, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, "SECRET provider error")
			}))
			defer server.Close()
			s, err := Collect(t.Context(), request(server.URL))
			require.NoError(t, err)
			require.Len(t, s.Evidence, 2)
			require.NotEqual(t, "partial", s.Evidence[0].State)
		})
	}
	require.Zero(t, contacted)
}
func TestRequestCancellationReachesHTTPHandler(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(canceled) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := Collect(ctx, request(server.URL)); done <- err }()
	<-entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP request not canceled")
	}
}
func TestRangeResponseLimitsAndUnsupportedType(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", MaxBytes+1), `{"status":"success","data":{"resultType":"vector","result":[]}}`, `{"status":"error","error":"SECRET"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
		_, err := Collect(t.Context(), request(server.URL))
		server.Close()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "SECRET")
	}
	for _, limit := range []string{"series", "samples"} {
		t.Run(limit, func(t *testing.T) {
			values := `[[1000,"1"]]`
			n := MaxSeries + 1
			if limit == "samples" {
				n = 1
				values = "[" + strings.TrimSuffix(strings.Repeat(`[1000,"1"],`, MaxSamples+1), ",") + "]"
			}
			series := fmt.Sprintf(`{"metric":{"namespace":"apps","pod":"api"},"values":%s}`, values)
			body := `{"status":"success","data":{"resultType":"matrix","result":[` + strings.TrimSuffix(strings.Repeat(series+",", n), ",") + `]}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			_, err := Collect(t.Context(), request(server.URL))
			require.ErrorContains(t, err, limit)
		})
	}
}

func TestRangeDeadlineReachesHTTPHandler(t *testing.T) {
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(canceled) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := Collect(ctx, request(server.URL))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("deadline did not cancel HTTP request")
	}
}
