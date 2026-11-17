// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nextStepFixture(t *testing.T, noIcon bool) *ui.Table {
	t.Helper()
	gvr := client.NewGVR("v1/pods")
	header := model1.Header{{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"}, {Name: "RESTARTS"}, {Name: "WHY"}}
	rows := []model1.RowEvent{
		{Row: model1.Row{ID: "payments/a-api", Fields: model1.Fields{"a-api", "0/1", "CrashLoopBackOff", "7", "last OOMKilled exit 137"}}},
		{Row: model1.Row{ID: "payments/b-web", Fields: model1.Fields{"b-web", "1/1", "Running", "0", ""}}},
	}
	m := &podLayoutModel{}
	m.data = model1.NewTableDataFull(gvr, m.GetNamespace(), header, model1.NewRowEventsWithEvts(rows...))
	v := ui.NewTable(gvr)
	v.Init(context.WithValue(context.Background(), internal.KeyStyles, config.NewStyles()))
	v.SetModel(m)
	v.SetNoIcon(noIcon)
	v.SetSortCol("NAME", true)
	v.UpdateUI(v.Update(m.data, true), m.data)
	return v
}

func drawNextStep(t *testing.T, v *ui.Table, width int) []string {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(width, 10)
	v.SetRect(0, 0, width, 10)
	v.Draw(screen)
	return strings.Split(strings.TrimRight(layoutScreenText(screen, width, 10), "\n"), "\n")
}

func TestNextStepLineSitsOnTheFrameEdgeAndNeverMovesRows(t *testing.T) {
	all := func(checks []ui.NextCheck) []ui.NextCheck { return checks }
	plain := nextStepFixture(t, false)
	plain.SelectRow(1, 0, true)
	before := drawNextStep(t, plain, 100)

	v := nextStepFixture(t, false)
	v.SetNextStepFilter(all)
	v.SelectRow(1, 0, true)
	lines := drawNextStep(t, v, 100)
	assert.Equal(t, before[:9], lines[:9], "guidance must not move or change any table row")
	assert.Contains(t, lines[9], " next: last exit 137 OOMKilled · p previous logs · :pressure limit vs usage · :graph ")
	assert.True(t, strings.HasPrefix(lines[9], "└─ next") && strings.HasSuffix(lines[9], "─┘"), lines[9])

	// A healthy selection leaves the border as it was.
	v.SelectRow(2, 0, true)
	assert.Equal(t, before[9], drawNextStep(t, v, 100)[9])

	// Narrower frames drop detail, then disappear instead of wrapping.
	v.SelectRow(1, 0, true)
	assert.Contains(t, drawNextStep(t, v, 80)[9], " next: last exit 137 OOMKilled · p previous logs · :pressure · :graph ")
	assert.Contains(t, drawNextStep(t, v, 60)[9], " next: last exit 137 OOMKilled · p previous logs ")
	narrow := drawNextStep(t, v, 44)
	assert.NotContains(t, strings.Join(narrow, "\n"), "next:")
	assert.Equal(t, "└"+strings.Repeat("─", 42)+"┘", narrow[9])

	// Checks the view cannot run are not printed; the evidence remains.
	v.SetNextStepFilter(func([]ui.NextCheck) []ui.NextCheck { return nil })
	line := drawNextStep(t, v, 100)[9]
	assert.Contains(t, line, " next: last exit 137: killed (SIGKILL), out of memory (OOMKilled) ─")
	assert.NotContains(t, line, "previous logs")
}

func TestNextStepLineUsesASCIISeparatorsWithoutIcons(t *testing.T) {
	v := nextStepFixture(t, true)
	v.SetNextStepFilter(func(checks []ui.NextCheck) []ui.NextCheck { return checks })
	v.SelectRow(1, 0, true)
	line := drawNextStep(t, v, 100)[9]
	assert.Contains(t, line, " OOMKilled | p previous logs | :pressure limit vs usage | :graph ")
	assert.NotContains(t, line, "·")
}

func TestNextStepFollowsSelectionAndRefreshedRows(t *testing.T) {
	calls := 0
	v := nextStepFixture(t, false)
	v.SetNextStepFilter(func(checks []ui.NextCheck) []ui.NextCheck { calls++; return checks })
	v.SelectRow(1, 0, true)
	require.False(t, v.SelectedNextStep().Empty())
	_ = v.SelectedNextStep()
	assert.Equal(t, 1, calls, "an unchanged selection is not recomputed on every draw")
	v.SelectRow(2, 0, true)
	assert.True(t, v.SelectedNextStep().Empty())
	assert.Equal(t, 1, calls, "a healthy row asks nothing of the view")
}
