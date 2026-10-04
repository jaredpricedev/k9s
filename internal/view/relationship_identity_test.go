// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestRelationshipsRetainOwnerAndEndpointUIDsWithoutDiscardingEvidence(t *testing.T) {
	obj := relationshipObject(t, `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":{"namespace":"team","name":"slice","ownerReferences":[{"apiVersion":"v1","kind":"Service","name":"api","uid":"service-original"}]},"endpoints":[{"addresses":["10.0.0.2"],"targetRef":{"apiVersion":"v1","kind":"Pod","name":"app","uid":"pod-original"},"conditions":{"ready":false}}]}`)
	refs := objectReferences(obj)
	ownerFound, endpointFound := false, false
	for _, item := range refs {
		ownerFound = ownerFound || (item.ref.Kind == "Service" && item.uid == "service-original")
		endpointFound = endpointFound || (item.ref.Kind == "Pod" && item.uid == "pod-original" && strings.Contains(item.reason, "ready=false"))
	}
	if !ownerFound || !endpointFound {
		t.Fatal("UID or endpoint evidence dropped", refs)
	}
	one := relationshipRef("", "Pod", "team", "app", "targetRef evidence")
	one.uid = "pod-original"
	two := one
	two.reason = "selector evidence"
	replacement := one
	replacement.uid = "pod-replacement"
	replacement.reason = "different observed identity"
	merged := stableRelationships([]inspectionReference{one, two, replacement, relationshipRef("", "Pod", "team", "app", "name-only configuration reference")})
	if len(merged) != 3 {
		t.Fatal("different known or unknown identities merged", merged)
	}
	for _, item := range merged {
		if item.uid == "pod-original" && (!strings.Contains(item.reason, "targetRef evidence") || !strings.Contains(item.reason, "selector evidence")) {
			t.Fatal("deduplication lost provenance", merged)
		}
	}
}

func TestRelatedJumpDetectsReplacementAndStopsAfterCancelledOrExpiredRead(t *testing.T) {
	old := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = old })
	obj := relationshipObject(t, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"team","name":"app","uid":"replacement"}}`)
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	conn := inspectionConnection{dynamic: dyn}
	target := resourceTargetForPath(client.PodGVR, "cluster", "team/app")
	target.UID = "original"
	if _, err := loadRelatedTarget(t.Context(), conn, target); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatal("same-name replacement jump accepted", err)
	}
	target.UID = ""
	observed, err := loadRelatedTarget(t.Context(), conn, target)
	if err != nil || observed.UID != "replacement" {
		t.Fatal("name-only target failed to capture observed UID", observed, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadRelatedTarget(ctx, conn, target); err != context.Canceled {
		t.Fatal("cancelled response became navigation", err)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := loadRelatedTarget(ctx, conn, target); err != context.DeadlineExceeded {
		t.Fatal("expired response became navigation", err)
	}
	dyn.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fmt.Errorf("forbidden") })
	if _, err := loadRelatedTarget(t.Context(), conn, target); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatal("denied visibility not reported", err)
	}
}

func TestRelatedNativeListKeepsExpectedUIDWhenCacheIdentityIsUnknown(t *testing.T) {
	old := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = old })
	b := NewBrowser(client.PodGVR).(*Browser)
	expected := resourceTargetForPath(client.PodGVR, "cluster", "team/app")
	expected.UID = "endpoint-pod"
	b.Table.expectedTarget = &expected
	selected := selectedResourceForPath(b, "cluster", "team/app")
	if selected.Err() != nil || selected.UID != "endpoint-pod" {
		t.Fatal("expected target identity lost without a warm cache", selected)
	}
	other := selectedResourceForPath(b, "cluster", "team/other")
	if other.UID != "" {
		t.Fatal("relationship UID leaked to another selection", other)
	}
	otherContext := selectedResourceForPath(b, "other-cluster", "team/app")
	if otherContext.UID != "" {
		t.Fatal("relationship UID leaked to another context", otherContext)
	}
}

func TestCompleteRelationshipLoaderRejectsCancelledSourceAndAPIGroupCollisions(t *testing.T) {
	old := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = old })
	for _, kind := range []string{"Deployment", "Gateway", "Certificate"} {
		t.Run(kind, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "unrelated.example/v1", "kind": kind,
				"metadata": map[string]any{"namespace": "team", "name": "object", "uid": "source"},
				"spec":     map[string]any{"secretName": "must-not-infer", "selector": map[string]any{"matchLabels": map[string]any{"app": "unrelated"}}},
			}}
			dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
			dyn.PrependReactor("get", "*", func(ktesting.Action) (bool, runtime.Object, error) { return true, obj.DeepCopy(), nil })
			gvr := client.NewGVR("unrelated.example/v1/" + strings.ToLower(kind) + "s")
			target := resourceTargetForPath(gvr, "cluster", "team/object")
			target.UID = "source"
			refs, err := loadTargetInspectionReferences(t.Context(), inspectionConnection{dynamic: dyn}, target, troubleshootCommand)
			if err != nil || len(refs) != 0 || len(dyn.Actions()) != 1 {
				t.Fatal("kind collision inferred relationships or read another resource", refs, err, dyn.Actions())
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := loadTargetInspectionReferences(ctx, inspectionConnection{dynamic: dyn}, target, troubleshootCommand); err != context.Canceled {
				t.Fatal("cancelled source read accepted", err)
			}
		})
	}
}

func TestRelatedPickerStopCancelsOutstandingIdentityRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	p := &relatedPicker{Picker: NewPicker(), cancel: cancel, generation: 4}
	p.Stop()
	if ctx.Err() != context.Canceled || p.generation != 5 || p.cancel != nil {
		t.Fatal("closed picker retained pending read", ctx.Err(), p.generation)
	}
}
