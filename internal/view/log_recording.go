package view

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	"github.com/gofrs/flock"
)

const (
	logWriterQueueBytes = 8 << 20
	logRootLockName     = ".k9plus-recordings.lock"
)

var errLogHistoryTornTail = errors.New("torn final recording record ignored; recording durability is uncertain")

type queuedLogEntry struct {
	entry logstream.Entry
	size  int
}
type logWriterState struct {
	path, err string
	info      logstream.RecordingInfo
	dropped   uint64
	closed    bool
}
type logWriter struct {
	mu          sync.Mutex
	queue       chan queuedLogEntry
	done        chan struct{}
	state       logWriterState
	queuedBytes int
	closing     bool
	lease       *logSessionLease
}

func newLogWriter(dir string, raw bool, opts logstream.RecordingOptions, leases ...*logSessionLease) *logWriter {
	w := &logWriter{queue: make(chan queuedLogEntry, 4096), done: make(chan struct{}), state: logWriterState{path: dir}}
	if len(leases) > 0 {
		w.lease = leases[0]
	}
	opts.Raw = raw
	w.state.info.Raw = raw
	go w.run(opts)
	return w
}
func (w *logWriter) status() logWriterState { w.mu.Lock(); defer w.mu.Unlock(); return w.state }
func (w *logWriter) offer(entries []logstream.Entry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return
	}
	for i := range entries {
		e := &entries[i]
		if w.state.err != "" {
			w.state.dropped++
			continue
		}
		data, err := json.Marshal(e)
		if err != nil {
			w.state.err = err.Error()
			w.state.dropped++
			continue
		}
		// Queue budget estimates serialized JSONL bytes, not exact Go heap size.
		size := len(data) + 1
		if w.queuedBytes+size > logWriterQueueBytes {
			w.state.dropped++
			continue
		}
		select {
		case w.queue <- queuedLogEntry{*e, size}:
			w.queuedBytes += size
		default:
			w.state.dropped++
		}
	}
}
func (w *logWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closing {
		w.closing = true
		close(w.queue)
	}
}
func (w *logWriter) run(opts logstream.RecordingOptions) {
	defer close(w.done)
	defer func() {
		if w.lease == nil {
			return
		}
		if err := w.lease.Close(); err != nil {
			w.mu.Lock()
			w.state.err = errors.Join(errors.New(w.state.err), err).Error()
			w.mu.Unlock()
			return
		}
		if err := os.Remove(filepath.Join(w.state.path, "active")); err != nil && !errors.Is(err, os.ErrNotExist) {
			w.mu.Lock()
			w.state.err = errors.Join(errors.New(w.state.err), err).Error()
			w.mu.Unlock()
		}
	}()
	r, err := logstream.OpenRecorder(w.state.path, opts)
	fail := func(err error) {
		if err != nil {
			w.mu.Lock()
			w.state.err = err.Error()
			w.mu.Unlock()
		}
	}
	fail(err)
	var batch []logstream.Entry
	batchRedactor := logstream.NewRedactor(1024)
	size := 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if r != nil && err == nil {
			err = r.AppendBatch(batch)
			fail(err)
			w.mu.Lock()
			w.state.info = r.Info()
			w.mu.Unlock()
		}
		batch = nil
		size = 0
	}
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case item, ok := <-w.queue:
			if !ok {
				flush()
				if r != nil {
					fail(r.Close())
					w.mu.Lock()
					w.state.info = r.Info()
					w.mu.Unlock()
				}
				w.mu.Lock()
				w.state.closed = true
				w.mu.Unlock()
				return
			}
			w.mu.Lock()
			w.queuedBytes -= item.size
			w.mu.Unlock()
			// Redaction can expand short repeated bearer values. Size the actual
			// transformation on the writer goroutine before deciding batch boundaries;
			// the recorder receives originals so its durable source tracking is intact.
			sized := item.entry
			if !opts.Raw {
				sized = batchRedactor.Transform(sized)
			}
			encoded, sizeErr := json.Marshal(sized)
			if sizeErr != nil {
				flush()
				err = sizeErr
				fail(err)
				continue
			}
			// Bound retained original payloads as well as their encoded safe output.
			recordBytes := max(len(encoded)+1, item.size)
			if recordBytes > 4<<20 {
				flush()
				err = fmt.Errorf("record exceeds 4MiB recording batch limit")
				fail(err)
				continue
			}
			if len(batch) >= 256 || size+recordBytes > 4<<20 {
				flush()
			}
			batch = append(batch, item.entry)
			size += recordBytes
			if len(batch) >= 256 || size >= 4<<20 {
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

// scanLogHistory reads a session without opening a writer or modifying metadata.
// Live retention may remove a segment; that is an explicit retryable error.
// Output is always safe, including when the selected session was explicitly raw.
func scanLogHistory(
	ctx context.Context,
	dir string,
	before uint64,
	q *logstream.Query,
	out io.Writer,
	contexts ...logstream.ExportContext,
) ([]logstream.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const limit = 200
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) > 1024 {
		return nil, fmt.Errorf("session exceeds 1024 segment limit")
	}
	tornTail, completeLast, err := inspectLogHistorySegments(paths)
	if err != nil {
		return nil, err
	}
	var page []logstream.Entry
	redactor := logstream.NewRedactor(1024)
	if headerErr := writeLogHistoryHeader(out, contexts, tornTail); headerErr != nil {
		return nil, headerErr
	}
	for pathIndex, path := range paths {
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("history segment must be a regular file")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		var reader io.Reader = f
		if tornTail && pathIndex == len(paths)-1 {
			reader = io.LimitReader(f, completeLast)
		}
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 64<<10), (1<<20)+1)
		for scan.Scan() {
			if err = ctx.Err(); err != nil {
				f.Close()
				return nil, err
			}
			e, decodeErr := decodeLogHistoryEntry(scan.Bytes(), redactor)
			if decodeErr != nil {
				f.Close()
				return nil, decodeErr
			}
			if before != 0 && e.ID >= before {
				continue
			}
			if q != nil && e.Marker == nil && !q.Match(e) {
				continue
			}
			if out != nil {
				if err = writeLogHistoryEntry(out, &e); err != nil {
					f.Close()
					return nil, err
				}
			} else if q != nil {
				page = append(page, e)
				if len(page) >= limit {
					f.Close()
					if tornTail {
						return page, errLogHistoryTornTail
					}
					return page, nil
				}
			} else {
				if len(page) == limit {
					copy(page, page[1:])
					page[len(page)-1] = e
				} else {
					page = append(page, e)
				}
			}
		}
		err = errors.Join(scan.Err(), f.Close())
		if err != nil {
			return nil, err
		}
	}
	if tornTail {
		return page, errLogHistoryTornTail
	}
	return page, nil
}

