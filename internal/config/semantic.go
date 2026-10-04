// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package config

import (
	"math"

	"github.com/derailed/tcell/v2"
)

// SemanticPalette gives colors a meaning shared by all views. Categories are
// deliberately separate from health. Status must also be expressed in words.
// Every field is optional in skins; omitted fields inherit the legacy palette.
type SemanticPalette struct {
	Canvas   Color `json:"canvas,omitempty" yaml:"canvas,omitempty"`
	Panel    Color `json:"panel,omitempty" yaml:"panel,omitempty"`
	Text     Color `json:"text,omitempty" yaml:"text,omitempty"`
	Muted    Color `json:"muted,omitempty" yaml:"muted,omitempty"`
	Focus    Color `json:"focus,omitempty" yaml:"focus,omitempty"`
	Healthy  Color `json:"healthy,omitempty" yaml:"healthy,omitempty"`
	Warning  Color `json:"warning,omitempty" yaml:"warning,omitempty"`
	Failure  Color `json:"failure,omitempty" yaml:"failure,omitempty"`
	Progress Color `json:"progress,omitempty" yaml:"progress,omitempty"`
	Unknown  Color `json:"unknown,omitempty" yaml:"unknown,omitempty"`
	Category Color `json:"category,omitempty" yaml:"category,omitempty"`
	Selected Color `json:"selected,omitempty" yaml:"selected,omitempty"`
}

// DefaultSemanticPalette is the stock presentation, also usable before an App
// exists. The foreground pairs target at least 4.5:1 against both surfaces.
func DefaultSemanticPalette() SemanticPalette {
	return SemanticPalette{
		Canvas: "#0b0e11", Panel: "#131a1e", Text: "#e1e7e3",
		Muted: "#a5b0aa", Focus: "#79c7d4", Healthy: "#8fce88",
		Warning: "#e7bd73", Failure: "#ef8278", Progress: "#91b8ec",
		Unknown: "#a5b0aa", Category: "#b6a3df", Selected: "#1c292f",
	}
}

// Semantic resolves optional tokens through existing skin fields. A nil Styles
// uses stock colors, which keeps standalone inspectors and fixtures consistent.
func (s *Styles) Semantic() SemanticPalette {
	d := DefaultSemanticPalette()
	if s == nil {
		return d
	}
	p := s.K9s.Semantic
	resolve := func(token, legacy, stock Color) Color {
		if token != "" {
			return token
		}
		if legacy != "" {
			return legacy
		}
		return stock
	}
	p.Canvas = resolve(p.Canvas, s.Body().BgColor, d.Canvas)
	p.Panel = resolve(p.Panel, s.K9s.Dialog.BgColor, d.Panel)
	p.Text = resolve(p.Text, s.Body().FgColor, d.Text)
	p.Muted = resolve(p.Muted, s.Frame().Status.CompletedColor, d.Muted)
	p.Focus = resolve(p.Focus, s.Frame().Border.FocusColor, d.Focus)
	p.Healthy = resolve(p.Healthy, s.Frame().Status.ModifyColor, d.Healthy)
	p.Warning = resolve(p.Warning, s.Frame().Status.PendingColor, d.Warning)
	p.Failure = resolve(p.Failure, s.Frame().Status.ErrorColor, d.Failure)
	p.Progress = resolve(p.Progress, s.Frame().Status.AddColor, d.Progress)
	p.Unknown = resolve(p.Unknown, p.Muted, d.Unknown)
	p.Category = resolve(p.Category, s.K9s.Views.Yaml.KeyColor, d.Category)
	p.Selected = resolve(p.Selected, s.Table().CursorBgColor, d.Selected)
	return p
}

// Invert keeps semantic overrides in step with the legacy light theme switch.
func (p *SemanticPalette) Invert() {
	for _, color := range []*Color{&p.Canvas, &p.Panel, &p.Text, &p.Muted, &p.Focus,
		&p.Healthy, &p.Warning, &p.Failure, &p.Progress, &p.Unknown, &p.Category, &p.Selected} {
		*color = color.InvertColor()
	}
}

