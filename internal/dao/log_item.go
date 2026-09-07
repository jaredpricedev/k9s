// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"bytes"
	"time"

	"github.com/derailed/k9s/internal/logstream"
)

// LogChan represents a channel for logs.
type LogChan chan *LogItem

var ItemEOF = new(LogItem)

// LogItem represents a container log line.
type LogItem struct {
	Sensitive           bool
	Raw                 []byte
	Source              logstream.Source
	RuntimeTime         time.Time
	Marker              *logstream.Marker
	Truncated           bool
	Labels, Annotations map[string]string
	Pod, Container      string
	SingleContainer     bool
	Bytes               []byte
	IsError             bool
}

// NewLogItem returns a new item.
func NewLogItem(bb []byte) *LogItem {
	return &LogItem{
		Bytes: bb,
	}
}

// NewLogItemFromString returns a new item.
func NewLogItemFromString(s string) *LogItem {
	return &LogItem{
		Bytes: []byte(s),
	}
}

// ID returns pod and or container based id.
func (l *LogItem) ID() string {
	if l.Pod != "" {
		return l.Pod
	}
	return l.Container
}

// GetTimestamp fetch log lime timestamp
func (l *LogItem) GetTimestamp() string {
	index := bytes.Index(l.Bytes, []byte{' '})
	if index < 0 {
		return ""
	}
	return string(l.Bytes[:index])
}

// Info returns pod and container information.
func (l *LogItem) Info() string {
	return l.Pod + "::" + l.Container
}

// IsEmpty checks if the entry is empty.
func (l *LogItem) IsEmpty() bool {
	return len(l.Bytes) == 0 && len(l.Raw) == 0 && l.Marker == nil
}

// Size returns the size of the item.
func (l *LogItem) Size() int {
	return 100 + len(l.Raw) + len(l.Bytes) + len(l.Pod) + len(l.Container)
}

// Render returns a log line as string.
func (l *LogItem) Render(paint string, showTime bool, bb *bytes.Buffer) {
	index := bytes.Index(l.Bytes, []byte{' '})
	if l.Raw != nil && l.RuntimeTime.IsZero() {
		index = -1
	}
	if showTime && index > 0 {
		bb.WriteString("[gray::b]")
		bb.Write(l.Bytes[:index])
		bb.WriteString(" ")
		if l := 30 - len(l.Bytes[:index]); l > 0 {
			bb.Write(bytes.Repeat([]byte{' '}, l))
		}
		bb.WriteString("[-::-]")
	}

	if l.Pod != "" {
		bb.WriteString("[" + paint + "::]" + l.Pod)
	}

	if !l.SingleContainer && l.Container != "" {
		if l.Pod != "" {
			bb.WriteString(" ")
		}
		bb.WriteString("[" + paint + "::b]" + l.Container + "[-::-] ")
	} else if l.Pod != "" {
		bb.WriteString("[-::] ")
	}

	if index > 0 {
		bb.Write(l.Bytes[index+1:])
	} else {
		bb.Write(l.Bytes)
	}
}

// Entry returns raw structured data without terminal presentation markup.
func (l *LogItem) Entry() logstream.Entry {
	e := logstream.Parse(l.Source, l.RuntimeTime, string(l.Raw))
	e.Marker, e.Truncated, e.Sensitive = l.Marker, l.Truncated, l.Sensitive
	return e
}
