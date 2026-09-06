// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model1

import (
	"fmt"
	"testing"
)

func BenchmarkRowEventsClone10K(b *testing.B) {
	const rowCount = 10_000
	events := NewRowEvents(rowCount)
	for i := range rowCount {
		id := fmt.Sprintf("row-%05d", i)
		event := RowEvent{Row: Row{ID: id, Fields: Fields{id, "ready", "42", "1m", "node-a", "extra"}}}
		if i%5 == 0 {
			event.Kind = EventUpdate
			event.Deltas = DeltaRow{"", "pending", "", "", "", ""}
		}
		events.Add(event)
	}
	var clone *RowEvents
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		clone = events.Clone()
	}
	b.StopTimer()
	if clone.Len() != rowCount {
		b.Fatalf("expected %d cloned rows, got %d", rowCount, clone.Len())
	}
}
