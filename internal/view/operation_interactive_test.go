// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

type handoffTestScreen struct {
	tcell.SimulationScreen
	restored    chan struct{}
	restoreOnce sync.Once
}

func (s *handoffTestScreen) Resume() error {
	err := s.SimulationScreen.Resume()
	s.restoreOnce.Do(func() { close(s.restored) })
	return err
}

func interactiveTestApp(t *testing.T) (*App, *handoffTestScreen) {
	t.Helper()
	app := lifecycleApp()
	app.Config.K9s.UI.Reactive = false
	screen := &handoffTestScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), restored: make(chan struct{})}
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	app.SetScreen(screen)
	ready := make(chan struct{})
	var once sync.Once
	app.SetRoot(tview.NewBox().SetDrawFunc(func(_ tcell.Screen, x, y, width, height int) (int, int, int, int) {
		once.Do(func() { close(ready) })
		return x, y, width, height
	}), true)
	app.SetRunning(true)
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("framework did not start")
	}
	t.Cleanup(func() {
		app.Application.Stop()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("framework did not stop")
		}
		app.SetRunning(false)
		app.Halt()
	})
	return app, screen
}

func TestInteractiveCancellationWaitsForChildCleanupAndFrameworkRestore(t *testing.T) {
	app, screen := interactiveTestApp(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, teardown, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	path := filepath.Join(t.TempDir(), "owned-destination")
	if err := os.WriteFile(path, []byte("fixture identity"), 0600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		err := runInteractiveHandoff(ctx, app, func() bool { return true }, func() error {
			close(started)
			<-ctx.Done()
			close(teardown)
			// Model a child whose bounded Wait/terminal cleanup has not finished.
			<-release
			return ctx.Err()
		})
		_ = os.Remove(path)
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case <-teardown:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach child")
	}
	select {
	case err := <-finished:
		t.Fatalf("ownership released before child cleanup: %v", err)
	default:
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("owned destination removed before child completed")
	}
	select {
	case <-screen.restored:
		t.Fatal("framework restored before suspended child finished")
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed child did not release worker")
	}
	select {
	case <-screen.restored:
	default:
		t.Fatal("worker finished before framework restoration")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned destination was not released after completion")
	}
}

func TestInteractiveCancellationBeforeHandoffCannotStartLateCallback(t *testing.T) {
	app, _ := interactiveTestApp(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go app.QueueUpdateDraw(func() { close(blocked); <-release })
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not block")
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	var starts atomic.Int32
	go func() {
		finished <- runInteractiveHandoff(ctx, app, func() bool { return true }, func() error { starts.Add(1); return nil })
	}()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unentered handoff did not cancel")
	}
	close(release)
	flushed := make(chan struct{})
	go app.QueueUpdateDraw(func() { close(flushed) })
	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not recover")
	}
	if starts.Load() != 0 {
		t.Fatal("canceled callback started a child")
	}
}
