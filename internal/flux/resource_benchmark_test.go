// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package flux

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Each iteration scans a full 10,000-release list. Vary annotation count to
// expose accidental full-map copies in this frequently repeated status read.
func BenchmarkStatusHelmReleases10K(b *testing.B) {
	for _, annotationCount := range []int{4, 64} {
		b.Run(fmt.Sprintf("annotations_%d", annotationCount), func(b *testing.B) {
			releases := make([]*unstructured.Unstructured, 10_000)
			for i := range releases {
				o := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "apps", fmt.Sprintf("release-%d", i))
				o.SetGeneration(7)
				annotations := map[string]string{
					"reconcile.fluxcd.io/requestedAt":                  "request-2",
					"meta.helm.sh/release-name":                        o.GetName(),
					"meta.helm.sh/release-namespace":                   "apps",
					"kubectl.kubernetes.io/last-applied-configuration": strings.Repeat("configuration", 128),
				}
				for j := len(annotations); j < annotationCount; j++ {
					annotations[fmt.Sprintf("platform.example.com/annotation-%d", j)] = "platform-metadata"
				}
				o.SetAnnotations(annotations)
				o.Object["spec"] = map[string]any{
					"suspend": false, "interval": "5m", "timeout": "10m",
					"chartRef": map[string]any{"kind": "OCIRepository", "name": "application-chart", "namespace": "flux-system"},
				}
				conditions := []any{
					condition("Ready", "True", 7, "UpgradeSucceeded", "Helm upgrade succeeded for release apps/application.v7 with chart application@1.2.3"),
					condition("Released", "True", 7, "UpgradeSucceeded", "Helm upgrade succeeded"),
				}
				status := map[string]any{
					"conditions": conditions, "observedGeneration": int64(7),
					"lastHandledReconcileAt": "request-2", "lastAttemptedRevision": "1.2.3",
				}
				switch i % 10 {
				case 0:
					status["lastHandledReconcileAt"] = "request-1"
				case 1:
					status["conditions"] = append(conditions, condition("Reconciling", "True", 7, "Progressing", "running Helm tests"))
				}
				o.Object["status"] = status
				releases[i] = o
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for _, release := range releases {
					state, _ := Status(release)
					if state == "" {
						b.Fatal("empty state")
					}
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(releases)), "ns/release")
		})
	}
}
