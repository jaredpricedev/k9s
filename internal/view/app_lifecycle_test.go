// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

func lifecycleApp() *App {
	return &App{App: ui.NewApp(config.NewConfig(nil), "")}
}

func TestHaltIsTemporaryAndShutdownIsFinal(t *testing.T) {
	a := lifecycleApp()
	lifetime := a.sessionContext()
	a.Resume()
	a.Halt()
	if lifetime.Err() != nil {
		t.Fatal("temporary halt canceled the application lifetime")
	}
	a.Resume()
	if a.cancelFn == nil {
		t.Fatal("temporary halt could not resume periodic work")
	}
	a.Shutdown()
	a.Shutdown()
	if lifetime.Err() != context.Canceled || a.cancelFn != nil || a.IsRunning() {
		t.Fatal("final cleanup did not cancel periodic work and lifetime")
	}
	a.Resume()
	if a.cancelFn != nil {
		t.Fatal("final shutdown restarted periodic work")
	}
}

func TestExternalTerminationBypassesKeyboardQuitPolicy(t *testing.T) {
	a := lifecycleApp()
	a.Config.K9s.NoExitOnCtrlC = true
	a.quitCmd(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	if a.lifecycle.requested.Load() {
		t.Fatal("keyboard Ctrl+C ignored configured quit policy")
	}
	a.requestExit(130)
	a.keyboard(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	var termination *TerminationError
	if !errors.As(a.exitError(), &termination) || termination.ExitCode() != 130 {
		t.Fatal("external SIGINT lost its termination status")
	}
	a.requestExit(0)
	if termination.ExitCode() != 130 || a.lifecycle.code.Load() != 130 {
		t.Fatal("cleanup erased the termination status")
	}
	a.Shutdown()
}

func TestBoundedCleanupDoesNotSuppressOtherResources(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var completed atomic.Bool
	started := time.Now()
	boundedCleanup(100*time.Millisecond, map[string]func(){
		"blocked": func() { <-release },
		"panic":   func() { panic("failing resource") },
		"healthy": func() { completed.Store(true) },
	})
	if !completed.Load() || time.Since(started) > time.Second {
		t.Fatal("one cleanup resource prevented bounded independent cleanup")
	}
}

type lifecycleScreen struct {
	tcell.SimulationScreen
	finalized atomic.Int32
}

func (s *lifecycleScreen) Fini() {
	s.finalized.Add(1)
	s.SimulationScreen.Fini()
}

func TestFrameworkPanicAndApplicationCleanup(t *testing.T) {
	a := lifecycleApp()
	ctx := a.sessionContext()
	screen := &lifecycleScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	a.SetScreen(screen)
	a.SetRoot(tview.NewBox().SetDrawFunc(func(tcell.Screen, int, int, int, int) (int, int, int, int) {
		panic("runtime draw failure")
	}), true)
	defer func() {
		if recover() == nil {
			t.Fatal("framework swallowed runtime panic")
		}
		if screen.finalized.Load() == 0 || ctx.Err() != context.Canceled || a.IsRunning() {
			t.Fatal("panic did not restore the framework screen and stop owned work")
		}
	}()
	_ = a.runApplication()
}

// Optional real-terminal harness for runtime panic and error cleanup. The
// separate PTY script asserts termios and emitted restoration sequences.
func TestApplicationLifecyclePTYFixture(t *testing.T) {
	mode := os.Getenv("K9PLUS_LIFECYCLE_TTY_FIXTURE")
	if mode == "" {
		t.Skip("opt-in terminal lifecycle fixture")
	}
	a := lifecycleApp()
	a.SetRoot(tview.NewBox(), true)
	if mode == "panic" {
		a.SetInputCapture(func(*tcell.EventKey) *tcell.EventKey { panic("runtime input failure") })
		defer func() {
			if recover() == nil {
				t.Fatal("runtime fixture did not panic")
			}
		}()
	}
	if mode == "error" {
		a.SetInputCapture(func(*tcell.EventKey) *tcell.EventKey {
			a.BailOut(1)
			return nil
		})
	}
	err := a.runApplication()
	if mode == "error" {
		var termination *TerminationError
		if !errors.As(err, &termination) || termination.ExitCode() != 1 {
			t.Fatalf("runtime error status lost: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}
