// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"
)

func TestResourceFilterCoalescesDraftsAndInvalidatesQueuedPrefixes(t *testing.T) {
	var requests resourceFilterRequests
	requests.start()
	defer requests.stop()
	queued := make(chan func(), 8)
	enqueue := func(f func()) { queued <- f }
	var applied []string
	apply := func(text string) { applied = append(applied, text) }
	for _, draft := range []string{"t", "ta", "target-00002"} {
		requests.submit(draft, resourceFilterDelay, enqueue, apply)
	}
	select {
	case f := <-queued:
		f()
	case <-time.After(time.Second):
		t.Fatal("quiet draft did not dispatch")
	}
	require.Equal(t, []string{"target-00002"}, applied)

	// A prefix may already be queued when the next rune makes it invalid.
	requests.submit("target", 0, enqueue, apply)
	var stale func()
	select {
	case stale = <-queued:
	case <-time.After(time.Second):
		t.Fatal("prefix did not dispatch")
	}
	requests.cancel()
	requests.submit("[", resourceFilterDelay, enqueue, apply)
	stale()
	assert.Equal(t, []string{"target-00002"}, applied)
	select {
	case f := <-queued:
		f()
	case <-time.After(time.Second):
		t.Fatal("invalid draft did not dispatch for validation")
	}
	assert.Equal(t, []string{"target-00002", "["}, applied)
}

func TestResourceFilterStopAndRestartRejectPreviousDispatch(t *testing.T) {
	var requests resourceFilterRequests
	requests.start()
	queued := make(chan func(), 4)
	var applied []string
	apply := func(text string) { applied = append(applied, text) }
	requests.submit("old resource", 0, func(f func()) { queued <- f }, apply)
	var stale func()
	select {
	case stale = <-queued:
	case <-time.After(time.Second):
		t.Fatal("filter did not dispatch")
	}
	requests.stop()
	requests.start()
	stale()
	assert.Empty(t, applied, "returning to the same table must not revive work from its prior lifetime")
	requests.stop()
	requests.submit("stopped", 0, func(f func()) { queued <- f }, apply)
	select {
	case <-queued:
		t.Fatal("stopped view dispatched a filter")
	default:
	}
}

func TestResourceFilterConcurrentDraftCancellation(_ *testing.T) {
	var requests resourceFilterRequests
	requests.start()
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 20 {
				requests.submit("draft", time.Hour, func(func()) {}, func(string) {})
				requests.cancel()
			}
		})
	}
	workers.Wait()
	requests.stop()
}

type filterRefreshModel struct {
	mockTableModel
	selector  labels.Selector
	refreshes int
}

func (m *filterRefreshModel) SetLabelSelector(selector labels.Selector) { m.selector = selector }
func (m *filterRefreshModel) GetLabelSelector() labels.Selector         { return m.selector }
func (m *filterRefreshModel) Refresh(context.Context) error {
	m.refreshes++
	return nil
}

func TestBrowserLocalFiltersDoNotRegenerateRetainedModel(t *testing.T) {
	b := &Browser{Table: NewTable(client.NewGVR("test"))}
	require.NoError(t, b.Table.Init(makeContext(t)))
	m := &filterRefreshModel{selector: labels.Everything()}
	b.SetModel(m)
	b.App().Config.SetConnection(mock.NewMockConnection())
	for _, text := range []string{"r2", "!r3", "-f r2", ""} {
		b.CmdBuff().SetText(text, "", true)
		b.BufferActive(false, 0)
	}
	assert.Zero(t, m.refreshes, "local queries must filter retained data, without rendering every API object again")
	b.CmdBuff().SetText("-l app=payments", "", true)
	b.BufferActive(false, 0)
	assert.Equal(t, 1, m.refreshes, "a label selector changes the underlying resource set")
	b.BufferActive(false, 0)
	assert.Equal(t, 1, m.refreshes, "submitting the same selector reuses the retained resource set")
	b.CmdBuff().SetText("r2", "", true)
	b.BufferActive(false, 0)
	assert.Equal(t, 2, m.refreshes, "clearing a label selector must restore the full underlying resource set")
	b.filterRequests.stop()
}

func TestResourceFilterPromptFlushesAcceptedDraftBeforeNextAction(t *testing.T) {
	v := NewTable(client.NewGVR("test"))
	require.NoError(t, v.Init(makeContext(t)))
	v.SetModel(new(mockTableModel))
	defer v.Stop()
	v.CmdBuff().SetText("r2", "", true)
	v.Filter("r2")
	require.Equal(t, 2, v.GetRowCount())
	v.activateCmd(nil)
	for _, prefix := range []string{"r", "r1"} {
		v.CmdBuff().SetText(prefix, "", true)
	}
	assert.Equal(t, "r2", v.CommittedFilter(), "the replacement stays a draft until quiet or accepted")
	v.SendKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	assert.False(t, v.CmdBuff().IsActive())
	assert.Equal(t, "r1", v.CommittedFilter())
	assert.Equal(t, "r1", v.GetCell(1, 0).Text, "the next action must see the accepted row immediately")

	v.activateCmd(nil)
	v.CmdBuff().SetText("[", "", true)
	v.SendKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	assert.True(t, v.CmdBuff().IsActive())
	assert.Equal(t, "r1", v.CommittedFilter())
	assert.Equal(t, "r1", v.GetCell(1, 0).Text)
	v.SendKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	assert.False(t, v.CmdBuff().IsActive())
	assert.Empty(t, v.CommittedFilter())
	assert.Equal(t, 5, v.GetRowCount())
}
