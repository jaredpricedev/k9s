// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package workspace

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	SeverityCritical  = "critical"
	categoryHistory   = "history"
	SeverityWarning   = "warning"
	SeverityInfo      = "info"
	CertificateWindow = 14 * 24 * time.Hour
	categoryHistory   = "history"
)

// Classify reads present resource status only. Conditions are interpreted by
// kind; historical container states are explicitly separate from current faults.
// Findings describe observations, not inferred causes or invented event history.
func Classify(ref ResourceRef, kind string, object *unstructured.Unstructured, now time.Time) []Finding {
	var findings []Finding
	add := func(category, severity, reason, detail string) {
		findings = append(findings, Finding{
			Ref: ref, Kind: kind, Category: category, Severity: severity,
			Reason: boundedText(reason, 100), Detail: boundedText(detail, 300), ObservedAt: now,
		})
	}
	switch kind {
	case kindPod:
		classifyPod(object, add)
	case kindDeployment, kindStatefulSet, kindDaemonSet, kindReplicaSet:
		classifyWorkload(kind, object, add)
	case kindJob:
		if boolean(object.Object, "spec", "suspend") {
			add("paused", SeverityInfo, "Suspended", "Job execution is explicitly suspended")
		}
		if condition(object, "Failed", "True") != nil {
			c := condition(object, "Failed", "True")
			add("failed", SeverityCritical, fallback(stringField(c, statusReasonField), "JobFailed"), fallback(stringField(c, "message"), "Job reports a Failed condition"))
		} else if condition(object, "Complete", "True") == nil && number(object.Object, "status", "failed") > 0 {
			add("attention", SeverityWarning, "FailedAttempts", fmt.Sprintf("%d failed pods reported; the job is not marked Failed", number(object.Object, "status", "failed")))
		}
	case kindCronJob:
		if boolean(object.Object, "spec", "suspend") {
			add("paused", SeverityInfo, "Suspended", "Future runs are explicitly suspended")
		}
	case kindKustomization, kindHelmRelease, kindGitRepository, kindHelmRepository, kindOCIRepository:
		if boolean(object.Object, "spec", "suspend") {
			add("paused", SeverityInfo, "Suspended", "Reconciliation is explicitly suspended")
		}
		if c := currentCondition(object, "Stalled", "True"); c != nil {
			add("failed", SeverityCritical,
				fallback(stringField(c, statusReasonField), "Stalled"), fallback(stringField(c, "message"), "Controller reports stalled reconciliation"))
		} else if c := currentCondition(object, "Ready", "False"); c != nil {
			add("unready", SeverityWarning, fallback(stringField(c, statusReasonField), "NotReady"), fallback(stringField(c, "message"), "Controller reports Ready=False"))
		}
		if pendingGeneration(object) {
			add("attention", SeverityInfo, "AwaitingObservation", "The controller has not observed the current resource generation")
		}
	case kindCertificate:
		if c := currentCondition(object, "Ready", "False"); c != nil {
			add("unready", SeverityWarning, fallback(stringField(c, statusReasonField), "NotReady"), fallback(stringField(c, "message"), "Certificate reports Ready=False"))
		}
		if raw, found, _ := unstructured.NestedString(object.Object, "status", "notAfter"); found {
			if expiry, err := time.Parse(time.RFC3339, raw); err == nil {
				remaining := expiry.Sub(now)
				if remaining <= 0 {
					add("expired", SeverityCritical, "CertificateExpired", "Certificate validity ended at "+expiry.UTC().Format(time.RFC3339))
				} else if remaining <= CertificateWindow {
					add("expiring", SeverityWarning, "CertificateExpiring", "Certificate validity ends at "+expiry.UTC().Format(time.RFC3339))
				}
			}
		}
	case "ResourceQuota":
		classifyQuota(object, add)
	case kindPersistentVolumeClaim:
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		if phase == "Lost" {
			add("failed", SeverityCritical, "VolumeLost", "Claim status reports Lost")
		} else if phase == "Pending" {
			add("attention", SeverityWarning, "Pending", "Claim is pending; a cause has not been established")
		}
	}
	sortFindings(findings)
	return findings
}

type addFinding func(category, severity, reason, detail string)

