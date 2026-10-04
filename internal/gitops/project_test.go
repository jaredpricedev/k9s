// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func argoFixture() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": argoApplication,
		"metadata": map[string]any{"name": "delivery", "namespace": "control", "uid": "app-uid", "generation": int64(7)},
		"spec": map[string]any{"source": map[string]any{"repoURL": "https://user:private-repo-password@example.test/repo?token=private", "path": "apps", "targetRevision": "main",
			"helm": map[string]any{"values": "password: raw-secret-value"}}},
		"status": map[string]any{"sync": map[string]any{"status": "Synced", "revision": "commit-sha"}, "health": map[string]any{"status": "Healthy"},
			"conditions": []any{map[string]any{"type": "OrphanedResourceWarning", "message": "Secret password=raw-secret-value"}},
			"resources":  []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "apps", "name": "api", "status": "Synced"}}},
	}}
}

func TestArgoProjectionKeepsReportedStateSeparateAndExcludesValues(t *testing.T) {
	object := argoFixture()
	node := projectNode(object, &inspect.ResourceIdentity{Context: "ctx", GVR: "argoproj.io/v1alpha1/applications", Namespace: "control", Name: "delivery", UID: "app-uid"}, time.Now())
	require.Equal(t, "reported ready", node.State)
	require.Nil(t, node.ObservedGeneration)
	require.Equal(t, int64(7), *node.Generation)
	require.False(t, *node.Automated)
	require.Len(t, node.Resources, 1)
	encoded, err := json.Marshal(node)
	require.NoError(t, err)
	for _, excluded := range []string{"raw-secret-value", "private-repo-password", "token=private", "user:"} {
		require.NotContains(t, string(encoded), excluded)
	}
	require.Contains(t, string(encoded), "https://example.test/repo")
	require.Contains(t, string(encoded), "commit-sha")
	require.Contains(t, string(encoded), "application version unavailable")
	require.Contains(t, string(encoded), "does not establish ownership")
	require.Contains(t, string(encoded), "Free-form controller diagnostics omitted")
	require.Equal(t, "Secret password=raw-secret-value", object.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["message"])
}

func TestArgoAutomationDefaultsAndStatusPriority(t *testing.T) {
	for _, tc := range []struct {
		policy   map[string]any
		expected *bool
	}{
		{map[string]any{}, boolPointer(true)},
		{map[string]any{"enabled": nil}, boolPointer(true)},
		{map[string]any{"enabled": false}, boolPointer(false)},
		{map[string]any{"enabled": "wrong-type"}, nil},
	} {
		object := argoFixture()
		require.NoError(t, unstructured.SetNestedMap(object.Object, tc.policy, "spec", "syncPolicy", "automated"))
		require.Equal(t, tc.expected, argoAutomation(object))
	}
	object := argoFixture()
	require.NoError(t, unstructured.SetNestedField(object.Object, "Failed", "status", "operationState", "phase"))
	node := projectNode(object, &inspect.ResourceIdentity{}, time.Now())
	require.Equal(t, "reported failure", node.State, "Old healthy sync status must not hide the latest reported operation failure")
	unstructured.RemoveNestedField(object.Object, "status", "operationState")
	require.NoError(t, unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "ComparisonError"}}, "status", "conditions"))
	node = projectNode(object, &inspect.ResourceIdentity{}, time.Now())
	require.Equal(t, "reported failure", node.State, "Reported blocking conditions take priority over old healthy status")
}

func boolPointer(value bool) *bool { return &value }

func TestFluxProjectionSeparatesStaleStatusAndDependencyWait(t *testing.T) {
	object := graphObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ops", "delivery", "kust-uid")
	object.SetGeneration(2)
	object.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Stalled", "status": "True", "observedGeneration": int64(1)}}}
	node := projectNode(object, &inspect.ResourceIdentity{}, time.Now())
	require.Equal(t, "outdated evidence", node.State)
	object.Object["status"] = map[string]any{"observedGeneration": int64(2), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "DependencyNotReady", "observedGeneration": int64(2)}}}
	node = projectNode(object, &inspect.ResourceIdentity{}, time.Now())
	require.Equal(t, "waiting for dependency", node.State)
	object.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": "opaque-new-request"})
	node = projectNode(object, &inspect.ResourceIdentity{}, time.Now())
	require.Equal(t, "waiting for request handling", node.State)
	object.Object["spec"] = map[string]any{"suspend": true}
	node = projectNode(object, &inspect.ResourceIdentity{}, time.Now())
	require.Equal(t, "suspended", node.State)
	require.True(t, *node.Suspended)
}
