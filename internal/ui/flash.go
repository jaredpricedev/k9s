// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"context"
	"log/slog"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	emoHappy = "😎"
	emoDoh   = "😗"
	emoRed   = "😡"
)

// Flash represents a flash message indicator.
type Flash struct {
	*tview.TextView

	app      *App
	testMode bool
	message  model.LevelMessage
}

// NewFlash returns a new flash view.
func NewFlash(app *App) *Flash {
	f := Flash{
		app:      app,
		TextView: tview.NewTextView(),
	}
	f.SetTextColor(tcell.ColorAqua)
	f.SetDynamicColors(true)
	f.SetTextAlign(tview.AlignCenter)
	f.SetBorderPadding(0, 0, 1, 1)
	f.app.Styles.AddListener(&f)

	return &f
}

// SetTestMode for testing ONLY!
func (f *Flash) SetTestMode(b bool) {
	f.testMode = b
}

// StylesChanged notifies listener the skin changed.
func (f *Flash) StylesChanged(s *config.Styles) {
	p := s.Semantic()
	f.SetBackgroundColor(p.Canvas.Color())
	f.SetTextColor(f.messageColor(f.message.Level))
}

// Watch watches for flash changes.
func (f *Flash) Watch(ctx context.Context, c model.FlashChan) {
	defer slog.Debug("Flash Watch Canceled!")
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c:
			f.SetMessage(msg)
		}
	}
}

// SetMessage sets flash message and level.
func (f *Flash) SetMessage(m model.LevelMessage) {
	fn := func() {
		f.message = m
		if m.Text == "" {
			f.Clear()
			return
		}
		f.SetTextColor(f.messageColor(m.Level))
		label := "INFO"
		if m.Level == model.FlashWarn {
			label = "WARN"
		}
		if m.Level == model.FlashErr {
			label = "ERROR"
		}
		f.SetText(f.flashEmoji(m.Level) + " " + label + ": " + tview.Escape(m.Text))
	}

	if f.testMode {
		fn()
	} else {
		f.app.QueueUpdateDraw(fn)
	}
}

func (f *Flash) messageColor(level model.FlashLevel) tcell.Color {
	p := f.app.Styles.Semantic()
	color := p.Text
	if level == model.FlashWarn {
		color = p.Warning
	}
	if level == model.FlashErr {
		color = p.Failure
	}
	return config.ReadableForeground(color.Color(), p.Canvas.Color())
}

func (f *Flash) flashEmoji(l model.FlashLevel) string {
	if f.app.Config.K9s.UI.NoIcons {
		return ""
	}
	//nolint:exhaustive
	switch l {
	case model.FlashWarn:
		return emoDoh
	case model.FlashErr:
		return emoRed
	default:
		return emoHappy
	}
}
