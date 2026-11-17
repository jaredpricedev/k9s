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

func TestNativeProviderLayoutPreservesCompleteStatusAndRetainedDetail(t *testing.T) {
	for _, fixture := range []struct {
		resource, state string
		header          model1.Header
		fields          model1.Fields
	}{
		{"flux", "Reconciling", model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "KIND"}, {Name: "STATUS"}, {Name: "SUSPEND"}, {Name: "REVISION"}, {Name: "SOURCE"}}, model1.Fields{"payments", "京都-long-production-release-controller", "Kustomization", "Reconciling", "false", "main@sha1:abcdef0123456789", "GitRepository:payments/repo"}},
		{"certificates", "NotReady", model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "STATUS"}, {Name: "EXPIRES"}, {Name: "RENEWAL"}, {Name: "ISSUER"}}, model1.Fields{"payments", "京都-long-production-certificate", "NotReady", "2026-10-06T03:04:05Z", "2026-10-05T03:04:05Z", "ClusterIssuer:production"}},
	} {
		t.Run(fixture.resource, func(t *testing.T) {
			styles := config.NewStyles()
			require.NoError(t, styles.Load("../../skins/monochrome.yaml", false))
			gvr := client.NewGVR("v1/" + fixture.resource)
			m := &podLayoutModel{all: true}
			m.data = model1.NewTableDataFull(gvr, client.NamespaceAll, fixture.header, model1.NewRowEventsWithEvts(model1.RowEvent{Row: model1.Row{ID: "payments/resource", Fields: fixture.fields}}))
			v := ui.NewTable(gvr)
			v.Init(context.WithValue(context.Background(), internal.KeyStyles, styles))
			v.SetModel(m)
			v.SetNoIcon(true)
			v.UpdateUI(v.Update(m.data, true), m.data)
			v.SelectRow(1, 0, true)
			screen := tcell.NewSimulationScreen("")
			require.NoError(t, screen.Init())
			defer screen.Fini()
			for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 16}, {80, 24}} {
				screen.SetSize(size[0], size[1])
				screen.Clear()
				v.SetRect(0, 0, size[0], size[1])
				v.Draw(screen)
				text := layoutScreenText(screen, size[0], size[1])
				require.Contains(t, text, fixture.state)
				require.Equal(t, fixture.fields, v.GetSelectedRow("payments/resource").Fields)
				if fixture.resource == "certificates" && strings.Contains(text, "EXPIRES") {
					require.Contains(t, text, "2026-10-06T03:04:05Z", "dates are complete or intentionally hidden")
				}
			}
		})
	}
}

func TestPodLayoutReturnsSpareWidthToNames(t *testing.T) {
	for _, all := range []bool{false, true} {
		v, _ := podLayoutFixture(t, all, config.NewStyles())
		screen := tcell.NewSimulationScreen("")
		require.NoError(t, screen.Init())
		screen.SetSize(180, 40)
		v.SetRect(0, 0, 180, 40)
		v.Draw(screen)
		text := layoutScreenText(screen, 180, 40)
		screen.Fini()
		assert.Contains(t, text, "payments-reconciliation-worker-production-release-2026-4",
			"all=%v: a name is not elided while the row has blank cells", all)
		for _, kept := range []string{"ContainerStatusUnknown", "worker-east-production", "2h"} {
			assert.Contains(t, text, kept, "all=%v: wider names do not displace other columns", all)
		}
	}
}

