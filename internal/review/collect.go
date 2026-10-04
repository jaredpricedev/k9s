// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/workspace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	CollectionTimeout = 30 * time.Second
	ReadTimeout       = 10 * time.Second
	collectSecretKind = "Secret"
	collectMetadata   = "metadata"
	collectNamespace  = "namespace"
	collectName       = "name"
	collectAPIVersion = "apiVersion"
	collectKind       = "kind"
)

type resolvedManifest struct {
	manifest Manifest
	mapping  Mapping
	entry    Entry
	captured types.UID
}

type mappingResult struct {
	mapping Mapping
	err     error
}

type namedResult struct {
	object *unstructured.Unstructured
	err    error
}

// Collect reviews authored targets through exact, bounded named GET requests.
// It never lists resources, expands namespaces, mutates a cluster, or treats a
// missing object as proof that an eventual create is authorized or will succeed.
//
//nolint:gocritic // Preserve the value API: each review captures its scope identity.
func Collect(ctx context.Context, reader dynamic.Interface, resolve Resolver, source Source, scope Scope, now time.Time) Snapshot {
	scope = copyScope(&scope)
	snapshot := Snapshot{Source: source.Identity, Scope: scope, ObservedAt: now}
	ctx, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	selector, selectorErr := labels.Parse(scope.LabelSelector)
	prepared := prepareTargets(ctx, resolve, &source, &scope, selector, selectorErr)
	readTargets(ctx, reader, prepared, selector, now)
	for index := range prepared {
		snapshot.Entries = append(snapshot.Entries, prepared[index].entry)
	}
	return snapshot
}

func prepareTargets(ctx context.Context, resolve Resolver, source *Source, scope *Scope, selector labels.Selector, selectorErr error) []resolvedManifest {
	prepared := make([]resolvedManifest, min(len(source.Objects), MaxManifests))
	mappings := make(map[string]mappingResult)
	identities := make(map[string]int)
	for index := range prepared {
		current := &prepared[index]
		current.manifest = source.Objects[index]
		manifest := &current.manifest
		current.entry = Entry{Document: manifest.Document, Identity: Identity{
			Context: scope.Context, APIVersion: manifest.APIVersion, Kind: manifest.Kind, Namespace: manifest.Namespace, Name: manifest.Name,
		}}
		prepareTarget(ctx, resolve, current, scope, selector, selectorErr, mappings, len(source.Objects) <= MaxManifests)
		if current.entry.State != "" {
			continue
		}
		// Different API versions and an omitted/defaulted namespace can resolve
		// to the same target. Conflicting documents must not silently pick a winner.
		targetKey := current.mapping.GVR.GroupResource().String() + ":" + manifest.Namespace + "/" + manifest.Name
		if prior, exists := identities[targetKey]; exists {
			prepared[prior].entry.State, prepared[prior].entry.Reason = StateExcluded, "Multiple authored documents resolve to this same target"
			current.entry.State, current.entry.Reason = StateExcluded, "Multiple authored documents resolve to this same target"
		} else {
			identities[targetKey] = index
		}
	}
	return prepared
}

