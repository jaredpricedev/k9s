// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/stretchr/testify/require"
)

func TestRawSubscriptionOutsideLockAndUnsubscribe(t *testing.T) {
	m := NewLog(client.PodGVR, &dao.LogOptions{Lines: 3, SinceTime: "initial", WorkloadKind: "Deployment", Labels: map[string]string{"team": "ops"}}, time.Millisecond)
	var calls int
	stop := m.SubscribeEntries(func(entries []logstream.Entry) {
		calls++
		require.Equal(t, "[red]hello", entries[0].Message)
		m.Clear()
	})
	item := (&dao.LogOptions{Path: "ns/p", Container: "main", Labels: map[string]string{"app": "demo"}}).ToLogItem([]byte("2026-09-06T10:00:00Z [red]hello"))
	done := make(chan struct{})
	go func() { m.Append(item); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback called under model lock")
	}
	stop()
	m.Append(item)
	require.Equal(t, 1, calls)
	snapshot := m.LogOptionsSnapshot()
	require.Equal(t, "initial", snapshot.SinceTime)
	require.Equal(t, "demo", snapshot.Labels["app"])
	snapshot.Labels["app"] = "mutated"
	require.Equal(t, "demo", m.LogOptionsSnapshot().Labels["app"])
}

type reentrantLogListener struct {
	m        *Log
	notified chan struct{}
	calls    atomic.Int32
}

func (l *reentrantLogListener) LogChanged(_ [][]byte) {
	l.m.SinceSeconds()
	l.calls.Add(1)
	select {
	case l.notified <- struct{}{}:
	default:
	}
}
func (*reentrantLogListener) LogCleared()     {}
func (*reentrantLogListener) LogFailed(error) {}
func (*reentrantLogListener) LogStop()        {}
func (*reentrantLogListener) LogResume()      {}
func (*reentrantLogListener) LogCanceled()    {}
func TestBusyTrafficFlushAndReentrantListener(t *testing.T) {
	m := NewLog(client.PodGVR, &dao.LogOptions{Lines: 100}, 5*time.Millisecond)
	listener := &reentrantLogListener{m: m, notified: make(chan struct{}, 10)}
	m.AddListener(listener)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(dao.LogChan, 100)
	done := make(chan struct{})
	go func() { m.updateLogs(ctx, input); close(done) }()
	go func() {
		for {
			select {
			case input <- dao.NewLogItemFromString("line"):
			case <-ctx.Done():
				return
			}
		}
	}()
	select {
	case <-listener.notified:
	case <-time.After(time.Second):
		t.Fatal("busy stream starved flush or listener deadlocked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("model did not stop")
	}
}

func TestConfigureSourceAndSinceTimeValidation(t *testing.T) {
	m := NewLog(client.PodGVR, &dao.LogOptions{SinceTime: "old", SinceSeconds: 60, Previous: true}, time.Second)
	m.ConfigureSource("dev", "cluster", true)
	require.Equal(t, "dev", m.LogOptionsSnapshot().Context)
	require.Equal(t, "cluster", m.LogOptionsSnapshot().Cluster)
	require.True(t, m.LogOptionsSnapshot().Events)
	require.Error(t, m.SetSinceTime(context.Background(), "bad"))
	require.Equal(t, "old", m.LogOptionsSnapshot().SinceTime)
}

func TestProfileMetadataUsesFirstPodWithoutUnboundedUnion(t *testing.T) {
	m := NewLog(client.PodGVR, &dao.LogOptions{Lines: 1, Labels: map[string]string{"team": "ops"}}, time.Second)
	m.Append((&dao.LogOptions{Labels: map[string]string{"team": "ops"}}).ToErrLogItem(fmt.Errorf("watch temporarily unavailable")))
	m.Append((&dao.LogOptions{Path: "ns/one", Labels: map[string]string{"app": "first"}}).ToLogItem([]byte("one")))
	m.Append((&dao.LogOptions{Path: "ns/two", Labels: map[string]string{"app": "second", "unrelated": "value"}}).ToLogItem([]byte("two")))
	snapshot := m.LogOptionsSnapshot()
	require.Equal(t, "first", snapshot.Labels["app"])
	require.NotContains(t, snapshot.Labels, "unrelated")
}
