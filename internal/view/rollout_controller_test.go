// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const controllerRevisionKind = "ControllerRevision"

func rolloutControllerFixture(kind string) *unstructured.Unstructured {
	o := rolloutTestDeployment()
	o.SetKind(kind)
	_ = unstructured.SetNestedSlice(o.Object, nil, "status", "conditions")
	_ = unstructured.SetNestedField(o.Object, "RollingUpdate", "spec", "updateStrategy", "type")
	_ = unstructured.SetNestedField(o.Object, int64(2), "status", "updatedReplicas")
	_ = unstructured.SetNestedField(o.Object, int64(2), "status", "readyReplicas")
	_ = unstructured.SetNestedField(o.Object, int64(2), "status", "availableReplicas")
	if kind == "StatefulSet" {
		_ = unstructured.SetNestedField(o.Object, "checkout-2", "status", "currentRevision")
		_ = unstructured.SetNestedField(o.Object, "checkout-2", "status", "updateRevision")
	} else {
		for _, field := range []string{"desiredNumberScheduled", "updatedNumberScheduled", "currentNumberScheduled", "numberReady", "numberAvailable"} {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", field)
		}
		_ = unstructured.SetNestedField(o.Object, int64(0), "status", "numberMisscheduled")
	}
	return o
}

func rolloutControllerRevision(kind, name, uid, ownerUID string) *unstructured.Unstructured {
	o := rolloutTestReplicaSet(name, uid, ownerUID, rolloutTestImage, true)
	o.SetKind(controllerRevisionKind)
	template, _, _ := unstructured.NestedMap(o.Object, "spec", "template")
	_ = unstructured.SetNestedMap(o.Object, map[string]any{"spec": map[string]any{"template": template}}, "data")
	_ = unstructured.SetNestedField(o.Object, int64(2), "revision")
	refs := o.GetOwnerReferences()
	refs[0].Kind = kind
	o.SetOwnerReferences(refs)
	return o
}

func TestControllerRolloutLoaderScopesRevisionAndDirectPodOwnership(t *testing.T) {
	for _, targetGVR := range []*client.GVR{client.StsGVR, client.DsGVR} {
		t.Run(targetGVR.R(), func(t *testing.T) {
			target := SelectedResourceTarget{Context: "review-context", GVR: targetGVR, Namespace: rolloutTestNamespace, Name: rolloutTestName, UID: rolloutTestUID}
			kind := rolloutTargetKind(target)
			workload := rolloutControllerFixture(kind)
			pod := rolloutTestPod("owned-direct", rolloutTestUID)
			refs := pod.GetOwnerReferences()
			refs[0].Kind = kind
			pod.SetOwnerReferences(refs)
			otherPod := pod.DeepCopy()
			otherPod.SetName("foreign")
			otherPod.SetUID("foreign-pod")
			refs[0].UID = "foreign-controller"
			otherPod.SetOwnerReferences(refs)
			revision := rolloutControllerRevision(kind, "checkout-2", "controller-revision-uid", rolloutTestUID)
			foreign := rolloutControllerRevision(kind, "foreign-revision", "foreign-revision-uid", "foreign-controller")
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
			dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ControllerRevisionList", client.PodGVR.GVR(): "PodList"}, workload, pod, otherPod, revision, foreign)
			snapshot, err := loadRolloutReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
			require.NoError(t, err)
			require.Len(t, snapshot.Revisions, 1)
			require.Len(t, snapshot.Pods, 1)
			require.Equal(t, kind, snapshot.Kind)
			require.Equal(t, kind, snapshot.Pods[0].OwnerKind)
			require.Equal(t, string(target.UID), snapshot.Pods[0].OwnerUID)
			require.Empty(t, snapshot.Pods[0].ReplicaSetUID)
			for _, action := range dyn.Actions() {
				require.Equal(t, rolloutTestNamespace, action.GetNamespace())
				if action.GetVerb() == "list" {
					require.Equal(t, "app="+rolloutTestName, action.(ktesting.ListAction).GetListRestrictions().Labels.String())
				} else {
					require.Equal(t, "get", action.GetVerb())
				}
				require.NotEqual(t, "secrets", action.GetResource().Resource)
			}
			// Revision denial does not erase directly owned Pod evidence.
			dyn.PrependReactor("list", "controllerrevisions", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", nil)
			})
			snapshot, err = loadRolloutReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
			require.NoError(t, err)
			require.Empty(t, snapshot.Revisions)
			require.Len(t, snapshot.Pods, 1)
			require.Equal(t, inspect.ObservationDenied, snapshot.Coverage[1].State)
			require.Equal(t, inspect.ObservationComplete, snapshot.Coverage[2].State)
		})
	}
}

func TestControllerRolloutTargetRejectsForeignKindAndReplacedUID(t *testing.T) {
	target := SelectedResourceTarget{GVR: client.StsGVR, Namespace: rolloutTestNamespace, Name: rolloutTestName, UID: rolloutTestUID}
	for _, kind := range []string{rolloutDeployKind, "StatefulSet"} {
		o := rolloutControllerFixture(kind)
		if kind == "StatefulSet" {
			o.SetUID(types.UID("replacement"))
		}
		dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
		dyn.PrependReactor("get", "statefulsets", func(ktesting.Action) (bool, runtime.Object, error) { return true, o, nil })
		snapshot, err := loadRolloutReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
		require.Error(t, err)
		require.Nil(t, snapshot)
		require.Len(t, dyn.Actions(), 1)
	}
}

