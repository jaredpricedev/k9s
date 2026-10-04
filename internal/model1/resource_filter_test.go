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

func TestResourceFiltersPreserveDocumentedModes(t *testing.T) {
	data := NewTableDataWithRows(client.NewGVR("test"), Header{
		HeaderColumn{Name: "NAME"}, HeaderColumn{Name: "STATUS"},
	}, NewRowEventsWithEvts(
		RowEvent{Row: Row{ID: "payments", Fields: Fields{"payments", "ready now"}}},
		RowEvent{Row: Row{ID: "orders", Fields: Fields{"orders", "pending"}}},
	))
	for _, test := range []struct {
		query, mode string
		count       int
	}{
		{"payments", "regex", 1},
		{"ready now", "regex", 1},
		{"payments ready now", "regex", 1},
		{"never matches", "regex", 0},
		{"!ready now", "inverse regex", 1},
		{"-f pmt", "fuzzy", 1},
		{"app=payments", "labels", 2},
		{"-l app in (payments, orders)", "labels", 2},
		{"", "regex", 2},
	} {
		t.Run(test.query, func(t *testing.T) {
			mode, err := ValidateResourceFilter(test.query)
			require.NoError(t, err)
			assert.Equal(t, test.mode, mode)
			result, err := data.FilterChecked(FilterOpts{Filter: test.query})
			require.NoError(t, err)
			assert.Equal(t, test.count, result.RowCount())
			assert.Equal(t, 2, data.RowCount(), "filtering must not replace source rows")
		})
	}
}

func TestResourceFilterErrorsAreExplicit(t *testing.T) {
	data := NewTableDataWithRows(client.NewGVR("test"), Header{{Name: "NAME"}},
		NewRowEventsWithEvts(RowEvent{Row: Row{ID: "payments", Fields: Fields{"payments"}}}))
	for _, query := range []string{"[", "!([", "-l app in (payments"} {
		t.Run(query, func(t *testing.T) {
			_, err := ValidateResourceFilter(query)
			require.Error(t, err)
			result, err := data.FilterChecked(FilterOpts{Filter: query})
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Zero(t, data.Filter(FilterOpts{Filter: query}).RowCount(), "invalid input must not appear as all rows")
		})
	}
}
