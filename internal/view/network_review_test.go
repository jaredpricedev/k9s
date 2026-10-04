// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func networkReviewFixture(t *testing.T) *networkReviewView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	app.Config.K9s.UI.NoIcons = true
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.SvcGVR, Namespace: "apps", Name: testWorkspaceAPIName, UID: "svc-a"}
	id := inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}
	v := &networkReviewView{Details: NewDetails(app, "Network path", target.Path(), contentInspection, true), target: target,
		scope: networkpath.Scope{Service: id, RouteNamespaces: []string{"apps", "edge"}}, destinationRevision: app.Config.DestinationRevision()}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	source := networkpath.Source{Identity: id, Kind: "Service", ResourceVersion: "1", CapturedAt: at}
	backend := source
	backend.Kind = "EndpointSlice"
	backend.Identity.GVR = "discovery.k8s.io/v1/endpointslices"
	backend.Identity.Name = "api-1"
	backend.Identity.UID = "slice-a"
	pod := source
	pod.Kind = "Pod"
	pod.Identity.GVR = "v1/pods"
	pod.Identity.Name = "api-a"
	pod.Identity.UID = "pod-a"
	s := &networkpath.Snapshot{Scope: v.scope, StartedAt: at, CapturedAt: at, Service: networkpath.Service{Source: source, Type: "ClusterIP",
		Ports: []networkpath.Port{{Name: "http", Number: 80, Protocol: "TCP", Target: "http"}}}, Pods: []networkpath.Pod{{Source: pod, Ready: "True", Phase: "Running"}},
		Items: []networkpath.Item{{Source: backend, Group: networkpath.GroupBackends, State: "ready true", Summary: "10.1.0.2 · ready true",
			Facts: []networkpath.Fact{{Name: "Reported endpoint ready", Value: "true"}, {Name: "Target identity", Value: "Pod api-a UID=pod-a"}}, Related: []inspect.ResourceIdentity{pod.Identity}}},
		Coverage: []networkpath.Coverage{{GVR: "gateway.networking.k8s.io/v1/httproutes", Namespace: "edge", State: "denied", Detail: "Route read denied", CapturedAt: at}}}
	v.acceptSnapshot(s, nil)
	return v
}

func TestNetworkReviewNativeFramesKeepScopeUntestedAndMinimumState(t *testing.T) {
	v := networkReviewFixture(t)
	require.NoError(t, v.app.Styles.Load("../../skins/monochrome.yaml", false))
	v.StylesChanged(v.app.Styles)
	v.selectTab(2)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, v, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("network-review-%dx%d", size[0], size[1]), frame)
		for _, want := range []string{"[3 Backends]", "apps/api", "Untested", "ready true", "? help"} {
			require.Contains(t, frame, want, "%dx%d", size[0], size[1])
		}
	}
	key := networkpath.ItemKey(v.selectedItem())
	require.Contains(t, drawnText(t, v, 39, 12), "Need 40x12")
	require.Contains(t, drawnText(t, v, 60, 24), "[3 Backends]")
	require.Equal(t, key, networkpath.ItemKey(v.selectedItem()))
	require.Nil(t, v.observed, "configuration review and resizing must not automatically open a flow stream or probe")
}

func TestNetworkReviewEvidenceReturnPartialRefreshQueryAndUID(t *testing.T) {
	v := networkReviewFixture(t)
	v.selectTab(2)
	v.inspectionQuery = testWorkspaceAPIName
	v.cmdBuff.SetText(testWorkspaceAPIName, "", true)
	v.render()
	v.selected[2] = networkpath.ItemKey(v.selectedItem())
	v.app.Content.Push(v)
	v.app.SetFocus(v)
	target := v.SelectedResource()
	require.Equal(t, "slice-a", string(target.UID))
	require.Nil(t, v.showEvidence(tcell.NewEventKey(tcell.KeyRune, 'v', tcell.ModNone)))
	_, front := v.app.Content.Pages.GetFrontPage()
	require.NotNil(t, front)
	front.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(tview.Primitive) {})
	require.False(t, v.app.Content.IsTopDialog())
	require.Equal(t, testWorkspaceAPIName, v.inspectionQuery)
	require.Equal(t, 2, v.activeTab)
	retained := v.snapshot
	v.acceptSnapshot(nil, errors.New("denied refresh"))
	require.Same(t, retained, v.snapshot)
	require.Equal(t, "slice-a", string(v.SelectedResource().UID))
	require.Equal(t, testWorkspaceAPIName, v.inspectionQuery)
	v.selectTab(6)
	require.Contains(t, v.text.GetText(true), "Route read denied")
	v.selectTab(2)
	require.Equal(t, testWorkspaceAPIName, v.inspectionQuery)
	v.destinationRevision--
	require.False(t, v.destinationCurrent())
	v.refresh()
	require.Contains(t, v.refreshFailure, "Destination changed")
}

