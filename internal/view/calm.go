// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
)

// The incident views (:slow, :graph and its per-Pod page, :probe, :impact,
// a drawn dashboard, :query) are laid out with the calm kit. Two keys
// belong to that layout and mean the same in every one of them.
const (
	// calmDetailKey shows all the detail the view had before the calm
	// layout: every signal, the detail table. The calm layout is the
	// default; the key toggles.
	calmDetailKey   = ui.KeyA
	calmDetailLabel = "All detail"
	// calmNextChartKey makes the next strip the main chart. It is n where
	// n steps through nothing else (:graph, a dashboard, :query). :slow
	// steps through episodes with n and :impact through rollouts, so a
	// main chart there is stepped with calmNextChartAltKey, which is free
	// in every incident view and already steps the panels of a dashboard.
	calmNextChartKey    = ui.KeyN
	calmNextChartAltKey = tcell.KeyTab
	calmNextChartLabel  = "Next chart"
)

// calmPalette maps the semantic palette onto the colours of a calm page:
// one token per role, never a colour of its own.
func calmPalette(p config.SemanticPalette) calm.Palette { //nolint:gocritic // The palette is passed by value, as graphPalette takes it.
	return calm.Palette{
		Text: graphHex(p.Text), Muted: graphHex(p.Muted), Accent: graphHex(p.Focus),
		Warn: graphHex(p.Warning), Fail: graphHex(p.Failure), Info: graphHex(p.Progress),
		Mark: graphHex(p.Category), Band: graphHex(p.Band),
	}
}

// calmOptions is how a calm page is drawn in this session: the skin's
// colours, and plain characters with noIcons.
func calmOptions(app *App) calm.Options {
	return calm.Options{ASCII: app.Config.K9s.UI.NoIcons, Palette: calmPalette(app.Styles.Semantic())}
}

// calmHeight is the rows a calm page is planned for. A view that has not
// been drawn yet has no height: its page is then planned without a limit.
func calmHeight(height int) int {
	if height <= 0 {
		return 1 << 10
	}
	return height
}

// tviewCells is the width of plain text in terminal cells.
func tviewCells(text string) int { return runewidth.StringWidth(text) }

// calmInsert puts a block of the view's own lines between the evidence of
// a rendered page and its keys, with one blank row beside it. The page is
// planned for the height less the block and that row. A page too small
// for its evidence takes no block.
func calmInsert(rendered calm.Rendered, block []string, tooSmall bool) []string {
	lines := rendered.Lines()
	if len(block) == 0 || tooSmall {
		return lines
	}
	at := len(lines)
	if len(rendered.Keys) > 0 {
		for index := len(lines) - 1; index >= 0; index-- {
			if lines[index] == rendered.Keys[0] {
				at = index
				break
			}
		}
	}
	out := append(make([]string, 0, len(lines)+len(block)+1), lines[:at]...)
	if at > 0 && lines[at-1] != "" {
		// A page that gave its blank rows up has none over the keys: the
		// block is set off from the evidence instead.
		out = append(append(out, ""), block...)
	} else {
		out = append(append(out, block...), "")
	}
	return append(out, lines[at:]...)
}

// calmKeepSelected keeps the selected row on a page that left the last
// rows of its group out: the rows of the group are turned so that the
// selected one is the last of those drawn, as a list scrolls to its
// cursor. It reports whether it moved a row; the page is then planned
// again, because the columns are measured from the rows that are drawn.
func calmKeepSelected(page *calm.Page, plan *calm.Plan) bool {
	moved := false
	for index := range page.Groups {
		rows := page.Groups[index].Rows
		shown := plan.Rows[index]
		if plan.Folded[index] || plan.Hidden[index] || shown <= 0 || shown >= len(rows) {
			continue
		}
		for at := shown; at < len(rows); at++ {
			if !rows[at].Selected {
				continue
			}
			turned := append(make([]calm.Row, 0, len(rows)), rows[at-shown+1:at+1]...)
			turned = append(append(turned, rows[:at-shown+1]...), rows[at+1:]...)
			page.Groups[index].Rows, moved = turned, true
			break
		}
	}
	return moved
}
