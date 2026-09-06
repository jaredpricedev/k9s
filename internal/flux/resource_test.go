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
