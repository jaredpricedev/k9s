// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestHTTPExplicitScopeIdentityAndIgnoredPageLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api/v1/namespaces/team-a":
			body = &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "wrong-name", UID: "uid"}}
		case "/api/v1/nodes":
			body = &corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid", ResourceVersion: "42"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34\ncontrol"}}}}}
		case "/apis/apps/v1/namespaces/team-a/deployments":
			list := &appsv1.DeploymentList{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DeploymentList"}}
			for i := range 101 {
				list.Items = append(list.Items, appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fmtNode(i), Namespace: "team-a", UID: types.UID(fmtNode(i)), ResourceVersion: "51"}})
			}
			list.Items[100].Spec.Template.Spec.Containers = []corev1.Container{{Name: "late", Image: "must-not-retain"}}
			body = list
		case "/apis/apps/v1/namespaces/team-a/daemonsets":
			body = &appsv1.DaemonSetList{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSetList"}, Items: []appsv1.DaemonSet{{ObjectMeta: metav1.ObjectMeta{Name: "wrong", Namespace: "other", UID: "uid"}}}}
		default:
			body = &appsv1.StatefulSetList{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSetList"}}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"", "all", "-", "*", "all namespaces", "UPPER", "team-a/other"} {
		got := Collect(context.Background(), client, ns, "ctx", "", time.Now())
		if calls.Load() != 0 || got.NamespaceState != stateUnavailable {
			t.Fatalf("invalid scope contacted server: %q %+v", ns, got)
		}
	}
	got := Collect(context.Background(), client, "team-a", "ctx", "", time.Now())
	if got.NamespaceState == stateObserved || got.NamespaceUID != "unknown" {
		t.Fatalf("accepted wrong namespace: %+v", got)
	}
	if len(got.Nodes.Items) != 1 || got.Nodes.Items[0].ResourceVersion != "42" || strings.Contains(got.Nodes.Items[0].Version, "\n") {
		t.Fatalf("typed node evidence: %+v", got.Nodes)
	}
	if len(got.Deployments.Items) != 100 || !got.Deployments.Truncated || got.Deployments.Items[99].Version != "unknown" {
		t.Fatalf("ignored limit escaped cap: %+v", got.Deployments)
	}
	if got.DaemonSets.State != stateUnavailable || len(got.DaemonSets.Items) != 0 {
		t.Fatalf("wrong namespace retained: %+v", got.DaemonSets)
	}
}

func TestHTTPReadDeadlineAndParentCancellation(t *testing.T) {
	canceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done(); canceled <- struct{}{} }))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	got := Collect(ctx, client, "team-a", "ctx", "", time.Now())
	if time.Since(started) > time.Second || got.Nodes.State != stateCanceledTimeout {
		t.Fatalf("parent cancellation lost: %+v", got)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP request not canceled")
	}
	// The actual transport must honor the per-read budget, even with a long parent.
	started = time.Now()
	section := collectNodes(context.Background(), client)
	if section.State != stateCanceledTimeout || time.Since(started) > 4*time.Second {
		t.Fatalf("per-read deadline lost: %+v", section)
	}
}

func TestRetainedFieldsAreBoundedAndIdentityIsMandatory(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: ""}}
	if validIdentity(node, "") {
		t.Fatal("missing UID accepted")
	}
	node.UID = types.UID(strings.Repeat("x", 513))
	if validIdentity(node, "") {
		t.Fatal("oversize identity accepted")
	}
	value := SafeField(strings.Repeat("é", 600) + "\n")
	if len([]rune(value)) != 512 {
		t.Fatal("field cap lost")
	}
	if SafeField("image\x1b\nname") != "image  name" {
		t.Fatal("controls retained")
	}
}
