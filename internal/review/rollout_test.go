// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const (
	rolloutTestNamespace = "apps"
	rolloutTestName      = "api"
	rolloutTestUID       = "deployment-uid"
	rolloutTestImage     = "example.test/api:v2"
	rolloutTestOldImage  = "example.test/api:v1"
)

func rolloutTestDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": rolloutAppsAPI, "kind": rolloutDeployment,
		"metadata": map[string]any{"name": rolloutTestName, "namespace": rolloutTestNamespace, "uid": rolloutTestUID, "generation": int64(3)},
		"spec": map[string]any{"replicas": int64(2), "template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": rolloutTestName}},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": rolloutTestName, "image": rolloutTestImage, "args": []any{"private-command-argument"}}}},
		}},
		"status": map[string]any{"observedGeneration": int64(3), "replicas": int64(2), "updatedReplicas": int64(2), "readyReplicas": int64(2), "availableReplicas": int64(2)},
	}}
}

func rolloutTestOwner(kind, uid string, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: rolloutAppsAPI, Kind: kind, Name: rolloutTestName, UID: types.UID(uid), Controller: &controller}
}

func rolloutTestRevision(name, uid, image string) *unstructured.Unstructured {
	deployment := rolloutTestDeployment()
	template, _, _ := unstructured.NestedMap(deployment.Object, "spec", "template")
	containers, _, _ := unstructured.NestedSlice(template, "spec", "containers")
	containers[0].(map[string]any)["image"] = image
	_ = unstructured.SetNestedSlice(template, containers, "spec", "containers")
	_ = unstructured.SetNestedField(template, "revision-hash", "metadata", "labels", rolloutTemplateHash)
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": rolloutAppsAPI, "kind": rolloutReplicaSet,
		"metadata": map[string]any{"name": name, "namespace": rolloutTestNamespace, "uid": uid, "annotations": map[string]any{"deployment.kubernetes.io/revision": "999"}},
		"spec":     map[string]any{"template": template}, "status": map[string]any{"replicas": int64(1), "readyReplicas": int64(1)},
	}}
	object.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutDeployment, rolloutTestUID, true)})
	return object
}

func rolloutTestPod(name, uid, owner string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": rolloutTestNamespace, "uid": uid},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": rolloutTestName, "image": rolloutTestImage}}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{
			"name": rolloutTestName, "imageID": "containerd://sha256:observed-digest", "ready": true, "restartCount": int64(0),
			"state": map[string]any{"running": map[string]any{}},
		}}},
	}}
	object.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutReplicaSet, owner, true)})
	return object
}

func TestRolloutProgressGenerationAndUnknownCounts(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		change      func(*unstructured.Unstructured)
	}{
		{"complete", RolloutComplete, func(*unstructured.Unstructured) {}},
		{"old generation hides old failure", RolloutProgressing, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", "observedGeneration")
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}}, "status", "conditions")
		}},
		{"blocked", RolloutBlocked, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}}, "status", "conditions")
		}},
		{"paused", RolloutPaused, func(o *unstructured.Unstructured) { _ = unstructured.SetNestedField(o.Object, true, "spec", "paused") }},
		{"ready missing", RolloutUnknown, func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "readyReplicas")
		}},
		{"generation missing", RolloutUnknown, func(o *unstructured.Unstructured) { unstructured.RemoveNestedField(o.Object, "metadata", "generation") }},
		{"future observed generation", RolloutUnknown, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(4), "status", "observedGeneration")
		}},
		{"negative count", RolloutUnknown, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(-1), "status", "readyReplicas")
		}},
		{"old replicas remain", RolloutProgressing, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(3), "status", "replicas")
		}},
		{"scaled zero", RolloutComplete, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(0), "spec", "replicas")
			for _, key := range []string{"replicas", "updatedReplicas", "readyReplicas", "availableReplicas"} {
				_ = unstructured.SetNestedField(o.Object, int64(0), "status", key)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := rolloutTestDeployment()
			tc.change(o)
			s := NewRolloutSnapshot(o, nil, nil, []RolloutCoverage{{Source: "Pods", State: inspect.ObservationDenied, Detail: "forbidden"}}, "lab", time.Now())
			state, reason := s.Progress()
			if state != tc.state || reason == "" {
				t.Fatal(state, reason, s)
			}
			if len(s.Coverage) == 0 || s.Coverage[0].State != inspect.ObservationDenied {
				t.Fatal("child visibility was overwritten", s.Coverage)
			}
		})
	}
}

