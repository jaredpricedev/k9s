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
	"github.com/derailed/k9s/internal/maintenance"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

const maintenanceAcceptedCordon = "node unschedulable=true"

func maintenanceViewFixture(t *testing.T) *maintenanceView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.NodeGVR, Name: testWorkspaceWorkerName, UID: guardedTestNodeUID}
	v := &maintenanceView{Details: NewDetails(app, maintenanceTitle, target.Name, contentInspection, true), target: target, revision: app.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(&maintenance.Snapshot{Identity: inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Name: target.Name, UID: string(target.UID)}, CapturedAt: time.Now(),
		Node:     maintenance.Node{Ready: "True", ResourceVersion: "12"},
		Pods:     []maintenance.Pod{{Identity: inspect.ResourceIdentity{Context: target.Context, Namespace: guardedTestNamespace, Name: investigationAppRole, UID: "reviewed-pod"}, Phase: string(corev1.PodRunning), Ready: maintenance.ReadyStateReady, Owner: "ReplicaSet/api", OwnerKind: inspectionReplicaSetKind, GraceSeconds: 30, EmptyDir: []string{"scratch"}, BudgetEvidence: []string{"Potential blocker: reported allowance 0"}, Constraints: []string{"nodeSelector: disk=ssd"}}},
		Coverage: []maintenance.Coverage{{Source: maintenance.PodsSource, State: maintenance.Complete, Count: 1, Limit: maintenance.MaxPods}, {Source: maintenance.BudgetsSource, Scope: guardedTestNamespace, State: maintenance.Denied, Detail: "PDB access denied"}}}, nil)
	return v
}

func TestMaintenanceDrainUsesMarkedNodeWhenCursorMoves(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	node := NewNode(client.NodeGVR).(*Node)
	table := node.GetTable()
	table.app = app
	table.SetModel(&mockTableModel{})
	table.SetCell(0, 0, tview.NewTableCell("NAME"))
	table.SetCell(1, 0, tview.NewTableCell(testWorkspaceWorkerName).SetReference(testWorkspaceWorkerName))
	table.SetCell(2, 0, tview.NewTableCell(maintenanceControllerReferenceName).SetReference(maintenanceControllerReferenceName))
	table.Select(1, 0)
	table.ToggleMark()
	table.Select(2, 0)
	// The marked Node has a retained relationship from another context. Review
	// must enforce that anchor even when the cursor has moved to another row.
	table.expectedTarget = &SelectedResourceTarget{Context: testWorkspacePriorContext, GVR: client.NodeGVR, Name: testWorkspaceWorkerName, UID: guardedTestNodeUID}
	require.Nil(t, node.drainCmd(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone)))
	message := <-app.Flash().Channel()
	require.Contains(t, message.Text, "Context changed; reopen the relationship")
	require.Equal(t, maintenanceControllerReferenceName, table.GetSelectedItem(), "review must preserve the list cursor")
	require.Empty(t, app.operations.list())
}

func TestMaintenanceNativeFramesPreserveIdentityStatusAndActions(t *testing.T) {
	v := maintenanceViewFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}, {40, 8}, {60, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		require.Contains(t, frame, "Maintenance / "+v.target.Name)
		require.Contains(t, frame, "partial evidence")
		require.Contains(t, frame, "Overview")
		require.Contains(t, frame, "d drain")
		require.Contains(t, frame, "r refresh")
		require.Contains(t, frame, "Esc back")
		require.Contains(t, frame, "1 affected Pods")
	}
	v.selectTab(3)
	for _, width := range []int{80, 60, 40} {
		frame := drawnText(t, v, width, 24)
		require.Contains(t, frame, "Constraints")
		require.Contains(t, frame, "disk=ssd")
	}
}

func TestMaintenanceReadOnlyAndTextEntryCannotSubmit(t *testing.T) {
	v := maintenanceViewFixture(t)
	v.app.Config.K9s.ReadOnly = true
	v.session = &operationSession{app: v.app, context: v.target.Context, revision: v.revision, stillCurrent: func() bool { return true }}
	require.False(t, v.canMaintain(true))
	require.Empty(t, v.app.operations.list())
	v.render()
	require.Contains(t, v.identityBar.GetText(true), "Read only")
	v.cmdBuff.SetText("maintenance query", "", true)
	v.cmdBuff.SetActive(true)
	runs := 0
	event := tcell.NewEventKey(tcell.KeyRune, 'd', tcell.ModNone)
	require.Same(t, event, v.keyAction(func() { runs++ })(event))
	require.Zero(t, runs)
}

func TestMaintenanceTabsRetainSearchScrollAndFailedRefreshIdentity(t *testing.T) {
	v := maintenanceViewFixture(t)
	reads := 0
	v.loader = func(context.Context, *SelectedResourceTarget) (*maintenance.Snapshot, error) {
		reads++
		return nil, fmt.Errorf("unexpected read")
	}
	v.BufferCompleted("Node", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(1)
	v.selectTab(0)
	require.Equal(t, "Node", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("refresh denied"))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, strings.Join(v.model.Peek(), "\n"), "Previous captured evidence retained")
	v.StylesChanged(v.app.Styles)
	v.Stop()
	v.Start()
	require.Zero(t, reads)
	replacement := *prior
	replacement.Identity.UID = nativeReplacement
	v.acceptSnapshot(&replacement, nil)
	require.Same(t, prior, v.snapshot)
	require.Equal(t, guardedTestNodeUID, string(v.target.UID))
	v.revision++
	v.refresh()
	require.Zero(t, reads)
	require.Contains(t, v.identityBar.GetText(true), "destination changed")
}

