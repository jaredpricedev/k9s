// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func controllerTestWorkload(kind string) *unstructured.Unstructured {
	o := rolloutTestDeployment()
	o.SetKind(kind)
	_ = unstructured.SetNestedField(o.Object, "RollingUpdate", "spec", "updateStrategy", "type")
	if kind == rolloutStatefulSet {
		_ = unstructured.SetNestedField(o.Object, "api-2", "status", "currentRevision")
		_ = unstructured.SetNestedField(o.Object, "api-2", "status", "updateRevision")
	} else {
		for _, key := range []string{"desiredNumberScheduled", "updatedNumberScheduled", "currentNumberScheduled", "numberReady", "numberAvailable"} {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", key)
		}
		_ = unstructured.SetNestedField(o.Object, int64(0), "status", "numberMisscheduled")
	}
	return o
}

func TestControllerRolloutStrategyAndObservedGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, kind, state string
		change            func(*unstructured.Unstructured)
	}{
		{"stateful complete", rolloutStatefulSet, RolloutComplete, func(*unstructured.Unstructured) {}},
		{"daemon complete", rolloutDaemonSet, RolloutComplete, func(*unstructured.Unstructured) {}},
		{"partition target only", rolloutStatefulSet, RolloutComplete, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(1), "spec", "updateStrategy", "rollingUpdate", "partition")
			_ = unstructured.SetNestedField(o.Object, int64(1), "status", "updatedReplicas")
			_ = unstructured.SetNestedField(o.Object, "api-old", "status", "currentRevision")
		}},
		{"on delete", rolloutStatefulSet, RolloutManual, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "OnDelete", "spec", "updateStrategy", "type")
			_ = unstructured.SetNestedField(o.Object, int64(5), "spec", "updateStrategy", "rollingUpdate", "partition")
			_ = unstructured.SetNestedField(o.Object, int64(1), "status", "updatedReplicas")
		}},
		{"revision mismatch", rolloutStatefulSet, RolloutProgressing, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "api-old", "status", "currentRevision")
		}},
		{"missing revision", rolloutStatefulSet, RolloutUnknown, func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "updateRevision")
		}},
		{"missing availability", rolloutStatefulSet, RolloutUnknown, func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "availableReplicas")
		}},
		{"missing misscheduled", rolloutDaemonSet, RolloutUnknown, func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "numberMisscheduled")
		}},
		{"misscheduled", rolloutDaemonSet, RolloutProgressing, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(1), "status", "numberMisscheduled")
		}},
		{"old failure", rolloutDaemonSet, RolloutProgressing, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", "observedGeneration")
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Failed", "status": "True"}}, "status", "conditions")
		}},
		{"current failure", rolloutStatefulSet, RolloutBlocked, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Failed", "status": "True"}}, "status", "conditions")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := controllerTestWorkload(tc.kind)
			tc.change(o)
			s := NewRolloutSnapshot(o, nil, nil, []RolloutCoverage{{Source: "Pods", State: inspect.ObservationDenied}}, "lab", time.Now())
			state, reason := s.Progress()
			require.Equal(t, tc.state, state)
			require.NotEmpty(t, reason)
			require.Equal(t, inspect.ObservationDenied, s.Coverage[0].State)
			if tc.name == "partition target only" {
				require.Contains(t, reason, "lower ordinals")
			}
		})
	}
}

