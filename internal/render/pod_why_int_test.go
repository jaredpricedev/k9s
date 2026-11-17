// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
)

func TestPodWhyReportsOnlyStatusEvidence(t *testing.T) {
	one := &v1.PodSpec{Containers: []v1.Container{{Name: "api"}}}
	two := &v1.PodSpec{Containers: []v1.Container{{Name: "api"}, {Name: "sidecar"}}}
	oom := v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}
	uu := map[string]struct {
		spec *v1.PodSpec
		st   v1.PodStatus
		e    string
	}{
		"healthy": {one, v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{
			{Name: "api", Ready: true, RestartCount: 9, State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}, LastTerminationState: oom}}}, ""},
		"backoff names the previous exit": {two, v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{Name: "api", State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: oom}}}, "api: last OOMKilled exit 137"},
		"image pull message on one line": {one, v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{Name: "api", State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling\n image \"x:v1\""}}}}}, `Back-off pulling image "x:v1"`},
		"unschedulable": {one, v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{
			{Type: v1.PodScheduled, Status: v1.ConditionFalse, Message: "0/3 nodes available: insufficient memory"}}}, "0/3 nodes available: insufficient memory"},
		"evicted": {one, v1.PodStatus{Reason: "Evicted", Message: "The node was low on resource: memory."}, "The node was low on resource: memory."},
		"failed exit": {one, v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{Name: "api", State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 2}}}}}, "Error exit 2"},
		"running but not ready": {two, v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{
			{Name: "api", Ready: true, State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
			{Name: "sidecar", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}}}}, "sidecar: running, not ready"},
		"creating without a message": {one, v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{Name: "api", State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}}, ""},
	}
	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			assert.Equal(t, u.e, podWhy(u.spec, &u.st))
		})
	}
}
