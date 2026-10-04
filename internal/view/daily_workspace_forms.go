// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

func (w *dailyWorkspace) form(title, message string, build func(*tview.Form, func())) {
	w.formOpen = true
	styles := w.app.Styles.Dialog()
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	form.SetBorderPadding(0, 0, 0, 0)
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).
		SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).
		SetFieldTextColor(styles.FieldFgColor.Color()).
		SetFieldBackgroundColor(styles.BgColor.Color())
	dismiss := func() {
		w.formOpen = false
		w.app.Content.Pages.RemovePage(dailyWorkspaceFormPage)
		w.app.SetFocus(w.table)
	}
	build(form, dismiss)
	form.SetCancelFunc(dismiss)
	frame := tview.NewFrame(form).SetBorders(0, 0, 1, 0, 0, 0)
	frame.SetBorder(true).SetBorderPadding(1, 1, 1, 1).SetTitle(title).SetTitleColor(w.app.Styles.Semantic().Focus.Color()).SetBackgroundColor(styles.BgColor.Color())
	form.SetBackgroundColor(styles.BgColor.Color())
	modal := &dailyWorkspaceModal{Frame: frame, form: form, message: tview.Escape(message), color: styles.FgColor.Color()}
	w.app.Content.Pages.AddPage(dailyWorkspaceFormPage, modal, false, true)
	w.app.SetFocus(modal)
}

// The shared ModalForm fixes its width at one third of the terminal. Workspace
// fields need room for namespace lists and selectors, so size this overlay to
// the available terminal instead. Frame delegates keyboard focus to the Form.
type dailyWorkspaceModal struct {
	*tview.Frame
	form    *tview.Form
	message string
	color   tcell.Color
}

func (m *dailyWorkspaceModal) Draw(screen tcell.Screen) {
	columns, rows := screen.Size()
	width := max(1, min(96, columns-4))
	lines := tview.WordWrap(m.message, max(1, width-4))
	m.Frame.Clear()
	for _, line := range lines {
		m.Frame.AddText(line, true, tview.AlignLeft, m.color)
	}
	height := min(max(1, rows-2), len(lines)+m.form.GetFormItemCount()+10)
	m.Frame.SetRect(max(0, (columns-width)/2), max(0, (rows-height)/2), width, height)
	m.Frame.Draw(screen)
}

