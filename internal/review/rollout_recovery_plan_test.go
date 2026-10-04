// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

type recoveryTestAPI struct {
	reader               *fake.FakeDynamicClient
	snapshot             *RolloutSnapshot
	deployment, revision *unstructured.Unstructured
	patches              []ktesting.PatchAction
	mutate               func(*unstructured.Unstructured)
}

func recoveryTestClient(t *testing.T) *recoveryTestAPI {
	t.Helper()
	o := rolloutTestDeployment()
	o.SetResourceVersion("11")
	r := rolloutTestRevision("api-old", "rs-old", rolloutTestOldImage)
	r.SetResourceVersion("17")
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), o, r)
	api := &recoveryTestAPI{reader: dyn, deployment: o, revision: r, snapshot: NewRolloutSnapshot(o, []*unstructured.Unstructured{r}, nil, nil, "lab", time.Now())}
	dyn.PrependReactor("patch", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchAction)
		api.patches = append(api.patches, patch)
		current, err := dyn.Tracker().Get(recoveryDeploymentGVR, rolloutTestNamespace, rolloutTestName)
		require.NoError(t, err)
		encoded, err := json.Marshal(current)
		require.NoError(t, err)
		operations, err := jsonpatch.DecodePatch(patch.GetPatch())
		require.NoError(t, err)
		data, err := operations.Apply(encoded)
		if err != nil {
			return true, nil, apierrors.NewConflict(recoveryDeploymentGVR.GroupResource(), rolloutTestName, nil)
		}
		result := &unstructured.Unstructured{}
		require.NoError(t, result.UnmarshalJSON(data))
		result.SetGeneration(4)
		result.SetResourceVersion("18")
		if api.mutate != nil {
			api.mutate(result)
		}
		opts := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		if len(opts.DryRun) == 0 {
			require.NoError(t, dyn.Tracker().Update(recoveryDeploymentGVR, result.DeepCopy(), rolloutTestNamespace))
		}
		return true, result, nil
	})
	return api
}

func TestRecoveryPlanDryRunPinsExactSourceAndKeepsRawBytesPrivate(t *testing.T) {
	api := recoveryTestClient(t)
	plan, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	require.Equal(t, recoveryPrepared, plan.State)
	require.True(t, plan.Comparison.Comparable)
	require.NotEmpty(t, plan.Comparison.Changes)
	require.True(t, plan.Unreviewed, "Sensitive fields require explicit acknowledgment")
	require.Len(t, api.patches, 1)
	patch := api.patches[0]
	opts := patch.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
	require.Equal(t, []string{metav1.DryRunAll}, opts.DryRun)
	require.Equal(t, "Strict", opts.FieldValidation)
	for _, value := range []string{"/metadata/uid", "/metadata/resourceVersion", "/metadata/generation", "/spec/template", "deployment-uid", "11"} {
		require.Contains(t, string(patch.GetPatch()), value)
	}
	require.NotContains(t, string(patch.GetPatch()), rolloutTemplateHash)
	live, err := api.reader.Resource(recoveryDeploymentGVR).Namespace(rolloutTestNamespace).Get(t.Context(), rolloutTestName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, api.snapshot.TemplateSHA256, rolloutTemplateDigest(live))
	require.Equal(t, "11", live.GetResourceVersion(), "Dry run must not persist")
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-command-argument")
	require.NotContains(t, string(encoded), "\"core\"")
	require.NotContains(t, string(encoded), "\"patch\":")
	require.Contains(t, string(encoded), rolloutTestOldImage)
	sourceLabels, _, _ := unstructured.NestedStringMap(api.revision.Object, "spec", "template", "metadata", "labels")
	require.Equal(t, "revision-hash", sourceLabels[rolloutTemplateHash], "Template normalization must not mutate source")
}

func TestRecoveryPlanRejectsRemovedReplacedChangedOrForeignSources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*recoveryTestAPI)
	}{
		{"removed revision", func(api *recoveryTestAPI) {
			require.NoError(t, api.reader.Tracker().Delete(recoveryRevisionGVR, rolloutTestNamespace, "api-old"))
		}},
		{"revision UID", func(api *recoveryTestAPI) {
			r := api.revision.DeepCopy()
			r.SetUID("replacement")
			require.NoError(t, api.reader.Tracker().Update(recoveryRevisionGVR, r, rolloutTestNamespace))
		}},
		{"revision RV", func(api *recoveryTestAPI) {
			r := api.revision.DeepCopy()
			r.SetResourceVersion("new")
			require.NoError(t, api.reader.Tracker().Update(recoveryRevisionGVR, r, rolloutTestNamespace))
		}},
		{"owner", func(api *recoveryTestAPI) {
			r := api.revision.DeepCopy()
			refs := r.GetOwnerReferences()
			refs[0].UID = "foreign"
			r.SetOwnerReferences(refs)
			require.NoError(t, api.reader.Tracker().Update(recoveryRevisionGVR, r, rolloutTestNamespace))
		}},
		{"deployment UID", func(api *recoveryTestAPI) {
			o := api.deployment.DeepCopy()
			o.SetUID("replacement")
			require.NoError(t, api.reader.Tracker().Update(recoveryDeploymentGVR, o, rolloutTestNamespace))
		}},
		{"deployment RV", func(api *recoveryTestAPI) {
			o := api.deployment.DeepCopy()
			o.SetResourceVersion("changed")
			require.NoError(t, api.reader.Tracker().Update(recoveryDeploymentGVR, o, rolloutTestNamespace))
		}},
		{"deployment generation", func(api *recoveryTestAPI) {
			o := api.deployment.DeepCopy()
			o.SetGeneration(4)
			require.NoError(t, api.reader.Tracker().Update(recoveryDeploymentGVR, o, rolloutTestNamespace))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := recoveryTestClient(t)
			tc.mutate(api)
			_, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
			require.Error(t, err)
			require.Empty(t, api.patches)
		})
	}
}

