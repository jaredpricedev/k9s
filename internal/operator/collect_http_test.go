// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestOperatorHTTPEnumsMustBeExactProviderReports(t *testing.T) {
	for _, variant := range []struct{ kind, status string }{
		{ConditionReady, ConditionTrue + "\n"}, {ConditionReady, ConditionTrue + "\u0085"},
		{ConditionReady, ConditionTrue + "\u202e"}, {ConditionReady + "\n", ConditionTrue},
	} {
		t.Run(variant.kind+variant.status, func(t *testing.T) {
			scope, cert, issuer := fixture()
			require.NoError(t, unstructured.SetNestedSlice(cert.Object, []any{map[string]any{
				"type": variant.kind, "status": variant.status, "observedGeneration": int64(5), "message": "secret-sentinel",
			}}, "status", "conditions"))
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/apis/cert-manager.io/v1/namespaces/apps/certificates/tls":
					assert.NoError(t, json.NewEncoder(w).Encode(cert.Object))
				case "/apis/cert-manager.io/v1/namespaces/apps/issuers/ca":
					assert.NoError(t, json.NewEncoder(w).Encode(issuer.Object))
				default:
					t.Errorf("unexpected API contact: %s", r.URL.Path)
					http.Error(w, "unexpected", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
			require.NoError(t, err)
			report := Collect(t.Context(), reader, scope, false, time.Now())
			require.EqualValues(t, 2, calls.Load())
			require.NotEqual(t, ControllerReport, report.State())
			for _, condition := range report.Conditions {
				require.Equal(t, ConditionUnknown, condition.Status)
				require.Equal(t, Unknown, condition.Meaning)
			}
			encoded, err := json.Marshal(report)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "secret-sentinel")
		})
	}
}

func TestOperatorHTTPUnsafeCapturesRetainOnlyBoundedScopeWithoutContact(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	for _, variant := range []string{"context length", "context control", "uid length", "uid control", "unsupported gvr"} {
		t.Run(variant, func(t *testing.T) {
			scope, _, _ := fixture()
			switch variant {
			case "context length":
				scope.Context = strings.Repeat("x", MaxFieldBytes+1)
			case "context control":
				scope.Context += "\u202e"
			case "uid length":
				scope.UID = strings.Repeat("x", MaxFieldBytes+1)
			case "uid control":
				scope.UID += "\u0085"
			default:
				scope.GVR = strings.Repeat("x", MaxFieldBytes+1)
			}
			report := Collect(t.Context(), reader, scope, false, time.Now())
			require.Zero(t, calls.Load())
			require.True(t, report.CollectionFailed())
			require.LessOrEqual(t, len(report.Scope.Context), MaxFieldBytes)
			require.LessOrEqual(t, len(report.Scope.UID), MaxFieldBytes)
			require.LessOrEqual(t, len(report.Scope.GVR), MaxFieldBytes)
			for _, coverage := range report.Coverage {
				require.LessOrEqual(t, len(coverage.Source), MaxFieldBytes)
			}
		})
	}
	scope, _, _ := fixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report := Collect(ctx, reader, scope, false, time.Now())
	require.Zero(t, calls.Load())
	require.True(t, report.CollectionFailed())
}
