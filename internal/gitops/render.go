// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *Snapshot) Partial() bool {
	if s.CollectionState != "" && s.CollectionState != Complete {
		return true
	}
	for _, link := range s.Links {
		if link.State != Complete {
			return true
		}
	}
	for _, source := range s.Coverage {
		if source.State != Complete {
			return true
		}
	}
	return false
}

// Render keeps findings before provenance; the TUI owns search/scroll, escaping
// and geometry. The same state labels work without icons or color.
func (s *Snapshot) Render(tab int) string {
	var out strings.Builder
	switch tab {
	case 1:
		s.renderChain(&out)
	case 2:
		s.renderSources(&out)
	case 3:
		s.renderEvidence(&out)
	default:
		s.renderOverview(&out)
	}
	return out.String()
}

func (s *Snapshot) renderOverview(out *strings.Builder) {
	out.WriteString("GITOPS OBSERVATION · READ ONLY\n")
	if len(s.Nodes) == 0 {
		out.WriteString("Selected resource unavailable; ownership and reconciliation unknown.\n")
		for _, source := range s.Coverage {
			fmt.Fprintf(out, "%s: %s\n", source.State, source.Reason)
		}
		return
	}
	primary := s.primaryController()
	if primary == nil {
		out.WriteString("No supported GitOps controller observed. Ownership remains unknown.\n")
	} else {
		fmt.Fprintf(out, "%s · %s %s/%s\n", strings.ToUpper(primary.State), primary.Kind, primary.Identity.Namespace, primary.Identity.Name)
		if primary.Kind == argoApplication {
			fmt.Fprintf(out, "Reported sync %s · health %s · operation %s\n",
				unknownText(primary.ReportedSync), unknownText(primary.ReportedHealth), unknownText(primary.OperationPhase))
			fmt.Fprintf(out, "Automatic sync: %s · last reported reconcile: %s\n", boolText(primary.Automated), unknownText(primary.ReconciledAt))
		}
		fmt.Fprintf(out, "Generation %s · observed %s\n", numberText(primary.Generation), numberText(primary.ObservedGeneration))
		fmt.Fprintln(out, primary.Reason)
		for _, condition := range primary.Conditions[:min(3, len(primary.Conditions))] {
			fmt.Fprintf(out, "  %s %s · %s\n", condition.Type, condition.Status, condition.Reason)
		}
		for _, gap := range primary.Gaps[:min(3, len(primary.Gaps))] {
			fmt.Fprintf(out, "[?] %s\n", gap)
		}
	}
	fmt.Fprintf(out, "\n%d observed identities · %d reference links", len(s.Nodes), len(s.Links))
	if s.Partial() {
		out.WriteString(" · PARTIAL EVIDENCE")
	}
	out.WriteString("\n")
	for _, link := range s.Links {
		if link.State != Complete {
			fmt.Fprintf(out, "[%s] %s: %s\n", link.State, link.Relation, link.Reason)
		}
	}
	out.WriteString("\n2 Chain: controller UIDs, tracking uncertainty, dependencies\n" +
		"3 Sources: chart / app / Git / image evidence\n4 Evidence: full retained identities, scope, source times and gaps\n")
	out.WriteString("\nReported sync/health is not a workload-readiness verdict. Tracking labels and inventory alone do not prove ownership.\n")
}

func (s *Snapshot) primaryController() *Node {
	for index := range s.Nodes {
		node := &s.Nodes[index]
		switch node.Kind {
		case argoApplication, "Kustomization", "HelmRelease", "ResourceSet":
			return node
		}
	}
	for index := range s.Nodes {
		if s.Nodes[index].Provider == ProviderFlux {
			return &s.Nodes[index]
		}
	}
	return nil
}

func (s *Snapshot) renderChain(out *strings.Builder) {
	out.WriteString("OWNERSHIP / RECONCILIATION CHAIN\nReferences describe independent observations; no inferred ownership fallback.\n\n")
	for index := range s.Nodes {
		node := &s.Nodes[index]
		fmt.Fprintf(out, "%d %s %s/%s · %s\n  UID %s\n", index+1, node.Kind, node.Identity.Namespace, node.Identity.Name, node.State, node.Identity.UID)
		for _, link := range s.Links {
			if link.From != index {
				continue
			}
			target := "unobserved"
			if link.To >= 0 {
				target = fmt.Sprintf("%d", link.To+1)
			}
			fmt.Fprintf(out, "  → %s %s · %s\n    %s · %s\n", target, link.Relation, link.State, link.Certainty, link.Reference)
			if link.State != Complete {
				fmt.Fprintf(out, "    %s\n", link.Reason)
			}
		}
	}
	if len(s.Links) == 0 {
		out.WriteString("No explicit supported controller/tracking/source reference observed. Custom tracking or source ownership may remain unknown.\n")
	}
}

func (s *Snapshot) renderSources(out *strings.Builder) {
	out.WriteString("SOURCE / VERSION EVIDENCE\nChart version, application version, source revision and running image digest are different facts.\n\n")
	for index := range s.Nodes {
		node := &s.Nodes[index]
		if len(node.Versions) == 0 && len(node.Resources) == 0 {
			continue
		}
		fmt.Fprintf(out, "%s %s/%s\n", node.Kind, node.Identity.Namespace, node.Identity.Name)
		for _, version := range node.Versions {
			fmt.Fprintf(out, "%s: %s\n  %s\n", version.Kind, version.Value, version.Source)
		}
		if len(node.Resources) > 0 {
			fmt.Fprintf(out, "%d Application-reported inventory entries; resource UIDs unavailable\n", len(node.Resources))
			for _, resource := range node.Resources {
				fmt.Fprintf(out, "  %s/%s %s/%s · sync %s / health %s\n", resource.Group, resource.Kind,
					resource.Namespace, resource.Name, unknownText(resource.Sync), unknownText(resource.Health))
			}
		}
		for _, gap := range node.Gaps {
			fmt.Fprintf(out, "[?] %s\n", gap)
		}
		out.WriteByte('\n')
	}
}

func (s *Snapshot) renderEvidence(out *strings.Builder) {
	fmt.Fprintf(out, "RETAINED GITOPS EVIDENCE\nCaptured %s\n"+
		"16 named identities / depth 6 / 15s collection; 3s per native read.\n"+
		"API discovery checks only explicitly requested groups/versions (32-request cap).\n\n", s.CapturedAt.UTC().Format(time.RFC3339Nano))
	encoded, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		out.WriteString("Retained projection unavailable\n")
		return
	}
	const limit = 256 * 1024
	if len(encoded) > limit {
		out.Write(encoded[:limit])
		out.WriteString("\n[~] Evidence display capped; remaining projection not shown\n")
		return
	}
	out.Write(encoded)
}

func unknownText(value string) string {
	if value == "" {
		return Unknown
	}
	return value
}
func boolText(value *bool) string {
	if value == nil {
		return Unknown
	}
	if *value {
		return "enabled"
	}
	return "disabled/manual"
}
func numberText(value *int64) string {
	if value == nil {
		return Unknown
	}
	return fmt.Sprint(*value)
}
