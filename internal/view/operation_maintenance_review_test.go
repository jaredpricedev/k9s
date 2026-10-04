// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

const maintenanceReviewedPodUID = "reviewed-pod"
const maintenanceEvictionSubresource = "eviction"
const maintenanceNewScope = "new-scope"
const maintenanceControllerReferenceName = "owner"
const maintenanceControllerReferenceUID = "owner-uid"

func reviewedMaintenancePod(name string) *corev1.Pod {
	controller := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: guardedTestNamespace, Name: name, UID: types.UID(maintenanceReviewedPodUID + "-" + name),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: rolloutTestAPI, Kind: inspectionReplicaSetKind, Name: maintenanceControllerReferenceName, UID: maintenanceControllerReferenceUID, Controller: &controller}}},
		Spec: corev1.PodSpec{NodeName: testWorkspaceWorkerName}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func TestMaintenanceReviewedScopeRejectsNewAndRecreatedPodsBeforeCordon(t *testing.T) {
	for _, change := range []string{maintenanceNewScope, "recreated", "continuation", "wrong-node", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			session, target, dynamic, typed := maintenanceFixture(t)
			pod := reviewedMaintenancePod(investigationAppRole)
			reviewed := map[string]string{client.FQN(pod.Namespace, pod.Name): string(pod.UID)}
			if change == maintenanceNewScope {
				pod.Name = "new-pod"
			}
			if change == "recreated" {
				pod.UID = nativeReplacement
			}
			if change == "wrong-node" {
				pod.Spec.NodeName = maintenanceControllerReferenceName
				typed.PrependReactor(client.ListVerb, hubblePods, func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &corev1.PodList{Items: []corev1.Pod{*pod}}, nil
				})
			}
			if change == "duplicate" {
				typed.PrependReactor(client.ListVerb, hubblePods, func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &corev1.PodList{Items: []corev1.Pod{*pod, *pod}}, nil
				})
			}
			if err := typed.Tracker().Add(pod); err != nil {
				t.Fatal(err)
			}
			if change == "continuation" {
				typed.PrependReactor(client.ListVerb, hubblePods, func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "remaining"}, Items: []corev1.Pod{*pod}}, nil
				})
			}
			err := session.drainReviewed(t.Context(), target, dao.DrainOptions{GracePeriodSeconds: -1}, reviewed)
			if err == nil {
				t.Fatal("unreviewed scope was submitted")
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() == client.PatchVerb {
					t.Fatal("scope change cordoned Node")
				}
			}
			for _, action := range typed.Actions() {
				if action.GetSubresource() == maintenanceEvictionSubresource || action.GetVerb() == client.DeleteVerb {
					t.Fatal("scope change wrote a Pod")
				}
			}
		})
	}
}

func TestMaintenanceNodeReplacementAfterAcceptedCordonStopsPodWrites(t *testing.T) {
	session, target, dynamic, typed := maintenanceFixture(t)
	pod := reviewedMaintenancePod(investigationAppRole)
	if err := typed.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	dynamic.PrependReactor(client.PatchVerb, guardedTestNodes, func(ktesting.Action) (bool, runtime.Object, error) {
		obj, err := dynamic.Tracker().Get(client.NodeGVR.GVR(), "", target.Name)
		if err != nil {
			return true, nil, err
		}
		replacement := obj.(*unstructured.Unstructured).DeepCopy()
		replacement.SetUID("replacement-node")
		if err := dynamic.Tracker().Update(client.NodeGVR.GVR(), replacement, ""); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	task := startOperationBatch(time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, selected SelectedResourceTarget) error {
		return session.drainReviewed(ctx, selected, dao.DrainOptions{GracePeriodSeconds: -1}, map[string]string{client.FQN(pod.Namespace, pod.Name): string(pod.UID)})
	}, nil, nil)
	receipt := waitOperationTask(t, task)
	if receipt.Outcomes[0].State != operationFailed || len(receipt.Outcomes[0].AcceptedSteps) != 1 {
		t.Fatal("Node replacement lost accepted cordon or claimed success", receipt)
	}
	for _, action := range typed.Actions() {
		if action.GetSubresource() == maintenanceEvictionSubresource || action.GetVerb() == client.DeleteVerb {
			t.Fatal("replacement Node triggered a Pod write")
		}
	}
}

func TestMaintenanceDeniedEvictionRetainsAcceptedCordonWithoutRemoval(t *testing.T) {
	session, target, _, typed := maintenanceFixture(t)
	pod := reviewedMaintenancePod(investigationAppRole)
	if err := typed.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	typed.PrependReactor(client.CreateVerb, hubblePods, func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != maintenanceEvictionSubresource {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: hubblePods}, pod.Name, fmt.Errorf("eviction denied"))
	})
	task := startOperationBatch(time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, selected SelectedResourceTarget) error {
		return session.drainReviewed(ctx, selected, dao.DrainOptions{GracePeriodSeconds: -1}, map[string]string{client.FQN(pod.Namespace, pod.Name): string(pod.UID)})
	}, nil, nil)
	outcome := waitOperationTask(t, task).Outcomes[0]
	if outcome.State != operationFailed || len(outcome.AcceptedSteps) != 1 || strings.Contains(outcome.Output, "Observed Pod removal") {
		t.Fatal("denied eviction was presented as removal", outcome)
	}
}

