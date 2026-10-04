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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const jobReviewPageSize, jobReviewMaxPages = 200, 4

//nolint:gocritic // Keep the captured identity immutable across asynchronous bounded reads.
func loadJobReview(ctx context.Context, connection client.Connection, target SelectedResourceTarget) (*review.JobReviewSnapshot, error) {
	if err := jobReviewTargetError(target); err != nil {
		return nil, err
	}
	dyn, err := connection.DynDial()
	if err != nil {
		return nil, err
	}
	if dyn == nil {
		return nil, fmt.Errorf("Job review client unavailable")
	}
	source, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := verifySelectedIdentity(target, source); err != nil {
		return nil, err
	}
	kind := "Job"
	if target.GVR == client.CjGVR {
		kind = "CronJob"
	}
	if source.GetKind() != kind || source.GetAPIVersion() != "batch/v1" {
		return nil, fmt.Errorf("The selected source is not a native batch/v1 %s", kind)
	}
	at := time.Now().UTC()
	coverage := []review.RolloutCoverage{{Source: kind, State: inspect.ObservationComplete, Detail: "Selected source GET; status is retained API evidence"}}
	jobs := []*unstructured.Unstructured{source}
	if kind == "CronJob" {
		var history review.RolloutCoverage
		jobs, history = collectJobReviewChildren(ctx, dyn, client.JobGVR.GVR(), target.Namespace, "Jobs",
			func(object *unstructured.Unstructured) bool {
				return review.JobReviewOwnedBy(object, kind, source.GetUID())
			})
		coverage = append(coverage, history)
	}
	// Project first so Pod ownership follows only the newest retained Job UIDs.
	snapshot := review.NewJobReviewSnapshot(source, jobs, nil, coverage, target.Context, at)
	owners := make(map[types.UID]bool)
	for index := range snapshot.Runs {
		owners[types.UID(snapshot.Runs[index].Identity.UID)] = true
	}
	var pods []*unstructured.Unstructured
	if len(owners) == 0 {
		coverage = append(coverage, review.RolloutCoverage{Source: "Pods", State: inspect.ObservationUnknown,
			Detail: "No verified retained Job UID obtained; Pod read not attempted"})
	} else {
		var visibility review.RolloutCoverage
		pods, visibility = collectJobReviewChildren(ctx, dyn, client.PodGVR.GVR(), target.Namespace, "Pods",
			func(object *unstructured.Unstructured) bool {
				for owner := range owners {
					if review.JobReviewOwnedBy(object, "Job", owner) {
						return true
					}
				}
				return false
			})
		coverage = append(coverage, visibility)
	}
	snapshot = review.NewJobReviewSnapshot(source, jobs, pods, coverage, target.Context, at)
	return snapshot, ctx.Err()
}

// The endpoint is always the captured namespace. Bound candidates as well as
// pages, even if a server ignores ListOptions.Limit; names/labels never replace
// the controlling-owner UID check. Partial results remain explicitly partial.
func collectJobReviewChildren(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, source string,
	owned func(*unstructured.Unstructured) bool,
) ([]*unstructured.Unstructured, review.RolloutCoverage) {
	visibility := review.RolloutCoverage{Source: source, State: inspect.ObservationComplete}
	objects := make([]*unstructured.Unstructured, 0)
	continuation, scanned, missingUID := "", 0, 0
	for page := range jobReviewMaxPages {
		list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: jobReviewPageSize, Continue: continuation})
		if err != nil {
			visibility.State = inspect.ObservationUnknown
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				visibility.State = inspect.ObservationDenied
			}
			visibility.Detail = fmt.Sprintf("%d UID-owned records obtained; namespace read unavailable: %s", len(objects), err)
			return objects, visibility
		}
		for index := range list.Items {
			if scanned == review.JobReviewCandidateCap {
				visibility.State = inspect.ObservationIncomplete
				visibility.Detail = "Stopped after 800 candidates; additional namespace records excluded"
				return objects, visibility
			}
			scanned++
			object := &list.Items[index]
			if object.GetNamespace() != namespace {
				continue
			}
			if object.GetUID() == "" {
				missingUID++
				continue
			}
			if owned(object) {
				objects = append(objects, object.DeepCopy())
			}
		}
		continuation = list.GetContinue()
		if continuation == "" {
			break
		}
		if page == jobReviewMaxPages-1 {
			visibility.State = inspect.ObservationIncomplete
			visibility.Detail = "Stopped after four namespace pages; additional records excluded"
			return objects, visibility
		}
	}
	visibility.Detail = fmt.Sprintf("%d controlling-UID-owned records among %d candidates; current namespace list, not complete history", len(objects), scanned)
	if missingUID > 0 {
		visibility.State = inspect.ObservationIncomplete
		visibility.Detail += fmt.Sprintf("; %d candidates missing UID excluded", missingUID)
	}
	return objects, visibility
}