func TestRolloutOwnershipAndTemplateSelection(t *testing.T) {
	current := rolloutTestRevision("api-new", "rs-current", rolloutTestImage)
	old := rolloutTestRevision("api-old", "rs-old", rolloutTestOldImage)
	unowned := rolloutTestRevision("api-unowned", "rs-unowned", rolloutTestImage)
	unowned.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutDeployment, rolloutTestUID, false)})
	replaced := rolloutTestRevision("api-replaced", "rs-replaced", rolloutTestImage)
	replaced.SetOwnerReferences([]metav1.OwnerReference{rolloutTestOwner(rolloutDeployment, "replacement-deployment", true)})
	pod := rolloutTestPod("api-pod", "pod-current", "rs-current")
	stranger := rolloutTestPod("stranger", "pod-stranger", "rs-replaced")
	s := NewRolloutSnapshot(rolloutTestDeployment(), []*unstructured.Unstructured{old, current, unowned, replaced}, []*unstructured.Unstructured{pod, stranger}, nil, "lab", time.Now())
	if len(s.Revisions) != 2 || len(s.Pods) != 1 || !s.Revisions[0].Current || s.Revisions[1].Current || !s.Revisions[1].CurrentKnown {
		t.Fatal("recency or selectors replaced UID/template evidence", s)
	}
	image := s.Pods[0].Images[0]
	if image.Declared != rolloutTestImage || image.ImageID != "containerd://sha256:observed-digest" || image.Ready == nil || !*image.Ready || image.Restarts == nil || *image.Restarts != 0 {
		t.Fatal("declared image and resolved digest were conflated", image)
	}
	if s.Revisions[0].Available != nil {
		t.Fatal("absent availability became zero")
	}
	labels, _, _ := unstructured.NestedStringMap(current.Object, "spec", "template", "metadata", "labels")
	if labels[rolloutTemplateHash] != "revision-hash" {
		t.Fatal("source template changed")
	}
}

func TestRolloutOwnerReferenceUsesNativeGroupAndUIDAcrossAPIVersions(t *testing.T) {
	legacy := rolloutTestRevision("legacy-owner-version", "legacy-rs", rolloutTestImage)
	ref := rolloutTestOwner(rolloutDeployment, rolloutTestUID, true)
	ref.APIVersion = "apps/v1beta1"
	legacy.SetOwnerReferences([]metav1.OwnerReference{ref})
	forged := rolloutTestRevision("forged-owner-group", "forged-rs", rolloutTestImage)
	ref.APIVersion = "example.test/v1"
	forged.SetOwnerReferences([]metav1.OwnerReference{ref})
	pod := rolloutTestPod("legacy-rs-pod", "legacy-pod", "legacy-rs")
	ref = rolloutTestOwner(rolloutReplicaSet, "legacy-rs", true)
	ref.APIVersion = "apps/v1beta2"
	pod.SetOwnerReferences([]metav1.OwnerReference{ref})
	s := NewRolloutSnapshot(rolloutTestDeployment(), []*unstructured.Unstructured{legacy, forged}, []*unstructured.Unstructured{pod}, nil, "lab", time.Now())
	if len(s.Revisions) != 1 || s.Revisions[0].Identity.UID != "legacy-rs" || len(s.Pods) != 1 || s.Pods[0].Identity.UID != "legacy-pod" {
		t.Fatal("owner version changed native group/UID ownership", s)
	}
}

func TestRolloutRecoveryIsSelectedRetainedAndRedacted(t *testing.T) {
	deployment := rolloutTestDeployment()
	old := rolloutTestRevision("api-old", "old-target", rolloutTestOldImage)
	s := NewRolloutSnapshot(deployment, []*unstructured.Unstructured{old}, nil, nil, "lab", time.Now())
	preview := RolloutRecoveryPreview(s, "old-target")
	if preview.State != RolloutNotExecuted || preview.RevisionIdentity.UID != "old-target" || !preview.Comparison.Comparable || len(preview.Comparison.Changes) != 1 || len(preview.Limits) == 0 {
		t.Fatal(preview)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-command-argument") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("raw arguments retained", string(encoded))
	}
	_ = unstructured.SetNestedField(deployment.Object, int64(99), "spec", "replicas")
	_ = unstructured.SetNestedField(old.Object, "changed-after-capture", "metadata", "annotations", "deployment.kubernetes.io/revision")
	if s.Desired == nil || *s.Desired != 2 || RolloutRecoveryPreview(s, "old-target").Revision != "999" {
		t.Fatal("caller mutation changed retained evidence")
	}
	missing := RolloutRecoveryPreview(s, "not-retained")
	if missing.State != RolloutNotExecuted || missing.Comparison.Comparable {
		t.Fatal("implicit replacement recovery target chosen", missing)
	}
}

