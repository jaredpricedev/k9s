// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"sync"
	"time"
)

const resourceFilterDelay = 20 * time.Millisecond

// Resource suggestions complete on every rune. Keep the prompt immediate but
// compute the retained resource projection once after a quiet editing interval.
// The revision also invalidates work already queued before a new edit or Stop.
type resourceFilterRequests struct {
	mx       sync.Mutex
	timer    *time.Timer
	revision uint64
	active   bool
}

func (r *resourceFilterRequests) start() {
	r.mx.Lock()
	defer r.mx.Unlock()
	r.active = true
}

func (r *resourceFilterRequests) cancel() {
	r.mx.Lock()
	defer r.mx.Unlock()
	r.cancelLocked()
}

func (r *resourceFilterRequests) cancelLocked() {
	r.revision++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *resourceFilterRequests) stop() {
	r.mx.Lock()
	defer r.mx.Unlock()
	r.active = false
	r.cancelLocked()
}

func (r *resourceFilterRequests) submit(text string, delay time.Duration, enqueue func(func()), apply func(string)) {
	r.mx.Lock()
	defer r.mx.Unlock()
	if !r.active {
		return
	}
	r.cancelLocked()
	revision := r.revision
	r.timer = time.AfterFunc(delay, func() {
		enqueue(func() {
			r.mx.Lock()
			defer r.mx.Unlock()
			if r.active && r.revision == revision {
				apply(text)
			}
		})
	})
}
