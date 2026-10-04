// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal/model"
	"github.com/stretchr/testify/require"
)

type bufferCallback struct {
	completed func()
}

func (w bufferCallback) BufferCompleted(_, _ string)       { w.completed() }
func (bufferCallback) BufferChanged(_, _ string)           {}
func (bufferCallback) BufferActive(bool, model.BufferKind) {}

func TestBufferCompletionCanRemoveItsWatcher(t *testing.T) {
	buffer := model.NewCmdBuff('/', model.FilterBuffer)
	var calls atomic.Int32
	var watcher *bufferCallback
	watcher = &bufferCallback{completed: func() {
		calls.Add(1)
		buffer.RemoveListener(watcher)
	}}
	buffer.AddListener(watcher)
	buffer.SetText("payments", "", true)
	buffer.SetText("orders", "", true)
	require.EqualValues(t, 1, calls.Load())
}

func TestBufferCompletionAndViewNavigationUseListenerSnapshots(t *testing.T) {
	buffer := model.NewCmdBuff('/', model.FilterBuffer)
	var calls atomic.Int32
	watcher := &bufferCallback{completed: func() { calls.Add(1) }}
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 200 {
			buffer.AddListener(watcher)
			buffer.RemoveListener(watcher)
		}
	})
	workers.Go(func() {
		for range 200 {
			buffer.SetText("payments", "", true)
		}
	})
	workers.Wait()
	buffer.AddListener(watcher)
	buffer.SetText("final", "", true)
	require.Positive(t, calls.Load())
}
