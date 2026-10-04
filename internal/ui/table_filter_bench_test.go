// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

// Exercise the native filter path, including the model's detached snapshots,
// source-field comparisons, responsive columns, sorting, cells and viewport.
// A sequence includes broad prefixes, narrowed rows, zero matches and reset.
func BenchmarkTableFilter10K(b *testing.B) {
	gvr := client.NewGVR("v1/pods")
	header := render.NewPod().Header("apps")
	rows := model1.NewRowEvents(10000)
	for i := range 10000 {
		name := fmt.Sprintf("target-%05d", i)
		fields := make(model1.Fields, len(header))
		for col, h := range header {
			switch h.Name {
			case "NAME":
				fields[col] = name
			case "NAMESPACE":
				fields[col] = "apps"
			case "READY":
				fields[col] = "0/1"
			case "STATUS":
				fields[col] = "CrashLoopBackOff"
			case "RESTARTS":
				fields[col] = "7"
			case "NODE":
				fields[col] = "worker-01"
			case "AGE":
				fields[col] = "12m"
			}
		}
		rows.Add(model1.RowEvent{Row: model1.Row{ID: "apps/" + name, Fields: fields}})
	}
	data := model1.NewTableDataFull(gvr, "apps", header, rows)
	table := ui.NewTable(gvr)
	table.Init(makeContext())
	table.SetModel(&mutableResourceFilterModel{mockModel: new(mockModel), data: data})
	table.SetLiteralFields(true)
	table.SetColorerFn(render.NewPod().ColorerFunc())
	table.SetRect(0, 0, 120, 34)
	table.UpdateUI(table.Update(data, false), data)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		b.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 34)
	queries := []string{"t", "ta", "tar", "targ", "targe", "target", "target-", "target-0", "target-00", "target-000", "target-0000", "target-00001", "no-match", ""}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, query := range queries {
			table.CmdBuff().SetText(query, "", true)
			table.Filter(query)
			table.Draw(screen)
		}
	}
}
