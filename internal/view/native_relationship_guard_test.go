// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

const (
	nativeTestNamespace       = "related-native"
	nativeContextChangedState = "context changed"
	nativeReplacement         = "replacement"
	nativeObserved            = "observed"
)

type nativeGuardConnection struct {
	client.Connection
	permissions int
}

func (c *nativeGuardConnection) CanI(string, *client.GVR, string, []string) (bool, error) {
	c.permissions++
	return false, nil
}

type nativeGuardModel struct{ mockTableModel }

func (*nativeGuardModel) GetNamespace() string { return nativeTestNamespace }

func nativeGuardFixture(t *testing.T, cachedUID types.UID) (*Browser, *nativeGuardConnection, *fake.FakeDynamicClient, string) {
	t.Helper()
	previous := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	dao.MetaAccess.RegisterMeta(client.PodGVR.String(), &metav1.APIResource{Kind: "Pod", Namespaced: true})
	t.Cleanup(func() { dao.MetaAccess = previous })
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	connection := &nativeGuardConnection{Connection: mock.NewMockConnection()}
	app.Config.SetConnection(connection)
	dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme())
	app.factory = watch.NewFactory(inspectionConnection{Connection: connection, dynamic: dynamic})
	require.NoError(t, app.factory.SetActiveNS(nativeTestNamespace))
	b := &Browser{Table: NewTable(client.PodGVR), meta: &metav1.APIResource{Kind: "Pod", Verbs: []string{"patch"}}}
	require.NoError(t, b.Table.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	b.SetModel(&nativeGuardModel{})
	b.SetCell(0, 0, tview.NewTableCell("NAME"))
	b.SetCell(1, 0, tview.NewTableCell("app").SetReference(nativeTestNamespace+"/app"))
	b.SetCell(2, 0, tview.NewTableCell("other").SetReference(nativeTestNamespace+"/other"))
	b.Select(1, 0)
	app.Content.Push(b)
	b.expectedTarget = &SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR,
		Namespace: nativeTestNamespace, Name: "app", UID: nativeObserved}
	if cachedUID != "" {
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"namespace": nativeTestNamespace, "name": "app", "uid": string(cachedUID)}}}
		store := app.factory.FactoryFor(nativeTestNamespace).ForResource(client.PodGVR.GVR()).Informer().GetStore()
		require.NoError(t, store.Add(object))
	}
	// A real kubectl executable records any accidental launch without touching a
	// cluster. The guarded handlers must never reach either authorization or exec.
	directory := t.TempDir()
	marker := filepath.Join(directory, "kubectl-called")
	t.Setenv("NATIVE_GUARD_MARKER", marker)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	//nolint:gosec // The local sentinel must be executable to detect an accidental kubectl launch.
	require.NoError(t, os.WriteFile(filepath.Join(directory, "kubectl"), []byte("#!/bin/sh\n: > \"$NATIVE_GUARD_MARKER\"\n"), 0700))
	t.Cleanup(b.Table.Stop)
	return b, connection, dynamic, marker
}

func TestNativeRelationshipHandlersKeepAnchorAndUpdater(t *testing.T) {
	for _, state := range []string{"matching", "missing cache", nativeReplacement, nativeContextChangedState, "unknown observed UID"} {
		for _, action := range []string{"yaml", "describe", "enter describe", "edit"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				uid := types.UID(nativeObserved)
				if state == "missing cache" {
					uid = ""
				} else if state == nativeReplacement || state == nativeContextChangedState {
					uid = nativeReplacement
				}
				b, connection, dynamic, marker := nativeGuardFixture(t, uid)
				if state == nativeContextChangedState {
					b.expectedTarget.Context = "prior-context"
				} else if state == "unknown observed UID" {
					b.expectedTarget.UID = ""
				}
				stopped := false
				b.cancelFn = func() { stopped = true }
				generation := b.operationGeneration.Load()
				handler := map[string]ui.ActionHandler{"yaml": b.viewCmd, "describe": b.describeCmd,
					"enter describe": b.enterCmd, "edit": b.editCmd}[action]
				require.Nil(t, handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)))
				message := <-b.App().Flash().Channel()
				expected := relatedNativeReadReason
				if action == "edit" {
					expected = relatedNativeEditReason
				}
				if state == nativeContextChangedState {
					require.Contains(t, message.Text, "Context changed")
				} else if state == nativeReplacement {
					require.Contains(t, message.Text, "Identity changed")
				} else {
					require.Equal(t, expected, message.Text)
				}
				require.False(t, stopped, "a guarded action stopped its source watcher")
				require.Equal(t, generation, b.operationGeneration.Load())
				require.Same(t, b, b.App().Content.Top())
				require.Len(t, b.App().Content.Peek(), 1, "a guarded action injected a native view")
				require.Zero(t, connection.permissions, "a guarded action requested authorization")
				require.Empty(t, dynamic.Actions(), "a UI guard issued a live API read")
				require.NoFileExists(t, marker)
			})
		}
	}
}

