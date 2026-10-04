// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"fmt"
	"maps"
	"slices"

	"github.com/derailed/k9s/internal/capacity"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

const StageBind = "Bind"
const StageAttach = "Attach"
const StageMount = "Mount"
const StageResize = "Resize"

// Stages labels observed evidence, never a causal diagnosis. A Pod event may
// involve another volume, and attached/Bound never establishes a mounted FS.
func (s *Snapshot) Stages() []Stage {
	var stages []Stage
	for i := range s.PVCs {
		pvc := &s.PVCs[i]
		binding := orUnknown(string(pvc.Phase))
		detail := "Reported PVC phase; independent PV claim UID verification is separate"
		if pvc.Phase == corev1.ClaimPending && pvc.Class != nil {
			if class := s.Class(*pvc.Class); class != nil && class.BindingMode != nil && *class.BindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
				binding = "deferred / Pending"
				detail = "WaitForFirstConsumer defers binding until scheduling; Pending alone is not failure"
			}
		}
		if pv := s.Volume(pvc.Volume); pv != nil && bindingVerified(pvc, pv) {
			detail = "PVC -> PV claim namespace/name/UID verified across independent reads; mount is unknown"
		}
		stages = append(stages, Stage{Name: StageBind, Status: binding, Detail: detail, Identity: pvc.Identity})
		for _, condition := range pvc.Conditions {
			if condition.Status == corev1.ConditionTrue &&
				(condition.Type == corev1.PersistentVolumeClaimFileSystemResizePending || condition.Type == corev1.PersistentVolumeClaimResizing) {
				stages = append(stages, Stage{Name: StageResize, Status: string(condition.Type), Detail: condition.Reason + ": " + condition.Message,
					Identity: pvc.Identity, At: condition.LastTransitionTime.Time})
			}
		}
		for _, name := range slices.Sorted(maps.Keys(pvc.AllocatedStatuses)) {
			status := pvc.AllocatedStatuses[name]
			stages = append(stages, Stage{Name: StageResize, Status: string(status), Detail: "PVC allocated resource status: " + string(name), Identity: pvc.Identity})
		}
		requested, hasRequest := pvc.Requests[corev1.ResourceStorage]
		actual, hasActual := pvc.Capacity[corev1.ResourceStorage]
		if hasRequest && hasActual && requested.Cmp(actual) > 0 {
			stages = append(stages, Stage{Name: StageResize, Status: "request exceeds observed capacity", Identity: pvc.Identity,
				Detail: fmt.Sprintf("Requested %s; status capacity %s. Controller/node/filesystem progress is not established by API acceptance",
					requested.String(), actual.String())})
		}
	}
	for i := range s.Attachments {
		a := &s.Attachments[i]
		status := "unknown (attached status not reported)"
		if a.StatusReported {
			status = fmt.Sprintf("attached=%t", a.Attached)
		}
		stages = append(stages, Stage{Name: StageAttach, Status: status, Identity: a.Identity,
			Detail: "VolumeAttachment uses PV name, not PV UID; attached does not establish mount"})
		if a.AttachError != nil {
			stages = append(stages, Stage{Name: StageAttach, Status: "reported attach error", Detail: a.AttachError.Message, At: a.AttachError.Time.Time, Identity: a.Identity})
		}
		if a.DetachError != nil {
			stages = append(stages, Stage{Name: StageAttach, Status: "reported detach error", Detail: a.DetachError.Message, At: a.DetachError.Time.Time, Identity: a.Identity})
		}
	}
	for i := range s.PVs {
		pv := &s.PVs[i]
		if pv.Driver != "" {
			driver := s.Driver(pv.Driver)
			if driver != nil && driver.AttachRequired != nil && !*driver.AttachRequired {
				stages = append(stages, Stage{Name: StageAttach, Status: "not required by driver", Identity: driver.Identity,
					Detail: "CSIDriver attachRequired=false; an absent VolumeAttachment is expected, not an attachment failure"})
			}
		}
	}
	for i := range s.Events {
		e := &s.Events[i]
		stage := ""
		switch e.Reason {
		case "ProvisioningFailed", "FailedBinding", "ExternalProvisioning", "Provisioning", "ProvisioningSucceeded":
			stage = StageBind
		case "FailedAttachVolume", "SuccessfulAttachVolume", "FailedDetachVolume":
			stage = StageAttach
		case "FailedMount", "FailedMapVolume", "FailedUnmount":
			stage = StageMount
		case "VolumeResizeFailed", "FileSystemResizeFailed", "ExternalExpanding", "Resizing", "FileSystemResizeSuccessful":
			stage = StageResize
		}
		if stage != "" {
			stages = append(stages, Stage{Name: stage, Status: "event " + e.Reason,
				Detail: e.Message + " (UID-scoped retained event; Pod events may involve another volume)", Identity: e.Identity, At: e.At})
		}
	}
	if len(s.PVCs) == 0 && s.Source(SourcePVCs).State != capacity.Complete && s.Source(SourcePVCs).State != capacity.Empty {
		stages = append(stages, Stage{Name: StageBind, Status: "unknown", Detail: "PVC source " + string(s.Source(SourcePVCs).State)})
	}
	return stages
}
