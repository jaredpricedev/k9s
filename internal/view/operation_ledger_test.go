// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	guardedTestOriginal        = "original"
	guardedTestNodes           = "nodes"
	guardedTestNightly         = "nightly"
	guardedTestPluginPath      = "ns/original"
	guardedTestNodeUID         = "node-uid"
	guardedTestCronUID         = "cron-uid"
	guardedTestWaiting         = "waiting"
	guardedTestSelectedContext = "selected"
	guardedTestOtherContext    = "other"
	guardedTestChangedValue    = "changed"
	guardedTestPipeline        = "tr a-z A-Z"
	guardedTestNamespace       = "ns"
)

func waitOperationTask(t *testing.T, task *operationTask) operationReceipt {
	t.Helper()
	select {
	case <-task.finished:
		return task.receipt()
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not finish within its deadline")
	}
	return operationReceipt{}
}

func TestGuardedOperationCancelRetainsAcceptedStepsAndStopsNextTarget(t *testing.T) {
	targets := []SelectedResourceTarget{{Context: guardedTestOriginal, GVR: client.NodeGVR, Name: testWorkspaceWorkerName, UID: guardedTestNodeUID}, {Name: "not-started"}}
	task := newOperationTask(time.Second, targets)
	var registry operationRegistry
	if err := registry.add(task, "Drain", guardedTestOriginal); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	var calls atomic.Int32
	task.start(func(ctx context.Context, _ SelectedResourceTarget) error {
		calls.Add(1)
		operationBeginWrite(ctx)
		operationAcceptWrite(ctx, "node unschedulable=true")
		close(accepted)
		<-ctx.Done()
		return ctx.Err()
	}, nil, nil)
	<-accepted
	if got := task.receipt(); len(got.Outcomes[0].AcceptedSteps) != 1 || got.Outcomes[0].State != operationRunning {
		t.Fatal(got)
	}
	registry.cancelAll()
	receipt := waitOperationTask(t, task)
	if calls.Load() != 1 || !receipt.CancelRequested || receipt.Outcomes[0].State != operationUnknown || receipt.Outcomes[1].State != operationCancelled || !receipt.Outcomes[1].NotSubmitted {
		t.Fatal(receipt, calls.Load())
	}
	if len(receipt.Outcomes[0].AcceptedSteps) != 1 {
		t.Fatal("accepted cordon lost on cancellation", receipt)
	}
	receipt.Outcomes[0].AcceptedSteps[0] = "mutated copy"
	text := operationReceiptText(task.receipt(), 1, 1)
	for _, expected := range []string{guardedTestOriginal, guardedTestNodeUID, "UNKNOWN", "node unschedulable=true", "does not roll back", "No automatic retry"} {
		if !strings.Contains(text, expected) {
			t.Fatal("missing retained evidence", expected, text)
		}
	}
	if strings.Contains(text, "mutated copy") || len(registry.list()) != 1 {
		t.Fatal("receipt was mutable or lost")
	}
}

func TestGuardedOperationDeniedPartialAndUnknownOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		attempted bool
		err       error
		state     operationState
	}{
		{testWorkspaceCoverageDenied, true, apierrors.NewForbidden(schema.GroupResource{Resource: guardedTestNodes}, testWorkspaceWorkerName, errors.New(testWorkspaceCoverageDenied)), operationFailed},
		{"transport", true, io.ErrUnexpectedEOF, operationUnknown},
		{"cancel-before-write", false, context.Canceled, operationCancelled},
		{"cancel-after-write", true, context.Canceled, operationUnknown},
		{"accepted", true, nil, operationAccepted},
	}
	var attempts atomic.Int32
	targets := make([]SelectedResourceTarget, len(cases))
	for index, tc := range cases {
		targets[index].Name = tc.name
	}
	task := startOperationBatch(time.Second, targets, func(ctx context.Context, target SelectedResourceTarget) error {
		attempts.Add(1)
		for _, tc := range cases {
			if tc.name == target.Name {
				if tc.attempted {
					operationBeginWrite(ctx)
				}
				return tc.err
			}
		}
		return nil
	}, nil, nil)
	receipt := waitOperationTask(t, task)
	for index, tc := range cases {
		if receipt.Outcomes[index].State != tc.state {
			t.Fatal(tc.name, receipt.Outcomes[index])
		}
	}
	if attempts.Load() != int32(len(cases)) {
		t.Fatal("an uncertain write was retried", attempts.Load())
	}
}

