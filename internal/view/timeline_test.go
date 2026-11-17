// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

func timelineSnapshot(scope *timeline.Scope) *timeline.Snapshot {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	pod := timeline.Ref{Resource: "v1/pods", Kind: "Pod", Namespace: "apps", Name: "checkout-api-7d9f8c6b5d-x2x9q"}
	entries := []timeline.Entry{
		{At: at.Add(-72 * time.Hour), Source: timeline.SourceRollout, Change: true, Summary: "rev 1 (earliest retained): image example.test/checkout:v1",
			Ref: timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "apps", Name: "checkout-api-6c8d7f9b5"}},
		{At: at.Add(-14 * time.Minute), Source: timeline.SourceConfig, Change: true, Summary: "ConfigMap updated by kubectl-edit",
			Origin: "metadata.managedFields[].time and manager",
			Ref:    timeline.Ref{Resource: "v1/configmaps", Kind: "ConfigMap", Namespace: "apps", Name: "checkout-config"}},
		{At: at.Add(-12 * time.Minute), Source: timeline.SourceRollout, Change: true, Summary: "rev 2: image v1 -> v2; env +FEATURE_FLAGS",
			Ref: timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "apps", Name: "checkout-api-7d9f8c6b5d"}},
		{At: at.Add(-10 * time.Minute), Last: at.Add(-time.Minute), Count: 14, Source: timeline.SourceEvent, Failing: true,
			Summary: "Warning BackOff x14: Back-off restarting failed container", Ref: pod},
		{At: at.Add(-2 * time.Minute), Source: timeline.SourceRestart, Failing: true, Summary: "container api Error (exit 1), restart count 6", Ref: pod},
		{At: at.Add(-144 * time.Hour), Source: timeline.SourceNode, Summary: "Ready=True KubeletReady", Ref: timeline.Ref{Resource: "v1/nodes", Kind: "Node", Name: "node-a"}},
	}
	return timeline.NewSnapshot(scope, at, entries, []timeline.Coverage{
		{Source: "Events", State: timeline.Complete, Detail: "3 in scope"}, {Source: "Nodes", State: timeline.Denied, Detail: "access denied"}})
}

func timelineViewFixture(t *testing.T) *timelineView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	scope := &timeline.Scope{Context: app.Config.ActiveContextName(), Namespace: "apps", Resource: "apps/v1/deployments", Kind: "Deployment", Name: "checkout-api"}
	v := newTimelineView(app, scope, "")
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(timelineSnapshot(scope), nil)
	return v
}

func TestTimelineFramesMarkTheFaultAndWhatPrecededItAtEverySize(t *testing.T) {
	v := timelineViewFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}, {40, 8}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0])
		}
		require.Contains(t, frame, "Timeline / Deployment apps/checkout-api")
		require.Contains(t, frame, "Fault began 11:50:00Z")
		require.Contains(t, frame, "NOT READ: nodes denied")
		require.Contains(t, frame, "preceded")
		require.NotContains(t, strings.ToLower(frame), "caused")
		rows := strings.Split(frame, "\n")
		fault, rule, config := -1, -1, -1
		for index, row := range rows {
			switch {
			case strings.HasPrefix(row, "▶ 11:50:00"):
				fault = index
			case strings.Contains(row, "below: preceded"):
				rule = index
			case strings.HasPrefix(row, "◆ 11:46:00") && strings.Contains(row, "CONFIG"):
				config = index
			}
		}
		require.Positive(t, fault, frame)
		require.Equal(t, fault+1, rule, "the rule sits directly under the first failing signal\n%s", frame)
		if size[1] >= 24 {
			require.Greater(t, config, rule, "the earlier configuration change is listed under the rule\n%s", frame)
		}
		switch {
		case size[0] >= 100:
			require.Contains(t, frame, "VS FAULT    AGE    RESOURCE")
			require.Contains(t, frame, "FAULT BEGAN 10m    pod/checkout-api-7…b5d-x2x9q Warning BackOff x14")
			require.Contains(t, frame, "4m before   14m    cm/checkout-config")
		case size[0] >= 80:
			require.Contains(t, frame, "FAULT BEGAN pod/checkou…x2x9q Warning BackOff x14")
			require.Contains(t, frame, "4m before   cm/checkout…onfig ConfigMap updated by kubectl-")
		case size[0] >= 60:
			require.Contains(t, frame, "EVENT   FAULT  Warning BackOff x14")
			require.Contains(t, frame, "CONFIG  -4m    ConfigMap updated by kubectl-edit", "status text survives; the name is what gives way")
			require.NotContains(t, frame, "RESOURCE")
		}
	}
}

