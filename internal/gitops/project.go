// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/flux"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	fieldSpec        = "spec"
	fieldStatus      = "status"
	fieldName        = "name"
	fieldKind        = "kind"
	argoGroup        = "argoproj.io"
	argoApplication  = "Application"
	maxText          = 512
	maxRepositoryURL = 8192
)

func projectNode(object *unstructured.Unstructured, identity *inspect.ResourceIdentity, now time.Time) Node {
	node := Node{Identity: *identity, Kind: object.GetKind(), ResourceVersion: object.GetResourceVersion(), ObservedAt: now,
		Generation: number(object.Object, "metadata", "generation"), ObservedGeneration: number(object.Object, fieldStatus, "observedGeneration"), State: Unknown}
	node.Markers = metadataMarkers(object)
	node.Conditions = projectConditions(object)
	if node.Kind == argoApplication && strings.HasPrefix(object.GetAPIVersion(), argoGroup+"/") {
		node.Provider = ProviderArgo
		projectArgo(object, &node)
	} else if flux.Supported(flux.GVRFor(object)) {
		node.Provider = ProviderFlux
		projectFluxState(object, &node)
		projectFluxVersions(object, &node)
	} else {
		node.Reason = "Native owner/reference evidence; no controller health inference"
	}
	return node
}

func projectArgo(object *unstructured.Unstructured, node *Node) {
	node.ReportedSync = text(object.Object, fieldStatus, "sync", fieldStatus)
	node.ReportedHealth = text(object.Object, fieldStatus, "health", fieldStatus)
	node.OperationPhase = text(object.Object, fieldStatus, "operationState", "phase")
	node.ReconciledAt = timestamp(object.Object, fieldStatus, "reconciledAt")
	node.Automated = argoAutomation(object)
	node.State, node.Reason = "reported progress", "Argo controller status; workload readiness and source freshness unverified"
	switch {
	case argoBlockingCondition(node) || node.OperationPhase == "Failed" || node.OperationPhase == "Error" || node.ReportedHealth == "Degraded":
		node.State = "reported failure"
	case node.OperationPhase == "Running" || node.OperationPhase == "Terminating":
		node.State = "reported operation active"
	case node.ReportedHealth == "Suspended":
		node.State = "reported suspended"
	case node.ReportedSync == "OutOfSync":
		node.State = "reported outdated"
	case node.ReportedSync == "Synced" && node.ReportedHealth == "Healthy":
		node.State = "reported ready"
	case node.ReportedSync == "" && node.ReportedHealth == "":
		node.State = Unknown
	}
	node.Gaps = append(node.Gaps, "Argo reported sync/health does not prove current spec convergence or application readiness",
		"Application inventory lacks resource UIDs; inventory alone does not establish ownership")
	source, found, _ := unstructured.NestedMap(object.Object, fieldSpec, "source")
	if found {
		projectArgoSource(source, "spec.source", node)
	}
	sources, _, _ := unstructured.NestedSlice(object.Object, fieldSpec, "sources")
	for index, raw := range sources[:min(len(sources), MaxSources)] {
		if source, ok := raw.(map[string]any); ok {
			projectArgoSource(source, fmt.Sprintf("spec.sources[%d] (declared)", index), node)
		}
	}
	addVersion(node, "reconciled source revision", text(object.Object, fieldStatus, "sync", "revision"), "status.sync.revision")
	revisions, _, _ := unstructured.NestedStringSlice(object.Object, fieldStatus, "sync", "revisions")
	for index, revision := range revisions[:min(len(revisions), MaxSources)] {
		addVersion(node, "reconciled source revision", safe(revision), fmt.Sprintf("status.sync.revisions[%d] (reported order)", index))
	}
	node.Gaps = append(node.Gaps, "Chart target version, reconciled Git revision and application version are distinct; application version unavailable")
	images, _, _ := unstructured.NestedStringSlice(object.Object, fieldStatus, "summary", "images")
	for _, image := range images[:min(len(images), MaxSources)] {
		addVersion(node, "Argo reported image reference", safe(image), "status.summary.images; running digest unverified")
	}
	projectArgoResources(object, node)
}