func TestRecoveryApplyRechecksBothSourcesSubmitsOnceAndReturnsAcceptanceOnly(t *testing.T) {
	api := recoveryTestClient(t)
	plan, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	var begun, accepted int
	result, err := ApplyRolloutRecovery(t.Context(), api.reader, plan, RolloutRecoveryHooks{
		BeforeWrite: func(context.Context) { begun++ }, Accepted: func(context.Context, string) { accepted++ },
	})
	require.NoError(t, err)
	require.Equal(t, 1, begun)
	require.Equal(t, 1, accepted)
	require.Equal(t, "accepted; controller outcome not yet observed", result.State)
	require.EqualValues(t, 4, result.Generation)
	require.Equal(t, plan.PreviewTemplateSHA256, result.TemplateSHA256)
	require.Len(t, api.patches, 2)
	require.Equal(t, api.patches[0].GetPatch(), api.patches[1].GetPatch(), "Apply must use the exact previewed payload")
	opts := api.patches[1].(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
	require.Empty(t, opts.DryRun)
	for _, action := range api.reader.Actions() {
		require.Equal(t, rolloutTestNamespace, action.GetNamespace())
		require.NotEqual(t, "list", action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
	// A second attempt cannot replay this stale, already-accepted plan.
	_, err = ApplyRolloutRecovery(t.Context(), api.reader, plan, RolloutRecoveryHooks{BeforeWrite: func(context.Context) { begun++ }})
	require.Error(t, err)
	require.Equal(t, 1, begun)
	require.Len(t, api.patches, 2)
}

func TestRecoveryApplyRejectsLateSourceChangeAndDoesNotRetryConflicts(t *testing.T) {
	api := recoveryTestClient(t)
	plan, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	r := api.revision.DeepCopy()
	r.SetUID("replacement")
	require.NoError(t, api.reader.Tracker().Update(recoveryRevisionGVR, r, rolloutTestNamespace))
	begun := 0
	_, err = ApplyRolloutRecovery(t.Context(), api.reader, plan, RolloutRecoveryHooks{BeforeWrite: func(context.Context) { begun++ }})
	require.Error(t, err)
	require.Zero(t, begun)
	require.Len(t, api.patches, 1)
	api = recoveryTestClient(t)
	plan, err = PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	writeAttempts := 0
	api.reader.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		writeAttempts++
		return true, nil, apierrors.NewConflict(recoveryDeploymentGVR.GroupResource(), rolloutTestName, nil)
	})
	_, err = ApplyRolloutRecovery(t.Context(), api.reader, plan, RolloutRecoveryHooks{})
	require.True(t, apierrors.IsConflict(err))
	require.Equal(t, 1, writeAttempts)
}

func TestRecoveryApplyUnknownAcknowledgmentAndSafeFailures(t *testing.T) {
	api := recoveryTestClient(t)
	plan, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	api.mutate = func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "unexpected-admission-mutation", "spec", "template", "metadata", "labels", "admission")
	}
	accepted := 0
	result, err := ApplyRolloutRecovery(t.Context(), api.reader, plan, RolloutRecoveryHooks{Accepted: func(context.Context, string) { accepted++ }})
	require.ErrorContains(t, err, "acknowledged")
	require.NotNil(t, result)
	require.Equal(t, 1, accepted)
	api = recoveryTestClient(t)
	api.reader.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewBadRequest("private-server-value")
	})
	_, err = PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-server-value")
	require.True(t, apierrors.IsBadRequest(err))
}

func TestRecoveryCanceledPreparationAndWriteBoundaries(t *testing.T) {
	api := recoveryTestClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PrepareRolloutRecovery(ctx, api.reader, api.snapshot, "rs-old")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, api.patches)
	plan, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	begun := 0
	_, err = ApplyRolloutRecovery(ctx, api.reader, plan, RolloutRecoveryHooks{BeforeWrite: func(context.Context) { begun++ }})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, begun)
	require.Len(t, api.patches, 1)
}

func TestRecoveryPlanRetainsBoundedUnverifiedOwnershipMarkers(t *testing.T) {
	api := recoveryTestClient(t)
	const hiddenMarker = "arbitrary-sensitive-tracking-value"
	api.deployment.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":     hiddenMarker,
		"kustomize.toolkit.fluxcd.io/name": hiddenMarker,
		"helm.toolkit.fluxcd.io/namespace": hiddenMarker,
	})
	api.deployment.SetAnnotations(map[string]string{
		"argocd.argoproj.io/tracking-id": hiddenMarker,
		"meta.helm.sh/release-name":      hiddenMarker,
	})
	require.NoError(t, api.reader.Tracker().Update(recoveryDeploymentGVR, api.deployment, rolloutTestNamespace))
	plan, err := PrepareRolloutRecovery(t.Context(), api.reader, api.snapshot, "rs-old")
	require.NoError(t, err)
	require.Len(t, plan.OwnershipMarkers, 5)
	for _, marker := range plan.OwnershipMarkers {
		require.Contains(t, marker, "unverified metadata")
	}
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), hiddenMarker)
	require.Contains(t, string(encoded), "Flux HelmRelease tracking")
}
