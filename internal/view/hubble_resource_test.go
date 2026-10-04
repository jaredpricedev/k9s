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
	"strconv"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type hubblePodRequest struct {
	limit int
	token string
}

// Use real REST clients so the test covers serialized ListOptions and bounded
// page decoding, which fake client-go reactors do not enforce.
func TestHubblePaginatedPodResolution(t *testing.T) {
	for _, count := range []int{0, 3, 1000, 1001, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var requests []hubblePodRequest
			conn := hubbleTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces/ns/pods" {
					t.Fatalf("unexpected pod request: %s", r.URL)
				}
				q := r.URL.Query()
				if q.Get("labelSelector") != "app=target" {
					t.Fatalf("selector broadened: %s", r.URL)
				}
				limit, err := strconv.Atoi(q.Get("limit"))
				if err != nil || limit < 1 || limit > hubblePodPageSize {
					t.Fatalf("unbounded list: %s", r.URL)
				}
				start := 0
				if q.Get("continue") != "" {
					start, err = strconv.Atoi(q.Get("continue"))
					if err != nil {
						t.Fatal(err)
					}
				}
				requests = append(requests, hubblePodRequest{limit: limit, token: q.Get("continue")})
				list := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
				end := min(start+limit, count)
				for i := start; i < end; i++ {
					list.Items = append(list.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%d", i), Namespace: "ns"}})
				}
				if end < count {
					list.Continue = strconv.Itoa(end)
				}
				if err := json.NewEncoder(w).Encode(list); err != nil {
					t.Error(err)
				}
			}, false)
			scope, err := resolveHubbleScope(t.Context(), conn, hubbleWorkloadTarget())
			switch {
			case count == 0:
				if err == nil || !strings.Contains(err.Error(), "no current pods") {
					t.Fatal(scope, err)
				}
			case count <= hubblePodScopeLimit:
				if err != nil || len(scope.Pods) != count || scope.Pods[0] != "ns/pod-0" || scope.Pods[count-1] != fmt.Sprintf("ns/pod-%d", count-1) {
					t.Fatal(scope, err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "1001 matches") || len(scope.Pods) != 0 {
					t.Fatal(scope, err)
				}
			}
			if count > hubblePodScopeLimit {
				if len(requests) != 5 || requests[4] != (hubblePodRequest{limit: 1, token: "1000"}) {
					t.Fatalf("did not stop at proof of cap: %+v", requests)
				}
			}
		})
	}
}

func TestHubbleResolutionRejectsUnsafeOrUnavailableScope(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		empty      bool
		uid        types.UID
	}{
		{name: "empty selector", want: "refusing empty", empty: true},
		{name: "replaced workload", want: "identity changed", uid: "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			conn := hubbleTestAPI(t, func(http.ResponseWriter, *http.Request) { requests++ }, tc.empty)
			target := hubbleWorkloadTarget()
			target.UID = tc.uid
			_, err := resolveHubbleScope(t.Context(), conn, target)
			if err == nil || !strings.Contains(err.Error(), tc.want) || requests != 0 {
				t.Fatalf("scope broadened: calls=%d err=%v", requests, err)
			}
		})
	}
	conn := hubbleTestAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("canceled resolver listed pods") }, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := resolveHubbleScope(ctx, conn, hubbleWorkloadTarget()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestHubbleResolutionPaginationErrors(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeated=%t", repeated), func(t *testing.T) {
			calls := 0
			conn := hubbleTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if !repeated && calls == 2 {
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Message: "pods forbidden", Reason: metav1.StatusReasonForbidden, Code: 403})
					return
				}
				_ = json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{Continue: "next"}, Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}})
			}, false)
			_, err := resolveHubbleScope(t.Context(), conn, hubbleWorkloadTarget())
			want := "forbidden"
			if repeated {
				want = "continuation token"
			}
			if err == nil || !strings.Contains(err.Error(), want) || calls != 2 {
				t.Fatal(calls, err)
			}
		})
	}
}

func TestHubbleResolutionPinsClientsAcrossContextSwitch(t *testing.T) {
	secondCalls := 0
	second := hubbleTestAPI(t, func(http.ResponseWriter, *http.Request) { secondCalls++ }, false)
	var mutable *inspectionConnection
	firstCalls := 0
	first := hubbleTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		firstCalls++
		mutable.dynamic, mutable.typed = second.dynamic, second.typed
		list := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "original"}}}}
		if firstCalls == 1 {
			list.Continue = "next"
		}
		_ = json.NewEncoder(w).Encode(list)
	}, false)
	mutable = &first
	pinned, err := pinInspectionConnection(mutable)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := resolveHubbleScope(t.Context(), pinned, hubbleWorkloadTarget())
	if err != nil || firstCalls != 2 || secondCalls != 0 || len(scope.Pods) != 2 || scope.Pods[1] != "ns/original" {
		t.Fatal(scope, firstCalls, secondCalls, err)
	}
}

func TestHubbleResolutionCanceledBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	conn := hubbleTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{Continue: "next"}})
		cancel()
	}, false)
	_, err := resolveHubbleScope(ctx, conn, hubbleWorkloadTarget())
	if err == nil || calls != 1 {
		t.Fatal("canceled resolution continued", calls, err)
	}
}

func hubbleWorkloadTarget() SelectedResourceTarget {
	return SelectedResourceTarget{Context: "original", GVR: client.NewGVR("apps/v1/deployments"), Namespace: "ns", Name: "app", UID: "current"}
}

func hubbleTestAPI(t *testing.T, pods http.HandlerFunc, emptySelector bool) inspectionConnection {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/apis/apps/v1/namespaces/ns/deployments/app" {
			selector := map[string]any{}
			if !emptySelector {
				selector["matchLabels"] = map[string]any{"app": "target"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "ns", "name": "app", "uid": "current"}, "spec": map[string]any{"selector": selector}})
			return
		}
		pods(w, r)
	}))
	t.Cleanup(server.Close)
	cfg := &rest.Config{Host: server.URL}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return inspectionConnection{dynamic: dyn, typed: typed}
}
