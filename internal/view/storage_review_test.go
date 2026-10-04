// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/storage"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func storageViewFixture(t *testing.T) *storageView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PvcGVR, Namespace: "apps", Name: "data", UID: "pvc-uid"}
	v := &storageView{Details: NewDetails(app, storageTitle, target.Path(), contentInspection, true), target: target, destinationRevision: app.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	app.Content.Push(v)
	v.Start()
	t.Cleanup(v.Stop)
	yes := true
	class := "fast"
	now := time.Now()
	pvcID := inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Namespace: "apps", Name: "data", UID: "pvc-uid"}
	snapshot := &storage.Snapshot{Scope: storage.Scope{Identity: pvcID, Namespace: "apps", PVCName: "data"}, CapturedAt: now,
		PVCs:     []storage.PVC{{Identity: pvcID, ResourceVersion: "20", Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("12Gi")}, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}, Phase: corev1.ClaimBound, Volume: "data-pv", Class: &class, Conditions: []corev1.PersistentVolumeClaimCondition{{Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue}}}},
		PVs:      []storage.PV{{Identity: inspect.ResourceIdentity{Context: target.Context, Name: "data-pv", UID: "pv-uid"}, Claim: &corev1.ObjectReference{Namespace: "apps", Name: "data", UID: "pvc-uid"}}},
		Classes:  []storage.Class{{Identity: inspect.ResourceIdentity{Context: target.Context, Name: class, UID: "sc-uid"}, ResourceVersion: "21", Provisioner: "fixture-csi", AllowExpansion: &yes}},
		Coverage: []capacity.Coverage{{Source: storage.SourcePVCs, State: capacity.Complete}, {Source: storage.SourceDrivers, State: capacity.Denied, Detail: "CSI access denied"}, {Source: storage.SourceUsage, State: capacity.NotConfigured}},
	}
	v.acceptSnapshot(snapshot, nil)
	return v
}
func TestStorageNativeFramesKeepStagesIdentityActionsAndSelection(t *testing.T) {
	v := storageViewFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}, {40, 8}, {60, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		require.Contains(t, frame, "Storage diagnosis / apps/data")
		require.Contains(t, frame, "partial evidence")
		require.Contains(t, frame, "STORAGE STAGES")
		require.Contains(t, frame, "e expand")
		require.Contains(t, frame, "Esc back")
		require.Contains(t, frame, "Overview")
	}
	v.selectTab(3)
	for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "CSI")
		require.Contains(t, frame, "drivers denied")
		require.Equal(t, 3, v.activeTab)
	}
}
func TestStorageRetainsEvidenceSearchScrollAndFailedRefresh(t *testing.T) {
	v := storageViewFixture(t)
	reads := 0
	v.loader = func(context.Context, *SelectedResourceTarget) (*storage.Snapshot, error) {
		reads++
		return nil, fmt.Errorf("unexpected read")
	}
	v.BufferCompleted("Bind", "")
	v.text.ScrollTo(2, 1)
	v.selectTab(1)
	v.selectTab(0)
	require.Equal(t, "Bind", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 2, row)
	require.Equal(t, 1, col)
	previous := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("storage refresh denied"))
	require.Same(t, previous, v.snapshot)
	require.Contains(t, strings.Join(v.model.Peek(), "\n"), "Previous captured evidence retained")
	v.StylesChanged(v.app.Styles)
	v.Stop()
	v.Start()
	require.Zero(t, reads)
	v.destinationRevision++
	v.refresh()
	require.Zero(t, reads)
	require.Contains(t, v.identityBar.GetText(true), "destination changed")
}
func storageFormKeys(v *storageView, events ...*tcell.EventKey) {
	for _, event := range events {
		v.modal.InputHandler()(event, func(p tview.Primitive) { v.app.SetFocus(p) })
	}
}
func storageFormText(v *storageView, text string) {
	for _, char := range text {
		storageFormKeys(v, tcell.NewEventKey(tcell.KeyRune, char, tcell.ModNone))
	}
}
func TestStorageNativeExpansionPreviewReadsCurrentInputAndBlocksReadOnlySubmission(t *testing.T) {
	v := storageViewFixture(t)
	v.app.Config.K9s.ReadOnly = true
	require.True(t, v.app.Config.IsReadOnly())
	submitted := 0
	v.expandSubmit = func(*storage.ExpansionPlan) { submitted++ }
	v.expansionForm()
	require.NotNil(t, v.modal)
	for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 16}} {
		frame := drawnText(t, v.modal, size[0], size[1])
		require.Contains(t, frame, "PVC expansion preview")
		require.Contains(t, frame, "Preview")
		require.Contains(t, frame, "Cancel")
	}
	storageFormKeys(v, tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	storageFormText(v, "15Gi")
	storageFormKeys(v, tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	frame := drawnText(t, v.modal, 80, 24)
	require.Contains(t, frame, "Confirm PVC expansion")
	require.Contains(t, frame, "12Gi -> 15Gi")
	storageFormKeys(v, tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Zero(t, submitted)
	require.True(t, v.formOpen)
	require.Contains(t, drawnText(t, v.modal, 80, 24), "Read-only mode blocks submission")
	v.Stop()
	require.False(t, v.formOpen)
	require.Nil(t, v.modal)
}
func TestStorageNativeExpansionRejectsSupersededPreview(t *testing.T) {
	v := storageViewFixture(t)
	v.expansionForm()
	plan, err := v.snapshot.Expansion("apps", "data", "15Gi")
	require.NoError(t, err)
	v.confirmExpansion(v.snapshot, v.generation, plan)
	submitted := 0
	v.expandSubmit = func(*storage.ExpansionPlan) { submitted++ }
	v.generation++
	storageFormKeys(v, tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Zero(t, submitted)
	require.True(t, v.formOpen)
}

func TestStorageSelectedIdentityFailureStopsBeforeAnySourceCollection(t *testing.T) {
	reader := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), expansionObjects()[0])
	target := &SelectedResourceTarget{Context: "captured", GVR: client.PvcGVR, Namespace: "apps", Name: "data", UID: "old-pvc-uid"}
	snapshot, err := loadStorageReview(t.Context(), inspectionConnection{dynamic: reader}, target, "other-namespace", time.Now())
	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "identity changed")
	require.Len(t, reader.Actions(), 1)
	require.Equal(t, "get", reader.Actions()[0].GetVerb())
	target.GVR = client.NewGVR("v1/secrets")
	require.ErrorContains(t, storageTargetError(target), "Select a native")
	target.GVR, target.UID = client.PvcGVR, ""
	require.ErrorContains(t, storageTargetError(target), "UID unavailable")
}

func TestStoragePVScopeUsesReportedClaimNamespaceAndUID(t *testing.T) {
	objects := expansionObjects()
	target := &SelectedResourceTarget{Context: "captured", GVR: client.PvGVR, Name: "data-pv", UID: "pv-uid"}
	scope := storageScope(target, objects[1], "other-namespace")
	require.Equal(t, "apps", scope.Namespace)
	require.Equal(t, "data", scope.PVCName)
	require.Equal(t, "pvc-uid", scope.PVCUID)
	require.Equal(t, "captured", scope.Identity.Context)
	unstructured.RemoveNestedField(objects[1].Object, "spec", "claimRef")
	scope = storageScope(target, objects[1], "other-namespace")
	require.Equal(t, "other-namespace", scope.Namespace)
	require.Empty(t, scope.PVCName)
	require.Empty(t, scope.PVCUID)
	target.GVR, target.Namespace, target.Name, target.UID = client.PvcGVR, "apps", "data", "pvc-uid"
	unstructured.RemoveNestedField(objects[0].Object, "spec", "storageClassName")
	scope = storageScope(target, objects[0], "other-namespace")
	require.Empty(t, scope.ClassName, "a missing claim class never becomes a guessed default")
}
