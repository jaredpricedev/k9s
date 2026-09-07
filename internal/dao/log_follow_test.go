// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCollectedRawIndependentOfRendering(t *testing.T) {
	o := &LogOptions{Path: "ns/pod", Container: "main", Context: "dev"}
	item := o.ToLogItem([]byte("2026-09-06T10:00:00.123456789Z {\"message\":\"[red]hello\\u001b[2J\"}\n"))
	expectedRaw := []byte(`{"message":"[red]hello\u001b[2J"}`)
	require.Equal(t, expectedRaw, item.Raw)
	require.JSONEq(t, string(expectedRaw), string(item.Raw))
	require.Equal(t, "pod", item.Source.Pod)
	require.Equal(t, "dev", item.Source.Context)
	require.Equal(t, "[red]hello\x1b[2J", item.Entry().Message)
	var b bytes.Buffer
	item.Render("white", false, &b)
	require.NotContains(t, b.String(), "\x1b")
	require.Contains(t, b.String(), "[red[]")
}

func TestInclusiveCursorOptions(t *testing.T) {
	o := &LogOptions{SinceTime: "2026-09-06T10:00:00.123456789Z"}
	require.Equal(t, "2026-09-06T10:00:00.123456789Z", o.ToPodLogOptions().SinceTime.Format(time.RFC3339Nano))
	o.Previous = true
	require.False(t, o.ToPodLogOptions().Follow)
}

func TestReadLogsBlockedReadAndFullEOFCancel(t *testing.T) {
	for _, blockedRead := range []bool{true, false} {
		t.Run(map[bool]string{true: "read", false: "fullEOF"}[blockedRead], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var r io.ReadCloser
			if blockedRead {
				p, w := io.Pipe()
				defer w.Close()
				r = p
			} else {
				r = io.NopCloser(strings.NewReader("partial"))
			}
			done := make(chan streamResult, 1)
			go func() { done <- readLogs(ctx, r, make(chan *LogItem), &LogOptions{}) }()
			cancel()
			select {
			case result := <-done:
				require.Equal(t, streamCanceled, result)
			case <-time.After(time.Second):
				t.Fatal("cancellation did not unblock reader/send")
			}
		})
	}
}

func TestReadLogsBoundsPhysicalLine(t *testing.T) {
	out := make(chan *LogItem, 10)
	result := readLogs(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("x", 200000)+"\nok\n")), out, &LogOptions{})
	require.Equal(t, streamEOF, result)
	first := <-out
	require.LessOrEqual(t, len(first.Raw), 65536)
	require.True(t, first.Truncated)
	require.Equal(t, "ok", string((<-out).Raw))
}

func TestCursorBoundaryKeepsNewDuplicatesAndSubsecondLines(t *testing.T) {
	o := &LogOptions{}
	c := &logCursor{}
	a := o.ToLogItem([]byte("2026-09-06T10:00:00.100Z same"))
	require.False(t, c.replayed(a))
	c.advance(a)
	require.False(t, c.replayed(a))
	c.advance(a)
	c.reconnect()
	require.True(t, c.replayed(a))
	require.True(t, c.replayed(a))
	require.False(t, c.replayed(a))
	c.advance(a)
	next := o.ToLogItem([]byte("2026-09-06T10:00:00.101Z new"))
	require.False(t, c.replayed(next))
	c.advance(next)
}

func TestCursorReconnectSkipsOnlyExactBoundaryReplay(t *testing.T) {
	opts := &LogOptions{Path: "ns/p", Container: "main"}
	cursor := &logCursor{}
	first := "2026-09-06T10:00:02Z first\n"
	out := make(chan *LogItem, 1)
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(first)), out, opts, cursor))
	require.Equal(t, "first", string((<-out).Raw))

	cursor.reconnect()
	resumed := make(chan *LogItem, 2)
	input := first + "2026-09-06T10:00:01Z arrived-late\n"
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(input)), resumed, opts, cursor))
	close(resumed)
	var raw []string
	for item := range resumed {
		raw = append(raw, string(item.Raw))
	}
	require.Equal(t, []string{"arrived-late"}, raw)
}

