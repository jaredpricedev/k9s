// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/taskbook"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const taskbookFixtureUID = "saved"
const taskbookFixtureContext = "offline"

func taskbookViewFixture(t *testing.T) *taskbookView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	o := inspect.NewObservation(inspect.ResourceIdentity{Context: taskbookFixtureContext, GVR: client.PodGVR.String(), Namespace: investigationAppRole, Name: testWorkspaceAPIName, UID: taskbookFixtureUID}, "Frozen fixture", time.Now(), map[string]any{})
	a, err := taskbook.New("Investigate api", inspect.NewBundle([]inspect.Observation{o}))
	if err != nil {
		t.Fatal(err)
	}
	v := &taskbookView{Details: NewDetails(app, "Task handoff", taskbookFixtureContext, contentInspection, true), artifact: a, ready: true}
	app.Content.Push(v)
	return v
}
func taskbookFormForTest(t *testing.T, v *taskbookView) *tview.Form {
	t.Helper()
	var form *tview.Form
	v.form.Focus(func(p tview.Primitive) { form, _ = p.(*tview.Form) })
	if form == nil {
		t.Fatal("missing native form")
	}
	return form
}
func TestTaskbookOwnerReturnAndAbandonedCallbacks(t *testing.T) {
	v := taskbookViewFixture(t)
	v.saveTask()
	form := taskbookFormForTest(t, v)
	destination := filepath.Join(t.TempDir(), "abandoned.json")
	form.GetFormItem(0).(*tview.InputField).SetText(destination)
	next := NewDetails(v.app, "next", "", contentTXT, false)
	v.app.Content.Push(next)
	v.app.SetFocus(next)
	focus := v.app.GetFocus()
	if v.form != nil || v.app.Content.Pages.GetPrimitive(taskbookFormPage) != nil {
		t.Fatal("form survived owner stop")
	}
	form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { v.app.SetFocus(p) })
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("abandoned save wrote file", err)
	}
	if v.app.GetFocus() != focus {
		t.Fatal("abandoned callback changed focus")
	}
	generation := v.generation
	v.app.Content.Pop()
	if !v.acceptTaskResult(generation, "") {
		t.Fatal("owner not restored")
	}
	v.Stop()
	if v.acceptTaskResult(generation, "") {
		t.Fatal("late result accepted after stop")
	}
}
func TestTaskbookFormKeyboardCancelRetainsArtifact(t *testing.T) {
	v := taskbookViewFixture(t)
	v.confirmRerun()
	form := taskbookFormForTest(t, v)
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { v.app.SetFocus(p) })
	if v.form != nil || len(v.artifact.Latest) != 0 || v.cancel != nil || v.app.Content.Top() != v {
		t.Fatal("cancel ran checks or lost owner")
	}
}
func TestTaskbookPreviewAndFormFitSupportedSizes(t *testing.T) {
	v := taskbookViewFixture(t)
	v.model.AddListener(v.Details)
	v.renderTask()
	for _, size := range []struct{ width, height int }{{80, 24}, {60, 18}, {40, 16}} {
		screen := tcell.NewSimulationScreen("UTF-8")
		if err := screen.Init(); err != nil {
			t.Fatal(err)
		}
		screen.SetSize(size.width, size.height)
		v.SetRect(0, 0, size.width, size.height)
		v.Draw(screen)
		var b strings.Builder
		for y := range size.height {
			for x := range size.width {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		if !strings.Contains(b.String(), "TASK HANDOFF") || !strings.Contains(b.String(), "Investigate api") {
			t.Fatal("identity/actions missing", size, b.String())
		}
		v.saveTask()
		v.form.Draw(screen)
		b.Reset()
		for y := range size.height {
			for x := range size.width {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		if !strings.Contains(b.String(), "Save task handoff") || !strings.Contains(b.String(), "Cancel") || !strings.Contains(b.String(), "Save") {
			t.Fatal("form unavailable", size, b.String())
		}
		v.dismissTaskForm(false)
		screen.Fini()
	}
}
func TestTaskbookAPIReadRejectsUIDReplacementDeniedAbsentSecret(t *testing.T) {
	id := inspect.ResourceIdentity{Context: desiredReviewFixtureContext, GVR: client.PodGVR.String(), Namespace: investigationAppRole, Name: testWorkspaceAPIName, UID: taskbookFixtureUID}
	obj := &unstructured.Unstructured{Object: map[string]any{"unsafeFixtureBody": "excluded"}}
	obj.SetAPIVersion(corev1.SchemeGroupVersion.String())
	obj.SetKind(inspectionPodKind)
	obj.SetName(testWorkspaceAPIName)
	obj.SetNamespace(investigationAppRole)
	obj.SetUID(types.UID(nativeReplacement))
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	read := taskbookAPIReader(inspectionConnection{dynamic: dyn})
	o := read(t.Context(), id)
	if o.State != inspect.ObservationStale || o.Object != nil || o.Identity != id {
		t.Fatal("replacement retained", o)
	}
	dyn.PrependReactor("get", hubblePods, func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: hubblePods}, testWorkspaceAPIName, nil)
	})
	o = read(t.Context(), id)
	if o.State != inspect.ObservationDenied || o.Object != nil {
		t.Fatal(o)
	}
	dyn.PrependReactor("get", hubblePods, func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: hubblePods}, testWorkspaceAPIName)
	})
	o = read(t.Context(), id)
	if o.State != inspect.ObservationStale {
		t.Fatal(o)
	}
	before := len(dyn.Actions())
	id.GVR = "v1/secrets"
	o = read(t.Context(), id)
	if len(dyn.Actions()) != before || o.State != inspect.ObservationIncomplete {
		t.Fatal("Secret read", o)
	}
}
func TestTaskbookWorkspaceEvidenceOmitsRawObject(t *testing.T) {
	at := time.Now()
	scope := workspace.Scope{Name: "work", Context: desiredReviewFixtureContext, Namespaces: []string{investigationAppRole}}
	raw := &unstructured.Unstructured{Object: map[string]any{"unrecognizedSensitiveField": "raw-must-not-export"}}
	w := &dailyWorkspace{scope: scope, snapshot: workspace.Snapshot{ObservedAt: at, Resources: []workspace.Resource{{Ref: workspace.ResourceRef{GVR: client.PodGVR.String(), Namespace: investigationAppRole, Name: testWorkspaceAPIName, UID: taskbookFixtureUID}, Summary: "Pod not ready", Object: raw}}}}
	b, scopes, err := taskbookEvidence(w)
	if err != nil || len(scopes) != 1 || b.Observations[0].Object != nil || b.Observations[0].ObservedAt != at {
		t.Fatal(b, scopes, err)
	}
	a, err := taskbook.New("Retained workspace", b)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := taskbook.Preview(a)
	if err != nil || strings.Contains(preview, "raw-must-not-export") {
		t.Fatal(preview, err)
	}
}

