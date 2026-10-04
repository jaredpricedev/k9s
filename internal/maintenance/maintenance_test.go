// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const testNamespace = "apps"
const testNode = "worker-1"
const testUID = "node-original"
const testPod = "api"
const testBudget = "api-budget"
const testAppLabel = "app"
const testPodsResource = "pods"
const testOtherPod = "other"
const testFreshZero = "fresh-zero"

func fixtureNode() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNode, UID: testUID, ResourceVersion: "11"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}
func fixtureIdentity() *inspect.ResourceIdentity {
	return &inspect.ResourceIdentity{Context: "captured", GVR: "v1/nodes", Name: testNode, UID: testUID}
}
func fixturePod() *corev1.Pod {
	controller := true
	grace := int64(60)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: testPod, Namespace: testNamespace, UID: "pod-original", Labels: map[string]string{testAppLabel: testPod},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: testPod, UID: "owner-original", Controller: &controller}}},
		Spec: corev1.PodSpec{NodeName: testNode, TerminationGracePeriodSeconds: &grace, NodeSelector: map[string]string{"disk": "ssd"},
			Containers: []corev1.Container{{Name: testPod, Env: []corev1.EnvVar{{Name: "PRIVATE_ENV", Value: "DO_NOT_RETAIN"}}}},
			Volumes:    []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "cache", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/private/not-retained"}}}, {Name: "storage", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}
func fixturePDB() *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: testBudget, Namespace: testNamespace, UID: "budget-original", Generation: 3},
		Spec:   policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{testAppLabel: testPod}}},
		Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 3, DisruptionsAllowed: 0, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1}}
}

func TestMaintenanceCollectKeepsIdentityEvidenceAndExcludesSecretContent(t *testing.T) {
	client := kubefake.NewSimpleClientset(fixtureNode(), fixturePod(), fixturePDB())
	snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
	require.NoError(t, err)
	require.False(t, snapshot.Partial())
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, int64(60), snapshot.Pods[0].GraceSeconds)
	require.Contains(t, snapshot.Render(0), "1 observed potential PDB blockers")
	require.Contains(t, snapshot.Render(1), "EmptyDir")
	require.Contains(t, snapshot.Render(3), "disk=ssd")
	require.Contains(t, snapshot.Render(3), "not scheduling proof")
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "DO_NOT_RETAIN")
	require.NotContains(t, string(encoded), "PRIVATE_ENV")
	require.NotContains(t, string(encoded), "/private/not-retained")
	for _, action := range client.Actions() {
		require.Contains(t, []string{"get", "list"}, action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
		if action.GetResource().Resource == testPodsResource {
			require.Equal(t, "spec.nodeName="+testNode, action.(ktesting.ListAction).GetListRestrictions().Fields.String())
		}
		if action.GetResource().Resource == "poddisruptionbudgets" {
			require.Equal(t, testNamespace, action.GetNamespace())
		}
	}
}

func TestMaintenanceCollectRejectsRecreatedNodeBeforePodReads(t *testing.T) {
	node := fixtureNode()
	node.UID = "replacement"
	client := kubefake.NewSimpleClientset(node)
	snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "identity changed")
	require.Len(t, client.Actions(), 1)
}

func TestMaintenanceDeniedAndPartialCoverageNeverClaimsEmptyOrSafe(t *testing.T) {
	for _, source := range []string{testPodsResource, "poddisruptionbudgets"} {
		t.Run(source, func(t *testing.T) {
			client := kubefake.NewSimpleClientset(fixtureNode(), fixturePod())
			client.PrependReactor("list", source, func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: source}, "", fmt.Errorf("denied"))
			})
			snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
			require.NoError(t, err)
			require.True(t, snapshot.Partial())
			require.Contains(t, snapshot.Render(5), Denied)
			if source == testPodsResource {
				require.False(t, snapshot.PodsComplete())
				require.Contains(t, snapshot.Render(1), "empty is not evidence")
			} else {
				require.Contains(t, snapshot.Render(1), "coverage partial or unavailable")
			}
		})
	}
	client := kubefake.NewSimpleClientset(fixtureNode())
	client.PrependReactor("list", testPodsResource, func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "remaining"}, Items: []corev1.Pod{*fixturePod()}}, nil
	})
	snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
	require.NoError(t, err)
	require.False(t, snapshot.PodsComplete())
	require.Len(t, snapshot.Pods, 1)
	require.Len(t, client.Actions(), 3, "must not follow continuation pages")
}

func TestMaintenancePDBSelectorPolicyAndFreshnessEdges(t *testing.T) {
	pod, budget := fixturePod(), fixturePDB()
	budget.Spec.Selector = nil
	matches, err := budgetMatches(budget, pod.Labels)
	require.NoError(t, err)
	require.False(t, matches)
	budget.Spec.Selector = &metav1.LabelSelector{}
	matches, err = budgetMatches(budget, pod.Labels)
	require.NoError(t, err)
	require.True(t, matches)
	budget.Spec.Selector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: testAppLabel, Operator: "invalid"}}}
	_, err = budgetMatches(budget, pod.Labels)
	require.Error(t, err)
	budget = fixturePDB()
	require.Contains(t, budgetAssessment(pod, budget), "Potential blocker")
	budget.Status.DisruptionsAllowed = 1
	require.Contains(t, budgetAssessment(pod, budget), "not an admission permit")
	budget.Status.ObservedGeneration = 2
	require.Contains(t, budgetAssessment(pod, budget), "Stale")
	budget = fixturePDB()
	always := policyv1.AlwaysAllow
	budget.Spec.UnhealthyPodEvictionPolicy = &always
	require.Contains(t, budgetAssessment(pod, budget), "Potential blocker", "AlwaysAllow does not bypass a healthy Pod's allowance")
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	require.Contains(t, budgetAssessment(pod, budget), "AlwaysAllow")
	budget.Spec.UnhealthyPodEvictionPolicy = nil
	require.Contains(t, budgetAssessment(pod, budget), "IfHealthyBudget")
	budget.Status.CurrentHealthy = 0
	require.Contains(t, budgetAssessment(pod, budget), "Potential blocker")
	pod.Status.Conditions = nil
	require.Contains(t, budgetAssessment(pod, budget), "Readiness unavailable")
	pod.Status.Phase = corev1.PodPending
	require.Contains(t, budgetAssessment(pod, budget), "Non-running")
	pod.Status.Phase = corev1.PodSucceeded
	require.Contains(t, budgetAssessment(pod, budget), "terminal")
}

