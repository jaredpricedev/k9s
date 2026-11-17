// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
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