func TestControllerRolloutNativeFramesAndOutcomeLifecycle(t *testing.T) {
	v := rolloutViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	require.NoError(t, v.app.Styles.Load("../../skins/monochrome.yaml", false))
	v.app.Styles.Update()
	v.target.GVR = client.StsGVR
	workload := rolloutControllerFixture("StatefulSet")
	revision := rolloutControllerRevision("StatefulSet", "checkout-2", "controller-revision-uid", rolloutTestUID)
	snapshot := review.NewRolloutSnapshot(workload, []*unstructured.Unstructured{revision}, nil, nil, v.target.Context, time.Now().Add(-time.Minute))
	v.selectedRevisionUID = ""
	v.acceptSnapshot(snapshot, nil)
	for _, width := range []int{120, 80, 60, 40} {
		paint := drawnText(t, v, width, 24)
		require.Contains(t, paint, "StatefulSet")
		require.Contains(t, paint, "COMPLETE")
		require.Contains(t, paint, "w follow")
		require.Contains(t, paint, "Esc back")
	}
	v.selectTab(1)
	v.reviewSelectedRevision(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Contains(t, v.text.GetText(true), "Preview revision checkout-2")
	require.NotContains(t, v.text.GetText(true), "current Deployment")
	// Following is explicit; browsing/tabs never invokes the runner.
	calls := make(chan *review.RolloutOutcomeRequest, 1)
	v.follower = func(ctx context.Context, request *review.RolloutOutcomeRequest, _ func(*review.RolloutOutcome)) *review.RolloutOutcome {
		calls <- request
		<-ctx.Done()
		return &review.RolloutOutcome{State: review.RolloutCanceled, FinishedAt: time.Now()}
	}
	select {
	case <-calls:
		t.Fatal("Browsing started an outcome observer")
	default:
	}
	action, ok := v.actions.Get(ui.KeyW)
	require.True(t, ok)
	action.Action(tcell.NewEventKey(tcell.KeyRune, 'w', tcell.ModNone))
	select {
	case request := <-calls:
		require.Equal(t, snapshot.Identity, request.Identity)
		require.True(t, request.AcceptedAt.IsZero())
		require.Empty(t, request.OperationID)
	case <-time.After(time.Second):
		t.Fatal("Explicit following did not start")
	}
	for _, width := range []int{80, 60, 40} {
		paint := drawnText(t, v, width, 24)
		require.Contains(t, paint, "OUTCOME FOLLOW-UP")
		require.Contains(t, paint, "READ-ONLY FOLLOW-UP")
		require.Contains(t, paint, "w stop following")
		require.Contains(t, paint, "Esc back")
	}
	v.active = true
	generation := v.followGeneration
	v.acceptOutcome(generation-1, v.destinationRevision, &review.RolloutOutcome{State: review.RolloutComplete})
	require.Equal(t, review.RolloutProgressing, v.outcome.State, "Late callback must not overwrite the active outcome")
	observerCancel := v.followCancel
	finished := &review.RolloutOutcome{Identity: snapshot.Identity, Generation: 3, State: review.RolloutTimedOut, StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(), Reason: "No completion observed"}
	v.acceptOutcome(generation, v.destinationRevision, finished)
	require.Same(t, snapshot, v.snapshot)
	require.Equal(t, review.RolloutTimedOut, v.outcome.State)
	observerCancel()
	// A completed observer releases cancellation. Stop during a real observer
	// retains truthful unknown evidence and does not discard original children.
	v.following = true
	v.followCancel = func() {}
	v.Stop()
	require.Equal(t, review.RolloutCanceled, v.outcome.State)
	require.Same(t, snapshot, v.snapshot)
	v.selectTab(rolloutEvidenceTab)
	require.Contains(t, v.text.GetText(true), "OUTCOME FOLLOW-UP EVIDENCE")
	require.Contains(t, v.text.GetText(true), "controller-revision-uid")
}

func TestRolloutFollowingStopsOnDestinationChangeBeforeLateResult(t *testing.T) {
	v := rolloutViewFixture(t)
	v.active, v.following = true, true
	v.outcome = &review.RolloutOutcome{Identity: v.snapshot.Identity, Generation: 3, StartedAt: time.Now(), State: review.RolloutProgressing}
	canceled := false
	v.followCancel = func() { canceled = true }
	generation := v.followGeneration
	require.NoError(t, v.app.Config.SetActiveNamespace("changed-destination"))
	drawnText(t, v, 60, 24)
	require.True(t, canceled)
	require.False(t, v.following)
	require.Equal(t, review.RolloutCanceled, v.outcome.State)
	v.acceptOutcome(generation, v.destinationRevision, &review.RolloutOutcome{State: review.RolloutComplete})
	require.Equal(t, review.RolloutCanceled, v.outcome.State)
}
