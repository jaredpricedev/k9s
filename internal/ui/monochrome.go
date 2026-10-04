// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package ui

import (
	"os"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// SetRoot applies NO_COLOR at the final drawing boundary, including legacy
// widgets and dynamic-color text. Framework input and terminal cleanup remain
// on the original screen. Skins retain their colors when NO_COLOR is cleared.
func (a *App) SetRoot(root tview.Primitive, fullscreen bool) *tview.Application {
	if root != nil && os.Getenv("NO_COLOR") != "" {
		root = &monochromeRoot{Primitive: root, styles: a.Styles}
	}
	return a.Application.SetRoot(root, fullscreen)
}

type monochromeRoot struct {
	tview.Primitive
	styles *config.Styles
}

func (r *monochromeRoot) Draw(screen tcell.Screen) {
	p := r.styles.Semantic()
	selected := []tcell.Color{p.Selected.Color()}
	if r.styles != nil {
		selected = append(selected, r.styles.Table().CursorBgColor.Color(), r.styles.K9s.Dialog.ButtonFocusBgColor.Color())
	}
	r.Primitive.Draw(monochromeScreen{Screen: screen, selected: selected, canvas: p.Canvas.Color(), panel: p.Panel.Color()})
}

type monochromeScreen struct {
	tcell.Screen
	selected      []tcell.Color
	canvas, panel tcell.Color
}

func (s monochromeScreen) style(style tcell.Style) tcell.Style {
	_, bg, _ := style.Decompose()
	// Preserve a visible cursor/focus even when the skin encodes it only in
	// background color. Bold, underline and existing reverse survive unchanged.
	for _, selected := range s.selected {
		if selected != tcell.ColorDefault && selected != s.canvas && selected != s.panel && bg == selected {
			style = style.Reverse(true)
			break
		}
	}
	return style.Foreground(tcell.ColorDefault).Background(tcell.ColorDefault)
}

func (s monochromeScreen) SetContent(x, y int, main rune, combining []rune, style tcell.Style) {
	s.Screen.SetContent(x, y, main, combining, s.style(style))
}

func (s monochromeScreen) SetCell(x, y int, style tcell.Style, chars ...rune) {
	s.Screen.SetCell(x, y, s.style(style), chars...)
}

func (s monochromeScreen) SetStyle(style tcell.Style)     { s.Screen.SetStyle(s.style(style)) }
func (s monochromeScreen) Fill(r rune, style tcell.Style) { s.Screen.Fill(r, s.style(style)) }
func (monochromeScreen) Colors() int                      { return 0 }
