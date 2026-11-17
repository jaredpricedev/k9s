// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/model1"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nextStepText(parts []nextStepPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.text)
	}
	return b.String()
}

// The WHY phrases are the ones render.podWhy writes; see its tests.
func TestNextStepMapsStatusEvidenceToChecks(t *testing.T) {
	pod := model1.Header{{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"}, {Name: "WHY"}}
	claim := model1.Header{{Name: "NAME"}, {Name: "STATUS"}}
	workload := model1.Header{{Name: "NAME"}, {Name: "READY"}}
	uu := map[string]struct {
		resource string
		h        model1.Header
		fields   model1.Fields
		e        string
		failing  bool
	}{
		"healthy pod":   {"pods", pod, model1.Fields{"a", "1/1", "Running", ""}, "", false},
		"completed pod": {"pods", pod, model1.Fields{"a", "0/1", "Completed", ""}, "", false},
		"starting without a message is progress, not a problem": {"pods", pod, model1.Fields{"a", "0/1", "ContainerCreating", ""}, "", false},
		"back-off after OOMKilled": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "api: last OOMKilled exit 137"},
			"next: api: last exit 137: killed (SIGKILL), memory limit (OOMKilled) · p previous logs · :pressure limit vs usage", true},
		"back-off after exit 1": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "last Error exit 1"},
			"next: last exit 1: the application's own error · p previous logs · d describe for events", true},
		"exit 2 now": {"pods", pod, model1.Fields{"a", "0/1", "Error", "Error exit 2"},
			"next: exit 2: the application's own error · l logs · d describe for events", true},
		"exit 137 without an OOM reason names both usual senders": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "last Error exit 137"},
			"next: last exit 137: killed (SIGKILL), usually the memory limit or the node · p previous logs · :pressure limit vs usage", true},
		"exit 143": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "last Error exit 143"},
			"next: last exit 143: SIGTERM, a normal stop or a failed graceful shutdown · p previous logs · d describe for events", true},
		"exit 139": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "last Error exit 139"},
			"next: last exit 139: segfault (SIGSEGV) · p previous logs · d describe for events", true},
		"exit 126": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "last Error exit 126"},
			"next: last exit 126: command not executable · d describe for the command and image · p previous logs", true},
		"exit 127": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "last ContainerCannotRun exit 127"},
			"next: last exit 127: command not found in the image · d describe for the command and image · p previous logs", true},
		"reported signal": {"pods", pod, model1.Fields{"a", "0/1", "Error", "Error signal 9"},
			"next: signal 9: killed (SIGKILL), usually the memory limit or the node · l logs · :pressure limit vs usage", true},
		"unlisted exit code is not decoded further": {"pods", pod, model1.Fields{"a", "0/1", "Error", "Error exit 42"},
			"next: exit 42: application-defined error · l logs · d describe for events", true},
		"back-off with no previous exit": {"pods", pod, model1.Fields{"a", "0/1", "CrashLoopBackOff", "back-off 5m0s restarting failed container"},
			"next: CrashLoopBackOff: no previous exit reported · p previous logs · d describe for events", true},
		"image pull back-off": {"pods", pod, model1.Fields{"a", "0/1", "ImagePullBackOff", `Back-off pulling image "x:v1"`},
			"next: ImagePullBackOff: image not pulled · d describe for the pull error · check the image name and pull secret", true},
		"image pull error in an init container": {"pods", pod, model1.Fields{"a", "0/1", "Init:ErrImagePull", "rpc error"},
			"next: ErrImagePull: image not pulled · d describe for the pull error · check the image name and pull secret", false},
		"container config error": {"pods", pod, model1.Fields{"a", "0/1", "CreateContainerConfigError", `secret "db" not found`},
			"next: CreateContainerConfigError: container configuration rejected · :config-review missing ConfigMap/Secret keys · d describe for the message", true},
		"unschedulable for resources": {"pods", pod, model1.Fields{"a", "0/1", "Pending", "0/3 nodes are available: 3 Insufficient memory."},
			"next: Unschedulable: resources, per the scheduler · :capacity requests vs allocatable · d describe for the scheduler message", false},
		"unschedulable for taints and affinity": {"pods", pod, model1.Fields{"a", "0/1", "Pending",
			"0/3 nodes are available: 1 node(s) had untolerated taint {dedicated: gpu}, 2 node(s) didn't match Pod's node affinity/selector."},
			"next: Unschedulable: taints + affinity/selector, per the scheduler · :capacity requests vs allocatable · d describe for the scheduler message", false},
		"unschedulable for an unbound claim": {"pods", pod, model1.Fields{"a", "0/1", "Pending",
			"0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims."},
			"next: Unschedulable: volumes, per the scheduler · :storage claim binding · d describe for the scheduler message", false},
		"unschedulable without a named constraint": {"pods", pod, model1.Fields{"a", "0/1", "Pending", "0/3 nodes are available."},
			"next: Unschedulable: scheduler names no constraint here · :capacity requests vs allocatable · d describe for the scheduler message", false},
		"pending with nothing reported": {"pods", pod, model1.Fields{"a", "0/1", "Pending", ""},
			"next: Pending: no scheduler message reported · d describe for events", false},
		"creating blocked on a mount": {"pods", pod, model1.Fields{"a", "0/1", "ContainerCreating", `MountVolume.SetUp failed for volume "data"`},
			"next: ContainerCreating: volume not ready, per the kubelet message · :storage bind/attach/mount stages · d describe for events", false},
		"running but not ready": {"pods", pod, model1.Fields{"a", "1/2", "Running", "sidecar: running, not ready"},
			"next: sidecar: running, not ready · d describe for the readiness probe · l logs", false},
		"evicted": {"pods", pod, model1.Fields{"a", "0/1", "Evicted", "The node was low on resource: memory."},
			"next: Evicted by the node · d describe for the eviction message · :capacity node allocatable", true},
		"pending claim": {"persistentvolumeclaims", claim, model1.Fields{"data", "Pending"},
			"next: Pending: claim not bound · :storage bind and provisioning stages · d describe for events", false},
		"bound claim": {"persistentvolumeclaims", claim, model1.Fields{"data", "Bound"}, "", false},
		"deployment short of ready": {"deployments", workload, model1.Fields{"api", "1/3"},
			"next: ready 1/3 · :rollout conditions and replicas · Enter pods · d describe for events", false},
		"deployment ready": {"deployments", workload, model1.Fields{"api", "3/3"}, "", false},
		"node not ready": {"nodes", claim, model1.Fields{"n1", "NotReady,SchedulingDisabled"},
			"next: NotReady: node condition · d describe for conditions and events", true},
		"unknown resource stays quiet": {"services", claim, model1.Fields{"svc", "Failed"}, "", false},
	}
	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			step := nextStepFor(u.resource, u.h, u.fields)
			assert.Equal(t, u.e, nextStepText(step.fit(200, false)))
			assert.Equal(t, u.e == "", step.Empty())
			assert.Equal(t, u.failing, step.Level == levelFailure)
		})
	}
}

