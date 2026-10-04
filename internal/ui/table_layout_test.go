// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type podLayoutModel struct {
	mockModel
	data *model1.TableData
	all  bool
}

func (m *podLayoutModel) Peek() *model1.TableData { return m.data }
func (m *podLayoutModel) Empty() bool             { return m.data.RowCount() == 0 }
func (m *podLayoutModel) RowCount() int           { return m.data.RowCount() }
func (m *podLayoutModel) ClusterWide() bool       { return m.all }
func (m *podLayoutModel) GetNamespace() string {
	if m.all {
		return client.NamespaceAll
	}
	return "payments"
}

func podLayoutFixture(t *testing.T, all bool, styles *config.Styles) (*ui.Table, *podLayoutModel) {
	t.Helper()
	gvr := client.NewGVR("v1/pods")
	header := model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"},
		{Name: "RESTARTS", Attrs: model1.Attrs{Align: tview.AlignRight}},
		{Name: "CPU", Attrs: model1.Attrs{MX: true}}, {Name: "MEM", Attrs: model1.Attrs{MX: true}},
		{Name: "NODE"}, {Name: "AGE"}}
	var rows []model1.RowEvent
	for i, status := range []string{"CrashLoopBackOff", "ContainerCreating", "ContainerStatusUnknown", "ImagePullBackOff", "Completed"} {
		name := fmt.Sprintf("payments-reconciliation-worker-production-release-2026-%d", i)
		rows = append(rows, model1.RowEvent{Row: model1.Row{ID: "payments/" + name,
			Fields: model1.Fields{"payments", name, "0/1", status, fmt.Sprint(1000 + i), "3m", "24Mi", "worker-east-production-01", "2h"}}})
	}
	m := &podLayoutModel{all: all}
	m.data = model1.NewTableDataFull(gvr, m.GetNamespace(), header, model1.NewRowEventsWithEvts(rows...))
	v := ui.NewTable(gvr)
	ctx := context.WithValue(context.Background(), internal.KeyStyles, styles)
	v.Init(ctx)
	v.SetModel(m)
	v.SetNoIcon(true)
	v.SetSortCol("NAME", true)
	v.UpdateUI(v.Update(m.data, true), m.data)
	v.SelectRow(1, 0, true)
	return v, m
}

func layoutScreenText(screen tcell.Screen, width, height int) string {
	var out strings.Builder
	for y := range height {
		for x := range width {
			ch, _, _, _ := screen.GetContent(x, y)
			out.WriteRune(ch)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func TestPodLayoutPreservesFaultStatesAndIdentityAcrossResize(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint("all=", all), func(t *testing.T) {
			v, m := podLayoutFixture(t, all, config.NewStyles())
			selected := v.GetSelectedItem()
			screen := tcell.NewSimulationScreen("")
			require.NoError(t, screen.Init())
			defer screen.Fini()
			for _, size := range [][2]int{{180, 40}, {80, 24}, {100, 30}, {120, 34}, {80, 24}} {
				v.SetOffset(0, 3)
				screen.SetSize(size[0], size[1])
				screen.Clear()
				v.SetRect(0, 0, size[0], size[1])
				v.Draw(screen)
				text := layoutScreenText(screen, size[0], size[1])
				for _, state := range []string{"CrashLoopBackOff", "ContainerCreating", "ContainerStatusUnknown", "ImagePullBackOff", "Completed", "0/1", "1000", "1004"} {
					assert.Contains(t, text, state, "%dx%d", size[0], size[1])
				}
				assert.Contains(t, text, "> ", "selection is readable without color or icons")
				if !all {
					assert.Contains(t, text, "…", "a shortened first-column name keeps its elision after the selection marker")
				}
				assert.Equal(t, selected, v.GetSelectedItem())
				row := v.GetSelectedRow(selected)
				require.NotNil(t, row)
				assert.Equal(t, 5, m.data.RowCount())
				assert.Contains(t, row.Fields[1], "production-release-2026-0", "detail retains full identity")
				_, offset := v.GetOffset()
				assert.Zero(t, offset, "resize recovers horizontal scroll")
			}
		})
	}
}

func TestPodSelectedStatusSurvivesSkinChange(t *testing.T) {
	styles := config.NewStyles()
	v, _ := podLayoutFixture(t, false, styles)
	styles.AddListener(v)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(80, 24)
	v.SetRect(0, 0, 80, 24)
	selected := v.GetSelectedItem()
	for _, skin := range []string{"", "../../skins/high-contrast.yaml", "../../skins/monochrome.yaml", "../../skins/black-and-wtf.yaml"} {
		styles.Reset(false)
		if skin != "" {
			require.NoError(t, styles.Load(skin, false))
		}
		styles.Update()
		screen.Clear()
		v.Draw(screen)
		assert.Equal(t, selected, v.GetSelectedItem())
		text := layoutScreenText(screen, 80, 24)
		assert.Contains(t, text, "CrashLoopBackOff")
		assert.Contains(t, text, "> ")
		found := false
		for y := range 24 {
			for x := range 80 {
				ch, _, style, _ := screen.GetContent(x, y)
				var word strings.Builder
				for dx := 0; dx < len("CrashLoopBackOff") && x+dx < 80; dx++ {
					c, _, _, _ := screen.GetContent(x+dx, y)
					word.WriteRune(c)
				}
				if ch != 'C' || word.String() != "CrashLoopBackOff" {
					continue
				}
				fg, bg, _ := style.Decompose()
				assert.Equal(t, styles.Semantic().Selected.Color(), bg)
				assert.Equal(t, config.ReadableForeground(styles.Semantic().Failure.Color(), bg), fg, "selected row retains severity")
				assert.GreaterOrEqual(t, config.ContrastRatio(fg, bg), 4.5)
				found = true
			}
		}
		assert.True(t, found)
	}
}

func TestTableDrawPreservesNonselectableView(t *testing.T) {
	table, _ := podLayoutFixture(t, false, config.NewStyles())
	table.SetSelectable(false, false)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(80, 24)
	table.SetRect(0, 0, 80, 24)
	table.Draw(screen)
	rows, columns := table.GetSelectable()
	assert.False(t, rows, "help and other static tables stay unselectable after a frame")
	assert.False(t, columns)
	assert.NotContains(t, layoutScreenText(screen, 80, 24), "> ", "static views do not imply a selected resource")
}
