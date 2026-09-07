// SPDX-License-Identifier: Apache-2.0
package view

import (
	"strings"

	"github.com/derailed/tview"
)

// The explicit submit button is the opt-in for network activity. Opening this
// form never connects to a target or reads a CA file.
func (d *inspectionDetails) tlsForm(probe bool) {
	styles := d.app.Styles.Dialog()
	form := tview.NewForm().SetButtonsAlign(tview.AlignCenter)
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color())
	var address, hostname, ca string
	title, button := "Verify certificate", "Verify"
	message := "Offline server-certificate verification using system roots or an explicit local CA file. No endpoint contacted."
	if probe {
		title = "Probe TLS endpoint"
		message = "Connects FROM THE K9PLUS MACHINE, not from a pod. Verified TLS handshake only; no HTTP request. TLS 1.2 minimum. No client certificate. No revocation check."
		button = "Run TLS handshake"
		form.AddInputField("Address (host:port)", "", 40, nil, func(s string) { address = s })
	}
	form.AddInputField("Server name", "", 40, nil, func(s string) { hostname = s })
	form.AddInputField("CA file (empty = system)", "", 40, nil, func(s string) { ca = s })
	const page = "inspection-tls-form"
	dismiss := func() { d.app.Content.Pages.RemovePage(page); d.app.SetFocus(d) }
	form.AddButton("Cancel", dismiss)
	form.AddButton(button, func() {
		if d.contextName != d.app.Config.ActiveContextName() {
			dismiss()
			d.app.Flash().Warn("Context changed; reopen inspection")
			return
		}
		hostname, ca, address = strings.TrimSpace(hostname), strings.TrimSpace(ca), strings.TrimSpace(address)
		if hostname == "" || (probe && address == "") {
			d.app.Flash().Warn("Server name and probe address are required")
			return
		}
		for _, value := range []string{hostname, ca, address} {
			if strings.ContainsAny(value, " \t\r\n") {
				d.app.Flash().Warn("Whitespace in these fields is not supported")
				return
			}
		}
		command := "tlsverify " + hostname
		if probe {
			command = "tlsprobe " + address + " " + hostname
		}
		if ca != "" {
			command += " ca=" + ca
		}
		dismiss()
		c := &Command{app: d.app}
		c.tlsCheckCommand(command)
	})
	modal := tview.NewModalForm(title, form)
	modal.SetText(message)
	modal.SetTextColor(styles.FgColor.Color())
	modal.SetDoneFunc(func(int, string) { dismiss() })
	d.app.Content.Pages.AddPage(page, modal, false, true)
	d.app.SetFocus(modal)
}
