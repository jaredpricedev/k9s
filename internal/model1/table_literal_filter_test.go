// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model1

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiteralResourceFiltersKeepRegexAndUnicodeSemantics(t *testing.T) {
	data := NewTableDataWithRows(client.NewGVR("test"),
		Header{{Name: "NAME"}, {Name: "STATUS"}, {Name: "HIDDEN", Attrs: Attrs{Hide: true}}},
		NewRowEventsWithEvts(
			RowEvent{Row: Row{ID: "ascii", Fields: Fields{"TARGET-00001", "Running Ⓕ", "private"}}},
			RowEvent{Row: Row{ID: "unicode", Fields: Fields{"Kubeſystem", "日本語", "private"}}},
			RowEvent{Row: Row{ID: "markup", Fields: Fields{"[red]target-00002", "Pending", "private"}}},
		))
	for _, tc := range []struct {
		query string
		ids   []string
	}{
		{query: "target-00001", ids: []string{"ascii"}},
		{query: "TaRgEt", ids: []string{"ascii", "markup"}},
		{query: "00001 running", ids: []string{"ascii"}},
		{query: "KUBESYSTEM", ids: []string{"unicode"}},
		{query: "日本語", ids: []string{"unicode"}},
		{query: "^target", ids: []string{"ascii"}},
		{query: "target-0000[12]", ids: []string{"ascii", "markup"}},
		{query: `\[red\]`, ids: []string{"markup"}},
		{query: "!target", ids: []string{"unicode"}},
		{query: "private"},
		{query: "no-match"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			filtered, err := data.FilterChecked(FilterOpts{Filter: tc.query})
			require.NoError(t, err)
			var ids []string
			filtered.RowsRange(func(_ int, row RowEvent) bool {
				ids = append(ids, row.Row.ID)
				return true
			})
			assert.Equal(t, tc.ids, ids)
		})
	}
	row, found := data.FindRow("ascii")
	require.True(t, found)
	assert.Equal(t, "TARGET-00001", row.Row.Fields[0], "matching must not mutate source text")
	_, err := data.FilterChecked(FilterOpts{Filter: "["})
	require.Error(t, err, "invalid regex must stay invalid")
}

func TestLiteralFilterKeepsUnicodeSeparatorsAndInvalidUTF8(t *testing.T) {
	data := NewTableDataWithRows(client.NewGVR("test"), Header{{Name: "NAME"}},
		NewRowEventsWithEvts(
			RowEvent{Row: Row{ID: "separated", Fields: Fields{"a日本語b"}}},
			RowEvent{Row: Row{ID: "invalid", Fields: Fields{"a\xffb"}}},
			RowEvent{Row: Row{ID: "folded", Fields: Fields{"AKBſC"}}},
		))
	for _, query := range []string{"ab", "Kb", "bS", "kbsc"} {
		filtered, err := data.FilterChecked(FilterOpts{Filter: query})
		require.NoError(t, err)
		if query == "ab" {
			assert.Zero(t, filtered.RowCount(), "Unicode separators must never be dropped")
		} else {
			require.Equal(t, 1, filtered.RowCount())
			_, found := filtered.FindRow("folded")
			assert.True(t, found)
		}
	}
}