func prepareTarget(ctx context.Context, resolve Resolver, current *resolvedManifest, scope *Scope,
	selector labels.Selector, selectorErr error, mappings map[string]mappingResult, withinLimit bool,
) {
	manifest := &current.manifest
	if !withinLimit {
		current.entry.State, current.entry.Reason = StateExcluded, "Source exceeds the 64-document review limit; no targets were read"
		return
	}
	if manifest.SecretExcluded || strings.EqualFold(manifest.Kind, collectSecretKind) {
		current.entry.State, current.entry.Reason = StateExcluded, "Secret contents are excluded from change review"
		return
	}
	if scope.Context == "" || selectorErr != nil {
		current.entry.State, current.entry.Reason = StateOutScope, "An explicit context and valid scope selector are required"
		return
	}
	if reason := validateManifest(manifest); reason != "" {
		current.entry.State, current.entry.Reason = StateUnknown, reason
		return
	}
	if manifest.Namespace == "" {
		if scope.DefaultNamespace == "" || len(scope.Namespaces) == 0 {
			current.entry.State, current.entry.Reason = StateOutScope, "Namespace is omitted; an unambiguous explicit default is required"
			return
		}
		manifest.Namespace = scope.DefaultNamespace
		current.entry.Identity.Namespace = manifest.Namespace
	}
	if !validNamespace(manifest.Namespace) || !namespaceAllowed(scope, manifest.Namespace) {
		current.entry.State, current.entry.Reason = StateOutScope, "Authored namespace is outside the explicit review scope"
		return
	}
	if ctx.Err() != nil {
		current.entry.State, current.entry.Reason = StateUnknown, collectionFailure(ctx.Err())
		return
	}
	key := manifest.APIVersion + ":" + manifest.Kind
	mapped, found := mappings[key]
	if !found {
		mapped = boundedResolve(ctx, resolve, manifest.APIVersion, manifest.Kind)
		mappings[key] = mapped
	}
	if mapped.err != nil {
		current.entry.State, current.entry.Reason = StateUnknown, "Exact authored API version and kind could not be resolved"
		return
	}
	current.mapping = mapped.mapping
	current.entry.Identity.GVR = mapped.mapping.GVR
	if !mapped.mapping.Namespaced {
		current.entry.State, current.entry.Reason = StateExcluded, "Cluster-scoped targets are excluded from this namespaced review"
		return
	}
	if !mappingMatches(manifest, mapped.mapping) {
		current.entry.State, current.entry.Reason = StateUnknown, "Resolved API mapping does not match the authored version and kind"
		return
	}
	if mapped.mapping.GVR.Resource == "secrets" {
		current.entry.State, current.entry.Reason = StateExcluded, "Secret contents are excluded from change review"
		return
	}
	if !kindAllowed(scope.Kinds, manifest, mapped.mapping.GVR) {
		current.entry.State, current.entry.Reason = StateOutScope, "Authored kind is outside the saved kind selection"
		return
	}
	current.captured = scope.CapturedUIDs[IdentityKey(mapped.mapping.GVR, manifest.Namespace, manifest.Name)]
	current.entry.Identity.UID = current.captured
	if !selector.Matches(labels.Set(manifestLabels(manifest))) && current.captured == "" {
		current.entry.State, current.entry.Reason = StateOutScope, "Authored labels do not match the saved selector and no captured identity permits the change"
	}
}

func readTargets(ctx context.Context, reader dynamic.Interface, prepared []resolvedManifest, selector labels.Selector, now time.Time) {
	reads := 0
	for index := range prepared {
		current := &prepared[index]
		if current.entry.State != "" {
			continue
		}
		if reads >= MaxManifests {
			current.entry.State, current.entry.Reason = StateExcluded, "The 64-target read limit was reached"
			continue
		}
		if ctx.Err() != nil {
			current.entry.State, current.entry.Reason = StateUnknown, collectionFailure(ctx.Err())
			continue
		}
		if reader == nil {
			current.entry.State, current.entry.Reason = StateUnknown, "A Kubernetes reader is unavailable; no live comparison was made"
			continue
		}
		reads++
		current.entry.ObservedAt = now
		readTarget(ctx, reader, current, selector)
	}
}

func readTarget(ctx context.Context, reader dynamic.Interface, current *resolvedManifest, selector labels.Selector) {
	result := boundedGet(ctx, reader, current.mapping.GVR, current.manifest.Namespace, current.manifest.Name)
	if result.err != nil {
		readFailure(current, result.err)
		return
	}
	live := result.object
	if !liveMatches(&current.manifest, live) {
		current.entry.State, current.entry.Reason = StateUnknown, "Live response identity did not match the authored API version, kind, namespace, and name"
		return
	}
	if current.captured != "" && live.GetUID() != current.captured {
		current.entry.State, current.entry.Reason = StateStale, "Live UID differs from the captured resource; reopen the scope before reviewing its replacement"
		return
	}
	current.entry.Identity.UID = live.GetUID()
	if !selector.Matches(labels.Set(live.GetLabels())) && current.captured == "" {
		current.entry.State, current.entry.Reason = StateOutScope, "Live labels do not match the saved selector and no captured identity permits the change"
		return
	}
	current.entry.Intent = CompareIntent(current.manifest, live.Object)
	current.entry.Ownership = ownershipMarkers(live)
	switch {
	case len(current.entry.Intent.Changes) > 0:
		current.entry.State, current.entry.Reason = StateChanged, "Declared intent differs in the reviewed fields; defaulted and omitted fields are not a prune plan"
		if current.entry.Intent.Truncated {
			current.entry.Reason = "Only part of the declared intent was reviewed; additional differences may be unreviewed. Defaulted and omitted fields are not a prune plan"
		}
	case current.entry.Intent.Truncated:
		current.entry.State, current.entry.Reason = StateUnknown, "Comparison was incomplete; reviewed fields cannot establish an intent match"
	case current.entry.Intent.DeclaredFields == 0:
		current.entry.State, current.entry.Reason = StateUnknown, "No declared fields could be reviewed; omitted and sensitive fields do not establish equality"
	default:
		current.entry.State, current.entry.Reason = StateMatch, "Reviewed declared fields match; omitted and sensitive fields remain unreviewed"
	}
}

