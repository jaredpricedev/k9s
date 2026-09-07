package logstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type RecordingOptions struct {
	MaxBytes, SegmentBytes   int64
	Retention                time.Duration
	MaxIndex, MaxRecordBytes int
	Raw                      bool
}
type RecordingInfo struct {
	Raw                             bool
	ConservativeRedaction           bool
	Bytes                           int64
	LastID                          uint64
	Indexed                         int
	EvictedSegments, SkippedRecords uint64
	IndexLimited                    bool
	Segments                        int
}
type segment struct {
	path     string
	size     int64
	modified time.Time
}
type recordRef struct {
	id     uint64
	path   string
	offset int64
}
type recordingMeta struct {
	Version             int
	Raw                 bool
	LastID              uint64
	OpenPrivateSources  []string
	RedactionClosed     bool
	Pending, WriterOpen bool
}
type Recorder struct {
	mu                  sync.Mutex
	dir                 string
	opts                RecordingOptions
	info                RecordingInfo
	segments            []segment
	index               []recordRef
	file                *os.File
	seq                 uint64
	metaBytes           int64
	closed              bool
	pending, writerOpen bool
	redactor            *Redactor
}

func recordingDefaults(o RecordingOptions) RecordingOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 128 << 20
	}
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = 8 << 20
	}
	if o.SegmentBytes > o.MaxBytes {
		o.SegmentBytes = o.MaxBytes
	}
	if o.Retention <= 0 {
		o.Retention = 24 * time.Hour
	}
	if o.MaxIndex <= 0 {
		o.MaxIndex = 100000
	}
	if o.MaxRecordBytes <= 0 {
		o.MaxRecordBytes = 1 << 20
	}
	return o
}
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recording path must be a real directory")
	}
	return os.Chmod(dir, 0700)
}

