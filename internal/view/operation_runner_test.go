// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	fake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func operationFixture(t *testing.T) (*operationSession, SelectedResourceTarget, *fake.FakeDynamicClient) {
	t.Helper()
	target := SelectedResourceTarget{Context: "original", GVR: client.DpGVR, Namespace: "ns", Name: "app", UID: types.UID("selected-uid")}
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "ns", "name": "app", "uid": "selected-uid", "resourceVersion": "12"},
		"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{}}},
	}}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), o)
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	return &operationSession{dynamic: dyn, typed: typed}, target, dyn
}

func TestRestartPinnedIdentityAndConditionalPatch(t *testing.T) {
	s, target, dyn := operationFixture(t)
	var wrote bool
	dyn.PrependReactor("patch", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		var patch map[string]any
		if err := json.Unmarshal(a.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
			t.Fatal(err)
		}
		metadata := patch["metadata"].(map[string]any)
		if metadata["uid"] != "selected-uid" || metadata["resourceVersion"] != "12" {
			t.Fatal("unconditional restart", patch)
		}
		annotation := patch["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)
		if annotation["kubectl.kubernetes.io/restartedAt"] == "" {
			t.Fatal(patch)
		}
		wrote = true
		return true, &unstructured.Unstructured{}, nil
	})
	if err := s.restart(t.Context(), target, metav1.PatchOptions{}); err != nil || !wrote {
		t.Fatal(err, wrote)
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(t.Context(), target.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obj.SetUID("replacement")
	if _, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Update(t.Context(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	wrote = false
	if err := s.restart(t.Context(), target, metav1.PatchOptions{}); err == nil || !strings.Contains(err.Error(), "replaced") || wrote {
		t.Fatal("replacement accepted", err, wrote)
	}
}

func TestOperationUnknownUIDIsNeverSubmitted(t *testing.T) {
	s, target, dyn := operationFixture(t)
	target.UID = ""
	for _, work := range []func() error{
		func() error { return s.restart(t.Context(), target, metav1.PatchOptions{}) },
		func() error { return s.scale(t.Context(), target, 1) },
		func() error { return s.delete(t.Context(), target, nil, dao.DefaultGrace) },
	} {
		if err := work(); err == nil {
			t.Fatal("write accepted an unknown identity")
		}
	}
	if len(dyn.Actions()) != 0 || len(s.typed.(*kubefake.Clientset).Actions()) != 0 {
		t.Fatal("unknown identity issued API requests")
	}
}

func TestScalePreservesUIDAndResourceVersion(t *testing.T) {
	s, target, dyn := operationFixture(t)
	dyn.PrependReactor("get", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != scaleDialogKey {
			return false, nil, nil
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "autoscaling/v1", "kind": "Scale", "metadata": map[string]any{"name": "app", "namespace": "ns", "uid": "selected-uid", "resourceVersion": "12"}, "spec": map[string]any{"replicas": int64(2)}}}, nil
	})
	var wrote bool
	dyn.PrependReactor("update", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		o := a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		n, _, _ := unstructured.NestedInt64(o.Object, "spec", "replicas")
		if a.GetSubresource() != scaleDialogKey || o.GetUID() != target.UID || o.GetResourceVersion() != "12" || n != 5 {
			t.Fatal(a)
		}
		wrote = true
		return true, o, nil
	})
	if err := s.scale(t.Context(), target, 5); err != nil || !wrote {
		t.Fatal(err, wrote)
	}
	for _, invalid := range []string{"-1", "2147483648", "", "1.5"} {
		if _, err := parseReplicaCount(invalid); err == nil {
			t.Fatal("invalid replica count", invalid)
		}
	}
	for _, valid := range []string{"0", "5", "2147483647"} {
		if _, err := parseReplicaCount(valid); err != nil {
			t.Fatal(valid, err)
		}
	}
}

func TestDeleteHasUIDAndVersionPreconditionsAndRBAC(t *testing.T) {
	s, target, dyn := operationFixture(t)
	dyn.PrependReactor("delete", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		opts := a.(ktesting.DeleteAction).GetDeleteOptions()
		if opts.Preconditions == nil || *opts.Preconditions.UID != target.UID || *opts.Preconditions.ResourceVersion != "12" {
			t.Fatal("unconditional delete", opts)
		}
		if opts.GracePeriodSeconds == nil || *opts.GracePeriodSeconds != 0 {
			t.Fatal("force intent lost", opts)
		}
		return true, nil, nil
	})
	if err := s.delete(t.Context(), target, nil, dao.ForceGrace); err != nil {
		t.Fatal(err)
	}
	s.typed.(*kubefake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{}, nil
	})
	before := len(dyn.Actions())
	if err := s.delete(t.Context(), target, nil, dao.DefaultGrace); err == nil || len(dyn.Actions()) != before {
		t.Fatal("denied delete made a resource request", err)
	}
}

