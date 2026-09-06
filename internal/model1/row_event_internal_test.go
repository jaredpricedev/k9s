// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

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
