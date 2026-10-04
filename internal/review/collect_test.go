// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	fixtureNamespace   = "apps"
	fixtureOtherNS     = "ops"
	fixtureContext     = "demo-dev"
	fixtureName        = "payments"
	fixtureVersion     = "apps/v1"
	fixtureKind        = "Deployment"
	fixtureUID         = types.UID("fixture-payments")
	fixtureSensitive   = "fixture-private-must-not-export"
	fixtureSelector    = "app=daily"
	fixtureLabel       = "daily"
	fixtureStatusField = "status"
)

var (
	fixtureGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	fixtureNow = time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
)

func authored(namespace, name string, replicas int64) Manifest {
	metadata := map[string]any{collectName: name, "labels": map[string]any{"app": fixtureLabel}}
	if namespace != "" {
		metadata[collectNamespace] = namespace
	}
	return Manifest{APIVersion: fixtureVersion, Kind: fixtureKind, Namespace: namespace, Name: name, Document: 1,
		Object: map[string]any{collectAPIVersion: fixtureVersion, collectKind: fixtureKind, collectMetadata: metadata,
			"spec": map[string]any{"replicas": replicas}}}
}

//nolint:gocritic // Tests pass immutable manifest fixtures by value and return independent live objects.
func observedObject(manifest Manifest, uid types.UID) *unstructured.Unstructured {
	object := (&unstructured.Unstructured{Object: manifest.Object}).DeepCopy()
	object.SetUID(uid)
	object.SetResourceVersion("100")
	return object
}

func fixtureScope() Scope {
	return Scope{Context: fixtureContext, Namespaces: []string{fixtureNamespace}, LabelSelector: fixtureSelector,
		Kinds: []string{"deployments"}}
}

func sourceOf(manifests ...Manifest) Source {
	return Source{Identity: SourceIdentity{Path: "/tmp/fixture-release.yaml", SHA256: strings.Repeat("a", 64),
		Documents: len(manifests), LoadedAt: fixtureNow}, Objects: manifests}
}

func fixtureResolve(_ context.Context, apiVersion, kind string) (Mapping, error) {
	if apiVersion != fixtureVersion || kind != fixtureKind {
		return Mapping{}, fmt.Errorf("unmapped fixture type")
	}
	return Mapping{GVR: fixtureGVR, Namespaced: true}, nil
}

func dynamicReader(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
}

//nolint:gocritic // The helper inspects returned snapshot values without mutating their captured scope.
func singleEntry(t *testing.T, snapshot Snapshot) Entry {
	t.Helper()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected one review row: %#v", snapshot)
	}
	return snapshot.Entries[0]
}

func assertOnlyNamedGets(t *testing.T, reader *fake.FakeDynamicClient, namespace, name string) {
	t.Helper()
	for _, action := range reader.Actions() {
		get, ok := action.(ktesting.GetAction)
		if !ok || action.GetVerb() != "get" || action.GetNamespace() != namespace || get.GetName() != name {
			t.Fatalf("unexpected resource action: %#v", action)
		}
	}
}

func TestCollectComparesOnlyNamedScopedAuthoredIntent(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 2)
	live := observedObject(authored(fixtureNamespace, fixtureName, 1), fixtureUID)
	reader := dynamicReader(live)
	snapshot := Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow)
	entry := singleEntry(t, snapshot)
	if entry.State != StateChanged || len(entry.Intent.Changes) == 0 || entry.Identity.UID != fixtureUID || entry.ObservedAt != fixtureNow {
		t.Fatalf("named intent comparison missing: %#v", entry)
	}
	if len(reader.Actions()) != 1 || snapshot.Scope.Context != fixtureContext || snapshot.Source.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("scope or source identity changed: %#v", snapshot)
	}
	assertOnlyNamedGets(t, reader, fixtureNamespace, fixtureName)
}

