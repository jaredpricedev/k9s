// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/workspace"
)

// dailyWorkspaceCommand accepts the original command line so saving a scope
// cannot accidentally pass through normal resource/namespace command parsing.
func (c *Command) dailyWorkspaceCommand(line string) {
	store, err := workspace.LoadStore(dailyWorkspacePath())
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	line = strings.TrimPrefix(strings.TrimSpace(line), ":")
	words := strings.Fields(line)
	if len(words) == 0 {
		return
	}
	open := func(mode, query string) {
		if _, err := parseDailyWorkspaceQuery(query); err != nil {
			c.app.Flash().Err(err)
			return
		}
		view := newDailyWorkspace(c.app, store, mode, query)
		if err := c.app.inject(view, false); err != nil {
			c.app.Flash().Err(err)
		}
	}
	switch words[0] {
	case dailyCommand:
		if len(words) != 1 {
			c.app.Flash().Warn("Use :daily")
			return
		}
		open(dailyWorkspaceQueueMode, "")
		return
	case inventoryCommand:
		open(inventoryCommand, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), words[0])))
		return
	}
	if len(words) == 1 {
		mode := dailyWorkspaceScopesMode
		if store.Active != "" {
			mode = dailyWorkspaceQueueMode
			for i := range store.Scopes {
				scope := &store.Scopes[i]
				if scope.Name == store.Active && scope.Layout != "" {
					mode = scope.Layout
				}
			}
		}
		open(mode, "")
		return
	}
	switch words[1] {
	case "save":
		c.dailyWorkspaceSave(&store, words, open)
	case "use":
		c.dailyWorkspaceUse(&store, words, open)
	case dailyWorkspaceDeleteToken:
		c.dailyWorkspaceDelete(&store, words, open)
	case "pin":
		c.dailyWorkspacePin(&store, words)
	case "search":
		c.dailyWorkspaceSearch(&store, words)
	default:
		c.app.Flash().Err(fmt.Errorf("use :workspace, or workspace save/use/delete/pin/search"))
	}
}

func (c *Command) dailyWorkspaceSave(store *workspace.Store, words []string, open func(string, string)) {
	if len(words) < 4 {
		c.app.Flash().Warn("Use :workspace save NAME ns1,ns2 [--selector=app=example] [--kinds=pods,deployments]")
		return
	}
	scope := workspace.Scope{Name: words[2], Context: c.app.Config.ActiveContextName(), Namespaces: strings.Split(words[3], ","), Layout: dailyWorkspaceQueueMode}
	for _, flag := range words[4:] {
		switch {
		case strings.HasPrefix(flag, "--selector="):
			scope.LabelSelector = strings.TrimPrefix(flag, "--selector=")
		case strings.HasPrefix(flag, "--kinds="):
			scope.Kinds = strings.Split(strings.TrimPrefix(flag, "--kinds="), ",")
		default:
			c.app.Flash().Errf("Unknown workspace option %q", flag)
			return
		}
	}
	normalized, err := workspace.NormalizeScope(scope)
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	found := false
	for i := range store.Scopes {
		old := &store.Scopes[i]
		if old.Name != normalized.Name {
			continue
		}
		normalized.Pins = old.Pins
		normalized.Searches = old.Searches
		store.Scopes[i] = normalized
		found = true
		break
	}
	if !found {
		store.Scopes = append(store.Scopes, normalized)
	}
	store.Active = normalized.Name
	if err := workspace.SaveStore(dailyWorkspacePath(), *store); err != nil {
		c.app.Flash().Err(err)
		return
	}
	open(dailyWorkspaceQueueMode, "")
}

