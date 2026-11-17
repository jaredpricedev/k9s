// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package probe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/networkpath"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Kinds of selection a probe accepts.
const (
	KindService     = "Service"
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindReplicaSet  = "ReplicaSet"
	KindPod         = "Pod"
)

// Read limits of building a plan.
const (
	// ResolveDeadline bounds every Kubernetes read of one plan.
	ResolveDeadline = 25 * time.Second
	// readTimeout bounds one GET or LIST.
	readTimeout = 4 * time.Second
	// listLimit is the page size of the one Pod list read for a workload.
	listLimit = 64
	// zoneDeadline bounds the Node reads that supply zones.
	zoneDeadline = 4 * time.Second
)

var (
	podsResource  = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodesResource = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	svcResource   = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	rsResource    = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	workloads     = map[string]schema.GroupVersionResource{
		KindDeployment:  {Group: "apps", Version: "v1", Resource: "deployments"},
		KindStatefulSet: {Group: "apps", Version: "v1", Resource: "statefulsets"},
		KindDaemonSet:   {Group: "apps", Version: "v1", Resource: "daemonsets"},
		KindReplicaSet:  rsResource,
	}
	zoneLabels = []string{"topology.kubernetes.io/zone", "failure-domain.beta.kubernetes.io/zone"}
)

// Accepts says whether a selection of this kind can be probed.
func Accepts(kind string) bool {
	_, workload := workloads[kind]
	return workload || kind == KindService || kind == KindPod
}

func readError(what string, err error) error {
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return errors.New(what + ": access denied")
	case apierrors.IsNotFound(err):
		return errors.New(what + ": not found")
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err):
		return errors.New(what + ": read timed out")
	case errors.Is(err, context.Canceled):
		return errors.New(what + ": read canceled")
	}
	return errors.New(what + ": read failed")
}

func get(ctx context.Context, objects dynamic.Interface, resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	if namespace == "" {
		return objects.Resource(resource).Get(readCtx, name, metav1.GetOptions{})
	}
	return objects.Resource(resource).Namespace(namespace).Get(readCtx, name, metav1.GetOptions{})
}

func list(ctx context.Context, objects dynamic.Interface, resource schema.GroupVersionResource, namespace, selector string) (*unstructured.UnstructuredList, error) {
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return objects.Resource(resource).Namespace(namespace).List(readCtx, metav1.ListOptions{LabelSelector: selector, Limit: listLimit})
}

func controllerOf(object *unstructured.Unstructured) *metav1.OwnerReference {
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return &owner
		}
	}
	return nil
}

// Resolve reads the Pods behind a selection and their declared probes, and
// returns what a probe would send. It reads Kubernetes objects only: no Pod
// is contacted. Every read is bounded, and a Pod stands in for the selection
// only when its UID was verified.
func Resolve(ctx context.Context, objects dynamic.Interface, contextName string, scope Scope) (*Plan, error) {
	if objects == nil {
		return nil, errors.New("Kubernetes resource client is unavailable")
	}
	if !Accepts(scope.Kind) || scope.Namespace == "" || scope.Name == "" {
		return nil, errors.New("select a Service, Deployment, StatefulSet, DaemonSet, ReplicaSet or Pod")
	}
	ctx, cancel := context.WithTimeout(ctx, ResolveDeadline)
	defer cancel()
	var (
		pods  []Pod
		found int
		notes []string
		err   error
	)
	switch scope.Kind {
	case KindPod:
		pods, err = onePod(ctx, objects, scope)
		found = len(pods)
	case KindService:
		pods, found, notes, err = servicePods(ctx, objects, contextName, scope)
	default:
		pods, found, notes, err = workloadPods(ctx, objects, scope)
	}
	if err != nil {
		return nil, err
	}
	plan := NewPlan(scope, pods, found)
	plan.Notes = notes
	if note := readZones(ctx, objects, plan); note != "" {
		plan.Notes = append(plan.Notes, note)
	}
	plan.ReadAt = time.Now().UTC()
	return plan, ctx.Err()
}

