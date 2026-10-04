// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model1

import (
	"regexp"
	"testing"

	"github.com/derailed/k9s/internal/client"
)

// RE2 remains the public resource-filter contract. Exercise arbitrary UTF-8
// and invalid bytes against its independent matcher, not the fast-path helpers.
func FuzzPlainResourceFilterPreservesRE2(f *testing.F) {
	for _, text := range []string{"Ⓕ target-001 Running", "AKBſC", "a日本語b", "a\xffb", "[red]TARGET-001", "N/A\x00Ab c", "👩‍💻kſ", "İıSſKK"} {
		f.Add(text)
	}
	queries := []string{"k", "s", "target-001", "Ab c", "N/A", "\x00"}
	rxs := make([]*regexp.Regexp, len(queries))
	for index, query := range queries {
		rxs[index] = regexp.MustCompile(`(?i)(` + regexp.QuoteMeta(query) + `)`)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 1024 {
			return
		}
		data := NewTableDataWithRows(client.NewGVR("test"), Header{{Name: "NAME"}},
			NewRowEventsWithEvts(RowEvent{Row: Row{ID: "row", Fields: Fields{text}}}))
		for index, query := range queries {
			filtered, err := data.FilterChecked(FilterOpts{Filter: query})
			if err != nil {
				t.Fatal(err)
			}
			if actual, expected := filtered.RowCount() == 1, rxs[index].MatchString(text); actual != expected {
				t.Fatalf("query %q, text %q: got %v, RE2 expects %v", query, text, actual, expected)
			}
		}
		row, _ := data.RowAt(0)
		if row.Row.Fields[0] != text {
			t.Fatal("filter changed source text")
		}
	})
}
