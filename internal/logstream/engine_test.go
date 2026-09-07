package logstream

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testLevelWarn  = "warn"
	testLevelError = "error"
)

func TestEngineGroupingIsolationBoundsAndFlush(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	a := Source{UID: "a", Container: "c"}
	b := Source{UID: "b", Container: "c"}
	en := NewEngine(Options{Capacity: 3, MaxGroupLines: 2, ReorderWindow: time.Millisecond})
	en.Add(a, now, "ERROR boom", now)
	en.Add(b, now, "other", now)
	en.Add(a, now, "  at frame()", now)
	en.Add(a, now, "  at overflow()", now)
	en.Flush()
	es := en.Snapshot()
	if len(es) != 3 {
		t.Fatalf("entries=%+v", es)
	}
	found := false
	for _, e := range es {
		if e.Raw == "ERROR boom\n  at frame()" {
			found = true
			if e.Occurrences != 2 || len(e.OriginalLines) != 2 {
				t.Fatalf("bad group %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("group missing")
	}
	en.Add(a, now, "tail", now)
	en.Tick(now.Add(time.Second))
	if len(en.Snapshot()) != 3 || en.Stats().Evicted == 0 {
		t.Fatalf("ring not bounded %+v", en.Stats())
	}
	old := en.Snapshot()
	old[0].OriginalLines[0] = "mutated"
	if en.Snapshot()[0].OriginalLines[0] == "mutated" {
		t.Fatal("snapshot aliased")
	}
}
func TestEngineOrderingCollapseAndHistogram(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	en := NewEngine(Options{Collapse: true, DisableMultiline: true, MaxReorder: 2, Capacity: 10})
	en.Add(Source{UID: "a"}, now.Add(2*time.Millisecond), `level=error msg=oops status=500`, now)
	en.Add(Source{UID: "b"}, now, `level=warn msg=first`, now)
	en.Add(Source{UID: "a"}, now.Add(3*time.Millisecond), `level=error msg=oops status=500`, now)
	en.Flush()
	es := en.Snapshot()
	if len(es) != 2 || es[0].Level != testLevelWarn || es[1].Occurrences != 2 {
		t.Fatalf("order/collapse %+v", es)
	}
	id := es[1].ID
	q, _ := CompileQuery("level >= error", nil)
	v := en.View(q, now)
	if v.Hidden != 1 || len(v.Entries) != 1 || v.Entries[0].ID != id {
		t.Fatalf("view %+v", v)
	}
	last := v.Histogram[59]
	if len(v.Histogram) != 60 || last.Observed != 3 || last.Visible != 2 || last.Severity != 5 || last.EntryID == 0 {
		t.Fatalf("hist %+v", last)
	}
	if en.Stats().ForcedOrder == 0 {
		t.Fatal("cap pressure not surfaced")
	}
	en.SetCollapse(false)
	en.Add(Source{UID: "a"}, now.Add(4*time.Millisecond), `level=error msg=oops status=500`, now)
	en.Flush()
	if len(en.Snapshot()) != 3 {
		t.Fatal("collapse toggle")
	}
}
func TestEngineSamplesAndMultilineToggle(t *testing.T) {
	now := time.Now()
	en := NewEngine(Options{MaxSources: 2, Capacity: 100, MaxGroupBytes: 32})
	s := Source{UID: "a"}
	for range 55 {
		en.Add(s, now, "raw", now)
	}
	en.Add(s, now, `{"level":"error"}`, now)
	en.Flush()
	samples := en.FormatSamples()[s.Key()]
	if samples.Total != 50 || samples.Raw != 50 {
		t.Fatalf("samples %+v", samples)
	}
	es := en.Snapshot()
	if es[len(es)-1].Level != testLevelError {
		t.Fatal("sampling disabled parser")
	}
	en.Add(s, now, "head", now)
	out := en.SetMultiline(false)
	if len(out) == 0 {
		t.Fatal("toggle failed to flush")
	}
	en.Add(s, now, strings.Repeat("x", 40), now)
	en.Flush()
	if en.Stats().Truncated == 0 {
		t.Fatal("oversized line not reported")
	}
	en.Add(Source{UID: "b"}, now, "b", now)
	en.Add(Source{UID: "c"}, now, "c", now)
	en.Flush()
	if len(en.FormatSamples()) > 2 || en.Stats().SourceEvictions == 0 {
		t.Fatal("source state unbounded")
	}
}
func TestPatternsPreserveDiagnosticNumbers(t *testing.T) {
	es := []Entry{Parse(Source{}, time.Time{}, "level=error status=500 peer=192.168.1.2 id=123e4567-e89b-12d3-a456-426614174000"), Parse(Source{}, time.Time{}, "level=error status=500 peer=10.0.0.1 id=223e4567-e89b-12d3-a456-426614174000"), Parse(Source{}, time.Time{}, "level=error status=404 peer=10.0.0.1 id=223e4567-e89b-12d3-a456-426614174000")}
	p := TopPatterns(es, 10)
	if len(p) != 2 || p[0].Count != 2 || len(p[0].Entries) != 2 || !strings.Contains(p[0].Template, "500") {
		t.Fatalf("patterns %+v", p)
	}
}
func TestStreamingPEMRedaction(t *testing.T) {
	en := NewEngine(Options{DisableMultiline: true})
	now := time.Now()
	s := Source{UID: "s"}
	for _, line := range []string{"-----BEGIN PRIVATE KEY-----", "secret-body", "-----END PRIVATE KEY-----", "normal"} {
		en.Add(s, now, line, now)
		en.Tick(now.Add(time.Second))
	}
	en.Flush()
	es := en.Snapshot()
	for _, e := range es[:3] {
		if strings.Contains(SafeEntry(e).Raw, "secret-body") || !e.Sensitive {
			t.Fatalf("sensitive leak %+v", e)
		}
	}
	if es[3].Sensitive {
		t.Fatal("PEM state never ended")
	}
}
func TestEngineConcurrentSnapshots(t *testing.T) {
	en := NewEngine(Options{DisableMultiline: true, Capacity: 20})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				en.Add(Source{}, time.Now(), "line", time.Now())
				en.Snapshot()
				en.View(nil, time.Now())
			}
		}()
	}
	wg.Wait()
	en.Flush()
	if len(en.Snapshot()) != 20 {
		t.Fatal("capacity")
	}
}
func TestReorderCommitIDsCanRecordAndCollapseBuckets(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	en := NewEngine(Options{DisableMultiline: true, Collapse: true, StartID: 40})
	en.Add(Source{}, now.Add(time.Second), "same", now)
	en.Add(Source{}, now, "same", now)
	out := en.Flush()
	if len(out) != 2 || out[0].ID != 41 || out[1].ID != 42 {
		t.Fatalf("IDs not monotonic after timestamp reorder: %+v", out)
	}
	view := en.View(nil, now.Add(time.Second))
	if view.Histogram[58].Observed != 1 || view.Histogram[59].Observed != 1 {
		t.Fatal("collapse moved occurrence to wrong bucket")
	}
	entry := view.Entries[0]
	if entry.ID != 41 || entry.LastID != 42 || entry.Repeats != 2 {
		t.Fatalf("collapsed provenance %+v", entry)
	}
}

