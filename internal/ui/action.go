// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/tcell/v2"
)

type (
	// RangeFn represents a range iteration callback.
	RangeFn func(tcell.Key, KeyAction)

	// ActionHandler handles a keyboard command.
	ActionHandler func(*tcell.EventKey) *tcell.EventKey

	// ActionOpts tracks various action options.
	ActionOpts struct {
		Visible   bool
		Shared    bool
		Plugin    bool
		HotKey    bool
		Dangerous bool
	}

	// KeyAction represents a keyboard action.
	KeyAction struct {
		Description string
		Action      ActionHandler
		Opts        ActionOpts
	}

	// KeyMap tracks key to action mappings.
	KeyMap map[tcell.Key]KeyAction

	// KeyActions tracks mappings between keystrokes and actions.
	KeyActions struct {
		actions KeyMap
		mx      sync.RWMutex
	}
)

// NewKeyAction returns a new keyboard action.
func NewKeyAction(d string, a ActionHandler, visible bool) KeyAction {
	return NewKeyActionWithOpts(d, a, ActionOpts{
		Visible: visible,
	})
}

// NewSharedKeyAction returns a new shared keyboard action.
func NewSharedKeyAction(d string, a ActionHandler, visible bool) KeyAction {
	return NewKeyActionWithOpts(d, a, ActionOpts{
		Visible: visible,
		Shared:  true,
	})
}

// NewKeyActionWithOpts returns a new keyboard action.
func NewKeyActionWithOpts(d string, a ActionHandler, opts ActionOpts) KeyAction {
	return KeyAction{
		Description: d,
		Action:      a,
		Opts:        opts,
	}
}

// NewKeyActions returns a new instance.
func NewKeyActions() *KeyActions {
	return &KeyActions{
		actions: make(map[tcell.Key]KeyAction),
	}
}

// NewKeyActionsFromMap copies the key map. The caller retains ownership of mm.
func NewKeyActionsFromMap(mm KeyMap) *KeyActions {
	a := NewKeyActions()
	maps.Copy(a.actions, mm)
	return a
}

// Get fetches an action given a key.
func (a *KeyActions) Get(key tcell.Key) (KeyAction, bool) {
	a.mx.RLock()
	defer a.mx.RUnlock()

	v, ok := a.actions[key]

	return v, ok
}

// Len returns action mapping count.
func (a *KeyActions) Len() int {
	a.mx.RLock()
	defer a.mx.RUnlock()

	return len(a.actions)
}

// Reset clears out actions.
func (a *KeyActions) Reset(aa *KeyActions) {
	km := aa.snapshot()
	a.mx.Lock()
	a.actions = km
	a.mx.Unlock()
}

// Range ranges over all actions and triggers a given function.
func (a *KeyActions) Range(f RangeFn) {
	for k, v := range a.snapshot() {
		f(k, v)
	}
}

// snapshot captures an owned copy, so callbacks and other action sets never
// access a mutable map after its owner's lock has been released.
func (a *KeyActions) snapshot() KeyMap {
	a.mx.RLock()
	defer a.mx.RUnlock()
	return maps.Clone(a.actions)
}

// Add adds a new key action.
func (a *KeyActions) Add(k tcell.Key, ka KeyAction) {
	a.mx.Lock()
	defer a.mx.Unlock()

	a.actions[k] = ka
}

// Bulk bulk insert key mappings.
func (a *KeyActions) Bulk(aa KeyMap) {
	a.mx.Lock()
	defer a.mx.Unlock()

	for k, v := range aa {
		a.actions[k] = v
	}
}

// Merge merges given actions into existing set.
func (a *KeyActions) Merge(aa *KeyActions) {
	km := aa.snapshot()
	a.mx.Lock()
	defer a.mx.Unlock()

	for k, v := range km {
		a.actions[k] = v
	}
}

// Clear remove all actions.
func (a *KeyActions) Clear() {
	a.mx.Lock()
	defer a.mx.Unlock()

	for k := range a.actions {
		delete(a.actions, k)
	}
}

// ClearDanger remove all dangerous actions.
func (a *KeyActions) ClearDanger() {
	a.mx.Lock()
	defer a.mx.Unlock()

	for k, v := range a.actions {
		if v.Opts.Dangerous {
			delete(a.actions, k)
		}
	}
}

// Set overlays the given actions, preserving existing keys as Merge does.
func (a *KeyActions) Set(aa *KeyActions) {
	a.Merge(aa)
}

// Delete deletes actions by the given keys.
func (a *KeyActions) Delete(kk ...tcell.Key) {
	a.mx.Lock()
	defer a.mx.Unlock()

	for _, k := range kk {
		delete(a.actions, k)
	}
}

// Hints returns a collection of hints.
func (a *KeyActions) Hints() model.MenuHints {
	a.mx.RLock()
	defer a.mx.RUnlock()

	kk := make([]tcell.Key, 0, len(a.actions))
	for k := range a.actions {
		if !a.actions[k].Opts.Shared {
			kk = append(kk, k)
		}
	}
	slices.Sort(kk)

	hh := make(model.MenuHints, 0, len(kk))
	for _, k := range kk {
		if name, ok := tcell.KeyNames[k]; ok {
			hh = append(hh,
				model.MenuHint{
					Mnemonic:    name,
					Description: a.actions[k].Description,
					Visible:     a.actions[k].Opts.Visible,
				},
			)
		} else {
			slog.Error("Unable to locate key name", slogs.Key, k)
		}
	}

	return hh
}
