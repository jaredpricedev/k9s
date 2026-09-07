package view

import (
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type inspectionConnection struct {
	client.Connection
	dynamic dynamic.Interface
	typed   kubernetes.Interface
}

func (c inspectionConnection) DynDial() (dynamic.Interface, error) { return c.dynamic, nil }
func TestWorkloadScopeAndVisibility(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "PodList"})
	o := &unstructured.Unstructured{Object: map[string]any{"kind": "Deployment", "metadata": map[string]any{"namespace": "ns", "name": "app"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "target"}}}}}
	dyn.PrependReactor("list", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		action := a.(ktesting.ListAction)
		if action.GetNamespace() != "ns" || action.GetListRestrictions().Labels.String() != "app=target" {
			t.Fatal("scope broadened", a)
		}
		return true, &unstructured.UnstructuredList{Object: map[string]any{"metadata": map[string]any{"continue": "more"}}}, nil
	})
	_, notice := workloadPods(t.Context(), inspectionConnection{dynamic: dyn}, o)
	if !strings.Contains(notice, "truncated") {
		t.Fatal(notice)
	}
	o.Object["spec"] = map[string]any{"selector": map[string]any{}}
	before := len(dyn.Actions())
	_, notice = workloadPods(t.Context(), inspectionConnection{dynamic: dyn}, o)
	if notice == "" || len(dyn.Actions()) != before {
		t.Fatal("empty selector listed namespace")
	}
	o.Object["spec"] = map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "target"}}}
	dyn.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fmt.Errorf("forbidden") })
	_, notice = workloadPods(t.Context(), inspectionConnection{dynamic: dyn}, o)
	if !strings.Contains(notice, "forbidden") {
		t.Fatal(notice)
	}
}
func TestGatewayReferencesPreserveNamespaceAndRejectOtherKinds(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{"kind": "Gateway", "metadata": map[string]any{"namespace": "local"}, "spec": map[string]any{"listeners": []any{map[string]any{"tls": map[string]any{"certificateRefs": []any{map[string]any{"name": "cert", "namespace": "remote"}, map[string]any{"name": "other", "kind": "ConfigMap"}}}}}}}}
	refs := tlsSecretReferences(o)
	if len(refs) != 1 || refs[0].Namespace != "remote" {
		t.Fatal(refs)
	}
}
func TestTLSReferenceDoesNotFetchCrossNamespaceSecret(t *testing.T) {
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	o := &unstructured.Unstructured{Object: map[string]any{"kind": "Gateway", "metadata": map[string]any{"namespace": "local"}, "spec": map[string]any{"listeners": []any{map[string]any{"tls": map[string]any{"certificateRefs": []any{map[string]any{"name": "cert", "namespace": "remote"}}}}}}}}
	text, err := tlsResourceReport(t.Context(), inspectionConnection{dynamic: dyn}, o)
	if err != nil || !strings.Contains(text, "Cross-namespace") || len(dyn.Actions()) != 0 {
		t.Fatal(text, err)
	}
}
func TestActionSearchMatchesAllWords(t *testing.T) {
	if !actionMatches("Action | Logs Previous (p)", "previous logs") || actionMatches("Action | Logs (l)", "previous logs") {
		t.Fatal("incorrect action matching")
	}
}

func (c inspectionConnection) Dial() (kubernetes.Interface, error) { return c.typed, nil }
func TestInspectionPinsOriginalClients(t *testing.T) {
	first := fake.NewSimpleDynamicClient(runtime.NewScheme())
	second := fake.NewSimpleDynamicClient(runtime.NewScheme())
	typed := kubefake.NewSimpleClientset()
	mutable := &inspectionConnection{dynamic: first, typed: typed}
	pinned, err := pinInspectionConnection(mutable)
	if err != nil {
		t.Fatal(err)
	}
	mutable.dynamic = second
	mutable.typed = kubefake.NewSimpleClientset()
	got, err := pinned.DynDial()
	if err != nil || got != first {
		t.Fatal("dynamic client followed switched context")
	}
	k, err := pinned.Dial()
	if err != nil || k != typed {
		t.Fatal("typed client followed switched context")
	}
}
