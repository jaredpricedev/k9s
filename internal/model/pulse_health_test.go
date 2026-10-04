// Modified for k9+; see NOTICE.
package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type pulseHealthFactory struct {
	dao.Factory
	failures map[*client.GVR]error
	synced   map[*client.GVR]bool
	objects  map[*client.GVR][]runtime.Object
	reads    []*client.GVR
	conn     client.Connection
}

func (f *pulseHealthFactory) Client() client.Connection { return f.conn }

func (f *pulseHealthFactory) List(gvr *client.GVR, _ string, _ bool, _ labels.Selector) ([]runtime.Object, error) {
	f.reads = append(f.reads, gvr)
	return f.objects[gvr], f.failures[gvr]
}
func (f *pulseHealthFactory) HasSynced(gvr *client.GVR, _ string) (bool, error) {
	return f.synced[gvr], f.failures[gvr]
}

func TestPulseCollectionContinuesAfterDeniedAndDistinguishesPendingEmpty(t *testing.T) {
	f := &pulseHealthFactory{
		failures: map[*client.GVR]error{client.NodeGVR: apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("permission required"))},
		synced:   map[*client.GVR]bool{client.PodGVR: true},
	}
	h := NewPulseHealth(f)
	c := make(HealthChan, 3)
	last := make(map[*client.GVR]HealthPoint)
	ctx := context.WithValue(context.Background(), internal.KeyWithMetrics, false)
	err := h.checkPulse(ctx, "apps", c, last, client.GVRs{client.NodeGVR, client.PodGVR, client.DpGVR})
	require.Error(t, err)
	require.Equal(t, []*client.GVR{client.NodeGVR, client.PodGVR, client.DpGVR}, f.reads)
	denied, empty, loading := <-c, <-c, <-c
	require.Equal(t, HealthDenied, denied.State)
	require.False(t, denied.HasValue())
	require.Equal(t, client.NodeGVR, denied.GVR, "failures keep their resource identity")
	require.Equal(t, HealthEmpty, empty.State)
	require.True(t, empty.HasValue())
	require.Zero(t, empty.Total)
	require.Equal(t, "apps", empty.Namespace)
	require.Equal(t, "informer cache", empty.Source)
	require.False(t, empty.ObservedAt.IsZero())
	require.Equal(t, HealthLoading, loading.State)
	require.False(t, loading.HasValue(), "an unsynced lister is not an observed empty result")
	f.failures[client.NodeGVR] = apierrors.NewServiceUnavailable("API unavailable")
	require.Error(t, h.checkPulse(ctx, "apps", c, last, client.GVRs{client.NodeGVR, client.PodGVR, client.DpGVR}))
	require.Equal(t, HealthUnavailable, (<-c).State)
	require.Equal(t, HealthEmpty, (<-c).State, "a failed kind must not suppress readable workloads")
	require.Equal(t, HealthLoading, (<-c).State)
}

func TestPulseRefreshPreservesLastGoodObservationAndFailureReason(t *testing.T) {
	f := &pulseHealthFactory{synced: map[*client.GVR]bool{client.PodGVR: true}, failures: make(map[*client.GVR]error)}
	f.objects = map[*client.GVR][]runtime.Object{client.PodGVR: {pulsePod("ready", true), pulsePod("unready", false)}}
	h := NewPulseHealth(f)
	c := make(HealthChan, 1)
	last := make(map[*client.GVR]HealthPoint)
	ctx := context.WithValue(context.Background(), internal.KeyWithMetrics, false)
	require.NoError(t, h.checkPulse(ctx, "apps", c, last, client.GVRs{client.PodGVR}))
	good := <-c
	require.Equal(t, 2, good.Total)
	require.Equal(t, 1, good.Faults)
	f.failures[client.PodGVR] = apierrors.NewServiceUnavailable("API disconnected")
	require.Error(t, h.checkPulse(ctx, "apps", c, last, client.GVRs{client.PodGVR}))
	retained := <-c
	require.Equal(t, HealthStale, retained.State)
	require.Equal(t, HealthUnavailable, retained.Failure)
	require.True(t, retained.HasValue())
	require.Equal(t, good.ObservedAt, retained.ObservedAt)
	require.Equal(t, good.Source, retained.Source)
	require.Equal(t, 2, retained.Total)
	require.Equal(t, 1, retained.Faults)
	require.Contains(t, retained.Message, "API disconnected")
	require.Equal(t, HealthStale, good.At(good.CheckedAt.Add(3*pulseRate)).State)
	delete(f.failures, client.PodGVR)
	require.NoError(t, h.checkPulse(ctx, "apps", c, last, client.GVRs{client.PodGVR}))
	require.Equal(t, HealthAvailable, (<-c).State, "successful recovery replaces stale availability")
}

