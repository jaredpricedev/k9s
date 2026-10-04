// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/view/cmd"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	commandDiscoveryTimeout = 5 * time.Second
	commandDiscoveryTTL     = 5 * time.Minute
	commandDiscoveryRetry   = 30 * time.Second
)

type commandSuggestionScope struct {
	context, namespace string
	revision           uint64
}
type commandSuggestionData struct {
	namespaces client.NamespaceNames
	contexts   []string
}
type commandSuggestionLoader func(context.Context) (commandSuggestionData, error)

// commandSuggestions retains a session-scoped catalog. Input only reads this
// cache and schedules work; the loader never runs on the event/render thread.
type commandSuggestions struct {
	mx         sync.Mutex
	ctx        context.Context
	scope      commandSuggestionScope
	load       commandSuggestionLoader
	dispatch   func(func())
	refresh    func()
	data       commandSuggestionData
	expires    time.Time
	generation uint64
	cancel     context.CancelFunc
}

func (d *commandSuggestions) reset(ctx context.Context, scope commandSuggestionScope, load commandSuggestionLoader) {
	d.mx.Lock()
	defer d.mx.Unlock()
	d.cancelLocked()
	d.ctx, d.scope, d.load = ctx, scope, load
	d.data = commandSuggestionData{namespaces: client.NamespaceNames{}}
	// A known active namespace remains usable when enumeration is denied.
	if scope.namespace != "" && !client.IsClusterWide(scope.namespace) {
		d.data.namespaces[scope.namespace] = struct{}{}
	}
	d.expires = time.Time{}
}

func (d *commandSuggestions) stop() {
	d.mx.Lock()
	defer d.mx.Unlock()
	d.cancelLocked()
	d.ctx, d.load = nil, nil
}

func (d *commandSuggestions) cancelLocked() {
	d.generation++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

func (d *commandSuggestions) cancelPending() {
	d.mx.Lock()
	defer d.mx.Unlock()
	d.cancelLocked()
}

func (d *commandSuggestions) catalog() commandSuggestionData {
	d.mx.Lock()
	defer d.mx.Unlock()
	return d.data // Published catalogs are immutable.
}

func (d *commandSuggestions) discover() {
	d.mx.Lock()
	if d.ctx == nil || d.ctx.Err() != nil || d.load == nil || d.cancel != nil || time.Now().Before(d.expires) {
		d.mx.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, commandDiscoveryTimeout)
	d.cancel = cancel
	d.generation++
	generation, scope, load := d.generation, d.scope, d.load
	d.mx.Unlock()
	go func() {
		defer cancel()
		data, err := load(ctx)
		if ctx.Err() == context.Canceled {
			return
		}
		if ctx.Err() != nil {
			// Timeouts also need to leave retryable state; cancellation caused
			// by Esc/session changes is rejected by the generation guard.
			err = ctx.Err()
		}
		d.dispatch(func() {
			d.mx.Lock()
			if generation != d.generation || scope != d.scope || d.ctx == nil || d.ctx.Err() != nil {
				d.mx.Unlock()
				return
			}
			d.cancel = nil
			if err == nil {
				d.data = data
				d.expires = time.Now().Add(commandDiscoveryTTL)
			} else {
				// Context names are local and may still have loaded successfully.
				if data.contexts != nil {
					d.data.contexts = data.contexts
				}
				d.expires = time.Now().Add(commandDiscoveryRetry)
				slog.Debug("Command completion discovery unavailable", slogs.Error, err)
			}
			d.mx.Unlock()
			// Recompute for the CURRENT buffer on the UI dispatcher. A response
			// never carries suffixes derived from the query that started it.
			d.refresh()
		})
	}()
}

func (*commandSuggestions) BufferChanged(_, _ string)   {}
func (*commandSuggestions) BufferCompleted(_, _ string) {}
func (d *commandSuggestions) BufferActive(active bool, _ model.BufferKind) {
	if active {
		d.discover()
	} else {
		d.cancelPending()
	}
}

func (a *App) suggestCommand() model.SuggestionFunc {
	d := &commandSuggestions{
		dispatch: a.QueueUpdateDraw,
		refresh: func() {
			if a.CmdBuff().IsActive() {
				a.CmdBuff().Notify(false)
			}
		},
	}
	a.commandSuggestions = d
	a.CmdBuff().AddListener(d)
	return func(s string) (entries sort.StringSlice) {
		a.syncCommandSuggestionScope()
		d.discover()
		if s == "" {
			return a.cmdHistory.List()
		}
		for _, alias := range a.command.suggestionAliases() {
			if suffix, ok := cmd.ShouldAddSuggest(strings.ToLower(s), alias); ok {
				entries = append(entries, suffix)
			}
		}
		for _, previous := range a.cmdHistory.List() {
			if suffix, ok := cmd.ShouldAddSuggest(s, previous); ok {
				entries = append(entries, suffix)
			}
		}
		data := d.catalog()
		entries = append(entries, cmd.SuggestSubCommand(s, data.namespaces, data.contexts)...)
		entries.Sort()
		return slices.Compact(entries)
	}
}

func (a *App) syncCommandSuggestionScope() {
	d := a.commandSuggestions
	d.mx.Lock()
	ctx, scope := d.ctx, d.scope
	d.mx.Unlock()
	if ctx != nil && scope != a.commandSuggestionScope() {
		a.resetCommandSuggestions(ctx)
	}
}

func (a *App) resetCommandSuggestions(ctx context.Context) {
	if a.commandSuggestions == nil {
		return
	}
	scope := a.commandSuggestionScope()
	var loader commandSuggestionLoader
	if conn := a.Conn(); conn != nil && conn.Config() != nil {
		// Copy flags on the UI thread without reading files or loading clients.
		cfg := conn.Config().Snapshot(scope.context)
		loader = commandCatalogLoader(cfg)
	}
	a.commandSuggestions.reset(ctx, scope, loader)
}

func (a *App) commandSuggestionScope() commandSuggestionScope {
	return commandSuggestionScope{context: a.Config.ActiveContextName(), namespace: a.Config.CachedNamespace(), revision: a.Config.DestinationRevision()}
}

func (a *App) stopCommandSuggestions() {
	if a.commandSuggestions != nil {
		a.commandSuggestions.stop()
	}
}

func commandCatalogLoader(cfg *client.Config) commandSuggestionLoader {
	return func(ctx context.Context) (data commandSuggestionData, err error) {
		contexts, err := cfg.Contexts()
		if err != nil {
			return data, err
		}
		data.contexts = make([]string, 0, len(contexts))
		for name := range contexts {
			data.contexts = append(data.contexts, name)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return data, contextErr
		}
		config, err := cfg.RESTConfig()
		if err != nil {
			return data, err
		}
		if wrap := cfg.Flags().WrapConfigFn; wrap != nil {
			config = wrap(config)
		}
		config.Timeout = commandDiscoveryTimeout
		dial, err := kubernetes.NewForConfig(config)
		if err != nil {
			return data, err
		}
		// LIST itself enforces authorization; no additional SAR is needed.
		namespaces, err := dial.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return data, err
		}
		data.namespaces = make(client.NamespaceNames, len(namespaces.Items))
		for i := range namespaces.Items {
			data.namespaces[namespaces.Items[i].Name] = struct{}{}
		}
		return data, nil
	}
}