func TestTimelineNoIconsUsesASCIIMarksOnly(t *testing.T) {
	v := timelineViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	v.render()
	frame := drawnText(t, v, 80, 24)
	require.Contains(t, frame, "> 11:50:00 ")
	require.Contains(t, frame, "* 11:46:00 ")
	require.Contains(t, frame, "! 11:58:00 ")
	require.Contains(t, frame, "---- below: preceded the first failing signal; order, not cause ----")
	for _, glyph := range []string{"▶", "◆", "─"} {
		require.NotContains(t, frame, glyph)
	}
}

func TestTimelineDetailStatesExactTimeSourceResourceAndOrderNotCause(t *testing.T) {
	v := timelineViewFixture(t)
	require.Equal(t, v.snapshot.Onset, v.focused, "opens on the first failing signal")
	require.Contains(t, v.detail.GetText(true), "2026-11-17 11:50:00Z · 10m before capture · Event · Pod apps/checkout-api-7d9f8c6b5d-x2x9q")
	require.Contains(t, v.detail.GetText(true), "First failing signal that could be read")
	v.table.Select(5, 0) // header, restart, event, rule, rollout, config
	text := v.detail.GetText(true)
	require.Contains(t, text, "2026-11-17 11:46:00Z · 14m before capture · Config · ConfigMap apps/checkout-config")
	require.Contains(t, text, "This change preceded the first failing signal by 4m. Order is not cause.")
	require.Contains(t, text, "Time from: metadata.managedFields[].time and manager")
	target := v.SelectedResource()
	require.Equal(t, "v1/configmaps", target.GVR.String())
	require.Equal(t, "apps/checkout-config", target.Path())
}

func TestTimelineWindowFilterAndReferenceNarrowWithoutReading(t *testing.T) {
	v := timelineViewFixture(t)
	reads := 0
	v.loader = func(context.Context) (*timeline.Snapshot, error) { reads++; return nil, errors.New("unexpected read") }

	v.setWindow(timeline.Windows[0])
	require.Equal(t, 4, v.visibleCount)
	require.Contains(t, v.header.GetText(true), "4 of 6 entries · window last 15m")
	v.setWindow(0)
	v.applyQuery("CONFIG")
	require.Equal(t, 1, v.visibleCount)
	require.Contains(t, v.header.GetText(true), "filter CONFIG")
	require.Contains(t, drawnText(t, v, 80, 24), "below: preceded", "a filtered-out fault still labels what is left")
	v.applyQuery("nothing-matches")
	require.Contains(t, drawnText(t, v, 80, 24), "No entry matches the filter in window all read. Esc clears the filter.")
	require.Nil(t, v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)))
	require.Empty(t, v.query, "Esc clears the filter before it leaves the view")

	v.setWindow(timeline.Windows[0])
	v.applyQuery("rev 1")
	require.Contains(t, drawnText(t, v, 80, 24), "No entry matches the filter in window last 15m")
	v.applyQuery("")
	v.setWindow(0)

	v.table.Select(5, 0)
	v.markReference()
	require.True(t, v.snapshot.Chosen)
	frame := drawnText(t, v, 120, 34)
	require.Contains(t, frame, "Reference 11:46:00Z (14m before capture): cm/checkout-config")
	require.Contains(t, frame, "VS REF")
	require.Contains(t, frame, "REFERENCE")
	require.Contains(t, frame, "4m after")
	v.markReference()
	require.False(t, v.snapshot.Chosen, "marking the reference again returns to the first failing signal")

	v.toggleSources()
	sources := drawnText(t, v, 80, 24)
	require.Contains(t, sources, "TIMELINE OBSERVATION")
	require.Contains(t, sources, "s or Esc returns to the timeline")
	v.toggleSources()
	require.Zero(t, reads, "narrowing, marking and paging never read the cluster")
}

func TestTimelineRetainsObservationAcrossFailedRereadAndBack(t *testing.T) {
	v := timelineViewFixture(t)
	reads := 0
	v.loader = func(context.Context) (*timeline.Snapshot, error) { reads++; return nil, errors.New("unexpected read") }
	prior := v.snapshot
	v.acceptSnapshot(nil, errors.New("events denied"))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.header.GetText(true), "Re-read failed, earlier observation retained: events denied")
	v.Stop()
	v.Start()
	require.Zero(t, reads, "returning to a retained timeline starts no read")

	empty := newTimelineView(v.app, &v.scope, "")
	require.NoError(t, empty.Init(t.Context()))
	t.Cleanup(empty.Stop)
	empty.acceptSnapshot(nil, errors.New("Deployment apps/checkout-api: access denied"))
	frame := drawnText(t, empty, 80, 24)
	require.Contains(t, frame, "Unavailable · r retry · Deployment apps/checkout-api: access denied")
	require.Contains(t, frame, "Nothing was read")
}

