// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package problems

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/model1"
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
	"Namespace":               {Version: "v1", Resource: "namespaces"},
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

// The cause phrases below are the ones each kind's list shows in its WHY
// column (render.StatusWhy), so a resource reads the same in both places.
// What this list adds is what it lacks columns for: the count or state that
// leads the cause, how bad it ranks, and since when.

var restatedCount = regexp.MustCompile(`^\d+ of \d+ (unavailable|not ready)$`)

// why appends the kind's WHY phrase. A phrase that only restates the count
// already leading the cause is dropped. It reports false when the status
// could not be decoded.
func (f *finding) why(kind string, u *unstructured.Unstructured, fallback string, since time.Time) bool {
	phrase, level, ok := render.StatusWhy(kind, u)
	if !ok {
		f.add(Degraded, "status could not be decoded", time.Time{})
		f.weak = true
		return false
	}
	severity := Progressing
	switch level {
	case model1.LevelNone:
		return true
	case model1.LevelFailing:
		severity = Failing
	case model1.LevelDegraded:
		severity = Degraded
	}
	if phrase == "" {
		phrase = fallback
	}
	if phrase == "" || (f.found && restatedCount.MatchString(phrase)) {
		return true
	}
	if severity != Failing {
		since = time.Time{}
	}
	f.add(severity, phrase, since)
	return true
}

// short leads the cause with a count the row has no column for; none ready
// ranks as failing.
func (f *finding) short(have, want int64, word string, since time.Time) {
	if have >= want {
		return
	}
	severity := Degraded
	if have == 0 {
		severity = Failing
	}
	f.add(severity, fmt.Sprintf("%d of %d %s", have, want, word), since)
}

func classifyDeployment(u *unstructured.Unstructured, f *finding) {
	f.short(number(u, 0, "status", "availableReplicas"), number(u, 1, "spec", "replicas"), "available",
		transition(condition(u, "Available", "False")))
	stalled := condition(u, "ReplicaFailure", "True")
	if stalled == nil {
		stalled = condition(u, "Progressing", "False")
	}
	f.why("Deployment", u, "Progressing=False", transition(stalled))
}

func classifyStatefulSet(u *unstructured.Unstructured, f *finding) {
	f.short(number(u, 0, "status", "readyReplicas"), number(u, 1, "spec", "replicas"), "ready", time.Time{})
	f.why("StatefulSet", u, "", time.Time{})
}

func classifyDaemonSet(u *unstructured.Unstructured, f *finding) {
	f.short(number(u, 0, "status", "numberReady"), number(u, 0, "status", "desiredNumberScheduled"), "ready", time.Time{})
	f.why("DaemonSet", u, "", time.Time{})
}

func classifyJob(u *unstructured.Unstructured, f *finding) {
	failed := condition(u, "Failed", "True")
	if failed == nil {
		failed = condition(u, "FailureTarget", "True")
	}
	f.why("Job", u, "Failed", transition(failed))
}

func classifyClaim(u *unstructured.Unstructured, f *finding) {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	phrase, level, ok := render.StatusWhy("PersistentVolumeClaim", u)
	switch {
	case !ok:
		f.why("PersistentVolumeClaim", u, "", time.Time{})
	case phase == "Lost":
		f.add(Failing, phrase, time.Time{})
		f.weak = true
	case phase == "Pending" && phrase != "":
		// Pending is a claim's first phase and is never re-entered, so creation
		// time is when this state began. The phrase says what is missing, not
		// why, so a Warning event may still be quoted beside it.
		f.add(Degraded, phrase, u.GetCreationTimestamp().Time)
		f.weak = true
	case phrase != "":
		// A condition on a bound claim (a resize under way or refused).
		severity := Progressing
		if level >= model1.LevelDegraded {
			severity = Degraded
		}
		f.add(severity, phrase, time.Time{})
	}
}

func classifyNode(u *unstructured.Unstructured, f *finding) {
	// The state word stands in for the STATUS column this list does not have.
	ready := condition(u, "Ready", "")
	switch status, _ := ready["status"].(string); {
	case ready == nil:
		f.add(Degraded, "no Ready condition reported", time.Time{})
		f.weak = true
	case status == "False":
		f.add(Failing, "NotReady", transition(ready))
	case status != "True":
		f.add(Failing, "Ready unknown", transition(ready))
	}
	phrase, level, ok := render.StatusWhy("Node", u)
	if !ok || level == model1.LevelNone || phrase == "" {
		return
	}
	if level == model1.LevelFailing && f.found {
		// One finding: the state and what the kubelet says about it.
		f.parts[0] += " · " + phrase
		return
	}
	var since time.Time
	for _, kind := range []string{"NetworkUnavailable", "MemoryPressure", "DiskPressure", "PIDPressure"} {
		if c := condition(u, kind, "True"); c != nil {
			since = transition(c)
			break
		}
	}
	severity := Degraded
	if level == model1.LevelFailing {
		severity = Failing
	}
	f.add(severity, phrase, since)
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
