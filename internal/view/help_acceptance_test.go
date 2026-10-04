// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

const (
	helpAcceptanceLabelEnd  = "labelcomplete"
	helpAcceptanceReasonEnd = "reasoncomplete"
)

type helpAcceptanceOwner struct {
	*Details
	extras map[string]string
}

func (v *helpAcceptanceOwner) ExtraHints() map[string]string { return v.extras }

func TestHelpAcceptanceStoppedPageDoesNotReceiveSkinUpdatesAndRestarts(t *testing.T) {
	app, _, help, _ := helpAcceptanceFixture(t)
	original := help.GetCell(0, 0).BackgroundColor
	help.Stop()
	require.NoError(t, app.Styles.Load("../../skins/monochrome.yaml", false))
	app.Styles.Update()
	require.NotEqual(t, original, app.Styles.Semantic().Canvas.Color(), "fixture must actually change canvas")
	require.Equal(t, original, help.GetCell(0, 0).BackgroundColor, "closed Help must release its own style listener")
	help.Start()
	require.Equal(t, app.Styles.Semantic().Canvas.Color(), help.GetCell(0, 0).BackgroundColor, "retained Help must adopt the current skin on return")
	require.NoError(t, app.Styles.Load("../../skins/gruvbox-light.yaml", false))
	app.Styles.Update()
	require.Equal(t, app.Styles.Semantic().Canvas.Color(), help.GetCell(0, 0).BackgroundColor)
	closed := help.GetCell(0, 0).BackgroundColor
	help.Stop()
	require.NoError(t, app.Styles.Load("../../skins/high-contrast.yaml", false))
	app.Styles.Update()
	require.Equal(t, closed, help.GetCell(0, 0).BackgroundColor, "repeated Stop/Start must not leave duplicate listeners")
}

func helpAcceptanceFixture(t *testing.T) (*App, *helpAcceptanceOwner, *Help, *int) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	app.Config.K9s.UI.NoIcons = true
	app.Config.K9s.UI.Logoless = true
	previousHotkeys, previousContexts := config.AppHotKeysFile, config.AppContextsDir
	config.AppHotKeysFile = filepath.Join(t.TempDir(), "hotkeys.yaml")
	config.AppContextsDir = t.TempDir()
	t.Cleanup(func() { config.AppHotKeysFile, config.AppContextsDir = previousHotkeys, previousContexts })
	require.NoError(t, os.WriteFile(config.AppHotKeysFile, []byte("hotKeys:\n  retained:\n    shortCut: Shift-A\n    description: Review the configured retained workspace hotkeycomplete\n    command: pods\n"), 0600))
	owner := &helpAcceptanceOwner{
		Details: NewDetails(app, "Help acceptance", "", contentTXT, false),
		extras:  map[string]string{"Scope": "The entire retained source identity and scope explanation remain available detailcomplete"},
	}
	executed := new(int)
	for index, category := range []string{ui.ActionInspect, ui.ActionNavigate, ui.ActionFilter, ui.ActionExport, ui.ActionChange} {
		label := "A " + category + " " + strings.Repeat("custom action [red] retains literal labels and an intentionally long description ", 3) + category + helpAcceptanceLabelEnd
		action := ui.NewKeyAction(label, func(*tcell.EventKey) *tcell.EventKey { *executed++; return nil }, false)
		action.ID = fmt.Sprintf("acceptance.%d", index)
		action.Category = category
		action.Availability = func() string {
			return "Unavailable: " + strings.Repeat("review the saved context and complete captured source before continuing ", 3) + category + helpAcceptanceReasonEnd
		}
		owner.Actions().Add(tcell.Key(int(ui.KeyA)+index), action)
	}
	app.Content.Push(owner)
	help := NewHelp(app)
	require.NoError(t, help.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	t.Cleanup(help.Stop)
	return app, owner, help, executed
}

// Keyboard scrolling must reach complete descriptions and disabled reasons;
// inspecting offscreen table cells alone cannot establish their readability.
func TestHelpAcceptanceLongLabelsReasonsAndSectionsAreKeyboardReachable(t *testing.T) {
	for _, skin := range []string{"stock", "monochrome", "high-contrast"} {
		for _, width := range []int{80, 60} {
			t.Run(fmt.Sprintf("%s/%d", skin, width), func(t *testing.T) {
				app, owner, help, executed := helpAcceptanceFixture(t)
				if skin != "stock" {
					require.NoError(t, app.Styles.Load("../../skins/"+skin+".yaml", false))
					app.Styles.Update()
				}
				screen := tcell.NewSimulationScreen("UTF-8")
				require.NoError(t, screen.Init())
				t.Cleanup(screen.Fini)
				screen.SetSize(width, 24)
				helpAcceptancePageThrough(t, help, screen, width)
				require.Zero(t, *executed, "reading Help must not execute any listed action")
				helpAcceptanceSearchDisabled(t, app, owner, executed)
			})
		}
	}
}

