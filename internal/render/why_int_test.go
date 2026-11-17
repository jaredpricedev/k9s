// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package render

import (
	"testing"
	"time"

	"github.com/derailed/k9s/internal/model1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func int32p(n int32) *int32 { return &n }
func boolp(b bool) *bool    { return &b }

func TestDeploymentWhy(t *testing.T) {
	cond := func(kind appsv1.DeploymentConditionType, status v1.ConditionStatus, reason, message string) appsv1.DeploymentCondition {
		return appsv1.DeploymentCondition{Type: kind, Status: status, Reason: reason, Message: message}
	}
	uu := map[string]struct {
		dp    appsv1.Deployment
		why   string
		level model1.Level
	}{
		"healthy": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(2)}, Status: appsv1.DeploymentStatus{
				Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2,
				Conditions: []appsv1.DeploymentCondition{
					cond(appsv1.DeploymentAvailable, v1.ConditionTrue, "MinimumReplicasAvailable", "ok"),
					cond(appsv1.DeploymentProgressing, v1.ConditionTrue, "NewReplicaSetAvailable", "ok")}}},
		},
		"scaled-to-zero": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(0)}, Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{cond(appsv1.DeploymentAvailable, v1.ConditionFalse, "MinimumReplicasUnavailable", "none")}}},
		},
		"deadline": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(2)}, Status: appsv1.DeploymentStatus{
				Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1, UnavailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					cond(appsv1.DeploymentAvailable, v1.ConditionFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
					cond(appsv1.DeploymentProgressing, v1.ConditionFalse, "ProgressDeadlineExceeded", "ReplicaSet \"api-5d\" has timed out\n progressing.")}}},
			why:   `ProgressDeadlineExceeded: ReplicaSet "api-5d" has timed out progressing.`,
			level: model1.LevelFailing,
		},
		"replica-failure": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(2)}, Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{
					cond(appsv1.DeploymentProgressing, v1.ConditionTrue, "ReplicaSetUpdated", "busy"),
					cond(appsv1.DeploymentReplicaFailure, v1.ConditionTrue, "FailedCreate", "pods \"api-\" is forbidden: exceeded quota")}}},
			why:   `FailedCreate: pods "api-" is forbidden: exceeded quota`,
			level: model1.LevelFailing,
		},
		"unobserved": {
			dp: appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Generation: 4}, Spec: appsv1.DeploymentSpec{Replicas: int32p(2)},
				Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}},
			why:   "generation 4 not yet observed, status is for 3",
			level: model1.LevelProgress,
		},
		"unavailable": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(3)}, Status: appsv1.DeploymentStatus{
				Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 0, UnavailableReplicas: 3,
				Conditions: []appsv1.DeploymentCondition{
					cond(appsv1.DeploymentAvailable, v1.ConditionFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
					cond(appsv1.DeploymentProgressing, v1.ConditionTrue, "ReplicaSetUpdated", "busy")}}},
			why:   "MinimumReplicasUnavailable: Deployment does not have minimum availability.",
			level: model1.LevelDegraded,
		},
		"rolling": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(3)}, Status: appsv1.DeploymentStatus{
				Replicas: 4, UpdatedReplicas: 1, AvailableReplicas: 3}},
			why:   "rollout: 1 of 3 updated",
			level: model1.LevelProgress,
		},
		"paused": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(3), Paused: true}, Status: appsv1.DeploymentStatus{
				Replicas: 3, UpdatedReplicas: 1, AvailableReplicas: 3}},
			why:   "rollout paused, 1 of 3 updated",
			level: model1.LevelDegraded,
		},
		"some-unavailable": {
			dp: appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32p(3)}, Status: appsv1.DeploymentStatus{
				Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 2, UnavailableReplicas: 1}},
			why:   "1 of 3 unavailable",
			level: model1.LevelDegraded,
		},
	}
	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			why, level := deploymentWhy(&u.dp)
			assert.Equal(t, u.why, why)
			assert.Equal(t, u.level, level)
		})
	}
}

