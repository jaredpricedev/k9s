// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/flux"
	"github.com/derailed/k9s/internal/inspect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	RouteNative               = "native reviewed apply"
	RouteSource               = "source / controller review"
	RouteAmbiguous            = "ownership unresolved"
	controllerRelation        = "controller reference"
	ownershipSecretKind       = "Secret"
	verifiedController        = "controller UID verified"
	maxOwnershipLinks         = MaxNodes * 32
	maxOwnershipMetadataBytes = 1024 * 1024
)

var ownershipMarkerKeys = []string{
	"app.kubernetes.io/managed-by", "app.kubernetes.io/instance",
	"argocd.argoproj.io/instance", "argocd.argoproj.io/tracking-id",
	"kustomize.toolkit.fluxcd.io/name", "kustomize.toolkit.fluxcd.io/namespace",
	"helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace",
	"meta.helm.sh/release-name", "meta.helm.sh/release-namespace",
}

type Policy struct {
	Route, Reason string
}

// Routing describes the next review step. Metadata and readable name-only
// targets never become proof of ownership. Native routing requires the exact
// captured root and a complete, bounded, UID-verified controlling-owner chain.
func Routing(snapshot *Snapshot) Policy {
	if !validRoutingRoot(snapshot) {
		return Policy{Route: RouteAmbiguous, Reason: "Exact captured resource ownership observation unavailable; native application is blocked"}
	}
	verified := verifiedOwnerNodes(snapshot)
	for index := range snapshot.Nodes {
		node := &snapshot.Nodes[index]
		if node.Provider == ProviderArgo || node.Provider == ProviderFlux {
			if index == 0 {
				return Policy{Route: RouteSource, Reason: "Selected supported controller observed by captured UID; review its source/controller before native application"}
			}
			if verified[index] {
				return Policy{Route: RouteSource, Reason: "UID-verified controlling-owner chain reaches a supported GitOps controller; source/controller review required"}
			}
			return Policy{Route: RouteSource, Reason: "Supported controller observed through tracking or declared references; association alone is not verified ownership"}
		}
	}
	for index := range snapshot.Nodes {
		if len(snapshot.Nodes[index].Markers) > 0 {
			return Policy{Route: RouteSource, Reason: "Possible management or copied tracking metadata observed; source/controller review required. " +
				"Metadata alone is not ownership proof"}
		}
	}
	if !completeNativeChain(snapshot, verified) {
		return Policy{Route: RouteAmbiguous, Reason: "Controlling-owner evidence is incomplete or unverified; refresh ownership evidence rather than guess"}
	}
	return Policy{Route: RouteNative, Reason: "No supported management markers in the complete bounded owner chain. " +
		"Native application needs explicit review; unmanaged ownership is not proven"}
}