func TestNativeRelationshipAvailabilityMatchesHandlersAndHelp(t *testing.T) {
	b, connection, dynamic, _ := nativeGuardFixture(t, nativeObserved)
	b.refreshActions()
	connection.permissions = 0 // Ignore setup of unrelated namespace shortcuts.
	for _, key := range []tcell.Key{ui.KeyY, ui.KeyD, tcell.KeyEnter, ui.KeyE} {
		action, ok := b.Actions().Get(key)
		require.True(t, ok, "missing native action %s", tcell.KeyNames[key])
		require.NotNil(t, action.Availability)
		reason := relatedNativeReadReason
		if key == ui.KeyE {
			reason = relatedNativeEditReason
		}
		require.Equal(t, reason, action.Availability())
		found := false
		for _, descriptor := range actionCatalog(b, b.App()) {
			if descriptor.Key == key {
				found = true
				require.False(t, descriptor.Available())
				require.Equal(t, reason, descriptor.UnavailableReason)
			}
		}
		require.True(t, found)
		require.Contains(t, sharedActionHelp(b.Actions(), ui.ActionContext{}), reason)
		help := NewHelp(b.App())
		found = false
		for _, hint := range help.hints() {
			if hint.Mnemonic == tcell.KeyNames[key] {
				found = true
				require.Contains(t, hint.Description, reason)
			}
		}
		require.True(t, found)
	}
	b.Select(2, 0)
	for _, key := range []tcell.Key{ui.KeyY, ui.KeyD, tcell.KeyEnter, ui.KeyE} {
		action, _ := b.Actions().Get(key)
		require.Empty(t, action.Availability(), "an unrelated row inherited the anchor")
	}
	require.Zero(t, connection.permissions)
	require.Empty(t, dynamic.Actions())
}

func TestNativeRelationshipUnrelatedAndFreshRowsStillOpenViews(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		for _, action := range []string{"yaml", "describe", "enter describe"} {
			t.Run(action+"/fresh="+map[bool]string{false: "false", true: "true"}[fresh], func(t *testing.T) {
				b, _, _, _ := nativeGuardFixture(t, nativeObserved)
				path := nativeTestNamespace + "/other"
				if fresh {
					b.expectedTarget = nil
					path = nativeTestNamespace + "/app"
				} else {
					b.Select(2, 0)
				}
				handler := map[string]ui.ActionHandler{"yaml": b.viewCmd, "describe": b.describeCmd, "enter describe": b.enterCmd}[action]
				require.Nil(t, handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)))
				view, ok := b.App().Content.Top().(*LiveView)
				require.True(t, ok, "the normal native action did not open its view")
				require.Equal(t, path, view.model.GetPath())
				require.Empty(t, view.nativeRelationshipReason(true))
				t.Cleanup(view.Stop)
			})
		}
	}
}

