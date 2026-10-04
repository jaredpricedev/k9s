// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func jobReviewFixture(kind, name, uid string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": kind,
		"metadata": map[string]any{"namespace": "apps", "name": name, "uid": uid}, "spec": map[string]any{}, "status": map[string]any{}}}
	o.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)))
	return o
}

func ownJobReviewFixture(object *unstructured.Unstructured, kind string, ownerUID types.UID) {
	controller := true
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: kind, UID: ownerUID, Controller: &controller}})
}

func TestJobReviewRetainsTerminalOutcomeWithoutTurningRetriesIntoFailure(t *testing.T) {
	job := jobReviewFixture("Job", "one-off", "job-a")
	require.NoError(t, unstructured.SetNestedField(job.Object, int64(2), "status", "failed"))
	require.NoError(t, unstructured.SetNestedField(job.Object, int64(1), "status", "active"))
	snapshot := NewJobReviewSnapshot(job, nil, nil, nil, "dev", time.Now())
	require.Len(t, snapshot.Runs, 1)
	state, reason := snapshot.Runs[0].Outcome()
	require.Equal(t, "running", state)
	require.Contains(t, reason, "completion unconfirmed")
	require.Nil(t, snapshot.Runs[0].Succeeded, "unreported count stays unknown")
	require.Equal(t, int64(2), *snapshot.Runs[0].Failed, "failed attempts are retained independently")
	require.NoError(t, unstructured.SetNestedSlice(job.Object, []any{map[string]any{"type": "Failed", "status": "True",
		"reason": "DeadlineExceeded", "message": "deadline reached", "lastTransitionTime": "2026-10-04T11:00:00Z"}}, "status", "conditions"))
	snapshot = NewJobReviewSnapshot(job, nil, nil, nil, "dev", time.Now())
	state, reason = snapshot.Runs[0].Outcome()
	require.Equal(t, "failed", state)
	require.Equal(t, "DeadlineExceeded", reason)
	require.Equal(t, "dev", snapshot.Runs[0].Identity.Context)
	require.Equal(t, "job-a", snapshot.Runs[0].Identity.UID)
	require.Equal(t, "2026-10-04T11:00:00Z", snapshot.Runs[0].Conditions[0].TransitionAt.Format(time.RFC3339))
}

func TestJobReviewCronOwnershipRetentionAndSecretExclusion(t *testing.T) {
	cronJob := jobReviewFixture("CronJob", "backup", "cron-current")
	require.NoError(t, unstructured.SetNestedField(cronJob.Object, "0 2 * * *", "spec", "schedule"))
	require.NoError(t, unstructured.SetNestedField(cronJob.Object, "Etc/UTC", "spec", "timeZone"))
	runs := make([]*unstructured.Unstructured, 0, 80)
	for index := range 70 {
		job := jobReviewFixture("Job", fmt.Sprintf("backup-%02d", index), fmt.Sprintf("run-%02d", index))
		ownJobReviewFixture(job, "CronJob", "cron-current")
		job.SetCreationTimestamp(metav1.NewTime(cronJob.GetCreationTimestamp().Add(time.Duration(index) * time.Minute)))
		require.NoError(t, unstructured.SetNestedSlice(job.Object, []any{map[string]any{"name": "backup", "image": "backup:v2",
			"env": []any{map[string]any{"name": "TOKEN", "value": "must-not-retain"}}}}, "spec", "template", "spec", "containers"))
		runs = append(runs, job)
	}
	old := jobReviewFixture("Job", "same-name-old-cron", "old-run")
	ownJobReviewFixture(old, "CronJob", "cron-previous")
	wrong := jobReviewFixture("Job", "wrong-api-owner", "wrong-run")
	ownJobReviewFixture(wrong, "CronJob", "cron-current")
	owners := wrong.GetOwnerReferences()
	owners[0].APIVersion = "example.io/v1"
	wrong.SetOwnerReferences(owners)
	runs = append(runs, old, wrong, runs[0])
	snapshot := NewJobReviewSnapshot(cronJob, runs, nil, nil, "dev", time.Now())
	require.Len(t, snapshot.Runs, MaxJobReviewRuns)
	require.Equal(t, "backup-69", snapshot.Runs[0].Identity.Name)
	require.Equal(t, "backup-06", snapshot.Runs[63].Identity.Name)
	require.Contains(t, fmt.Sprint(snapshot.Coverage), inspect.ObservationIncomplete)
	require.NotContains(t, fmt.Sprintf("%+v", snapshot), "must-not-retain")
	require.Equal(t, "backup:v2", snapshot.Runs[0].Images[0].Declared)
	for index := range snapshot.Runs {
		require.NotEqual(t, "old-run", snapshot.Runs[index].Identity.UID)
		require.NotEqual(t, "wrong-run", snapshot.Runs[index].Identity.UID)
	}
}

