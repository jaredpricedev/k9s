// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func networkSessionLinkFixture(t *testing.T) *networkReviewView {
	t.Helper()
	v := networkReviewFixture(t)
	v.app.App.Init()
	require.NoError(t, v.app.Content.Init(context.WithValue(t.Context(), internal.KeyApp, v.app)))
	v.app.Content.Push(v)
	t.Cleanup(v.app.Content.Clear)
	return v
}

func networkSessionSpec(v *networkReviewView) *session.Spec {
	return &session.Spec{Kind: session.PortForward, Label: "Retained owned forward", Binding: "127.0.0.1:19090 -> 80",
		Destination: session.Destination{Context: v.target.Context, GVR: client.PodGVR.String(), Namespace: "apps", Name: "api-a", UID: "pod-a"},
		Origin: session.Destination{Context: v.target.Context, GVR: v.target.GVR.String(), Namespace: v.target.Namespace,
			Name: v.target.Name, UID: string(v.target.UID)}}
}

func TestNetworkSessionLinkRetainsEvidenceAndOwnedSessionAcrossBack(t *testing.T) {
	v := networkSessionLinkFixture(t)
	var cancellations, collections atomic.Int32
	v.loader = func(context.Context, networkpath.Scope) (*networkpath.Snapshot, error) {
		collections.Add(1)
		return nil, nil
	}
	owned, err := v.app.localSessions.Add(networkSessionSpec(v), func() { cancellations.Add(1) })
	require.NoError(t, err)
	owned.Running("Fixture-owned local transport")
	other := networkSessionSpec(v)
	other.Origin.UID, other.Destination.UID = "unrelated-service", "unrelated-pod"
	_, err = v.app.localSessions.Add(other, nil)
	require.NoError(t, err)
	v.inspectionQuery = testWorkspaceAPIName
	v.cmdBuff.SetText(testWorkspaceAPIName, "", true)
	v.render()
	retained := v.snapshot
	before := v.app.localSessions.Records()
	v.app.Config.K9s.ReadOnly = true
	require.NoError(t, v.app.Styles.Load("../../skins/monochrome.yaml", false))
	v.StylesChanged(v.app.Styles)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {50, 20}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "s sessions")
		require.Contains(t, frame, "? help")
		require.Contains(t, frame, "Untested", "owned transport state must not become connectivity evidence")
	}
	found := false
	for _, action := range ui.DescribeActions(v.actions, ui.ActionContext{ReadOnly: true}) {
		if action.ID == "network.sessions" {
			found = true
			require.True(t, action.Discoverable)
			require.Equal(t, "s", action.Shortcut)
			require.True(t, action.Available(), "review remains available in read-only mode")
		}
	}
	require.True(t, found, "contextual help must describe the actual session navigation action")
	v.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone), func(p tview.Primitive) { v.app.SetFocus(p) })
	opened, ok := v.app.Content.Top().(*localSessions)
	require.True(t, ok)
	require.Equal(t, owned.ID(), opened.selectedID, "preselect the matching origin rather than the newer unrelated record")
	opened.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(p tview.Primitive) { v.app.SetFocus(p) })
	require.Same(t, v, v.app.Content.Top())
	require.Same(t, retained, v.snapshot)
	require.Equal(t, testWorkspaceAPIName, v.inspectionQuery)
	require.Equal(t, 0, v.activeTab)
	require.Equal(t, before, v.app.localSessions.Records(), "navigation must not launch, cancel or append lifecycle events")
	require.Zero(t, cancellations.Load())
	require.Zero(t, collections.Load(), "returning to retained evidence must not reread the configured path")
}

func TestNetworkSessionLinkUsesSelectedPodUIDRatherThanServiceOrFlowName(t *testing.T) {
	v := networkSessionLinkFixture(t)
	spec := networkSessionSpec(v)
	spec.Origin = session.Destination{}
	_, err := v.app.localSessions.Add(spec, nil)
	require.NoError(t, err)
	require.Contains(t, v.existingSessionsUnavailable(), "No app-owned")
	pod := v.snapshot.Pods[0].Source
	v.snapshot.Items = append(v.snapshot.Items, networkpath.Item{Source: pod, Group: networkpath.GroupBackends})
	v.selected[2] = networkpath.ItemKey(&v.snapshot.Items[len(v.snapshot.Items)-1])
	v.selectTab(2)
	require.Equal(t, "pod-a", string(v.SelectedResource().UID))
	require.Empty(t, v.existingSessionsUnavailable())
	v.snapshot.Flows = &networkpath.FlowWindow{Items: []networkpath.Item{{Group: networkpath.GroupFlows,
		Source: networkpath.Source{Kind: "Hubble Relay flow"}, Summary: "apps/api-a"}}}
	v.selectTab(5)
	require.Contains(t, v.existingSessionsUnavailable(), "no proven API resource UID")
	before := v.app.localSessions.Records()
	require.Nil(t, v.openExistingSessions(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone)))
	require.Same(t, v, v.app.Content.Top())
	require.Equal(t, before, v.app.localSessions.Records())
}

func TestNetworkSessionLinkDeclinesOtherIdentitiesAndStaleCallbacks(t *testing.T) {
	for name, change := range map[string]func(*session.Destination){
		"replacement UID": func(d *session.Destination) { d.UID = nativeReplacement },
		"other context":   func(d *session.Destination) { d.Context = guardedTestOtherContext },
		"other kind":      func(d *session.Destination) { d.GVR = client.DpGVR.String() },
		"other namespace": func(d *session.Destination) { d.Namespace = guardedTestOtherContext },
		"other name":      func(d *session.Destination) { d.Name = guardedTestOtherContext },
	} {
		t.Run(name, func(t *testing.T) {
			v := networkSessionLinkFixture(t)
			spec := networkSessionSpec(v)
			change(&spec.Origin)
			_, err := v.app.localSessions.Add(spec, nil)
			require.NoError(t, err)
			require.Contains(t, v.existingSessionsUnavailable(), "No app-owned")
			v.openExistingSessions(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
			require.Same(t, v, v.app.Content.Top())
			require.Len(t, v.app.localSessions.Records(), 1)
		})
	}
	v := networkSessionLinkFixture(t)
	_, err := v.app.localSessions.Add(networkSessionSpec(v), nil)
	require.NoError(t, err)
	key := tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone)
	v.cmdBuff.SetActive(true)
	require.Same(t, key, v.openExistingSessions(key), "s remains input while searching retained evidence")
	v.cmdBuff.SetActive(false)
	v.destinationRevision--
	require.Contains(t, v.existingSessionsUnavailable(), "Destination changed")
	v.openExistingSessions(key)
	require.Same(t, v, v.app.Content.Top())
	v.destinationRevision++
	require.NoError(t, v.app.inject(newLocalSessions(v.app), false))
	current := v.app.Content.Top()
	require.Contains(t, v.existingSessionsUnavailable(), "no longer")
	v.openExistingSessions(key)
	require.Same(t, current, v.app.Content.Top(), "an old callback cannot replace the current workspace")
}
