// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

func TestAutomaticChromePreservesDestinationAndManualPreference(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	require.NoError(t, a.Init("v0.1.0", 10))
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	main := a.Main.GetPrimitive("main")
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 34}, {80, 24}} {
		screen.SetSize(size[0], size[1])
		main.SetRect(0, 0, size[0], size[1])
		main.Draw(screen)
		_, y, _, height := a.statusIndicator().GetRect()
		require.Zero(t, y, "destination is always the first row")
		require.Equal(t, 1, height)
		require.Contains(t, a.statusIndicator().GetText(true), "ctx:")
		require.Contains(t, a.statusIndicator().GetText(true), "ns:")
		require.Equal(t, size[0] >= 120, a.showHeader)
		if !a.showHeader {
			require.Contains(t, a.Menu().GetCell(0, 0).Text, "Actions")
			require.Contains(t, a.Menu().GetCell(0, 0).Text, "Help")
		}
	}
	// Opening a command/filter prompt inserts it after the persistent chrome.
	a.App.BufferActive(true, model.CommandBuffer)
	main.Draw(screen)
	_, y, _, _ := a.statusIndicator().GetRect()
	require.Zero(t, y)
	require.Contains(t, a.statusIndicator().GetText(true), "ctx:")
	a.App.BufferActive(false, model.CommandBuffer)
	a.Config.K9s.UI.HeaderMode = "full"
	main.Draw(screen)
	require.True(t, a.showHeader, "explicit full remains full even at 80×24")
	a.toggleHeader(false, a.showLogo)
	screen.SetSize(180, 40)
	main.SetRect(0, 0, 180, 40)
	main.Draw(screen)
	require.False(t, a.showHeader, "manual compact preference survives resize")
	styles := config.NewStyles()
	require.NoError(t, styles.Load("../../skins/monochrome.yaml", false))
	a.statusIndicator().StylesChanged(styles)
	main.Draw(screen)
	require.Contains(t, a.statusIndicator().GetText(true), "ctx:")
}

func TestHeaderReclaimsUnusedClusterInfoSpace(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	_ = a.Init("v0.1.0", 10)
	a.showHeader = true
	a.showLogo = true
	info := a.clusterInfo()
	info.layout()
	for row, value := range []string{"default [RW]", "default", "default", "v0.1.0", "v1.36.4+k3s1", "2%", "20%"} {
		info.setCell(row, value)
	}
	var hints model.MenuHints
	for i := range 18 {
		hints = append(hints, model.MenuHint{Mnemonic: string(rune('a' + i)), Description: "Inspect source and dependencies", Visible: true})
	}
	a.Menu().HydrateMenu(hints)
	header := a.buildHeader()
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(180, 7)
	header.SetRect(0, 0, 180, 7)
	header.Draw(screen)
	_, _, width, _ := info.GetRect()
	require.LessOrEqual(t, width, 28, "short cluster metadata must not reserve 50 columns")
	_, _, menuWidth, _ := a.Menu().GetRect()
	require.GreaterOrEqual(t, menuWidth, 120, "reclaimed space belongs to the shortcut labels")
	info.setCell(0, "production-east-management [RW]")
	header.Draw(screen)
	_, _, expanded, _ := info.GetRect()
	require.Greater(t, expanded, width, "context changes must resize the information block")
	screen.SetSize(100, 7)
	header.SetRect(0, 0, 100, 7)
	header.Draw(screen)
	_, _, narrow, _ := info.GetRect()
	require.LessOrEqual(t, narrow, 33, "long contexts must leave room for shortcuts")
	_, _, logoWidth, _ := a.Logo().GetRect()
	require.LessOrEqual(t, logoWidth, 7, "a narrow header must compact the logo to protect shortcut text")
	require.Contains(t, a.Logo().Logo().GetText(true), "[k9+]")
}

func TestHeaderMeasuresIndependentColumnWidthsWithinBudget(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	a.showHeader, a.showLogo = true, true
	info := a.clusterInfo()
	info.layout()
	user := strings.Repeat("u", 17)
	for row, value := range []string{"dev [RW]", "demo", user, "v0.1.0", "v1.34.0", "unavailable", "not configured"} {
		info.setCell(row, value)
	}
	header := a.buildHeader()
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	for _, width := range []int{80, 100, 120} {
		screen.SetSize(width, 7)
		header.SetRect(0, 0, width, 7)
		header.Draw(screen)
		_, _, actual, _ := info.GetRect()
		// The widest label (Context:) and value (User) belong to different rows.
		require.Equal(t, min(29, width/3), actual)
		if width >= 100 {
			var line strings.Builder
			for x := range actual {
				ch, _, _, _ := screen.GetContent(x, 2)
				line.WriteRune(ch)
			}
			require.Contains(t, line.String(), user, "independent column widths must preserve the full value when it fits")
		}
	}
}
