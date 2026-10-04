// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const comparisonReplacementUID = "new"
const comparisonRunningPhase = "Running"
const comparisonPhaseKey = "phase"
const comparisonStatusKey = "status"

func TestComparisonViewKeepsExplicitBaseline(t *testing.T) {
	v := &comparisonView{normalize: true}
	a := inspect.NewObservation(inspect.ResourceIdentity{Context: "lab", GVR: "apps/v1/deployments", Namespace: "ns", Name: "app", UID: "old"}, "API", time.Now(), map[string]any{"spec": map[string]any{"replicas": int64(1)}})
	v.acceptObservation(a, true)
	if v.baseline == nil || v.other.State != inspect.ObservationUnknown {
		t.Fatal("B captured without explicit refresh")
	}
	a.Object["spec"].(map[string]any)["replicas"] = int64(99)
	b := inspect.NewObservation(inspect.ResourceIdentity{
		Context: "lab", GVR: "apps/v1/deployments", Namespace: "ns", Name: "app", UID: comparisonReplacementUID,
	}, "API", time.Now(), map[string]any{"spec": map[string]any{"replicas": int64(2)}})
	v.acceptObservation(b, false)
	v.acceptObservation(b, true)
	if v.baseline.Identity.UID != "old" || v.baseline.Object["spec"].(map[string]any)["replicas"] != int64(1) || v.other.Identity.UID != comparisonReplacementUID {
		t.Fatal("B refresh replaced or mutated chosen A")
	}
	if c := inspect.Compare(*v.baseline, v.other, true); !c.Recreated || len(c.Changes) != 1 {
		t.Fatal("replacement observation lost", c)
	}
}

func TestResourceObservationHonorsSelectedIdentityAndPermissions(t *testing.T) {
	gvr := client.NewGVR("apps/v1/deployments")
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "app", "namespace": "ns", "uid": comparisonReplacementUID},
		"spec":     map[string]any{"replicas": int64(1)},
	}}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	conn := inspectionConnection{dynamic: dyn}
	target := SelectedResourceTarget{Context: "lab", GVR: gvr, Namespace: "ns", Name: "app", UID: "old"}
	a := loadResourceObservation(t.Context(), conn, target)
	if a.State != inspect.ObservationStale || !strings.Contains(a.Reason, "identity changed") || a.Identity.UID != comparisonReplacementUID {
		t.Fatal("A silently changed selected identity", a)
	}
	target.UID = ""
	b := loadResourceObservation(t.Context(), conn, target)
	if b.State != inspect.ObservationComplete || b.Identity.UID != comparisonReplacementUID || b.Source != "Kubernetes API observation" || b.ObservedAt.IsZero() {
		t.Fatal("B not explicitly observed", b)
	}
	dyn.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "app", nil)
	})
	denied := loadResourceObservation(t.Context(), conn, target)
	if denied.State != inspect.ObservationDenied || inspect.Compare(b, denied, true).Comparable {
		t.Fatal("permission denial treated as no changes", denied)
	}
}

func TestResourceObservationDoesNotFetchSecretValues(t *testing.T) {
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	o := loadResourceObservation(t.Context(), inspectionConnection{dynamic: dyn}, SelectedResourceTarget{Context: "lab", GVR: client.NewGVR("v1/secrets"), Namespace: "ns", Name: "credentials"})
	if len(dyn.Actions()) != 0 || o.State != inspect.ObservationIncomplete || o.Object != nil {
		t.Fatal("default snapshot fetched Secret", o)
	}
}

func TestResourceObservationInvalidSelection(t *testing.T) {
	o := loadResourceObservation(context.Background(), inspectionConnection{}, SelectedResourceTarget{UnavailableReason: "synthetic view"})
	if o.State != inspect.ObservationUnknown || !strings.Contains(o.Reason, "synthetic") {
		t.Fatal(o)
	}
}

