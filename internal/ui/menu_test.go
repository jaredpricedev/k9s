// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMenu(t *testing.T) {
	v := ui.NewMenu(config.NewStyles())
	v.HydrateMenu(model.MenuHints{
		{Mnemonic: "a", Description: "bleeA", Visible: true},
		{Mnemonic: "b", Description: "bleeB", Visible: true},
		{Mnemonic: "0", Description: "zero", Visible: true},
	})

	assert.Equal(t, " [#ff00ff:-:b]<0> [#ffffff:-:d]zero ", v.GetCell(0, 0).Text)
	assert.Equal(t, " [#1e90ff:-:b]<a> [#ffffff:-:d]bleeA ", v.GetCell(0, 1).Text)
	assert.Equal(t, " [#1e90ff:-:b]<b> [#ffffff:-:d]bleeB ", v.GetCell(1, 1).Text)
}

func TestMenuFitsDescriptionsWithoutLosingShortcutColumns(t *testing.T) {
	v := ui.NewMenu(config.NewStyles())
	var hints model.MenuHints
	for i := range 18 {
		hints = append(hints, model.MenuHint{Mnemonic: string(rune('a' + i)), Description: fmt.Sprintf("%02d Inspect certificate issuance and references", i), Visible: true})
	}
	hints[0].Mnemonic = "ctrl-shift-a"
	hints[6].Mnemonic = "ctrl-g"
	hints[12].Mnemonic = "ctrl-m"
	v.HydrateMenu(hints)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	t.Cleanup(screen.Fini)
	for _, width := range []int{100, 180, 100} {
		screen.SetSize(width, 6)
		v.SetRect(0, 0, width, 6)
		v.Draw(screen)
		line := menuScreenLine(screen, width)
		for _, key := range []string{"<ctrl-shift-a>", "<ctrl-g>", "<ctrl-m>"} {
			assert.Contains(t, line, key, "width %d", width)
			if index := strings.Index(line, key); index > 1 {
				assert.Equal(t, "  ", line[index-2:index], line)
			}
		}
		if width == 100 {
			assert.Contains(t, line, "…")
		} else {
			assert.Contains(t, line, hints[0].Description)
			assert.NotContains(t, line, "…")
		}
	}
	v.StackPopped(nil, nil)
	v.SetRect(0, 0, 120, 6)
	v.Draw(screen)
	assert.Empty(t, v.GetCell(0, 0).Text, "resizing an empty stack must not restore old hints")
}

func TestMenuDisplaysLiteralTagsAndUnicode(t *testing.T) {
	v := ui.NewMenu(config.NewStyles())
	v.HydrateMenu(model.MenuHints{{Mnemonic: "ctrl-i", Description: "[green] Inspect 日本語", Visible: true}})
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	t.Cleanup(screen.Fini)
	screen.SetSize(100, 6)
	v.SetRect(0, 0, 100, 6)
	v.Draw(screen)
	assert.Contains(t, menuScreenLine(screen, 100), "<ctrl-i> [green] Inspect")
	assert.Contains(t, v.GetCell(0, 0).Text, "日本語")
}

func menuScreenLine(screen tcell.Screen, width int) string {
	var line strings.Builder
	for x := 0; x < width; {
		r, combining, _, size := screen.GetContent(x, 0)
		line.WriteRune(r)
		for _, r := range combining {
			line.WriteRune(r)
		}
		x += max(size, 1)
	}
	return line.String()
}

func TestActionHints(t *testing.T) {
	uu := map[string]struct {
		aa *ui.KeyActions
		e  model.MenuHints
	}{
		"a": {
			aa: ui.NewKeyActionsFromMap(ui.KeyMap{
				ui.KeyB: ui.NewKeyAction("bleeB", nil, true),
				ui.KeyA: ui.NewKeyAction("bleeA", nil, true),
				ui.Key0: ui.NewKeyAction("zero", nil, true),
				ui.Key1: ui.NewKeyAction("one", nil, false),
			}),
			e: model.MenuHints{
				{Mnemonic: "0", Description: "zero", Visible: true},
				{Mnemonic: "1", Description: "one", Visible: false},
				{Mnemonic: "a", Description: "bleeA", Visible: true},
				{Mnemonic: "b", Description: "bleeB", Visible: true},
			},
		},
	}

	for k := range uu {
		u := uu[k]
		t.Run(k, func(t *testing.T) {
			assert.Equal(t, u.e, u.aa.Hints())
		})
	}
}
