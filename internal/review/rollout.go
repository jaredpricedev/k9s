// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	MaxRolloutRevisions = 32
	MaxRolloutPods      = 64
	rolloutInputLimit   = 800
	rolloutStatusLimit  = 100
	rolloutAppsAPI      = "apps/v1"
	rolloutDeployment   = "Deployment"
	rolloutReplicaSet   = "ReplicaSet"
	rolloutTemplateHash = "pod-template-hash"
	RolloutComplete     = "complete"
	RolloutProgressing  = "progressing"
	RolloutBlocked      = "blocked"
	RolloutPaused       = "paused"
	RolloutUnknown      = "unknown"
	RolloutNotExecuted  = "not executed"
)

// RolloutSnapshot is retained API evidence for one native Deployment. Counts
// remain optional: absent status fields are never converted into measured zero.
type RolloutSnapshot struct {
	Identity                                     inspect.ResourceIdentity
	CapturedAt                                   time.Time
	DeploymentState                              string
	Generation, ObservedGeneration               *int64
	Desired, Updated, Ready, Available, Replicas *int64
	Paused                                       bool
	Conditions                                   []RolloutCondition
	Revisions                                    []RolloutRevision
	Pods                                         []RolloutPod
	Coverage                                     []RolloutCoverage
	DeploymentTemplate                           inspect.Observation
}

type RolloutCondition struct {
	Type, Status, Reason, Message string
	UpdatedAt, TransitionAt       time.Time
}

// Current means the retained API template matches the Deployment template
// after removing only pod-template-hash. Multiple revisions can match; neither
// revision number nor creation time establishes the controller's chosen RS.
type RolloutRevision struct {
	Identity                   inspect.ResourceIdentity
	Revision                   string
	Current, CurrentKnown      bool
	Replicas, Ready, Available *int64
	Template                   inspect.Observation
}

type RolloutPod struct {
	Identity             inspect.ResourceIdentity
	ReplicaSetUID, Phase string
	Images               []RolloutContainerImage
}

type RolloutContainerImage struct {
	Name, Role, Declared, ImageID, State, Reason string
	Ready                                        *bool
	Restarts                                     *int64
}

// Coverage is collection visibility, not a controller health result.
type RolloutCoverage struct{ Source, State, Detail string }

type RolloutRecovery struct {
	RevisionIdentity inspect.ResourceIdentity
	Revision         string
	Comparison       inspect.Comparison
	Limits           []string
	State, Reason    string
}

// NewRolloutSnapshot owns projections of the fetched objects. It checks actual
// controlling owner UIDs even when the caller used a label selector. It performs
// no reads, writes or implicit selection of a recovery revision.
func NewRolloutSnapshot(
	deployment *unstructured.Unstructured, replicaSets, pods []*unstructured.Unstructured,
	coverage []RolloutCoverage, contextName string, at time.Time,
) *RolloutSnapshot {
	s := &RolloutSnapshot{CapturedAt: at, DeploymentState: inspect.ObservationUnknown}
	for _, c := range coverage {
		s.Coverage = append(s.Coverage, RolloutCoverage{Source: rolloutText(c.Source), State: rolloutText(c.State), Detail: rolloutText(c.Detail)})
	}
	if deployment == nil {
		s.addCoverage("Deployment", inspect.ObservationUnknown, "No Deployment object obtained")
		return s
	}
	s.Identity = rolloutIdentity(deployment, contextName, "apps/v1/deployments")
	if deployment.GetAPIVersion() != rolloutAppsAPI || deployment.GetKind() != rolloutDeployment {
		s.addCoverage("Deployment", inspect.ObservationUnknown, "Only native apps/v1 Deployments are supported")
		return s
	}
	s.DeploymentState = inspect.ObservationComplete
	s.Generation = rolloutNumber(deployment.Object, "metadata", "generation")
	s.ObservedGeneration = rolloutNumber(deployment.Object, "status", "observedGeneration")
	s.Desired = rolloutNumber(deployment.Object, "spec", "replicas")
	s.Updated = rolloutNumber(deployment.Object, "status", "updatedReplicas")
	s.Ready = rolloutNumber(deployment.Object, "status", "readyReplicas")
	s.Available = rolloutNumber(deployment.Object, "status", "availableReplicas")
	s.Replicas = rolloutNumber(deployment.Object, "status", "replicas")
	s.Paused, _, _ = unstructured.NestedBool(deployment.Object, "spec", "paused")
	s.Conditions = rolloutConditions(deployment)
	s.DeploymentTemplate = rolloutTemplateObservation(deployment, s.Identity, at, false)
	if s.Identity.UID == "" {
		s.addCoverage("identity", inspect.ObservationUnknown, "Deployment UID missing; continuity and descendant ownership cannot be established")
		return s
	}
	s.addRevisions(deployment, replicaSets, contextName)
	s.addPods(pods, contextName)
	return s
}

