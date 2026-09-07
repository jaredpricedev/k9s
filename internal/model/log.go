// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/color"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/slogs"
)

// LogsListener represents a log model listener.
type LogsListener interface {
	// LogChanged notifies the model changed.
	LogChanged([][]byte)

	// LogCleared indicates logs are cleared.
	LogCleared()

	// LogFailed indicates a log failure.
	LogFailed(error)

	// LogStop indicates logging was canceled.
	LogStop()

	// LogResume indicates logging has resumed.
	LogResume()

	// LogCanceled indicates no more logs will come.
	LogCanceled()
}

type entrySubscription struct {
	active   atomic.Bool
	callback func([]logstream.Entry)
}

// Log represents a resource logger.
type Log struct {
	entryListeners  []*entrySubscription
	profilePodAdded bool
	factory         dao.Factory
	lines           *dao.LogItems
	listeners       []LogsListener
	gvr             *client.GVR
	logOptions      *dao.LogOptions
	cancelFn        context.CancelFunc
	mx              sync.RWMutex
	filter          string
	lastSent        int
	flushTimeout    time.Duration
}

// NewLog returns a new model.
func NewLog(gvr *client.GVR, opts *dao.LogOptions, flushTimeout time.Duration) *Log {
	return &Log{
		gvr:          gvr,
		logOptions:   opts.Clone(),
		lines:        dao.NewLogItems(),
		flushTimeout: flushTimeout,
	}
}

func (l *Log) GVR() *client.GVR {
	return l.gvr
}

func (l *Log) LogOptions() *dao.LogOptions {
	return l.LogOptionsSnapshot()
}

// LogOptionsSnapshot returns detached settings and workload/pod profile metadata.
func (l *Log) LogOptionsSnapshot() *dao.LogOptions {
	l.mx.RLock()
	defer l.mx.RUnlock()
	return l.logOptions.Clone()
}

// SubscribeEntries delivers raw records independently of legacy display filters.
// Callbacks run outside model locks. An already running callback may finish after unsubscribe.
func (l *Log) SubscribeEntries(callback func([]logstream.Entry)) func() {
	sub := &entrySubscription{callback: callback}
	sub.active.Store(true)
	l.mx.Lock()
	l.entryListeners = append(l.entryListeners, sub)
	l.mx.Unlock()
	return func() {
		sub.active.Store(false)
		l.mx.Lock()
		defer l.mx.Unlock()
		for i, s := range l.entryListeners {
			if s == sub {
				l.entryListeners = append(l.entryListeners[:i], l.entryListeners[i+1:]...)
				break
			}
		}
	}
}

// SinceSeconds returns since seconds option.
func (l *Log) SinceSeconds() int64 {
	l.mx.RLock()
	defer l.mx.RUnlock()

	return l.logOptions.SinceSeconds
}

// IsHead returns log head option.
func (l *Log) IsHead() bool {
	l.mx.RLock()
	defer l.mx.RUnlock()

	return l.logOptions.Head
}

// ToggleShowTimestamp toggles to logs timestamps.
func (l *Log) ToggleShowTimestamp(b bool) {
	l.mx.Lock()
	l.logOptions.ShowTimestamp = b
	l.mx.Unlock()
	l.Refresh()
}

func (l *Log) Head(ctx context.Context) {
	l.mx.Lock()
	l.logOptions.Head = true
	l.mx.Unlock()
	l.Restart(ctx)
}

// SetSinceSeconds sets the logs retrieval time.
func (l *Log) SetSinceSeconds(ctx context.Context, i int64) {
	l.mx.Lock()
	l.logOptions.SinceSeconds, l.logOptions.Head = i, false
	l.mx.Unlock()
	l.Restart(ctx)
}

// Configure sets logger configuration.
//
//nolint:gocritic // The configuration value API is established and copied under the model lock.
func (l *Log) Configure(opts config.Logger) {
	l.mx.Lock()
	defer l.mx.Unlock()
	l.logOptions.Lines = opts.TailCount
	l.logOptions.SinceSeconds = opts.SinceSeconds
	l.logOptions.LogBufferSize = opts.LogBufferSize
}

// GetPath returns resource path.
func (l *Log) GetPath() string {
	l.mx.RLock()
	defer l.mx.RUnlock()
	return l.logOptions.Path
}

// GetContainer returns the resource container if any or "" otherwise.
func (l *Log) GetContainer() string {
	l.mx.RLock()
	defer l.mx.RUnlock()
	return l.logOptions.Container
}

// HasDefaultContainer returns true if the pod has a default container, false otherwise.
func (l *Log) HasDefaultContainer() bool {
	l.mx.RLock()
	defer l.mx.RUnlock()
	return l.logOptions.DefaultContainer != ""
}

// Init initializes the model.
func (l *Log) Init(f dao.Factory) {
	l.factory = f
}

// Clear the logs.
func (l *Log) Clear() {
	l.mx.Lock()
	l.lines.Clear()
	l.lastSent = 0
	l.mx.Unlock()

	l.fireLogCleared()
}

