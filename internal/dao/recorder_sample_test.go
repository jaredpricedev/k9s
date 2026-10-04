// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dao

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/cache"
)

func TestRecorderFailureRetainsLabeledSampleAndNeverRecordsMissingZero(t *testing.T) {
	recorder := &Recorder{series: cache.NewLRUExpireCache(10), mxChan: make(MetricsChan, 5), lastPoints: make(map[string]Point)}
	now := time.Now()
	good := Point{Time: now, Tags: map[string]string{"type": nodeMetrics}, Value: client.NodeMetrics{CurrentMetrics: client.CurrentMetrics{CurrentCPU: 800, CurrentMEM: 200}}, Sample: client.NewMetricSample(client.ClusterMetrics{PercCPU: 80, PercMEM: 20}, now, client.NodeMetricsSource, now)}
	recorder.publishPoint(t.Context(), good)
	require.Equal(t, client.MetricsAvailable, (<-recorder.mxChan)[0].Sample.State)
	require.Len(t, recorder.series.Keys(), 1)
	recorder.publishFailure(t.Context(), nodeMetrics, "all", client.NodeMetricsSource, apierrors.NewServiceUnavailable("HTTP 503"))
	stale := (<-recorder.mxChan)[0]
	require.Equal(t, client.MetricsStale, stale.Sample.State)
	require.Equal(t, client.MetricsUnavailable, stale.Sample.Failure)
	require.Equal(t, good.Value, stale.Value)
	require.Equal(t, now, stale.Sample.ObservedAt)
	require.Len(t, recorder.series.Keys(), 1, "failed observation must not create a measured zero in history")
	recorder.publishFailure(t.Context(), podMetrics, "restricted", client.PodMetricsSource, client.ErrMetricsDenied)
	denied := (<-recorder.mxChan)[0]
	require.Equal(t, client.MetricsDenied, denied.Sample.State)
	require.False(t, denied.Sample.HasValue())
	require.Len(t, recorder.series.Keys(), 1)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	recorder.publishPoint(canceled, good)
	require.Empty(t, recorder.mxChan)
	require.Len(t, recorder.series.Keys(), 1)
}

func TestRecorderMeasuredZeroIsARealFreshSample(t *testing.T) {
	recorder := &Recorder{series: cache.NewLRUExpireCache(10), mxChan: make(MetricsChan, 1)}
	now := time.Now()
	zero := Point{Time: now, Tags: map[string]string{"type": nodeMetrics}, Sample: client.NewMetricSample(client.ClusterMetrics{}, now, client.NodeMetricsSource, now)}
	recorder.publishPoint(t.Context(), zero)
	point := (<-recorder.mxChan)[0]
	require.True(t, point.Sample.Fresh())
	require.True(t, point.Sample.HasValue())
	require.Zero(t, point.Sample.Values.PercCPU)
	require.Len(t, recorder.series.Keys(), 1)
}

func TestRecorderReplacementCancelsBlockedPublisherBeforeReusingChannel(t *testing.T) {
	oldContext, oldCancel := context.WithCancel(t.Context())
	defer oldCancel()
	oldChannel := make(MetricsChan)
	recorder := &Recorder{series: cache.NewLRUExpireCache(10), mxChan: oldChannel, watchCancel: oldCancel}
	point := Point{Time: time.Now(), Tags: map[string]string{"type": nodeMetrics}, Sample: client.NewMetricSample(client.ClusterMetrics{}, time.Now(), client.NodeMetricsSource, time.Now())}
	published := make(chan struct{})
	go func() { recorder.publishPoint(oldContext, point); close(published) }()
	require.Eventually(t, func() bool {
		if recorder.mx.TryLock() {
			recorder.mx.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond, "the old publisher should be blocked waiting for its consumer")
	replacement := make(chan MetricsChan, 1)
	newContext, newCancel := context.WithCancel(t.Context())
	defer newCancel()
	go func() { replacement <- recorder.Watch(newContext, "replacement") }()
	var newChannel MetricsChan
	select {
	case newChannel = <-replacement:
	case <-time.After(time.Second):
		t.Fatal("replacing a blocked watcher must not deadlock")
	}
	<-published
	require.ErrorIs(t, oldContext.Err(), context.Canceled)
	_, open := <-oldChannel
	require.False(t, open)
	recorder.publishPoint(oldContext, point)
	select {
	case points := <-newChannel:
		require.False(t, points[0].Sample.HasValue(), "old watcher samples must never enter the replacement watch")
	default:
	}
}
