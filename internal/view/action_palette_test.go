// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionPaletteListsRunnableActionsBeforeExplanations(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	app.Config.K9s.ReadOnly = true
	owner := &discoveryOwner{Details: NewDetails(app, "test", "", contentTXT, false)}
	noop := func(*tcell.EventKey) *tcell.EventKey { return nil }
	owner.actions.Add(tcell.KeyCtrlD, ui.NewKeyActionWithOpts("Delete", noop, ui.ActionOpts{Dangerous: true}))
	owner.actions.Add(ui.KeyY, ui.NewKeyAction("YAML", noop, true))
	app.Content.Push(owner)
	app.actionsCmd(nil)
	palette := app.Content.GetPrimitive(actionsCommand).(*actionPalette)

	unavailable, commands := false, false
	seen := map[string]bool{}
	for _, entry := range palette.entries {
		action := entry.action
		if action.UnavailableReason != "" {
			unavailable = true
		} else {
			assert.False(t, unavailable, "%s is runnable but follows an unavailable action", action.Label)
		}
		assert.False(t, action.Shortcut == "" && seen[action.Label], "%s repeats without a key", action.Label)
		seen[action.Label] = true
		commands = commands || strings.HasPrefix(action.Shortcut, ":")
	}
	require.True(t, unavailable, "fixture needs a disabled action")
	require.True(t, commands, "fixture needs typed commands")

	rows := palette.rows()
	require.Len(t, rows, len(palette.entries))
	groups := 0
	for index, row := range rows {
		assert.NotContains(t, row, " | ", "the group is a column, not a prefix on every row")
		if strings.Contains(row, palette.entries[index].action.Category) || strings.Contains(row, paletteUnavailable) {
			groups++
		}
	}
	assert.Less(t, groups, len(rows), "a group name is printed once per run")

	palette.width = 50
	last := palette.rows()[len(palette.entries)-1]
	assert.Contains(t, last, "x ", "narrow rows mark unavailable actions without relying on color")
}

func TestHelpWrapBreaksBetweenWords(t *testing.T) {
	lines := helpWrap("custom action [red] retains literal labels and an intentionally long description", 20)
	for _, line := range lines {
		assert.LessOrEqual(t, len(line), 22, line)
	}
	joined := strings.Join(lines, " ")
	for _, word := range []string{"intentionally", "description", "retains"} {
		assert.Contains(t, joined, word, "words stay whole")
	}
	assert.Equal(t, []string{"abcde", "fgh"}, helpWrap("abcdefgh", 5), "only an over-wide word is split")
	assert.Equal(t, []string{""}, helpWrap("", 10))
}
