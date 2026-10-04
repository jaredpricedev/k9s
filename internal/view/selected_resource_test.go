// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestUnavailableSelectionsNeverIssueInspectionRequests(t *testing.T) {
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	conn := inspectionConnection{dynamic: dyn}
	views := map[string]ResourceViewer{
		"Pulse":      NewPulse(client.PuGVR),
		"Xray":       NewXray(client.PodGVR),
		"empty list": NewBrowser(client.PodGVR),
	}
	for label, view := range views {
		t.Run(label, func(t *testing.T) {
			target := resolveSelectedResource(view, "selected-context")
			if target.Err() == nil || target.UnavailableReason == "" || target.Context != "selected-context" {
				t.Fatal("missing explicit unavailable identity", target)
			}
			for _, command := range []string{troubleshootCommand, tlsCommand} {
				if _, err := loadTargetInspection(t.Context(), conn, target, command); err == nil {
					t.Fatal(command, "accepted unavailable selection")
				}
				if _, err := loadTargetInspectionReferences(t.Context(), conn, target, command); err == nil {
					t.Fatal(command, "accepted unavailable related selection")
				}
			}
		})
	}
	if len(dyn.Actions()) != 0 {
		t.Fatal("unsupported views sent API requests", dyn.Actions())
	}
}

func TestPulseAndXrayInvestigationCommandsKeepNavigationOpen(t *testing.T) {
	for label, view := range map[string]ResourceViewer{"Pulse": NewPulse(client.PuGVR), "Xray": NewXray(client.PodGVR)} {
		t.Run(label, func(t *testing.T) {
			app := NewApp(mock.NewMockConfig(t))
			app.Content.Push(view)
			command := NewCommand(app)
			for _, name := range []string{troubleshootCommand, tlsCommand} {
				command.investigationCommand(name)
				if app.Content.Top() != view {
					t.Fatal("unsupported inspection replaced navigation view", name)
				}
			}
			command.investigationCommand(actionsCommand)
			palette, ok := app.Content.Top().(*actionPalette)
			if !ok || palette.target.Err() == nil {
				t.Fatal("actions did not open a safe navigation palette", app.Content.Top())
			}
		})
	}
}

func TestCombinedFluxSelectionMatchesNativeIdentity(t *testing.T) {
	gvr := client.NewGVR("kustomize.toolkit.fluxcd.io/v1/kustomizations")
	path := "flux-system/platform"
	combined := resourceTargetForPath(client.FluxGVR, "cluster", gvr.String()+"|"+path)
	native := resourceTargetForPath(gvr, "cluster", path)
	if combined.Err() != nil || combined != native || combined.Namespace != "flux-system" || combined.Name != "platform" {
		t.Fatal("combined row identity diverged", combined, native)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "flux-system", "name": "platform", "uid": "observed-uid"},
	}}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	for _, target := range []SelectedResourceTarget{combined, native} {
		conn := inspectionConnection{dynamic: dyn, typed: kubefake.NewSimpleClientset()}
		text, err := loadTargetInspection(t.Context(), conn, target, troubleshootCommand)
		if err != nil || !strings.Contains(text, "Kustomization flux-system/platform") || !strings.Contains(text, "Selected UID: unknown") {
			t.Fatal("incorrect combined/native snapshot", text, err)
		}
		_, err = loadTargetInspectionReferences(t.Context(), conn, target, troubleshootCommand)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range dyn.Actions() {
		if action.GetNamespace() != "flux-system" || action.GetResource() != gvr.GVR() {
			t.Fatal("combined identity leaked into API request", action)
		}
	}
	for _, path := range []string{gvr.String() + "|flux-system/<restricted>", gvr.String() + "|flux-system/<unavailable>", gvr.String() + "|flux-system/extra/platform"} {
		if target := resourceTargetForPath(client.FluxGVR, "cluster", path); target.Err() == nil {
			t.Fatal("synthetic row accepted", target)
		}
	}
}

func TestInspectionRejectsSameNameReplacementBeforeReadingEvidence(t *testing.T) {
	metas := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = metas })
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"namespace": "team", "name": "app", "uid": "replacement"},
	}}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	target := resourceTargetForPath(client.PodGVR, "cluster", "team/app")
	target.UID = "selected"
	for _, command := range []string{troubleshootCommand, tlsCommand} {
		if text, err := loadTargetInspection(t.Context(), inspectionConnection{dynamic: dyn}, target, command); err == nil || !strings.Contains(err.Error(), "identity changed") || text != "" {
			t.Fatal("replacement rendered as selected object", text, err)
		}
		if refs, err := loadTargetInspectionReferences(t.Context(), inspectionConnection{dynamic: dyn}, target, command); err == nil || !strings.Contains(err.Error(), "identity changed") || refs != nil {
			t.Fatal("replacement supplied related resources", refs, err)
		}
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "get" || action.GetResource() != (schema.GroupVersionResource{Version: "v1", Resource: "pods"}) {
			t.Fatal("read evidence before identity validation", action)
		}
	}
	obj.SetUID("")
	if err := verifySelectedIdentity(target, obj); err == nil || !strings.Contains(err.Error(), "current UID unknown") {
		t.Fatal("missing live identity accepted", err)
	}
	target.UID = ""
	if err := verifySelectedIdentity(target, obj); err != nil || !strings.Contains(resourceSummary(obj), "UID: unknown") {
		t.Fatal("unknown identity not explicit", err, resourceSummary(obj))
	}
}

func TestClusterSelectionUsesEmptyAPINamespace(t *testing.T) {
	target := resourceTargetForPath(client.NodeGVR, "cluster", "-/worker")
	if target.Err() != nil || target.Namespace != "" || target.Path() != "worker" {
		t.Fatal("cluster scope marker leaked to namespace", target)
	}
}