func classifyPod(object *unstructured.Unstructured, add addFinding) {
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	if phase == "Failed" {
		reason, _, _ := unstructured.NestedString(object.Object, "status", statusReasonField)
		message, _, _ := unstructured.NestedString(object.Object, "status", "message")
		add("failed", SeverityCritical, fallback(reason, "PodFailed"), fallback(message, "Pod status reports Failed"))
	}
	currentFault := false
	for _, field := range []string{"initContainerStatuses", "containerStatuses", "ephemeralContainerStatuses"} {
		statuses, _, _ := unstructured.NestedSlice(object.Object, "status", field)
		for _, raw := range statuses {
			status, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := stringField(status, containerNameField)
			if waiting, found, _ := unstructured.NestedMap(status, containerStateField, "waiting"); found {
				reason := stringField(waiting, statusReasonField)
				switch reason {
				case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "InvalidImageName":
					currentFault = true
					add("fault", SeverityCritical, reason, name+": "+fallback(stringField(waiting, "message"), "current container waiting state reports "+reason))
				}
			}
			if terminated, found, _ := unstructured.NestedMap(status, containerStateField, "terminated"); found {
				reason := stringField(terminated, statusReasonField)
				if reason == oomKilledReason || number(terminated, containerExitCodeField) != 0 {
					currentFault = true
					add("fault", SeverityCritical, fallback(reason, "ContainerExited"), fmt.Sprintf(
						"%s: current termination reports %s (exit %d)", name, fallback(reason, "failure"), number(terminated, containerExitCodeField),
					))
				}
			}
			if last, found, _ := unstructured.NestedMap(status, "lastState", "terminated"); found && stringField(last, statusReasonField) == oomKilledReason {
				add(categoryHistory, SeverityInfo, "PreviousOOMKilled", name+": the previous container termination reports OOMKilled; this is historical state")
			}
		}
	}
	if c := condition(object, "PodScheduled", "False"); c != nil {
		add("blocked", SeverityWarning, fallback(stringField(c, statusReasonField), "NotScheduled"), fallback(stringField(c, "message"), "Pod is not scheduled"))
	} else if phase == "Pending" && !currentFault {
		add("attention", SeverityInfo, "Pending", "Pod status reports Pending; a cause has not been established")
	}
	if phase == podRunningPhase && !currentFault {
		if c := condition(object, "Ready", "False"); c != nil {
			add("unready", SeverityWarning, fallback(stringField(c, statusReasonField), "NotReady"), fallback(stringField(c, "message"), "Pod reports Ready=False"))
		}
	}
}

func classifyWorkload(kind string, object *unstructured.Unstructured, add addFinding) {
	if kind == kindDeployment && boolean(object.Object, "spec", "paused") {
		add("paused", SeverityInfo, "Paused", "Deployment rollout is explicitly paused")
	}
	if pendingGeneration(object) {
		add("attention", SeverityInfo, "AwaitingObservation", "The controller has not observed the current resource generation")
		return
	}
	// Deployment condition failures have distinct semantics: Progressing=True
	// and ReplicaFailure=False are normal and must never become queue failures.
	if kind == kindDeployment {
		if c := currentCondition(object, "Progressing", "False"); c != nil {
			add("failed", SeverityCritical,
				fallback(stringField(c, statusReasonField), "RolloutStalled"), fallback(stringField(c, "message"), "Deployment reports Progressing=False"))
		}
		if c := currentCondition(object, "ReplicaFailure", "True"); c != nil {
			add("failed", SeverityCritical,
				fallback(stringField(c, statusReasonField), "ReplicaFailure"), fallback(stringField(c, "message"), "Deployment reports ReplicaFailure=True"))
		}
	}
	var desired, ready int64
	if kind == kindDaemonSet {
		desired = number(object.Object, "status", "desiredNumberScheduled")
		ready = number(object.Object, "status", "numberAvailable")
	} else {
		desired = 1
		if value, found, _ := unstructured.NestedInt64(object.Object, "spec", "replicas"); found {
			desired = value
		}
		ready = number(object.Object, "status", "readyReplicas")
		if kind == kindDeployment {
			ready = number(object.Object, "status", "availableReplicas")
		}
	}
	if desired > ready {
		readiness := "ready"
		if kind == kindDeployment || kind == kindDaemonSet {
			readiness = "available"
		}
		add("unready", SeverityWarning, "ReplicasNotReady", fmt.Sprintf("%d of %d desired replicas are %s", ready, desired, readiness))
	} else if kind == kindDeployment {
		if c := currentCondition(object, "Available", "False"); c != nil {
			add("unready", SeverityWarning, fallback(stringField(c, statusReasonField), "NotAvailable"), fallback(stringField(c, "message"), "Deployment reports Available=False"))
		}
	}
	if kind == kindDeployment {
		if updated, found, _ := unstructured.NestedInt64(object.Object, "status", "updatedReplicas"); found && updated < desired {
			add("attention", SeverityInfo, "RolloutInProgress", fmt.Sprintf("%d of %d desired replicas use the current pod template", updated, desired))
		}
	}
}

