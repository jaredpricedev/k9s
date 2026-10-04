// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/logstream"
	corev1 "k8s.io/api/core/v1"
)

const unknownValue = "unknown"

var Tabs = []string{"Overview", "Claims", "Topology", "CSI", "Evidence"}

func (s *Snapshot) Render(tab int) string {
	var b strings.Builder
	switch tab {
	case 1:
		s.claimText(&b)
	case 2:
		s.topologyText(&b)
	case 3:
		s.csiText(&b)
	case 4:
		s.evidenceText(&b)
	default:
		s.overviewText(&b)
	}
	return logstream.SafeText(b.String())
}
func (s *Snapshot) overviewText(b *strings.Builder) {
	state := "complete visible page"
	if s.Partial() {
		state = "partial evidence"
	}
	fmt.Fprintf(b, "STORAGE STAGES | %s\nPVCs %s | PVs %s | Pods %s\n", state,
		s.retainedCount(SourcePVCs, len(s.PVCs)), s.retainedCount(SourcePVs, len(s.PVs)), s.retainedCount(SourcePods, len(s.Pods)))
	stages := s.Stages()
	for _, name := range []string{StageBind, StageAttach, StageMount, StageResize} {
		var statuses []string
		for i := range stages {
			if stages[i].Name == name && !contains(statuses, stages[i].Status) {
				statuses = append(statuses, stages[i].Status)
			}
		}
		summary := unknownValue
		if len(statuses) > 0 {
			summary = strings.Join(statuses[:min(2, len(statuses))], "; ")
		}
		fmt.Fprintf(b, "%s: %s\n", name, summary)
	}
	b.WriteString("Capacity is provisioned/reported size; usage and free space are N/A.\n")
	b.WriteString("Bound/attached does not establish a mounted filesystem.\n\nNext evidence checks\n")
	checks := 0
	shownStages := make(map[string]bool)
	for i := range stages {
		stage := &stages[i]
		if shownStages[stage.Name] || stage.Name == StageBind || stage.Status == "attached=true" || stage.Status == "not required by driver" {
			continue
		}
		shownStages[stage.Name] = true
		fmt.Fprintf(b, "- %s %s: %s; review %s.\n", stage.Name, stage.Identity.Name, stage.Status, stageTab(stage.Name))
		checks++
		if checks == 3 {
			break
		}
	}
	for _, gap := range s.Gaps[:min(2, len(s.Gaps))] {
		fmt.Fprintf(b, "- %s\n", gap)
	}
	if checks == 0 && len(s.Gaps) == 0 {
		b.WriteString("- Review placement, driver prerequisites and full source coverage; absence of a retained failure is not a health verdict.\n")
	}
	b.WriteString("\ne previews one PVC expansion; no write occurs before explicit confirmation.\n")
}

