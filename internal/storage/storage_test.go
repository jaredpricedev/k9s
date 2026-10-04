// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/capacity"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const testClass = "fast-csi"
const testDriver = "example.test.csi"

func fixture(t *testing.T) (*Scope, []runtime.Object) {
	t.Helper()
	yes := true
	attach := false
	mode := storagev1.VolumeBindingWaitForFirstConsumer
	pvc := &corev1.PersistentVolumeClaim{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}, ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "apps", UID: "pvc-uid", ResourceVersion: "20"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "data-pv", StorageClassName: ptr(testClass), Resources: corev1.VolumeResourceRequirements{Requests: resources("12Gi")}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: resources("8Gi"), Conditions: []corev1.PersistentVolumeClaimCondition{{Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue, Reason: "AwaitingNodeExpansion", Message: "Fixture node filesystem resize pending"}}, AllocatedResourceStatuses: map[corev1.ResourceName]corev1.ClaimResourceStatus{corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending}}}
	pv := &corev1.PersistentVolume{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"}, ObjectMeta: metav1.ObjectMeta{Name: "data-pv", UID: "pv-uid", ResourceVersion: "21"}, Spec: corev1.PersistentVolumeSpec{Capacity: resources("12Gi"), StorageClassName: testClass, ClaimRef: &corev1.ObjectReference{Namespace: "apps", Name: "data", UID: "pvc-uid"}, PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: testDriver, VolumeHandle: "fixture-sensitive-volume-handle", VolumeAttributes: map[string]string{"password": "fixture-secret"}, NodePublishSecretRef: &corev1.SecretReference{Name: "fixture-secret", Namespace: "apps"}}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	class := &storagev1.StorageClass{TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"}, ObjectMeta: metav1.ObjectMeta{Name: testClass, UID: "sc-uid", ResourceVersion: "22"}, Provisioner: testDriver, AllowVolumeExpansion: &yes, VolumeBindingMode: &mode, Parameters: map[string]string{"password": "fixture-secret"}}
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: "worker", Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}}
	driver := &storagev1.CSIDriver{TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "CSIDriver"}, ObjectMeta: metav1.ObjectMeta{Name: testDriver, UID: "driver-uid"}, Spec: storagev1.CSIDriverSpec{AttachRequired: &attach}}
	event := &corev1.Event{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Event"}, ObjectMeta: metav1.ObjectMeta{Name: "mount-fault", Namespace: "apps", UID: "event-uid"}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: "api", UID: "pod-uid"}, Reason: "FailedMount", Message: "Fixture mount timeout", LastTimestamp: metav1.NewTime(time.Now())}
	scope := &Scope{Identity: inspect.ResourceIdentity{Context: "dev", GVR: "v1/persistentvolumeclaims", Namespace: "apps", Name: "data", UID: "pvc-uid"}, Kind: "PersistentVolumeClaim", Namespace: "apps", PVCName: "data", PVName: "data-pv", ClassName: testClass}
	return scope, []runtime.Object{object(t, pvc), object(t, pv), object(t, class), object(t, pod), object(t, driver), object(t, event)}
}
func object(t *testing.T, obj any) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	require.NoError(t, err)
	return &unstructured.Unstructured{Object: m}
}
func reader(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	kinds := map[schema.GroupVersionResource]string{}
	for _, source := range sources {
		kind := map[string]string{SourcePods: "Pod", SourcePVCs: "PersistentVolumeClaim", SourcePVs: "PersistentVolume", SourceClasses: "StorageClass", SourceDrivers: "CSIDriver", SourceCSINodes: "CSINode", SourceAttachments: "VolumeAttachment", SourceEvents: "Event"}[source.name]
		kinds[source.gvr] = kind + "List"
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...)
}
func resources(storage string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(storage)}
}
func ptr(s string) *string { return &s }

