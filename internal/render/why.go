// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package render

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/model1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// The functions in this file state, in one short phrase, the evidence an
// object's own spec and status report for not being in its wanted state. They
// read nothing else: no events, no pods, no other objects. A phrase is empty
// when the object reports nothing wrong, and it never names a cause the API
// did not report. The level is the renderer's verdict for the list summary;
// LevelNone leaves that to the visible status columns.

// whyCondition joins a condition's reason and message.
func whyCondition(reason, message string) string {
	reason, message = strings.TrimSpace(reason), oneLine(message)
	switch {
	case reason == "":
		return message
	case message == "":
		return reason
	}
	return reason + ": " + message
}

// whyGeneration reports a controller that has not yet read the latest spec,
// in which case every other status field describes an older generation.
func whyGeneration(generation, observed int64) string {
	if observed == 0 || observed >= generation {
		return ""
	}
	return fmt.Sprintf("generation %d not yet observed, status is for %d", generation, observed)
}

func deploymentWhy(dp *appsv1.Deployment) (string, model1.Level) {
	var desired int32 = 1
	if dp.Spec.Replicas != nil {
		desired = *dp.Spec.Replicas
	}
	st := &dp.Status
	var unavailable string
	paused := dp.Spec.Paused
	for i := range st.Conditions {
		c := &st.Conditions[i]
		switch {
		case c.Type == appsv1.DeploymentReplicaFailure && c.Status == v1.ConditionTrue:
			return whyCondition(c.Reason, c.Message), model1.LevelFailing
		case c.Type == appsv1.DeploymentProgressing && c.Status == v1.ConditionFalse:
			return whyCondition(c.Reason, c.Message), model1.LevelFailing
		case c.Type == appsv1.DeploymentProgressing && c.Status == v1.ConditionUnknown:
			// A paused Deployment reports Unknown/DeploymentPaused. That is a
			// choice someone made, stated below, not a fault of its own.
			if paused || c.Reason == "DeploymentPaused" {
				paused = true
				continue
			}
			return whyCondition(c.Reason, c.Message), model1.LevelDegraded
		case c.Type == appsv1.DeploymentAvailable && c.Status != v1.ConditionTrue && desired > 0:
			unavailable = whyCondition(c.Reason, c.Message)
		}
	}
	if why := whyGeneration(dp.Generation, st.ObservedGeneration); why != "" {
		return why, model1.LevelProgress
	}
	if paused && st.UpdatedReplicas < desired {
		return fmt.Sprintf("rollout paused, %d of %d updated", st.UpdatedReplicas, desired), model1.LevelDegraded
	}
	if unavailable != "" {
		return unavailable, model1.LevelDegraded
	}
	if paused {
		if st.UnavailableReplicas > 0 {
			return fmt.Sprintf("paused; %d of %d unavailable", st.UnavailableReplicas, max(desired, st.Replicas)), model1.LevelDegraded
		}
		return "paused", model1.LevelNone
	}
	if st.UpdatedReplicas < desired {
		return fmt.Sprintf("rollout: %d of %d updated", st.UpdatedReplicas, desired), model1.LevelProgress
	}
	if st.UnavailableReplicas > 0 {
		return fmt.Sprintf("%d of %d unavailable", st.UnavailableReplicas, max(desired, st.Replicas)), model1.LevelDegraded
	}
	return "", model1.LevelNone
}

func statefulSetWhy(sts *appsv1.StatefulSet) (string, model1.Level) {
	var desired int32 = 1
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	st := &sts.Status
	if why := whyGeneration(sts.Generation, st.ObservedGeneration); why != "" {
		return why, model1.LevelProgress
	}
	if st.UpdateRevision != "" && st.CurrentRevision != st.UpdateRevision && st.UpdatedReplicas < desired {
		return fmt.Sprintf("rollout: %d of %d updated", st.UpdatedReplicas, desired), model1.LevelProgress
	}
	if st.Replicas < desired {
		return fmt.Sprintf("%d of %d created", st.Replicas, desired), model1.LevelDegraded
	}
	if st.ReadyReplicas < desired {
		return fmt.Sprintf("%d of %d not ready", desired-st.ReadyReplicas, desired), model1.LevelDegraded
	}
	return "", model1.LevelNone
}