func validRoutingRoot(snapshot *Snapshot) bool {
	if snapshot == nil || len(snapshot.Nodes) == 0 || len(snapshot.Nodes) > MaxNodes || len(snapshot.Links) > maxOwnershipLinks {
		return false
	}
	root := &snapshot.Nodes[0]
	if root.Identity != snapshot.Request.Target || root.Identity.Context == "" || root.Identity.UID == "" {
		return false
	}
	seen := make(map[string]bool, len(snapshot.Nodes))
	for index := range snapshot.Nodes {
		node := &snapshot.Nodes[index]
		if _, err := capturedGVR(&node.Identity); err != nil || node.Identity.Context != root.Identity.Context || node.Identity.UID == "" ||
			!validName(node.Identity.Name) || node.Kind == "" || node.Kind == ownershipSecretKind ||
			(node.Identity.Namespace != "" && !validNamespace(node.Identity.Namespace)) {
			return false
		}
		key := ownershipNodeKey(&node.Identity)
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func verifiedOwnerNodes(snapshot *Snapshot) map[int]bool {
	verified := map[int]bool{0: true}
	for range len(snapshot.Nodes) {
		for index := range snapshot.Links {
			link := &snapshot.Links[index]
			if verified[link.From] && validControllerLink(snapshot, link) {
				verified[link.To] = true
			}
		}
	}
	return verified
}

func validControllerLink(snapshot *Snapshot, link *Link) bool {
	return link.From >= 0 && link.From < len(snapshot.Nodes) && link.To >= 0 && link.To < len(snapshot.Nodes) && link.To != link.From &&
		link.Relation == controllerRelation && link.Certainty == verifiedController && link.State == Complete
}

func completeNativeChain(snapshot *Snapshot, verified map[int]bool) bool {
	if snapshot.CollectionState != Complete || snapshot.Partial() || !completeNodeCoverage(snapshot) ||
		len(verified) != len(snapshot.Nodes) {
		return false
	}
	owners := make(map[int]int)
	for index := range snapshot.Links {
		link := &snapshot.Links[index]
		if !validControllerLink(snapshot, link) {
			return false
		}
		owners[link.From]++
		if owners[link.From] > 1 || link.To == 0 {
			return false
		}
	}
	// One parent per node plus reachability gives a complete acyclic chain only
	// when the graph has exactly one edge per independently observed owner.
	return len(snapshot.Links) == len(snapshot.Nodes)-1
}

func completeNodeCoverage(snapshot *Snapshot) bool {
	if len(snapshot.Coverage) != len(snapshot.Nodes) {
		return false
	}
	observed := make(map[string]bool, len(snapshot.Coverage))
	for index := range snapshot.Coverage {
		coverage := &snapshot.Coverage[index]
		key := coverage.Source + ":" + coverage.Scope
		if observed[key] || coverage.State != Complete {
			return false
		}
		observed[key] = true
	}
	for index := range snapshot.Nodes {
		identity := &snapshot.Nodes[index].Identity
		if !observed[identity.GVR+":"+identity.Namespace+"/"+identity.Name] {
			return false
		}
	}
	return true
}

// DeclaredRouting prevents authored management or controlling-owner metadata
// from entering native apply without source/controller review. It also rejects
// malformed metadata instead of treating an unreadable marker as absent.
func DeclaredRouting(object map[string]any) Policy {
	metadata, valid := declaredOwnership(object)
	if !valid {
		return Policy{Route: RouteAmbiguous, Reason: "Authored ownership metadata is malformed, excluded or oversized; native application is blocked"}
	}
	value := &unstructured.Unstructured{Object: object}
	if len(metadata.Markers) > 0 || declaredProvider(value) {
		return Policy{Route: RouteSource, Reason: "Authored controller or possible management metadata requires source/controller review; " +
			"metadata alone is not ownership proof"}
	}
	if len(metadata.Owners) > 0 {
		return Policy{Route: RouteAmbiguous, Reason: "Authored controlling-owner reference requires current UID-verified owner review; creation establishes no ownership"}
	}
	return Policy{Route: RouteNative, Reason: "No supported management metadata declared; unmanaged ownership is not proven"}
}

func declaredProvider(object *unstructured.Unstructured) bool {
	version, err := schema.ParseGroupVersion(object.GetAPIVersion())
	return err == nil && version.Group == argoGroup && object.GetKind() == argoApplication || flux.Supported(flux.GVRFor(object))
}

type ownershipMarker struct {
	Location, Key, Value string
}

type ownershipReference struct {
	APIVersion, Kind, Name, UID string
	BlockOwnerDeletion          *bool
}

type declaredOwnershipMetadata struct {
	Markers []ownershipMarker
	Owners  []ownershipReference
}

func declaredOwnership(object map[string]any) (*declaredOwnershipMetadata, bool) {
	if object == nil || object["kind"] == ownershipSecretKind {
		return nil, false
	}
	if raw, exists := object["metadata"]; exists {
		if _, valid := raw.(map[string]any); !valid {
			return nil, false
		}
	}
	result := &declaredOwnershipMetadata{}
	for _, location := range []string{"labels", "annotations"} {
		values, found, err := unstructured.NestedMap(object, "metadata", location)
		if err != nil {
			return nil, false
		}
		if !found {
			continue
		}
		for _, key := range ownershipMarkerKeys {
			if raw, exists := values[key]; exists {
				value, valid := raw.(string)
				if !valid {
					return nil, false
				}
				result.Markers = append(result.Markers, ownershipMarker{Location: location, Key: key, Value: value})
			}
		}
	}
	owners, _, err := unstructured.NestedSlice(object, "metadata", "ownerReferences")
	if err != nil {
		return nil, false
	}
	for _, raw := range owners {
		owner, valid := declaredOwner(raw)
		if !valid {
			return nil, false
		}
		if owner != nil {
			result.Owners = append(result.Owners, *owner)
		}
	}
	if len(result.Owners) > maxObjectReferences || ownershipDigest(result) == "" {
		return nil, false
	}
	sort.Slice(result.Owners, func(a, b int) bool {
		return result.Owners[a].UID < result.Owners[b].UID
	})
	return result, true
}

func declaredOwner(raw any) (*ownershipReference, bool) {
	owner, valid := raw.(map[string]any)
	if !valid {
		return nil, false
	}
	controller, exists := owner["controller"]
	if !exists || controller == nil {
		return nil, true
	}
	controls, valid := controller.(bool)
	if !valid {
		return nil, false
	}
	if !controls {
		return nil, true
	}
	apiVersion, apiOK := owner["apiVersion"].(string)
	kind, kindOK := owner["kind"].(string)
	name, nameOK := owner["name"].(string)
	uid, uidOK := owner["uid"].(string)
	version, err := schema.ParseGroupVersion(apiVersion)
	if !apiOK || !kindOK || !nameOK || !uidOK || err != nil || !validName(version.Version) ||
		(version.Group != "" && !validName(version.Group)) || kind == "" || !validName(name) || uid == "" {
		return nil, false
	}
	result := &ownershipReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid}
	if rawBlock, found := owner["blockOwnerDeletion"]; found && rawBlock != nil {
		block, ok := rawBlock.(bool)
		if !ok {
			return nil, false
		}
		result.BlockOwnerDeletion = &block
	}
	return result, true
}

// DeclaredOwnershipFingerprint hashes complete allowlisted marker values and
// controlling-owner references before display truncation. Only the digest is
// retained; the graph digest alone cannot pin values omitted from safe labels.
// Empty means malformed, excluded or oversized input and must block execution.
func DeclaredOwnershipFingerprint(object map[string]any) string {
	metadata, valid := declaredOwnership(object)
	if !valid {
		return ""
	}
	return ownershipDigest(metadata)
}

type fingerprintNode struct {
	Identity       inspect.ResourceIdentity
	Kind, Provider string
	Markers        []string
}

type fingerprintLink struct {
	From, To                              string
	Relation, Reference, Certainty, State string
}

type fingerprintCoverage struct {
	Source, Scope, State string
}

// OwnershipFingerprint pins the bounded captured identity/reference graph,
// including incomplete coverage, but excludes resource versions, observations,
// generation and controller health. Pair it with DeclaredOwnershipFingerprint
// on exact named raw objects to pin marker values excluded from safe evidence.
func OwnershipFingerprint(snapshot *Snapshot) string {
	if !validRoutingRoot(snapshot) || len(snapshot.Coverage) > MaxNodes {
		return ""
	}
	projection := struct {
		Request         Request
		CollectionState string
		Nodes           []fingerprintNode
		Links           []fingerprintLink
		Coverage        []fingerprintCoverage
	}{Request: snapshot.Request, CollectionState: snapshot.CollectionState}
	for index := range snapshot.Nodes {
		node := &snapshot.Nodes[index]
		markers := append([]string(nil), node.Markers...)
		sort.Strings(markers)
		projection.Nodes = append(projection.Nodes, fingerprintNode{Identity: node.Identity, Kind: node.Kind, Provider: node.Provider, Markers: markers})
	}
	for index := range snapshot.Links {
		link := &snapshot.Links[index]
		if link.From < 0 || link.From >= len(snapshot.Nodes) || link.To < -1 || link.To >= len(snapshot.Nodes) {
			return ""
		}
		to := "unobserved"
		if link.To >= 0 {
			to = ownershipNodeKey(&snapshot.Nodes[link.To].Identity)
		}
		projection.Links = append(projection.Links, fingerprintLink{From: ownershipNodeKey(&snapshot.Nodes[link.From].Identity), To: to,
			Relation: link.Relation, Reference: link.Reference, Certainty: link.Certainty, State: link.State})
	}
	for index := range snapshot.Coverage {
		coverage := &snapshot.Coverage[index]
		projection.Coverage = append(projection.Coverage, fingerprintCoverage{Source: coverage.Source, Scope: coverage.Scope, State: coverage.State})
	}
	sort.Slice(projection.Nodes, func(a, b int) bool {
		return ownershipNodeKey(&projection.Nodes[a].Identity) < ownershipNodeKey(&projection.Nodes[b].Identity)
	})
	sort.Slice(projection.Links, func(a, b int) bool {
		return ownershipDigest(&projection.Links[a]) < ownershipDigest(&projection.Links[b])
	})
	sort.Slice(projection.Coverage, func(a, b int) bool {
		return ownershipDigest(&projection.Coverage[a]) < ownershipDigest(&projection.Coverage[b])
	})
	return ownershipDigest(&projection)
}

func ownershipNodeKey(identity *inspect.ResourceIdentity) string {
	return strings.Join([]string{identity.Context, identity.GVR, identity.Namespace, identity.Name, identity.UID}, "\x00")
}

func ownershipDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxOwnershipMetadataBytes {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
