// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	runewidth "github.com/mattn/go-runewidth"
)

const (
	menuIndexFmt = " [key:-:b]<%d> [fg:-:fgstyle]%s "
	maxRows      = 6
)

var menuRX = regexp.MustCompile(`\d`)

// Menu presents menu options.
type Menu struct {
	*tview.Table

	styles *config.Styles
	hints  model.MenuHints
	width  int
}

// NewMenu returns a new menu.
func NewMenu(styles *config.Styles) *Menu {
	m := Menu{
		Table:  tview.NewTable(),
		styles: styles,
	}
	m.SetBackgroundColor(styles.BgColor())
	styles.AddListener(&m)

	return &m
}

// StylesChanged notifies skin changed.
func (m *Menu) StylesChanged(s *config.Styles) {
	m.styles = s
	m.SetBackgroundColor(s.BgColor())
	m.HydrateMenu(m.hints)
}

// StackPushed notifies a component was added.
func (m *Menu) StackPushed(c model.Component) {
	m.HydrateMenu(c.Hints())
}

// StackPopped notifies a component was removed.
func (m *Menu) StackPopped(_, top model.Component) {
	if top != nil {
		m.HydrateMenu(top.Hints())
	} else {
		m.HydrateMenu(nil)
	}
}

// StackTop notifies the top component.
func (m *Menu) StackTop(t model.Component) {
	m.HydrateMenu(t.Hints())
}

// HydrateMenu populate menu ui from hints.
func (m *Menu) HydrateMenu(hh model.MenuHints) {
	m.Clear()
	m.hints = append(m.hints[:0], hh...)
	hh = m.hints
	sort.Sort(hh)

	table := make([]model.MenuHints, maxRows+1)
	colCount := (len(hh) / maxRows) + 1
	if m.hasDigits(hh) {
		colCount++
	}
	for row := range maxRows {
		table[row] = make(model.MenuHints, colCount)
	}
	t := m.buildMenuTable(hh, table, colCount)

	for row := range t {
		for col := range len(t[row]) {
			c := tview.NewTableCell(t[row][col])
			if t[row][col] == "" {
				c = tview.NewTableCell("")
			}
			c.SetBackgroundColor(m.styles.BgColor())
			m.SetCell(row, col, c)
		}
	}
}

// Draw fits descriptions again after a resize, keeping shortcut columns intact.
func (m *Menu) Draw(screen tcell.Screen) {
	if _, _, width, _ := m.GetInnerRect(); width != m.width {
		m.width = width
		m.HydrateMenu(m.hints)
	}
	m.Table.Draw(screen)
}

func (*Menu) hasDigits(hh model.MenuHints) bool {
	for _, h := range hh {
		if !h.Visible {
			continue
		}
		if menuRX.MatchString(h.Mnemonic) {
			return true
		}
	}
	return false
}

func (m *Menu) buildMenuTable(hh model.MenuHints, table []model.MenuHints, colCount int) [][]string {
	var row, col int
	firstCmd := true
	maxKeys := make([]int, colCount)
	for _, h := range hh {
		if !h.Visible {
			continue
		}

		if !menuRX.MatchString(h.Mnemonic) && firstCmd {
			row, col, firstCmd = 0, col+1, false
			if table[0][0].IsBlank() {
				col = 0
			}
		}
		if size := runewidth.StringWidth(keyConv(strings.ToLower(h.Mnemonic))); maxKeys[col] < size {
			maxKeys[col] = size
		}
		table[row][col] = h
		row++
		if row >= maxRows {
			row, col = 0, col+1
		}
	}

	out := make([][]string, len(table))
	for r := range out {
		out[r] = make([]string, len(table[r]))
	}
	m.layout(table, maxKeys, out)

	return out
}

