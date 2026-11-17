// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package alerts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/observability"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

const (
	// MaxOwnerLookups bounds how many alerted pods have their owner read.
	// Pods beyond it keep an unknown owner; nothing is paged or retried.
	MaxOwnerLookups = 30
	// OwnerTimeout bounds one metadata read; OwnerDeadline bounds them all.
	OwnerTimeout  = 3 * time.Second
	OwnerDeadline = 5 * time.Second
)

// Owner read states use the investigation vocabulary in CONTEXT.md.
const (
	OwnersComplete   = "complete"
	OwnersIncomplete = "incomplete"
	OwnersDenied     = "denied"
	OwnersUnknown    = "unknown"
	OwnersNotNeeded  = "not-needed"
)

// Source is the one alert read a snapshot needs. *observability.Client
// satisfies it.
type Source interface {
	Alerts(context.Context, observability.AlertQuery) observability.AlertsResult
}

// OwnerRead reports the bounded metadata reads that resolved pod owners.
type OwnerRead struct {
	State                    string
	Asked, Resolved, Missing int
	Detail                   string // why the read is not complete
}

// Snapshot is one explicit observation of what Alertmanager holds.
type Snapshot struct {
	Status      observability.Status
	ReadAt      time.Time
	Items       []Item
	NotRetained int
	Owners      OwnerRead
}

// Collect reads the alerts once and, for alerted pods whose workload the
// labels do not name, reads that pod's owner references through a
// metadata-only client. A nil meta skips the owner reads. It never retries.
func Collect(ctx context.Context, source Source, meta metadata.Interface, now time.Time) Snapshot {
	result := source.Alerts(ctx, observability.AlertQuery{})
	snapshot := Snapshot{Status: result.Status, ReadAt: now, NotRetained: result.NotRetained, Owners: OwnerRead{State: OwnersNotNeeded}}
	if !result.OK() {
		return snapshot
	}
	snapshot.Items = make([]Item, 0, len(result.Alerts))
	for i := range result.Alerts {
		snapshot.Items = append(snapshot.Items, NewItem(&result.Alerts[i]))
	}
	snapshot.Owners = resolveOwners(ctx, meta, snapshot.Items)
	return snapshot
}

var (
	podResource        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	replicaSetResource = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	ownerResources     = map[string]string{
		"Deployment": "apps/v1/deployments", "StatefulSet": "apps/v1/statefulsets", "DaemonSet": "apps/v1/daemonsets",
		"Job": "batch/v1/jobs", "ReplicaSet": "apps/v1/replicasets",
	}
)

type ownerAnswer struct {
	owner   *Target
	missing bool
	err     error
}

func controller(meta *metav1.ObjectMeta) *metav1.OwnerReference {
	for i := range meta.OwnerReferences {
		if ref := &meta.OwnerReferences[i]; ref.Controller != nil && *ref.Controller {
			return ref
		}
	}
	return nil
}

// lookupOwner follows a pod's controller reference, and a ReplicaSet's on to
// its Deployment. A pod with no controller has no owner; that is an answer.
func lookupOwner(ctx context.Context, meta metadata.Interface, namespace, pod string) ownerAnswer {
	get := func(resource schema.GroupVersionResource, name string) (*metav1.PartialObjectMetadata, error) {
		readCtx, cancel := context.WithTimeout(ctx, OwnerTimeout)
		defer cancel()
		return meta.Resource(resource).Namespace(namespace).Get(readCtx, name, metav1.GetOptions{})
	}
	object, err := get(podResource, pod)
	if apierrors.IsNotFound(err) {
		return ownerAnswer{missing: true}
	}
	if err != nil {
		return ownerAnswer{err: err}
	}
	ref := controller(&object.ObjectMeta)
	if ref == nil {
		return ownerAnswer{}
	}
	if ref.Kind == "ReplicaSet" {
		// Without the ReplicaSet's own owner the pod is reported under the
		// ReplicaSet, which is what was read.
		if set, err := get(replicaSetResource, ref.Name); err == nil {
			if parent := controller(&set.ObjectMeta); parent != nil && ownerResources[parent.Kind] != "" {
				ref = parent
			}
		}
	}
	resource, ok := ownerResources[ref.Kind]
	if !ok {
		return ownerAnswer{}
	}
	return ownerAnswer{owner: &Target{Kind: ref.Kind, Namespace: namespace, Name: ref.Name, Resource: resource}}
}

func resolveOwners(ctx context.Context, meta metadata.Interface, items []Item) OwnerRead {
	wanted := make(map[string][]int) // namespace/pod -> item indexes
	var order []string
	for i := range items {
		if item := &items[i]; item.Target.Kind == "Pod" && item.Owner == nil {
			key := item.Target.Namespace + "/" + item.Target.Name
			if _, seen := wanted[key]; !seen {
				order = append(order, key)
			}
			wanted[key] = append(wanted[key], i)
		}
	}
	read := OwnerRead{State: OwnersNotNeeded}
	if len(order) == 0 {
		return read
	}
	if meta == nil {
		return OwnerRead{State: OwnersUnknown, Detail: "no Kubernetes metadata client"}
	}
	sort.Strings(order)
	skipped := max(0, len(order)-MaxOwnerLookups)
	order = order[:len(order)-skipped]
	read.Asked = len(order)
	ctx, cancel := context.WithTimeout(ctx, OwnerDeadline)
	defer cancel()
	answers := make([]ownerAnswer, len(order))
	var pass sync.WaitGroup
	for index, key := range order {
		pass.Add(1)
		go func() {
			defer pass.Done()
			target := &items[wanted[key][0]].Target
			answers[index] = lookupOwner(ctx, meta, target.Namespace, target.Name)
		}()
	}
	pass.Wait()
	denied, failed := 0, 0
	for index, key := range order {
		answer := answers[index]
		switch {
		case answer.err != nil && (apierrors.IsForbidden(answer.err) || apierrors.IsUnauthorized(answer.err)):
			denied++
			continue
		case answer.err != nil:
			failed++
			if errors.Is(answer.err, context.DeadlineExceeded) || ctx.Err() != nil {
				read.Detail = "timed out"
			}
			continue
		}
		read.Resolved++
		if answer.missing {
			read.Missing++
		}
		for _, i := range wanted[key] {
			items[i].Missing = answer.missing
			if answer.owner != nil {
				owner := *answer.owner
				items[i].Owner, items[i].OwnerFrom = &owner, "owner references"
			}
		}
	}
	switch {
	case denied == len(order):
		read.State, read.Detail = OwnersDenied, "pod metadata read is not permitted"
	case denied+failed+skipped > 0:
		read.State = OwnersIncomplete
		detail := fmt.Sprintf("%d of %d pods", read.Resolved, len(order)+skipped)
		if read.Detail != "" {
			detail += ", " + read.Detail
		}
		if skipped > 0 {
			detail += fmt.Sprintf(", limit %d", MaxOwnerLookups)
		}
		read.Detail = detail
	default:
		read.State = OwnersComplete
	}
	return read
}