func TestOperationBatchReportsPartialFailureAndBoundsTotalWait(t *testing.T) {
	targets := []SelectedResourceTarget{{Name: "failed"}, {Name: "accepted"}, {Name: "slow"}, {Name: "unsubmitted"}}
	done := make(chan []operationOutcome, 1)
	var attempts atomic.Int32
	startOperationBatch(50*time.Millisecond, targets, func(ctx context.Context, target SelectedResourceTarget) error {
		attempts.Add(1)
		switch target.Name {
		case "failed":
			return errors.New("forbidden")
		case "slow":
			<-ctx.Done()
			return ctx.Err()
		default:
			return nil
		}
	}, nil, func(results []operationOutcome) { done <- results })
	select {
	case results := <-done:
		if len(results) != 4 || results[0].Err == nil || results[1].Err != nil || !errors.Is(results[2].Err, context.DeadlineExceeded) || !errors.Is(results[3].Err, context.DeadlineExceeded) || attempts.Load() != 3 || !results[3].NotSubmitted || results[2].NotSubmitted {
			t.Fatal(results, attempts.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("batch exceeded its deadline")
	}
}

// This exercises a real tview input/draw loop while an HTTP API deliberately
// holds a request for five seconds, rather than merely asserting a goroutine.
func TestFiveSecondAPILeavesInputResizeAndQuitResponsive(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"pod","namespace":"ns","uid":"uid","resourceVersion":"1"}}`))
	}))
	defer server.Close()
	dyn, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(80, 24)
	app := tview.NewApplication().SetScreen(screen)
	view := tview.NewTextView()
	app.SetRoot(view, true)
	keys := make(chan tcell.Key, 8)
	resized := make(chan struct{}, 1)
	finished := make(chan error, 1)
	results := make(chan []operationOutcome, 1)
	target := SelectedResourceTarget{GVR: client.PodGVR, Namespace: "ns", Name: "pod", UID: "uid"}
	app.SetInputCapture(func(evt *tcell.EventKey) *tcell.EventKey {
		if evt.Rune() == 's' {
			startOperationBatch(10*time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, target SelectedResourceTarget) error {
				_, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
				return err
			}, nil, func(outcomes []operationOutcome) { results <- outcomes })
		} else if evt.Rune() == 'q' {
			app.Stop()
		} else {
			keys <- evt.Key()
		}
		return nil
	})
	app.SetAfterDrawFunc(func(tcell.Screen) {
		w, h := screen.Size()
		if w == 100 && h == 30 {
			select {
			case resized <- struct{}{}:
			default:
			}
		}
	})
	go func() { finished <- app.Run() }()
	app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
	select {
	case <-entered:
	case <-time.After(time.Second):
		app.Stop()
		t.Fatal("API request did not start")
	}
	started := time.Now()
	for _, evt := range []*tcell.EventKey{tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone), tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone), tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)} {
		app.QueueEvent(evt)
	}
	for range 3 {
		select {
		case <-keys:
		case <-time.After(time.Second):
			app.Stop()
			t.Fatal("input blocked by API")
		}
	}
	screen.SetSize(100, 30)
	app.QueueEvent(tcell.NewEventResize(100, 30))
	select {
	case <-resized:
	case <-time.After(time.Second):
		app.Stop()
		t.Fatal("resize blocked by API")
	}
	app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		app.Stop()
		t.Fatal("quit blocked by API")
	}
	if time.Since(started) >= 2*time.Second {
		t.Fatal("input took too long")
	}
	select {
	case <-results:
		t.Fatal("API did not hold request while input was handled")
	default:
	}
	select {
	case results := <-results:
		if len(results) != 1 || results[0].Err != nil {
			t.Fatal(results)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("API never completed")
	}
}

func TestOperationDestinationGuardRejectsLateGenerationAndContext(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	b := NewBrowser(client.PodGVR).(*Browser)
	b.app, b.meta = app, &metav1.APIResource{Kind: "Pod"}
	app.Content.Push(b)
	s, err := captureOperationScreen(b)
	if err != nil || !s.current() {
		t.Fatal(err)
	}
	b.operationGeneration.Add(1)
	if s.current() {
		t.Fatal("late result accepted after screen generation changed")
	}
	s.generation = b.operationGeneration.Load()
	s.context = "different-context"
	if s.current() {
		t.Fatal("late result accepted in a different context")
	}
	s.context = app.Config.ActiveContextName()
	b.GetModel().SetNamespace("different-namespace")
	if s.current() {
		t.Fatal("late result accepted in a different namespace")
	}
	b.GetModel().SetNamespace(s.namespace)
	app.Config.K9s.ReadOnly = true
	if s.confirm() {
		t.Fatal("read-only change accepted confirmed write")
	}
	if _, err := captureOperationScreen(nil); err == nil {
		t.Fatal("nil view accepted")
	}
}

func TestOperationRetriesOnlyExplicitConflict(t *testing.T) {
	attempts := 0
	err := retryOperationConflict(t.Context(), func() error {
		attempts++
		if attempts == 1 {
			return apierrors.NewConflict(client.DpGVR.GVR().GroupResource(), "app", errors.New("changed"))
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatal(err, attempts)
	}
	attempts = 0
	err = retryOperationConflict(t.Context(), func() error { attempts++; return context.DeadlineExceeded })
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
		t.Fatal("ambiguous write retried", err, attempts)
	}
}
