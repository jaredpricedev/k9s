// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"sort"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	MaxJobReviewRuns      = 64
	MaxJobReviewPods      = 64
	JobReviewCandidateCap = 800
	jobReviewBatchAPI     = "batch/v1"
	jobReviewKind         = "Job"
	jobReviewCronKind     = "CronJob"
	JobOutcomeRunning     = "running"
	JobOutcomeFailed      = "failed"
	JobOutcomeSucceeded   = "succeeded"
	JobOutcomeSuspended   = "suspended"
	jobScheduledTimestamp = "batch.kubernetes.io/cronjob-scheduled-timestamp"
)

// JobReviewSnapshot retains bounded projections of native source status. Lists
// and status times do not become a continuous execution or missed-run history.
type JobReviewSnapshot struct {
	Identity   inspect.ResourceIdentity
	Kind       string
	CapturedAt time.Time
	Schedule   *JobSchedule
	Runs       []JobRun
	Pods       []JobReviewPod
	Coverage   []RolloutCoverage
}

type JobSchedule struct {
	Expression, TimeZone, Concurrency             string
	Suspended                                     *bool
	StartingDeadline, JobDeadline, HistorySuccess *int64
	HistoryFailed                                 *int64
	LastScheduled, LastSuccessful                 time.Time
	Preview                                       JobSchedulePreview
}

type JobRun struct {
	Origin, CronJobName, CronJobUID                string
	Identity                                       inspect.ResourceIdentity
	CreatedAt, StartedAt, CompletedAt, ScheduledAt time.Time
	Active, Succeeded, Failed, Completions         *int64
	Deadline, BackoffLimit, TTL                    *int64
	Suspended                                      *bool
	Conditions                                     []JobRunCondition
	Images                                         []JobRunImage
}

type JobRunCondition struct {
	Type, Status, Reason, Message string
	ProbeAt, TransitionAt         time.Time
}

type JobRunImage struct{ Name, Role, Declared string }

type JobReviewPod struct {
	Identity      inspect.ResourceIdentity
	JobUID, Phase string
	Images        []RolloutContainerImage
}

func NewJobReviewSnapshot(source *unstructured.Unstructured, jobs, pods []*unstructured.Unstructured,
	coverage []RolloutCoverage, contextName string, at time.Time,
) *JobReviewSnapshot {
	s := &JobReviewSnapshot{CapturedAt: at}
	for _, item := range coverage {
		s.Coverage = append(s.Coverage, RolloutCoverage{rolloutText(item.Source), rolloutText(item.State), rolloutText(item.Detail)})
	}
	if source == nil || source.GetAPIVersion() != jobReviewBatchAPI ||
		(source.GetKind() != jobReviewKind && source.GetKind() != jobReviewCronKind) {
		s.addCoverage("source", inspect.ObservationUnknown, "Only native batch/v1 Job or CronJob sources are supported")
		return s
	}
	s.Kind = source.GetKind()
	gvr := "batch/v1/jobs"
	if s.Kind == jobReviewCronKind {
		gvr = "batch/v1/cronjobs"
		s.Schedule = projectJobSchedule(source, at)
	} else {
		jobs = []*unstructured.Unstructured{source}
	}
	s.Identity = rolloutIdentity(source, contextName, gvr)
	if s.Identity.UID == "" {
		s.addCoverage("identity", inspect.ObservationUnknown, "Source UID unavailable; child ownership cannot be established")
		return s
	}
	s.addRuns(jobs, contextName)
	s.addPods(pods, contextName)
	return s
}

func projectJobSchedule(source *unstructured.Unstructured, at time.Time) *JobSchedule {
	spec, _, _ := unstructured.NestedMap(source.Object, "spec")
	status, _, _ := unstructured.NestedMap(source.Object, "status")
	s := &JobSchedule{Expression: rolloutField(spec, "schedule"), TimeZone: rolloutField(spec, "timeZone"),
		Concurrency: rolloutField(spec, "concurrencyPolicy"), Suspended: jobBool(spec, "suspend"),
		StartingDeadline: rolloutNumber(spec, "startingDeadlineSeconds"),
		JobDeadline:      rolloutNumber(spec, "jobTemplate", "spec", "activeDeadlineSeconds"),
		HistorySuccess:   rolloutNumber(spec, "successfulJobsHistoryLimit"), HistoryFailed: rolloutNumber(spec, "failedJobsHistoryLimit"),
		LastScheduled: rolloutTime(status, "lastScheduleTime"), LastSuccessful: rolloutTime(status, "lastSuccessfulTime")}
	s.Preview = PreviewJobSchedule(s.Expression, s.TimeZone, at, s.Suspended != nil && *s.Suspended)
	return s
}