func projectArgoSource(source map[string]any, provenance string, node *Node) {
	addVersion(node, "repository (credentials/query omitted)", repository(source, "repoURL"), provenance+".repoURL")
	kind := "declared source target (branch/tag/commit)"
	if text(source, "chart") != "" {
		kind = "declared chart version or constraint"
		addVersion(node, "chart", text(source, "chart"), provenance+".chart")
	}
	addVersion(node, kind, text(source, "targetRevision"), provenance+".targetRevision")
	addVersion(node, "declared path", text(source, "path"), provenance+".path")
}

func projectArgoResources(object *unstructured.Unstructured, node *Node) {
	rows, _, _ := unstructured.NestedSlice(object.Object, fieldStatus, "resources")
	for _, raw := range rows[:min(len(rows), MaxResources)] {
		if row, ok := raw.(map[string]any); ok {
			node.Resources = append(node.Resources, Resource{Group: text(row, "group"), Kind: text(row, fieldKind), Namespace: text(row, "namespace"),
				Name: text(row, fieldName), Sync: text(row, fieldStatus), Health: text(row, "health", fieldStatus)})
		}
	}
	if len(rows) > MaxResources {
		node.Gaps = append(node.Gaps, "Application inventory capped at 100 reported entries")
	}
}

func projectFluxVersions(object *unstructured.Unstructured, node *Node) {
	revisionKind := "controller source revision"
	if object.GetKind() == kindHelmRelease {
		revisionKind = "controller chart revision"
	}
	for _, revision := range []string{"lastAppliedRevision", "lastAttemptedRevision"} {
		addVersion(node, revisionKind, text(object.Object, fieldStatus, revision), "status."+revision)
	}
	addVersion(node, "artifact revision", text(object.Object, fieldStatus, "artifact", "revision"), "status.artifact.revision")
	addVersion(node, "artifact digest", text(object.Object, fieldStatus, "artifact", "digest"), "status.artifact.digest")
	addVersion(node, "declared chart version or constraint", text(object.Object, fieldSpec, "chart", fieldSpec, "version"), "spec.chart.spec.version")
	addVersion(node, "repository (credentials/query omitted)", repository(object.Object, fieldSpec, "url"), "spec.url")
	addVersion(node, "declared source ref", text(object.Object, fieldSpec, "ref", "branch"), "spec.ref.branch")
	history, _, _ := unstructured.NestedSlice(object.Object, fieldStatus, "history")
	for index, raw := range history[:min(len(history), MaxSources)] {
		if release, ok := raw.(map[string]any); ok {
			provenance := fmt.Sprintf("status.history[%d] (reported release %s)", index, text(release, fieldStatus))
			addVersion(node, "observed Helm chart version", text(release, "chartVersion"), provenance+".chartVersion")
			addVersion(node, "observed Helm application version", text(release, "appVersion"), provenance+".appVersion")
		}
	}
	node.Gaps = append(node.Gaps, "Chart, application and source versions remain separate; running image digests require Pod evidence")
}

func projectConditions(object *unstructured.Unstructured) []Condition {
	rows, _, _ := unstructured.NestedSlice(object.Object, fieldStatus, "conditions")
	var result []Condition
	for _, raw := range rows[:min(len(rows), MaxConditions)] {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		condition := Condition{Type: text(row, "type"), Status: text(row, fieldStatus), Reason: text(row, "reason"),
			TransitionTime: timestamp(row, "lastTransitionTime"), ObservedGeneration: number(row, "observedGeneration")}
		if text(row, "message") != "" {
			condition.Message = "Free-form controller diagnostics omitted from retained evidence"
		}
		result = append(result, condition)
	}
	return result
}

func addVersion(node *Node, kind, value, source string) {
	if value != "" && len(node.Versions) < MaxSources*4 {
		node.Versions = append(node.Versions, Version{Kind: kind, Value: value, Source: source})
	}
}

