// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import "testing"

// Cell is one cell of a page drawn by tview, for the tests of the
// reference pages, which live outside the package.
type Cell struct {
	R         rune
	Fg, Bg    string
	Bold, Dim bool
}

// Screen draws lines through tview on a simulated screen.
func Screen(t *testing.T, lines []string, width int) [][]Cell {
	t.Helper()
	drawn := draw(t, lines, width)
	out := make([][]Cell, len(drawn))
	for y, row := range drawn {
		out[y] = make([]Cell, len(row))
		for x, c := range row {
			out[y][x] = Cell{R: c.r, Fg: c.fg, Bg: c.bg, Bold: c.bold, Dim: c.dim}
		}
	}
	return out
}

// Plain is lines as they read on the screen.
func Plain(lines []string) []string { return plainAll(lines) }

// TestPalette is the stock semantic palette.
var TestPalette = testPalette
