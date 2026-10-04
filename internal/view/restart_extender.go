// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RestartExtender represents a restartable resource.
type RestartExtender struct {
	ResourceViewer
}

// NewRestartExtender returns a new extender.
func NewRestartExtender(v ResourceViewer) ResourceViewer {
	r := RestartExtender{ResourceViewer: v}
	v.AddBindKeysFn(r.bindKeys)

	return &r
}

// BindKeys creates additional menu actions.
func (r *RestartExtender) bindKeys(aa *ui.KeyActions) {
	if r.App().Config.IsReadOnly() {
		return
	}
	aa.Add(ui.KeyR, ui.NewKeyActionWithOpts("Restart", r.restartCmd,
		ui.ActionOpts{
			Visible:   true,
			Dangerous: true,
		},
	))
}

func (r *RestartExtender) restartCmd(*tcell.EventKey) *tcell.EventKey {
	paths := r.GetTable().GetSelectedItems()
	if len(paths) == 0 || paths[0] == "" {
		return nil
	}

	session, err := captureOperation(r)
	if err != nil {
		r.App().Flash().Err(err)
		return nil
	}
	targets, err := captureOperationTargets(r, session.context, paths)
	if err != nil {
		r.App().Flash().Err(err)
		return nil
	}
	msg := fmt.Sprintf("Restart in context %s?\n%s\n\nAPI acceptance starts the rollout; watch READY / STATUS for completion.",
		session.context, operationDestination(targets))
	d := r.App().Styles.Dialog()

	opts := dialog.RestartDialogOpts{
		Title:        "Confirm Restart",
		Message:      msg,
		FieldManager: "kubectl-rollout",
		Ack: func(opts *metav1.PatchOptions) bool {
			if !session.confirm() {
				return true
			}
			intent := *opts.DeepCopy()
			session.submit("Restart", targets, func(ctx context.Context, target SelectedResourceTarget) error {
				return session.restart(ctx, target, intent)
			}, nil)
			return true
		},
		Cancel: func() {},
	}
	dialog.ShowRestart(&d, r.App().Content.Pages, &opts)

	return nil
}
