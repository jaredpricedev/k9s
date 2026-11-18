// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/chart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The end of a generated name is what tells it from its siblings: a title
// that asks for it is shortened in the middle.
func TestTitleKeepsTheEndOfAGeneratedName(t *testing.T) {
	title := Title{View: "Graph", Object: "Pod apps/checkout-api-7d9f8c6b5d-x2x9q", Window: "last 15m", Read: minute(57), Done: 10, Total: 10}
	kept := title
	kept.KeepEnd = true

	assert.Equal(t, "Graph  Pod apps/checkout-api-7d9f… · last 15m  14:57 · 10/10", plain(TitleLine(title, 60, plainOpts)))
	assert.Equal(t, "Graph  Pod apps/chec…f8c6b5d-x2x9q · last 15m  14:57 · 10/10", plain(TitleLine(kept, 60, plainOpts)))
	assert.Equal(t, "Graph  Pod apps/c...6b5d-x2x9q | last 15m  14:57 | 10/10", plain(TitleLine(kept, 56, asciiOpts)))
	// A name that fits is written whole either way.
	assert.Equal(t, plain(TitleLine(title, 132, plainOpts)), plain(TitleLine(kept, 132, plainOpts)))
	for w := 8; w <= 140; w++ {
		line := TitleLine(kept, w, plainOpts)
		require.LessOrEqual(t, width(line), w, "width %d: %q", w, plain(line))
	}

	// Too little room for both ends: the name is cut at its end, marked.
	assert.Equal(t, "abc…", plainOpts.clipMiddle("abcdefghij", 4))
	assert.Equal(t, "abcd…hij", plainOpts.clipMiddle("abcdefghijklmnopqrstuvwxyzabcdefghij", 8))
	assert.Equal(t, "abcdefghij", plainOpts.clipMiddle("abcdefghij", 10))
	// Width is measured in cells: a wide rune is two.
	assert.LessOrEqual(t, cells(plainOpts.clipMiddle("日本語のポッド名前です", 8)), 8)
}

// A page whose window is not a stretch of time has no axis to draw; it is
// still a page.
func TestPageWithAChartAndNoWindowHasNoAxis(t *testing.T) {
	page := Page{
		Title:   Title{View: "Graph", Object: "Pod x2x9q"},
		Verdict: Verdict{Sentences: []string{"Memory has no samples."}},
		Chart: &Chart{Head: "  MEMORY", Gutter: 5, Draw: func(chart.Frame, int, int) []string {
			return nil
		}},
		Groups: []Group{{Label: "ALSO", Role: Secondary, Rows: []Row{{What: "CPU", Number: "no data", Strip: UnreadStrip()}}}},
		Next:   []Action{{Key: "r", Label: "read again"}},
	}
	plan := Lay(&page, 80, 20, plainOpts)
	var lines []string
	require.NotPanics(t, func() { lines = Render(&page, &plan, plainOpts).Lines() })
	text := strings.Join(plainAll(lines), "\n")
	assert.Contains(t, text, "Memory has no samples.")
	assert.Contains(t, text, "MEMORY")
	assert.NotContains(t, text, "UTC")
}
