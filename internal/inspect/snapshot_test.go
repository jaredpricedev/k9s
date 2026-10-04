// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func snapshotFixture(uid, version string, replicas int64) Observation {
	return NewObservation(ResourceIdentity{Context: "lab", GVR: "apps/v1/deployments", Namespace: "ns", Name: "app", UID: uid}, "Kubernetes API observation", time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC), map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "app", "uid": uid, "resourceVersion": version, "managedFields": []any{map[string]any{"manager": version}}}, "spec": map[string]any{"replicas": replicas},
	})
}

func TestComparisonKeepsBaselineAndReversesNoiseNormalization(t *testing.T) {
	a := snapshotFixture("uid", "1", 1)
	b := snapshotFixture("uid", "2", 2)
	a.Object["removed"] = "before"
	b.Object["added"] = "after"
	before, _ := json.Marshal(a)
	c := Compare(a, b, true)
	if !c.Comparable || c.Recreated || len(c.Changes) != 3 || len(c.Omitted) != 2 {
		t.Fatalf("comparison: %+v", c)
	}
	for i, want := range []string{"added", "removed", "changed"} {
		if c.Changes[i].Kind != want {
			t.Fatal(c.Changes)
		}
	}
	full := Compare(a, b, false)
	if len(full.Changes) != 5 || len(full.Omitted) != 0 {
		t.Fatal("normalization could not be reversed", full)
	}
	after, _ := json.Marshal(a)
	if !bytes.Equal(before, after) {
		t.Fatal("comparison changed chosen baseline")
	}
	text := ComparisonText(c)
	for _, label := range []string{"A · chosen baseline", "B · comparison observation", "Context: lab", "apps/v1/deployments ns/app", "UID: uid", "Source: Kubernetes API observation", "2026-10-04T01:02:03Z"} {
		if !strings.Contains(text, label) {
			t.Fatalf("missing identity/source/time %q", label)
		}
	}
}

func TestComparisonLabelsRecreatedAndUnavailableObservations(t *testing.T) {
	a, b := snapshotFixture("old", "1", 1), snapshotFixture("new", "2", 1)
	if c := Compare(a, b, true); !c.Recreated || !strings.Contains(ComparisonText(c), "RECREATED IDENTITY") {
		t.Fatal("same-name replacement hidden", c)
	}
	for _, state := range []string{ObservationDenied, ObservationUnknown, ObservationIncomplete, ObservationStale} {
		b.State, b.Reason = state, "fixture unavailable"
		c := Compare(a, b, true)
		text := ComparisonText(c)
		if c.Comparable || len(c.Changes) != 0 || !strings.Contains(text, "State: "+state) || strings.Contains(text, "No differences") {
			t.Fatal("unavailable input became no changes", state, text)
		}
	}
}

func TestObservationExcludesSecretAndRedactsResourceCredentials(t *testing.T) {
	for _, identity := range []ResourceIdentity{{GVR: "v1/secrets"}, {GVR: "v1/configmaps"}} {
		secret := NewObservation(identity, "API", time.Now(), map[string]any{"kind": "Secret", "data": map[string]any{"password": "TOP-SECRET-BASE64"}, "metadata": map[string]any{"annotations": map[string]any{"last-applied-configuration": "secret manifest"}}})
		raw, _ := json.Marshal(secret)
		if secret.Object != nil || secret.State != ObservationIncomplete || strings.Contains(string(raw), "TOP-SECRET") || strings.Contains(string(raw), "secret manifest") {
			t.Fatal("Secret retained", string(raw))
		}
	}
	input := map[string]any{"kind": "Deployment", "spec": map[string]any{"password": "raw-pass", "accessToken": "raw-token", "message": "Bearer abc.secret", "env": []any{map[string]any{"name": "API_TOKEN", "value": "raw-env"}}, "args": []any{"--password=raw-arg"}}}
	o := NewObservation(ResourceIdentity{GVR: "apps/v1/deployments"}, "API", time.Now(), input)
	raw, _ := json.Marshal(o)
	for _, secret := range []string{"raw-pass", "raw-token", "abc.secret", "raw-env", "raw-arg"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("credential retained", secret, string(raw))
		}
	}
	if len(o.Limits) == 0 || input["spec"].(map[string]any)["password"] != "raw-pass" {
		t.Fatal("safety did not report redaction or mutated caller")
	}
}

