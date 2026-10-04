// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package workspace

import (
	"context"
	"fmt"
	"strings"
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
	testContainerName   = "api"
	testInvalidSelector = "app in ("
)

const (
	testResourceName = "checkout"
	testNamespace    = "prod"
	testPodGVR       = "v1/pods"
)

var testNow = time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)

func testObject(kind, namespace, name string) unstructured.Unstructured {
	object := unstructured.Unstructured{Object: map[string]any{"apiVersion": nativeAPIVersion, "kind": kind}}
	object.SetName(name)
	object.SetNamespace(namespace)
	object.SetUID(types.UID("uid-" + name))
	return object
}

func testReader() *fake.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{{Group: "example.test", Version: nativeAPIVersion, Resource: "widgets"}: "WidgetList"}
	for _, known := range knownKinds {
		listKinds[known.gvr] = known.kind + "List"
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
}

func TestCollectUsesOnlyConfiguredNamespacesAndExactSelector(t *testing.T) {
	reader := testReader()
	var namespaces []string
	reader.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		namespaces = append(namespaces, action.GetNamespace())
		if action.GetResource().Resource != podsResourceName {
			t.Fatalf("unexpected integration query: %v", action.GetResource())
		}
		options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		if options.LabelSelector != "app=checkout,tier in (api,worker)" || options.Limit != PageSize {
			t.Fatalf("unexpected list options: %#v", options)
		}
		object := testObject(kindPod, action.GetNamespace(), testResourceName)
		object.SetLabels(map[string]string{"app": testResourceName, "tier": testContainerName})
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{object}}, nil
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{"stage", testNamespace, "stage"}, Kinds: []string{podsResourceName, "po"}, LabelSelector: "app=checkout,tier in (api,worker)"}, testNow)
	if strings.Join(namespaces, ",") != "stage,prod" || len(snapshot.Resources) != 2 || len(snapshot.Coverage) != 2 {
		t.Fatalf("unexpected scoped inventory: %#v, namespaces %v", snapshot, namespaces)
	}
	for _, resource := range snapshot.Resources {
		if resource.Ref.UID != "uid-checkout" || resource.Ref.GVR != testPodGVR {
			t.Fatalf("missing stable identity: %#v", resource.Ref)
		}
	}
}

func TestCollectFollowsBoundedPagination(t *testing.T) {
	reader := testReader()
	calls := 0
	reader.PrependReactor("list", podsResourceName, func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		if options.Continue != map[bool]string{true: "", false: fmt.Sprintf("page-%d", calls)}[calls == 0] {
			t.Fatalf("incorrect continuation at call %d: %q", calls, options.Continue)
		}
		calls++
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{testObject(kindPod, testNamespace, fmt.Sprintf("pod-%d", calls))}}
		list.SetContinue(fmt.Sprintf("page-%d", calls))
		return true, list, nil
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{testNamespace}, Kinds: []string{podsResourceName}}, testNow)
	if calls != MaxPagesPerQuery || len(snapshot.Resources) != MaxPagesPerQuery || !snapshot.Coverage[0].Truncated || snapshot.Coverage[0].State != coverageTruncated {
		t.Fatalf("pagination must remain bounded: calls=%d snapshot=%#v", calls, snapshot)
	}
}

func TestCollectStopsAtScopeResourceCapEvenIfServerIgnoresLimit(t *testing.T) {
	reader := testReader()
	calls := 0
	reader.PrependReactor("list", podsResourceName, func(action ktesting.Action) (bool, runtime.Object, error) {
		calls++
		list := &unstructured.UnstructuredList{}
		for i := range MaxResources + 10 {
			list.Items = append(list.Items, testObject(kindPod, action.GetNamespace(), fmt.Sprintf("pod-%d", i)))
		}
		return true, list, nil
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{testNamespace, "stage"}, Kinds: []string{podsResourceName}}, testNow)
	if calls != 1 || len(snapshot.Resources) != MaxResources || len(snapshot.Coverage) != 2 || !snapshot.Coverage[0].Truncated || !snapshot.Coverage[1].Truncated {
		t.Fatalf("resource cap not enforced: calls=%d resources=%d coverage=%#v", calls, len(snapshot.Resources), snapshot.Coverage)
	}
}

func TestCollectCoverageSeparatesDeniedAbsentAndUnavailable(t *testing.T) {
	reader := testReader()
	reader.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		gr := action.GetResource().GroupResource()
		switch action.GetResource().Resource {
		case podsResourceName:
			return true, nil, apierrors.NewForbidden(gr, "", fmt.Errorf("read denied"))
		case "deployments":
			return true, nil, apierrors.NewNotFound(gr, "")
		default:
			return true, nil, fmt.Errorf("connection unavailable")
		}
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{testNamespace}, Kinds: []string{podsResourceName, "deployments", "jobs"}}, testNow)
	for i, state := range []string{"denied", "absent", coverageUnavailable} {
		if snapshot.Coverage[i].State != state {
			t.Fatalf("coverage %d: want %s got %#v", i, state, snapshot.Coverage[i])
		}
	}
	if len(snapshot.Findings) != 0 {
		t.Fatal("API failure must be coverage, not a fabricated resource finding")
	}
}