func TestMaintenanceMultiplePDBsAndCollectionLimitsStayExplicit(t *testing.T) {
	other := fixturePDB()
	other.Name, other.UID = testOtherPod, types.UID("other-budget")
	client := kubefake.NewSimpleClientset(fixtureNode(), fixturePod(), fixturePDB(), other)
	snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 2, snapshot.Pods[0].MatchingBudgets)
	require.Contains(t, snapshot.Render(1), "Multiple matching PDBs")
	pods := make([]corev1.Pod, MaxPods+1)
	for index := range pods {
		pods[index] = *fixturePod()
		pods[index].Name = fmt.Sprintf("pod-%03d", index)
		pods[index].UID = types.UID(fmt.Sprintf("pod-uid-%03d", index))
	}
	client.PrependReactor("list", testPodsResource, func(ktesting.Action) (bool, runtime.Object, error) { return true, &corev1.PodList{Items: pods}, nil })
	snapshot, err = Collect(t.Context(), client, fixtureIdentity(), time.Now())
	require.NoError(t, err)
	require.Len(t, snapshot.Pods, MaxPods)
	require.False(t, snapshot.PodsComplete())
}

func TestMaintenanceCanceledCollectionRemainsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := kubefake.NewSimpleClientset(fixtureNode())
	// Fake trackers ignore context; the explicit collector must honor it itself.
	snapshot, err := Collect(ctx, client, fixtureIdentity(), time.Now())
	require.Nil(t, snapshot)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, client.Actions())
}

func TestMaintenanceMalformedPodIdentityAndWrongNamespaceBudgetFailClosed(t *testing.T) {
	for _, malformed := range []string{"missing", "duplicate-name", "duplicate-uid"} {
		t.Run(malformed, func(t *testing.T) {
			pod := fixturePod()
			other := fixturePod()
			switch malformed {
			case "missing":
				other.Name, other.UID = testOtherPod, ""
			case "duplicate-name":
				other.UID = "other-uid"
			case "duplicate-uid":
				other.Name = testOtherPod
			}
			client := kubefake.NewSimpleClientset(fixtureNode())
			client.PrependReactor("list", testPodsResource, func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &corev1.PodList{Items: []corev1.Pod{*pod, *other}}, nil
			})
			snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
			require.NoError(t, err)
			require.False(t, snapshot.PodsComplete())
			require.Nil(t, snapshot.ReviewedPods())
		})
	}
	client := kubefake.NewSimpleClientset(fixtureNode(), fixturePod())
	budget := fixturePDB()
	budget.Namespace = "unrelated"
	client.PrependReactor("list", "poddisruptionbudgets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &policyv1.PodDisruptionBudgetList{Items: []policyv1.PodDisruptionBudget{*budget}}, nil
	})
	snapshot, err := Collect(t.Context(), client, fixtureIdentity(), time.Now())
	require.NoError(t, err)
	require.Empty(t, snapshot.Budgets)
	require.Contains(t, snapshot.Render(5), "Wrong-namespace")
	require.Contains(t, snapshot.Render(1), "coverage partial or unavailable")
}

func TestMaintenanceCanceledProjectionCannotProduceExecutableIdentitySet(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot := &Snapshot{Identity: *fixtureIdentity(), Coverage: []Coverage{{Source: PodsSource, State: Complete}}}
	snapshot.projectPods(ctx, []corev1.Pod{*fixturePod()}, []policyv1.PodDisruptionBudget{*fixturePDB()})
	require.False(t, snapshot.PodsComplete())
	require.Nil(t, snapshot.ReviewedPods())
	require.Contains(t, snapshot.Render(5), "projection interrupted")
}

func TestMaintenanceUnknownBudgetEvidenceRemainsVisibleInOverview(t *testing.T) {
	for _, state := range []string{testFreshZero, "negative-counts", "invalid-policy", "unknown-readiness"} {
		t.Run(state, func(t *testing.T) {
			pod, budget := fixturePod(), fixturePDB()
			switch state {
			case testFreshZero:
				budget.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: budget.Generation}
			case "negative-counts":
				budget.Status.CurrentHealthy = -1
			case "invalid-policy":
				invalid := policyv1.UnhealthyPodEvictionPolicyType("invalid")
				budget.Spec.UnhealthyPodEvictionPolicy = &invalid
			case "unknown-readiness":
				pod.Status.Conditions = nil
			}
			snapshot, err := Collect(t.Context(), kubefake.NewSimpleClientset(fixtureNode(), pod, budget), fixtureIdentity(), time.Now())
			require.NoError(t, err)
			if state == testFreshZero {
				require.False(t, snapshot.Partial(), "fresh all-zero counts are valid retained evidence")
				require.Contains(t, snapshot.Render(0), "1 observed potential PDB blockers")
			} else {
				require.True(t, snapshot.Partial())
				require.Contains(t, snapshot.Render(0), "PDB blockers unknown")
			}
		})
	}
}
