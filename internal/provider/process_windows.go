//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package provider

import "os/exec"

func configureProcess(*exec.Cmd) {}
func cleanupProcess(*exec.Cmd)   {}
