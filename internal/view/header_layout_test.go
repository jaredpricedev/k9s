// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	"testing"
)

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