func TestControllerWhy(t *testing.T) {
	why, level := statefulSetWhy(&appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: int32p(3)},
		Status: appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3}})
	assert.Empty(t, why)
	assert.Equal(t, model1.LevelNone, level)

	why, level = statefulSetWhy(&appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: int32p(3)},
		Status: appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 2, UpdatedReplicas: 1, CurrentRevision: "a", UpdateRevision: "b"}})
	assert.Equal(t, "rollout: 1 of 3 updated", why)
	assert.Equal(t, model1.LevelProgress, level)

	why, level = statefulSetWhy(&appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: int32p(3)},
		Status: appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 1, UpdatedReplicas: 3}})
	assert.Equal(t, "2 of 3 not ready", why)
	assert.Equal(t, model1.LevelDegraded, level)

	why, level = daemonSetWhy(&appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{
		DesiredNumberScheduled: 3, CurrentNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberReady: 3, NumberAvailable: 3}})
	assert.Empty(t, why)
	assert.Equal(t, model1.LevelNone, level)

	why, level = daemonSetWhy(&appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{
		DesiredNumberScheduled: 3, CurrentNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberReady: 2, NumberUnavailable: 1}})
	assert.Equal(t, "1 of 3 unavailable", why)
	assert.Equal(t, model1.LevelDegraded, level)

	why, _ = daemonSetWhy(&appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, CurrentNumberScheduled: 2,
		UpdatedNumberScheduled: 2, NumberReady: 2, NumberMisscheduled: 1}})
	assert.Equal(t, "1 running on nodes that should not run it", why)

	why, level = replicaSetWhy(&appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Replicas: int32p(0)}})
	assert.Empty(t, why, "a scaled-down ReplicaSet reports nothing")
	assert.Equal(t, model1.LevelNone, level)

	why, level = replicaSetWhy(&appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Replicas: int32p(2)}, Status: appsv1.ReplicaSetStatus{
		Conditions: []appsv1.ReplicaSetCondition{{Type: appsv1.ReplicaSetReplicaFailure, Status: v1.ConditionTrue,
			Reason: "FailedCreate", Message: "exceeded quota: compute"}}}})
	assert.Equal(t, "FailedCreate: exceeded quota: compute", why)
	assert.Equal(t, model1.LevelFailing, level)

	why, level = replicaSetWhy(&appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Replicas: int32p(2)},
		Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 1}})
	assert.Equal(t, "1 of 2 not ready", why)
	assert.Equal(t, model1.LevelDegraded, level)
}

func TestNodeWhy(t *testing.T) {
	cond := func(kind v1.NodeConditionType, status v1.ConditionStatus, reason, message string) v1.NodeCondition {
		return v1.NodeCondition{Type: kind, Status: status, Reason: reason, Message: message}
	}
	ready := cond(v1.NodeReady, v1.ConditionTrue, "KubeletReady", "kubelet is posting ready status")
	uu := map[string]struct {
		no    v1.Node
		why   string
		level model1.Level
	}{
		"healthy": {no: v1.Node{Status: v1.NodeStatus{Conditions: []v1.NodeCondition{
			cond(v1.NodeMemoryPressure, v1.ConditionFalse, "KubeletHasSufficientMemory", "ok"), ready}}}},
		"no-conditions": {no: v1.Node{}},
		"not-ready": {
			no: v1.Node{Status: v1.NodeStatus{Conditions: []v1.NodeCondition{
				cond(v1.NodeReady, v1.ConditionFalse, "KubeletNotReady", "container runtime network not ready: cni plugin not initialized")}}},
			why:   "container runtime network not ready: cni plugin not initialized",
			level: model1.LevelFailing,
		},
		"unknown-and-cordoned": {
			no: v1.Node{Spec: v1.NodeSpec{Unschedulable: true}, Status: v1.NodeStatus{Conditions: []v1.NodeCondition{
				cond(v1.NodeMemoryPressure, v1.ConditionUnknown, "NodeStatusUnknown", "Kubelet stopped posting node status."),
				cond(v1.NodeReady, v1.ConditionUnknown, "NodeStatusUnknown", "Kubelet stopped posting node status.")}}},
			why:   "Kubelet stopped posting node status; cordoned",
			level: model1.LevelFailing,
		},
		"not-ready-without-message": {
			no:    v1.Node{Status: v1.NodeStatus{Conditions: []v1.NodeCondition{cond(v1.NodeReady, v1.ConditionFalse, "KubeletNotReady", "")}}},
			why:   "KubeletNotReady",
			level: model1.LevelFailing,
		},
		"pressure": {
			no: v1.Node{Status: v1.NodeStatus{Conditions: []v1.NodeCondition{
				cond(v1.NodeMemoryPressure, v1.ConditionTrue, "KubeletHasInsufficientMemory", "kubelet has insufficient memory available"),
				cond(v1.NodeDiskPressure, v1.ConditionTrue, "KubeletHasDiskPressure", "kubelet has disk pressure"),
				cond(v1.NodePIDPressure, v1.ConditionFalse, "KubeletHasSufficientPID", "ok"), ready}}},
			why:   "MemoryPressure; DiskPressure",
			level: model1.LevelDegraded,
		},
		"network": {
			no: v1.Node{Status: v1.NodeStatus{Conditions: []v1.NodeCondition{
				cond(v1.NodeNetworkUnavailable, v1.ConditionTrue, "NoRouteCreated", "Node created without a route"), ready}}},
			why:   "NetworkUnavailable: Node created without a route",
			level: model1.LevelFailing,
		},
		"cordoned": {
			no:  v1.Node{Spec: v1.NodeSpec{Unschedulable: true}, Status: v1.NodeStatus{Conditions: []v1.NodeCondition{ready}}},
			why: "cordoned",
		},
	}
	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			why, level := nodeWhy(&u.no)
			assert.Equal(t, u.why, why)
			assert.Equal(t, u.level, level)
		})
	}
}

