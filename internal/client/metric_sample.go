// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package client

import (
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	mv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// MetricState distinguishes a measured zero from an absent or unusable sample.
type MetricState string

const (
	MetricsAvailable     MetricState = "available"
	MetricsUnavailable   MetricState = "unavailable"
	MetricsDenied        MetricState = "denied"
	MetricsStale         MetricState = "stale"
	MetricsNotConfigured MetricState = "not configured"
	NodeMetricsSource                = "metrics.k8s.io/v1beta1/nodes"
	PodMetricsSource                 = "metrics.k8s.io/v1beta1/pods"
	MetricMaxAge                     = 2 * time.Minute
)

var (
	ErrMetricsNotConfigured = errors.New("metrics-server is not configured")
	ErrMetricsDenied        = errors.New("metrics access denied")
	ErrMetricsEmpty         = errors.New("no usable metrics samples")
)

// MetricSample is shared by cluster chrome and Pulse. ObservedAt is the server's
// observation time, not the time a cached sample happened to be rendered.
type MetricSample struct {
	State      MetricState
	Values     ClusterMetrics
	ObservedAt time.Time
	Source     string
	Reason     string
	Failure    MetricState
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func (s MetricSample) HasValue() bool {
	return s.State == MetricsAvailable || s.State == MetricsStale
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func (s MetricSample) Fresh() bool {
	return s.State == MetricsAvailable && !s.ObservedAt.IsZero() && time.Since(s.ObservedAt) <= MetricMaxAge
}

// At re-evaluates an available observation's age without changing its server
// timestamp. Retained data must become stale even while collection is blocked.
//
//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func (s MetricSample) At(now time.Time) MetricSample {
	if s.State == MetricsAvailable {
		return NewMetricSample(s.Values, s.ObservedAt, s.Source, now)
	}
	return s
}

// MetricFailure retains a last good observation only when it is explicitly stale.
//
//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func MetricFailure(previous MetricSample, state MetricState, source, reason string) MetricSample {
	if previous.HasValue() && previous.Source == source {
		previous.State, previous.Failure, previous.Reason = MetricsStale, state, reason
		return previous
	}
	return MetricSample{State: state, Source: source, Reason: reason, Failure: state}
}

func MetricErrorState(err error) MetricState {
	switch {
	case errors.Is(err, ErrMetricsNotConfigured):
		return MetricsNotConfigured
	case errors.Is(err, ErrMetricsDenied), apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return MetricsDenied
	default:
		return MetricsUnavailable
	}
}

// MetricsAccess checks discovery and authorization independently from collection.
// Call it from a worker: discovery and CanI may issue API requests.
func MetricsAccess(conn Connection, ns string, gvr *GVR) error {
	if discovery, ok := conn.(interface{ MetricsDiscovery() error }); ok {
		if err := discovery.MetricsDiscovery(); err != nil {
			return err
		}
	} else if !conn.HasMetrics() {
		return ErrMetricsNotConfigured
	}
	authorized, err := conn.CanI(ns, gvr, "", ListAccess)
	if err != nil {
		return err
	}
	if !authorized {
		return ErrMetricsDenied
	}
	return nil
}

func NewMetricSample(values ClusterMetrics, observedAt time.Time, source string, now time.Time) MetricSample {
	if observedAt.IsZero() {
		return MetricFailure(MetricSample{}, MetricsUnavailable, source, "missing observation time")
	}
	s := MetricSample{State: MetricsAvailable, Values: values, ObservedAt: observedAt, Source: source}
	if now.Sub(observedAt) > MetricMaxAge {
		s.State, s.Reason = MetricsStale, "observation is older than two minutes"
	}
	return s
}

// NodeMetricsObservedAt uses the oldest observation so partial freshness is never
// presented as a fresh cluster-wide measurement.
func NodeMetricsObservedAt(metrics *mv1beta1.NodeMetricsList) time.Time {
	var oldest time.Time
	if metrics == nil {
		return oldest
	}
	for i := range metrics.Items {
		metric := &metrics.Items[i]
		if metric.Timestamp.IsZero() {
			return time.Time{}
		}
		if oldest.IsZero() || metric.Timestamp.Time.Before(oldest) {
			oldest = metric.Timestamp.Time
		}
	}
	return oldest
}

func PodMetricsObservedAt(metrics *mv1beta1.PodMetricsList) time.Time {
	var oldest time.Time
	if metrics == nil {
		return oldest
	}
	for i := range metrics.Items {
		metric := &metrics.Items[i]
		if metric.Timestamp.IsZero() {
			return time.Time{}
		}
		if oldest.IsZero() || metric.Timestamp.Time.Before(oldest) {
			oldest = metric.Timestamp.Time
		}
	}
	return oldest
}
