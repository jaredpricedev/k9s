// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
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
	rolloutTestNamespace  = "delivery"
	rolloutTestName       = "checkout"
	rolloutTestUID        = "deployment-rollout-uid"
	rolloutTestRSUID      = "replicaset-current-uid"
	rolloutTestHistorical = "replicaset-historical-uid"
	rolloutTestAPI        = "apps/v1"
	rolloutTestImage      = "example.test/checkout:v2"
)

func rolloutTestDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": rolloutTestAPI, "kind": rolloutDeployKind,
		"metadata": map[string]any{"name": rolloutTestName, "namespace": rolloutTestNamespace, "uid": rolloutTestUID, "generation": int64(3)},
		"spec":     map[string]any{"replicas": int64(2), "selector": map[string]any{"matchLabels": map[string]any{"app": rolloutTestName}}, "template": rolloutTestTemplate(rolloutTestImage)},
		"status": map[string]any{"observedGeneration": int64(3), "updatedReplicas": int64(1), "readyReplicas": int64(1), "availableReplicas": int64(1), "replicas": int64(2),
			"conditions": []any{map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "full controller evidence [red] stays literal"}}},
	}}
}

func rolloutTestTemplate(image string) map[string]any {
	return map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": rolloutTestName}},
		"spec": map[string]any{"containers": []any{map[string]any{"name": testWorkspaceAPIName, "image": image}}}}
}

func rolloutTestReplicaSet(name, uid, deploymentUID, image string, controller bool) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": rolloutTestAPI, "kind": rolloutRSKind,
		"metadata": map[string]any{"name": name, "namespace": rolloutTestNamespace, "uid": uid, "labels": map[string]any{"app": rolloutTestName},
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "2"}},
		"spec":   map[string]any{"template": rolloutTestTemplate(image)},
		"status": map[string]any{"replicas": int64(1), "readyReplicas": int64(1), "availableReplicas": int64(1)},
	}}
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: rolloutTestAPI, Kind: rolloutDeployKind, Name: rolloutTestName, UID: types.UID(deploymentUID), Controller: &controller}})
	return object
}

func rolloutTestPod(name, owner string) *unstructured.Unstructured {
	controller := true
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": inspectionPodKind,
		"metadata": map[string]any{"name": name, "namespace": rolloutTestNamespace, "uid": "uid-" + name, "labels": map[string]any{"app": rolloutTestName}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": testWorkspaceAPIName, "image": rolloutTestImage}}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": testWorkspaceAPIName, "ready": true, "restartCount": int64(0),
			"imageID": "containerd://sha256:0123456789abcdef", "state": map[string]any{"running": map[string]any{}}}}},
	}}
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: rolloutTestAPI, Kind: rolloutRSKind, UID: types.UID(owner), Controller: &controller}})
	return object
}

func TestRolloutLoaderUsesExplicitNamespaceSelectorAndUIDOwnership(t *testing.T) {
	deployment := rolloutTestDeployment()
	current := rolloutTestReplicaSet("a-current", rolloutTestRSUID, rolloutTestUID, rolloutTestImage, true)
	historical := rolloutTestReplicaSet("z-historical", rolloutTestHistorical, rolloutTestUID, "example.test/checkout:v1", true)
	unrelated := rolloutTestReplicaSet("foreign", "foreign-uid", "different-deployment", rolloutTestImage, true)
	falseController := rolloutTestReplicaSet("non-controller", "non-controller-uid", rolloutTestUID, rolloutTestImage, false)
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		client.RsGVR.GVR(): "ReplicaSetList", client.PodGVR.GVR(): "PodList",
	}, deployment, current, historical, unrelated, falseController, rolloutTestPod("owned-pod", rolloutTestRSUID), rolloutTestPod("foreign-pod", "foreign-uid"))
	target := SelectedResourceTarget{Context: "review-context", GVR: client.DpGVR, Namespace: rolloutTestNamespace, Name: rolloutTestName, UID: rolloutTestUID}
	snapshot, err := loadRolloutReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
	require.NoError(t, err)
	require.Len(t, snapshot.Revisions, 2)
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, "owned-pod", snapshot.Pods[0].Identity.Name)
	require.Equal(t, rolloutTestImage, snapshot.Pods[0].Images[0].Declared)
	require.Contains(t, snapshot.Pods[0].Images[0].ImageID, "sha256:")
	require.Equal(t, "review-context", snapshot.Identity.Context)
	for _, action := range dyn.Actions() {
		require.Equal(t, rolloutTestNamespace, action.GetNamespace())
		if action.GetVerb() == "list" {
			list := action.(ktesting.ListAction)
			require.Equal(t, "app="+rolloutTestName, list.GetListRestrictions().Labels.String())
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			require.EqualValues(t, rolloutListPageSize, options.Limit)
		} else {
			require.Equal(t, "get", action.GetVerb(), "Rollout review must never issue a write")
		}
	}
	require.Len(t, dyn.Actions(), 3)
	preview := review.RolloutRecoveryPreview(snapshot, rolloutTestHistorical)
	require.Equal(t, review.RolloutNotExecuted, preview.State)
	require.True(t, preview.Comparison.Comparable)
	require.NotEmpty(t, preview.Comparison.Changes)
	require.Len(t, dyn.Actions(), 3, "Retained template review must not fetch again")
}