func writeLogHistoryHeader(out io.Writer, contexts []logstream.ExportContext, tornTail bool) error {
	if out == nil {
		return nil
	}
	exportContext := logstream.ExportContext{Loss: "retained disk history only; segments may have been evicted"}
	if len(contexts) > 0 {
		exportContext.Filters = contexts[0].Filters
		exportContext.Loss += "; " + contexts[0].Loss
	}
	if tornTail {
		exportContext.Loss += "; " + errLogHistoryTornTail.Error()
	}
	return logstream.Export(out, nil, exportContext)
}

func decodeLogHistoryEntry(line []byte, redactor *logstream.Redactor) (logstream.Entry, error) {
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	var entry logstream.Entry
	if err := d.Decode(&entry); err != nil {
		return logstream.Entry{}, fmt.Errorf("incomplete or invalid recorded line: %w", err)
	}
	return redactor.Transform(entry), nil
}

func writeLogHistoryEntry(out io.Writer, entry *logstream.Entry) error {
	text := logstream.Snippet([]logstream.Entry{*entry}, logstream.ExportContext{})
	_, err := io.WriteString(out, text[strings.IndexByte(text, '\n')+1:])
	return err
}

func inspectLogHistorySegments(paths []string) (tornTail bool, completeLast int64, resultErr error) {
	completeLast = -1
	for i, path := range paths {
		st, err := os.Lstat(path)
		if err != nil {
			return false, 0, err
		}
		if !st.Mode().IsRegular() {
			return false, 0, fmt.Errorf("history segment must be a regular file")
		}
		if st.Size() == 0 {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return false, 0, err
		}
		var last [1]byte
		_, err = f.ReadAt(last[:], st.Size()-1)
		if err != nil {
			f.Close()
			return false, 0, err
		}
		if last[0] == '\n' {
			if closeErr := f.Close(); closeErr != nil {
				return false, 0, closeErr
			}
			continue
		}
		if i != len(paths)-1 {
			f.Close()
			return false, 0, fmt.Errorf("incomplete non-final history segment")
		}
		completeLast, err = lastCompleteHistoryOffset(f, st.Size())
		err = errors.Join(err, f.Close())
		if err != nil {
			return false, 0, err
		}
		tornTail = true
	}
	return tornTail, completeLast, nil
}

