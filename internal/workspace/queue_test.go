// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package workspace

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const categoryPaused = "paused"

func setStatus(object *unstructured.Unstructured, status map[string]any) {
	object.Object["status"] = status
}

func cond(kind, state, reason string, observed int64) any {
	return map[string]any{"type": kind, "status": state, statusReasonField: reason, observedGenerationField: observed}
}

func TestQueuePodSeparatesCurrentFaultFromHistoricalOOM(t *testing.T) {
	object := testObject(kindPod, testNamespace, testResourceName)
	setStatus(&object, map[string]any{
		"phase":         podRunningPhase,
		conditionsField: []any{cond("Ready", "True", "", 0)},
		"containerStatuses": []any{map[string]any{
			containerNameField: testContainerName, "ready": true,
			containerStateField: map[string]any{"running": map[string]any{}},
			"lastState":         map[string]any{"terminated": map[string]any{statusReasonField: oomKilledReason, containerExitCodeField: int64(137)}},
		}},
	})
	findings := Classify(ResourceRef{}, kindPod, &object, testNow)
	if len(findings) != 1 || findings[0].Category != "history" || findings[0].Severity != SeverityInfo || !strings.Contains(findings[0].Detail, "historical") {
		t.Fatalf("previous OOM treated as a current incident: %#v", findings)
	}
	status := object.Object["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
	status[containerStateField] = map[string]any{"waiting": map[string]any{statusReasonField: "CrashLoopBackOff", "message": "back-off restarting container"}}
	findings = Classify(ResourceRef{}, kindPod, &object, testNow)
	if len(findings) != 2 || findings[0].Category != "fault" || findings[0].Reason != "CrashLoopBackOff" || findings[1].Category != "history" {
		t.Fatalf("current fault and historical state not separate: %#v", findings)
	}
}

func TestQueueCurrentOOMAndCompletedInitContainer(t *testing.T) {
	object := testObject(kindPod, testNamespace, testResourceName)
	setStatus(&object, map[string]any{"phase": podRunningPhase, "initContainerStatuses": []any{map[string]any{
		containerNameField: "setup", containerStateField: map[string]any{"terminated": map[string]any{statusReasonField: "Completed", containerExitCodeField: int64(0)}},
	}}, "containerStatuses": []any{map[string]any{
		containerNameField: testContainerName, containerStateField: map[string]any{"terminated": map[string]any{statusReasonField: oomKilledReason, containerExitCodeField: int64(137)}},
	}}})
	findings := Classify(ResourceRef{}, kindPod, &object, testNow)
	if len(findings) != 1 || findings[0].Category != "fault" || findings[0].Reason != oomKilledReason {
		t.Fatalf("completed init container incorrectly failed: %#v", findings)
	}
}

func TestQueueWorkloadsUseCurrentGenerationAndKindAwareConditions(t *testing.T) {
	object := testObject(kindDeployment, testNamespace, testResourceName)
	object.SetGeneration(3)
	object.Object["spec"] = map[string]any{"replicas": int64(3)}
	setStatus(&object, map[string]any{observedGenerationField: int64(3), "availableReplicas": int64(3), conditionsField: []any{
		cond("Available", "True", "MinimumReplicasAvailable", 3),
		cond("Progressing", "True", "NewReplicaSetAvailable", 3),
		cond("ReplicaFailure", "False", "NoReplicaFailure", 3),
	}})
	if findings := Classify(ResourceRef{}, kindDeployment, &object, testNow); len(findings) != 0 {
		t.Fatalf("normal False condition treated as failure: %#v", findings)
	}
	setStatus(&object, map[string]any{observedGenerationField: int64(2), "availableReplicas": int64(0), conditionsField: []any{
		cond("Progressing", "False", "ProgressDeadlineExceeded", 2),
	}})
	findings := Classify(ResourceRef{}, kindDeployment, &object, testNow)
	if len(findings) != 1 || findings[0].Reason != "AwaitingObservation" || findings[0].Severity != SeverityInfo {
		t.Fatalf("stale deployment failure reported as current: %#v", findings)
	}
	object.Object["status"].(map[string]any)[observedGenerationField] = int64(3)
	object.Object["status"].(map[string]any)[conditionsField] = []any{cond("Progressing", "False", "ProgressDeadlineExceeded", 3)}
	findings = Classify(ResourceRef{}, kindDeployment, &object, testNow)
	if len(findings) != 2 || findings[0].Reason != "ProgressDeadlineExceeded" || findings[0].Severity != SeverityCritical || findings[1].Reason != "ReplicasNotReady" {
		t.Fatalf("current deployment failure missed: %#v", findings)
	}
}

func TestQueueZeroReplicaWorkloadHasNoUnreadyFinding(t *testing.T) {
	object := testObject(kindStatefulSet, testNamespace, "sleeping")
	object.SetGeneration(1)
	object.Object["spec"] = map[string]any{"replicas": int64(0)}
	setStatus(&object, map[string]any{observedGenerationField: int64(1)})
	if findings := Classify(ResourceRef{}, kindStatefulSet, &object, testNow); len(findings) != 0 {
		t.Fatalf("zero replicas incorrectly unready: %#v", findings)
	}
}

func TestQueueJobFailedAttemptsAreNotTerminalFailure(t *testing.T) {
	object := testObject(kindJob, testNamespace, "migration")
	setStatus(&object, map[string]any{"failed": int64(2)})
	findings := Classify(ResourceRef{}, kindJob, &object, testNow)
	if len(findings) != 1 || findings[0].Reason != "FailedAttempts" || findings[0].Severity != SeverityWarning {
		t.Fatalf("retrying job marked terminal failed: %#v", findings)
	}
	object.Object["status"].(map[string]any)[conditionsField] = []any{cond("Complete", "True", "", 0)}
	if findings := Classify(ResourceRef{}, kindJob, &object, testNow); len(findings) != 0 {
		t.Fatalf("completed job historical retries still actionable: %#v", findings)
	}
	object.Object["status"].(map[string]any)[conditionsField] = []any{cond("Failed", "True", "BackoffLimitExceeded", 0)}
	if findings := Classify(ResourceRef{}, kindJob, &object, testNow); len(findings) != 1 || findings[0].Severity != SeverityCritical {
		t.Fatalf("terminal job failure missed: %#v", findings)
	}
}

func TestQueueFluxStalledFalseIsHealthyAndSuspendIsExplicit(t *testing.T) {
	object := testObject(kindKustomization, testNamespace, testResourceName)
	object.SetGeneration(4)
	setStatus(&object, map[string]any{observedGenerationField: int64(4), conditionsField: []any{cond("Stalled", "False", "", 4), cond("Reconciling", "False", "", 4), cond("Ready", "True", "", 4)}})
	if findings := Classify(ResourceRef{}, kindKustomization, &object, testNow); len(findings) != 0 {
		t.Fatalf("Flux normal False conditions treated as faults: %#v", findings)
	}
	object.Object["spec"] = map[string]any{"suspend": true}
	if findings := Classify(ResourceRef{}, kindKustomization, &object, testNow); len(findings) != 1 || findings[0].Category != categoryPaused {
		t.Fatalf("Flux suspension missed: %#v", findings)
	}
	object.Object["status"].(map[string]any)[conditionsField] = []any{cond("Stalled", "True", "ReconciliationFailed", 4)}
	if findings := Classify(ResourceRef{}, kindKustomization, &object, testNow); len(findings) != 2 || findings[0].Reason != "ReconciliationFailed" {
		t.Fatalf("Flux stall missed: %#v", findings)
	}
}

func TestQueueCertificatesUseObservedValidityRatherThanGuessing(t *testing.T) {
	for _, tc := range []struct {
		expiry time.Duration
		reason string
	}{
		{-time.Hour, "CertificateExpired"},
		{time.Hour, "CertificateExpiring"},
		{CertificateWindow, "CertificateExpiring"},
		{CertificateWindow + time.Hour, ""},
	} {
		object := testObject(kindCertificate, testNamespace, testResourceName)
		setStatus(&object, map[string]any{"notAfter": testNow.Add(tc.expiry).Format(time.RFC3339), conditionsField: []any{cond("Ready", "True", "", 0)}})
		findings := Classify(ResourceRef{}, kindCertificate, &object, testNow)
		if tc.reason == "" && len(findings) != 0 || tc.reason != "" && (len(findings) != 1 || findings[0].Reason != tc.reason) {
			t.Fatalf("expiry %s classified incorrectly: %#v", tc.expiry, findings)
		}
	}
	object := testObject(kindCertificate, testNamespace, testResourceName)
	setStatus(&object, map[string]any{"notAfter": "unknown"})
	if findings := Classify(ResourceRef{}, kindCertificate, &object, testNow); len(findings) != 0 {
		t.Fatalf("unknown certificate date fabricated a finding: %#v", findings)
	}
}

func TestQueueQuotaUnderstandsKubernetesQuantities(t *testing.T) {
	object := testObject("ResourceQuota", testNamespace, "compute")
	setStatus(&object, map[string]any{
		"hard": map[string]any{"requests.cpu": "2", "requests.memory": "1Gi", podsResourceName: "10", "services": "20"},
		"used": map[string]any{"requests.cpu": "1800m", "requests.memory": "1024Mi", podsResourceName: "8", "services": "garbage"},
	})
	findings := Classify(ResourceRef{}, "ResourceQuota", &object, testNow)
	if len(findings) != 2 || findings[0].Reason != "QuotaExhausted" || findings[1].Reason != "QuotaNearLimit" {
		t.Fatalf("quota units interpreted incorrectly: %#v", findings)
	}
	if !strings.Contains(findings[0].Detail, "requests.memory") || !strings.Contains(findings[1].Detail, "requests.cpu") {
		t.Fatalf("wrong quota resources: %#v", findings)
	}
}

func TestQueueCronJobSuspensionDoesNotInventMissedRuns(t *testing.T) {
	object := testObject(kindCronJob, testNamespace, "backup")
	object.Object["spec"] = map[string]any{"schedule": "0 * * * *", "suspend": false}
	setStatus(&object, map[string]any{"lastScheduleTime": testNow.Add(-24 * time.Hour).Format(time.RFC3339)})
	if findings := Classify(ResourceRef{}, kindCronJob, &object, testNow); len(findings) != 0 {
		t.Fatalf("invented overdue run from old schedule timestamp: %#v", findings)
	}
	object.Object["spec"].(map[string]any)["suspend"] = true
	if findings := Classify(ResourceRef{}, kindCronJob, &object, testNow); len(findings) != 1 || findings[0].Category != categoryPaused {
		t.Fatalf("cronjob suspension missed: %#v", findings)
	}
}

func TestQueueUnknownKindConditionsDoNotBecomeFailures(t *testing.T) {
	object := testObject("Widget", testNamespace, "custom")
	setStatus(&object, map[string]any{conditionsField: []any{cond("Disabled", "False", "Expected", 0)}})
	if findings := Classify(ResourceRef{}, "Widget", &object, testNow); len(findings) != 0 {
		t.Fatalf("unknown condition interpreted as fault: %#v", findings)
	}
}
