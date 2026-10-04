// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package operator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func fixture() (scope *provider.Scope, cert, issuer *unstructured.Unstructured) {
	scope = &provider.Scope{Context: "chosen", Namespace: "apps", TargetNamespace: "apps", GVR: CertificateGVR, Name: "tls", UID: "cert-uid"}
	cert = &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/" + Version, "kind": CertificateKind,
		"metadata": map[string]any{"namespace": "apps", "name": "tls", "uid": "cert-uid", "generation": int64(5), "resourceVersion": "22", "annotations": map[string]any{"credential": "secret-sentinel"}},
		"spec":     map[string]any{"issuerRef": map[string]any{"name": "ca"}, "secretName": "tls"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(5), "message": "secret-sentinel"}, map[string]any{"type": "Issuing", "status": "False", "observedGeneration": int64(5)}}, "notAfter": "2026-12-01T12:00:00Z", "revision": int64(2), "message": "secret-sentinel", "acme": map[string]any{"explanationURL": "https://secret-sentinel.invalid/token"}}}}
	issuer = &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/" + Version, "kind": "Issuer", "metadata": map[string]any{"namespace": "apps", "name": "ca", "uid": "issuer-uid", "resourceVersion": "3"}, "spec": map[string]any{"secret": "secret-sentinel"}}}
	return scope, cert, issuer
}
func TestOperatorExactProjectionPolarityAndNamedRelations(t *testing.T) {
	scope, cert, issuer := fixture()
	reader := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cert, issuer)
	report := Collect(t.Context(), reader, scope, true, time.Now())
	require.Len(t, reader.Actions(), 2)
	for _, action := range reader.Actions() {
		require.Equal(t, "get", action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
	require.Equal(t, "controller reports ready", report.Conditions[0].Meaning)
	require.Equal(t, "controller reports not issuing", report.Conditions[1].Meaning)
	require.Equal(t, "controller report", report.State())
	require.False(t, report.CollectionFailed())
	require.Equal(t, "issuer-uid", report.Issuer.Observed.UID)
	require.False(t, report.Issuer.UIDLinkVerified)
	require.True(t, report.Issuer.Declared.Defaulted)
	require.True(t, report.CustomJump)
	require.NotContains(t, fmt.Sprintf("%+v", report), "secret-sentinel")
	for tab := range Tabs {
		require.NotContains(t, report.Render(tab), "secret-sentinel")
	}
	require.Contains(t, report.Render(0), "not independent live TLS")
	require.Contains(t, report.Render(2), "configured custom jump")
}
func TestOperatorObservedGenerationAndConditionPolarity(t *testing.T) {
	for _, test := range []struct {
		kind, status string
		observed     *int64
		want         string
	}{{"Ready", "False", ptr(5), "controller reports not ready"}, {"Issuing", "False", ptr(5), "controller reports not issuing"}, {"Issuing", "True", ptr(5), "controller reports issuance required"}, {"Ready", "True", ptr(4), Stale}, {"Ready", "True", nil, "unknown generation"}, {"Ready", "True", ptr(6), "unknown future generation"}, {"Ready", "Unknown", ptr(5), Unknown}} {
		condition := &Condition{Type: test.kind, Status: test.status, ObservedGeneration: test.observed}
		require.Equal(t, test.want, conditionMeaning(condition, ptr(5)))
	}
}
func ptr(n int64) *int64 { return &n }
func TestOperatorSchemaChangesIdentityAndConditionCap(t *testing.T) {
	const wrongGVK = "gvk"
	for _, variant := range []string{"uid", wrongGVK, "condition schema", "condition cap", "generation schema", "issuer schema"} {
		t.Run(variant, func(t *testing.T) {
			scope, cert, issuer := fixture()
			switch variant {
			case "uid":
				cert.SetUID("changed")
			case wrongGVK:
				cert.SetAPIVersion(Group + "/v1beta1")
			case "condition schema":
				require.NoError(t, unstructured.SetNestedMap(cert.Object, map[string]any{"Ready": "True"}, "status", "conditions"))
			case "condition cap":
				conditions := make([]any, MaxConditions+10)
				for i := range conditions {
					conditions[i] = map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(5)}
				}
				require.NoError(t, unstructured.SetNestedSlice(cert.Object, conditions, "status", "conditions"))
			case "generation schema":
				cert.Object["metadata"].(map[string]any)["generation"] = "5"
			case "issuer schema":
				cert.Object["spec"].(map[string]any)["issuerRef"].(map[string]any)["kind"] = int64(1)
			}
			reader := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cert, issuer)
			if variant == wrongGVK {
				reader.PrependReactor("get", "certificates", func(ktesting.Action) (bool, runtime.Object, error) { return true, cert, nil })
			}
			report := Collect(t.Context(), reader, scope, false, time.Now())
			require.LessOrEqual(t, len(report.Conditions), MaxConditions)
			if variant == "uid" || variant == wrongGVK {
				require.Empty(t, report.Conditions)
				require.True(t, report.CollectionFailed())
				require.Equal(t, "unsupported schema or identity", report.Coverage[0].State)
				require.Len(t, reader.Actions(), 1)
			} else if variant == "issuer schema" {
				require.Equal(t, "unsupported schema", report.Issuer.Coverage.State)
				require.Len(t, reader.Actions(), 1)
			} else {
				require.Positive(t, report.SchemaIssues)
				require.Equal(t, "partial schema", report.State())
			}
		})
	}
}
func TestOperatorDeniedRelationsAndUnsupportedVersionsKeepFallback(t *testing.T) {
	scope, cert, _ := fixture()
	reader := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cert)
	reader.PrependReactor("get", "issuers", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: Group, Resource: "issuers"}, "ca", fmt.Errorf("secret-sentinel"))
	})
	report := Collect(t.Context(), reader, scope, false, time.Now())
	require.Equal(t, "denied", report.Issuer.Coverage.State)
	require.Equal(t, "partial relation", report.State())
	require.NotContains(t, report.Render(3), "secret-sentinel")
	for _, gvr := range []string{Group + "/v1beta1/certificates", "example.io/v1/certificates", Group + "/v1/issuers"} {
		scope.GVR = gvr
		reader.ClearActions()
		report = Collect(t.Context(), reader, scope, false, time.Now())
		require.Empty(t, reader.Actions())
		require.Equal(t, Unsupported, report.State())
		require.Len(t, report.Actions, 2)
		require.Contains(t, report.Render(2), "generic YAML")
	}
}
func TestOperatorStrictNamed404AndHTTPAdapterCancellation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: Group, Version: Version, Resource: CertificateResource}
	require.True(t, NamedAbsent(apierrors.NewNotFound(gvr.GroupResource(), "tls"), gvr, "tls"))
	require.False(t, NamedAbsent(apierrors.NewNotFound(gvr.GroupResource(), "other"), gvr, "tls"))
	require.False(t, NamedAbsent(&apierrors.StatusError{ErrStatus: metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound}}, gvr, "tls"))
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			started := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				assert.Equal(t, "/apis/cert-manager.io/v1/namespaces/apps/certificates/tls", r.URL.Path)
				started <- struct{}{}
				if cancelRead {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, err := w.Write([]byte("proxy 404 secret-sentinel"))
				assert.NoError(t, err)
			}))
			defer server.Close()
			reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
			require.NoError(t, err)
			scope, _, _ := fixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *Report, 1)
			go func() { done <- Collect(ctx, reader, scope, false, time.Now()) }()
			if cancelRead {
				<-started
				cancel()
			}
			select {
			case report := <-done:
				require.Equal(t, Unknown, report.State())
				require.NotContains(t, report.Render(3), "secret-sentinel")
			case <-time.After(time.Second):
				t.Fatal("HTTP adapter cancellation blocked")
			}
		})
	}
}
func TestOperatorUnknownConditionPolarityIsNeverFault(t *testing.T) {
	scope, cert, issuer := fixture()
	require.NoError(t, unstructured.SetNestedSlice(cert.Object, []any{map[string]any{"type": "Paused", "status": "False", "message": "secret-sentinel"}}, "status", "conditions"))
	report := Collect(t.Context(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cert, issuer), scope, false, time.Now())
	require.Empty(t, report.Conditions)
	require.Equal(t, 1, report.UnsupportedConditions)
	require.Equal(t, Unknown, report.State())
	require.NotContains(t, strings.ToLower(report.Render(0)), "fault")
}