func lastCompleteHistoryOffset(f *os.File, size int64) (int64, error) {
	const chunkSize = 64 << 10
	buf := make([]byte, chunkSize)
	for end := size; end > 0; {
		start := max(int64(0), end-chunkSize)
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if index := bytes.LastIndexByte(buf[:n], '\n'); index >= 0 {
			return start + int64(index) + 1, nil
		}
		end = start
	}
	return 0, nil
}

// Only directories created with this private ownership token are pruned. The
// configured parent is never removed, nor are arbitrary caller directories.
func newLogSession(root string, maxSessions, retentionHours int) (string, error) {
	return prepareLogSession(context.Background(), root, maxSessions, retentionHours)
}
func prepareLogSession(ctx context.Context, root string, maxSessions, retentionHours int) (string, error) {
	path, _, err := prepareLogSessionOwned(ctx, root, maxSessions, retentionHours, false)
	return path, err
}

type logSessionLease struct {
	lock *flock.Flock
}

func (l *logSessionLease) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}

func tryLogSessionLease(path string) (*logSessionLease, bool, error) {
	lockPath := filepath.Join(path, "active")
	if err := ensureRegularLockFile(lockPath); err != nil {
		return nil, false, err
	}
	lock := flock.New(lockPath, flock.SetPermissions(0600))
	locked, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return nil, false, err
	}
	if !locked {
		_ = lock.Close()
		return nil, true, nil
	}
	return &logSessionLease{lock: lock}, false, nil
}

