package logstream

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type Options struct {
	StartID                                                        uint64
	Capacity, MaxSources, MaxGroupLines, MaxGroupBytes, MaxReorder int
	GroupIdle, ReorderWindow                                       time.Duration
	Collapse, DisableMultiline                                     bool
}
type Stats struct {
	Observed, Evicted, Truncated, ForcedOrder, SourceEvictions, HistogramDropped uint64
	Retained                                                                     int
}
type FormatSample struct{ Total, JSON, Logfmt, Raw int }
type TimeCount struct {
	Time  time.Time
	Count uint64
}
type sourceState struct {
	pending *Entry
	touched time.Time
	sample  FormatSample
}
type queued struct {
	entry    Entry
	observed time.Time
}
type Engine struct {
	mu              sync.Mutex
	opts            Options
	sources         map[string]*sourceState
	ring            []Entry
	start, size     int
	queue           []queued
	nextID, orderID uint64
	stats           Stats
	redactor        *Redactor
}

func defaults(o Options) Options {
	if o.Capacity <= 0 {
		o.Capacity = 10000
	}
	if o.MaxSources <= 0 {
		o.MaxSources = 1024
	}
	if o.MaxGroupLines <= 0 {
		o.MaxGroupLines = 64
	}
	if o.MaxGroupBytes <= 0 {
		o.MaxGroupBytes = 64 << 10
	}
	if o.MaxReorder <= 0 {
		o.MaxReorder = 2048
	}
	if o.GroupIdle <= 0 {
		o.GroupIdle = 500 * time.Millisecond
	}
	if o.ReorderWindow <= 0 {
		o.ReorderWindow = 250 * time.Millisecond
	}
	return o
}
func NewEngine(o Options) *Engine {
	o = defaults(o)
	return &Engine{opts: o, sources: map[string]*sourceState{}, ring: make([]Entry, o.Capacity), redactor: NewRedactor(o.MaxSources), nextID: o.StartID}
}

//nolint:gocritic // Public value API isolates caller-owned source state.
func (e *Engine) Add(s Source, runtime time.Time, raw string, observed time.Time) []Entry {
	// Bound the parser input as well as retained groups.
	truncated := false
	if len(raw) > e.opts.MaxGroupBytes {
		raw = raw[:e.opts.MaxGroupBytes]
		truncated = true
	}
	entry := Parse(s, runtime, raw)
	entry.Truncated = truncated
	return e.AddEntry(entry, observed)
}

//nolint:gocritic // Public value API isolates caller-owned entry state.
func (e *Engine) AddEntry(entry Entry, observed time.Time) []Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.add(entry, observed)
}

