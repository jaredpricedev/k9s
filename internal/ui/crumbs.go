// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	runewidth "github.com/mattn/go-runewidth"
)

// Crumbs represents user breadcrumbs.
type Crumbs struct {
	*tview.TextView

	styles *config.Styles
	stack  *model.Stack
	width  int
}

// NewCrumbs returns a new breadcrumb view.
func NewCrumbs(styles *config.Styles) *Crumbs {
	c := Crumbs{
		stack:    model.NewStack(),
		styles:   styles,
		TextView: tview.NewTextView(),
	}
	c.SetBackgroundColor(styles.BgColor())
	c.SetTextAlign(tview.AlignLeft)
	c.SetBorderPadding(0, 0, 1, 1)
	c.SetDynamicColors(true)
	c.SetWrap(false)
	styles.AddListener(&c)

	return &c
}

// StylesChanged notifies skin changed.
func (c *Crumbs) StylesChanged(s *config.Styles) {
	c.styles = s
	c.SetBackgroundColor(s.BgColor())
	c.refresh(c.stack.Flatten())
}

// StackPushed indicates a new item was added.
func (c *Crumbs) StackPushed(comp model.Component) {
	c.stack.Push(comp)
	c.refresh(c.stack.Flatten())
}

// StackPopped indicates an item was deleted.
func (c *Crumbs) StackPopped(_, _ model.Component) {
	c.stack.Pop()
	c.refresh(c.stack.Flatten())
}

// StackTop indicates the top of the stack.
func (*Crumbs) StackTop(model.Component) {}

// Draw keeps the current view visible when the terminal changes size.
func (c *Crumbs) Draw(screen tcell.Screen) {
	if _, _, width, _ := c.GetInnerRect(); width != c.width {
		c.width = width
		c.refresh(c.stack.Flatten())
	}
	c.TextView.Draw(screen)
}

// Refresh updates view with new crumbs.
func (c *Crumbs) refresh(crumbs []string) {
	c.Clear()
	labels := make([]string, len(crumbs))
	var total int
	for i, crumb := range crumbs {
		labels[i] = strings.Join(strings.Fields(strings.ToLower(crumb)), " ")
		total += runewidth.StringWidth(labels[i]) + 5
	}
	if c.width > 0 && total > c.width && len(labels) > 0 {
		last := len(labels) - 1
		start, used := last, runewidth.StringWidth(labels[last])+5
		for start > 0 && used+runewidth.StringWidth(labels[start-1])+5+2 <= c.width {
			start--
			used += runewidth.StringWidth(labels[start]) + 5
		}
		labels = labels[start:]
		available := c.width
		if start > 0 && available > 7 {
			_, _ = fmt.Fprint(c, "… ")
			available -= 2
		}
		if used > available {
			if available <= 5 {
				_, _ = fmt.Fprintf(c, "[%s:%s:b]%s", c.styles.Frame().Crumb.FgColor, c.styles.Frame().Crumb.ActiveColor, tview.Escape(Truncate(labels[len(labels)-1], available)))
				return
			}
			labels[len(labels)-1] = Truncate(labels[len(labels)-1], available-5)
		}
	}
	last, bgColor := len(labels)-1, c.styles.Frame().Crumb.BgColor
	for i, crumb := range labels {
		if i == last {
			bgColor = c.styles.Frame().Crumb.ActiveColor
		}
		_, _ = fmt.Fprintf(c, "[%s:%s:b] <%s> [-:%s:-] ",
			c.styles.Frame().Crumb.FgColor,
			bgColor, tview.Escape(crumb),
			c.styles.Body().BgColor)
	}
}
