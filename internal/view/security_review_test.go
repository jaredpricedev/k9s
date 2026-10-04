// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSecurityReviewLoadsOnlyCapturedObjectAndProjectsAllowlistedDeclarations(t *testing.T) {
	stamp := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"namespace": "apps", "name": "api-0", "uid": "pod-uid", "resourceVersion": "42", "annotations": map[string]any{"secret": "annotation-must-not-escape"}},
		"spec": map[string]any{"hostNetwork": true, "serviceAccountName": "runtime", "securityContext": map[string]any{"runAsUser": int64(1000), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"volumes":             []any{map[string]any{"name": "host-data", "hostPath": map[string]any{"path": "/var/lib/data", "type": "Directory"}}},
			"initContainers":      []any{map[string]any{"name": "init", "image": "example/init:v1", "securityContext": map[string]any{"privileged": true, "capabilities": map[string]any{"add": []any{"SYS_ADMIN"}}}}},
			"containers":          []any{map[string]any{"name": "api", "image": "example/api:v2", "env": []any{map[string]any{"name": "TOKEN", "value": "never-retain-this"}}, "volumeMounts": []any{map[string]any{"name": "host-data", "mountPath": "/data"}}, "securityContext": map[string]any{"readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false}}},
			"ephemeralContainers": []any{map[string]any{"name": "debug", "image": "example/debug:v1"}}},
		"status": map[string]any{"initContainerStatuses": []any{map[string]any{"name": "init", "imageID": "runtime://init-sha"}}, "containerStatuses": []any{map[string]any{"name": "api", "imageID": "runtime://api-sha"}}, "ephemeralContainerStatuses": []any{map[string]any{"name": "debug", "imageID": "runtime://debug-sha"}}},
	}}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"}, object)
	target := SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: "apps", Name: "api-0", UID: types.UID("pod-uid")}
	snapshot, err := loadSecurityDeclarations(t.Context(), dyn, target, stamp)
	require.NoError(t, err)
	require.Equal(t, stamp, snapshot.Identity.CapturedAt)
	require.Equal(t, "42", snapshot.Identity.ResourceVersion)
	require.Equal(t, "true", snapshot.HostNetwork)
	require.Equal(t, "runtime", snapshot.ServiceAccount)
	require.Len(t, snapshot.Containers, 3)
	require.Equal(t, "init", snapshot.Containers[0].Class)
	require.Equal(t, "true", snapshot.Containers[0].Privileged)
	require.Equal(t, []string{"SYS_ADMIN"}, snapshot.Containers[0].CapabilitiesAdd)
	require.Equal(t, "1000", snapshot.Containers[0].RunAsUser, "pod-level declaration fills an omitted container value")
	require.Equal(t, "runtime://api-sha", snapshot.Containers[1].ImageID)
	require.Contains(t, snapshot.Containers[1].Mounts[0], "readOnly=not declared", "omitted mount defaults remain unknown")
	require.Equal(t, "runtime://debug-sha", snapshot.Containers[2].ImageID)
	require.Equal(t, "not declared", snapshot.Containers[2].Privileged)
	require.False(t, snapshot.Containers[2].CapabilitiesAddDeclared)
	require.Len(t, snapshot.Volumes, 1)
	require.Contains(t, snapshot.Volumes[0], "/var/lib/data")
	require.Len(t, dyn.Actions(), 1)
	require.NotContains(t, fmt.Sprintf("%+v", snapshot), "annotation-must-not-escape")
	require.NotContains(t, fmt.Sprintf("%+v", snapshot), "never-retain-this")
	action := dyn.Actions()[0].(ktesting.GetAction)
	require.Equal(t, "apps", action.GetNamespace())
	require.Equal(t, "api-0", action.GetName())
	require.Equal(t, "get", dyn.Actions()[0].GetVerb())
}

func TestSecurityReviewRejectsReplacedUIDAndDoesNotList(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "apps", "name": "api-0", "uid": "replacement"}, "spec": map[string]any{}}}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"}, object)
	target := SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: "apps", Name: "api-0", UID: types.UID("old")}
	_, err := loadSecurityDeclarations(t.Context(), dyn, target, time.Now())
	require.ErrorIs(t, err, errSecurityIdentityChanged)
	require.Len(t, dyn.Actions(), 1)
	require.Equal(t, "get", dyn.Actions()[0].GetVerb())
}