func readFailure(current *resolvedManifest, err error) {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		current.entry.State, current.entry.Reason = StateDenied, "Access to this named resource was denied; no live comparison was made"
	case apierrors.IsNotFound(err):
		if current.captured != "" {
			current.entry.State, current.entry.Reason = StateStale,
				"The captured resource is no longer readable at this identity; reopen the scope before reviewing a create candidate"
		} else {
			current.entry.State, current.entry.Reason = StateCreate, "Named target was not found; a create candidate is not an apply or permission check"
			current.entry.Intent = CompareIntent(current.manifest, nil)
			if current.entry.Intent.Truncated {
				current.entry.Reason = "Named target was not found; authored intent was only partially reviewed. A create candidate is not an apply or permission check"
			}
		}
	default:
		current.entry.State, current.entry.Reason = StateUnknown, collectionFailure(err)
	}
}

func copyScope(scope *Scope) Scope {
	capture := *scope
	capture.Namespaces = append([]string(nil), scope.Namespaces...)
	capture.Kinds = append([]string(nil), scope.Kinds...)
	if scope.CapturedUIDs != nil {
		copied := make(map[string]types.UID, len(scope.CapturedUIDs))
		for key, uid := range scope.CapturedUIDs {
			copied[key] = uid
		}
		capture.CapturedUIDs = copied
	}
	return capture
}

func validateManifest(manifest *Manifest) string {
	if !validReviewVersion(manifest.APIVersion) {
		return "Authored API version is not valid"
	}
	if !sourceKind(manifest.Kind) || !validReviewName(manifest.Name) {
		return "Authored resource kind or name is not valid"
	}
	if manifest.Object == nil || manifest.Object[collectAPIVersion] != manifest.APIVersion || manifest.Object[collectKind] != manifest.Kind {
		return "Authored manifest identity fields do not match the loaded source"
	}
	name, _, _ := unstructured.NestedString(manifest.Object, collectMetadata, collectName)
	namespace, _, _ := unstructured.NestedString(manifest.Object, collectMetadata, collectNamespace)
	if name != manifest.Name || namespace != manifest.Namespace {
		return "Authored manifest metadata does not match the loaded source"
	}
	return ""
}

func validNamespace(namespace string) bool {
	return namespace != "" && namespace != "*" && namespace != "all" && len(validation.IsDNS1123Label(namespace)) == 0
}

func validReviewName(name string) bool {
	return sourceIdentityText(name, 253) && len(pathvalidation.IsValidPathSegmentName(name)) == 0
}

func validReviewVersion(version string) bool {
	gv, err := schema.ParseGroupVersion(version)
	return sourceIdentityText(version, 320) && err == nil && gv.String() == version && gv.Version != "" &&
		len(validation.IsDNS1035Label(gv.Version)) == 0 && (gv.Group == "" || len(validation.IsDNS1123Subdomain(gv.Group)) == 0)
}

func namespaceAllowed(scope *Scope, namespace string) bool {
	if len(scope.Namespaces) == 0 {
		return true // The caller checked that this namespace was authored explicitly.
	}
	allowed := false
	for _, selected := range scope.Namespaces {
		if !validNamespace(selected) {
			return false
		}
		if selected == namespace {
			allowed = true
		}
	}
	return allowed
}

