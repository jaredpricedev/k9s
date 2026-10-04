// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

const changeSetWriteUnknown = "write unknown"

func changeSetOwner(_ context.Context, identity *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
	return &gitops.Snapshot{Request: gitops.Request{Target: *identity}, CollectionState: gitops.Complete,
		Nodes:    []gitops.Node{{Identity: *identity, Kind: rolloutDeployment}},
		Coverage: []gitops.Coverage{{Source: identity.GVR, Scope: identity.Namespace + "/" + identity.Name, State: gitops.Complete}}}, nil
}

func changeSetFixture(t *testing.T) (Source, Scope, *fake.FakeDynamicClient, Resolver) {
	t.Helper()
	source, scope, live, resolve := previewFixture(t)
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), live)
	dyn.PrependReactor("patch", previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchAction)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(patch.GetPatch(), &payload))
		current, err := dyn.Tracker().Get(action.GetResource(), action.GetNamespace(), patch.GetName())
		require.NoError(t, err)
		before := current.(*unstructured.Unstructured)
		request := &unstructured.Unstructured{Object: payload}
		require.Equal(t, types.ApplyPatchType, patch.GetPatchType())
		require.Equal(t, before.GetUID(), request.GetUID())
		require.Equal(t, before.GetResourceVersion(), request.GetResourceVersion())
		options := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		require.Equal(t, ChangeSetFieldManager, options.FieldManager)
		require.Equal(t, metav1.FieldValidationStrict, options.FieldValidation)
		require.Nil(t, options.Force)
		admitted := before.DeepCopy()
		spec, _, _ := unstructured.NestedMap(payload, "spec")
		require.NoError(t, unstructured.SetNestedMap(admitted.Object, spec, "spec"))
		admitted.SetResourceVersion("10")
		admitted.SetGeneration(2)
		if len(options.DryRun) == 0 {
			require.NoError(t, dyn.Tracker().Update(action.GetResource(), admitted.DeepCopy(), action.GetNamespace()))
		} else {
			require.Equal(t, []string{metav1.DryRunAll}, options.DryRun)
		}
		return true, admitted, nil
	})
	return source, scope, dyn, resolve
}

func changeSetPersistentCount(dyn *fake.FakeDynamicClient) int {
	count := 0
	for _, action := range dyn.Actions() {
		switch action.GetVerb() {
		case "patch":
			if len(action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions().DryRun) == 0 {
				count++
			}
		case "create":
			if len(action.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions().DryRun) == 0 {
				count++
			}
		}
	}
	return count
}

func TestChangeSetExplicitDryRunThenIndependentObservationAndPrivatePlan(t *testing.T) {
	source, scope, dyn, resolve := changeSetFixture(t)
	plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	require.Equal(t, ChangeSetPrepared, plan.Entries[0].State)
	require.Equal(t, plan.Entries[0].LiveReadAt, plan.Entries[0].Projection.A.ObservedAt)
	require.Equal(t, plan.Entries[0].PreviewReadAt, plan.Entries[0].Projection.B.ObservedAt)
	require.False(t, plan.Entries[0].PreviewReadAt.Before(plan.Entries[0].LiveReadAt))
	require.NotEmpty(t, plan.Entries[0].Intent.Changes)
	require.Zero(t, changeSetPersistentCount(dyn))
	// Public evidence and the source supplied by the caller cannot retarget the
	// private payload or identity used after confirmation.
	plan.Entries[0].Identity.Name = previewTestOther
	plan.Entries[0].ExpectedAbsent = true
	plan.Source.Path = "/never/read/another/source"
	plan.ExpiresAt = time.Now().Add(time.Hour)
	source.Objects[0].Object["spec"] = map[string]any{"replicas": int64(99)}
	var attempted int
	var steps []string
	accepted, err := ApplyChangeSetTarget(t.Context(), dyn, changeSetOwner, plan, 0, ChangeSetHooks{BeforeWrite: func(context.Context) { attempted++ }, Accepted: func(_ context.Context, step string) { steps = append(steps, step) }})
	require.NoError(t, err)
	require.Equal(t, "checkout", accepted.Identity.Name)
	require.True(t, accepted.Observed)
	require.Equal(t, int64(2), accepted.ObservedGeneration)
	require.Equal(t, "10", accepted.ObservedResourceVersion)
	require.Contains(t, accepted.ObservationSource, "independent named API GET")
	require.False(t, accepted.ObservedAt.Before(accepted.AcceptedAt))
	require.Len(t, steps, 1)
	require.Equal(t, 1, attempted)
	require.Equal(t, 1, changeSetPersistentCount(dyn))
	_, err = ApplyChangeSetTarget(t.Context(), dyn, changeSetOwner, plan, 0, ChangeSetHooks{})
	require.Error(t, err)
	require.Equal(t, 1, changeSetPersistentCount(dyn), "no automatic or duplicate execution")
}

