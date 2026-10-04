// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package session tracks only the local processes and streams explicitly
// launched by this application. It never discovers or stops outside processes.
package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

const (
	maxActive  = 64
	maxHistory = 128
	maxEvents  = 32
)

// Kind identifies an explicitly launched workflow.
type Kind string

const (
	PortForward Kind = "port-forward"
	Shell       Kind = "shell"
	Attach      Kind = "attach"
	Plugin      Kind = "plugin"
	Diagnostic  Kind = "diagnostic"
)

// State describes the known local lifecycle, independently of remote effects.
type State string

const (
	Starting  State = "starting"
	Running   State = "running"
	Stopping  State = "stopping"
	Stopped   State = "stopped"
	Completed State = "completed"
	Failed    State = "failed"
	Unknown   State = "unknown"
)

// Destination is captured before launch and remains unchanged on navigation or
// reconnect. No credentials, command arguments or environment values belong here.
type Destination struct {
	Context   string
	Server    string
	GVR       string
	Namespace string
	Name      string
	UID       string
	Container string
	Revision  uint64
}

// Spec contains displayable identity and the source operation receipt, if any.
type Spec struct {
	Kind         Kind
	Label        string
	Destination  Destination
	Origin       Destination
	Binding      string
	OperationID  string
	IdentityNote string
}

// Event is an authored lifecycle message, never captured command output.
type Event struct {
	At      time.Time
	Message string
}

// Record is a detached snapshot safe for callers to retain or modify.
type Record struct {
	ID        string
	Spec      Spec
	State     State
	StartedAt time.Time
	EndedAt   time.Time
	Active    bool
	Events    []Event
}

// Registry retains bounded history while keeping every active owned session.
// Its zero value is ready for use.
type Registry struct {
	mx       sync.Mutex
	sequence uint64
	closed   bool
	handles  []*Handle
}

// Handle owns the cancellation and completion of exactly one local session.
type Handle struct {
	registry   *Registry
	record     Record
	cancel     func()
	cancelOnce sync.Once
	done       chan struct{}
	finished   bool
}

// Add registers ownership before the launch worker starts. Rejected launches
// retain no cancellation callback and cannot affect existing sessions.
func (r *Registry) Add(spec *Spec, cancel func()) (*Handle, error) {
	r.mx.Lock()
	defer r.mx.Unlock()
	if r.closed {
		return nil, errors.New("local session registry is shutting down")
	}
	if spec == nil {
		return nil, errors.New("local session specification is unavailable")
	}
	active := 0
	for _, handle := range r.handles {
		if !handle.finished {
			active++
		}
	}
	if active >= maxActive {
		return nil, errors.New("local session limit reached; stop an owned session before launching another")
	}
	r.sequence++
	started := time.Now()
	handle := &Handle{registry: r, cancel: cancel, done: make(chan struct{}), record: Record{
		ID: fmt.Sprintf("session-%d", r.sequence), Spec: *spec, State: Starting,
		StartedAt: started, Active: true, Events: []Event{{At: started, Message: "Launch registered; local ownership captured."}},
	}}
	r.handles = append(r.handles, handle)
	r.trimHistoryLocked()
	return handle, nil
}

// Records returns newest-first snapshots, including retained ended sessions.
func (r *Registry) Records() []Record {
	r.mx.Lock()
	defer r.mx.Unlock()
	records := make([]Record, 0, len(r.handles))
	for i := len(r.handles) - 1; i >= 0; i-- {
		records = append(records, copyRecord(&r.handles[i].record))
	}
	return records
}

// Find returns a private snapshot of an owned session.
func (r *Registry) Find(id string) (Record, bool) {
	r.mx.Lock()
	defer r.mx.Unlock()
	for _, handle := range r.handles {
		if handle.record.ID == id {
			return copyRecord(&handle.record), true
		}
	}
	return Record{}, false
}

