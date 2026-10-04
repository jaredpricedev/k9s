// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
)

const drainKey = "drain"

// DrainFunc represents a drain callback function.
type DrainFunc func(v ResourceViewer, sels []string, opts dao.DrainOptions)

// ShowDrain pops a node drain dialog.
func ShowDrain(view ResourceViewer, sels []string, opts dao.DrainOptions, okFn DrainFunc) {
	if view.App().Config.IsReadOnly() {
		view.App().Flash().Warn("Drain is unavailable in read-only mode")
		return
	}
	// Retain the reviewed destination even if the caller changes its selection.
	sels = slices.Clone(sels)
	contextName := view.App().Config.ActiveContextName()
	destinationRevision := view.App().Config.DestinationRevision()
	styles := view.App().Styles.Dialog()

	f := tview.NewForm().
		SetItemPadding(0).
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(styles.ButtonBgColor.Color()).
		SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).
		SetFieldTextColor(styles.FieldFgColor.Color()).
		SetFieldBackgroundColor(styles.BgColor.Color())

	// Allow replacement values to be incomplete while typing. Submission reads
	// these widgets together; no previously parsed value can survive an edit.
	f.AddInputField("GracePeriod:", strconv.Itoa(opts.GracePeriodSeconds), 0, nil, nil)
	f.AddInputField("Timeout:", opts.Timeout.String(), 0, nil, nil)
	f.AddCheckbox("Ignore DaemonSets:", opts.IgnoreAllDaemonSets, nil)
	f.AddCheckbox("Delete EmptyDir Data:", opts.DeleteEmptyDirData, nil)
	f.AddCheckbox("Force:", opts.Force, nil)
	f.AddCheckbox("Disable Eviction:", opts.DisableEviction, nil)

	pages := view.App().Content.Pages
	closed := false
	dismiss := func() {
		if closed {
			return
		}
		closed = true
		DismissDrain(view, pages)
	}

	modal := tview.NewModalForm("<Drain>", f)
	path := "Drain "
	if len(sels) == 1 {
		path += sels[0]
	} else {
		path += fmt.Sprintf("(%d) nodes", len(sels))
	}
	path += "?"
	message := tview.Escape(path+"\nContext: "+contextName) +
		"\nGrace: seconds; -1 = Pod default, 0 = immediate." +
		"\nTimeout: duration (5s, 2m); 0 = native no-timeout." +
		"\nThe operation wait remains bounded (at most 10m); :operations cancels remaining work."
	modal.SetText(message)
	f.AddButton("Cancel", dismiss)
	f.AddButton("OK", func() {
		if closed {
			return
		}
		current, invalidField, err := drainFormOptions(f)
		if err != nil {
			modal.SetText(message + "\n\n" + tview.Escape(err.Error()))
			f.SetFocus(invalidField)
			view.App().SetFocus(f)
			return
		}
		if view.App().Config.IsReadOnly() {
			modal.SetText(message + "\n\nDrain unavailable: read-only mode is active.")
			return
		}
		if view.App().Config.ActiveContextName() != contextName || view.App().Config.DestinationRevision() != destinationRevision {
			modal.SetText(message + "\n\nDestination changed. Cancel and reopen the drain form.")
			return
		}
		dismiss()
		okFn(view, sels, current)
	})
	modal.SetDoneFunc(func(int, string) {
		dismiss()
	})

	pages.AddPage(drainKey, modal, false, true)
	pages.ShowPage(drainKey)
	view.App().SetFocus(pages.GetPrimitive(drainKey))
}

// DismissDrain dismisses the drain dialog.
func DismissDrain(v ResourceViewer, p *ui.Pages) {
	p.RemovePage(drainKey)
	if page := p.CurrentPage(); page != nil {
		v.App().SetFocus(page.Item)
	}
}

// ----------------------------------------------------------------------------
// Helpers...

func asDurOpt(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < 0 {
		return 0, errors.New("Timeout: enter a duration >= 0 (5s, 2m); 0 = no timeout")
	}

	return d, nil
}

func asIntOpt(v string) (int, error) {
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || i < -1 {
		return 0, errors.New("GracePeriod: enter whole seconds >= 0, or -1 for the Pod default")
	}

	return i, nil
}

// drainFormOptions validates the current visible values as one submission. Both
// errors remain in the dialog until the next submission; the first invalid
// field receives focus. Zero timeout deliberately retains kubectl's sentinel.
func drainFormOptions(f *tview.Form) (dao.DrainOptions, int, error) {
	grace, graceErr := asIntOpt(f.GetFormItem(0).(*tview.InputField).GetText())
	timeout, timeoutErr := asDurOpt(f.GetFormItem(1).(*tview.InputField).GetText())
	firstInvalid := -1
	if timeoutErr != nil {
		firstInvalid = 1
	}
	if graceErr != nil {
		firstInvalid = 0
	}
	if err := errors.Join(graceErr, timeoutErr); err != nil {
		return dao.DrainOptions{}, firstInvalid, err
	}
	return dao.DrainOptions{
		GracePeriodSeconds:  grace,
		Timeout:             timeout,
		IgnoreAllDaemonSets: f.GetFormItem(2).(*tview.Checkbox).IsChecked(),
		DeleteEmptyDirData:  f.GetFormItem(3).(*tview.Checkbox).IsChecked(),
		Force:               f.GetFormItem(4).(*tview.Checkbox).IsChecked(),
		DisableEviction:     f.GetFormItem(5).(*tview.Checkbox).IsChecked(),
	}, firstInvalid, nil
}