func (m *Menu) layout(table []model.MenuHints, mm []int, out [][]string) {
	widths := make([]int, len(mm))
	var total, columns int
	for r := range table {
		for c, hint := range table[r] {
			widths[c] = max(widths[c], tview.TaggedStringWidth(m.formatMenu(hint, mm[c])))
		}
	}
	for _, width := range widths {
		total += width
		if width > 0 {
			columns++
		}
	}
	// Leave the table's separator and each cell's trailing space intact. Share
	// only description space; a long label must not push later keys offscreen.
	if m.width > 0 && total+columns > m.width {
		natural := append([]int(nil), widths...)
		remaining := m.width - columns
		for c, width := range widths {
			widths[c] = min(width, mm[c]+6)
			remaining -= widths[c]
		}
		for remaining > 0 {
			changed := false
			for c := range widths {
				if remaining > 0 && widths[c] < natural[c] {
					widths[c]++
					remaining--
					changed = true
				}
			}
			if !changed {
				break
			}
		}
	}
	for r := range table {
		for c := range table[r] {
			hint := table[r][c]
			if hint.Description != "" {
				fullWidth := tview.TaggedStringWidth(m.formatMenu(hint, mm[c]))
				labelWidth := runewidth.StringWidth(hint.Description)
				hint.Description = Truncate(hint.Description, max(1, widths[c]-fullWidth+labelWidth))
			}
			out[r][c] = m.formatMenu(hint, mm[c])
		}
	}
}

func (m *Menu) formatMenu(h model.MenuHint, size int) string {
	if h.Mnemonic == "" || h.Description == "" {
		return ""
	}
	styles := m.styles.Frame()
	i, err := strconv.Atoi(h.Mnemonic)
	if err == nil {
		return formatNSMenu(i, h.Description, &styles)
	}

	return formatPlainMenu(h, size, &styles)
}

// ----------------------------------------------------------------------------
// Helpers...

func keyConv(s string) string {
	if s == "" || !strings.Contains(s, "alt") {
		return s
	}
	if runtime.GOOS != "darwin" {
		return s
	}

	return strings.Replace(s, "alt", "opt", 1)
}

// Truncate a string to the given l and suffix ellipsis if needed.
func Truncate(str string, width int) string {
	return runewidth.Truncate(str, width, string(tview.SemigraphicsHorizontalEllipsis))
}

func ToMnemonic(s string) string {
	if s == "" {
		return s
	}

	return "<" + keyConv(strings.ToLower(s)) + ">"
}

func formatNSMenu(i int, name string, styles *config.Frame) string {
	fmat := strings.Replace(menuIndexFmt, "[key", "["+styles.Menu.NumKeyColor.String(), 1)
	fmat = strings.ReplaceAll(fmat, ":bg:", ":"+styles.Title.BgColor.String()+":")
	fmat = strings.Replace(fmat, "[fg", "["+styles.Menu.FgColor.String(), 1)
	fmat = strings.Replace(fmat, "fgstyle]", styles.Menu.FgStyle.ToShortString()+"]", 1)

	return fmt.Sprintf(fmat, i, tview.Escape(name))
}

func formatPlainMenu(h model.MenuHint, size int, styles *config.Frame) string {
	menuFmt := " [key:-:b]%s [fg:-:fgstyle]%s "
	fmat := strings.Replace(menuFmt, "[key", "["+styles.Menu.KeyColor.String(), 1)
	fmat = strings.Replace(fmat, "[fg", "["+styles.Menu.FgColor.String(), 1)
	fmat = strings.ReplaceAll(fmat, ":bg:", ":"+styles.Title.BgColor.String()+":")
	fmat = strings.Replace(fmat, "fgstyle]", styles.Menu.FgStyle.ToShortString()+"]", 1)

	key := ToMnemonic(h.Mnemonic)
	key += strings.Repeat(" ", max(0, size+2-runewidth.StringWidth(key)))
	return fmt.Sprintf(fmat, tview.Escape(key), tview.Escape(h.Description))
}