func mappingMatches(manifest *Manifest, mapping Mapping) bool {
	gv, err := schema.ParseGroupVersion(manifest.APIVersion)
	return err == nil && mapping.GVR.Group == gv.Group && mapping.GVR.Version == gv.Version &&
		mapping.GVR.Resource != "" && len(validation.IsDNS1123Subdomain(mapping.GVR.Resource)) == 0
}

func kindAllowed(kinds []string, manifest *Manifest, gvr schema.GroupVersionResource) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, selected := range kinds {
		selected = strings.ToLower(strings.TrimSpace(selected))
		if strings.Contains(selected, "/") {
			resolved, _, err := workspace.ResolveKind(selected)
			if err == nil && resolved == gvr {
				return true
			}
			continue
		}
		if strings.EqualFold(selected, manifest.Kind) || selected == gvr.Resource {
			return true
		}
		resolved, _, err := workspace.ResolveKind(selected)
		if err == nil && resolved.Group == gvr.Group && resolved.Resource == gvr.Resource {
			return true
		}
	}
	return false
}

func manifestLabels(manifest *Manifest) map[string]string {
	result, _, _ := unstructured.NestedStringMap(manifest.Object, collectMetadata, "labels")
	return result
}

func liveMatches(manifest *Manifest, live *unstructured.Unstructured) bool {
	return live != nil && live.GetAPIVersion() == manifest.APIVersion && live.GetKind() == manifest.Kind &&
		live.GetNamespace() == manifest.Namespace && live.GetName() == manifest.Name && live.GetUID() != ""
}

func boundedResolve(ctx context.Context, resolve Resolver, apiVersion, kind string) mappingResult {
	if resolve == nil {
		return mappingResult{err: fmt.Errorf("API resolver unavailable")}
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	result := make(chan mappingResult, 1)
	go func() {
		mapping, err := resolve(ctx, apiVersion, kind)
		result <- mappingResult{mapping: mapping, err: err}
	}()
	select {
	case resolved := <-result:
		return resolved
	case <-ctx.Done():
		return mappingResult{err: ctx.Err()}
	}
}

func boundedGet(ctx context.Context, reader dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) namedResult {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	result := make(chan namedResult, 1)
	go func() {
		object, err := reader.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		result <- namedResult{object: object, err: err}
	}()
	select {
	case read := <-result:
		if ctx.Err() != nil {
			return namedResult{err: ctx.Err()}
		}
		return read
	case <-ctx.Done():
		return namedResult{err: ctx.Err()}
	}
}

func collectionFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "Review was canceled before this target could be observed"
	case errors.Is(err, context.DeadlineExceeded):
		return "The bounded review deadline expired; no live comparison was made"
	default:
		return "Named resource read was unavailable; no live comparison was made"
	}
}

func ownershipMarkers(object *unstructured.Unstructured) []string {
	var result []string
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller == nil || !*owner.Controller || !sourceKind(owner.Kind) || !validReviewName(owner.Name) || owner.UID == "" {
			continue
		}
		if !validReviewVersion(owner.APIVersion) {
			continue
		}
		result = append(result, logstream.SafeText(fmt.Sprintf("Controller reference: %s %s (unverified metadata)", owner.Kind, owner.Name)))
	}
	markers := object.GetLabels()
	for _, controller := range []struct{ prefix, kind string }{
		{"kustomize.toolkit.fluxcd.io", "Kustomization"}, {"helm.toolkit.fluxcd.io", "HelmRelease"},
	} {
		name, namespace := markers[controller.prefix+"/name"], markers[controller.prefix+"/namespace"]
		if name != "" && len(validation.IsDNS1123Subdomain(name)) == 0 && validNamespace(namespace) {
			result = append(result, logstream.SafeText(fmt.Sprintf("Flux tracking marker: %s %s/%s (unverified metadata)", controller.kind, namespace, name)))
		}
	}
	annotations := object.GetAnnotations()
	name, namespace := annotations["meta.helm.sh/release-name"], annotations["meta.helm.sh/release-namespace"]
	if name != "" && len(validation.IsDNS1123Subdomain(name)) == 0 && validNamespace(namespace) {
		result = append(result, logstream.SafeText(fmt.Sprintf("Helm release marker: %s/%s (unverified metadata)", namespace, name)))
	}
	return result
}
