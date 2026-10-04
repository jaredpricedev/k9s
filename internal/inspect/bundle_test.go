// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bundleFixture() Bundle {
	return NewBundle([]Observation{{Identity: ResourceIdentity{Context: "fixture", GVR: "v1/secrets", Namespace: "app", Name: "tls", UID: "uid-a"},
		Source: "Kubernetes API", ObservedAt: time.Now().UTC(), State: "complete",
		Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "tls"}, "data": map[string]any{"password": "do-not-share"}}}})
}

func TestBundleRoundTripSafeOfflineEvidence(t *testing.T) {
	b := bundleFixture()
	b.Notes = []string{"Bearer sensitive-token\x1b[31m note"}
	b.Snippets = []Snippet{{Source: "explicit selected log", ObservedAt: time.Now(), Text: "Bearer log-token\x1b[2J", Limits: "one selected line"}}
	for _, markdown := range []bool{false, true} {
		encoded, err := EncodeBundle(b, markdown)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"do-not-share", "sensitive-token", "log-token", "\x1b"} {
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("sensitive content leaked: %s", encoded)
			}
		}
		got, err := DecodeBundle(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		if got.Version != 1 || got.Observations[0].Identity.UID != "uid-a" || got.Observations[0].Source != "Kubernetes API" || len(got.Limits) == 0 {
			t.Fatalf("lost evidence provenance: %+v", got)
		}
	}
	if b.Observations[0].Object["data"].(map[string]any)["password"] != "do-not-share" {
		t.Fatal("sanitizer mutated input")
	}
}

func TestBundleRejectsMalformedAndOversizedInputs(t *testing.T) {
	for _, raw := range []string{`{"version":999}`, `{"version":1,"unknown":true}`, `{}`, `[]`, strings.Repeat(" ", MaxBundleBytes+1)} {
		if _, err := DecodeBundle(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted invalid input %q", raw[:min(40, len(raw))])
		}
	}
	b := bundleFixture()
	b.Notes = []string{strings.Repeat("x", 16<<10+1)}
	if _, err := EncodeBundle(b, false); err == nil {
		t.Fatal("accepted oversized note")
	}
	b = bundleFixture()
	b.Observations[0].ObservedAt = time.Time{}
	if _, err := EncodeBundle(b, false); err == nil {
		t.Fatal("accepted unnamed observation time")
	}
}

func TestBundleFilePermissionsAndNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := SaveBundle(path, bundleFixture()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
	if err := SaveBundle(path, bundleFixture()); err == nil {
		t.Fatal("overwrote existing bundle")
	}
	if _, err := ReadBundle(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := SaveBundle(link, bundleFixture()); err == nil {
		t.Fatal("followed export symlink")
	}
}

func TestBundlePreviewPrioritizesNotesAndEvidenceAndBoundsRendering(t *testing.T) {
	b := bundleFixture()
	b.Observations[0] = NewObservation(ResourceIdentity{Context: "fixture", GVR: "v1/pods", Namespace: "app", Name: "api", UID: "uid"}, "Kubernetes API", time.Now(), map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "api"}})
	b.Notes = []string{"Investigate the observed restart"}
	text, err := BundlePreview(b)
	if err != nil || strings.Index(text, "NOTES") > strings.Index(text, "INCLUDED RESOURCE SNAPSHOT") || !strings.Contains(text, "Context: fixture") || !strings.Contains(text, "State: complete") {
		t.Fatal("preview hides useful evidence", text, err)
	}
	object := map[string]any{"kind": "Pod"}
	for i := range 6 {
		object[string(rune('a'+i))] = strings.Repeat("x", 60<<10)
	}
	b.Observations[0] = NewObservation(b.Observations[0].Identity, "Kubernetes API", time.Now(), object)
	if _, err := BundlePreview(b); err == nil {
		t.Fatal("accepted oversized UI preview")
	}
}

func TestBundleRequiresNamedSanitizedIdentityAndSources(t *testing.T) {
	for _, field := range []string{"name", "gvr", "source", "snippet"} {
		t.Run(field, func(t *testing.T) {
			b := bundleFixture()
			switch field {
			case "name":
				b.Observations[0].Identity.Name = "\x00"
			case "gvr":
				b.Observations[0].Identity.GVR = "\x00"
			case "source":
				b.Observations[0].Source = "\x00"
			case "snippet":
				b.Snippets = []Snippet{{Source: "\x00", ObservedAt: time.Now(), Text: "selected"}}
			}
			if _, err := EncodeBundle(b, false); err == nil {
				t.Fatal("accepted identity/source erased by sanitization")
			}
		})
	}
}

func TestBundlePreviewAndExportUseSameSanitizedProjection(t *testing.T) {
	b := bundleFixture()
	b.Observations[0].Identity.GVR = "v1/configmaps"
	b.Observations[0].Object = map[string]any{"kind": "Sec\x00ret", "unclassified": "disguised-secret-content"}
	b.Notes = []string{"Bearer note-token"}
	preview, err := BundlePreview(b)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeBundle(b, false)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBundle(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	importedPreview, err := BundlePreview(decoded)
	if err != nil || preview != importedPreview {
		t.Fatal("preview differs from exported evidence", err)
	}
	for _, value := range []string{"disguised-secret-content", "note-token"} {
		if strings.Contains(preview, value) || bytes.Contains(encoded, []byte(value)) {
			t.Fatal("sensitive content crossed preview/export boundary", value)
		}
	}
}