func TestRolloutMatchingDoesNotTrustRedactedEquality(t *testing.T) {
	deployment := rolloutTestDeployment()
	revision := rolloutTestRevision("same-image", "different-args", rolloutTestImage)
	containers, _, _ := unstructured.NestedSlice(revision.Object, "spec", "template", "spec", "containers")
	containers[0].(map[string]any)["args"] = []any{"different-private-argument"}
	_ = unstructured.SetNestedSlice(revision.Object, containers, "spec", "template", "spec", "containers")
	s := NewRolloutSnapshot(deployment, []*unstructured.Unstructured{revision}, nil, nil, "lab", time.Now())
	if s.Revisions[0].Current || !s.Revisions[0].CurrentKnown {
		t.Fatal("redacted templates established current revision")
	}
	preview := RolloutRecoveryPreview(s, "different-args")
	if len(preview.Comparison.Changes) != 0 || len(preview.Comparison.A.Limits) == 0 || len(preview.Limits) == 0 {
		t.Fatal("excluded differences not marked unreviewed", preview)
	}
}

func TestRolloutBoundsAndMissingIdentity(t *testing.T) {
	deployment := rolloutTestDeployment()
	var revisions, pods []*unstructured.Unstructured
	for i := range MaxRolloutRevisions + 3 {
		revisions = append(revisions, rolloutTestRevision(fmt.Sprintf("rs-%03d", i), fmt.Sprintf("uid-%03d", i), rolloutTestImage))
	}
	for i := range MaxRolloutPods + 3 {
		pods = append(pods, rolloutTestPod(fmt.Sprintf("pod-%03d", i), fmt.Sprintf("pod-uid-%03d", i), "uid-000"))
	}
	s := NewRolloutSnapshot(deployment, revisions, pods, nil, "lab", time.Now())
	if len(s.Revisions) != MaxRolloutRevisions || len(s.Pods) != MaxRolloutPods {
		t.Fatal("retained collection was unbounded", s)
	}
	for _, source := range []string{"ReplicaSets", "Pods"} {
		bounded := false
		for _, coverage := range s.Coverage {
			if coverage.Source == source && coverage.State == inspect.ObservationIncomplete {
				bounded = true
			}
		}
		if !bounded {
			t.Fatal("collection cap lost its source coverage", source, s.Coverage)
		}
	}
	deployment.SetUID("")
	s = NewRolloutSnapshot(deployment, revisions, pods, nil, "lab", time.Now())
	state, _ := s.Progress()
	if state != RolloutUnknown || len(s.Revisions) != 0 || len(s.Pods) != 0 || len(s.Coverage) != 1 {
		t.Fatal("name-only descendant attribution", s)
	}
}

func TestRolloutTemplateMatchingCoverageDoesNotChooseCurrentRevision(t *testing.T) {
	for _, tc := range []struct {
		name, matchingState, detail string
		revisions                   []*unstructured.Unstructured
	}{
		{"no match", inspect.ObservationComplete, "0 exact API template matches", []*unstructured.Unstructured{
			rolloutTestRevision("old", "old", rolloutTestOldImage),
		}},
		{"multiple matches", inspect.ObservationComplete, "multiple matches cannot choose", []*unstructured.Unstructured{
			rolloutTestRevision("same-a", "same-a", rolloutTestImage), rolloutTestRevision("same-b", "same-b", rolloutTestImage),
		}},
		{"unavailable template", inspect.ObservationIncomplete, "template comparisons were unavailable", []*unstructured.Unstructured{
			rolloutTestRevision("missing", "missing", rolloutTestImage),
		}},
		{"no retained set", inspect.ObservationUnknown, "0 exact API template matches", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unavailable template" {
				unstructured.RemoveNestedField(tc.revisions[0].Object, "spec", "template")
			}
			s := NewRolloutSnapshot(rolloutTestDeployment(), tc.revisions, nil, nil, "lab", time.Now())
			matching, chosen := false, false
			for _, coverage := range s.Coverage {
				if coverage.Source == "Template matching" && coverage.State == tc.matchingState && strings.Contains(coverage.Detail, tc.detail) {
					matching = true
				}
				if coverage.Source == "Current revision" && coverage.State == inspect.ObservationUnknown {
					chosen = true
				}
			}
			if !matching || !chosen {
				t.Fatal("template equality became controller-selected current revision", s.Coverage)
			}
		})
	}
}
