// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/flux"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type reference struct {
	group, version, kind, namespace, name, uid string
	relation, certainty, reason                string
	omitted                                    bool
}

const maxObjectReferences = 8

func references(object *unstructured.Unstructured, request *Request) []reference {
	var refs []reference
	owners := object.GetOwnerReferences()
	controlling := 0
	for _, owner := range owners {
		if owner.Controller != nil && *owner.Controller {
			controlling++
		}
	}
	retained := 0
	for _, owner := range owners {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		if retained >= maxObjectReferences {
			continue
		}
		retained++
		version, err := schema.ParseGroupVersion(owner.APIVersion)
		ref := reference{group: version.Group, version: version.Version, kind: owner.Kind, namespace: object.GetNamespace(), name: owner.Name,
			uid: string(owner.UID), relation: "controller reference", certainty: "UID verification pending"}
		if err != nil || ref.uid == "" || controlling > 1 {
			ref.reason = "Controller reference malformed or ambiguous; no single owner inferred"
		}
		refs = append(refs, ref)
	}
	if controlling > maxObjectReferences {
		refs = append(refs, omittedReferences("controller reference coverage", controlling-maxObjectReferences,
			"Controlling references beyond the eight-reference limit were not retained"))
	}
	refs = append(refs, fluxTracking(object)...)
	annotations := object.GetAnnotations()
	if annotations["meta.helm.sh/release-name"] != "" || annotations["meta.helm.sh/release-namespace"] != "" {
		refs = append(refs, reference{namespace: annotations["meta.helm.sh/release-namespace"], name: annotations["meta.helm.sh/release-name"],
			relation: "Helm release metadata", certainty: "unverified metadata",
			reason: "Release metadata lacks a Kubernetes controller UID; review through Helm; no Secret release storage read"})
	}
	if ref, exists := argoTracking(object, request.ArgoNamespace); exists {
		refs = append(refs, ref)
	}
	if flux.Supported(flux.GVRFor(object)) {
		if source, exists := flux.Source(object); exists {
			refs = append(refs, fluxReference(source, "declared source reference"))
		}
		dependencies := flux.Dependencies(object)
		for _, dependency := range dependencies[:min(len(dependencies), maxObjectReferences)] {
			refs = append(refs, fluxReference(dependency, "declared dependency reference"))
		}
		if len(dependencies) > maxObjectReferences {
			refs = append(refs, omittedReferences("dependency reference coverage", len(dependencies)-maxObjectReferences,
				"Dependencies beyond the eight-reference limit were not observed; their states remain unknown"))
		}
		refs = append(refs, unresolvedDependencyCoverage(object, len(dependencies))...)
	}
	return refs
}

func omittedReferences(relation string, count int, reason string) reference {
	return reference{relation: relation, omitted: true, certainty: "unobserved references", reason: fmt.Sprintf("%d omitted: %s", count, reason)}
}

func unresolvedDependencyCoverage(object *unstructured.Unstructured, resolved int) []reference {
	field := "dependsOn"
	switch object.GetKind() {
	case kindKustomization, kindHelmRelease:
	case "ResourceSet":
		field = "inputsFrom"
	default:
		return nil
	}
	values, found, err := unstructured.NestedSlice(object.Object, fieldSpec, field)
	if err != nil {
		return []reference{omittedReferences("dependency reference coverage", 1, "Malformed declared dependency collection; no complete dependency verdict")}
	}
	if found && len(values) > resolved {
		return []reference{omittedReferences("dependency reference coverage", len(values)-resolved,
			"Malformed or selector-based entries cannot identify bounded named targets; dependency evidence incomplete")}
	}
	return nil
}

func fluxTracking(object *unstructured.Unstructured) []reference {
	labels := object.GetLabels()
	var refs []reference
	for _, marker := range []struct{ group, kind string }{
		{"kustomize.toolkit.fluxcd.io", kindKustomization}, {"helm.toolkit.fluxcd.io", kindHelmRelease},
	} {
		name, namespace := labels[marker.group+"/name"], labels[marker.group+"/namespace"]
		if name == "" && namespace == "" {
			continue
		}
		ref := reference{group: marker.group, kind: marker.kind, namespace: namespace, name: name,
			relation: "Flux tracking marker", certainty: "unverified metadata"}
		if !validName(name) || !validNamespace(namespace) {
			ref.reason = "Flux marker pair missing or malformed; no namespace/name guessed"
		}
		refs = append(refs, ref)
	}
	return refs
}

func argoTracking(object *unstructured.Unstructured, namespace string) (reference, bool) {
	labels, annotations := object.GetLabels(), object.GetAnnotations()
	name, source := labels["argocd.argoproj.io/instance"], "Argo tracking label"
	if id := annotations["argocd.argoproj.io/tracking-id"]; id != "" {
		parts := strings.Split(id, ":")
		version, err := schema.ParseGroupVersion(object.GetAPIVersion())
		if len(parts) != 3 || err != nil || parts[1] != version.Group+"/"+object.GetKind() || parts[2] != object.GetNamespace()+"/"+object.GetName() {
			return reference{relation: "Argo tracking ID", certainty: "unverified metadata", reason: "Tracking ID is malformed or describes another resource; ignored"}, true
		}
		name, source = parts[0], "Argo self-resource tracking ID"
	}
	if name == "" && namespace != "" {
		name, source = labels["app.kubernetes.io/instance"], "General instance label + explicit Argo namespace (ambiguous)"
	}
	if name == "" {
		return reference{}, false
	}
	ref := reference{group: argoGroup, version: "v1alpha1", kind: argoApplication, name: name, namespace: namespace,
		relation: source, certainty: "unverified metadata"}
	if !validName(name) {
		ref.reason = "Argo application marker is malformed"
	} else if namespace == "" {
		ref.reason = "Application namespace unknown; provide :gitops CONTROLLER_NAMESPACE; no cluster-wide search or argocd default"
	} else if !validNamespace(namespace) {
		ref.reason = "Explicit Application namespace is malformed"
	}
	return ref, true
}

func fluxReference(source flux.Reference, relation string) reference {
	return reference{group: source.Group, kind: source.Kind, namespace: source.Namespace, name: source.Name, relation: relation,
		certainty: "declared reference; current named target observation"}
}

func validName(name string) bool { return name != "" && len(validation.IsDNS1123Subdomain(name)) == 0 }
func validNamespace(namespace string) bool {
	return namespace != "" && len(validation.IsDNS1123Label(namespace)) == 0
}
func referenceLabel(ref *reference) string {
	if ref.omitted {
		return "Per-object reference coverage limit or unresolved declarations"
	}
	return safe(fmt.Sprintf("%s/%s %s/%s", ref.group, ref.kind, ref.namespace, ref.name))
}