//nolint:gocritic // The entry is cloned and normalized locally before retention.
func (e *Engine) add(entry Entry, now time.Time) []Entry {
	if now.IsZero() {
		now = time.Now()
	}
	entry = cloneEntry(entry)
	entry.ID = 0
	if len(entry.Raw) > e.opts.MaxGroupBytes {
		entry.Raw = entry.Raw[:e.opts.MaxGroupBytes]
		p := Parse(entry.Source, entry.RuntimeTime, entry.Raw)
		p.Marker = entry.Marker
		p.Sensitive = entry.Sensitive
		entry = p
		entry.Truncated = true
	}
	if entry.Repeats == 0 {
		entry.Repeats = 1
	}
	if entry.Occurrences == 0 {
		entry.Occurrences = 1
	}
	if len(entry.OriginalLines) == 0 {
		entry.OriginalLines = []string{entry.Raw}
	}
	if len(entry.OriginalLines) > e.opts.MaxGroupLines {
		entry.OriginalLines = entry.OriginalLines[:e.opts.MaxGroupLines]
		entry.Truncated = true
	}
	// Public AddEntry normally receives Parse output; arbitrary fields must not bypass the raw byte cap.
	if entry.LastRuntimeTime.IsZero() {
		entry.LastRuntimeTime = entry.RuntimeTime
	}
	if len(entry.Timeline) == 0 {
		entry.Timeline = []TimeCount{{entry.RuntimeTime.Truncate(time.Second), entry.Occurrences}}
	}
	entry = e.redactor.Track(entry)
	if entry.Truncated {
		e.stats.Truncated++
	}
	if entry.Marker == nil {
		e.stats.Observed += entry.Occurrences
	}
	key := entry.Source.Key()
	state := e.sources[key]
	if state == nil {
		if len(e.sources) >= e.opts.MaxSources {
			var oldest string
			var when time.Time
			for k, s := range e.sources {
				if oldest == "" || s.touched.Before(when) {
					oldest = k
					when = s.touched
				}
			}
			e.flushSource(e.sources[oldest], now)
			delete(e.sources, oldest)
			e.stats.SourceEvictions++
		}
		state = &sourceState{}
		e.sources[key] = state
	}
	if state.sample.Total < 50 && entry.Marker == nil {
		state.sample.Total++
		switch entry.Format {
		case "json":
			state.sample.JSON++
		case "logfmt":
			state.sample.Logfmt++
		default:
			state.sample.Raw++
		}
	}
	if state.pending != nil && now.Sub(state.touched) >= e.opts.GroupIdle {
		e.flushSource(state, now)
	}
	state.touched = now
	if entry.Marker != nil || e.opts.DisableMultiline {
		e.flushSource(state, now)
		e.enqueue(entry, now)
	} else if state.pending != nil &&
		continuation(entry, *state.pending) &&
		len(state.pending.OriginalLines)+len(entry.OriginalLines) <= e.opts.MaxGroupLines &&
		len(state.pending.Raw)+1+len(entry.Raw) <= e.opts.MaxGroupBytes {
		p := state.pending
		p.Raw += "\n" + entry.Raw
		p.OriginalLines = append(p.OriginalLines, entry.OriginalLines...)
		p.Occurrences += entry.Occurrences
		p.LastRuntimeTime = entry.RuntimeTime
		p.Truncated = p.Truncated || entry.Truncated
		p.Sensitive = p.Sensitive || entry.Sensitive
		p.Timeline = mergeTimeline(p.Timeline, entry.Timeline, &e.stats.HistogramDropped)
	} else {
		e.flushSource(state, now)
		state.pending = &entry
	}
	return e.drain(now, false)
}

//nolint:gocritic // Value parameters keep this predicate side-effect free at its call site.
func continuation(e, p Entry) bool {
	if e.Marker != nil || p.Marker != nil {
		return false
	}
	if e.Sensitive && p.Sensitive {
		return true
	}
	if e.Format != "raw" {
		return false
	}
	return strings.HasPrefix(e.Raw, " ") ||
		strings.HasPrefix(e.Raw, "\t") ||
		strings.HasPrefix(e.Raw, "at ") ||
		strings.HasPrefix(e.Raw, "Caused by:") ||
		strings.HasPrefix(e.Raw, "goroutine ") ||
		strings.HasPrefix(e.Raw, "... ")
}
func (e *Engine) flushSource(s *sourceState, now time.Time) {
	if s != nil && s.pending != nil {
		e.enqueue(*s.pending, now)
		s.pending = nil
	}
}

//nolint:gocritic // enqueue takes ownership of a normalized value before assigning IDs.
func (e *Engine) enqueue(entry Entry, now time.Time) {
	e.orderID++
	entry.ID = e.orderID
	entry.LastID = entry.ID
	e.queue = append(e.queue, queued{entry, now})
}
func (e *Engine) drain(now time.Time, all bool) []Entry {
	sort.SliceStable(e.queue, func(i, j int) bool {
		a, b := e.queue[i].entry, e.queue[j].entry
		if a.RuntimeTime.Equal(b.RuntimeTime) {
			return a.ID < b.ID
		}
		return a.RuntimeTime.Before(b.RuntimeTime)
	})
	var out []Entry
	for len(e.queue) > 0 {
		forced := len(e.queue) > e.opts.MaxReorder
		due := all || forced
		if !due {
			for i := range e.queue {
				q := &e.queue[i]
				if now.Sub(q.observed) >= e.opts.ReorderWindow {
					due = true
					break
				}
			}
		}
		if !due {
			break
		}
		if forced {
			e.stats.ForcedOrder++
		}
		entry := e.queue[0].entry
		copy(e.queue, e.queue[1:])
		e.queue[len(e.queue)-1] = queued{}
		e.queue = e.queue[:len(e.queue)-1]
		e.nextID++
		entry.ID = e.nextID
		entry.LastID = entry.ID
		e.commit(entry)
		out = append(out, cloneEntry(entry))
	}
	return out
}