// ContrastRatio reports the WCAG sRGB contrast benchmark. Terminal-default
// colors cannot be measured, so they return 0 instead of an invented guarantee.
func ContrastRatio(fg, bg tcell.Color) float64 {
	if fg.Hex() < 0 || bg.Hex() < 0 {
		return 0
	}
	luminance := func(c tcell.Color) float64 {
		r, g, b := c.RGB()
		linear := func(n int32) float64 {
			x := float64(n) / 255
			if x <= 0.04045 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
	}
	a, b := luminance(fg), luminance(bg)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}

// ReadableForeground adjusts an insufficiently contrasting custom foreground
// toward black or white, preserving its hue when possible. Explicit terminal
// defaults are respected because their RGB values are owned by the terminal.
func ReadableForeground(fg, bg tcell.Color) tcell.Color {
	if ratio := ContrastRatio(fg, bg); ratio == 0 || ratio >= 4.5 {
		return fg
	}
	r, g, b := fg.RGB()
	target := int32(255)
	if ContrastRatio(tcell.ColorBlack, bg) > ContrastRatio(tcell.ColorWhite, bg) {
		target = 0
	}
	for step := 1; step <= 20; step++ {
		mix := func(n int32) int32 { return n + (target-n)*int32(step)/20 }
		c := tcell.NewRGBColor(mix(r), mix(g), mix(b))
		if ContrastRatio(c, bg) >= 4.5 {
			return c
		}
	}
	return tcell.NewRGBColor(target, target, target)
}

// applySemanticOverrides feeds optional tokens into inherited widgets as well
// as newer views. Skins without tokens continue to use their legacy fields.
func (s *Styles) applySemanticOverrides() {
	p, k := s.K9s.Semantic, &s.K9s
	set := func(value Color, targets ...*Color) {
		if value == "" {
			return
		}
		for _, target := range targets {
			*target = value
		}
	}
	set(p.Canvas, &k.Body.BgColor, &k.Prompt.BgColor, &k.Help.BgColor,
		&k.Frame.Title.BgColor, &k.Views.Table.BgColor, &k.Views.Table.Header.BgColor,
		&k.Views.Log.BgColor, &k.Views.Log.Indicator.BgColor, &k.Views.Xray.BgColor,
		&k.Views.Charts.BgColor, &k.Views.Charts.DialBgColor, &k.Views.Charts.ChartBgColor)
	set(p.Panel, &k.Dialog.BgColor, &k.Frame.Crumb.BgColor)
	set(p.Text, &k.Body.FgColor, &k.Body.LogoColorMsg, &k.Help.FgColor,
		&k.Prompt.FgColor, &k.Frame.Title.FgColor, &k.Frame.Menu.FgColor,
		&k.Frame.Crumb.FgColor, &k.Frame.Status.NewColor, &k.Info.FgColor,
		&k.Views.Table.FgColor, &k.Views.Table.CursorFgColor, &k.Views.Log.FgColor,
		&k.Views.Xray.FgColor, &k.Views.Xray.CursorTextColor, &k.Views.Picker.MainColor,
		&k.Views.Yaml.ValueColor, &k.Views.Yaml.ColonColor, &k.Dialog.FgColor,
		&k.Dialog.ButtonFgColor, &k.Dialog.LabelFgColor, &k.Dialog.FieldFgColor)
	set(p.Muted, &k.Frame.Border.FgColor, &k.Frame.Title.CounterColor,
		&k.Frame.Status.CompletedColor, &k.Views.Table.Header.FgColor,
		&k.Views.Log.Indicator.FgColor, &k.Views.Log.Indicator.ToggleOffColor,
		&k.Views.Xray.GraphicColor, &k.Info.SectionColor, &k.Prompt.SuggestColor)
	set(p.Focus, &k.Frame.Border.FocusColor, &k.Frame.Menu.KeyColor,
		&k.Frame.Title.HighlightColor, &k.Frame.Title.FilterColor,
		&k.Help.SectionColor, &k.Help.KeyColor, &k.Prompt.Border.CommandColor,
		&k.Prompt.Border.DefaultColor, &k.Body.LogoColor, &k.Frame.Crumb.ActiveColor,
		&k.Views.Table.Header.SorterColor, &k.Views.Table.Header.SelectedSortColumnColor,
		&k.Views.Picker.FocusColor, &k.Views.Picker.ShortcutColor, &k.Views.Log.Indicator.ToggleOnColor,
		&k.Views.Charts.FocusBgColor, &k.Dialog.ButtonFocusBgColor)
	set(p.Category, &k.Frame.Menu.NumKeyColor, &k.Help.NumKeyColor,
		&k.Views.Yaml.KeyColor, &k.Views.Table.MarkColor, &k.Info.MEMColor)
	set(p.Healthy, &k.Frame.Status.ModifyColor)
	set(p.Warning, &k.Frame.Status.PendingColor, &k.Body.LogoColorWarn)
	set(p.Failure, &k.Frame.Status.ErrorColor, &k.Body.LogoColorError)
	set(p.Progress, &k.Frame.Status.AddColor, &k.Body.LogoColorInfo, &k.Info.CPUColor)
	set(p.Unknown, &k.Frame.Status.CompletedColor, &k.Frame.Status.KillColor)
	set(p.Selected, &k.Views.Table.CursorBgColor, &k.Views.Xray.CursorColor, &k.Dialog.ButtonBgColor)
	resolved := s.Semantic()
	if p.Healthy != "" || p.Failure != "" {
		k.Views.Charts.DefaultDialColors = Colors{resolved.Healthy, resolved.Failure}
		k.Views.Charts.DefaultChartColors = Colors{resolved.Healthy, resolved.Failure}
	}
	if k.Views.Charts.ResourceColors == nil {
		k.Views.Charts.ResourceColors = make(map[string]Colors)
	}
	if p.Progress != "" {
		k.Views.Charts.ResourceColors[CPU] = Colors{resolved.Progress, resolved.Selected}
	}
	if p.Category != "" {
		k.Views.Charts.ResourceColors[MEM] = Colors{resolved.Category, resolved.Selected}
		k.Views.Charts.ResourceColors["mem"] = Colors{resolved.Category, resolved.Selected}
	}
}
