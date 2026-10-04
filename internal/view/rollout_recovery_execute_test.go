// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	jsonpatch "github.com/evanphx/json-patch"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func recoveryViewFixture(t *testing.T) (*rolloutReviewView, *fake.FakeDynamicClient) {
	t.Helper()
	v := rolloutViewFixture(t)
	v.app.Content.Push(v)
	v.active = true
	t.Cleanup(v.app.Shutdown)
	o := rolloutTestDeployment()
	o.SetResourceVersion("11")
	r := rolloutTestReplicaSet("z-historical", rolloutTestHistorical, rolloutTestUID, "example.test/checkout:v1", true)
	r.SetResourceVersion("17")
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), o, r)
	dyn.PrependReactor("patch", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchAction)
		live, err := dyn.Tracker().Get(client.DpGVR.GVR(), rolloutTestNamespace, rolloutTestName)
		require.NoError(t, err)
		data, err := json.Marshal(live)
		require.NoError(t, err)
		decoded, err := jsonpatch.DecodePatch(patch.GetPatch())
		require.NoError(t, err)
		data, err = decoded.Apply(data)
		require.NoError(t, err)
		result := &unstructured.Unstructured{}
		require.NoError(t, result.UnmarshalJSON(data))
		result.SetGeneration(4)
		result.SetResourceVersion("18")
		for _, field := range []string{"observedGeneration", "updatedReplicas", "readyReplicas", "availableReplicas", "replicas"} {
			value := int64(2)
			if field == "observedGeneration" {
				value = 4
			}
			require.NoError(t, unstructured.SetNestedField(result.Object, value, "status", field))
		}
		require.NoError(t, unstructured.SetNestedSlice(result.Object, []any{}, "status", "conditions"))
		opts := patch.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		if len(opts.DryRun) == 0 {
			require.NoError(t, dyn.Tracker().Update(client.DpGVR.GVR(), result.DeepCopy(), rolloutTestNamespace))
		}
		return true, result, nil
	})
	typed := kfake.NewClientset()
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		request := action.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		require.Equal(t, rolloutTestNamespace, request.Spec.ResourceAttributes.Namespace)
		require.Equal(t, rolloutTestName, request.Spec.ResourceAttributes.Name)
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	v.recoverySession = func() (*operationSession, error) {
		return &operationSession{app: v.app, context: v.target.Context, revision: v.destinationRevision,
			dynamic: dyn, typed: typed, timeout: time.Second, stillCurrent: func() bool { return v.active && v.app.Content.Top() == v && v.destinationCurrent() }}, nil
	}
	v.selectedRevisionUID, v.selectionInvalidated = "", false
	v.acceptSnapshot(review.NewRolloutSnapshot(o, []*unstructured.Unstructured{r}, nil, nil, v.target.Context, time.Now()), nil)
	v.recoveryRevisionUID = rolloutTestHistorical
	v.selectTab(rolloutRecoveryTab)
	var err error
	v.recoveryPlan, err = review.PrepareRolloutRecovery(t.Context(), dyn, v.snapshot, rolloutTestHistorical)
	require.NoError(t, err)
	return v, dyn
}

func persistentRecoveryRequests(dyn *fake.FakeDynamicClient) int {
	count := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "patch" {
			continue
		}
		opts := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		if len(opts.DryRun) == 0 {
			count++
		}
	}
	return count
}

func TestRecoveryNativeCancelDefaultsAndNoAutomaticExecution(t *testing.T) {
	v, dyn := recoveryViewFixture(t)
	v.render()
	v.selectTab(0)
	v.selectTab(rolloutRecoveryTab)
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	require.NotNil(t, v.recoveryModal)
	_, button := v.recoveryForm.GetFocusedItemIndex()
	require.Equal(t, 0, button)
	v.recoveryForm.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
	require.Nil(t, v.recoveryModal)
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	v.recoveryForm.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, 0), func(tview.Primitive) {})
	require.Nil(t, v.recoveryModal)
	require.Zero(t, persistentRecoveryRequests(dyn))
	actionsBefore := len(dyn.Actions())
	v.prepareRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'x', 0))
	_, button = v.recoveryForm.GetFocusedItemIndex()
	require.Equal(t, 0, button)
	for _, width := range []int{80, 60, 40} {
		paint := drawnText(t, v.recoveryModal, width, 16)
		require.Contains(t, paint, "Cancel")
		require.Contains(t, paint, "Preview")
	}
	v.recoveryForm.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
	require.Len(t, dyn.Actions(), actionsBefore, "Canceling explicit preview must not make an API request")
}

