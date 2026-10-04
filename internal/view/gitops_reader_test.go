// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const gitopsReaderAbsentCase = "API not served"
const gitopsDiscoveryTestVersion = "v1"
const gitopsProxyNotFoundCase = "plain proxy 404"

func nativeGitOpsReader(t *testing.T, handler http.Handler) *gitopsReader {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	configuration := &rest.Config{Host: server.URL}
	typed, err := kubernetes.NewForConfig(configuration)
	require.NoError(t, err)
	dyn, err := dynamic.NewForConfig(configuration)
	require.NoError(t, err)
	return &gitopsReader{dynamic: dyn, discovery: typed.Discovery().RESTClient()}
}

func writeGitOpsJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestGitOpsNativeReaderOnlyRequestsExplicitAPIAndNamedResource(t *testing.T) {
	var requests []string
	var lock sync.Mutex
	reader := nativeGitOpsReader(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		lock.Unlock()
		switch r.URL.Path {
		case "/apis/source.toolkit.fluxcd.io":
			writeGitOpsJSON(t, w, metav1.APIGroup{TypeMeta: metav1.TypeMeta{Kind: "APIGroup", APIVersion: gitopsDiscoveryTestVersion}, Name: "source.toolkit.fluxcd.io",
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "source.toolkit.fluxcd.io/v1", Version: gitopsDiscoveryTestVersion},
				Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: "source.toolkit.fluxcd.io/v1", Version: gitopsDiscoveryTestVersion}}})
		case "/apis/source.toolkit.fluxcd.io/v1":
			writeGitOpsJSON(t, w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: gitopsDiscoveryTestVersion}, GroupVersion: "source.toolkit.fluxcd.io/v1",
				APIResources: []metav1.APIResource{{Name: "gitrepositories/status", Kind: "GitRepository", Namespaced: true, Verbs: []string{client.GetVerb}},
					{Name: "gitrepositories", Kind: "GitRepository", Namespaced: true, Verbs: []string{client.GetVerb}}}})
		case "/apis/source.toolkit.fluxcd.io/v1/namespaces/sources/gitrepositories/repo":
			writeGitOpsJSON(t, w, map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
				"metadata": map[string]any{"namespace": "sources", "name": "repo", "uid": "source-uid"}})
		default:
			http.NotFound(w, r)
		}
	}))
	resolved, err := reader.Resolve(t.Context(), "source.toolkit.fluxcd.io", "", "GitRepository")
	require.NoError(t, err)
	require.Equal(t, schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: gitopsDiscoveryTestVersion, Resource: "gitrepositories"}, resolved.GVR)
	require.True(t, resolved.Namespaced)
	_, err = reader.Resolve(t.Context(), "source.toolkit.fluxcd.io", gitopsDiscoveryTestVersion, "GitRepository")
	require.NoError(t, err)
	object, err := reader.Get(t.Context(), resolved.GVR, "sources", "repo")
	require.NoError(t, err)
	require.Equal(t, "source-uid", string(object.GetUID()))
	_, err = reader.Resolve(t.Context(), "", gitopsDiscoveryTestVersion, "Secret")
	require.Error(t, err)
	_, err = reader.Get(t.Context(), schema.GroupVersionResource{Version: gitopsDiscoveryTestVersion, Resource: "secrets"}, "sources", "credential")
	require.Error(t, err)
	lock.Lock()
	defer lock.Unlock()
	require.Equal(t, []string{"GET /apis/source.toolkit.fluxcd.io", "GET /apis/source.toolkit.fluxcd.io/v1",
		"GET /apis/source.toolkit.fluxcd.io/v1/namespaces/sources/gitrepositories/repo"}, requests)
}

func TestGitOpsNativeReaderDoesNotSubstituteAdvertisedVersionsOrKinds(t *testing.T) {
	for _, scenario := range []string{gitopsReaderAbsentCase, "denied", "version", "ambiguous", "no-get", "preferred"} {
		t.Run(scenario, func(t *testing.T) {
			reader := nativeGitOpsReader(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if scenario == gitopsReaderAbsentCase {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					writeGitOpsJSON(t, w, &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: gitopsDiscoveryTestVersion},
						Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound})
					return
				}
				if scenario == "denied" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					writeGitOpsJSON(t, w, &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: gitopsDiscoveryTestVersion},
						Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden})
					return
				}
				if scenario == "preferred" {
					writeGitOpsJSON(t, w, metav1.APIGroup{TypeMeta: metav1.TypeMeta{Kind: "APIGroup", APIVersion: gitopsDiscoveryTestVersion}, Name: "apps",
						PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "apps/v9", Version: "v9"},
						Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: "apps/v1", Version: gitopsDiscoveryTestVersion}}})
					return
				}
				resources := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: gitopsDiscoveryTestVersion}, GroupVersion: "apps/v1",
					APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: []string{client.GetVerb}}}}
				switch scenario {
				case "version":
					resources.GroupVersion = "apps/v2"
				case "ambiguous":
					resources.APIResources = append(resources.APIResources, metav1.APIResource{Name: "otherdeployments", Kind: "Deployment", Verbs: []string{client.GetVerb}})
				case "no-get":
					resources.APIResources[0].Verbs = []string{"list"}
				}
				writeGitOpsJSON(t, w, resources)
			}))
			version := gitopsDiscoveryTestVersion
			if scenario == "preferred" {
				version = ""
			}
			_, err := reader.Resolve(t.Context(), "apps", version, "Deployment")
			require.Error(t, err)
			if scenario == gitopsReaderAbsentCase || scenario == "no-get" {
				require.ErrorIs(t, err, gitops.ErrAPIAbsent)
			}
			if scenario == "denied" {
				require.True(t, apierrors.IsForbidden(err))
			}
		})
	}
}

func TestGitOpsNativeNamedAbsenceRequiresVerifiedStatusIdentity(t *testing.T) {
	for _, scenario := range []string{gitopsProxyNotFoundCase, "wrong identity", "verified missing"} {
		t.Run(scenario, func(t *testing.T) {
			reader := nativeGitOpsReader(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if scenario == gitopsProxyNotFoundCase {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				status := apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "selected").Status()
				status.Kind, status.APIVersion = "Status", gitopsDiscoveryTestVersion
				if scenario == "wrong identity" {
					status.Details.Name = "different-resource"
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				writeGitOpsJSON(t, w, status)
			}))
			request := &gitops.Request{Target: inspect.ResourceIdentity{Context: "captured", GVR: "v1/pods", Namespace: "team", Name: "selected", UID: "selected-uid"}}
			snapshot, err := gitops.Collect(t.Context(), reader, request)
			require.NoError(t, err)
			require.Empty(t, snapshot.Nodes)
			require.Len(t, snapshot.Coverage, 1)
			if scenario == "verified missing" {
				require.Equal(t, gitops.Missing, snapshot.Coverage[0].State)
			} else {
				require.Equal(t, gitops.Unknown, snapshot.Coverage[0].State)
			}
			_, err = reader.Resolve(t.Context(), "argoproj.io", "v1alpha1", "Application")
			require.Error(t, err)
			require.NotErrorIs(t, err, gitops.ErrAPIAbsent, "named-object 404 must not establish absence of the requested API")
		})
	}
}

func TestGitOpsNativeHTTPReadHonorsCancellation(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	reader := nativeGitOpsReader(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := reader.Resolve(ctx, "argoproj.io", "v1alpha1", "Application")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native API read did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("native API transport did not release canceled read")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("native HTTP request remained open after cancellation")
	}
}
