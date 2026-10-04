// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resourceFilterWatcher struct{ table *ui.Table }

func (w resourceFilterWatcher) BufferCompleted(_, _ string) {
	w.table.Filter(w.table.CmdBuff().GetText())
}
func (w resourceFilterWatcher) BufferChanged(_, _ string) { w.table.TouchFilterDraft() }
func (w resourceFilterWatcher) BufferActive(active bool, _ model.BufferKind) {
	if !active {
		w.table.EndFilter()
	}
}

func TestResourceFilterPromptReplacementKeepsLastCommittedRows(t *testing.T) {
	table := ui.NewTable(client.NewGVR("test"))
	table.Init(makeContext())
	table.SetModel(new(mockModel))
	buffer := table.CmdBuff()
	watcher := resourceFilterWatcher{table: table}
	buffer.AddListener(watcher)
	defer buffer.RemoveListener(watcher)
	buffer.SetText("zorg", "", true)
	require.Equal(t, 2, table.GetRowCount())
	table.SelectRow(1, 0, true)
	require.Equal(t, "r2", table.GetSelectedItem())

	// This is the actual opening protocol: a replacement draft is empty, while
	// FishBuff's suggestion notifications and live refreshes may run immediately.
	table.BeginFilter()
	buffer.ClearText(false)
	prompt := ui.NewPrompt(nil, true, config.NewStyles())
	prompt.SetModel(buffer)
	prompt.SetFilterValidator(model1.ValidateResourceFilter)
	prompt.SetFilterClearHandler(table.TouchFilterDraft)
	buffer.SetActive(true)
	table.Refresh()
	require.Equal(t, "zorg", table.CommittedFilter())
	require.Equal(t, 2, table.GetRowCount(), "opening an empty draft must not widen the committed result")
	buffer.SetText("[", "", true)
	prompt.SendKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	table.Refresh()
	assert.True(t, buffer.IsActive())
	assert.Equal(t, "zorg", table.CommittedFilter())
	assert.Equal(t, "r2", table.GetSelectedItem())
	assert.Equal(t, 2, table.GetRowCount())
	assert.Contains(t, prompt.GetText(false), "missing closing ]")

	// An explicit reset deliberately removes the committed query.
	prompt.SendKey(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
	assert.Empty(t, table.CommittedFilter())
	assert.Equal(t, 3, table.GetRowCount())
}

func TestTableFilterKeepsValidResultAndSelectionOnSyntaxError(t *testing.T) {
	v := ui.NewTable(client.NewGVR("test"))
	v.Init(makeContext())
	v.SetModel(new(mockModel))
	query := func(text string) {
		v.CmdBuff().SetText(text, "", true)
		v.Filter(text)
	}
	query("blee duh")
	require.Equal(t, 3, v.GetRowCount())
	v.SelectRow(2, 0, true)
	require.Equal(t, "r2", v.GetSelectedItem())
	query("[")
	require.Error(t, v.FilterError())
	assert.Contains(t, v.FilterError().Error(), "missing closing ]")
	assert.Equal(t, "blee duh", v.CommittedFilter())
	assert.Equal(t, "r2", v.GetSelectedItem())
	assert.Equal(t, 3, v.GetRowCount())
	assert.Equal(t, 2, v.GetFilteredData().RowCount())
	assert.Contains(t, v.GetTitle(), "blee duh")
	assert.Contains(t, v.FilterStatusText(), "previous results retained")

	// A watched update must not erase the frozen valid result or selection.
	changed := makeTableData()
	changed.SetRowEvents(model1.NewRowEvents(0))
	v.UpdateUI(v.Update(changed, false), changed)
	assert.Equal(t, "r2", v.GetSelectedItem())
	assert.Equal(t, 3, v.GetRowCount())

	query("never matches")
	require.NoError(t, v.FilterError())
	assert.Equal(t, "never matches", v.CommittedFilter())
	assert.Equal(t, 1, v.GetRowCount(), "zero matches retains only the header")
	assert.Empty(t, v.GetSelectedItem())
	assert.Contains(t, v.FilterStatusText(), "No matches")
	assert.Contains(t, v.FilterStatusText(), "0/2")

	query("")
	assert.Empty(t, v.CommittedFilter())
	assert.Empty(t, v.FilterStatusText())
	assert.Equal(t, 3, v.GetRowCount())
}

func TestResourcePromptShowsModeAndKeepsInvalidInputEditable(t *testing.T) {
	buffer := model.NewFishBuff('/', model.FilterBuffer)
	prompt := ui.NewPrompt(nil, true, config.NewStyles())
	prompt.SetModel(buffer)
	prompt.SetFilterValidator(model1.ValidateResourceFilter)
	buffer.SetActive(true)
	buffer.SetText("[", "", true)
	assert.Contains(t, prompt.GetTitle(), "regex")
	assert.Contains(t, prompt.GetText(false), "missing closing ]")
	prompt.SendKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	assert.True(t, buffer.IsActive(), "invalid input remains editable on Enter")
	buffer.SetText("-l app in (payments, orders)", "", true)
	assert.Contains(t, prompt.GetTitle(), "labels")
	assert.NotContains(t, prompt.GetText(false), "previous results retained")
	prompt.SendKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	assert.False(t, buffer.IsActive())
	buffer.SetActive(true)
	prompt.SendKey(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
	assert.Empty(t, buffer.GetText())
}
