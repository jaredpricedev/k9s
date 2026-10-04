// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package upgrade

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestCollectReturnsOnlyBoundedVersionFactsAndPartialDenial(t *testing.T) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "team-a", UID: types.UID("namespace-uid"),
		Annotations: map[string]string{"token": "must-not-be-retained"},
	}}
	objects := []runtime.Object{namespace}
	for i := range maxItems + 1 {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: fmtNode(i), UID: types.UID(fmtNode(i) + "-uid"),
			Annotations: map[string]string{"secret": "do-not-copy"},
		}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.1"}}}
		objects = append(objects, node)
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a", UID: types.UID("controller-uid"),
			Annotations: map[string]string{"secret": "never read"}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "api", Image: "example/api:v2"}},
		}}},
	}
	objects = append(objects, deployment)
	client := fake.NewSimpleClientset(objects...)
	client.PrependReactor("list", "daemonsets", func(_ clienttesting.Action) (bool, runtime.Object, error) { return true, nil, apiForbidden{} })
	got := Collect(context.Background(), client, "team-a", "prod-context", "v1.34.1", time.Unix(1, 0))
	if got.NamespaceUID != "namespace-uid" || len(got.Nodes.Items) > maxItems || !got.Nodes.Truncated {
		t.Fatalf("namespace or node cap lost: %+v", got)
	}
	if got.NamespaceState != stateObserved || got.DaemonSets.State != stateDenied || got.Deployments.State != stateObserved {
		t.Fatalf("partial states lost: ds=%+v dep=%+v", got.DaemonSets, got.Deployments)
	}
	if len(got.Deployments.Items) != 1 || got.Deployments.Items[0].Version != "example/api:v2" || got.Deployments.Items[0].UID != "controller-uid" {
		t.Fatalf("unexpected controller facts: %+v", got.Deployments)
	}
}

func TestCollectCanceledContextMarksSectionsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Collect(ctx, fake.NewSimpleClientset(), "team-a", "ctx", "v", time.Now())
	if got.Nodes.State != stateCanceledTimeout || got.Deployments.State != stateCanceledTimeout {
		t.Fatalf("canceled states missing: %+v %+v", got.Nodes, got.Deployments)
	}
}

func TestStateForErrorSeparatesUnsupportedDeniedAndCancellation(t *testing.T) {
	if StateForError(apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, "")) != "unsupported/404" {
		t.Fatal("404 state lost")
	}
	if StateForError(apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "node-a", errors.New("token=must-not-appear"))) != stateDenied {
		t.Fatal("denied state lost")
	}
	if StateForError(context.DeadlineExceeded) != stateCanceledTimeout {
		t.Fatal("timeout state lost")
	}
}

type apiForbidden struct{}

func (apiForbidden) Error() string { return "forbidden: token=unsafe" }
func fmtNode(i int) string         { return "node-" + strconv.Itoa(i) }
