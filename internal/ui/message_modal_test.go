// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageModalPreservesLiteralDestinationAndLabelsAcrossSkins(t *testing.T) {
	styles := config.NewStyles()
	text := "Context: production[blue]-東京\nNamespace: payments\nMode: read-only\nConnection: CONNECTED\n" +
		"Cluster: demo-cluster\nUser: [red]operator\nMetrics: unavailable\n" +
		"Source: metrics.k8s.io/v1beta1/nodes\nReason: permission denied [RBAC]"
	closed := false
	modal := ui.NewMessageModal(styles, "Destination", text, func() { closed = true })
	defer modal.Cleanup()
	modal.Focus(func(p tview.Primitive) { p.Focus(func(tview.Primitive) {}) })
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(80, 24)
	for _, skin := range []string{"", "../../skins/high-contrast.yaml", "../../skins/monochrome.yaml"} {
		styles.Reset(false)
		if skin != "" {
			require.NoError(t, styles.Load(skin, false))
		}
		styles.Update()
		screen.Clear()
		modal.Draw(screen)
		actual := messageScreenText(screen, 80, 24)
		for _, line := range strings.Split(text, "\n") {
			assert.Contains(t, actual, line, "literal labels and identity survive %q", skin)
		}
		assert.Contains(t, actual, "Dismiss")
		assert.True(t, modal.HasFocus(), "skin updates retain the focused close control")
	}
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(tview.Primitive) {})
	assert.True(t, closed)
	pages := ui.NewPages()
	pages.AddPage("dialog", modal, false, true)
	assert.True(t, pages.IsTopDialog(), "resource keys remain blocked while the information modal is open")
	modal.Cleanup()
	modal.Cleanup()
	background := modal.GetBackgroundColor()
	styles.Reset(false)
	styles.Update()
	assert.Equal(t, background, modal.GetBackgroundColor(), "dismissal detaches the style listener")
}

func TestMessageModalScrollsCompleteLongDestination(t *testing.T) {
	styles := config.NewStyles()
	lines := []string{"Context: " + strings.Repeat("東京[production]", 12)}
	for range 24 {
		lines = append(lines, "Observation source and diagnostic reason remain available.")
	}
	lines = append(lines, "Final source/reason: fixture complete")
	modal := ui.NewMessageModal(styles, "Destination", strings.Join(lines, "\n"), func() {})
	defer modal.Cleanup()
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(80, 24)
	modal.Draw(screen)
	for range 4 {
		modal.InputHandler()(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone), func(tview.Primitive) {})
		modal.Draw(screen)
	}
	assert.Contains(t, layoutScreenText(screen, 80, 24), "Final source/reason: fixture complete")
}

func messageScreenText(screen tcell.Screen, width, height int) string {
	var text strings.Builder
	for y := range height {
		for x := 0; x < width; {
			ch, combining, _, cells := screen.GetContent(x, y)
			text.WriteRune(ch)
			text.WriteString(string(combining))
			x += max(1, cells)
		}
		text.WriteByte('\n')
	}
	return text.String()
}
