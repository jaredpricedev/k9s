// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package probe

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Outcome is what became of one request.
type Outcome int

const (
	// NotSent means the request never left: the run was canceled, ran out
	// of time, or was stopped after a refusal.
	NotSent Outcome = iota
	// Answered means the Pod answered with an HTTP status, whatever it was.
	Answered
	// TimedOut means no answer arrived within the deadline.
	TimedOut
	// Denied means the API server refused the pods/proxy request (401/403).
	Denied
	// Gone means the API server no longer knows the Pod.
	Gone
	// Unreachable means the API server answered that it could not reach the
	// Pod: the Pod itself said nothing.
	Unreachable
	// Failed means no HTTP response came back from the API server at all.
	Failed
	// InFlight means the request left and its answer is still awaited. It
	// is only seen while a run is in progress.
	InFlight
	// Abandoned means the request left and the run ended before its answer
	// was waited for. The application may still have handled it.
	Abandoned
)

// Attempt is one request and its outcome.
type Attempt struct {
	Outcome Outcome
	// Code is the HTTP status, from the Pod when Answered and from the API
	// server otherwise; zero when there was no response.
	Code int
	// Took is the time to the first byte of the answer. For TimedOut it is
	// the deadline: the real time is at least that.
	Took time.Duration
	// Detail is a short fixed phrase, never response text.
	Detail string
}

// Sent reports whether the request left. A request that is in flight or
// was abandoned did leave: the application can see it.
func (a Attempt) Sent() bool { return a.Outcome != NotSent }

// Finished reports whether the request left and has an outcome: an answer,
// a timeout or a failure. A request in flight or abandoned has none.
func (a Attempt) Finished() bool {
	return a.Outcome != NotSent && a.Outcome != InFlight && a.Outcome != Abandoned
}

// Response is what a Transport saw for one request.
type Response struct {
	Outcome Outcome
	Code    int
	Took    time.Duration
	Detail  string
}

// Transport sends one GET. An implementation sends nothing but the request
// line the Request describes: no body and no header of its own choosing.
type Transport interface {
	Get(ctx context.Context, request *Request) Response
}

// Options tune a run. The zero value uses the package defaults.
type Options struct {
	Attempts    int
	Deadline    time.Duration
	Pause       time.Duration
	Concurrency int
	// Total bounds the whole run.
	Total time.Duration
}

func (o Options) normalized() Options {
	o.Attempts = ClampAttempts(o.Attempts)
	if o.Deadline <= 0 {
		o.Deadline = DefaultDeadline
	}
	if o.Pause < 0 {
		o.Pause = 0
	}
	if o.Concurrency < 1 {
		o.Concurrency = Concurrency
	}
	if o.Total <= 0 {
		o.Total = RunDeadline
	}
	return o
}

// PodRun is the attempts made against one target of the plan.
type PodRun struct {
	// Attempts has one entry per planned request; unsent ones are NotSent.
	// It is nil for a target that is not contacted.
	Attempts []Attempt
}

// Run is one execution of a plan.
type Run struct {
	Kind              Kind
	Attempts          int
	Deadline          time.Duration
	StartedAt, EndsAt time.Time
	// Pods is parallel to Plan.Targets.
	Pods []PodRun
	// Stopped says why the run ended early: StoppedCanceled,
	// StoppedDeadline or StoppedDenied. Empty when every planned request
	// was sent.
	Stopped string
}

// Reasons a run ended before every planned request was sent.
const (
	StoppedCanceled = "canceled"
	StoppedDeadline = "run deadline"
	StoppedDenied   = "denied"
)

// NewRun is an empty run for the plan, with every request unsent.
func NewRun(plan *Plan, o Options) *Run {
	o = o.normalized()
	run := &Run{Kind: plan.Kind, Attempts: o.Attempts, Deadline: o.Deadline, Pods: make([]PodRun, len(plan.Targets))}
	for i := range plan.Targets {
		if plan.Targets[i].Request != nil {
			run.Pods[i].Attempts = make([]Attempt, o.Attempts)
		}
	}
	return run
}

// Planned is the number of requests the run set out to send.
func (r *Run) Planned() int {
	count := 0
	for i := range r.Pods {
		count += len(r.Pods[i].Attempts)
	}
	return count
}

// Sent is the number of requests that left.
func (r *Run) Sent() int {
	count := 0
	for i := range r.Pods {
		for _, attempt := range r.Pods[i].Attempts {
			if attempt.Sent() {
				count++
			}
		}
	}
	return count
}

