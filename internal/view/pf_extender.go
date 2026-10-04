// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

// PortForwardExtender adds port-forward extensions.
type PortForwardExtender struct {
	ResourceViewer
}

// NewPortForwardExtender returns a new extender.
func NewPortForwardExtender(r ResourceViewer) ResourceViewer {
	p := PortForwardExtender{ResourceViewer: r}
	p.AddBindKeysFn(p.bindKeys)

	return &p
}

func (p *PortForwardExtender) bindKeys(aa *ui.KeyActions) {
	aa.Bulk(ui.KeyMap{
		ui.KeyF:      ui.NewKeyAction("Show PortForward", p.showPFCmd, true),
		ui.KeyShiftF: ui.NewKeyAction("Port-Forward", p.portFwdCmd, true),
	})
}

func (p *PortForwardExtender) portFwdCmd(evt *tcell.EventKey) *tcell.EventKey {
	path := p.GetTable().GetSelectedItem()
	if path == "" {
		return evt
	}

	if err := prepareForwardDialog(p, path); err != nil {
		p.App().Flash().Err(err)
	}

	return nil
}

func (p *PortForwardExtender) showPFCmd(evt *tcell.EventKey) *tcell.EventKey {
	path := p.GetTable().GetSelectedItem()
	if path == "" {
		return evt
	}

	target := selectedResourceForPath(p, p.App().Config.ActiveContextName(), path)
	p.App().openLocalSessionsForTarget(&target)
	return nil
}