func TestCollectRejectsSourceScopeViolationsBeforeAnyRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest Manifest
		scope    Scope
	}{
		{"namespace outside subset", authored(fixtureOtherNS, fixtureName, 1), fixtureScope()},
		{"namespace omitted", authored("", fixtureName, 1), fixtureScope()},
		{"empty context", authored(fixtureNamespace, fixtureName, 1), Scope{Namespaces: []string{fixtureNamespace}}},
		{"broad namespace", authored("all", fixtureName, 1), Scope{Context: fixtureContext}},
		{"invalid selected namespace", authored(fixtureNamespace, fixtureName, 1), Scope{Context: fixtureContext, Namespaces: []string{fixtureNamespace, "*"}}},
		{"other kind", authored(fixtureNamespace, fixtureName, 1), Scope{Context: fixtureContext, Namespaces: []string{fixtureNamespace}, Kinds: []string{"pods"}}},
		{"invalid selector", authored(fixtureNamespace, fixtureName, 1), Scope{Context: fixtureContext, Namespaces: []string{fixtureNamespace}, LabelSelector: "app in ("}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := dynamicReader()
			entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(tc.manifest), tc.scope, fixtureNow))
			if entry.State != StateOutScope || len(reader.Actions()) != 0 || len(entry.Intent.Changes) != 0 {
				t.Fatalf("source scope violation caused a read: %#v actions=%#v", entry, reader.Actions())
			}
		})
	}
}

func TestCollectEmptyNamespaceSubsetRequiresAuthoredNamespace(t *testing.T) {
	desired := authored(fixtureOtherNS, fixtureName, 1)
	reader := dynamicReader(observedObject(desired, fixtureUID))
	entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), Scope{Context: fixtureContext}, fixtureNow))
	if entry.State != StateMatch || len(reader.Actions()) != 1 {
		t.Fatalf("explicit authored namespace should be reviewable: %#v", entry)
	}
	assertOnlyNamedGets(t, reader, fixtureOtherNS, fixtureName)
	reader.ClearActions()
	desired = authored("", fixtureName, 1)
	entry = singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), Scope{
		Context: fixtureContext, DefaultNamespace: fixtureNamespace,
	}, fixtureNow))
	if entry.State != StateOutScope || len(reader.Actions()) != 0 {
		t.Fatalf("empty namespace subset must never infer a default: %#v", entry)
	}
}

func TestCollectUnambiguousDefaultIsExplicitAndDoesNotAlterAuthoredObject(t *testing.T) {
	desired := authored("", fixtureName, 1)
	live := observedObject(authored(fixtureNamespace, fixtureName, 1), fixtureUID)
	reader := dynamicReader(live)
	scope := fixtureScope()
	scope.DefaultNamespace = fixtureNamespace
	entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), scope, fixtureNow))
	if entry.State != StateMatch || entry.Identity.Namespace != fixtureNamespace || len(reader.Actions()) != 1 {
		t.Fatalf("explicit default was not honored: %#v", entry)
	}
	if _, found, _ := unstructured.NestedString(desired.Object, collectMetadata, collectNamespace); found {
		t.Fatal("collector mutated the authored manifest")
	}
	assertOnlyNamedGets(t, reader, fixtureNamespace, fixtureName)
}

func TestCollectLabelChangeRequiresMatchingCapturedUID(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 2)
	desired.Object[collectMetadata].(map[string]any)["labels"] = map[string]any{"app": "next"}
	live := observedObject(authored(fixtureNamespace, fixtureName, 1), fixtureUID)
	reader := dynamicReader(live)
	scope := fixtureScope()
	entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), scope, fixtureNow))
	if entry.State != StateOutScope || len(reader.Actions()) != 0 {
		t.Fatalf("source label drift was silently admitted: %#v", entry)
	}
	scope.CapturedUIDs = map[string]types.UID{IdentityKey(fixtureGVR, fixtureNamespace, fixtureName): fixtureUID}
	entry = singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), scope, fixtureNow))
	if entry.State != StateChanged || len(reader.Actions()) != 1 {
		t.Fatalf("captured identity should permit reviewed label drift: %#v", entry)
	}
	reader.ClearActions()
	scope.CapturedUIDs[IdentityKey(fixtureGVR, fixtureNamespace, fixtureName)] = "prior-uid"
	entry = singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), scope, fixtureNow))
	if entry.State != StateStale || entry.Identity.UID != "prior-uid" || len(entry.Intent.Changes) != 0 {
		t.Fatalf("replacement inherited the prior identity's authorization: %#v", entry)
	}
}

func TestCollectLiveLabelsOutsideScopeAreNotComparedWithoutCapturedIdentity(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	live := observedObject(desired, fixtureUID)
	live.SetLabels(map[string]string{"app": "other"})
	reader := dynamicReader(live)
	entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow))
	if entry.State != StateOutScope || len(entry.Intent.Changes) != 0 {
		t.Fatalf("live object outside selector was compared: %#v", entry)
	}
}

