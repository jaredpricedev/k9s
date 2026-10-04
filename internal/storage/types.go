// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/inspect"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

const (
	SourcePods        = "Pods"
	SourcePVCs        = "PVCs"
	SourcePVs         = "PVs"
	SourceClasses     = "StorageClasses"
	SourceDrivers     = "CSIDrivers"
	SourceCSINodes    = "CSINodes"
	SourceAttachments = "VolumeAttachments"
	SourceEvents      = "Events"
	SourceUsage       = "Volume usage"
)

// Scope is captured from one UID-checked selection and the opening namespace.
// References between independent reads are labeled; name references are not UIDs.
type Scope struct {
	Identity                                                   inspect.ResourceIdentity
	Kind, Namespace, PodName, PVCName, PVName, ClassName, Node string
	PVCUID                                                     string
}

type Pod struct {
	Identity    inspect.ResourceIdentity
	Node, Phase string
	Claims      []string
	Conditions  []corev1.PodCondition
}

type PVC struct {
	Identity           inspect.ResourceIdentity
	ResourceVersion    string
	Requests, Capacity corev1.ResourceList
	Phase              corev1.PersistentVolumeClaimPhase
	Volume             string
	Class              *string
	AccessModes        []corev1.PersistentVolumeAccessMode
	VolumeMode         *corev1.PersistentVolumeMode
	Conditions         []corev1.PersistentVolumeClaimCondition
	AllocatedStatuses  map[corev1.ResourceName]corev1.ClaimResourceStatus
	SelectedNode       string
	Deleting           bool
}

type PV struct {
	Identity                  inspect.ResourceIdentity
	ResourceVersion           string
	Capacity                  corev1.ResourceList
	Phase                     corev1.PersistentVolumePhase
	Class, Driver, VolumeKind string
	Claim                     *corev1.ObjectReference
	NodeAffinity              *corev1.VolumeNodeAffinity
	ReclaimPolicy             corev1.PersistentVolumeReclaimPolicy
	AccessModes               []corev1.PersistentVolumeAccessMode
	VolumeMode                *corev1.PersistentVolumeMode
}

type Class struct {
	Identity          inspect.ResourceIdentity
	ResourceVersion   string
	Provisioner       string
	AllowExpansion    *bool
	BindingMode       *storagev1.VolumeBindingMode
	AllowedTopologies []corev1.TopologySelectorTerm
}

type Driver struct {
	Identity       inspect.ResourceIdentity
	AttachRequired *bool
	LifecycleModes []storagev1.VolumeLifecycleMode
	FSGroupPolicy  *storagev1.FSGroupPolicy
}

type CSINode struct {
	Identity inspect.ResourceIdentity
	Drivers  []NodeDriver
}

// NodeDriver excludes the CSI-specific node identifier from retained evidence.
type NodeDriver struct {
	Name         string
	TopologyKeys []string
	Allocatable  *storagev1.VolumeNodeResources
}

type Attachment struct {
	Identity                 inspect.ResourceIdentity
	Volume, Node, Driver     string
	Attached, StatusReported bool
	AttachError, DetachError *storagev1.VolumeError
}

type Event struct {
	Identity              inspect.ResourceIdentity
	About                 corev1.ObjectReference
	Reason, Message, Type string
	At                    time.Time
	Count                 int32
}

type Stage struct {
	Name, Status, Detail string
	Identity             inspect.ResourceIdentity
	At                   time.Time
}

type Snapshot struct {
	Scope         Scope
	CapturedAt    time.Time
	Pods          []Pod
	PVCs          []PVC
	PVs           []PV
	Classes       []Class
	Drivers       []Driver
	CSINodes      []CSINode
	Attachments   []Attachment
	Events        []Event
	Coverage      []capacity.Coverage
	Gaps          []string
	selectionSeen bool
}

func (s *Snapshot) Partial() bool {
	if len(s.Gaps) > 0 {
		return true
	}
	for _, c := range s.Coverage {
		if c.State == capacity.Absent && (c.Source == SourceDrivers || c.Source == SourceCSINodes || c.Source == SourceAttachments) {
			continue
		}
		if c.State != capacity.Complete && c.State != capacity.Empty && c.State != capacity.NotConfigured {
			return true
		}
	}
	return false
}

func (s *Snapshot) Source(name string) capacity.Coverage {
	for _, c := range s.Coverage {
		if c.Source == name {
			return c
		}
	}
	return capacity.Coverage{Source: name, State: capacity.Unavailable, Detail: "Not collected"}
}
