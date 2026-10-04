// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"

	"github.com/derailed/k9s/internal/logstream"
)

func (w *logWorkbench) historyDir() string {
	if w.historyPath != "" {
		return w.historyPath
	}
	return w.writerState().path
}
func (w *logWorkbench) beginIO(work func(context.Context) logIOResult) {
	if w.ioCancel != nil {
		w.ioCancel()
	}
	w.ioGeneration++
	gen := w.ioGeneration
	ctx, cancel := context.WithCancel(context.Background())
	w.ioCancel = cancel
	w.ioRunning = true
	go func() {
		result := work(ctx)
		result.generation = gen
		select {
		case w.ioResults <- result:
		case <-ctx.Done():
		}
	}()
}
func (w *logWorkbench) readHistory(search bool) {
	dir := w.historyDir()
	if dir == "" {
		w.notice = "No recording selected; R starts safe recording, O opens a session"
		return
	}
	before := w.historyBefore
	var q *logstream.Query
	if search {
		q = w.query
	}
	w.freeze()
	w.beginIO(func(ctx context.Context) logIOResult {
		entries, err := scanLogHistory(ctx, dir, before, q, nil)
		notice := "Recorded history (200/page); [ older, ] latest; Esc live"
		if errors.Is(err, errLogHistoryTornTail) {
			notice += "; torn final record ignored; durability uncertain"
			err = nil
		}
		return logIOResult{entries: entries, path: dir, notice: notice, err: err}
	})
}
func (w *logWorkbench) openSessions() {
	root := w.recordingRoot()
	w.beginIO(func(ctx context.Context) logIOResult {
		paths, err := listLogSessionsContext(ctx, root)
		if paths == nil {
			paths = []string{}
		}
		return logIOResult{sessions: paths, notice: "Read-only session picker; Enter opens; live capture continues", err: err}
	})
}
func (w *logWorkbench) consumeIO() {
	for {
		select {
		case result := <-w.ioResults:
			if result.generation != w.ioGeneration {
				continue
			}
			w.ioRunning = false
			if result.err != nil {
				w.notice = "Disk ERROR: " + result.err.Error()
				continue
			}
			w.notice = result.notice
			if result.sessions != nil {
				w.sessions = result.sessions
				w.mode = modeSessions
				w.headers("Recorded session (read-only)")
				for i, path := range w.sessions {
					w.table.SetCell(i+1, 0, w.newCell(wbText(path)).SetExpansion(1))
				}
				w.table.Select(1, 0)
				w.pages.SwitchToPage("table")
				w.focus(w.table)
			} else if result.path != "" {
				w.historyPath = result.path
				w.history = result.entries
				w.mode = modeHistory

				if len(result.entries) > 0 {
					w.historyBefore = result.entries[0].ID
				}
				w.focus(w.table)
			}
		default:
			return
		}
	}
}
