// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

//go:build windows

package view

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

func configureCommandCancellation(cmd *exec.Cmd, _ bool) {
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Invoke the native tree terminator directly, never through a shell.
		systemDirectory, err := windows.GetSystemDirectory()
		if err != nil {
			return cmd.Process.Kill()
		}
		binary := filepath.Join(systemDirectory, "taskkill.exe")
		if err := exec.CommandContext(ctx, binary, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

func joinCommandGroup(_, _ *exec.Cmd)                 {}
func commandTerminalLease(bool) (func() error, error) { return func() error { return nil }, nil }
