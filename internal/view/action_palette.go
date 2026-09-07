// SPDX-License-Identifier: Apache-2.0
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

type paletteAction struct {
	key   tcell.Key
	label string
}
type actionPalette struct {
	*Picker
	owner       ResourceViewer
	app         *App
	path, query string
	contextName string
	entries     []paletteAction
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
	p.owner.Actions().Range(func(k tcell.Key, a ui.KeyAction) {
		if !a.Opts.Visible || a.Action == nil || (p.app.Config.IsReadOnly() && a.Opts.Dangerous) {
			return
		}
		category := "Action"
		if a.Opts.Plugin {
			category = "Plugin"
		} else if a.Opts.HotKey {
			category = "Shortcut"
		}
		if a.Opts.Dangerous {
			category += " / changes resource"
		}
		label := category + " | " + a.Description + "  (" + tcell.KeyNames[k] + ")"
		if actionMatches(label, p.query) {
			p.entries = append(p.entries, paletteAction{k, label})
		}
	})
	for _, entry := range []paletteAction{{9001, "Troubleshoot snapshot (:troubleshoot)"}, {9002, "TLS certificate inspection (:tls)"}} {
		if entry.key == 9002 && !tlsEntryResource(p.owner.GVR().R()) {
			continue
		}
		if actionMatches(entry.label, p.query) {
			p.entries = append(p.entries, entry)
		}
	}
	sort.Slice(p.entries, func(i, j int) bool { return p.entries[i].label < p.entries[j].label })
	for _, a := range p.entries {
		p.AddItem(tview.Escape(a.label), "", 0, nil)
	}
	p.SetTitle(" Actions | " + tview.Escape(p.path) + " | search: " + tview.Escape(p.query) + " ")
	if len(p.entries) == 0 {
		p.AddItem("No matching actions. Backspace to revise; Esc returns.", "", 0, nil)
	}
}
func (p *actionPalette) keyboard(e *tcell.EventKey) *tcell.EventKey {
	switch e.Key() {
	case tcell.KeyEscape:
		p.app.PrevCmd(e)
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
	key := p.entries[i].key
	p.app.PrevCmd(nil)
	if p.app.Config.ActiveContextName() != p.contextName || p.owner.GetTable().GetSelectedItem() != p.path {
		p.app.Flash().Err(fmt.Errorf("context or selection changed; reopen actions"))
		return
	}
	if key == 9001 {
		p.app.openInspection(p.owner, troubleshootCommand, p.path)
		return
	}
	if key == 9002 {
		p.app.openInspection(p.owner, tlsCommand, p.path)
		return
	}
	a, ok := p.owner.Actions().Get(key)
	if !ok || a.Action == nil || (p.app.Config.IsReadOnly() && a.Opts.Dangerous) {
		return
	}
	event := tcell.NewEventKey(key, 0, tcell.ModNone)
	if key >= ui.KeyShiftA && key <= ui.KeyShiftZ || key >= ui.KeyA && key <= ui.KeyZ {
		event = tcell.NewEventKey(tcell.KeyRune, rune(key), tcell.ModNone)
	}
	if a.Description == "Help" {
		p.app.helpCmd(event)
		return
	}
	a.Action(event)
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
