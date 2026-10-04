// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package dao

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkspaceLogEntryRejectsReplacementBeforeOpeningStreams(t *testing.T) {
	pod := followTestPod("target", "replacement", 0)
	k := fake.NewClientset(pod)
	opened := false
	_, err := followPodLogs(t.Context(), k, "ns", metav1.ListOptions{FieldSelector: "metadata.name=target"},
		&LogOptions{InitialPodUID: "selected"}, func(context.Context, *v1.Pod, *LogOptions, *v1.PodLogOptions) (io.ReadCloser, error) {
			opened = true
			return nil, nil
		})
	require.ErrorContains(t, err, "identity changed")
	require.False(t, opened)
	for _, action := range k.Actions() {
		require.Equal(t, "list", action.GetVerb(), "identity mismatch must not start a watch or log stream")
	}
}
