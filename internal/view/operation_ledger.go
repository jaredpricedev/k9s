// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type operationState string

const (
	operationPending      operationState = "PENDING"
	operationRunning      operationState = "RUNNING"
	operationAccepted     operationState = "ACCEPTED"
	operationObserved     operationState = "OBSERVED"
	operationCompleted    operationState = "COMPLETED"
	operationFailed       operationState = "FAILED"
	operationCancelled    operationState = "CANCELED"
	operationUnknown      operationState = "UNKNOWN"
	operationNotSubmitted operationState = "NOT SUBMITTED"
	maxOperationHistory                  = 64
	maxActiveOperations                  = 8
	maxOperationDeadline                 = 10 * time.Minute
)

type operationProgressKey struct{}

// A target can have accepted steps even when a later step fails or its result is
// unknown. In particular, canceling drain does not uncordon the node or undo
// an accepted eviction. Keep those facts separate from the final wait outcome.
type operationProgress struct {
	mu              sync.Mutex
	attempted       bool
	accepted        []string
	output          strings.Builder
	dropped         int
	publishAccepted func([]string)
	localCompleted  bool
	observed        bool
}

// OBSERVED is set only by a worker after an independent bounded named API read
// verifies its accepted UID/generation and admitted fields. The worker records
// the observed resourceVersion, source and time separately in its receipt.
// It is not an API acceptance response or a runtime/controller readiness claim.
func operationObserveWrite(ctx context.Context) {
	if progress, ok := ctx.Value(operationProgressKey{}).(*operationProgress); ok {
		progress.mu.Lock()
		progress.observed = true
		progress.mu.Unlock()
	}
}

func operationBeginWrite(ctx context.Context) {
	if progress, ok := ctx.Value(operationProgressKey{}).(*operationProgress); ok {
		progress.mu.Lock()
		progress.attempted = true
		progress.mu.Unlock()
	}
}

func operationAcceptWrite(ctx context.Context, step string) {
	if progress, ok := ctx.Value(operationProgressKey{}).(*operationProgress); ok {
		progress.mu.Lock()
		progress.accepted = append(progress.accepted, step)
		if len(progress.accepted) > 128 {
			progress.accepted = progress.accepted[:128]
			progress.accepted[127] = "Additional accepted steps omitted (128-step receipt limit); inspect the destination"
		}
		accepted := slices.Clone(progress.accepted)
		progress.mu.Unlock()
		if progress.publishAccepted != nil {
			progress.publishAccepted(accepted)
		}
	}
}

func operationCommandCompleted(ctx context.Context) {
	if progress, ok := ctx.Value(operationProgressKey{}).(*operationProgress); ok {
		progress.mu.Lock()
		progress.localCompleted = true
		progress.mu.Unlock()
	}
}

func (p *operationProgress) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	const limit = 32 * 1024
	n := min(len(data), max(0, limit-p.output.Len()))
	p.output.Write(data[:n])
	p.dropped += len(data) - n
	return len(data), nil
}

func operationOutput(ctx context.Context) io.Writer {
	if progress, ok := ctx.Value(operationProgressKey{}).(*operationProgress); ok {
		return progress
	}
	return io.Discard
}

type operationTask struct {
	mu              sync.Mutex
	id              uint64
	action, context string
	started, ended  time.Time
	outcomes        []operationOutcome
	cancelRequested bool
	ctx             context.Context
	cancel          context.CancelFunc
	finished        chan struct{}
}

type operationReceipt struct {
	ID              uint64
	Action, Context string
	Started, Ended  time.Time
	CancelRequested bool
	Outcomes        []operationOutcome
}

func boundedOperationTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 10 * time.Second
	}
	return min(timeout, maxOperationDeadline)
}

func newOperationTask(timeout time.Duration, targets []SelectedResourceTarget) *operationTask {
	ctx, cancel := context.WithTimeout(context.Background(), boundedOperationTimeout(timeout))
	task := &operationTask{ctx: ctx, cancel: cancel, started: time.Now(), finished: make(chan struct{})}
	for _, target := range targets {
		task.outcomes = append(task.outcomes, operationOutcome{Target: target, State: operationPending})
	}
	return task
}

func (t *operationTask) cancelRemaining() {
	t.mu.Lock()
	if t.ended.IsZero() {
		t.cancelRequested = true
		t.cancel()
	}
	t.mu.Unlock()
}