//nolint:funlen // Recovery validation and repair are intentionally kept in durable-operation order.
func OpenRecorder(dir string, opts RecordingOptions) (*Recorder, error) {
	opts = recordingDefaults(opts)
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	r := &Recorder{dir: dir, opts: opts, redactor: NewRedactor(1024)}
	r.info.Raw = opts.Raw
	metaPath := filepath.Join(dir, "session.json")
	if st, statErr := os.Lstat(metaPath); statErr == nil {
		if !st.Mode().IsRegular() || st.Size() > 1<<20 {
			return nil, fmt.Errorf("invalid recording metadata")
		}
		data, readErr := os.ReadFile(metaPath)
		if readErr != nil {
			return nil, readErr
		}
		var meta recordingMeta
		if unmarshalErr := json.Unmarshal(data, &meta); unmarshalErr != nil {
			return nil, fmt.Errorf("read recording metadata: %w", unmarshalErr)
		}
		if meta.Version != 1 {
			return nil, fmt.Errorf("unsupported recording version %d", meta.Version)
		}
		if meta.Raw != opts.Raw {
			return nil, fmt.Errorf("recording raw mode differs: resume requires explicit Raw=%t", meta.Raw)
		}
		r.metaBytes = int64(len(data))
		r.info.Bytes = r.metaBytes
		r.info.LastID = meta.LastID
		r.redactor.closed = meta.RedactionClosed || meta.Pending || meta.WriterOpen
		for _, key := range meta.OpenPrivateSources {
			if len(r.redactor.active) >= 1024 {
				r.redactor.closed = true
				break
			}
			r.redactor.active[key] = true
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	repairs := make(map[string]int64)
	for _, path := range files {
		base := filepath.Base(path)
		var n uint64
		if _, scanErr := fmt.Sscanf(base, "%020d.jsonl", &n); scanErr != nil || base != fmt.Sprintf("%020d.jsonl", n) {
			continue
		}
		if n > r.seq {
			r.seq = n
		}
		st, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("recording segment is not a regular file: %s", base)
		}
		if chmodErr := os.Chmod(path, 0600); chmodErr != nil {
			return nil, chmodErr
		}
		sg := segment{path, st.Size(), st.ModTime()}
		r.segments = append(r.segments, sg)
		r.info.Bytes += sg.size
		var validEnd int64
		scanErr := scanRecords(path, opts.MaxRecordBytes, func(e Entry, off, end int64) error {
			validEnd = end
			if e.ID > r.info.LastID {
				// A record newer than the durable metadata checkpoint may have
				// redacted away the delimiters needed to reconstruct PEM state.
				r.redactor.closed = true
				r.info.LastID = e.ID
			}
			r.addIndex(recordRef{e.ID, path, off})
			return nil
		}, func(_, end int64, trailing bool) {
			r.info.SkippedRecords++
			r.redactor.closed = true
			if !trailing {
				validEnd = end
			}
		})
		if scanErr != nil {
			return nil, scanErr
		}
		if validEnd < sg.size {
			repairs[path] = validEnd
		}
	}
	r.writerOpen = true
	if metaErr := r.writeMeta(); metaErr != nil {
		return nil, metaErr
	}
	// Tail repair also removes recovery evidence, so it follows the durable
	// conservative checkpoint (and skips segments already removed by retention).
	for i := range r.segments {
		sg := &r.segments[i]
		if validEnd, ok := repairs[sg.path]; ok {
			if truncateErr := os.Truncate(sg.path, validEnd); truncateErr != nil {
				return nil, truncateErr
			}
			r.info.Bytes -= sg.size - validEnd
			sg.size = validEnd
		}
	}
	return r, nil
}

// scanRecords bounds each allocation and drains oversize records. A final record without
// a newline is considered interrupted even when the JSON happens to be syntactically valid.
func scanRecords(path string, maxRecordBytes int, visit func(Entry, int64, int64) error, bad func(int64, int64, bool)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64<<10)
	var off int64
	for {
		start := off
		var data []byte
		oversized := false
		for {
			part, readErr := reader.ReadSlice('\n')
			off += int64(len(part))
			if len(data)+len(part) > maxRecordBytes {
				oversized = true
			}
			if !oversized {
				data = append(data, part...)
			}
			if errors.Is(readErr, bufio.ErrBufferFull) {
				continue
			}
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			if off == start && readErr == io.EOF {
				return nil
			}
			trailing := readErr == io.EOF
			if trailing || oversized {
				if bad != nil {
					bad(start, off, trailing)
				}
			} else {
				d := json.NewDecoder(bytes.NewReader(data))
				d.UseNumber()
				var e Entry
				if decodeErr := d.Decode(&e); decodeErr != nil || e.ID == 0 {
					if bad != nil {
						bad(start, off, false)
					}
				} else {
					var extra any
					if decodeErr := d.Decode(&extra); !errors.Is(decodeErr, io.EOF) {
						if bad != nil {
							bad(start, off, false)
						}
					} else if visitErr := visit(e, start, off); visitErr != nil {
						return visitErr
					}
				}
			}
			if trailing {
				return nil
			}
			break
		}
	}
}
func (r *Recorder) addIndex(ref recordRef) {
	if len(r.index) >= r.opts.MaxIndex {
		copy(r.index, r.index[1:])
		r.index[len(r.index)-1] = ref
		r.info.IndexLimited = true
	} else {
		r.index = append(r.index, ref)
	}
}
func (r *Recorder) writeMeta() error {
	m := recordingMeta{Version: 1, Raw: r.opts.Raw, LastID: r.info.LastID, RedactionClosed: r.redactor.closed, Pending: r.pending, WriterOpen: r.writerOpen}
	for key := range r.redactor.active {
		m.OpenPrivateSources = append(m.OpenPrivateSources, key)
	}
	sort.Strings(m.OpenPrivateSources)
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if int64(len(data)) > r.opts.MaxBytes || len(data) > 1<<20 {
		return fmt.Errorf("recording metadata exceeds session byte limit")
	}
	growth := int64(len(data)) - r.metaBytes
	if writeErr := atomicPrivateWrite(filepath.Join(r.dir, "session.json"), data); writeErr != nil {
		return writeErr
	}
	r.info.Bytes += growth
	r.metaBytes = int64(len(data))
	// Make the metadata rename durable before any following record eviction/write.
	dir, err := os.Open(r.dir)
	if err != nil {
		return err
	}
	syncErr := errors.Join(dir.Sync(), dir.Close())
	if syncErr != nil {
		return syncErr
	}
	// Persist recovered state before eviction can remove its only replay evidence.
	// Atomic replacement has a bounded transient metadata-file overhead.
	return r.evict(0)
}
func (r *Recorder) evict(incoming int64) error {
	now := time.Now()
	for len(r.segments) > 0 && (r.info.Bytes+incoming > r.opts.MaxBytes || len(r.segments) >= 1024 || now.Sub(r.segments[0].modified) > r.opts.Retention) {
		sg := r.segments[0]
		if r.file != nil && r.file.Name() == sg.path {
			if err := r.file.Close(); err != nil {
				return err
			}
			r.file = nil
		}
		if err := os.Remove(sg.path); err != nil {
			return err
		}
		r.info.Bytes -= sg.size
		r.info.EvictedSegments++
		copy(r.segments, r.segments[1:])
		r.segments[len(r.segments)-1] = segment{}
		r.segments = r.segments[:len(r.segments)-1]
		out := r.index[:0]
		for _, ref := range r.index {
			if ref.path != sg.path {
				out = append(out, ref)
			}
		}
		r.index = out
	}
	return nil
}

