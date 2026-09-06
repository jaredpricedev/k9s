// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRowEventsDeleteReleasesRemovedBackingSlot(t *testing.T) {
	events := NewRowEventsWithEvts(
		RowEvent{Row: Row{ID: "A", Fields: Fields{"large-A"}}},
		RowEvent{Row: Row{ID: "B", Fields: Fields{"large-B"}}},
	)

	require.NoError(t, events.Delete("A"))

	backing := events.events[:cap(events.events)]
	assert.Equal(t, RowEvent{}, backing[len(events.events)])
}

func TestRowEventsClearReleasesBackingSlots(t *testing.T) {
	events := NewRowEventsWithEvts(
		RowEvent{Row: Row{ID: "A", Fields: Fields{"large-A"}}},
		RowEvent{Row: Row{ID: "B", Fields: Fields{"large-B"}}},
	)

	events.Clear()

	assert.Equal(t, []RowEvent{{}, {}}, events.events[:cap(events.events)])
}

func TestRowEventsCloneKeepsFieldsDeltasAndIndexesIndependent(t *testing.T) {
	events := NewRowEventsWithEvts(
		RowEvent{Kind: EventUpdate, Row: Row{ID: "A", Fields: Fields{"ready"}}, Deltas: DeltaRow{"pending"}},
		RowEvent{Kind: EventAdd, Row: Row{ID: "B", Fields: Fields{"running"}}},
	)
	clone := events.Clone()

	require.Equal(t, 2, clone.Len())
	for i, id := range []string{"A", "B"} {
		index, ok := clone.FindIndex(id)
		require.True(t, ok)
		assert.Equal(t, i, index)
		found, ok := clone.Get(id)
		require.True(t, ok)
		assert.Equal(t, events.events[i].Kind, found.Kind)
		assert.Equal(t, events.events[i].Row, found.Row)
	}

	events.events[0].Row.Fields[0] = "failed"
	events.events[0].Deltas[0] = "ready"
	first, ok := clone.Get("A")
	require.True(t, ok)
	assert.Equal(t, Fields{"ready"}, first.Row.Fields)
	assert.Equal(t, DeltaRow{"pending"}, first.Deltas)

	first.Row.ID = "renamed"
	clone.Set(0, first)
	_, ok = events.Get("A")
	assert.True(t, ok, "changing the clone's index must not remove the source ID")
	_, ok = events.Get("renamed")
	assert.False(t, ok)
	_, ok = clone.Get("A")
	assert.False(t, ok)
	_, ok = clone.Get("renamed")
	assert.True(t, ok)
}