func acquireLogRootLease(ctx context.Context, root string) (*logSessionLease, error) {
	lockPath := filepath.Join(root, logRootLockName)
	if err := ensureRegularLockFile(lockPath); err != nil {
		return nil, err
	}
	lock := flock.New(lockPath, flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if !locked {
		_ = lock.Close()
		return nil, ctx.Err()
	}
	return &logSessionLease{lock: lock}, nil
}

func ensureRegularLockFile(path string) error {
	for {
		st, err := os.Lstat(path)
		if err == nil {
			if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
				return fmt.Errorf("recording lock must be a regular file: %s", filepath.Base(path))
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(createErr, os.ErrExist) {
			continue
		}
		if createErr != nil {
			return createErr
		}
		if closeErr := f.Close(); closeErr != nil {
			return closeErr
		}
	}
}

func prepareReservedLogSession(ctx context.Context, root string, maxSessions, retentionHours int) (string, *logSessionLease, error) {
	return prepareLogSessionOwned(ctx, root, maxSessions, retentionHours, true)
}

func pruneLogSessions(ctx context.Context, root string, maxSessions, retentionHours int) error {
	sessions, err := listLogSessionsContext(ctx, root)
	if err != nil {
		return err
	}
	for i, path := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		pathInfo, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if i < maxSessions-1 && time.Since(pathInfo.ModTime()) <= time.Duration(retentionHours)*time.Hour {
			continue
		}
		lease, active, leaseErr := tryLogSessionLease(path)
		if leaseErr != nil {
			return leaseErr
		}
		if active {
			continue
		}
		if removeErr := removeLeasedLogSession(ctx, path, lease); removeErr != nil {
			return removeErr
		}
	}
	return nil
}

func prepareLogSessionOwned(ctx context.Context, root string, maxSessions, retentionHours int, reserve bool) (string, *logSessionLease, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if maxSessions <= 0 {
		maxSessions = 8
	}
	if maxSessions > 64 {
		maxSessions = 64
	}
	if retentionHours <= 0 {
		retentionHours = 24
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	st, err := os.Lstat(root)
	if err != nil {
		return "", nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("recording root must be a real directory")
	}
	if chmodErr := os.Chmod(root, 0700); chmodErr != nil {
		return "", nil, chmodErr
	}
	rootLease, err := acquireLogRootLease(ctx, root)
	if err != nil {
		return "", nil, err
	}
	defer rootLease.Close()
	if pruneErr := pruneLogSessions(ctx, root, maxSessions, retentionHours); pruneErr != nil {
		return "", nil, pruneErr
	}
	remaining, err := listLogSessionsContext(ctx, root)
	if err != nil {
		return "", nil, err
	}
	if len(remaining) >= maxSessions {
		return "", nil, fmt.Errorf("recording session cap reached (%d active or retained sessions)", maxSessions)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", nil, contextErr
	}
	path, err := os.MkdirTemp(root, "session-")
	if err != nil {
		return "", nil, err
	}
	var lease *logSessionLease
	if reserve {
		var active bool
		lease, active, err = tryLogSessionLease(path)
		if err != nil || active {
			_ = removeLogSession(context.Background(), path)
			if err == nil {
				err = fmt.Errorf("new recording session lease unexpectedly busy")
			}
			return "", nil, err
		}
	}
	// A reserved session becomes discoverable only after its activity lease is
	// held. The root lease prevents another cooperating process from pruning the
	// directory during publication.
	if err = os.WriteFile(filepath.Join(path, "k9plus-owned"), []byte("log-workbench-v1\n"), 0600); err != nil {
		_ = lease.Close()
		_ = removeLogSession(context.Background(), path)
		return "", nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = lease.Close()
		_ = removeLogSession(context.Background(), path)
		return "", nil, err
	}
	return path, lease, nil
}

// The root lease is held by the caller. It blocks another cooperating process
// from claiming the session after the activity lease must be closed for Windows
// to unlink the lock file.
func removeLeasedLogSession(ctx context.Context, path string, lease *logSessionLease) error {
	defer lease.Close()
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if entry.Name() == "active" || entry.Name() == "k9plus-owned" {
			continue
		}
		child := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			err = removeLogSession(ctx, child)
		} else {
			err = os.Remove(child)
		}
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := lease.Close(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(path, "active")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(path, "k9plus-owned")); err != nil {
		return err
	}
	return os.Remove(path)
}
func listLogSessions(root string) ([]string, error) {
	return listLogSessionsContext(context.Background(), root)
}
func listLogSessionsContext(ctx context.Context, root string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type session struct {
		path     string
		modified time.Time
	}
	var all []session
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "session-") {
			continue
		}
		path := filepath.Join(root, d.Name())
		token, err := os.ReadFile(filepath.Join(path, "k9plus-owned"))
		if err != nil || string(token) != "log-workbench-v1\n" {
			continue
		}
		st, err := d.Info()
		if err != nil {
			return nil, err
		}
		all = append(all, session{path, st.ModTime()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].modified.After(all[j].modified) })
	out := make([]string, 0, len(all))
	for _, s := range all {
		out = append(out, s.path)
	}
	return out, nil
}

// Cancellation is checked between exported records without holding UI/model or
// live-recorder locks. Export never constructs an unbounded joined string.
type contextLogWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextLogWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

// Keep the ownership token until every other child has been removed, so a
// canceled prune remains identifiable on a later maintenance pass. Symlinks are
// unlinked, never followed. Cancellation is observed between filesystem calls.
func removeLogSession(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	token := ""
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		child := filepath.Join(path, entry.Name())
		if entry.Name() == "k9plus-owned" {
			token = child
			continue
		}
		var removeErr error
		if entry.IsDir() {
			removeErr = removeLogSession(ctx, child)
		} else {
			removeErr = os.Remove(child)
		}
		if removeErr != nil {
			return removeErr
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if token != "" {
		if removeErr := os.Remove(token); removeErr != nil {
			return removeErr
		}
	}
	return os.Remove(path)
}

// dropped counts admission rejections only. Previously accepted records can be
// unwritten/uncertain after a storage error, so never infer durable loss from ID.
func (s *logWriterState) lossContext() string {
	result := fmt.Sprintf("queue-admission-drops=%d; disk-evicted-segments=%d", s.dropped, s.info.EvictedSegments)
	if s.err != "" {
		result += "; recording-error=" + s.err + "; accepted batch/queued records may be unwritten or have uncertain durability"
	}
	return result
}
