// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package flux

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSupportedAndGVRFor(t *testing.T) {
	tests := []struct {
		apiVersion string
		kind       string
		resource   string
	}{
		{"source.toolkit.fluxcd.io/v1", "GitRepository", "gitrepositories"},
		{"source.toolkit.fluxcd.io/v1beta2", "OCIRepository", "ocirepositories"},
		{"source.toolkit.fluxcd.io/v1", "HelmRepository", "helmrepositories"},
		{"source.toolkit.fluxcd.io/v1", "HelmChart", "helmcharts"},
		{"source.toolkit.fluxcd.io/v1", "Bucket", "buckets"},
		{"kustomize.toolkit.fluxcd.io/v1", "Kustomization", "kustomizations"},
		{"helm.toolkit.fluxcd.io/v2", "HelmRelease", "helmreleases"},
		{"image.toolkit.fluxcd.io/v1beta2", "ImageRepository", "imagerepositories"},
		{"image.toolkit.fluxcd.io/v1", "ImagePolicy", "imagepolicies"},
		{"image.toolkit.fluxcd.io/v1beta2", "ImageUpdateAutomation", "imageupdateautomations"},
		{"fluxcd.controlplane.io/v1", "ResourceSet", "resourcesets"},
		{"fluxcd.controlplane.io/v1", "ResourceSetInputProvider", "resourcesetinputproviders"},
		{"fluxcd.controlplane.io/v1", "FluxInstance", "fluxinstances"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			o := object(tt.apiVersion, tt.kind, "apps", "demo")
			gvr := GVRFor(o)
			require.NotSame(t, client.NoGVR, gvr)
			assert.Equal(t, tt.resource, gvr.R())
			assert.True(t, Supported(gvr))
		})
	}

	unsupported := object("notification.toolkit.fluxcd.io/v1", "Alert", "apps", "demo")
	assert.Same(t, client.NoGVR, GVRFor(unsupported))
	assert.False(t, Supported(client.NewGVR("notification.toolkit.fluxcd.io/v1/alerts")))
	assert.False(t, Supported(nil))
	assert.False(t, Supported(client.NoGVR))
	assert.Same(t, client.NoGVR, GVRFor(nil))
}

func TestStatusUsesFluxConditionPriorityAndGeneration(t *testing.T) {
	tests := []struct {
		name       string
		generation int64
		spec       map[string]any
		conditions []any
		wantState  string
		wantMsg    string
	}{
		{
			name: "suspension wins", generation: 3, spec: map[string]any{"suspend": true},
			conditions: []any{condition("Ready", "True", 3, "Succeeded", "applied")},
			wantState:  "Suspended", wantMsg: "Reconciliation is suspended",
		},
		{
			name: "stalled wins", generation: 3,
			conditions: []any{
				condition("Ready", "True", 3, "Succeeded", "old ready"),
				condition("Stalled", "True", 3, "Invalid", "invalid spec"),
			},
			wantState: "Failed", wantMsg: "invalid spec",
		},
		{
			name: "active reconcile wins over ready", generation: 4,
			conditions: []any{
				condition("Ready", "True", 3, "Succeeded", "old ready"),
				condition("Reconciling", "True", 4, "Progressing", "building artifact"),
			},
			wantState: "Reconciling", wantMsg: "building artifact",
		},
		{
			name: "stale ready is pending", generation: 4,
			conditions: []any{condition("Ready", "True", 3, "Succeeded", "old ready")},
			wantState:  "Pending", wantMsg: "old ready",
		},
		{
			name: "zero observed generation is pending", generation: 4,
			conditions: []any{condition("Ready", "True", 0, "Succeeded", "unobserved ready")},
			wantState:  "Pending", wantMsg: "unobserved ready",
		},
		{
			name: "missing observed generation is pending", generation: 4,
			conditions: []any{map[string]any{"type": "Ready", "status": "True", "message": "unverified ready"}},
			wantState:  "Pending", wantMsg: "unverified ready",
		},
		{
			name: "current ready", generation: 4,
			conditions: []any{condition("Ready", "True", 4, "Succeeded", "applied")},
			wantState:  "Ready", wantMsg: "applied",
		},
		{
			name: "ready false is failed", generation: 4,
			conditions: []any{condition("Ready", "False", 4, "BuildFailed", "checkout failed")},
			wantState:  "Failed", wantMsg: "checkout failed",
		},
		{
			name: "ready unknown stays unknown", generation: 4,
			conditions: []any{condition("Ready", "Unknown", 4, "Progressing", "waiting")},
			wantState:  "Unknown", wantMsg: "waiting",
		},
		{name: "new object is pending", generation: 1, wantState: "Pending"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "apps", "demo")
			o.SetGeneration(tt.generation)
			if tt.spec != nil {
				o.Object["spec"] = tt.spec
			}
			if tt.conditions != nil {
				o.Object["status"] = map[string]any{"conditions": tt.conditions}
			}
			state, msg := Status(o)
			assert.Equal(t, tt.wantState, state)
			assert.Equal(t, tt.wantMsg, msg)
		})
	}
}