func TestCursorBackwardDeliveryDoesNotPolluteReconnectBoundary(t *testing.T) {
	opts := &LogOptions{}
	cursor := &logCursor{}
	boundary := opts.ToLogItem([]byte("2026-09-06T10:00:02Z boundary"))
	late := opts.ToLogItem([]byte("2026-09-06T10:00:01Z same-at-different-time"))
	cursor.advance(boundary)
	cursor.advance(late)
	cursor.reconnect()
	atBoundary := opts.ToLogItem([]byte("2026-09-06T10:00:02Z same-at-different-time"))
	require.False(t, cursor.replayed(atBoundary))
}

func TestReadLogStreamBackwardTimestampPreservesPEMProvenance(t *testing.T) {
	opts := &LogOptions{Path: "ns/p", Container: "main"}
	cursor := &logCursor{}
	out := make(chan *LogItem, 3)
	input := "2026-09-06T10:00:02Z -----BEGIN PRIVATE KEY-----\n" +
		"2026-09-06T10:00:01Z privatefragment\n" +
		"2026-09-06T10:00:03Z -----END PRIVATE KEY-----\n"
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(input)), out, opts, cursor))
	close(out)
	var entries []logstream.Entry
	for item := range out {
		entries = append(entries, item.Entry())
	}
	require.Len(t, entries, 3)
	require.Equal(t, "privatefragment", entries[1].Raw)
	require.True(t, entries[1].Sensitive)
	require.NotContains(t, logstream.SafeEntry(entries[1]).Raw, "privatefragment")
}

func TestReadLogStreamReconnectUsesSerializedSinceTimePrecisionForPEMReplay(t *testing.T) {
	opts := &LogOptions{Path: "ns/p", Container: "main"}
	cursor := &logCursor{}
	initial := "2026-09-06T10:00:00.100Z -----BEGIN PRIVATE KEY-----\n" +
		"2026-09-06T10:00:00.200Z private-a\n" +
		"2026-09-06T10:00:00.300Z -----END PRIVATE KEY-----\n" +
		"2026-09-06T10:00:00.400Z -----BEGIN PRIVATE KEY-----\n"
	out := make(chan *LogItem, 4)
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(initial)), out, opts, cursor))
	for range 4 {
		<-out
	}

	serialized, err := json.Marshal(&metav1.Time{Time: cursor.time})
	require.NoError(t, err)
	require.Equal(t, `"2026-09-06T10:00:00Z"`, string(serialized))

	cursor.reconnect()
	resumed := make(chan *LogItem, 5)
	replay := initial + "2026-09-06T10:00:00.500Z private-b\n"
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(replay)), resumed, opts, cursor))
	close(resumed)
	var got []*LogItem
	for item := range resumed {
		got = append(got, item)
	}
	require.Len(t, got, 1)
	require.Equal(t, "private-b", string(got[0].Raw))
	require.True(t, got[0].Sensitive)
	require.NotContains(t, string(got[0].Bytes), "private-b")
}

func TestReadLogStreamReconnectFailsClosedWhenSerializedBoundaryIsUncertain(t *testing.T) {
	opts := &LogOptions{Path: "ns/p", Container: "main"}
	cursor := &logCursor{}
	var input strings.Builder
	input.WriteString("2026-09-06T10:00:00.001Z -----BEGIN PRIVATE KEY-----\n")
	for i := range 1023 {
		fmt.Fprintf(&input, "2026-09-06T10:00:00.%06dZ private-%d\n", i+2, i)
	}
	input.WriteString("2026-09-06T10:00:00.999999Z -----END PRIVATE KEY-----\n")
	out := make(chan *LogItem, 1025)
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(input.String())), out, opts, cursor))
	for range 1025 {
		<-out
	}

	cursor.reconnect()
	resumed := make(chan *LogItem, 1026)
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(input.String()+"2026-09-06T10:00:00.9999999Z private-after-uncertain-replay\n")), resumed, opts, cursor))
	close(resumed)
	var last *LogItem
	for item := range resumed {
		last = item
	}
	require.NotNil(t, last)
	require.Equal(t, "private-after-uncertain-replay", string(last.Raw))
	require.True(t, last.Sensitive)
	require.NotContains(t, string(last.Bytes), "private-after-uncertain-replay")
}