func classifyQuota(object *unstructured.Unstructured, add addFinding) {
	hard, _, _ := unstructured.NestedStringMap(object.Object, "status", "hard")
	used, _, _ := unstructured.NestedStringMap(object.Object, "status", "used")
	names := make([]string, 0, len(hard))
	for name := range hard {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		usedValue, exists := used[name]
		if !exists {
			continue
		}
		ceiling, err := resource.ParseQuantity(hard[name])
		if err != nil {
			continue
		}
		actual, err := resource.ParseQuantity(usedValue)
		if err != nil {
			continue
		}
		if actual.Cmp(ceiling) >= 0 {
			add("quota", SeverityWarning, "QuotaExhausted", fmt.Sprintf("%s: used %s of %s hard limit", name, usedValue, hard[name]))
		} else if ceiling.Sign() > 0 && actual.AsApproximateFloat64()/ceiling.AsApproximateFloat64() >= 0.9 {
			add("quota", SeverityWarning, "QuotaNearLimit", fmt.Sprintf("%s: used %s of %s hard limit (at least 90%%)", name, usedValue, hard[name]))
		}
	}
}

func condition(object *unstructured.Unstructured, kind, status string) map[string]any {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", conditionsField)
	for _, raw := range conditions {
		value, ok := raw.(map[string]any)
		if ok && stringField(value, "type") == kind && stringField(value, "status") == status {
			return value
		}
	}
	return nil
}

func currentCondition(object *unstructured.Unstructured, kind, status string) map[string]any {
	value := condition(object, kind, status)
	if value == nil || pendingGeneration(object) {
		return nil
	}
	if generation := number(value, observedGenerationField); generation > 0 && generation < object.GetGeneration() {
		return nil
	}
	return value
}

func pendingGeneration(object *unstructured.Unstructured) bool {
	observed, found, _ := unstructured.NestedInt64(object.Object, "status", observedGenerationField)
	if found {
		return observed < object.GetGeneration()
	}
	if object.GetGeneration() <= 0 {
		return false
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", conditionsField)
	for _, raw := range conditions {
		value, ok := raw.(map[string]any)
		if ok && number(value, observedGenerationField) >= object.GetGeneration() {
			return false
		}
	}
	return true
}

func number(object map[string]any, path ...string) int64 {
	value, _, _ := unstructured.NestedInt64(object, path...)
	return value
}

func boolean(object map[string]any, path ...string) bool {
	value, _, _ := unstructured.NestedBool(object, path...)
	return value
}

func stringField(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return value
}

func fallback(value, alternative string) string {
	if value == "" {
		return alternative
	}
	return value
}

func sortFindings(findings []Finding) {
	priority := func(severity string) int {
		switch severity {
		case SeverityCritical:
			return 0
		case SeverityWarning:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if priority(findings[i].Severity) != priority(findings[j].Severity) {
			return priority(findings[i].Severity) < priority(findings[j].Severity)
		}
		left, right := refKey(findings[i].Ref), refKey(findings[j].Ref)
		if left != right {
			return left < right
		}
		return findings[i].Reason < findings[j].Reason
	})
}

func summarize(kind string, object *unstructured.Unstructured, findings []Finding) string {
	for index := range findings {
		finding := &findings[index]
		if finding.Severity != SeverityInfo || finding.Category == "paused" || finding.Reason == "AwaitingObservation" {
			return finding.Reason
		}
	}
	switch kind {
	case kindPod:
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		statuses, _, _ := unstructured.NestedSlice(object.Object, "status", "containerStatuses")
		ready := 0
		for _, raw := range statuses {
			status, _ := raw.(map[string]any)
			if boolean(status, "ready") {
				ready++
			}
		}
		if len(statuses) > 0 {
			return fmt.Sprintf("%s %d/%d ready", fallback(phase, "Unknown"), ready, len(statuses))
		}
		return fallback(phase, "Status unknown")
	case kindJob:
		if condition(object, "Complete", "True") != nil {
			return "Complete"
		}
		return fmt.Sprintf("%d active, %d succeeded", number(object.Object, "status", "active"), number(object.Object, "status", "succeeded"))
	case kindCronJob:
		schedule, _, _ := unstructured.NestedString(object.Object, "spec", "schedule")
		return fallback(schedule, "Schedule unknown")
	case kindDeployment, kindStatefulSet, kindReplicaSet:
		return fmt.Sprintf("%d ready", number(object.Object, "status", "readyReplicas"))
	case kindDaemonSet:
		return fmt.Sprintf("%d/%d available", number(object.Object, "status", "numberAvailable"), number(object.Object, "status", "desiredNumberScheduled"))
	case kindKustomization, kindHelmRelease, kindGitRepository, kindHelmRepository, kindOCIRepository, kindCertificate:
		if currentCondition(object, "Ready", "True") != nil {
			return "Ready"
		}
		return "Readiness unknown"
	case kindPersistentVolumeClaim:
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		return fallback(phase, "Status unknown")
	}
	return strings.TrimSpace(kind)
}
