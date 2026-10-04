// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/tview"
)

// Pages represents a stack of view pages.
type Pages struct {
	*tview.Pages
	*model.Stack
	cleanupMu sync.Mutex
	cleanups  map[string]func()
}

// SetPageCleanup ties transient widget resources to page replacement/removal.
func (p *Pages) SetPageCleanup(name string, cleanup func()) {
	p.cleanupMu.Lock()
	previous := p.cleanups[name]
	if p.cleanups == nil {
		p.cleanups = make(map[string]func())
	}
	p.cleanups[name] = cleanup
	p.cleanupMu.Unlock()
	if previous != nil {
		previous()
	}
}

func (p *Pages) cleanupPage(name string) {
	p.cleanupMu.Lock()
	cleanup := p.cleanups[name]
	delete(p.cleanups, name)
	p.cleanupMu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

// AddPage releases resources owned by a replaced transient page.
func (p *Pages) AddPage(name string, item tview.Primitive, resize, visible bool) *tview.Pages {
	p.cleanupPage(name)
	return p.Pages.AddPage(name, item, resize, visible)
}

// RemovePage releases resources before removing a page.
func (p *Pages) RemovePage(name string) *tview.Pages {
	p.cleanupPage(name)
	return p.Pages.RemovePage(name)
}

// ClearPageResources dismisses transient pages that own registered resources.
func (p *Pages) ClearPageResources() {
	p.cleanupMu.Lock()
	names := make([]string, 0, len(p.cleanups))
	for name := range p.cleanups {
		names = append(names, name)
	}
	p.cleanupMu.Unlock()
	for _, name := range names {
		p.RemovePage(name)
	}
}

// Clear releases transient resources when the component stack is cleared.
func (p *Pages) Clear() {
	p.ClearPageResources()
	p.Stack.Clear()
}

// NewPages return a new view.
func NewPages() *Pages {
	p := Pages{
		Pages: tview.NewPages(),
		Stack: model.NewStack(),
	}
	p.AddListener(&p)

	return &p
}

// IsTopDialog checks if front page is a dialog.
func (p *Pages) IsTopDialog() bool {
	_, pa := p.GetFrontPage()
	if _, ok := pa.(interface{ IsDialog() bool }); ok {
		return pa.(interface{ IsDialog() bool }).IsDialog()
	}
	switch pa.(type) {
	case *tview.ModalForm, *ModalForm, *ModalList, *MessageModal:
		return true
	default:
		return false
	}
}

// Show displays a given page.
func (p *Pages) Show(c model.Component) {
	p.SwitchToPage(componentID(c))
}

// Current returns the current component.
func (p *Pages) Current() model.Component {
	c := p.CurrentPage()
	if c == nil {
		return nil
	}

	return c.Item.(model.Component)
}

// AddAndShow adds a new page and bring it to front.
func (p *Pages) addAndShow(c model.Component) {
	p.add(c)
	p.Show(c)
}

// Add adds a new page.
func (p *Pages) add(c model.Component) {
	p.AddPage(componentID(c), c, true, true)
}

// Delete removes a page.
func (p *Pages) delete(c model.Component) {
	p.RemovePage(componentID(c))
}

// Dump for debug.
func (p *Pages) Dump() {
	slog.Debug("Dumping Pages", slogs.Page, p)
	for i, c := range p.Peek() {
		slog.Debug(fmt.Sprintf("%d -- %s -- %#v", i, componentID(c), p.GetPrimitive(componentID(c))))
	}
}

// Stack Protocol...

// StackPushed notifies a new component was pushed.
func (p *Pages) StackPushed(c model.Component) {
	p.addAndShow(c)
}

// StackPopped notifies a component was removed.
func (p *Pages) StackPopped(o, _ model.Component) {
	p.ClearPageResources()
	p.delete(o)
}

// StackTop notifies a new component is at the top of the stack.
func (p *Pages) StackTop(top model.Component) {
	if top == nil {
		return
	}
	p.Show(top)
}

// Helpers...

func componentID(c model.Component) string {
	if c.Name() == "" {
		slog.Error("Component has no name", slogs.Component, fmt.Sprintf("%T", c))
	}
	return fmt.Sprintf("%s-%p", c.Name(), c)
}
