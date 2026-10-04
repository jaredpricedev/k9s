// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func expansionObjects() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim",
			"metadata": map[string]any{"name": "data", "namespace": "apps", "uid": "pvc-uid", "resourceVersion": "20"},
			"spec":     map[string]any{"storageClassName": "fast", "volumeName": "data-pv", "resources": map[string]any{"requests": map[string]any{"storage": "12Gi"}}},
			"status": map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "8Gi"},
				"conditions": []any{map[string]any{"type": "FileSystemResizePending", "status": "True"}}}}},
		{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": map[string]any{"name": "data-pv", "uid": "pv-uid", "resourceVersion": "21"},
			"spec": map[string]any{"storageClassName": "fast", "claimRef": map[string]any{"namespace": "apps", "name": "data", "uid": "pvc-uid"}}}},
		{Object: map[string]any{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": map[string]any{"name": "fast", "uid": "sc-uid", "resourceVersion": "21"},
			"provisioner": "fixture-csi", "allowVolumeExpansion": true}},
	}
}
func expansionOperationFixture(t *testing.T) (*operationSession, *storage.ExpansionPlan, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	v := storageViewFixture(t)
	plan, err := v.snapshot.Expansion("apps", "data", "15Gi")
	require.NoError(t, err)
	objects := expansionObjects()
	reader := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects[0], objects[1], objects[2])
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	return &operationSession{dynamic: reader, typed: typed}, plan, reader
}
func expansionTarget(plan *storage.ExpansionPlan) SelectedResourceTarget {
	return SelectedResourceTarget{Context: plan.PVC.Context, GVR: client.PvcGVR, Namespace: plan.PVC.Namespace,
		Name: plan.PVC.Name, UID: types.UID(plan.PVC.UID)}
}

func TestStorageExpansionAcceptedReceiptPreservesPendingFilesystemAndCapacity(t *testing.T) {
	s, plan, reader := expansionOperationFixture(t)
	target := expansionTarget(plan)
	task := startOperationBatch(time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, target SelectedResourceTarget) error {
		return s.expandPVC(ctx, &target, plan)
	}, nil, nil)
	receipt := waitOperationTask(t, task)
	require.Equal(t, operationAccepted, receipt.Outcomes[0].State)
	require.Contains(t, receipt.Outcomes[0].AcceptedSteps[0], "controller/filesystem progress unconfirmed")
	pvc, err := reader.Resource(client.PvcGVR.GVR()).Namespace("apps").Get(t.Context(), "data", metav1.GetOptions{})
	require.NoError(t, err)
	requested, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	capacity, _, _ := unstructured.NestedString(pvc.Object, "status", "capacity", "storage")
	require.Equal(t, "15Gi", requested)
	require.Equal(t, "8Gi", capacity)
	require.Contains(t, operationReceiptText(receipt, 1, 1), "Acceptance is distinct from controller completion")
	writes := 0
	for _, action := range reader.Actions() {
		if action.GetVerb() != "patch" {
			continue
		}
		writes++
		patchAction := action.(ktesting.PatchAction)
		require.Equal(t, types.JSONPatchType, patchAction.GetPatchType())
		var patch []map[string]any
		require.NoError(t, json.Unmarshal(patchAction.GetPatch(), &patch))
		require.Len(t, patch, 3)
		require.Equal(t, "/metadata/uid", patch[0]["path"])
		require.Equal(t, "pvc-uid", patch[0]["value"])
		require.Equal(t, "20", patch[1]["value"])
		require.Equal(t, "/spec/resources/requests/storage", patch[2]["path"])
	}
	require.Equal(t, 1, writes)
}

