// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package activity retains explicitly scoped observations, never inferred
// continuous application history or hidden background collection.
package activity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/workspace"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	MaxFacts           = 160
	KindPod            = "Pod"
	KindJob            = "Job"
	KindCronJob        = "CronJob"
	KindDeployment     = "Deployment"
	KindHelmRelease    = "HelmRelease"
	KindApplication    = "Application"
	GroupDeclaredImage = "Declared image reference"
	GroupRuntimeImage  = "Resolved runtime image ID"
	EventGVR           = "v1/events"
)

type Fact struct {
	Group, Field, Value string
	SourceAt            time.Time
}

// Source excludes raw specs/env/Secret values. Image declarations, resolved
// runtime IDs, chart versions, app versions and controller revisions are kept
// separately, with API source times distinct from local observation time.
type Source struct {
	Identity                     inspect.ResourceIdentity
	Kind, ResourceVersion, State string
	Generation                   *int64
	Coverage                     workspace.Coverage
	CapturedAt, CreatedAt        time.Time
	Facts                        []Fact
	Job                          *review.JobRun
}

func Project(resource *workspace.Resource, contextName string, at time.Time) *Source {
	object := resource.Object
	if object == nil || resource.Ref.UID == "" || object.GetUID() == "" || object.GetName() != resource.Ref.Name ||
		object.GetNamespace() != resource.Ref.Namespace || string(object.GetUID()) != resource.Ref.UID {
		return nil
	}
	gvr, kind, err := workspace.ResolveKind(resource.Ref.GVR)
	if resource.Ref.GVR == "argoproj.io/v1alpha1/applications" {
		kind = KindApplication
	}
	apiVersion := gvr.Version
	if gvr.Group != "" {
		apiVersion = gvr.Group + "/" + gvr.Version
	}
	if err != nil || object.GetAPIVersion() != apiVersion || object.GetKind() != kind || strings.EqualFold(kind, "Secret") {
		return nil
	}
	s := &Source{Identity: inspect.ResourceIdentity{Context: contextName, GVR: resource.Ref.GVR, Namespace: resource.Ref.Namespace,
		Name: resource.Ref.Name, UID: resource.Ref.UID},
		Kind: kind, ResourceVersion: safe(object.GetResourceVersion()), State: inspect.ObservationComplete, CapturedAt: at, CreatedAt: object.GetCreationTimestamp().Time}
	if generation, found, _ := unstructured.NestedInt64(object.Object, "metadata", "generation"); found {
		s.Generation = &generation
		s.add("Resource generation", "metadata generation", fmt.Sprint(generation), time.Time{})
	}
	s.projectImages(object)
	s.projectStatus(object)
	s.projectRevisions(object)
	if kind == KindJob {
		job := review.NewJobReviewSnapshot(object, nil, nil, nil, contextName, at)
		if len(job.Runs) > 0 {
			s.Job = &job.Runs[0]
			outcome, reason := s.Job.Outcome()
			s.add("Job outcome", "terminal outcome", outcome+" · "+reason, s.Job.CompletedAt)
			s.add("Job source", "trigger evidence", s.Job.Origin, s.Job.ScheduledAt)
			for _, f := range []struct {
				field string
				value *int64
			}{
				{"active Pods", s.Job.Active}, {"succeeded Pods", s.Job.Succeeded}, {"failed Pod attempts", s.Job.Failed},
				{"deadline seconds", s.Job.Deadline}, {"backoff limit", s.Job.BackoffLimit}, {"TTL seconds", s.Job.TTL},
			} {
				if f.value != nil {
					s.add("Job status", f.field, fmt.Sprint(*f.value), time.Time{})
				}
			}
		}
	}
	sort.Slice(s.Facts, func(i, j int) bool { return factKey(&s.Facts[i]) < factKey(&s.Facts[j]) })
	return s
}

func (s *Source) add(group, field, value string, at time.Time) {
	if len(s.Facts) >= MaxFacts {
		s.State = inspect.ObservationIncomplete
		return
	}
	s.Facts = append(s.Facts, Fact{group, field, safe(value), at})
}

func (s *Source) addPath(object *unstructured.Unstructured, group, field string, path ...string) {
	value, found, _ := unstructured.NestedFieldNoCopy(object.Object, path...)
	if !found {
		return
	}
	switch scalar := value.(type) {
	case string:
		s.add(group, field, scalar, time.Time{})
	case int64:
		s.add(group, field, fmt.Sprint(scalar), time.Time{})
	case bool:
		s.add(group, field, fmt.Sprint(scalar), time.Time{})
	}
}

func (s *Source) projectImages(object *unstructured.Unstructured) {
	path := []string{"spec"}
	if s.Kind != KindPod {
		switch s.Kind {
		case KindDeployment, "StatefulSet", "DaemonSet", "ReplicaSet", KindJob:
			path = []string{"spec", "template", "spec"}
		case KindCronJob:
			path = []string{"spec", "jobTemplate", "spec", "template", "spec"}
		default:
			return
		}
	}
	for _, group := range []struct{ field, role string }{{"containers", "app"}, {"initContainers", "init"}, {"ephemeralContainers", "ephemeral"}} {
		containers, _, _ := unstructured.NestedSlice(object.Object, append(path, group.field)...)
		if len(containers) > 64 {
			s.State = inspect.ObservationIncomplete
		}
		for _, raw := range containers[:min(64, len(containers))] {
			container, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := container["name"].(string)
			image, _ := container["image"].(string)
			if name != "" && image != "" {
				s.add(GroupDeclaredImage, group.role+"/"+safe(name), image, time.Time{})
			}
		}
	}
	if s.Kind != KindPod {
		return
	}
	for _, group := range []struct{ field, role string }{{"containerStatuses", "app"}, {"initContainerStatuses", "init"}, {"ephemeralContainerStatuses", "ephemeral"}} {
		statuses, _, _ := unstructured.NestedSlice(object.Object, "status", group.field)
		if len(statuses) > 64 {
			s.State = inspect.ObservationIncomplete
		}
		for _, raw := range statuses[:min(64, len(statuses))] {
			status, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := status["name"].(string)
			imageID, _ := status["imageID"].(string)
			if name != "" && imageID != "" {
				s.add(GroupRuntimeImage, group.role+"/"+safe(name), imageID, time.Time{})
			}
		}
	}
}