func TestChangeSetSourceUIDRVGenerationAndOwnershipChangesRejectBeforeWrite(t *testing.T) {
	for _, scenario := range []string{"source", previewUID, previewResourceVersion, previewGeneration, "owner metadata", "owner coverage", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			source, scope, dyn, resolve := changeSetFixture(t)
			plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
			require.NoError(t, err)
			owner := OwnershipReader(changeSetOwner)
			current, err := dyn.Tracker().Get(plan.Entries[0].Identity.GVR, "team", "checkout")
			require.NoError(t, err)
			live := current.(*unstructured.Unstructured).DeepCopy()
			switch scenario {
			case "source":
				require.NoError(t, os.WriteFile(source.Identity.Path, []byte("changed"), 0600))
			case previewUID:
				live.SetUID(previewTestReplacement)
			case previewResourceVersion:
				live.SetResourceVersion("later")
			case previewGeneration:
				live.SetGeneration(99)
			case "owner metadata":
				live.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "reconciler"})
			case "owner coverage":
				owner = func(ctx context.Context, id *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
					graph, ownerErr := changeSetOwner(ctx, id)
					graph.Coverage[0].State = gitops.Denied
					return graph, ownerErr
				}
			case "expired":
				plan.core.expiresAt = time.Now().Add(-time.Second)
			}
			require.NoError(t, dyn.Tracker().Update(plan.Entries[0].Identity.GVR, live, "team"))
			attempted := false
			_, err = ApplyChangeSetTarget(t.Context(), dyn, owner, plan, 0, ChangeSetHooks{BeforeWrite: func(context.Context) { attempted = true }})
			require.Error(t, err)
			require.False(t, attempted)
			require.Zero(t, changeSetPersistentCount(dyn))
		})
	}
}

func TestChangeSetManagementAmbiguousOwnershipAndSecretsNeverSubmitAdmission(t *testing.T) {
	for _, scenario := range []string{"authored marker", "copied marker", "malformed live marker", "ownership denied", collectSecretKind, "admission marker"} {
		t.Run(scenario, func(t *testing.T) {
			source, scope, dyn, resolve := changeSetFixture(t)
			owner := OwnershipReader(changeSetOwner)
			switch scenario {
			case "authored marker":
				source.Objects[0].Object[collectMetadata].(map[string]any)["labels"] = map[string]any{"argocd.argoproj.io/instance": "delivery"}
			case "copied marker":
				owner = func(ctx context.Context, id *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
					graph, ownerErr := changeSetOwner(ctx, id)
					graph.Nodes[0].Markers = []string{"Argo tracking metadata (unverified)"}
					return graph, ownerErr
				}
			case "malformed live marker":
				live, err := dyn.Tracker().Get(scopeGVR(), "team", "checkout")
				require.NoError(t, err)
				live.(*unstructured.Unstructured).Object[collectMetadata].(map[string]any)["labels"] = map[string]any{"app.kubernetes.io/managed-by": int64(1)}
				require.NoError(t, dyn.Tracker().Update(scopeGVR(), live, "team"))
			case "ownership denied":
				owner = func(context.Context, *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
					return nil, errors.New(sourceTestToken)
				}
			case collectSecretKind:
				source.Objects[0].Kind = collectSecretKind
				source.Objects[0].SecretExcluded = true
			case "admission marker":
				dyn.PrependReactor("patch", previewTestDeployments, func(ktesting.Action) (bool, runtime.Object, error) {
					live := observedObject(authored("team", "checkout", 2), "live-uid")
					live.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "injected"})
					return true, live, nil
				})
			}
			plan, err := PrepareChangeSet(t.Context(), dyn, resolve, owner, source, scope)
			require.NoError(t, err)
			require.NotEqual(t, ChangeSetPrepared, plan.Entries[0].State)
			_, eligible := plan.Target(0)
			require.False(t, eligible)
			require.Zero(t, changeSetPersistentCount(dyn))
			if scenario != "admission marker" {
				for _, action := range dyn.Actions() {
					require.NotEqual(t, "patch", action.GetVerb())
					require.NotEqual(t, "create", action.GetVerb())
					require.NotEqual(t, "secrets", action.GetResource().Resource)
				}
			}
			encoded, err := json.Marshal(plan)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), sourceTestToken)
		})
	}
}

func scopeGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: previewTestAppsGroup, Version: previewTestVersion, Resource: previewTestDeployments}
}

func TestChangeSetVerifiedAbsenceUsesCreateAndProxy404DoesNot(t *testing.T) {
	source, scope, _, resolve := changeSetFixture(t)
	scope.CapturedUIDs = nil
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("create", previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions()
		require.Equal(t, ChangeSetFieldManager, options.FieldManager)
		require.Equal(t, metav1.FieldValidationStrict, options.FieldValidation)
		object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		object.SetUID("created-uid")
		object.SetResourceVersion("1")
		object.SetGeneration(1)
		if len(options.DryRun) == 0 {
			require.NoError(t, dyn.Tracker().Create(action.GetResource(), object.DeepCopy(), action.GetNamespace()))
		}
		return true, object, nil
	})
	plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	require.Equal(t, ChangeSetPrepared, plan.Entries[0].State)
	require.True(t, plan.Entries[0].ExpectedAbsent)
	require.Contains(t, plan.Entries[0].Reason, "separate field-ownership review")
	require.Empty(t, plan.Entries[0].Identity.UID)
	result, err := ApplyChangeSetTarget(t.Context(), dyn, changeSetOwner, plan, 0, ChangeSetHooks{})
	require.NoError(t, err)
	require.True(t, result.Observed)
	require.Equal(t, types.UID("created-uid"), result.Identity.UID)
	require.Equal(t, 1, changeSetPersistentCount(dyn))
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("unexpected persistent request: %s", r.Method)
		}
		http.Error(w, "proxy missing upstream", http.StatusNotFound)
	}))
	defer server.Close()
	reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	unknown, err := PrepareChangeSet(t.Context(), reader, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	require.Equal(t, StateUnknown, unknown.Entries[0].State)
	require.False(t, unknown.Entries[0].ExpectedAbsent)
	require.Equal(t, 1, requests)
}

func TestChangeSetPriorCreateOwnershipConflictRemainsBlockedWithoutMigration(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: previewTestAppsGroup, Version: previewTestVersion, Resource: previewTestDeployments}
	for _, manager := range []string{ChangeSetFieldManager, "another-native-client"} {
		t.Run(manager, func(t *testing.T) {
			source, scope, dyn, resolve := changeSetFixture(t)
			current, err := dyn.Tracker().Get(gvr, "team", "checkout")
			require.NoError(t, err)
			live := current.(*unstructured.Unstructured).DeepCopy()
			live.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "apps/v1"}})
			require.NoError(t, dyn.Tracker().Update(gvr, live, "team"))
			dyn.PrependReactor("patch", previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
				options := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
				require.Equal(t, []string{metav1.DryRunAll}, options.DryRun)
				require.Nil(t, options.Force)
				return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusConflict, Reason: metav1.StatusReasonConflict,
					Message: "request contains confidential fixture content", Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: metav1.CauseTypeFieldManagerConflict}}}}}
			})
			plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
			require.NoError(t, err)
			require.Equal(t, PreviewConflict, plan.Entries[0].State)
			require.NotContains(t, plan.Entries[0].Reason, "confidential fixture content")
			if manager == ChangeSetFieldManager {
				require.Contains(t, plan.Entries[0].Reason, "prior native create")
				require.Contains(t, plan.Entries[0].Reason, "no automatic migration, Force or retry")
			} else {
				require.NotContains(t, plan.Entries[0].Reason, "prior native create")
			}
			_, eligible := plan.Target(0)
			require.False(t, eligible)
			_, err = ApplyChangeSetTarget(t.Context(), dyn, changeSetOwner, plan, 0, ChangeSetHooks{})
			require.Error(t, err)
			require.Zero(t, changeSetPersistentCount(dyn), "blocked conflict cannot migrate ownership or persist a second request")
		})
	}
}

