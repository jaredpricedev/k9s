// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	MaxSourceBytes = 1024 * 1024
	MaxManifests   = 64
	MaxChanges     = 2000
	StateChanged   = "changes"
	StateMatch     = "reviewed fields match"
	StateCreate    = "not found"
	StateExcluded  = "excluded"
	StateOutScope  = "out of scope"
	StateDenied    = "denied"
	StateUnknown   = "unknown"
	StateStale     = "identity changed"
)

// Source captures explicit local input. Raw authored objects stay internal to
// the read-only comparison and cannot be serialized into a review report.
type Source struct {
	Identity SourceIdentity
	Objects  []Manifest `json:"-" yaml:"-"`
}

type SourceIdentity struct {
	Path, SHA256 string
	Bytes        int64
	Documents    int
	LoadedAt     time.Time
}

type Manifest struct {
	APIVersion, Kind, Namespace, Name string
	Document                          int
	Object                            map[string]any `json:"-" yaml:"-"`
	SecretExcluded                    bool
}

// Scope is copied from the visible workspace or the explicit browsing
// namespace. CapturedUIDs tie already observed objects to their identities.
type Scope struct {
	Context, DefaultNamespace, LabelSelector string
	Namespaces, Kinds                        []string
	CapturedUIDs                             map[string]types.UID
}

type Mapping struct {
	GVR        schema.GroupVersionResource
	Namespaced bool
}

type Resolver func(context.Context, string, string) (Mapping, error)

type Identity struct {
	Context, APIVersion, Kind, Namespace, Name string
	GVR                                        schema.GroupVersionResource
	UID                                        types.UID
}

// Key associates captured live identities; it is not a Kubernetes selector.
func IdentityKey(gvr schema.GroupVersionResource, namespace, name string) string {
	return gvr.String() + ":" + namespace + "/" + name
}

// Changes contain only display-safe values. Sensitive or omitted fields are
// listed as unreviewed rather than establishing equality or a prune plan.
type Change struct {
	Path, Kind, Before, After string
}

type IntentResult struct {
	Changes                       []Change
	Unreviewed                    []string
	DeclaredFields, MatchedFields int
	Truncated                     bool
}

type Entry struct {
	Identity      Identity
	Document      int
	State, Reason string
	ObservedAt    time.Time
	Intent        IntentResult
	Ownership     []string
}

type Snapshot struct {
	Source     SourceIdentity
	Scope      Scope
	ObservedAt time.Time
	Entries    []Entry
}