// Append retains compatibility with individual producers.
//
//nolint:gocritic // Public value API prevents recorder redaction from mutating caller-owned entries.
func (r *Recorder) Append(entry Entry) error { return r.AppendBatch([]Entry{entry}) }

// AppendBatch durably commits at most 256 entries and 4MiB with one intent and
// checkpoint. A failed batch may have written a prefix; close/reopen is required.
//
//nolint:funlen // The intent, segment rotation, write, sync, and checkpoint sequence is one atomic protocol.
func (r *Recorder) AppendBatch(entries []Entry) (result error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("recording is closed")
	}
	if r.pending {
		return fmt.Errorf("previous recording append was interrupted; close and reopen the session")
	}
	if len(entries) > 256 {
		return fmt.Errorf("batch exceeds 256 entries")
	}
	if len(entries) == 0 {
		return nil
	}
	last := r.info.LastID
	for i := range entries {
		entry := &entries[i]
		if entry.ID == 0 || entry.ID <= last {
			return fmt.Errorf("entry ID %d must exceed recording last ID %d; seed Engine.Options.StartID", entry.ID, last)
		}
		last = entry.ID
	}
	r.pending = true
	defer func() {
		if result != nil {
			r.pending = true
			r.redactor.closed = true
		}
	}()
	if err := r.writeMeta(); err != nil {
		return err
	}
	total := 0
	for i := range entries {
		entry := r.redactor.Track(entries[i])
		if !r.opts.Raw {
			entry = SafeEntry(entry)
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		total += len(data)
		if total > 4<<20 {
			return fmt.Errorf("batch exceeds 4MiB recording byte limit")
		}
		if len(data) > r.opts.MaxRecordBytes || int64(len(data)) > r.opts.MaxBytes {
			return fmt.Errorf("record exceeds recording byte limit (%d bytes)", len(data))
		}
		if evictErr := r.evict(int64(len(data))); evictErr != nil {
			return evictErr
		}
		if r.file != nil && len(r.segments) > 0 && r.segments[len(r.segments)-1].size+int64(len(data)) > r.opts.SegmentBytes {
			if rotateErr := errors.Join(r.file.Sync(), r.file.Close()); rotateErr != nil {
				return rotateErr
			}
			r.file = nil
		}
		if r.file == nil {
			r.seq++
			path := filepath.Join(r.dir, fmt.Sprintf("%020d.jsonl", r.seq))
			r.file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			r.segments = append(r.segments, segment{path: path, modified: time.Now()})
		}
		sg := &r.segments[len(r.segments)-1]
		off := sg.size
		n, err := r.file.Write(data)
		if err != nil || n != len(data) {
			return rollbackWrite(r.file, off, err)
		}
		sg.size += int64(n)
		sg.modified = time.Now()
		r.info.Bytes += int64(n)
		r.info.LastID = entry.ID
		r.addIndex(recordRef{entry.ID, sg.path, off})
	}
	if r.file != nil {
		if err := r.file.Sync(); err != nil {
			return err
		}
	}
	r.pending = false
	return r.writeMeta()
}