// Cancel requests cleanup of one owned resource. Confirmation comes from its
// worker's Finish, rather than from the cancellation request itself.
func (r *Registry) Cancel(id string) bool {
	r.mx.Lock()
	var found *Handle
	for _, handle := range r.handles {
		if handle.record.ID == id {
			found = handle
			break
		}
	}
	r.mx.Unlock()
	if found == nil {
		return false
	}
	found.Cancel()
	return true
}

// Shutdown cancels only registered resources and waits until the supplied
// deadline. Unconfirmed cleanup remains explicit and can finish later.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.mx.Lock()
	r.closed = true
	handles := slices.Clone(r.handles)
	r.mx.Unlock()
	for _, handle := range handles {
		// A faulty resource's cleanup callback cannot consume the entire
		// shutdown deadline or prevent cancellation of other owned resources.
		go handle.Cancel()
	}
	for _, handle := range handles {
		select {
		case <-handle.done:
		case <-ctx.Done():
			for _, pending := range handles {
				pending.markCleanupUnknown()
			}
			return ctx.Err()
		}
	}
	return nil
}

// ID is immutable and safe to read while the worker is running.
func (h *Handle) ID() string { return h.record.ID }

// Done closes only once actual worker completion has been reported.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Running records readiness, including an assigned local binding. A late
// readiness callback cannot overwrite a stop request or completed session.
func (h *Handle) Running(binding string) {
	h.registry.mx.Lock()
	defer h.registry.mx.Unlock()
	if h.finished || h.record.State != Starting {
		return
	}
	if binding != "" {
		h.record.Spec.Binding = binding
	}
	h.record.State = Running
	h.eventLocked("Local session is ready.")
}

// Event retains a bounded authored lifecycle note. Callers must not provide raw
// process output, arguments, credentials or environment values.
func (h *Handle) Event(message string) {
	h.registry.mx.Lock()
	defer h.registry.mx.Unlock()
	if !h.finished {
		h.eventLocked(message)
	}
}

// Cancel invokes this session's owned cleanup at most once, outside the lock.
func (h *Handle) Cancel() {
	h.cancelOnce.Do(func() {
		h.registry.mx.Lock()
		if h.finished {
			h.registry.mx.Unlock()
			return
		}
		if h.record.State != Unknown {
			h.record.State = Stopping
		}
		h.eventLocked("Stop requested; awaiting confirmation of local cleanup.")
		cancel := h.cancel
		h.registry.mx.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

// Finish reports a terminal local outcome. It does not imply that accepted
// cluster changes or remote commands were rolled back.
func (h *Handle) Finish(state State, message string) {
	h.registry.mx.Lock()
	defer h.registry.mx.Unlock()
	if h.finished {
		return
	}
	if state != Stopped && state != Completed && state != Failed && state != Unknown {
		state = Unknown
	}
	h.finished = true
	h.record.State, h.record.Active, h.record.EndedAt = state, false, time.Now()
	h.eventLocked(message)
	close(h.done)
	h.registry.trimHistoryLocked()
}

func (h *Handle) markCleanupUnknown() {
	h.registry.mx.Lock()
	defer h.registry.mx.Unlock()
	if h.finished || h.record.State == Unknown {
		return
	}
	h.record.State = Unknown
	h.eventLocked("Cleanup deadline reached; local termination is not yet confirmed.")
}

func (h *Handle) eventLocked(message string) {
	if message == "" {
		return
	}
	h.record.Events = append(h.record.Events, Event{At: time.Now(), Message: message})
	if len(h.record.Events) > maxEvents {
		h.record.Events = slices.Clone(h.record.Events[len(h.record.Events)-maxEvents:])
	}
}

func (r *Registry) trimHistoryLocked() {
	ended := 0
	for _, handle := range r.handles {
		if handle.finished {
			ended++
		}
	}
	if ended <= maxHistory {
		return
	}
	kept := make([]*Handle, 0, len(r.handles)-(ended-maxHistory))
	for _, handle := range r.handles {
		if handle.finished && ended > maxHistory {
			ended--
			continue
		}
		kept = append(kept, handle)
	}
	r.handles = kept
}

func copyRecord(record *Record) Record {
	snapshot := *record
	snapshot.Events = slices.Clone(record.Events)
	return snapshot
}
