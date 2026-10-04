// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func (s *Snapshot) project(reads map[string]*sourceResult) {
	for _, source := range sources {
		for _, obj := range items(reads[source.name]) {
			switch source.name {
			case SourcePods:
				s.addPod(&obj)
			case SourcePVCs:
				s.addPVC(&obj)
			case SourcePVs:
				s.addPV(&obj)
			case SourceClasses:
				s.addClass(&obj)
			case SourceDrivers:
				s.addDriver(&obj)
			case SourceCSINodes:
				s.addCSINode(&obj)
			case SourceAttachments:
				s.addAttachment(&obj)
			case SourceEvents:
				s.addEvent(&obj)
			}
		}
	}
}
func (s *Snapshot) selected(source, kind, name, uid string) bool {
	if s.Scope.Kind == kind && s.Scope.Identity.Name == name {
		if s.Scope.Identity.UID != uid {
			s.decodeFailure(source, fmt.Errorf("selected %s identity changed during collection", kind))
			return false
		}
		s.selectionSeen = true
	}
	return true
}
func (s *Snapshot) addPod(obj *unstructured.Unstructured) {
	var raw corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourcePods, err)
		return
	}
	if s.Scope.Namespace != "" && raw.Namespace != s.Scope.Namespace {
		return
	}
	if s.Scope.PodName != "" && raw.Name != s.Scope.PodName {
		return
	}
	if !s.selected(SourcePods, "Pod", raw.Name, string(raw.UID)) {
		return
	}
	pod := Pod{Identity: s.identity("v1/pods", &raw), Node: raw.Spec.NodeName, Phase: string(raw.Status.Phase), Conditions: raw.Status.Conditions}
	for i := range raw.Spec.Volumes {
		volume := &raw.Spec.Volumes[i]
		if volume.PersistentVolumeClaim != nil {
			pod.Claims = append(pod.Claims, volume.PersistentVolumeClaim.ClaimName)
		}
	}
	s.Pods = append(s.Pods, pod)
}
func (s *Snapshot) addPVC(obj *unstructured.Unstructured) {
	var raw corev1.PersistentVolumeClaim
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourcePVCs, err)
		return
	}
	if s.Scope.Namespace != "" && raw.Namespace != s.Scope.Namespace {
		return
	}
	if s.Scope.PVCName != "" && raw.Name != s.Scope.PVCName {
		return
	}
	if s.Scope.PVCUID != "" && string(raw.UID) != s.Scope.PVCUID {
		s.decodeFailure(SourcePVCs, fmt.Errorf("selected PVC reference UID differs from the current claim"))
		return
	}
	if !s.selected(SourcePVCs, "PersistentVolumeClaim", raw.Name, string(raw.UID)) {
		return
	}
	s.PVCs = append(s.PVCs, PVC{Identity: s.identity("v1/persistentvolumeclaims", &raw), ResourceVersion: raw.ResourceVersion,
		Requests: raw.Spec.Resources.Requests, Capacity: raw.Status.Capacity, Phase: raw.Status.Phase, Volume: raw.Spec.VolumeName,
		Class: raw.Spec.StorageClassName, AccessModes: raw.Spec.AccessModes, VolumeMode: raw.Spec.VolumeMode, Conditions: raw.Status.Conditions,
		AllocatedStatuses: raw.Status.AllocatedResourceStatuses, SelectedNode: raw.Annotations["volume.kubernetes.io/selected-node"], Deleting: raw.DeletionTimestamp != nil})
}
func (s *Snapshot) addPV(obj *unstructured.Unstructured) {
	var raw corev1.PersistentVolume
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourcePVs, err)
		return
	}
	if s.Scope.PVName != "" && raw.Name != s.Scope.PVName {
		return
	}
	if !s.selected(SourcePVs, "PersistentVolume", raw.Name, string(raw.UID)) {
		return
	}
	pv := PV{Identity: s.identity("v1/persistentvolumes", &raw), ResourceVersion: raw.ResourceVersion, Capacity: raw.Spec.Capacity,
		Phase: raw.Status.Phase, Class: raw.Spec.StorageClassName, Claim: raw.Spec.ClaimRef, NodeAffinity: raw.Spec.NodeAffinity,
		ReclaimPolicy: raw.Spec.PersistentVolumeReclaimPolicy, AccessModes: raw.Spec.AccessModes, VolumeMode: raw.Spec.VolumeMode, VolumeKind: "other/unreported"}
	switch {
	case raw.Spec.CSI != nil:
		pv.Driver = raw.Spec.CSI.Driver
		pv.VolumeKind = "CSI"
	case raw.Spec.Local != nil:
		pv.VolumeKind = "Local"
	case raw.Spec.NFS != nil:
		pv.VolumeKind = "NFS"
	case raw.Spec.HostPath != nil:
		pv.VolumeKind = "HostPath"
	}
	s.PVs = append(s.PVs, pv)
}
func (s *Snapshot) addClass(obj *unstructured.Unstructured) {
	var raw storagev1.StorageClass
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourceClasses, err)
		return
	}
	if s.Scope.ClassName != "" && raw.Name != s.Scope.ClassName {
		return
	}
	if !s.selected(SourceClasses, "StorageClass", raw.Name, string(raw.UID)) {
		return
	}
	s.Classes = append(s.Classes, Class{Identity: s.identity("storage.k8s.io/v1/storageclasses", &raw), ResourceVersion: raw.ResourceVersion,
		Provisioner: raw.Provisioner, AllowExpansion: raw.AllowVolumeExpansion, BindingMode: raw.VolumeBindingMode, AllowedTopologies: raw.AllowedTopologies})
}
func (s *Snapshot) addDriver(obj *unstructured.Unstructured) {
	var raw storagev1.CSIDriver
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourceDrivers, err)
		return
	}
	s.Drivers = append(s.Drivers, Driver{Identity: s.identity("storage.k8s.io/v1/csidrivers", &raw), AttachRequired: raw.Spec.AttachRequired,
		LifecycleModes: raw.Spec.VolumeLifecycleModes, FSGroupPolicy: raw.Spec.FSGroupPolicy})
}
func (s *Snapshot) addCSINode(obj *unstructured.Unstructured) {
	var raw storagev1.CSINode
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourceCSINodes, err)
		return
	}
	if s.Scope.Node != "" && raw.Name != s.Scope.Node {
		return
	}
	node := CSINode{Identity: s.identity("storage.k8s.io/v1/csinodes", &raw)}
	for _, driver := range raw.Spec.Drivers {
		node.Drivers = append(node.Drivers, NodeDriver{Name: driver.Name, TopologyKeys: driver.TopologyKeys, Allocatable: driver.Allocatable})
	}
	s.CSINodes = append(s.CSINodes, node)
}
func (s *Snapshot) addAttachment(obj *unstructured.Unstructured) {
	var raw storagev1.VolumeAttachment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourceAttachments, err)
		return
	}
	volume := ""
	if raw.Spec.Source.PersistentVolumeName != nil {
		volume = *raw.Spec.Source.PersistentVolumeName
	}
	_, reported, _ := unstructured.NestedBool(obj.Object, "status", "attached")
	s.Attachments = append(s.Attachments, Attachment{Identity: s.identity("storage.k8s.io/v1/volumeattachments", &raw), Volume: volume,
		Node: raw.Spec.NodeName, Driver: raw.Spec.Attacher, Attached: raw.Status.Attached, StatusReported: reported,
		AttachError: raw.Status.AttachError, DetachError: raw.Status.DetachError})
}
func (s *Snapshot) addEvent(obj *unstructured.Unstructured) {
	var raw corev1.Event
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
		s.decodeFailure(SourceEvents, err)
		return
	}
	if s.Scope.Namespace != "" && raw.Namespace != s.Scope.Namespace {
		return
	}
	at := raw.LastTimestamp.Time
	if at.IsZero() {
		at = raw.EventTime.Time
	}
	if at.IsZero() {
		at = raw.FirstTimestamp.Time
	}
	s.Events = append(s.Events, Event{Identity: s.identity("v1/events", &raw), About: raw.InvolvedObject, Reason: raw.Reason,
		Message: raw.Message, Type: raw.Type, At: at, Count: raw.Count})
}
