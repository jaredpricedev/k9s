// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config_test

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestNewLogger(t *testing.T) {
	l := config.NewLogger()
	l = l.Validate()

	assert.Equal(t, int64(100), l.TailCount)
	assert.Equal(t, 5000, l.BufferSize)
	assert.Equal(t, 50, l.LogBufferSize)
}

func TestLoggerValidate(t *testing.T) {
	var l config.Logger
	l = l.Validate()

	assert.Equal(t, int64(100), l.TailCount)
	assert.Equal(t, 5000, l.BufferSize)
	assert.Equal(t, 50, l.LogBufferSize)
}

func TestLoggerRecordingBounds(t *testing.T) {
	l := config.NewLogger().Validate()
	assert.Equal(t, 8, l.RecordingSessions)
	assert.Equal(t, 24, l.RecordingRetentionHours)
	assert.Equal(t, 128, l.RecordingMaxMiB)
	l.RecordingSessions = 999
	l.RecordingRetentionHours = -1
	l.RecordingMaxMiB = -1
	l = l.Validate()
	assert.Equal(t, 8, l.RecordingSessions)
	assert.Equal(t, 24, l.RecordingRetentionHours)
	assert.Equal(t, 128, l.RecordingMaxMiB)
}
