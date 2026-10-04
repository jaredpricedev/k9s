// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	logCollectorUnknown int32 = iota
	logCollectorConnecting
	logCollectorActive
	logCollectorEnded
	logCollectorFailed
	logCollectorStopped
)

func (w *logWorkbench) collectorLabel() string {
	switch w.collectorState.Load() {
	case logCollectorConnecting:
		return "Collector connecting"
	case logCollectorActive:
		return "Collector receiving"
	case logCollectorEnded:
		return "Collector ended"
	case logCollectorFailed:
		return "Collector failed"
	case logCollectorStopped:
		return "Collector stopped"
	default:
		return "Collector unknown"
	}
}

func (w *logWorkbench) semanticPalette() config.SemanticPalette {
	var styles *config.Styles
	if w.owner != nil && w.owner.app != nil {
		styles = w.owner.app.Styles
	}
	return styles.Semantic()
}

func (w *logWorkbench) applyStyles(styles *config.Styles) {
	palette := styles.Semantic()
	w.SetBackgroundColor(palette.Canvas.Color())
	w.table.SetBackgroundColor(palette.Canvas.Color())
	w.table.SetSelectedStyle(tcell.StyleDefault.Foreground(palette.Text.Color()).Background(palette.Selected.Color()).Bold(true))
	w.status.SetBackgroundColor(palette.Panel.Color())
	w.status.SetTextColor(palette.Text.Color())
	w.detail.SetBackgroundColor(palette.Canvas.Color())
	w.detail.SetTextColor(palette.Text.Color())
}

func (w *logWorkbench) newCell(text string) *tview.TableCell {
	return tview.NewTableCell(text).SetTextColor(w.semanticPalette().Text.Color())
}

func (w *logWorkbench) entryAvailability() string {
	if !w.entryActionsAvailable() {
		return "Open an entry table or entry detail first"
	}
	if _, ok := w.selectedEntry(); !ok {
		return "Select a retained log entry first"
	}
	return ""
}