//nolint:gocritic // commit takes ownership of the normalized value before ring retention.
func (e *Engine) commit(entry Entry) {
	if e.opts.Collapse && e.size > 0 {
		idx := (e.start + e.size - 1) % len(e.ring)
		last := &e.ring[idx]
		if last.Marker == nil && entry.Marker == nil && last.Source == entry.Source && last.Raw == entry.Raw && last.Sensitive == entry.Sensitive {
			last.Occurrences += entry.Occurrences
			last.Repeats += entry.Repeats
			last.LastID = entry.LastID
			last.LastRuntimeTime = entry.LastRuntimeTime
			last.Truncated = last.Truncated || entry.Truncated
			last.Timeline = mergeTimeline(last.Timeline, entry.Timeline, &e.stats.HistogramDropped)
			return
		}
	}
	if e.size == len(e.ring) {
		e.stats.Evicted += e.ring[e.start].Occurrences
		e.ring[e.start] = entry
		e.start = (e.start + 1) % len(e.ring)
	} else {
		e.ring[(e.start+e.size)%len(e.ring)] = entry
		e.size++
	}
}
func (e *Engine) Tick(now time.Time) []Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.sources {
		if s.pending != nil && now.Sub(s.touched) >= e.opts.GroupIdle {
			e.flushSource(s, s.touched)
		}
	}
	return e.drain(now, false)
}
func (e *Engine) Flush() []Entry { e.mu.Lock(); defer e.mu.Unlock(); return e.flush() }
func (e *Engine) flush() []Entry {
	now := time.Now()
	keys := make([]string, 0, len(e.sources))
	for k := range e.sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e.flushSource(e.sources[k], now)
	}
	return e.drain(now, true)
}

// ClearRetained flushes pending source groups/reordering for recording, then
// clears only the render ring. IDs, parser samples, source identity and private-
// key provenance remain intact. Future identical records start a fresh collapse.
func (e *Engine) ClearRetained() []Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	committed := e.flush()
	clear(e.ring)
	e.start, e.size = 0, 0
	return committed
}
func (e *Engine) SetCollapse(on bool) { e.mu.Lock(); defer e.mu.Unlock(); e.opts.Collapse = on }
func (e *Engine) SetMultiline(on bool) []Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.flush()
	e.opts.DisableMultiline = !on
	return out
}
func (e *Engine) Snapshot() []Entry { e.mu.Lock(); defer e.mu.Unlock(); return e.snapshot() }
func (e *Engine) snapshot() []Entry {
	out := make([]Entry, e.size)
	for i := range out {
		out[i] = cloneEntry(e.ring[(e.start+i)%len(e.ring)])
	}
	return out
}
func (e *Engine) Get(id uint64) (Entry, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.size {
		v := e.ring[(e.start+i)%len(e.ring)]
		if v.ID == id {
			return cloneEntry(v), true
		}
	}
	return Entry{}, false
}
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.stats
	s.Retained = e.size
	return s
}
func (e *Engine) FormatSamples() map[string]FormatSample {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]FormatSample, len(e.sources))
	for k, s := range e.sources {
		out[k] = s.sample
	}
	return out
}
func mergeTimeline(a, b []TimeCount, dropped *uint64) []TimeCount {
	out := append(append([]TimeCount(nil), a...), b...)
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	n := 0
	for _, tc := range out {
		if n > 0 && out[n-1].Time.Equal(tc.Time) {
			out[n-1].Count += tc.Count
		} else {
			out[n] = tc
			n++
		}
	}
	out = out[:n]
	if n > 60 {
		for _, tc := range out[:n-60] {
			*dropped += tc.Count
		}
		out = out[n-60:]
	}
	return out
}
