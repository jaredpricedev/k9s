// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestReviewScopeUsesVisibleWorkspaceAndCapturedUIDs(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	_, err := a.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	contextName := a.Config.ActiveContextName()
	store := workspace.Store{Version: 1, Active: "newer", Scopes: []workspace.Scope{
		{Name: "older", Context: contextName, Namespaces: []string{"apps"}, Kinds: []string{"deployments"}, LabelSelector: "team=api"},
		{Name: "newer", Context: contextName, Namespaces: []string{"other"}},
	}}
	w := newDailyWorkspace(a, store, dailyWorkspaceQueueMode, "")
	w.scope = store.Scopes[0]
	w.snapshot.Resources = []workspace.Resource{{Ref: workspace.ResourceRef{GVR: "apps/v1/deployments", Namespace: "apps", Name: "api", UID: "original"}}}
	a.Content.Push(w)
	scope, err := captureReviewScope(a)
	require.NoError(t, err)
	require.Equal(t, []string{"apps"}, scope.Namespaces)
	require.Equal(t, "apps", scope.DefaultNamespace)
	require.Equal(t, []string{"deployments"}, scope.Kinds)
	require.Equal(t, "team=api", scope.LabelSelector)
	require.EqualValues(t, "original", scope.CapturedUIDs[review.IdentityKey(client.DpGVR.GVR(), "apps", "api")])
	scope.Namespaces[0], scope.Kinds[0] = "mutated", client.PodGVR.R()
	require.Equal(t, "apps", w.scope.Namespaces[0])
	require.Equal(t, "deployments", w.scope.Kinds[0])
	w.scope.Context = "other-context"
	_, err = captureReviewScope(a)
	require.ErrorContains(t, err, "context changed")
}

func TestReviewScopeDoesNotInventNamespaceForAllNamespaceBrowser(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	_, err := a.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	require.NoError(t, a.Config.SetActiveNamespace(client.NamespaceAll))
	a.Content.Push(NewDetails(a, "test", "", contentTXT, false))
	scope, err := captureReviewScope(a)
	require.NoError(t, err)
	require.Empty(t, scope.DefaultNamespace)
	require.Empty(t, scope.Namespaces)
	require.NoError(t, a.Config.SetActiveNamespace("apps"))
	scope, err = captureReviewScope(a)
	require.NoError(t, err)
	require.Equal(t, "apps", scope.DefaultNamespace)
	require.Equal(t, []string{"apps"}, scope.Namespaces)
}

func TestReviewScopeUsesRetainedResourceNamespaceAndRejectsOtherContext(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	_, err := a.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	owner := &workspaceDiscoveryOwner{Details: NewDetails(a, "test", "", contentTXT, false),
		target: SelectedResourceTarget{Context: a.Config.ActiveContextName(), GVR: client.DpGVR, Namespace: "apps", Name: "api", UID: "original"}}
	a.Content.Push(owner)
	scope, err := captureReviewScope(a)
	require.NoError(t, err)
	require.Equal(t, []string{"apps"}, scope.Namespaces)
	require.Equal(t, "apps", scope.DefaultNamespace)
	owner.target.Context = "other-context"
	_, err = captureReviewScope(a)
	require.ErrorContains(t, err, "context changed")
}

func TestReviewResolverUsesExactVersionAndCancelablePinnedClient(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/apis/apps/v1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments/status", Kind: "Deployment", Namespaced: true},
			{Name: "deployments", Kind: "Deployment", Namespaced: true},
		}}))
	}))
	defer server.Close()
	typed, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	resolve := reviewResolver(inspectionConnection{typed: typed})
	mapping, err := resolve(t.Context(), "apps/v1", "Deployment")
	require.NoError(t, err)
	require.Equal(t, client.DpGVR.GVR(), mapping.GVR)
	require.True(t, mapping.Namespaced)
	_, err = resolve(t.Context(), "apps/v1", "Unknown")
	require.ErrorContains(t, err, "not served")
	require.Equal(t, []string{"GET /apis/apps/v1", "GET /apis/apps/v1"}, requests)
	_, err = reviewResolver(nil)(t.Context(), "apps/v1", "Deployment")
	require.ErrorContains(t, err, "unavailable")
}

func TestRolloutActionRequiresExactNativeIdentity(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	owner := &workspaceDiscoveryOwner{Details: NewDetails(a, "test", "", contentTXT, false), target: SelectedResourceTarget{Context: a.Config.ActiveContextName(), GVR: client.DpGVR, Namespace: "apps", Name: "api", UID: "captured"}}
	find := func() bool {
		for _, action := range changeReviewActions(owner, a) {
			if action.ID == "resource.rollout" {
				return action.Available()
			}
		}
		return false
	}
	require.True(t, find())
	owner.target.GVR = client.NewGVR("example.io/v1/deployments")
	require.False(t, find())
	owner.target.GVR, owner.target.UID = client.DpGVR, ""
	require.False(t, find())
}
