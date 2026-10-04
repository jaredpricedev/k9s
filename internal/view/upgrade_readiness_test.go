// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/upgrade"
)

func TestUpgradeReadinessLayoutsKeepIdentityCoverageAndLimits(t *testing.T) {
	snapshot := upgrade.Snapshot{
		Context: "prod-west", Namespace: "payments", NamespaceUID: "1234567890abcdef", NamespaceState: "observed",
		ServerVersion: "v1.34.1", ServerVersionState: "observed", ObservedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Nodes:       upgrade.Section{State: "observed", Items: []upgrade.Fact{{Name: "node-1", Version: "v1.34.1"}}, Truncated: true},
		Deployments: upgrade.Section{State: "observed", Items: []upgrade.Fact{{Name: "api/web", Version: "registry.example/api:v2"}}},
	}
	for _, width := range []int{80, 60, 40} {
		text := renderUpgradeReadiness(&snapshot, "", width)
		for _, want := range []string{"prod-west", "payments", "UID:", "native Kubernetes API", "2026-10-04T12:00:00Z", "first 100", "facts", "registry"} {
			if !strings.Contains(text, want) {
				t.Errorf("width %d omitted %q:\n%s", width, want, text)
			}
		}
	}
	if got := renderUpgradeReadiness(&snapshot, "", 39); !strings.Contains(got, "Terminal too small") {
		t.Fatalf("minimum size state missing: %q", got)
	}
}

func TestUpgradeReadinessNeverClaimsCompatibilityOrExhaustiveUsage(t *testing.T) {
	text := renderUpgradeReadiness(&upgrade.Snapshot{Context: "ctx", Namespace: "ns", NamespaceUID: "uid", ObservedAt: time.Now()}, "", 80)
	for _, want := range []string{"unsupported", "do not establish compatibility", "does not exhaust deprecated API usage"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing caveat %q in %s", want, text)
		}
	}
}