func (c *Command) dailyWorkspaceUse(store *workspace.Store, words []string, open func(string, string)) {
	if len(words) != 3 {
		c.app.Flash().Warn("Use :workspace use NAME")
		return
	}
	for i := range store.Scopes {
		scope := &store.Scopes[i]
		if scope.Name != words[2] {
			continue
		}
		if scope.Context != c.app.Config.ActiveContextName() {
			c.app.Flash().Errf("Scope %s belongs to context %s; switch explicitly with :ctx, then use this scope", scope.Name, scope.Context)
			return
		}
		store.Active = scope.Name
		if err := workspace.SaveStore(dailyWorkspacePath(), *store); err != nil {
			c.app.Flash().Err(err)
			return
		}
		mode := scope.Layout
		if mode == "" {
			mode = dailyWorkspaceQueueMode
		}
		open(mode, "")
		return
	}
	c.app.Flash().Errf("Unknown workspace %q", words[2])
}

func (c *Command) dailyWorkspaceDelete(store *workspace.Store, words []string, open func(string, string)) {
	if len(words) != 3 {
		c.app.Flash().Warn("Use :workspace delete NAME")
		return
	}
	before := len(store.Scopes)
	store.Scopes = slices.DeleteFunc(store.Scopes, func(scope workspace.Scope) bool { return scope.Name == words[2] })
	if len(store.Scopes) == before {
		c.app.Flash().Errf("Unknown workspace %q", words[2])
		return
	}
	if store.Active == words[2] {
		store.Active = ""
	}
	if err := workspace.SaveStore(dailyWorkspacePath(), *store); err != nil {
		c.app.Flash().Err(err)
		return
	}
	open(dailyWorkspaceScopesMode, "")
}

func (c *Command) dailyWorkspacePin(store *workspace.Store, words []string) {
	if len(words) != 2 {
		c.app.Flash().Warn("Use :workspace pin with an API resource selected")
		return
	}
	selected, ok := c.app.Content.Top().(SelectedResource)
	if !ok {
		c.app.Flash().Warn("Select an API resource first")
		return
	}
	target := selected.SelectedResource()
	if err := target.Err(); err != nil {
		c.app.Flash().Err(err)
		return
	}
	if target.Context != c.app.Config.ActiveContextName() {
		c.app.Flash().Warn("Context changed; select the resource again")
		return
	}
	for i := range store.Scopes {
		scope := &store.Scopes[i]
		if scope.Name != store.Active {
			continue
		}
		if scope.Context != target.Context {
			c.app.Flash().Warn("Active workspace belongs to a different context")
			return
		}
		ref := workspace.ResourceRef{GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}
		for _, pin := range scope.Pins {
			if pin == ref {
				c.app.Flash().Info("Resource is already pinned")
				return
			}
		}
		scope.Pins = append(slices.Clone(scope.Pins), ref)
		if err := workspace.SaveStore(dailyWorkspacePath(), *store); err != nil {
			c.app.Flash().Err(err)
			return
		}
		c.app.Flash().Info("Resource pinned in " + scope.Name)
		return
	}
	c.app.Flash().Warn("Create or use a workspace before pinning resources")
}

func (c *Command) dailyWorkspaceSearch(store *workspace.Store, words []string) {
	if len(words) < 5 || words[2] != "save" {
		c.app.Flash().Warn("Use :workspace search save NAME QUERY")
		return
	}
	query := strings.Join(words[4:], " ")
	if _, err := parseDailyWorkspaceQuery(query); err != nil {
		c.app.Flash().Err(err)
		return
	}
	for i := range store.Scopes {
		scope := &store.Scopes[i]
		if scope.Name != store.Active {
			continue
		}
		if scope.Context != c.app.Config.ActiveContextName() {
			c.app.Flash().Warn("Active workspace belongs to a different context")
			return
		}
		search := workspace.SavedSearch{Name: words[3], Query: query}
		scope.Searches = slices.Clone(scope.Searches)
		found := false
		for j, old := range scope.Searches {
			if old.Name == search.Name {
				scope.Searches[j] = search
				found = true
				break
			}
		}
		if !found {
			scope.Searches = append(scope.Searches, search)
		}
		if err := workspace.SaveStore(dailyWorkspacePath(), *store); err != nil {
			c.app.Flash().Err(err)
			return
		}
		c.app.Flash().Info("Search saved in " + scope.Name)
		return
	}
	c.app.Flash().Warn("Create or use a workspace before saving searches")
}