// Refresh refreshes the logs.
func (l *Log) Refresh() {
	l.fireLogCleared()
	l.fireLogBuffChanged()
}

// Restart restarts the logger.
func (l *Log) Restart(ctx context.Context) {
	l.Stop()
	l.Clear()
	l.fireLogResume()
	l.Start(ctx)
}

// Start starts logging.
func (l *Log) Start(ctx context.Context) {
	if err := l.load(ctx); err != nil {
		slog.Error("Tail logs failed!", slogs.Error, err)
		l.fireLogError(err)
	}
}

// Stop terminates logging.
func (l *Log) Stop() {
	l.cancel()
}

// Set sets the log lines (for testing only!)
func (l *Log) Set(lines *dao.LogItems) {
	l.mx.Lock()
	l.lines.Merge(lines)
	l.mx.Unlock()

	l.fireLogCleared()
	l.fireLogBuffChanged()
}

// ClearFilter resets the log filter if any.
func (l *Log) ClearFilter() {
	l.mx.Lock()
	l.filter = ""
	l.mx.Unlock()

	l.fireLogCleared()
	l.fireLogBuffChanged()
}

// Filter filters the model using either fuzzy or regexp.
func (l *Log) Filter(q string) {
	l.mx.Lock()
	l.filter = q
	l.mx.Unlock()

	l.fireLogCleared()
	l.fireLogBuffChanged()
}

func (l *Log) cancel() {
	l.mx.Lock()
	defer l.mx.Unlock()
	if l.cancelFn != nil {
		l.cancelFn()
		l.cancelFn = nil
	}
}

func (l *Log) load(ctx context.Context) error {
	accessor, err := dao.AccessorFor(l.factory, l.gvr)
	if err != nil {
		return err
	}
	loggable, ok := accessor.(dao.Loggable)
	if !ok {
		return fmt.Errorf("resource %s is not Loggable", l.gvr)
	}

	l.cancel()
	ctx = context.WithValue(ctx, internal.KeyFactory, l.factory)
	var stop context.CancelFunc
	ctx, stop = context.WithCancel(ctx)
	l.mx.Lock()
	l.cancelFn = stop
	opts := l.logOptions.Clone()
	l.mx.Unlock()
	cc, err := loggable.TailLogs(ctx, opts)
	l.mx.Lock()
	l.logOptions.WorkloadKind, l.logOptions.WorkloadName = opts.WorkloadKind, opts.WorkloadName
	l.logOptions.Labels, l.logOptions.Annotations = maps.Clone(opts.Labels), maps.Clone(opts.Annotations)
	l.profilePodAdded = false
	l.logOptions.SingleContainer, l.logOptions.DefaultContainer = opts.SingleContainer, opts.DefaultContainer
	l.mx.Unlock()
	if err != nil {
		slog.Error("Tail logs failed", slogs.Error, err)
		l.cancel()
		return err
	}
	for _, c := range cc {
		go l.updateLogs(ctx, c)
	}

	return nil
}

// Append adds a log line.
func (l *Log) Append(line *dao.LogItem) {
	if line == nil || line.IsEmpty() {
		return
	}
	l.mx.Lock()
	if !l.profilePodAdded && line.Source.Pod != "" && (len(line.Labels) > 0 || len(line.Annotations) > 0) {
		l.profilePodAdded = true
		if l.logOptions.Labels == nil {
			l.logOptions.Labels = make(map[string]string)
		}
		if l.logOptions.Annotations == nil {
			l.logOptions.Annotations = make(map[string]string)
		}
		for k, v := range line.Labels {
			if _, ok := l.logOptions.Labels[k]; !ok {
				l.logOptions.Labels[k] = v
			}
		}
		for k, v := range line.Annotations {
			if _, ok := l.logOptions.Annotations[k]; !ok {
				l.logOptions.Annotations[k] = v
			}
		}
	}
	capacity := int(l.logOptions.Lines)
	if capacity <= 0 {
		capacity = 10000
	}
	if capacity > 100000 {
		capacity = 100000
	}
	if l.lines.Len() < capacity {
		l.lines.Add(line)
	} else {
		l.lines.Shift(line)
		if l.lastSent > 0 {
			l.lastSent--
		}
	}
	subscribers := append([]*entrySubscription(nil), l.entryListeners...)
	l.mx.Unlock()
	for _, sub := range subscribers {
		if sub.active.Load() {
			sub.callback([]logstream.Entry{line.Entry()})
		}
	}
}

// Notify snapshots state before calling listeners, allowing reentrant UI actions.
func (l *Log) Notify() {
	l.mx.Lock()
	var lines [][]byte
	var err error
	if l.lastSent < l.lines.Len() {
		lines, err = l.logBuffer(l.lastSent)
		l.lastSent = l.lines.Len()
	}
	l.mx.Unlock()
	if err != nil {
		l.fireLogError(err)
	} else if len(lines) > 0 {
		l.fireLogChanged(lines)
	}
}

