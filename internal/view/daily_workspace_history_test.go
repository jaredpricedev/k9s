// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func observedWorkspaceFixture() (*dailyWorkspace, workspace.Snapshot) {
	w := dailyWorkspaceFixture()
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	w.scope.Namespaces = []string{testWorkspaceNamespace}
	w.resetObservationWindow(at)
	w.mode = dailyWorkspaceHistoryMode
	f := workspace.Finding{Ref: workspaceFixtureRef(testWorkspaceAPIName), Kind: inspectionPodKind, Category: "fault", Reason: testWorkspaceCrashLoopReason, Detail: "Current container state; cause unconfirmed", ObservedAt: at}
	s := workspace.Snapshot{ObservedAt: at, Findings: []workspace.Finding{f}, Resources: []workspace.Resource{{Ref: f.Ref, Kind: inspectionPodKind}}, Coverage: []workspace.Coverage{{GVR: podCmd, Namespace: testWorkspaceNamespace, State: dailyWorkspaceCoverageComplete}}}
	w.acceptSnapshot(s, nil)
	return w, s
}

func TestDailyHistoryCurrentQueueAndRetainedUnknownStaySeparate(t *testing.T) {
	w, s := observedWorkspaceFixture()
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Findings = nil
	s.Resources = nil
	s.Coverage[0].State = testWorkspaceCoverageDenied
	w.acceptSnapshot(s, nil)
	require.Len(t, w.snapshot.Findings, 1, "all-failed refresh keeps previous current snapshot")
	require.Equal(t, workspace.QueueUnknown, w.observationWindow.Active()[0].State)
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Coverage = append(s.Coverage, workspace.Coverage{GVR: "apps/v1/deployments", Namespace: testWorkspaceNamespace, State: dailyWorkspaceCoverageComplete})
	w.acceptSnapshot(s, nil)
	require.Empty(t, w.snapshot.Findings, "partial current result remains distinct from unresolved prior history")
	require.Len(t, w.observationWindow.Active(), 1)
	w.render()
	require.Equal(t, workspace.QueueUnknown, w.rows[0].cells[0])
	require.Contains(t, w.rows[0].evidence, "\"State\": \"denied\"")
	require.Contains(t, w.header.GetText(true), "1 unresolved gaps")
	require.Equal(t, workspaceFixtureRef(testWorkspaceAPIName).UID, string(w.SelectedResource().UID))
	selected := w.selectedKey()
	w.query = "name:api"
	w.render()
	w.Stop()
	w.render()
	require.Equal(t, selected, w.selectedKey())
	require.Equal(t, "name:api", w.query)
	require.Contains(t, w.rows[0].detail, "Last positive")
}

func TestDailyHistoryRowsRetainDistinctEventSelectionAndSourceUID(t *testing.T) {
	w, s := observedWorkspaceFixture()
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Findings = nil
	w.acceptSnapshot(s, nil)
	w.render()
	require.Equal(t, "H: cleared", w.rows[0].cells[0])
	w.table.Select(1, 0)
	selected := w.selectedKey()
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Coverage[0].State = testWorkspaceCoverageDenied
	w.acceptSnapshot(s, errors.New("partial refresh"))
	w.render()
	require.Equal(t, selected, w.selectedKey(), "new gap row does not select another event at same identity")
	require.Contains(t, w.rows[1].evidence, "not proof of cause resolution")
	require.Contains(t, w.rows[1].evidence, "api-uid")
	w.table.Select(1, 0)
	require.Error(t, w.SelectedResource().Err(), "query gap has no invented resource target")
	window := w.observationWindow
	w.resetObservationWindow(s.ObservedAt.Add(time.Minute))
	require.NotSame(t, window, w.observationWindow)
	require.Empty(t, w.observationWindow.History)
	require.Empty(t, w.jobSources)
}

