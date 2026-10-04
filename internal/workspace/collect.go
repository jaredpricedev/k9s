// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package workspace

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	PageSize          int64 = 200
	MaxPagesPerQuery        = 4
	MaxResources            = 2000
	MaxQueries              = 192
	CollectionTimeout       = 30 * time.Second
	QueryTimeout            = 10 * time.Second
)

const (
	kindPod                   = "Pod"
	kindDeployment            = "Deployment"
	kindStatefulSet           = "StatefulSet"
	kindDaemonSet             = "DaemonSet"
	kindReplicaSet            = "ReplicaSet"
	kindJob                   = "Job"
	kindCronJob               = "CronJob"
	kindPersistentVolumeClaim = "PersistentVolumeClaim"
	kindKustomization         = "Kustomization"
	kindHelmRelease           = "HelmRelease"
	kindGitRepository         = "GitRepository"
	kindHelmRepository        = "HelmRepository"
	kindOCIRepository         = "OCIRepository"
	kindCertificate           = "Certificate"
	coverageInvalid           = "invalid"
	coverageCanceled          = "canceled"
	podsResourceName          = "pods"
	observedGenerationField   = "observedGeneration"
	conditionsField           = "conditions"
	podRunningPhase           = "Running"
	oomKilledReason           = "OOMKilled"
)

const (
	nativeAPIVersion       = "v1"
	appsAPIGroup           = "apps"
	fluxSourceAPIGroup     = "source.toolkit.fluxcd.io"
	coverageTruncated      = "truncated"
	coverageUnavailable    = "unavailable"
	statusReasonField      = "reason"
	containerNameField     = "name"
	containerStateField    = "state"
	containerExitCodeField = "exitCode"
)

// Snapshot is a bounded, explicit refresh of one workspace. Coverage describes
// every attempted query, including partial results; an empty queue is not proof
// of health when any coverage is incomplete.
type Snapshot struct {
	ObservedAt time.Time
	Resources  []Resource
	Findings   []Finding
	Coverage   []Coverage
}

type Resource struct {
	Ref     ResourceRef
	Kind    string
	Summary string
	Object  *unstructured.Unstructured
}

type Finding struct {
	Ref        ResourceRef
	Kind       string
	Category   string
	Severity   string
	Reason     string
	Detail     string
	ObservedAt time.Time
}

type Coverage struct {
	GVR       string
	Namespace string
	State     string
	Detail    string
	Truncated bool
}

type knownKind struct {
	gvr     schema.GroupVersionResource
	kind    string
	aliases []string
}

var apiVersionPattern = regexp.MustCompile(`^v\d+(?:(?:alpha|beta)\d+)?$`)

var knownKinds = []knownKind{
	{schema.GroupVersionResource{Version: nativeAPIVersion, Resource: podsResourceName}, kindPod, []string{"pod", "po"}},
	{schema.GroupVersionResource{Group: appsAPIGroup, Version: nativeAPIVersion, Resource: "deployments"}, kindDeployment, []string{"deployment", "deploy"}},
	{schema.GroupVersionResource{Group: appsAPIGroup, Version: nativeAPIVersion, Resource: "statefulsets"}, kindStatefulSet, []string{"statefulset", "sts"}},
	{schema.GroupVersionResource{Group: appsAPIGroup, Version: nativeAPIVersion, Resource: "daemonsets"}, kindDaemonSet, []string{"daemonset", "ds"}},
	{schema.GroupVersionResource{Group: appsAPIGroup, Version: nativeAPIVersion, Resource: "replicasets"}, kindReplicaSet, []string{"replicaset", "rs"}},
	{schema.GroupVersionResource{Group: "batch", Version: nativeAPIVersion, Resource: "jobs"}, kindJob, []string{"job"}},
	{schema.GroupVersionResource{Group: "batch", Version: nativeAPIVersion, Resource: "cronjobs"}, kindCronJob, []string{"cronjob", "cj"}},
	{schema.GroupVersionResource{Version: nativeAPIVersion, Resource: "resourcequotas"}, "ResourceQuota", []string{"resourcequota", "quota"}},
	{schema.GroupVersionResource{Version: nativeAPIVersion, Resource: "services"}, "Service", []string{"service", "svc"}},
	{schema.GroupVersionResource{Version: nativeAPIVersion, Resource: "persistentvolumeclaims"}, kindPersistentVolumeClaim, []string{"persistentvolumeclaim", "pvc"}},
	{schema.GroupVersionResource{Version: nativeAPIVersion, Resource: "configmaps"}, "ConfigMap", []string{"configmap", "cm"}},
	{schema.GroupVersionResource{Group: "networking.k8s.io", Version: nativeAPIVersion, Resource: "ingresses"}, "Ingress", []string{"ingress", "ing"}},
	{
		schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
		"HorizontalPodAutoscaler", []string{"horizontalpodautoscaler", "hpa"},
	},
	{
		schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: nativeAPIVersion, Resource: "kustomizations"},
		kindKustomization, []string{"kustomization", "ks"},
	},
	{schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, kindHelmRelease, []string{"helmrelease", "hr"}},
	{schema.GroupVersionResource{Group: fluxSourceAPIGroup, Version: nativeAPIVersion, Resource: "gitrepositories"}, kindGitRepository, []string{"gitrepository"}},
	{schema.GroupVersionResource{Group: fluxSourceAPIGroup, Version: nativeAPIVersion, Resource: "helmrepositories"}, kindHelmRepository, []string{"helmrepository"}},
	{schema.GroupVersionResource{Group: fluxSourceAPIGroup, Version: nativeAPIVersion, Resource: "ocirepositories"}, kindOCIRepository, []string{"ocirepository"}},
	{schema.GroupVersionResource{Group: "cert-manager.io", Version: nativeAPIVersion, Resource: "certificates"}, kindCertificate, []string{"certificate", "cert"}},
}