func daemonSetWhy(ds *appsv1.DaemonSet) (string, model1.Level) {
	st := &ds.Status
	if why := whyGeneration(ds.Generation, st.ObservedGeneration); why != "" {
		return why, model1.LevelProgress
	}
	desired := st.DesiredNumberScheduled
	switch {
	case st.NumberMisscheduled > 0:
		return fmt.Sprintf("%d running on nodes that should not run it", st.NumberMisscheduled), model1.LevelDegraded
	case st.CurrentNumberScheduled < desired:
		return fmt.Sprintf("%d of %d not scheduled", desired-st.CurrentNumberScheduled, desired), model1.LevelDegraded
	case st.UpdatedNumberScheduled < desired:
		return fmt.Sprintf("rollout: %d of %d updated", st.UpdatedNumberScheduled, desired), model1.LevelProgress
	case st.NumberUnavailable > 0:
		return fmt.Sprintf("%d of %d unavailable", st.NumberUnavailable, desired), model1.LevelDegraded
	case st.NumberReady < desired:
		return fmt.Sprintf("%d of %d not ready", desired-st.NumberReady, desired), model1.LevelDegraded
	}
	return "", model1.LevelNone
}

func replicaSetWhy(rs *appsv1.ReplicaSet) (string, model1.Level) {
	var desired int32 = 1
	if rs.Spec.Replicas != nil {
		desired = *rs.Spec.Replicas
	}
	st := &rs.Status
	for i := range st.Conditions {
		c := &st.Conditions[i]
		if c.Type == appsv1.ReplicaSetReplicaFailure && c.Status == v1.ConditionTrue {
			return whyCondition(c.Reason, c.Message), model1.LevelFailing
		}
	}
	if why := whyGeneration(rs.Generation, st.ObservedGeneration); why != "" {
		return why, model1.LevelProgress
	}
	if st.Replicas < desired {
		return fmt.Sprintf("%d of %d created", st.Replicas, desired), model1.LevelDegraded
	}
	if st.ReadyReplicas < desired {
		return fmt.Sprintf("%d of %d not ready", desired-st.ReadyReplicas, desired), model1.LevelDegraded
	}
	return "", model1.LevelNone
}

// nodeWhy lists every condition the kubelet or a controller reports against
// the node, worst first, then whether it is cordoned.
func nodeWhy(no *v1.Node) (string, model1.Level) {
	var (
		failing, degraded []string
		level             = model1.LevelNone
	)
	for i := range no.Status.Conditions {
		c := &no.Status.Conditions[i]
		switch {
		case c.Type == v1.NodeReady && c.Status != v1.ConditionTrue:
			// STATUS already says NotReady; the message is the evidence and
			// the reason stands in only when the kubelet left none.
			why := strings.TrimSuffix(oneLine(c.Message), ".")
			if why == "" {
				why = c.Reason
			}
			if why != "" {
				failing = append([]string{why}, failing...)
			}
			level = model1.LevelFailing
		case c.Type == v1.NodeNetworkUnavailable && c.Status == v1.ConditionTrue:
			failing = append(failing, strings.TrimSuffix(whyCondition(string(c.Type), c.Message), "."))
		case c.Status == v1.ConditionTrue && strings.HasSuffix(string(c.Type), "Pressure"):
			// The kubelet's pressure messages only restate the type.
			degraded = append(degraded, string(c.Type))
		}
	}
	if len(degraded) > 0 && level == model1.LevelNone {
		level = model1.LevelDegraded
	}
	if len(failing) > 0 {
		level = model1.LevelFailing
	}
	parts := append(failing, degraded...)
	if no.Spec.Unschedulable {
		parts = append(parts, "cordoned")
	}
	return strings.Join(parts, "; "), level
}

func jobWhy(job *batchv1.Job) (string, model1.Level) {
	st := &job.Status
	var suspended bool
	for i := range st.Conditions {
		c := &st.Conditions[i]
		if c.Status != v1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			return whyCondition(c.Reason, c.Message), model1.LevelFailing
		case batchv1.JobComplete:
			return "", model1.LevelNone
		case batchv1.JobSuspended:
			suspended = true
		}
	}
	if suspended || (job.Spec.Suspend != nil && *job.Spec.Suspend) {
		return "suspended", model1.LevelNone
	}
	if st.Failed > 0 {
		if job.Spec.BackoffLimit != nil {
			return fmt.Sprintf("%s, backoff limit %d", podFailures(st.Failed), *job.Spec.BackoffLimit), model1.LevelDegraded
		}
		return podFailures(st.Failed), model1.LevelDegraded
	}
	return "", model1.LevelNone
}