func TestStreamingPEMNormalizedOrderedDelimiters(t *testing.T) {
	for _, header := range []string{
		"-----BEGIN \x1b[31mPRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----\nfirst-body\n-----END PRIVATE KEY-----\n-----BEGIN PRIVATE KEY-----",
	} {
		t.Run(header, func(t *testing.T) {
			en := NewEngine(Options{DisableMultiline: true})
			now := time.Now()
			source := Source{UID: "pem-source"}
			en.Add(source, now, header, now)
			en.Flush()
			en.Add(source, now.Add(time.Second), "private-body", now.Add(time.Second))
			en.Flush()
			entries := en.Snapshot()
			body := entries[len(entries)-1]
			if !body.Sensitive || strings.Contains(SafeEntry(body).Raw, "private-body") {
				t.Fatalf("continuation leaked after normalized/ordered header: %+v", body)
			}
			en.Add(source, now.Add(2*time.Second), "-----END PRIVATE KEY-----", now.Add(2*time.Second))
			en.Flush()
			en.Add(source, now.Add(3*time.Second), "ordinary", now.Add(3*time.Second))
			en.Flush()
			entries = en.Snapshot()
			if entries[len(entries)-1].Sensitive {
				t.Fatal("final END did not close source state")
			}
		})
	}
}

func TestStructuredPatternsUseMessageAndDiagnosticStatus(t *testing.T) {
	var entries []Entry
	for i, code := range []int{500, 500, 503} {
		e := Parse(Source{Pod: fmt.Sprint(i)}, time.Now(), fmt.Sprintf(`{"time":"2026-09-06T12:00:0%dZ","level":"error","message":"request failed","attempt":%d,"http":{"status":%d}}`, i, i, code))
		e.ID = uint64(i + 1)
		e.Occurrences = 1
		entries = append(entries, e)
	}
	patterns := TopPatterns(entries, 100)
	if len(patterns) != 2 || patterns[0].Count != 2 || !strings.Contains(patterns[0].Template, "500") {
		t.Fatalf("structured patterns fragmented: %+v", patterns)
	}
	for _, e := range patterns[0].Entries {
		if EntryPattern(e).Key != patterns[0].Key {
			t.Fatal("drilldown identity differs")
		}
	}
}

func TestClearRetainedPreservesIDsAndPrivateSourceState(t *testing.T) {
	engine := NewEngine(Options{Collapse: true})
	source := Source{Pod: "p"}
	now := time.Now()
	engine.Add(source, now, "-----BEGIN PRIVATE KEY-----", now)
	pending := engine.ClearRetained()
	if len(pending) != 1 || len(engine.Snapshot()) != 0 {
		t.Fatal("clear did not flush pending before emptying ring")
	}
	engine.Add(source, now, "PRIVATECONTINUATION", now)
	later := engine.Flush()
	if len(later) != 1 || later[0].ID <= pending[0].ID || !later[0].Sensitive || strings.Contains(SafeEntry(later[0]).Raw, "PRIVATECONTINUATION") {
		t.Fatal("clear lost monotonic identity or privacy provenance")
	}
}
