// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package client

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type metricsAccessFixture struct {
	Connection
	discovered bool
	authorized bool
	accessErr  error
}

func (m metricsAccessFixture) HasMetrics() bool { return m.discovered }
func (m metricsAccessFixture) CanI(string, *GVR, string, []string) (bool, error) {
	return m.authorized, m.accessErr
}

func TestMetricAvailability(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		conn metricsAccessFixture
		err  error
		want MetricState
	}{
		{name: "absent metrics-server", conn: metricsAccessFixture{}, want: MetricsNotConfigured},
		{name: "denied authorization", conn: metricsAccessFixture{discovered: true}, want: MetricsDenied},
		{name: "authorization outage", conn: metricsAccessFixture{discovered: true, accessErr: apierrors.NewServiceUnavailable("HTTP 503")}, want: MetricsUnavailable},
		{name: "advertised API read 503", conn: metricsAccessFixture{discovered: true, authorized: true}, err: apierrors.NewServiceUnavailable("HTTP 503"), want: MetricsUnavailable},
		{name: "read forbidden", conn: metricsAccessFixture{discovered: true, authorized: true}, err: apierrors.NewForbidden(schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, "", nil), want: MetricsDenied},
		{name: "empty observations", conn: metricsAccessFixture{discovered: true, authorized: true}, err: ErrMetricsEmpty, want: MetricsUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MetricsAccess(tt.conn, "", NmxGVR)
			if err == nil {
				err = tt.err
			}
			require.Error(t, err)
			sample := MetricFailure(MetricSample{}, MetricErrorState(err), NodeMetricsSource, err.Error())
			require.Equal(t, tt.want, sample.State)
			require.False(t, sample.HasValue())
		})
	}
	zero := NewMetricSample(ClusterMetrics{}, now, NodeMetricsSource, now)
	require.True(t, zero.Fresh())
	require.True(t, zero.HasValue())
	require.Equal(t, 0, zero.Values.PercCPU)
	stale := NewMetricSample(ClusterMetrics{PercCPU: 40, PercMEM: 30}, now.Add(-3*time.Minute), NodeMetricsSource, now)
	require.Equal(t, MetricsStale, stale.State)
	require.True(t, stale.HasValue())
	require.False(t, stale.Fresh())
	missing := NewMetricSample(ClusterMetrics{}, time.Time{}, NodeMetricsSource, now)
	require.False(t, missing.HasValue())
	retained := MetricFailure(zero, MetricsUnavailable, NodeMetricsSource, "HTTP 503")
	require.Equal(t, MetricsStale, retained.State)
	require.Equal(t, MetricsUnavailable, retained.Failure)
	require.Equal(t, now, retained.ObservedAt)
	require.False(t, MetricFailure(zero, MetricsUnavailable, PodMetricsSource, "different destination").HasValue())
}

func TestClusterLoadRejectsMissingSamplesAndPreservesMeasuredZero(t *testing.T) {
	node := v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: v1.NodeStatus{Allocatable: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1"), v1.ResourceMemory: resource.MustParse("1Gi")}}}
	nodes := &v1.NodeList{Items: []v1.Node{node}}
	dial := NewMetricsServer(nil)
	for name, observations := range map[string]*metrics.NodeMetricsList{
		"empty":         {},
		"wrong node":    {Items: []metrics.NodeMetrics{{ObjectMeta: metav1.ObjectMeta{Name: "n2"}, Usage: v1.ResourceList{v1.ResourceCPU: resource.MustParse("0"), v1.ResourceMemory: resource.MustParse("0")}}}},
		"missing usage": {Items: []metrics.NodeMetrics{{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			var values ClusterMetrics
			require.ErrorIs(t, dial.ClusterLoad(nodes, observations, &values), ErrMetricsEmpty)
		})
	}
	var values ClusterMetrics
	require.NoError(t, dial.ClusterLoad(nodes, &metrics.NodeMetricsList{Items: []metrics.NodeMetrics{{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Usage: v1.ResourceList{v1.ResourceCPU: resource.MustParse("0"), v1.ResourceMemory: resource.MustParse("0")}}}}, &values))
	require.Equal(t, ClusterMetrics{}, values)
}

func TestMetricSampleRetainedAvailableObservationAgesWithoutCollection(t *testing.T) {
	now := time.Now()
	sample := NewMetricSample(ClusterMetrics{PercCPU: 21, PercMEM: 63}, now, NodeMetricsSource, now)
	aged := sample.At(now.Add(MetricMaxAge + time.Second))
	require.Equal(t, MetricsStale, aged.State)
	require.Equal(t, sample.Values, aged.Values)
	require.Equal(t, sample.ObservedAt, aged.ObservedAt)
	require.True(t, aged.HasValue())
	require.False(t, aged.Fresh())
	originalOld := sample
	originalOld.ObservedAt = now.Add(-MetricMaxAge - time.Second)
	require.False(t, originalOld.Fresh(), "an available state alone cannot establish freshness")
}
