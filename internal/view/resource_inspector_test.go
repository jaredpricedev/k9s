package view

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
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

func TestInspectionDetailKeepsRawModelAndEscapesReportedMarkup(t *testing.T) {
	a := &App{App: &ui.App{Configurator: ui.Configurator{Styles: config.NewStyles()}}}
	d := NewDetails(a, "test", "resource", contentInspection, true)
	d.text.SetDynamicColors(true)
	lines := []string{"CONDITIONS", "[red]reported value"}
	d.TextChanged(lines)
	plain := drawnText(t, d.text, 80, 10)
	if !strings.Contains(plain, "[red]reported value") || strings.Contains(plain, "::b]") {
		t.Fatal(plain)
	}
	d.TextChanged(lines)
	if again := drawnText(t, d.text, 80, 10); again != plain {
		t.Fatal("formatting changed on redraw")
	}
}