// Finished is the number of requests that left and have an outcome.
func (r *Run) Finished() int {
	count := 0
	for i := range r.Pods {
		for _, attempt := range r.Pods[i].Attempts {
			if attempt.Finished() {
				count++
			}
		}
	}
	return count
}

// Abandon marks every request still in flight as abandoned and returns how
// many there were. The caller uses it when it stops waiting for a run: those
// requests left, so they are not counted as unsent.
func (r *Run) Abandon() int {
	count := 0
	for i := range r.Pods {
		for n := range r.Pods[i].Attempts {
			if r.Pods[i].Attempts[n].Outcome == InFlight {
				r.Pods[i].Attempts[n] = Attempt{Outcome: Abandoned, Detail: abandonedDetail}
				count++
			}
		}
	}
	return count
}

// Abandoned is the number of requests that left and were not waited for.
func (r *Run) Abandoned() int {
	count := 0
	for i := range r.Pods {
		for _, attempt := range r.Pods[i].Attempts {
			if attempt.Outcome == Abandoned {
				count++
			}
		}
	}
	return count
}

const abandonedDetail = "canceled before an answer"

// Progress reports one attempt: the target index, the attempt index and its
// state. It is called twice per request from the goroutine that sends it:
// with Outcome InFlight just before the request leaves, and with the outcome
// when there is one. A request abandoned by a cancel is not reported again.
type Progress func(pod, attempt int, result Attempt)

// Execute sends the plan's requests: up to Concurrency Pods at a time, the
// requests to one Pod one after another with a pause between them, each
// under its own deadline. Canceling ctx stops it; what was not sent stays
// NotSent, and a request that was in flight is Abandoned: it left, so it is
// not counted as unsent. After the API server denies pods/proxy, nothing more
// is sent to any Pod. It returns when every goroutine has stopped.
func Execute(ctx context.Context, transport Transport, plan *Plan, o Options, progress Progress) *Run {
	o = o.normalized()
	run := NewRun(plan, o)
	run.StartedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(ctx, o.Total)
	defer cancel()
	var (
		wait   sync.WaitGroup
		denied atomic.Bool
		slots  = make(chan struct{}, o.Concurrency)
	)
	for i := range plan.Targets {
		request := plan.Targets[i].Request
		if request == nil {
			continue
		}
		wait.Add(1)
		go func(index int, request Request) {
			defer wait.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			attempts := run.Pods[index].Attempts
			for n := range attempts {
				if ctx.Err() != nil || denied.Load() {
					return
				}
				if n > 0 && o.Pause > 0 {
					select {
					case <-time.After(o.Pause):
					case <-ctx.Done():
						return
					}
				}
				attempts[n] = Attempt{Outcome: InFlight}
				if progress != nil {
					progress(index, n, attempts[n])
				}
				result, waited := send(ctx, transport, &request, o.Deadline)
				if !waited {
					attempts[n] = Attempt{Outcome: Abandoned, Detail: abandonedDetail}
					return
				}
				attempts[n] = result
				if progress != nil {
					progress(index, n, result)
				}
				switch result.Outcome {
				case Denied:
					denied.Store(true)
					return
				case Gone:
					return
				}
			}
		}(i, *request)
	}
	wait.Wait()
	run.EndsAt = time.Now().UTC()
	switch {
	case denied.Load():
		run.Stopped = StoppedDenied
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		run.Stopped = StoppedDeadline
	case ctx.Err() != nil:
		run.Stopped = StoppedCanceled
	}
	return run
}

// send makes one request under its deadline. It returns false when the run
// was canceled while the request was in flight: that request left, but it
// has no outcome, because nobody waited for it.
func send(ctx context.Context, transport Transport, request *Request, deadline time.Duration) (Attempt, bool) {
	requestCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	response := transport.Get(requestCtx, request)
	if ctx.Err() != nil {
		return Attempt{}, false
	}
	attempt := Attempt{Outcome: response.Outcome, Code: response.Code, Took: response.Took, Detail: response.Detail}
	if errors.Is(requestCtx.Err(), context.DeadlineExceeded) && response.Outcome != Answered {
		attempt = Attempt{Outcome: TimedOut, Took: deadline, Detail: "no answer within the deadline"}
	}
	if !attempt.Finished() {
		attempt.Outcome, attempt.Detail = Failed, "no response"
	}
	return attempt, true
}
