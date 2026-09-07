// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLogs_Normal(t *testing.T) {
	input := "line one\nline two\nline three\n"
	stream := io.NopCloser(strings.NewReader(input))
	out := make(chan *LogItem, 100)
	opts := &LogOptions{Path: "ns/pod", Container: "c1"}

	result := readLogs(context.Background(), stream, out, opts)
	close(out)

	assert.Equal(t, streamEOF, result)

	var lines []string
	for item := range out {
		if !item.IsError {
			lines = append(lines, string(item.Bytes))
		}
	}
	assert.Len(t, lines, 3)
}

func TestReadLogs_CancelStopsEarly(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *LogItem, 10)
	done := make(chan streamResult, 1)
	go func() { done <- readLogs(ctx, reader, out, &LogOptions{}) }()
	_, err := writer.Write([]byte("one\ntwo\n"))
	require.NoError(t, err)
	<-out
	cancel()
	select {
	case result := <-done:
		require.Equal(t, streamCanceled, result)
	case <-time.After(time.Second):
		t.Fatal("blocked read survived cancellation")
	}
}

func TestReadLogs_PartialLineAtEOF(t *testing.T) {
	// Input without trailing newline
	input := "full line\npartial line without newline"
	stream := io.NopCloser(strings.NewReader(input))
	out := make(chan *LogItem, 100)
	opts := &LogOptions{Path: "ns/pod", Container: "c1"}

	result := readLogs(context.Background(), stream, out, opts)
	close(out)

	assert.Equal(t, streamEOF, result)

	var items []*LogItem
	for item := range out {
		items = append(items, item)
	}
	// Should get: full line, partial line, and the EOF error message
	assert.GreaterOrEqual(t, len(items), 2, "should emit partial line before EOF")

	// Find the partial line (non-error item that doesn't end with newline)
	foundPartial := false
	for _, item := range items {
		if !item.IsError && strings.Contains(string(item.Bytes), "partial line without newline") {
			foundPartial = true
		}
	}
	assert.True(t, foundPartial, "partial line at EOF should be emitted")
}
