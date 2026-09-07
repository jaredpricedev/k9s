package logstream

import (
	"encoding/json"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Bucket struct {
	Start             time.Time
	Observed, Visible uint64
	Severity          int
	EntryID           uint64
}
type View struct {
	Entries   []Entry
	Hidden    uint64
	Histogram []Bucket
	Stats     Stats
}

func (e *Engine) View(q *Query, now time.Time) View {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := View{Stats: e.stats, Histogram: make([]Bucket, 60)}
	v.Stats.Retained = e.size
	start := now.Truncate(time.Second).Add(-59 * time.Second)
	for i := range v.Histogram {
		v.Histogram[i].Start = start.Add(time.Duration(i) * time.Second)
	}
	entries := e.snapshot()
	for i := range entries {
		entry := &entries[i]
		visible := q.Match(*entry)
		if visible {
			v.Entries = append(v.Entries, *entry)
		} else {
			v.Hidden += entry.Occurrences
		}
		if entry.Marker != nil {
			continue
		}
		timeline := entry.Timeline
		if len(timeline) == 0 {
			timeline = []TimeCount{{entry.RuntimeTime, entry.Occurrences}}
		}
		for _, tc := range timeline {
			idx := int(tc.Time.Truncate(time.Second).Sub(start) / time.Second)
			if tc.Time.Before(start) || idx < 0 || idx >= len(v.Histogram) {
				continue
			}
			b := &v.Histogram[idx]
			b.Observed += tc.Count
			if visible {
				b.Visible += tc.Count
				if b.EntryID == 0 {
					b.EntryID = entry.ID
				}
			}
			if s := Severity(entry.Level); s > b.Severity {
				b.Severity = s
			}
		}
	}
	return v
}

type Pattern struct {
	Template, Level, Key string
	Count                uint64
	Entries              []Entry
}

var uuidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
var hexPattern = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{16,}\b`)
var ipPattern = regexp.MustCompile(`[0-9a-fA-F:.]+`)

func PatternTemplate(raw string) string {
	s := uuidPattern.ReplaceAllString(raw, "<uuid>")
	s = hexPattern.ReplaceAllStringFunc(s, func(v string) string {
		if strings.ContainsAny(strings.ToLower(v), "abcdefx") {
			return "<hex>"
		}
		return v
	})
	s = ipPattern.ReplaceAllStringFunc(s, func(v string) string {
		if net.ParseIP(v) != nil {
			return "<ip>"
		}
		return v
	})
	return s
}
func TopPatterns(entries []Entry, limit int) []Pattern {
	if limit <= 0 {
		return nil
	}
	groups := map[string]*Pattern{}
	for i := range entries {
		e := &entries[i]
		if e.Marker != nil {
			continue
		}
		identity := EntryPattern(*e)
		template, key := identity.Template, identity.Key
		p := groups[key]
		if p == nil {
			p = &Pattern{Template: template, Level: e.Level, Key: key}
			groups[key] = p
		}
		p.Count += e.Occurrences
		p.Entries = append(p.Entries, cloneEntry(*e))
	}
	out := make([]Pattern, 0, len(groups))
	for _, p := range groups {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Level+out[i].Template < out[j].Level+out[j].Template
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// EntryPattern is the shared aggregation/drilldown identity. Structured messages
// omit incidental envelope fields (time, request IDs and attempt counters), while
// diagnostic status/code fields stay explicit and distinct. Numeric content in
// the message is preserved by PatternTemplate. Raw/missing-message records retain
// their entire original text.
//
//nolint:gocritic // Exported value API computes identity without mutating the caller's entry.
func EntryPattern(e Entry) Pattern {
	message := e.Raw
	if e.Format != "raw" && e.Message != "" {
		message = e.Message
	}
	template := PatternTemplate(message)
	type diagnostic struct {
		Path  string
		Value any
	}
	var values []diagnostic
	for _, path := range []string{"http.status", "http.status_code", "http.response.status_code", "status", "status_code", "statusCode", "code"} {
		if value, ok := e.Field(path); ok {
			values = append(values, diagnostic{path, value})
		}
	}
	encoded, _ := json.Marshal(values)
	key := e.Level + "\x00" + template + "\x00" + string(encoded)
	for _, value := range values {
		data, _ := json.Marshal(value.Value)
		template += " · " + value.Path + "=" + string(data)
	}
	return Pattern{Template: template, Level: e.Level, Key: key}
}
