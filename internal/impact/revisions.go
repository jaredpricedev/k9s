// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/timeline"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Workload kinds a comparison can follow.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindPod         = "Pod"
)

// Read bounds for the revision history.
const (
	ReadTimeout  = 3 * time.Second
	MaxRevisions = 200

	revisionAnnotation = "deployment.kubernetes.io/revision"
)

var (
	podsResource        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	replicaSetsResource = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	revisionsResource   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
	workloadResources   = map[string]schema.GroupVersionResource{
		KindDeployment:  {Group: "apps", Version: "v1", Resource: "deployments"},
		KindStatefulSet: {Group: "apps", Version: "v1", Resource: "statefulsets"},
		KindDaemonSet:   {Group: "apps", Version: "v1", Resource: "daemonsets"},
	}
)

// Workload identifies what is compared.
type Workload struct{ Kind, Namespace, Name string }

// Label names the workload in one phrase.
func (w Workload) Label() string { return w.Kind + " " + w.Namespace + "/" + w.Name }

// Resource is the workload's API resource in group/version/resource form.
func (w Workload) Resource() string {
	r := workloadResources[w.Kind]
	return r.Group + "/" + r.Version + "/" + r.Resource
}

// PodPattern is the anchored regular expression that the workload's pod
// names follow: name-<hash>-<id> for a Deployment, name-<ordinal> for a
// StatefulSet and name-<id> for a DaemonSet. Pods are matched by name, not
// by owner, because metrics carry the pod name and not the owner chain.
func (w Workload) PodPattern() string {
	name := regexp.QuoteMeta(w.Name)
	switch w.Kind {
	case KindStatefulSet:
		return name + "-[0-9]+"
	case KindDaemonSet:
		return name + "-[a-z0-9]+"
	default:
		return name + "-[a-z0-9]+-[a-z0-9]+"
	}
}

// Revision is one retained revision of a workload's pod template.
type Revision struct {
	Number   int64
	Name     string
	Created  time.Time
	Template *corev1.PodTemplateSpec
}

// History is a workload's retained revisions, oldest first.
type History struct {
	Workload  Workload
	Revisions []Revision
	// Notes state what the history cannot show.
	Notes []string
	// Incomplete is set when the revision list was cut by the read limit,
	// so the latest retained revision may not be the latest rollout.
	Incomplete string
}

// Rollout is one revision taking effect, with its neighbours.
type Rollout struct {
	Revision Revision
	// Previous is the revision it replaced, when that is still retained.
	Previous *Revision
	// NextAt is when the following revision was created, or zero.
	NextAt time.Time
	// Back counts rollouts between this one and the latest; Total is the
	// number of retained revisions.
	Back, Total int
}

// Label is "rev 2".
func (r *Rollout) Label() string { return "rev " + strconv.FormatInt(r.Revision.Number, 10) }

// PreviousAt is when the replaced revision was created, or zero.
func (r *Rollout) PreviousAt() time.Time {
	if r.Previous == nil {
		return time.Time{}
	}
	return r.Previous.Created
}

// Changes lists what the revision's pod template changed against the one it
// replaced (images, environment key names, resources; never values), and
// the memory limit change as "512Mi -> 256Mi" when there was one.
func (r *Rollout) Changes() (changes []string, memoryLimit string) {
	if r.Previous == nil || r.Previous.Template == nil || r.Revision.Template == nil {
		return nil, ""
	}
	changes = timeline.TemplateChanges(r.Previous.Template, r.Revision.Template)
	// Environment key lists can be long; images and resources read first.
	sort.SliceStable(changes, func(i, j int) bool {
		return !strings.Contains(changes[i], "env ") && strings.Contains(changes[j], "env ")
	})
	old := map[string]corev1.Container{}
	for _, c := range r.Previous.Template.Spec.Containers {
		old[c.Name] = c
	}
	var limits []string
	for _, c := range r.Revision.Template.Spec.Containers {
		previous, found := old[c.Name]
		if !found {
			continue
		}
		a, hadA := previous.Resources.Limits[corev1.ResourceMemory]
		b, hadB := c.Resources.Limits[corev1.ResourceMemory]
		if hadA == hadB && a.Cmp(b) == 0 {
			continue
		}
		from, to := "unset", "unset"
		if hadA {
			from = a.String()
		}
		if hadB {
			to = b.String()
		}
		text := from + " -> " + to
		if len(r.Revision.Template.Spec.Containers) > 1 {
			text = c.Name + " " + text
		}
		limits = append(limits, text)
	}
	return changes, strings.Join(limits, ", ")
}