func TestSecurityReviewTargetAndCanceledGET(t *testing.T) {
	base := SelectedResourceTarget{Context: "c", GVR: client.PodGVR, Namespace: "apps", Name: "pod", UID: types.UID("uid")}
	require.NoError(t, securityReviewTargetError(base))
	cluster := base
	cluster.Namespace = ""
	require.ErrorContains(t, securityReviewTargetError(cluster), "namespaced")
	unsupported := base
	unsupported.GVR = client.NodeGVR
	require.ErrorContains(t, securityReviewTargetError(unsupported), "native Pod")
	noUID := base
	noUID.UID = ""
	require.ErrorContains(t, securityReviewTargetError(noUID), "UID unavailable")

	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := loadSecurityDeclarations(ctx, dyn, base, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, dyn.Actions(), "canceled work must not submit a read")

	dyn = fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"})
	dyn.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "pod")
	})
	_, err = loadSecurityDeclarations(t.Context(), dyn, base, time.Now())
	require.True(t, apierrors.IsNotFound(err), "proxy/API 404 remains a not-found source failure")
	require.Len(t, dyn.Actions(), 1)
}

func TestSecurityReviewProjectionSkipsUnlistedPayloadAndUsesCronJobTemplate(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"uid": "uid", "resourceVersion": "9"}, "secret": "must-not-appear",
		"spec": map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "worker", "image": "repo/worker:tag"}},
		}}}}},
	}}
	target := SelectedResourceTarget{Context: "c", GVR: client.CjGVR, Namespace: "apps", Name: "worker", UID: types.UID("uid")}
	snapshot := projectSecurityDeclarations(target, obj, time.Now())
	require.Len(t, snapshot.Containers, 1)
	require.Equal(t, "repo/worker:tag", snapshot.Containers[0].Image)
	require.Empty(t, snapshot.Containers[0].ImageID)
	require.Equal(t, "not declared", snapshot.HostIPC)
}

func TestSecurityReviewNativeTabsSearchSafetyAndResponsiveFrames(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	view := &securityReviewView{Details: NewDetails(app, securityReviewTitle, "apps/api-0", contentInspection, true),
		target: SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR, Namespace: "apps", Name: "api-0", UID: types.UID("uid")}, revision: app.Config.DestinationRevision()}
	require.NoError(t, view.Init(t.Context()))
	t.Cleanup(view.Stop)
	view.snapshot = securityDeclarationSnapshot{Identity: securityReviewIdentity{Context: "ctx", GVR: "v1/pods", Namespace: "apps", Name: "api-0", UID: "uid", ResourceVersion: "1", CapturedAt: time.Now()},
		HostNetwork: "true", ServiceAccount: "default", Coverage: []string{"declarations captured"}, Containers: []securityContainerFacts{{Class: "app", Name: "api", Image: "repo/api:[red]unsafe", Privileged: "not declared", ImageID: "unknown"}}}
	view.render()
	action, ok := view.actions.Get(ui.Key2)
	require.True(t, ok)
	action.Action(tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone))
	require.Equal(t, 1, view.activeTab)
	require.Contains(t, view.text.GetText(true), "Omitted fields remain unknown")
	view.cmdBuff.SetActive(true)
	event := tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone)
	action, ok = view.actions.Get(ui.Key3)
	require.True(t, ok)
	require.Same(t, event, action.Action(event), "numeric search input must not switch tabs")
	require.Equal(t, 1, view.activeTab)
	view.cmdBuff.SetActive(false)
	view.activeTab = 0
	view.render()
	require.Contains(t, view.text.GetText(true), "repo/api:[red]unsafe", "API text remains literal; no tview markup interpretation")
	for _, size := range []struct{ width, height int }{{40, 12}, {80, 24}} {
		screen := tcell.NewSimulationScreen("UTF-8")
		require.NoError(t, screen.Init())
		screen.SetSize(size.width, size.height)
		view.SetRect(0, 0, size.width, size.height)
		view.Draw(screen)
		var frame strings.Builder
		for y := range size.height {
			for x := range size.width {
				r, _, _, _ := screen.GetContent(x, y)
				frame.WriteRune(r)
			}
			frame.WriteByte('\n')
		}
		screen.Fini()
		require.Contains(t, frame.String(), "Facts", "%dx%d", size.width, size.height)
		require.Contains(t, frame.String(), "hostNetwork", "%dx%d", size.width, size.height)
	}
}