func helpAcceptancePageThrough(t *testing.T, help *Help, screen tcell.SimulationScreen, width int) {
	t.Helper()
	help.SetRect(0, 0, width, 24)
	help.Focus(func(tview.Primitive) {})
	help.Draw(screen)
	first := investigationAcceptanceFrame(screen)
	require.Regexp(t, `<e>\s+A Change`, first, "visible shortcut and action remain paired:\n%s", first)
	require.Contains(t, first, "[red]", "configured labels must remain literal")
	require.NotContains(t, first, "HELP CONTROLS", "fixture must actually require scrolling")
	helpAcceptanceTextContrast(t, screen, width)
	var seen strings.Builder
	seen.WriteString(first)
	for range help.GetRowCount() + 24 {
		help.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), func(tview.Primitive) {})
		screen.Clear()
		help.Draw(screen)
		seen.WriteString(investigationAcceptanceFrame(screen))
	}
	for _, token := range []string{"RESOURCE", "DETAILS", "HOTKEYS", "HELP CONTROLS", "detailcomplete", "hotkeycomplete", "Page through all actions and reasons", helpAcceptanceLabelEnd, helpAcceptanceReasonEnd} {
		require.Contains(t, seen.String(), token, "keyboard-visible help content at width %d", width)
	}
	for _, category := range []string{ui.ActionInspect, ui.ActionNavigate, ui.ActionFilter, ui.ActionExport, ui.ActionChange} {
		require.Contains(t, seen.String(), category+helpAcceptanceLabelEnd)
		require.Contains(t, seen.String(), category+helpAcceptanceReasonEnd)
	}
	row, _ := help.GetOffset()
	require.Positive(t, row, "native Down must scroll the nonselectable help table")
	for range help.GetRowCount() {
		help.InputHandler()(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModNone), func(tview.Primitive) {})
	}
	screen.Clear()
	help.Draw(screen)
	require.Contains(t, investigationAcceptanceFrame(screen), "RESOURCE")
	help.InputHandler()(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone), func(tview.Primitive) {})
	row, _ = help.GetOffset()
	require.Positive(t, row, "advertised PgDn must page help")
}

func helpAcceptanceTextContrast(t *testing.T, screen tcell.Screen, width int) {
	t.Helper()
	measured := 0
	for y := 1; y < 23; y++ {
		for x := 2; x < width-2; x++ {
			char, _, style, _ := screen.GetContent(x, y)
			if unicode.IsSpace(char) {
				continue
			}
			foreground, background, _ := style.Decompose()
			ratio := config.ContrastRatio(foreground, background)
			if ratio > 0 {
				measured++
				require.GreaterOrEqual(t, ratio, 4.5, "native Help text at %d,%d", x, y)
			}
		}
	}
	require.Greater(t, measured, 10, "configured skin must provide measurable rendered text")
}

// Hidden shortcuts remain searchable by task category. Keyboard invocation of
// their disabled entries must retain the explanation without running handlers.
func helpAcceptanceSearchDisabled(t *testing.T, app *App, owner *helpAcceptanceOwner, executed *int) {
	t.Helper()
	for _, category := range []string{ui.ActionInspect, ui.ActionNavigate, ui.ActionFilter, ui.ActionExport, ui.ActionChange} {
		app.actionsCmd(nil)
		palette := app.Content.GetPrimitive(actionsCommand).(*actionPalette)
		for _, char := range category + " " + helpAcceptanceLabelEnd {
			palette.InputHandler()(tcell.NewEventKey(tcell.KeyRune, char, tcell.ModNone), func(tview.Primitive) {})
		}
		require.Len(t, palette.entries, 1)
		require.Equal(t, category, palette.entries[0].action.Category)
		require.Contains(t, palette.entries[0].label, helpAcceptanceReasonEnd)
		palette.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
		require.Same(t, palette, app.Content.GetPrimitive(actionsCommand))
		require.Zero(t, *executed)
		// This widget fixture does not run the full application's Flash
		// consumer. Read the actual refusal message before the next action.
		select {
		case message := <-app.Flash().Channel():
			require.Equal(t, palette.entries[0].action.UnavailableReason, message.Text)
		case <-time.After(time.Second):
			t.Fatal("disabled action did not explain why it could not execute")
		}
		palette.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(tview.Primitive) {})
		require.Same(t, owner, app.Content.Top())
	}
}
