// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const registryCallerMutation = "caller mutation"

func TestRegistryCancelOwnsExactlyOneResource(t *testing.T) {
	var registry Registry
	var firstStops, secondStops atomic.Int32
	first, err := registry.Add(&Spec{Kind: PortForward, Destination: Destination{Context: "original", UID: "captured-uid"}}, func() {
		firstStops.Add(1)
		// Cancellation callbacks may inspect the registry without a deadlock.
		_ = registry.Records()
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Add(&Spec{Kind: Shell}, func() { secondStops.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() { registry.Cancel(first.ID()) })
	}
	workers.Wait()
	if firstStops.Load() != 1 || secondStops.Load() != 0 {
		t.Fatalf("wrong cancellation ownership: first=%d second=%d", firstStops.Load(), secondStops.Load())
	}
	first.Running("127.0.0.1:9090")
	record, ok := registry.Find(first.ID())
	if !ok || record.State != Stopping || !record.Active {
		t.Fatalf("stop request was overwritten: %+v", record)
	}
	if record.Spec.Destination.Context != "original" || record.Spec.Destination.UID != "captured-uid" {
		t.Fatal("captured identity changed")
	}
	first.Finish(Stopped, "Local stream ended.")
	select {
	case <-first.Done():
	default:
		t.Fatal("confirmed completion did not close Done")
	}
	second.Running("")
	other, _ := registry.Find(second.ID())
	if other.State != Running {
		t.Fatal("unrelated owned session changed")
	}
}

func TestRegistrySnapshotsAndHistoryStayBounded(t *testing.T) {
	var registry Registry
	active, err := registry.Add(&Spec{Kind: Shell}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	active.Running("")
	for i := range maxHistory + 20 {
		handle, addErr := registry.Add(&Spec{Kind: Plugin, Label: fmt.Sprintf("plugin-%d", i)}, nil)
		if addErr != nil {
			t.Fatal(addErr)
		}
		for range maxEvents + 10 {
			handle.Event("Authored lifecycle event.")
		}
		handle.Finish(Completed, "Child exited.")
	}
	records := registry.Records()
	if len(records) != maxHistory+1 {
		t.Fatalf("history count=%d", len(records))
	}
	if len(records[0].Events) != maxEvents {
		t.Fatalf("event count=%d", len(records[0].Events))
	}
	records[0].Events[0].Message = registryCallerMutation
	records[0].Spec.Destination.Context = registryCallerMutation
	fresh, _ := registry.Find(records[0].ID)
	if fresh.Events[0].Message == registryCallerMutation || fresh.Spec.Destination.Context == registryCallerMutation {
		t.Fatal("caller changed retained state")
	}
	retained, ok := registry.Find(active.ID())
	if !ok || !retained.Active || retained.State != Running {
		t.Fatal("history trimming removed an active session")
	}
}

func TestRegistryShutdownRetainsUnconfirmedCleanupAndAllowsLateCompletion(t *testing.T) {
	var registry Registry
	var stopped atomic.Int32
	handle, err := registry.Add(&Spec{Kind: Diagnostic}, func() { stopped.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err = registry.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown result=%v", err)
	}
	if stopped.Load() != 1 {
		t.Fatalf("cleanup calls=%d", stopped.Load())
	}
	record, _ := registry.Find(handle.ID())
	if record.State != Unknown || !record.Active || !record.EndedAt.IsZero() {
		t.Fatalf("unconfirmed resource presented as ended: %+v", record)
	}
	if _, err = registry.Add(&Spec{Kind: Shell}, nil); err == nil {
		t.Fatal("launch accepted after shutdown")
	}
	handle.Finish(Stopped, "Owned child termination confirmed.")
	if err = registry.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, _ = registry.Find(handle.ID())
	if record.State != Stopped || record.Active || record.EndedAt.IsZero() {
		t.Fatal("late confirmed cleanup was lost")
	}
	if len(record.Events) < 4 {
		t.Fatal("cleanup uncertainty was not retained in lifecycle history")
	}
}

func TestRegistryActiveLimitDoesNotCancelExistingSessions(t *testing.T) {
	var registry Registry
	var stopped atomic.Int32
	for range maxActive {
		if _, err := registry.Add(&Spec{Kind: PortForward}, func() { stopped.Add(1) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.Add(&Spec{Kind: Shell}, nil); err == nil {
		t.Fatal("unbounded active sessions accepted")
	}
	if stopped.Load() != 0 || len(registry.Records()) != maxActive {
		t.Fatal("rejected launch changed existing ownership")
	}
}

func TestRegistryShutdownDeadlineAlsoBoundsFaultyCancellationCallback(t *testing.T) {
	var registry Registry
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	blocked, err := registry.Add(&Spec{Kind: Plugin}, func() { close(started); <-release })
	if err != nil {
		t.Fatal(err)
	}
	var healthy *Handle
	healthy, err = registry.Add(&Spec{Kind: PortForward}, func() { healthy.Finish(Stopped, "Owned listener cleanup confirmed.") })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err = registry.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-started:
	default:
		t.Fatal("faulty cleanup callback was not attempted")
	}
	select {
	case <-healthy.Done():
	default:
		t.Fatal("faulty cleanup callback prevented unrelated owned cleanup")
	}
	record, _ := registry.Find(blocked.ID())
	if record.State != Unknown || !record.Active {
		t.Fatal("incomplete cleanup was reported as confirmed")
	}
	blocked.Finish(Stopped, "Late worker completion confirmed.")
}