func TestCollectReadStatesDoNotBecomeFabricatedDiffs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		uid   types.UID
		state string
	}{
		{"absent candidate", apierrors.NewNotFound(fixtureGVR.GroupResource(), fixtureName), "", StateCreate},
		{"captured absent", apierrors.NewNotFound(fixtureGVR.GroupResource(), fixtureName), fixtureUID, StateStale},
		{"denied", apierrors.NewForbidden(fixtureGVR.GroupResource(), fixtureName, errors.New(fixtureSensitive)), "", StateDenied},
		{"unauthorized", apierrors.NewUnauthorized(fixtureSensitive), "", StateDenied},
		{"unavailable", errors.New(fixtureSensitive), "", StateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := dynamicReader()
			reader.PrependReactor("get", "*", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err })
			scope := fixtureScope()
			if tc.uid != "" {
				scope.CapturedUIDs = map[string]types.UID{IdentityKey(fixtureGVR, fixtureNamespace, fixtureName): tc.uid}
			}
			entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(authored(fixtureNamespace, fixtureName, 1)), scope, fixtureNow))
			if entry.State != tc.state || strings.Contains(entry.Reason, fixtureSensitive) || tc.state != StateCreate && len(entry.Intent.Changes) != 0 {
				t.Fatalf("read failure became a diff or exposed raw errors: %#v", entry)
			}
			if tc.state == StateStale && entry.Identity.UID != fixtureUID {
				t.Fatal("missing captured resource lost its retained UID")
			}
		})
	}
}

func TestCollectValidatesEveryLiveIdentityComponent(t *testing.T) {
	for _, field := range []string{collectAPIVersion, collectKind, collectNamespace, collectName, "uid"} {
		t.Run(field, func(t *testing.T) {
			desired := authored(fixtureNamespace, fixtureName, 1)
			live := observedObject(desired, fixtureUID)
			switch field {
			case collectAPIVersion:
				live.SetAPIVersion("apps/v1beta1")
			case collectKind:
				live.SetKind("StatefulSet")
			case collectNamespace:
				live.SetNamespace(fixtureOtherNS)
			case collectName:
				live.SetName("replacement")
			default:
				live.SetUID("")
			}
			reader := dynamicReader()
			reader.PrependReactor("get", "*", func(ktesting.Action) (bool, runtime.Object, error) { return true, live, nil })
			entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow))
			if entry.State != StateUnknown || len(entry.Intent.Changes) != 0 {
				t.Fatalf("mismatched live identity was compared: %#v", entry)
			}
		})
	}
}

func TestCollectSecretsAndClusterKindsNeverReadOrCompared(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest Manifest
		mapping  Mapping
	}{
		{"secret", Manifest{APIVersion: "v1", Kind: collectSecretKind, Namespace: fixtureNamespace, Name: fixtureName, Document: 1, SecretExcluded: true}, Mapping{}},
		{"cluster kind", authored(fixtureNamespace, fixtureName, 1), Mapping{GVR: fixtureGVR, Namespaced: false}},
		{"secret mapping", authored(fixtureNamespace, fixtureName, 1), Mapping{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "secrets"}, Namespaced: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := dynamicReader()
			resolve := func(context.Context, string, string) (Mapping, error) { return tc.mapping, nil }
			entry := singleEntry(t, Collect(t.Context(), reader, resolve, sourceOf(tc.manifest), fixtureScope(), fixtureNow))
			if entry.State != StateExcluded || len(reader.Actions()) != 0 || len(entry.Intent.Changes) != 0 {
				t.Fatalf("excluded target caused a read or comparison: %#v", entry)
			}
		})
	}
}

