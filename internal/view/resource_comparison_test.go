// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const comparisonReplacementUID = "new"

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
