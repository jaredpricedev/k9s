// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
)

func TestDisplayWidthMatchesTerminalSemantics(t *testing.T) {
	for _, text := range []string{"", "Running", "10.0.1.42", "a b!~", "[red::b]ready[-::]", "[red[]literal", "[broken", "日本語", "e\u0301", "👩‍💻", "a\tb", "a\nb", "\x1b", "\x00", "\x7f"} {
		assert.Equal(t, tview.TaggedStringWidth(text), displayWidth(text), "%q", text)
	}
}

func TestDisplayWidthASCIIHasNoAllocations(t *testing.T) {
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = displayWidth("deployment-12345") }))
}

func TestTableComputesColorOncePerRow(t *testing.T) {
	table := NewTable(client.NewGVR("test"))
	header := model1.Header{{Name: "A"}, {Name: "B"}, {Name: "C"}}
	row := model1.RowEvent{Row: model1.Row{ID: "row", Fields: model1.Fields{"a", "b", "c"}}}
	calls := 0
	table.SetColorerFn(func(_ string, _ model1.Header, _ *model1.RowEvent) tcell.Color { calls++; return tcell.ColorGreen })
	table.buildRow(0, row, row, header, MaxyPad{2, 2, 2})
	assert.Equal(t, 1, calls)
	for col := range 3 {
		assert.Equal(t, tcell.ColorGreen, table.GetCell(0, col).Color)
	}
}

func TestTableSkipsColorForRowsWithoutVisibleCells(t *testing.T) {
	table := NewTable(client.NewGVR("test"))
	table.SetColorerFn(func(_ string, _ model1.Header, _ *model1.RowEvent) tcell.Color {
		t.Fatal("colorer must not inspect an invisible row")
		return tcell.ColorRed
	})
	table.buildRow(0, model1.RowEvent{}, model1.RowEvent{}, nil, nil)
	row := model1.RowEvent{Row: model1.Row{Fields: model1.Fields{"hidden"}}}
	table.buildRow(0, row, row, model1.Header{{Name: "HIDDEN", Attrs: model1.Attrs{Hide: true}}}, MaxyPad{7})
}
