// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package configreview

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestConfigMapHTTP404RequiresVerifiedNamedObjectStatus(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, state, optionalState string
	}{
		{"plain", "PRIVATE-HTTP-BODY", "text/plain", Unknown, Unknown},
		{"proxy", "<html>PRIVATE-HTTP-BODY</html>", "text/html", Unknown, Unknown},
		{"unnamed-status", `{"apiVersion":"v1","kind":"Status","status":"Failure","code":404,"reason":"NotFound"}`,
			"application/json", Unknown, Unknown},
		{"other-name", configMapNotFoundBody("other", "configmaps", ""), "application/json", Unknown, Unknown},
		{"other-resource", configMapNotFoundBody("settings", "secrets", ""), "application/json", Unknown, Unknown},
		{"other-group", configMapNotFoundBody("settings", "configmaps", "custom.example"), "application/json", Unknown, Unknown},
		{"named-object", configMapNotFoundBody("settings", "configmaps", ""), "application/json", Missing, "optional reference missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readers, scope, requests := configurationHTTP404Readers(t, tc.body, tc.contentType)
			snapshot, err := Collect(t.Context(), readers, scope)
			require.NoError(t, err)
			require.Len(t, snapshot.References, 2)
			require.Len(t, snapshot.Objects, 1)
			require.Equal(t, tc.state, snapshot.Objects[0].State)
			require.Equal(t, tc.state, snapshot.State(&snapshot.References[0]))
			require.Equal(t, tc.optionalState, snapshot.State(&snapshot.References[1]))
			require.NotContains(t, snapshot.ReferenceText()+snapshot.Evidence(), "PRIVATE-HTTP-BODY")
			require.EqualValues(t, 10, requests.Load(), "one source GET, eight consumer LISTs and one named ConfigMap GET")
		})
	}
}

func configMapNotFoundBody(name, kind, group string) string {
	return fmt.Sprintf(`{"apiVersion":"v1","kind":"Status","status":"Failure","code":404,"reason":"NotFound",`+
		`"details":{"name":%q,"kind":%q,"group":%q},"message":"PRIVATE-HTTP-BODY"}`, name, kind, group)
}

func configurationHTTP404Readers(t *testing.T, body, contentType string) (*Readers, *Scope, *atomic.Int32) {
	t.Helper()
	listPaths := make(map[string]resource, len(consumerResources))
	for _, r := range consumerResources {
		base := "/api/" + r.gvr.Version
		if r.gvr.Group != "" {
			base = "/apis/" + r.gvr.GroupVersion().String()
		}
		listPaths[base+"/namespaces/apps/"+r.gvr.Resource] = r
	}
	requests := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces/apps/pods/api":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"apps","name":"api","uid":"pod-uid"},`+
				`"spec":{"containers":[{"name":"api","env":[`+
				`{"name":"REQUIRED","valueFrom":{"configMapKeyRef":{"name":"settings","key":"url"}}},`+
				`{"name":"OPTIONAL","valueFrom":{"configMapKeyRef":{"name":"settings","key":"url","optional":true}}}]}]}}`)
		case "/api/v1/namespaces/apps/configmaps/settings":
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, body)
		default:
			listed, exists := listPaths[r.URL.Path]
			assert.True(t, exists, "unexpected API request: %s", r.URL.Path)
			assert.Equal(t, "101", r.URL.Query().Get("limit"))
			_, _ = fmt.Fprintf(w, `{"apiVersion":%q,"kind":%q,"metadata":{},"items":[]}`,
				listed.gvr.GroupVersion().String(), listed.kind+"List")
		}
	}))
	t.Cleanup(server.Close)
	objects, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	scope := &Scope{Identity: Identity{ResourceIdentity: inspect.ResourceIdentity{
		Context: "demo", GVR: "v1/pods", Namespace: "apps", Name: "api", UID: "pod-uid"}}}
	return &Readers{Objects: objects}, scope, requests
}
