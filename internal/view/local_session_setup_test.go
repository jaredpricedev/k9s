// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/watch"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

func TestForwardPreparationCannotReopenAfterLeavingAndReturningToItsPage(t *testing.T) {
	view, _, _, _ := nativeGuardFixture(t, nativeObserved)
	app := view.App()
	app.SetRunning(true)
	t.Cleanup(func() { app.SetRunning(false) })
	origin := selectedResourceForPath(view, app.Config.ActiveContextName(), view.GetSelectedItem())
	capture := &forwardDialogCapture{view: view, selection: view.GetSelectedItem(), generation: view.operationGeneration.Load(),
		destination: forwardDestination{app: app, factory: app.factory, owner: view, revision: app.Config.DestinationRevision(), origin: origin}}
	require.True(t, capture.current(), "captured forwarding page was not initially current")
	other := &discoveryOwner{Details: NewDetails(app, "other page", "", contentTXT, false)}
	app.Content.Push(other)
	require.False(t, capture.current(), "pending result took ownership of another page")
	app.Content.Pop()
	require.Same(t, view, app.Content.Top())
	require.False(t, capture.current(), "returning to the same page re-enabled its canceled preparation")
}

const (
	localSetupNodeName = "owned-node"
	localSetupNodeUID  = "selected-node-uid"
	localSetupPodName  = "generated-debug-pod"
	localSetupPodsPath = "/api/v1/namespaces/apps/pods"
)

func writeLocalSetupJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func localSetupConfig(server *httptest.Server) *rest.Config {
	return &rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}}
}

func TestLocalSessionMetadataUsesAuthorizedAllNamespaceCacheWithoutAPI(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
		"name": localSessionTestName, "namespace": localSessionTestNamespace, "uid": localSessionTestUID}}}
	reader := fake.NewSimpleDynamicClient(runtime.NewScheme(), pod)
	factory := watch.NewFactory(sessionSelectionConnection{Connection: mock.NewMockConnection(), reader: reader})
	genericInformer, err := factory.ForResource(client.BlankNamespace, client.PodGVR)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Terminate()
	informer := genericInformer.Informer()
	if !cache.WaitForCacheSync(t.Context().Done(), informer.HasSynced) {
		t.Fatal("all-namespace fixture cache did not start")
	}
	if cacheErr := informer.GetStore().Add(pod); cacheErr != nil {
		t.Fatal(cacheErr)
	}
	captured, err := cachedLocalSessionPod(factory, client.NamespaceAll, localSessionTestNamespace+"/"+localSessionTestName)
	if err != nil || string(captured.UID) != localSessionTestUID {
		t.Fatal("all-namespace cached identity unavailable", captured, err)
	}
	for _, action := range reader.Actions() {
		if action.GetVerb() != client.ListVerb && action.GetVerb() != client.WatchVerb {
			t.Fatal("metadata preparation started an API GET instead of using the established list/watch cache", action)
		}
	}
}

func TestForwardControllerLookupUsesCapturedUIDSelectorNamespaceAndListBound(t *testing.T) {
	target := &SelectedResourceTarget{Context: localSessionTestContext, GVR: client.DpGVR, Namespace: localSessionTestNamespace,
		Name: "source-deployment", UID: "source-controller-uid"}
	var lists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			writeLocalSetupJSON(t, w, &authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"},
				Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
		case "/apis/apps/v1/namespaces/apps/deployments/source-deployment":
			writeLocalSetupJSON(t, w, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": target.Name, "namespace": target.Namespace, "uid": "source-controller-uid", "resourceVersion": "100"},
				"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "captured"}}}}})
		case localSetupPodsPath:
			lists.Add(1)
			if r.URL.Query().Get("limit") != "200" || r.URL.Query().Get("labelSelector") != "app=captured" {
				t.Error("unbounded or wrongly scoped candidate lookup", r.URL.RawQuery)
			}
			writeLocalSetupJSON(t, w, &v1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: localSessionTestName, Namespace: target.Namespace, UID: localSessionTestUID},
				Status:     v1.PodStatus{Phase: v1.PodRunning}}}})
		default:
			t.Error("unexpected controller request", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	pod, err := collectForwardControllerPod(t.Context(), localSetupConfig(server), target)
	if err != nil || pod.Name != localSessionTestName || lists.Load() != 1 {
		t.Fatal("captured controller candidate lookup failed", pod, err, lists.Load())
	}
	target.UID = "replaced-controller-uid"
	if _, err := collectForwardControllerPod(t.Context(), localSetupConfig(server), target); err == nil || lists.Load() != 1 {
		t.Fatal("same-name controller replacement could be adopted or listed Pods", err, lists.Load())
	}
}

func TestLocalSessionOriginIdentityMatchingRequiresContextKindAndUID(t *testing.T) {
	view := localSessionUIFixture(t)
	target := &SelectedResourceTarget{Context: localSessionTestContext, GVR: client.SvcGVR, Namespace: localSessionTestNamespace,
		Name: "source-service", UID: "service-origin-uid"}
	spec := localSessionTestSpec()
	spec.Origin = localSessionSpec(session.PortForward, "", target, 1).Destination
	if _, err := view.app.localSessions.Add(spec, nil); err != nil {
		t.Fatal(err)
	}
	if !view.app.hasLocalSessionsForTarget(target) {
		t.Fatal("explicit origin lost its local session navigation")
	}
	for _, change := range []func(*SelectedResourceTarget){func(v *SelectedResourceTarget) { v.Context = "different" },
		func(v *SelectedResourceTarget) { v.GVR = client.DpGVR }, func(v *SelectedResourceTarget) { v.UID = "different-session-uid" }} {
		other := *target
		change(&other)
		if view.app.hasLocalSessionsForTarget(&other) {
			t.Fatal("local navigation adopted another captured identity")
		}
	}
}