func TestJobAndCronJobWhy(t *testing.T) {
	why, level := jobWhy(&batchv1.Job{Status: batchv1.JobStatus{Failed: 1, Succeeded: 1, Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: v1.ConditionTrue}}}})
	assert.Empty(t, why, "a completed job is not explained by its earlier retries")
	assert.Equal(t, model1.LevelNone, level)

	why, level = jobWhy(&batchv1.Job{Status: batchv1.JobStatus{Failed: 3, Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: v1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"}}}})
	assert.Equal(t, "BackoffLimitExceeded: Job has reached the specified backoff limit", why)
	assert.Equal(t, model1.LevelFailing, level)

	why, level = jobWhy(&batchv1.Job{Spec: batchv1.JobSpec{BackoffLimit: int32p(6)}, Status: batchv1.JobStatus{Active: 1, Failed: 2}})
	assert.Equal(t, "2 pod failures, backoff limit 6", why)
	assert.Equal(t, model1.LevelDegraded, level)

	why, level = jobWhy(&batchv1.Job{Spec: batchv1.JobSpec{Suspend: boolp(true)}})
	assert.Equal(t, "suspended", why)
	assert.Equal(t, model1.LevelNone, level, "a suspended job is stated, not counted as a problem")

	why, level = jobWhy(&batchv1.Job{Status: batchv1.JobStatus{Active: 1}})
	assert.Empty(t, why)
	assert.Equal(t, model1.LevelNone, level)

	now := metav1.Now()
	earlier := metav1.NewTime(now.Add(-time.Hour))
	why, _ = cronJobWhy(&batchv1.CronJob{Spec: batchv1.CronJobSpec{Suspend: boolp(true)}})
	assert.Equal(t, "suspended", why)
	why, level = cronJobWhy(&batchv1.CronJob{Status: batchv1.CronJobStatus{LastScheduleTime: &now, LastSuccessfulTime: &earlier}})
	assert.Equal(t, "last scheduled run has no recorded success", why)
	assert.Equal(t, model1.LevelDegraded, level)
	why, _ = cronJobWhy(&batchv1.CronJob{Status: batchv1.CronJobStatus{LastScheduleTime: &now, LastSuccessfulTime: &earlier,
		Active: []v1.ObjectReference{{Name: "run"}}}})
	assert.Empty(t, why, "a run that is still active has not failed")
	why, _ = cronJobWhy(&batchv1.CronJob{Status: batchv1.CronJobStatus{LastScheduleTime: &now}})
	assert.Empty(t, why, "a missing success time is unknown, not a failure")
	why, _ = cronJobWhy(&batchv1.CronJob{Status: batchv1.CronJobStatus{LastScheduleTime: &earlier, LastSuccessfulTime: &now}})
	assert.Empty(t, why)
}

func TestPVCAndServiceWhy(t *testing.T) {
	class := "fast-ssd"
	why, level := pvcWhy(&v1.PersistentVolumeClaim{Spec: v1.PersistentVolumeClaimSpec{VolumeName: "pv-1"},
		Status: v1.PersistentVolumeClaimStatus{Phase: v1.ClaimBound}})
	assert.Empty(t, why)
	assert.Equal(t, model1.LevelNone, level)

	why, level = pvcWhy(&v1.PersistentVolumeClaim{Spec: v1.PersistentVolumeClaimSpec{StorageClassName: &class},
		Status: v1.PersistentVolumeClaimStatus{Phase: v1.ClaimPending}})
	assert.Equal(t, "no volume bound", why, "the class has its own column")
	assert.Equal(t, model1.LevelDegraded, level)

	why, _ = pvcWhy(&v1.PersistentVolumeClaim{Status: v1.PersistentVolumeClaimStatus{Phase: v1.ClaimPending}})
	assert.Equal(t, "no volume bound, no storage class set", why)

	why, level = pvcWhy(&v1.PersistentVolumeClaim{Spec: v1.PersistentVolumeClaimSpec{VolumeName: "pv-9"},
		Status: v1.PersistentVolumeClaimStatus{Phase: v1.ClaimLost}})
	assert.Equal(t, "bound volume pv-9 is lost", why)
	assert.Equal(t, model1.LevelFailing, level)

	why, level = pvcWhy(&v1.PersistentVolumeClaim{Spec: v1.PersistentVolumeClaimSpec{VolumeName: "pv-1"},
		Status: v1.PersistentVolumeClaimStatus{Phase: v1.ClaimBound, Conditions: []v1.PersistentVolumeClaimCondition{
			{Type: v1.PersistentVolumeClaimFileSystemResizePending, Status: v1.ConditionTrue,
				Message: "Waiting for user to (re-)start a pod to finish file system resize of volume on node."}}}})
	assert.Equal(t, "FileSystemResizePending: Waiting for user to (re-)start a pod to finish file system resize of volume on node.", why)
	assert.Equal(t, model1.LevelNone, level)

	why, level = serviceWhy(&v1.Service{Spec: v1.ServiceSpec{Type: v1.ServiceTypeLoadBalancer}})
	assert.Equal(t, "no load balancer ingress assigned", why)
	assert.Equal(t, model1.LevelDegraded, level)
	why, _ = serviceWhy(&v1.Service{Spec: v1.ServiceSpec{Type: v1.ServiceTypeLoadBalancer},
		Status: v1.ServiceStatus{LoadBalancer: v1.LoadBalancerStatus{Ingress: []v1.LoadBalancerIngress{{IP: "203.0.113.7"}}}}})
	assert.Empty(t, why)
	why, _ = serviceWhy(&v1.Service{Spec: v1.ServiceSpec{Type: v1.ServiceTypeClusterIP}})
	assert.Empty(t, why)
}

// The phrase is a column named WHY in every list, and the verdict travels on
// the row rather than in a field.
func TestWhyColumnIsRenderedBesideTheVerdict(t *testing.T) {
	dp := appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "api"},
		Spec:       appsv1.DeploymentSpec{Replicas: int32p(2)},
		Status: appsv1.DeploymentStatus{Replicas: 2, Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentProgressing, Status: v1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "timed out"}}},
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&dp)
	require.NoError(t, err)
	var (
		re  Deployment
		row model1.Row
	)
	require.NoError(t, re.Render(&unstructured.Unstructured{Object: raw}, "", &row))
	index, ok := re.Header("").IndexOf("WHY", true)
	require.True(t, ok)
	assert.Equal(t, "ProgressDeadlineExceeded: timed out", row.Fields[index])
	assert.Equal(t, model1.LevelFailing, row.Level)
	assert.Equal(t, model1.LevelFailing, row.Clone().Level)
	assert.Equal(t, model1.LevelFailing, row.Customize([]int{0, 1}).Level, "custom columns keep the verdict")

	for name, header := range map[string]model1.Header{
		"sts": defaultSTSHeader, "ds": defaultDSHeader, "rs": defaultRSHeader, "node": defaultNOHeader,
		"job": defaultJOBHeader, "cronjob": defaultCJHeader, "pvc": defaultPVCHeader, "svc": defaultSVCHeader,
	} {
		index, ok := header.IndexOf("WHY", true)
		require.True(t, ok, name)
		assert.False(t, header[index].Wide, name)
	}
}