func (t *operationTask) receipt() operationReceipt {
	t.mu.Lock()
	defer t.mu.Unlock()
	outcomes := slices.Clone(t.outcomes)
	for i := range outcomes {
		outcomes[i].AcceptedSteps = slices.Clone(outcomes[i].AcceptedSteps)
	}
	return operationReceipt{ID: t.id, Action: t.action, Context: t.context,
		Started: t.started, Ended: t.ended, CancelRequested: t.cancelRequested, Outcomes: outcomes}
}

func (t *operationTask) start(work func(context.Context, SelectedResourceTarget) error,
	publish func(operationOutcome), done func([]operationOutcome),
) {
	go func() {
		defer close(t.finished)
		defer t.cancel()
		pending := t.receipt().Outcomes
		for i := range pending {
			target := pending[i].Target
			err := t.ctx.Err()
			notSubmitted := err != nil
			if err == nil && target.UnavailableReason != "" {
				err, notSubmitted = target.Err(), true
			}
			progress := &operationProgress{publishAccepted: func(steps []string) {
				t.mu.Lock()
				t.outcomes[i].AcceptedSteps = steps
				t.mu.Unlock()
			}}
			ctx := context.WithValue(t.ctx, operationProgressKey{}, progress)
			if err == nil {
				t.mu.Lock()
				t.outcomes[i].State = operationRunning
				t.mu.Unlock()
				err = callOperationWork(ctx, target, work)
			}
			if err != nil && ctx.Err() != nil {
				err = errors.Join(err, ctx.Err())
			}
			outcome := operationOutcome{Target: target, Err: err, NotSubmitted: notSubmitted}
			progress.mu.Lock()
			outcome.AcceptedSteps = slices.Clone(progress.accepted)
			outcome.Output = progress.output.String()
			if progress.dropped > 0 {
				outcome.Output += fmt.Sprintf("\nOutput truncated: %d bytes omitted.\n", progress.dropped)
			}
			outcome.State = operationResultState(err, notSubmitted, progress.attempted)
			if err == nil && progress.localCompleted {
				outcome.State = operationCompleted
			}
			if err == nil && progress.observed {
				outcome.State = operationObserved
			}
			progress.mu.Unlock()
			t.mu.Lock()
			t.outcomes[i] = outcome
			t.mu.Unlock()
			if publish != nil {
				publish(outcome)
			}
		}
		t.mu.Lock()
		t.ended = time.Now()
		t.mu.Unlock()
		if done != nil {
			done(t.receipt().Outcomes)
		}
	}()
}

//nolint:gocritic // Workers receive immutable captured identities, not mutable UI pointers.
func callOperationWork(ctx context.Context, target SelectedResourceTarget, work func(context.Context, SelectedResourceTarget) error) (err error) {
	defer func() {
		if recover() != nil {
			err = errOperationWorkerFailure
		}
	}()
	return work(ctx, target)
}

var errOperationWorkerFailure = errors.New("operation worker failed; inspect the captured destination before retrying")

func operationResultState(err error, notSubmitted, attempted bool) operationState {
	if err == nil {
		return operationAccepted
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if attempted {
			return operationUnknown
		}
		return operationCancelled
	}
	if notSubmitted {
		return operationNotSubmitted
	}
	var networkError net.Error
	var commandError *exec.ExitError
	var serverStatus apierrors.APIStatus
	serverError := errors.As(err, &serverStatus) && serverStatus.Status().Code >= 500
	uncertain := errors.Is(err, errExternalOperationOutcome) || errors.Is(err, errOperationWorkerFailure) ||
		errors.As(err, &commandError) || errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) ||
		apierrors.IsInternalError(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsUnexpectedServerError(err) || serverError
	if attempted && uncertain {
		return operationUnknown
	}
	return operationFailed
}

type operationRegistry struct {
	mu    sync.Mutex
	next  uint64
	tasks []*operationTask
}

func (r *operationRegistry) add(task *operationTask, action, contextName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := 0
	for _, existing := range r.tasks {
		if existing.receipt().Ended.IsZero() {
			active++
		}
	}
	if active >= maxActiveOperations {
		return fmt.Errorf("at most %d operations can run together; use :operations to review or cancel remaining work", maxActiveOperations)
	}
	if len(r.tasks) >= maxOperationHistory {
		for i, existing := range r.tasks {
			if !existing.receipt().Ended.IsZero() {
				r.tasks = slices.Delete(r.tasks, i, i+1)
				break
			}
		}
	}
	r.next++
	task.mu.Lock()
	task.id, task.action, task.context = r.next, action, contextName
	task.mu.Unlock()
	r.tasks = append(r.tasks, task)
	return nil
}

