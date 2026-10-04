// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func maintenanceFixture(t *testing.T) (*operationSession, SelectedResourceTarget, *fake.FakeDynamicClient, *kubefake.Clientset) {
	target := SelectedResourceTarget{Context: guardedTestOriginal, GVR: client.NodeGVR, Name: testWorkspaceWorkerName, UID: guardedTestNodeUID}
	native := &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: testWorkspaceWorkerName, UID: guardedTestNodeUID, ResourceVersion: "12"}}
	encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(native)
	if err != nil {
		t.Fatal(err)
	}
	node := &unstructured.Unstructured{Object: encoded}

	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), node)
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	typed.Resources = []*metav1.APIResourceList{{GroupVersion: corev1.SchemeGroupVersion.String(), APIResources: []metav1.APIResource{{Name: "pods/eviction", Group: "policy", Version: corev1.SchemeGroupVersion.Version, Kind: "Eviction"}}}}
	return &operationSession{dynamic: dyn, typed: maintenanceFixtureClient{Interface: typed}}, target, dyn, typed
}

func TestGuardedCordonUsesUIDAndVersionAndNeverWritesReplacement(t *testing.T) {
	s, target, dyn, _ := maintenanceFixture(t)
	writes := 0
	dyn.PrependReactor(client.PatchVerb, guardedTestNodes, func(action ktesting.Action) (bool, runtime.Object, error) {
		writes++
		var patch map[string]any
		if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
			t.Fatal(err)
		}
		metadata := patch["metadata"].(map[string]any)
		if metadata["uid"] != guardedTestNodeUID || metadata["resourceVersion"] != "12" || patch["spec"].(map[string]any)["unschedulable"] != true {
			t.Fatal("unguarded cordon", patch)
		}
		return true, &unstructured.Unstructured{}, nil
	})
	if err := s.cordon(t.Context(), target, true); err != nil || writes != 1 {
		t.Fatal(err, writes)
	}
	target.UID = "old-uid"
	if err := s.cordon(t.Context(), target, false); err == nil || writes != 1 {
		t.Fatal("replacement was uncordoned", err, writes)
	}
}

func TestGuardedDrainUsesOriginalPodUIDOnNativeEvictionRetry(t *testing.T) {
	s, target, dyn, typed := maintenanceFixture(t)
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: investigationAppRole, Namespace: guardedTestNamespace, UID: "pod-original", OwnerReferences: []metav1.OwnerReference{{APIVersion: rolloutTestAPI, Kind: "ReplicaSet", Name: "owner", UID: "owner-uid", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: testWorkspaceWorkerName}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := typed.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	cordons, evictions := 0, 0
	dyn.PrependReactor(client.PatchVerb, guardedTestNodes, func(ktesting.Action) (bool, runtime.Object, error) {
		cordons++
		return true, &unstructured.Unstructured{}, nil
	})
	typed.PrependReactor("create", hubblePods, func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		evictions++
		eviction := action.(ktesting.CreateAction).GetObject().(*policyv1.Eviction)
		if eviction.DeleteOptions == nil || eviction.DeleteOptions.Preconditions == nil || eviction.DeleteOptions.Preconditions.UID == nil || *eviction.DeleteOptions.Preconditions.UID != "pod-original" {
			t.Error("eviction followed a replacement UID", eviction)
		}
		if evictions == 1 {
			replacement := pod.DeepCopy()
			replacement.UID = "pod-replacement"
			if err := typed.Tracker().Update(corev1.SchemeGroupVersion.WithResource(hubblePods), replacement, guardedTestNamespace); err != nil {
				t.Error(err)
			}
			return true, nil, apierrors.NewTooManyRequests("PDB blocks eviction", 0)
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: hubblePods}, investigationAppRole, errors.New("UID precondition failed"))
	})
	task := startOperationBatch(time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, target SelectedResourceTarget) error {
		return s.drain(ctx, target, dao.DrainOptions{GracePeriodSeconds: -1, Timeout: time.Second})
	}, nil, nil)
	receipt := waitOperationTask(t, task)
	if cordons != 1 || evictions != 2 || receipt.Outcomes[0].State != operationFailed || len(receipt.Outcomes[0].AcceptedSteps) != 1 {
		t.Fatal("native rejection or accepted cordon lost", cordons, evictions, receipt)
	}
}

func TestGuardedDrainNativeFiltersAndDeniedRBACPrecedeCordon(t *testing.T) {
	for _, denied := range []bool{false, true} {
		s, target, dyn, typed := maintenanceFixture(t)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unmanaged", Namespace: guardedTestNamespace, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{NodeName: testWorkspaceWorkerName}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if err := typed.Tracker().Add(pod); err != nil {
			t.Fatal(err)
		}
		if denied {
			typed.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authv1.SelfSubjectAccessReview{}, nil
			})
		}
		err := s.drain(t.Context(), target, dao.DrainOptions{GracePeriodSeconds: -1, Timeout: time.Second})
		if err == nil || (!denied && !strings.Contains(err.Error(), "cannot delete Pods")) {
			t.Fatal("native ownership filter or denied guard bypassed", denied, err)
		}
		for _, action := range dyn.Actions() {
			if action.GetVerb() == client.PatchVerb {
				t.Fatal("invalid drain cordoned node", denied)
			}
		}
		for _, action := range typed.Actions() {
			if action.GetResource().Resource == hubblePods && (action.GetVerb() == "delete" || action.GetSubresource() == "eviction") {
				t.Fatal("invalid drain wrote Pod", action)
			}
		}
	}
}

func TestGuardedNativeDeleteAddsUIDPreconditionAndRetainsGrace(t *testing.T) {
	typed := kubefake.NewSimpleClientset()
	nativeClient := maintenanceClient{Interface: typed, expected: map[string]types.UID{"ns/app": guardedTestOriginal}}
	grace := int64(-1)
	typed.PrependReactor("delete", hubblePods, func(action ktesting.Action) (bool, runtime.Object, error) {
		opts := action.(ktesting.DeleteAction).GetDeleteOptions()
		if opts.Preconditions == nil || *opts.Preconditions.UID != guardedTestOriginal || opts.GracePeriodSeconds == nil || *opts.GracePeriodSeconds != -1 {
			t.Fatal(opts)
		}
		return true, nil, nil
	})
	if err := nativeClient.CoreV1().Pods(guardedTestNamespace).Delete(t.Context(), investigationAppRole, metav1.DeleteOptions{GracePeriodSeconds: &grace}); err != nil {
		t.Fatal(err)
	}
	if err := nativeClient.CoreV1().Pods(guardedTestNamespace).Delete(t.Context(), "unknown", metav1.DeleteOptions{}); err == nil {
		t.Fatal("unreviewed Pod was deleted")
	}
}