func TestFollowEmptySelectorNewPodRestartAndReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := fake.NewSimpleClientset()
	opened := make(chan logstream.Source, 10)
	closed := make(chan struct{}, 10)
	opener := func(ctx context.Context, _ *v1.Pod, o *LogOptions, po *v1.PodLogOptions) (io.ReadCloser, error) {
		require.True(t, po.Timestamps)
		opened <- o.Source
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { w.Close(); closed <- struct{}{} })
		return r, nil
	}
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{LabelSelector: "app in (demo)"}, &LogOptions{AllContainers: true}, opener)
	require.NoError(t, err)
	go func() {
		for range out {
		}
	}()
	pod := followTestPod("one", "uid1", 0)
	_, err = k.CoreV1().Pods("ns").Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)
	s := awaitSource(t, opened)
	require.Equal(t, "uid1", s.UID)
	require.Equal(t, int32(0), s.Generation)
	pod.Status.ContainerStatuses[0].RestartCount = 1
	_, err = k.CoreV1().Pods("ns").Update(ctx, pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	s = awaitSource(t, opened)
	require.Equal(t, int32(1), s.Generation)
	require.NoError(t, k.CoreV1().Pods("ns").Delete(ctx, "one", metav1.DeleteOptions{}))
	_, err = k.CoreV1().Pods("ns").Create(ctx, followTestPod("one", "uid2", 0), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Equal(t, "uid2", awaitSource(t, opened).UID)
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("source did not close")
	}
}

func TestFollowSourceCapAndDeniedEvents(t *testing.T) {
	objects := make([]runtime.Object, 40)
	for i := range objects {
		objects[i] = followTestPod(fmt.Sprintf("p%02d", i), fmt.Sprintf("uid%d", i), 0)
	}
	k := fake.NewSimpleClientset(objects...)
	k.PrependWatchReactor("events", func(_ ktesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", fmt.Errorf("denied"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, maximum atomic.Int32
	open := func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		n := active.Add(1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { active.Add(-1); w.Close() })
		return r, nil
	}
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Events: true}, open)
	require.NoError(t, err)
	seenCap, seenEvents := false, false
	deadline := time.After(3 * time.Second)
	for !seenCap || !seenEvents {
		select {
		case item := <-out:
			if item.Marker != nil {
				seenCap = seenCap || item.Marker.Kind == "source-limit"
				seenEvents = seenEvents || item.Marker.Kind == "events-unavailable"
			}
		case <-deadline:
			t.Fatal("missing cap/events markers")
		}
	}
	require.LessOrEqual(t, maximum.Load(), int32(32))
	require.Positive(t, maximum.Load())
	cancel()
	for range out {
	}
	require.Eventually(t, func() bool { return active.Load() == 0 }, time.Second, time.Millisecond)
}

func TestFollowWatchClosureRelists(t *testing.T) {
	k := fake.NewSimpleClientset()
	fw := watch.NewRaceFreeFake()
	var watches atomic.Int32
	k.PrependWatchReactor("pods", func(_ ktesting.Action) (bool, watch.Interface, error) {
		if watches.Add(1) == 1 {
			return true, fw, nil
		}
		return false, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan logstream.Source, 1)
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{}, func(_ context.Context, _ *v1.Pod, o *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		opened <- o.Source
		return io.NopCloser(strings.NewReader("")), nil
	})
	require.NoError(t, err)
	go func() {
		for range out {
		}
	}()
	require.Eventually(t, func() bool { return watches.Load() > 0 }, time.Second, time.Millisecond)
	require.NoError(t, k.Tracker().Add(followTestPod("new", "replacement", 0)))
	fw.Stop()
	require.Equal(t, "replacement", awaitSource(t, opened).UID)
}