// DefaultKinds excludes optional controllers: a refresh never discovers or
// scans integrations the workspace did not explicitly select.
func DefaultKinds() []string {
	return []string{podsResourceName, "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs", "resourcequotas"}
}

// ResolveKind accepts curated aliases and explicit group/version/resource (or
// version/resource for core resources). Unknown GVRs still use namespace-only
// endpoints; known cluster resources and Secrets are not workspace inventory.
func ResolveKind(name string) (schema.GroupVersionResource, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	var gvr schema.GroupVersionResource
	parts := strings.Split(name, "/")
	if len(parts) > 1 {
		switch len(parts) {
		case 2:
			gvr = schema.GroupVersionResource{Version: parts[0], Resource: parts[1]}
		case 3:
			gvr = schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}
		default:
			return gvr, "", fmt.Errorf("invalid kind %q: use group/version/resource", name)
		}
		if !apiVersionPattern.MatchString(gvr.Version) || gvr.Resource == "" || len(parts) == 3 && gvr.Group == "" {
			return schema.GroupVersionResource{}, "", fmt.Errorf("invalid kind %q", name)
		}
		for _, part := range parts {
			if len(validation.IsDNS1123Subdomain(part)) != 0 {
				return schema.GroupVersionResource{}, "", fmt.Errorf("invalid kind %q", name)
			}
		}
		if restrictedResource(gvr.Resource) {
			return schema.GroupVersionResource{}, "", fmt.Errorf("%s is not a namespaced workspace inventory kind", gvr.Resource)
		}
		for _, item := range knownKinds {
			if item.gvr.Group == gvr.Group && item.gvr.Resource == gvr.Resource {
				return gvr, item.kind, nil
			}
		}
		return gvr, gvr.Resource, nil
	}
	for _, item := range knownKinds {
		if name == item.gvr.Resource {
			return item.gvr, item.kind, nil
		}
		for _, alias := range item.aliases {
			if name == alias {
				return item.gvr, item.kind, nil
			}
		}
	}
	return gvr, "", fmt.Errorf("unknown kind %q: use a full group/version/resource", name)
}

func restrictedResource(resource string) bool {
	switch resource {
	case "secrets", "secret", "nodes", "namespaces", "persistentvolumes", "customresourcedefinitions",
		"clusterroles", "clusterrolebindings", "storageclasses", "certificatesigningrequests",
		"mutatingwebhookconfigurations", "validatingwebhookconfigurations":
		return true
	}
	return false
}

func gvrString(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Version + "/" + gvr.Resource
	}
	return gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
}