// cronJobWhy can only report what a CronJob status holds: whether scheduling
// is suspended and whether the latest scheduled run has a recorded success.
// Why a run failed is on its Job.
func cronJobWhy(cj *batchv1.CronJob) (string, model1.Level) {
	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		return "suspended", model1.LevelNone
	}
	st := &cj.Status
	if len(st.Active) == 0 && st.LastScheduleTime != nil && st.LastSuccessfulTime != nil &&
		st.LastSuccessfulTime.Before(st.LastScheduleTime) {
		return "last scheduled run has no recorded success", model1.LevelDegraded
	}
	return "", model1.LevelNone
}

func pvcWhy(pvc *v1.PersistentVolumeClaim) (string, model1.Level) {
	if pvc.DeletionTimestamp != nil {
		return "", model1.LevelNone
	}
	var parts []string
	level := model1.LevelNone
	switch pvc.Status.Phase {
	case v1.ClaimLost:
		level = model1.LevelFailing
		if pvc.Spec.VolumeName != "" {
			parts = append(parts, "bound volume "+pvc.Spec.VolumeName+" is lost")
		} else {
			parts = append(parts, "bound volume is lost")
		}
	case v1.ClaimPending:
		level = model1.LevelDegraded
		class := pvc.Annotations[v1.BetaStorageClassAnnotation]
		if class == "" && pvc.Spec.StorageClassName != nil {
			class = *pvc.Spec.StorageClassName
		}
		switch {
		case pvc.Spec.VolumeName != "":
			parts = append(parts, "waiting to bind volume "+pvc.Spec.VolumeName)
		case class == "":
			parts = append(parts, "no volume bound, no storage class set")
		default:
			parts = append(parts, "no volume bound")
		}
	}
	for i := range pvc.Status.Conditions {
		c := &pvc.Status.Conditions[i]
		if c.Status != v1.ConditionTrue {
			continue
		}
		parts = append(parts, whyCondition(string(c.Type), c.Message))
		if strings.HasSuffix(string(c.Type), "Error") || strings.HasSuffix(string(c.Type), "Infeasible") {
			level = max(level, model1.LevelDegraded)
		}
	}
	return strings.Join(parts, "; "), level
}

func serviceWhy(svc *v1.Service) (string, model1.Level) {
	if svc.Spec.Type == v1.ServiceTypeLoadBalancer && len(svc.Status.LoadBalancer.Ingress) == 0 {
		return "no load balancer ingress assigned", model1.LevelDegraded
	}
	return "", model1.LevelNone
}

func podFailures(n int32) string {
	if n == 1 {
		return "1 pod failure"
	}
	return fmt.Sprintf("%d pod failures", n)
}

// StatusWhy returns the phrase and level the list for kind shows in its WHY
// column, for callers that hold the object as read rather than as a list row
// (the :problems list). ok is false when the kind has no WHY column or the
// object could not be decoded; Pods have PodWhy.
func StatusWhy(kind string, u *unstructured.Unstructured) (why string, level model1.Level, ok bool) {
	decode := func(into any) bool {
		return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into) == nil
	}
	switch kind {
	case "Deployment":
		var o appsv1.Deployment
		if ok = decode(&o); ok {
			why, level = deploymentWhy(&o)
		}
	case "StatefulSet":
		var o appsv1.StatefulSet
		if ok = decode(&o); ok {
			why, level = statefulSetWhy(&o)
		}
	case "DaemonSet":
		var o appsv1.DaemonSet
		if ok = decode(&o); ok {
			why, level = daemonSetWhy(&o)
		}
	case "ReplicaSet":
		var o appsv1.ReplicaSet
		if ok = decode(&o); ok {
			why, level = replicaSetWhy(&o)
		}
	case "Job":
		var o batchv1.Job
		if ok = decode(&o); ok {
			why, level = jobWhy(&o)
		}
	case "CronJob":
		var o batchv1.CronJob
		if ok = decode(&o); ok {
			why, level = cronJobWhy(&o)
		}
	case "PersistentVolumeClaim":
		var o v1.PersistentVolumeClaim
		if ok = decode(&o); ok {
			why, level = pvcWhy(&o)
		}
	case "Node":
		var o v1.Node
		if ok = decode(&o); ok {
			why, level = nodeWhy(&o)
		}
	case "Service":
		var o v1.Service
		if ok = decode(&o); ok {
			why, level = serviceWhy(&o)
		}
	}
	return why, level, ok
}
