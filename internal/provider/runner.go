// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package provider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTimeout           = 8 * time.Second
	DefaultStdoutBytes int64 = 1 << 20
	DefaultStderrBytes int64 = 8 << 10
	MaxTimeout               = time.Minute
	MaxOutputBytes     int64 = 4 << 20
	processWaitDelay         = 200 * time.Millisecond
)

func normalizedLimits(l Limits) Limits {
	if l.Timeout <= 0 || l.Timeout > MaxTimeout {
		l.Timeout = DefaultTimeout
	}
	if l.StdoutBytes <= 0 || l.StdoutBytes > MaxOutputBytes {
		l.StdoutBytes = DefaultStdoutBytes
	}
	if l.StderrBytes <= 0 || l.StderrBytes > MaxOutputBytes {
		l.StderrBytes = DefaultStderrBytes
	}
	return l
}

// Run executes exact argv with no shell and closed stdin. The caller must run it
// away from the event thread. Cancellation terminates the process group on Unix;
// WaitDelay bounds inherited output pipes on every supported platform.
//
//nolint:gocritic // A captured request value cannot follow later caller destination changes.
func Run(ctx context.Context, input Input) Result {
	result := Result{Scope: input.Scope, Source: input.Executable, StartedAt: time.Now().UTC(), ExitCode: -1, State: Failed}
	finish := func(err error) Result {
		result.FinishedAt, result.Err = time.Now().UTC(), err
		return result
	}
	if err := ctx.Err(); err != nil {
		result.State = Canceled
		if errors.Is(err, context.DeadlineExceeded) {
			result.State = TimedOut
		}
		return finish(err)
	}
	if input.ProviderID == "" || input.Executable == "" || strings.ContainsRune(input.Executable, 0) || (input.Dir != "" && !filepath.IsAbs(input.Dir)) {
		return finish(errors.New("provider request requires an identity, executable and captured absolute directory"))
	}
	// Copy mutable slices before launching work. Providers cannot rewrite the
	// caller's argv/environment or obtain a reference to global application state.
	input.Args, input.Env = slices.Clone(input.Args), slices.Clone(input.Env)
	limits := normalizedLimits(input.Limits)
	jobCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	path, err := exec.LookPath(input.Executable)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, exec.ErrDot) {
			err = errors.Join(ErrDenied, err)
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			err = errors.Join(ErrAbsent, err)
		}
		return finish(err)
	}
	result.Source = path
	command := exec.CommandContext(jobCtx, path, input.Args...)
	command.Dir, command.Env = input.Dir, input.Env
	command.WaitDelay = processWaitDelay
	configureProcess(command)
	stdout := &limitedOutput{limit: limits.StdoutBytes, cancel: cancel}
	stderr := &limitedOutput{limit: limits.StderrBytes, cancel: cancel}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	cleanupProcess(command)
	result.Stdout, result.Stderr = stdout.snapshot(), stderr.snapshot()
	result.Truncated = stdout.exceeded() || stderr.exceeded()
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	switch {
	case result.Truncated:
		result.State, err = OutputLimit, ErrOutputLimit
	case errors.Is(jobCtx.Err(), context.DeadlineExceeded):
		result.State, err = TimedOut, jobCtx.Err()
	case jobCtx.Err() != nil:
		result.State, err = Canceled, jobCtx.Err()
	case errors.Is(err, os.ErrPermission):
		err = errors.Join(ErrDenied, err)
	case err == nil:
		result.State = Succeeded
	}
	return finish(err)
}

type limitedOutput struct {
	mx        sync.Mutex
	buffer    bytes.Buffer
	limit     int64
	truncated bool
	cancel    context.CancelFunc
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	w.mx.Lock()
	defer w.mx.Unlock()
	remaining := int(w.limit) - w.buffer.Len()
	if len(p) > remaining {
		_, _ = w.buffer.Write(p[:remaining])
		w.truncated = true
		w.cancel()
		return len(p), nil // Continue draining until the canceled child is reaped.
	}
	return w.buffer.Write(p)
}
func (w *limitedOutput) snapshot() []byte {
	w.mx.Lock()
	defer w.mx.Unlock()
	return bytes.Clone(w.buffer.Bytes())
}
func (w *limitedOutput) exceeded() bool { w.mx.Lock(); defer w.mx.Unlock(); return w.truncated }
