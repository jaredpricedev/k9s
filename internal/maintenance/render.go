// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package maintenance

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/logstream"
)

// Render returns linear evidence. The terminal view owns escaping, search,
// scrolling and style; the same facts remain meaningful without color.
func (s *Snapshot) Render(tab int) string {
	var out strings.Builder
	switch tab {
	case 1:
		s.renderPods(&out)
	case 2:
		s.renderBudgets(&out)
	case 3:
		s.renderConstraints(&out)
	case 4:
		out.WriteString("OPERATION AND RECOVERY\nNo maintenance receipt is associated with this preview yet.\n" +
			"API acceptance is separate from observed Pod removal and workload recovery.\n")
	case 5:
		s.renderEvidence(&out)
	default:
		s.renderOverview(&out)
	}
	return logstream.SafeText(out.String())
}

func (s *Snapshot) renderOverview(out *strings.Builder) {
	var daemonSets, emptyDirs, unmanaged, mirrors, potential, ambiguous int
	for index := range s.Pods {
		pod := &s.Pods[index]
		if pod.OwnerKind == "DaemonSet" {
			daemonSets++
		}
		if len(pod.EmptyDir) > 0 {
			emptyDirs++
		}
		if pod.OwnerKind == "" {
			unmanaged++
		}
		if pod.Mirror {
			mirrors++
		}
		if pod.MatchingBudgets > 1 {
			ambiguous++
		}
		for _, evidence := range pod.BudgetEvidence {
			if strings.Contains(evidence, "Potential blocker:") {
				potential++
				break
			}
		}
	}
	fmt.Fprintf(out, "MAINTENANCE PREVIEW\n%d affected Pods | %d observed potential PDB blockers\nNode Ready: %s | unschedulable=%t\n",
		len(s.Pods), potential, s.Node.Ready, s.Node.Unschedulable)
	if !s.PDBsComplete() {
		out.WriteString("PDB blockers unknown: partial, stale or unavailable evidence.\n")
	}
	if s.Partial() {
		out.WriteString("Partial evidence: 6 Evidence explains gaps.\n")
	}
	fmt.Fprintf(out, "Handling choices: %d DaemonSet, %d EmptyDir, %d unmanaged, %d mirror\n", daemonSets, emptyDirs, unmanaged, mirrors)
	if ambiguous > 0 {
		fmt.Fprintf(out, "%d Pods match multiple PDBs; no safe allowance inferred.\n", ambiguous)
	}
	out.WriteString("d review drain options  c cordon  u uncordon\n\n")
	out.WriteString("Placement of replacement Pods is unknown. Aggregate free capacity does not prove they can schedule.\n")
	out.WriteString("Native kubectl filters and eviction admission decide eligibility. PDB status is retained evidence, not a permit.\n")
	out.WriteString("Drain cancellation keeps accepted cordon/evictions. Use 5 Outcomes and refresh before recovery; never infer workload readiness from Pod removal.\n")
}

func (s *Snapshot) renderPods(out *strings.Builder) {
	out.WriteString("AFFECTED WORKLOADS\nOnly the retained node-scoped Pod page is shown.\n\n")
	if len(s.Pods) == 0 {
		if s.PodsComplete() {
			out.WriteString("No Pods observed on this Node in a complete page.\n")
		} else {
			out.WriteString("Pods unavailable; empty is not evidence that the Node has no workloads.\n")
		}
	}
	for index := range s.Pods {
		pod := &s.Pods[index]
		fmt.Fprintf(out, "%s/%s  %s / %s\nController reference: %s | grace %ds", pod.Identity.Namespace, pod.Identity.Name, pod.Phase, pod.Ready, pod.Owner, pod.GraceSeconds)
		if pod.GraceDefault {
			out.WriteString(" (default)")
		}
		fmt.Fprintf(out, "\nUID: %s\n", pod.Identity.UID)
		if pod.Mirror {
			out.WriteString("Mirror/static Pod: native drain skips; kubelet ownership remains.\n")
		}
		if pod.OwnerKind == "DaemonSet" {
			out.WriteString("DaemonSet: ignore skips live managed Pods; it does not remove them.\n")
		}
		if len(pod.EmptyDir) > 0 {
			fmt.Fprintf(out, "EmptyDir: %s; delete-data must be explicit, data is not preserved by drain.\n", strings.Join(pod.EmptyDir, ", "))
		}
		if len(pod.HostPath) > 0 {
			fmt.Fprintf(out, "hostPath: %s; node-local data and replacement mobility are unverified.\n", strings.Join(pod.HostPath, ", "))
		}
		if len(pod.Claims) > 0 {
			fmt.Fprintf(out, "PVCs: %s; attachment/topology and data availability are not assessed.\n", strings.Join(pod.Claims, ", "))
		}
		if pod.OwnerKind == "" && !pod.Mirror {
			out.WriteString("Unmanaged: force may be required; replacement/recovery is not assured.\n")
		}
		if pod.Terminating {
			out.WriteString("Deletion timestamp observed; completion is not yet inferred.\n")
		}
		for _, evidence := range pod.BudgetEvidence {
			fmt.Fprintf(out, "PDB: %s\n", evidence)
		}
		out.WriteByte('\n')
	}
}