func TestTableSummarizesAndOrdersProblemsFromCellClassification(t *testing.T) {
	gvr := client.NewGVR("v1/pods")
	header := model1.Header{{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"}, {Name: "RESTARTS"}, {Name: "VALID"}}
	var rows []model1.RowEvent
	for _, f := range [][]string{
		{"a-healthy", "1/1", "Running", "9", ""},
		{"b-starting", "0/1", "ContainerCreating", "0", ""},
		{"c-crashing", "0/1", "CrashLoopBackOff", "7", ""},
		{"d-pending", "0/1", "Pending", "0", ""},
		{"e-healthy", "2/2", "Running", "0", ""},
	} {
		rows = append(rows, model1.RowEvent{Row: model1.Row{ID: "ns/" + f[0], Fields: f}})
	}
	m := &podLayoutModel{}
	m.data = model1.NewTableDataFull(gvr, "payments", header, model1.NewRowEventsWithEvts(rows...))
	v := ui.NewTable(gvr)
	v.Init(context.WithValue(context.Background(), internal.KeyStyles, config.NewStyles()))
	v.SetModel(m)
	v.SetNoIcon(true)
	v.SetSortCol("NAME", true)
	v.SetRect(0, 0, 120, 30)
	v.UpdateUI(v.Update(m.data, true), m.data)

	title := v.GetTitle()
	for _, want := range []string{"1 failing", "1 degraded", "1 in progress"} {
		assert.Contains(t, title, want, "restarts and readiness counts alone do not inflate the summary")
	}
	v.SelectRow(4, 0, true)
	require.Equal(t, "ns/a-healthy", v.GetSelectedItem())
	var order []string
	for row := 1; row < v.GetRowCount(); row++ {
		id, _ := v.GetRowID(row)
		order = append(order, strings.TrimPrefix(id, "ns/"))
	}
	assert.Equal(t, []string{"c-crashing", "d-pending", "b-starting", "a-healthy", "e-healthy"}, order,
		"severity first, then the chosen column order")
	assert.Contains(t, v.GetTitle(), "problems first", "a changed order is stated in text")

	require.False(t, v.ToggleProblemsFirst())
	defer ui.SetProblemsFirst(true)
	first, _ := v.GetRowID(1)
	assert.Equal(t, "ns/a-healthy", first, "Shift-X restores plain column order")
	assert.NotContains(t, v.GetTitle(), "problems first")
	assert.Equal(t, "ns/a-healthy", v.GetSelectedItem(), "reordering keeps the selected resource")
}

// whyLayoutFixture lists a non-Pod kind whose renderer supplies WHY and a
// verdict, the way the Deployment, Node and Job renderers do.
func whyLayoutFixture(t *testing.T, resource string, header model1.Header, rows ...model1.Row) *ui.Table {
	t.Helper()
	gvr := client.NewGVR(resource)
	events := make([]model1.RowEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, model1.RowEvent{Row: row})
	}
	m := &podLayoutModel{all: true}
	m.data = model1.NewTableDataFull(gvr, client.NamespaceAll, header, model1.NewRowEventsWithEvts(events...))
	v := ui.NewTable(gvr)
	v.Init(context.WithValue(context.Background(), internal.KeyStyles, config.NewStyles()))
	v.SetModel(m)
	v.SetNoIcon(true)
	v.SetSortCol("NAME", true)
	return v
}

func drawWhyLayout(t *testing.T, v *ui.Table, width, height int) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(width, height)
	v.SetRect(0, 0, width, height)
	v.Refresh()
	v.Draw(screen)
	return layoutScreenText(screen, width, height)
}

func TestWhyColumnExplainsFailingWorkloadsWithoutDisplacingState(t *testing.T) {
	header := model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"},
		{Name: "READY", Attrs: model1.Attrs{Align: tview.AlignRight}},
		{Name: "UP-TO-DATE", Attrs: model1.Attrs{Align: tview.AlignRight}},
		{Name: "AVAILABLE", Attrs: model1.Attrs{Align: tview.AlignRight}},
		{Name: "WHY"}, {Name: "VALID", Attrs: model1.Attrs{Wide: true}}, {Name: "AGE"}}
	const cause = "ProgressDeadlineExceeded: ReplicaSet \"checkout-api-5d9c7b\" has timed out progressing after ten minutes of waiting."
	healthy := model1.Row{ID: "apps/a-healthy", Fields: model1.Fields{"apps", "a-healthy", "2/2", "2", "2", "", "", "3d"}}
	stalled := model1.Row{ID: "apps/checkout-api-production-release", Level: model1.LevelFailing,
		Fields: model1.Fields{"apps", "checkout-api-production-release", "1/2", "1", "1", cause, "", "3d"}}
	rolling := model1.Row{ID: "apps/m-rolling", Level: model1.LevelProgress,
		Fields: model1.Fields{"apps", "m-rolling", "2/3", "1", "2", "rollout: 1 of 3 updated", "", "3d"}}

	quiet := whyLayoutFixture(t, "apps/v1/deployments", header, healthy)
	for _, width := range []int{120, 80, 60} {
		text := drawWhyLayout(t, quiet, width, 12)
		assert.NotContains(t, text, "WHY", "width %d: no column when no row reports a cause", width)
		assert.Contains(t, text, "2/2")
	}

	v := whyLayoutFixture(t, "apps/v1/deployments", header, healthy, stalled, rolling)
	for _, width := range []int{120, 80, 60} {
		text := drawWhyLayout(t, v, width, 12)
		assert.Contains(t, text, "WHY", "width %d", width)
		assert.Contains(t, text, "ProgressDeadlineE", "width %d: the cause is readable", width)
		assert.NotContains(t, text, "of waiting.", "width %d: a long cause is elided", width)
		assert.Contains(t, text, "…", "width %d: elision is marked", width)
		for _, state := range []string{"1/2", "2/3", "2/2", "READY"} {
			assert.Contains(t, text, state, "width %d: the cause never displaces readiness", width)
		}
		assert.Contains(t, text, "checkout-api", "width %d: the name stays recognizable", width)
		for _, line := range strings.Split(text, "\n") {
			assert.LessOrEqual(t, len([]rune(strings.TrimRight(line, " "))), width)
		}
		title := v.GetTitle()
		assert.Contains(t, title, "1 failing", "a failed rollout counts as failing, not merely degraded")
		assert.Contains(t, title, "1 in progress")
		assert.NotContains(t, title, "degraded")
		first, _ := v.GetRowID(1)
		assert.Equal(t, "apps/checkout-api-production-release", first, "the failing row leads")
	}
	assert.Equal(t, cause, v.GetSelectedRow("apps/checkout-api-production-release").Fields[5], "the model keeps the whole cause")

	v.ToggleWide()
	assert.Contains(t, drawWhyLayout(t, v, 260, 12), "of waiting.", "Ctrl-W shows the cause in full")
}

