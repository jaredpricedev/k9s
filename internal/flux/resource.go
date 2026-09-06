// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

// Package flux contains bounded, read-only helpers for the Flux resources K9s
// presents in its native and unified Flux views.
package flux

import (
	"strings"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	sourceGroup      = "source.toolkit.fluxcd.io"
	kustomizeGroup   = "kustomize.toolkit.fluxcd.io"
	helmGroup        = "helm.toolkit.fluxcd.io"
	imageGroup       = "image.toolkit.fluxcd.io"
	operatorGroup    = "fluxcd.controlplane.io"
	listErrorKey     = "k9scli.io/flux-list-error"
	stateUnknown     = "Unknown"
	statePending     = "Pending"
	stateReconciling = "Reconciling"
	stateReady       = "Ready"
	conditionTrue    = "True"
)

// Reference identifies another Kubernetes object used by a Flux resource.
type Reference struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

var resourcesByGroupKind = map[string]map[string]string{
	sourceGroup: {
		"GitRepository":  "gitrepositories",
		"OCIRepository":  "ocirepositories",
		"HelmRepository": "helmrepositories",
		"HelmChart":      "helmcharts",
		"Bucket":         "buckets",
	},
	kustomizeGroup: {"Kustomization": "kustomizations"},
	helmGroup:      {"HelmRelease": "helmreleases"},
	imageGroup: {
		"ImageRepository":       "imagerepositories",
		"ImagePolicy":           "imagepolicies",
		"ImageUpdateAutomation": "imageupdateautomations",
	},
	operatorGroup: {
		"ResourceSet":              "resourcesets",
		"ResourceSetInputProvider": "resourcesetinputproviders",
		"FluxInstance":             "fluxinstances",
	},
}

var resourceGroups = func() map[string]struct{} {
	groups := make(map[string]struct{})
	for group, kinds := range resourcesByGroupKind {
		for _, resource := range kinds {
			groups[group+"/"+resource] = struct{}{}
		}
	}
	return groups
}()

// Supported reports whether gvr is one of the deliberately supported Flux
// APIs. Versions are left open so installed stable and beta Flux CRDs work.
func Supported(gvr *client.GVR) bool {
	if gvr == nil || gvr == client.NoGVR || gvr.V() == "" {
		return false
	}
	_, ok := resourceGroups[gvr.G()+"/"+gvr.R()]
	return ok
}

// GVRFor derives the resource identifier from a supported object's exact kind.
func GVRFor(o *unstructured.Unstructured) *client.GVR {
	if o == nil {
		return client.NoGVR
	}
	gv, err := schema.ParseGroupVersion(o.GetAPIVersion())
	if err != nil || gv.Group == "" || gv.Version == "" {
		return client.NoGVR
	}
	resource, ok := resourcesByGroupKind[gv.Group][o.GetKind()]
	if !ok {
		return client.NoGVR
	}
	return client.NewGVR(gv.Group + "/" + gv.Version + "/" + resource)
}

// IsStaticHelmRepository identifies Flux OCI HelmRepositories, which are data
// containers for HelmCharts and do not reconcile or report controller status.
func IsStaticHelmRepository(o *unstructured.Unstructured) bool {
	if o == nil || o.GetKind() != "HelmRepository" {
		return false
	}
	repositoryType, _ := nestedString(o, "spec", "type")
	if repositoryType != "oci" {
		return false
	}
	gv, err := schema.ParseGroupVersion(o.GetAPIVersion())
	return err == nil && gv.Group == sourceGroup && gv.Version != ""
}

// Suspended reports the common Flux spec.suspend flag. A missing or malformed
// field, or a static OCI HelmRepository where suspension does not apply, is false.
func Suspended(o *unstructured.Unstructured) bool {
	if o == nil || IsStaticHelmRepository(o) {
		return false
	}
	suspended, found, err := unstructured.NestedBool(o.Object, "spec", "suspend")
	if err == nil && found && suspended {
		return true
	}
	reconcile, _ := nestedString(o, "metadata", "annotations", operatorGroup+"/reconcile")
	if !strings.EqualFold(strings.TrimSpace(reconcile), "disabled") {
		return false
	}
	gvr := GVRFor(o)
	return gvr != client.NoGVR && gvr.G() == operatorGroup
}

// ReconcilePending reports a non-empty reconcile request the controller has
// not yet acknowledged. Tokens are opaque strings, not ordered timestamps.
// Acknowledgement does not imply completion; Flux conditions report progress.
// Static OCI HelmRepositories do not reconcile and never have pending requests.
func ReconcilePending(o *unstructured.Unstructured) bool {
	if o == nil || IsStaticHelmRepository(o) {
		return false
	}
	requested, _ := nestedString(o, "metadata", "annotations", "reconcile.fluxcd.io/requestedAt")
	if requested == "" {
		return false
	}
	handled, _ := nestedString(o, "status", "lastHandledReconcileAt")
	return requested != handled
}