func onePod(ctx context.Context, objects dynamic.Interface, scope Scope) ([]Pod, error) {
	object, err := get(ctx, objects, podsResource, scope.Namespace, scope.Name)
	if err != nil {
		return nil, readError(scope.Label(), err)
	}
	if scope.UID != "" && string(object.GetUID()) != scope.UID {
		return nil, errors.New(scope.Label() + " was replaced since it was selected; select the current Pod")
	}
	return []Pod{PodFromObject(object.Object)}, nil
}

// servicePods resolves the Pods behind a Service with the network path
// collector, so that :probe and :network-review agree on the backends, then
// reads up to MaxPods of them for their declared probes.
func servicePods(ctx context.Context, objects dynamic.Interface, contextName string, scope Scope) (pods []Pod, found int, notes []string, err error) {
	uid := scope.UID
	if uid == "" {
		service, getErr := get(ctx, objects, svcResource, scope.Namespace, scope.Name)
		if getErr != nil {
			return nil, 0, nil, readError(scope.Label(), getErr)
		}
		uid = string(service.GetUID())
	}
	snapshot, collectErr := networkpath.Collect(ctx, objects, networkpath.Scope{Service: inspect.ResourceIdentity{
		Context: contextName, GVR: networkpath.ServiceGVR, Namespace: scope.Namespace, Name: scope.Name, UID: uid}})
	if snapshot == nil || (collectErr != nil && len(snapshot.Pods) == 0) {
		if collectErr == nil {
			collectErr = errors.New("no path evidence")
		}
		return nil, 0, nil, fmt.Errorf("%s: %s", scope.Label(), collectErr.Error())
	}
	for i := range snapshot.Coverage {
		coverage := &snapshot.Coverage[i]
		if coverage.GVR != networkpath.PodGVR || coverage.Name != "" {
			continue
		}
		switch coverage.State {
		case networkpath.StateTruncated:
			notes = append(notes, "the Pod list of the Service was cut by its read limit; more Pods may exist")
		case inspect.ObservationDenied:
			notes = append(notes, "listing the Pods of the Service was denied")
		}
	}
	type identity struct{ name, uid string }
	behind := make([]identity, 0, len(snapshot.Pods))
	for i := range snapshot.Pods {
		id := snapshot.Pods[i].Source.Identity
		behind = append(behind, identity{id.Name, id.UID})
	}
	sort.Slice(behind, func(i, j int) bool { return behind[i].name < behind[j].name })
	found = len(behind)
	replaced := 0
	for _, id := range behind[:min(len(behind), MaxPods)] {
		object, getErr := get(ctx, objects, podsResource, scope.Namespace, id.name)
		if getErr != nil || string(object.GetUID()) != id.uid {
			// A Pod replaced between the two reads is not the backend that
			// was resolved, so it is not probed in its place.
			replaced++
			continue
		}
		pods = append(pods, PodFromObject(object.Object))
	}
	if replaced > 0 {
		found -= replaced
		notes = append(notes, fmt.Sprintf("%d backend Pod(s) changed or could not be read again and are not listed", replaced))
	}
	return pods, found, notes, nil
}

