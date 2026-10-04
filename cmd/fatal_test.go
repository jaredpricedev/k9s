// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package cmd

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/view"
	"github.com/spf13/cobra"
)

func TestFatalRecoveryReturnsFailureAndPreservesNormalQuit(t *testing.T) {
	if os.Getenv("K9PLUS_FATAL_EXIT_CHILD") != "" {
		rootCmd.RunE = func(*cobra.Command, []string) (err error) {
			defer recoverFatalPanic(&err)
			if os.Getenv("K9PLUS_FATAL_EXIT_CHILD") == "panic" {
				panic("unexpected test failure")
			}
			switch os.Getenv("K9PLUS_FATAL_EXIT_CHILD") {
			case "SIGHUP":
				return &view.TerminationError{Code: 129}
			case "SIGINT":
				return &view.TerminationError{Code: 130}
			case "SIGTERM":
				return &view.TerminationError{Code: 143}
			}
			return nil
		}
		rootCmd.SetArgs([]string{})
		Execute()
		return
	}
	for mode, code := range map[string]int{"panic": 1, "quit": 0, "SIGHUP": 129, "SIGINT": 130, "SIGTERM": 143} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFatalRecoveryReturnsFailureAndPreservesNormalQuit$")
			command.Env = append(os.Environ(), "K9PLUS_FATAL_EXIT_CHILD="+mode)
			output, err := command.CombinedOutput()
			if code != 0 {
				failure, ok := err.(*exec.ExitError)
				if !ok || failure.ExitCode() != code {
					t.Fatalf("%s must exit %d, got %v: %s", mode, code, err, output)
				}
			} else if err != nil {
				t.Fatalf("normal quit failed: %v: %s", err, output)
			}
		})
	}
}
