// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dashboard

// TileKind is how a panel takes part in the grid.
type TileKind int

const (
	// TileSection is a row title across the width.
	TileSection TileKind = iota
	// TileNote is one line across the width: a panel that is not drawn,
	// with the reason.
	TileNote
	// TileValue is a small tile: stat, gauge, bar gauge.
	TileValue
	// TileChart is a line chart.
	TileChart
)

// Smallest widths a tile is given, in cells, and the gap between tiles.
const (
	MinChartWidth = 48
	MinValueWidth = 24
	// MaxValueWidth keeps a value tile from stretching across the page
	// when it stands alone on its line.
	MaxValueWidth = 44
	TileGap       = 2
)

// Slot is one panel's place on a grid line.
type Slot struct {
	// Panel indexes Dashboard.Panels.
	Panel int
	// X is the first cell of the tile, Width its width.
	X, Width int
}

// Line is one line of tiles: panels of one dashboard row, side by side.
type Line struct {
	Kind  TileKind
	Slots []Slot
}

// TileOf says how a panel takes part in the grid under a plan.
func TileOf(p *Panel, pp *PanelPlan) TileKind {
	switch {
	case p.Kind == KindRow:
		return TileSection
	case pp.NotDrawn != "":
		return TileNote
	case p.Kind.Value():
		return TileValue
	}
	return TileChart
}

// Flow lays the panels out for a width, in the dashboard's own order.
// Panels that share a dashboard row stay side by side as far as the width
// allows; a row that does not fit is split into even lines rather than a
// full one and a remainder. Every panel is placed: nothing is left out
// for lack of room, the page scrolls instead.
func Flow(d *Dashboard, plan *Plan, width int) []Line {
	var lines []Line
	full := func(kind TileKind, panel int) {
		lines = append(lines, Line{Kind: kind, Slots: []Slot{{Panel: panel, Width: width}}})
	}
	for i := 0; i < len(d.Panels); {
		kind := TileOf(&d.Panels[i], &plan.Panels[i])
		if kind == TileSection || kind == TileNote {
			full(kind, i)
			i++
			continue
		}
		// A run of tiles of one kind in one dashboard row.
		end := i + 1
		for end < len(d.Panels) && d.Panels[end].Band == d.Panels[i].Band && TileOf(&d.Panels[end], &plan.Panels[end]) == kind {
			end++
		}
		least := MinChartWidth
		if kind == TileValue {
			least = MinValueWidth
		}
		fit := max((width+TileGap)/(least+TileGap), 1)
		count := end - i
		rows := (count + fit - 1) / fit
		per := (count + rows - 1) / rows
		for start := i; start < end; start += per {
			n := min(per, end-start)
			// Tiles of a split row keep one width, so their plots line up.
			tile := (width - (per-1)*TileGap) / per
			if kind == TileValue {
				tile = min(tile, max(MaxValueWidth, MinValueWidth))
			}
			line := Line{Kind: kind}
			for k := range n {
				line.Slots = append(line.Slots, Slot{Panel: start + k, X: k * (tile + TileGap), Width: tile})
			}
			lines = append(lines, line)
		}
		i = end
	}
	return lines
}

// LineOf finds the line and slot of a panel; ok is false for a panel that
// has no tile.
func LineOf(lines []Line, panel int) (line, slot int, ok bool) {
	for i := range lines {
		for j := range lines[i].Slots {
			if lines[i].Slots[j].Panel == panel && lines[i].Kind != TileSection {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

// Advance moves the selection by delta panels in reading order, skipping row
// titles, and stops at either end.
func Advance(lines []Line, panel, delta int) int {
	var order []int
	for i := range lines {
		if lines[i].Kind == TileSection {
			continue
		}
		for _, slot := range lines[i].Slots {
			order = append(order, slot.Panel)
		}
	}
	if len(order) == 0 {
		return panel
	}
	at := 0
	for i, p := range order {
		if p == panel {
			at = i
		}
	}
	return order[min(max(at+delta, 0), len(order)-1)]
}

// AdvanceLine moves the selection one line of tiles up or down, to the tile
// nearest to the one it leaves.
func AdvanceLine(lines []Line, panel, delta int) int {
	line, slot, ok := LineOf(lines, panel)
	if !ok {
		return Advance(lines, panel, 0)
	}
	x := lines[line].Slots[slot].X
	for next := line + delta; next >= 0 && next < len(lines); next += delta {
		if lines[next].Kind == TileSection {
			continue
		}
		best := lines[next].Slots[0]
		for _, candidate := range lines[next].Slots {
			if abs(candidate.X-x) < abs(best.X-x) {
				best = candidate
			}
		}
		return best.Panel
	}
	return panel
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Visible chooses which lines of a page are shown: as many whole lines as
// the height holds, starting at first, moved so that the selected line is
// among them, and never leaving room unused above while lines are hidden
// there. heights are the lines' smallest heights.
func Visible(heights []int, first, selected, budget int) (start, count int) {
	n := len(heights)
	if n == 0 {
		return 0, 0
	}
	selected = min(max(selected, 0), n-1)
	start = min(max(first, 0), selected)
	fill := func(from int) int {
		used, shown := 0, 0
		for i := from; i < n; i++ {
			if shown > 0 && used+heights[i] > budget {
				break
			}
			used += heights[i]
			shown++
		}
		return shown
	}
	for count = fill(start); start+count <= selected; count = fill(start) {
		start++
	}
	for start > 0 && start+count == n && fill(start-1) == count+1 {
		start--
		count++
	}
	return start, count
}