func TestDailyHistoryRetainsScheduleAssumptionsWithoutAdditionalCollection(t *testing.T) {
	w, s := observedWorkspaceFixture()
	w.scope.Kinds = append(w.scope.Kinds, "cronjobs")
	w.resetObservationWindow(s.ObservedAt)
	source := jobReviewTestSource()
	source.SetNamespace(testWorkspaceNamespace)
	ref := workspace.ResourceRef{GVR: "batch/v1/cronjobs", Namespace: testWorkspaceNamespace, Name: source.GetName(), UID: string(source.GetUID())}
	s.Resources = append(s.Resources, workspace.Resource{Ref: ref, Kind: "CronJob", Object: source})
	s.Coverage = append(s.Coverage, workspace.Coverage{GVR: ref.GVR, Namespace: ref.Namespace, State: dailyWorkspaceCoverageComplete})
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	w.acceptSnapshot(s, nil)
	w.render()
	require.Len(t, w.jobSources, 1)
	row := w.rows[len(w.rows)-1]
	require.Contains(t, row.detail, "nonexistent local times")
	require.Contains(t, row.detail, "suspended")
	require.Contains(t, row.evidence, "Future matching schedule times only")
	require.Contains(t, row.evidence, "Not queried in this workspace refresh")
	require.NotContains(t, row.evidence, "missed execution detected")
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Resources = nil
	s.Findings = nil
	s.Coverage = nil
	w.acceptSnapshot(s, errors.New("forbidden"))
	w.render()
	require.Len(t, w.jobSources, 1)
	require.Contains(t, w.rows[len(w.rows)-1].detail, "Not observed in the latest refresh")
	require.True(t, w.jobSources[dailyWorkspaceRefKey(&ref)].CapturedAt.Before(w.observationWindow.LastRefreshAt))
}

func TestDailyHistoryNativeWidthsExposeStartUnknownAndEvidence(t *testing.T) {
	w, s := observedWorkspaceFixture()
	require.NoError(t, w.app.Styles.Load("../../skins/monochrome.yaml", false))
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Findings = nil
	s.Coverage[0].State = testWorkspaceCoverageDenied
	w.acceptSnapshot(s, nil)
	w.Flex.SetDirection(tview.FlexRow).AddItem(w.header, 4, 0, false).AddItem(w.table, 0, 1, true).AddItem(w.detail, 3, 0, false).AddItem(w.footer, 1, 0, false)
	w.render()
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		frame := drawnText(t, w, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("observed-history-%dx%d", size[0], size[1]), frame)
		for _, want := range []string{"[6 History]", "unknown", "Enter", "? help"} {
			require.Contains(t, frame, want, "%dx%d", size[0], size[1])
		}
		require.Contains(t, frame, "api")
		require.Contains(t, frame, "CrashLoopBackOff")
		require.NotContains(t, frame, "\x1b")
	}
	selected := w.selectedKey()
	require.Contains(t, drawnText(t, w, 39, 12), "Need 40x12")
	require.Contains(t, drawnText(t, w, 60, 24), "[6 History]")
	require.Equal(t, selected, w.selectedKey())
}

func TestDailyHistoryStaleRefreshCannotReplaceCurrentQueue(t *testing.T) {
	w, s := observedWorkspaceFixture()
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	w.acceptSnapshot(s, nil)
	prior := w.snapshot.ObservedAt
	s.ObservedAt = s.ObservedAt.Add(-time.Minute)
	s.Findings = nil
	s.Resources = nil
	w.acceptSnapshot(s, nil)
	require.Equal(t, prior, w.snapshot.ObservedAt)
	require.Len(t, w.snapshot.Findings, 1)
	require.Contains(t, w.notice, "Stale")
	require.Equal(t, workspace.QueueUnknown, w.observationWindow.Active()[0].State)
	require.Equal(t, "stale", w.coverage[0].State)
}

func TestDailyHistoryJobSourcesStayBoundedAndRejectOutsideScope(t *testing.T) {
	w, s := observedWorkspaceFixture()
	w.scope.Kinds = append(w.scope.Kinds, "cronjobs")
	w.resetObservationWindow(s.ObservedAt)
	for i := range maxWorkspaceJobSources + 3 {
		object := jobReviewTestSource()
		object.SetNamespace(testWorkspaceNamespace)
		object.SetName(fmt.Sprintf("backup-%d", i))
		object.SetUID("cron-uid")
		ref := workspace.ResourceRef{GVR: "batch/v1/cronjobs", Namespace: testWorkspaceNamespace, Name: object.GetName(), UID: string(object.GetUID())}
		s.Resources = append(s.Resources, workspace.Resource{Ref: ref, Kind: "CronJob", Object: object})
	}
	outside := jobReviewTestSource()
	outside.SetNamespace("outside")
	s.Resources = append(s.Resources, workspace.Resource{Ref: workspace.ResourceRef{GVR: "batch/v1/cronjobs", Namespace: "outside", Name: outside.GetName(), UID: string(outside.GetUID())}, Object: outside})
	w.acceptSnapshot(s, nil)
	require.Len(t, w.jobSources, maxWorkspaceJobSources)
	require.Equal(t, 3, w.jobSourcesOmitted)
	s.ObservedAt = s.ObservedAt.Add(workspace.QueueRetention + time.Minute)
	s.Resources = nil
	w.acceptSnapshot(s, nil)
	require.Empty(t, w.jobSources)
	require.Equal(t, maxWorkspaceJobSources+3, w.jobSourcesOmitted)
}
