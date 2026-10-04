// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	rolloutListPageSize = 200
	rolloutMaxPages     = 4
	rolloutMaxRevisions = 32
	rolloutMaxPods      = 64
	rolloutRSKind       = "ReplicaSet"
	rolloutDeployKind   = "Deployment"
	rolloutPodAPI       = "v1"
)

//nolint:gocritic // The loader preserves the captured target across asynchronous reads.
func loadRolloutReview(ctx context.Context, connection client.Connection, target SelectedResourceTarget) (*review.RolloutSnapshot, error) {
	if err := rolloutTargetError(target); err != nil {
		return nil, err
	}
	dyn, err := connection.DynDial()
	if err != nil {
		return nil, err
	}
	if dyn == nil {
		return nil, fmt.Errorf("Rollout client unavailable")
	}
	deployment, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := verifySelectedIdentity(target, deployment); err != nil {
		return nil, err
	}
	if deployment.GetKind() != rolloutTargetKind(target) || deployment.GetAPIVersion() != "apps/v1" {
		return nil, fmt.Errorf("The selected source is not the captured native apps/v1 controller")
	}
	at := time.Now().UTC()
	coverage := []review.RolloutCoverage{{
		Source: deployment.GetKind(), State: inspect.ObservationComplete,
		Detail: "Selected controller GET; counts and conditions are API observations",
	}}
	selector, reason := rolloutSelector(deployment)
	if deployment.GetUID() == "" {
		reason = "Workload UID unavailable; no name-only ownership lookup"
	}
	if reason != "" {
		coverage = append(coverage, review.RolloutCoverage{Source: rolloutRevisionSource(deployment.GetKind()), State: inspect.ObservationUnknown, Detail: reason},
			review.RolloutCoverage{Source: "Pods", State: inspect.ObservationUnknown, Detail: "Controller selector or identity unavailable; no unscoped list"})
		return review.NewRolloutSnapshot(deployment, nil, nil, coverage, target.Context, at), nil
	}
	if deployment.GetKind() != rolloutDeployKind {
		return loadControllerChildren(ctx, dyn, target, deployment, selector, coverage, at), ctx.Err()
	}
	revisions, rsCoverage := collectRolloutObjects(ctx, dyn, client.RsGVR.GVR(), target.Namespace, selector, "ReplicaSets", rolloutMaxRevisions,
		func(object *unstructured.Unstructured) bool {
			return rolloutOwnedBy(object, rolloutDeployKind, deployment.GetUID())
		})
	coverage = append(coverage, rsCoverage)
	owners := make(map[types.UID]bool, len(revisions))
	for _, revision := range revisions {
		owners[revision.GetUID()] = true
	}
	pods, podCoverage := collectRolloutObjects(ctx, dyn, client.PodGVR.GVR(), target.Namespace, selector, "Pods", rolloutMaxPods,
		func(object *unstructured.Unstructured) bool {
			for owner := range owners {
				if rolloutOwnedBy(object, rolloutRSKind, owner) {
					return true
				}
			}
			return false
		})
	if rsCoverage.State != inspect.ObservationComplete && podCoverage.State == inspect.ObservationComplete {
		podCoverage.State = inspect.ObservationIncomplete
		podCoverage.Detail += "; ReplicaSet visibility incomplete, so additional owned Pods may be excluded"
	}
	coverage = append(coverage, podCoverage)
	snapshot := review.NewRolloutSnapshot(deployment, revisions, pods, coverage, target.Context, at)
	if ctx.Err() != nil {
		return snapshot, ctx.Err()
	}
	return snapshot, nil
}

func rolloutSelector(deployment *unstructured.Unstructured) (value, reason string) {
	raw, found, err := unstructured.NestedMap(deployment.Object, "spec", "selector")
	if err != nil || !found {
		return "", "Controller selector unavailable"
	}
	var selector metav1.LabelSelector
	if convertErr := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); convertErr != nil {
		return "", "Controller selector invalid"
	}
	parsed, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil || parsed.Empty() {
		return "", "Controller selector empty or invalid; no unscoped workload list"
	}
	return parsed.String(), ""
}

