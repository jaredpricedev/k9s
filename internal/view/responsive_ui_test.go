// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

const responsiveTestNS, responsiveOOMReason = "apps", "OOMKilled"

func saveResponsiveFrame(t *testing.T, name, frame string) {
	t.Helper()
	if dir := os.Getenv("K9S_CAPTURE_RESPONSIVE_DIR"); dir != "" {
		// #nosec G703 -- Only the trusted local test capture harness sets this output directory.
		require.NoError(t, os.MkdirAll(dir, 0750))
		// #nosec G703 -- This path never contains cluster or operator input.
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(frame), 0600))
	}
}

func TestWorkspaceCoveragePrioritizesGapsAndRetainsSelectionAcrossReasonUpdates(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.mode = dailyWorkspaceCoverageMode
	w.coverage = []workspace.Coverage{{GVR: podCmd, Namespace: responsiveTestNS, State: "complete"}, {GVR: "v1/services", Namespace: responsiveTestNS, State: testWorkspaceCoverageDenied, Detail: "list forbidden"}, {GVR: "v1/events", Namespace: "ops", State: "truncated", Detail: "bounded events"}}
	w.render()
	require.Equal(t, "truncated", w.rows[0].cells[0])
	require.Equal(t, testWorkspaceCoverageDenied, w.rows[1].cells[0])
	require.Equal(t, "complete", w.rows[2].cells[0])
	w.table.Select(2, 0)
	prior := w.selectedKey()
	w.coverage[1].Detail = "new authorization reason"
	w.render()
	require.Equal(t, prior, w.selectedKey())
	require.Contains(t, drawnText(t, w.header, 120, 4), "[3 Coverage]")
	require.Contains(t, w.detail.GetText(true), "v1/services")
	require.Contains(t, w.detail.GetText(true), "new authorization reason")
}

func TestWorkspaceResponsiveActiveTabsAndFaultIdentity(t *testing.T) {
	w := dailyWorkspaceFixture()
	w.mode = dailyWorkspaceQueueMode
	w.snapshot = workspace.Snapshot{ObservedAt: time.Now(), Findings: []workspace.Finding{{Ref: workspaceFixtureRef("京都-api-with-a-long-name"), Kind: inspectionPodKind, Severity: "warning", Reason: testWorkspaceCrashLoopReason, Detail: "current cause unconfirmed"}}}
	w.coverage = []workspace.Coverage{{GVR: podCmd, Namespace: "backend", State: testWorkspaceCoverageDenied, Detail: "Unknown coverage"}}
	w.Flex.SetDirection(tview.FlexRow).AddItem(w.header, 4, 0, false).AddItem(w.table, 0, 1, true).AddItem(w.detail, 3, 0, false).AddItem(w.footer, 1, 0, false)
	w.render()
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			frame := drawnText(t, w, size[0], size[1])
			saveResponsiveFrame(t, fmt.Sprintf("workspace-%dx%d", size[0], size[1]), frame)
			require.Contains(t, frame, "[1 Daily]")
			require.Contains(t, frame, testWorkspaceCrashLoopReason)
			require.Contains(t, frame, "gaps")
			require.Contains(t, frame, "? help")
		})
	}
	frame := drawnText(t, w, 39, 16)
	require.Contains(t, frame, "Need 40x12")
	require.Len(t, w.rows, 1)
	require.Empty(t, w.query)
	frame = drawnText(t, w, 60, 24)
	require.Contains(t, frame, testWorkspaceCrashLoopReason)
}

func TestInvestigationNarrowRowsNeverWrapAndFullEvidenceRemainsAvailable(t *testing.T) {
	d := compactInvestigationFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		frame := drawInvestigation(t, d, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("investigation-%dx%d", size[0], size[1]), frame)
		for _, want := range []string{"CURRENT FINDINGS", testWorkspaceCrashLoopReason, responsiveOOMReason, "NEXT CHECKS"} {
			require.Contains(t, frame, want)
		}
		require.Equal(t, 1, strings.Count(frame, testWorkspaceCrashLoopReason), "current fault rendered once")
	}
	for _, width := range []int{118, 78, 58, 38} {
		text := investigationContainerTable(d.snapshot.Investigation, width, 4)
		for _, line := range strings.Split(text, "\n") {
			require.LessOrEqual(t, tview.TaggedStringWidth(line), width)
		}
	}
	d.selectInvestigationTab(4)
	require.Contains(t, d.text.GetText(true), "second full source line")
	require.NoError(t, d.app.Styles.Load("../../skins/monochrome.yaml", false))
	d.StylesChanged(d.app.Styles)
	frame := drawInvestigation(t, d, 39, 16)
	require.Contains(t, frame, "Need 40x12")
	require.Equal(t, 4, d.activeTab)
	frame = drawInvestigation(t, d, 60, 24)
	require.Contains(t, frame, "[5 Evidence]")
}

func TestWorkspaceFormStackedLabelsKeepLongNamespaceInput(t *testing.T) {
	form := tview.NewForm().SetItemPadding(0)
	form.AddInputField("Name", "daily", 36, nil, nil).AddInputField("Namespaces (comma-separated)", "backend,frontend", 48, nil, nil).AddButton("Cancel", nil).AddButton("Save", nil)
	frame := tview.NewFrame(form)
	modal := &dailyWorkspaceModal{Frame: frame, form: form, message: "Saved locally. Explicit namespace selection.", color: config.DefaultSemanticPalette().Text.Color()}
	app := tview.NewApplication().SetRoot(modal, true)
	form.SetFocus(1)
	app.SetFocus(modal)
	for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 16}} {
		text := drawnText(t, modal, size[0], size[1])
		saveResponsiveFrame(t, fmt.Sprintf("workspace-form-%dx%d", size[0], size[1]), text)
		require.Contains(t, text, "Namespaces")
		require.Contains(t, text, "backend,frontend")
		require.Contains(t, text, "Save")
	}
}

func TestFailedCommandRecoveryRestoresExactEditableInputWithoutExecuting(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	command := "unknown 京都 --selector=app=api"
	a.restoreFailedCommand(command)
	require.Equal(t, command, a.CmdBuff().GetText())
	require.True(t, a.CmdBuff().IsActive())
	require.Nil(t, a.Content.Top(), "recovery only edits input; it never dispatches a command")
}
