// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/tcell/v2"
)

const shutdownTimeout = 5 * time.Second

type appLifecycle struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	requested atomic.Bool
	code      atomic.Int32
}

// TerminationError preserves conventional shell termination statuses after
// tview has restored the terminal and application resources have been stopped.
type TerminationError struct{ Code int }

func (e *TerminationError) Error() string { return fmt.Sprintf("k9+ terminated (status %d)", e.Code) }
func (e *TerminationError) ExitCode() int { return e.Code }

func (a *App) sessionContext() context.Context {
	a.lifecycle.mu.Lock()
	defer a.lifecycle.mu.Unlock()
	if a.lifecycle.ctx == nil {
		a.lifecycle.ctx, a.lifecycle.cancel = context.WithCancel(context.Background())
	}
	return a.lifecycle.ctx
}

func (a *App) requestExit(code int) {
	if code != 0 {
		a.lifecycle.code.CompareAndSwap(0, int32(code))
	}
	a.lifecycle.requested.Store(true)
	a.sessionContext()
	a.lifecycle.mu.Lock()
	a.lifecycle.cancel()
	a.lifecycle.mu.Unlock()
}

func (a *App) exitError() error {
	if code := a.lifecycle.code.Load(); code != 0 {
		return &TerminationError{Code: int(code)}
	}
	return nil
}

func (a *App) runApplication() error {
	defer a.Shutdown()
	a.SetRunning(true)
	if err := a.Application.Run(); err != nil {
		return err
	}
	return a.exitError()
}

// tview owns terminal restoration but does not install process signal handlers.
// Queue an event rather than calling Stop while a screen may be starting or
// temporarily suspended for a child process. The root input capture handles it
// before dialogs and NoExitOnCtrlC; raw keyboard Ctrl+C keeps its normal policy.
func (a *App) initSignals() func() {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	signal.Notify(ch, terminationSignals()...)
	go func() {
		defer close(finished)
		select {
		case sig := <-ch:
			a.requestExit(terminationCode(sig))
			a.Application.QueueEvent(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		<-finished
	}
}

// Shutdown is final, idempotent cleanup. It is also safe after partial Init or
// failed screen initialization. Halt/Resume and tview.Suspend remain temporary
// boundaries; neither closes the lifetime context or drains recordings.
func (a *App) Shutdown() {
	if a == nil {
		return
	}
	a.lifecycle.once.Do(func() {
		a.requestExit(0)
		// tview already calls Fini before rethrowing a render/input panic. Some
		// custom screens do not allow a second Fini; cleanup must still cancel
		// resources and preserve the original panic if Stop encounters that.
		func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("Final terminal cleanup failed", slogs.Error, p)
				}
			}()
			a.Application.Stop()
		}()
		a.SetRunning(false)
		a.Halt()
		jobs := map[string]func(){
			"scanner": a.stopImgScanner,
			"pages and recordings": func() {
				defer a.shutdownLogRecordings()
				if a.Content != nil {
					a.Content.ClearPageResources()
					// Do not Pop: stack notifications would restart older pages.
					for _, page := range a.Content.Peek() {
						page.Stop()
					}
				}
			},
			"watchers and forwards": func() {
				if a.factory != nil {
					a.factory.Terminate()
				}
			},
			"shell pod": func() {
				if a.Config != nil && a.Conn() != nil {
					if err := nukeK9sShell(a); err != nil {
						slog.Error("Unable to remove k9+ shell pod", slogs.Error, err)
					}
				}
			},
			"configuration": func() {
				if a.Config != nil {
					if err := a.Config.Save(true); err != nil {
						slog.Error("Config save failed", slogs.Error, err)
					}
				}
			},
		}
		boundedCleanup(shutdownTimeout, jobs)
	})
}

// Independent cleanup jobs all start even if one resource is slow or panics.
// A deadline must never hold the terminal in raw mode or delay process exit
// indefinitely; logs identify incomplete resource cleanup without hiding it.
func boundedCleanup(timeout time.Duration, jobs map[string]func()) {
	completed := make(chan string, len(jobs))
	for name, job := range jobs {
		go func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("Application cleanup failed", "resource", name, slogs.Error, p)
				}
				completed <- name
			}()
			job()
		}()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for len(jobs) > 0 {
		select {
		case name := <-completed:
			delete(jobs, name)
		case <-timer.C:
			for name := range jobs {
				slog.Error("Application cleanup deadline reached", "resource", name)
			}
			return
		}
	}
}
