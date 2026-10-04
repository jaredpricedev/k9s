// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func TestMonochromeDrawingRetainsWordsAndSelection(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	t.Cleanup(screen.Fini)
	screen.SetSize(60, 12)
	styles := config.NewStyles()
	p := styles.Semantic()
	table := tview.NewTable().SetSelectable(true, false)
	table.SetBackgroundColor(p.Canvas.Color())
	table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(p.Text.Color()).Bold(true))
	table.SetCell(0, 0, tview.NewTableCell("CURRENT CrashLoopBackOff | PREVIOUS OOMKilled"))
	table.SetCell(1, 0, tview.NewTableCell("Unavailable: metrics permission denied"))
	table.SetRect(0, 0, 60, 12)
	root := &monochromeRoot{Primitive: table, styles: styles}
	root.Draw(screen)
	var words string
	for y := range 12 {
		for x := range 60 {
			ch, _, style, _ := screen.GetContent(x, y)
			fg, bg, attrs := style.Decompose()
			require.Equal(t, tcell.ColorDefault, fg)
			require.Equal(t, tcell.ColorDefault, bg)
			words += string(ch)
			if y == 0 && x < len("CURRENT") {
				require.NotZero(t, attrs&tcell.AttrReverse, "selection must be visible without color")
			}
		}
	}
	require.Contains(t, words, "CURRENT CrashLoopBackOff")
	require.Contains(t, words, "PREVIOUS OOMKilled")
	require.Contains(t, words, "Unavailable: metrics permission denied")
}
