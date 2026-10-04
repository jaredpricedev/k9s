// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"

	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
)

// Each subscription captures its own model/generation. A stopped worker cannot
// borrow a later subscription's generation even if its listener was copied just
// before navigation or a reconnect removed it.
type browserWatchListener struct {
	browser    *Browser
	model      ui.Tabular
	generation uint64
}

func (l *browserWatchListener) TableNoData(data *model1.TableData) {
	l.browser.tableNoData(data, l.generation, l.model)
}
func (l *browserWatchListener) TableDataChanged(data *model1.TableData) {
	l.browser.tableDataChanged(data, l.generation, l.model)
}
func (l *browserWatchListener) TableLoadFailed(err error) {
	l.browser.tableLoadFailed(err, l.generation, l.model)
}

func (b *Browser) watchCurrent(generation uint64, source ui.Tabular) bool {
	return b.watchGeneration.Load() == generation && b.GetModel() == source && b.app.IsRunning()
}

// Replace the model itself so a late render cannot mutate a new session's data.
// The terminal table, filter, sorting, path and retained navigation stay put.
//
//nolint:gocritic // Keep captured resource identities immutable during a page rebind.
func (b *Browser) rebindSession(selected SelectedResourceTarget) {
	if selected.Err() == nil {
		b.sessionSelection = &selected
	}
	previous := b.GetModel()
	fresh := model.NewTable(b.GVR())
	fresh.SetNamespace(previous.GetNamespace())
	fresh.SetInstance(b.instance)
	fresh.SetLabelSelector(previous.GetLabelSelector())
	fresh.SetRefreshRate(b.app.Config.K9s.RefreshDuration())
	fresh.SetViewSetting(context.Background(), b.GetViewSetting())
	b.SetModel(fresh)
	b.SetContext(context.Background())
	b.ClearMarks()
	b.ClearSelection()
	b.accessor, _ = dao.AccessorFor(b.app.factory, b.GVR())
}

func (b *Browser) restoreSessionSelection() {
	target := b.sessionSelection
	if target == nil {
		return
	}
	b.sessionSelection = nil
	current := selectedResourceForPath(b, target.Context, target.Path())
	if target.UID == "" || current.Err() != nil || current.UID != target.UID {
		b.ClearSelection()
		b.app.Flash().Warn("Selection changed or is unavailable after reconnect; select the current resource.")
		return
	}
	for row := 1; row < b.GetRowCount(); row++ {
		if path, ok := b.GetRowID(row); ok && path == target.Path() {
			b.Select(row, 0)
			return
		}
	}
	b.ClearSelection()
}
