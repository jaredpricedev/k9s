// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestTableDataDeletePreservesSurvivorsAndConsumesExistingKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keep      sets.Set[string]
		wantIDs   string
		remaining sets.Set[string]
	}{
		{name: "none", keep: sets.New("A", "B", "C", "D", "E", "F"), wantIDs: "A B C D E F", remaining: sets.New[string]()},
		{name: "all", keep: sets.New[string](), remaining: sets.New[string]()},
		{name: "nil keys"},
		{name: "alternating", keep: sets.New("A", "C", "E"), wantIDs: "A C E", remaining: sets.New[string]()},
		{name: "prefix", keep: sets.New("D", "E", "F"), wantIDs: "D E F", remaining: sets.New[string]()},
		{name: "suffix", keep: sets.New("A", "B", "C"), wantIDs: "A B C", remaining: sets.New[string]()},
		{name: "unknown keys", keep: sets.New("F", "new", "B"), wantIDs: "B F", remaining: sets.New("new")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := NewRowEvents(6)
			original := make(map[string]RowEvent, 6)
			for _, id := range strings.Fields("A B C D E F") {
				event := RowEvent{Kind: EventUpdate, Row: Row{ID: id, Fields: Fields{id, "ready"}}, Deltas: DeltaRow{"", "pending"}}
				events.Add(event)
				original[id] = event
			}
			table := NewTableDataWithRows(nil, nil, events)
			table.Delete(tc.keep)

			wantIDs := strings.Fields(tc.wantIDs)
			require.Equal(t, len(wantIDs), table.RowCount())
			assert.Equal(t, tc.remaining, tc.keep)
			for i, id := range wantIDs {
				event, ok := table.RowAt(i)
				require.True(t, ok)
				assert.Equal(t, original[id], event)
				found, ok := table.FindRow(id)
				require.True(t, ok)
				assert.Equal(t, event, found)
				index, ok := events.FindIndex(id)
				require.True(t, ok)
				assert.Equal(t, i, index)
			}
			for id := range original {
				_, found := table.FindRow(id)
				assert.Equal(t, sets.New(wantIDs...).Has(id), found, id)
			}
			for _, event := range events.events[len(wantIDs):cap(events.events)] {
				assert.Equal(t, RowEvent{}, event, "removed fields and deltas must not remain reachable through the backing array")
			}
		})
	}
}

func TestTableDataDeleteEmptyPreservesUnknownKeys(t *testing.T) {
	table := NewTableData(nil)
	keys := sets.New("new")
	table.Delete(keys)
	assert.Zero(t, table.RowCount())
	assert.Equal(t, sets.New("new"), keys)
}

func TestTableDataDeletePreservesDuplicateIDBehavior(t *testing.T) {
	events := NewRowEventsWithEvts(
		RowEvent{Row: Row{ID: "A", Fields: Fields{"first"}}},
		RowEvent{Row: Row{ID: "B", Fields: Fields{"middle"}}},
		RowEvent{Row: Row{ID: "A", Fields: Fields{"second"}}},
		RowEvent{Row: Row{ID: "A", Fields: Fields{"last"}}},
	)
	table := NewTableDataWithRows(nil, nil, events)
	keys := sets.New("A", "B")
	table.Delete(keys)

	// Existing deletion semantics remove the last indexed duplicate once.
	require.Equal(t, 3, table.RowCount())
	first, _ := table.RowAt(0)
	middle, _ := table.RowAt(1)
	last, _ := table.RowAt(2)
	assert.Equal(t, Fields{"first"}, first.Row.Fields)
	assert.Equal(t, "B", middle.Row.ID)
	assert.Equal(t, Fields{"second"}, last.Row.Fields)
	found, ok := table.FindRow("A")
	require.True(t, ok)
	assert.Equal(t, last, found)
	assert.Empty(t, keys)
}
