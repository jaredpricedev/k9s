// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const changeSetTestSensitive = "confidential-change-set-fixture"
const changeSetTestNamespace = "change-team"
const changeSetTestName = "checkout"
const changeSetNamespaceCase = "namespace"

func changeSetViewFixture(t *testing.T) (*changeSetView, *fake.FakeDynamicClient) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, activateErr := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, activateErr)
	t.Cleanup(app.Shutdown)
	documents := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: checkout, namespace: change-team}\nspec: {replicas: 2}\n" +
		"---\napiVersion: apps/v1\nkind: Deployment\nmetadata: {name: denied-worker, namespace: change-team}\nspec: {replicas: 2}\n" +
		"---\napiVersion: v1\nkind: Secret\nmetadata: {name: credentials, namespace: change-team}\nstringData: {password: " + changeSetTestSensitive + "}\n"
	path := filepath.Join(t.TempDir(), "change-set.yaml")
	require.NoError(t, os.WriteFile(path, []byte(documents), 0600))
	source, err := review.LoadSource(t.Context(), path)
	require.NoError(t, err)
	scope := review.Scope{Context: app.Config.ActiveContextName(), Namespaces: []string{changeSetTestNamespace}, DefaultNamespace: changeSetTestNamespace}
	live := []runtime.Object{}
	for _, name := range []string{changeSetTestName, "denied-worker"} {
		live = append(live, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{
			"name": name, changeSetNamespaceCase: changeSetTestNamespace, "uid": "uid-" + name, "resourceVersion": "9", "generation": int64(1)}, "spec": map[string]any{"replicas": int64(1)}}})
	}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), live...)
	dyn.PrependReactor("patch", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchAction)
		current, lookupErr := dyn.Tracker().Get(action.GetResource(), action.GetNamespace(), patch.GetName())
		require.NoError(t, lookupErr)
		admitted := current.(*unstructured.Unstructured).DeepCopy()
		var payload map[string]any
		require.NoError(t, json.Unmarshal(patch.GetPatch(), &payload))
		spec, _, _ := unstructured.NestedMap(payload, "spec")
		require.NoError(t, unstructured.SetNestedMap(admitted.Object, spec, "spec"))
		admitted.SetResourceVersion("10")
		admitted.SetGeneration(2)
		opts := action.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
		require.Equal(t, review.ChangeSetFieldManager, opts.FieldManager)
		require.Nil(t, opts.Force)
		if len(opts.DryRun) == 0 {
			require.NoError(t, dyn.Tracker().Update(action.GetResource(), admitted.DeepCopy(), action.GetNamespace()))
		} else {
			require.Equal(t, []string{metav1.DryRunAll}, opts.DryRun)
		}
		return true, admitted, nil
	})
	v := newChangeSetView(app, source, scope, nil)
	require.NoError(t, v.Init(t.Context()))
	app.Content.Push(v)
	v.active = true
	v.resolve = func(context.Context, string, string) (review.Mapping, error) {
		return review.Mapping{GVR: client.DpGVR.GVR(), Namespaced: true}, nil
	}
	v.owner = func(ctx context.Context, identity *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
		return gitops.Collect(ctx, &gitopsReader{dynamic: dyn}, &gitops.Request{Target: *identity})
	}
	typed := kfake.NewClientset()
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		request := action.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		require.Equal(t, changeSetTestNamespace, request.Spec.ResourceAttributes.Namespace)
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: request.Spec.ResourceAttributes.Name != "denied-worker"}}, nil
	})
	v.session = &operationSession{app: app, context: v.contextName, revision: v.destinationRevision, dynamic: dyn, typed: typed, timeout: time.Second,
		stillCurrent: func() bool { return v.active && app.Content.Top() == v && v.destinationCurrent() }}
	v.preparer = func(ctx context.Context) (*review.ChangeSetPlan, error) {
		return review.PrepareChangeSet(ctx, dyn, v.resolve, v.owner, v.source, v.scope)
	}
	v.plan, err = v.preparer(t.Context())
	require.NoError(t, err)
	require.Equal(t, review.ChangeSetPrepared, v.plan.Entries[0].State)
	v.render()
	return v, dyn
}

func pressChangeSetButton(form *tview.Form, index int) {
	form.GetButton(index).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(tview.Primitive) {})
}
func acknowledgeChangeSet(form *tview.Form) {
	form.GetFormItem(0).(*tview.Checkbox).InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', 0), func(tview.Primitive) {})
}