// Status returns a small, stable state vocabulary and the most relevant Flux
// condition message. Suspension and pending requests take priority over prior
// outcomes; acknowledged requests follow Flux's condition priority: terminal
// stalls, active reconciliation, then readiness. Static OCI HelmRepositories
// are ready for use without controller conditions.
func Status(o *unstructured.Unstructured) (state, message string) {
	if o == nil {
		return stateUnknown, ""
	}
	if IsStaticHelmRepository(o) {
		return stateReady, "OCI HelmRepository is a static source; reconcile its HelmChart or use an OCIRepository"
	}
	if Suspended(o) {
		return "Suspended", "Reconciliation is suspended"
	}
	if listErr, _ := nestedString(o, "metadata", "annotations", listErrorKey); listErr != "" {
		if o.GetName() == "<restricted>" {
			return "Restricted", listErr
		}
		return stateUnknown, listErr
	}

	conditions, _ := nestedSlice(o, "status", "conditions")
	var ready, reconciling, stalled map[string]any
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := condition["type"].(string)
		switch typeName {
		case stateReady:
			ready = condition
		case stateReconciling:
			if conditionStatus(condition) == conditionTrue {
				reconciling = condition
			}
		case "Stalled":
			if conditionStatus(condition) == conditionTrue {
				stalled = condition
			}
		}
	}

	if ReconcilePending(o) {
		// Controllers can publish progress before acknowledging the request
		// in their final patch. Preserve that progress instead of old outcomes.
		if reconciling != nil {
			return stateReconciling, conditionMessage(reconciling)
		}
		return stateReconciling, "Waiting for controller to handle reconciliation request"
	}
	if stalled != nil {
		return "Failed", conditionMessage(stalled)
	}
	if reconciling != nil {
		return stateReconciling, conditionMessage(reconciling)
	}
	if ready == nil {
		return statePending, ""
	}
	message = conditionMessage(ready)
	if staleCondition(o, ready) {
		return statePending, message
	}
	switch conditionStatus(ready) {
	case conditionTrue:
		return stateReady, message
	case "False":
		return "Failed", message
	default:
		return stateUnknown, message
	}
}

func conditionStatus(condition map[string]any) string {
	status, _ := condition["status"].(string)
	return status
}

func conditionMessage(condition map[string]any) string {
	if message, ok := condition["message"].(string); ok && message != "" {
		return message
	}
	reason, _ := condition["reason"].(string)
	return reason
}

func staleCondition(o *unstructured.Unstructured, condition map[string]any) bool {
	if o.GetGeneration() <= 0 {
		return false
	}
	observed, ok := integer(condition["observedGeneration"])
	if !ok {
		observed, ok, _ = unstructured.NestedInt64(o.Object, "status", "observedGeneration")
	}
	return !ok || observed < o.GetGeneration()
}

func integer(value any) (int64, bool) {
	switch value := value.(type) {
	case int64:
		return value, true
	case int32:
		return int64(value), true
	case int:
		return int64(value), true
	case float64:
		return int64(value), value == float64(int64(value))
	default:
		return 0, false
	}
}

// Source returns the single source relationship represented by a resource.
func Source(o *unstructured.Unstructured) (Reference, bool) {
	if o == nil {
		return Reference{}, false
	}
	switch o.GetKind() {
	case "Kustomization", "HelmChart":
		return nestedReference(o, sourceGroup, "", "spec", "sourceRef")
	case "HelmRelease":
		if ref, ok := nestedReference(o, sourceGroup, "", "spec", "chartRef"); ok {
			return ref, true
		}
		if ref, ok := nestedReference(o, sourceGroup, "", "spec", "chart", "spec", "sourceRef"); ok {
			return ref, true
		}
		chart, ok := nestedString(o, "status", "helmChart")
		if !ok || chart == "" {
			return Reference{}, false
		}
		namespace, name := o.GetNamespace(), chart
		if before, after, found := strings.Cut(chart, "/"); found && before != "" && after != "" && !strings.Contains(after, "/") {
			namespace, name = before, after
		}
		return Reference{Group: sourceGroup, Kind: "HelmChart", Namespace: namespace, Name: name}, true
	case "ImagePolicy":
		return nestedReference(o, imageGroup, "ImageRepository", "spec", "imageRepositoryRef")
	case "ImageUpdateAutomation":
		return nestedReference(o, sourceGroup, "GitRepository", "spec", "sourceRef")
	case "ResourceSet":
		inputs, ok := nestedSlice(o, "spec", "inputsFrom")
		if !ok || len(inputs) != 1 {
			return Reference{}, false
		}
		input, ok := inputs[0].(map[string]any)
		if !ok || input["selector"] != nil {
			return Reference{}, false
		}
		return referenceFromMap(input, o.GetNamespace(), operatorGroup, "ResourceSetInputProvider")
	default:
		return Reference{}, false
	}
}