func TestCollectResolvesExactAuthoredVersionAndCachesEachGVK(t *testing.T) {
	first := authored(fixtureNamespace, "first", 1)
	second := authored(fixtureNamespace, "second", 1)
	third := authored(fixtureNamespace, "third", 1)
	third.APIVersion = "apps/v1beta1"
	third.Object[collectAPIVersion] = third.APIVersion
	reader := dynamicReader(observedObject(first, "first-uid"), observedObject(second, "second-uid"), observedObject(third, "third-uid"))
	var mu sync.Mutex
	var versions []string
	resolve := func(_ context.Context, apiVersion, kind string) (Mapping, error) {
		mu.Lock()
		versions = append(versions, apiVersion+":"+kind)
		mu.Unlock()
		gv, err := schema.ParseGroupVersion(apiVersion)
		return Mapping{GVR: gv.WithResource("deployments"), Namespaced: true}, err
	}
	snapshot := Collect(t.Context(), reader, resolve, sourceOf(first, second, third), fixtureScope(), fixtureNow)
	if len(versions) != 2 || versions[0] != fixtureVersion+":"+fixtureKind || versions[1] != third.APIVersion+":"+fixtureKind {
		t.Fatalf("resolver changed the authored version or was not cached: %v", versions)
	}
	for _, entry := range snapshot.Entries {
		if entry.State != StateMatch {
			t.Fatalf("exact-version object not reviewed: %#v", entry)
		}
	}
}

func TestCollectMappingFailureCannotBecomeCreateCandidate(t *testing.T) {
	reader := dynamicReader()
	resolve := func(context.Context, string, string) (Mapping, error) { return Mapping{}, errors.New(fixtureSensitive) }
	entry := singleEntry(t, Collect(t.Context(), reader, resolve, sourceOf(authored(fixtureNamespace, fixtureName, 1)), fixtureScope(), fixtureNow))
	if entry.State != StateUnknown || len(reader.Actions()) != 0 || strings.Contains(entry.Reason, fixtureSensitive) {
		t.Fatalf("mapping failure became a create candidate: %#v", entry)
	}
}

func TestCollectDefaultedDuplicateTargetsAreExcludedBeforeReads(t *testing.T) {
	first := authored(fixtureNamespace, fixtureName, 1)
	second := authored("", fixtureName, 2)
	second.Document = 2
	scope := fixtureScope()
	scope.DefaultNamespace = fixtureNamespace
	reader := dynamicReader()
	snapshot := Collect(t.Context(), reader, fixtureResolve, sourceOf(first, second), scope, fixtureNow)
	if len(reader.Actions()) != 0 || len(snapshot.Entries) != 2 {
		t.Fatalf("duplicate intent caused resource reads: %#v", snapshot)
	}
	for _, entry := range snapshot.Entries {
		if entry.State != StateExcluded {
			t.Fatalf("duplicate documents silently picked a winner: %#v", entry)
		}
	}
}

func TestCollectSourceAndReadCountsRemainBounded(t *testing.T) {
	manifests := make([]Manifest, MaxManifests)
	for index := range manifests {
		manifests[index] = authored(fixtureNamespace, fmt.Sprintf("object-%d", index), 1)
		manifests[index].Document = index + 1
	}
	reader := dynamicReader()
	snapshot := Collect(t.Context(), reader, fixtureResolve, sourceOf(manifests...), fixtureScope(), fixtureNow)
	if len(reader.Actions()) != MaxManifests || len(snapshot.Entries) != MaxManifests {
		t.Fatalf("expected bounded named lookups: reads=%d rows=%d", len(reader.Actions()), len(snapshot.Entries))
	}
	for _, action := range reader.Actions() {
		if action.GetVerb() != "get" || action.GetNamespace() != fixtureNamespace {
			t.Fatalf("review performed a list, broad read, or mutation: %#v", action)
		}
	}
	reader.ClearActions()
	manifests = append(manifests, authored(fixtureNamespace, "too-many", 1))
	snapshot = Collect(t.Context(), reader, fixtureResolve, sourceOf(manifests...), fixtureScope(), fixtureNow)
	if len(reader.Actions()) != 0 || len(snapshot.Entries) != MaxManifests {
		t.Fatalf("oversized source escaped the document/read cap: %#v", snapshot)
	}
}

func TestCollectCancellationStopsEvenANonCooperativeGet(t *testing.T) {
	reader := dynamicReader()
	started, release := make(chan struct{}), make(chan struct{})
	reader.PrependReactor("get", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		close(started)
		<-release
		return true, nil, errors.New(fixtureSensitive)
	})
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan Snapshot, 1)
	go func() {
		finished <- Collect(ctx, reader, fixtureResolve, sourceOf(authored(fixtureNamespace, fixtureName, 1)), fixtureScope(), fixtureNow)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("named GET did not begin")
	}
	cancel()
	select {
	case snapshot := <-finished:
		entry := singleEntry(t, snapshot)
		if entry.State != StateUnknown || len(entry.Intent.Changes) != 0 {
			t.Fatalf("canceled request became a comparison: %#v", entry)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("non-cooperative reader kept the review blocked after cancellation")
	}
	close(release)
}