func TestChangeSetNativeFramesTabsSelectionAndReturnState(t *testing.T) {
	v, dyn := changeSetViewFixture(t)
	for _, size := range []struct{ width, height int }{{120, 34}, {80, 24}, {60, 24}, {40, 16}, {40, 12}, {39, 12}, {40, 8}} {
		frame := drawnText(t, v, size.width, size.height)
		if size.width < ui.MinTaskWidth || size.height < ui.MinTaskHeight {
			require.Contains(t, frame, "40x12")
			continue
		}
		for _, text := range []string{changeSetTestName, "uid-checkout", "SHA", "Plan", "PREVIEW", "Esc back"} {
			require.Contains(t, frame, text, "frame %dx%d:\n%s", size.width, size.height, frame)
		}
	}
	for tab, heading := range map[int]string{1: "REVIEWED TARGET CHANGES", 2: "OWNERSHIP ROUTING", 3: "PER-TARGET OPERATION", 4: "RETAINED CHANGE-SET"} {
		v.selectTab(tab)
		require.Contains(t, drawnText(t, v, 40, 16), heading)
	}
	v.selectTab(0)
	v.table.Select(2, 0)
	v.tableKey(tcell.NewEventKey(tcell.KeyRune, ' ', 0))
	require.True(t, v.selected[1])
	require.Equal(t, 1, v.focusedEntry)
	v.BufferCompleted("checkout", "")
	require.Len(t, v.rows, 1)
	v.BufferCompleted("", "")
	require.Len(t, v.rows, 3)
	v.selectTab(1)
	v.BufferCompleted("replicas", "")
	v.text.ScrollTo(3, 0)
	v.selectTab(2)
	v.selectTab(1)
	require.Equal(t, "replicas", v.inspectionQuery)
	row, _ := v.text.GetScrollOffset()
	require.Equal(t, 3, row)
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
}

func TestChangeSetNativeCancelAcknowledgmentPartialBatchAndRetainedReceipt(t *testing.T) {
	v, dyn := changeSetViewFixture(t)
	for index := range v.plan.Entries {
		v.selected[index] = true
	}
	v.confirmApply()
	require.NotNil(t, v.form)
	item, button := v.form.GetFocusedItemIndex()
	require.Equal(t, -1, item)
	require.Equal(t, 0, button)
	for _, width := range []int{80, 60, 40} {
		frame := drawnText(t, v.modal, width, 16)
		require.Contains(t, frame, "Cancel")
		require.Contains(t, frame, "Apply")
		require.Contains(t, frame, "Unreviewed fields")
	}
	form := v.form
	pressChangeSetButton(form, 1)
	require.Zero(t, persistentRecoveryRequests(dyn))
	require.Empty(t, v.app.operations.list())
	require.NotNil(t, v.form)
	pressChangeSetButton(form, 0)
	require.Nil(t, v.form)
	v.confirmApply()
	acknowledgeChangeSet(v.form)
	pressChangeSetButton(v.form, 1)
	require.NotNil(t, v.task)
	select {
	case <-v.task.finished:
	case <-time.After(time.Second):
		t.Fatal("native batch did not finish")
	}
	receipt := v.task.receipt()
	require.Len(t, receipt.Outcomes, 3)
	require.Equal(t, operationObserved, receipt.Outcomes[0].State)
	require.Equal(t, operationFailed, receipt.Outcomes[1].State)
	require.Equal(t, operationNotSubmitted, receipt.Outcomes[2].State)
	require.Len(t, receipt.Outcomes[0].AcceptedSteps, 1)
	require.Contains(t, receipt.Outcomes[0].Output, "independent named API GET")
	require.Contains(t, receipt.Outcomes[0].Output, "generation 2")
	require.Equal(t, 1, persistentRecoveryRequests(dyn))
	text := operationReceiptText(receipt, 1, 1)
	require.NotContains(t, text, changeSetTestSensitive)
	require.Contains(t, text, "runtime/controller readiness remains separate")
	v.Stop()
	require.Len(t, v.app.operations.list(), 1)
	for _, action := range dyn.Actions() {
		require.NotEqual(t, "secrets", action.GetResource().Resource)
		require.NotEqual(t, "list", action.GetVerb())
	}
}

func TestChangeSetReadonlyChangedDestinationSelectionAndLateFormsCannotSubmit(t *testing.T) {
	for _, change := range []func(*changeSetView){
		func(v *changeSetView) { v.app.Config.K9s.ReadOnly = true },
		func(v *changeSetView) { require.NoError(t, v.app.Config.SetActiveNamespace("new-namespace")) },
		func(v *changeSetView) { v.destinationRevision++ },
		func(v *changeSetView) { _, err := v.app.Config.ActivateContext("ct-1-2"); require.NoError(t, err) },
		func(v *changeSetView) { v.selected[1] = true },
		func(v *changeSetView) { v.Stop() },
	} {
		v, dyn := changeSetViewFixture(t)
		v.selected[0] = true
		v.confirmApply()
		form := v.form
		acknowledgeChangeSet(form)
		change(v)
		pressChangeSetButton(form, 1)
		require.Zero(t, persistentRecoveryRequests(dyn))
		require.Empty(t, v.app.operations.list())
	}
	v, dyn := changeSetViewFixture(t)
	v.selected[0] = true
	v.confirmApply()
	old := v.form
	acknowledgeChangeSet(old)
	v.confirmApply()
	pressChangeSetButton(old, 1)
	require.Empty(t, v.app.operations.list())
	require.Zero(t, persistentRecoveryRequests(dyn))
	v.app.Config.K9s.ReadOnly = true
	v.confirmPreparation()
	require.False(t, v.loading)
}

