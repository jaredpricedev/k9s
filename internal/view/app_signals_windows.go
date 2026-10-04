//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import "os"

func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }
func terminationCode(os.Signal) int   { return 130 }
