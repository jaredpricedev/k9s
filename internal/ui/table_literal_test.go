// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableLiteralFieldsRenderTagsAndPreserveRawValues(t *testing.T) {
	for _, literal := range []bool{true, false} {
		table := NewTable(client.FluxGVR)
		if literal {
			table.SetLiteralFields(true)
		}
		message := "[green][::i][green::b]controller failed"
		row := model1.RowEvent{Row: model1.Row{ID: "apps/test", Fields: model1.Fields{message, "Failed"}}}
		header := model1.Header{{Name: "MESSAGE"}, {Name: "STATUS"}}
		data := model1.NewTableDataWithRows(client.FluxGVR, header, model1.NewRowEventsWithEvts(row))
		pads := make(MaxyPad, len(header))
		computeMaxColumns(pads, "", data, literal)
		if literal {
			assert.Equal(t, len(message)+1, pads[0])
		}
		table.SetColorerFn(func(_ string, _ model1.Header, event *model1.RowEvent) tcell.Color {
			assert.Equal(t, message, event.Row.Fields[0])
			return tcell.ColorRed
		})
		table.buildRow(0, row, row, header, pads)
		cell := table.GetCell(0, 0)
		expected := message
		if literal {
			expected = "[green[][::i[][green::b[]controller failed"
		}
		assert.Equal(t, expected, strings.TrimSpace(cell.Text))
		assert.Equal(t, tcell.ColorRed, table.GetCell(0, 1).Color)
		screen := tcell.NewSimulationScreen("")
		require.NoError(t, screen.Init())
		screen.SetSize(100, 2)
		table.SetRect(0, 0, 100, 2)
		table.Draw(screen)
		var visible strings.Builder
		for x := range 100 {
			ch, _, _, _ := screen.GetContent(x, 0)
			visible.WriteRune(ch)
		}
		_, _, style, _ := screen.GetContent(len("[green][::i]"), 0)
		fg, _, attrs := style.Decompose()
		if literal {
			assert.True(t, strings.HasPrefix(visible.String(), message), visible.String())
			assert.Equal(t, tcell.ColorRed, fg)
			assert.Zero(t, attrs&tcell.AttrBold)
		} else {
			assert.True(t, strings.HasPrefix(visible.String(), "[green][::i]controller failed"), visible.String())
			assert.Equal(t, tcell.ColorGreen, fg)
			assert.NotZero(t, attrs&tcell.AttrBold)
		}
		screen.Fini()
		original, ok := data.FindRow("apps/test")
		require.True(t, ok)
		assert.Equal(t, message, original.Row.Fields[0])
	}
}

func TestTableLiteralFieldsKeepTrustedDecoratorsAndCompareRawDeltas(t *testing.T) {
	table := NewTable(client.FluxGVR)
	table.SetLiteralFields(true)
	header := model1.Header{{Name: "MESSAGE", Attrs: model1.Attrs{
		Decorator: func(value string) string { return "[blue::]" + value + "[-::]" },
	}}}
	raw := "[green]unchanged"
	row := model1.RowEvent{Row: model1.Row{Fields: model1.Fields{raw}}, Deltas: model1.DeltaRow{raw}}
	table.buildRow(0, row, row, header, MaxyPad{len(raw) + 1})
	assert.Equal(t, "[blue::][green[]unchanged[-::] ", table.GetCell(0, 0).Text)
	assert.NotContains(t, table.GetCell(0, 0).Text, DeltaSign)
	row.Deltas[0] = "previous"
	table.buildRow(0, row, row, header, MaxyPad{len(raw) + 1})
	assert.Contains(t, table.GetCell(0, 0).Text, "[green[]unchanged"+DeltaSign)
}

func TestTablePaddingUsesDisplayWidthAndPreservesTrustedTags(t *testing.T) {
	data := model1.NewTableDataWithRows(client.NewGVR("test"), model1.Header{{Name: "A"}, {Name: "B"}},
		model1.NewRowEventsWithEvts(model1.RowEvent{Row: model1.Row{Fields: model1.Fields{"界", "[red::b]ok[-::]"}}}))
	pads := make(MaxyPad, 2)
	ComputeMaxColumns(pads, "", data)
	assert.Equal(t, MaxyPad{3, 3}, pads)
	assert.Equal(t, "界   ", formatCell("界", 5))
	assert.Equal(t, "[red::b]ok[-::]   ", formatCell("[red::b]ok[-::]", 5))
	// Table handles viewport clipping; formatting must never split control tags or runes.
	assert.Equal(t, "[red::b]界[-::]", formatCell("[red::b]界[-::]", 1))
}

func TestTableNarrowViewportKeepsUnicodeAndTrustedStyling(t *testing.T) {
	table := NewTable(client.NewGVR("test"))
	header := model1.Header{{Name: "MESSAGE"}}
	row := model1.RowEvent{Row: model1.Row{Fields: model1.Fields{"[red::b]界界界[-::]"}}}
	table.buildRow(0, row, row, header, MaxyPad{1})
	assert.Equal(t, "[red::b]界界界[-::]", table.GetCell(0, 0).Text)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(4, 1)
	table.SetRect(0, 0, 4, 1)
	table.Draw(screen)
	first, _, style, _ := screen.GetContent(0, 0)
	assert.Equal(t, '界', first)
	fg, _, attrs := style.Decompose()
	assert.Equal(t, tcell.ColorRed, fg)
	assert.NotZero(t, attrs&tcell.AttrBold)
}