func rollbackWrite(file *os.File, offset int64, writeErr error) error {
	if writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	truncateErr := file.Truncate(offset)
	if truncateErr != nil {
		truncateErr = fmt.Errorf("truncate partial record: %w", truncateErr)
	}
	_, seekErr := file.Seek(offset, io.SeekStart)
	if seekErr != nil {
		seekErr = fmt.Errorf("seek after partial record: %w", seekErr)
	}
	return errors.Join(writeErr, truncateErr, seekErr)
}
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.writerOpen = false
	metaErr := r.writeMeta()
	var syncErr, closeErr error
	if r.file != nil {
		syncErr = r.file.Sync()
		closeErr = r.file.Close()
		r.file = nil
	}
	return errors.Join(metaErr, syncErr, closeErr)
}
func (r *Recorder) Info() RecordingInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.info
	i.ConservativeRedaction = r.redactor.closed
	i.Indexed = len(r.index)
	i.Segments = len(r.segments)
	return i
}
func pageLimit(limit int) int {
	if limit <= 0 {
		return 200
	}
	if limit > 10000 {
		return 10000
	}
	return limit
}
func (r *Recorder) Page(beforeID uint64, limit int) ([]Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit = pageLimit(limit)
	var out []Entry
	for _, sg := range r.segments {
		err := scanRecords(sg.path, r.opts.MaxRecordBytes, func(e Entry, _, _ int64) error {
			if beforeID == 0 || e.ID < beforeID {
				if len(out) == limit {
					copy(out, out[1:])
					out[len(out)-1] = e
				} else {
					out = append(out, e)
				}
			}
			return nil
		}, nil)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

var errEnough = errors.New("enough results")

func (r *Recorder) search(q *Query, limit int, paths map[string]bool) ([]Entry, error) {
	limit = pageLimit(limit)
	var out []Entry
	for _, sg := range r.segments {
		if paths != nil && !paths[sg.path] {
			continue
		}
		err := scanRecords(sg.path, r.opts.MaxRecordBytes, func(e Entry, _, _ int64) error {
			if q.Match(e) {
				out = append(out, e)
				if len(out) >= limit {
					return errEnough
				}
			}
			return nil
		}, nil)
		if errors.Is(err, errEnough) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (r *Recorder) Search(q *Query, limit int) ([]Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.search(q, limit, nil)
}
func (r *Recorder) SearchRaw(pattern string, limit int) ([]Entry, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	q := &Query{regex: re}
	r.mu.Lock()
	defer r.mu.Unlock()
	var paths map[string]bool
	// A literal ASCII token has identical representation in Raw and its JSON encoding.
	// More complex expressions use Go directly to avoid JSON-escaping false negatives.
	if pattern != "" && regexp.QuoteMeta(pattern) == pattern && !strings.ContainsAny(pattern, "\\\"<>&\r\n\t") && isASCII(pattern) && len(r.segments) > 0 {
		if rg, err := exec.LookPath("rg"); err == nil {
			args := []string{"--files-with-matches", "--fixed-strings", "--text", "--color=never", "-e", pattern, "--"}
			for _, sg := range r.segments {
				args = append(args, sg.path)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, rg, args...)
			output, err := cmd.Output()
			if err == nil {
				paths = map[string]bool{}
				for _, p := range strings.Split(strings.TrimSpace(string(output)), "\n") {
					paths[p] = true
				}
			} else if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				paths = map[string]bool{}
			}
		}
	}
	return r.search(q, limit, paths)
}
func isASCII(s string) bool {
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}
