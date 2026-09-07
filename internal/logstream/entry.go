// Package logstream processes raw log entries without Kubernetes or presentation dependencies.
package logstream

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Source struct {
	Cluster, Context, Namespace, Pod, UID, Container string
	Generation                                       int32
}

//nolint:gocritic // Value receiver preserves the small immutable identity API.
func (s Source) Key() string { b, _ := json.Marshal(s); return string(b) }

const (
	levelTrace = "trace"
	levelDebug = "debug"
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
	levelFatal = "fatal"
)

type Marker struct {
	Kind, Origin, Message string
	Time                  time.Time
	Approximate           bool
}
type Entry struct {
	ID                           uint64
	LastID, Repeats              uint64
	Source                       Source
	RuntimeTime, ApplicationTime time.Time
	Raw, Format, Level, Message  string
	Fields                       map[string]any
	Marker                       *Marker
	OriginalLines                []string
	Occurrences                  uint64
	LastRuntimeTime              time.Time
	Truncated                    bool
	Sensitive                    bool
	Timeline                     []TimeCount
}

func normalizeLevel(s string) string {
	switch strings.ToLower(s) {
	case levelTrace, "trc":
		return levelTrace
	case levelDebug, "dbg":
		return levelDebug
	case levelInfo, "information", "inf":
		return levelInfo
	case "warning", "warn", "wrn":
		return levelWarn
	case levelError, "err", "eror":
		return levelError
	case levelFatal, "critical", "crit", "panic", "emerg", "alert":
		return levelFatal
	}
	return strings.ToLower(s)
}
func Severity(s string) int {
	switch normalizeLevel(s) {
	case levelTrace:
		return 1
	case levelDebug:
		return 2
	case levelInfo:
		return 3
	case levelWarn:
		return 4
	case levelError:
		return 5
	case levelFatal:
		return 6
	}
	return 0
}

//nolint:gocritic // Source is accepted by value to keep Parse immutable for callers.
func Parse(source Source, runtime time.Time, raw string) Entry {
	e := Entry{
		Source: source, RuntimeTime: runtime, LastRuntimeTime: runtime, Raw: raw, Format: "raw",
		Message: raw, OriginalLines: []string{raw}, Occurrences: 1, Repeats: 1,
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var fields map[string]any
	if err := d.Decode(&fields); err == nil && fields != nil {
		var trailing any
		if d.Decode(&trailing) == io.EOF {
			e.Format = "json"
			e.Fields = fields
		}
	}
	if e.Fields == nil {
		if f, err := parseLogfmt(raw); err == nil {
			e.Format = "logfmt"
			e.Fields = f
		}
	}
	for _, key := range []string{"level", "severity", "log.level"} {
		if v, ok := e.Field(key); ok {
			e.Level = normalizeLevel(fmt.Sprint(v))
			break
		}
	}
	for _, key := range []string{"message", "msg", "log.message"} {
		probe := e
		probe.Message = ""
		if v, ok := probe.Field(key); ok {
			e.Message = fmt.Sprint(v)
			break
		}
	}
	for _, key := range []string{"timestamp", "time", "ts", "@timestamp"} {
		if v, ok := e.Field(key); ok {
			if t, err := time.Parse(time.RFC3339Nano, fmt.Sprint(v)); err == nil {
				e.ApplicationTime = t
				break
			}
		}
	}
	return e
}

//nolint:gocritic // Value receiver is part of the query-facing Entry API and does not mutate state.
func (e Entry) Field(path string) (any, bool) {
	if path == "level" && e.Level != "" {
		return e.Level, true
	}
	if v, ok := e.Fields[path]; ok {
		return v, true
	}
	if path == "message" || path == "msg" {
		if e.Message != "" {
			return e.Message, true
		}
	}
	var v any = e.Fields
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func parseLogfmt(raw string) (map[string]any, error) {
	fields := map[string]any{}
	s := raw
	for strings.TrimSpace(s) != "" {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		end := strings.IndexByte(s, '=')
		if end <= 0 {
			return nil, fmt.Errorf("expected key=value")
		}
		key := s[:end]
		if strings.ContainsAny(key, " \t\r\n\"'") {
			return nil, fmt.Errorf("invalid key")
		}
		s = s[end+1:]
		var value string
		quoted := strings.HasPrefix(s, `"`)
		if quoted {
			i := 1
			for ; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == '"' {
					break
				}
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated value")
			}
			var err error
			value, err = strconv.Unquote(s[:i+1])
			if err != nil {
				return nil, err
			}
			s = s[i+1:]
			if s != "" && !unicode.IsSpace(rune(s[0])) {
				return nil, fmt.Errorf("expected separator")
			}
		} else {
			i := strings.IndexFunc(s, unicode.IsSpace)
			if i < 0 {
				i = len(s)
			}
			value = s[:i]
			s = s[i:]
			if strings.ContainsAny(value, "\"=") {
				return nil, fmt.Errorf("invalid unquoted value")
			}
		}
		var v any = value
		if !quoted {
			if value == "true" {
				v = true
			} else if value == "false" {
				v = false
			} else if value != "" && json.Valid([]byte(value)) && (value[0] == '-' || value[0] >= '0' && value[0] <= '9') {
				v = json.Number(value)
			}
		}
		fields[key] = v
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty logfmt")
	}
	return fields, nil
}
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, v := range x {
			m[k] = cloneValue(v)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = cloneValue(v)
		}
		return a
	}
	return v
}

//nolint:gocritic // Cloning by value is intentional and centralizes all reference-field copies.
func cloneEntry(e Entry) Entry {
	e.Timeline = append([]TimeCount(nil), e.Timeline...)
	if e.Fields != nil {
		e.Fields = cloneValue(e.Fields).(map[string]any)
	}
	e.OriginalLines = append([]string(nil), e.OriginalLines...)
	if e.Marker != nil {
		m := *e.Marker
		e.Marker = &m
	}
	return e
}