func TestSecurityRefreshPublishesSuccessfulReadAndRetainsEvidenceAfterFailure(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	app.App.Init()
	require.NoError(t, app.Content.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR, Namespace: "apps", Name: "api-0", UID: "uid"}
	v := &securityReviewView{Details: NewDetails(app, securityReviewTitle, target.Path(), contentInspection, true), target: target, revision: app.Config.DestinationRevision()}
	v.snapshot.Identity = securityReviewIdentity{Context: target.Context, Namespace: target.Namespace, Name: target.Name, UID: "uid", ResourceVersion: "retained", CapturedAt: time.Now()}
	v.loader = func(context.Context, SelectedResourceTarget) (securityDeclarationSnapshot, error) {
		return securityDeclarationSnapshot{Identity: securityReviewIdentity{Context: target.Context, Namespace: target.Namespace, Name: target.Name, UID: "uid", ResourceVersion: "fresh", CapturedAt: time.Now()}, HostNetwork: "false"}, nil
	}
	require.NoError(t, app.inject(v, false))
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	app.SetScreen(screen).SetRoot(app.Content, true)
	app.SetRunning(true)
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	t.Cleanup(func() { app.SetRunning(false); app.Application.Stop(); <-finished; v.Stop() })
	app.Application.QueueUpdateDraw(v.refresh)
	waitSecurityUpdate(t, app, func() bool { return v.snapshot.Identity.ResourceVersion == "fresh" })
	var rendered string
	app.Application.QueueUpdateDraw(func() { rendered = v.text.GetText(true) })
	require.Contains(t, rendered, "RV fresh")
	require.Contains(t, rendered, "hostNetwork: false")
	app.Application.QueueUpdateDraw(func() {
		v.loader = func(context.Context, SelectedResourceTarget) (securityDeclarationSnapshot, error) {
			return securityDeclarationSnapshot{}, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "api-0")
		}
		v.refresh()
	})
	waitSecurityUpdate(t, app, func() bool { return strings.Contains(v.status, "Refresh failed") })
	app.Application.QueueUpdateDraw(func() { rendered = v.text.GetText(true) })
	require.Contains(t, rendered, "RV fresh")
	require.Contains(t, rendered, "absence unverified")
	require.Contains(t, rendered, "not refreshed")
}

func waitSecurityUpdate(t *testing.T, app *App, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		done := false
		app.Application.QueueUpdateDraw(func() { done = ready() })
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("security read never reached the native UI dispatcher")
}

func TestSecurityScalarProjectionRejectsUnexpectedStructuredValues(t *testing.T) {
	spec := map[string]any{"securityContext": map[string]any{"runAsUser": map[string]any{"secret": "never-retain"}, "procMount": "Default\nforged heading"}}
	require.Equal(t, securityNotDeclared, effectiveDeclaredScalar(spec, nil, "runAsUser"))
	require.Equal(t, "Default forged heading", effectiveDeclaredScalar(spec, nil, "procMount"))
}

func TestSecurityReviewRejectsMismatchedNamedReplyEvenWhenUIDMatches(t *testing.T) {
	target := SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: "apps", Name: "api-0", UID: types.UID("pod-uid")}
	for _, mismatch := range []string{"name", "wrong namespace", "kind", "apiVersion"} {
		t.Run(mismatch, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Pod",
				"metadata": map[string]any{"name": target.Name, "namespace": target.Namespace, "uid": string(target.UID)},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": "unverified", "image": "must-not-retain"}}},
			}}
			switch mismatch {
			case "name":
				object.SetName("other")
			case "wrong namespace":
				object.SetNamespace("other")
			case "kind":
				object.SetKind("Secret")
			case "apiVersion":
				object.SetAPIVersion("other/v1")
			}
			dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
			dyn.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, object, nil
			})
			snapshot, err := loadSecurityDeclarations(t.Context(), dyn, target, time.Now())
			require.ErrorIs(t, err, errSecurityIdentityChanged)
			require.Empty(t, snapshot.Containers)
			require.Len(t, dyn.Actions(), 1)
		})
	}
}