func TestCollectDeniedCSIStillRetainsBindingMountAndResizeEvidence(t *testing.T) {
	scope, objects := fixture(t)
	api := reader(objects...)
	api.PrependReactor("list", "csidrivers", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "storage.k8s.io", Resource: "csidrivers"}, "", errors.New("fixture driver access denied"))
	})
	snapshot := Collect(t.Context(), api, scope, time.Now())
	require.Len(t, snapshot.PVCs, 1)
	require.Len(t, snapshot.PVs, 1)
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, capacity.Denied, snapshot.Source(SourceDrivers).State)
	require.True(t, snapshot.Partial())
	require.Equal(t, capacity.NotConfigured, snapshot.Source(SourceUsage).State)
	require.Equal(t, "8Gi", capacity.Quantity(snapshot.PVCs[0].Capacity, corev1.ResourceStorage))
	overview := snapshot.Render(0)
	require.Contains(t, overview, "FileSystemResizePending")
	require.Contains(t, overview, "FailedMount")
	require.Contains(t, overview, "Mount:")
	require.Equal(t, 1, strings.Count(overview, "- Resize data:"), "one next check per stage avoids repeated resize actions")
	claims := snapshot.Render(1)
	require.Contains(t, claims, "Requested storage: 12Gi")
	require.Contains(t, claims, "Status capacity: 8Gi | usage N/A")
	require.Contains(t, claims, "NodeResizePending")
	require.Contains(t, claims, "no restart is automatically recommended")
	all := strings.Join([]string{snapshot.Render(0), snapshot.Render(1), snapshot.Render(2), snapshot.Render(3), snapshot.Render(4)}, "\n")
	require.NotContains(t, all, "fixture-secret")
	require.NotContains(t, all, "fixture-sensitive-volume-handle")
	require.Len(t, api.Actions(), 8)
	for _, action := range api.Actions() {
		require.Equal(t, "list", action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
		// client-go fake NewRootListActionWithOptions drops Limit; the real
		// transport test below verifies all eight query bounds.
		if action.GetNamespace() != "" {
			require.EqualValues(t, 101, action.(ktesting.ListActionImpl).ListOptions.Limit)
		}
	}
}

func TestExpansionPreviewIsConditionalRequestNotCompletedResize(t *testing.T) {
	scope, objects := fixture(t)
	snapshot := Collect(t.Context(), reader(objects...), scope, time.Now())
	plan, err := snapshot.Expansion("apps", "data", "15Gi")
	require.NoError(t, err)
	require.Equal(t, "12Gi", plan.Previous.String())
	require.Equal(t, "15Gi", plan.Requested.String())
	require.Equal(t, "8Gi", plan.Capacity.String())
	require.Equal(t, "20", plan.PVCVersion)
	require.Equal(t, "22", plan.ClassVersion)
	preview := plan.Preview()
	require.Contains(t, preview, "API acceptance is not controller or filesystem completion")
	require.Contains(t, preview, "No restart is implied")
	patch, err := plan.Patch()
	require.NoError(t, err)
	var operations []map[string]any
	require.NoError(t, json.Unmarshal(patch, &operations))
	require.Equal(t, []map[string]any{{"op": "test", "path": "/metadata/uid", "value": "pvc-uid"}, {"op": "test", "path": "/metadata/resourceVersion", "value": "20"}, {"op": "replace", "path": "/spec/resources/requests/storage", "value": "15Gi"}}, operations)
	require.Equal(t, "12Gi", capacity.Quantity(snapshot.PVCs[0].Requests, corev1.ResourceStorage), "preview never mutates observed configuration")
}

func TestDeniedClaimsAndPodsRemainUnknownWithoutSuppressingReadablePV(t *testing.T) {
	scope, objects := fixture(t)
	api := reader(objects...)
	for _, source := range []string{"persistentvolumeclaims", "pods"} {
		api.PrependReactor("list", source, func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "", errors.New("fixture access denied"))
		})
	}
	snapshot := Collect(t.Context(), api, scope, time.Now())
	require.Len(t, snapshot.PVs, 1)
	require.Len(t, snapshot.Classes, 1)
	require.Len(t, api.Actions(), 8)
	overview := snapshot.Render(0)
	require.Contains(t, overview, "PVCs unknown | PVs 1 retained | Pods unknown")
	require.Contains(t, overview, "Bind: unknown")
	require.True(t, snapshot.Partial())
}

