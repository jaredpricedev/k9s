// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	suspendDialogKey     = "suspend"
	lastScheduledCol     = "LAST_SCHEDULE"
	defaultSuspendStatus = "true"
)

// CronJob represents a cronjob viewer.
type CronJob struct {
	ResourceViewer
}

// NewCronJob returns a new viewer.
func NewCronJob(gvr *client.GVR) ResourceViewer {
	c := CronJob{ResourceViewer: NewVulnerabilityExtender(
		NewOwnerExtender(NewBrowser(gvr)),
	)}
	c.AddBindKeysFn(c.bindKeys)
	c.GetTable().SetEnterFn(c.showJobs)

	return &c
}

func (*CronJob) showJobs(app *App, _ ui.Tabular, gvr *client.GVR, fqn string) {
	slog.Debug("Showing Jobs", slogs.GVR, gvr, slogs.FQN, fqn)
	o, err := app.factory.Get(gvr, fqn, true, labels.Everything())
	if err != nil {
		app.Flash().Err(err)
		return
	}

	var cj batchv1.CronJob
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.(*unstructured.Unstructured).Object, &cj)
	if err != nil {
		app.Flash().Err(err)
		return
	}

	ns, _ := client.Namespaced(fqn)
	if err := app.Config.SetActiveNamespace(ns); err != nil {
		slog.Error("Unable to set active namespace during show pods", slogs.Error, err)
	}
	v := NewJob(client.JobGVR)
	v.SetContextFn(jobCtx(fqn, string(cj.UID)))
	if err := app.inject(v, false); err != nil {
		app.Flash().Err(err)
	}
}

func jobCtx(fqn, uid string) ContextFunc {
	return func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, internal.KeyPath, fqn)
		return context.WithValue(ctx, internal.KeyUID, uid)
	}
}

func (c *CronJob) bindDangerousKeys(aa *ui.KeyActions) {
	aa.Bulk(ui.KeyMap{
		ui.KeyT: ui.NewKeyActionWithOpts("Trigger", c.triggerCmd,
			ui.ActionOpts{
				Visible:   true,
				Dangerous: true,
			}),
		ui.KeyS: ui.NewKeyActionWithOpts("Suspend/Resume", c.toggleSuspendCmd,
			ui.ActionOpts{
				Visible:   true,
				Dangerous: true,
			}),
	})
}

func (c *CronJob) bindKeys(aa *ui.KeyActions) {
	if !c.App().Config.IsReadOnly() {
		c.bindDangerousKeys(aa)
	}
}

func (c *CronJob) triggerCmd(evt *tcell.EventKey) *tcell.EventKey {
	if c.App().Config.IsReadOnly() {
		return evt
	}

	fqns := c.GetTable().GetSelectedItems()
	if len(fqns) == 0 {
		return evt
	}
	session, err := captureOperation(c)
	if err != nil {
		c.App().Flash().Err(err)
		return nil
	}
	targets, err := captureOperationTargets(c, session.context, fqns)
	if err != nil {
		c.App().Flash().Err(err)
		return nil
	}
	msg := fmt.Sprintf("Create a manual Job from %d CronJob(s)?\nContext: %s\n%s\nThe created Job retains the selected "+
		"CronJob owner UID. Acceptance does not mean the Job succeeded.", len(targets), session.context, operationDestination(targets))
	d := c.App().Styles.Dialog()
	dialog.ShowConfirm(&d, c.App().Content.Pages, "Confirm Job Trigger", msg, func() {
		session.submit("Trigger manual Job", targets, session.triggerCronJob, nil)
	}, func() {})

	return nil
}

func (c *CronJob) toggleSuspendCmd(evt *tcell.EventKey) *tcell.EventKey {
	if c.App().Config.IsReadOnly() {
		return evt
	}

	table := c.GetTable()
	sel := table.GetSelectedItem()

	if sel == "" {
		return evt
	}

	cell := table.GetCell(c.GetTable().GetSelectedRowIndex(), c.GetTable().NameColIndex()+2)

	if cell == nil {
		c.App().Flash().Errf("Unable to assert current status")
		return nil
	}

	c.showSuspendDialog(cell, sel)

	return nil
}

func (c *CronJob) showSuspendDialog(cell *tview.TableCell, sel string) {
	title := "Suspend"

	if strings.TrimSpace(cell.Text) == defaultSuspendStatus {
		title = "Resume"
	}

	session, err := captureOperation(c)
	if err != nil {
		c.App().Flash().Err(err)
		return
	}
	targets, err := captureOperationTargets(c, session.context, []string{sel})
	if err != nil {
		c.App().Flash().Err(err)
		return
	}
	expected := strings.TrimSpace(cell.Text) == defaultSuspendStatus
	msg := fmt.Sprintf("%s scheduling?\nContext: %s\n%s\nExisting Jobs continue. Missed-run behavior remains owned by the "+
		"CronJob controller.", title, session.context, operationDestination(targets))
	d := c.App().Styles.Dialog()
	dialog.ShowConfirm(&d, c.App().Content.Pages, title, msg, func() {
		session.submit(title+" CronJob", targets, func(ctx context.Context, target SelectedResourceTarget) error {
			return session.suspendCronJob(ctx, target, expected, !expected)
		}, nil)
	}, func() {})
}