func TestChangeSetQueuedPreparationRejectsCoveredStoppedAndChangedNamespace(t *testing.T) {
	for _, scenario := range []string{"covered", "stopped", changeSetNamespaceCase, "generation"} {
		t.Run(scenario, func(t *testing.T) {
			v, _ := changeSetViewFixture(t)
			original := v.plan
			queued := make(chan func(), 1)
			v.enqueue = func(callback func()) { queued <- callback }
			v.app.SetRunning(true)
			t.Cleanup(func() { v.app.SetRunning(false) })
			v.preparer = func(context.Context) (*review.ChangeSetPlan, error) { return original, nil }
			v.prepare()
			require.Nil(t, v.plan)
			var callback func()
			select {
			case callback = <-queued:
			case <-time.After(time.Second):
				t.Fatal("preparation callback not queued")
			}
			switch scenario {
			case "covered":
				v.app.Content.Push(NewDetails(v.app, "next", "", contentTXT, false))
			case "stopped":
				v.Stop()
			case changeSetNamespaceCase:
				v.workspaceNamespace = "changed"
			case "generation":
				v.generation++
			}
			callback()
			require.Nil(t, v.plan)
		})
	}
}

func TestChangeSetOperationObservationRequiresSuccessfulIndependentRead(t *testing.T) {
	for _, cancelAfterAccepted := range []bool{false, true} {
		task := newOperationTask(time.Second, []SelectedResourceTarget{{Context: "captured", GVR: client.DpGVR, Namespace: changeSetTestNamespace, Name: changeSetTestName, UID: "pinned"}})
		task.start(func(ctx context.Context, _ SelectedResourceTarget) error {
			operationBeginWrite(ctx)
			operationAcceptWrite(ctx, "API accepted UID pinned RV 9 generation 2")
			if cancelAfterAccepted {
				task.cancel()
				return errors.Join(review.ErrChangeSetOutcomeUnknown, ctx.Err())
			}
			operationObserveWrite(ctx)
			fmt.Fprintln(operationOutput(ctx), "independent named GET · RV10 · generation2 · source/time captured")
			return nil
		}, nil, nil)
		select {
		case <-task.finished:
		case <-time.After(time.Second):
			t.Fatal("observation task did not finish")
		}
		receipt := task.receipt()
		require.Len(t, receipt.Outcomes[0].AcceptedSteps, 1)
		if cancelAfterAccepted {
			require.Equal(t, operationUnknown, receipt.Outcomes[0].State)
		} else {
			require.Equal(t, operationObserved, receipt.Outcomes[0].State)
		}
	}
}

func TestChangeSetDisconnectedWorkspaceRetainsPlanAndWorker(t *testing.T) {
	v, _ := changeSetViewFixture(t)
	v.loading = true
	v.generation = 7
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v.cancel = cancel
	require.True(t, retainedDisconnectedWorkspace(v))
	v.app.Content.Pages.AddPage("current-change-set", v, true, true)
	for range 3 {
		v.app.connectivityComponent(v, false)
	}
	require.NoError(t, ctx.Err())
	require.True(t, v.active)
	require.True(t, v.loading)
	require.Equal(t, uint64(7), v.generation)
	require.NotNil(t, v.plan)
	require.NotContains(t, strings.Join(v.model.Peek(), "\n"), changeSetTestSensitive)
}

func TestChangeSetBrowsingAndCancelNeverAutomaticallyPrepareAdmission(t *testing.T) {
	v, dyn := changeSetViewFixture(t)
	v.plan = nil
	before := len(dyn.Actions())
	preparations := 0
	v.preparer = func(context.Context) (*review.ChangeSetPlan, error) {
		preparations++
		return nil, errors.New("unexpected automatic preparation")
	}
	v.Stop()
	v.Start()
	for tab := range changeSetTabs {
		v.selectTab(tab)
		drawnText(t, v, 40, 16)
	}
	require.Zero(t, preparations)
	require.Len(t, dyn.Actions(), before)
	v.confirmPreparation()
	require.NotNil(t, v.form)
	item, button := v.form.GetFocusedItemIndex()
	require.Equal(t, -1, item)
	require.Equal(t, 0, button)
	pressChangeSetButton(v.form, 0)
	require.Zero(t, preparations)
	require.Len(t, dyn.Actions(), before)
}
