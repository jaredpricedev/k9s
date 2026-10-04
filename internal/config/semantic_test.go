// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package config_test

import (
	"encoding/json"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSemanticPaletteContrastIncluding256ColorQuantization(t *testing.T) {
	s := config.NewStyles()
	p := s.Semantic()
	palette := make([]tcell.Color, 256)
	for i := range palette {
		palette[i] = tcell.PaletteColor(i)
	}
	for _, fg := range []config.Color{p.Text, p.Muted, p.Focus, p.Healthy, p.Warning, p.Failure, p.Progress, p.Unknown, p.Category} {
		for _, bg := range []config.Color{p.Canvas, p.Panel, p.Selected} {
			assert.GreaterOrEqual(t, config.ContrastRatio(fg.Color(), bg.Color()), 4.5, "%s on %s", fg, bg)
			assert.GreaterOrEqual(t, config.ContrastRatio(tcell.FindColor(fg.Color(), palette), tcell.FindColor(bg.Color(), palette)), 4.5, "256-color %s on %s", fg, bg)
		}
	}
	d := s.Dialog()
	assert.GreaterOrEqual(t, config.ContrastRatio(d.ButtonFgColor.Color(), d.ButtonBgColor.Color()), 4.5)
	assert.GreaterOrEqual(t, config.ContrastRatio(d.ButtonFocusFgColor.Color(), d.ButtonFocusBgColor.Color()), 4.5)
	c := s.Charts()
	assert.GreaterOrEqual(t, config.ContrastRatio(c.FocusFgColor.Color(), c.FocusBgColor.Color()), 4.5)
	assert.GreaterOrEqual(t, config.ContrastRatio(s.K9s.Help.SectionColor.Color(), s.K9s.Help.BgColor.Color()), 4.5)
}

func TestDialogStyleSourceIsNotSerialized(t *testing.T) {
	styles := config.NewStyles()
	dialog := styles.Dialog()
	assert.Same(t, styles, dialog.StyleSource())
	encodedJSON, err := json.Marshal(dialog)
	require.NoError(t, err, "the source backlink must not create a JSON cycle")
	assert.NotContains(t, string(encodedJSON), "styleSource")
	encodedYAML, err := yaml.Marshal(dialog)
	require.NoError(t, err, "the source backlink must not create a YAML cycle")
	var decoded config.Dialog
	require.NoError(t, yaml.Unmarshal(encodedYAML, &decoded))
	assert.Nil(t, decoded.StyleSource())
	assert.Equal(t, dialog.FgColor, decoded.FgColor)
}

type paletteListener struct{ changes []config.SemanticPalette }

func (l *paletteListener) StylesChanged(s *config.Styles) {
	l.changes = append(l.changes, s.Semantic())
}

func TestSemanticSkinsFallbackAndRuntimeReset(t *testing.T) {
	s := config.NewStyles()
	l := &paletteListener{}
	s.AddListener(l)
	require.NoError(t, s.Load("../../skins/monochrome.yaml", false))
	s.Update()
	p := s.Semantic()
	assert.Equal(t, config.Color("#ffffff"), p.Text)
	assert.Equal(t, p.Text, p.Failure, "monochrome relies on status words and selection marker")
	assert.Equal(t, p.Text, s.Table().FgColor, "tokens reach inherited widgets")
	s.Reset(false)
	s.Update()
	assert.Len(t, l.changes, 2)
	assert.Equal(t, config.DefaultSemanticPalette().Failure, s.Semantic().Failure, "reset must clear semantic overrides")
	assert.NotEqual(t, l.changes[0].Failure, l.changes[1].Failure)
	require.NoError(t, s.Load("../../skins/black-and-wtf.yaml", false))
	p = s.Semantic()
	assert.Equal(t, s.Body().FgColor, p.Text)
	assert.Equal(t, s.Body().BgColor, p.Canvas)
	assert.Equal(t, s.K9s.Views.Yaml.KeyColor, p.Category)
	assert.Equal(t, config.DefaultSemanticPalette(), (*config.Styles)(nil).Semantic())
}

func TestLegacyLowContrastFocusPairsAreRepaired(t *testing.T) {
	s := config.NewStyles()
	s.K9s.Dialog.ButtonFgColor, s.K9s.Dialog.ButtonBgColor = "black", "darkslateblue"
	s.K9s.Views.Charts.FocusFgColor, s.K9s.Views.Charts.FocusBgColor = "white", "orange"
	d, c := s.Dialog(), s.Charts()
	assert.GreaterOrEqual(t, config.ContrastRatio(d.ButtonFgColor.Color(), d.ButtonBgColor.Color()), 4.5)
	assert.GreaterOrEqual(t, config.ContrastRatio(c.FocusFgColor.Color(), c.FocusBgColor.Color()), 4.5)
	assert.Equal(t, tcell.ColorDefault, config.ReadableForeground(tcell.ColorDefault, tcell.ColorBlack), "terminal-default colors are not guessed")
}
