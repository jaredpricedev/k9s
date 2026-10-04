// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package dao

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type helmRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn helmRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

const (
	guardedHelmTestNamespace = "ns"
	guardedHelmTestOriginal  = "original"
)

func TestGuardedHelmObserverRecordsOnlyAcknowledgedWrites(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		var observed []bool
		ctx := WithHelmOperationObserver(t.Context(), func(method, path string, accepted bool) {
			if method != http.MethodPatch || path != "/apis/apps/v1/namespaces/ns/deployments/app" {
				t.Fatal(method, path)
			}
			observed = append(observed, accepted)
		})
		transport := helmOperationTransport{ctx: ctx, base: helmRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("sensitive response body"))}, nil
		})}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, "https://api.example/apis/apps/v1/namespaces/ns/deployments/app", strings.NewReader("secret input"))
		if err != nil {
			t.Fatal(err)
		}
		response, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if len(observed) == 0 || observed[0] || (status == 200 && (len(observed) != 2 || !observed[1])) || (status == 403 && len(observed) != 1) {
			t.Fatal(status, observed)
		}
		observed = nil
		req.Method = http.MethodGet
		response, err = transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if len(observed) != 0 {
			t.Fatal("read counted as write", observed)
		}
	}
}

func TestGuardedHelmKeepsNativeRequestCancellation(t *testing.T) {
	reqCtx, cancel := context.WithCancel(t.Context())
	cancel()
	transport := helmOperationTransport{ctx: t.Context(), base: helmRoundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, req.Context().Err() })}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://api.example", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, requestErr := transport.RoundTrip(req)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(requestErr, context.Canceled) {
		t.Fatal("native cancellation was replaced", requestErr)
	}
}

func TestGuardedHelmFingerprintTracksProviderContentWithoutRetainingValues(t *testing.T) {
	revision := &release.Release{Name: "app", Namespace: guardedHelmTestNamespace, Version: 3, Config: map[string]any{"private": "sensitive-value"}, Manifest: guardedHelmTestOriginal}
	before, err := HelmRevisionFingerprint(revision)
	if err != nil {
		t.Fatal(err)
	}
	revision.Manifest = "replacement"
	after, err := HelmRevisionFingerprint(revision)
	if err != nil || before == after || !strings.HasPrefix(after, "sha256:") || strings.Contains(after, "sensitive-value") {
		t.Fatal(before, after, err)
	}
	if _, err := HelmRevisionFingerprint(nil); err == nil {
		t.Fatal("unknown provider identity accepted")
	}
}

func TestGuardedHelmRejectsUnboundedSQLStorageBeforeIO(t *testing.T) {
	t.Setenv("HELM_DRIVER", "sql")
	if _, err := ensureHelmOperationConfig(t.Context(), genericclioptions.NewConfigFlags(false), guardedHelmTestNamespace); err == nil || !strings.Contains(err.Error(), "cancellation") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ensureHelmOperationConfig(ctx, nil, guardedHelmTestNamespace); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