func TestControllerRevisionOwnershipDigestAndConfigurationEvidence(t *testing.T) {
	o := controllerTestWorkload(rolloutStatefulSet)
	_ = unstructured.SetNestedStringMap(o.Object, map[string]string{"checksum/config": strings.Repeat("ab", 32), "checksum/private": "private-not-a-checksum"}, "spec", "template", "metadata", "annotations")
	_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"name": "config", "configMap": map[string]any{"name": "settings"}}, map[string]any{"name": "private", "secret": map[string]any{"secretName": "credentials"}}}, "spec", "template", "spec", "volumes")
	template, _, _ := unstructured.NestedMap(o.Object, "spec", "template")
	template["$patch"] = "replace"
	r := &unstructured.Unstructured{Object: map[string]any{"apiVersion": rolloutAppsAPI, "kind": rolloutControllerRevision,
		"metadata": map[string]any{"name": "api-2", "namespace": rolloutTestNamespace, "uid": "revision-uid", "resourceVersion": "17"}, "revision": int64(2), "data": map[string]any{"spec": map[string]any{"template": template}}}}
	r.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutStatefulSet, rolloutTestUID, true)})
	foreign := r.DeepCopy()
	foreign.SetUID("foreign")
	foreign.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutStatefulSet, "replaced-uid", true)})
	p := rolloutTestPod("direct-owned", "pod-uid", rolloutTestUID)
	p.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutStatefulSet, rolloutTestUID, true)})
	s := NewRolloutSnapshot(o, []*unstructured.Unstructured{r, foreign}, []*unstructured.Unstructured{p}, nil, "lab", time.Now())
	require.Len(t, s.Revisions, 1)
	require.Len(t, s.Pods, 1)
	require.Equal(t, rolloutStatefulSet, s.Pods[0].OwnerKind)
	require.Empty(t, s.Pods[0].ReplicaSetUID)
	require.True(t, s.Revisions[0].CurrentKnown)
	require.True(t, s.Revisions[0].Current)
	require.Len(t, s.TemplateSHA256, 64)
	require.Equal(t, s.TemplateSHA256, s.Revisions[0].TemplateSHA256)
	require.Equal(t, "17", s.Revisions[0].ResourceVersion)
	require.Len(t, s.Configuration, 3)
	require.Equal(t, strings.Repeat("ab", 32), s.Configuration[0].Revision)
	encoded, err := json.Marshal(s)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-command-argument")
	// ConfigMap/Secret bodies and live resource revisions are never read here.
	for _, config := range s.Configuration {
		require.Contains(t, config.State, "unverified")
	}
	retained, _, _ := unstructured.NestedMap(r.Object, "data", "spec", "template")
	require.Equal(t, "replace", retained["$patch"], "Revision source must remain immutable")
	preview := RolloutRecoveryPreview(s, "revision-uid")
	require.True(t, preview.Comparison.Comparable)
	require.Equal(t, RolloutNotExecuted, preview.State)
}

func TestStatefulSetPartitionUsesAbsoluteOrdinals(t *testing.T) {
	for _, tc := range []struct {
		name               string
		partition, updated int64
		state              string
	}{
		{"below range needs every Pod", 4, 2, RolloutProgressing},
		{"at first ordinal needs every Pod", 5, 2, RolloutProgressing},
		{"within range missing updates", 6, 0, RolloutProgressing},
		{"within range target observed", 6, 2, RolloutComplete},
		{"above range no Pods targeted", 8, 0, RolloutComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := controllerTestWorkload(rolloutStatefulSet)
			_ = unstructured.SetNestedField(o.Object, int64(5), "spec", "ordinals", "start")
			_ = unstructured.SetNestedField(o.Object, tc.partition, "spec", "updateStrategy", "rollingUpdate", "partition")
			_ = unstructured.SetNestedField(o.Object, int64(3), "spec", "replicas")
			for _, field := range []string{"replicas", "readyReplicas", "availableReplicas"} {
				_ = unstructured.SetNestedField(o.Object, int64(3), "status", field)
			}
			_ = unstructured.SetNestedField(o.Object, tc.updated, "status", "updatedReplicas")
			s := NewRolloutSnapshot(o, nil, nil, nil, "lab", time.Now())
			state, _ := s.Progress()
			require.Equal(t, tc.state, state)
			require.EqualValues(t, 5, *s.StartOrdinal)
		})
	}
	// Full-range updates still require current/update revisions to converge.
	o := controllerTestWorkload(rolloutStatefulSet)
	_ = unstructured.SetNestedField(o.Object, int64(5), "spec", "ordinals", "start")
	_ = unstructured.SetNestedField(o.Object, int64(5), "spec", "updateStrategy", "rollingUpdate", "partition")
	_ = unstructured.SetNestedField(o.Object, "api-old", "status", "currentRevision")
	s := NewRolloutSnapshot(o, nil, nil, nil, "lab", time.Now())
	state, _ := s.Progress()
	require.Equal(t, RolloutProgressing, state)
}
