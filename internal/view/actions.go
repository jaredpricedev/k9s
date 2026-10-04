// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"k8s.io/apimachinery/pkg/util/sets"
)

// AllScopes represents actions available for all views.
const AllScopes = "all"

// Runner represents a runnable action handler.
type Runner interface {
	// App returns the current app.
	App() *App

	// GetSelectedItem returns the current selected item.
	GetSelectedItem() string

	// Aliases returns all aliases assoxciated with the view GVR.
	Aliases() sets.Set[string]

	// EnvFn returns the current environment function.
	EnvFn() EnvFunc
}

func hasAll(scopes []string) bool {
	return slices.Contains(scopes, AllScopes)
}

func includes(aliases []string, s string) bool {
	return slices.Contains(aliases, s)
}

func inScope(scopes []string, aliases sets.Set[string]) bool {
	if hasAll(scopes) {
		return true
	}
	for _, s := range scopes {
		if _, ok := aliases[s]; ok {
			return ok
		}
	}

	return false
}

func hotKeyActions(r Runner, aa *ui.KeyActions) error {
	hh := config.NewHotKeys()
	aa.Range(func(k tcell.Key, a ui.KeyAction) {
		if a.Opts.HotKey {
			aa.Delete(k)
		}
	})

	var errs error
	if err := hh.Load(r.App().Config.ContextHotkeysPath()); err != nil {
		errs = errors.Join(errs, err)
	}
	for k, hk := range hh.HotKey {
		key, err := asKey(hk.ShortCut)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if _, ok := aa.Get(key); ok {
			if !hk.Override {
				errs = errors.Join(errs, fmt.Errorf("duplicate hotkey found for %q in %q", hk.ShortCut, k))
				continue
			}
			slog.Debug("HotKey overrode action shortcut",
				slogs.Shortcut, hk.ShortCut,
				slogs.Key, k,
			)
		}

		command, err := r.EnvFn()().Substitute(hk.Command)
		if err != nil {
			slog.Warn("Invalid shortcut command", slogs.Error, err)
			continue
		}

		aa.Add(key, ui.NewKeyActionWithOpts(
			hk.Description,
			gotoCmd(r, command, "", !hk.KeepHistory),
			ui.ActionOpts{
				Shared: true,
				HotKey: true,
			},
		))
	}

	return errs
}

func gotoCmd(r Runner, cmd, path string, clearStack bool) ui.ActionHandler {
	return func(*tcell.EventKey) *tcell.EventKey {
		r.App().gotoResource(cmd, path, clearStack, true)
		return nil
	}
}

func pluginActions(r Runner, aa *ui.KeyActions) error {
	// Skip plugin loading if no valid connection
	if r.App().Conn() == nil || !r.App().Conn().ConnectionOK() {
		return nil
	}

	aa.Range(func(k tcell.Key, a ui.KeyAction) {
		if a.Opts.Plugin {
			aa.Delete(k)
		}
	})

	path, err := r.App().Config.ContextPluginsPath()
	if err != nil {
		return err
	}
	pp := config.NewPlugins()
	if err := pp.Load(path, true); err != nil {
		return err
	}

	var (
		errs    error
		aliases = r.Aliases()
		ro      = r.App().Config.IsReadOnly()
	)
	for k := range pp.Plugins {
		if !inScope(pp.Plugins[k].Scopes, aliases) || (ro && pp.Plugins[k].Dangerous) {
			continue
		}
		key, err := asKey(pp.Plugins[k].ShortCut)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		plugin := pp.Plugins[k]
		errs = errors.Join(errs, bindPluginAction(r, aa, k, key, &plugin))
	}

	return errs
}

// Native Flux mutations stay inline even when an older plugin file requests an
// override. Optional CLI workflows can use other keys; other views retain their
// normal override semantics. Reserve the keys in read-only mode as well.
func bindPluginAction(r Runner, aa *ui.KeyActions, name string, key tcell.Key, plugin *config.Plugin) error {
	if resource, ok := r.(interface{ GVR() *client.GVR }); ok &&
		(resource.GVR() == client.FluxGVR || dao.FluxNativeActions(resource.GVR())) &&
		(key == ui.KeyShiftR || key == ui.KeyShiftT) {
		slog.Debug("Native Flux shortcut retained; assign CLI plugins another key", slogs.Plugin, name, slogs.Key, plugin.ShortCut)
		return nil
	}
	if _, ok := aa.Get(key); ok {
		if !plugin.Override {
			return fmt.Errorf("duplicate plugin key found for %q in %q", plugin.ShortCut, name)
		}
		slog.Debug("Plugin overrode action shortcut", slogs.Plugin, name, slogs.Key, plugin.ShortCut)
	}
	aa.Add(key, ui.NewKeyActionWithOpts(plugin.Description, pluginAction(r, plugin), ui.ActionOpts{
		Visible: true, Plugin: true, Dangerous: plugin.Dangerous,
	}))
	return nil
}

func pluginAction(r Runner, p *config.Plugin) ui.ActionHandler {
	return func(evt *tcell.EventKey) *tcell.EventKey {
		path := r.GetSelectedItem()
		if path == "" {
			return evt
		}
		if r.EnvFn() == nil {
			return nil
		}
		invocation, err := capturePluginInvocation(r, p)
		if err != nil {
			r.App().Flash().Err(err)
			return nil
		}

		invocation.prepare(func() {
			if len(invocation.plugin.Inputs) > 0 {
				d := r.App().Styles.Dialog()
				dialog.ShowPluginInputs(&d, r.App().Content.Pages, "Plugin Inputs", invocation.plugin.Inputs,
					func(msg string) { r.App().Flash().Warn(msg) },
					func(inputValues dialog.PluginInputValues) { invocation.execute(inputValues) },
					func() {},
				)
				return
			}
			invocation.execute(nil)
		})
		return nil
	}
}
