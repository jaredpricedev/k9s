// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui_test

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func TestTaskTabsAlwaysExposeActiveLabelAndCycler(t *testing.T) {
	for _, width := range []int{120, 80, 60, 38} {
		line := ui.TaskTabs([]string{"Overview", "Containers", "Events", "Resources", "Evidence"}, 4, width)
		screen := tcell.NewSimulationScreen("")
		require.NoError(t, screen.Init())
		screen.SetSize(width, 1)
		tview.Print(screen, line, 0, 0, width, tview.AlignLeft, tcell.ColorDefault)
		var visible strings.Builder
		for x := range width {
			r, _, _, _ := screen.GetContent(x, 0)
			visible.WriteRune(r)
		}
		screen.Fini()
		require.Contains(t, visible.String(), "[5 Evidence]")
		require.LessOrEqual(t, tview.TaggedStringWidth(line), width)
		if width < 80 {
			require.Contains(t, line, "Tab next")
		}
	}
}
