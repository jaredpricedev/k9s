// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
package dao

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// Deliver one physical record per Read so the test can observe the first read
// beyond the queue plus one pending record, without relying on network timing.
type recordProbe struct {
	records           []string
	index             int
	pending, advanced chan struct{}
	once              sync.Once
}

func (r *recordProbe) Read(p []byte) (int, error) {
	if r.index == 3 {
		r.once.Do(func() { close(r.advanced) })
	}
	if r.index >= len(r.records) {
		return 0, io.EOF
	}
	line := r.records[r.index]
	r.index++
	if r.index == 3 {
		close(r.pending)
	}
	return copy(p, line), nil
}
func (*recordProbe) Close() error { return nil }

func TestReadLogsBackpressurePreserves4000RecordBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	records := make([]string, 4000)
	for i := range records {
		records[i] = fmt.Sprintf("2026-09-06T10:00:00.%09dZ line-%d\n", i+1, i)
	}
	probe := &recordProbe{records: records, pending: make(chan struct{}), advanced: make(chan struct{})}
	out := make(chan *LogItem, 2)
	done := make(chan streamResult, 1)
	go func() { done <- readLogs(ctx, probe, out, &LogOptions{}); close(out) }()
	<-probe.pending
	select {
	case <-probe.advanced:
		t.Fatal("reader consumed beyond the full queue and its one pending record")
	case <-time.After(30 * time.Millisecond):
	}
	index := 0
	for item := range out {
		require.Nil(t, item.Marker)
		require.Equal(t, fmt.Sprintf("line-%d", index), item.Entry().Raw)
		index++
	}
	require.Equal(t, 4000, index)
	require.Equal(t, streamEOF, <-done)
	require.Equal(t, 2, cap(out))
}

func TestSharedStartupBackpressurePreservesSixHundredRecords(t *testing.T) {
	objects := make([]runtime.Object, 6)
	for i := range objects {
		objects[i] = followTestPod(fmt.Sprintf("pod-%d", i), fmt.Sprintf("uid-%d", i), 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := fake.NewSimpleClientset(objects...)
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Head: true, LogBufferSize: 2}, func(_ context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		var input strings.Builder
		for i := range 100 {
			fmt.Fprintf(&input, "2026-09-06T10:00:00.%09dZ line-%d\n", i+1, i)
		}
		return io.NopCloser(strings.NewReader(input.String())), nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, cap(out))
	seen := make(map[string]bool)
	for item := range out {
		if item.Marker != nil {
			require.NotEqual(t, "loss", item.Marker.Kind)
			continue
		}
		key := item.Source.UID + "/" + item.Entry().Raw
		require.False(t, seen[key], "duplicate %s", key)
		seen[key] = true
	}
	require.Len(t, seen, 600)
	for source := range 6 {
		for line := range 100 {
			require.True(t, seen[fmt.Sprintf("uid-%d/line-%d", source, line)])
		}
	}
}

type signaledReader struct {
	io.Reader
	read chan struct{}
	once sync.Once
}

func (r *signaledReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.once.Do(func() { close(r.read) })
	}
	return n, err
}

func TestBlockedDeliveryCancellationPreservesCursorAndReplay(t *testing.T) {
	for _, ending := range []string{"\n", ""} {
		t.Run(fmt.Sprintf("newline=%t", ending != ""), func(t *testing.T) {
			cursor := &logCursor{}
			opts := &LogOptions{Path: "ns/p", Container: "main"}
			first := "2026-09-06T10:00:00.100Z first\n"
			second := "2026-09-06T10:00:00.200Z second"
			seed := make(chan *LogItem, 1)
			require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(first)), seed, opts, cursor))
			<-seed
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan *LogItem) // Deliberately no receiver until cancellation.
			done := make(chan streamResult, 1)
			read := make(chan struct{})
			reader := io.NopCloser(&signaledReader{Reader: strings.NewReader(second + ending), read: read})
			go func() { done <- readLogStream(ctx, reader, out, opts, cursor) }()
			<-read
			select {
			case <-done:
				t.Fatal("record did not wait for delivery")
			case <-time.After(30 * time.Millisecond):
			}
			cancel()
			select {
			case result := <-done:
				require.Equal(t, streamCanceled, result)
			case <-time.After(time.Second):
				t.Fatal("cancellation did not unblock delivery")
			}
			require.Equal(t, "2026-09-06T10:00:00.1Z", cursor.time.Format(time.RFC3339Nano))
			cursor.reconnect()
			resumed := make(chan *LogItem, 3)
			require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(first+second+"\n")), resumed, opts, cursor))
			close(resumed)
			var raw []string
			for item := range resumed {
				raw = append(raw, item.Entry().Raw)
			}
			require.Equal(t, []string{"second"}, raw)
		})
	}
}

func TestCanceledPEMDeliveryRemainsSafeOnReplay(t *testing.T) {
	cursor := &logCursor{}
	opts := &LogOptions{Path: "ns/p", Container: "main"}
	seed := "2026-09-06T10:00:00Z -----BEGIN PRIVATE KEY-----\n2026-09-06T10:00:01Z fragment-one\n"
	out := make(chan *LogItem, 2)
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(seed)), out, opts, cursor))
	<-out
	<-out
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan streamResult, 1)
	read := make(chan struct{})
	reader := io.NopCloser(&signaledReader{Reader: strings.NewReader("2026-09-06T10:00:02Z -----END PRIVATE KEY-----\n"), read: read})
	go func() { done <- readLogStream(ctx, reader, make(chan *LogItem), opts, cursor) }()
	<-read
	select {
	case <-done:
		t.Fatal("END record did not wait for delivery")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	require.Equal(t, streamCanceled, <-done)
	require.Equal(t, "2026-09-06T10:00:01Z", cursor.time.Format(time.RFC3339Nano))
	cursor.reconnect()
	resumed := make(chan *LogItem, 5)
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader("2026-09-06T10:00:01Z fragment-one\n2026-09-06T10:00:02Z fragment-two\n2026-09-06T10:00:03Z -----END PRIVATE KEY-----\n")), resumed, opts, cursor))
	close(resumed)
	var entries []logstream.Entry
	for item := range resumed {
		entries = append(entries, item.Entry())
		require.NotContains(t, string(item.Bytes), "fragment-two")
	}
	require.Len(t, entries, 2)
	require.Equal(t, "fragment-two", entries[0].Raw)
	require.True(t, entries[0].Sensitive)
	require.NotContains(t, logstream.SafeEntry(entries[0]).Raw, "fragment-two")
}