func (s *Source) projectStatus(object *unstructured.Unstructured) {
	group := "Reported controller status"
	switch s.Kind {
	case KindDeployment, "StatefulSet", "DaemonSet", "ReplicaSet":
		group = "Rollout progress"
	case "Kustomization", KindHelmRelease, "GitRepository", "HelmRepository", "OCIRepository", KindApplication:
		group = "Reconciliation"
	case KindJob, KindCronJob:
		group = "Job status"
	}
	for _, field := range []string{
		"observedGeneration", "replicas", "readyReplicas", "availableReplicas", "updatedReplicas", "unavailableReplicas",
		"desiredNumberScheduled", "numberAvailable", "phase",
	} {
		s.addPath(object, group, field, "status", field)
	}
	s.addPath(object, group, "desired replicas", "spec", "replicas")
	s.addPath(object, group, "suspended", "spec", "suspend")
	s.addPath(object, group, "paused", "spec", "paused")
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	if len(conditions) > 32 {
		s.State = inspect.ObservationIncomplete
	}
	for _, raw := range conditions[:min(32, len(conditions))] {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := c["type"].(string)
		status, _ := c["status"].(string)
		reason, _ := c["reason"].(string)
		if typ == "" || status == "" {
			continue
		}
		value := status
		if reason != "" {
			value += " · " + reason
		}
		observed, hasObserved := c["observedGeneration"].(int64)
		if !hasObserved {
			observed, hasObserved, _ = unstructured.NestedInt64(object.Object, "status", "observedGeneration")
		}
		if hasObserved && s.Generation != nil && observed < *s.Generation {
			value += " (older observed generation)"
		}
		transition, _ := c["lastTransitionTime"].(string)
		at, _ := time.Parse(time.RFC3339Nano, transition)
		s.add(group, "condition/"+safe(typ), value, at)
	}
}

func (s *Source) projectRevisions(object *unstructured.Unstructured) {
	if s.Kind == KindDeployment {
		if revision := object.GetAnnotations()["deployment.kubernetes.io/revision"]; revision != "" {
			s.add("Rollout revision", "Deployment annotation", revision, time.Time{})
		}
	}
	for _, field := range []string{"currentRevision", "updateRevision"} {
		s.addPath(object, "Rollout revision", field, "status", field)
	}
	for _, field := range []string{"lastAppliedRevision", "lastAttemptedRevision", "lastReleaseRevision", "lastHandledReconcileAt"} {
		s.addPath(object, "Controller revision", field, "status", field)
	}
	s.addPath(object, "Source revision", "artifact revision", "status", "artifact", "revision")
	s.addPath(object, "Source artifact digest", "artifact digest", "status", "artifact", "digest")
	if s.Kind == KindCronJob {
		for _, field := range []string{"schedule", "timeZone", "concurrencyPolicy", "startingDeadlineSeconds"} {
			s.addPath(object, "Schedule source", field, "spec", field)
		}
		for _, field := range []string{"lastScheduleTime", "lastSuccessfulTime"} {
			s.addPath(object, "Schedule API timestamp", field, "status", field)
		}
	}
	if s.Kind == KindApplication {
		s.addPath(object, "Source revision", "Argo sync revision", "status", "sync", "revision")
		s.addPath(object, "Reconciliation", "Argo sync status", "status", "sync", "status")
		s.addPath(object, "Reconciliation", "Argo health status", "status", "health", "status")
		s.addPath(object, "Reconciliation", "Argo operation phase", "status", "operationState", "phase")
		s.addPath(object, "Chart name", "Argo source chart", "spec", "source", "chart")
		s.addPath(object, "Source target revision", "Argo declared target", "spec", "source", "targetRevision")
	}
	if s.Kind != KindHelmRelease {
		return
	}
	history, _, _ := unstructured.NestedSlice(object.Object, "status", "history")
	if len(history) == 0 {
		return
	}
	entry, ok := history[0].(map[string]any)
	if !ok {
		return
	}
	for _, field := range []struct{ group, name string }{
		{"Chart name", "chartName"}, {"Chart version", "chartVersion"}, {"Application version", "appVersion"},
		{"Helm release revision", "version"}, {"Helm history status", "status"}, {"Helm artifact digest", "digest"}, {"Helm config digest", "configDigest"},
	} {
		value, found := entry[field.name]
		if !found {
			continue
		}
		switch value.(type) {
		case string, int64:
			s.add(field.group, "latest reported history entry", fmt.Sprint(value), time.Time{})
		}
	}
}

func factKey(f *Fact) string { return f.Group + "\x00" + f.Field }
func safe(value string) string {
	text := logstream.SafeText(value)
	runes := []rune(text)
	if len(runes) > 4096 {
		return string(runes[:4095]) + "…"
	}
	return text
}