func (s *Snapshot) retainedCount(source string, count int) string {
	switch s.Source(source).State {
	case capacity.Complete, capacity.Empty:
		return fmt.Sprintf("%d retained", count)
	case capacity.Partial:
		return fmt.Sprintf("%d retained (partial)", count)
	default:
		return unknownValue
	}
}
func (s *Snapshot) claimText(b *strings.Builder) {
	fmt.Fprintf(b, "CLAIMS AND CONTROLLER PROGRESS | PVC source %s\n", s.Source(SourcePVCs).State)
	b.WriteString("Requests, status capacity and filesystem progress are separate; capacity is not usage.\n")
	for i := range s.PVCs {
		p := &s.PVCs[i]
		fmt.Fprintf(b, "\nPVC %s/%s | UID %s | version %s\nPhase: %s | PV %s | class %s\n",
			p.Identity.Namespace, p.Identity.Name, orUnknown(p.Identity.UID), orUnknown(p.ResourceVersion), p.Phase, orUnknown(p.Volume), classLabel(p.Class))
		fmt.Fprintf(b, "Requested storage: %s\nStatus capacity: %s | usage N/A\nAccess modes: %v | volume mode: %s\n",
			capacity.Quantity(p.Requests, corev1.ResourceStorage), capacity.Quantity(p.Capacity, corev1.ResourceStorage), p.AccessModes, volumeMode(p.VolumeMode))
		if p.Deleting {
			b.WriteString("Deletion is pending; expansion is unavailable.\n")
		}
		for _, condition := range p.Conditions {
			fmt.Fprintf(b, "%s=%s | %s | at %s\n  %s\n", condition.Type, condition.Status, condition.Reason, stamp(condition.LastTransitionTime.Time), condition.Message)
		}
		for _, name := range slices.Sorted(maps.Keys(p.AllocatedStatuses)) {
			status := p.AllocatedStatuses[name]
			fmt.Fprintf(b, "Allocated resource status: %s=%s\n", name, status)
		}
		if p.SelectedNode != "" {
			fmt.Fprintf(b, "Selected-node annotation: %s (binding/scheduling hint, not a mount observation)\n", p.SelectedNode)
		}
		for j := range s.Pods {
			pod := &s.Pods[j]
			if pod.Identity.Namespace == p.Identity.Namespace && contains(pod.Claims, p.Identity.Name) {
				fmt.Fprintf(b, "Pod reference: %s/%s | UID %s | node %s | %s\n",
					pod.Identity.Namespace,
					pod.Identity.Name,
					orUnknown(pod.Identity.UID),
					orUnknown(pod.Node),
					pod.Phase)
				for _, condition := range pod.Conditions {
					if condition.Type == corev1.PodScheduled && condition.Status != corev1.ConditionTrue {
						fmt.Fprintf(b, "  Placement %s=%s | %s\n  %s\n", condition.Type, condition.Status, condition.Reason, condition.Message)
					}
				}
			}
		}
	}
	b.WriteString("\nPod volume references are namespace/name references, not PVC owner UIDs. FilesystemResizePending may " +
		"complete online; no restart is automatically recommended.\n")
}
func (s *Snapshot) topologyText(b *strings.Builder) {
	fmt.Fprintf(b, "BINDING AND TOPOLOGY | PV %s | StorageClass %s\n", s.Source(SourcePVs).State, s.Source(SourceClasses).State)
	for i := range s.PVs {
		pv := &s.PVs[i]
		fmt.Fprintf(b, "\nPV %s | UID %s | %s\nCapacity: %s | class %s | reclaim %s\nType %s | driver %s | mode %s\n",
			pv.Identity.Name, orUnknown(pv.Identity.UID), pv.Phase, capacity.ResourceText(pv.Capacity), orUnknown(pv.Class),
			pv.ReclaimPolicy, pv.VolumeKind, orUnknown(pv.Driver), volumeMode(pv.VolumeMode))
		if pv.Claim != nil {
			fmt.Fprintf(b, "Claim reference: %s/%s | UID %s\n", pv.Claim.Namespace, pv.Claim.Name, orUnknown(string(pv.Claim.UID)))
		}
		if pv.NodeAffinity != nil && pv.NodeAffinity.Required != nil {
			b.WriteString("PV required node affinity (OR between terms; AND within a term):\n")
			for termIndex, term := range pv.NodeAffinity.Required.NodeSelectorTerms {
				fmt.Fprintf(b, "  Term %d\n", termIndex+1)
				for _, match := range term.MatchExpressions {
					fmt.Fprintf(b, "    label %s %s %v\n", match.Key, match.Operator, match.Values)
				}
				for _, match := range term.MatchFields {
					fmt.Fprintf(b, "    field %s %s %v\n", match.Key, match.Operator, match.Values)
				}
			}
		}
	}
	for i := range s.Classes {
		c := &s.Classes[i]
		binding := "default Immediate"
		if c.BindingMode != nil {
			binding = string(*c.BindingMode)
		}
		fmt.Fprintf(b, "\nStorageClass %s | UID %s | version %s\nProvisioner: %s\nBinding: %s | allow expansion: %s\n",
			c.Identity.Name, orUnknown(c.Identity.UID), orUnknown(c.ResourceVersion), orUnknown(c.Provisioner), binding, optionalBool(c.AllowExpansion, "default false"))
		for termIndex, term := range c.AllowedTopologies {
			fmt.Fprintf(b, "Allowed topology term %d\n", termIndex+1)
			for _, match := range term.MatchLabelExpressions {
				fmt.Fprintf(b, "  %s in %v\n", match.Key, match.Values)
			}
		}
	}
	b.WriteString("\nNode labels and scheduler policy are not collected here; these terms do not prove placement compatibility. " +
		"No default StorageClass is inferred from a missing/empty claim class.\n")
}
func (s *Snapshot) csiText(b *strings.Builder) {
	fmt.Fprintf(b, "CSI AND ATTACHMENT | drivers %s | nodes %s | attachments %s\n",
		s.Source(SourceDrivers).State,
		s.Source(SourceCSINodes).State,
		s.Source(SourceAttachments).State)
	b.WriteString("Driver metadata does not report ControllerExpandVolume/NodeExpandVolume runtime capabilities. StorageClass " +
		"permission is configuration, not successful expansion.\n")
	for i := range s.Drivers {
		d := &s.Drivers[i]
		fmt.Fprintf(b, "\nCSIDriver %s | UID %s\nattachRequired: %s | lifecycle modes %v\n",
			d.Identity.Name,
			orUnknown(d.Identity.UID),
			optionalBool(d.AttachRequired, "default true"),
			d.LifecycleModes)
		if d.FSGroupPolicy != nil {
			fmt.Fprintf(b, "FSGroupPolicy: %s\n", *d.FSGroupPolicy)
		}
	}
	for i := range s.CSINodes {
		n := &s.CSINodes[i]
		fmt.Fprintf(b, "\nCSINode %s | UID %s\n", n.Identity.Name, orUnknown(n.Identity.UID))
		for _, d := range n.Drivers {
			limit := "not reported"
			if d.Allocatable != nil && d.Allocatable.Count != nil {
				limit = fmt.Sprint(*d.Allocatable.Count)
			}
			fmt.Fprintf(b, "Driver %s | topology keys %v | allocatable volume count %s\n", d.Name, d.TopologyKeys, limit)
		}
	}
	for i := range s.Attachments {
		a := &s.Attachments[i]
		attached := unknownValue
		if a.StatusReported {
			attached = fmt.Sprint(a.Attached)
		}
		fmt.Fprintf(b, "\nVolumeAttachment %s | UID %s\nPV name %s | node %s | attacher %s | attached %s\n",
			a.Identity.Name,
			orUnknown(a.Identity.UID),
			orUnknown(a.Volume),
			orUnknown(a.Node),
			orUnknown(a.Driver),
			attached)
		if a.AttachError != nil {
			fmt.Fprintf(b, "Attach error at %s: %s\n", stamp(a.AttachError.Time.Time), a.AttachError.Message)
		}
		if a.DetachError != nil {
			fmt.Fprintf(b, "Detach error at %s: %s\n", stamp(a.DetachError.Time.Time), a.DetachError.Message)
		}
	}
	b.WriteString("\nAttachment-to-PV and Pod-to-node associations use names. attachRequired=false permits no VolumeAttachment. " +
		"Missing/denied driver evidence does not establish that attachment is required or a volume is mounted.\n")
}
func (s *Snapshot) evidenceText(b *strings.Builder) {
	id := &s.Scope.Identity
	fmt.Fprintf(b, "READ-ONLY STORAGE SNAPSHOT\nContext: %s\nSelected: %s %s/%s\nUID: %s\nCaptured: %s\nPod namespace: %s\n",
		id.Context, id.GVR, id.Namespace, id.Name, orUnknown(id.UID), stamp(s.CapturedAt), orAll(s.Scope.Namespace))
	b.WriteString("Independent reads are not atomic. Reference UID matches verify only the observed binding relationship. No " +
		"Secret API is queried; credentials, CSI attributes, mount options and storage parameters are excluded.\n")
	for _, c := range s.Coverage {
		fmt.Fprintf(b, "\n%s: %s | read %s | visible %d | bound %d\n%s\n", c.Source, c.State, stamp(c.ReadAt), c.Visible, c.Bound, c.Detail)
	}
	for _, gap := range s.Gaps {
		fmt.Fprintf(b, "Coverage gap: %s\n", gap)
	}
	for i := range s.Events {
		e := &s.Events[i]
		fmt.Fprintf(b, "\nEvent %s | UID %s | at %s | count %d\nAbout %s %s/%s | UID %s\n%s %s: %s\n",
			e.Identity.Name, orUnknown(e.Identity.UID), stamp(e.At), e.Count, e.About.Kind, e.About.Namespace, e.About.Name,
			orUnknown(string(e.About.UID)), e.Type, e.Reason, e.Message)
	}
	b.WriteString("\nRetained events are not a complete history. Capacity is not usage/free space. API-accepted expansion is not " +
		"controller or filesystem completion; inspect fresh capacity, conditions and allocated resource statuses.\n")
}
func stageTab(stage string) string {
	if stage == StageAttach {
		return "CSI"
	}
	if stage == StageMount {
		return "Evidence"
	}
	return "Claims"
}
func classLabel(name *string) string {
	if name == nil {
		return "not reported (no default inferred)"
	}
	if *name == "" {
		return "explicit none"
	}
	return *name
}
func volumeMode(mode *corev1.PersistentVolumeMode) string {
	if mode == nil {
		return "default Filesystem"
	}
	return string(*mode)
}
func optionalBool(value *bool, fallback string) string {
	if value == nil {
		return fallback
	}
	return fmt.Sprint(*value)
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func orUnknown(value string) string {
	if value == "" {
		return unknownValue
	}
	return value
}
func orAll(value string) string {
	if value == "" {
		return "all (bounded page)"
	}
	return value
}
func stamp(at time.Time) string {
	if at.IsZero() {
		return "not reported"
	}
	return at.UTC().Format(time.RFC3339)
}
