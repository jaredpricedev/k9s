// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
package dao

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const testEventMarker = "event"

func TestCollectedRecordsRenderSeparateLines(t *testing.T) {
	out := make(chan *LogItem, 10)
	require.Equal(t, streamEOF, readLogs(context.Background(), io.NopCloser(strings.NewReader("2026-09-06T10:00:00Z first\n2026-09-06T10:00:01Z second\n")), out, &LogOptions{Path: "ns/p", Container: "main", SingleContainer: true}))
	var display bytes.Buffer
	for _, want := range []string{"first", "second"} {
		item := <-out
		require.Equal(t, want, item.Entry().Raw)
		item.Render("white", false, &display)
	}
	require.Equal(t, "first\nsecond\n", display.String())
}

type failAfterReader struct{ io.Reader }

func (r failAfterReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, fmt.Errorf("transport interrupted")
	}
	return n, err
}

func TestFiniteReadFailureResumesInclusiveCursor(t *testing.T) {
	for _, mode := range []string{"head", "previous", "completed"} {
		t.Run(mode, func(t *testing.T) {
			out := make(chan *LogItem, 20)
			attempts := 0
			o := &LogOptions{Path: "ns/p", Container: "main", Head: mode == "head", Previous: mode == "previous"}
			followLogSource(context.Background(), followTestPod("p", "uid", 0), o, true, func(_ context.Context, _ *v1.Pod, _ *LogOptions, po *v1.PodLogOptions) (io.ReadCloser, error) {
				attempts++
				require.False(t, po.Follow)
				if attempts == 1 {
					return io.NopCloser(failAfterReader{strings.NewReader("2026-09-06T10:00:00.123Z first\n2026-09-06T10:00:00.124Z torn")}), nil
				}
				require.NotNil(t, po.SinceTime)
				require.Equal(t, "2026-09-06T10:00:00.123Z", po.SinceTime.Format(time.RFC3339Nano))
				return io.NopCloser(strings.NewReader("2026-09-06T10:00:00.123Z first\n2026-09-06T10:00:00.124Z second\n")), nil
			}, out)
			close(out)
			var raw []string
			for item := range out {
				if item.Marker == nil {
					raw = append(raw, item.Entry().Raw)
				}
			}
			require.Equal(t, []string{"first", "second"}, raw)
			require.Equal(t, 2, attempts)
		})
	}
}

func TestLiveSourceRecoversBeyondLifetimeRetryBudget(t *testing.T) {
	for _, successful := range []bool{false, true} {
		t.Run(fmt.Sprint(successful), func(t *testing.T) {
			state := &logSourceState{cursor: &logCursor{}}
			out := make(chan *LogItem, 100)
			opens := 0
			waits := 0
			result := runLogSource(context.Background(), followTestPod("p", "uid", 0), &LogOptions{Path: "ns/p", Container: "main"}, state, func(_ context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
				opens++
				if opens <= 25 {
					if successful {
						return io.NopCloser(strings.NewReader(fmt.Sprintf("2026-09-06T10:00:00.%09dZ record-%d\n", opens, opens))), nil
					}
					return nil, fmt.Errorf("temporary disconnect")
				}
				state.terminal.Store(true)
				return io.NopCloser(strings.NewReader("2026-09-06T10:00:01Z recovered\n")), nil
			}, out, func(_ context.Context, delay time.Duration) bool {
				waits++
				require.GreaterOrEqual(t, delay, 500*time.Millisecond)
				require.LessOrEqual(t, delay, 30*time.Second)
				return true
			})
			require.Equal(t, sourceCompleted, result)
			require.Equal(t, 26, opens)
			require.Equal(t, 25, waits)
			close(out)
			var raw []string
			for item := range out {
				if item.Marker == nil {
					raw = append(raw, item.Entry().Raw)
				}
			}
			require.Equal(t, "recovered", raw[len(raw)-1])
			if successful {
				require.Len(t, raw, 26)
			} else {
				require.Len(t, raw, 1)
			}
		})
	}
}

func TestRunningSourceObservesTerminalWatchUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pod := followTestPod("target", "uid", 0)
	k := fake.NewSimpleClientset(pod)
	opened := make(chan *io.PipeWriter, 1)
	var opens atomic.Int32
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{}, func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		opens.Add(1)
		r, w := io.Pipe()
		opened <- w
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	})
	require.NoError(t, err)
	w := <-opened
	pod.Status.ContainerStatuses[0].State = v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 0}}
	_, err = k.CoreV1().Pods("ns").Update(ctx, pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	for {
		select {
		case item := <-out:
			if item.Marker != nil && item.Marker.Kind == "source-terminal" {
				w.Close()
				goto completed
			}
		case <-time.After(time.Second):
			t.Fatal("terminal update not observed")
		}
	}
completed:
	for {
		select {
		case item := <-out:
			if item.Marker != nil && item.Marker.Kind == "source-complete" {
				require.Equal(t, int32(1), opens.Load())
				return
			}
		case <-time.After(time.Second):
			t.Fatal("terminal EOF did not complete")
		}
	}
}

func TestEventsWaitForPodDiscoveryAtStartupAndLive(t *testing.T) {
	for _, startup := range []bool{true, false} {
		t.Run(fmt.Sprint(startup), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			k := fake.NewSimpleClientset()
			podWatch := watch.NewRaceFreeFake()
			eventWatch := watch.NewFake()
			k.PrependWatchReactor("pods", func(_ ktesting.Action) (bool, watch.Interface, error) { return true, podWatch, nil })
			ready := make(chan string, 1)
			k.PrependWatchReactor("events", func(a ktesting.Action) (bool, watch.Interface, error) {
				ready <- a.(ktesting.WatchAction).GetWatchRestrictions().ResourceVersion
				return true, eventWatch, nil
			})
			pod := followTestPod("new", "uid-new", 0)
			event := &v1.Event{ObjectMeta: metav1.ObjectMeta{Name: testEventMarker, UID: testEventMarker, ResourceVersion: "99"}, InvolvedObject: v1.ObjectReference{UID: "uid-new", Kind: "Pod"}, Reason: "Unhealthy", Message: "before discovery"}
			k.PrependReactor("list", "events", func(_ ktesting.Action) (bool, runtime.Object, error) {
				list := &v1.EventList{ListMeta: metav1.ListMeta{ResourceVersion: "100"}}
				if startup {
					podWatch.Add(pod.DeepCopy())
					list.Items = []v1.Event{*event.DeepCopy()}
				}
				return true, list, nil
			})
			out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Events: true}, func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
				r, w := io.Pipe()
				context.AfterFunc(ctx, func() { w.Close() })
				return r, nil
			})
			require.NoError(t, err)
			require.Equal(t, "100", <-ready)
			if !startup {
				eventWatch.Add(event.DeepCopy())
				podWatch.Add(pod.DeepCopy())
			}
			deadline := time.After(time.Second)
			for {
				select {
				case item := <-out:
					if item.Marker != nil && item.Marker.Kind == testEventMarker {
						require.Equal(t, "uid-new", item.Source.UID)
						require.Equal(t, "Unhealthy: before discovery", item.Marker.Message)
						return
					}
				case <-deadline:
					t.Fatal("Event lost before pod discovery")
				}
			}
		})
	}
}

func TestPendingEventCorrelationCapIsVisible(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := fake.NewSimpleClientset()
	pw := watch.NewRaceFreeFake()
	ew := watch.NewFake()
	ready := make(chan struct{})
	k.PrependWatchReactor("pods", func(_ ktesting.Action) (bool, watch.Interface, error) { return true, pw, nil })
	k.PrependWatchReactor("events", func(_ ktesting.Action) (bool, watch.Interface, error) { close(ready); return true, ew, nil })
	out, err := followPodLogs(ctx, k, "ns", metav1.ListOptions{}, &LogOptions{Events: true}, func(ctx context.Context, _ *v1.Pod, _ *LogOptions, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		r, w := io.Pipe()
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	})
	require.NoError(t, err)
	<-ready
	for i := range 513 {
		ew.Add(&v1.Event{ObjectMeta: metav1.ObjectMeta{UID: types.UID(fmt.Sprintf("event-%d", i)), ResourceVersion: "1"}, InvolvedObject: v1.ObjectReference{UID: types.UID(fmt.Sprintf("uid-%d", i)), Kind: "Pod"}, Reason: "BackOff"})
	}
	pw.Add(followTestPod("new", "uid-512", 0))
	partial, correlated := false, false
	deadline := time.After(time.Second)
	for !partial || !correlated {
		select {
		case item := <-out:
			if item.Marker != nil {
				partial = partial || (item.Marker.Kind == "events-partial" && strings.Contains(item.Marker.Message, "pending"))
				correlated = correlated || (item.Marker.Kind == testEventMarker && item.Source.UID == "uid-512")
			}
		case <-deadline:
			t.Fatal("pending Event limit or retained correlation missing")
		}
	}
}