func TestStorageExpansionFreshRechecksRejectChangesBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name, resource, message string
		mutate                  func(*unstructured.Unstructured)
	}{
		{"PVC replaced", "persistentvolumeclaims", "replaced", func(o *unstructured.Unstructured) { o.SetUID("replacement") }},
		{"PVC changed", "persistentvolumeclaims", "resourceVersion changed", func(o *unstructured.Unstructured) { o.SetResourceVersion("22") }},
		{"class replaced", "storageclasses", "StorageClass UID/resourceVersion changed", func(o *unstructured.Unstructured) { o.SetUID("replacement") }},
		{"class changed", "storageclasses", "StorageClass UID/resourceVersion changed", func(o *unstructured.Unstructured) { o.SetResourceVersion("22") }},
		{"class disallows", "storageclasses", "does not explicitly allow", func(o *unstructured.Unstructured) { o.Object["allowVolumeExpansion"] = false }},
		{"missing provisioner", "storageclasses", "provisioner is not reported", func(o *unstructured.Unstructured) { o.Object["provisioner"] = "" }},
		{"PV replaced", "persistentvolumes", "PV UID changed", func(o *unstructured.Unstructured) { o.SetUID("replacement") }},
		{"binding changed", "persistentvolumes", "claim UID binding", func(o *unstructured.Unstructured) {
			require.NoError(t, unstructured.SetNestedField(o.Object, "replacement", "spec", "claimRef", "uid"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, plan, reader := expansionOperationFixture(t)
			reader.PrependReactor("get", tc.resource, func(action ktesting.Action) (bool, runtime.Object, error) {
				obj, err := reader.Tracker().Get(action.GetResource(), action.GetNamespace(), action.(ktesting.GetAction).GetName())
				if err != nil {
					return true, nil, err
				}
				o := obj.(*unstructured.Unstructured).DeepCopy()
				tc.mutate(o)
				return true, o, nil
			})
			target := expansionTarget(plan)
			require.ErrorContains(t, s.expandPVC(t.Context(), &target, plan), tc.message)
			for _, action := range reader.Actions() {
				require.NotEqual(t, "patch", action.GetVerb())
			}
		})
	}
}

func TestStorageExpansionPerTargetFailureAndDeniedAuthorizationAreRetained(t *testing.T) {
	s, plan, reader := expansionOperationFixture(t)
	target := expansionTarget(plan)
	second := *plan
	second.PVC.Name, second.PVC.UID = "other-data", "other-pvc-uid"
	second.PV.Name, second.PV.UID = "other-pv", "other-pv-uid"
	objects := expansionObjects()
	objects[0].SetName(second.PVC.Name)
	objects[0].SetUID(types.UID(second.PVC.UID))
	require.NoError(t, unstructured.SetNestedField(objects[0].Object, second.PV.Name, "spec", "volumeName"))
	objects[1].SetName(second.PV.Name)
	objects[1].SetUID(types.UID(second.PV.UID))
	require.NoError(t, unstructured.SetNestedField(objects[1].Object, second.PVC.Name, "spec", "claimRef", "name"))
	require.NoError(t, unstructured.SetNestedField(objects[1].Object, second.PVC.UID, "spec", "claimRef", "uid"))
	require.NoError(t, reader.Tracker().Add(objects[0]))
	require.NoError(t, reader.Tracker().Add(objects[1]))
	var attempts atomic.Int32
	reader.PrependReactor("patch", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		attempts.Add(1)
		if action.(ktesting.PatchAction).GetName() == target.Name {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumeclaims"}, "data", errors.New("fixture expansion denied"))
		}
		return false, nil, nil
	})
	task := startOperationBatch(time.Second, []SelectedResourceTarget{target, expansionTarget(&second)}, func(ctx context.Context, target SelectedResourceTarget) error {
		if target.Name == second.PVC.Name {
			return s.expandPVC(ctx, &target, &second)
		}
		return s.expandPVC(ctx, &target, plan)
	}, nil, nil)
	receipt := waitOperationTask(t, task)
	require.Equal(t, operationFailed, receipt.Outcomes[0].State)
	require.Empty(t, receipt.Outcomes[0].AcceptedSteps)
	require.Equal(t, operationAccepted, receipt.Outcomes[1].State)
	require.EqualValues(t, 2, attempts.Load())
	s.typed.(*kubefake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{}, nil
	})
	before := len(reader.Actions())
	require.ErrorContains(t, s.expandPVC(t.Context(), &target, plan), "access denied")
	require.Len(t, reader.Actions(), before)
}

func TestStorageExpansionNeverPatchesEqualOrLowerThanReviewedRequest(t *testing.T) {
	for _, requested := range []string{"8Gi", "10Gi", "12Gi"} {
		t.Run(requested, func(t *testing.T) {
			s, plan, reader := expansionOperationFixture(t)
			plan.Requested = resource.MustParse(requested)
			target := expansionTarget(plan)
			require.ErrorContains(t, s.expandPVC(t.Context(), &target, plan), "shrink/equal requests are rejected")
			for _, action := range reader.Actions() {
				require.NotEqual(t, "patch", action.GetVerb())
			}
		})
	}
}