func TestChangeSetCreationRaceCancellationAndPostAcceptanceUncertainty(t *testing.T) {
	for _, scenario := range []string{"before canceled", "after canceled", "observation denied", "admission changed", changeSetWriteUnknown} {
		t.Run(scenario, func(t *testing.T) {
			source, scope, dyn, resolve := changeSetFixture(t)
			plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			accepted := 0
			hooks := ChangeSetHooks{Accepted: func(_ context.Context, _ string) {
				accepted++
				if scenario == "after canceled" {
					cancel()
				}
			}}
			if scenario == "before canceled" {
				cancel()
			}
			if scenario == "observation denied" {
				dyn.PrependReactor(previewTestGet, previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
					if accepted > 0 {
						return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "checkout", errors.New(sourceTestToken))
					}
					return false, nil, nil
				})
			}
			if scenario == "admission changed" || scenario == changeSetWriteUnknown {
				dyn.PrependReactor("patch", previewTestDeployments, func(_ ktesting.Action) (bool, runtime.Object, error) {
					if scenario == changeSetWriteUnknown {
						return true, nil, apierrors.NewInternalError(errors.New(sourceTestToken))
					}
					live := observedObject(authored("team", "checkout", 99), "live-uid")
					live.SetResourceVersion("changed")
					return true, live, nil
				})
			}
			result, err := ApplyChangeSetTarget(ctx, dyn, changeSetOwner, plan, 0, hooks)
			require.Error(t, err)
			require.NotContains(t, err.Error(), sourceTestToken)
			if scenario == "before canceled" {
				require.Nil(t, result)
				require.Zero(t, accepted)
				require.Zero(t, changeSetPersistentCount(dyn))
			} else if scenario == changeSetWriteUnknown {
				require.Nil(t, result)
				require.Zero(t, accepted)
				require.True(t, apierrors.IsInternalError(err))
			} else {
				require.NotNil(t, result)
				require.Equal(t, 1, accepted)
				require.False(t, result.Observed)
				require.ErrorIs(t, err, ErrChangeSetOutcomeUnknown)
			}
		})
	}
}

func TestChangeSetCapturedScopeDuplicatesAndRawConfidentialValuesStayPrivate(t *testing.T) {
	source, scope, dyn, resolve := changeSetFixture(t)
	manifest := source.Objects[0].Object
	require.NoError(t, unstructured.SetNestedSlice(manifest, []any{map[string]any{"name": "api", "image": "example.test/api:v2", "env": []any{map[string]any{"name": "PASSWORD", "value": sourceTestToken}}}}, "spec", "template", "spec", "containers"))
	plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), sourceTestToken)
	require.NotEmpty(t, plan.Entries[0].Intent.Unreviewed)
	scope.Namespaces = []string{previewTestOther}
	excluded, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	require.Equal(t, StateOutScope, excluded.Entries[0].State)
	scope.Namespaces = []string{"team"}
	source.Objects = append(source.Objects, source.Objects[0])
	duplicate, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	for _, entry := range duplicate.Entries {
		require.Equal(t, StateExcluded, entry.State)
	}
	require.Zero(t, changeSetPersistentCount(dyn))
	// A status-only acceptance response is not an observed rollout result.
	require.NotContains(t, strings.ToLower(string(encoded)), "controller complete")
}

type changeSetNativeOwnerReader struct{ dynamic dynamic.Interface }

func (r *changeSetNativeOwnerReader) Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return r.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}
func (*changeSetNativeOwnerReader) Resolve(context.Context, string, string, string) (gitops.ResolvedResource, error) {
	return gitops.ResolvedResource{}, errors.New("fixture has no owner references")
}

