// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModalListFitsViewportAndKeepsLastItemSelectable(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	t.Cleanup(screen.Fini)
	for _, size := range []struct{ width, height int }{{80, 24}, {40, 12}, {8, 4}} {
		screen.SetSize(size.width, size.height)
		list := tview.NewList().ShowSecondaryText(false)
		for i := range 35 {
			list.AddItem(fmt.Sprintf("%02d-%s", i, strings.Repeat("long resource name ", 8)), "", 0, nil)
		}
		modal := NewModalList("<Resource selection>", list)
		selected := -1
		modal.SetDoneFunc(func(index int, _ string) { selected = index })
		list.SetCurrentItem(34)
		modal.Draw(screen)
		x, y, w, h := modal.GetRect()
		assert.GreaterOrEqual(t, x, 0)
		assert.GreaterOrEqual(t, y, 0)
		assert.LessOrEqual(t, x+w, size.width)
		assert.LessOrEqual(t, y+h, size.height)
		if size.height > 4 {
			list.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
			assert.Equal(t, 34, selected)
		}
	}
}

func TestModalListMeasuresVisibleTextAndTitle(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	t.Cleanup(screen.Fini)
	screen.SetSize(100, 25)
	list := tview.NewList().ShowSecondaryText(false).AddItem("[red::b]资源[-::-]", "", 0, nil)
	modal := NewModalList("<Choose a resource>", list)
	modal.Draw(screen)
	_, _, width, _ := modal.GetRect()
	assert.Equal(t, tview.TaggedStringWidth("<Choose a resource>")+2, width)
}
