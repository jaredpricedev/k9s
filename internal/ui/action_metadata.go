// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"sort"
	"strings"
	"unicode"

	"github.com/derailed/tcell/v2"
)

const (
	ActionInspect  = "Inspect"
	ActionNavigate = "Navigate"
	ActionFilter   = "Filter"
	ActionExport   = "Export"
	ActionChange   = "Change"
)

// ActionContext describes restrictions shared by every discovery surface.
// SelectionReason is empty when a resource target is available.
type ActionContext struct {
	ReadOnly        bool
	SelectionReason string
}

// ActionDescriptor is an owned snapshot. Discoverability is independent of the
// shortcut bar, and an unavailable action remains searchable with its reason.
type ActionDescriptor struct {
	ID, Label, Category, Shortcut            string
	Key                                      tcell.Key
	Visible, Discoverable, RequiresSelection bool
	UnavailableReason                        string
	Handler                                  ActionHandler
	Priority                                 int
}

func (a *ActionDescriptor) Available() bool { return a.UnavailableReason == "" && a.Handler != nil }

// ActionCategoryOrder gives all discovery surfaces the same task-oriented order.
func ActionCategoryOrder(category string) int {
	switch category {
	case ActionInspect:
		return 0
	case ActionNavigate:
		return 1
	case ActionFilter:
		return 2
	case ActionExport:
		return 3
	case ActionChange:
		return 4
	default:
		return 5
	}
}

// DescribeActions invokes availability callbacks outside the actions lock.
func DescribeActions(actions *KeyActions, context ActionContext) []ActionDescriptor {
	if actions == nil {
		return nil
	}
	snapshot := actions.Snapshot()
	result := make([]ActionDescriptor, 0, len(snapshot))
	for key, action := range snapshot {
		item := ActionDescriptor{
			ID: action.ID, Label: action.Description, Category: action.Category,
			Key: key, Shortcut: tcell.KeyNames[key], Visible: action.Opts.Visible,
			Discoverable: action.Opts.Discoverable, RequiresSelection: action.Opts.RequiresSelection,
			Handler:  action.Action,
			Priority: PrimaryActionPriority(key, action.Opts.Priority),
		}
		if item.Category == "" {
			item.Category = actionCategory(action.Description, action.Opts)
		}
		if item.ID == "" {
			item.ID = actionID(action.Description)
		}
		switch {
		case context.ReadOnly && action.Opts.Dangerous:
			item.UnavailableReason = "Read-only mode: changes are disabled"
		case item.RequiresSelection && context.SelectionReason != "":
			item.UnavailableReason = context.SelectionReason
		case action.Availability != nil:
			item.UnavailableReason = action.Availability()
		}
		if item.Handler == nil && item.UnavailableReason == "" {
			item.UnavailableReason = "This action is unavailable in the current view"
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return ActionCategoryOrder(result[i].Category) < ActionCategoryOrder(result[j].Category)
		}
		if result[i].Label != result[j].Label {
			return result[i].Label < result[j].Label
		}
		return result[i].Key < result[j].Key
	})
	return result
}

// PrimaryActionPriority uses stable binding identity, rather than translated
// or custom action descriptions. A view can supply its own positive priority.
func PrimaryActionPriority(key tcell.Key, priority int) int {
	if priority != 0 {
		return priority
	}
	switch key {
	case tcell.KeyEnter:
		return 1
	case KeySpace:
		return 2
	case KeyD:
		return 2
	case KeySlash:
		return 3
	case KeyR:
		return 4
	case KeyL:
		return 3
	case tcell.KeyTab:
		return 6
	case tcell.KeyEscape:
		return 7
	}
	return 0
}

func actionID(label string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(label) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	return b.String()
}

func actionCategory(label string, opts ActionOpts) string {
	if opts.Dangerous {
		return ActionChange
	}
	l := strings.ToLower(label)
	for _, word := range []string{"copy", "save", "export", "record", "screen dump"} {
		if strings.Contains(l, word) {
			return ActionExport
		}
	}
	for _, word := range []string{"filter", "sort", "noise", "isolate", "exclude", "collapse", "redact", "wrap", "freeze", "scroll", "source", "timestamp", "clear"} {
		if strings.Contains(l, word) {
			return ActionFilter
		}
	}
	for _, word := range []string{"inspect", "view", "help", "yaml", "describe", "logs", "pattern", "timeline", "detail", "status"} {
		if strings.Contains(l, word) {
			return ActionInspect
		}
	}
	return ActionNavigate
}
