// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func projectNode(node *corev1.Node) Node {
	result := Node{Unschedulable: node.Spec.Unschedulable, Ready: ReadyStateUnknown, ResourceVersion: node.ResourceVersion}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			result.Ready = string(condition.Status)
		}
	}
	return result
}

func (s *Snapshot) projectPods(ctx context.Context, pods []corev1.Pod, budgets []policyv1.PodDisruptionBudget) {
	for index := range budgets {
		if err := ctx.Err(); err != nil {
			s.projectionInterrupted(err)
			return
		}
		s.Budgets = append(s.Budgets, projectBudget(&budgets[index], s.Identity.Context))
	}
	for index := range pods {
		if err := ctx.Err(); err != nil {
			s.projectionInterrupted(err)
			return
		}
		pod := &pods[index]
		projected := projectPod(pod, s.Identity.Context)
		for budgetIndex := range budgets {
			if err := ctx.Err(); err != nil {
				s.projectionInterrupted(err)
				return
			}
			budget := &budgets[budgetIndex]
			if budget.Namespace != pod.Namespace {
				continue
			}
			matches, err := budgetMatches(budget, pod.Labels)
			if err != nil {
				projected.BudgetEvidence = append(projected.BudgetEvidence, budget.Name+": invalid selector; coverage unknown")
				continue
			}
			if !matches {
				continue
			}
			projected.MatchingBudgets++
			s.Budgets[budgetIndex].Matches++
			projected.BudgetEvidence = append(projected.BudgetEvidence, budget.Name+": "+budgetAssessment(pod, budget))
		}
		if projected.MatchingBudgets > 1 {
			projected.BudgetEvidence = append(projected.BudgetEvidence, "Multiple matching PDBs: admission is ambiguous; no safe allowance inferred")
		}
		if !s.budgetsComplete(pod.Namespace) {
			projected.BudgetEvidence = append(projected.BudgetEvidence, "PDB coverage partial or unavailable for this namespace")
		}
		if len(projected.BudgetEvidence) == 0 {
			projected.BudgetEvidence = []string{"No matching PDB observed in a complete namespace page; API admission still decides"}
		}
		s.Pods = append(s.Pods, projected)
	}
}

func (s *Snapshot) projectionInterrupted(err error) {
	for index := range s.Coverage {
		source := &s.Coverage[index]
		if source.Source == PodsSource && source.State == Complete {
			source.State, source.Detail = Partial, "Workload projection interrupted; reviewed identity set is incomplete"
		}
	}
	s.Coverage = append(s.Coverage, Coverage{Source: "Workload projection", State: Unavailable, Detail: logstream.SafeText(err.Error()), ObservedAt: s.CapturedAt})
}

func (s *Snapshot) budgetsComplete(namespace string) bool {
	for _, source := range s.Coverage {
		if source.Source == BudgetsSource && source.Scope == namespace {
			return source.State == Complete
		}
	}
	return false
}

func projectPod(pod *corev1.Pod, contextName string) Pod {
	result := Pod{Identity: inspect.ResourceIdentity{Context: contextName, GVR: "v1/pods", Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID)},
		Phase: string(pod.Status.Phase), Ready: podReadiness(pod), GraceSeconds: 30, GraceDefault: pod.Spec.TerminationGracePeriodSeconds == nil,
		Terminating: pod.DeletionTimestamp != nil, Mirror: pod.Annotations[corev1.MirrorPodAnnotationKey] != ""}
	if pod.Spec.TerminationGracePeriodSeconds != nil {
		result.GraceSeconds = *pod.Spec.TerminationGracePeriodSeconds
	}
	if owner := metav1.GetControllerOf(pod); owner != nil {
		result.OwnerKind, result.Owner = owner.Kind, owner.Kind+"/"+owner.Name
	}
	if result.Owner == "" {
		result.Owner = "Unmanaged (no controller reference)"
	}
	for index := range pod.Spec.Volumes {
		volume := &pod.Spec.Volumes[index]
		switch {
		case volume.EmptyDir != nil:
			result.EmptyDir = append(result.EmptyDir, volume.Name)
		case volume.HostPath != nil:
			result.HostPath = append(result.HostPath, volume.Name)
		case volume.PersistentVolumeClaim != nil:
			result.Claims = append(result.Claims, volume.PersistentVolumeClaim.ClaimName)
		}
	}
	result.Constraints = podConstraints(pod)
	return result
}

func podReadiness(pod *corev1.Pod) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			switch condition.Status {
			case corev1.ConditionTrue:
				return ReadyStateReady
			case corev1.ConditionFalse:
				return ReadyStateNotReady
			}
		}
	}
	return ReadyStateUnknown
}

