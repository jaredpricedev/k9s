// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const helperFailure = "failure"

func helperInput(t *testing.T, mode string, args ...string) Input {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Input{ProviderID: "fixture", Executable: bin,
		Args: append([]string{"-test.run=TestProviderHelperProcess", "--", mode}, args...),
		Env:  append(os.Environ(), "K9PLUS_PROVIDER_HELPER=1", "GORACE=atexit_sleep_ms=0"), Dir: t.TempDir(),
		Scope:  Scope{Context: "captured", Namespace: "apps", GVR: "apps/v1/deployments", Name: "api", UID: "original", Revision: 7},
		Limits: Limits{Timeout: time.Second, StdoutBytes: 4096, StderrBytes: 4096}}
}

func TestProviderHelperProcess(_ *testing.T) {
	if os.Getenv("K9PLUS_PROVIDER_HELPER") != "1" {
		return
	}
	start := 0
	for i, arg := range os.Args {
		if arg == "--" {
			start = i + 1
			break
		}
	}
	args := os.Args[start:]
	switch args[0] {
	case "args":
		_ = json.NewEncoder(os.Stdout).Encode(args[1:])
	case "version":
		fmt.Print("v2.3.4")
	case helperFailure:
		fmt.Fprint(os.Stderr, "private provider detail")
		os.Exit(23)
	case "flood":
		for range 10000 {
			fmt.Fprint(os.Stdout, strings.Repeat("x", 100))
			fmt.Fprint(os.Stderr, strings.Repeat("e", 100))
		}
	case "sleep":
		time.Sleep(10 * time.Second)
		fmt.Print("late output")
	case "descendant":
		bin, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		child := exec.CommandContext(context.Background(), bin, "-test.run=TestProviderHelperProcess", "--", "sleep")
		child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Printf("child=%d", child.Process.Pid)
	}
	os.Exit(0)
}

func TestRunPreservesScopeAndLiteralArguments(t *testing.T) {
	in := helperInput(t, "args", "$(touch injected)", "with spaces", "quoted\"value", "a;b", "--context=captured")
	result := Run(context.Background(), in)
	if result.State != Succeeded || result.Err != nil || result.Scope != in.Scope || result.ExitCode != 0 {
		t.Fatalf("result %#v", result)
	}
	var args []string
	if err := json.Unmarshal(result.Stdout, &args); err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, "|") != strings.Join(in.Args[3:], "|") {
		t.Fatalf("argv changed: %q", args)
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "injected")); !os.IsNotExist(err) {
		t.Fatal("literal argument was executed by a shell")
	}
	if result.StartedAt.IsZero() || result.FinishedAt.Before(result.StartedAt) {
		t.Fatal("missing observation time")
	}
}

func TestRunBoundsOutputAndCancelsProducingProcess(t *testing.T) {
	in := helperInput(t, "flood")
	in.Limits.StdoutBytes, in.Limits.StderrBytes = 256, 128
	result := Run(context.Background(), in)
	if result.State != OutputLimit || !result.Truncated || !errors.Is(result.Err, ErrOutputLimit) || len(result.Stdout) > 256 || len(result.Stderr) > 128 {
		t.Fatalf("unbounded result %#v", result)
	}
}

