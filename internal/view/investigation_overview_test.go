// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const investigationNotReadyReason = "ContainersNotReady"

func compactInvestigationFixture(t *testing.T) *inspectionDetails {
	t.Helper()
	a := NewApp(mock.NewMockConfig(t))
	d := &inspectionDetails{Details: NewDetails(a, troubleshootCommand, "apps/api", contentInspection, true)}
	require.NoError(t, d.Init(context.Background()))
	t.Cleanup(d.Stop)
	at := time.Now().UTC().Add(-18 * time.Second)
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "api-7fc4c9", "namespace": "apps", "uid": "da9bf836-77a3-471b-8c36-1ec01adf9241"}, "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": investigationNotReadyReason}}, "containerStatuses": []any{map[string]any{"name": testWorkspaceAPIName, "ready": false, "restartCount": int64(7), "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "full current fault [red] is literal\nsecond full source line"}}, "lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": int64(137), "finishedAt": at.Add(-6 * time.Minute).Format(time.RFC3339)}}}, map[string]any{"name": "proxy", "ready": true, "restartCount": int64(0), "state": map[string]any{"running": map[string]any{}}}}}, "spec": map[string]any{"nodeName": "worker-02", "containers": []any{map[string]any{"name": testWorkspaceAPIName, "resources": map[string]any{"limits": map[string]any{"memory": "256Mi"}, "requests": map[string]any{"cpu": "100m", "memory": "128Mi"}}}}}}}
	i := inspect.NewInvestigation(o, "demo-dev", "v1/pods", at)
	i.Coverage = append(i.Coverage, inspect.InvestigationCoverage{Source: "events", State: inspect.ObservationComplete, Detail: "UID scoped; retained history"})
	d.acceptSnapshot(inspectionSnapshot{Text: resourceSummaryAt(o, at), UID: o.GetUID(), CapturedAt: at, Investigation: i}, nil)
	return d
}

func drawInvestigation(t *testing.T, d *inspectionDetails, width, height int) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(width, height)
	d.SetRect(0, 0, width, height)
	d.Draw(screen)
	var out strings.Builder
	for y := range height {
		var row strings.Builder
		for x := range width {
			r, _, _, _ := screen.GetContent(x, y)
			row.WriteRune(r)
		}
		out.WriteString(strings.TrimRight(row.String(), " ") + "\n")
	}
	return out.String()
}

func TestInvestigationNativeOverviewAtCompactAndWideSizes(t *testing.T) {
	d := compactInvestigationFixture(t)
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 34}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			text := drawInvestigation(t, d, size.width, size.height)
			for _, want := range []string{"CURRENT FINDINGS", "CrashLoopBackOff", testWorkspaceAPIName, "Previous termination: OOMKilled", "RESTART", "proxy", "5 Evidence", "age", "metrics: not collected", "NEXT CHECKS", "l Pod logs"} {
				require.Contains(t, text, want)
			}
			require.Equal(t, 1, strings.Count(text, "CrashLoopBackOff"), "show the current fault once")
			require.NotContains(t, text, "second full source line")
			require.NotContains(t, text, "::b]")
			if dir := os.Getenv("K9S_CAPTURE_INVESTIGATION_DIR"); dir != "" {
				// #nosec G703 -- The optional output path comes from the trusted local capture harness, never cluster data.
				require.NoError(t, os.MkdirAll(dir, 0750))
				// #nosec G703 -- The optional output path comes from the trusted local capture harness, never cluster data.
				require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("investigation-%dx%d.txt", size.width, size.height)), []byte(text), 0600))
			}
		})
	}
}