func expansionHTTPFixture(t *testing.T, race bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	objects := expansionObjects()
	var patches atomic.Int32
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "selfsubjectaccessreviews") {
			_ = json.NewEncoder(w).Encode(&authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}, Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPatch {
			patches.Add(1)
			assert.Equal(t, string(types.JSONPatchType), r.Header.Get("Content-Type"))
			var patch []map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&patch)) || !assert.Len(t, patch, 3) {
				http.Error(w, "fixture invalid patch", http.StatusBadRequest)
				return
			}
			assert.Equal(t, "pvc-uid", patch[0]["value"])
			assert.Equal(t, "20", patch[1]["value"])
			if patch[0]["value"] != string(objects[0].GetUID()) || patch[1]["value"] != objects[0].GetResourceVersion() {
				// This disposable server checks the atomic identity/version
				// tests against its actual concurrent replacement state.
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonConflict, Code: 409, Message: "fixture JSON patch UID test rejected"})
				return
			}
			updated := objects[0].DeepCopy()
			assert.NoError(t, unstructured.SetNestedField(updated.Object, "15Gi", "spec", "resources", "requests", "storage"))
			updated.SetResourceVersion("22")
			objects[0] = updated
			_ = json.NewEncoder(w).Encode(updated)
			return
		}
		for _, object := range objects {
			if strings.HasSuffix(r.URL.Path, "/"+object.GetName()) {
				if race && object.GetKind() == "PersistentVolume" {
					// PVC GET has completed; replace it before PATCH reaches
					// the server without changing the already returned PVC.
					objects[0] = objects[0].DeepCopy()
					objects[0].SetUID("replacement")
					objects[0].SetResourceVersion("22")
				}
				_ = json.NewEncoder(w).Encode(object)
				return
			}
		}
		http.Error(w, "fixture unexpected request", http.StatusNotFound)
	}))
	return server, &patches
}

func TestStorageExpansionRealServerRejectsConcurrentReplacementWithoutRetry(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted-still-pending", true: "replacement-between-read-and-patch"}[race], func(t *testing.T) {
			v := storageViewFixture(t)
			plan, err := v.snapshot.Expansion("apps", "data", "15Gi")
			require.NoError(t, err)
			server, patches := expansionHTTPFixture(t, race)
			defer server.Close()
			config := &rest.Config{Host: server.URL}
			reader, err := dynamic.NewForConfig(config)
			require.NoError(t, err)
			typed, err := kubernetes.NewForConfig(config)
			require.NoError(t, err)
			s := &operationSession{dynamic: reader, typed: typed}
			task := startOperationBatch(time.Second, []SelectedResourceTarget{expansionTarget(plan)}, func(ctx context.Context, target SelectedResourceTarget) error {
				return s.expandPVC(ctx, &target, plan)
			}, nil, nil)
			receipt := waitOperationTask(t, task)
			require.EqualValues(t, 1, patches.Load(), "no conflict replan/retry for reviewed expansion")
			if race {
				require.Equal(t, operationFailed, receipt.Outcomes[0].State)
				require.Empty(t, receipt.Outcomes[0].AcceptedSteps)
			} else {
				require.Equal(t, operationAccepted, receipt.Outcomes[0].State)
				require.Contains(t, receipt.Outcomes[0].AcceptedSteps[0], "progress unconfirmed")
			}
			current, err := reader.Resource(client.PvcGVR.GVR()).Namespace("apps").Get(t.Context(), "data", metav1.GetOptions{})
			require.NoError(t, err)
			capacity, _, _ := unstructured.NestedString(current.Object, "status", "capacity", "storage")
			requested, _, _ := unstructured.NestedString(current.Object, "spec", "resources", "requests", "storage")
			require.Equal(t, "8Gi", capacity)
			if race {
				require.Equal(t, types.UID("replacement"), current.GetUID())
				require.Equal(t, "12Gi", requested, "replacement PVC was not expanded")
			} else {
				require.Equal(t, "15Gi", requested, "accepted request persists separately from unchanged capacity")
			}
		})
	}
}
