// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

//go:build !windows

package view

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func configureCommandCancellation(cmd *exec.Cmd, background bool) {
	cmd.WaitDelay = time.Second
	// Every child tree receives its own process group. Interactive commands
	// additionally own the terminal foreground group until its lease is restored.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: !background && term.IsTerminal(int(os.Stdin.Fd())), Ctty: int(os.Stdin.Fd())}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		group := cmd.Process.Pid
		if cmd.SysProcAttr.Pgid > 0 {
			group = cmd.SysProcAttr.Pgid
		}
		return syscall.Kill(-group, syscall.SIGKILL)
	}
}

func joinCommandGroup(cmd, first *exec.Cmd) {
	if first.Process == nil || cmd.SysProcAttr == nil || first.SysProcAttr == nil || !first.SysProcAttr.Foreground {
		return
	}
	cmd.SysProcAttr.Pgid = first.Process.Pid
	cmd.SysProcAttr.Foreground = false
}

func commandTerminalLease(background bool) (func() error, error) {
	if background || !term.IsTerminal(int(os.Stdin.Fd())) {
		return func() error { return nil }, nil
	}
	group, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return nil, err
	}
	// The suspended parent must be able to restore foreground ownership after
	// the child exits; SIGTTOU otherwise stops a background terminal owner.
	signal.Ignore(syscall.SIGTTOU)
	return func() error {
		err := unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, group)
		signal.Reset(syscall.SIGTTOU)
		return err
	}, nil
}
