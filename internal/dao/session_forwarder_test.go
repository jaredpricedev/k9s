// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dao

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionForwarderConcurrentStopIsIdempotent(t *testing.T) {
	forwarder := NewPortForwarder(nil)
	forwarder.SetActive(true)
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			forwarder.Stop()
			_ = forwarder.Active()
		})
	}
	workers.Wait()
	require.False(t, forwarder.Active())
	select {
	case <-forwarder.stopChan:
	default:
		t.Fatal("owned forwarder's stop channel remained open")
	}
}
