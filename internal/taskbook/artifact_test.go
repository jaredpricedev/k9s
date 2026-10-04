// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package taskbook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/provider"
)

const (
	fixtureNamespace = "app"
	fixtureContext   = "lab"
	fixtureName      = "api"
	fixtureUID       = "saved"
	fixtureGVR       = "v1/pods"
	fixtureSpecField = "spec"
	fixtureUIDField  = "uid"
)

func fixture(t *testing.T) Artifact {
	t.Helper()
	o := inspect.NewObservation(inspect.ResourceIdentity{Context: fixtureContext, GVR: fixtureGVR, Namespace: fixtureNamespace, Name: fixtureName, UID: fixtureUID}, "Retained fixture API", time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), map[string]any{"kind": "Pod", "metadata": map[string]any{fixtureUIDField: fixtureUID}, fixtureSpecField: map[string]any{"password": "must-redact"}})
	b := inspect.NewBundle([]inspect.Observation{o})
	b.Snippets = []inspect.Snippet{{Source: "Frozen fixture log selection", ObservedAt: o.ObservedAt, Text: "Bearer fixture-token\nfailed requests", Limits: "Only last 8 lines; partial history"}}
	a, err := New("Investigate rollout", b)
	if err != nil {
		t.Fatal(err)
	}
	a.Questions = []string{"Who owns recovery?"}
	a.Links = []string{"https://example.invalid/evidence"}
	return a
}
func TestDecodeOfflineRetainsOriginalSourcesAndCoverage(t *testing.T) {
	a := fixture(t)
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Latest) != 0 || got.Evidence.Observations[0].ObservedAt != a.Evidence.Observations[0].ObservedAt || got.Evidence.Snippets[0].Limits != a.Evidence.Snippets[0].Limits {
		t.Fatal("offline open refreshed or lost coverage", got)
	}
	out, err := Preview(got)
	if err != nil || !strings.Contains(out, "Opening never runs checks") || strings.Contains(out, "fixture-token") || strings.Contains(string(data), "must-redact") {
		t.Fatal("unsafe offline preview", out, err)
	}
}
func TestRejectCorruptOversizedUnknownImportedCommands(t *testing.T) {
	a := fixture(t)
	for _, mutate := range []func(*Artifact){func(a *Artifact) { a.Version = 99 }, func(a *Artifact) { a.Evidence.Observations = nil }, func(a *Artifact) { a.Checks[0].ID = "sh" }, func(a *Artifact) { a.Checks[0].Observation = 99 }, func(a *Artifact) { a.Summary = strings.Repeat("a", 4097) }} {
		next := a
		next.Checks = append([]Check(nil), a.Checks...)
		mutate(&next)
		b, _ := json.Marshal(next)
		if _, err := Decode(bytes.NewReader(b)); err == nil {
			t.Fatal("accepted malformed imported task", string(b))
		}
	}
	for _, data := range []string{"{", strings.Repeat("x", MaxBytes+1), `{"version":1,"argv":["rm","-rf"]}`} {
		if _, err := Decode(strings.NewReader(data)); err == nil {
			t.Fatal("accepted corrupt/oversized/imported argv")
		}
	}
}
func TestSaveExclusivePrivateAndBounded(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(t.TempDir(), "task.json")
	if err := Save(path, a); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if err := Save(path, a); err == nil {
		t.Fatal("overwrote artifact")
	}
	if _, err := Read(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(path, link); err == nil {
		if _, err = Read(link); err == nil {
			t.Fatal("accepted symlink")
		}
	}
}
func TestExplicitRerunExcludesReplacementAndPreservesOriginal(t *testing.T) {
	a := fixture(t)
	calls := 0
	results, err := Rerun(t.Context(), a, provider.Scope{Context: fixtureContext, Namespace: fixtureNamespace, Revision: 7}, func(_ context.Context, id inspect.ResourceIdentity) inspect.Observation {
		calls++
		id.UID = "replacement"
		return inspect.NewObservation(id, "API", time.Now(), map[string]any{"replacement-body": "excluded"})
	}, nil)
	if err != nil || calls != 1 || results[0].State != inspect.ObservationStale || results[0].Observation != nil {
		t.Fatal("replacement admitted", results, err)
	}
	if a.Evidence.Observations[0].Identity.UID != fixtureUID || len(a.Latest) != 0 {
		t.Fatal("rerun modified original evidence")
	}
}
func TestReadGuardsRequireUIDContextAndExcludeSecrets(t *testing.T) {
	for _, scenario := range []string{fixtureUIDField, "context", "secret"} {
		t.Run(scenario, func(t *testing.T) {
			a := fixture(t)
			switch scenario {
			case fixtureUIDField:
				a.Evidence.Observations[0].Identity.UID = ""
			case "context":
				a.Evidence.Observations[0].Identity.Context = "other"
			case "secret":
				a.Evidence.Observations[0].Identity.GVR = "v1/secrets"
			}
			read := func(context.Context, inspect.ResourceIdentity) inspect.Observation {
				t.Fatal("guard contacted API")
				return inspect.Observation{}
			}
			discover := func(context.Context, provider.Scope, ...provider.Spec) []provider.Capability {
				t.Fatal("guard contacted provider")
				return nil
			}
			results, err := Rerun(t.Context(), a, provider.Scope{Context: fixtureContext, Namespace: fixtureNamespace, Revision: 7}, read, discover)
			if err != nil || results[0].State == inspect.ObservationComplete {
				t.Fatal(results, err)
			}
		})
	}
}
func TestProviderAbsentDeniedAndFixedArgv(t *testing.T) {
	a := fixture(t)
	a.Checks = []Check{{ID: "kubectl-version"}}
	for _, state := range []provider.CapabilityState{provider.Absent, provider.Denied, provider.Ready} {
		results, err := Rerun(t.Context(), a, provider.Scope{Context: fixtureContext, Namespace: fixtureNamespace, Revision: 7}, nil, func(_ context.Context, scope provider.Scope, specs ...provider.Spec) []provider.Capability {
			if scope.Context != fixtureContext || scope.Namespace != fixtureNamespace || scope.UID != fixtureUID || len(specs) != 1 || specs[0].Executable != "kubectl" || strings.Join(specs[0].VersionArgs, " ") != "version --client=true --output=json" {
				t.Fatal("provider scope or fixed args changed", scope, specs)
			}
			return []provider.Capability{{Scope: scope, State: state, Source: "Fixture CLI version", Version: "Bearer version-token", Detail: "unsafe raw diagnostic", ObservedAt: time.Now()}}
		})
		if err != nil || results[0].State != string(state) || strings.Contains(results[0].Detail, "unsafe raw diagnostic") || strings.Contains(results[0].Detail, "version-token") {
			t.Fatal(results, err)
		}
	}
}
func TestDeniedIncompleteAndCanceledRemainUnavailable(t *testing.T) {
	a := fixture(t)
	for _, state := range []string{inspect.ObservationDenied, inspect.ObservationIncomplete} {
		results, err := Rerun(t.Context(), a, provider.Scope{Context: fixtureContext, Namespace: fixtureNamespace, Revision: 7}, func(_ context.Context, id inspect.ResourceIdentity) inspect.Observation {
			return inspect.Observation{Identity: id, State: state, Source: "Fixture", ObservedAt: time.Now(), Object: map[string]any{"partial": "excluded"}}
		}, nil)
		if err != nil || results[0].State != state || results[0].Observation.Object != nil {
			t.Fatal(results, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results, err := Rerun(ctx, a, provider.Scope{Context: fixtureContext, Namespace: fixtureNamespace, Revision: 7}, func(context.Context, inspect.ResourceIdentity) inspect.Observation {
		t.Fatal("canceled check executed")
		return inspect.Observation{}
	}, nil)
	if err != nil || results[0].State == inspect.ObservationComplete {
		t.Fatal(results, err)
	}
}

func TestSuccessfulRerunRemainsSeparateFromRetainedEvidence(t *testing.T) {
	a := fixture(t)
	before := a.Evidence.Observations[0].ObservedAt
	results, err := Rerun(t.Context(), a, provider.Scope{Context: fixtureContext}, func(_ context.Context, id inspect.ResourceIdentity) inspect.Observation {
		return inspect.NewObservation(id, "Explicit fixture API", time.Now(), map[string]any{fixtureSpecField: map[string]any{"replicas": int64(3)}})
	}, nil)
	if err != nil || results[0].State != inspect.ObservationComplete || results[0].Observation == nil || a.Evidence.Observations[0].ObservedAt != before || len(a.Latest) != 0 {
		t.Fatal(results, err)
	}
	a.Latest = results
	safe, err := Safe(a)
	if err != nil {
		t.Fatal(err)
	}
	safe.Latest[0].Observation.Object[fixtureSpecField].(map[string]any)["replicas"] = int64(99)
	if results[0].Observation.Object[fixtureSpecField].(map[string]any)["replicas"] != int64(3) {
		t.Fatal("latest result shares mutable snapshot")
	}
}

func TestImportedLatestCannotClaimCompleteMissingSnapshot(t *testing.T) {
	a := fixture(t)
	o := a.Evidence.Observations[0]
	o.Object = nil
	a.Latest = []Result{{Check: a.Checks[0], State: inspect.ObservationComplete, Source: "Imported fixture", ObservedAt: time.Now(), Observation: &o}}
	if _, err := Safe(a); err == nil {
		t.Fatal("import claimed complete latest result with no snapshot")
	}
}
