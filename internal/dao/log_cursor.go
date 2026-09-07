// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"crypto/sha256"
	"time"

	"github.com/derailed/k9s/internal/logstream"
)

type logReplayKey struct {
	time int64
	hash [32]byte
}

// logCursor retains the serialized inclusive timestamp boundary, including repeated identical lines.
type logCursor struct {
	failClosed      bool
	redactor        *logstream.Redactor
	time            time.Time
	counts          map[logReplayKey]int
	replay          map[logReplayKey]int
	replayUncertain bool
	limited         bool
}

func (c *logCursor) reconnect() {
	c.replay = make(map[logReplayKey]int, len(c.counts))
	for k, v := range c.counts {
		c.replay[k] = v
	}
	if c.replayUncertain {
		c.failClosed = true
	}
}

// replayed consumes only replay allowance; it never advances the delivered cursor.
func (c *logCursor) replayed(item *LogItem) bool {
	t := item.RuntimeTime
	if t.IsZero() {
		return false
	}
	if !t.Truncate(time.Second).Equal(c.time.Truncate(time.Second)) {
		return false
	}
	key := logReplayKey{time: t.UnixNano(), hash: sha256.Sum256(item.Raw)}
	if c.replay[key] > 0 {
		c.replay[key]--
		return true
	}
	return false
}

// advance commits a record only after its channel delivery succeeded.
func (c *logCursor) advance(item *LogItem) {
	t := item.RuntimeTime
	if t.IsZero() {
		return
	}
	boundary := c.time.Truncate(time.Second)
	itemBoundary := t.Truncate(time.Second)
	if t.Before(c.time) && !itemBoundary.Equal(boundary) {
		return
	}
	if t.After(c.time) {
		if !itemBoundary.Equal(boundary) {
			c.counts = make(map[logReplayKey]int)
			c.replayUncertain = false
		}
		c.time = t
		c.replay = nil
	}
	key := logReplayKey{time: t.UnixNano(), hash: sha256.Sum256(item.Raw)}
	if len(c.counts) < 1024 || c.counts[key] > 0 {
		c.counts[key]++
	} else {
		c.replayUncertain = true
		c.limited = true
	}
}
