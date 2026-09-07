// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"fmt"
	"io"
	"maps"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/logstream"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

const maxLogSources = 32
const maxLogPods = 128

type logOpener func(context.Context, *v1.Pod, *LogOptions, *v1.PodLogOptions) (io.ReadCloser, error)
type sourceOutcome int

const (
	sourceCanceled sourceOutcome = iota
	sourceCompleted
	sourceDenied
	sourceExhausted
)

type logSourceState struct {
	terminal atomic.Bool
	cursor   *logCursor
}
type logSourceDone struct {
	key     string
	outcome sourceOutcome
}
type followedSource struct {
	state           *logSourceState
	outcome         sourceOutcome
	source          logstream.Source
	revision        string
	left            bool
	cancel          context.CancelFunc
	running, wanted bool
}

// followPodLogs owns all discovery state and returns one channel for the entire session.
//
//nolint:gocyclo,funlen,gocritic // Single owner loop; selection is copied so nested watch options cannot mutate the caller.
func followPodLogs(parentCtx context.Context, k kubernetes.Interface, ns string, selection metav1.ListOptions, opts *LogOptions, open logOpener) (LogChan, error) {
	selector, err := labels.Parse(selection.LabelSelector)
	if err != nil {
		return nil, err
	}
	fieldSelector, err := fields.ParseSelector(selection.FieldSelector)
	if err != nil {
		return nil, err
	}
	matches := func(p *v1.Pod) bool {
		return selector.Matches(labels.Set(p.Labels)) && fieldSelector.Matches(fields.Set{"metadata.name": p.Name, "metadata.namespace": p.Namespace})
	}
	listOpts := selection
	listOpts.Limit = maxLogPods
	initial, err := k.CoreV1().Pods(ns).List(parentCtx, listOpts)
	if err != nil {
		return nil, err
	}
	size := opts.LogBufferSize
	if size <= 0 {
		size = logChannelBuffer
	}
	if size > 4096 {
		size = 4096
	}
	out := make(LogChan, size)
	opts = opts.Clone()
	var initialWatch watch.Interface
	var initialWatchErr error
	if !opts.Head && !opts.Previous {
		wo := selection
		wo.ResourceVersion = initial.ResourceVersion
		wo.AllowWatchBookmarks = true
		initialWatch, initialWatchErr = k.CoreV1().Pods(ns).Watch(parentCtx, wo)
	}
	go func() {
		ctx, cancel := context.WithCancel(parentCtx)
		var workers sync.WaitGroup
		defer func() { cancel(); workers.Wait(); close(out) }()
		pods := make(map[string]*v1.Pod)
		marker := func(source logstream.Source, kind, origin, message string, at time.Time, approx bool) bool {
			o := opts.Clone()
			o.Path = client.FQN(source.Namespace, source.Pod)
			o.Container = source.Container
			o.Source = source
			if at.IsZero() {
				at = time.Now().UTC()
			}
			item := o.ToLogItem([]byte(at.Format(time.RFC3339Nano) + " " + message))
			item.Marker = &logstream.Marker{Kind: kind, Origin: origin, Message: message, Time: at, Approximate: approx}
			if p := pods[source.Pod]; p != nil {
				item.Labels = maps.Clone(p.Labels)
				item.Annotations = maps.Clone(p.Annotations)
			}
			item.IsError = kind == "error" || kind == "events-unavailable" || kind == "source-limit"
			return sendLog(ctx, out, item)
		}
		base := logstream.Source{Context: opts.Context, Cluster: opts.Cluster, Namespace: ns}
		sources := make(map[string]*followedSource)
		done := make(chan logSourceDone, maxLogSources)
		active := 0
		capWarned := false
		warnCap := func() {
			if !capWarned {
				capWarned = true
				marker(base, "source-limit", "collector", "source discovery/stream limit reached (32 active streams, 128 tracked pods); some sources omitted", time.Time{}, true)
			}
		}
		ingestList := func(list *v1.PodList) {
			next := make(map[string]*v1.Pod)
			sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
			for i := range list.Items {
				p := &list.Items[i]
				if !matches(p) {
					continue
				}
				if len(next) >= maxLogPods {
					warnCap()
					break
				}
				next[p.Name] = p.DeepCopy()
			}
			if list.Continue != "" {
				warnCap()
			}
			pods = next
		}
		ingestList(initial)
		replayPending := func() {}
		reconcile := func() {
			for _, s := range sources {
				s.wanted = false
			}
			names := make([]string, 0, len(pods))
			for name := range pods {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				p := pods[name]
				statuses := make(map[string]v1.ContainerStatus)
				for _, ss := range [][]v1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses, p.Status.EphemeralContainerStatuses} {
					for i := range ss {
						statuses[ss[i].Name] = ss[i]
					}
				}
				containers := make([]string, 0, len(p.Spec.Containers))
				if opts.Container != "" && !opts.AllContainers {
					containers = append(containers, opts.Container)
				} else if def, ok := GetDefaultContainer(&p.ObjectMeta, &p.Spec); ok && !opts.AllContainers {
					containers = append(containers, def)
				} else {
					for i := range p.Spec.InitContainers {
						containers = append(containers, p.Spec.InitContainers[i].Name)
					}
					for i := range p.Spec.Containers {
						containers = append(containers, p.Spec.Containers[i].Name)
					}
					for i := range p.Spec.EphemeralContainers {
						containers = append(containers, p.Spec.EphemeralContainers[i].Name)
					}
				}
				for _, container := range containers {
					status := statuses[container]
					source := logstream.Source{
						Context: opts.Context, Cluster: opts.Cluster, Namespace: p.Namespace, Pod: p.Name,
						UID: string(p.UID), Container: container, Generation: status.RestartCount,
					}
					if opts.Previous {
						source.Generation--
					}
					key := source.Key()
					terminal := opts.Head || opts.Previous || status.State.Terminated != nil || p.Status.Phase == v1.PodSucceeded || p.Status.Phase == v1.PodFailed
					s, exists := sources[key]
					if exists {
						s.wanted = true
						wasTerminal := s.state.terminal.Swap(terminal)
						if terminal && !wasTerminal {
							marker(source, "source-terminal", "pod-status", "container is terminal; completing remaining log read", time.Time{}, true)
						}
						if revision := podLogRevision(p); revision != s.revision {
							s.revision = revision
							marker(source, "rollout", "pod-label", revision, time.Time{}, true)
						}
						if s.running || s.outcome != sourceCanceled {
							continue
						}
					}
					if active >= maxLogSources || (!exists && len(sources) >= 256) {
						warnCap()
						continue
					}
					if !exists {
						s = &followedSource{revision: podLogRevision(p), source: source, state: &logSourceState{cursor: &logCursor{}}}
						sources[key] = s
					}
					s.state.terminal.Store(terminal)
					sc, stop := context.WithCancel(ctx)
					s.cancel, s.running, s.wanted, s.left = stop, true, true, false
					active++
					o := opts.Clone()
					o.Source = source
					o.Path = client.FQN(p.Namespace, p.Name)
					o.Container = container
					o.SingleContainer = len(containers) == 1
					o.Labels = maps.Clone(p.Labels)
					o.Annotations = maps.Clone(p.Annotations)
					marker(source, "source-join", "pod-watch", "source joined", time.Time{}, true)
					if terminated := status.LastTerminationState.Terminated; terminated != nil {
						marker(
							source, "last-termination", "pod-status",
							fmt.Sprintf("last termination: %s (exit %d): %s", terminated.Reason, terminated.ExitCode, terminated.Message),
							terminated.FinishedAt.Time, false,
						)
					}
					if revision := podLogRevision(p); revision != "" {
						marker(source, "rollout", "pod-label", revision, time.Time{}, true)
					}
					workers.Add(1)
					go func(p *v1.Pod, o *LogOptions, key string, state *logSourceState) {
						defer workers.Done()
						outcome := runLogSource(sc, p, o, state, open, out, waitLogRetry)
						select {
						case done <- logSourceDone{key: key, outcome: outcome}:
						case <-ctx.Done():
						}
					}(p.DeepCopy(), o, key, s.state)
				}
			}
			for key, s := range sources {
				if !s.wanted {
					s.cancel()
					if !s.running {
						delete(sources, key)
					}
					if !s.left {
						s.left = true
						marker(s.source, "source-leave", "pod-watch", "source left or container generation changed", time.Time{}, true)
					}
				}
			}
			replayPending()
		}
		finite := opts.Head || opts.Previous
		pw := initialWatch
		var ew watch.Interface
		defer func() {
			if pw != nil {
				pw.Stop()
			}
			if ew != nil {
				ew.Stop()
			}
		}()
		var pc, ec <-chan watch.Event
		watchPods := func(rv string) {
			wo := selection
			wo.ResourceVersion = rv
			wo.AllowWatchBookmarks = true
			var err error
			pw, err = k.CoreV1().Pods(ns).Watch(ctx, wo)
			if err != nil {
				marker(base, "error", "pod-watch", err.Error(), time.Time{}, true)
				pc = nil
			} else {
				pc = pw.ResultChan()
			}
		}
		if pw != nil {
			pc = pw.ResultChan()
		}
		if initialWatchErr != nil {
			marker(base, "error", "pod-watch", initialWatchErr.Error(), time.Time{}, true)
		}
		reconcile()
		eventsDisabled := !opts.Events
		eventSeen := make(map[string]struct{})
		type pendingEventKey struct{ uid, key string }
		pending := make(map[string]map[string]*v1.Event)
		pendingOrder := make([]pendingEventKey, 0, 512)
		pendingWarned := false
		retainEvent := func(event *v1.Event) {
			uid, key := string(event.InvolvedObject.UID), string(event.UID)+"/"+event.ResourceVersion
			if uid == "" {
				return
			}
			if pending[uid] != nil && pending[uid][key] != nil {
				return
			}
			if len(pendingOrder) >= 512 {
				oldest := pendingOrder[0]
				pendingOrder = pendingOrder[1:]
				delete(pending[oldest.uid], oldest.key)
				if len(pending[oldest.uid]) == 0 {
					delete(pending, oldest.uid)
				}
				if !pendingWarned {
					pendingWarned = true
					marker(base, "events-partial", "kubernetes-events", "pending Event correlation limited to 512 records; older unmatched Events omitted", time.Time{}, true)
				}
			}
			message := event.Message
			if len(message) > maxLogLineBytes {
				message = message[:maxLogLineBytes] + " [Event message truncated]"
			}
			reason := event.Reason
			if len(reason) > 1024 {
				reason = reason[:1024]
			}
			compact := &v1.Event{
				ObjectMeta:     metav1.ObjectMeta{UID: event.UID, ResourceVersion: event.ResourceVersion},
				InvolvedObject: v1.ObjectReference{UID: event.InvolvedObject.UID},
				Reason:         reason, Message: message, LastTimestamp: event.LastTimestamp, EventTime: event.EventTime,
			}
			if pending[uid] == nil {
				pending[uid] = make(map[string]*v1.Event)
			}
			pending[uid][key] = compact
			pendingOrder = append(pendingOrder, pendingEventKey{uid, key})
		}
		processEvent := func(event *v1.Event) {
			key := string(event.UID) + "/" + event.ResourceVersion
			if _, seen := eventSeen[key]; seen {
				return
			}
			for _, p := range pods {
				if p.UID != event.InvolvedObject.UID {
					continue
				}
				if len(eventSeen) >= 512 {
					clear(eventSeen)
				}
				eventSeen[key] = struct{}{}
				at := event.LastTimestamp.Time
				if at.IsZero() {
					at = event.EventTime.Time
				}
				source := logstream.Source{Context: opts.Context, Cluster: opts.Cluster, Namespace: p.Namespace, Pod: p.Name, UID: string(p.UID)}
				marker(source, "event", "kubernetes-events", event.Reason+": "+event.Message, at, at.IsZero())
				return
			}
			retainEvent(event)
		}
		replayPending = func() {
			// Replay in Event arrival order and compact the bounded FIFO after joins.
			kept := pendingOrder[:0]
			for _, ref := range pendingOrder {
				event := pending[ref.uid][ref.key]
				if event == nil {
					continue
				}
				found := false
				for _, p := range pods {
					if string(p.UID) == ref.uid {
						found = true
						break
					}
				}
				if !found {
					kept = append(kept, ref)
					continue
				}
				processEvent(event)
				delete(pending[ref.uid], ref.key)
				if len(pending[ref.uid]) == 0 {
					delete(pending, ref.uid)
				}
			}
			pendingOrder = kept
		}

		eventUnavailable := func(err error) {
			marker(base, "events-unavailable", "kubernetes-events", err.Error(), time.Time{}, false)
			eventsDisabled = finite || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
		}
		watchEvents := func() {
			eventOptions := metav1.ListOptions{FieldSelector: "involvedObject.kind=Pod", Limit: 512}
			snapshot, err := k.CoreV1().Events(ns).List(ctx, eventOptions)
			if err != nil {
				eventUnavailable(err)
				return
			}
			limit := len(snapshot.Items)
			if limit > 512 {
				limit = 512
			}
			for i := range limit {
				processEvent(&snapshot.Items[i])
			}
			if snapshot.Continue != "" || len(snapshot.Items) > 512 {
				marker(base, "events-partial", "kubernetes-events", "Event history limited to 512 records; older correlation may be incomplete", time.Time{}, true)
			}
			if finite {
				eventsDisabled = true
				return
			}
			eventOptions.Limit = 0
			eventOptions.ResourceVersion = snapshot.ResourceVersion
			ew, err = k.CoreV1().Events(ns).Watch(ctx, eventOptions)
			if err != nil {
				eventUnavailable(err)
			} else {
				ec = ew.ResultChan()
			}
		}
		if !eventsDisabled {
			watchEvents()
		}
		retry := time.NewTicker(500 * time.Millisecond)
		defer retry.Stop()
		relist := time.NewTicker(15 * time.Second)
		defer relist.Stop()
		refresh := func() {
			list, err := k.CoreV1().Pods(ns).List(ctx, listOpts)
			if err != nil {
				marker(base, "error", "pod-list", err.Error(), time.Time{}, true)
				return
			}
			if pw != nil {
				pw.Stop()
			}
			ingestList(list)
			watchPods(list.ResourceVersion)
			reconcile()
		}
		for {
			if finite && active == 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case result := <-done:
				key := result.key
				if s := sources[key]; s != nil {
					s.outcome = result.outcome
					if result.outcome == sourceCompleted {
						marker(s.source, "source-complete", "collector", "source read completed", time.Time{}, true)
					}
					s.running = false
					active--
					if !s.wanted {
						delete(sources, key)
					}
				}
				if !finite {
					reconcile()
				}
			case e, ok := <-pc:
				if !ok || e.Type == watch.Error {
					if pw != nil {
						pw.Stop()
					}
					pc = nil
					marker(base, "watch-recovery", "pod-watch", "pod watch interrupted; relisting", time.Time{}, true)
					continue
				}
				p, ok := e.Object.(*v1.Pod)
				if !ok {
					continue
				}
				if e.Type == watch.Deleted || !matches(p) {
					delete(pods, p.Name)
				} else if _, exists := pods[p.Name]; exists || len(pods) < maxLogPods {
					pods[p.Name] = p.DeepCopy()
				} else {
					warnCap()
				}
				reconcile()
			case e, ok := <-ec:
				if !ok {
					ec = nil
					continue
				}
				if e.Type == watch.Error {
					ec = nil
					if ew != nil {
						ew.Stop()
					}
					err := apierrors.FromObject(e.Object)
					marker(base, "events-unavailable", "kubernetes-events", err.Error(), time.Time{}, false)
					eventsDisabled = apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
					continue
				}
				event, ok := e.Object.(*v1.Event)
				if !ok {
					continue
				}
				processEvent(event)
			case <-retry.C:
				if !finite && pc == nil {
					refresh()
				}
				if !eventsDisabled && ec == nil {
					watchEvents()
				}
			case <-relist.C:
				if !finite {
					refresh()
				}
			}
		}
	}()
	return out, nil
}