func TestMaintenanceReceiptSeparatesPartialAcceptanceFromRecovery(t *testing.T) {
	v := maintenanceViewFixture(t)
	task := newOperationTask(time.Second, []SelectedResourceTarget{v.target})
	t.Cleanup(task.cancel)
	task.id, task.action, task.context = 7, "Drain", v.target.Context
	task.started = v.snapshot.CapturedAt.Add(time.Second)
	task.outcomes[0] = operationOutcome{Target: v.target, State: operationFailed, Err: fmt.Errorf("eviction denied"), AcceptedSteps: []string{maintenanceAcceptedCordon}}
	v.task, v.reviewedPods = task, map[string]string{guardedTestNamespace + "/" + investigationAppRole: "reviewed-pod"}
	text := v.renderOutcomes()
	require.Contains(t, text, "ACCEPTED: "+maintenanceAcceptedCordon)
	require.Contains(t, text, "eviction denied")
	require.Contains(t, text, "No fresh post-start observation")
	v.snapshot.CapturedAt = task.started.Add(time.Second)
	v.snapshot.Node.Unschedulable = true
	text = v.renderOutcomes()
	require.Contains(t, text, "1 original reviewed Pod UIDs still observed")
	require.Contains(t, text, "not proof of deletion or replacement readiness")
	v.snapshot.Pods = nil
	text = v.renderOutcomes()
	require.Contains(t, text, "0 original reviewed Pod UIDs")
	v.snapshot.Coverage[0].State = maintenance.Partial
	require.Contains(t, v.renderOutcomes(), "cannot be counted")
	v.cancelOperation()
	require.True(t, task.receipt().CancelRequested)
	require.Contains(t, v.renderOutcomes(), "accepted changes remain")
}

func TestMaintenanceCancelDrainOptionsHasNoWriteOrReceipt(t *testing.T) {
	v := maintenanceViewFixture(t)
	operation, _, dynamic, typed := maintenanceFixture(t)
	operation.app, operation.context, operation.revision = v.app, v.target.Context, v.revision
	operation.stillCurrent = func() bool { return true }
	v.session = operation
	origin := NewBrowser(client.NodeGVR).(*Browser)
	origin.app = v.app
	v.origin = origin
	v.app.Content.Pages.AddPage("maintenance-fixture", v, true, true)
	v.drainOptions()
	modal := v.app.Content.GetPrimitive(drainKey)
	require.NotNil(t, modal)
	var form *tview.Form
	modal.Focus(func(primitive tview.Primitive) { form = primitive.(*tview.Form) })
	require.NotNil(t, form)
	pressDrainButton(t, v.app, form, "Cancel")
	require.Nil(t, v.task)
	require.Empty(t, v.app.operations.list())
	require.Empty(t, dynamic.Actions())
	require.Empty(t, typed.Actions())
}

func TestMaintenanceNativeRendererEscapesEvidenceOnceAndShowsRunningCancel(t *testing.T) {
	v := maintenanceViewFixture(t)
	v.target.Context += "\x1b[31m"
	v.renderChrome()
	require.NotContains(t, v.identityBar.GetText(true), "\x1b")
	v.snapshot.Coverage[1].Detail = "Denied [red] literal evidence"
	v.selectTab(5)
	frame := drawnText(t, v, 120, 34)
	require.Contains(t, frame, "Denied [red] literal evidence")
	require.NotContains(t, frame, "[red[]")
	task := newOperationTask(time.Second, []SelectedResourceTarget{v.target})
	t.Cleanup(task.cancel)
	task.action, task.context = "Drain", v.target.Context
	task.outcomes[0].State = operationRunning
	task.outcomes[0].AcceptedSteps = []string{maintenanceAcceptedCordon}
	v.task = task
	v.selectTab(4)
	frame = drawnText(t, v, 40, 12)
	require.Contains(t, frame, "RUNNING")
	require.Contains(t, frame, "x cancel remaining")
	require.Contains(t, v.renderOutcomes(), "1 acknowledged steps so far")
}

func TestMaintenanceUnknownOutcomeRequiresFreshPostOutcomeObservation(t *testing.T) {
	v := maintenanceViewFixture(t)
	v.session = &operationSession{app: v.app, context: v.target.Context, revision: v.revision, stillCurrent: func() bool { return true }}
	task := newOperationTask(time.Second, []SelectedResourceTarget{v.target})
	t.Cleanup(task.cancel)
	task.outcomes[0].State = operationUnknown
	task.ended = v.snapshot.CapturedAt.Add(time.Second)
	v.task = task
	require.False(t, v.canMaintain(true), "unknown outcomes must not casually retry from the pre-operation snapshot")
	v.snapshot.CapturedAt = task.ended.Add(time.Second)
	require.True(t, v.canMaintain(true), "explicit fresh observation allows the next reviewed confirmation")
	require.Empty(t, v.app.operations.list(), "read-only checks never submit an operation")
}
