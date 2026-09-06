// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCronJobBindKeysHonorsReadOnly(t *testing.T) {
	for _, tt := range []struct {
		name     string
		readOnly bool
		want     bool
	}{
		{name: "mutable", want: true},
		{name: "read-only", readOnly: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cronJob := newCronJobForReadOnlyTest(t, tt.readOnly)
			actions := ui.NewKeyActions()

			cronJob.bindKeys(actions)

			for _, key := range []tcell.Key{ui.KeyT, ui.KeyS} {
				action, ok := actions.Get(key)
				assert.Equal(t, tt.want, ok)
				if ok {
					assert.True(t, action.Opts.Dangerous)
				}
			}
		})
	}
}

func TestCronJobActionsRecheckReadOnly(t *testing.T) {
	for _, key := range []tcell.Key{ui.KeyT, ui.KeyS} {
		t.Run(tcell.KeyNames[key], func(t *testing.T) {
			cronJob := newCronJobForReadOnlyTest(t, false)
			actions := ui.NewKeyActions()
			cronJob.bindKeys(actions)
			action, ok := actions.Get(key)
			require.True(t, ok)

			cronJob.App().Config.K9s.ReadOnly = true
			evt := tcell.NewEventKey(key, 0, tcell.ModNone)

			assert.Same(t, evt, action.Action(evt))
			assert.False(t, cronJob.App().Content.IsTopDialog())
		})
	}
}

type cronJobReadOnlyTestViewer struct {
	ResourceViewer
	table *Table
	app   *App
}

func (v *cronJobReadOnlyTestViewer) App() *App        { return v.app }
func (v *cronJobReadOnlyTestViewer) GetTable() *Table { return v.table }
func (*cronJobReadOnlyTestViewer) Start()             {}
func (*cronJobReadOnlyTestViewer) Stop()              {}

type cronJobReadOnlyTestModel struct{ ui.Tabular }

func (*cronJobReadOnlyTestModel) Empty() bool          { return false }
func (*cronJobReadOnlyTestModel) GetNamespace() string { return "default" }
func (*cronJobReadOnlyTestModel) ClusterWide() bool    { return false }

func newCronJobForReadOnlyTest(t *testing.T, readOnly bool) *CronJob {
	t.Helper()
	cfg := mock.NewMockConfig(t)
	cfg.K9s.ReadOnly = readOnly
	app := NewApp(cfg)
	table := NewTable(client.CjGVR)
	table.SetModel(&cronJobReadOnlyTestModel{})
	table.SetCell(1, 0, tview.NewTableCell("cronjob").SetReference("default/cronjob"))
	table.SetCell(1, 2, tview.NewTableCell("false"))
	table.Select(1, 0)

	return &CronJob{ResourceViewer: &cronJobReadOnlyTestViewer{
		table: table,
		app:   app,
	}}
}