func TestSnapshotAndComparisonLimits(t *testing.T) {
	o := NewObservation(ResourceIdentity{GVR: "v1/configmaps"}, "API", time.Now(), map[string]any{"large": strings.Repeat("x", 64*1024+1)})
	if o.State != ObservationIncomplete || o.Object != nil || !strings.Contains(o.Reason, "limits") {
		t.Fatal(o.State, o.Reason)
	}
	tooMany := make(map[string]any, MaxSnapshotFields+1)
	for i := range MaxSnapshotFields + 1 {
		tooMany[fmt.Sprint(i)] = i
	}
	if o := NewObservation(ResourceIdentity{}, "API", time.Now(), tooMany); o.State != ObservationIncomplete || o.Object != nil {
		t.Fatal("field limit ignored")
	}
	a, b := snapshotFixture("uid", "1", 1), snapshotFixture("uid", "1", 1)
	for i := range MaxComparisonChanges + 1 {
		b.Object[fmt.Sprintf("field-%04d", i)] = "added"
	}
	c := Compare(a, b, true)
	if !c.Truncated || len(c.Changes) != MaxComparisonChanges || !strings.Contains(ComparisonText(c), "incomplete comparison") {
		t.Fatal("change cap ignored", len(c.Changes), c.Truncated)
	}
	// A large changed array may be one diff, but its presentation remains bounded.
	a, b = snapshotFixture("uid", "1", 1), snapshotFixture("uid", "1", 1)
	var items []any
	for range 16 {
		items = append(items, strings.Repeat("x", 20000))
	}
	b.Object["items"] = items
	text := ComparisonText(Compare(a, b, false))
	if len(text) > MaxComparisonText || !strings.Contains(text, "Display truncated") {
		t.Fatal("render cap ignored", len(text))
	}
	a, b = snapshotFixture("uid", "1", 1), snapshotFixture("uid", "1", 1)
	for i := range MaxComparisonChanges {
		b.Object[fmt.Sprintf("field-%04d", i)] = strings.Repeat("x", 110)
	}
	text = ComparisonText(Compare(a, b, false))
	if len(text) > MaxComparisonText || !strings.Contains(text, "Display truncated") {
		t.Fatal("footer exceeded render budget", len(text))
	}
}

func TestObservationRemovesControlTextAndNestedSecrets(t *testing.T) {
	o := NewObservation(ResourceIdentity{Context: "\x1b[31mcontext"}, "API", time.Now(), map[string]any{"message": "\x1b[31mred\x1b[0m", "nested": map[string]any{"kind": "Secret", "data": map[string]any{"x": "nested-sensitive"}}})
	raw, _ := json.Marshal(o)
	if strings.Contains(string(raw), "nested-sensitive") || strings.Contains(string(raw), "\\u001b") || o.Identity.Context != "context" {
		t.Fatal("unsafe metadata retained", string(raw))
	}
}

func TestObservationClassifiesFieldsAfterTerminalSanitization(t *testing.T) {
	identity := ResourceIdentity{GVR: "v1/configmaps"}
	for _, object := range []map[string]any{
		{"kind": "Sec\x00ret", "unclassified": "secret-body"},
		{"ki\x00nd": "Secret", "unclassified": "secret-body"},
	} {
		observation := NewObservation(identity, "API", time.Now(), object)
		encoded, err := json.Marshal(observation)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "secret-body") {
			t.Fatal("terminal normalization exposed a Secret body", string(encoded))
		}
	}
	object := map[string]any{
		"kind": "Pod", "pass\x00word": "plain-password", "co\x00mmand": []any{"plain-command"},
		"env":    []any{map[string]any{"na\x00me": "API_TO\x00KEN", "val\x00ue": "plain-env"}},
		"nested": map[string]any{"kind": "Sec\x00ret", "unclassified": "nested-secret"},
	}
	observation := NewObservation(identity, "API", time.Now(), object)
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"plain-password", "plain-command", "plain-env", "nested-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("normalized sensitive field leaked", secret, string(encoded))
		}
	}
}

func TestObservationRejectsSanitizedFieldCollisions(t *testing.T) {
	object := map[string]any{"kind": "Pod", "name": "one", "na\x00me": "two"}
	observation := NewObservation(ResourceIdentity{GVR: "v1/pods"}, "API", time.Now(), object)
	if observation.State != ObservationIncomplete || observation.Object != nil || !strings.Contains(observation.Reason, "collide") {
		t.Fatal("sanitization silently chose a colliding field", observation)
	}
}

func TestObservationRedactsCommonAPIKeyAndSecretEnvironmentNames(t *testing.T) {
	names := []string{"API_KEY", "API-KEY", "apikey", "AWS_ACCESS_KEY_ID", "ACCESS-KEY", "JWT_SECRET", "SIGNING_SECRET_KEY"}
	var env []any
	for _, name := range names {
		env = append(env, map[string]any{"name": name, "value": "private-" + name})
	}
	object := map[string]any{"kind": "Pod", "api_key": "private-api-field", "publicKey": "public-reference", "env": env}
	observation := NewObservation(ResourceIdentity{GVR: "v1/pods"}, "API", time.Now(), object)
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.Contains(string(encoded), "private-"+name) {
			t.Fatal("common credential environment name was not redacted", name)
		}
	}
	if strings.Contains(string(encoded), "private-api-field") || observation.Object["publicKey"] != "public-reference" {
		t.Fatal("credential fields or public key references classified incorrectly", string(encoded))
	}
}
