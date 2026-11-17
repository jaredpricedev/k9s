// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package problems

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/render"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var resources = map[string]schema.GroupVersionResource{
	"Pod":                     {Version: "v1", Resource: "pods"},
	"Deployment":              {Group: "apps", Version: "v1", Resource: "deployments"},
	"ReplicaSet":              {Group: "apps", Version: "v1", Resource: "replicasets"},
	"StatefulSet":             {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"DaemonSet":               {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"Job":                     {Group: "batch", Version: "v1", Resource: "jobs"},
	"CronJob":                 {Group: "batch", Version: "v1", Resource: "cronjobs"},
	"PersistentVolumeClaim":   {Version: "v1", Resource: "persistentvolumeclaims"},
	"PersistentVolume":        {Version: "v1", Resource: "persistentvolumes"},
	"Node":                    {Version: "v1", Resource: "nodes"},
	"Event":                   {Version: "v1", Resource: "events"},
	"Service":                 {Version: "v1", Resource: "services"},
	"ConfigMap":               {Version: "v1", Resource: "configmaps"},
	"Secret":                  {Version: "v1", Resource: "secrets"},
	"Ingress":                 {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	"HorizontalPodAutoscaler": {Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
}

// finding accumulates status evidence for one resource. The first phrase added
// at the worst severity leads the cause; nothing is inferred beyond the status.
type finding struct {
	severity Severity
	parts    []string
	since    time.Time
	weak     bool
	found    bool
}

func (f *finding) add(severity Severity, phrase string, since time.Time) {
	if !f.found || severity < f.severity {
		f.severity, f.since = severity, since
		f.parts = append([]string{phrase}, f.parts...)
	} else {
		f.parts = append(f.parts, phrase)
	}
	f.found = true
}

// classify returns nil when the status reports nothing that is failing,
// degraded or unfinished. That is "no problem reported", not proof of health.
func classify(kind string, u *unstructured.Unstructured) *Row {
	var f finding
	switch kind {
	case "Pod":
		classifyPod(u, &f)
	case "Deployment":
		classifyDeployment(u, &f)
	case "StatefulSet":
		classifyStatefulSet(u, &f)
	case "DaemonSet":
		classifyDaemonSet(u, &f)
	case "Job":
		classifyJob(u, &f)
	case "PersistentVolumeClaim":
		classifyClaim(u, &f)
	case "Node":
		classifyNode(u, &f)
	}
	if !f.found {
		return nil
	}
	return &Row{Severity: f.severity, Kind: kind, Resource: resources[kind], Namespace: u.GetNamespace(), Name: u.GetName(),
		UID: u.GetUID(), Cause: strings.Join(f.parts, " · "), Since: f.since, own: len(f.parts), weak: f.weak}
}

var podFailureWords = []string{"fail", "err", "backoff", "crash", "oom", "evict", "unknown", "exitcode", "signal", "invalid", "lost", "deadline"}

func classifyPod(u *unstructured.Unstructured, f *finding) {
	var pod v1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &pod); err != nil {
		f.add(Degraded, "status could not be decoded", time.Time{})
		f.weak = true
		return
	}
	st := &pod.Status
	if st.Phase == v1.PodSucceeded {
		return
	}
	// The same status word and cause phrase as the Pod list's STATUS and WHY.
	status := new(render.Pod).Phase(pod.DeletionTimestamp, &pod.Spec, st)
	why := render.PodWhy(&pod.Spec, st)
	scheduled, ready := podCondition(st, v1.PodScheduled), podCondition(st, v1.PodReady)
	var since time.Time
	if scheduled != nil && scheduled.Status == v1.ConditionFalse {
		since = scheduled.LastTransitionTime.Time
	} else if ready != nil && ready.Status != v1.ConditionTrue {
		since = ready.LastTransitionTime.Time
	}
	word := strings.ToLower(status)
	severity, problem := Degraded, true
	switch {
	case containsAny(word, podFailureWords):
		severity = Failing
	case pod.DeletionTimestamp != nil, strings.Contains(word, "creating"), strings.Contains(word, "initializ"), strings.HasPrefix(word, "init:"):
		severity = Progressing
	case st.Phase == v1.PodPending:
	case st.Phase == v1.PodRunning:
		problem = ready != nil && ready.Status != v1.ConditionTrue
		for i := range st.ContainerStatuses {
			problem = problem || !st.ContainerStatuses[i].Ready
		}
		if problem && why == "" && ready != nil {
			why = join(ready.Reason, ready.Message)
		}
	default:
		problem = false
	}
	if !problem {
		return
	}
	f.weak = why == ""
	f.add(severity, strings.TrimSuffix(status+" · "+why, " · "), since)
}

func podCondition(st *v1.PodStatus, kind v1.PodConditionType) *v1.PodCondition {
	for i := range st.Conditions {
		if st.Conditions[i].Type == kind {
			return &st.Conditions[i]
		}
	}
	return nil
}

func classifyDeployment(u *unstructured.Unstructured, f *finding) {
	if c := condition(u, "Progressing", "False"); c != nil {
		f.add(Failing, conditionPhrase(c, "Progressing=False"), transition(c))
	}
	if c := condition(u, "ReplicaFailure", "True"); c != nil {
		f.add(Failing, conditionPhrase(c, "ReplicaFailure"), transition(c))
	}
	desired := number(u, 1, "spec", "replicas")
	available := number(u, 0, "status", "availableReplicas")
	if available < desired {
		severity := Degraded
		if available == 0 {
			severity = Failing
		}
		f.add(severity, fmt.Sprintf("%d of %d available", available, desired), transition(condition(u, "Available", "False")))
		return
	}
	paused, _, _ := unstructured.NestedBool(u.Object, "spec", "paused")
	if updated := number(u, 0, "status", "updatedReplicas"); !paused && (updated < desired || unobserved(u)) {
		f.add(Progressing, fmt.Sprintf("rollout: %d of %d updated", updated, desired), time.Time{})
	}
}

func classifyStatefulSet(u *unstructured.Unstructured, f *finding) {
	desired := number(u, 1, "spec", "replicas")
	ready := number(u, 0, "status", "readyReplicas")
	if ready < desired {
		severity := Degraded
		if ready == 0 {
			severity = Failing
		}
		f.add(severity, fmt.Sprintf("%d of %d ready", ready, desired), time.Time{})
		return
	}
	current, _, _ := unstructured.NestedString(u.Object, "status", "currentRevision")
	update, _, _ := unstructured.NestedString(u.Object, "status", "updateRevision")
	if updated := number(u, 0, "status", "updatedReplicas"); (update != "" && current != update && updated < desired) || unobserved(u) {
		f.add(Progressing, fmt.Sprintf("rollout: %d of %d updated", updated, desired), time.Time{})
	}
}

func classifyDaemonSet(u *unstructured.Unstructured, f *finding) {
	desired := number(u, 0, "status", "desiredNumberScheduled")
	ready := number(u, 0, "status", "numberReady")
	if ready < desired {
		severity := Degraded
		if ready == 0 {
			severity = Failing
		}
		f.add(severity, fmt.Sprintf("%d of %d ready", ready, desired), time.Time{})
	}
	if stray := number(u, 0, "status", "numberMisscheduled"); stray > 0 {
		f.add(Degraded, fmt.Sprintf("%d misscheduled", stray), time.Time{})
	}
	if updated := number(u, 0, "status", "updatedNumberScheduled"); !f.found && (updated < desired || unobserved(u)) {
		f.add(Progressing, fmt.Sprintf("rollout: %d of %d updated", updated, desired), time.Time{})
	}
}

func classifyJob(u *unstructured.Unstructured, f *finding) {
	for _, kind := range []string{"Failed", "FailureTarget"} {
		if c := condition(u, kind, "True"); c != nil {
			f.add(Failing, conditionPhrase(c, "Failed"), transition(c))
			return
		}
	}
	if failed := number(u, 0, "status", "failed"); failed > 0 && condition(u, "Complete", "True") == nil {
		f.add(Degraded, plural(int(failed), "failed pod")+"; not marked Failed", time.Time{})
	}
}

func classifyClaim(u *unstructured.Unstructured, f *finding) {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	switch phase {
	case "Lost":
		f.add(Failing, "Lost", time.Time{})
		f.weak = true
	case "Pending":
		// Pending is a claim's first phase and is never re-entered, so creation
		// time is when this state began.
		f.add(Degraded, "Pending", u.GetCreationTimestamp().Time)
		f.weak = true
	}
	for _, kind := range []string{"Resizing", "FileSystemResizePending"} {
		if c := condition(u, kind, "True"); c != nil {
			f.add(Progressing, conditionPhrase(c, kind), transition(c))
		}
	}
}

func classifyNode(u *unstructured.Unstructured, f *finding) {
	ready := condition(u, "Ready", "")
	switch status, _ := ready["status"].(string); {
	case ready == nil:
		f.add(Degraded, "no Ready condition reported", time.Time{})
		f.weak = true
	case status == "False":
		f.add(Failing, "NotReady · "+conditionPhrase(ready, "Ready=False"), transition(ready))
	case status != "True":
		f.add(Failing, "Ready unknown · "+conditionPhrase(ready, "Ready=Unknown"), transition(ready))
	}
	for _, kind := range []string{"MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable"} {
		if c := condition(u, kind, "True"); c != nil {
			f.add(Degraded, kind+" · "+conditionPhrase(c, "True"), transition(c))
		}
	}
}

// condition returns the named condition; an empty status matches any status.
func condition(u *unstructured.Unstructured, kind, status string) map[string]any {
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if ok && c["type"] == kind && (status == "" || c["status"] == status) {
			return c
		}
	}
	return nil
}

func conditionPhrase(c map[string]any, fallback string) string {
	reason, _ := c["reason"].(string)
	message, _ := c["message"].(string)
	if phrase := join(reason, message); phrase != "" {
		return phrase
	}
	return fallback
}

func join(reason, message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if reason == "" || message == "" {
		return reason + message
	}
	return reason + ": " + message
}

func transition(c map[string]any) time.Time {
	if c == nil {
		return time.Time{}
	}
	for _, field := range []string{"lastTransitionTime", "lastUpdateTime"} {
		if raw, ok := c[field].(string); ok {
			if at, err := time.Parse(time.RFC3339, raw); err == nil {
				return at
			}
		}
	}
	return time.Time{}
}

func number(u *unstructured.Unstructured, fallback int64, path ...string) int64 {
	value, found, err := unstructured.NestedFieldNoCopy(u.Object, path...)
	if !found || err != nil {
		return fallback
	}
	switch n := value.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return fallback
}

func unobserved(u *unstructured.Unstructured) bool {
	observed, found, _ := unstructured.NestedFieldNoCopy(u.Object, "status", "observedGeneration")
	return found && observed != nil && number(u, 0, "status", "observedGeneration") < u.GetGeneration()
}

func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}
