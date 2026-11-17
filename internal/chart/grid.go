// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package chart

import (
	"regexp"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// Mini is one small chart of a Grid, for example one Pod.
type Mini struct {
	// Title names the chart; Note is written at the right end of the title
	// line, for example "p99 41s".
	Title, Note string
	// TitleColor is a tview colour for the title line; empty adds no tag.
	TitleColor string
	Series     []Series
}

// Grid lays several mini charts out side by side with one shared Y scale
// and one shared time axis, so that their shapes can be compared directly.
type Grid struct {
	Unit  Unit
	Minis []Mini
	// MinWidth and MinHeight are the smallest plot a mini chart may get;
	// zero means 16 cells and 3 rows.
	MinWidth, MinHeight int
	// Empty is written inside a mini chart that has no sample.
	Empty string
	// Ceiling, when positive, is the top of the shared Y axis instead of
	// the rounded maximum of the mini charts given. A caller that shows
	// the charts one page at a time sets it to the Top of all of them, so
	// that the scale does not change from page to page.
	Ceiling float64
	// Escape protects titles and notes from the caller's markup (nil
	// leaves them alone), as in Legend.
	Escape func(string) string
}

// GridLayout says how a Grid fits a width and a height.
type GridLayout struct {
	// Columns is the number of mini charts side by side, Rows the number
	// of grid rows drawn.
	Columns, Rows int
	// PlotWidth and PlotHeight are the plot size of each mini chart in
	// cells. Gutter is the width of the shared Y labels.
	PlotWidth, PlotHeight, Gutter int
	// Shown and NotShown count mini charts; NotShown is never dropped
	// silently: the caller names it.
	Shown, NotShown int
}

const (
	// gridGap is what separates two mini charts: two spaces and the axis bar.
	gridGap = 3
	// gridGoodWidth is the plot width the layout prefers when the height
	// allows another row instead.
	gridGoodWidth = 28
)

// Top is the top of the shared Y axis: the rounded maximum of every mini
// chart.
func (g *Grid) Top() float64 {
	if g.Ceiling > 0 {
		return g.Ceiling
	}
	top := 0.0
	for i := range g.Minis {
		panel := Panel{Unit: g.Unit, Series: g.Minis[i].Series}
		if v, ok := panel.Max(); ok && v > top {
			top = v
		}
	}
	return g.Unit.Ceil(top)
}

// scale is a panel with the shared top and every level line of the grid,
// so that the label gutter is wide enough for a level's own label in any
// mini chart.
func (g *Grid) scale() Panel {
	scale := Panel{Unit: g.Unit, Ceiling: g.Top()}
	for i := range g.Minis {
		for j := range g.Minis[i].Series {
			if s := &g.Minis[i].Series[j]; s.Kind == Reference && len(s.Points) > 0 {
				scale.Series = append(scale.Series, Series{Kind: Reference, Points: s.Points[len(s.Points)-1:]})
			}
		}
	}
	return scale
}

// Layout fits the grid into width by height cells. Each grid row takes one
// title line and PlotHeight plot lines; the shared time axis takes the last
// two lines. Mini charts that do not fit are counted in NotShown.
func (g *Grid) Layout(width, height int) GridLayout {
	minWidth, minHeight := g.MinWidth, g.MinHeight
	if minWidth <= 0 {
		minWidth = 16
	}
	if minHeight <= 0 {
		minHeight = 3
	}
	scale := g.scale()
	layout := GridLayout{Gutter: scale.GutterWidth(), NotShown: len(g.Minis)}
	count := len(g.Minis)
	left := layout.Gutter + 2
	plotWidth := func(columns int) int { return (width - left - (columns-1)*gridGap) / columns }
	maxColumns := 0
	for c := 1; c <= count && plotWidth(c) >= minWidth; c++ {
		maxColumns = c
	}
	maxRows := (height - 2) / (1 + minHeight)
	if count == 0 || maxColumns == 0 || maxRows < 1 {
		return layout
	}
	// Prefer plots wide enough to read a shape in (gridGoodWidth); take
	// narrower ones only when the height does not allow the rows that
	// needs. Then even the columns out, so that eight charts become four
	// and four instead of six and two.
	columns := 1
	for c := 1; c <= maxColumns && plotWidth(c) >= max(gridGoodWidth, minWidth); c++ {
		columns = c
	}
	rows := (count + columns - 1) / columns
	for rows > maxRows && columns < maxColumns {
		columns++
		rows = (count + columns - 1) / columns
	}
	if rows > maxRows {
		rows = maxRows
	} else {
		columns = (count + rows - 1) / rows
	}
	layout.Columns, layout.Rows = columns, rows
	layout.PlotWidth = plotWidth(columns)
	layout.PlotHeight = (height-2)/rows - 1
	layout.Shown = min(count, rows*columns)
	layout.NotShown = count - layout.Shown
	return layout
}

var tviewTag = regexp.MustCompile(`\[[a-zA-Z0-9#,-]*:[a-zA-Z0-9#,-]*:[a-zA-Z-]*\]`)

// cellWidth is the width of text in terminal cells, not counting the
// colour tags this package writes.
func cellWidth(text string) int {
	return runewidth.StringWidth(tviewTag.ReplaceAllString(text, ""))
}

func padTo(text string, width int) string {
	return text + strings.Repeat(" ", max(width-cellWidth(text), 0))
}

// Render draws the grid into at most height lines of at most width cells
// and returns the layout it used. Every mini chart shares the window, the
// step and the Y scale; the Y labels are written once, on the left, and the
// time axis once per column, under the last row. Nothing is drawn when not
// even one mini chart fits: the layout then reports every chart as not
// shown.
func (g *Grid) Render(start, end time.Time, step time.Duration, width, height int, o Options) ([]string, GridLayout) {
	layout := g.Layout(width, height)
	if layout.Shown == 0 {
		return nil, layout
	}
	escape := g.Escape
	if escape == nil {
		escape = func(s string) string { return s }
	}
	frame := Frame{Start: start, End: end, Step: step, Width: layout.PlotWidth}
	top := g.Top()
	left := layout.Gutter + 2
	out := make([]string, 0, layout.Rows*(1+layout.PlotHeight)+2)
	for row := range layout.Rows {
		first := row * layout.Columns
		last := min(first+layout.Columns, layout.Shown)
		var title strings.Builder
		title.WriteString(strings.Repeat(" ", left))
		plots := make([]string, layout.PlotHeight)
		for index := first; index < last; index++ {
			mini := &g.Minis[index]
			column := index - first
			if column > 0 {
				title.WriteString(strings.Repeat(" ", gridGap))
			}
			note := runewidth.Truncate(mini.Note, layout.PlotWidth, "")
			name := runewidth.Truncate(mini.Title, max(layout.PlotWidth-runewidth.StringWidth(note)-1, 0), "…")
			fill := layout.PlotWidth - runewidth.StringWidth(name) - runewidth.StringWidth(note)
			title.WriteString(paint(mini.TitleColor, escape(name)+strings.Repeat(" ", max(fill, 0))+escape(note)))

			panel := Panel{Unit: g.Unit, Series: mini.Series, Ceiling: top, Bare: column > 0, Empty: g.Empty}
			lines := panel.Render(frame, layout.PlotHeight, layout.Gutter, o)
			for i := range plots {
				if column == 0 {
					plots[i] = lines[i]
					continue
				}
				plots[i] = padTo(plots[i], left+column*(layout.PlotWidth+gridGap)-gridGap) + "  " + lines[i]
			}
		}
		out = append(out, strings.TrimRight(title.String(), " "))
		out = append(out, plots...)
	}
	// One time axis per column, under the last row.
	axis := []string{"", ""}
	for column := range layout.Columns {
		gutter := layout.Gutter
		if column > 0 {
			gutter = 1
			for i := range axis {
				axis[i] = padTo(axis[i], left+column*(layout.PlotWidth+gridGap)-gridGap)
			}
		}
		for i, line := range Axis(frame, gutter, nil, o) {
			axis[i] += line
		}
	}
	return append(out, axis...), layout
}