func TestRolloutLoaderRetainsDeniedCoverageAndDoesNotFetchUnscopedPods(t *testing.T) {
	deployment := rolloutTestDeployment()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		client.RsGVR.GVR(): "ReplicaSetList", client.PodGVR.GVR(): "PodList",
	}, deployment)
	dyn.PrependReactor("list", "replicasets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: client.DpGVR.G(), Resource: "replicasets"}, rolloutTestName, nil)
	})
	target := SelectedResourceTarget{GVR: client.DpGVR, Namespace: rolloutTestNamespace, Name: rolloutTestName, UID: rolloutTestUID}
	snapshot, err := loadRolloutReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
	require.NoError(t, err)
	require.Equal(t, inspect.ObservationDenied, snapshot.Coverage[1].State)
	require.Equal(t, inspect.ObservationIncomplete, snapshot.Coverage[2].State)
	require.Empty(t, snapshot.Revisions)
	deployment.SetUID("")
	noUID := fake.NewSimpleDynamicClient(runtime.NewScheme(), deployment)
	snapshot, err = loadRolloutReview(t.Context(), inspectionConnection{dynamic: noUID}, target)
	require.ErrorContains(t, err, "identity unavailable")
	require.Len(t, noUID.Actions(), 1, "Missing identity must not trigger a name-only ownership list")
	require.Nil(t, snapshot)
}

func TestRolloutCollectionStopsAtRetentionAndPageBounds(t *testing.T) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		client.RsGVR.GVR(): "ReplicaSetList", client.PodGVR.GVR(): "PodList",
	})
	listReads := 0
	dyn.PrependReactor("list", "replicasets", func(action ktesting.Action) (bool, runtime.Object, error) {
		listReads++
		options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		require.EqualValues(t, rolloutListPageSize, options.Limit)
		list := &unstructured.UnstructuredList{}
		list.SetContinue("continued")
		return true, list, nil
	})
	objects, coverage := collectRolloutObjects(t.Context(), dyn, client.RsGVR.GVR(), rolloutTestNamespace, "app="+rolloutTestName, "ReplicaSets", rolloutMaxRevisions, func(*unstructured.Unstructured) bool { return true })
	require.Empty(t, objects)
	require.Equal(t, rolloutMaxPages, listReads)
	require.Equal(t, inspect.ObservationIncomplete, coverage.State)
	dyn.PrependReactor("list", "replicasets", func(ktesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		for index := range rolloutMaxRevisions + 1 {
			list.Items = append(list.Items, *rolloutTestReplicaSet(fmt.Sprintf("revision-%d", index), fmt.Sprintf("revision-uid-%d", index), rolloutTestUID, rolloutTestImage, true))
		}
		return true, list, nil
	})
	objects, coverage = collectRolloutObjects(t.Context(), dyn, client.RsGVR.GVR(), rolloutTestNamespace, "app="+rolloutTestName, "ReplicaSets", rolloutMaxRevisions, func(object *unstructured.Unstructured) bool {
		return rolloutOwnedBy(object, rolloutDeployKind, rolloutTestUID)
	})
	require.Len(t, objects, rolloutMaxRevisions)
	require.Equal(t, inspect.ObservationIncomplete, coverage.State)
}

