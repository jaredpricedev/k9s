// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package fleet

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fake "k8s.io/client-go/dynamic/fake"
	kt "k8s.io/client-go/testing"
)

const fleetPeerContext = "peer"

func fleetScope() Scope {
	return Scope{Contexts: [2]string{"primary", fleetPeerContext}, GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: "apps", Name: "api", PrimaryUID: "primary-uid"}
}
func fleetObject(uid types.UID) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "api", "namespace": "apps", "uid": string(uid), "resourceVersion": "7", "generation": int64(3)}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "api:v2", "env": []any{map[string]any{"name": "PASSWORD", "value": "secret-value"}}}}}}}, "status": map[string]any{"readyReplicas": int64(1)}}}
}
func reader(uid types.UID) *fake.FakeDynamicClient {
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "apps", "uid": "namespace-uid"}}}
	return fake.NewSimpleDynamicClient(runtime.NewScheme(), fleetObject(uid), ns)
}
func TestTwoActorsExactGETsIndependentUIDAndAlias(t *testing.T) {
	scope := fleetScope()
	primary, peer := reader("primary-uid"), reader("peer-uid")
	var mx sync.Mutex
	var names []string
	snapshot, err := Collect(t.Context(), scope, func(_ context.Context, name string) (Actor, error) {
		mx.Lock()
		names = append(names, name)
		mx.Unlock()
		dyn := primary
		if name == fleetPeerContext {
			dyn = peer
		}
		return Actor{Reader: dyn, Authority: "https://user:password@host:6443/path?token=secret"}, nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"primary", fleetPeerContext}, names)
	require.Equal(t, types.UID("peer-uid"), snapshot.Observations[1].UID)
	require.True(t, snapshot.MayAlias())
	require.Equal(t, "https://host:6443", snapshot.Observations[0].Authority)
	for _, dyn := range []*fake.FakeDynamicClient{primary, peer} {
		require.Len(t, dyn.Actions(), 2)
		for _, action := range dyn.Actions() {
			require.Equal(t, "get", action.GetVerb())
			require.Contains(t, []string{"deployments", "namespaces"}, action.GetResource().Resource)
		}
	}
	text := requireFacts(&snapshot.Observations[0])
	require.NotContains(t, text, "secret-value")
	require.Contains(t, text, "declared image (template)")
	require.NotContains(t, text, "healthy")
}
func requireFacts(o *Observation) string {
	text := ""
	for _, f := range o.Facts {
		text += f.Category + f.Name + f.Value
	}
	return text
}
func TestPeerFailureAndPrimaryReplacement(t *testing.T) {
	scope := fleetScope()
	snapshot, err := Collect(t.Context(), scope, func(_ context.Context, name string) (Actor, error) {
		if name == fleetPeerContext {
			return Actor{}, errors.New("credentials password=secret")
		}
		return Actor{Reader: reader("primary-uid")}, nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Observations[0].Facts)
	require.Empty(t, snapshot.Observations[1].Facts)
	require.NotContains(t, snapshot.Observations[1].State, "secret")
	snapshot, err = Collect(t.Context(), scope, func(context.Context, string) (Actor, error) { return Actor{Reader: reader("replacement")}, nil })
	require.NoError(t, err)
	require.Equal(t, "primary replaced; reopen selection", snapshot.Observations[0].State)
	require.Empty(t, snapshot.Observations[0].Facts)
	require.NotEmpty(t, snapshot.Observations[1].Facts)
}
func TestStrictNamed404(t *testing.T) {
	scope := fleetScope()
	valid := apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api")
	require.True(t, namedNotFound(valid, scope.GVR, "api"))
	proxy404 := apierrors.NewGenericServerResponse(http.StatusNotFound, "get", scope.GVR.GroupResource(), "api", "proxy response", 0, true)
	for _, err := range []error{errors.New("404 Not Found proxy"), proxy404, apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "different"), &apierrors.StatusError{ErrStatus: metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound}}} {
		require.False(t, namedNotFound(err, scope.GVR, "api"))
	}
	for _, err := range []error{valid, errors.New("proxy404"), proxy404} {
		dyn := reader("primary-uid")
		dyn.PrependReactor("get", "deployments", func(kt.Action) (bool, runtime.Object, error) { return true, nil, err })
		snapshot, e := Collect(t.Context(), scope, func(context.Context, string) (Actor, error) { return Actor{Reader: dyn}, nil })
		require.NoError(t, e)
		if errors.Is(err, valid) {
			require.Equal(t, "named object absent", snapshot.Observations[0].State)
		} else {
			require.Contains(t, snapshot.Observations[0].State, "unexpected API")
		}
	}
}