func followTestPod(name, uid string, generation int32) *v1.Pod {
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(uid), Labels: map[string]string{"app": "demo"}}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main"}}}, Status: v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{{Name: "main", RestartCount: generation, State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}}}}}
}
func awaitSource(t *testing.T, ch <-chan logstream.Source) logstream.Source {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("source not opened")
		return logstream.Source{}
	}
}

func TestFollowSourceReconnectInclusiveBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pod := followTestPod("one", "uid1", 0)
	var attempts atomic.Int32
	opts := &LogOptions{Path: "ns/one", Container: "main"}
	out := make(chan *LogItem, 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		followLogSource(ctx, pod, opts, false, func(_ context.Context, _ *v1.Pod, _ *LogOptions, po *v1.PodLogOptions) (io.ReadCloser, error) {
			if attempts.Add(1) == 1 {
				return io.NopCloser(strings.NewReader("2026-09-06T10:00:00.123456789Z first\n")), nil
			}
			if po.SinceTime == nil || po.SinceTime.Format(time.RFC3339Nano) != "2026-09-06T10:00:00.123456789Z" {
				return nil, fmt.Errorf("wrong inclusive cursor: %v", po.SinceTime)
			}
			return io.NopCloser(strings.NewReader("2026-09-06T10:00:00.123456789Z first\n2026-09-06T10:00:00.123456789Z second\n2026-09-06T10:00:00.123456790Z third\n")), nil
		}, out)
	}()
	var raw []string
	for len(raw) < 3 {
		select {
		case item := <-out:
			require.False(t, item.IsError)
			raw = append(raw, string(item.Raw))
		case <-time.After(2 * time.Second):
			t.Fatal("reconnect missing output")
		}
	}
	require.Equal(t, []string{"first", "second", "third"}, raw)
	cancel()
	<-done
}

func TestFiniteStreamsCloseWithoutReconnect(t *testing.T) {
	for _, mode := range []string{"head", "previous", "completed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pod := followTestPod("one", "uid1", 0)
			opts := &LogOptions{Head: mode == "head", Previous: mode == "previous"}
			if mode == "completed" {
				pod.Status.Phase = v1.PodSucceeded
				pod.Status.ContainerStatuses[0].State = v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 0}}
			}
			var calls atomic.Int32
			out, err := followPodLogs(ctx, fake.NewSimpleClientset(pod), "ns", metav1.ListOptions{}, opts, func(_ context.Context, _ *v1.Pod, _ *LogOptions, po *v1.PodLogOptions) (io.ReadCloser, error) {
				calls.Add(1)
				if po.Follow {
					return nil, fmt.Errorf("finite stream follows")
				}
				return io.NopCloser(strings.NewReader("done\n")), nil
			})
			require.NoError(t, err)
			if mode == "completed" {
				require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
				time.AfterFunc(600*time.Millisecond, cancel)
			}
			for range out {
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestFollowRevisionMarkerUpdatesWithoutNewGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pod := followTestPod("one", "uid1", 0)
	pod.Labels["pod-template-hash"] = "old"
	k := fake.NewSimpleClientset(pod)
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{}, func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	})
	require.NoError(t, err)
	pod.Labels["pod-template-hash"] = "new"
	_, err = k.CoreV1().Pods("ns").Update(ctx, pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	deadline := time.After(time.Second)
	for {
		select {
		case item := <-out:
			if item.Marker != nil && item.Marker.Kind == "rollout" && strings.Contains(item.Marker.Message, "new") {
				return
			}
		case <-deadline:
			t.Fatal("revision update marker missing")
		}
	}
}

