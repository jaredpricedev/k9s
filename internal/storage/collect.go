// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"context"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const MaxObjects = 100
const ReadTimeout = 3 * time.Second

type sourceSpec struct {
	name    string
	gvr     schema.GroupVersionResource
	cluster bool
}

var sources = []sourceSpec{
	{SourcePods, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, false},
	{SourcePVCs, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, false},
	{SourcePVs, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, true},
	{SourceClasses, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, true},
	{SourceDrivers, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "csidrivers"}, true},
	{SourceCSINodes, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "csinodes"}, true},
	{SourceAttachments, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "volumeattachments"}, true},
	{SourceEvents, schema.GroupVersionResource{Version: "v1", Resource: "events"}, false},
}

type sourceResult struct {
	source   sourceSpec
	list     *unstructured.UnstructuredList
	coverage capacity.Coverage
}

// Collect reads eight fixed, independently bounded sources without Secrets or
// arbitrary discovery. The result owns only typed storage evidence, not objects.
func Collect(ctx context.Context, reader dynamic.Interface, scope *Scope, now time.Time) *Snapshot {
	snapshot := &Snapshot{Scope: *scope, CapturedAt: now}
	results := make(chan sourceResult, len(sources))
	for _, source := range sources {
		go func(source sourceSpec) { results <- readSource(ctx, reader, scope, source, now) }(source)
	}
	reads := make(map[string]*sourceResult, len(sources))
	for range sources {
		select {
		case result := <-results:
			reads[result.source.name] = &result
		case <-ctx.Done():
			// Keep already completed independent reads even if cancellation and
			// buffered results become selectable at the same time.
			drainResults(results, reads)
			for _, source := range sources {
				if _, ok := reads[source.name]; !ok {
					reads[source.name] = &sourceResult{source: source, coverage: capacity.Coverage{
						Source: source.name, State: capacity.Unavailable, ReadAt: now, Bound: MaxObjects, Detail: ctx.Err().Error(),
					}}
				}
			}
			goto project
		}
	}
project:
	for _, source := range sources {
		snapshot.Coverage = append(snapshot.Coverage, reads[source.name].coverage)
	}
	snapshot.Coverage = append(snapshot.Coverage, capacity.Coverage{Source: SourceUsage, State: capacity.NotConfigured, ReadAt: now,
		Detail: "No named volume-usage provider, observation time or coverage is configured; capacity is not usage or free space"})
	snapshot.project(reads)
	snapshot.join()
	return snapshot
}

func drainResults(results <-chan sourceResult, reads map[string]*sourceResult) {
	for {
		select {
		case result := <-results:
			reads[result.source.name] = &result
		default:
			return
		}
	}
}

func readSource(ctx context.Context, reader dynamic.Interface, scope *Scope, source sourceSpec, now time.Time) sourceResult {
	r := sourceResult{source: source, coverage: capacity.Coverage{Source: source.name, State: capacity.Unavailable, ReadAt: now, Bound: MaxObjects}}
	if err := ctx.Err(); err != nil {
		r.coverage.Detail = err.Error()
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	options := metav1.ListOptions{Limit: MaxObjects + 1}
	name := ""
	switch source.name {
	case SourcePods:
		name = scope.PodName
	case SourcePVCs:
		name = scope.PVCName
	case SourcePVs:
		name = scope.PVName
	case SourceClasses:
		name = scope.ClassName
	case SourceCSINodes:
		name = scope.Node
	}
	if name != "" {
		options.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
	}
	resource := reader.Resource(source.gvr)
	var err error
	if source.cluster {
		r.list, err = resource.List(ctx, options)
	} else {
		r.list, err = resource.Namespace(scope.Namespace).List(ctx, options)
	}
	if err != nil {
		r.coverage.State = errorState(err)
		r.coverage.Detail = err.Error()
		return r
	}
	if r.list == nil {
		r.coverage.Detail = "API returned no collection"
		return r
	}
	r.coverage.ReadAt = time.Now()
	r.coverage.State = capacity.Complete
	if len(r.list.Items) == 0 {
		r.coverage.State = capacity.Empty
	}
	if len(r.list.Items) > MaxObjects || r.list.GetContinue() != "" {
		r.coverage.State = capacity.Partial
		r.coverage.Detail = "Bounded page; more objects may exist. Narrow the scope."
	}
	r.list.Items = r.list.Items[:min(len(r.list.Items), MaxObjects)]
	r.coverage.Visible = len(r.list.Items)
	return r
}
func errorState(err error) capacity.State {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return capacity.Denied
	case apierrors.IsNotFound(err), apiMeta.IsNoMatchError(err):
		return capacity.Absent
	default:
		return capacity.Unavailable
	}
}
func items(read *sourceResult) []unstructured.Unstructured {
	if read.list == nil {
		return nil
	}
	return read.list.Items
}
func (s *Snapshot) identity(gvr string, obj metav1.Object) inspect.ResourceIdentity {
	return inspect.ResourceIdentity{Context: s.Scope.Identity.Context, GVR: gvr, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID())}
}
func (s *Snapshot) decodeFailure(source string, err error) {
	for i := range s.Coverage {
		if s.Coverage[i].Source == source {
			s.Coverage[i].State = capacity.Partial
			s.Coverage[i].Detail = "Some evidence could not be decoded: " + err.Error()
		}
	}
}