func TestSuspendedSupportsFluxOperatorReconcileAnnotation(t *testing.T) {
	operator := object("fluxcd.controlplane.io/v1", "ResourceSet", "apps", "platform")
	operator.SetAnnotations(map[string]string{"fluxcd.controlplane.io/reconcile": "disabled"})
	assert.True(t, Suspended(operator))
	state, _ := Status(operator)
	assert.Equal(t, "Suspended", state)

	core := object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "apps", "platform")
	core.SetAnnotations(map[string]string{"fluxcd.controlplane.io/reconcile": "disabled"})
	assert.False(t, Suspended(core))
}

func TestStatusTracksReconcileRequestLifecycle(t *testing.T) {
	for _, previous := range []struct {
		name       string
		conditions []any
	}{
		{"ready", []any{condition("Ready", "True", 3, "UpgradeSucceeded", "previous release succeeded")}},
		{"failed", []any{condition("Ready", "False", 3, "UpgradeFailed", "previous release failed")}},
		{"stalled", []any{condition("Stalled", "True", 3, "RetriesExceeded", "previous retries exhausted")}},
	} {
		t.Run(previous.name, func(t *testing.T) {
			o := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "apps", "demo")
			o.SetGeneration(3)
			o.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": "retry-2"})
			status := map[string]any{
				"lastHandledReconcileAt": "retry-1",
				"conditions":             previous.conditions,
			}
			o.Object["status"] = status

			// A metadata-only request does not change generation, so previous
			// conditions cannot report whether the new request has finished.
			state, msg := Status(o)
			assert.Equal(t, "Reconciling", state)
			assert.Contains(t, msg, "Waiting for controller")

			status["conditions"] = []any{
				condition("Ready", "True", 3, "UpgradeSucceeded", "previous release succeeded"),
				condition("Reconciling", "True", 3, "Progressing", "running Helm tests"),
			}
			state, msg = Status(o)
			assert.Equal(t, "Reconciling", state)
			assert.Equal(t, "running Helm tests", msg)

			// Acknowledgement may be patched while retries are still in progress.
			status["lastHandledReconcileAt"] = "retry-2"
			state, msg = Status(o)
			assert.Equal(t, "Reconciling", state)
			assert.Equal(t, "running Helm tests", msg)

			for _, outcome := range []struct {
				condition map[string]any
				state     string
				message   string
			}{
				{condition("Ready", "True", 3, "TestSucceeded", "tests passed"), "Ready", "tests passed"},
				{condition("Ready", "False", 3, "UpgradeFailed", "upgrade failed"), "Failed", "upgrade failed"},
				{condition("Stalled", "True", 3, "RetriesExceeded", "retries exhausted"), "Failed", "retries exhausted"},
			} {
				status["conditions"] = []any{outcome.condition}
				state, msg = Status(o)
				assert.Equal(t, outcome.state, state)
				assert.Equal(t, outcome.message, msg)
			}

			o.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": "retry-3"})
			state, msg = Status(o)
			assert.Equal(t, "Reconciling", state)
			assert.Contains(t, msg, "Waiting for controller")
		})
	}
}

func TestStaticOCIHelmRepositoryDoesNotWaitForReconciliation(t *testing.T) {
	for _, tt := range []struct {
		name        string
		suspended   bool
		requestedAt string
		conditions  []any
	}{
		{name: "new repository without status"},
		{name: "ignored reconcile annotation", requestedAt: "request-2"},
		{name: "ignored suspension", suspended: true, requestedAt: "request-2"},
		{name: "historical failure", conditions: []any{condition("Ready", "False", 1, "Failed", "old failure")}},
		{name: "historical stall", conditions: []any{condition("Stalled", "True", 1, "Failed", "old stall")}},
		{name: "historical progress", requestedAt: "request-2", conditions: []any{condition("Reconciling", "True", 1, "Progressing", "old progress")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := object("source.toolkit.fluxcd.io/v1", "HelmRepository", "apps", "oci-charts")
			o.SetGeneration(2)
			o.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": tt.requestedAt})
			o.Object["spec"] = map[string]any{"type": "oci", "url": "oci://ghcr.io/example/charts", "suspend": tt.suspended}
			if tt.conditions != nil {
				o.Object["status"] = map[string]any{"conditions": tt.conditions, "lastHandledReconcileAt": "request-1"}
			}
			before := o.DeepCopy()
			state, message := Status(o)
			assert.Equal(t, "Ready", state)
			assert.Contains(t, message, "static source")
			assert.Contains(t, message, "HelmChart")
			assert.False(t, Suspended(o), "suspension is not applicable to static repositories")
			assert.False(t, ReconcilePending(o), "static repositories never acknowledge reconciliation requests")
			assert.Equal(t, before, o, "static-source checks must not mutate informer objects")
		})
	}
}