func TestNetworkReviewFlowRowsDeclineFabricatedResourceUID(t *testing.T) {
	v := networkReviewFixture(t)
	v.snapshot.Flows = &networkpath.FlowWindow{Items: []networkpath.Item{{Group: networkpath.GroupFlows, State: "DROPPED", Source: networkpath.Source{Kind: "Hubble Relay flow"}}}}
	v.selectTab(5)
	target := v.SelectedResource()
	require.ErrorContains(t, target.Err(), "no proven API resource UID")
	require.Contains(t, v.snapshot.Evidence(v.selectedItem()), "Configured paths and policy candidates are not proven connectivity")
}

func TestNetworkReviewReportedPinsRetainContextAcrossWindowChanges(t *testing.T) {
	v := networkReviewFixture(t)
	sample := networkpath.FlowSample{Context: v.target.Context, StartedAt: time.Now(), CapturedAt: time.Now(),
		Events: []hubble.Event{{ID: 1, Source: hubble.Peer{Cluster: "lab", Pod: "apps/api-a"},
			Destination: hubble.Peer{Kind: "FQDN", IP: "10.0.0.1", Names: "example.test"}, Protocol: "TCP", SourcePort: 1234, DestinationPort: 443}}}
	v.snapshot.Flows = networkpath.ComposeFlows(&sample)
	v.selectTab(5)
	v.app.Content.Push(v)
	v.app.SetFocus(v)
	drawnText(t, v, 80, 24)
	for _, key := range []rune{'p', 't', 'P'} {
		v.InputHandler()(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone), func(tview.Primitive) {})
	}
	require.Len(t, v.flowPins, 3)
	require.True(t, v.selectedItem().Pinned)
	require.Equal(t, v.target.Context, v.flowPins[0].Context)
	require.NotZero(t, v.flowPins[0].CreatedAt)
	require.Contains(t, drawnText(t, v, 40, 12), "*>")
	require.ErrorContains(t, v.SelectedResource().Err(), "no proven API resource UID")
	sample.Events = nil
	v.snapshot.Flows = networkpath.ComposeFlows(&sample)
	v.applyFlowPins()
	require.Len(t, v.snapshot.Flows.Pins, 3, "missing reports retain pins without asserting clearance")
	require.Empty(t, v.snapshot.Flows.Items)
	sample.Events = []hubble.Event{{ID: 2, Source: hubble.Peer{Cluster: "lab", Pod: "apps/api-a"},
		Destination: hubble.Peer{Kind: "FQDN", IP: "10.0.0.1", Names: "example.test"}, Protocol: "TCP", SourcePort: 1234, DestinationPort: 443}}
	v.snapshot.Flows = networkpath.ComposeFlows(&sample)
	v.applyFlowPins()
	require.True(t, v.selectedItem().Pinned)
	require.Nil(t, v.pinFlow(tcell.NewEventKey(tcell.KeyRune, 'P', tcell.ModNone), "conversation"))
	require.Len(t, v.flowPins, 2)
	for len(v.flowPins) < 32 {
		v.flowPins = append(v.flowPins, networkpath.FlowPin{Kind: "conversation", Key: fmt.Sprint(len(v.flowPins))})
	}
	require.Nil(t, v.pinFlow(tcell.NewEventKey(tcell.KeyRune, 'P', tcell.ModNone), "conversation"))
	require.Len(t, v.flowPins, 32, "pins are bounded without evicting captured references")
}

func TestNetworkReviewConnectivityPollPreservesExplicitRetainedTask(t *testing.T) {
	v := networkReviewFixture(t)
	v.active, v.loading = true, true
	v.generation = 10
	canceled := false
	v.cancel = func() { canceled = true }
	v.selectTab(2)
	v.inspectionQuery = testWorkspaceAPIName
	snapshot := v.snapshot
	require.True(t, retainedDisconnectedWorkspace(v), "retained task must not exhaust background connection retry budget")
	v.app.connectivityComponent(v, false)
	require.True(t, v.active)
	require.True(t, v.loading)
	require.False(t, canceled, "background connectivity must not cancel an explicitly requested bounded read")
	require.Equal(t, uint64(10), v.generation)
	require.Same(t, snapshot, v.snapshot)
	v.app.connectivityComponent(v, true)
	require.Equal(t, testWorkspaceAPIName, v.inspectionQuery)
	require.Equal(t, 2, v.activeTab)
	require.Same(t, snapshot, v.snapshot)
}
