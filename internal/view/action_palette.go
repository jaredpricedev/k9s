// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const actionsCommand = "actions"

// paletteAction keeps the complete searchable label apart from the aligned
// row that is drawn, so presentation never narrows what a query can match.
type paletteAction struct {
	action ui.ActionDescriptor
	label  string
}

const paletteUnavailable = "Unavailable"

type actionPalette struct {
	*Picker
	owner           actionOwner
	app             *App
	path, query     string
	contextName     string
	target          SelectedResourceTarget
	entries         []paletteAction
	total, width    int
	originalCapture func(*tcell.EventKey) *tcell.EventKey
}

func (p *actionPalette) Init(ctx context.Context) error {
	p.contextName = p.app.Config.ActiveContextName()
	if err := p.Picker.Init(ctx); err != nil {
		return err
	}
	p.SetInputCapture(p.keyboard)
	p.SetSelectedFunc(func(i int, _, _ string, _ rune) { p.invoke(i) })
	p.refresh()
	return nil
}
func (*actionPalette) Name() string { return actionsCommand }
func (*actionPalette) Hints() model.MenuHints {
	return model.MenuHints{
		{Mnemonic: "type", Description: "Search", Visible: true},
		{Mnemonic: "enter", Description: "Run action", Visible: true},
		{Mnemonic: "esc", Description: "Back", Visible: true},
	}
}
func (p *actionPalette) refresh() {
	p.Clear()
	p.entries = nil
	p.total = 0
	catalog := actionCatalog(p.owner, p.app)
	named := make(map[string]bool, len(catalog))
	for _, action := range catalog {
		if action.Discoverable && action.Shortcut != "" {
			named[action.ID+"\x00"+action.Label] = true
		}
	}
	for _, action := range catalog {
		// A binding without a printable key adds nothing beside its named twin.
		if !action.Discoverable || (action.Shortcut == "" && named[action.ID+"\x00"+action.Label]) {
			continue
		}
		p.total++
		label := action.Category + " | " + action.Label + "  (" + action.Shortcut + ")"
		if action.UnavailableReason != "" {
			label += " · " + action.UnavailableReason
		}
		if actionMatches(label, p.query) {
			p.entries = append(p.entries, paletteAction{action: action, label: label})
		}
	}
	// What can run now comes first: direct keys, then commands. Explanations
	// for unavailable actions follow instead of interrupting the list.
	sort.SliceStable(p.entries, func(i, j int) bool {
		a, b := p.entries[i].action, p.entries[j].action
		if (a.UnavailableReason == "") != (b.UnavailableReason == "") {
			return a.UnavailableReason == ""
		}
		if a.Category != b.Category {
			return ui.ActionCategoryOrder(a.Category) < ui.ActionCategoryOrder(b.Category)
		}
		if ac, bc := strings.HasPrefix(a.Shortcut, ":"), strings.HasPrefix(b.Shortcut, ":"); ac != bc {
			return bc
		}
		return false
	})
	for _, row := range p.rows() {
		p.AddItem(row, "", 0, nil)
	}
	identity := p.path
	if p.target.Err() != nil {
		identity = p.owner.Name()
		if _, resources := p.owner.(ResourceViewer); resources {
			identity += " · " + p.target.UnavailableReason
		}
	}
	search := "type to search"
	if p.query != "" {
		search = fmt.Sprintf("search: %s · %d of %d", p.query, len(p.entries), p.total)
	}
	p.SetTitle(" Actions · " + tview.Escape(identity) + " · " + tview.Escape(search) + " ")
	if len(p.entries) == 0 {
		p.AddItem("No matching actions. Backspace to revise; Esc returns.", "", 0, nil)
	}
}