// Rollout returns the rollout that is back steps before the latest one.
// A revision that was created before the one it replaced is a rollback onto
// an older ReplicaSet; Kubernetes does not record when that happened, so no
// windows can be placed around it.
func (h *History) Rollout(back int) (Rollout, error) {
	total := len(h.Revisions)
	if total == 0 {
		return Rollout{}, errors.New("no revision of " + h.Workload.Label() + " is retained, so no rollout time is known")
	}
	back = min(max(back, 0), total-1)
	index := total - 1 - back
	rollout := Rollout{Revision: h.Revisions[index], Back: back, Total: total}
	if index > 0 {
		rollout.Previous = &h.Revisions[index-1]
	}
	if index < total-1 {
		rollout.NextAt = h.Revisions[index+1].Created
	}
	if rollout.Revision.Created.IsZero() {
		return rollout, errors.New(rollout.Label() + " carries no creation time")
	}
	if rollout.Previous != nil && rollout.Revision.Created.Before(rollout.Previous.Created) {
		return rollout, fmt.Errorf("%s reuses %s, created %s, before the revision it replaced: a rollback. Kubernetes does not record when a rollback happened, so there is no rollout time to compare around",
			rollout.Label(), rollout.Revision.Name, rollout.Revision.Created.UTC().Format("2006-01-02 15:04Z"))
	}
	return rollout, nil
}

func readError(what string, err error) error {
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return errors.New(what + ": access denied; the rollout history is unknown")
	case apierrors.IsNotFound(err):
		return errors.New(what + ": not found")
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err):
		return errors.New(what + ": read timed out")
	case errors.Is(err, context.Canceled):
		return errors.New(what + ": read canceled")
	default:
		return errors.New(what + ": read failed")
	}
}

func controller(object *unstructured.Unstructured) *metav1.OwnerReference {
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return &owner
		}
	}
	return nil
}

func get(ctx context.Context, objects dynamic.Interface, resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	return objects.Resource(resource).Namespace(namespace).Get(readCtx, name, metav1.GetOptions{})
}

// Resolve finds the workload behind a selection: the Deployment, StatefulSet
// or DaemonSet itself, or the one that controls a selected Pod.
func Resolve(ctx context.Context, objects dynamic.Interface, kind, namespace, name string) (Workload, error) {
	if _, ok := workloadResources[kind]; ok {
		return Workload{Kind: kind, Namespace: namespace, Name: name}, nil
	}
	if kind != KindPod {
		return Workload{}, errors.New("select a Deployment, StatefulSet, DaemonSet or one of their Pods")
	}
	pod, err := get(ctx, objects, podsResource, namespace, name)
	if err != nil {
		return Workload{}, readError("Pod "+namespace+"/"+name, err)
	}
	owner := controller(pod)
	if owner != nil && owner.Kind == "ReplicaSet" {
		set, err := get(ctx, objects, replicaSetsResource, namespace, owner.Name)
		if err != nil {
			return Workload{}, readError("ReplicaSet "+namespace+"/"+owner.Name, err)
		}
		owner = controller(set)
	}
	if owner == nil {
		return Workload{}, errors.New("Pod " + namespace + "/" + name + " has no controller, so it has no rollout to compare around")
	}
	if _, ok := workloadResources[owner.Kind]; !ok {
		return Workload{}, fmt.Errorf("Pod %s/%s is controlled by a %s, which has no rollout history", namespace, name, owner.Kind)
	}
	return Workload{Kind: owner.Kind, Namespace: namespace, Name: owner.Name}, nil
}