func TestIsStaticHelmRepositoryRequiresFluxKindAndOCIType(t *testing.T) {
	for _, tt := range []struct {
		name       string
		apiVersion string
		kind       string
		spec       any
		static     bool
	}{
		{"stable OCI HelmRepository", "source.toolkit.fluxcd.io/v1", "HelmRepository", map[string]any{"type": "oci"}, true},
		{"beta OCI HelmRepository", "source.toolkit.fluxcd.io/v1beta2", "HelmRepository", map[string]any{"type": "oci"}, true},
		{"unrelated API group", "example.com/v1", "HelmRepository", map[string]any{"type": "oci"}, false},
		{"missing API group", "v1", "HelmRepository", map[string]any{"type": "oci"}, false},
		{"missing API version", "source.toolkit.fluxcd.io/", "HelmRepository", map[string]any{"type": "oci"}, false},
		{"malformed API version", "source.toolkit.fluxcd.io/v1/invalid", "HelmRepository", map[string]any{"type": "oci"}, false},
		{"OCIRepository has a reconciler", "source.toolkit.fluxcd.io/v1", "OCIRepository", map[string]any{"type": "oci"}, false},
		{"default type has a reconciler", "source.toolkit.fluxcd.io/v1", "HelmRepository", map[string]any{"type": "default"}, false},
		{"URL alone does not select OCI type", "source.toolkit.fluxcd.io/v1", "HelmRepository", map[string]any{"url": "oci://ghcr.io/example/charts"}, false},
		{"type is case sensitive", "source.toolkit.fluxcd.io/v1", "HelmRepository", map[string]any{"type": "OCI"}, false},
		{"malformed type", "source.toolkit.fluxcd.io/v1", "HelmRepository", map[string]any{"type": true}, false},
		{"malformed spec", "source.toolkit.fluxcd.io/v1", "HelmRepository", "invalid", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := object(tt.apiVersion, tt.kind, "apps", "charts")
			o.Object["spec"] = tt.spec
			assert.Equal(t, tt.static, IsStaticHelmRepository(o))
			state, _ := Status(o)
			if tt.static {
				assert.Equal(t, "Ready", state)
			} else {
				assert.Equal(t, "Pending", state, "only static sources are ready without conditions")
			}
		})
	}
	assert.False(t, IsStaticHelmRepository(nil))
	assert.False(t, IsStaticHelmRepository(&unstructured.Unstructured{}))
}