func (s *JobReviewSnapshot) addRuns(objects []*unstructured.Unstructured, contextName string) {
	candidates := make([]*unstructured.Unstructured, 0, min(len(objects), JobReviewCandidateCap))
	seen := make(map[types.UID]bool)
	for _, object := range objects[:min(len(objects), JobReviewCandidateCap)] {
		if !rolloutNativeChild(object, jobReviewBatchAPI, jobReviewKind, s.Identity.Namespace) || object.GetUID() == "" || seen[object.GetUID()] {
			continue
		}
		if s.Kind == jobReviewCronKind && !JobReviewOwnedBy(object, jobReviewCronKind, types.UID(s.Identity.UID)) {
			continue
		}
		seen[object.GetUID()] = true
		candidates = append(candidates, object)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].GetCreationTimestamp(), candidates[j].GetCreationTimestamp()
		if a.Equal(&b) {
			return candidates[i].GetName() < candidates[j].GetName()
		}
		return a.After(b.Time)
	})
	for _, object := range candidates[:min(len(candidates), MaxJobReviewRuns)] {
		s.Runs = append(s.Runs, projectJobRun(object, contextName))
	}
	if len(objects) > JobReviewCandidateCap || len(candidates) > MaxJobReviewRuns {
		s.addCoverage("Job retention", inspect.ObservationIncomplete, "Only the newest 64 verified runs among at most 800 obtained candidates are retained")
	}
}

func projectJobRun(object *unstructured.Unstructured, contextName string) JobRun {
	spec, _, _ := unstructured.NestedMap(object.Object, "spec")
	status, _, _ := unstructured.NestedMap(object.Object, "status")
	scheduled, _ := time.Parse(time.RFC3339Nano, object.GetAnnotations()[jobScheduledTimestamp])
	run := JobRun{Identity: rolloutIdentity(object, contextName, "batch/v1/jobs"), CreatedAt: object.GetCreationTimestamp().Time,
		StartedAt: rolloutTime(status, "startTime"), CompletedAt: rolloutTime(status, "completionTime"), ScheduledAt: scheduled,
		Active: rolloutNumber(status, "active"), Succeeded: rolloutNumber(status, "succeeded"), Failed: rolloutNumber(status, "failed"),
		Completions: rolloutNumber(spec, "completions"), Deadline: rolloutNumber(spec, "activeDeadlineSeconds"),
		BackoffLimit: rolloutNumber(spec, "backoffLimit"), TTL: rolloutNumber(spec, "ttlSecondsAfterFinished"), Suspended: jobBool(spec, "suspend")}
	run.Conditions = projectJobConditions(object)
	run.Origin, run.CronJobName, run.CronJobUID = jobRunOrigin(object, !scheduled.IsZero())
	for _, group := range []struct{ field, role string }{{"containers", "app"}, {"initContainers", "init"}} {
		containers, _, _ := unstructured.NestedSlice(spec, "template", "spec", group.field)
		for _, raw := range containers[:min(len(containers), 100)] {
			if container, ok := raw.(map[string]any); ok {
				run.Images = append(run.Images, JobRunImage{rolloutField(container, "name"), group.role, rolloutField(container, "image")})
			}
		}
	}
	return run
}

func jobRunOrigin(object *unstructured.Unstructured, scheduled bool) (origin, cronName, cronUID string) {
	origin = "No CronJob controller owner reported; origin unconfirmed"
	for _, owner := range object.GetOwnerReferences() {
		if owner.APIVersion == jobReviewBatchAPI && owner.Kind == jobReviewCronKind && owner.Controller != nil && *owner.Controller {
			cronName, cronUID = rolloutText(owner.Name), rolloutText(string(owner.UID))
			origin = "CronJob controller owner reference; schedule execution unconfirmed"
			break
		}
	}
	if object.GetAnnotations()["cronjob.kubernetes.io/instantiate"] == "manual" {
		origin = "Manual trigger annotation"
	} else if scheduled {
		origin = "Scheduled timestamp annotation; Job creation is observed"
	}
	return origin, cronName, cronUID
}