// Progress describes the selected Deployment's retained status. It does not
// establish Pod health, service availability, historical progress or the
// outcome of any accepted write. Child coverage stays independently visible.
func (s *RolloutSnapshot) Progress() (state, reason string) {
	if s == nil || s.DeploymentState != inspect.ObservationComplete || s.Identity.UID == "" {
		return RolloutUnknown, "Deployment identity or API observation is unavailable"
	}
	if s.Paused {
		return RolloutPaused, "Deployment spec explicitly pauses rollout progression"
	}
	if s.Generation == nil || *s.Generation <= 0 || s.ObservedGeneration == nil {
		return RolloutUnknown, "Resource generation or controller observedGeneration is missing"
	}
	if *s.ObservedGeneration < *s.Generation {
		return RolloutProgressing, "Controller has not observed the current generation; older conditions are not a current verdict"
	}
	if *s.ObservedGeneration > *s.Generation {
		return RolloutUnknown, "Controller observedGeneration exceeds the resource generation; retained status is inconsistent"
	}
	for _, c := range s.Conditions {
		if c.Type == "Progressing" && c.Status == "False" || c.Type == "ReplicaFailure" && c.Status == "True" {
			reason := c.Reason
			if reason == "" {
				reason = c.Type + "=" + c.Status
			}
			return RolloutBlocked, reason
		}
	}
	for _, count := range []*int64{s.Desired, s.Updated, s.Ready, s.Available, s.Replicas} {
		if count == nil {
			return RolloutUnknown, "One or more desired/updated/ready/available/total replica counts are missing"
		}
	}
	if *s.Updated == *s.Desired && *s.Replicas == *s.Desired && *s.Ready >= *s.Desired && *s.Available >= *s.Desired {
		return RolloutComplete, "Deployment status reports its current generation available; child evidence has separate coverage"
	}
	if *s.Replicas > *s.Updated {
		return RolloutProgressing, "Retained status still reports replicas outside the updated template"
	}
	return RolloutProgressing, "Current generation is observed; updated and available replica counts have not converged"
}

