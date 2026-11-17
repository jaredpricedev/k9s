// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trendData(cpu, restarts string) *model1.TableData {
	header := model1.Header{{Name: "NAME"}, {Name: "RESTARTS"}, {Name: "CPU"}, {Name: "MEM"}}
	rows := model1.NewRowEventsWithEvts(
		model1.RowEvent{Row: model1.Row{ID: "ns/a", Fields: model1.Fields{"a", restarts, cpu, "n/a"}}},
	)
	return model1.NewTableDataFull(client.NewGVR("v1/pods"), "ns", header, rows)
}

func TestTrendsRecordOnlyRealObservations(t *testing.T) {
	table := NewTable(client.NewGVR("v1/pods"))
	start := time.Unix(1000, 0)
	table.recordTrends(trendData("10", "3"), start)
	assert.Equal(t, "10", table.trendField("ns/a", "CPU", "10"), "one sample is not a trend")

	table.recordTrends(trendData("40", "3"), start.Add(time.Second))
	require.Len(t, table.trends["ns/a"].cpu, 1, "a redraw inside the interval adds no sample")

	table.recordTrends(trendData("40", "5"), start.Add(trendInterval))
	assert.Equal(t, []int{10, 40}, table.trends["ns/a"].cpu)
	assert.Empty(t, table.trends["ns/a"].mem, "an unavailable value is not recorded as zero")
	assert.Contains(t, table.trendField("ns/a", "CPU", "40"), "      ▂█", "samples are right-aligned and scaled from zero")
	assert.Contains(t, table.trendField("ns/a", "RESTARTS", "5"), " +2", "restarts gained while the list was open")
	assert.Equal(t, "3", table.trendField("ns/a", "RESTARTS", "3"))

	table.noIcon = true
	assert.Equal(t, "+ 40", table.trendField("ns/a", "CPU", "40"), "text-only direction without glyphs")

	pads := MaxyPad{1, 1, 2, 3}
	table.trendPads(trendData("40", "5"), pads)
	assert.Equal(t, MaxyPad{1, 4, 4, 5}, pads, "column budgets include the decorations")

	header := model1.Header{{Name: "NAME"}, {Name: "RESTARTS"}, {Name: "CPU"}, {Name: "MEM"}}
	empty := model1.NewTableDataFull(client.NewGVR("v1/pods"), "ns", header, model1.NewRowEventsWithEvts())
	table.recordTrends(empty, start.Add(2*trendInterval))
	assert.Empty(t, table.trends, "history is dropped with the resource")
}

func TestSparklineLevels(t *testing.T) {
	assert.Equal(t, "▁▁▁▁▁▁▁▁", sparkline([]int{0, 0, 0, 0, 0, 0, 0, 0}))
	assert.Equal(t, "    ▁▄██", sparkline([]int{0, 4, 8, 8}))
	assert.Equal(t, "=", trendDirection([]int{5, 9, 5}))
	assert.Equal(t, "-", trendDirection([]int{5, 1}))
}
