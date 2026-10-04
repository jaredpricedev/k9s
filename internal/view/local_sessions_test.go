// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	localSessionTestContext   = "captured-ctx"
	localSessionTestUID       = "3717fc62-52d0-4c5a-bd5b-602a7d5a4faa"
	localSessionTestName      = "owned-pod"
	localSessionTestNamespace = "apps"
)

func localSessionUIFixture(t *testing.T) *localSessions {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	app.App.Init()
	return newLocalSessions(app)
}

func localSessionTestSpec() *session.Spec {
	return &session.Spec{Kind: session.PortForward, Label: "Pod endpoint", Binding: "127.0.0.1:19090 -> 80",
		Destination: session.Destination{Context: localSessionTestContext, GVR: client.PodGVR.String(), Namespace: localSessionTestNamespace, Name: localSessionTestName, UID: localSessionTestUID, Container: "api"}}
}

func TestLocalSessionsDistinguishesWorkflowsForTheSameTarget(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 18}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			view := localSessionUIFixture(t)
			for _, label := range []string{"Owned alpha", "Owned beta"} {
				spec := localSessionTestSpec()
				spec.Kind, spec.Label = session.Plugin, label
				handle, err := view.app.localSessions.Add(spec, nil)
				if err != nil {
					t.Fatal(err)
				}
				handle.Running("")
			}
			paint := drawnText(t, view, size[0], size[1])
			for _, label := range []string{"Owned alpha", "Owned beta"} {
				if !strings.Contains(paint, label) {
					t.Fatalf("workflow %q requires moving selection at %v:\n%s", label, size, paint)
				}
			}
		})
	}
}

func TestLocalSessionsReadableIdentityStateBindingAndActionsOffline(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 18}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			view := localSessionUIFixture(t)
			handle, err := view.app.localSessions.Add(localSessionTestSpec(), nil)
			if err != nil {
				t.Fatal(err)
			}
			handle.Running("")
			view.StylesChanged(view.app.Styles)
			paint := drawnText(t, view, size[0], size[1])
			for _, expected := range []string{"LOCAL SESSIONS", "RUNNING", localSessionTestContext, localSessionTestUID, "127.0.0.1:19090", "Enter details", "c stop owned", "Esc back"} {
				if !strings.Contains(paint, expected) {
					t.Fatalf("missing %q at %v:\n%s", expected, size, paint)
				}
			}
			if !retainedDisconnectedWorkspace(view) {
				t.Fatal("owned sessions cannot be reviewed offline")
			}
			view.key(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone))
			if view.selectedID != handle.ID() {
				t.Fatal("local refresh changed selected owned session")
			}
		})
	}
}

func TestLocalSessionsLongPreviewKeepsBindingAndDetailsResetScroll(t *testing.T) {
	view := localSessionUIFixture(t)
	spec := localSessionTestSpec()
	spec.Destination.Context = strings.Repeat("context-", 20)
	spec.Destination.Name = strings.Repeat("pod-", 20)
	handle, err := view.app.localSessions.Add(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		handle.Event("Authored lifecycle entry.")
	}
	view.render()
	paint := drawnText(t, view, 60, 18)
	if !strings.Contains(paint, "127.0.0.1:19090") || !strings.Contains(paint, localSessionTestUID) {
		t.Fatalf("identity or binding was pushed below preview:\n%s", paint)
	}
	view.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !strings.Contains(view.detail.GetText(true), spec.Destination.Context) || !strings.Contains(view.detail.GetText(true), spec.Destination.Name) {
		t.Fatal("full identity was lost from lifecycle detail")
	}
	view.detail.ScrollTo(20, 0)
	view.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	row, _ := view.detail.GetScrollOffset()
	if row != 0 || view.details || view.selectedID != handle.ID() {
		t.Fatal("Back did not restore overview selection and scroll")
	}
}