func TestWhyColumnFitsListsWithoutAStateColumn(t *testing.T) {
	header := model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "COMPLETIONS"}, {Name: "DURATION"},
		{Name: "SELECTOR", Attrs: model1.Attrs{Wide: true}}, {Name: "WHY"}, {Name: "AGE"}}
	v := whyLayoutFixture(t, "batch/v1/jobs", header,
		model1.Row{ID: "ops/ledger-reconcile", Level: model1.LevelFailing, Fields: model1.Fields{
			"ops", "ledger-reconcile", "0/1", "<none>", "", "DeadlineExceeded: Job was active longer than specified deadline", "4h"}},
		model1.Row{ID: "ops/nightly-export", Fields: model1.Fields{"ops", "nightly-export", "1/1", "42s", "", "", "4h"}})
	for _, width := range []int{120, 80, 60} {
		text := drawWhyLayout(t, v, width, 10)
		for _, want := range []string{"ledger-reconcile", "COMPLETIONS", "0/1"} {
			assert.Contains(t, text, want, "width %d: the cause never displaces completions", width)
		}
		// A Job counted as failing must show its evidence at every width: the
		// duration gives way before the cause does.
		assert.Contains(t, text, "DeadlineExceeded", "width %d", width)
		assert.Contains(t, text, "4h", "width %d: age stays", width)
		if width == 60 {
			assert.NotContains(t, text, "DURATION", "an optional column gives way to the cause")
		}
		for _, line := range strings.Split(text, "\n") {
			assert.LessOrEqual(t, len([]rune(strings.TrimRight(line, " "))), width)
		}
		assert.Contains(t, v.GetTitle(), "1 failing", "a failed Job is counted although it has no status column")
	}

	// A list whose natural columns overflow and whose cause cannot be made
	// readable keeps every column: nothing is traded for an unreadable cause.
	wide := model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "COMPLETIONS"}, {Name: "WHY"}, {Name: "AGE"}}
	cramped := whyLayoutFixture(t, "batch/v1/jobs", wide, model1.Row{ID: "ops/x", Level: model1.LevelFailing, Fields: model1.Fields{
		"ops", "a-job-name-long-enough", "0/1", "DeadlineExceeded: Job was active longer than specified deadline", "4h"}})
	text := drawWhyLayout(t, cramped, 40, 10)
	assert.NotContains(t, text, "WHY", "too narrow to read: the cause is left out, not the state")
	assert.Contains(t, text, "NAMESPACE", "nothing is given up for a cause that cannot be shown")
	assert.Contains(t, text, "a-job-name-long-enough")
	assert.Contains(t, cramped.GetTitle(), "1 failing")

	// A status word is never lowered by a milder renderer verdict.
	claims := model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "STATUS"}, {Name: "WHY"}, {Name: "AGE"}}
	lost := whyLayoutFixture(t, "v1/persistentvolumeclaims", claims, model1.Row{ID: "ops/data", Level: model1.LevelProgress,
		Fields: model1.Fields{"ops", "data", "Failed", "generation 2 not yet observed, status is for 1", "4h"}})
	drawWhyLayout(t, lost, 120, 10)
	assert.Contains(t, lost.GetTitle(), "1 failing")
	assert.NotContains(t, lost.GetTitle(), "in progress")

	nodes := model1.Header{{Name: "NAME"}, {Name: "STATUS"}, {Name: "ROLE"}, {Name: "TAINTS"}, {Name: "VERSION"},
		{Name: "PODS"}, {Name: "CPU"}, {Name: "CPU/A"}, {Name: "%CPU"}, {Name: "MEM"}, {Name: "MEM/A"}, {Name: "%MEM"},
		{Name: "GPU/A"}, {Name: "GPU/C"}, {Name: "WHY"}, {Name: "AGE"}}
	n := whyLayoutFixture(t, "v1/nodes", nodes,
		model1.Row{ID: "worker-b", Level: model1.LevelFailing, Fields: model1.Fields{"worker-b", "NotReady,SchedulingDisabled", "worker", "0",
			"v1.33.1", "12", "n/a", "4000", "n/a", "n/a", "16000", "n/a", "0", "0", "Kubelet stopped posting node status; cordoned", "9d"}},
		model1.Row{ID: "worker-c", Level: model1.LevelDegraded, Fields: model1.Fields{"worker-c", "Ready", "worker", "0",
			"v1.33.1", "30", "n/a", "4000", "n/a", "n/a", "16000", "n/a", "0", "0", "DiskPressure", "9d"}})
	for _, width := range []int{120, 80} {
		text := drawWhyLayout(t, n, width, 10)
		assert.Contains(t, text, "NotReady,SchedulingDisabled", "width %d: status is complete", width)
		assert.Contains(t, text, "Kubelet stopped posting", "width %d: optional columns give way to the cause", width)
		assert.Contains(t, text, "DiskPressure", "width %d", width)
		assert.Contains(t, n.GetTitle(), "1 failing")
		assert.Contains(t, n.GetTitle(), "1 degraded")
	}
}
