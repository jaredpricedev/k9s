// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package cmd

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestFatalRecoveryReturnsFailureAndPreservesNormalQuit(t *testing.T) {
	if os.Getenv("K9PLUS_FATAL_EXIT_CHILD") != "" {
		rootCmd.RunE = func(*cobra.Command, []string) (err error) {
			defer recoverFatalPanic(&err)
			if os.Getenv("K9PLUS_FATAL_EXIT_CHILD") == "panic" {
				panic("unexpected test failure")
			}
			return nil
		}
		rootCmd.SetArgs([]string{})
		Execute()
		return
	}
	for _, mode := range []string{"panic", "quit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFatalRecoveryReturnsFailureAndPreservesNormalQuit$")
			command.Env = append(os.Environ(), "K9PLUS_FATAL_EXIT_CHILD="+mode)
			output, err := command.CombinedOutput()
			if mode == "panic" {
				failure, ok := err.(*exec.ExitError)
				if !ok || failure.ExitCode() != 1 {
					t.Fatalf("fatal recovery must exit 1, got %v: %s", err, output)
				}
			} else if err != nil {
				t.Fatalf("normal quit failed: %v: %s", err, output)
			}
		})
	}
}
