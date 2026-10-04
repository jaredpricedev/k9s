// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

//go:build !windows

package view

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config/mock"
	"golang.org/x/term"
)

const (
	guardedExecShellFlag = "-c"
	guardedExecLinux     = "linux"
	guardedExecPrintf    = "printf"
	guardedExecString    = "%s"
)

func TestGuardedBackgroundExecWaitsForRealExitAndReportsUnknownEffects(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	started := time.Now()
	ok, failures, statuses := run(nil, &shellOpts{binary: sh, args: []string{guardedExecShellFlag, "sleep 0.05; exit 7"}, background: true})
	if !ok {
		t.Fatal("worker did not start")
	}
	var result error
	for failure := range failures {
		result = errors.Join(result, failure)
	}
	for range statuses {
	}
	var exit *exec.ExitError
	if !errors.As(result, &exit) || exit.ExitCode() != 7 || time.Since(started) < 50*time.Millisecond {
		t.Fatal("background command reported early success", result)
	}
	if state := operationResultState(result, false, true); state != operationUnknown {
		t.Fatal("nonzero exit claimed writes were rejected", state)
	}
}

func TestGuardedBackgroundExecPreservesCapturedCancellation(t *testing.T) {
	printf, err := exec.LookPath(guardedExecPrintf)
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ok, failures, statuses := run(NewApp(mock.NewMockConfig(t)), &shellOpts{binary: printf,
		args: []string{guardedExecString, "must not start"}, background: true, ctx: ctx})
	if !ok {
		t.Fatal("background worker did not start")
	}
	var result error
	for failure := range failures {
		result = errors.Join(result, failure)
	}
	if !errors.Is(result, context.Canceled) {
		t.Fatal("captured cancellation was replaced by the app lifetime", result)
	}
	for status := range statuses {
		t.Fatal("canceled command produced output", status)
	}
}

func TestGuardedExecPipelineCompletesWithoutWaitingForUnclosedInput(t *testing.T) {
	printf, err := exec.LookPath(guardedExecPrintf)
	if err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath("tr"); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	statuses := make(chan string, 1)
	literal := "hello $(touch /never-create-this-file)"
	if err := execute(&shellOpts{binary: printf, args: []string{guardedExecString, literal}, pipes: []string{guardedTestPipeline, "cat"}, background: true, ctx: ctx}, statuses); err != nil {
		t.Fatal(err)
	}
	if got := <-statuses; got != outputPrefix+" "+strings.ToUpper(literal) {
		t.Fatal("arguments were interpolated or pipeline output lost", got)
	}
}

func TestGuardedExecCancellationKillsBackgroundDescendants(t *testing.T) {
	testGuardedExecCancellationKillsDescendants(t, true)
}

func TestGuardedExecCancellationKillsForegroundDescendants(t *testing.T) {
	if os.Getenv("K9PLUS_TEST_REQUIRE_TTY") == "1" && !term.IsTerminal(int(os.Stdin.Fd())) {
		t.Fatal("foreground regression requires a real controlling TTY")
	}
	testGuardedExecCancellationKillsDescendants(t, false)
}

func TestGuardedExecForegroundPipelinePreservesTerminalOwnership(t *testing.T) {
	printf, err := exec.LookPath(guardedExecPrintf)
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if executionErr := execute(&shellOpts{binary: printf, args: []string{guardedExecString, "guarded foreground pipeline\n"}, pipes: []string{"cat"}, ctx: ctx}, make(chan string, 1)); executionErr != nil {
		t.Fatal(executionErr)
	}
}

func testGuardedExecCancellationKillsDescendants(t *testing.T, background bool) {
	if runtime.GOOS != guardedExecLinux {
		t.Skip("process state assertion uses Linux procfs")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- execute(&shellOpts{binary: sh, args: []string{guardedExecShellFlag, "sleep 20 & echo $! > \"$1\"; wait", testWorkspaceWorkerName, pidPath}, background: background, ctx: ctx}, make(chan string, 1))
	}()
	var childPID string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			childPID = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	pid, parseErr := strconv.Atoi(childPID)
	if parseErr != nil {
		cancel()
		t.Fatal("background child did not start", parseErr)
	}
	cancel()
	select {
	case waitErr := <-done:
		if !errors.Is(waitErr, context.Canceled) {
			t.Fatal(waitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled process did not release its wait")
	}
	assertGuardedDescendantStopped(t, pid)
}

// SIGKILL delivery and orphan reaping are asynchronous even after the direct
// command has exited. Bound the observation wait without accepting a live child.
func assertGuardedDescendantStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	path := filepath.Join(string(filepath.Separator), "proc", strconv.Itoa(pid), "stat")
	for {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(data))
		if len(fields) >= 3 && fields[2] == "Z" {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("child remained running after cancellation", string(data))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGuardedExecOutputIsBounded(t *testing.T) {
	head, err := exec.LookPath("head")
	if err != nil {
		t.Skip(err)
	}
	statuses := make(chan string, 1)
	if err := execute(&shellOpts{binary: head, args: []string{guardedExecShellFlag, "40000", "/dev/zero"}, background: true, ctx: t.Context()}, statuses); err != nil {
		t.Fatal(err)
	}
	output := <-statuses
	if len(output) > 33*1024 || !strings.Contains(output, "truncated") {
		t.Fatal("unbounded command output", len(output))
	}
}

func TestGuardedTerminalLeaseCancellationCannotStartLateHandoff(t *testing.T) {
	release, err := acquireCommandTerminal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if lateRelease, acquireErr := acquireCommandTerminal(ctx); !errors.Is(acquireErr, context.DeadlineExceeded) || lateRelease != nil {
		t.Fatal("canceled waiter acquired terminal", acquireErr)
	}
}