func TestJobSchedulePreviewTimezoneDSTAndSuspensionLimits(t *testing.T) {
	spring := PreviewJobSchedule("30 2 * * *", "America/New_York", time.Date(2026, 3, 7, 8, 0, 0, 0, time.UTC), false)
	require.Equal(t, inspect.ObservationComplete, spring.State)
	require.Len(t, spring.Times, MaxJobSchedulePreview)
	require.Equal(t, "2026-03-09T02:30:00-04:00", spring.Times[0].Format(time.RFC3339), "nonexistent spring-forward local time is skipped")
	fall := PreviewJobSchedule("30 1 * * *", "America/New_York", time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), true)
	require.Equal(t, "2026-11-01T01:30:00-04:00", fall.Times[0].Format(time.RFC3339))
	require.Equal(t, "2026-11-01T01:30:00-05:00", fall.Times[1].Format(time.RFC3339), "repeated local time retains distinct offsets")
	require.Contains(t, strings.Join(fall.Assumptions, " "), "conditional on resuming")
	require.Contains(t, strings.Join(fall.Assumptions, " "), "do not prove missed execution")
	unknown := PreviewJobSchedule("@daily", "", time.Now(), false)
	require.Equal(t, inspect.ObservationIncomplete, unknown.State)
	require.Equal(t, "UTC", unknown.TimeZone)
	require.Contains(t, unknown.Reason, "Controller timezone unknown")
	for _, pair := range [][2]string{{"bad schedule", "Etc/UTC"}, {"* * * * *", "Unknown/Zone"}, {"CRON_TZ=UTC * * * * *", "Etc/UTC"}} {
		preview := PreviewJobSchedule(pair[0], pair[1], time.Now(), false)
		require.Equal(t, inspect.ObservationUnknown, preview.State)
		require.Empty(t, preview.Times)
		require.NotEmpty(t, preview.Reason)
	}
}

func TestJobReviewPodsRequireControllingJobUID(t *testing.T) {
	job := jobReviewFixture("Job", "backup", "current-job")
	good := jobReviewFixture("Pod", "backup-current", "pod-current")
	good.SetAPIVersion("v1")
	ownJobReviewFixture(good, "Job", "current-job")
	old := good.DeepCopy()
	old.SetName("backup-old")
	old.SetUID("pod-old")
	ownJobReviewFixture(old, "Job", "replaced-job")
	snapshot := NewJobReviewSnapshot(job, nil, []*unstructured.Unstructured{good, old, good}, nil, "dev", time.Now())
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, "pod-current", snapshot.Pods[0].Identity.UID)
}

func TestJobReviewManualOriginAndSelectedJobDoNotInventParentSchedule(t *testing.T) {
	job := jobReviewFixture("Job", "manual-backup", "manual-run")
	ownJobReviewFixture(job, "CronJob", "cron-owner")
	job.SetAnnotations(map[string]string{"cronjob.kubernetes.io/instantiate": "manual"})
	require.NoError(t, unstructured.SetNestedSlice(job.Object, []any{map[string]any{"type": "Complete", "status": "True",
		"lastProbeTime": "2026-10-04T11:00:00Z", "lastTransitionTime": "2026-10-04T10:59:00Z"}}, "status", "conditions"))
	snapshot := NewJobReviewSnapshot(job, nil, nil, nil, "dev", time.Now())
	require.Nil(t, snapshot.Schedule, "selected Job source did not read its parent schedule")
	require.Equal(t, "Manual trigger annotation", snapshot.Runs[0].Origin)
	require.Equal(t, "cron-owner", snapshot.Runs[0].CronJobUID)
	require.Equal(t, "2026-10-04T11:00:00Z", snapshot.Runs[0].Conditions[0].ProbeAt.Format(time.RFC3339))
	state, _ := snapshot.Runs[0].Outcome()
	require.Equal(t, "succeeded", state)
}