func TestNamespaceUIDRequiresExactNamespaceIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  *unstructured.Unstructured
	}{
		{name: "wrong kind", obj: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "apps", "uid": "namespace-uid"}}}},
		{name: "missing uid", obj: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "apps"}}}},
		{name: "namespaced namespace", obj: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "apps", "namespace": "wrong", "uid": "namespace-uid"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := reader("primary-uid")
			dyn.PrependReactor("get", "namespaces", func(kt.Action) (bool, runtime.Object, error) {
				return true, tc.obj.DeepCopy(), nil
			})
			snapshot, err := Collect(t.Context(), fleetScope(), func(context.Context, string) (Actor, error) {
				return Actor{Reader: dyn, Authority: "https://same.test"}, nil
			})
			require.NoError(t, err)
			require.Empty(t, snapshot.Observations[0].NamespaceUID)
			require.False(t, snapshot.MayAlias())
		})
	}
}
func TestCancelledSetupAndUnsupportedNeverCollect(t *testing.T) {
	scope := fleetScope()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	snapshot, err := Collect(ctx, scope, func(ctx context.Context, _ string) (Actor, error) { <-ctx.Done(); return Actor{}, ctx.Err() })
	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, "canceled", snapshot.Observations[0].State)
	scope.GVR.Resource = "secrets"
	calls := 0
	_, err = Collect(t.Context(), scope, func(context.Context, string) (Actor, error) { calls++; return Actor{}, nil })
	require.ErrorContains(t, err, "unsupported")
	require.Zero(t, calls)
}
func TestAuthoritySanitizesBeforeTruncation(t *testing.T) {
	require.Equal(t, "https://example.test:443", Authority("https://very-long-user:secret@example.test:443/secret?token=secret#fragment"))
	require.Equal(t, "unknown", Authority("https://%"))
}

func TestCancellationDuringSetupAndDeniedPeer(t *testing.T) {
	scope := fleetScope()
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{}, 2)
	finished := make(chan *Snapshot, 1)
	go func() {
		snapshot, _ := Collect(ctx, scope, func(ctx context.Context, _ string) (Actor, error) {
			started <- struct{}{}
			<-ctx.Done()
			return Actor{}, ctx.Err()
		})
		finished <- snapshot
	}()
	<-started
	<-started
	cancel()
	select {
	case snapshot := <-finished:
		require.Equal(t, "canceled", snapshot.Observations[0].State)
	case <-time.After(time.Second):
		t.Fatal("collection did not respect cancellation")
	}
	dyn := reader("peer-uid")
	dyn.PrependReactor("get", "deployments", func(kt.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api", errors.New("denied"))
	})
	snapshot, err := Collect(t.Context(), scope, func(_ context.Context, name string) (Actor, error) {
		if name == fleetPeerContext {
			return Actor{Reader: dyn}, nil
		}
		return Actor{Reader: reader("primary-uid")}, nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Observations[0].Facts)
	require.Equal(t, "denied", snapshot.Observations[1].State)
	require.False(t, snapshot.MayAlias())
}
func TestBoundedFieldsAndMissingStatus(t *testing.T) {
	obj := fleetObject("primary-uid")
	delete(obj.Object, "status")
	facts := facts(obj)
	require.Len(t, facts, 2)
	require.Equal(t, "declared image (template)", facts[1].Category)
	require.Len(t, []rune(clean(strings.Repeat("x", 1000))), 512)
	require.NotContains(t, clean("image\x1b[31m\n"), "\x1b")
}