func TestNextStepFitDropsLeastImportantPartsAndNeverWraps(t *testing.T) {
	step := podNextStep("CrashLoopBackOff", "api: last OOMKilled exit 137")
	uu := []struct {
		width int
		ascii bool
		e     string
	}{
		{120, false, "next: api: last exit 137: killed (SIGKILL), memory limit (OOMKilled) · p previous logs · :pressure limit vs usage"},
		{120, true, "next: api: last exit 137: killed (SIGKILL), memory limit (OOMKilled) | p previous logs | :pressure limit vs usage"},
		{100, false, "next: api: last exit 137: killed (SIGKILL), memory limit (OOMKilled) · p previous logs · :pressure"},
		{90, false, "next: api: last exit 137: killed (SIGKILL), memory limit (OOMKilled) · p previous logs"},
		{80, false, "next: api: last exit 137 OOMKilled · p previous logs · :pressure limit vs usage"},
		{66, false, "next: api: last exit 137 OOMKilled · p previous logs · :pressure"},
		{52, false, "next: api: last exit 137 OOMKilled · p previous logs"},
		{45, false, ""},
		{39, false, ""},
	}
	for _, u := range uu {
		got := nextStepText(step.fit(u.width, u.ascii))
		assert.Equal(t, u.e, got, "width %d", u.width)
		assert.LessOrEqual(t, runewidth.StringWidth(got), u.width)
	}
	pending := podNextStep("Pending", "0/1 nodes available: insufficient memory")
	assert.Equal(t, "next: Unschedulable: resources, per the scheduler · :capacity requests vs allocatable · d describe",
		nextStepText(pending.fit(114, false)), "details are dropped one check at a time, from the last")
	cjk := NextStep{Evidence: strings.Repeat("等", 30), Checks: []NextCheck{logsCheck(false)}}
	assert.Empty(t, cjk.fit(60, false), "width is measured in cells, not runes")
	assert.NotEmpty(t, cjk.fit(80, false))
}

func TestNextStepKeepsOnlyRunnableChecks(t *testing.T) {
	step := podNextStep("CrashLoopBackOff", "last OOMKilled exit 137")
	got := step.available(func(checks []NextCheck) []NextCheck {
		require.Len(t, checks, 2)
		return checks[:1]
	})
	assert.Equal(t, "next: last exit 137: killed (SIGKILL), memory limit (OOMKilled) · p previous logs", nextStepText(got.fit(200, false)))

	pull := podNextStep("ImagePullBackOff", "x").available(func([]NextCheck) []NextCheck { return nil })
	assert.Equal(t, "next: ImagePullBackOff: image not pulled · check the image name and pull secret", nextStepText(pull.fit(200, false)),
		"advice without a key stays; the unavailable key is not printed")

	none := step.available(func([]NextCheck) []NextCheck { return nil })
	assert.Equal(t, "next: last exit 137: killed (SIGKILL), memory limit (OOMKilled)", nextStepText(none.fit(200, false)))
}
