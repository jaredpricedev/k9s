// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// ScaleExtender adds scaling extensions.
type ScaleExtender struct {
	ResourceViewer
	readCancel   context.CancelFunc
	styleCleanup func()
}

// NewScaleExtender returns a viewer with scaling actions.
func NewScaleExtender(r ResourceViewer) ResourceViewer {
	s := ScaleExtender{ResourceViewer: r}
	s.AddBindKeysFn(s.bindKeys)
	return &s
}

func (s *ScaleExtender) bindKeys(aa *ui.KeyActions) {
	if s.App().Config.IsReadOnly() {
		return
	}
	meta, err := dao.MetaAccess.MetaFor(s.GVR())
	if err != nil {
		slog.Error("No meta information found", slogs.GVR, s.GVR(), slogs.Error, err)
		return
	}
	if dao.IsScalable(meta) {
		aa.Add(ui.KeyS, ui.NewKeyActionWithOpts("Scale", s.scaleCmd, ui.ActionOpts{Visible: true, Dangerous: true}))
	}
}

func (s *ScaleExtender) scaleCmd(*tcell.EventKey) *tcell.EventKey {
	paths := s.GetTable().GetSelectedItems()
	if len(paths) == 0 {
		return nil
	}
	session, err := captureOperation(s)
	if err != nil {
		s.App().Flash().Err(err)
		return nil
	}
	targets, err := captureOperationTargets(s, session.context, paths)
	if err != nil {
		s.App().Flash().Err(err)
		return nil
	}
	if len(targets) > 1 {
		s.showScaleDialog(session, targets, "0", "")
		return nil
	}

	// The desired replica count can require a /scale request. Keep that read
	// off the input thread and give the user an immediate way to cancel it.
	s.dismissDialog()
	ctx, cancel := context.WithTimeout(context.Background(), session.timeout)
	s.readCancel = cancel
	styles := s.App().Styles.Dialog()
	form := tview.NewForm().SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color())
	loading := tview.NewModalForm("<Scale>", form)
	loading.SetText(fmt.Sprintf("Loading desired replicas in context %s...\n%s", session.context, operationDestination(targets)))
	dismiss := func() { cancel(); s.dismissDialog() }
	form.AddButton("Cancel", dismiss)
	loading.SetDoneFunc(func(int, string) { dismiss() })
	s.styleCleanup = dialog.BindFormStyles(s.App().Styles, form, loading)
	s.App().Content.AddPage(scaleDialogKey, loading, false, false)
	s.App().Content.ShowPage(scaleDialogKey)
	go func() {
		defer cancel()
		count, err := session.replicas(ctx, targets[0])
		if ctx.Err() == context.Canceled {
			return
		}
		session.dispatch(func() {
			if s.App().Content.GetPrimitive(scaleDialogKey) != loading {
				return
			}
			factor, notice := "", ""
			if err != nil {
				notice = "Current desired replicas unavailable: " + operationError(err) + "\nEnter the intended count explicitly."
			} else {
				factor = strconv.FormatInt(count, 10)
			}
			s.showScaleDialog(session, targets, factor, notice)
		})
	}()
	return nil
}

func (s *ScaleExtender) showScaleDialog(session *operationSession, targets []SelectedResourceTarget, factor, notice string) {
	s.dismissDialog()
	styles := s.App().Styles.Dialog()
	f := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).SetFieldTextColor(styles.FieldFgColor.Color())
	f.AddInputField("Replicas:", factor, 10, func(text string, _ rune) bool {
		if text == "" {
			return true
		}
		_, err := parseReplicaCount(text)
		return err == nil
	}, func(changed string) { factor = changed })
	f.AddButton("OK", func() {
		count, err := parseReplicaCount(factor)
		if err != nil {
			s.App().Flash().Err(err)
			return
		}
		s.dismissDialog()
		session.submit(fmt.Sprintf("Scale to %d", count), targets, func(ctx context.Context, target SelectedResourceTarget) error {
			return session.scale(ctx, target, count)
		}, nil)
	})
	f.AddButton("Cancel", s.dismissDialog)
	for i := range f.GetButtonCount() {
		f.GetButton(i).SetBackgroundColorActivated(styles.ButtonFocusBgColor.Color()).SetLabelColorActivated(styles.ButtonFocusFgColor.Color())
	}
	confirm := tview.NewModalForm("<Scale>", f)
	msg := fmt.Sprintf("Scale in context %s?\n%s\n\nDesired replicas are submitted to the API; watch READY / STATUS for completion.",
		session.context, operationDestination(targets))
	if notice != "" {
		msg += "\n\n" + notice
	}
	confirm.SetText(msg)
	confirm.SetDoneFunc(func(int, string) { s.dismissDialog() })
	s.styleCleanup = dialog.BindFormStyles(s.App().Styles, f, confirm)
	s.App().Content.AddPage(scaleDialogKey, confirm, false, false)
	s.App().Content.ShowPage(scaleDialogKey)
}

func parseReplicaCount(text string) (int32, error) {
	n, err := strconv.ParseInt(text, 10, 32)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("replicas must be between 0 and 2147483647")
	}
	return int32(n), nil
}

func (s *ScaleExtender) dismissDialog() {
	if s.readCancel != nil {
		s.readCancel()
		s.readCancel = nil
	}
	if s.styleCleanup != nil {
		s.styleCleanup()
		s.styleCleanup = nil
	}
	if s.App() != nil {
		s.App().Content.RemovePage(scaleDialogKey)
	}
}

// Stop cancels a waiting replica read and releases live dialog style listeners.
// A confirmed write belongs to the operation runner and is not undone by Stop.
func (s *ScaleExtender) Stop() {
	s.dismissDialog()
	s.ResourceViewer.Stop()
}