func TestRunDistinguishesTimeoutCancellationFailureAndMissing(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", helperFailure, string(Absent), string(Denied)} {
		t.Run(mode, func(t *testing.T) {
			in := helperInput(t, "sleep")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := Failed
			switch mode {
			case "timeout":
				in.Limits.Timeout = 30 * time.Millisecond
				want = TimedOut
			case "cancel":
				go func() { time.Sleep(30 * time.Millisecond); cancel() }()
				want = Canceled
			case helperFailure:
				in.Args[2] = helperFailure
			case string(Absent):
				in.Executable = filepath.Join(t.TempDir(), string(Absent))
			case string(Denied):
				in.Executable = filepath.Join(t.TempDir(), string(Denied))
				if err := os.WriteFile(in.Executable, []byte("unexecutable"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			result := Run(ctx, in)
			if result.State != want || result.Err == nil {
				t.Fatalf("result %#v", result)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("cancellation did not bound execution")
			}
			if mode == string(Absent) && !errors.Is(result.Err, ErrAbsent) {
				t.Fatal(result.Err)
			}
			if mode == string(Denied) && !errors.Is(result.Err, ErrDenied) {
				t.Fatal(result.Err)
			}
			if mode == helperFailure && result.ExitCode != 23 {
				t.Fatalf("exit %d", result.ExitCode)
			}
		})
	}
}

func TestRunBoundsInheritedPipesAndDiscardsLateOutput(t *testing.T) {
	started := time.Now()
	result := Run(context.Background(), helperInput(t, "descendant"))
	if time.Since(started) > 2*time.Second || strings.Contains(string(result.Stdout), "late output") || result.Err == nil {
		t.Fatalf("late inherited-pipe result %#v", result)
	}
}

func TestDiscoverChecksOnlyExplicitSpecsAndSeparatesStates(t *testing.T) {
	scope := Scope{Context: "original", Namespace: "selected", Revision: 17}
	in := helperInput(t, "version")
	ready := Spec{ID: "fixture", Executable: in.Executable, VersionArgs: in.Args, Env: in.Env, Dir: in.Dir, Limits: in.Limits}
	checks := Discover(context.Background(), scope, ready,
		Spec{ID: string(Absent), Executable: filepath.Join(t.TempDir(), "missing"), VersionArgs: []string{"--version"}},
		Spec{ID: "denied-api", Probe: func(context.Context, Scope) (Observation, error) { return Observation{}, ErrDenied }},
		Spec{ID: "incompatible", Executable: in.Executable, VersionArgs: in.Args, Env: in.Env, Dir: in.Dir, Compatible: func(string) bool { return false }})
	wants := []CapabilityState{Ready, Absent, Denied, Incompatible}
	for i, check := range checks {
		if check.State != wants[i] || check.Scope != scope {
			t.Fatalf("check %#v", check)
		}
	}
	if len(Discover(context.Background(), scope)) != 0 {
		t.Fatal("empty request discovered unrequested providers")
	}
	if checks[0].Version != "v2.3.4" {
		t.Fatal(checks[0].Version)
	}
}

func TestDiscoverFailedVersionProbeAndCanceledLateAPIReply(t *testing.T) {
	in := helperInput(t, helperFailure)
	checks := Discover(context.Background(), in.Scope, Spec{ID: "version", Executable: in.Executable, VersionArgs: in.Args, Env: in.Env, Dir: in.Dir})
	if checks[0].State != Unavailable || strings.Contains(checks[0].Detail, "private") {
		t.Fatalf("probe failure %#v", checks[0])
	}
	ctx, cancel := context.WithCancel(context.Background())
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan []Capability, 1)
	go func() {
		done <- Discover(ctx, in.Scope, Spec{ID: "api", Probe: func(context.Context, Scope) (Observation, error) {
			close(started)
			<-release
			return Observation{Detail: "late success"}, nil
		}})
	}()
	<-started
	cancel()
	select {
	case result := <-done:
		if result[0].State != Unavailable || !errors.Is(result[0].Err, context.Canceled) {
			t.Fatalf("canceled check %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("API check did not honor cancellation")
	}
	close(release)
}

func TestDiscoverAPIProbePanicBecomesSafeUnavailableObservation(t *testing.T) {
	checks := Discover(context.Background(), Scope{Context: "captured"}, Spec{ID: "faulty", Probe: func(context.Context, Scope) (Observation, error) { panic("SECRET adapter detail") }})
	if len(checks) != 1 || checks[0].State != Unavailable || checks[0].Err == nil || strings.Contains(checks[0].Err.Error(), "SECRET") {
		t.Fatalf("unsafe adapter failure %#v", checks)
	}
}
