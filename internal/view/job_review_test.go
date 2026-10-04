// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const jobReviewTestUID = "cron-backup-current"

func jobReviewTestSource() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]any{"namespace": "apps", "name": "backup", "uid": jobReviewTestUID},
		"spec": map[string]any{"schedule": "30 1 * * *", "timeZone": "America/New_York", "suspend": true,
			"concurrencyPolicy": "Forbid", "startingDeadlineSeconds": int64(60), "successfulJobsHistoryLimit": int64(2), "failedJobsHistoryLimit": int64(1)},
		"status": map[string]any{"lastScheduleTime": "2026-10-04T05:30:00Z"}}}
}

func jobReviewTestRun(name, uid, owner string) *unstructured.Unstructured {
	controller := true
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"namespace": "apps", "name": name, "uid": uid, "creationTimestamp": "2026-10-04T05:30:01Z",
			"annotations": map[string]any{"batch.kubernetes.io/cronjob-scheduled-timestamp": "2026-10-04T05:30:00Z"}},
		"spec": map[string]any{"completions": int64(1), "activeDeadlineSeconds": int64(300), "backoffLimit": int64(3)},
		"status": map[string]any{"active": int64(0), "failed": int64(3), "startTime": "2026-10-04T05:30:03Z",
			"conditions": []any{map[string]any{"type": "Failed", "status": "True", "reason": "DeadlineExceeded",
				"message": "full source [red] reason remains literal", "lastTransitionTime": "2026-10-04T05:35:03Z"}}}}}
	o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", UID: types.UID(owner), Controller: &controller}})
	return o
}

func jobReviewTestPod(name, owner string) *unstructured.Unstructured {
	controller := true
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"namespace": "apps", "name": name, "uid": name + "-uid"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "backup", "image": "backup:v2"}}},
		"status": map[string]any{"phase": "Failed", "containerStatuses": []any{map[string]any{"name": "backup", "ready": false,
			"restartCount": int64(3), "imageID": "containerd://sha256:123", "state": map[string]any{"terminated": map[string]any{"reason": "Error"}}}}}}}
	o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: types.UID(owner), Controller: &controller}})
	return o
}

func TestJobReviewCollectorScopesNamespaceAndVerifiesOwnership(t *testing.T) {
	source := jobReviewTestSource()
	good := jobReviewTestRun("backup-current", "job-current", jobReviewTestUID)
	old := jobReviewTestRun("backup-old-owner", "job-previous", "cron-previous")
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		client.JobGVR.GVR(): "JobList", client.PodGVR.GVR(): "PodList",
	}, source, good, old, jobReviewTestPod("backup-current-pod", "job-current"), jobReviewTestPod("backup-old-pod", "job-previous"))
	target := SelectedResourceTarget{Context: "dev", GVR: client.CjGVR, Namespace: "apps", Name: "backup", UID: jobReviewTestUID}
	snapshot, err := loadJobReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
	require.NoError(t, err)
	require.Len(t, snapshot.Runs, 1)
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, "job-current", snapshot.Runs[0].Identity.UID)
	require.Equal(t, "backup-current-pod", snapshot.Pods[0].Identity.Name)
	require.Equal(t, "2026-10-04T05:30:00Z", snapshot.Runs[0].ScheduledAt.Format(time.RFC3339))
	require.True(t, *snapshot.Schedule.Suspended)
	require.Contains(t, strings.Join(snapshot.Schedule.Preview.Assumptions, " "), "conditional on resuming")
	for _, action := range dyn.Actions() {
		require.Equal(t, "apps", action.GetNamespace())
		require.Contains(t, []string{"get", "list"}, action.GetVerb(), "review performs no writes")
	}
}