type nodeSetupFixture struct {
	t                                           *testing.T
	allowCreate, replaceNode, cancelAfterCreate bool
	cancel                                      context.CancelFunc
	creates, gets, deletes                      atomic.Int32
	owned                                       v1.Pod
}

func (f *nodeSetupFixture) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
		var review authv1.SelfSubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			f.t.Error(err)
		}
		allowed := review.Spec.ResourceAttributes.Verb != client.CreateVerb || f.allowCreate
		writeLocalSetupJSON(f.t, w, &authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"},
			Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}})
	case r.URL.Path == "/api/v1/nodes/"+localSetupNodeName:
		uid := localSetupNodeUID
		if f.replaceNode {
			uid = "replaced-node-uid"
		}
		writeLocalSetupJSON(f.t, w, &v1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{Name: localSetupNodeName, UID: types.UID(uid)}})
	case r.URL.Path == localSetupPodsPath && r.Method == http.MethodPost:
		f.creates.Add(1)
		var desired v1.Pod
		if err := json.NewDecoder(r.Body).Decode(&desired); err != nil {
			f.t.Error(err)
		}
		if desired.Name != "" || desired.GenerateName == "" {
			f.t.Error("debug launch reused a non-owned Pod name")
		}
		writeLocalSetupJSON(f.t, w, &f.owned)
	case r.URL.Path == localSetupPodsPath+"/"+localSetupPodName && r.Method == http.MethodGet:
		if f.gets.Add(1) == 1 && f.cancelAfterCreate {
			f.cancel()
		}
		writeLocalSetupJSON(f.t, w, &f.owned)
	case r.URL.Path == localSetupPodsPath+"/"+localSetupPodName && r.Method == http.MethodDelete:
		f.deletes.Add(1)
		var options metav1.DeleteOptions
		if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
			f.t.Error(err)
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != f.owned.UID {
			f.t.Error("accepted debug Pod cleanup lacked owned UID precondition")
		}
		writeLocalSetupJSON(f.t, w, &metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusSuccess})
	default:
		f.t.Error("unexpected node setup request", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func TestOwnedNodeSetupRejectsReplacementAndDeniedCreationBeforeWrites(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		f := &nodeSetupFixture{t: t, replaceNode: replacement}
		server := httptest.NewServer(http.HandlerFunc(f.serve))
		launch := &nodeSessionLaunch{target: SelectedResourceTarget{Context: localSessionTestContext, GVR: client.NodeGVR,
			Name: localSetupNodeName, UID: localSetupNodeUID}, config: localSetupConfig(server), pod: &v1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: localSessionTestNamespace}}}
		err := launch.execute(t.Context(), nil)
		server.Close()
		if err == nil || f.creates.Load() != 0 {
			t.Fatal("guarded node setup submitted a write", replacement, err, f.creates.Load())
		}
	}
}

func TestOwnedNodeAcceptedCreationKeepsReceiptAndUIDCleanupAfterCancellation(t *testing.T) {
	progress := new(operationProgress)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), operationProgressKey{}, progress))
	defer cancel()
	f := &nodeSetupFixture{t: t, allowCreate: true, cancelAfterCreate: true, cancel: cancel, owned: v1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: localSetupPodName,
			Namespace: localSessionTestNamespace, UID: localSessionTestUID}, Status: v1.PodStatus{Phase: v1.PodPending}}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	view := localSessionUIFixture(t)
	handle, err := view.app.localSessions.Add(localSessionTestSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	launch := &nodeSessionLaunch{target: SelectedResourceTarget{Context: localSessionTestContext, GVR: client.NodeGVR,
		Name: localSetupNodeName, UID: localSetupNodeUID}, config: localSetupConfig(server), pod: &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: localSessionTestNamespace, GenerateName: "owned-debug-"}}}
	err = launch.execute(ctx, &localLaunch{handle: handle})
	if !errors.Is(err, context.Canceled) || f.creates.Load() != 1 || f.deletes.Load() != 1 {
		t.Fatal("accepted debug setup did not clean only its returned Pod after cancellation", err, f.creates.Load(), f.deletes.Load())
	}
	if len(progress.accepted) != 2 || !strings.Contains(progress.accepted[0], localSessionTestUID) || !strings.Contains(progress.accepted[1], localSessionTestUID) {
		t.Fatal("cancellation erased create/delete acceptance facts", progress.accepted)
	}
}

func TestOwnedDebugCleanupDoesNotTreatProxy404AsConfirmedAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("unverified cleanup sent a write", r.Method)
		}
		http.Error(w, "proxy route unavailable", http.StatusNotFound)
	}))
	defer server.Close()
	clientset, err := kubernetes.NewForConfig(localSetupConfig(server))
	require.NoError(t, err)
	registry := &session.Registry{}
	handle, err := registry.Add(localSessionTestSpec(), nil)
	require.NoError(t, err)
	err = cleanupOwnedDebugPod(t.Context(), clientset, &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "apps", UID: "owned-uid"}}, handle)
	require.ErrorIs(t, err, errExternalOperationOutcome)
	record, ok := registry.Find(handle.ID())
	require.True(t, ok)
	require.NotContains(t, fmt.Sprint(record.Events), "already absent")
}