func (s *RolloutSnapshot) addRevisions(deployment *unstructured.Unstructured, objects []*unstructured.Unstructured, contextName string) {
	if len(objects) > rolloutInputLimit {
		s.addCoverage("ReplicaSets", inspect.ObservationIncomplete, "Input exceeded the bounded collection limit")
	}
	for _, object := range objects[:min(len(objects), rolloutInputLimit)] {
		if !rolloutNativeChild(object, rolloutAppsAPI, rolloutReplicaSet, deployment.GetNamespace()) ||
			!rolloutOwnedBy(object, rolloutDeployment, string(deployment.GetUID())) {
			continue
		}
		if object.GetUID() == "" {
			s.addCoverage("ReplicaSets", inspect.ObservationIncomplete, "An owned ReplicaSet lacked a UID; it cannot be a recovery target")
			continue
		}
		identity := rolloutIdentity(object, contextName, "apps/v1/replicasets")
		r := RolloutRevision{Identity: identity, Revision: rolloutText(object.GetAnnotations()["deployment.kubernetes.io/revision"]),
			Replicas: rolloutNumber(object.Object, "status", "replicas"), Ready: rolloutNumber(object.Object, "status", "readyReplicas"),
			Available: rolloutNumber(object.Object, "status", "availableReplicas"), Template: rolloutTemplateObservation(object, identity, s.CapturedAt, true)}
		r.Current, r.CurrentKnown = rolloutTemplatesMatch(deployment, object)
		s.Revisions = append(s.Revisions, r)
	}
	sort.Slice(s.Revisions, func(i, j int) bool { return s.Revisions[i].Identity.Name < s.Revisions[j].Identity.Name })
	if len(s.Revisions) > MaxRolloutRevisions {
		s.Revisions = s.Revisions[:MaxRolloutRevisions]
		s.addCoverage("ReplicaSets", inspect.ObservationIncomplete, fmt.Sprintf("Retained at most %d UID-owned ReplicaSets", MaxRolloutRevisions))
	}
	s.reportTemplateMatching()
}

func (s *RolloutSnapshot) reportTemplateMatching() {
	matches, unknown := 0, 0
	for index := range s.Revisions {
		revision := &s.Revisions[index]
		if !revision.CurrentKnown {
			unknown++
		} else if revision.Current {
			matches++
		}
	}
	state := inspect.ObservationComplete
	detail := fmt.Sprintf("%d exact API template matches among %d retained UID-owned ReplicaSets; matches describe template equivalence only",
		matches, len(s.Revisions))
	if matches == 0 {
		detail += "; current revision remains unknown: defaults/admission/labels/history or collection gaps may differ"
	} else if matches > 1 {
		detail += "; multiple matches cannot choose the controller-selected revision"
	}
	if unknown > 0 {
		state = inspect.ObservationIncomplete
		detail += fmt.Sprintf("; %d template comparisons were unavailable", unknown)
	} else if len(s.Revisions) == 0 {
		state = inspect.ObservationUnknown
	}
	s.addCoverage("Template matching", state, detail)
	s.addCoverage("Current revision", inspect.ObservationUnknown,
		"Controller-selected current ReplicaSet is not established by template equality, revision number or recency")
}

func (s *RolloutSnapshot) addPods(objects []*unstructured.Unstructured, contextName string) {
	if len(objects) > rolloutInputLimit {
		s.addCoverage("Pods", inspect.ObservationIncomplete, "Input exceeded the bounded collection limit")
	}
	owners := make(map[string]struct{}, len(s.Revisions))
	for index := range s.Revisions {
		r := &s.Revisions[index]
		owners[r.Identity.UID] = struct{}{}
	}
	for _, object := range objects[:min(len(objects), rolloutInputLimit)] {
		if !rolloutNativeChild(object, "v1", "Pod", s.Identity.Namespace) {
			continue
		}
		owner := ""
		for _, ref := range object.GetOwnerReferences() {
			if rolloutController(ref, rolloutReplicaSet) {
				if _, found := owners[string(ref.UID)]; found {
					owner = string(ref.UID)
					break
				}
			}
		}
		if owner == "" {
			continue
		}
		if object.GetUID() == "" {
			s.addCoverage("Pods", inspect.ObservationIncomplete, "An owned Pod lacked a UID; its identity cannot be retained")
			continue
		}
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		s.Pods = append(s.Pods, RolloutPod{Identity: rolloutIdentity(object, contextName, "v1/pods"), ReplicaSetUID: owner,
			Phase: rolloutText(phase), Images: rolloutImages(object)})
	}
	sort.Slice(s.Pods, func(i, j int) bool { return s.Pods[i].Identity.Name < s.Pods[j].Identity.Name })
	if len(s.Pods) > MaxRolloutPods {
		s.Pods = s.Pods[:MaxRolloutPods]
		s.addCoverage("Pods", inspect.ObservationIncomplete, fmt.Sprintf("Retained at most %d UID-owned Pods", MaxRolloutPods))
	}
}

