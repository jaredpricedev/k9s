// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package configreview reviews declared configuration relationships without
// treating those declarations as the contents of a running process.
package configreview

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
)

const (
	ConfigMapKind       = "ConfigMap"
	SecretKind          = "Secret"
	Present             = "present"
	Missing             = "missing"
	Denied              = "denied"
	Unknown             = "unknown"
	MaxReferences       = 64
	MaxConsumersPerKind = 100
	MaxKeys             = 256
)

// Reference contains declaration fields only. Literal environment values and
// configuration values never belong to this projection.
type Identity struct {
	inspect.ResourceIdentity
	Kind string
}

type Reference struct {
	Consumer                                                           Identity
	Kind, Name, Key, Container, Variable, Use, Mount, ItemPath, Prefix string
	Optional, SubPath                                                  bool
}

type Object struct {
	Identity                       Identity
	ResourceVersion, State, Reason string
	KeyNames, AnnotationKeys       []string
	KeysKnown, KeysTruncated       bool
	ObservedAt                     time.Time
}

type Coverage struct {
	Source, State, Detail string
	ObservedAt            time.Time
}

type Snapshot struct {
	Identity          Identity
	CapturedAt        time.Time
	References        []Reference
	Objects           []Object
	Coverage          []Coverage
	ConsumersComplete bool
}

var Tabs = []string{"Overview", "References", "Consumers", "Changes", "Evidence"}

func objectKey(namespace, kind, name string) string { return namespace + "/" + kind + "/" + name }

func (s *Snapshot) Find(ref *Reference) *Object {
	for i := range s.Objects {
		o := &s.Objects[i]
		if o.Identity.Namespace == ref.Consumer.Namespace && o.Identity.Kind == ref.Kind && o.Identity.Name == ref.Name {
			return o
		}
	}
	return nil
}

// State is the declaration's latest visible evidence, not a diagnosis of a
// process that may have cached or independently changed its configuration.
func (s *Snapshot) State(ref *Reference) string {
	o := s.Find(ref)
	if o == nil {
		return Unknown
	}
	if o.State != Present {
		if ref.Optional && o.State == Missing {
			return "optional reference missing"
		}
		return o.State
	}
	if ref.Key == "" {
		return "object present; expansion/runtime unknown"
	}
	if !o.KeysKnown {
		return "object present; key visibility unknown"
	}
	if slices.Contains(o.KeyNames, ref.Key) {
		return "declared key present"
	}
	if o.KeysTruncated {
		return "key not retained; coverage incomplete"
	}
	if ref.Optional {
		return "optional key missing"
	}
	return "required key missing"
}

func (s *Snapshot) Overview() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CONFIGURATION REFERENCES · READ ONLY\n%d declarations · %d named objects\n", len(s.References), len(s.Objects))
	gaps := 0
	for i := range s.Coverage {
		if s.Coverage[i].State != inspect.ObservationComplete {
			gaps++
		}
	}
	fmt.Fprintf(&b, "Coverage: %d gaps · 5 opens source/time/limits\n", gaps)
	shown := 0
	seen := make(map[string]bool)
	for i := range s.References {
		r := &s.References[i]
		state := s.State(r)
		key := objectKey(r.Consumer.Namespace, r.Kind, r.Name) + "/" + state
		if !seen[key] && (strings.Contains(state, "missing") || state == Denied || state == Unknown) {
			seen[key] = true
			fmt.Fprintf(&b, "[?] %s %s: %s\n", r.Kind, r.Name, state)
			shown++
			if shown >= 3 {
				break
			}
		}
	}
	if shown == 0 {
		b.WriteString("No missing/denied retained references observed\n")
	}
	b.WriteString("2 References · 3 Consumers · 4 Changes\nSecret key inventory/runtime values unknown\nRuntime files/environment were not read\n")
	return logstream.SafeText(b.String())
}

func (s *Snapshot) ReferenceText() string {
	var b strings.Builder
	b.WriteString("DECLARED REFERENCES\nDeclared selector keys are shown; Secret key inventory and all values are unavailable.\n\n")
	for i := range s.References {
		r := &s.References[i]
		fmt.Fprintf(&b, "%s %s/%s · %s\nConsumer: %s %s/%s UID=%s\n"+
			"Container: %s · variable: %s · use: %s · optional=%t\nKey: %s · prefix: %s\n"+
			"Mount: %s · item path: %s · subPath=%t\n",
			r.Kind, r.Consumer.Namespace, r.Name, s.State(r), r.Consumer.Kind, r.Consumer.Namespace, r.Consumer.Name, r.Consumer.UID,
			r.Container, r.Variable, r.Use, r.Optional, r.Key, r.Prefix, r.Mount, r.ItemPath, r.SubPath)
		if o := s.Find(r); o != nil {
			fmt.Fprintf(&b, "Observed: %s · UID=%s · RV=%s\n%s\n", stamp(o.ObservedAt), o.Identity.UID, o.ResourceVersion, o.Reason)
		}
		if r.Use == "env" || r.Use == "envFrom" {
			b.WriteString("Rollout hypothesis: existing processes may retain earlier environment values; current runtime state is unknown.\n")
		} else if r.SubPath {
			b.WriteString("Kubernetes subPath mounts do not receive projected-volume updates; actual file contents and independent writers were not observed.\n")
		} else if r.Use == "volume" {
			b.WriteString("Projected-volume refresh is asynchronous; a declared reference does not establish current mounted bytes.\n")
		}
		b.WriteByte('\n')
	}
	return logstream.SafeText(b.String())
}

