// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	previewTestVersion     = "v1"
	previewTestAppLabel    = "app"
	previewTestAppsGroup   = "apps"
	previewTestDeployments = "deployments"
	previewTestSecret      = "secret"
	previewTestContext     = "pinned"
	previewTestGet         = "get"
	previewTestReplacement = "replacement"
	previewTestOther       = "other"
)

func previewFixture(t *testing.T) (Source, Scope, *unstructured.Unstructured, Resolver) {
	t.Helper()
	source, err := LoadSource(t.Context(), sourceTestFile(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: checkout\n  namespace: team\nspec: {replicas: 2}\n"))
	require.NoError(t, err)
	gvr := schema.GroupVersionResource{Group: previewTestAppsGroup, Version: previewTestVersion, Resource: previewTestDeployments}
	scope := Scope{Context: previewTestContext, Namespaces: []string{"team"}, CapturedUIDs: map[string]types.UID{IdentityKey(gvr, "team", "checkout"): "live-uid"}}
	live := observedObject(authored("team", "checkout", 1), types.UID("live-uid"))
	live.SetResourceVersion("9")
	live.SetLabels(map[string]string{previewTestAppLabel: "checkout"})
	resolve := func(context.Context, string, string) (Mapping, error) {
		return Mapping{GVR: gvr, Namespaced: true}, nil
	}
	return source, scope, live, resolve
}

func TestServerPreviewOnlyExplicitDryRunAndRetainsIdentityPermissionsAndOwnership(t *testing.T) {
	source, scope, live, resolve := previewFixture(t)
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), live)
	original, _ := json.Marshal(source.Objects[0].Object)
	dyn.PrependReactor("patch", previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchAction)
		require.Equal(t, types.ApplyPatchType, patch.GetPatchType())
		require.Equal(t, "team", patch.GetNamespace())
		require.Equal(t, "checkout", patch.GetName())
		options := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		require.Equal(t, []string{metav1.DryRunAll}, options.DryRun)
		require.Equal(t, PreviewFieldManager, options.FieldManager)
		require.Equal(t, metav1.FieldValidationStrict, options.FieldValidation)
		require.Nil(t, options.Force)
		var object map[string]any
		require.NoError(t, json.Unmarshal(patch.GetPatch(), &object))
		metadata := object["metadata"].(map[string]any)
		require.Equal(t, "live-uid", metadata["uid"])
		require.Equal(t, "9", metadata["resourceVersion"])
		admitted := live.DeepCopy()
		require.NoError(t, unstructured.SetNestedField(admitted.Object, int64(2), "spec", "replicas"))
		return true, admitted, nil
	})
	local := Collect(t.Context(), dyn, resolve, source, scope, time.Now())
	require.Len(t, local.Entries, 1)
	require.Len(t, dyn.Actions(), 1, "local comparison must not issue preview")
	result := PreviewServer(t.Context(), dyn, resolve, source, scope, time.Now())
	require.Len(t, result.Entries, 1)
	entry := result.Entries[0]
	require.Equal(t, PreviewAccepted, entry.State)
	require.Contains(t, entry.Reason, "no resource was persisted")
	require.Equal(t, types.UID("live-uid"), entry.Identity.UID)
	require.True(t, entry.Projection.Comparable)
	require.NotEmpty(t, entry.Projection.Changes)
	require.Len(t, dyn.Actions(), 3)
	after, _ := json.Marshal(source.Objects[0].Object)
	require.Equal(t, string(original), string(after), "preview must not mutate source")
	retained, err := dyn.Resource(entry.Identity.GVR).Namespace("team").Get(t.Context(), "checkout", metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, _ := unstructured.NestedInt64(retained.Object, "spec", "replicas")
	require.EqualValues(t, 1, replicas, "dry-run fixture must retain live state")
}

