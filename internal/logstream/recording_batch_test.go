package logstream

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRecorderBatchCheckpointAndExport(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRecorder(dir, RecordingOptions{SegmentBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	var entries []Entry
	for i := 1; i <= 256; i++ {
		e := Parse(Source{Pod: "p"}, time.Now(), "Bearer secret")
		e.ID = uint64(i)
		entries = append(entries, e)
	}
	if err = r.AppendBatch(entries); err != nil {
		t.Fatal(err)
	}
	if r.Info().LastID != 256 {
		t.Fatal(r.Info())
	}
	var b bytes.Buffer
	if err = r.Export(&b, ExportContext{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "Bearer secret") || strings.Count(b.String(), " id=") != 256 {
		t.Fatal("unsafe or incomplete export")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = OpenRecorder(dir, RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Info().LastID != 256 {
		t.Fatal(r.Info())
	}
	entries[0].ID = 257
	entries[1].ID = 257
	if err = r.AppendBatch(entries[:2]); err == nil {
		t.Fatal("duplicate batch accepted")
	}
	if r.Info().LastID != 256 {
		t.Fatal("invalid batch partially committed")
	}
	if err = r.AppendBatch(entries[:1]); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderBatchInterruptedFailsClosed(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRecorder(dir, RecordingOptions{MaxRecordBytes: 600})
	if err != nil {
		t.Fatal(err)
	}
	a := Parse(Source{Pod: "p"}, time.Now(), "hello")
	a.ID = 1
	b := Parse(a.Source, time.Now(), strings.Repeat("x", 1000))
	b.ID = 2
	if err = r.AppendBatch([]Entry{a, b}); err == nil {
		t.Fatal("oversized record accepted")
	}
	if err = r.Append(a); err == nil {
		t.Fatal("interrupted writer accepted append")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = OpenRecorder(dir, RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a.ID = r.Info().LastID + 1
	a.Raw = "possible-key-continuation"
	a.Message = a.Raw
	if err = r.Append(a); err != nil {
		t.Fatal(err)
	}
	page, err := r.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page[len(page)-1].Raw, "possible-key-continuation") {
		t.Fatal("uncertain recovery did not fail closed")
	}
}

func TestRecorderBatchBound(t *testing.T) {
	r, err := OpenRecorder(t.TempDir(), RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.AppendBatch(make([]Entry, 257)); err == nil {
		t.Fatal("unbounded batch")
	}
}

func TestRecorderBatchByteBoundaryAndSourceOrder(t *testing.T) {
	r, err := OpenRecorder(t.TempDir(), RecordingOptions{Raw: true, MaxRecordBytes: 3 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	entries := []Entry{{ID: 1, Raw: strings.Repeat("x", 2<<20)}, {ID: 2, Raw: strings.Repeat("y", 2<<20)}}
	if err = r.AppendBatch(entries); err == nil || !strings.Contains(err.Error(), "4MiB") {
		t.Fatal("missing batch byte limit", err)
	}
	if r.Info().LastID != 1 {
		t.Fatal("partial batch checkpoint identity", r.Info())
	}
}