// Collect performs LIST requests only. Every request includes the workspace's
// namespace and exact label selector, with pagination, time, and resource caps.
//
//nolint:gocritic // Preserve the value API: each refresh captures its workspace identity.
func Collect(ctx context.Context, reader dynamic.Interface, scope Scope, now time.Time) Snapshot {
	snapshot := Snapshot{ObservedAt: now}
	if _, err := labels.Parse(scope.LabelSelector); err != nil {
		snapshot.Coverage = append(snapshot.Coverage, Coverage{State: coverageInvalid, Detail: "Invalid label selector: " + err.Error()})
		return snapshot
	}
	if len(scope.Namespaces) == 0 {
		snapshot.Coverage = append(snapshot.Coverage, Coverage{State: coverageInvalid, Detail: "Choose explicit namespaces before refreshing"})
		return snapshot
	}
	ctx, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	kinds := scope.Kinds
	if len(kinds) == 0 {
		kinds = DefaultKinds()
	}
	seenQueries := make(map[string]bool)
	for _, name := range kinds {
		gvr, kind, err := ResolveKind(name)
		if err != nil {
			snapshot.Coverage = append(snapshot.Coverage, Coverage{GVR: name, State: coverageInvalid, Detail: err.Error()})
			continue
		}
		for _, namespace := range scope.Namespaces {
			coverage := Coverage{GVR: gvrString(gvr), Namespace: namespace}
			if namespace == "" || namespace == "*" || strings.EqualFold(namespace, "all") || len(validation.IsDNS1123Label(namespace)) != 0 {
				coverage.State, coverage.Detail = coverageInvalid, "Choose a specific namespace; all-namespace queries are disabled"
				snapshot.Coverage = append(snapshot.Coverage, coverage)
				continue
			}
			key := coverage.GVR + "/" + namespace
			if seenQueries[key] {
				continue
			}
			seenQueries[key] = true
			if len(seenQueries) > MaxQueries || len(snapshot.Resources) >= MaxResources {
				coverage.State, coverage.Detail, coverage.Truncated = coverageTruncated, "Workspace collection limit reached; narrow the scope", true
				snapshot.Coverage = append(snapshot.Coverage, coverage)
				continue
			}
			if ctx.Err() != nil {
				coverage.State, coverage.Detail = coverageCanceled, ctx.Err().Error()
				snapshot.Coverage = append(snapshot.Coverage, coverage)
				continue
			}
			if reader == nil {
				coverage.State, coverage.Detail = coverageUnavailable, "Kubernetes reader is unavailable"
				snapshot.Coverage = append(snapshot.Coverage, coverage)
				continue
			}
			collectQuery(ctx, reader, gvr, kind, namespace, scope.LabelSelector, now, &snapshot, &coverage)
			snapshot.Coverage = append(snapshot.Coverage, coverage)
		}
	}
	sort.Slice(snapshot.Resources, func(i, j int) bool { return refKey(snapshot.Resources[i].Ref) < refKey(snapshot.Resources[j].Ref) })
	sortFindings(snapshot.Findings)
	return snapshot
}

func collectQuery(
	ctx context.Context, reader dynamic.Interface, gvr schema.GroupVersionResource, kind, namespace, selector string,
	now time.Time, snapshot *Snapshot, coverage *Coverage,
) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	continuation := ""
	invalidObjects := 0
	for range MaxPagesPerQuery {
		remaining := MaxResources - len(snapshot.Resources)
		limit := min(PageSize, int64(remaining))
		objects, err := reader.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: limit, Continue: continuation})
		if err != nil {
			coverage.Detail = boundedText(err.Error(), 300)
			switch {
			case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
				coverage.State = "denied"
			case apierrors.IsNotFound(err):
				coverage.State = "absent"
			case ctx.Err() != nil:
				coverage.State = coverageCanceled
			default:
				coverage.State = coverageUnavailable
			}
			return
		}
		if ctx.Err() != nil {
			coverage.State, coverage.Detail = coverageCanceled, ctx.Err().Error()
			return
		}
		if objects == nil {
			coverage.State, coverage.Detail = coverageUnavailable, "Kubernetes returned an empty response"
			return
		}
		for i := range objects.Items {
			if len(snapshot.Resources) >= MaxResources {
				coverage.State, coverage.Detail, coverage.Truncated = coverageTruncated, "Workspace resource limit reached; narrow the scope", true
				return
			}
			object := &objects.Items[i]
			// Ignore malformed/cross-namespace responses rather than assigning a
			// potentially wrong namespace or pin identity to them.
			if object.GetNamespace() != namespace || object.GetName() == "" || object.GetUID() == "" {
				invalidObjects++
				continue
			}
			ref := ResourceRef{GVR: gvrString(gvr), Namespace: namespace, Name: object.GetName(), UID: string(object.GetUID())}
			findings := Classify(ref, kind, object, now)
			snapshot.Resources = append(snapshot.Resources, Resource{Ref: ref, Kind: kind, Summary: summarize(kind, object, findings), Object: object.DeepCopy()})
			snapshot.Findings = append(snapshot.Findings, findings...)
		}
		continuation = objects.GetContinue()
		if continuation == "" {
			coverage.State, coverage.Detail = "complete", "Current resource state observed"
			if invalidObjects > 0 {
				coverage.State, coverage.Detail = coverageUnavailable, fmt.Sprintf("Skipped %d malformed or unidentified resources; inventory is incomplete", invalidObjects)
			}
			return
		}
		if len(snapshot.Resources) >= MaxResources {
			break
		}
	}
	coverage.State, coverage.Detail, coverage.Truncated = coverageTruncated, "Pagination limit reached; narrow the scope", true
}

func refKey(ref ResourceRef) string { return ref.Namespace + "/" + ref.GVR + "/" + ref.Name }

func boundedText(value string, maxLen int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxLen {
		return string(runes[:maxLen-1]) + "…"
	}
	return value
}