func TestTypedInvestigationRetainsTabsQueryScrollAndFailedRefresh(t *testing.T) {
	d := compactInvestigationFixture(t)
	d.BufferCompleted("CrashLoop", "")
	d.cmdBuff.SetText("CrashLoop", "", true)
	d.currentRegion = 1
	d.text.Highlight("search_1")
	d.text.ScrollTo(3, 2)
	d.selectInvestigationTab(investigationEvidenceTab)
	require.Empty(t, d.inspectionQuery)
	require.Contains(t, d.text.GetText(true), "second full source line")
	d.BufferCompleted("full", "")
	d.cmdBuff.SetText("full", "", true)
	d.selectInvestigationTab(0)
	require.Equal(t, "CrashLoop", d.inspectionQuery)
	require.Equal(t, "CrashLoop", d.cmdBuff.GetText())
	// The current fault now appears once. Returning to that same retained
	// search clamps the old duplicate result index to its sole remaining match.
	require.Equal(t, 0, d.currentRegion)
	require.Equal(t, []string{"search_0"}, d.text.GetHighlights())
	row, col := d.text.GetScrollOffset()
	require.Equal(t, 3, row)
	require.Equal(t, 2, col)
	snapshot := d.snapshot
	d.acceptSnapshot(inspectionSnapshot{}, fmt.Errorf("permission denied"))
	require.Equal(t, snapshot, d.snapshot)
	require.Contains(t, d.identityBar.GetText(true), "Refresh failed")
	d.StylesChanged(d.app.Styles)
	require.Equal(t, "CrashLoop", d.inspectionQuery)
	row, col = d.text.GetScrollOffset()
	require.Equal(t, 3, row)
	require.Equal(t, 2, col)
	d.Stop()
	d.Start()
	require.Equal(t, 0, d.activeTab)
	require.Equal(t, snapshot, d.snapshot)
	d.selectInvestigationTab(investigationEvidenceTab)
	require.Equal(t, "full", d.inspectionQuery)
	require.Contains(t, d.text.GetText(true), "RETAINED SNAPSHOT")
}

func TestInvestigationTabsAreRegisteredAndDoNotStealSearchInput(t *testing.T) {
	d := compactInvestigationFixture(t)
	for _, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, tcell.KeyTab, tcell.KeyBacktab} {
		_, ok := d.actions.Get(key)
		require.True(t, ok)
	}
	key, _ := d.actions.Get(ui.Key2)
	key.Action(tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone))
	require.Equal(t, 1, d.activeTab)
	d.cmdBuff.SetActive(true)
	key, _ = d.actions.Get(ui.Key3)
	event := tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone)
	require.Same(t, event, key.Action(event))
	require.Equal(t, 1, d.activeTab)
}

func TestInvestigationPodLogsAvailabilityChecksCapturedTarget(t *testing.T) {
	d := compactInvestigationFixture(t)
	d.target = SelectedResourceTarget{Context: d.app.Config.ActiveContextName(), GVR: client.PodGVR, Namespace: "apps", Name: testWorkspaceAPIName, UID: d.snapshot.UID}
	action, ok := d.actions.Get(ui.KeyL)
	require.True(t, ok)
	require.True(t, action.Opts.RequiresSelection)
	require.Empty(t, action.Availability())
	d.target.Context = "another-cluster"
	require.Contains(t, action.Availability(), "Context changed")
	d.target.Context = d.app.Config.ActiveContextName()
	d.target.GVR = client.DpGVR
	require.Contains(t, action.Availability(), "Select a Pod")
	d.target.Name = ""
	require.NotEmpty(t, action.Availability())
}

func TestOverviewKeepsUnknownSeparateFromZeroAndPriorCrash(t *testing.T) {
	ready := true
	zero := int64(0)
	i := &inspect.Investigation{Containers: []inspect.InvestigationContainer{{Name: "now-running", Role: investigationAppRole, Ready: &ready, Restarts: &zero, State: "running", LastTermination: &inspect.ContainerTermination{Reason: "OOMKilled"}}, {Name: "unknown", Role: investigationAppRole, State: "unknown"}}}
	text := investigationOverview(i, 76)
	require.Contains(t, text, "No current fault established")
	require.NotContains(t, text, "[!] OOMKilled")
	require.Contains(t, text, "OOMKilled")
	require.Contains(t, text, "unknown")
	require.Contains(t, text, "?")
}