func TestRecoveryNativeAcknowledgmentAndRetainedControllerReceipt(t *testing.T) {
	v, dyn := recoveryViewFixture(t)
	v.recoveryPlan.Unreviewed = true
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	for _, width := range []int{80, 60, 40} {
		paint := drawnText(t, v.recoveryModal, width, 16)
		require.Contains(t, paint, "Cancel")
		require.Contains(t, paint, "Apply")
		require.Contains(t, paint, "Unreviewed fields")
	}
	form := v.recoveryForm
	form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
	require.NotNil(t, v.recoveryModal)
	form.GetFormItem(0).(*tview.Checkbox).InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', 0), func(tview.Primitive) {})
	form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
	require.Nil(t, v.recoveryModal)
	tasks := v.app.operations.list()
	require.Len(t, tasks, 1)
	select {
	case <-tasks[0].finished:
	case <-time.After(time.Second):
		t.Fatal("Guarded native recovery/observation did not finish")
	}
	receipt := tasks[0].receipt()
	require.Equal(t, operationAccepted, receipt.Outcomes[0].State)
	require.NotEmpty(t, receipt.Outcomes[0].AcceptedSteps)
	require.Contains(t, receipt.Outcomes[0].Output, "CONTROLLER OUTCOME: complete")
	require.Contains(t, receipt.Outcomes[0].Output, "generation 4")
	require.Contains(t, receipt.Outcomes[0].Output, "template SHA256")
	require.Equal(t, 1, persistentRecoveryRequests(dyn))
	for _, action := range dyn.Actions() {
		require.Equal(t, rolloutTestNamespace, action.GetNamespace())
		require.NotEqual(t, "list", action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
}

func TestRecoveryLateFormsReadonlyChangedSelectionAndDestinationCannotSubmit(t *testing.T) {
	for _, change := range []func(*rolloutReviewView){
		func(v *rolloutReviewView) { v.app.Config.K9s.ReadOnly = true },
		func(v *rolloutReviewView) { v.selectedRevisionUID = rolloutTestRSUID },
		func(v *rolloutReviewView) { require.NoError(t, v.app.Config.SetActiveNamespace("changed")) },
		func(v *rolloutReviewView) { v.app.Content.Push(NewDetails(v.app, "next", "", contentTXT, false)) },
	} {
		v, dyn := recoveryViewFixture(t)
		v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
		form := v.recoveryForm
		require.NotNil(t, form)
		if form.GetFormItemCount() > 0 {
			form.GetFormItem(0).(*tview.Checkbox).InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', 0), func(tview.Primitive) {})
		}
		change(v)
		form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
		require.Zero(t, persistentRecoveryRequests(dyn))
		require.Empty(t, v.app.operations.list())
	}
}

func TestRecoveryOwnershipWarningAndReplacedConfirmationCannotSubmit(t *testing.T) {
	v, dyn := recoveryViewFixture(t)
	v.recoveryPlan.OwnershipMarkers = []string{"Argo CD tracking present (unverified metadata)"}
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	paint := drawnText(t, v.recoveryModal, 120, 24)
	require.Contains(t, paint, "Ownership metadata is unverified.", "Ownership warning must be visible before paging")
	for range 5 {
		v.recoveryModal.InputHandler()(tcell.NewEventKey(tcell.KeyPgDn, 0, 0), func(tview.Primitive) {})
		frame := drawnText(t, v.recoveryModal, 120, 24)
		require.Contains(t, frame, "ctx "+v.target.Context, "Destination stays pinned while paging")
		require.Contains(t, frame, "Cancel")
		require.Contains(t, frame, "Apply")
		paint += frame
	}
	for _, text := range []string{"Target UID " + rolloutTestUID, "Source template SHA256", "Ownership metadata is unverified.", "Another controller may reconcile", "ConfigMap/Secret contents"} {
		require.Contains(t, paint, text)
	}
	v.selectTab(rolloutEvidenceTab)
	require.Contains(t, v.text.GetText(true), "Argo CD tracking present (unverified metadata)")
	v.selectTab(rolloutRecoveryTab)
	oldForm := v.recoveryForm
	if oldForm.GetFormItemCount() > 0 {
		oldForm.GetFormItem(0).(*tview.Checkbox).InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', 0), func(tview.Primitive) {})
	}
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	newForm := v.recoveryForm
	require.NotSame(t, oldForm, newForm)
	oldForm.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
	require.Same(t, newForm, v.recoveryForm)
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
}

func TestRecoveryNewPreparationClearsPreviousExecutablePlan(t *testing.T) {
	v, dyn := recoveryViewFixture(t)
	v.recoverySession = func() (*operationSession, error) { return nil, nil }
	v.prepareRecovery()
	require.Nil(t, v.recoveryPlan)
	require.Contains(t, v.recoveryNotice, "previous server plan cleared")
	v.applyRecoveryCmd(tcell.NewEventKey(tcell.KeyRune, 'a', 0))
	require.Nil(t, v.recoveryModal)
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
}