func rolloutViewFixture(t *testing.T) *rolloutReviewView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	v := &rolloutReviewView{Details: NewDetails(app, rolloutReviewTitle, rolloutTestNamespace+"/"+rolloutTestName, contentInspection, true),
		destinationRevision: app.Config.DestinationRevision(),
		target:              SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.DpGVR, Namespace: rolloutTestNamespace, Name: rolloutTestName, UID: rolloutTestUID}}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	current := rolloutTestReplicaSet("a-current", rolloutTestRSUID, rolloutTestUID, rolloutTestImage, true)
	historical := rolloutTestReplicaSet("z-historical", rolloutTestHistorical, rolloutTestUID, "example.test/checkout:v1", true)
	v.acceptSnapshot(review.NewRolloutSnapshot(rolloutTestDeployment(), []*unstructured.Unstructured{current, historical}, []*unstructured.Unstructured{rolloutTestPod("owned-pod", rolloutTestRSUID)},
		[]review.RolloutCoverage{{Source: "ReplicaSets", State: inspect.ObservationComplete}, {Source: "Pods", State: inspect.ObservationDenied, Detail: "Some Pod sources unavailable"}}, v.target.Context, time.Now().Add(-18*time.Second)), nil)
	return v
}

func TestRolloutTabsRetainQueryScrollRevisionAndFailedRefresh(t *testing.T) {
	v := rolloutViewFixture(t)
	reads := 0
	v.loader = func(context.Context, SelectedResourceTarget) (*review.RolloutSnapshot, error) {
		reads++
		return nil, fmt.Errorf("unexpected fetch")
	}
	v.cmdBuff.SetText("generation", "", true)
	v.BufferCompleted("generation", "")
	v.text.ScrollTo(4, 2)
	v.selectTab(1)
	v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	require.Equal(t, rolloutTestHistorical, v.selectedRevisionUID)
	v.reviewSelectedRevision(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Equal(t, rolloutRecoveryTab, v.activeTab)
	require.Contains(t, v.text.GetText(true), "NOT EXECUTED")
	require.Contains(t, v.text.GetText(true), "checkout:v1")
	v.selectTab(0)
	require.Equal(t, "generation", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 4, row)
	require.Equal(t, 2, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("refresh denied"))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.identityBar.GetText(true), "Refresh failed")
	v.StylesChanged(v.app.Styles)
	require.Equal(t, "generation", v.inspectionQuery)
	v.Stop()
	v.Start()
	require.Same(t, prior, v.snapshot)
	require.Equal(t, 0, reads, "Tabs, template selection and returning must reuse retained evidence")
	v.cmdBuff.SetActive(true)
	event := tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone)
	action, ok := v.actions.Get(ui.Key2)
	require.True(t, ok)
	require.Same(t, event, action.Action(event), "An empty active search must retain numeric input")
}

func TestRolloutOverviewFitsCompactAndWideTerminals(t *testing.T) {
	v := rolloutViewFixture(t)
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 34}} {
		screen := tcell.NewSimulationScreen("")
		require.NoError(t, screen.Init())
		screen.SetSize(size.width, size.height)
		v.SetRect(0, 0, size.width, size.height)
		v.Draw(screen)
		var b strings.Builder
		for y := range size.height {
			for x := range size.width {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		screen.Fini()
		for _, want := range []string{"BLOCKED", "ProgressDeadlineExceeded", "DESIRED", "UPDATED", "captured", "age", "Pods: denied", "5 Evidence"} {
			require.Contains(t, b.String(), want, "%dx%d", size.width, size.height)
		}
	}
}

func TestRolloutSelectionAndDestinationCannotExpandOrRevive(t *testing.T) {
	target := SelectedResourceTarget{GVR: client.DpGVR, Namespace: rolloutTestNamespace, Name: rolloutTestName, UID: rolloutTestUID}
	require.NoError(t, rolloutTargetError(target))
	target.Namespace = client.NamespaceAll
	require.ErrorContains(t, rolloutTargetError(target), "namespaced")
	target.Namespace = rolloutTestNamespace
	target.UID = ""
	require.ErrorContains(t, rolloutTargetError(target), "UID unavailable")
	target.UID = rolloutTestUID
	target.GVR = client.StsGVR
	require.NoError(t, rolloutTargetError(target))
	target.GVR = client.DsGVR
	require.NoError(t, rolloutTargetError(target))
	target.GVR = client.PodGVR
	require.ErrorContains(t, rolloutTargetError(target), "native apps/v1 Deployment")
	v := rolloutViewFixture(t)
	prior := v.snapshot
	initialRevision := v.destinationRevision
	originalNamespace := v.app.Config.ActiveNamespace()
	require.NoError(t, v.app.Config.SetActiveNamespace("another-namespace"))
	require.NoError(t, v.app.Config.SetActiveNamespace(originalNamespace))
	require.Greater(t, v.app.Config.DestinationRevision(), initialRevision)
	require.False(t, v.destinationCurrent(), "Changing away and back must not revive captured review ownership")
	v.refresh()
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.refreshFailure, "Destination changed")
}
