// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func capabilityFixture(err error, items ...unstructured.Unstructured) *fake.FakeDynamicClient {
	gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	reader := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "NodeMetricsList",
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
		{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}:      "IssuerList",
	})
	reader.PrependReactor("list", "*", func(_ ktesting.Action) (bool, runtime.Object, error) {
		if err != nil {
			return true, nil, err
		}
		return true, &unstructured.UnstructuredList{Items: items}, err
	})
	return reader
}

// client-go's root-list fake currently drops Limit while constructing its Action.
// Capture the real arguments before forwarding to the fake, so the wire bound
// assertion tests our request rather than that fake implementation detail.
type capturingCapabilities struct {
	dynamic.Interface
	options []metav1.ListOptions
}
type capturingCapabilityResource struct {
	dynamic.NamespaceableResourceInterface
	owner *capturingCapabilities
	scope dynamic.ResourceInterface
}

func (c *capturingCapabilities) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	resource := c.Interface.Resource(gvr)
	return capturingCapabilityResource{NamespaceableResourceInterface: resource, owner: c, scope: resource}
}
func (c capturingCapabilityResource) Namespace(namespace string) dynamic.ResourceInterface {
	c.scope = c.NamespaceableResourceInterface.Namespace(namespace)
	return c
}

//nolint:gocritic // Match dynamic.ResourceInterface's value argument.
func (c capturingCapabilityResource) List(ctx context.Context, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	c.owner.options = append(c.owner.options, options)
	return c.scope.List(ctx, options)
}
func metricObservation(at time.Time) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "NodeMetrics", "metadata": map[string]any{"name": "n1"}, "timestamp": at.Format(time.RFC3339Nano), "usage": map[string]any{"cpu": "0", "memory": "0"}}}
}

func TestCapabilityMetricsDistinguishesEvidenceStates(t *testing.T) {
	for _, tt := range []struct {
		name  string
		err   error
		items []unstructured.Unstructured
		want  capabilityState
	}{
		{name: "API absent", err: apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, ""), want: capabilityAbsent},
		{name: "RBAC denied", err: apierrors.NewForbidden(schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, "", errors.New("denied")), want: capabilityDenied},
		{name: "metrics 503", err: apierrors.NewServiceUnavailable("metrics-server returned HTTP 503"), want: capabilityUnavailable},
		{name: "empty sample", want: capabilityUnavailable},
		{name: "genuine zero", items: []unstructured.Unstructured{metricObservation(time.Now())}, want: capabilityAvailable},
		{name: "stale sample", items: []unstructured.Unstructured{metricObservation(time.Now().Add(-3 * time.Minute))}, want: capabilityStale},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := capabilityFixture(tt.err, tt.items...)
			capture := &capturingCapabilities{Interface: reader}
			report := collectCapabilities(t.Context(), capture, capabilityRequest{Task: "metrics", Context: "captured"}, nil)
			require.Len(t, report.Checks, 1)
			require.Equal(t, tt.want, report.Checks[0].State)
			require.NotEmpty(t, report.Checks[0].Recovery)
			require.Equal(t, "captured", report.Request.Context)
			require.Len(t, reader.Actions(), 1)
			action := reader.Actions()[0].(ktesting.ListAction)
			require.Len(t, capture.options, 1)
			require.Equal(t, int64(1), capture.options[0].Limit)
			require.Equal(t, "nodes", action.GetResource().Resource)
			require.Empty(t, action.GetNamespace())
		})
	}
}

func TestCapabilityOnlyChecksChosenIntegration(t *testing.T) {
	reader := capabilityFixture(apierrors.NewNotFound(schema.GroupResource{Resource: "certificates"}, ""))
	report := collectCapabilities(t.Context(), reader, capabilityRequest{Task: "cert-manager", Namespace: "cert-ns"}, nil)
	require.Len(t, report.Checks, 2)
	for _, action := range reader.Actions() {
		require.Equal(t, "cert-manager.io", action.GetResource().Group)
		require.Equal(t, "cert-ns", action.GetNamespace())
		require.Equal(t, int64(1), action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit)
	}
	before := len(reader.Actions())
	_ = collectCapabilities(t.Context(), reader, capabilityRequest{Task: ""}, nil)
	require.Len(t, reader.Actions(), before)
}

type blockingCapabilities struct{ dynamic.Interface }
type blockingCapabilityResource struct {
	dynamic.NamespaceableResourceInterface
}

func (blockingCapabilities) Resource(schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return blockingCapabilityResource{}
}
func (b blockingCapabilityResource) Namespace(string) dynamic.ResourceInterface { return b }

