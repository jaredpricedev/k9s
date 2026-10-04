// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package taskbook

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/provider"
)

// Reader is a captured read-only API connection, never mutable application
// configuration. Rerun enforces identity again before admitting returned data.
type Reader func(context.Context, inspect.ResourceIdentity) inspect.Observation

type Discoverer func(context.Context, provider.Scope, ...provider.Spec) []provider.Capability

// Rerun executes only the artifact's fixed allowlist. Callers must explicitly
// request it and discard results when their captured destination is obsolete.
//
//nolint:gocritic // Artifact and target scope are immutable execution snapshots.
func Rerun(ctx context.Context, a Artifact, destination provider.Scope, read Reader, discover Discoverer) ([]Result, error) {
	safe, err := Safe(a)
	if err != nil {
		return nil, err
	}
	if discover == nil {
		discover = provider.Discover
	}
	results := make([]Result, 0, len(safe.Checks))
	for _, check := range safe.Checks {
		identity := safe.Evidence.Observations[check.Observation].Identity
		result := Result{Check: check, ObservedAt: time.Now().UTC(), State: inspect.ObservationUnknown, Source: "Explicit taskbook check"}
		switch {
		case ctx.Err() != nil:
			result.Detail = "Check canceled; remaining evidence not refreshed"
		case identity.Context == "" || identity.Context != destination.Context:
			result.State = inspect.ObservationStale
			result.Detail = "Recorded context differs; switch explicitly before rerunning"
		case secretTarget(identity.GVR):
			result.State = inspect.ObservationIncomplete
			result.Detail = "Secret kinds excluded before API or provider checks"
		case check.ID == ResourceGet:
			result = rerunResource(ctx, result, identity, read)
		default:
			scope := destination
			scope.TargetNamespace, scope.GVR, scope.Name, scope.UID = identity.Namespace, identity.GVR, identity.Name, identity.UID
			caps := discover(ctx, scope, versionSpec(check.ID))
			if len(caps) != 1 {
				result.State = string(provider.Unavailable)
				result.Detail = "Provider check unavailable"
				break
			}
			cap := caps[0]
			if cap.Scope != scope {
				result.State = inspect.ObservationStale
				result.Detail = "Provider destination changed; output excluded"
				break
			}
			result.State = string(cap.State)
			result.Source = cap.Source
			result.ObservedAt = cap.ObservedAt
			// Raw stderr/errors never become task evidence. Only successful bounded
			// version stdout is eligible for the sanitizer.
			if cap.State == provider.Ready {
				result.Detail = cap.Version
			} else {
				result.Detail = "Provider " + string(cap.State) + "; no version evidence obtained"
			}
		}
		if result.ObservedAt.IsZero() {
			result.ObservedAt = time.Now().UTC()
		}
		results = append(results, result)
	}
	safe.Latest = results
	safe, err = Safe(safe)
	if err != nil {
		return nil, err
	}
	return safe.Latest, nil
}

func secretTarget(gvr string) bool {
	parts := strings.Split(strings.ToLower(gvr), "/")
	return parts[len(parts)-1] == "secrets"
}

const clientVersionArg = "version"

func versionSpec(id string) provider.Spec {
	switch id {
	case "kubectl-version":
		return provider.Spec{ID: id, Executable: "kubectl", Limits: provider.Limits{StdoutBytes: 8192, StderrBytes: 8192},
			VersionArgs: []string{clientVersionArg, "--client=true", "--output=json"}}
	case "helm-version":
		return provider.Spec{ID: id, Executable: "helm", Limits: provider.Limits{StdoutBytes: 8192, StderrBytes: 8192},
			VersionArgs: []string{clientVersionArg, "--short"}}
	case "flux-version":
		return provider.Spec{ID: id, Executable: "flux", Limits: provider.Limits{StdoutBytes: 8192, StderrBytes: 8192},
			VersionArgs: []string{"--version"}}
	case "cilium-version":
		return provider.Spec{ID: id, Executable: "cilium", Limits: provider.Limits{StdoutBytes: 8192, StderrBytes: 8192},
			VersionArgs: []string{clientVersionArg, "--client"}}
	default:
		return provider.Spec{ID: id, Probe: func(context.Context, provider.Scope) (provider.Observation, error) {
			return provider.Observation{}, errors.New("unknown fixed check")
		}}
	}
}

//nolint:gocritic // Captured result and identity values do not share mutable input.
func rerunResource(ctx context.Context, result Result, identity inspect.ResourceIdentity, read Reader) Result {
	result.Source = "Kubernetes API GET (explicit taskbook rerun)"
	if identity.UID == "" {
		result.State = inspect.ObservationIncomplete
		result.Detail = "Saved UID required; no API request made"
		return result
	}
	if read == nil {
		result.State = string(provider.Unavailable)
		result.Detail = "Captured read-only API unavailable"
		return result
	}
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	observation := read(readCtx, identity)
	canceled := readCtx.Err() != nil
	cancel()
	if canceled {
		result.Detail = "Read canceled or timed out"
		return result
	}
	if observation.Identity != identity {
		result.State = inspect.ObservationStale
		result.Detail = "Resource identity changed; replacement body excluded"
		return result
	}
	observation = inspect.SafeObservation(observation)
	if observation.State != inspect.ObservationComplete {
		observation.Object = nil
	}
	result.State = observation.State
	result.Detail = observation.Reason
	result.Observation = &observation
	result.ObservedAt = observation.ObservedAt
	return result
}