func TestJobReviewForbiddenHistoryRetainsScheduleWithoutInventingMissedRuns(t *testing.T) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		client.JobGVR.GVR(): "JobList", client.PodGVR.GVR(): "PodList",
	}, jobReviewTestSource())
	dyn.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "", fmt.Errorf("not authorized"))
	})
	target := SelectedResourceTarget{Context: "dev", GVR: client.CjGVR, Namespace: "apps", Name: "backup", UID: jobReviewTestUID}
	snapshot, err := loadJobReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
	require.NoError(t, err)
	require.Empty(t, snapshot.Runs)
	require.NotNil(t, snapshot.Schedule)
	require.Equal(t, inspect.ObservationDenied, snapshot.Coverage[1].State)
	require.Equal(t, inspect.ObservationUnknown, snapshot.Coverage[2].State)
	text := jobReviewOverview(snapshot, 76, 16)
	require.Contains(t, text, "No verified retained Job")
	require.Contains(t, text, "Jobs: denied")
	require.NotContains(t, text, "missed execution detected")
	for _, action := range dyn.Actions() {
		require.NotEqual(t, "pods", action.GetResource().Resource, "no Pod read without verified Job UIDs")
	}
}

func TestJobReviewCollectorRejectsRecreationAndCapsIgnoredServerLimits(t *testing.T) {
	source := jobReviewTestSource()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{client.JobGVR.GVR(): "JobList"}, source)
	target := SelectedResourceTarget{Context: "dev", GVR: client.CjGVR, Namespace: "apps", Name: "backup", UID: "previous-source-uid"}
	_, err := loadJobReview(t.Context(), inspectionConnection{dynamic: dyn}, target)
	require.ErrorContains(t, err, "identity changed")
	require.Len(t, dyn.Actions(), 1, "recreated source cannot start descendant reads")
	dyn.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		for index := range review.JobReviewCandidateCap + 3 {
			list.Items = append(list.Items, *jobReviewTestRun(fmt.Sprintf("backup-%04d", index), fmt.Sprintf("run-%04d", index), jobReviewTestUID))
		}
		return true, list, nil
	})
	objects, coverage := collectJobReviewChildren(t.Context(), dyn, client.JobGVR.GVR(), "apps", "Jobs",
		func(object *unstructured.Unstructured) bool {
			return review.JobReviewOwnedBy(object, "CronJob", jobReviewTestUID)
		})
	require.Len(t, objects, review.JobReviewCandidateCap)
	require.Equal(t, inspect.ObservationIncomplete, coverage.State)
	require.Contains(t, coverage.Detail, "800 candidates")
}

func jobReviewViewFixture(t *testing.T) *jobReviewView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	app.Config.K9s.UI.NoIcons = true
	v := &jobReviewView{Details: NewDetails(app, jobReviewTitle, "apps/backup", contentInspection, true),
		destinationRevision: app.Config.DestinationRevision(),
		target:              SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.CjGVR, Namespace: "apps", Name: "backup", UID: jobReviewTestUID}}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(review.NewJobReviewSnapshot(jobReviewTestSource(), []*unstructured.Unstructured{
		jobReviewTestRun("backup-current", "job-current", jobReviewTestUID), jobReviewTestRun("京都-long-job-name-e\u0301", "job-other", jobReviewTestUID),
	}, []*unstructured.Unstructured{jobReviewTestPod("backup-pod", "job-current")}, []review.RolloutCoverage{
		{Source: "Jobs", State: inspect.ObservationComplete}, {Source: "Pods", State: inspect.ObservationDenied, Detail: "Partial retained Pod visibility"},
	}, v.target.Context, time.Now().Add(-18*time.Second)), nil)
	return v
}

