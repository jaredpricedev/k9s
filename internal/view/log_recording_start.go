package view

import (
	"context"
	"sync"

	"github.com/derailed/k9s/internal/logstream"
)

type recordingStartResult struct {
	path string
	err  error
}
type logRecordingStart struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	result  chan recordingStartResult
	mu      sync.Mutex
	claimed bool
	raw     bool
	opts    logstream.RecordingOptions
	lease   *logSessionLease
}

func newRecordingStart(root string, maxSessions, hours int, raw bool, opts logstream.RecordingOptions) *logRecordingStart {
	ctx, cancel := context.WithCancel(context.Background())
	op := &logRecordingStart{ctx: ctx, cancel: cancel, done: make(chan struct{}), result: make(chan recordingStartResult, 1), raw: raw, opts: opts}
	go op.run(root, maxSessions, hours)
	return op
}
func (op *logRecordingStart) run(root string, maxSessions, hours int) {
	defer close(op.done)
	path, lease, err := prepareReservedLogSession(op.ctx, root, maxSessions, hours)
	op.mu.Lock()
	op.lease = lease
	op.mu.Unlock()
	if path != "" {
		defer func() {
			op.mu.Lock()
			claimed := op.claimed
			op.mu.Unlock()
			if !claimed {
				_ = lease.Close()
				_ = removeLogSession(context.Background(), path)
			}
		}()
	}
	select {
	case op.result <- recordingStartResult{path, err}:
	case <-op.ctx.Done():
		return
	}
	if err == nil {
		<-op.ctx.Done()
	}
}

// Claim transfers the newly prepared directory to exactly one writer. Cancel
// wakes the preparation goroutine; its cleanup sees ownership was transferred.
func (op *logRecordingStart) claim() (*logSessionLease, bool) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.ctx.Err() != nil || op.claimed {
		return nil, false
	}
	op.claimed = true
	lease := op.lease
	op.lease = nil
	op.cancel()
	return lease, true
}

func (w *logWorkbench) consumeRecordingStart() {
	op := w.recordStart
	if op == nil {
		return
	}
	if w.stopped.Load() {
		op.cancel()
	}
	if op.ctx.Err() != nil {
		select {
		case <-op.done:
			w.recordStart = nil
			if !w.stopped.Load() {
				w.notice = "Recording start canceled"
			}
		default:
		}
		return
	}
	select {
	case result := <-op.result:
		if result.err != nil {
			op.cancel()
			w.recordStart = nil
			w.notice = "Recording ERROR: " + result.err.Error()
			return
		}
		lease, claimed := op.claim()
		if !claimed {
			return
		}
		wr := newLogWriter(result.path, op.raw, op.opts, lease)
		if w.owner != nil && !w.owner.app.registerLogWriter(wr) {
			w.recordStart = nil
			return
		}
		w.captureMu.Lock()
		w.writer = wr
		w.captureMu.Unlock()
		w.recordStart = nil
		mode := "safe"
		if op.raw {
			mode = "EXPLICIT RAW"
		}
		w.notice = "Recording " + mode + " to " + result.path
	default:
	}
}
