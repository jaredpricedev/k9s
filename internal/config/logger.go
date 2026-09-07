// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config

const (
	// DefaultLoggerTailCount tracks default log tail size.
	DefaultLoggerTailCount = 100

	// MaxLogThreshold sets the max value for log size.
	MaxLogThreshold = 5_000

	// DefaultSinceSeconds tracks default log age.
	DefaultSinceSeconds = -1 // tail logs by default

	// DefaultLogBufferSize is the channel buffer for log streaming.
	DefaultLogBufferSize = 50
)

// Logger tracks logger options.
type Logger struct {
	NoiseAppLabels          []string `json:"noiseAppLabels,omitempty" yaml:"noiseAppLabels,omitempty"`
	RecordingSessions       int      `json:"recordingSessions,omitempty" yaml:"recordingSessions,omitempty"`
	RecordingRetentionHours int      `json:"recordingRetentionHours,omitempty" yaml:"recordingRetentionHours,omitempty"`
	RecordingMaxMiB         int      `json:"recordingMaxMiB,omitempty" yaml:"recordingMaxMiB,omitempty"`
	TailCount               int64    `json:"tail" yaml:"tail"`
	BufferSize              int      `json:"buffer" yaml:"buffer"`
	SinceSeconds            int64    `json:"sinceSeconds" yaml:"sinceSeconds"`
	TextWrap                bool     `json:"textWrap" yaml:"textWrap"`
	DisableAutoscroll       bool     `json:"disableAutoscroll" yaml:"disableAutoscroll"`
	ColumnLock              bool     `json:"columnLock" yaml:"columnLock"`
	ShowTime                bool     `json:"showTime" yaml:"showTime"`
	LogBufferSize           int      `json:"logBufferSize" yaml:"logBufferSize"`
}

// NewLogger returns a new instance.
func NewLogger() Logger {
	return Logger{
		RecordingSessions:       8,
		RecordingRetentionHours: 24,
		RecordingMaxMiB:         128,
		TailCount:               DefaultLoggerTailCount,
		BufferSize:              MaxLogThreshold,
		SinceSeconds:            DefaultSinceSeconds,
		LogBufferSize:           DefaultLogBufferSize,
	}
}

// Validate checks thresholds and make sure we're cool. If not use defaults.
//
//nolint:gocritic // Validation intentionally returns a corrected value without mutating the caller.
func (l Logger) Validate() Logger {
	if l.RecordingSessions <= 0 || l.RecordingSessions > 64 {
		l.RecordingSessions = 8
	}
	if l.RecordingRetentionHours <= 0 || l.RecordingRetentionHours > 8760 {
		l.RecordingRetentionHours = 24
	}
	if l.RecordingMaxMiB <= 0 || l.RecordingMaxMiB > 1024 {
		l.RecordingMaxMiB = 128
	}
	if l.TailCount <= 0 {
		l.TailCount = DefaultLoggerTailCount
	}
	if l.TailCount > MaxLogThreshold {
		l.TailCount = MaxLogThreshold
	}
	if l.BufferSize <= 0 || l.BufferSize > MaxLogThreshold {
		l.BufferSize = MaxLogThreshold
	}
	if l.SinceSeconds == 0 {
		l.SinceSeconds = DefaultSinceSeconds
	}
	if l.LogBufferSize <= 0 {
		l.LogBufferSize = DefaultLogBufferSize
	}

	return l
}
