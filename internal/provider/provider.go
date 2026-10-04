// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package provider supplies explicit, bounded discovery and execution contracts.
// Adapters receive captured values; they never receive the application's mutable
// configuration or own its current Kubernetes context.
package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Scope struct {
	Context, Namespace, TargetNamespace, GVR, Name, UID string
	Revision                                            uint64
}

type Limits struct {
	Timeout                  time.Duration
	StdoutBytes, StderrBytes int64
}

type Input struct {
	ProviderID, Executable, Dir string
	Args, Env                   []string
	Scope                       Scope
	Limits                      Limits
}

type ExecutionState string

const (
	Succeeded   ExecutionState = "succeeded"
	Failed      ExecutionState = "failed"
	Canceled    ExecutionState = "canceled"
	TimedOut    ExecutionState = "timed out"
	OutputLimit ExecutionState = "output limit"
)

type Result struct {
	Scope                 Scope
	Source                string
	StartedAt, FinishedAt time.Time
	Stdout, Stderr        []byte
	ExitCode              int
	Truncated             bool
	State                 ExecutionState
	Err                   error
}

type CapabilityState string

const (
	Ready        CapabilityState = "available"
	Absent       CapabilityState = "absent"
	Denied       CapabilityState = "denied"
	Incompatible CapabilityState = "incompatible"
	Unavailable  CapabilityState = "unavailable"
)

var (
	ErrAbsent       = errors.New("provider absent")
	ErrDenied       = errors.New("provider access denied")
	ErrIncompatible = errors.New("provider incompatible")
	ErrOutputLimit  = errors.New("provider output exceeds the captured limit")
)

// Observation is the result of an API/permission adapter's explicit check.
// The adapter must use an independently captured connection and context-aware
// requests, and must not attempt authentication/login or change global context.
type Observation struct{ Version, Source, Detail string }
type Probe func(context.Context, Scope) (Observation, error)

type Spec struct {
	ID, Executable, Dir string
	VersionArgs, Env    []string
	Limits              Limits
	Compatible          func(string) bool
	Probe               Probe // Explicit API/permission probe; mutually exclusive with Executable.
}

type Capability struct {
	ID                      string
	Scope                   Scope
	State                   CapabilityState
	Version, Source, Detail string
	Limits                  Limits
	ObservedAt              time.Time
	Err                     error
}

// Discover checks only the supplied adapters. Calling code decides when the
// operator explicitly requests this bounded background work. There is no global
// registry scan, installation, authentication or context switching.
func Discover(ctx context.Context, scope Scope, specs ...Spec) []Capability {
	checks := make([]Capability, 0, len(specs))
	for _, spec := range specs {
		check := Capability{ID: spec.ID, Scope: scope, Limits: normalizedLimits(spec.Limits), ObservedAt: time.Now().UTC(), State: Unavailable}
		if err := ctx.Err(); err != nil {
			check.Err = err
			check.Detail = "check canceled"
			checks = append(checks, check)
			continue
		}
		if spec.ID == "" || (spec.Probe != nil && spec.Executable != "") {
			check.Err = errors.New("invalid provider specification")
		} else if spec.Probe != nil {
			probeCtx, cancel := context.WithTimeout(ctx, check.Limits.Timeout)
			observation, err := boundedProbe(probeCtx, scope, spec.Probe)
			if probeCtx.Err() != nil {
				err = probeCtx.Err()
			}
			cancel()
			check.Source, check.Version, check.Detail, check.Err = observation.Source, observation.Version, observation.Detail, err
		} else if spec.Executable != "" && len(spec.VersionArgs) > 0 {
			result := Run(ctx, Input{ProviderID: spec.ID, Executable: spec.Executable, Args: spec.VersionArgs, Env: spec.Env, Dir: spec.Dir, Scope: scope, Limits: check.Limits})
			check.Source, check.Err = result.Source, result.Err
			if result.State == Succeeded {
				check.Version = strings.TrimSpace(string(result.Stdout))
			}
			if check.Version == "" && check.Err == nil {
				check.Err = ErrIncompatible
			}
		} else {
			check.Err = errors.New("provider has no explicit probe")
		}
		check.ObservedAt = time.Now().UTC()
		switch {
		case errors.Is(check.Err, ErrAbsent):
			check.State, check.Detail = Absent, "executable or API absent"
		case errors.Is(check.Err, ErrDenied):
			check.State, check.Detail = Denied, "permission denied"
		case errors.Is(check.Err, ErrIncompatible):
			check.State, check.Detail = Incompatible, "unsupported capability or version"
		case check.Err != nil:
			check.State, check.Detail = Unavailable, "check failed or timed out"
		case spec.Compatible != nil && !spec.Compatible(check.Version):
			check.State, check.Detail, check.Err = Incompatible, "unsupported version", ErrIncompatible
		default:
			check.State = Ready
		}
		checks = append(checks, check)
	}
	return checks
}

func boundedProbe(ctx context.Context, scope Scope, probe Probe) (Observation, error) {
	type reply struct {
		observation Observation
		err         error
	}
	response := make(chan reply, 1)
	go func() { observation, err := probe(ctx, scope); response <- reply{observation, err} }()
	select {
	case <-ctx.Done():
		return Observation{}, ctx.Err()
	case result := <-response:
		return result.observation, result.err
	}
}
