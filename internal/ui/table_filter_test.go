// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"strings"
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

type mutableResourceFilterModel struct {
	*mockModel
	data *model1.TableData
}

func (m *mutableResourceFilterModel) Peek() *model1.TableData { return m.data.Clone() }

type snapshotResourceFilterModel struct{ *mutableResourceFilterModel }

func (m *snapshotResourceFilterModel) PeekFiltered(opts model1.FilterOpts) (*model1.TableData, int, error) {
	return m.data.FilteredSnapshot(opts)
}

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
	assert.NotEmpty(t, v.GetSelectedItem(), "restored rows must be actionable before another draw")
}

func TestTableFilterRestoresSelectionAfterEmptyDraw(t *testing.T) {
	v := ui.NewTable(client.NewGVR("test"))
	v.Init(makeContext())
	v.SetModel(new(mockModel))
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(120, 34)
	v.SetRect(0, 0, 120, 34)
	query := func(text string) {
		v.CmdBuff().SetText(text, "", true)
		v.Filter(text)
		v.Draw(screen)
	}
	query("zorg")
	require.Equal(t, "r2", v.GetSelectedItem())
	query("never matches")
	require.Empty(t, v.GetSelectedItem())
	// Empty draws, resize and refresh can move the underlying cursor outside
	// all cells. The next valid query must select an actual resource directly.
	v.Select(8, 0)
	v.Draw(screen)
	query("")
	require.NotEmpty(t, v.GetSelectedItem())
	query("zorg")
	require.Equal(t, "r2", v.GetSelectedItem())
}

func TestTableFilterReusesUnchangedRowsAndRedrawsChangedFields(t *testing.T) {
	v := ui.NewTable(client.NewGVR("test"))
	v.Init(makeContext())
	m := &mutableResourceFilterModel{mockModel: new(mockModel), data: makeTableData()}
	v.SetModel(m)
	query := func(text string) {
		v.CmdBuff().SetText(text, "", true)
		v.Filter(text)
	}
	query("ble")
	cell := v.GetCell(1, 0)
	selected := v.GetSelectedItem()
	query("blee")
	assert.Same(t, cell, v.GetCell(1, 0), "a new query matching identical rows must reuse the rendered cells")
	assert.Equal(t, "blee", v.CommittedFilter())
	assert.Equal(t, selected, v.GetSelectedItem())
	assert.Contains(t, v.FilterStatusText(), "2/2")

	row, ok := m.data.RowAt(0)
	require.True(t, ok)
	row.Row.Fields[1] = "changed evidence"
	m.data.SetRow(0, row)
	query("blee")
	assert.NotSame(t, cell, v.GetCell(1, 0), "changed source fields must rebuild even when the matching IDs stay the same")
	assert.Equal(t, "changed evidence", strings.TrimSpace(v.GetCell(1, 1).Text))
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

func TestResourceFilterRefreshUsesCommittedProjectionWhileDraftWaits(t *testing.T) {
	v := ui.NewTable(client.NewGVR("test"))
	v.Init(makeContext())
	m := &mutableResourceFilterModel{mockModel: new(mockModel), data: makeTableData()}
	v.SetModel(m)
	v.CmdBuff().SetText("zorg", "", true)
	v.Filter("zorg")
	require.Equal(t, "r2", v.GetSelectedItem())
	v.BeginFilter()
	v.TouchFilterDraft()
	v.CmdBuff().SetText("b", "", true)
	v.DeferFilterDraft("b")
	v.Refresh()
	assert.Equal(t, "zorg", v.CommittedFilter())
	assert.Equal(t, 2, v.GetRowCount(), "a watched refresh must not project an intermediate broad prefix")
	assert.Equal(t, "r2", v.GetSelectedItem())

	v.CmdBuff().SetText("[", "", true)
	v.DeferFilterDraft("[")
	v.Refresh()
	require.Error(t, v.FilterError())
	assert.Equal(t, "zorg", v.CommittedFilter())
	assert.Equal(t, "r2", v.GetSelectedItem())
	assert.Equal(t, 2, v.GetRowCount())

	v.CmdBuff().SetText("never matches", "", true)
	v.DeferFilterDraft("never matches")
	v.Filter("never matches")
	assert.Equal(t, 1, v.GetRowCount())
	assert.Empty(t, v.GetSelectedItem())
	v.CmdBuff().ClearText(false)
	v.DeferFilterDraft("")
	v.Filter("")
	assert.Equal(t, 3, v.GetRowCount())
	assert.NotEmpty(t, v.GetSelectedItem())
}

func TestSnapshotFilterKeepsIdentityAndCurrentEvidence(t *testing.T) {
	v := ui.NewTable(client.NewGVR("test"))
	v.Init(makeContext())
	m := &snapshotResourceFilterModel{&mutableResourceFilterModel{mockModel: new(mockModel), data: makeTableData()}}
	v.SetModel(m)
	v.SetLiteralFields(true)
	v.SetSortCol("C", true)
	v.SetRect(0, 0, 120, 34)
	query := func(text string) {
		v.CmdBuff().SetText(text, "", true)
		v.Filter(text)
	}
	query("[")
	require.Error(t, v.FilterError())
	assert.Equal(t, 2, v.GetFilteredData().RowCount(), "first malformed query retains initial source")
	query("zorg")
	require.Equal(t, "r2", v.GetSelectedItem())
	assert.Contains(t, v.FilterStatusText(), "1/2", "count includes all captured source rows")
	query("[")
	require.Error(t, v.FilterError())
	assert.Equal(t, "zorg", v.CommittedFilter())
	assert.Equal(t, "r2", v.GetSelectedItem())
	query("blee")
	v.SelectRow(2, 0, true)
	require.Equal(t, "r2", v.GetSelectedItem())
	oldCell := v.GetCell(2, 1)
	m.data.Update(model1.Rows{
		{ID: "r1", Fields: model1.Fields{"blee", "duh", "fred"}},
		{ID: "r2", Fields: model1.Fields{"blee", "[red] evidence", "aaa"}},
	})
	query("blee")
	assert.Equal(t, "r2", v.GetSelectedItem(), "a watched sort-field change retains resource identity")
	assert.NotSame(t, oldCell, v.GetCell(1, 1))
	assert.Contains(t, v.GetCell(1, 1).Text, "[red[] evidence", "literal markup survives the detached snapshot")
	sourceRow, found := m.data.FindRow("r2")
	require.True(t, found)
	assert.Equal(t, "[red] evidence", sourceRow.Row.Fields[1])
	assert.Equal(t, "duh", sourceRow.Deltas[1], "display processing cannot rewrite watched deltas")
	query("never matches")
	assert.Empty(t, v.GetSelectedItem())
	assert.Contains(t, v.FilterStatusText(), "0/2")
	v.SetRect(0, 0, 60, 24)
	v.Refresh()
	query("!zorg")
	assert.Equal(t, 3, v.GetRowCount(), "inverse query uses current changed fields")
	query("")
	assert.Equal(t, 3, v.GetRowCount())
	assert.NotEmpty(t, v.GetSelectedItem())
}
