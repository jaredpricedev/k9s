// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui_test

import (
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyActionsHints(t *testing.T) {
	kk := ui.NewKeyActionsFromMap(ui.KeyMap{
		ui.KeyF: ui.NewKeyAction("fred", nil, true),
		ui.KeyB: ui.NewKeyAction("blee", nil, true),
		ui.KeyZ: ui.NewKeyAction("zorg", nil, false),
	})

	hh := kk.Hints()

	assert.Len(t, hh, 3)
	assert.Equal(t, model.MenuHint{Mnemonic: "b", Description: "blee", Visible: true}, hh[0])
}

func TestKeyActionsOwnConstructorMap(t *testing.T) {
	original := ui.KeyMap{ui.KeyA: ui.NewKeyAction("original", nil, true)}
	actions := ui.NewKeyActionsFromMap(original)
	original[ui.KeyA] = ui.NewKeyAction("changed", nil, false)
	actions.Add(ui.KeyB, ui.NewKeyAction("added", nil, true))
	action, ok := actions.Get(ui.KeyA)
	require.True(t, ok)
	assert.Equal(t, "original", action.Description)
	assert.NotContains(t, original, ui.KeyB)
	ui.NewKeyActionsFromMap(nil).Add(ui.KeyA, action)
}

func TestKeyActionsRangeSnapshotAllowsMutation(t *testing.T) {
	actions := ui.NewKeyActionsFromMap(ui.KeyMap{
		ui.KeyA: ui.NewKeyAction("a", nil, true),
		ui.KeyB: ui.NewKeyAction("b", nil, true),
	})
	finished := make(chan []tcell.Key, 1)
	go func() {
		var visited []tcell.Key
		actions.Range(func(key tcell.Key, action ui.KeyAction) {
			visited = append(visited, key)
			actions.Get(key)
			actions.Clear()
			actions.Add(ui.KeyC, action)
		})
		finished <- visited
	}()
	select {
	case visited := <-finished:
		assert.ElementsMatch(t, []tcell.Key{ui.KeyA, ui.KeyB}, visited)
		assert.Equal(t, 1, actions.Len())
	case <-time.After(2 * time.Second):
		t.Fatal("Range callback deadlocked while mutating actions")
	}
}

func TestKeyActionsSelfMergeAndReset(t *testing.T) {
	actions := ui.NewKeyActionsFromMap(ui.KeyMap{ui.KeyA: ui.NewKeyAction("a", nil, true)})
	actions.Merge(actions)
	actions.Set(actions)
	actions.Reset(actions)
	assert.Equal(t, 1, actions.Len())
	_, ok := actions.Get(ui.KeyA)
	assert.True(t, ok)
}

// Exercises the unsafe contracts that previously escaped the UI race suite:
// iteration overlapping writes and reading a merge source overlapping updates.
func TestKeyActionsConcurrentSnapshots(t *testing.T) {
	a := ui.NewKeyActionsFromMap(ui.KeyMap{ui.KeyA: ui.NewKeyAction("a", nil, true)})
	b := ui.NewKeyActionsFromMap(ui.KeyMap{ui.KeyB: ui.NewKeyAction("b", nil, true)})
	start := make(chan struct{})
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1000 {
				fn()
			}
		}()
	}
	run(func() {
		a.Add(ui.KeyC, ui.NewKeyAction("c", nil, true))
		a.Delete(ui.KeyC)
	})
	run(func() {
		a.Range(func(key tcell.Key, _ ui.KeyAction) { a.Get(key) })
	})
	run(func() { b.Merge(a) })
	run(func() { a.Merge(b) })
	run(func() { b.Set(a) })
	run(func() { b.Reset(a) })
	close(start)
	wg.Wait()
	_, ok := a.Get(ui.KeyA)
	assert.True(t, ok)
}
