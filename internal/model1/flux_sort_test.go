// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model1

import (
	"slices"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFluxStatusSortPrioritizesProblemsAndPreservesLookup(t *testing.T) {
	states := []string{"Ready", "Suspended", "Pending", "Reconciling", "Unknown", "Restricted", "Failed"}
	for _, asc := range []bool{true, false} {
		events := NewRowEvents(len(states) + 1)
		for _, status := range states {
			events.Add(RowEvent{Row: Row{ID: status, Fields: Fields{"resource", status}}})
		}
		events.Add(RowEvent{Row: Row{ID: "Failed-z", Fields: Fields{"resource", "Failed"}}})
		// Reordered header must use STATUS by name.
		data := NewTableDataWithRows(client.FluxGVR, Header{{Name: "NAME"}, {Name: "STATUS"}}, events)
		data.Sort(SortColumn{Name: "STATUS", ASC: asc})
		want := []string{"Failed", "Failed-z", "Restricted", "Unknown", "Reconciling", "Pending", "Suspended", "Ready"}
		if !asc {
			slices.Reverse(want)
		}
		got := make([]string, 0, len(want))
		data.RowsRange(func(_ int, re RowEvent) bool { got = append(got, re.Row.ID); return true })
		assert.Equal(t, want, got)
		for _, id := range want {
			row, ok := data.FindRow(id)
			require.True(t, ok)
			assert.Equal(t, id, row.Row.ID)
		}
	}
}

func TestFluxSortDoesNotChangeOtherResourcesOrColumns(t *testing.T) {
	for _, tt := range []struct {
		gvr    *client.GVR
		column string
	}{
		{client.NewGVR("v1/pods"), "STATUS"},
		{client.FluxGVR, "NAME"},
	} {
		events := NewRowEvents(2)
		events.Add(RowEvent{Row: Row{ID: "z", Fields: Fields{"Unknown", "Unknown"}}})
		events.Add(RowEvent{Row: Row{ID: "a", Fields: Fields{"Ready", "Ready"}}})
		data := NewTableDataWithRows(tt.gvr, Header{{Name: "NAME"}, {Name: "STATUS"}}, events)
		data.Sort(SortColumn{Name: tt.column, ASC: true})
		first, ok := data.RowAt(0)
		require.True(t, ok)
		assert.Equal(t, "a", first.Row.ID)
	}
}