// workloadPods lists the Pods a workload controls: one list by its selector,
// kept to Pods whose controller is the workload (through its ReplicaSets
// for a Deployment).
func workloadPods(ctx context.Context, objects dynamic.Interface, scope Scope) (pods []Pod, found int, notes []string, err error) {
	workload, err := get(ctx, objects, workloads[scope.Kind], scope.Namespace, scope.Name)
	if err != nil {
		return nil, 0, nil, readError(scope.Label(), err)
	}
	if scope.UID != "" && string(workload.GetUID()) != scope.UID {
		return nil, 0, nil, errors.New(scope.Label() + " was replaced since it was selected; select the current one")
	}
	declared, _, _ := unstructured.NestedMap(workload.Object, "spec", "selector")
	var labelSelector metav1.LabelSelector
	convertErr := runtime.DefaultUnstructuredConverter.FromUnstructured(declared, &labelSelector)
	if convertErr != nil || (len(labelSelector.MatchLabels) == 0 && len(labelSelector.MatchExpressions) == 0) {
		return nil, 0, nil, errors.New(scope.Label() + " has no usable Pod selector")
	}
	selector, err := metav1.LabelSelectorAsSelector(&labelSelector)
	if err != nil {
		return nil, 0, nil, errors.New(scope.Label() + " has no usable Pod selector")
	}
	owners := map[string]bool{string(workload.GetUID()): true}
	if scope.Kind == KindDeployment {
		// A Deployment controls ReplicaSets, which control the Pods.
		owners = map[string]bool{}
		sets, listErr := list(ctx, objects, rsResource, scope.Namespace, selector.String())
		if listErr != nil {
			return nil, 0, nil, readError("ReplicaSets of "+scope.Label(), listErr)
		}
		for i := range sets.Items {
			if owner := controllerOf(&sets.Items[i]); owner != nil && owner.UID == workload.GetUID() {
				owners[string(sets.Items[i].GetUID())] = true
			}
		}
		if sets.GetContinue() != "" {
			notes = append(notes, fmt.Sprintf("the ReplicaSet list was cut at %d; Pods of an unlisted ReplicaSet are missing", listLimit))
		}
	}
	listed, err := list(ctx, objects, podsResource, scope.Namespace, selector.String())
	if err != nil {
		return nil, 0, nil, readError("Pods of "+scope.Label(), err)
	}
	for i := range listed.Items {
		item := &listed.Items[i]
		owner := controllerOf(item)
		if owner == nil || !owners[string(owner.UID)] || !selector.Matches(labels.Set(item.GetLabels())) {
			continue
		}
		pods = append(pods, PodFromObject(item.Object))
	}
	if listed.GetContinue() != "" {
		notes = append(notes, fmt.Sprintf("the Pod list was cut at %d; more Pods exist than are counted here", listLimit))
	}
	return pods, len(pods), notes, nil
}

// readZones fills the zone of each listed Pod from its Node's topology
// label. It stops at the first failed read and says so, because a zone that
// was not read is unknown, not absent.
func readZones(ctx context.Context, objects dynamic.Interface, plan *Plan) string {
	ctx, cancel := context.WithTimeout(ctx, zoneDeadline)
	defer cancel()
	zones := map[string]string{}
	for i := range plan.Targets {
		node := plan.Targets[i].Pod.Node
		if node == "" {
			continue
		}
		zone, read := zones[node]
		if !read {
			object, err := get(ctx, objects, nodesResource, "", node)
			if err != nil {
				reason := "read failed"
				if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
					reason = "access to nodes denied"
				}
				return "zones not read: " + reason
			}
			for _, label := range zoneLabels {
				if zone = object.GetLabels()[label]; zone != "" {
					break
				}
			}
			zones[node] = zone
		}
		plan.Targets[i].Pod.Zone = zone
	}
	return ""
}

// AccessReviewer creates one self subject access review.
type AccessReviewer interface {
	Create(ctx context.Context, review *authorizationv1.SelfSubjectAccessReview, opts metav1.CreateOptions) (*authorizationv1.SelfSubjectAccessReview, error)
}

// CheckAccess asks the API server whether this user may GET pods/proxy in
// the namespace. The review is a question about the user; it creates
// nothing and contacts no Pod. No answer is AccessUnknown, not a refusal.
func CheckAccess(ctx context.Context, reviewer AccessReviewer, namespace string) (access Access, reason string) {
	if reviewer == nil {
		return AccessUnknown, "access was not checked"
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	review, err := reviewer.Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: "get", Resource: "pods", Subresource: "proxy"},
	}}, metav1.CreateOptions{})
	if err != nil || review == nil {
		return AccessUnknown, "the access review did not answer"
	}
	if !review.Status.Allowed {
		return AccessDenied, "missing " + MissingAccess(namespace)
	}
	return AccessAllowed, ""
}
