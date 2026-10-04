// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package watch

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const capturedForwarderID = "team/pod|api|8080:80"

type capturedForwarder struct {
	Forwarder
	stopped atomic.Bool
}

func (*capturedForwarder) ID() string { return capturedForwarderID }
func (f *capturedForwarder) Stop()    { f.stopped.Store(true) }

func TestSessionOldForwarderCompletionCannotStopReplacement(t *testing.T) {
	oldFactory, freshFactory := NewFactory(nil), NewFactory(nil)
	old, fresh := new(capturedForwarder), new(capturedForwarder)
	oldFactory.AddForwarder(old)
	freshFactory.AddForwarder(fresh)
	oldFactory.Terminate()
	oldFactory.DeleteOwnedForwarder(old)
	require.True(t, old.stopped.Load())
	require.False(t, fresh.stopped.Load())
	registered, ok := freshFactory.ForwarderFor(capturedForwarderID)
	require.True(t, ok)
	require.Same(t, fresh, registered)

	// Even within one factory, a queued exit has ownership of its original
	// stream, not every future stream using this same binding.
	freshFactory.AddForwarder(old)
	freshFactory.AddForwarder(fresh)
	freshFactory.DeleteOwnedForwarder(old)
	require.False(t, fresh.stopped.Load())
	freshFactory.DeleteOwnedForwarder(fresh)
	require.True(t, fresh.stopped.Load())
}

func TestSessionForwarderSnapshotsCannotMutateFactoryOwnership(t *testing.T) {
	factory := NewFactory(nil)
	forwarder := new(capturedForwarder)
	factory.AddForwarder(forwarder)
	snapshot := factory.Forwarders()
	delete(snapshot, capturedForwarderID)
	registered, ok := factory.ForwarderFor(capturedForwarderID)
	require.True(t, ok)
	require.Same(t, forwarder, registered)
}