func text(object map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(object, fields...)
	return safe(value)
}
func safe(value string) string { return logstream.SafeText(value[:min(len(value), maxText)]) }
func number(object map[string]any, fields ...string) *int64 {
	value, found, err := unstructured.NestedInt64(object, fields...)
	if !found || err != nil {
		return nil
	}
	return &value
}
func boolean(object map[string]any, fields ...string) *bool {
	value, found, err := unstructured.NestedBool(object, fields...)
	if !found || err != nil {
		return nil
	}
	return &value
}
func timestamp(object map[string]any, fields ...string) string {
	value, err := time.Parse(time.RFC3339Nano, text(object, fields...))
	if err != nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func repository(object map[string]any, fields ...string) string {
	// Parse before display truncation: dropping an eventual @ boundary can turn
	// a long user-only credential into a seemingly public hostname.
	value, _, _ := unstructured.NestedString(object, fields...)
	return safeRepository(value)
}
func safeRepository(value string) string {
	if value == "" {
		return ""
	}
	if len(value) > maxRepositoryURL {
		return "Repository identity unavailable; oversized URL excluded"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "Repository identity unavailable; URL format excluded"
	}
	parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
	return safe(parsed.String())
}

// Argo documents automated.enabled=nil as true only when the policy exists.
// Missing policy is manual; malformed evidence remains unknown.
func argoAutomation(object *unstructured.Unstructured) *bool {
	policy, found, err := unstructured.NestedMap(object.Object, fieldSpec, "syncPolicy", "automated")
	if err != nil {
		return nil
	}
	if !found {
		manual := false
		return &manual
	}
	if enabled := boolean(policy, "enabled"); enabled != nil {
		return enabled
	}
	if value, exists := policy["enabled"]; exists && value != nil {
		return nil
	}
	enabled := true
	return &enabled
}

func argoBlockingCondition(node *Node) bool {
	for _, condition := range node.Conditions {
		switch condition.Type {
		case "ComparisonError", "SyncError", "InvalidSpecError", "UnknownError":
			return true
		}
	}
	return false
}

func metadataMarkers(object *unstructured.Unstructured) []string {
	labels, annotations := object.GetLabels(), object.GetAnnotations()
	var markers []string
	for _, marker := range []struct{ name, key string }{
		{"managed-by metadata", "app.kubernetes.io/managed-by"},
		{"Argo tracking metadata", "argocd.argoproj.io/instance"},
		{"Argo tracking metadata", "argocd.argoproj.io/tracking-id"},
		{"Flux Kustomization metadata", "kustomize.toolkit.fluxcd.io/name"},
		{"Flux Kustomization namespace metadata", "kustomize.toolkit.fluxcd.io/namespace"},
		{"Flux HelmRelease metadata", "helm.toolkit.fluxcd.io/name"},
		{"Flux HelmRelease namespace metadata", "helm.toolkit.fluxcd.io/namespace"},
		{"Helm release metadata", "meta.helm.sh/release-name"},
		{"Helm release namespace metadata", "meta.helm.sh/release-namespace"},
	} {
		if labels[marker.key] != "" || annotations[marker.key] != "" {
			markers = append(markers, marker.name+" (unverified)")
		}
	}
	if len(markers) == 0 && labels["app.kubernetes.io/instance"] != "" {
		markers = append(markers, "General application instance label; management/ownership ambiguous")
	}
	return markers
}

func projectFluxState(object *unstructured.Unstructured, node *Node) {
	state, _ := flux.Status(object)
	node.State, node.Reason = "reported "+strings.ToLower(state), "Flux controller-reported state; workload readiness is separate"
	if flux.IsStaticHelmRepository(object) {
		node.State, node.Reason = "static source", "OCI HelmRepository has no reconciliation status; inspect its HelmChart or OCIRepository"
		return
	}
	suspended := flux.Suspended(object)
	node.Suspended = &suspended
	if suspended {
		node.State = "suspended"
		return
	}
	if flux.ReconcilePending(object) {
		node.State, node.Reason = "waiting for request handling", "Controller has not acknowledged the current opaque reconcile request"
		return
	}
	observed := node.ObservedGeneration
	if observed == nil {
		for index := range node.Conditions {
			condition := &node.Conditions[index]
			if condition.Type == conditionReady || condition.Status == "True" && (condition.Type == "Stalled" || condition.Type == "Reconciling") {
				observed = condition.ObservedGeneration
				if condition.Type != "Ready" {
					break
				}
			}
		}
	}
	if node.Generation == nil || observed == nil {
		node.Gaps = append(node.Gaps, "Controller status generation unavailable; reported state freshness unknown")
		return
	}
	if *node.Generation != *observed {
		node.State, node.Reason = "outdated evidence", "Reported status observedGeneration does not match the captured specification generation"
		return
	}
	for _, condition := range node.Conditions {
		if condition.Type == conditionReady && condition.Status == "False" && condition.Reason == "DependencyNotReady" {
			node.State, node.Reason = "waiting for dependency", "Controller reports DependencyNotReady; dependency observations remain separate"
		}
	}
}
