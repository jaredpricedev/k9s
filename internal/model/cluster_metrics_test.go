// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/cache"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type headerMetricsConnection struct {
	client.Connection
	discovered, authorized bool
}

func (c headerMetricsConnection) HasMetrics() bool { return c.discovered }
func (c headerMetricsConnection) CanI(string, *client.GVR, string, []string) (bool, error) {
	return c.authorized, nil
}

type headerMetricsFactory struct {
	dao.Factory
	conn client.Connection
}

func (f headerMetricsFactory) Client() client.Connection { return f.conn }

type headerMetricsServer struct {
	MetricsServer
	observations *metrics.NodeMetricsList
	err          error
	calls        int
}

func (s *headerMetricsServer) FetchNodesMetrics(context.Context) (*metrics.NodeMetricsList, error) {
	s.calls++
	return s.observations, s.err
}

func TestHeaderMetricsAvailableZeroAndUnavailable503(t *testing.T) {
	now := time.Now()
	node := v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: v1.NodeStatus{Allocatable: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1"), v1.ResourceMemory: resource.MustParse("1Gi")}}}
	zero := &metrics.NodeMetricsList{Items: []metrics.NodeMetrics{{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Timestamp: metav1.NewTime(now), Usage: v1.ResourceList{v1.ResourceCPU: resource.MustParse("0"), v1.ResourceMemory: resource.MustParse("0")}}}}
	for _, tt := range []struct {
		name                   string
		discovered, authorized bool
		observations           *metrics.NodeMetricsList
		err                    error
		want                   client.MetricState
		reads                  int
	}{
		{name: "absent", want: client.MetricsNotConfigured},
		{name: "denied", discovered: true, want: client.MetricsDenied},
		{name: "HTTP 503", discovered: true, authorized: true, err: apierrors.NewServiceUnavailable("HTTP 503"), want: client.MetricsUnavailable, reads: 1},
		{name: "empty", discovered: true, authorized: true, observations: &metrics.NodeMetricsList{}, want: client.MetricsUnavailable, reads: 1},
		{name: "measured zero", discovered: true, authorized: true, observations: zero, want: client.MetricsAvailable, reads: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := &headerMetricsServer{MetricsServer: client.NewMetricsServer(nil), observations: tt.observations, err: tt.err}
			cluster := &Cluster{factory: headerMetricsFactory{conn: headerMetricsConnection{discovered: tt.discovered, authorized: tt.authorized}}, mx: server, cache: cache.NewLRUExpireCache(2)}
			cluster.cache.Add(clusterNodesKey, &v1.NodeList{Items: []v1.Node{node}}, time.Minute)
			sample := cluster.MetricsSample(context.Background(), client.MetricSample{})
			require.Equal(t, tt.want, sample.State)
			require.Equal(t, tt.reads, server.calls)
			require.Equal(t, tt.want == client.MetricsAvailable, sample.HasValue())
			if sample.HasValue() {
				require.Zero(t, sample.Values.PercCPU)
				require.Zero(t, sample.Values.PercMEM)
				require.Equal(t, now, sample.ObservedAt)
			}
			if tt.err != nil {
				lastGood := client.NewMetricSample(client.ClusterMetrics{PercCPU: 80, PercMEM: 20}, now, client.NodeMetricsSource, now)
				stale := cluster.MetricsSample(context.Background(), lastGood)
				require.Equal(t, client.MetricsStale, stale.State)
				require.Equal(t, lastGood.Values, stale.Values)
				require.Equal(t, lastGood.ObservedAt, stale.ObservedAt)
			}
		})
	}
}
