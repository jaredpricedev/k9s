// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"context"
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/tcell/v2"
)

func performanceTable(rows int) *model1.TableData {
	events := model1.NewRowEvents(rows)
	for i := range rows {
		name := fmt.Sprintf("deployment-%05d", i)
		events.Add(model1.RowEvent{Row: model1.Row{ID: "apps/" + name, Fields: model1.Fields{"apps", name, "1/1", "Running", "0", "12m", "10.0.1.42", "worker-01"}}})
	}
	return model1.NewTableDataFull(client.NewGVR("v1/pods"), client.NamespaceAll, model1.Header{{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"}, {Name: "RESTARTS"}, {Name: "AGE"}, {Name: "IP"}, {Name: "NODE"}}, events)
}

func BenchmarkPerformancePadding(b *testing.B) {
	for _, rows := range []int{1000, 10000} {
		b.Run(fmt.Sprint(rows), func(b *testing.B) {
			data := performanceTable(rows)
			pads := make(MaxyPad, data.HeaderCount())
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				ComputeMaxColumns(pads, "NAME", data)
			}
		})
	}
}

// This includes real table construction and viewport drawing, but excludes API traffic.
func BenchmarkPerformanceTableRefresh(b *testing.B) {
	for _, rows := range []int{1000, 10000} {
		b.Run(fmt.Sprint(rows), func(b *testing.B) {
			data := performanceTable(rows)
			table := NewTable(client.NewGVR("v1/pods"))
			ctx := context.WithValue(context.Background(), internal.KeyStyles, config.NewStyles())
			table.Init(ctx)
			table.SetColorerFn((&render.Pod{}).ColorerFunc())
			table.SetRect(0, 0, 160, 40)
			screen := tcell.NewSimulationScreen("")
			if err := screen.Init(); err != nil {
				b.Fatal(err)
			}
			defer screen.Fini()
			screen.SetSize(160, 40)
			cdata := table.Update(data, false)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				table.UpdateUI(cdata, data)
				table.Draw(screen)
			}
		})
	}
}