func podConstraints(pod *corev1.Pod) []string {
	var result []string
	if len(pod.Spec.NodeSelector) > 0 {
		var entries []string
		for key, value := range pod.Spec.NodeSelector {
			entries = append(entries, key+"="+value)
		}
		sort.Strings(entries)
		result = append(result, "nodeSelector: "+strings.Join(entries, ", "))
	}
	for _, evidence := range []struct {
		label   string
		value   any
		present bool
	}{
		{"affinity", pod.Spec.Affinity, pod.Spec.Affinity != nil},
		{"tolerations", pod.Spec.Tolerations, len(pod.Spec.Tolerations) > 0},
		{"topologySpreadConstraints", pod.Spec.TopologySpreadConstraints, len(pod.Spec.TopologySpreadConstraints) > 0},
	} {
		if !evidence.present {
			continue
		}
		data, err := json.Marshal(evidence.value)
		if err == nil {
			result = append(result, evidence.label+": "+logstream.SafeText(string(data)))
		}
	}
	if pod.Spec.SchedulerName != "" {
		result = append(result, "scheduler: "+pod.Spec.SchedulerName)
	}
	if pod.Spec.PriorityClassName != "" {
		result = append(result, "priorityClass: "+pod.Spec.PriorityClassName)
	}
	if len(result) == 0 {
		result = append(result, "No explicit placement constraints observed; admission and replacement placement remain unknown")
	}
	return result
}

func projectBudget(budget *policyv1.PodDisruptionBudget, contextName string) Budget {
	result := Budget{Identity: inspect.ResourceIdentity{Context: contextName, GVR: "policy/v1/poddisruptionbudgets",
		Namespace: budget.Namespace, Name: budget.Name, UID: string(budget.UID)},
		Generation: budget.Generation, ObservedGeneration: budget.Status.ObservedGeneration, DisruptionsAllowed: budget.Status.DisruptionsAllowed,
		CurrentHealthy: budget.Status.CurrentHealthy, DesiredHealthy: budget.Status.DesiredHealthy,
		ExpectedPods: budget.Status.ExpectedPods, Policy: string(policyv1.IfHealthyBudget)}
	if budget.Spec.UnhealthyPodEvictionPolicy != nil {
		result.Policy = string(*budget.Spec.UnhealthyPodEvictionPolicy)
	}
	if budget.Spec.Selector == nil {
		result.Selector = "nil selector: matches no Pods"
		return result
	}
	selector, err := metav1.LabelSelectorAsSelector(budget.Spec.Selector)
	if err != nil {
		result.SelectorError = logstream.SafeText(err.Error())
		return result
	}
	result.Selector = selector.String()
	if selector.Empty() {
		result.Selector = "empty selector: matches every Pod in this namespace"
	}
	return result
}

func budgetMatches(budget *policyv1.PodDisruptionBudget, podLabels map[string]string) (bool, error) {
	if budget.Spec.Selector == nil {
		return false, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(budget.Spec.Selector)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(podLabels)), nil
}

func budgetAssessment(pod *corev1.Pod, budget *policyv1.PodDisruptionBudget) string {
	if budget.Status.ObservedGeneration != budget.Generation || budget.Generation <= 0 {
		return "Stale or missing observedGeneration; allowance unknown"
	}
	status := budget.Status
	policy := budget.Spec.UnhealthyPodEvictionPolicy
	if policy != nil && *policy != policyv1.AlwaysAllow && *policy != policyv1.IfHealthyBudget {
		return "Invalid unhealthy-Pod policy; allowance unknown"
	}
	if status.DisruptionsAllowed < 0 || status.CurrentHealthy < 0 || status.DesiredHealthy < 0 || status.ExpectedPods < 0 {
		return "Invalid reported counts; allowance unknown"
	}
	if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return "Terminating or terminal Pod; allowance is not interpreted as a blocker"
	}
	if pod.Status.Phase != corev1.PodRunning {
		return "Non-running Pod; admission behavior is not inferred from allowance"
	}
	ready := podReadiness(pod)
	if ready == ReadyStateUnknown {
		return "Readiness unavailable; unhealthy-Pod policy cannot be evaluated"
	}
	if ready == ReadyStateNotReady {
		if budget.Spec.UnhealthyPodEvictionPolicy != nil && *budget.Spec.UnhealthyPodEvictionPolicy == policyv1.AlwaysAllow {
			return "Unready + AlwaysAllow: PDB allowance may be bypassed; API still decides"
		}
		if status.CurrentHealthy >= status.DesiredHealthy {
			return "Unready + IfHealthyBudget: reported healthy budget may permit eviction; API still decides"
		}
	}
	if status.DisruptionsAllowed == 0 {
		return "Potential blocker: reported allowance 0; admission may reject/retry"
	}
	return fmt.Sprintf("Reported allowance %d; this is not an admission permit", status.DisruptionsAllowed)
}
