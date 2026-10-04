//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"os"
	"syscall"
)

func terminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP, os.Interrupt, syscall.SIGTERM}
}

func terminationCode(sig os.Signal) int {
	if value, ok := sig.(syscall.Signal); ok {
		return 128 + int(value)
	}
	return 1
}