//nolint:gocritic // Match dynamic.ResourceInterface's value argument.
func (blockingCapabilityResource) List(ctx context.Context, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCapabilityDeadlineAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	snapshot := collectCapabilities(ctx, blockingCapabilities{}, capabilityRequest{Task: "metrics"}, nil)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	require.Equal(t, capabilityConnectionFailure, snapshot.Checks[0].State)
	canceled, stop := context.WithCancel(t.Context())
	stop()
	reader := capabilityFixture(nil, metricObservation(time.Now()))
	snapshot = collectCapabilities(canceled, reader, capabilityRequest{Task: "metrics"}, nil)
	require.Empty(t, reader.Actions())
	require.Equal(t, capabilityCanceled, snapshot.Checks[0].State)
}

func TestCapabilityHubbleReadinessAndRecovery(t *testing.T) {
	called := false
	probe := func(context.Context, hubble.Config) (string, error) { called = true; return "v1.2", nil }
	missing := checkRelayReadiness(t.Context(), hubble.Config{}, probe)
	require.Equal(t, capabilityNotConfigured, missing.State)
	require.Contains(t, missing.Recovery, "k9s.hubble.address")
	require.False(t, called)
	malformed := checkRelayReadiness(t.Context(), hubble.Config{Address: "relay:4245", CertFile: "without-key"}, probe)
	require.Equal(t, capabilityTLSFailure, malformed.State)
	require.Contains(t, malformed.Detail, "both Hubble certFile and keyFile")
	require.False(t, called)
	for _, tt := range []struct {
		name string
		err  error
		want capabilityState
	}{
		{name: "verified TLS", want: capabilityAvailable},
		{name: "bad trust", err: x509.UnknownAuthorityError{}, want: capabilityTLSFailure},
		{name: "connection refused", err: status.Error(codes.Unavailable, "connection refused"), want: capabilityConnectionFailure},
		{name: "relay denied", err: status.Error(codes.PermissionDenied, "ServerStatus denied"), want: capabilityDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			check := checkRelayReadiness(t.Context(), hubble.Config{Address: "relay.example:4245", ServerName: "relay.example"}, func(context.Context, hubble.Config) (string, error) { return "v1", tt.err })
			require.Equal(t, tt.want, check.State)
			require.NotEmpty(t, check.Recovery)
			if tt.err == nil {
				require.Contains(t, check.Detail, "TLS certificate verification succeeded")
				require.Contains(t, check.Detail, "does not imply")
			}
		})
	}
	plaintext := checkRelayReadiness(t.Context(), hubble.Config{Address: "127.0.0.1:4245", Plaintext: true}, probe)
	require.Contains(t, plaintext.Detail, "TLS identity was not verified")
	require.NotContains(t, plaintext.Detail, "TLS certificate verification succeeded")
}

func TestCapabilityPinsClientsAndSuppressesChangedViewContextGeneration(t *testing.T) {
	first := capabilityFixture(nil, metricObservation(time.Now()))
	second := capabilityFixture(apierrors.NewServiceUnavailable("wrong context"))
	mutable := &inspectionConnection{dynamic: first, typed: kubefake.NewSimpleClientset()}
	pinned, err := pinInspectionConnection(mutable)
	require.NoError(t, err)
	mutable.dynamic = second
	reader, err := pinned.DynDial()
	require.NoError(t, err)
	report := collectCapabilities(t.Context(), reader, capabilityRequest{Task: "metrics"}, nil)
	require.Equal(t, capabilityAvailable, report.Checks[0].State)
	require.Empty(t, second.Actions())
	app := NewApp(mock.NewMockConfig(t))
	details := &capabilityDetails{Details: NewDetails(app, "Diagnostics", "metrics", contentInspection, true), request: capabilityRequest{Context: app.Config.ActiveContextName()}, generation: 7}
	app.Content.Push(details)
	require.True(t, details.current(7))
	require.False(t, details.current(6))
	details.request.Context = "changed-context"
	require.False(t, details.current(7))
	details.request.Context = app.Config.ActiveContextName()
	app.Content.Push(NewDetails(app, "Replacement", "", contentInspection, true))
	require.False(t, details.current(7))
	retained := renderCapabilities(capabilitySnapshot{Request: capabilityRequest{Task: "metrics"}, CheckedAt: time.Now(), Checks: []capabilityCheck{{Name: "Node metrics", State: capabilityAvailable}}}, true)
	require.Contains(t, retained, "STALE retained observation")
}

func TestCapabilityRelayCannotOpenRetainedOrPendingReadiness(t *testing.T) {
	now := time.Now()
	details := &capabilityDetails{fresh: true, snapshot: capabilitySnapshot{CheckedAt: now, Checks: []capabilityCheck{{State: capabilityAvailable}}}}
	require.True(t, details.relayReady(now))
	details.fresh = false
	require.False(t, details.relayReady(now), "a retained or in-flight check is not current readiness")
	details.fresh = true
	require.False(t, details.relayReady(now.Add(3*time.Minute)))
	details.snapshot.Checks[0].State = capabilityDenied
	require.False(t, details.relayReady(now))
}