func podLogRevision(p *v1.Pod) string {
	for _, key := range []string{"pod-template-hash", "controller-revision-hash", "deployment.kubernetes.io/revision"} {
		if value := p.Labels[key]; value != "" {
			return key + "=" + value
		}
		if value := p.Annotations[key]; value != "" {
			return key + "=" + value
		}
	}
	return ""
}

func followLogSource(ctx context.Context, p *v1.Pod, o *LogOptions, terminal bool, open logOpener, out chan<- *LogItem) {
	state := &logSourceState{cursor: &logCursor{}}
	state.terminal.Store(terminal)
	runLogSource(ctx, p, o, state, open, out, waitLogRetry)
}

func waitLogRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Live sources remain eligible until canceled/denied/completed. Only finite reads
// have a consecutive failure budget. Delay and all retained state stay bounded.
func runLogSource(
	ctx context.Context,
	p *v1.Pod,
	o *LogOptions,
	state *logSourceState,
	open logOpener,
	out chan<- *LogItem,
	wait func(context.Context, time.Duration) bool,
) sourceOutcome {
	cursor := state.cursor
	failures := 0
	delay := logBackoffInitial
	for {
		if ctx.Err() != nil {
			return sourceCanceled
		}
		po := o.ToPodLogOptions()
		if !cursor.time.IsZero() {
			po.SinceTime = &metav1.Time{Time: cursor.time}
			po.SinceSeconds = nil
			po.TailLines = nil
		}
		if state.terminal.Load() {
			po.Follow = false
		}
		cursor.reconnect()
		before := cursor.time
		stream, err := open(ctx, p, o, po)
		if err == nil {
			result := readLogStream(ctx, stream, out, o, cursor)
			if result == streamCanceled {
				return sourceCanceled
			}
			if result == streamEOF && state.terminal.Load() {
				return sourceCompleted
			}
			if result == streamError {
				if failures < logRetryCount {
					failures++
				}
			} else {
				failures = 0
			}
		} else {
			if !sendLog(ctx, out, o.ToErrLogItem(fmt.Errorf("open logs: %w", err))) {
				return sourceCanceled
			}
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				return sourceDenied
			}
			if failures < logRetryCount {
				failures++
			}
		}
		if state.terminal.Load() && failures >= logRetryCount {
			sendLog(ctx, out, o.ToErrLogItem(fmt.Errorf("finite log read failed after %d consecutive failures", logRetryCount)))
			return sourceExhausted
		}
		if cursor.limited {
			item := o.ToErrLogItem(fmt.Errorf("timestamp boundary dedup limit exceeded; replay may contain duplicates"))
			item.Marker.Kind = "dedup-limit"
			item.Marker.Approximate = true
			if !sendLog(ctx, out, item) {
				return sourceCanceled
			}
			cursor.limited = false
		}
		progressed := cursor.time.After(before)
		if progressed {
			delay = logBackoffInitial
		}
		if !wait(ctx, delay) {
			return sourceCanceled
		}
		if !progressed {
			delay = min(delay*2, logBackoffMax)
		}
	}
}