func (s *Snapshot) renderBudgets(out *strings.Builder) {
	out.WriteString("PDB / EVICTION EVIDENCE\nRetained status can change; admission evaluates each request.\n\n")
	if len(s.Budgets) == 0 {
		out.WriteString("No PDB observed. Consult Evidence before interpreting absence.\n")
	}
	for index := range s.Budgets {
		budget := &s.Budgets[index]
		fmt.Fprintf(out, "%s/%s | %d matching observed Pods\nUID: %s\nGeneration: %d / observed %d\n"+
			"Allowance: %d | healthy %d / desired %d | expected %d\nPolicy: %s\nSelector: %s\n",
			budget.Identity.Namespace, budget.Identity.Name, budget.Matches, budget.Identity.UID, budget.Generation, budget.ObservedGeneration,
			budget.DisruptionsAllowed, budget.CurrentHealthy, budget.DesiredHealthy, budget.ExpectedPods, budget.Policy, budget.Selector)
		if budget.SelectorError != "" {
			fmt.Fprintf(out, "Invalid selector: %s; coverage unknown\n", budget.SelectorError)
		}
		out.WriteByte('\n')
	}
	out.WriteString("Unready Running Pods: AlwaysAllow may bypass allowance. " +
		"IfHealthyBudget may allow eviction when currentHealthy >= desiredHealthy. Missing readiness or stale generation remains unknown.\n")
	out.WriteString("Multiple matching PDBs are ambiguous. Zero allowance is a potential blocker, not proof for every Pod phase.\n")
}

func (s *Snapshot) renderConstraints(out *strings.Builder) {
	out.WriteString("REPLACEMENT PLACEMENT\nNo scheduler simulation or replacement readiness is claimed. Aggregate free capacity is not scheduling proof.\n\n")
	for index := range s.Pods {
		pod := &s.Pods[index]
		fmt.Fprintf(out, "%s/%s\n", pod.Identity.Namespace, pod.Identity.Name)
		for _, constraint := range pod.Constraints {
			fmt.Fprintf(out, "%s\n", constraint)
		}
		if len(pod.Claims) > 0 || len(pod.HostPath) > 0 {
			out.WriteString("Storage attachment/topology/local-data mobility remain unverified.\n")
		}
		out.WriteByte('\n')
	}
	out.WriteString("Replacement controller templates, available Nodes/taints, quotas, admission, images and volumes may differ. " +
		"Review capacity and the workload after maintenance.\n")
}

func (s *Snapshot) renderEvidence(out *strings.Builder) {
	fmt.Fprintf(out, "CAPTURED IDENTITY\nContext: %s\nGVR: %s\nNode: %s\nUID: %s\nResourceVersion: %s\nCaptured: %s\n\nSOURCES / COVERAGE\n",
		s.Identity.Context, s.Identity.GVR, s.Identity.Name, s.Identity.UID, s.Node.ResourceVersion, s.CapturedAt.UTC().Format("2006-01-02 15:04:05Z"))
	for index := range s.Coverage {
		source := &s.Coverage[index]
		fmt.Fprintf(out, "%s %s: %s | %d/%d | %s\n", source.Source, source.Scope, source.State, source.Count, source.Limit, source.ObservedAt.UTC().Format("15:04:05Z"))
		if source.Detail != "" {
			fmt.Fprintf(out, "  %s\n", source.Detail)
		}
	}
	out.WriteString("\nAPI reads are independent observations, not an atomic cluster snapshot. " +
		"One Pod page (500), up to 32 observed namespaces / 500 PDBs; 3s per read, 10s total.\n")
	out.WriteString("Controller references are reported metadata, not verified controller existence or replacement health. " +
		"Eviction/delete authorization is rechecked by the guarded submit path.\n")
	out.WriteString("No Secret reads, environment, commands, arbitrary annotations or volume credentials retained. Constraints are safe spec fields.\n")
}