func projectJobConditions(object *unstructured.Unstructured) []JobRunCondition {
	rows, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	conditions := make([]JobRunCondition, 0, min(len(rows), 100))
	for _, raw := range rows[:min(len(rows), 100)] {
		if condition, ok := raw.(map[string]any); ok {
			conditions = append(conditions, JobRunCondition{Type: rolloutField(condition, "type"), Status: rolloutField(condition, "status"),
				Reason: rolloutField(condition, "reason"), Message: rolloutField(condition, "message"),
				ProbeAt: rolloutTime(condition, "lastProbeTime"), TransitionAt: rolloutTime(condition, "lastTransitionTime")})
		}
	}
	return conditions
}

// Outcome uses true terminal conditions. Failed Pod attempts alone do not
// establish a failed Job, and absent counts remain unknown rather than zero.
func (r *JobRun) Outcome() (state, reason string) {
	complete, failed := false, false
	for _, condition := range r.Conditions {
		if condition.Status != "True" {
			continue
		}
		if condition.Type == "Complete" {
			complete = true
		}
		if condition.Type == "Failed" {
			failed = true
			reason = condition.Reason
		}
	}
	if complete && failed {
		return inspect.ObservationUnknown, "Conflicting terminal conditions"
	}
	if failed {
		return JobOutcomeFailed, reason
	}
	if complete {
		return JobOutcomeSucceeded, "Complete=True"
	}
	if r.Suspended != nil && *r.Suspended {
		return JobOutcomeSuspended, "spec.suspend=true"
	}
	if r.Active != nil && *r.Active > 0 {
		return JobOutcomeRunning, "Active Pod count observed; completion unconfirmed"
	}
	return inspect.ObservationUnknown, "No terminal condition retained; outcome unconfirmed"
}

func (s *JobReviewSnapshot) addPods(objects []*unstructured.Unstructured, contextName string) {
	owners := make(map[types.UID]bool)
	for index := range s.Runs {
		owners[types.UID(s.Runs[index].Identity.UID)] = true
	}
	seen := make(map[types.UID]bool)
	for _, object := range objects[:min(len(objects), JobReviewCandidateCap)] {
		if !rolloutNativeChild(object, "v1", "Pod", s.Identity.Namespace) || object.GetUID() == "" || seen[object.GetUID()] {
			continue
		}
		for owner := range owners {
			if !JobReviewOwnedBy(object, jobReviewKind, owner) {
				continue
			}
			if len(s.Pods) == MaxJobReviewPods {
				s.addCoverage("Pod retention", inspect.ObservationIncomplete, "At most 64 controlling-Job-UID-owned Pods retained")
				return
			}
			phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
			s.Pods = append(s.Pods, JobReviewPod{Identity: rolloutIdentity(object, contextName, "v1/pods"),
				JobUID: string(owner), Phase: rolloutText(phase), Images: rolloutImages(object)})
			seen[object.GetUID()] = true
			break
		}
	}
	if len(objects) > JobReviewCandidateCap {
		s.addCoverage("Pod retention", inspect.ObservationIncomplete, "Pod candidate limit reached; additional records excluded")
	}
}

func JobReviewOwnedBy(object *unstructured.Unstructured, kind string, uid types.UID) bool {
	if object == nil || uid == "" || object.GetUID() == "" {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		version, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil && version.Group == "batch" && version.Version == "v1" && owner.Kind == kind && owner.UID == uid &&
			owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func jobBool(object map[string]any, fields ...string) *bool {
	value, found, err := unstructured.NestedBool(object, fields...)
	if !found || err != nil {
		return nil
	}
	return &value
}

func (s *JobReviewSnapshot) addCoverage(source, state, detail string) {
	s.Coverage = append(s.Coverage, RolloutCoverage{source, state, detail})
}