// rows aligns group, action and shortcut columns. The group is printed once
// per run; unavailable actions are muted and carry their reason.
func (p *actionPalette) rows() []string {
	width := p.width
	if width <= 0 {
		width = 80
	}
	groupWidth := len(paletteUnavailable) + 2
	if width < 64 {
		groupWidth = 0
	}
	labelWidth, keyWidth := 0, 0
	for _, e := range p.entries {
		labelWidth = max(labelWidth, tview.TaggedStringWidth(e.action.Label))
		keyWidth = max(keyWidth, tview.TaggedStringWidth(e.action.Shortcut))
	}
	keyWidth = min(keyWidth, 20)
	labelWidth = max(8, min(labelWidth, 40, width-groupWidth-keyWidth-4))
	reasonWidth := width - groupWidth - labelWidth - keyWidth - 5
	muted, group, key := "[-::]", "[-::]", "[-::]"
	if p.app != nil && p.app.Styles != nil {
		palette := p.app.Styles.Semantic()
		muted, group, key = colorTag(palette.Muted.Color()), colorTag(palette.Category.Color()), colorTag(palette.Focus.Color())
	}
	rows := make([]string, 0, len(p.entries))
	previous := ""
	for _, e := range p.entries {
		name := e.action.Category
		if e.action.UnavailableReason != "" {
			name = paletteUnavailable
		}
		shown := ""
		if name != previous {
			shown, previous = name, name
		}
		var row strings.Builder
		row.WriteByte(' ')
		if groupWidth > 0 {
			color := group
			if name == paletteUnavailable {
				color = muted
			}
			row.WriteString(color + padCells(shown, groupWidth) + "[-::]")
		}
		text := e.action.Label
		if groupWidth == 0 && e.action.UnavailableReason != "" {
			// Without the group column, muted color alone would carry the state.
			text = "x " + text
		}
		label := tview.Escape(padCells(ui.Truncate(text, labelWidth), labelWidth))
		shortcut := tview.Escape(padCells(e.action.Shortcut, keyWidth))
		if e.action.UnavailableReason == "" {
			row.WriteString(label + "  " + key + shortcut + "[-::]")
		} else {
			// The complete reason stays searchable, appears on Enter and in Help.
			reason := ""
			if reasonWidth >= 12 {
				reason = "  " + tview.Escape(ui.Truncate(e.action.UnavailableReason, reasonWidth))
			}
			row.WriteString(muted + label + "  " + shortcut + reason + "[-::]")
		}
		rows = append(rows, row.String())
	}
	return rows
}

// Draw re-aligns the columns after a resize without moving the selection.
func (p *actionPalette) Draw(screen tcell.Screen) {
	if _, _, width, _ := p.GetInnerRect(); width != p.width {
		current := p.GetCurrentItem()
		p.width = width
		p.refresh()
		if current < p.GetItemCount() {
			p.SetCurrentItem(current)
		}
	}
	p.Picker.Draw(screen)
}

func colorTag(c tcell.Color) string {
	if c == tcell.ColorDefault {
		return "[-::]"
	}
	return fmt.Sprintf("[#%06x::]", c.Hex())
}

func padCells(s string, width int) string {
	if gap := width - tview.TaggedStringWidth(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func (p *actionPalette) keyboard(e *tcell.EventKey) *tcell.EventKey {
	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlO:
		p.close()
		return nil
	case tcell.KeyEnter:
		p.invoke(p.GetCurrentItem())
		return nil
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		r := []rune(p.query)
		if len(r) > 0 {
			p.query = string(r[:len(r)-1])
		}
		p.refresh()
		return nil
	case tcell.KeyRune:
		if len([]rune(p.query)) < 100 {
			p.query += string(e.Rune())
			p.refresh()
		}
		return nil
	}
	return e
}
func (p *actionPalette) invoke(i int) {
	if i < 0 || i >= len(p.entries) {
		return
	}
	if p.app.Content.Top() != p.owner || p.app.Config.ActiveContextName() != p.contextName {
		p.close()
		p.app.Flash().Warn("View or context changed; reopen actions")
		return
	}
	action := p.entries[i].action
	// Recompute availability by stable ID before executing. A disabled entry is
	// explanatory and keeps the palette open; it can never run its handler.
	found := false
	for _, current := range actionCatalog(p.owner, p.app) {
		if current.ID == action.ID && current.Shortcut == action.Shortcut {
			action, found = current, true
			break
		}
	}
	if !found {
		p.app.Flash().Warn("Action no longer available; reopen actions")
		return
	}
	if !action.Available() {
		p.app.Flash().Warn(action.UnavailableReason)
		return
	}
	if action.RequiresSelection {
		current := actionTarget(p.owner, p.app.Config.ActiveContextName())
		if p.app.Config.ActiveContextName() != p.contextName || current.GVR != p.target.GVR ||
			current.Path() != p.target.Path() || (p.target.UID != "" && current.UID != p.target.UID) ||
			current.UnavailableReason != p.target.UnavailableReason {
			p.app.Flash().Err(fmt.Errorf("context or selection changed; reopen actions"))
			return
		}
	}
	p.close()
	event := actionEvent(action.Key)
	action.Handler(event)
}

func actionMatches(label, query string) bool {
	label = strings.ToLower(label)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(label, word) {
			return false
		}
	}
	return true
}

func actionEvent(key tcell.Key) *tcell.EventKey {
	if key >= 32 && key <= 126 {
		return tcell.NewEventKey(tcell.KeyRune, rune(key), tcell.ModNone)
	}
	return tcell.NewEventKey(key, 0, tcell.ModNone)
}