func rolloutOwnedBy(object *unstructured.Unstructured, kind string, uid types.UID) bool {
	if uid == "" || object.GetUID() == "" {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		version, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil && version.Group == client.DpGVR.G() && owner.Kind == kind && owner.UID == uid && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

// Lists stay in the explicit namespace and selector. Candidates are checked by
// controlling-owner UID before retention; labels alone never prove ownership.
func collectRolloutObjects(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, selector, source string, limit int,
	owned func(*unstructured.Unstructured) bool,
) ([]*unstructured.Unstructured, review.RolloutCoverage) {
	coverage := review.RolloutCoverage{Source: source, State: inspect.ObservationComplete}
	objects := make([]*unstructured.Unstructured, 0, limit)
	var continuation string
	missingUID := 0
	for page := range rolloutMaxPages {
		list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: rolloutListPageSize, Continue: continuation})
		if err != nil {
			coverage.State = inspect.ObservationUnknown
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				coverage.State = inspect.ObservationDenied
			}
			coverage.Detail = fmt.Sprintf("%d retained; named namespace/selector list unavailable", len(objects))
			return objects, coverage
		}
		for index := range list.Items {
			object := &list.Items[index]
			if object.GetNamespace() != namespace {
				continue
			}
			if object.GetUID() == "" {
				missingUID++
				continue
			}
			if !owned(object) {
				continue
			}
			if len(objects) == limit {
				coverage.State = inspect.ObservationIncomplete
				coverage.Detail = fmt.Sprintf("Retained %d controlling-UID-owned objects; cap reached, additional results excluded", limit)
				return objects, coverage
			}
			objects = append(objects, object.DeepCopy())
		}
		continuation = list.GetContinue()
		if continuation == "" {
			break
		}
		if page == rolloutMaxPages-1 {
			coverage.State = inspect.ObservationIncomplete
			coverage.Detail = fmt.Sprintf("Retained %d objects; list stopped at %d pages of %d candidates", len(objects), rolloutMaxPages, rolloutListPageSize)
			return objects, coverage
		}
	}
	coverage.Detail = fmt.Sprintf("%d retained controlling-UID-owned objects; namespace selector scope", len(objects))
	if missingUID > 0 {
		coverage.State = inspect.ObservationIncomplete
		coverage.Detail += fmt.Sprintf("; %d candidates missing UID excluded", missingUID)
	}
	return objects, coverage
}

//nolint:gocritic // Target is retained across child reads, independently of navigation.
func loadControllerChildren(ctx context.Context, dyn dynamic.Interface, target SelectedResourceTarget, workload *unstructured.Unstructured,
	selector string, coverage []review.RolloutCoverage, at time.Time) *review.RolloutSnapshot {
	revisions, revisionCoverage := collectRolloutObjects(ctx, dyn, schema.GroupVersionResource{Group: "apps", Version: rolloutPodAPI, Resource: "controllerrevisions"},
		target.Namespace, selector, "ControllerRevisions", rolloutMaxRevisions, func(o *unstructured.Unstructured) bool {
			return o.GetAPIVersion() == "apps/v1" && o.GetKind() == "ControllerRevision" && rolloutOwnedBy(o, workload.GetKind(), workload.GetUID())
		})
	pods, podCoverage := collectRolloutObjects(ctx, dyn, client.PodGVR.GVR(), target.Namespace, selector, "Pods", rolloutMaxPods,
		func(o *unstructured.Unstructured) bool {
			return o.GetAPIVersion() == rolloutPodAPI && o.GetKind() == "Pod" && rolloutOwnedBy(o, workload.GetKind(), workload.GetUID())
		})
	// Direct controlling ownership does not depend on revision visibility.
	coverage = append(coverage, revisionCoverage, podCoverage)
	return review.NewRolloutSnapshot(workload, revisions, pods, coverage, target.Context, at)
}

func rolloutRevisionSource(kind string) string {
	if kind == rolloutDeployKind {
		return "ReplicaSets"
	}
	return "ControllerRevisions"
}
