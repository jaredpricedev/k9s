// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"slices"
	"time"

	"github.com/derailed/k9s/internal/workspace"
)

func dailyWorkspaceScopeIn(store workspace.Store, name string) (workspace.Scope, bool) {
	for i := range store.Scopes {
		scope := &store.Scopes[i]
		if scope.Name == name {
			return *scope, true
		}
	}
	return workspace.Scope{}, false
}
func dailyWorkspaceSameBinding(a, b *workspace.Scope) bool {
	return a.Name == b.Name && a.Context == b.Context && a.LabelSelector == b.LabelSelector && slices.Equal(a.Namespaces, b.Namespaces) && slices.Equal(a.Kinds, b.Kinds)
}

// Reload metadata when returning from another view. A changed or removed scope
// invalidates its observation; updating pins/searches/layout retains evidence.
func (w *dailyWorkspace) syncStore() error {
	latest, err := workspace.LoadStore(w.path)
	if err != nil {
		return err
	}
	w.store = latest
	if w.scope.Name == "" {
		return nil
	}
	scope, found := dailyWorkspaceScopeIn(latest, w.scope.Name)
	if !found {
		w.invalidateScope(workspace.Scope{}, "This saved scope was removed while away. Choose or create a scope.")
		return nil
	}
	if !dailyWorkspaceSameBinding(&w.scope, &scope) {
		w.invalidateScope(scope, "This scope changed while away. Select it again to observe its revised context and namespaces.")
		return nil
	}
	w.scope = scope
	return nil
}

//nolint:gocritic // Retain the new scope by value when retiring a prior observation.
func (w *dailyWorkspace) invalidateScope(scope workspace.Scope, message string) {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.generation++
	w.scope = scope
	w.snapshot = workspace.Snapshot{}
	w.resetObservationWindow(time.Now())
	w.coverage = nil
	w.rows = nil
	w.reader = nil
	w.query = ""
	w.tabQueries = make(map[string]string)
	w.tabSelections = make(map[string]string)
	w.table.Clear()
	w.table.Select(1, 0)
	w.mode = dailyWorkspaceScopesMode
	w.notice = message
}

// updateScope patches only the requested metadata into a freshly loaded store.
// Covered views cannot recreate removed scopes or overwrite scope identity edits.
func (w *dailyWorkspace) updateScope(change func(*workspace.Scope) error) error {
	latest, err := workspace.LoadStore(w.path)
	if err != nil {
		return err
	}
	scope, found := dailyWorkspaceScopeIn(latest, w.scope.Name)
	if !found {
		w.store = latest
		w.invalidateScope(workspace.Scope{}, "This saved scope was removed. Choose or create a scope.")
		w.render()
		return fmt.Errorf("saved workspace was removed; reopen it")
	}
	if !dailyWorkspaceSameBinding(&w.scope, &scope) {
		w.store = latest
		w.invalidateScope(scope, "This saved scope changed. Select it again before editing metadata.")
		w.render()
		return fmt.Errorf("saved workspace context or namespace scope changed; reopen it")
	}
	if changeErr := change(&scope); changeErr != nil {
		return changeErr
	}
	normalized, err := workspace.NormalizeScope(scope)
	if err != nil {
		return err
	}
	for i := range latest.Scopes {
		current := &latest.Scopes[i]
		if current.Name == normalized.Name {
			latest.Scopes[i] = normalized
			break
		}
	}
	if err := workspace.SaveStore(w.path, latest); err != nil {
		return err
	}
	w.store = latest
	w.scope = normalized
	return nil
}

// persistScope supports callers that supply a metadata copy: only modified
// fields are patched, and concurrent changes to the same list cause a conflict.
//
//nolint:gocritic // Capture the caller's intended metadata value for conflict checking.
func (w *dailyWorkspace) persistScope(scope workspace.Scope) error {
	base := w.scope
	if !dailyWorkspaceSameBinding(&base, &scope) {
		return fmt.Errorf("workspace context or namespace edits require the edit form")
	}
	return w.updateScope(func(latest *workspace.Scope) error {
		if scope.Layout != base.Layout {
			latest.Layout = scope.Layout
		}
		if !slices.Equal(scope.Pins, base.Pins) {
			if !slices.Equal(latest.Pins, base.Pins) {
				return fmt.Errorf("pins changed while away; reopen workspace")
			}
			latest.Pins = slices.Clone(scope.Pins)
		}
		if !slices.Equal(scope.Searches, base.Searches) {
			if !slices.Equal(latest.Searches, base.Searches) {
				return fmt.Errorf("searches changed while away; reopen workspace")
			}
			latest.Searches = slices.Clone(scope.Searches)
		}
		return nil
	})
}