func TestServerPreviewCreateIsSeparateFromCandidateAndNeverPersists(t *testing.T) {
	source, scope, _, resolve := previewFixture(t)
	scope.CapturedUIDs = nil
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("create", previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
		opts := action.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions()
		require.Equal(t, []string{metav1.DryRunAll}, opts.DryRun)
		require.Equal(t, PreviewFieldManager, opts.FieldManager)
		object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		require.Equal(t, "team", object.GetNamespace())
		object.SetUID("dry-run-only-uid")
		return true, object, nil
	})
	result := PreviewServer(t.Context(), dyn, resolve, source, scope, time.Now())
	require.Equal(t, PreviewAccepted, result.Entries[0].State)
	require.Equal(t, "create dry-run", result.Entries[0].Request)
	for _, action := range dyn.Actions() {
		require.Contains(t, []string{previewTestGet, "create"}, action.GetVerb())
		require.Equal(t, "team", action.GetNamespace())
	}
	_, err := dyn.Resource(result.Entries[0].Identity.GVR).Namespace("team").Get(t.Context(), "checkout", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
}

func TestServerPreviewRejectsReplacedExcludedAndOutOfScopeBeforeAdmission(t *testing.T) {
	for _, scenario := range []string{previewTestReplacement, previewTestSecret, "cluster scoped", collectNamespace, "selector", "duplicate", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			source, scope, live, resolve := previewFixture(t)
			switch scenario {
			case previewTestReplacement:
				live.SetUID("replacement-uid")
			case previewTestSecret:
				source.Objects[0].Kind = "Secret"
				source.Objects[0].SecretExcluded = true
			case "cluster scoped":
				resolve = func(context.Context, string, string) (Mapping, error) {
					return Mapping{GVR: schema.GroupVersionResource{Group: previewTestAppsGroup, Version: previewTestVersion, Resource: previewTestDeployments}, Namespaced: false}, nil
				}
			case collectNamespace:
				scope.Namespaces = []string{previewTestOther}
			case "selector":
				scope.CapturedUIDs = nil
				scope.LabelSelector = "app=other"
			case "duplicate":
				source.Objects = append(source.Objects, source.Objects[0])
			case "unsupported":
				resolve = func(context.Context, string, string) (Mapping, error) { return Mapping{}, errors.New("unavailable") }
			}
			dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), live)
			result := PreviewServer(t.Context(), dyn, resolve, source, scope, time.Now())
			require.NotEmpty(t, result.Entries)
			for _, entry := range result.Entries {
				require.NotEqual(t, PreviewAccepted, entry.State)
			}
			for _, action := range dyn.Actions() {
				require.Equal(t, previewTestGet, action.GetVerb(), "invalid target must never invoke admission")
			}
		})
	}
}

func TestServerPreviewClassifiesDeniedConflictValidationWithoutLeakingAuthoredDiagnostics(t *testing.T) {
	for _, serverErr := range []error{
		apierrors.NewForbidden(schema.GroupResource{Group: previewTestAppsGroup, Resource: previewTestDeployments}, "checkout", errors.New(sourceTestToken)),
		apierrors.NewConflict(schema.GroupResource{Group: previewTestAppsGroup, Resource: previewTestDeployments}, "checkout", errors.New(sourceTestToken)),
		apierrors.NewBadRequest(sourceTestToken),
	} {
		source, scope, live, resolve := previewFixture(t)
		dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), live)
		dyn.PrependReactor("patch", previewTestDeployments, func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, serverErr })
		result := PreviewServer(t.Context(), dyn, resolve, source, scope, time.Now())
		require.NotEqual(t, PreviewAccepted, result.Entries[0].State)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), sourceTestToken)
	}
}
func TestServerPreviewCancellationIsBoundedAndEveryRequestRemainsDryRun(t *testing.T) {
	source, scope, live, resolve := previewFixture(t)
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), live)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	dyn.PrependReactor("patch", previewTestDeployments, func(ktesting.Action) (bool, runtime.Object, error) {
		close(entered)
		<-release
		return true, nil, context.Canceled
	})
	results := make(chan ServerPreview, 1)
	go func() { results <- PreviewServer(ctx, dyn, resolve, source, scope, time.Now()) }()
	<-entered
	cancel()
	select {
	case result := <-results:
		require.Contains(t, result.Entries[0].Reason, "canceled")
	case <-time.After(time.Second):
		t.Fatal("dry-run cancellation blocked on client")
	}
	close(release)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	dyn.ClearActions()
	result := PreviewServer(ctx, dyn, resolve, source, scope, time.Now())
	require.Empty(t, dyn.Actions())
	require.NotEmpty(t, result.Entries)
	encoded, _ := json.Marshal(result)
	require.NotContains(t, string(encoded), sourceTestToken)
}
