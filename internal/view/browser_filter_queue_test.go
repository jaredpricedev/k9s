// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type retainedFilterModel struct {
	mockTableModel
	data *model1.TableData
}

func (m *retainedFilterModel) Peek() *model1.TableData { return m.data.Clone() }

func TestBrowserQueuedWatchProjectsAcceptedQueryOnUIThread(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "changed rows"
		if empty {
			name = "no rows"
		}
		t.Run(name, func(t *testing.T) {
			b := &Browser{Table: NewTable(client.NewGVR("test")),
				meta: &metav1.APIResource{Kind: "Test", Categories: []string{"k9s"}}, cancelFn: func() {}}
			require.NoError(t, b.Table.Init(makeContext(t)))
			data := makeTableData()
			data.RowsRange(func(i int, event model1.RowEvent) bool {
				event.Row.ID = event.Row.Fields[1]
				data.SetRow(i, event)
				return true
			})
			b.SetModel(&retainedFilterModel{data: data})
			app := b.App()
			app.Config.SetConnection(mock.NewMockConnection())
			// Keep this test focused on rendering, without plugin/API discovery.
			app.Content.Push(NewDetails(app, "other view", "", contentTXT, false))
			b.Filter("r2")
			require.Equal(t, "r2", b.GetSelectedItem())
			b.activateCmd(nil)
			b.CmdBuff().SetText("r1", "", true)
			b.filterRequests.cancel()

			var projections atomic.Int32
			projected := make(chan struct{})

			initialPaint, blocked, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var firstPaint, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			screen := tcell.NewSimulationScreen("UTF-8")
			require.NoError(t, screen.Init())
			screen.SetSize(120, 34)
			app.SetScreen(screen).SetRoot(b, true).SetAfterDrawFunc(func(tcell.Screen) {
				firstPaint.Do(func() { close(initialPaint) })
			})
			app.SetRunning(true)
			finished := make(chan error, 1)
			go func() { finished <- app.Application.Run() }()
			t.Cleanup(func() {
				unblock()
				app.Stop()
				select {
				case err := <-finished:
					assert.NoError(t, err)
				case <-time.After(time.Second):
					t.Error("simulation did not stop")
				}
				app.SetRunning(false)
				b.Table.Stop()
			})
			select {
			case <-initialPaint:
			case <-time.After(time.Second):
				t.Fatal("simulation did not draw")
			}
			go app.Application.QueueUpdate(func() {
				b.SetDecorateFn(func(*model1.TableData) {
					if projections.Add(1) == 1 {
						close(projected)
					}
				})
				close(blocked)
				<-release
				// Enter runs on the UI thread before queued watcher work.
				b.SendKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			})
			select {
			case <-blocked:
			case <-time.After(time.Second):
				t.Fatal("simulation did not reach its initial paint")
			}
			snapshot := data.Clone()
			if empty {
				snapshot.SetRowEvents(model1.NewRowEvents(0))
				b.firstView.Store(1)
				b.TableNoData(snapshot)
			} else {
				b.TableDataChanged(snapshot)
			}
			assert.Zero(t, projections.Load(), "watchers must not project the old draft outside the UI callback")
			unblock()
			select {
			case <-projected:
			case <-time.After(time.Second):
				t.Fatal("queued watcher did not project")
			}
			type observed struct {
				query, selected, status string
				rows                    int
			}
			result := make(chan observed, 1)
			app.Application.QueueUpdate(func() {
				result <- observed{b.CommittedFilter(), b.GetSelectedItem(), b.FilterStatusText(), b.GetRowCount()}
			})
			state := <-result
			assert.Equal(t, "r1", state.query)
			if empty {
				assert.Equal(t, 1, state.rows)
				assert.Empty(t, state.selected)
				assert.Contains(t, state.status, "0/0")
			} else {
				assert.Equal(t, 2, state.rows)
				assert.Equal(t, "r1", state.selected)
				assert.Contains(t, state.status, "1/4")
			}
		})
	}
}
