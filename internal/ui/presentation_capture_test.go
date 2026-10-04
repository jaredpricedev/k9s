// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

type presentationCell struct {
	Text string
	Fg   int32
	Bg   int32
	Bold bool
}

type presentationFixture struct {
	Source, Skin, ColorMode string
	Width, Height           int
	NoIcons                 bool
	Cells                   [][]presentationCell
}

// TestCapturePresentationFixture exports deterministic simulated terminal cells.
// The 256-color fixture approximates palette quantization; neither output is a
// live-cluster screenshot or an operator-research result.
func TestCapturePresentationFixture(t *testing.T) {
	dir := os.Getenv("K9S_RENDER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set K9S_RENDER_FIXTURE_DIR to export verified terminal-cell fixtures")
	}
	require.NoError(t, os.MkdirAll(dir, 0755))
	for _, skin := range []string{"stock", "high-contrast", "monochrome"} {
		styles := config.NewStyles()
		if skin != "stock" {
			require.NoError(t, styles.Load("../../skins/"+skin+".yaml", false))
			styles.Update()
		}
		table, _ := podLayoutFixture(t, true, styles)
		screen := tcell.NewSimulationScreen("")
		require.NoError(t, screen.Init())
		width, height := 80, 12
		screen.SetSize(width, height)
		table.SetRect(0, 0, width, height)
		table.Draw(screen)
		for _, mode := range []string{"true-color", "256"} {
			capture := presentationFixture{Source: "tcell simulation screen", Skin: skin, ColorMode: mode,
				Width: width, Height: height, NoIcons: true,
				Cells: capturePresentationCells(screen, styles, mode == "256", width, height)}
			body, err := json.Marshal(capture)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("pods-80x12-%s-%s.json", skin, mode)), body, 0600))
		}
		screen.Fini()
	}
}

func capturePresentationCells(screen tcell.Screen, styles *config.Styles, quantize bool, width, height int) [][]presentationCell {
	palette := make([]tcell.Color, 256)
	for i := range palette {
		palette[i] = tcell.PaletteColor(i)
	}
	var cells [][]presentationCell
	for y := range height {
		var row []presentationCell
		for x := range width {
			ch, combining, style, _ := screen.GetContent(x, y)
			fg, bg, attr := style.Decompose()
			if fg.Hex() < 0 {
				fg = styles.Semantic().Text.Color()
			}
			if bg.Hex() < 0 {
				bg = styles.Semantic().Canvas.Color()
			}
			if quantize {
				fg, bg = tcell.FindColor(fg, palette), tcell.FindColor(bg, palette)
			}
			row = append(row, presentationCell{string(ch) + string(combining), fg.Hex(), bg.Hex(), attr&tcell.AttrBold != 0})
		}
		cells = append(cells, row)
	}
	return cells
}
