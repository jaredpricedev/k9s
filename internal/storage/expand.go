// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"encoding/json"
	"fmt"

	"github.com/derailed/k9s/internal/inspect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ExpansionPlan retains the exact reviewed PVC version and related identities.
// Applying it requests a larger spec budget; it does not prove resize progress.
type ExpansionPlan struct {
	PVC, PV, Class           inspect.ResourceIdentity
	PVCVersion, ClassVersion string
	Previous, Requested      resource.Quantity
	Capacity                 resource.Quantity
	CapacityReported         bool
}

func (s *Snapshot) Expansion(namespace, name, quantity string) (*ExpansionPlan, error) {
	pvc := s.Claim(namespace, name)
	if pvc == nil {
		return nil, fmt.Errorf("PVC is not retained in this captured storage scope")
	}
	if pvc.Identity.UID == "" || pvc.ResourceVersion == "" {
		return nil, fmt.Errorf("PVC UID/resourceVersion unavailable; refresh before expansion")
	}
	if pvc.Deleting {
		return nil, fmt.Errorf("PVC is being deleted; expansion is unavailable")
	}
	if pvc.Phase != corev1.ClaimBound {
		return nil, fmt.Errorf("PVC must be Bound before this expansion workflow")
	}
	if pvc.Class == nil || *pvc.Class == "" {
		return nil, fmt.Errorf("PVC has no reported StorageClass; no default class is inferred")
	}
	class := s.Class(*pvc.Class)
	if class == nil {
		return nil, fmt.Errorf("StorageClass evidence unavailable (%s); expansion support is unknown", s.Source(SourceClasses).State)
	}
	if class.AllowExpansion == nil || !*class.AllowExpansion {
		return nil, fmt.Errorf("StorageClass does not explicitly allow volume expansion")
	}
	if class.Provisioner == "" {
		return nil, fmt.Errorf("StorageClass provisioner is not reported; expansion support is unknown")
	}
	if class.Identity.UID == "" || class.ResourceVersion == "" {
		return nil, fmt.Errorf("StorageClass UID/resourceVersion unavailable; refresh before expansion")
	}
	pv := s.Volume(pvc.Volume)
	if pv == nil || !bindingVerified(pvc, pv) {
		return nil, fmt.Errorf("PVC -> PV claim UID binding is not verified; refresh readable PV evidence before expansion")
	}
	previous, found := pvc.Requests[corev1.ResourceStorage]
	if !found || previous.Sign() <= 0 {
		return nil, fmt.Errorf("Current requested storage is unknown or invalid")
	}
	requested, err := resource.ParseQuantity(quantity)
	if err != nil {
		return nil, fmt.Errorf("Enter a Kubernetes storage quantity, for example 20Gi: %w", err)
	}
	if requested.Cmp(previous) <= 0 {
		return nil, fmt.Errorf("Expansion must exceed the current request %s; shrink/equal requests are rejected", previous.String())
	}
	plan := &ExpansionPlan{PVC: pvc.Identity, PV: pv.Identity, Class: class.Identity, PVCVersion: pvc.ResourceVersion, ClassVersion: class.ResourceVersion,
		Previous: previous.DeepCopy(), Requested: requested.DeepCopy()}
	plan.Capacity, plan.CapacityReported = pvc.Capacity[corev1.ResourceStorage]
	plan.Capacity = plan.Capacity.DeepCopy()
	return plan, nil
}

// Patch atomically conditions the write on the reviewed PVC UID and version.
// Cross-resource SC/PV checks still require a fresh read immediately before it.
func (p *ExpansionPlan) Patch() ([]byte, error) {
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": p.PVC.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": p.PVCVersion},
		{"op": "replace", "path": "/spec/resources/requests/storage", "value": p.Requested.String()},
	})
}

// Recheck validates fresh reads against the reviewed immutable plan. PVC and
// class version changes require another preview; expansion is never replanned
// or retried silently. The patch still tests the PVC UID/version atomically.
func (p *ExpansionPlan) Recheck(pvc, pv, class *unstructured.Unstructured) error {
	if pvc == nil || pv == nil || class == nil {
		return fmt.Errorf("Fresh PVC/PV/StorageClass evidence is unavailable")
	}
	if string(pvc.GetUID()) != p.PVC.UID || pvc.GetResourceVersion() != p.PVCVersion {
		return fmt.Errorf("Reviewed PVC UID/resourceVersion changed; refresh and preview again")
	}
	if string(class.GetUID()) != p.Class.UID || class.GetResourceVersion() != p.ClassVersion {
		return fmt.Errorf("Reviewed StorageClass UID/resourceVersion changed; refresh and preview again")
	}
	if string(pv.GetUID()) != p.PV.UID {
		return fmt.Errorf("Reviewed PV UID changed; refresh and preview again")
	}
	s := &Snapshot{Scope: Scope{Identity: p.PVC, Namespace: p.PVC.Namespace, PVCName: p.PVC.Name,
		PVName: p.PV.Name, ClassName: p.Class.Name}}
	s.addPVC(pvc)
	s.addPV(pv)
	s.addClass(class)
	current, err := s.Expansion(p.PVC.Namespace, p.PVC.Name, p.Requested.String())
	if err != nil {
		return err
	}
	if current.Previous.Cmp(p.Previous) != 0 || current.PV.Name != p.PV.Name || current.Class.Name != p.Class.Name {
		return fmt.Errorf("Reviewed PVC request or binding changed; refresh and preview again")
	}
	return nil
}
func (p *ExpansionPlan) Preview() string {
	actual := "unknown"
	if p.CapacityReported {
		actual = p.Capacity.String()
	}
	return fmt.Sprintf("Requested: %s -> %s\nObserved capacity: %s (not usage/free space)\n"+
		"PVC %s/%s | UID %s | resourceVersion %s\nStorageClass %s | UID %s | version %s\nPV %s | UID %s\n\n"+
		"Only the PVC requested-storage field changes. API acceptance is not controller or filesystem completion. "+
		"Refresh storage evidence for capacity, conditions and allocated resize status. "+
		"Driver expansion capability is not established by CSIDriver metadata. "+
		"No restart is implied by FilesystemResizePending. Cross-resource reads are not atomic. "+
		"This workflow excludes failed-expansion recovery that lowers a failed request while remaining above status capacity.",
		p.Previous.String(), p.Requested.String(), actual, p.PVC.Namespace, p.PVC.Name, p.PVC.UID, p.PVCVersion,
		p.Class.Name, p.Class.UID, p.ClassVersion, p.PV.Name, p.PV.UID)
}