func (r *operationRegistry) list() []*operationTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.tasks)
}

func (r *operationRegistry) cancelAll() {
	for _, task := range r.list() {
		task.cancelRemaining()
	}
}

type operationReview struct {
	*Details
	index     int
	receiptID uint64
}

func (c *Command) operationsCommand() {
	v := &operationReview{Details: NewDetails(c.app, "Operations", "session receipts", contentTXT, false), index: -1}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
	}
}

func (v *operationReview) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.text.SetWordWrap(true)
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh receipts", func(*tcell.EventKey) *tcell.EventKey { v.render(); return nil }, true))
	v.actions.Add(ui.KeyC, ui.NewKeyAction("Cancel remaining", v.cancelCmd, true))
	v.actions.Add(ui.KeyN, ui.NewKeyAction("Next receipt", func(*tcell.EventKey) *tcell.EventKey { v.index++; v.render(); return nil }, true))
	v.actions.Add(ui.KeyP, ui.NewKeyAction("Previous receipt", func(*tcell.EventKey) *tcell.EventKey { v.index--; v.render(); return nil }, true))
	v.render()
	return nil
}

func (v *operationReview) Start() { v.Details.Start(); v.render() }

func (v *operationReview) cancelCmd(*tcell.EventKey) *tcell.EventKey {
	tasks := v.app.operations.list()
	for _, task := range tasks {
		if task.receipt().ID == v.receiptID {
			task.cancelRemaining()
			break
		}
	}
	v.render()
	return nil
}

func (v *operationReview) render() {
	tasks := v.app.operations.list()
	if len(tasks) == 0 {
		v.Update("No operations in this session.\n\nReceipts stay available here after navigation.\nEsc: back\n")
		return
	}
	if v.index < 0 {
		v.index = len(tasks) - 1
	}
	if v.index >= len(tasks) {
		v.index = 0
	}
	receipt := tasks[v.index].receipt()
	v.receiptID = receipt.ID
	v.Update(tview.Escape(operationReceiptText(receipt, v.index+1, len(tasks))))
}

//nolint:gocritic // Render a copied receipt without retaining the task's mutable state.
func operationReceiptText(receipt operationReceipt, index, count int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Receipt %d/%d · operation #%d · %s\nContext: %s\nStarted: %s\n",
		index, count, receipt.ID, receipt.Action, receipt.Context, receipt.Started.UTC().Format(time.RFC3339))
	if receipt.Ended.IsZero() {
		b.WriteString("Running. r refresh · c cancel remaining\n")
	} else {
		fmt.Fprintf(&b, "Finished: %s\n", receipt.Ended.UTC().Format(time.RFC3339))
	}
	if receipt.CancelRequested {
		b.WriteString("Cancellation requested. Accepted writes are retained.\n")
	}
	b.WriteString("n/p browse receipts · Esc back\n\n")
	for i := range receipt.Outcomes {
		outcome := &receipt.Outcomes[i]
		fmt.Fprintf(&b, "%s  %s %s\n", outcome.State, operationResourceName(&outcome.Target), outcome.Target.Path())
		if outcome.Target.UID != "" {
			fmt.Fprintf(&b, "UID: %s\n", outcome.Target.UID)
		} else {
			b.WriteString("Identity: provider/file identity; no Kubernetes UID assertion\n")
		}
		for _, step := range outcome.AcceptedSteps {
			fmt.Fprintf(&b, "  ACCEPTED: %s\n", step)
		}
		if outcome.Err != nil {
			fmt.Fprintf(&b, "  %s\n", operationOutcomeError(outcome))
		}
		if outcome.Output != "" {
			fmt.Fprintf(&b, "  Output:\n%s\n", outcome.Output)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Acceptance is distinct from controller completion.\nCOMPLETED records an external command exit, not " +
		"an observed Kubernetes outcome.\nCancellation stops remaining work; it does not roll back accepted " +
		"writes.\nOBSERVED records an independent named API observation of admitted fields; runtime/controller readiness remains separate.\n" +
		"UNKNOWN: inspect the captured destination before retrying. No automatic retry.\n")
	return b.String()
}
