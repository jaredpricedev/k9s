//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package provider

import (
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func cleanupProcess(cmd *exec.Cmd) {
	// A tool may exit while descendants still own output pipes. They belong to
	// this isolated process group and must not continue producing late output.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