func TestGuardedOperationPanicAndOutputAreBounded(t *testing.T) {
	task := startOperationBatch(time.Second, []SelectedResourceTarget{{Name: testWorkspaceWorkerName}}, func(ctx context.Context, _ SelectedResourceTarget) error {
		operationBeginWrite(ctx)
		_, _ = io.WriteString(operationOutput(ctx), strings.Repeat("x", 40*1024))
		panic("secret-value-should-not-be-retained")
	}, nil, nil)
	receipt := waitOperationTask(t, task)
	if receipt.Outcomes[0].State != operationUnknown || len(receipt.Outcomes[0].Output) > 33*1024 || !strings.Contains(receipt.Outcomes[0].Output, "truncated") {
		t.Fatal(receipt.Outcomes[0].State, len(receipt.Outcomes[0].Output))
	}
	if strings.Contains(operationReceiptText(receipt, 1, 1), "secret-value") {
		t.Fatal("worker panic exposed sensitive inputs")
	}
}

func TestGuardedOperationRegistryBoundsActiveWorkWithoutDroppingReceipts(t *testing.T) {
	var registry operationRegistry
	for range maxActiveOperations {
		task := newOperationTask(time.Second, []SelectedResourceTarget{{Name: guardedTestWaiting}})
		if err := registry.add(task, guardedTestWaiting, guardedTestOriginal); err != nil {
			t.Fatal(err)
		}
		task.start(func(ctx context.Context, _ SelectedResourceTarget) error { <-ctx.Done(); return ctx.Err() }, nil, nil)
	}
	rejected := newOperationTask(time.Second, nil)
	defer rejected.cancel()
	if registry.add(rejected, "excess", guardedTestOriginal) == nil {
		t.Fatal("unbounded active workers")
	}
	registry.cancelAll()
	for _, task := range registry.list() {
		if got := waitOperationTask(t, task); !got.CancelRequested || got.Outcomes[0].State != operationCancelled {
			t.Fatal(got)
		}
	}
	if len(registry.list()) != maxActiveOperations {
		t.Fatal("cancellation erased receipts")
	}
}

func TestGuardedOperationCancellationRetainsReviewedReceiptIdentityAfterHistoryPruning(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	for range maxOperationHistory - 1 {
		ended := newOperationTask(time.Second, nil)
		ended.ended = time.Now()
		ended.cancel()
		if err := app.operations.add(ended, "finished", guardedTestOriginal); err != nil {
			t.Fatal(err)
		}
	}
	reviewed := newOperationTask(time.Second, nil)
	defer reviewed.cancel()
	if err := app.operations.add(reviewed, guardedTestWaiting, guardedTestOriginal); err != nil {
		t.Fatal(err)
	}
	view := &operationReview{Details: NewDetails(app, "Operations", "receipts", contentTXT, false), index: maxOperationHistory - 1}
	view.render()
	later := newOperationTask(time.Second, nil)
	defer later.cancel()
	if err := app.operations.add(later, "later", guardedTestOriginal); err != nil {
		t.Fatal(err)
	}
	view.cancelCmd(nil)
	if !errors.Is(reviewed.ctx.Err(), context.Canceled) || later.ctx.Err() != nil {
		t.Fatal("history pruning redirected cancellation to another operation", reviewed.ctx.Err(), later.ctx.Err())
	}
}

func TestGuardedOperationSubmitReturnsReceiptAndRejectsReadOnly(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	session := &operationSession{app: app, context: app.Config.ActiveContextName(), revision: app.Config.DestinationRevision(),
		timeout: time.Second, stillCurrent: func() bool { return true }}
	var attempted atomic.Int32
	work := func(ctx context.Context, _ SelectedResourceTarget) error {
		attempted.Add(1)
		operationBeginWrite(ctx)
		operationAcceptWrite(ctx, "fixture acknowledgement")
		return nil
	}
	targets := []SelectedResourceTarget{{Name: testWorkspaceWorkerName}}
	task := session.submit(guardedTestWaiting, targets, work, nil)
	if task == nil {
		t.Fatal("accepted submission did not return its receipt")
	}
	receipt := waitOperationTask(t, task)
	if receipt.ID == 0 || receipt.Outcomes[0].State != operationAccepted || attempted.Load() != 1 {
		t.Fatal(receipt)
	}
	app.Config.K9s.ReadOnly = true
	if refused := session.submit(guardedTestWaiting, targets, work, nil); refused != nil || attempted.Load() != 1 {
		t.Fatal("read-only submission returned an operation or attempted work")
	}
}
