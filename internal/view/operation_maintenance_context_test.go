// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	k9sclient "github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/drain"
)

const maintenanceDaemonSetSource = "daemonset"
const maintenanceDiscoverySource = "discovery"

// Fake discovery answers synchronously from fixture resources; give it an
// explicit context-aware contract rather than accepting arbitrary legacy IO.
type maintenanceFixtureDiscovery struct{ discovery.DiscoveryInterface }

func (d maintenanceFixtureDiscovery) ServerResourcesForGroupVersionContext(ctx context.Context, version string) (*metav1.APIResourceList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.ServerResourcesForGroupVersion(version)
}

type maintenanceFixtureClient struct {
	kubernetes.Interface
	apps      typedappsv1.AppsV1Interface
	discovery discovery.DiscoveryInterface
}

func (c maintenanceFixtureClient) AppsV1() typedappsv1.AppsV1Interface {
	if c.apps != nil {
		return c.apps
	}
	return c.Interface.AppsV1()
}

func (c maintenanceFixtureClient) Discovery() discovery.DiscoveryInterface {
	if c.discovery != nil {
		return c.discovery
	}
	return maintenanceFixtureDiscovery{DiscoveryInterface: c.Interface.Discovery()}
}

func TestGuardedDrainLegacyPreflightReadsHonorCancellationBeforeCordon(t *testing.T) {
	for _, source := range []string{maintenanceDaemonSetSource, maintenanceDiscoverySource} {
		t.Run(source, func(t *testing.T) { testMaintenancePreflightCancellation(t, source) })
	}
}

func testMaintenancePreflightCancellation(t *testing.T, source string) {
	session, target, dynamic, typed := maintenanceFixture(t)
	controller := true
	ownerKind := "ReplicaSet"
	if source == maintenanceDaemonSetSource {
		ownerKind = "DaemonSet"
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: investigationAppRole, Namespace: guardedTestNamespace, UID: "preflight-pod",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: rolloutTestAPI, Kind: ownerKind, Name: "owner", UID: "owner-uid", Controller: &controller}}},
		Spec: corev1.PodSpec{NodeName: target.Name}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := typed.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	started, release, disconnected := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-request.Context().Done():
			close(disconnected)
		case <-release:
		}
	}))
	defer func() { close(release); server.Close() }()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	adapter := maintenanceFixtureClient{Interface: typed}
	if source == maintenanceDaemonSetSource {
		adapter.apps = client.AppsV1()
	} else {
		adapter.discovery = client.Discovery()
	}
	session.typed = adapter
	task := startOperationBatch(10*time.Second, []SelectedResourceTarget{target}, func(ctx context.Context, selected SelectedResourceTarget) error {
		return session.drain(ctx, selected, dao.DrainOptions{GracePeriodSeconds: -1, IgnoreAllDaemonSets: true})
	}, nil, nil)
	defer task.cancelRemaining()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("native preflight did not issue its retained read")
	}
	task.cancelRemaining()
	receipt := waitOperationTask(t, task)
	if receipt.Outcomes[0].State != operationCancelled || len(receipt.Outcomes[0].AcceptedSteps) != 0 {
		t.Fatal("preflight cancellation claimed a write", receipt.Outcomes[0])
	}
	for _, action := range dynamic.Actions() {
		if action.GetVerb() == k9sclient.PatchVerb {
			t.Fatal("node was cordoned after preflight cancellation")
		}
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("native preflight HTTP read outlived its operation context")
	}
}

func TestGuardedEvictionDiscoveryPreservesNativeFallbackAndDeniedEvidence(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				status := apierrors.NewNotFound(schema.GroupResource{Resource: maintenanceDiscoverySource}, "v1").ErrStatus
				if code == http.StatusForbidden {
					status = apierrors.NewForbidden(schema.GroupResource{Resource: maintenanceDiscoverySource}, "v1", nil).ErrStatus
				}
				status.TypeMeta = metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Status"}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(code)
				_ = json.NewEncoder(writer).Encode(status)
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			version, err := drain.CheckEvictionSupport(maintenanceClient{Interface: client, ctx: t.Context()})
			if code == http.StatusNotFound {
				if err != nil || !version.Empty() {
					t.Fatal("native core/v1 absence did not retain delete fallback", version, err)
				}
			} else if !apierrors.IsForbidden(err) {
				t.Fatal("denied discovery claimed unsupported eviction", version, err)
			}
		})
	}
}

func TestGuardedEvictionDiscoveryDoesNotDowngradeReviewedSupportAfterCordon(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		resources := &metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: corev1.SchemeGroupVersion.String()}, GroupVersion: corev1.SchemeGroupVersion.String()}
		if reads.Add(1) == 1 {
			resources.APIResources = []metav1.APIResource{{Name: "pods/eviction", Group: policyv1.GroupName, Version: policyv1.SchemeGroupVersion.Version, Kind: "Eviction"}}
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(resources)
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	guarded := maintenanceClient{Interface: client, ctx: t.Context(),
		discoveryClient: &maintenanceDiscovery{DiscoveryInterface: client.Discovery(), ctx: t.Context()}}
	for range 2 {
		version, readErr := drain.CheckEvictionSupport(guarded)
		if readErr != nil || version != policyv1.SchemeGroupVersion {
			t.Fatal("reviewed eviction support changed to unreviewed delete", version, readErr)
		}
	}
	if reads.Load() != 1 {
		t.Fatal("preflight observation was not retained", reads.Load())
	}
}

func TestGuardedEvictionDiscoveryWithoutContextTransportFailsClosed(t *testing.T) {
	_, _, _, typed := maintenanceFixture(t)
	if _, err := drain.CheckEvictionSupport(maintenanceClient{Interface: typed, ctx: t.Context()}); err == nil {
		t.Fatal("legacy discovery without a bounded transport was accepted")
	}
}
