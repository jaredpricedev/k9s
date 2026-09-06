// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model1

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

func BenchmarkTableDataDelete10K(b *testing.B) {
	const rowCount = 10_000
	fixture := make([]RowEvent, rowCount)
	for i := range fixture {
		id := fmt.Sprintf("row-%05d", i)
		fixture[i] = RowEvent{Row: Row{ID: id, Fields: Fields{id, "ready", "42", "1m", "node-a", "extra"}}}
	}
	for _, tc := range []struct {
		name       string
		deleteEach int
		wantRows   int
	}{
		{name: "None", wantRows: rowCount},
		{name: "OnePercent", deleteEach: 100, wantRows: 9_900},
		{name: "Half", deleteEach: 2, wantRows: 5_000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			keep := sets.New[string]()
			for i, event := range fixture {
				if tc.deleteEach == 0 || i%tc.deleteEach != 0 {
					keep.Insert(event.Row.ID)
				}
			}
			var table *TableData
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				table = NewTableDataWithRows(nil, nil, NewRowEventsWithEvts(fixture...))
				keys := keep.Clone()
				b.StartTimer()
				table.Delete(keys)
			}
			b.StopTimer()
			if table.RowCount() != tc.wantRows {
				b.Fatalf("expected %d survivors, got %d", tc.wantRows, table.RowCount())
			}
		})
	}
}