func TestCollectSnapshotCopiesScopeAndDoesNotSerializeLiveMaps(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	live := observedObject(desired, fixtureUID)
	live.Object[fixtureStatusField] = map[string]any{"opaque": fixtureSensitive}
	live.SetAnnotations(map[string]string{"credential-marker": fixtureSensitive})
	scope := fixtureScope()
	scope.CapturedUIDs = map[string]types.UID{IdentityKey(fixtureGVR, fixtureNamespace, fixtureName): fixtureUID}
	snapshot := Collect(t.Context(), dynamicReader(live), fixtureResolve, sourceOf(desired), scope, fixtureNow)
	scope.Namespaces[0] = fixtureOtherNS
	scope.Kinds[0] = "pods"
	scope.CapturedUIDs[IdentityKey(fixtureGVR, fixtureNamespace, fixtureName)] = "replacement"
	if snapshot.Scope.Namespaces[0] != fixtureNamespace || snapshot.Scope.Kinds[0] != "deployments" ||
		snapshot.Scope.CapturedUIDs[IdentityKey(fixtureGVR, fixtureNamespace, fixtureName)] != fixtureUID {
		t.Fatal("later caller edits redirected the retained review scope")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(encoded), fixtureSensitive) || strings.Contains(string(encoded), "\"Object\"") {
		t.Fatalf("snapshot exposed raw source/live content: %s, error=%v", encoded, err)
	}
}

func TestOwnershipMarkersReportOnlyValidatedUnverifiedMetadata(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	live := observedObject(desired, fixtureUID)
	controller, nonController := true, false
	live.SetOwnerReferences([]metav1.OwnerReference{
		{APIVersion: fixtureVersion, Kind: "ReplicaSet", Name: "payments-abc", UID: "owner-uid", Controller: &controller},
		{APIVersion: fixtureVersion, Kind: fixtureKind, Name: "other-owner", UID: "owner-two", Controller: &nonController},
		{APIVersion: fixtureVersion, Kind: "[red]secret", Name: "bad", UID: "bad-owner", Controller: &controller},
	})
	live.SetLabels(map[string]string{"app": fixtureLabel, "owner": fixtureSensitive, "app.kubernetes.io/managed-by": "Helm",
		"kustomize.toolkit.fluxcd.io/name": "release", "kustomize.toolkit.fluxcd.io/namespace": fixtureOtherNS})
	live.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "release", "meta.helm.sh/release-namespace": fixtureOtherNS})
	entry := singleEntry(t, Collect(t.Context(), dynamicReader(live), fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow))
	markers := strings.Join(entry.Ownership, "\n")
	if len(entry.Ownership) != 3 || !strings.Contains(markers, "Controller reference") || !strings.Contains(markers, "Flux tracking marker") ||
		!strings.Contains(markers, "Helm release marker") || strings.Contains(markers, fixtureSensitive) || strings.Contains(markers, "other-owner") {
		t.Fatalf("arbitrary or invalid labels became ownership: %v", entry.Ownership)
	}
	for _, marker := range entry.Ownership {
		if !strings.Contains(marker, "unverified metadata") {
			t.Fatalf("tracking metadata was promoted to ownership proof: %q", marker)
		}
	}
}

func TestCollectAcceptsValidKubernetesPathSegmentNames(t *testing.T) {
	const roleVersion, roleKind, roleName = "rbac.authorization.k8s.io/v1", "Role", "system:view"
	desired := authored(fixtureNamespace, roleName, 1)
	desired.APIVersion, desired.Kind = roleVersion, roleKind
	desired.Object[collectAPIVersion], desired.Object[collectKind] = roleVersion, roleKind
	delete(desired.Object, "spec")
	desired.Object["rules"] = []any{map[string]any{"verbs": []any{"get"}, "resources": []any{"pods"}}}
	reader := dynamicReader(observedObject(desired, fixtureUID))
	resolve := func(_ context.Context, apiVersion, kind string) (Mapping, error) {
		if apiVersion != roleVersion || kind != roleKind {
			t.Fatalf("resolver lost authored role identity: %q %q", apiVersion, kind)
		}
		return Mapping{GVR: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, Namespaced: true}, nil
	}
	entry := singleEntry(t, Collect(t.Context(), reader, resolve, sourceOf(desired), Scope{
		Context: fixtureContext, Namespaces: []string{fixtureNamespace}, Kinds: []string{"roles"},
	}, fixtureNow))
	if entry.State != StateMatch || len(reader.Actions()) != 1 {
		t.Fatalf("valid names containing a colon must remain reviewable: %#v", entry)
	}
	assertOnlyNamedGets(t, reader, fixtureNamespace, roleName)
}