// RolloutRecoveryPreview compares the exact selected revision's retained,
// sanitized template projection. It is not a submitted rollback, a server
// dry-run or a complete reconstruction of kubectl's annotation changes.
func RolloutRecoveryPreview(s *RolloutSnapshot, revisionUID string) RolloutRecovery {
	preview := RolloutRecovery{State: RolloutNotExecuted, Reason: "No rollback submitted", Limits: []string{
		"Read-only pod-template comparison; Deployment annotation replacement is not modeled",
		"Admission, defaulting, apply conflicts and controller outcomes were not evaluated",
		"Sensitive fields, args and commands are redacted; omitted differences remain unreviewed",
	}}
	if s == nil || s.Identity.UID == "" || revisionUID == "" {
		preview.Reason = "Selected Deployment and revision UIDs are required; no rollback submitted"
		return preview
	}
	for index := range s.Revisions {
		revision := &s.Revisions[index]
		if revision.Identity.UID != revisionUID {
			continue
		}
		preview.RevisionIdentity, preview.Revision = revision.Identity, revision.Revision
		preview.Comparison = inspect.Compare(s.DeploymentTemplate, revision.Template, false)
		if !preview.Comparison.Comparable {
			preview.Reason = "Template evidence is incomplete; no rollback submitted"
		}
		return preview
	}
	preview.Reason = "Revision is outside the retained UID-owned set; no rollback submitted"
	return preview
}

func rolloutConditions(object *unstructured.Unstructured) []RolloutCondition {
	rows, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	conditions := make([]RolloutCondition, 0, min(len(rows), rolloutStatusLimit))
	for _, raw := range rows[:min(len(rows), rolloutStatusLimit)] {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		conditions = append(conditions, RolloutCondition{Type: rolloutField(row, "type"), Status: rolloutField(row, "status"),
			Reason: rolloutField(row, "reason"), Message: rolloutField(row, "message"),
			UpdatedAt: rolloutTime(row, "lastUpdateTime"), TransitionAt: rolloutTime(row, "lastTransitionTime")})
	}
	return conditions
}

func rolloutImages(object *unstructured.Unstructured) []RolloutContainerImage {
	var images []RolloutContainerImage
	for _, group := range []struct{ spec, status, role string }{
		{"containers", "containerStatuses", "app"}, {"initContainers", "initContainerStatuses", "init"},
		{"ephemeralContainers", "ephemeralContainerStatuses", "ephemeral"},
	} {
		statuses := make(map[string]map[string]any)
		rows, _, _ := unstructured.NestedSlice(object.Object, "status", group.status)
		for _, raw := range rows[:min(len(rows), rolloutStatusLimit)] {
			row, ok := raw.(map[string]any)
			if ok {
				statuses[rolloutField(row, "name")] = row
			}
		}
		containers, _, _ := unstructured.NestedSlice(object.Object, "spec", group.spec)
		for _, raw := range containers[:min(len(containers), rolloutStatusLimit)] {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			image := RolloutContainerImage{Name: rolloutField(row, "name"), Role: group.role, Declared: rolloutField(row, "image"), State: RolloutUnknown}
			if group.role == "init" && rolloutField(row, "restartPolicy") == "Always" {
				image.Role = "sidecar"
			}
			if status, found := statuses[image.Name]; found {
				rolloutImageStatus(&image, status)
			}
			images = append(images, image)
		}
	}
	return images
}

func rolloutImageStatus(image *RolloutContainerImage, status map[string]any) {
	image.ImageID = rolloutField(status, "imageID")
	image.Restarts = rolloutNumber(status, "restartCount")
	if ready, found := status["ready"].(bool); found {
		image.Ready = &ready
	}
	for _, state := range []string{"waiting", "running", "terminated"} {
		if raw, found, _ := unstructured.NestedMap(status, "state", state); found {
			image.State, image.Reason = state, rolloutField(raw, "reason")
			return
		}
	}
}

