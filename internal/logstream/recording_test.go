package logstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRollbackWriteReportsCleanupFailures(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "rollback-*")
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	err = rollbackWrite(f, 0, io.ErrShortWrite)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("rollback error lost original write failure: %v", err)
	}
	if !strings.Contains(err.Error(), "truncate partial record") || !strings.Contains(err.Error(), "seek after partial record") {
		t.Fatalf("rollback failures were not reported: %v", err)
	}
}

func TestRecordingSafeResumePagingAndSearch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{MaxIndex: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 5; i++ {
		e := Parse(Source{Context: "cluster", Namespace: "ns", Pod: "p", Container: "c"}, time.Now(), fmt.Sprintf(`level=error status=500 msg="Bearer secret%d"`, i))
		e.ID = i
		if err = r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	info := r.Info()
	if info.Indexed > 2 || info.LastID != 5 {
		t.Fatalf("info %+v", info)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0700 {
		t.Fatal("directory permissions")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		st, _ = os.Stat(f)
		if st.Mode().Perm() != 0600 {
			t.Fatal("file permissions")
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "secret") {
			t.Fatal("disk secret")
		}
	}
	f, err := os.OpenFile(files[len(files)-1], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"ID":6`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err = OpenRecorder(dir, RecordingOptions{MaxIndex: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Info().SkippedRecords == 0 {
		t.Fatal("trailing corruption invisible")
	}
	page, err := r.Page(4, 2)
	if err != nil || len(page) != 2 || page[0].ID != 2 || page[1].ID != 3 {
		t.Fatalf("page %+v %v", page, err)
	}
	q, _ := CompileQuery("level=error status >= 500", nil)
	hits, err := r.Search(q, 10)
	if err != nil || len(hits) != 5 {
		t.Fatalf("search %d %v", len(hits), err)
	}
	t.Setenv("PATH", t.TempDir())
	hits, err = r.SearchRaw("REDACTED", 10)
	if err != nil || len(hits) != 5 {
		t.Fatalf("fallback search %d %v", len(hits), err)
	}
	e := Parse(Source{}, time.Now(), "new")
	e.ID = 5
	if err = r.Append(e); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	e.ID = 6
	if err = r.Append(e); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err = Export(&b, hits, ExportContext{Filters: []string{"status>=500"}, Loss: "2 lines lost"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "cluster") || !strings.Contains(b.String(), "2 lines lost") || strings.Contains(b.String(), "secret") {
		t.Fatal("export context/safety")
	}
}
func TestRecordingBoundsRawAndCorruptRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{MaxBytes: 2500, SegmentBytes: 1000, MaxRecordBytes: 1600})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 12; i++ {
		e := Parse(Source{}, time.Now(), "hello")
		e.ID = i
		if err = r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if r.Info().Bytes > 2500 || r.Info().EvictedSegments == 0 {
		t.Fatalf("unbounded %+v", r.Info())
	}
	e := Parse(Source{}, time.Now(), strings.Repeat("large", 1000))
	e.ID = 13
	if err = r.Append(e); err == nil {
		t.Fatal("oversized accepted")
	}
	r.Close()
	rawdir := filepath.Join(t.TempDir(), "raw")
	raw, err := OpenRecorder(rawdir, RecordingOptions{Raw: true})
	if err != nil {
		t.Fatal(err)
	}
	e = Parse(Source{}, time.Now(), "Bearer rawsecret")
	e.ID = 1
	if err = raw.Append(e); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err = OpenRecorder(rawdir, RecordingOptions{}); err == nil {
		t.Fatal("implicitly resumed raw recording")
	}
	raw, err = OpenRecorder(rawdir, RecordingOptions{Raw: true})
	if err != nil {
		t.Fatal(err)
	}
	if !raw.Info().Raw {
		t.Fatal("missing raw metadata")
	}
	raw.Close()
}
func TestRecorderMultilineSecretAndRetention(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range []string{"-----BEGIN PRIVATE KEY-----", "middle-secret", "-----END PRIVATE KEY-----"} {
		e := Parse(Source{UID: "a"}, time.Now(), line)
		e.ID = uint64(i + 1)
		if err = r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		data, _ := os.ReadFile(f)
		if strings.Contains(string(data), "middle-secret") {
			t.Fatal("streaming PEM leaked")
		}
		old := time.Now().Add(-48 * time.Hour)
		if err = os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	r, err = OpenRecorder(dir, RecordingOptions{Retention: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	page, _ := r.Page(0, 10)
	if len(page) != 0 || r.Info().EvictedSegments == 0 {
		t.Fatalf("retention %+v", r.Info())
	}
}
func TestProfileScopeRulesAndAtomicPersistence(t *testing.T) {
	scope := ProfileScope("ctx", "ns", map[string]string{"app": "web"}, nil, "Deployment", "web")
	if scope.LabelKey != "app" || scope.LabelValue != "web" {
		t.Fatalf("scope %+v", scope)
	}
	other := ProfileScope("ctx", "other", map[string]string{"app": "web"}, nil, "Deployment", "web")
	rules, err := TeamRules(`["health","probe"]`)
	if err != nil {
		t.Fatal(err)
	}
	rules, err = MergeRules(rules, []string{"health", "noisy"})
	if err != nil || len(rules) != 3 {
		t.Fatal("merge")
	}
	for _, a := range []string{`"x"`, `[1]`, `[""]`, `["a"] garbage`} {
		if _, parseErr := TeamRules(a); parseErr == nil {
			t.Fatalf("accepted %s", a)
		}
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err = SaveProfile(path, scope, rules); err != nil {
		t.Fatal(err)
	}
	if err = SaveProfile(path, other, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(path, scope)
	if err != nil || len(got) != 3 {
		t.Fatalf("load %+v %v", got, err)
	}
	if err = SaveProfile(path, scope, nil); err != nil {
		t.Fatal(err)
	}
	got, err = LoadProfile(path, scope)
	if err != nil || len(got) != 0 {
		t.Fatal("clear")
	}
	got, _ = LoadProfile(path, other)
	if len(got) != 1 {
		t.Fatal("clear clobbered unrelated scope")
	}
}
func TestResumeMalformedAndOversizedAndMissingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{MaxRecordBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	e := Parse(Source{}, time.Now(), "good")
	e.ID = 1
	if err = r.Append(e); err != nil {
		t.Fatal(err)
	}
	r.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0600)
	if _, err = f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(strings.Repeat("x", 4096) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err = OpenRecorder(dir, RecordingOptions{MaxRecordBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Info().SkippedRecords != 2 {
		t.Fatalf("corruption count %+v", r.Info())
	}
	p, err := r.Page(0, 10)
	if err != nil || len(p) != 1 {
		t.Fatalf("corrupt paging %+v %v", p, err)
	}
	os.Remove(files[0])
	if _, err = r.Search(nil, 10); err == nil {
		t.Fatal("disk disappearance hidden")
	}
}
func TestResumePrivateKeyState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := Parse(Source{UID: "a"}, time.Now(), "-----BEGIN PRIVATE KEY-----")
	e.ID = 1
	if err = r.Append(e); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r, err = OpenRecorder(dir, RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	e = Parse(Source{UID: "a"}, time.Now(), "resume-secret")
	e.ID = 2
	if err = r.Append(e); err != nil {
		t.Fatal(err)
	}
	p, _ := r.Page(0, 10)
	if strings.Contains(p[1].Raw, "resume-secret") {
		t.Fatal("resume lost secret state")
	}
}
func TestRecordingTotalCapIncludesMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{MaxBytes: 2400, SegmentBytes: 1100})
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 10; i++ {
		e := Parse(Source{}, time.Now(), "message")
		e.ID = i
		if err = r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := os.ReadDir(dir)
	var total int64
	for _, file := range files {
		st, _ := file.Info()
		total += st.Size()
	}
	if total > 2400 || r.Info().Bytes != total {
		t.Fatalf("total cap/accounting: disk=%d info=%+v", total, r.Info())
	}
	r.Close()
}
func TestRecorderExportsWholeRetainedSessionSafely(t *testing.T) {
	r, err := OpenRecorder(filepath.Join(t.TempDir(), "record"), RecordingOptions{Raw: true, MaxIndex: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := uint64(1); i <= 205; i++ {
		e := Parse(Source{Cluster: "cluster"}, time.Now(), fmt.Sprintf("entry-%d Bearer secret", i))
		e.ID = i
		if err = r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if err = r.Export(&b, ExportContext{Loss: "observed only"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), " id=") != 205 || strings.Count(b.String(), "# k9+") != 1 || strings.Contains(b.String(), "Bearer secret") {
		t.Fatalf("incomplete or unsafe disk export (%d bytes)", b.Len())
	}
	if err = r.Export(failingWriter{}, ExportContext{}); err == nil {
		t.Fatal("export write error hidden")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("disk full") }

func TestResumeStaleMetadataFailsClosedBeforeRetention(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "record")
			r, err := OpenRecorder(dir, RecordingOptions{})
			if err != nil {
				t.Fatal(err)
			}
			previous, err := os.ReadFile(filepath.Join(dir, "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			header := Parse(Source{UID: "pem-source"}, time.Now(), "-----BEGIN PRIVATE KEY-----")
			header.ID = 1
			if err = r.Append(header); err != nil {
				t.Fatal(err)
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			// Use legacy metadata with no writer/pending flags to exercise replay
			// detection independently of the new write-ahead checkpoint protocol.
			var legacy map[string]any
			if err = json.Unmarshal(previous, &legacy); err != nil {
				t.Fatal(err)
			}
			delete(legacy, "Pending")
			delete(legacy, "WriterOpen")
			previous, err = json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			// Recover the crash window: complete header record, previous metadata checkpoint.
			if err = os.WriteFile(filepath.Join(dir, "session.json"), previous, 0600); err != nil {
				t.Fatal(err)
			}
			if expire {
				files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
				old := time.Now().Add(-48 * time.Hour)
				for _, f := range files {
					if err = os.Chtimes(f, old, old); err != nil {
						t.Fatal(err)
					}
				}
			}
			r, err = OpenRecorder(dir, RecordingOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			body := Parse(header.Source, time.Now(), "private-body")
			body.ID = 2
			if err = r.Append(body); err != nil {
				t.Fatal(err)
			}
			entries, err := r.Page(0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(entries[len(entries)-1].Raw, "private-body") {
				t.Fatal("stale metadata exposed continuation")
			}
		})
	}
}
func TestFailedAppendLeavesRecoverablePendingState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "record")
	r, err := OpenRecorder(dir, RecordingOptions{MaxRecordBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	header := Parse(Source{UID: "pem-source"}, time.Now(), "-----BEGIN PRIVATE KEY-----")
	header.ID = 1
	if err = r.Append(header); err == nil {
		t.Fatal("expected oversized append error")
	}
	// Open without Close: errors must leave a durable conservative checkpoint.
	resumed, err := OpenRecorder(dir, RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	body := Parse(header.Source, time.Now(), "private-body")
	body.ID = 2
	if err = resumed.Append(body); err != nil {
		t.Fatal(err)
	}
	entries, _ := resumed.Page(0, 10)
	if strings.Contains(entries[len(entries)-1].Raw, "private-body") {
		t.Fatal("failed append lost continuation checkpoint")
	}
}
