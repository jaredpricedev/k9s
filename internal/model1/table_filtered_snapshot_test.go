// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model1

import (
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableFilteredSnapshotDetachedCurrentRows(t *testing.T) {
	source := NewTableDataFull(client.NewGVR("v1/pods"), "apps", Header{{Name: "NAME"}, {Name: "STATUS"}},
		NewRowEventsWithEvts(
			RowEvent{Kind: EventUpdate, Row: Row{ID: "apps/a", Fields: Fields{"alpha", "Ready"}}, Deltas: DeltaRow{"", "Pending"}},
			RowEvent{Row: Row{ID: "apps/b", Fields: Fields{"beta", "Pending"}}},
		))
	snapshot, total, err := source.FilteredSnapshot(FilterOpts{Filter: "alpha"})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Equal(t, 1, snapshot.RowCount())
	row, found := snapshot.FindRow("apps/a")
	require.True(t, found)
	assert.Equal(t, EventUpdate, row.Kind)
	assert.Equal(t, "Pending", row.Deltas[1])

	// Mutating the displayed snapshot must not affect retained source metadata,
	// and a later watched update must not alter the already accepted result.
	row.Row.Fields[0], row.Deltas[1] = "display-only", "display delta"
	snapshot.Header()[0].Name = "DISPLAY"
	snapshot.AddRow(RowEvent{Row: Row{ID: "apps/c", Fields: Fields{"gamma", "Ready"}}})
	original, found := source.FindRow("apps/a")
	require.True(t, found)
	assert.Equal(t, "alpha", original.Row.Fields[0])
	assert.Equal(t, "Pending", original.Deltas[1])
	assert.Equal(t, "NAME", source.Header()[0].Name)
	_, found = source.FindRow("apps/c")
	assert.False(t, found, "private lookup indexes must also be detached")
	source.Update(Rows{{ID: "apps/a", Fields: Fields{"alpha", "Failed"}}})
	assert.Equal(t, "Ready", row.Row.Fields[1])
	assert.Equal(t, 2, snapshot.RowCount())
	next, total, err := source.FilteredSnapshot(FilterOpts{Filter: "Failed"})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, 1, next.RowCount())
	invalid, total, err := source.FilteredSnapshot(FilterOpts{Filter: "["})
	require.Error(t, err)
	assert.Nil(t, invalid)
	assert.Equal(t, 1, total)
	assert.Equal(t, 1, source.RowCount())
}

func TestTableFilteredSnapshotConcurrentWatch(t *testing.T) {
	source := NewTableDataFull(client.NewGVR("v1/pods"), "apps", Header{{Name: "NAME"}, {Name: "STATUS"}}, NewRowEvents(0))
	watchRows := func(generation int) Rows {
		rows := make(Rows, 128)
		for i := range rows {
			rows[i] = Row{ID: fmt.Sprintf("apps/pod-%03d", i), Fields: Fields{fmt.Sprintf("pod-%03d", i), fmt.Sprint(generation)}}
		}
		return rows
	}
	source.Update(watchRows(0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for generation := range 100 {
			source.Update(watchRows(generation))
		}
	}()
	for range 100 {
		snapshot, total, err := source.FilteredSnapshot(FilterOpts{Filter: "pod-"})
		require.NoError(t, err)
		require.Equal(t, 128, total)
		require.Equal(t, total, snapshot.RowCount())
		first, found := snapshot.RowAt(0)
		require.True(t, found)
		snapshot.RowsRange(func(_ int, row RowEvent) bool {
			assert.Equal(t, first.Row.Fields[1], row.Row.Fields[1], "one snapshot must not mix watched generations")
			return true
		})
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("source writer did not complete alongside filtered snapshots")
	}
}