// ReadHistory reads the workload and its retained revisions: ReplicaSets
// for a Deployment, ControllerRevisions for a StatefulSet or DaemonSet. It
// makes two bounded reads and changes nothing.
func ReadHistory(ctx context.Context, objects dynamic.Interface, w Workload) (*History, error) {
	resource, ok := workloadResources[w.Kind]
	if !ok {
		return nil, errors.New("select a Deployment, StatefulSet, DaemonSet or one of their Pods")
	}
	object, err := get(ctx, objects, resource, w.Namespace, w.Name)
	if err != nil {
		return nil, readError(w.Label(), err)
	}
	history := &History{Workload: w}
	options := metav1.ListOptions{Limit: MaxRevisions}
	if selector, found, _ := unstructured.NestedMap(object.Object, "spec", "selector"); found {
		var typed metav1.LabelSelector
		if runtime.DefaultUnstructuredConverter.FromUnstructured(selector, &typed) == nil {
			if parsed, err := metav1.LabelSelectorAsSelector(&typed); err == nil {
				options.LabelSelector = parsed.String()
			}
		}
	}
	listResource, what := replicaSetsResource, "ReplicaSets"
	if w.Kind != KindDeployment {
		listResource, what = revisionsResource, "ControllerRevisions"
	}
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	list, err := objects.Resource(listResource).Namespace(w.Namespace).List(readCtx, options)
	if err != nil {
		return nil, readError(what+" of "+w.Label(), err)
	}
	if list.GetContinue() != "" {
		history.Incomplete = fmt.Sprintf("Only the first %d %s were read (limit %d): a later rollout may be missing.", len(list.Items), what, MaxRevisions)
		history.Notes = append(history.Notes, history.Incomplete)
	}
	for index := range list.Items {
		item := &list.Items[index]
		if owner := controller(item); owner == nil || !ownedBy(owner, object.GetUID(), w) {
			continue
		}
		revision := Revision{Name: item.GetName(), Created: item.GetCreationTimestamp().Time}
		if w.Kind == KindDeployment {
			var set appsv1.ReplicaSet
			if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &set) != nil {
				continue
			}
			revision.Number, _ = strconv.ParseInt(set.Annotations[revisionAnnotation], 10, 64)
			revision.Template = &set.Spec.Template
		} else {
			revision.Number, _, _ = unstructured.NestedInt64(item.Object, "revision")
			revision.Template = revisionTemplate(item)
		}
		if revision.Number > 0 {
			history.Revisions = append(history.Revisions, revision)
		}
	}
	sort.SliceStable(history.Revisions, func(i, j int) bool { return history.Revisions[i].Number < history.Revisions[j].Number })
	history.Notes = append(history.Notes,
		"A rollout time is the creation time of the revision's "+strings.TrimSuffix(what, "s")+". Revisions pruned by the history limit are not shown.")
	return history, nil
}

func ownedBy(owner *metav1.OwnerReference, uid types.UID, w Workload) bool {
	if uid != "" && owner.UID != "" {
		return owner.UID == uid
	}
	return owner.Kind == w.Kind && owner.Name == w.Name
}

// revisionTemplate reads the pod template a ControllerRevision recorded. Its
// data is the patch {"spec":{"template":{...}}}.
func revisionTemplate(item *unstructured.Unstructured) *corev1.PodTemplateSpec {
	raw, found, err := unstructured.NestedMap(item.Object, "data", "spec", "template")
	if err != nil || !found {
		return nil
	}
	delete(raw, "$patch")
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var template corev1.PodTemplateSpec
	if json.Unmarshal(encoded, &template) != nil {
		return nil
	}
	return &template
}