func TestMaintenancePartialDrainKeepsAcceptedAndObservedEvidenceSeparate(t *testing.T) {
	session, target, _, typed := maintenanceFixture(t)
	first, second := reviewedMaintenancePod("api-a"), reviewedMaintenancePod("api-b")
	for _, pod := range []*corev1.Pod{first, second} {
		if err := typed.Tracker().Add(pod); err != nil {
			t.Fatal(err)
		}
	}
	var evictions atomic.Int32
	typed.PrependReactor(client.CreateVerb, hubblePods, func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != maintenanceEvictionSubresource {
			return false, nil, nil
		}
		evictions.Add(1)
		eviction := action.(ktesting.CreateAction).GetObject().(*policyv1.Eviction)
		if eviction.Name == second.Name {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: hubblePods}, second.Name, fmt.Errorf("eviction denied"))
		}
		if eviction.DeleteOptions == nil || eviction.DeleteOptions.Preconditions == nil || eviction.DeleteOptions.Preconditions.UID == nil || *eviction.DeleteOptions.Preconditions.UID != first.UID {
			return true, nil, fmt.Errorf("original Pod UID precondition missing")
		}
		return true, eviction, typed.Tracker().Delete(client.PodGVR.GVR(), first.Namespace, first.Name)
	})
	reviewed := map[string]string{client.FQN(first.Namespace, first.Name): string(first.UID), client.FQN(second.Namespace, second.Name): string(second.UID)}
	task := startOperationBatch(2*time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, selected SelectedResourceTarget) error {
		return session.drainReviewed(ctx, selected, dao.DrainOptions{GracePeriodSeconds: -1}, reviewed)
	}, nil, nil)
	outcome := waitOperationTask(t, task).Outcomes[0]
	if evictions.Load() != 2 || outcome.State != operationFailed || len(outcome.AcceptedSteps) != 2 || !strings.Contains(outcome.Output, "Observed Pod removal: "+first.Namespace+"/"+first.Name) {
		t.Fatal("partial drain evidence was flattened", evictions.Load(), outcome)
	}
	for _, step := range outcome.AcceptedSteps {
		if strings.Contains(step, second.Name) {
			t.Fatal("denied Pod eviction was claimed accepted", step)
		}
	}
}

func TestMaintenancePDBBlockingAndCancellationNeverBypassEviction(t *testing.T) {
	session, target, _, typed := maintenanceFixture(t)
	pod := reviewedMaintenancePod(investigationAppRole)
	if err := typed.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	typed.PrependReactor(client.CreateVerb, hubblePods, func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != maintenanceEvictionSubresource {
			return false, nil, nil
		}
		select {
		case started <- struct{}{}:
		default:
		}
		return true, nil, apierrors.NewTooManyRequests("PDB blocks eviction", 0)
	})
	task := startOperationBatch(2*time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, selected SelectedResourceTarget) error {
		return session.drainReviewed(ctx, selected, dao.DrainOptions{GracePeriodSeconds: -1}, map[string]string{client.FQN(pod.Namespace, pod.Name): string(pod.UID)})
	}, nil, nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native PDB admission was not attempted")
	}
	task.cancelRemaining()
	outcome := waitOperationTask(t, task).Outcomes[0]
	if outcome.State != operationUnknown || len(outcome.AcceptedSteps) != 1 || !strings.Contains(outcome.Output, "PDB blocks eviction") {
		t.Fatal("cancellation lost accepted cordon/PDB evidence", outcome)
	}
	for _, action := range typed.Actions() {
		if action.GetVerb() == client.DeleteVerb {
			t.Fatal("PDB blocking was bypassed with delete")
		}
	}
}