func TestExpansionRejectsShrinkUnsupportedAndUnverifiedIdentity(t *testing.T) {
	for _, test := range []struct {
		name, quantity, reason string
		mutate                 func(*Snapshot)
	}{
		{"shrink", "10Gi", "shrink/equal", nil}, {"equal", "12Gi", "shrink/equal", nil}, {"invalid", "letters", "Kubernetes storage quantity", nil},
		{"pending", "15Gi", "must be Bound", func(s *Snapshot) { s.PVCs[0].Phase = corev1.ClaimPending }},
		{"missing provisioner", "15Gi", "provisioner is not reported", func(s *Snapshot) { s.Classes[0].Provisioner = "" }},
		{"unsupported", "15Gi", "does not explicitly allow", func(s *Snapshot) { no := false; s.Classes[0].AllowExpansion = &no }},
		{"unreported support", "15Gi", "does not explicitly allow", func(s *Snapshot) { s.Classes[0].AllowExpansion = nil }},
		{"missing claim UID", "15Gi", "UID/resourceVersion", func(s *Snapshot) { s.PVCs[0].Identity.UID = "" }},
		{"missing version", "15Gi", "UID/resourceVersion", func(s *Snapshot) { s.PVCs[0].ResourceVersion = "" }},
		{"no class", "15Gi", "no default class", func(s *Snapshot) { s.PVCs[0].Class = nil }},
		{"recreated binding", "15Gi", "claim UID binding", func(s *Snapshot) { s.PVs[0].Claim.UID = "replacement" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope, objects := fixture(t)
			snapshot := Collect(t.Context(), reader(objects...), scope, time.Now())
			if test.mutate != nil {
				test.mutate(snapshot)
			}
			_, err := snapshot.Expansion("apps", "data", test.quantity)
			require.ErrorContains(t, err, test.reason)
		})
	}
}

func TestDeferredBindingAndNoAttachDriverAreNotInventedFailures(t *testing.T) {
	scope, objects := fixture(t)
	snapshot := Collect(t.Context(), reader(objects...), scope, time.Now())
	require.Contains(t, snapshot.Render(0), "Attach: not required by driver")
	snapshot.PVCs[0].Phase = corev1.ClaimPending
	snapshot.PVCs[0].Volume = ""
	snapshot.PVCs[0].Conditions = nil
	snapshot.PVCs[0].AllocatedStatuses = nil
	var deferred *Stage
	for _, stage := range snapshot.Stages() {
		if stage.Name == StageBind {
			deferred = &stage
			break
		}
	}
	require.NotNil(t, deferred)
	require.Equal(t, "deferred / Pending", deferred.Status)
	require.Contains(t, deferred.Detail, "Pending alone is not failure")
	require.Contains(t, snapshot.Render(3), "attachRequired=false permits no VolumeAttachment")
}

func TestScopeNamesDoNotJoinAcrossNamespacesOrUnrelatedUnboundPV(t *testing.T) {
	scope, objects := fixture(t)
	snapshot := Collect(t.Context(), reader(objects...), scope, time.Now())
	other := snapshot.Pods[0]
	other.Identity.Namespace = "other"
	other.Identity.UID = "other-pod"
	snapshot.Pods = append(snapshot.Pods, other)
	snapshot.Scope = Scope{ClassName: testClass}
	snapshot.join()
	require.Len(t, snapshot.Pods, 1)
	require.Equal(t, "apps", snapshot.Pods[0].Identity.Namespace)
	snapshot.Scope = Scope{Kind: "PersistentVolume", PVName: "unbound-other-pv"}
	snapshot.join()
	require.Empty(t, snapshot.PVCs)
	require.Empty(t, snapshot.Pods)
}

func TestBoundedSourceAndReplacementRemainPartial(t *testing.T) {
	scope, objects := fixture(t)
	api := reader(objects...)
	api.PrependReactor("list", "persistentvolumeclaims", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*objects[0].(*unstructured.Unstructured)}}, nil
	})
	replaced := objects[0].(*unstructured.Unstructured)
	replaced.SetUID("replacement")
	snapshot := Collect(t.Context(), api, scope, time.Now())
	require.Empty(t, snapshot.PVCs)
	require.Equal(t, capacity.Partial, snapshot.Source(SourcePVCs).State)
	api = reader(objects...)
	api.PrependReactor("list", "volumeattachments", func(ktesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		list.SetContinue("more")
		return true, list, nil
	})
	snapshot = Collect(t.Context(), api, scope, time.Now())
	require.Equal(t, capacity.Partial, snapshot.Source(SourceAttachments).State)
	require.True(t, snapshot.Partial())
}
