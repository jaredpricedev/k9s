// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/derailed/k9s/internal/logstream"
)

const maxLogLineBytes = 64 * 1024

func sendLog(ctx context.Context, out chan<- *LogItem, item *LogItem) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case out <- item:
		return true
	}
}

func readLogs(ctx context.Context, stream io.ReadCloser, out chan<- *LogItem, opts *LogOptions) streamResult {
	return readLogStream(ctx, stream, out, opts, nil)
}

func readLogStream(ctx context.Context, stream io.ReadCloser, out chan<- *LogItem, opts *LogOptions, cursor *logCursor) streamResult {
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	defer stream.Close()
	r := bufio.NewReaderSize(stream, 4096)
	redactor := logstream.NewRedactor(1)
	if cursor != nil {
		if cursor.redactor == nil {
			cursor.redactor = redactor
		} else {
			redactor = cursor.redactor
		}
	}
	failClosed := cursor != nil && cursor.failClosed
	for {
		if ctx.Err() != nil {
			return streamCanceled
		}
		line := make([]byte, 0, 4096)
		truncated := false
		var err error
		for {
			var part []byte
			part, err = r.ReadSlice('\n')
			remaining := maxLogLineBytes - len(line)
			if len(part) > remaining {
				line = append(line, part[:remaining]...)
				truncated = true
			} else {
				line = append(line, part...)
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				break
			}
			if ctx.Err() != nil {
				return streamCanceled
			}
		}
		if ctx.Err() != nil {
			return streamCanceled
		}
		// A transport error may leave a torn physical record. Resume from the
		// last complete runtime record instead of advancing the cursor past it.
		if len(line) > 0 && (err == nil || errors.Is(err, io.EOF)) {
			item := opts.ToLogItem(line)
			item.Truncated = truncated
			if cursor != nil && cursor.replayed(item) {
				if errors.Is(err, io.EOF) {
					return streamEOF
				}
				continue
			}
			tracked := redactor.Track(item.Entry())
			// Discarded bytes might contain a private-key delimiter. Preserve raw
			// records, but fail closed for this source generation after truncation.
			if truncated {
				failClosed = true
				if cursor != nil {
					cursor.failClosed = true
				}
			}
			item.Sensitive = tracked.Sensitive || failClosed
			tracked.Sensitive = item.Sensitive
			item.setDisplay(logstream.SafeEntry(tracked))
			if !sendLog(ctx, out, item) {
				// Track may have consumed an undelivered END delimiter. If this source
				// state is reused, fail closed instead of trusting that advanced state.
				if cursor != nil && item.Sensitive {
					cursor.failClosed = true
				}
				return streamCanceled
			}
			if cursor != nil {
				cursor.advance(item)
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return streamEOF
			}
			if !sendLog(ctx, out, opts.ToErrLogItem(fmt.Errorf("stream read: %w", err))) {
				return streamCanceled
			}
			return streamError
		}
	}
}