// ToggleAllContainers toggles to show all containers logs.
func (l *Log) ToggleAllContainers(ctx context.Context) {
	l.mx.Lock()
	l.logOptions.ToggleAllContainers()
	l.mx.Unlock()
	l.Restart(ctx)
}

func (l *Log) updateLogs(ctx context.Context, c dao.LogChan) {
	duration := l.flushTimeout
	if duration <= 0 {
		duration = 100 * time.Millisecond
	}
	ticker := time.NewTicker(duration)
	defer ticker.Stop()
	for {
		select {
		case item, ok := <-c:
			if ctx.Err() != nil {
				return
			}
			if !ok {
				l.Notify()
				l.fireCanceled()
				return
			}
			if item == dao.ItemEOF {
				l.Notify()
				l.fireCanceled()
				return
			}
			l.Append(item)
			var overflow bool
			l.mx.RLock()
			overflow = int64(l.lines.Len()-l.lastSent) > l.logOptions.Lines
			l.mx.RUnlock()
			if overflow {
				l.Notify()
			}
		case <-ticker.C:
			l.Notify()
		case <-ctx.Done():
			return
		}
	}
}

// AddListener adds a new model listener.
func (l *Log) AddListener(listener LogsListener) {
	l.mx.Lock()
	defer l.mx.Unlock()

	l.listeners = append(l.listeners, listener)
}

// RemoveListener delete a listener from the list.
func (l *Log) RemoveListener(listener LogsListener) {
	l.mx.Lock()
	defer l.mx.Unlock()

	victim := -1
	for i, lis := range l.listeners {
		if lis == listener {
			victim = i
			break
		}
	}

	if victim >= 0 {
		l.listeners = append(l.listeners[:victim], l.listeners[victim+1:]...)
	}
}

func (l *Log) applyFilter(index int, q string) ([][]byte, error) {
	if q == "" {
		return nil, nil
	}
	matches, indices, err := l.lines.Filter(index, q, l.logOptions.ShowTimestamp)
	if err != nil {
		return nil, err
	}

	// No filter!
	if matches == nil {
		ll := make([][]byte, l.lines.Len())
		l.lines.Render(index, l.logOptions.ShowTimestamp, ll)
		return ll, nil
	}
	// Blank filter
	if len(matches) == 0 {
		return nil, nil
	}
	filtered := make([][]byte, 0, len(matches))
	ll := make([][]byte, l.lines.Len())
	l.lines.Lines(index, l.logOptions.ShowTimestamp, ll)
	for i, idx := range matches {
		filtered = append(filtered, color.Highlight(ll[idx], indices[i], 209))
	}

	return filtered, nil
}

// logBuffer requires the model lock.
func (l *Log) logBuffer(index int) ([][]byte, error) {
	if index > l.lines.Len() {
		index = l.lines.Len()
	}
	if l.filter != "" {
		return l.applyFilter(index, l.filter)
	}
	lines := make([][]byte, l.lines.Len()-index)
	l.lines.Render(index, l.logOptions.ShowTimestamp, lines)
	return lines, nil
}
func (l *Log) fireLogBuffChanged() {
	l.mx.Lock()
	lines, err := l.logBuffer(0)
	l.mx.Unlock()
	if err != nil {
		l.fireLogError(err)
	} else if len(lines) > 0 {
		l.fireLogChanged(lines)
	}
}
func (l *Log) listenerSnapshot() []LogsListener {
	l.mx.RLock()
	defer l.mx.RUnlock()
	return append([]LogsListener(nil), l.listeners...)
}

func (l *Log) fireLogResume() {
	for _, lis := range l.listenerSnapshot() {
		lis.LogResume()
	}
}

func (l *Log) fireCanceled() {
	for _, lis := range l.listenerSnapshot() {
		lis.LogCanceled()
	}
}

func (l *Log) fireLogError(err error) {
	for _, lis := range l.listenerSnapshot() {
		lis.LogFailed(err)
	}
}

func (l *Log) fireLogChanged(lines [][]byte) {
	for _, lis := range l.listenerSnapshot() {
		lis.LogChanged(lines)
	}
}

func (l *Log) fireLogCleared() {
	for _, lis := range l.listenerSnapshot() {
		lis.LogCleared()
	}
}

// ConfigureSource sets provenance before Start. It does not restart an active session.
func (l *Log) ConfigureSource(contextName, clusterName string, events bool) {
	l.mx.Lock()
	defer l.mx.Unlock()
	l.logOptions.Context, l.logOptions.Cluster, l.logOptions.Events = contextName, clusterName, events
}

// SetSinceTime restarts at an inclusive runtime timestamp. Previous remains unchanged.
func (l *Log) SetSinceTime(ctx context.Context, value string) error {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fmt.Errorf("invalid runtime timestamp: %w", err)
	}
	l.mx.Lock()
	l.logOptions.SinceTime = parsed.Format(time.RFC3339Nano)
	l.logOptions.SinceSeconds = 0
	l.logOptions.Head = false
	l.mx.Unlock()
	l.Restart(ctx)
	return nil
}