func TestJobReviewNativeFramesAndMinimumRetainMeaning(t *testing.T) {
	v := jobReviewViewFixture(t)
	require.NoError(t, v.app.Styles.Load("../../skins/monochrome.yaml", false))
	v.StylesChanged(v.app.Styles)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		text := drawnText(t, v, size[0], size[1])
		for _, want := range []string{"[1 Overview]", "failed", "DeadlineExceeded", "suspended", "Pods: denied", "? help"} {
			require.Contains(t, text, want, "%dx%d", size[0], size[1])
		}
		if dir := os.Getenv("K9S_CAPTURE_JOB_REVIEW_DIR"); dir != "" {
			// #nosec G703 -- Only the trusted local fixture capture harness supplies this directory.
			require.NoError(t, os.MkdirAll(dir, 0750))
			// #nosec G703 -- The filename contains only test dimensions, never cluster input.
			require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("job-review-%dx%d.txt", size[0], size[1])), []byte(text), 0600))
		}
	}
	v.selectTab(1)
	text := drawnText(t, v, 40, 12)
	require.Contains(t, text, "DeadlineExceeded")
	require.Contains(t, text, "failed")
	require.Contains(t, text, "j/k")
	text = drawnText(t, v, 39, 12)
	require.Contains(t, text, "Need 40x12")
	require.Equal(t, 1, v.activeTab)
	text = drawnText(t, v, 60, 24)
	require.Contains(t, text, "[2 Runs]")
}

func TestJobReviewTabsFailedRefreshReturnAndInputPreserveRetainedState(t *testing.T) {
	v := jobReviewViewFixture(t)
	reads := 0
	v.loader = func(context.Context, SelectedResourceTarget) (*review.JobReviewSnapshot, error) {
		reads++
		return nil, fmt.Errorf("unexpected read")
	}
	v.cmdBuff.SetText("retained", "", true)
	v.BufferCompleted("retained", "")
	v.text.ScrollTo(3, 1)
	v.selectTab(1)
	v.moveSelection(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	selected := v.selectedRunUID
	v.selectTab(0)
	require.Equal(t, "retained", v.inspectionQuery)
	row, col := v.text.GetScrollOffset()
	require.Equal(t, 3, row)
	require.Equal(t, 1, col)
	prior := v.snapshot
	v.acceptSnapshot(nil, fmt.Errorf("refresh denied"))
	require.Same(t, prior, v.snapshot)
	require.Contains(t, v.identityBar.GetText(true), "Refresh failed")
	v.Stop()
	v.Start()
	require.Same(t, prior, v.snapshot)
	require.Equal(t, selected, v.selectedRunUID)
	require.Equal(t, 0, reads)
	v.cmdBuff.SetActive(true)
	event := tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone)
	action, ok := v.actions.Get(ui.Key2)
	require.True(t, ok)
	require.Same(t, event, action.Action(event), "prompt input retains key precedence")
	v.selectTab(2)
	require.NoError(t, v.SelectedResource().Err())
	require.Equal(t, client.PodGVR, v.SelectedResource().GVR)
	v.snapshot.Pods = nil
	require.Error(t, v.SelectedResource().Err(), "empty Pod tab cannot silently select the source Job")
}

func TestJobReviewRefreshInvalidatesDisappearedSelectionAndDestination(t *testing.T) {
	v := jobReviewViewFixture(t)
	v.selectTab(1)
	v.moveSelection(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	priorUID := v.selectedRunUID
	next := review.NewJobReviewSnapshot(jobReviewTestSource(), []*unstructured.Unstructured{
		jobReviewTestRun("replacement", "new-run-uid", jobReviewTestUID),
	}, nil, nil, v.target.Context, time.Now())
	v.acceptSnapshot(next, nil)
	require.NotEqual(t, priorUID, v.selectedRunUID)
	require.Contains(t, v.selectionNotice, "UID no longer retained")
	require.Contains(t, v.identityBar.GetText(true), "UID no longer retained")
	v.destinationRevision++
	reads := 0
	v.loader = func(context.Context, SelectedResourceTarget) (*review.JobReviewSnapshot, error) {
		reads++
		return nil, nil
	}
	v.refresh()
	require.Zero(t, reads, "changed destination cannot refresh through the old client")
	require.Contains(t, v.podLogsReason(), "Destination changed")
	require.Same(t, next, v.snapshot)
}
