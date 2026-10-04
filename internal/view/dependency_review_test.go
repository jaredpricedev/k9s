// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func TestDependencyReviewNativeFramesAndCapturedSourceReturn(t *testing.T) {
	v := networkReviewFixture(t)
	require.NoError(t, v.app.Styles.Load("../../skins/monochrome.yaml", false))
	v.StylesChanged(v.app.Styles)
	v.selectTab(7)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("dependency-edges-%dx%d", size[0], size[1]), frame)
		for _, want := range []string{"[8 Edges]", "References/reports", "Edge group: apps", "apps/api", "config", "no health proof", "? help"} {
			require.Contains(t, frame, want)
		}
	}
	v.app.Content.Push(v)
	v.app.SetFocus(v)
	v.inspectionQuery = testWorkspaceAPIName
	v.cmdBuff.SetText(testWorkspaceAPIName, "", true)
	v.render()
	key := networkpath.ItemKey(v.selectedItem())
	require.Equal(t, "slice-a", string(v.SelectedResource().UID))
	v.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'v', tcell.ModNone), func(tview.Primitive) {})
	_, front := v.app.Content.Pages.GetFrontPage()
	require.NotNil(t, front)
	front.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(tview.Primitive) {})
	require.False(t, v.app.Content.IsTopDialog())
	require.Equal(t, 7, v.activeTab)
	require.Equal(t, testWorkspaceAPIName, v.inspectionQuery)
	require.Equal(t, key, networkpath.ItemKey(v.selectedItem()))
	require.Contains(t, drawnText(t, v, 39, 12), "Need 40x12")
	require.Contains(t, drawnText(t, v, 60, 24), "[8 Edges]")
	require.Equal(t, key, networkpath.ItemKey(v.selectedItem()))
	require.Nil(t, v.observed, "edge review must never implicitly start observation or probes")
}

func TestDependencyReviewCommandUsesRetainedScopeAndStaleSelectionGap(t *testing.T) {
	v := networkReviewFixture(t)
	v.app.Content.Push(v)
	NewCommand(v.app).dependencyReviewCommand()
	require.Equal(t, 7, v.activeTab)
	v.selected[7] = networkpath.ItemKey(v.selectedItem())
	snapshot := *v.snapshot
	snapshot.Items = nil
	v.acceptSnapshot(&snapshot, nil)
	require.Contains(t, v.selectionNotice, "no longer retained")
	require.Empty(t, v.selected[7])
	require.ErrorContains(t, v.SelectedResource().Err(), "No verified retained source")
	require.Contains(t, v.dependencies.Evidence(), "Flow observation not requested")
}

func TestDependencyTargetNavigationDeclinesUnprovenFlowUIDs(t *testing.T) {
	v := networkReviewFixture(t)
	v.selectTab(7)
	require.Equal(t, "svc-a", string(v.edgeTarget().UID), "label association's captured supporting Service remains an explicit target")
	v.edgeItems[0].Related = nil
	require.ErrorContains(t, v.edgeTarget().Err(), "No captured native target UID")
	v.destinationRevision--
	require.ErrorContains(t, v.edgeTarget().Err(), "captured destination")
}
