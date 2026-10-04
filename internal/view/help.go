// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	helpTitle    = "Help"
	helpTitleFmt = " [aqua::b]%s "
)

// HelpFunc processes menu hints.
type HelpFunc func() model.MenuHints

// Help presents a help viewer.
type Help struct {
	*Table

	styles          *config.Styles
	hints           HelpFunc
	maxKey, maxDesc int
	width           int
	extras          map[string]string
}

// NewHelp returns a new help viewer.
func NewHelp(app *App) *Help {
	h := &Help{
		Table:  NewTable(client.HlpGVR),
		hints:  app.Content.Top().Hints,
		extras: app.Content.Top().ExtraHints(),
	}
	if owner, ok := app.Content.Top().(actionOwner); ok {
		h.hints = func() model.MenuHints { return actionCatalogHints(owner, app) }
	}
	return h
}

func (*Help) SetCommand(*cmd.Interpreter)            {}
func (*Help) SetFilter(string, bool)                 {}
func (*Help) SetLabelSelector(labels.Selector, bool) {}

// Init initializes the component.
func (h *Help) Init(ctx context.Context) error {
	if err := h.Table.Init(ctx); err != nil {
		return err
	}
	h.SetSelectable(false, false)
	h.resetTitle()
	h.SetBorder(true)
	h.SetBorderPadding(0, 0, 1, 1)
	h.bindKeys()
	h.build()
	h.app.Styles.AddListener(h)
	h.StylesChanged(h.app.Styles)

	return nil
}

// InCmdMode checks if prompt is active.
func (*Help) InCmdMode() bool {
	return false
}

// StylesChanged notifies skin changed.
func (h *Help) StylesChanged(s *config.Styles) {
	h.styles = s
	h.SetBackgroundColor(s.BgColor())
	h.updateStyle()
}

func (h *Help) bindKeys() {
	h.Actions().Delete(ui.KeySpace, tcell.KeyCtrlSpace, tcell.KeyCtrlS, ui.KeySlash)
	h.Actions().Bulk(ui.KeyMap{
		tcell.KeyEscape: ui.NewKeyAction("Back", h.app.PrevCmd, true),
		ui.KeyQ:         ui.NewKeyAction("Back", h.app.PrevCmd, false),
		ui.KeyHelp:      ui.NewKeyAction("Back", h.app.PrevCmd, false),
		tcell.KeyEnter:  ui.NewKeyAction("Back", h.app.PrevCmd, false),
	})
}

// build keeps a key and its action together in one scrollable list. Long
// availability reasons use continuation rows rather than forcing the action
// description beyond the viewport. No universal Space/Tab/l behavior is
// invented: the owned action snapshot supplies the current mode's bindings.
func (h *Help) build() {
	h.Clear()
	width := h.width
	if width <= 0 {
		width = 160
	}
	h.maxKey = 0
	hints := h.hints()
	sort.Sort(hints)
	for _, hint := range hints {
		h.maxKey = max(h.maxKey, tview.TaggedStringWidth(ui.ToMnemonic(hint.Mnemonic)))
	}
	h.maxKey = min(h.maxKey, max(8, width/3))
	h.maxDesc = max(1, width-h.maxKey-2)
	row := h.addHelpSection(0, "RESOURCE", hints)
	if len(h.extras) > 0 {
		var extra model.MenuHints
		for label, value := range h.extras {
			extra = append(extra, model.MenuHint{Mnemonic: label, Description: value})
		}
		sort.Sort(extra)
		row = h.addHelpSection(row+1, "DETAILS", extra)
	}
	if hints, err := h.showHotKeys(); err == nil && len(hints) > 0 {
		row = h.addHelpSection(row+1, "HOTKEYS", hints)
	}
	h.addHelpSection(row+1, "HELP CONTROLS", model.MenuHints{
		{Mnemonic: "up/down", Description: "Scroll help"},
		{Mnemonic: "PgUp/PgDn", Description: "Page through all actions and reasons"},
		{Mnemonic: "esc", Description: "Return to retained view"},
	})
	if h.styles != nil {
		h.updateStyle()
	}
}