func (s *Snapshot) ConsumerText() string {
	var b strings.Builder
	b.WriteString("VISIBLE DECLARED CONSUMERS\nPod and controller declarations can overlap; these are not a replica total.\n")
	if !s.ConsumersComplete {
		b.WriteString("[?] Consumer coverage is incomplete; no global impact or absence claim.\n")
	}
	seen := make(map[Identity]bool)
	for i := range s.References {
		ref := &s.References[i]
		if !seen[ref.Consumer] {
			seen[ref.Consumer] = true
			fmt.Fprintf(&b, "\n%s %s/%s UID=%s\nContext: %s · GVR: %s\n",
				ref.Consumer.Kind, ref.Consumer.Namespace, ref.Consumer.Name, ref.Consumer.UID, ref.Consumer.Context, ref.Consumer.GVR)
		}
	}
	b.WriteString("\nConsumers are bounded same-namespace spec observations. Runtime usage, admission, disabled controllers and later updates are not established.\n")
	return logstream.SafeText(b.String())
}

// Changes compares the visible projection only. ResourceVersion changes never
// establish value changes, and missing/denied reads never establish deletion.
func (s *Snapshot) Changes(previous *Snapshot) string {
	if previous == nil {
		return "No earlier capture in this view. r explicitly captures another observation.\nValues and historical runtime state are outside this review.\n"
	}
	if previous.Identity != s.Identity {
		return "Captured source identity changed; snapshots are not compared.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OBSERVED METADATA / KEY-NAME CHANGES\n%s -> %s\n", stamp(previous.CapturedAt), stamp(s.CapturedAt))
	for i := range s.Objects {
		current := &s.Objects[i]
		ref := Reference{Consumer: Identity{ResourceIdentity: inspect.ResourceIdentity{Namespace: current.Identity.Namespace}},
			Kind: current.Identity.Kind, Name: current.Identity.Name}
		old := previous.Find(&ref)
		fmt.Fprintf(&b, "\n%s %s/%s\n", current.Identity.Kind, current.Identity.Namespace, current.Identity.Name)
		if old == nil || old.State != Present || current.State != Present {
			b.WriteString("Comparable complete object evidence unavailable; no deletion/value-change inference.\n")
			continue
		}
		if old.Identity.UID != current.Identity.UID {
			b.WriteString("Named object replaced: UID changed; this is a different configuration object.\n")
		}
		fmt.Fprintf(&b, "UID: %s -> %s\nRV: %s -> %s (metadata version only)\n", old.Identity.UID, current.Identity.UID, old.ResourceVersion, current.ResourceVersion)
		if old.KeysKnown && current.KeysKnown && !old.KeysTruncated && !current.KeysTruncated {
			fmt.Fprintf(&b, "Key names: %s -> %s\n", strings.Join(old.KeyNames, ", "), strings.Join(current.KeyNames, ", "))
		} else {
			b.WriteString("Key-name comparison unavailable or incomplete; Secret key visibility remains unknown.\n")
		}
	}
	b.WriteString("\nNo configuration values compared. Rollout necessity and runtime adoption remain hypotheses; open rollout review for controller evidence.\n")
	return logstream.SafeText(b.String())
}

func (s *Snapshot) Evidence() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CAPTURED SOURCE\nContext: %s\nGVR: %s\nNamespace/name: %s/%s\nUID: %s\nCaptured: %s\n",
		s.Identity.Context, s.Identity.GVR, s.Identity.Namespace, s.Identity.Name, s.Identity.UID, stamp(s.CapturedAt))
	for _, c := range s.Coverage {
		fmt.Fprintf(&b, "\n%s: %s at %s\n%s\n", c.Source, c.State, stamp(c.ObservedAt), c.Detail)
	}
	for i := range s.Objects {
		o := &s.Objects[i]
		fmt.Fprintf(&b, "\n%s %s/%s UID=%s RV=%s\nState: %s · observed: %s\n"+
			"Key visibility: %t · truncated: %t\nAnnotation names only: %s\n%s\n",
			o.Identity.Kind, o.Identity.Namespace, o.Identity.Name, o.Identity.UID, o.ResourceVersion, o.State, stamp(o.ObservedAt),
			o.KeysKnown, o.KeysTruncated, strings.Join(o.AnnotationKeys, ", "), o.Reason)
		if o.KeysKnown {
			fmt.Fprintf(&b, "Known ConfigMap key names: %s\n", strings.Join(o.KeyNames, ", "))
		}
	}
	b.WriteString("\nSecret reads negotiate only PartialObjectMetadata; no full-object fallback or Secret LIST. " +
		"Annotation values are discarded. ConfigMap data/binaryData are reduced to bounded key names and never retained or displayed as values. " +
		"Collection is explicit, same-namespace, bounded and read-only.\n")
	return logstream.SafeText(b.String())
}

func stamp(at time.Time) string {
	if at.IsZero() {
		return "not reported"
	}
	return at.UTC().Format(time.RFC3339)
}