func pulsePod(name string, ready bool) *unstructured.Unstructured {
	condition := "False"
	if ready {
		condition = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": "apps"},
		"spec":   map[string]any{"containers": []any{map[string]any{"name": "api"}}},
		"status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": condition}}, "containerStatuses": []any{map[string]any{"name": "api", "ready": ready, "state": map[string]any{"running": map[string]any{}}}}},
	}}
}

type pulseHealthConnection struct {
	client.Connection
	connected bool
}

func (c pulseHealthConnection) ConnectionOK() bool { return c.connected }

func TestPulseDisconnectRetainsObservationAndDoesNotReadCacheAsFresh(t *testing.T) {
	f := &pulseHealthFactory{conn: pulseHealthConnection{connected: true}, synced: map[*client.GVR]bool{client.PodGVR: true}}
	h := NewPulseHealth(f)
	last, c := make(map[*client.GVR]HealthPoint), make(HealthChan, 1)
	ctx := context.WithValue(context.Background(), internal.KeyWithMetrics, false)
	require.NoError(t, h.checkPulse(ctx, "apps", c, last, client.GVRs{client.PodGVR}))
	good := <-c
	f.conn = pulseHealthConnection{connected: false}
	require.Error(t, h.checkPulse(ctx, "apps", c, last, client.GVRs{client.PodGVR}))
	stale := <-c
	require.Equal(t, HealthStale, stale.State)
	require.Equal(t, good.ObservedAt, stale.ObservedAt)
	require.Contains(t, stale.Message, "disconnected")
	require.Len(t, f.reads, 1)
}

func TestPulseErrorClassificationAndCancellation(t *testing.T) {
	require.Equal(t, HealthAbsent, healthErrorState(apierrors.NewNotFound(schema.GroupResource{Resource: "events"}, "")))
	require.Equal(t, HealthAbsent, healthErrorState(&meta.NoResourceMatchError{PartialResource: schema.GroupVersionResource{Resource: "events"}}))
	require.Equal(t, HealthDenied, healthErrorState(errors.New("[list] access denied on resource nodes")))
	require.Equal(t, HealthUnavailable, healthErrorState(context.DeadlineExceeded))
	f := &pulseHealthFactory{}
	h := NewPulseHealth(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, h.checkPulse(ctx, "apps", make(HealthChan), make(map[*client.GVR]HealthPoint), PulseGVRs), context.Canceled)
	require.Empty(t, f.reads)
	// A full output channel must not prevent a watch from stopping.
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	f.synced = map[*client.GVR]bool{client.PodGVR: true}
	require.ErrorIs(t, h.checkPulse(context.WithValue(ctx, internal.KeyWithMetrics, false), "apps", make(HealthChan), make(map[*client.GVR]HealthPoint), client.GVRs{client.PodGVR}), context.DeadlineExceeded)
}

type blockedPulseFactory struct {
	*pulseHealthFactory
	started, release chan struct{}
}

func (f *blockedPulseFactory) List(gvr *client.GVR, ns string, wait bool, sel labels.Selector) ([]runtime.Object, error) {
	if gvr == client.PodGVR {
		close(f.started)
		<-f.release
	}
	return f.pulseHealthFactory.List(gvr, ns, wait, sel)
}

func TestPulseBoundsInheritedNoncancelableReadWithoutRepeatedWorkers(t *testing.T) {
	f := &blockedPulseFactory{pulseHealthFactory: &pulseHealthFactory{synced: map[*client.GVR]bool{client.PodGVR: true}}, started: make(chan struct{}), release: make(chan struct{})}
	h := NewPulseHealth(f)
	defer close(f.release)
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), internal.KeyWithMetrics, false), 20*time.Millisecond)
	defer cancel()
	point, err := h.checkBounded(ctx, "apps", client.PodGVR)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, HealthUnavailable, point.State)
	<-f.started
	point, err = h.checkBounded(context.Background(), "apps", client.PodGVR)
	require.ErrorContains(t, err, "still running")
	require.False(t, point.HasValue())
	// The same collector can still read another kind while one kind is stuck.
	point, err = h.checkBounded(context.Background(), "apps", client.DpGVR)
	require.NoError(t, err)
	require.Equal(t, HealthLoading, point.State)
}
