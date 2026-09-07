package view

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResourceSummaryDoesNotDumpSpecOrSecretData(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{
		"kind": "Pod", "metadata": map[string]any{"name": "example", "namespace": "ns"},
		"spec": map[string]any{"password": "DO-NOT-DISPLAY"}, "data": map[string]any{"tls.key": "DO-NOT-DISPLAY"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ContainersNotReady", "message": "waiting"}}},
	}}
	text := resourceSummary(o)
	if strings.Contains(text, "DO-NOT-DISPLAY") || !strings.Contains(text, "Ready: False") {
		t.Fatal(text)
	}
}