func TestTaskbookDisconnectRetainsArtifactAndNativeControlsBeyondRetryBudget(t *testing.T) {
	v := taskbookViewFixture(t)
	a := v.app
	if err := v.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	v.renderTask()
	v.Start()
	conn := &disconnectedWorkspaceConnection{Connection: mock.NewMockConnection()}
	a.Config.SetConnection(conn)
	a.factory = watch.NewFactory(conn)
	a.clusterModel = model.NewClusterInfo(a.factory, "test", a.Config.K9s)
	a.Config.K9s.MaxConnRetry = 1
	artifact := v.artifact
	v.BufferCompleted("Investigate", "")
	v.text.ScrollTo(1, 0)
	pending, cancel := context.WithCancel(t.Context())
	v.cancel, v.generation = cancel, 9
	defer cancel()
	for range 3 {
		if err := a.refreshCluster(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&a.conRetry) <= a.Config.K9s.MaxConnRetry || a.Content.Top() != v ||
		!reflect.DeepEqual(v.artifact, artifact) || !v.ready || v.generation != 9 || pending.Err() != nil || v.inspectionQuery != "Investigate" {
		t.Fatal("disconnect changed retained task handoff state")
	}
	row, col := v.text.GetScrollOffset()
	if row != 1 || col != 0 {
		t.Fatalf("scroll offset changed: %d,%d", row, col)
	}
	a.connectivityComponent(v, true)
	if v.generation != 9 || pending.Err() != nil {
		t.Fatal("recovery canceled pending task handoff work")
	}
	event := tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone)
	v.InputHandler()(event, func(p tview.Primitive) { a.SetFocus(p) })
	if v.form == nil {
		t.Fatal("native task handoff save control unavailable after disconnect")
	}
	taskbookFormForTest(t, v).GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { a.SetFocus(p) })
	v.renderTask()
	v.text.ScrollTo(0, 0)
	frame := drawnText(t, v, 80, 24)
	if !strings.Contains(frame, "TASK HANDOFF") || !strings.Contains(frame, "s: save") {
		t.Fatal("retained artifact or task controls missing", frame)
	}
}
func TestTaskbookCanceledWorkerDoesNotAcceptLateResult(t *testing.T) {
	v := taskbookViewFixture(t)
	generation := v.generation
	ctx, cancel := context.WithCancel(t.Context())
	v.cancel = cancel
	v.Stop()
	if ctx.Err() == nil || v.acceptTaskResult(generation, "") {
		t.Fatal("worker not canceled or late result accepted")
	}
}

func TestTaskbookDestinationRoundTripRejectsLateRerun(t *testing.T) {
	v := taskbookViewFixture(t)
	if _, err := v.app.Config.ActivateContext("ct-1-1"); err != nil {
		t.Fatal(err)
	}
	generation, revision := v.generation, v.app.Config.DestinationRevision()
	contextName := v.app.Config.ActiveContextName()
	namespace := v.app.Config.ActiveNamespace()
	if !v.acceptTaskRerun(generation, contextName, revision) {
		t.Fatal("current destination rejected")
	}
	if err := v.app.Config.SetActiveNamespace("other"); err != nil {
		t.Fatal(err)
	}
	if err := v.app.Config.SetActiveNamespace(namespace); err != nil {
		t.Fatal(err)
	}
	if v.acceptTaskRerun(generation, contextName, revision) {
		t.Fatal("round trip revived obsolete destination")
	}
}