func TestFollowStartDoesNotWaitForMarkerConsumer(t *testing.T) {
	objects := make([]runtime.Object, 129)
	for i := range objects {
		objects[i] = followTestPod(fmt.Sprintf("p%d", i), fmt.Sprintf("u%d", i), 0)
	}
	k := fake.NewSimpleClientset(objects...)
	k.PrependWatchReactor("pods", func(_ ktesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("denied"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{LogBufferSize: 1}, func(_ context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		})
		if err == nil {
			cancel()
			for range out {
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startup blocked on unconsumed marker")
	}
}

func TestSinglePodSelectionAndTerminationProvenance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pod := followTestPod("target", "uid-target", 2)
	finished := metav1.NewTime(time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC))
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 42, FinishedAt: finished}
	k := fake.NewSimpleClientset(followTestPod("other", "uid-other", 0), pod)
	opened := make(chan logstream.Source, 5)
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{FieldSelector: "metadata.name=target"}, &LogOptions{}, func(ctx context.Context, _ *v1.Pod, o *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		opened <- o.Source
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	})
	require.NoError(t, err)
	require.Equal(t, "target", awaitSource(t, opened).Pod)
	deadline := time.After(time.Second)
	for {
		select {
		case item := <-out:
			if item.Marker != nil && item.Marker.Kind == "last-termination" {
				require.Equal(t, "uid-target", item.Source.UID)
				require.Equal(t, finished.Time, item.Marker.Time)
				require.Equal(t, "pod-status", item.Marker.Origin)
				require.Contains(t, item.Marker.Message, "42")
				return
			}
		case <-deadline:
			t.Fatal("termination marker missing")
		}
	}
}

func TestEventMarkerDeduplicatesWithSourceAndTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := fake.NewSimpleClientset(followTestPod("target", "uid-target", 0))
	fw := watch.NewRaceFreeFake()
	watchReady := make(chan struct{})
	k.PrependWatchReactor("events", func(_ ktesting.Action) (bool, watch.Interface, error) { close(watchReady); return true, fw, nil })
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Events: true}, func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	})
	require.NoError(t, err)
	<-watchReady
	at := metav1.NewTime(time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC))
	event := &v1.Event{ObjectMeta: metav1.ObjectMeta{UID: "event-1", ResourceVersion: "1"}, InvolvedObject: v1.ObjectReference{UID: "uid-target", Kind: "Pod"}, Reason: "BackOff", Message: "retry", LastTimestamp: at}
	fw.Add(event)
	fw.Modify(event)
	time.AfterFunc(50*time.Millisecond, cancel)
	count := 0
	for item := range out {
		if item.Marker != nil && item.Marker.Kind == testEventMarker {
			count++
			require.Equal(t, "uid-target", item.Source.UID)
			require.Equal(t, at.Time, item.Marker.Time)
			require.Equal(t, "kubernetes-events", item.Marker.Origin)
		}
	}
	require.Equal(t, 1, count)
}

func TestPrivateKeyProvenanceSurvivesReadReconnect(t *testing.T) {
	out := make(chan *LogItem, 10)
	cursor := &logCursor{}
	opts := &LogOptions{Path: "ns/pod", Container: "main"}
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader("2026-09-06T10:00:00Z -----BEGIN PRIVATE KEY-----\n")), out, opts, cursor))
	<-out
	cursor.reconnect()
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader("2026-09-06T10:00:01Z privatefragment\n")), out, opts, cursor))
	item := <-out
	require.Equal(t, "privatefragment", string(item.Raw))
	require.True(t, item.Entry().Sensitive)
	require.NotContains(t, logstream.SafeEntry(item.Entry()).Raw, "privatefragment")
	require.NotContains(t, string(item.Bytes), "privatefragment")
}

func TestTruncatedPrivateKeyDelimiterFailsClosed(t *testing.T) {
	out := make(chan *LogItem, 10)
	cursor := &logCursor{}
	opts := &LogOptions{Path: "ns/p", Container: "main"}
	input := strings.Repeat("x", 70000) + "-----BEGIN PRIVATE KEY-----\nprivatefragment\n"
	require.Equal(t, streamEOF, readLogStream(context.Background(), io.NopCloser(strings.NewReader(input)), out, opts, cursor))
	first, second := <-out, <-out
	require.True(t, first.Truncated)
	require.True(t, second.Entry().Sensitive)
	require.NotContains(t, string(second.Bytes), "privatefragment")
}