func (w *dailyWorkspace) editingScope() workspace.Scope {
	if w.mode == dailyWorkspaceScopesMode {
		row, _ := w.table.GetSelection()
		if row > 0 && row <= len(w.rows) {
			for i := range w.store.Scopes {
				scope := &w.store.Scopes[i]
				if scope.Name == w.rows[row-1].scopeName {
					return *scope
				}
			}
		}
	}
	return w.scope
}
func (w *dailyWorkspace) scopeForm(edit bool) {
	scope := workspace.Scope{Context: w.app.Config.ActiveContextName(), Layout: dailyWorkspaceQueueMode}
	title := "Create workspace"
	if edit {
		scope = w.editingScope()
		if scope.Name == "" {
			w.app.Flash().Warn("Select a saved scope to edit")
			return
		}
		title = "Edit workspace"
	}
	oldName := scope.Name
	name, contextName, namespace, selector, kinds := scope.Name, scope.Context, strings.Join(scope.Namespaces, ","), scope.LabelSelector, strings.Join(scope.Kinds, ",")
	if !edit {
		namespace = w.app.Config.ActiveNamespace()
		if client.IsAllNamespaces(namespace) || client.IsClusterScoped(namespace) {
			namespace = ""
		}
	}
	message := "Saved locally. Pick explicit namespaces (maximum 16). Empty kinds use workload defaults. " +
		"Enter/Tab moves through fields. Saving never switches your Kubernetes context."
	w.form(title, message, func(form *tview.Form, dismiss func()) {
		form.AddInputField("Name", name, 36, nil, func(value string) { name = value })
		form.AddInputField("Context", contextName, 36, nil, func(value string) { contextName = value })
		form.AddInputField("Namespaces (comma-separated)", namespace, 48, nil, func(value string) { namespace = value })
		form.AddInputField("Label selector (optional)", selector, 48, nil, func(value string) { selector = value })
		form.AddInputField("Kinds (optional, comma-separated)", kinds, 48, nil, func(value string) { kinds = value })
		form.AddButton("Cancel", dismiss).AddButton("Save workspace", func() {
			latest, err := workspace.LoadStore(w.path)
			if err != nil {
				w.app.Flash().Err(err)
				return
			}
			updated := scope
			if edit {
				current, found := dailyWorkspaceScopeIn(latest, oldName)
				if !found || !dailyWorkspaceSameBinding(&scope, &current) {
					w.app.Flash().Warn("Saved scope changed or was removed; reopen the edit form")
					return
				}
				updated = current
			}
			updated.Name = name
			updated.Context = contextName
			updated.Namespaces = strings.Split(namespace, ",")
			updated.LabelSelector = selector
			updated.Kinds = nil
			if strings.TrimSpace(kinds) != "" {
				updated.Kinds = strings.Split(kinds, ",")
			}
			normalized, err := workspace.NormalizeScope(updated)
			if err != nil {
				w.app.Flash().Err(err)
				return
			}
			candidate := latest
			candidate.Scopes = slices.Clone(latest.Scopes)
			found := false
			for i := range candidate.Scopes {
				old := &candidate.Scopes[i]
				if old.Name == oldName && oldName != "" {
					candidate.Scopes[i] = normalized
					found = true
					break
				}
			}
			if !found {
				candidate.Scopes = append(candidate.Scopes, normalized)
			}
			if candidate.Active == oldName || candidate.Active == "" {
				candidate.Active = normalized.Name
			}
			if err := workspace.SaveStore(w.path, candidate); err != nil {
				w.app.Flash().Err(err)
				return
			}
			w.store = candidate
			dismiss()
			// Reenter through the context check; changing namespaces clears the old
			// observation so retained evidence never masquerades as the revised scope.
			if oldName == w.scope.Name || candidate.Active == normalized.Name {
				w.acceptEditedScope(normalized)
			} else {
				w.render()
			}
		})
	})
}
func (w *dailyWorkspace) searchForm() {
	if w.scope.Name == "" {
		w.app.Flash().Warn("Open a saved scope before saving a search")
		return
	}
	name, query := "", w.query
	message := "Searches filter retained rows only. Use space-separated literal tokens; " +
		"kind:, ns:, name: and status: are optional fields. Reusing a name updates its query."
	w.form("Save inventory search", message, func(form *tview.Form, dismiss func()) {
		form.AddInputField("Name", name, 36, nil, func(value string) { name = value })
		form.AddInputField("Query", query, 56, nil, func(value string) { query = value })
		form.AddButton("Cancel", dismiss).AddButton("Save search", func() {
			if _, err := parseDailyWorkspaceQuery(query); err != nil {
				w.app.Flash().Err(err)
				return
			}
			err := w.updateScope(func(scope *workspace.Scope) error {
				scope.Searches = slices.Clone(scope.Searches)
				found := false
				search := workspace.SavedSearch{Name: strings.TrimSpace(name), Query: query}
				for i, current := range scope.Searches {
					if current.Name == search.Name {
						scope.Searches[i] = search
						found = true
						break
					}
				}
				if !found {
					scope.Searches = append(scope.Searches, search)
				}
				return nil
			})
			if err != nil {
				w.app.Flash().Err(err)
				return
			}
			dismiss()
			w.app.Flash().Info("Search saved; S opens saved searches")
		})
	})
}
func (w *dailyWorkspace) savedSearchForm() {
	if len(w.scope.Searches) == 0 {
		w.app.Flash().Warn("No saved searches. Press s to save one")
		return
	}
	names := make([]string, len(w.scope.Searches))
	for i, search := range w.scope.Searches {
		names[i] = search.Name
	}
	index := 0
	message := "Applies the saved literal query to the Inventory tab. It keeps the same context, namespaces and label selector."
	w.form("Saved searches", message, func(form *tview.Form, dismiss func()) {
		form.AddDropDown(dailyWorkspaceSearchLabel, names, 0, func(_ string, selected int) { index = selected })
		form.AddButton("Cancel", dismiss).AddButton("Open inventory", func() {
			query := w.scope.Searches[index].Query
			if _, err := parseDailyWorkspaceQuery(query); err != nil {
				w.app.Flash().Err(err)
				return
			}
			dismiss()
			w.setMode(inventoryCommand)
			w.applyQuery(query)
		})
	})
}
func (w *dailyWorkspace) deleteScopeForm() {
	scope := w.editingScope()
	if scope.Name == "" {
		w.app.Flash().Warn("Select a saved scope first")
		return
	}
	message := fmt.Sprintf("Removes local workspace %q, its pins and searches. Kubernetes resources are unaffected.", scope.Name)
	w.form("Remove saved scope", message, func(form *tview.Form, dismiss func()) {
		form.AddButton("Cancel", dismiss).AddButton("Remove local scope", func() {
			latest, err := workspace.LoadStore(w.path)
			if err != nil {
				w.app.Flash().Err(err)
				return
			}
			current, found := dailyWorkspaceScopeIn(latest, scope.Name)
			if !found || !dailyWorkspaceSameBinding(&scope, &current) {
				w.app.Flash().Warn("Saved scope changed or was removed; reopen this view")
				return
			}
			candidate := latest
			candidate.Scopes = slices.Clone(latest.Scopes)
			candidate.Scopes = slices.DeleteFunc(candidate.Scopes, func(value workspace.Scope) bool { return value.Name == scope.Name })
			if candidate.Active == scope.Name {
				candidate.Active = ""
			}
			if err := workspace.SaveStore(w.path, candidate); err != nil {
				w.app.Flash().Err(err)
				return
			}
			w.store = candidate
			if w.scope.Name == scope.Name {
				if w.cancel != nil {
					w.cancel()
				}
				w.generation++
				w.scope = workspace.Scope{}
				w.snapshot = workspace.Snapshot{}
				w.coverage = nil
			}
			dismiss()
			w.mode = dailyWorkspaceScopesMode
			w.render()
		})
	})
}