// Dependencies returns explicit Flux dependency relationships, dropping
// malformed or selector-based references that cannot identify one object.
func Dependencies(o *unstructured.Unstructured) []Reference {
	if o == nil {
		return nil
	}
	var (
		values       []any
		ok           bool
		defaultGroup string
		defaultKind  string
	)
	switch o.GetKind() {
	case "Kustomization", "HelmRelease":
		values, ok = nestedSlice(o, "spec", "dependsOn")
		gv, _ := schema.ParseGroupVersion(o.GetAPIVersion())
		defaultGroup, defaultKind = gv.Group, o.GetKind()
	case "ResourceSet":
		values, ok = nestedSlice(o, "spec", "inputsFrom")
		defaultGroup, defaultKind = operatorGroup, "ResourceSetInputProvider"
	default:
		return nil
	}
	if !ok {
		return nil
	}

	refs := make([]Reference, 0, len(values))
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if !ok || entry["selector"] != nil {
			continue
		}
		if ref, ok := referenceFromMap(entry, o.GetNamespace(), defaultGroup, defaultKind); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func nestedReference(o *unstructured.Unstructured, defaultGroup, defaultKind string, fields ...string) (Reference, bool) {
	value, found, err := unstructured.NestedFieldNoCopy(o.Object, fields...)
	if err != nil || !found {
		return Reference{}, false
	}
	entry, ok := value.(map[string]any)
	if !ok {
		return Reference{}, false
	}
	return referenceFromMap(entry, o.GetNamespace(), defaultGroup, defaultKind)
}

func referenceFromMap(entry map[string]any, defaultNamespace, defaultGroup, defaultKind string) (Reference, bool) {
	name, _ := entry["name"].(string)
	if name == "" {
		return Reference{}, false
	}
	kind, _ := entry["kind"].(string)
	if kind == "" {
		kind = defaultKind
	}
	if kind == "" {
		return Reference{}, false
	}
	namespace, _ := entry["namespace"].(string)
	if namespace == "" {
		namespace = defaultNamespace
	}
	group, _ := entry["group"].(string)
	if group == "" {
		if apiVersion, _ := entry["apiVersion"].(string); apiVersion != "" {
			if gv, err := schema.ParseGroupVersion(apiVersion); err == nil {
				group = gv.Group
			}
		}
	}
	if group == "" {
		group = defaultGroup
	}
	return Reference{Group: group, Kind: kind, Namespace: namespace, Name: name}, true
}

func nestedSlice(o *unstructured.Unstructured, fields ...string) ([]any, bool) {
	value, found, err := unstructured.NestedFieldNoCopy(o.Object, fields...)
	if err != nil || !found {
		return nil, false
	}
	values, ok := value.([]any)
	return values, ok
}

func nestedString(o *unstructured.Unstructured, fields ...string) (string, bool) {
	value, found, err := unstructured.NestedString(o.Object, fields...)
	return value, found && err == nil
}

// Revision returns the latest concise revision exposed by Flux status.
func Revision(o *unstructured.Unstructured) string {
	if o == nil {
		return ""
	}
	paths := [][]string{
		{"status", "lastAppliedRevision"},
		{"status", "lastAttemptedRevision"},
		{"status", "artifact", "revision"},
		{"status", "latestImage"},
		{"status", "observedSourceRevision"},
		{"status", "lastPushCommit"},
	}
	for _, path := range paths {
		if revision, ok := nestedString(o, path...); ok && revision != "" {
			return revision
		}
	}
	if value, found, err := unstructured.NestedFieldNoCopy(o.Object, "status", "latestRef"); err == nil && found {
		if latest, ok := value.(map[string]any); ok {
			image, _ := latest["image"].(string)
			tag, _ := latest["tag"].(string)
			digest, _ := latest["digest"].(string)
			switch {
			case image != "" && tag != "":
				return image + ":" + tag
			case image != "" && digest != "":
				return image + "@" + digest
			case image != "":
				return image
			}
		}
	}
	if history, ok := nestedSlice(o, "status", "history"); ok && len(history) > 0 {
		if latest, ok := history[0].(map[string]any); ok {
			if revision, _ := latest["chartVersion"].(string); revision != "" {
				return revision
			}
		}
	}
	return ""
}