func (h *Help) addHelpSection(row int, title string, hints model.MenuHints) int {
	h.SetCell(row, 0, h.titleCell(title).SetReference("heading"))
	h.SetCell(row, 1, tview.NewTableCell(""))
	row++
	for _, hint := range hints {
		key := ui.ToMnemonic(hint.Mnemonic)
		h.SetCell(row, 0, tview.NewTableCell(key).SetReference(hint.Mnemonic).SetMaxWidth(h.maxKey))
		lines := tview.WordWrap(tview.Escape(hint.Description), h.maxDesc)
		if len(lines) == 0 {
			lines = []string{""}
		}
		for index, line := range lines {
			if index > 0 {
				h.SetCell(row, 0, tview.NewTableCell(""))
			}
			h.SetCell(row, 1, tview.NewTableCell(line).SetMaxWidth(h.maxDesc).SetExpansion(1))
			row++
		}
	}
	return row
}

// Draw recalculates wrapping after resize without losing the reader's place.
func (h *Help) Draw(screen tcell.Screen) {
	_, _, width, _ := h.GetInnerRect()
	if width != h.width {
		row, col := h.GetOffset()
		h.width = width
		h.build()
		h.SetOffset(row, col)
	}
	h.Table.Draw(screen)
}

func (h *Help) showHotKeys() (model.MenuHints, error) {
	hh := config.NewHotKeys()
	if err := hh.Load(h.App().Config.ContextHotkeysPath()); err != nil {
		return nil, fmt.Errorf("no hotkey configuration found")
	}
	kk := make(sort.StringSlice, 0, len(hh.HotKey))
	for k := range hh.HotKey {
		kk = append(kk, k)
	}
	kk.Sort()
	mm := make(model.MenuHints, 0, len(hh.HotKey))
	for _, k := range kk {
		mm = append(mm, model.MenuHint{
			Mnemonic:    hh.HotKey[k].ShortCut,
			Description: hh.HotKey[k].Description,
		})
	}

	return mm, nil
}

func (h *Help) resetTitle() {
	h.SetTitle(fmt.Sprintf(helpTitleFmt, helpTitle+" · arrows/PgUp/PgDn scroll · Esc back"))
}

func (h *Help) updateStyle() {
	p := h.styles.Semantic()
	var (
		style   = tcell.StyleDefault.Background(p.Canvas.Color())
		key     = style.Foreground(config.ReadableForeground(p.Focus.Color(), p.Canvas.Color())).Bold(true)
		numKey  = style.Foreground(config.ReadableForeground(p.Category.Color(), p.Canvas.Color())).Bold(true)
		info    = style.Foreground(config.ReadableForeground(p.Text.Color(), p.Canvas.Color()))
		heading = key
	)
	for col := range h.GetColumnCount() {
		for row := range h.GetRowCount() {
			c := h.GetCell(row, col)
			if c == nil {
				continue
			}
			switch {
			case extractRef(c) == "heading":
				c.SetStyle(heading)
			case col%2 != 0:
				c.SetStyle(info)
			default:
				if _, err := strconv.Atoi(extractRef(c)); err == nil {
					c.SetStyle(numKey)
					continue
				}
				c.SetStyle(key)
			}
		}
	}
}

// ----------------------------------------------------------------------------
// Helpers...

func extractRef(c *tview.TableCell) string {
	if ref, ok := c.GetReference().(string); ok {
		return ref
	}

	return c.Text
}

func (h *Help) titleCell(title string) *tview.TableCell {
	c := tview.NewTableCell(title)
	c.SetTextColor(h.Styles().Semantic().Focus.Color())
	c.SetAttributes(tcell.AttrBold)
	c.SetExpansion(1)
	c.SetAlign(tview.AlignLeft)

	return c
}
