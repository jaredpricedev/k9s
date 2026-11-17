// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strings"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
)

// One key switches how every chart view draws its lines: the areas and
// solid lines that read at a glance, or the finer braille dots. The choice
// is kept for the session and is the same in :graph, the drawn dashboards,
// :query and :slow, so a chart does not change its look from view to view.
const (
	chartStyleKey   = ui.KeyB
	chartStyleLabel = "Chart style: solid or dots"
)

// chartStyle is the style charts are drawn in for the rest of the session.
func (a *App) chartStyle() chart.Style { return a.chartLook }

// chartStyleHint names the key in a key line by what it switches to.
func chartStyleHint(style chart.Style) string {
	if style == chart.Dots {
		return "b solid"
	}
	return "b dots"
}

// chartStyleForms offers every form of a key line twice: with the style
// key named, and as it is. The first that fits a width is still the
// fullest wording, and the key is named wherever there is room for it.
func chartStyleForms(forms []string, style chart.Style) []string {
	out := make([]string, 0, 2*len(forms))
	hint := " · " + chartStyleHint(style)
	for _, form := range forms {
		// The way out stays the last thing a key line names.
		with := form + hint
		if at := strings.LastIndex(form, " · Esc"); at >= 0 && !strings.Contains(form[at+1:], " · ") {
			with = form[:at] + hint + form[at:]
		}
		out = append(out, with, form)
	}
	return out
}

// chartStyleFit returns the fullest form of a key line that fits the
// width, naming the style key where there is room. fallback is what is
// written when nothing fits.
func chartStyleFit(forms []string, style chart.Style, width int, ascii bool, fallback string) string {
	for _, text := range chartStyleForms(forms, style) {
		text = graphWords(text, ascii)
		if runewidth.StringWidth(text) <= width {
			return text
		}
	}
	return fallback
}

// chartStyleAction switches the style and draws the view again. It says
// what it did in words: with noIcons charts are plain characters in either
// style, and a key that seemed to do nothing would be a broken key.
func chartStyleAction(app *App, redraw func()) ui.KeyAction {
	return ui.NewKeyAction(chartStyleLabel, func(*tcell.EventKey) *tcell.EventKey {
		app.chartLook = app.chartLook.Next()
		switch {
		case app.Config.K9s.UI.NoIcons:
			app.Flash().Info("noIcons draws charts in plain characters: the chart style changes nothing here")
		case app.chartLook == chart.Dots:
			app.Flash().Info("Charts in braille dots: finer, harder to read · b returns to areas and solid lines")
		default:
			app.Flash().Info("Charts as filled areas and solid lines · b draws braille dots")
		}
		redraw()
		return nil
	}, true)
}
