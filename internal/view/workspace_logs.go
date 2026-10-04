// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/tview"
)

// workspaceLogEntry owns the cancellable identity check between a retained
// workspace observation and the native live log workbench.
type workspaceLogEntry struct {
	*Details
	cancel context.CancelFunc
}

func (w *workspaceLogEntry) Stop() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.Details.Stop()
}

func (*workspaceLogEntry) CompactWorkspace() bool { return true }

//nolint:gocritic // The captured identity remains an immutable value across the read.
func (a *App) dailyWorkspaceOpenLogs(target SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if target.GVR != client.PodGVR {
		return fmt.Errorf("select a Pod to open logs; inspect workload relationships to choose its Pod")
	}
	if target.Context != a.Config.ActiveContextName() {
		return fmt.Errorf("context changed; reopen the saved workspace in its context")
	}
	conn, err := pinInspectionConnection(a.Conn())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	w := &workspaceLogEntry{Details: NewDetails(a, "Open Pod logs", target.Path(), contentInspection, false).
		Update("Checking captured Pod identity before opening live logs..."), cancel: cancel}
	if err := a.inject(w, false); err != nil {
		cancel()
		return err
	}
	revision := a.Config.DestinationRevision()
	go func() {
		observed, err := loadRelatedTarget(ctx, conn, target)
		if !a.IsRunning() {
			cancel()
			return
		}
		a.QueueUpdateDraw(func() {
			defer cancel()
			if a.Content.Top() != w || a.Config.ActiveContextName() != target.Context ||
				a.Config.DestinationRevision() != revision {
				return
			}
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if err != nil {
				w.Update("Logs were not opened: " + tview.Escape(logstream.Sanitize(err.Error())) + "\n\nEsc returns to the saved workspace.")
				a.Flash().Err(err)
				return
			}
			cfg := a.Config.K9s.Logger
			opts := &dao.LogOptions{Path: observed.Path(), InitialPodUID: string(observed.UID),
				Lines: cfg.TailCount, SinceSeconds: cfg.SinceSeconds, ShowTimestamp: cfg.ShowTime,
				LogBufferSize: cfg.LogBufferSize, AllContainers: true}
			a.PrevCmd(nil)
			if err := a.inject(NewLog(client.PodGVR, opts), false); err != nil {
				a.Flash().Err(err)
			}
		})
	}()
	return nil
}
