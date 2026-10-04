// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func outcomeTestRequest(o *unstructured.Unstructured) *RolloutOutcomeRequest {
	s := NewRolloutSnapshot(o, nil, nil, nil, "lab", time.Now())
	return &RolloutOutcomeRequest{Identity: s.Identity, Generation: *s.Generation, TemplateSHA256: s.TemplateSHA256, Interval: 100 * time.Millisecond, Timeout: time.Second}
}

func TestRolloutOutcomeAcceptedIsSeparateFromObservedCompletion(t *testing.T) {
	o := rolloutTestDeployment()
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	reads := 0
	dyn.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		require.Equal(t, rolloutTestNamespace, action.GetNamespace())
		require.Equal(t, rolloutTestName, action.(ktesting.GetAction).GetName())
		reads++
		observed := o.DeepCopy()
		if reads == 1 {
			_ = unstructured.SetNestedField(observed.Object, int64(1), "status", "updatedReplicas")
		}
		return true, observed, nil
	})
	r := outcomeTestRequest(o)
	r.AcceptedAt, r.OperationID = time.Now().Add(-time.Second), "accepted-receipt-12"
	updates := 0
	result := FollowRollout(t.Context(), dyn, r, func(observed *RolloutOutcome) {
		updates++
		require.Equal(t, RolloutProgressing, observed.State)
		require.Equal(t, r.AcceptedAt, observed.AcceptedAt)
	})
	require.Equal(t, RolloutComplete, result.State)
	require.Equal(t, 1, updates)
	require.Equal(t, 2, result.Reads)
	require.Equal(t, r.Identity, result.Identity)
	require.Empty(t, result.Snapshot.Pods)
	for _, action := range dyn.Actions() {
		require.Equal(t, "get", action.GetVerb())
		require.Equal(t, "deployments", action.GetResource().Resource)
	}
}

func TestRolloutOutcomeRejectsReplacementAndSupersedingSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
	}{
		{"UID", func(o *unstructured.Unstructured) { o.SetUID("replacement") }},
		{"generation", func(o *unstructured.Unstructured) { o.SetGeneration(4) }},
		{"template", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "new-image", "spec", "template", "metadata", "labels", "new")
		}},
		{"kind", func(o *unstructured.Unstructured) { o.SetKind(rolloutStatefulSet) }},
		{"namespace", func(o *unstructured.Unstructured) { o.SetNamespace("other") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := rolloutTestDeployment()
			r := outcomeTestRequest(o)
			tc.change(o)
			dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
			dyn.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) { return true, o, nil })
			result := FollowRollout(t.Context(), dyn, r, nil)
			require.Equal(t, RolloutSuperseded, result.State)
			require.Nil(t, result.Snapshot)
		})
	}
}

func TestRolloutOutcomeTimeoutCancellationDenialAndReadBudget(t *testing.T) {
	o := rolloutTestDeployment()
	_ = unstructured.SetNestedField(o.Object, int64(1), "status", "updatedReplicas")
	for _, tc := range []struct {
		name, state string
		err         error
		cancel      bool
		timeout     time.Duration
		reads       int
	}{
		{"deadline", RolloutTimedOut, nil, false, 20 * time.Millisecond, 0},
		{"read cap", RolloutTimedOut, nil, false, time.Second, 1},
		{"cancel after accepted", RolloutCanceled, nil, true, time.Second, 0},
		{"denied", RolloutUnknown, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, rolloutTestName, nil), false, time.Second, 0},
		{"removed", RolloutSuperseded, apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, rolloutTestName), false, time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), o)
			if tc.err != nil {
				dyn.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err })
			}
			r := outcomeTestRequest(o)
			r.AcceptedAt, r.OperationID, r.Timeout, r.MaxReads = time.Now(), "accepted-13", tc.timeout, tc.reads
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := FollowRollout(ctx, dyn, r, func(*RolloutOutcome) {
				if tc.cancel {
					cancel()
				}
			})
			require.Equal(t, tc.state, result.State)
			require.Equal(t, "accepted-13", result.OperationID)
			require.NotZero(t, result.FinishedAt)
			require.Len(t, dyn.Actions(), 1)
		})
	}
}

func TestRolloutOutcomeBoundsNonCooperativeReadAndInvalidReceipt(t *testing.T) {
	o := rolloutTestDeployment()
	r := outcomeTestRequest(o)
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	release := make(chan struct{})
	defer close(release)
	dyn.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) { <-release; return true, o, nil })
	r.Timeout = 20 * time.Millisecond
	started := time.Now()
	result := FollowRollout(t.Context(), dyn, r, nil)
	require.Equal(t, RolloutTimedOut, result.State)
	require.Less(t, time.Since(started), time.Second)
	r.AcceptedAt = time.Now() // Acceptance requires an actual operation receipt identifier.
	result = FollowRollout(t.Context(), dyn, r, nil)
	require.Equal(t, RolloutUnknown, result.State)
	require.Zero(t, result.Reads)
}