func TestCollectCancellationDoesNotIssueFurtherQueries(t *testing.T) {
	reader := testReader()
	ctx, cancel := context.WithCancel(context.Background())
	reader.PrependReactor("list", podsResourceName, func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, &unstructured.UnstructuredList{}, nil
	})
	snapshot := Collect(ctx, reader, Scope{Namespaces: []string{testNamespace, "stage"}, Kinds: []string{podsResourceName}}, testNow)
	if len(reader.Actions()) != 1 || len(snapshot.Coverage) != 2 {
		t.Fatalf("continued after cancellation: %#v", snapshot.Coverage)
	}
	for _, coverage := range snapshot.Coverage {
		if coverage.State != coverageCanceled {
			t.Fatalf("cancellation not shown: %#v", coverage)
		}
	}
}

func TestCollectCancelledScopeNeverStartsNetworkQueries(t *testing.T) {
	reader := testReader()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot := Collect(ctx, reader, Scope{Namespaces: []string{testNamespace}, Kinds: []string{podsResourceName}}, testNow)
	if len(reader.Actions()) != 0 || len(snapshot.Coverage) != 1 || snapshot.Coverage[0].State != coverageCanceled {
		t.Fatalf("query issued after cancellation: %#v", snapshot)
	}
}

func TestCollectDefaultNeverScansOptionalIntegrations(t *testing.T) {
	reader := testReader()
	reader.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		if strings.Contains(action.GetResource().Group, "fluxcd") || action.GetResource().Group == "cert-manager.io" {
			t.Fatalf("unconfigured integration queried: %#v", action.GetResource())
		}
		return true, &unstructured.UnstructuredList{}, nil
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{testNamespace}}, testNow)
	if len(reader.Actions()) != len(DefaultKinds()) || len(snapshot.Coverage) != len(DefaultKinds()) {
		t.Fatalf("unexpected default queries: %#v", snapshot.Coverage)
	}
}

func TestCollectConfiguredOptionalAPIAbsenceIsCoverage(t *testing.T) {
	reader := testReader()
	reader.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetResource().Resource != "kustomizations" || action.GetNamespace() != testNamespace {
			t.Fatalf("unexpected optional query: %#v", action)
		}
		return true, nil, apierrors.NewNotFound(action.GetResource().GroupResource(), "")
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{testNamespace}, Kinds: []string{"kustomizations"}}, testNow)
	if len(reader.Actions()) != 1 || len(snapshot.Coverage) != 1 || snapshot.Coverage[0].State != "absent" || len(snapshot.Findings) != 0 {
		t.Fatalf("optional API absence became a resource fault: %#v", snapshot)
	}
}

func TestCollectRejectsBroadQueriesSecretContentsAndInvalidSelectors(t *testing.T) {
	for _, scope := range []Scope{
		{Namespaces: nil, Kinds: []string{podsResourceName}},
		{Namespaces: []string{""}, Kinds: []string{podsResourceName}},
		{Namespaces: []string{"all", "*", "../prod"}, Kinds: []string{podsResourceName}},
		{Namespaces: []string{testNamespace}, Kinds: []string{"v1/secrets", "v1/nodes"}},
		{Namespaces: []string{testNamespace}, Kinds: []string{podsResourceName}, LabelSelector: testInvalidSelector},
	} {
		reader := testReader()
		snapshot := Collect(context.Background(), reader, scope, testNow)
		if len(reader.Actions()) != 0 || len(snapshot.Coverage) == 0 {
			t.Fatalf("unsafe scope queried: %#v, coverage %#v", scope, snapshot.Coverage)
		}
	}
}

func TestCollectRejectsCrossNamespaceAndMissingUIDResponses(t *testing.T) {
	reader := testReader()
	reader.PrependReactor("list", podsResourceName, func(ktesting.Action) (bool, runtime.Object, error) {
		missingUID := testObject(kindPod, testNamespace, "missing")
		missingUID.SetUID("")
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{testObject(kindPod, "outside", "outside"), missingUID, testObject(kindPod, testNamespace, "valid")}}, nil
	})
	snapshot := Collect(context.Background(), reader, Scope{Namespaces: []string{testNamespace}, Kinds: []string{podsResourceName}}, testNow)
	if len(snapshot.Resources) != 1 || snapshot.Resources[0].Ref.Name != "valid" || snapshot.Coverage[0].State == coverageComplete {
		t.Fatalf("accepted malformed identities: %#v", snapshot)
	}
}

func TestResolveKindExplicitResources(t *testing.T) {
	for _, input := range []string{"po", podsResourceName, testPodGVR} {
		gvr, kind, err := ResolveKind(input)
		if err != nil || gvrString(gvr) != testPodGVR || kind != kindPod {
			t.Fatalf("resolve %q: %v %s %v", input, gvr, kind, err)
		}
	}
	if gvr, _, err := ResolveKind("example.test/v1/widgets"); err != nil || gvrString(gvr) != "example.test/v1/widgets" {
		t.Fatalf("explicit CRD unsupported: %v %v", gvr, err)
	}
	for _, input := range []string{"secrets", "v1/secrets", "v1/nodes", "apps//deployments", "v1/pods/status", "../v1/pods", "unknown"} {
		if _, _, err := ResolveKind(input); err == nil {
			t.Errorf("accepted invalid/restricted kind %q", input)
		}
	}
}
