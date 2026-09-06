// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model1

import (
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal/client"
)

func performanceRows(count int) *RowEvents {
	events := NewRowEvents(count)
	for i := range count {
		id := fmt.Sprintf("row-%05d", i)
		events.Add(RowEvent{Row: Row{ID: id, Fields: Fields{id, "ready", "42", "1m", "node-a", "extra"}}})
	}
	return events
}

func BenchmarkPerformanceCustomize(b *testing.B) {
	events := performanceRows(10000)
	columns := []int{0, 1, 3, 4}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = events.Customize(columns)
	}
}

func BenchmarkPerformanceRegex(b *testing.B) {
	data := NewTableDataWithRows(client.NewGVR("test"), Header{{Name: "NAME"}, {Name: "STATUS"}, {Name: "RESTARTS"}, {Name: "AGE"}, {Name: "NODE"}, {Name: "EXTRA"}}, performanceRows(10000))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = data.Filter(FilterOpts{Filter: "row-09.*ready"})
	}
}
