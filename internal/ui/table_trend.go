// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/model1"
)

const (
	trendSamples  = 8
	trendInterval = 2 * time.Second
)

var sparkGlyphs = []rune("▁▂▃▄▅▆▇█")

// rowTrend is what this table has itself observed for one resource while it
// has been open. It is session memory, not cluster history: it starts empty,
// is never persisted and is dropped when the resource leaves the list.
type rowTrend struct {
	cpu, mem      []int
	firstRestarts int
	restartsKnown bool
}

// recordTrends samples the unfiltered rows at most once per interval, so a
// resize, sort or filter redraw does not invent additional observations.
func (t *Table) recordTrends(data *model1.TableData, now time.Time) {
	cpu, mem, restarts := -1, -1, -1
	for i, h := range data.Header() {
		switch h.Name {
		case "CPU":
			cpu = i
		case "MEM":
			mem = i
		case tableRestartsColumn:
			restarts = i
		}
	}
	if (cpu < 0 && mem < 0 && restarts < 0) || (!t.trendAt.IsZero() && now.Sub(t.trendAt) < trendInterval) {
		return
	}
	t.trendAt = now
	next := make(map[string]*rowTrend, data.RowCount())
	number := func(fields model1.Fields, index int) (int, bool) {
		if index < 0 || index >= len(fields) {
			return 0, false
		}
		value, err := strconv.Atoi(strings.TrimSpace(fields[index]))
		return value, err == nil && value >= 0
	}
	push := func(series []int, value int) []int {
		series = append(series, value)
		if len(series) > trendSamples {
			series = series[len(series)-trendSamples:]
		}
		return series
	}
	data.RowsRange(func(_ int, re model1.RowEvent) bool {
		trend := t.trends[re.Row.ID]
		if trend == nil {
			trend = new(rowTrend)
		}
		if value, ok := number(re.Row.Fields, cpu); ok {
			trend.cpu = push(trend.cpu, value)
		}
		if value, ok := number(re.Row.Fields, mem); ok {
			trend.mem = push(trend.mem, value)
		}
		if value, ok := number(re.Row.Fields, restarts); ok && !trend.restartsKnown {
			trend.firstRestarts, trend.restartsKnown = value, true
		}
		next[re.Row.ID] = trend
		return true
	})
	t.trends = next
}

// trendPads widens the measured columns for the decorations trendField adds,
// so the column budget and the drawn text agree.
func (t *Table) trendPads(data *model1.TableData, pads MaxyPad) {
	usage, growth := 0, 0
	for _, trend := range t.trends {
		if len(trend.cpu) > 1 || len(trend.mem) > 1 {
			usage = trendSamples + 1
			if t.noIcon {
				usage = 2
			}
		}
	}
	index := -1
	for i, h := range data.Header() {
		if h.Name == tableRestartsColumn {
			index = i
		}
	}
	if index >= 0 {
		data.RowsRange(func(_ int, re model1.RowEvent) bool {
			if index < len(re.Row.Fields) {
				growth = max(growth, len(t.restartGrowth(re.Row.ID, re.Row.Fields[index])))
			}
			return true
		})
	}
	for i, h := range data.Header() {
		if i >= len(pads) {
			break
		}
		switch h.Name {
		case "CPU", "MEM":
			pads[i] += usage
		case tableRestartsColumn:
			pads[i] += growth
		}
	}
}

func (t *Table) restartGrowth(id, value string) string {
	trend := t.trends[id]
	current, err := strconv.Atoi(strings.TrimSpace(value))
	if trend == nil || !trend.restartsKnown || err != nil || current <= trend.firstRestarts {
		return ""
	}
	return fmt.Sprintf(" +%d", current-trend.firstRestarts)
}

// trendField decorates a usage or restart value with what this table has
// watched it do: a sparkline of recent samples before a usage number, or the
// restarts gained since the list was opened. The raw field is unchanged for
// sorting, filtering, export and status classification.
func (t *Table) trendField(id, column, field string) string {
	trend := t.trends[id]
	if trend == nil {
		return field
	}
	var series []int
	switch column {
	case "CPU":
		series = trend.cpu
	case "MEM":
		series = trend.mem
	case tableRestartsColumn:
		if growth := t.restartGrowth(id, field); growth != "" {
			return field + t.presentation.deltaPrefix + growth + "[-::]"
		}
		return field
	default:
		return field
	}
	if len(series) < 2 {
		return field
	}
	if t.noIcon {
		return trendDirection(series) + " " + field
	}
	return t.presentation.trendPrefix + sparkline(series) + "[-::] " + field
}

// sparkline scales samples from zero to the row's own maximum, so a steady
// workload reads as a level line and a spike stands out against it.
func sparkline(series []int) string {
	top := 0
	for _, value := range series {
		top = max(top, value)
	}
	out := make([]rune, 0, trendSamples)
	for range trendSamples - len(series) {
		out = append(out, ' ')
	}
	for _, value := range series {
		level := 0
		if top > 0 {
			level = value * (len(sparkGlyphs) - 1) / top
		}
		out = append(out, sparkGlyphs[level])
	}
	return string(out)
}

// trendDirection is the text-only form: the latest sample against the oldest.
func trendDirection(series []int) string {
	first, last := series[0], series[len(series)-1]
	switch {
	case last > first:
		return "+"
	case last < first:
		return "-"
	}
	return "="
}
