// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const actionsCommand = "actions"

type paletteAction struct {
	action ui.ActionDescriptor
	label  string
}
type actionPalette struct {
	*Picker
	owner           actionOwner
	app             *App
	path, query     string
	contextName     string
	target          SelectedResourceTarget
	entries         []paletteAction
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
	for _, action := range actionCatalog(p.owner, p.app) {
		if !action.Discoverable {
			continue
		}
		label := action.Category + " | " + action.Label + "  (" + action.Shortcut + ")"
		if action.UnavailableReason != "" {
			label += " · " + action.UnavailableReason
		}
		if actionMatches(label, p.query) {
			p.entries = append(p.entries, paletteAction{action: action, label: label})
		}
	}
	for _, a := range p.entries {
		p.AddItem(tview.Escape(a.label), "", 0, nil)
	}
	identity := p.path
	if p.target.Err() != nil {
		identity = p.owner.Name()
		if _, resources := p.owner.(ResourceViewer); resources {
			identity += " | " + p.target.UnavailableReason
		}
	}
	p.SetTitle(" Actions | " + tview.Escape(identity) + " | search: " + tview.Escape(p.query) + " ")
	if len(p.entries) == 0 {
		p.AddItem("No matching actions. Backspace to revise; Esc returns.", "", 0, nil)
	}
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
