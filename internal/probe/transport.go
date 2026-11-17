// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package probe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
)

var (
	statusKind    = regexp.MustCompile(`"kind"\s*:\s*"Status"`)
	statusFailure = regexp.MustCompile(`"status"\s*:\s*"Failure"`)
)

// APITransport sends probe requests through the Kubernetes API server Pod
// proxy with the credentials of the app's own connection. It holds no
// token: authentication lives in the round tripper client-go builds.
type APITransport struct {
	base   *url.URL
	client *http.Client
}

// NewAPITransport builds the transport from the active REST configuration.
// It contacts nothing.
func NewAPITransport(cfg *rest.Config) (*APITransport, error) {
	if cfg == nil {
		return nil, errors.New("no Kubernetes connection")
	}
	host := cfg.Host
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	base, err := url.Parse(host)
	if err != nil || base.Hostname() == "" {
		return nil, errors.New("Kubernetes API server address is unusable")
	}
	roundTripper, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, errors.New("Kubernetes credentials are unusable")
	}
	base.User, base.RawQuery, base.Fragment = nil, "", ""
	base.Path = strings.TrimRight(base.Path, "/")
	return &APITransport{base: base, client: &http.Client{
		Transport: roundTripper,
		// A redirect from the application is its answer, not a place to go.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// url is the API server URL of one request. The declared path keeps its own
// escaping and query.
func (t *APITransport) url(request *Request) (string, error) {
	declared, err := url.Parse(request.Path)
	if err != nil || !usablePath(request.Path) {
		return "", errors.New("declared path refused")
	}
	prefix := t.base.Path + "/api/v1/namespaces/" + url.PathEscape(request.Namespace) + "/pods/" + url.PathEscape(request.ProxyTarget()) + "/proxy"
	u := *t.base
	u.Path = prefix + declared.Path
	if escaped := declared.EscapedPath(); escaped != declared.Path {
		u.RawPath = prefix + escaped
	}
	u.RawQuery = declared.RawQuery
	return u.String(), nil
}

// reachDetail turns an API server failure message into one of a few fixed
// phrases, so that no response text is retained or shown.
func reachDetail(body []byte) string {
	lower := bytes.ToLower(body)
	switch {
	case bytes.Contains(lower, []byte("connection refused")):
		return "connection refused"
	case bytes.Contains(lower, []byte("no route to host")):
		return "no route to host"
	case bytes.Contains(lower, []byte("timeout")) || bytes.Contains(lower, []byte("timed out")) || bytes.Contains(lower, []byte("deadline")):
		return "the API server timed out reaching the Pod"
	case bytes.Contains(lower, []byte("eof")) || bytes.Contains(lower, []byte("reset")):
		return "connection closed by the Pod"
	}
	return "the API server could not reach the Pod"
}

// classify separates an answer of the Pod from an answer of the API server
// about the Pod. The API server reports its own failures as a Status
// object; an application's 403 or 503 is an answer like any other.
func classify(code int, body []byte) (outcome Outcome, detail string) {
	if code < 400 || !statusKind.Match(body) || !statusFailure.Match(body) {
		return Answered, ""
	}
	switch code {
	case http.StatusForbidden:
		return Denied, "forbidden"
	case http.StatusUnauthorized:
		return Denied, "not authenticated"
	case http.StatusNotFound:
		return Gone, "the Pod is no longer there"
	}
	return Unreachable, reachDetail(body)
}

// Get sends one GET with no body and no header of its own, and measures
// the time from the request being written to the first byte of the answer.
// Connection set-up is not part of that time. At most MaxResponseBytes of
// the body are read, only to tell an API server failure from an answer of
// the Pod, and then dropped.
func (t *APITransport) Get(ctx context.Context, request *Request) Response {
	target, err := t.url(request)
	if err != nil {
		return Response{Outcome: Failed, Detail: "request refused before sending"}
	}
	var (
		mu           sync.Mutex
		wrote, first time.Time
	)
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			mu.Lock()
			wrote = time.Now()
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			if first.IsZero() {
				first = time.Now()
			}
			mu.Unlock()
		},
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, target, http.NoBody)
	if err != nil {
		return Response{Outcome: Failed, Detail: "request could not be built"}
	}
	started := time.Now()
	resp, err := t.client.Do(req)
	answered := time.Now()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Response{Outcome: TimedOut, Took: answered.Sub(started), Detail: "no answer within the deadline"}
		}
		return Response{Outcome: Failed, Took: answered.Sub(started), Detail: "no response from the API server"}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	_ = resp.Body.Close()
	mu.Lock()
	took := answered.Sub(started)
	if !wrote.IsZero() && !first.IsZero() && first.After(wrote) {
		took = first.Sub(wrote)
	}
	mu.Unlock()
	outcome, detail := classify(resp.StatusCode, body)
	return Response{Outcome: outcome, Code: resp.StatusCode, Took: took, Detail: detail}
}

// Close releases idle connections.
func (t *APITransport) Close() {
	if t != nil && t.client != nil {
		t.client.CloseIdleConnections()
	}
}