func TestCollectIncompleteComparisonCannotEstablishMatch(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	desired.Object["spec"].(map[string]any)["description"] = strings.Repeat("x", 65537)
	reader := dynamicReader(observedObject(desired, fixtureUID))
	entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow))
	if entry.State != StateUnknown || !entry.Intent.Truncated || !strings.Contains(entry.Reason, "incomplete") || len(reader.Actions()) != 1 {
		t.Fatalf("an incomplete projection incorrectly established a match: %#v", entry)
	}
	if len(entry.Intent.Changes) != 0 {
		t.Fatal("projection failure should not invent field differences")
	}
}

func TestCollectTruncatedDifferencesAndCreateCandidatesAreExplicitlyPartial(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	live := observedObject(desired, fixtureUID)
	desiredFields, liveFields := map[string]any{}, map[string]any{}
	for index := range MaxChanges + 1 {
		key := fmt.Sprintf("field-%04d", index)
		desiredFields[key], liveFields[key] = "desired", "live"
	}
	desired.Object["spec"], live.Object["spec"] = desiredFields, liveFields
	for _, tc := range []struct {
		name  string
		live  []runtime.Object
		state string
	}{
		{"differences", []runtime.Object{live}, StateChanged},
		{"create candidate", nil, StateCreate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := dynamicReader(tc.live...)
			entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow))
			if entry.State != tc.state || !entry.Intent.Truncated || len(entry.Intent.Changes) != MaxChanges ||
				!strings.Contains(entry.Reason, "part") || len(reader.Actions()) != 1 {
				t.Fatalf("partial differences must remain explicitly partial: %#v", entry)
			}
		})
	}
}

func TestCollectSavedKindsHonorAliasesAndExactAuthoredVersions(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	for _, tc := range []struct {
		kind  string
		state string
	}{
		{"deploy", StateMatch},
		{fixtureKind, StateMatch},
		{"apps/v1/deployments", StateMatch},
		{"apps/v1beta1/deployments", StateOutScope},
		{"v1/pods", StateOutScope},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			reader := dynamicReader(observedObject(desired, fixtureUID))
			scope := fixtureScope()
			scope.Kinds = []string{tc.kind}
			entry := singleEntry(t, Collect(t.Context(), reader, fixtureResolve, sourceOf(desired), scope, fixtureNow))
			if entry.State != tc.state || (entry.State == StateOutScope && len(reader.Actions()) != 0) {
				t.Fatalf("saved kind selection changed authored target: %#v, actions=%v", entry, reader.Actions())
			}
			assertOnlyNamedGets(t, reader, fixtureNamespace, fixtureName)
		})
	}
}

func TestCollectOwnershipDoesNotExposeRecognizedCredentialsInValidNames(t *testing.T) {
	desired := authored(fixtureNamespace, fixtureName, 1)
	live := observedObject(desired, fixtureUID)
	controller := true
	live.SetOwnerReferences([]metav1.OwnerReference{
		{APIVersion: fixtureVersion, Kind: "ReplicaSet", Name: "Bearer " + fixtureSensitive, UID: "owner-uid", Controller: &controller},
	})
	entry := singleEntry(t, Collect(t.Context(), dynamicReader(live), fixtureResolve, sourceOf(desired), fixtureScope(), fixtureNow))
	encoded, err := json.Marshal(entry)
	if err != nil || strings.Contains(string(encoded), fixtureSensitive) || len(entry.Ownership) != 1 {
		t.Fatalf("valid owner names exposed credential-like text: %s, error=%v", encoded, err)
	}
	if !strings.Contains(entry.Ownership[0], "unverified metadata") {
		t.Fatalf("redaction changed the metadata's unverified status: %q", entry.Ownership[0])
	}
}
