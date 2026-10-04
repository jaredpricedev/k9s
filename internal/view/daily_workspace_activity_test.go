// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/activity"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func applicationWorkspaceFixture(t *testing.T) (*dailyWorkspace, workspace.Snapshot) {
	t.Helper()
	w := dailyWorkspaceFixture()
	w.app = NewApp(mock.NewMockConfig(t))
	_, err := w.app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	w.app.Config.K9s.UI.NoIcons = true
	w.scope.Context = w.app.Config.ActiveContextName()
	w.contextName = w.scope.Context
	w.scope.Namespaces = []string{testWorkspaceNamespace}
	w.scope.Kinds = []string{"deployments"}
	w.mode = dailyWorkspaceActivityMode
	w.path = filepath.Join(t.TempDir(), "workspaces.yaml")
	w.store.Scopes = []workspace.Scope{w.scope}
	require.NoError(t, workspace.SaveStore(w.path, w.store))
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	w.resetObservationWindow(at)
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"namespace": testWorkspaceNamespace, "name": "api", "uid": "deploy-a", "generation": int64(2)},
		"spec":     map[string]any{"replicas": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "repo/api:v1"}}}}},
		"status":   map[string]any{"observedGeneration": int64(2), "readyReplicas": int64(1)}}}
	ref := workspace.ResourceRef{GVR: "apps/v1/deployments", Namespace: testWorkspaceNamespace, Name: "api", UID: "deploy-a"}
	s := workspace.Snapshot{ObservedAt: at, Resources: []workspace.Resource{{Ref: ref, Kind: "Deployment", Object: object}}, Coverage: []workspace.Coverage{{GVR: ref.GVR, Namespace: ref.Namespace, State: dailyWorkspaceCoverageComplete}}}
	w.acceptSnapshot(s, nil)
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	require.NoError(t, unstructured.SetNestedSlice(object.Object, []any{map[string]any{"name": "api", "image": "repo/api:v2"}}, "spec", "template", "spec", "containers"))
	require.NoError(t, unstructured.SetNestedField(object.Object, int64(2), "status", "readyReplicas"))
	w.acceptSnapshot(s, nil)
	w.actions = w.makeActions()
	w.Flex.SetDirection(tview.FlexRow).AddItem(w.header, 4, 0, false).AddItem(w.table, 0, 1, true).AddItem(w.detail, 3, 0, false).AddItem(w.footer, 1, 0, false)
	w.SetBorder(true)
	w.render()
	return w, s
}

func TestApplicationActivityNativeFramesKeepChangeIdentityAndGaps(t *testing.T) {
	w, _ := applicationWorkspaceFixture(t)
	require.NoError(t, w.app.Styles.Load("../../skins/monochrome.yaml", false))
	w.StylesChanged(w.app.Styles)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, w, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("application-activity-%dx%d", size[0], size[1]), frame)
		for _, want := range []string{"[7 Activity]", "Since", "Events ?", "changed", "api", "? help"} {
			require.Contains(t, frame, want, "%dx%d", size[0], size[1])
		}
		require.Contains(t, frame, "10:01", "selected observation time must remain visible at %dx%d", size[0], size[1])
		if size[0] >= 60 {
			require.Contains(t, frame, "repo/api:v1")
			require.Contains(t, frame, "repo/api:v2")
		} else {
			require.Contains(t, frame, "After: repo/api:v2")
		}
	}
	key := w.selectedKey()
	require.Contains(t, drawnText(t, w, 39, 12), "Need 40x12")
	require.Contains(t, drawnText(t, w, 60, 24), "[7 Activity]")
	require.Equal(t, key, w.selectedKey())
}