func TestNativeRelationshipCustomEnterStillRuns(t *testing.T) {
	b, connection, _, _ := nativeGuardFixture(t, nativeObserved)
	called := false
	b.SetEnterFn(func(_ *App, _ ui.Tabular, _ *client.GVR, path string) {
		called = true
		require.Equal(t, nativeTestNamespace+"/app", path)
	})
	require.Empty(t, b.nativeEnterReason())
	require.Nil(t, b.enterCmd(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)))
	require.True(t, called)
	require.Zero(t, connection.permissions)
}

func TestNativeRelationshipUnrelatedEditStillAuthorizes(t *testing.T) {
	b, connection, _, marker := nativeGuardFixture(t, nativeObserved)
	b.Select(2, 0)
	stopped := false
	b.cancelFn = func() { stopped = true }
	require.Nil(t, b.editCmd(tcell.NewEventKey(ui.KeyE, 0, tcell.ModNone)))
	require.True(t, stopped, "ordinary Edit must retain its normal updater lifecycle")
	require.Equal(t, 1, connection.permissions, "the unrelated row never reached Edit authorization")
	require.Contains(t, (<-b.App().Flash().Channel()).Text, "can't edit resource")
	require.Same(t, b, b.App().Content.Top())
	require.NoFileExists(t, marker, "denied ordinary Edit must not launch kubectl")
	t.Cleanup(b.Stop)
}

func TestNativeRelationshipStaleCustomEnterAndJumpCannotDispatch(t *testing.T) {
	for _, contextChanged := range []bool{false, true} {
		for _, customJump := range []bool{false, true} {
			b, connection, dynamic, marker := nativeGuardFixture(t, nativeReplacement)
			if contextChanged {
				b.expectedTarget.Context = "prior-context"
			}
			called := false
			b.SetEnterFn(func(*App, ui.Tabular, *client.GVR, string) { called = true })
			if customJump {
				b.App().CustomJumps().Jumps[client.PodGVR.String()] = config.JumpRule{TargetGVR: client.PodGVR.String()}
			}
			b.refreshActions()
			connection.permissions = 0
			reason := b.nativeEnterReason()
			require.NotEmpty(t, reason)
			action, ok := b.Actions().Get(tcell.KeyEnter)
			require.True(t, ok)
			require.Equal(t, reason, action.Availability())
			if contextChanged {
				require.Contains(t, reason, "Context changed")
			} else {
				require.Contains(t, reason, "Identity changed")
			}
			require.Nil(t, b.enterCmd(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)))
			require.Equal(t, reason, (<-b.App().Flash().Channel()).Text)
			require.False(t, called, "the stale custom Enter hook ran")
			require.Same(t, b, b.App().Content.Top())
			require.Zero(t, connection.permissions, "the stale custom jump requested source access")
			require.Empty(t, dynamic.Actions())
			require.NoFileExists(t, marker)
		}
	}
}

func TestNativeRelationshipLiveViewCannotBypassAnchor(t *testing.T) {
	for _, uid := range []types.UID{nativeObserved, nativeReplacement} {
		t.Run(string(uid), func(t *testing.T) {
			b, connection, dynamic, marker := nativeGuardFixture(t, uid)
			view := NewLiveView(b.App(), yamlAction, model.NewYAML(client.PodGVR, nativeTestNamespace+"/app"))
			reason := view.nativeRelationshipReason(false)
			require.NotEmpty(t, reason)
			require.ErrorContains(t, b.App().inject(view, false), reason)
			require.Same(t, b, b.App().Content.Top())
			// Also guard a retained live view that was reached before an anchor
			// was installed, without stopping or relaunching its collector.
			stopped := false
			view.cancel = func() { stopped = true }
			view.bindKeys()
			action, ok := view.Actions().Get(ui.KeyE)
			require.True(t, ok)
			require.Equal(t, view.nativeRelationshipReason(true), action.Availability())
			require.Nil(t, view.editCmd(tcell.NewEventKey(ui.KeyE, 0, tcell.ModNone)))
			require.False(t, stopped)
			require.NotEmpty(t, (<-b.App().Flash().Channel()).Text)
			require.Zero(t, connection.permissions)
			require.Empty(t, dynamic.Actions())
			require.NoFileExists(t, marker)
		})
	}
}
