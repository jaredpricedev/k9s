// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/task"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const taskTestVersion = "v1"
const taskTestAPI, taskTestPods, taskTestName, taskTestNS = "api", "pods", "name", "apps"

func TestLinearTaskOfflineReplayAndPartialExit(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "1")
	b := inspect.NewBundle([]inspect.Observation{{Identity: inspect.ResourceIdentity{Context: "retained", GVR: "v1/pods", Namespace: taskTestNS, Name: taskTestAPI},
		Source: "selected API", ObservedAt: time.Now(), State: inspect.ObservationDenied, Reason: "Read denied"}})
	path := filepath.Join(t.TempDir(), "evidence.json")
	require.NoError(t, inspect.SaveBundle(path, b))
	for _, allowPartial := range []bool{false, true} {
		command := taskCmd()
		var output bytes.Buffer
		command.SetOut(&output)
		args := []string{"evidence", path, "--output", "json"}
		if allowPartial {
			args = append(args, "--allow-partial")
		}
		command.SetArgs(args)
		err := command.Execute()
		if allowPartial {
			require.NoError(t, err)
		} else {
			var status taskStatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, 2, status.ExitCode())
		}
		var report task.Report
		require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		require.False(t, report.Complete)
		require.Equal(t, "retained", report.Context)
		require.NotContains(t, output.String(), "\x1b")
	}
}

func TestLinearInvestigationCapturesUIDAndSeparatesPreviousExit(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: taskTestVersion, Resource: taskTestPods}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": taskTestVersion, "kind": "Pod",
		"metadata": map[string]any{taskTestName: taskTestAPI, "namespace": taskTestNS, "uid": "pod-a"},
		"status": map[string]any{"containerStatuses": []any{map[string]any{
			taskTestName: "app", "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
			"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": int64(137)}},
		}}},
	}}
	reader := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Version: taskTestVersion, Resource: "events"}: "EventList",
	}, pod)
	reader.PrependReactor("list", "events", func(action ktesting.Action) (bool, runtime.Object, error) {
		list := action.(ktesting.ListAction)
		require.Equal(t, taskTestNS, action.GetNamespace())
		require.Equal(t, "involvedObject.uid=pod-a", list.GetListRestrictions().Fields.String())
		return true, &unstructured.UnstructuredList{}, nil
	})
	scope := workspace.Scope{Context: "demo", Namespaces: []string{taskTestNS}}
	r := collectTaskInvestigation(context.Background(), reader, gvr, scope, taskTestAPI)
	require.True(t, r.Complete, "logs and metrics are explicitly outside this requested task")
	require.Equal(t, "CURRENT CrashLoopBackOff", r.Facts[0].State)
	require.Equal(t, "PREVIOUS OOMKilled", r.Facts[1].State)
	require.Equal(t, "pod-a", r.Facts[0].Resource.UID)
	for _, action := range reader.Actions() {
		require.Contains(t, []string{"get", "list"}, action.GetVerb())
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
}

func TestLinearInvestigationRejectsChangedIdentityAndDeniedRead(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: taskTestVersion, Resource: taskTestPods}
	scope := workspace.Scope{Context: "demo", Namespaces: []string{taskTestNS}}
	for _, denied := range []bool{false, true} {
		reader := fake.NewSimpleDynamicClient(runtime.NewScheme())
		reader.PrependReactor("get", taskTestPods, func(ktesting.Action) (bool, runtime.Object, error) {
			if denied {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: taskTestPods}, taskTestAPI, nil)
			}
			return true, &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{
				taskTestName: taskTestAPI, "namespace": "different", "uid": "replacement",
			}}}, nil
		})
		r := collectTaskInvestigation(context.Background(), reader, gvr, scope, taskTestAPI)
		require.False(t, r.Complete)
		require.Len(t, reader.Actions(), 1, "invalid or denied resource must not trigger event reads")
	}
}

func TestLinearTaskRejectsInvalidFormatBeforeReads(t *testing.T) {
	command := taskCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"evidence", "missing", "--output", "ansi"})
	require.ErrorContains(t, command.Execute(), "output must be text or json")
	require.Empty(t, out.String())
	_, _, err := workspace.ResolveKind("Secret")
	require.Error(t, err)
}