func TestComparisonOverviewPrioritizesChangesAndPreservesEvidence(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	identity := inspect.ResourceIdentity{Context: "lab", GVR: "apps/v1/deployments", Namespace: "team", Name: "app", UID: "old"}
	a := inspect.NewObservation(identity, "API", now, map[string]any{
		"metadata": map[string]any{"resourceVersion": "1", "labels": map[string]any{"app": "before"}},
		"spec":     map[string]any{"replicas": int64(1), "template": strings.Repeat("original ", 60)},
	})
	identity.UID = comparisonReplacementUID
	b := inspect.NewObservation(identity, "API", now.Add(time.Minute), map[string]any{
		"metadata": map[string]any{"resourceVersion": "2", "labels": map[string]any{"app": "after"}},
		"spec":     map[string]any{"replicas": int64(2), "template": strings.Repeat("updated ", 60)},
	})
	v := &comparisonView{Details: NewDetails(NewApp(mock.NewMockConfig(t)), "comparison", "team/app", contentInspection, true), normalize: true}
	v.acceptObservation(a, true)
	v.acceptObservation(b, false)
	captures := 0
	v.loader = func(context.Context, bool) inspect.Observation { captures++; return b }
	v.renderComparison()
	compact := strings.Join(v.model.Peek(), "\n")
	for _, expected := range []string{"3 changed fields", "RECREATED IDENTITY", testInspectionTimestamp, "2026-10-03T12:01:00Z", "UID old", "UID new", "API bookkeeping hidden", "Credential redaction is heuristic", "o overview / all evidence"} {
		if !strings.Contains(compact, expected) {
			t.Fatalf("missing %q:\n%s", expected, compact)
		}
	}
	if strings.Index(compact, "/spec/replicas") > strings.Index(compact, "/metadata/labels/app") {
		t.Fatal("configuration changes buried below metadata", compact)
	}
	if !strings.Contains(v.retainedText, strings.Repeat("updated ", 60)) || strings.Contains(compact, strings.Repeat("updated ", 60)) {
		t.Fatal("full evidence was lost or overview did not shorten values")
	}
	v.showEvidence = true
	v.renderComparison()
	if strings.Join(v.model.Peek(), "\n") != v.retainedText {
		t.Fatal("evidence mode did not reveal retained report")
	}
	v.showEvidence, v.normalize = false, false
	v.renderComparison()
	if !strings.Contains(strings.Join(v.model.Peek(), "\n"), "4 changed fields") || captures != 0 || v.baseline.Identity.UID != "old" {
		t.Fatal("presentation change fetched data or moved baseline A")
	}
}

func TestComparisonOverviewUnavailableAndManyChanges(t *testing.T) {
	identity := inspect.ResourceIdentity{Context: "lab", GVR: "v1/pods", Namespace: "team", Name: "pod", UID: "uid"}
	a := inspect.NewObservation(identity, "API", time.Now(), map[string]any{"spec": map[string]any{"a": int64(1)}})
	b := inspect.Observation{Identity: identity, State: inspect.ObservationDenied, Reason: "forbidden", Limits: []string{"observation unavailable"}}
	text := comparisonOverview(inspect.Compare(a, b, true), true)
	if !strings.Contains(text, "Comparison unavailable") || !strings.Contains(text, "denied") || !strings.Contains(text, "unknown") || strings.Contains(text, "No differences") {
		t.Fatal("missing evidence became an empty comparison", text)
	}
	c := inspect.Comparison{A: a, B: a, Comparable: true}
	for i := range 10 {
		c.Changes = append(c.Changes, inspect.Change{Path: "/spec/replicas", Kind: "changed", Before: i, After: i + 1})
	}
	text = comparisonOverview(c, true)
	if strings.Count(text, "CHANGED /spec/replicas") != 6 || !strings.Contains(text, "+4 more changes") {
		t.Fatal("overview did not limit change preview", text)
	}
	if len(c.Changes) != 10 {
		t.Fatal("overview mutated retained changes")
	}
}

func TestComparisonOverviewLongValuesShowChangedPortion(t *testing.T) {
	container := func(memory string) []any {
		return []any{map[string]any{"name": testWorkspaceAPIName, "image": "example.test/api:v1", "resources": map[string]any{
			"limits":   map[string]any{string(corev1.ResourceCPU): "1", "memory": "256Mi"},
			"requests": map[string]any{string(corev1.ResourceCPU): "250m", "memory": memory},
		}}}
	}
	before, after := comparisonValues(container("128Mi"), container("192Mi"))
	if !strings.Contains(before, "128Mi") || !strings.Contains(after, "192Mi") || before == after {
		t.Fatal("long values hid their actual difference", before, after)
	}
}

func TestComparisonFirstStageShowsSuccessfulCaptureAndExplicitNextAction(t *testing.T) {
	v := &comparisonView{normalize: true}
	a := inspect.NewObservation(inspect.ResourceIdentity{Context: "lab", GVR: "v1/pods", Namespace: "team", Name: testWorkspaceAPIName, UID: "retained-a"}, "API", time.Now(), map[string]any{comparisonStatusKey: map[string]any{comparisonPhaseKey: comparisonRunningPhase}})
	v.acceptObservation(a, true)
	text := comparisonOverview(inspect.Compare(*v.baseline, v.other, true), true)
	if !strings.Contains(text, "A captured; r capture B") || strings.Contains(text, "Comparison unavailable") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "team/api") || v.other.Source != "not captured" || !v.other.ObservedAt.IsZero() || v.baseline.Identity.UID != "retained-a" {
		t.Fatal("first stage implied new observations", text)
	}
}