func TestReconcilePendingUsesOpaqueRequestTokens(t *testing.T) {
	for _, tt := range []struct {
		name        string
		annotations any
		status      any
		pending     bool
	}{
		{name: "absent annotations"},
		{name: "malformed annotations", annotations: "invalid"},
		{name: "malformed token", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": int64(2)}},
		{name: "empty token", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": ""}},
		{name: "first request", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": "retry"}, pending: true},
		{name: "malformed status", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": "retry"}, status: "invalid", pending: true},
		{name: "malformed handled token", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": "retry"}, status: map[string]any{"lastHandledReconcileAt": true}, pending: true},
		{name: "handled request", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": "retry"}, status: map[string]any{"lastHandledReconcileAt": "retry"}},
		{name: "tokens have no temporal ordering", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": "a"}, status: map[string]any{"lastHandledReconcileAt": "z"}, pending: true},
		{name: "tokens preserve whitespace", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": " retry "}, status: map[string]any{"lastHandledReconcileAt": "retry"}, pending: true},
		{name: "unrelated malformed annotation", annotations: map[string]any{"reconcile.fluxcd.io/requestedAt": "retry", "invalid": true}, pending: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "apps", "demo")
			o.Object["metadata"].(map[string]any)["annotations"] = tt.annotations
			o.Object["status"] = tt.status
			assert.Equal(t, tt.pending, ReconcilePending(o))
		})
	}
	assert.False(t, ReconcilePending(nil))
	assert.False(t, ReconcilePending(&unstructured.Unstructured{}))
	assert.False(t, ReconcilePending(&unstructured.Unstructured{Object: map[string]any{"metadata": "invalid"}}))
}

func TestStatusPendingRequestRespectsSuspensionAndListErrors(t *testing.T) {
	for _, tt := range []struct {
		name       string
		apiVersion string
		kind       string
		spec       map[string]any
		annotation string
		value      string
		wantState  string
		wantMsg    string
	}{
		{name: "no conditions", apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", wantState: "Reconciling", wantMsg: "Waiting for controller"},
		{name: "spec suspended", apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", spec: map[string]any{"suspend": true}, wantState: "Suspended", wantMsg: "Reconciliation is suspended"},
		{name: "operator suspended", apiVersion: "fluxcd.controlplane.io/v1", kind: "ResourceSet", annotation: "fluxcd.controlplane.io/reconcile", value: " Disabled ", wantState: "Suspended", wantMsg: "Reconciliation is suspended"},
		{name: "list unavailable", apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", annotation: "k9scli.io/flux-list-error", value: "service unavailable", wantState: "Unknown", wantMsg: "service unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := object(tt.apiVersion, tt.kind, "apps", "demo")
			o.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": "retry", tt.annotation: tt.value})
			o.Object["spec"] = tt.spec
			before := o.DeepCopy()
			state, msg := Status(o)
			assert.Equal(t, tt.wantState, state)
			assert.Contains(t, msg, tt.wantMsg)
			assert.Equal(t, before, o, "status reads must not mutate informer objects")
		})
	}
}

func TestStatusHandlesSyntheticListErrorsAndMalformedData(t *testing.T) {
	restricted := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "apps", "<restricted>")
	restricted.SetAnnotations(map[string]string{"k9scli.io/flux-list-error": "helmreleases is forbidden"})
	state, msg := Status(restricted)
	assert.Equal(t, "Restricted", state)
	assert.Equal(t, "helmreleases is forbidden", msg)

	unavailable := object("source.toolkit.fluxcd.io/v1", "GitRepository", "apps", "<unavailable>")
	unavailable.SetAnnotations(map[string]string{"k9scli.io/flux-list-error": "service unavailable"})
	state, msg = Status(unavailable)
	assert.Equal(t, "Unknown", state)
	assert.Equal(t, "service unavailable", msg)

	malformed := object("source.toolkit.fluxcd.io/v1", "GitRepository", "apps", "bad")
	malformed.Object["spec"] = "not-an-object"
	malformed.Object["status"] = map[string]any{"conditions": []any{"not-a-condition", map[string]any{"type": 3}}}
	assert.NotPanics(t, func() {
		assert.False(t, Suspended(malformed))
		state, _ = Status(malformed)
		assert.Equal(t, "Pending", state)
		_, ok := Source(malformed)
		assert.False(t, ok)
		assert.Empty(t, Dependencies(malformed))
		assert.Empty(t, Revision(malformed))
	})
}

func TestSourceResolvesNamespacesAndHelmAPIs(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		ns         string
		fields     map[string]any
		want       Reference
		ok         bool
	}{
		{
			name: "kustomization defaults source namespace", apiVersion: "kustomize.toolkit.fluxcd.io/v1", kind: "Kustomization", ns: "apps",
			fields: map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "manifests"}}},
			want:   Reference{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "apps", Name: "manifests"}, ok: true,
		},
		{
			name: "kustomization preserves source namespace", apiVersion: "kustomize.toolkit.fluxcd.io/v1", kind: "Kustomization", ns: "apps",
			fields: map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "OCIRepository", "name": "bundle", "namespace": "shared"}}},
			want:   Reference{Group: "source.toolkit.fluxcd.io", Kind: "OCIRepository", Namespace: "shared", Name: "bundle"}, ok: true,
		},
		{
			name: "helm release direct OCI chart", apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", ns: "apps",
			fields: map[string]any{"spec": map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "podinfo", "namespace": "artifacts"}}},
			want:   Reference{Group: "source.toolkit.fluxcd.io", Kind: "OCIRepository", Namespace: "artifacts", Name: "podinfo"}, ok: true,
		},
		{
			name: "helm release legacy chart source", apiVersion: "helm.toolkit.fluxcd.io/v2beta2", kind: "HelmRelease", ns: "apps",
			fields: map[string]any{"spec": map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}}}},
			want:   Reference{Group: "source.toolkit.fluxcd.io", Kind: "HelmRepository", Namespace: "apps", Name: "charts"}, ok: true,
		},
		{
			name: "helm release generated chart fallback", apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", ns: "apps",
			fields: map[string]any{"status": map[string]any{"helmChart": "flux-system/apps-demo"}},
			want:   Reference{Group: "source.toolkit.fluxcd.io", Kind: "HelmChart", Namespace: "flux-system", Name: "apps-demo"}, ok: true,
		},
		{
			name: "helm chart defaults namespace", apiVersion: "source.toolkit.fluxcd.io/v1", kind: "HelmChart", ns: "flux-system",
			fields: map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}},
			want:   Reference{Group: "source.toolkit.fluxcd.io", Kind: "HelmRepository", Namespace: "flux-system", Name: "charts"}, ok: true,
		},
		{
			name: "one named operator input is unambiguous", apiVersion: "fluxcd.controlplane.io/v1", kind: "ResourceSet", ns: "apps",
			fields: map[string]any{"spec": map[string]any{"inputsFrom": []any{map[string]any{"name": "pull-requests"}}}},
			want:   Reference{Group: "fluxcd.controlplane.io", Kind: "ResourceSetInputProvider", Namespace: "apps", Name: "pull-requests"}, ok: true,
		},
		{
			name: "selector operator input is ambiguous", apiVersion: "fluxcd.controlplane.io/v1", kind: "ResourceSet", ns: "apps",
			fields: map[string]any{"spec": map[string]any{"inputsFrom": []any{map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "podinfo"}}}}}},
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := object(tt.apiVersion, tt.kind, tt.ns, "demo")
			for k, v := range tt.fields {
				o.Object[k] = v
			}
			got, ok := Source(o)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDependenciesDefaultToOwningTypeAndNamespace(t *testing.T) {
	o := object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "apps", "frontend")
	o.Object["spec"] = map[string]any{"dependsOn": []any{
		map[string]any{"name": "backend"},
		map[string]any{"name": "database", "namespace": "data"},
		map[string]any{"namespace": "ignored"},
	}}

	assert.Equal(t, []Reference{
		{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "apps", Name: "backend"},
		{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "data", Name: "database"},
	}, Dependencies(o))

	operator := object("fluxcd.controlplane.io/v1", "ResourceSet", "apps", "platform")
	operator.Object["spec"] = map[string]any{"inputsFrom": []any{
		map[string]any{"name": "branches"},
		map[string]any{"name": "tags", "kind": "ResourceSetInputProvider", "apiVersion": "fluxcd.controlplane.io/v1"},
		map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"team": "a"}}},
	}}
	assert.Equal(t, []Reference{
		{Group: "fluxcd.controlplane.io", Kind: "ResourceSetInputProvider", Namespace: "apps", Name: "branches"},
		{Group: "fluxcd.controlplane.io", Kind: "ResourceSetInputProvider", Namespace: "apps", Name: "tags"},
	}, Dependencies(operator))
}