//nolint:gocritic // The template observation captures its independent source identity by value.
func rolloutTemplateObservation(object *unstructured.Unstructured, identity inspect.ResourceIdentity, at time.Time, removeHash bool) inspect.Observation {
	template, found, _ := unstructured.NestedMap(object.Object, "spec", "template")
	observation := inspect.Observation{Identity: identity, Source: "Kubernetes API /spec/template", ObservedAt: at, State: inspect.ObservationUnknown}
	if !found {
		observation.Reason = "Pod template not obtained"
		return inspect.SafeObservation(observation)
	}
	if removeHash {
		rolloutRemoveHash(template)
	}
	observation.State, observation.Object = inspect.ObservationComplete, map[string]any{"spec": map[string]any{"template": template}}
	return inspect.SafeObservation(observation)
}

func rolloutTemplatesMatch(deployment, revision *unstructured.Unstructured) (matches, known bool) {
	a, foundA, _ := unstructured.NestedMap(deployment.Object, "spec", "template")
	b, foundB, _ := unstructured.NestedMap(revision.Object, "spec", "template")
	if !foundA || !foundB {
		return false, false
	}
	rolloutRemoveHash(a)
	rolloutRemoveHash(b)
	return reflect.DeepEqual(a, b), true
}

func rolloutRemoveHash(template map[string]any) {
	labels, found, _ := unstructured.NestedMap(template, "metadata", "labels")
	if found {
		delete(labels, rolloutTemplateHash)
		if len(labels) == 0 {
			unstructured.RemoveNestedField(template, "metadata", "labels")
		} else {
			_ = unstructured.SetNestedMap(template, labels, "metadata", "labels")
		}
	}
	if metadata, found, _ := unstructured.NestedMap(template, "metadata"); found && len(metadata) == 0 {
		unstructured.RemoveNestedField(template, "metadata")
	}
}

func rolloutOwnedBy(object *unstructured.Unstructured, kind, uid string) bool {
	if uid == "" {
		return false
	}
	for _, ref := range object.GetOwnerReferences() {
		if rolloutController(ref, kind) && string(ref.UID) == uid {
			return true
		}
	}
	return false
}

//nolint:gocritic // Owner identity is checked as a value without retaining mutable API records.
func rolloutController(ref metav1.OwnerReference, kind string) bool {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	return err == nil && gv.Group == "apps" && ref.Controller != nil && *ref.Controller && ref.Kind == kind && ref.UID != ""
}

func rolloutNativeChild(object *unstructured.Unstructured, apiVersion, kind, namespace string) bool {
	return object != nil && object.GetAPIVersion() == apiVersion && object.GetKind() == kind && object.GetNamespace() == namespace
}

func rolloutIdentity(object *unstructured.Unstructured, contextName, gvr string) inspect.ResourceIdentity {
	return inspect.ResourceIdentity{Context: rolloutText(contextName), GVR: gvr, Namespace: rolloutText(object.GetNamespace()),
		Name: rolloutText(object.GetName()), UID: rolloutText(string(object.GetUID()))}
}

func rolloutNumber(object map[string]any, fields ...string) *int64 {
	value, found, err := unstructured.NestedInt64(object, fields...)
	if err != nil || !found || value < 0 {
		return nil
	}
	return &value
}

func rolloutText(value string) string {
	if len(value) > 4096 {
		value = value[:4096]
	}
	return logstream.SafeText(value)
}

func rolloutField(object map[string]any, name string) string {
	value, _ := object[name].(string)
	return rolloutText(value)
}
func rolloutTime(object map[string]any, name string) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, rolloutField(object, name))
	return at
}

func (s *RolloutSnapshot) addCoverage(source, state, detail string) {
	s.Coverage = append(s.Coverage, RolloutCoverage{Source: source, State: state, Detail: detail})
}
