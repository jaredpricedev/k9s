// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	investigationSelectedUID = "selected"
	investigationTestAPIName = "api"
)

func TestInvestigationRetainsCurrentPreviousAndUnknownFactsIndependently(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": investigationPodKind, "metadata": map[string]any{"name": investigationTestAPIName, "namespace": "apps", "uid": "uid"}, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": investigationTestAPIName, "ready": false, "restartCount": int64(7), "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "current message"}}, "lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": int64(137), "message": "previous message"}}}, map[string]any{"name": "unknown", "state": map[string]any{"running": map[string]any{}}}, map[string]any{"name": "zero", "ready": true, "restartCount": int64(0)}}}, "spec": map[string]any{"containers": []any{map[string]any{"name": investigationTestAPIName, "resources": map[string]any{"limits": map[string]any{"memory": "256Mi"}}}}}}}
	i := NewInvestigation(o, "dev", "v1/pods", time.Now())
	require.Len(t, i.Containers, 3)
	c := i.Containers[0]
	require.Equal(t, "CrashLoopBackOff", c.CurrentLabel())
	require.True(t, c.CurrentIssue())
	require.Equal(t, "OOMKilled · exit 137", c.LastTermination.Label())
	require.Equal(t, "previous message", c.LastTermination.Message)
	require.Nil(t, c.CurrentTermination)
	require.Nil(t, i.Containers[1].Ready)
	require.Nil(t, i.Containers[1].Restarts)
	require.NotNil(t, i.Containers[2].Restarts)
	require.Zero(t, *i.Containers[2].Restarts)
	require.Equal(t, "unset", i.Resources[0].CPURequest)
	require.Equal(t, "256Mi", i.Resources[0].MemoryLimit)
	require.Equal(t, "N/A", i.Resources[0].MemoryUsage)
	statuses, _, _ := unstructured.NestedSlice(o.Object, "status", "containerStatuses")
	statuses[0].(map[string]any)["restartCount"] = int64(99)
	require.EqualValues(t, 7, *c.Restarts, "retained typed facts must not alias mutable source state")
}

func TestInvestigationConditionPolarityDependsOnAPIKind(t *testing.T) {
	for _, tc := range []struct {
		kind, api, name, status string
		adverse                 bool
	}{{"Node", "v1", "MemoryPressure", string(corev1.ConditionFalse), false}, {"Node", "v1", "MemoryPressure", "True", true}, {"Job", "batch/v1", "Complete", string(corev1.ConditionFalse), false}, {"Job", "batch/v1", "Failed", "True", true}, {investigationPodKind, "v1", string(corev1.PodReady), string(corev1.ConditionFalse), true}, {investigationPodKind, "example.io/v1", string(corev1.PodReady), string(corev1.ConditionFalse), false}, {"Widget", "example.io/v1", string(corev1.PodReady), string(corev1.ConditionFalse), false}, {investigationPodKind, "v1", string(corev1.PodReady), "Unknown", false}} {
		t.Run(tc.kind+tc.api+tc.name+tc.status, func(t *testing.T) {
			o := &unstructured.Unstructured{Object: map[string]any{"kind": tc.kind, "apiVersion": tc.api, "status": map[string]any{"conditions": []any{map[string]any{"type": tc.name, "status": tc.status}}}}}
			i := NewInvestigation(o, "dev", "resource", time.Now())
			require.Equal(t, tc.adverse, i.Conditions[0].Adverse)
		})
	}
}

func TestInvestigationEventScopeOrderAndSeriesCount(t *testing.T) {
	now := time.Now().UTC()
	i := &Investigation{Identity: ResourceIdentity{UID: investigationSelectedUID}}
	i.AddEvents([]corev1.Event{{InvolvedObject: corev1.ObjectReference{UID: "other"}, Message: "wrong identity"}, {InvolvedObject: corev1.ObjectReference{UID: investigationSelectedUID}, Reason: "older", LastTimestamp: metav1.NewTime(now.Add(-time.Minute)), Count: 1}, {InvolvedObject: corev1.ObjectReference{UID: investigationSelectedUID}, Reason: "latest", Count: 1, Series: &corev1.EventSeries{Count: 12, LastObservedTime: metav1.NewMicroTime(now)}}})
	require.Len(t, i.Events, 2)
	require.Equal(t, "latest", i.Events[0].Reason)
	require.EqualValues(t, 12, i.Events[0].Count)
	require.Equal(t, now, i.Events[0].LastObserved)
}

func TestCompletedInitAndHistoricalCrashAreNotCurrentFaults(t *testing.T) {
	zero := int64(0)
	ready := true
	require.False(t, (InvestigationContainer{Role: "init", State: "terminated", CurrentTermination: &ContainerTermination{Reason: "Completed", ExitCode: &zero}}).CurrentIssue())
	require.False(t, (InvestigationContainer{Role: containerRoleApp, State: "running", Ready: &ready, LastTermination: &ContainerTermination{Reason: "OOMKilled"}}).CurrentIssue())
	require.False(t, (InvestigationContainer{Role: "ephemeral", State: "running"}).CurrentIssue())
	require.False(t, (InvestigationContainer{Role: containerRoleApp, State: "waiting", Reason: "ContainerCreating"}).CurrentIssue())
	falseReady := false
	require.True(t, (InvestigationContainer{Role: containerRoleSidecar, State: "running", Ready: &falseReady}).CurrentIssue())
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": investigationPodKind, "status": map[string]any{"phase": "Succeeded", "conditions": []any{map[string]any{"type": string(corev1.PodReady), "status": string(corev1.ConditionFalse), "reason": "PodCompleted"}}}}}
	require.False(t, NewInvestigation(o, "dev", "v1/pods", time.Now()).Conditions[0].Adverse)
}