func TestRevisionUsesKindSpecificStatusFields(t *testing.T) {
	tests := []struct {
		kind   string
		status map[string]any
		want   string
	}{
		{"GitRepository", map[string]any{"artifact": map[string]any{"revision": "main@sha1:abcd"}}, "main@sha1:abcd"},
		{"Kustomization", map[string]any{"lastAppliedRevision": "main@sha1:beef", "lastAttemptedRevision": "old"}, "main@sha1:beef"},
		{"HelmRelease", map[string]any{"lastAttemptedRevision": "6.7.1"}, "6.7.1"},
		{"ImagePolicy", map[string]any{"latestImage": "ghcr.io/acme/app:1.2.3"}, "ghcr.io/acme/app:1.2.3"},
		{"ImagePolicy", map[string]any{"latestRef": map[string]any{"image": "ghcr.io/acme/app", "tag": "2.0.0", "digest": "sha256:abcd"}}, "ghcr.io/acme/app:2.0.0"},
		{"ImageUpdateAutomation", map[string]any{"observedSourceRevision": "main@sha1:feed", "lastPushCommit": "cafe"}, "main@sha1:feed"},
		{"ImageUpdateAutomation", map[string]any{"lastPushCommit": "sha1:cafe"}, "sha1:cafe"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			o := object("source.toolkit.fluxcd.io/v1", tt.kind, "apps", "demo")
			o.Object["status"] = tt.status
			assert.Equal(t, tt.want, Revision(o))
		})
	}
}

func object(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
	}}
}

func condition(kind, status string, generation int64, reason, message string) map[string]any {
	return map[string]any{
		"type":               kind,
		"status":             status,
		"observedGeneration": generation,
		"reason":             reason,
		"message":            message,
	}
}