func TestChangeSetUsesRealNativeOwnershipCollectorAndRechecksSourceAfterPreflight(t *testing.T) {
	source, scope, dyn, resolve := changeSetFixture(t)
	owner := func(ctx context.Context, identity *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
		require.Equal(t, "apps/v1/deployments", identity.GVR)
		return gitops.Collect(ctx, &changeSetNativeOwnerReader{dynamic: dyn}, &gitops.Request{Target: *identity})
	}
	plan, err := PrepareChangeSet(t.Context(), dyn, resolve, owner, source, scope)
	require.NoError(t, err)
	require.Equal(t, ChangeSetPrepared, plan.Entries[0].State)
	mutatingOwner := func(ctx context.Context, identity *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
		graph, readErr := owner(ctx, identity)
		require.NoError(t, os.WriteFile(source.Identity.Path, []byte("changed during ownership preflight"), 0600))
		return graph, readErr
	}
	_, err = ApplyChangeSetTarget(t.Context(), dyn, mutatingOwner, plan, 0, ChangeSetHooks{})
	require.ErrorContains(t, err, "Source changed during preflight")
	require.Zero(t, changeSetPersistentCount(dyn))
}

func TestChangeSetPrivateExpiryBoundsSlowPreflightAndSuccessfulResponseRetainsAcceptance(t *testing.T) {
	source, scope, dyn, resolve := changeSetFixture(t)
	plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	plan.core.expiresAt = time.Now().Add(25 * time.Millisecond)
	slowOwner := func(ctx context.Context, _ *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err = ApplyChangeSetTarget(t.Context(), dyn, slowOwner, plan, 0, ChangeSetHooks{})
	require.Error(t, err)
	require.Zero(t, changeSetPersistentCount(dyn))
	source, scope, dyn, resolve = changeSetFixture(t)
	plan, err = PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, scope)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dyn.PrependReactor("patch", previewTestDeployments, func(action ktesting.Action) (bool, runtime.Object, error) {
		live, readErr := dyn.Tracker().Get(action.GetResource(), "team", "checkout")
		require.NoError(t, readErr)
		result := live.(*unstructured.Unstructured).DeepCopy()
		require.NoError(t, unstructured.SetNestedField(result.Object, int64(2), "spec", "replicas"))
		result.SetResourceVersion("10")
		result.SetGeneration(2)
		cancel() // Native response succeeded; cancellation cannot erase this fact.
		return true, result, nil
	})
	steps := 0
	result, err := ApplyChangeSetTarget(ctx, dyn, changeSetOwner, plan, 0, ChangeSetHooks{Accepted: func(context.Context, string) { steps++ }})
	require.ErrorIs(t, err, ErrChangeSetOutcomeUnknown)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	require.Equal(t, 1, steps)
	require.Equal(t, types.UID("live-uid"), result.Identity.UID)
	require.False(t, result.Observed)
}

func TestChangeSetConfigMapBinaryDataNeverCrossesSafePlanBoundary(t *testing.T) {
	encodedValue := "Y29uZmlkZW50aWFsLWJpbmFyeS1jcmVkZW50aWFs"
	source, err := LoadSource(t.Context(), sourceTestFile(t, "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: settings, namespace: team}\nbinaryData: {access.dat: "+encodedValue+"}\n"))
	require.NoError(t, err)
	gvr := schema.GroupVersionResource{Version: previewTestVersion, Resource: "configmaps"}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		object.SetUID("preview-only")
		return true, object, nil
	})
	resolve := func(context.Context, string, string) (Mapping, error) {
		return Mapping{GVR: gvr, Namespaced: true}, nil
	}
	plan, err := PrepareChangeSet(t.Context(), dyn, resolve, changeSetOwner, source, Scope{Context: previewTestContext, Namespaces: []string{"team"}})
	require.NoError(t, err)
	data, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NotContains(t, string(data), encodedValue)
	for _, change := range plan.Entries[0].Intent.Changes {
		require.NotContains(t, change.After, encodedValue)
	}
}