func TestLocalSessionsStopTargetsExactlyTheSelectedOwnedHandle(t *testing.T) {
	view := localSessionUIFixture(t)
	var firstStops, secondStops atomic.Int32
	first, err := view.app.localSessions.Add(localSessionTestSpec(), func() { firstStops.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	second, err := view.app.localSessions.Add(localSessionTestSpec(), func() { secondStops.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	view.render()
	view.table.Select(2, 0)
	view.key(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone))
	if firstStops.Load() != 1 || secondStops.Load() != 0 {
		t.Fatal("cleanup reached another owned session")
	}
	record, _ := view.app.localSessions.Find(first.ID())
	if record.State != session.Stopping || !record.Active {
		t.Fatal("cleanup request was presented as confirmed")
	}
	second.Finish(session.Completed, "Child exited.")
}

func TestLocalSessionsActionsUseLocalSelectionInReadOnlyMode(t *testing.T) {
	view := localSessionUIFixture(t)
	view.app.Config = mock.NewMockConfig(t)
	view.app.Config.K9s.ReadOnly = true
	handle, err := view.app.localSessions.Add(localSessionTestSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	handle.Running("")
	view.render()
	for _, action := range actionCatalog(view, view.app) {
		if action.Key == ui.KeyC || action.Key == tcell.KeyEnter {
			if action.UnavailableReason != "" || action.RequiresSelection {
				t.Fatal("local cleanup/details incorrectly require a live API resource", action)
			}
		}
	}
}

func TestLocalSessionsExpiredSelectionCannotRetargetCleanup(t *testing.T) {
	view := localSessionUIFixture(t)
	old, err := view.app.localSessions.Add(localSessionTestSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	old.Finish(session.Completed, "Child ended.")
	view.render()
	for range 140 {
		handle, addErr := view.app.localSessions.Add(localSessionTestSpec(), nil)
		if addErr != nil {
			t.Fatal(addErr)
		}
		handle.Finish(session.Completed, "Retained history entry.")
	}
	var stops atomic.Int32
	active, err := view.app.localSessions.Add(localSessionTestSpec(), func() { stops.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	view.render()
	view.key(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone))
	if view.selectedID != "" || !view.selectionInvalidated || stops.Load() != 0 {
		t.Fatal("expired history selection redirected cleanup")
	}
	view.table.Select(1, 0)
	if view.selectedID != active.ID() {
		t.Fatal("explicit selection did not restore ownership")
	}
}

func TestLocalLaunchPageChangesCancelPendingAndRetainReadyResources(t *testing.T) {
	var registry session.Registry
	var pendingStops, readyStops atomic.Int32
	pending, err := registry.Add(localSessionTestSpec(), func() { pendingStops.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	ready, err := registry.Add(localSessionTestSpec(), func() { readyStops.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	owner, next := NewPicker(), NewPicker()
	book := localLaunchBook{launches: map[string]*localLaunch{pending.ID(): {handle: pending, owner: owner}, ready.ID(): {handle: ready, owner: owner}}}
	book.launches[ready.ID()].running("127.0.0.1:19090")
	book.StackPushed(next)
	book.StackPopped(owner, next)
	if pendingStops.Load() != 1 || readyStops.Load() != 0 {
		t.Fatal("page lifecycle changed established resource ownership")
	}
	pending.Finish(session.Stopped, "Pending launch canceled.")
	ready.Finish(session.Stopped, "Owned stream ended.")
}

func TestOwnedDebugPodCleanupUsesCapturedUIDAndNeverDeletesReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			owned := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: localSessionTestName, Namespace: localSessionTestNamespace, UID: types.UID(localSessionTestUID)}}
			current := owned.DeepCopy()
			if replacement {
				current.UID = "local-session-replaced-uid"
			}
			clientset := kubefake.NewSimpleClientset(current)
			var deletes atomic.Int32
			clientset.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
				deletes.Add(1)
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != owned.UID {
					t.Error("cleanup lacked captured UID precondition")
				}
				return false, nil, nil
			})
			var registry session.Registry
			handle, err := registry.Add(localSessionTestSpec(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err = cleanupOwnedDebugPod(ctx, clientset, owned, handle); err != nil {
				t.Fatal(err)
			}
			if replacement && deletes.Load() != 0 {
				t.Fatal("same-name replacement was deleted")
			}
			if !replacement && deletes.Load() != 1 {
				t.Fatal("owned debug Pod was not cleaned up after cancellation")
			}
		})
	}
}

func TestDebugWriteGuardCannotBeBypassedByPluginDangerousDeclaration(t *testing.T) {
	runner := &capturedPluginRunner{app: NewApp(mock.NewMockConfig(t)), path: guardedTestPluginPath, env: Env{}}
	runner.app.Config.K9s.ReadOnly = true
	plugin := config.Plugin{Command: nativeKubectlCommand, Args: []string{"--context", localSessionTestContext, "debug", localSessionTestName}, Dangerous: false}
	if _, err := capturePluginInvocation(runner, &plugin); err == nil {
		t.Fatal("native debug write accepted in read-only mode")
	}
	if plugin.Dangerous {
		t.Fatal("configured plugin was mutated")
	}
	actions := ui.NewKeyActions()
	if err := bindPluginAction(runner, actions, "debug", ui.KeyShiftU, &plugin); err != nil {
		t.Fatal(err)
	}
	action, _ := actions.Get(ui.KeyShiftU)
	if !action.Opts.Dangerous {
		t.Fatal("action discovery incorrectly presented known debug creation as a read")
	}
	if kubectlDebugCommand(nativeKubectlCommand, []string{"logs", localSessionTestName, "-c", "debug"}) {
		t.Fatal("endpoint/read command mistaken for debug write")
	}
	if kubectlDebugCommand("helm", []string{"template", "--debug"}) {
		t.Fatal("Helm read flag mistaken for debug write")
	}
}

func TestForwardFailureNotesKeepDeniedUnavailableAndReplacedDistinctWithoutRawErrors(t *testing.T) {
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, localSessionTestName, errors.New("SECRET raw error"))
	absent := apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, localSessionTestName)
	for _, item := range []struct {
		err  error
		word string
	}{{denied, "denied"}, {absent, "unverified"}, {dao.ErrForwardIdentityChanged, "replaced"}, {context.DeadlineExceeded, "deadline"}} {
		note := localForwardSetupNote(item.err)
		if !strings.Contains(note, item.word) || strings.Contains(note, "SECRET") {
			t.Fatal("failure meaning or secret exclusion lost", note)
		}
	}
}

func TestLocalSessionCapturedEndpointExcludesURLCredentials(t *testing.T) {
	endpoint := localSessionEndpoint("https://alice:SECRET@api.example.test/path?token=SECRET#SECRET")
	if endpoint != "https://api.example.test/path" {
		t.Fatal("captured server exposed credentials or lost its identity", endpoint)
	}
}