func TestTimelineLayoutKeepsTimeKindAndTextBeforeNames(t *testing.T) {
	wide := timelineLayout(120, 9, 33, true)
	require.Equal(t, timelineColumns{clock: 9, age: 6, relation: 11, resource: 28, what: 52}, wide)
	standard := timelineLayout(80, 8, 33, true)
	require.Zero(t, standard.age)
	require.Equal(t, 11, standard.relation)
	require.GreaterOrEqual(t, standard.resource, 12)
	require.GreaterOrEqual(t, standard.what, timelineMinWhat)
	narrow := timelineLayout(60, 8, 33, true)
	require.Zero(t, narrow.resource, "the name column gives way first")
	require.True(t, narrow.compactRelation)
	require.GreaterOrEqual(t, narrow.what, timelineMinWhat)
	require.Zero(t, timelineLayout(60, 8, 33, false).relation, "no before/after column without a failing signal")
	for _, columns := range []timelineColumns{wide, standard, narrow} {
		used := 2 + columns.clock + 1 + timelineKindWidth + 1 + columns.relation + 1 + columns.what
		if columns.age > 0 {
			used += columns.age + 1
		}
		if columns.resource > 0 {
			used += columns.resource + 1
		}
		require.Contains(t, []int{60, 80, 120}, used)
	}
	require.Equal(t, "pod/checkou…x2x9q", timelineName("pod/checkout-api-7d9f8c6b5d-x2x9q", 17))
	require.Equal(t, 17, runewidth.StringWidth(timelineName("pod/日本語のとても長い名前のポッド", 17)))
	require.Equal(t, "cm/a   ", timelineName("cm/a", 7))
}

func TestTimelineScopeFollowsSelectionThenNamespace(t *testing.T) {
	pod := SelectedResourceTarget{Context: "ctx", GVR: client.PodGVR, Namespace: "apps", Name: "api", UID: "pod-uid"}
	scope, note, err := timelineScope(pod, client.NamespaceAll, "")
	require.NoError(t, err)
	require.Empty(t, note)
	require.Equal(t, timeline.Scope{Context: "ctx", Namespace: "apps", Resource: "v1/pods", Kind: "Pod", Name: "api", UID: "pod-uid"}, *scope)

	service := SelectedResourceTarget{Context: "ctx", GVR: client.SvcGVR, Namespace: "apps", Name: "api"}
	scope, note, err = timelineScope(service, client.NamespaceAll, "")
	require.NoError(t, err)
	require.Equal(t, "namespace apps", scope.Label())
	require.Contains(t, note, "not a workload; showing its namespace")

	namespace := SelectedResourceTarget{Context: "ctx", GVR: client.NsGVR, Name: "ops"}
	scope, _, err = timelineScope(namespace, "apps", "")
	require.NoError(t, err)
	require.Equal(t, "namespace ops", scope.Label())

	none := SelectedResourceTarget{Context: "ctx", UnavailableReason: "nothing selected"}
	scope, _, err = timelineScope(none, "apps", "")
	require.NoError(t, err)
	require.Equal(t, "namespace apps", scope.Label())
	scope, _, err = timelineScope(pod, "apps", "ops")
	require.NoError(t, err)
	require.Equal(t, "namespace ops", scope.Label(), "an explicit namespace argument wins")

	for _, active := range []string{client.NamespaceAll, client.BlankNamespace, client.ClusterScope} {
		_, _, err = timelineScope(none, active, "")
		require.ErrorContains(t, err, "a timeline reads a single namespace")
	}
}

func TestTimelineIsRegisteredInTheActionCatalog(t *testing.T) {
	v := timelineViewFixture(t)
	var found *ui.ActionDescriptor
	catalog := actionCatalog(v, v.app)
	for index := range catalog {
		if catalog[index].ID == "resource.timeline" {
			found = &catalog[index]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, ":timeline", found.Shortcut)
	require.Equal(t, ui.ActionInspect, found.Category)
	require.Empty(t, found.UnavailableReason)
	command := NewCommand(v.app)
	command.updateSuggestionAliases()
	require.Contains(t, command.suggestionAliases(), timelineCommandToken)
}
