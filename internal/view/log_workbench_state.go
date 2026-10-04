// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/logstream"
)

func (w *logWorkbench) freeze() {
	if w.frozenSet || w.mode == modeHistory {
		return
	}
	if w.renderedEntries != nil {
		w.frozenEntries = w.renderedEntries
	} else {
		w.frozenEntries = w.engine.Snapshot()
	}
	w.frozenSet = true
	w.follow = false
}
func (w *logWorkbench) resume() {
	w.follow = true
	w.frozenEntries = nil
	w.frozenSet = false
	w.frozenPosition = logViewPosition{}
}
func (w *logWorkbench) liveSnapshot() []logstream.Entry {
	if !w.follow {
		w.freeze()
		return w.frozenEntries
	}
	snapshot := w.engine.Snapshot()
	w.renderedEntries = snapshot
	return snapshot
}
func (w *logWorkbench) preserveFrozenPosition() {
	if !w.frozenSet || w.frozenPosition.valid {
		return
	}
	w.frozenPosition.selected = w.selected
	w.frozenPosition.mark = w.mark
	w.frozenPosition.row, w.frozenPosition.column = w.table.GetOffset()
	w.frozenPosition.valid = true
}
func (w *logWorkbench) restoreFrozenPosition() {
	if !w.frozenPosition.valid {
		return
	}
	w.selected = w.frozenPosition.selected
	w.mark = w.frozenPosition.mark
	w.table.SetOffset(w.frozenPosition.row, w.frozenPosition.column)
	w.selectionScope = scopeLive
	w.frozenPosition = logViewPosition{}
}