func TestApplicationActivityEvidenceReturnKeepsUIDQueryAndMode(t *testing.T) {
	w, _ := applicationWorkspaceFixture(t)
	w.app.Content.Push(w)
	w.app.SetFocus(w.table)
	w.Start()
	t.Cleanup(w.Stop)
	require.True(t, w.applyQuery("name:api status:changed"))
	selected := w.selectedKey()
	target := w.SelectedResource()
	require.Equal(t, "deploy-a", string(target.UID))
	require.Contains(t, w.rows[0].evidence, "\"Before\": \"repo/api:v1\"")
	require.Contains(t, w.rows[0].evidence, "\"After\": \"repo/api:v2\"")
	require.Contains(t, w.rows[0].evidence, "Observation began")
	action, ok := w.Actions().Get(tcell.KeyEnter)
	require.True(t, ok)
	require.False(t, action.Opts.RequiresSelection)
	require.Equal(t, "Retained evidence", action.Description)
	w.showRowDetails()
	require.True(t, w.app.Content.IsTopDialog())
	escape := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	require.Same(t, escape, w.app.GetInputCapture()(escape), "global workspace capture must defer to the evidence dialog")
	_, front := w.app.Content.Pages.GetFrontPage()
	require.NotNil(t, front)
	front.InputHandler()(escape, func(tview.Primitive) {})
	require.False(t, w.app.Content.IsTopDialog())
	require.Equal(t, selected, w.selectedKey())
	require.Equal(t, "name:api status:changed", w.query)
	require.Equal(t, dailyWorkspaceActivityMode, w.mode)
}

func TestApplicationActivityGapRefreshRetainsSupportingSourceAndState(t *testing.T) {
	w, s := applicationWorkspaceFixture(t)
	selected := w.selectedKey()
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Resources = nil
	s.Coverage[0].State = testWorkspaceCoverageDenied
	w.acceptSnapshot(s, errors.New("denied current observation"))
	w.render()
	require.Equal(t, selected, w.selectedKey(), "coverage gaps stay separate from observed activity rows")
	require.Len(t, w.activityWindow.Entries, 3)
	require.Equal(t, activity.EntryGap, w.activityWindow.Entries[2].State)
	require.Equal(t, "deploy-a", string(w.SelectedResource().UID))
	w.setMode(dailyWorkspaceHistoryMode)
	w.setMode(dailyWorkspaceActivityMode)
	require.Equal(t, selected, w.selectedKey())
	prior := w.activityWindow
	w.Stop()
	w.render()
	require.Same(t, prior, w.activityWindow)
	w.resetObservationWindow(s.ObservedAt.Add(time.Minute))
	require.Empty(t, w.activityWindow.Entries)
}

func TestApplicationActivityEventUsesRegardingUIDAndRetainsExactEventEvidence(t *testing.T) {
	w, s := applicationWorkspaceFixture(t)
	count := int64(3)
	event := activity.EventSource{Identity: w.activityWindow.Entries[0].Source.Identity, Regarding: w.activityWindow.Entries[0].Source.Identity,
		Kind: "Deployment", Reason: "ProgressDeadlineExceeded", Message: "retained Event report", Type: "Warning", Count: &count, CapturedAt: s.ObservedAt.Add(time.Second)}
	event.Identity.GVR = "v1/events"
	event.Identity.Name = "api.event"
	event.Identity.UID = "event-a"
	collection := activity.EventCollection{Requested: true, CapturedAt: event.CapturedAt, Events: []activity.EventSource{event}, Coverage: []workspace.Coverage{{GVR: "v1/events", Namespace: testWorkspaceNamespace, State: testWorkspaceCoverageDenied}}}
	w.activityWindow.ObserveEvents(&collection)
	w.activityEventCoverage = collection.Coverage
	w.render()
	require.Equal(t, "Event", w.rows[0].cells[0])
	require.Equal(t, "deploy-a", string(w.SelectedResource().UID))
	require.Contains(t, w.rows[0].evidence, "event-a")
	require.Contains(t, w.rows[0].evidence, "deploy-a")
	require.Contains(t, w.activityStatus(76), "Events partial")
	key := w.selectedKey()
	w.activityWindow.ObserveEvents(&collection)
	w.render()
	require.Equal(t, key, w.selectedKey())
}
