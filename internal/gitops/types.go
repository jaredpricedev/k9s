// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package gitops retains bounded read-only reconciliation evidence. Tracking
// metadata remains distinct from verified controller references and actual
// workload readiness; source revisions do not identify application versions.
package gitops

import (
	"context"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	MaxNodes       = 16
	MaxDepth       = 6
	MaxConditions  = 16
	MaxSources     = 16
	MaxResources   = 100
	CollectTimeout = 15 * time.Second
	ReadTimeout    = 3 * time.Second
	Complete       = "complete"
	Partial        = "partial"
	Unknown        = "unknown"
	Denied         = "denied"
	Missing        = "missing"
	Absent         = "API absent"
	secretResource = "secrets"
	conditionReady = "Ready"
	ProviderArgo   = "Argo"
	ProviderFlux   = "Flux"
)

var Tabs = []string{"Overview", "Chain", "Sources", "Evidence"}

// Request is a captured destination, not a mutable global context. ArgoNamespace
// is explicitly provided when resource tracking lacks the Application namespace.
// It never defaults to a presumed argocd namespace or triggers cluster discovery.
type Request struct {
	Target        inspect.ResourceIdentity
	ArgoNamespace string
}

// Reader supplies exact named reads and requested API resolution only. Native
// adapters must honor ctx; no shell, broad listing or Secret adapter is involved.
type Reader interface {
	Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	Resolve(ctx context.Context, group, version, kind string) (ResolvedResource, error)
}

// ResolvedResource comes from requested API discovery; namespace scope is
// explicit so controller references cannot silently cross into cluster scope.
type ResolvedResource struct {
	GVR        schema.GroupVersionResource
	Namespaced bool
}

// Snapshot contains safe projections only. Raw objects are never retained.
type Snapshot struct {
	Request         Request
	CapturedAt      time.Time
	CollectionState string
	Nodes           []Node
	Links           []Link
	Coverage        []Coverage
	Limits          []string
}

type Node struct {
	Identity                                     inspect.ResourceIdentity
	Kind, ResourceVersion                        string
	Provider                                     string
	ObservedAt                                   time.Time
	Generation, ObservedGeneration               *int64
	State, Reason                                string
	ReportedSync, ReportedHealth, OperationPhase string
	Suspended, Automated                         *bool
	ReconciledAt                                 string
	Conditions                                   []Condition
	Markers                                      []string
	Versions                                     []Version
	Resources                                    []Resource
	Gaps                                         []string
}

type Condition struct {
	Type, Status, Reason, Message, TransitionTime string
	ObservedGeneration                            *int64
}

// Version keeps version categories separate, even when values happen to match.
type Version struct {
	Kind, Value, Source string
}

// Resource is an Application-reported inventory entry. These entries normally
// lack UIDs and cannot establish verified resource ownership by themselves.
type Resource struct {
	Group, Kind, Namespace, Name, Sync, Health string
}

// Link explains the exact reference used. A readable marker target does not
// convert a name-only tracking label into proof of controller ownership.
type Link struct {
	From, To                                      int
	Relation, Reference, Certainty, State, Reason string
}

type Coverage struct {
	Source, Scope, State, Reason string
	ObservedAt                   time.Time
}