func TestEventsBackfillBeforeWatchFromListResourceVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := &v1.Event{ObjectMeta: metav1.ObjectMeta{Name: "before", Namespace: "ns", UID: "event-before", ResourceVersion: "98"}, InvolvedObject: v1.ObjectReference{Kind: "Pod", UID: "uid-target"}, Reason: "Unhealthy", Message: "before opening", LastTimestamp: metav1.NewTime(time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC))}
	unrelated := before.DeepCopy()
	unrelated.Name = "unrelated"
	unrelated.UID = "event-unrelated"
	unrelated.InvolvedObject.UID = "other-pod"
	k := fake.NewSimpleClientset(followTestPod("target", "uid-target", 0), before, unrelated)
	k.PrependReactor("list", "events", func(_ ktesting.Action) (bool, runtime.Object, error) {
		obj, err := k.Tracker().List(v1.SchemeGroupVersion.WithResource("events"), v1.SchemeGroupVersion.WithKind("Event"), "ns")
		if err == nil {
			obj.(*v1.EventList).ResourceVersion = "100"
		}
		return true, obj, err
	})
	fw := watch.NewRaceFreeFake()
	watchVersion := make(chan string, 1)
	k.PrependWatchReactor("events", func(a ktesting.Action) (bool, watch.Interface, error) {
		watchVersion <- a.(ktesting.WatchAction).GetWatchRestrictions().ResourceVersion
		return true, fw, nil
	})
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Events: true}, func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	})
	require.NoError(t, err)
	select {
	case rv := <-watchVersion:
		require.Equal(t, "100", rv)
	case <-time.After(time.Second):
		t.Fatal("Event watch not opened")
	}
	after := before.DeepCopy()
	after.Name = "after"
	after.UID = "event-after"
	after.ResourceVersion = "101"
	after.Reason = "BackOff"
	after.Message = "after opening"
	fw.Add(before.DeepCopy())
	fw.Add(after)
	var messages []string
	for len(messages) < 2 {
		select {
		case item := <-out:
			if item.Marker != nil && item.Marker.Kind == testEventMarker {
				messages = append(messages, item.Marker.Message)
			}
		case <-time.After(time.Second):
			t.Fatal("missing backfilled/watched events")
		}
	}
	require.Equal(t, []string{"Unhealthy: before opening", "BackOff: after opening"}, messages)
}

func TestBoundedEventBackfillKeepsHeadFinite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := fake.NewSimpleClientset(followTestPod("target", "uid-target", 0))
	k.PrependReactor("list", "events", func(_ ktesting.Action) (bool, runtime.Object, error) {
		list := &v1.EventList{ListMeta: metav1.ListMeta{ResourceVersion: "100", Continue: "more"}}
		for i := range 513 {
			list.Items = append(list.Items, v1.Event{ObjectMeta: metav1.ObjectMeta{UID: types.UID(fmt.Sprintf("event-%d", i)), ResourceVersion: "1"}, InvolvedObject: v1.ObjectReference{UID: "uid-target", Kind: "Pod"}, Reason: "BackOff"})
		}
		return true, list, nil
	})
	var watched atomic.Bool
	k.PrependWatchReactor("events", func(_ ktesting.Action) (bool, watch.Interface, error) {
		watched.Store(true)
		return true, nil, fmt.Errorf("finite view must not watch Events")
	})
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Head: true, Events: true}, func(_ context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("done\n")), nil
	})
	require.NoError(t, err)
	count, partial := 0, 0
	for item := range out {
		if item.Marker != nil {
			switch item.Marker.Kind {
			case testEventMarker:
				count++
			case "events-partial":
				partial++
			}
		}
	}
	require.Equal(t, 512, count)
	require.Equal(t, 1, partial)
	require.False(t, watched.Load())
}
