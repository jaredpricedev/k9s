package logstream

import (
	"regexp"
	"strings"
	"unicode"
)

var pemRE = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?(?:-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|$)`)
var bearerRE = regexp.MustCompile(`(?i)\bBearer[ \t]+[^\s"'<>]+`)
var jwtRE = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var awsRE = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)

//nolint:gocritic // ECMA-48 defines these exact byte ranges for intermediate and final escape-sequence bytes.
var terminalRE = regexp.MustCompile("\x1b(?:\\[[0-?]*[\x20-\x2f]*[\x40-\x7e]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|[\x20-\x2f]*[\x40-\x7e])")

func Sanitize(s string) string {
	s = terminalRE.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || strings.ContainsRune("\u202e\u202d\u202a\u202b\u202c\u2066\u2067\u2068\u2069", r) {
			return -1
		}
		return r
	}, s)
}
func SafeText(s string) string {
	s = Sanitize(s)
	s = pemRE.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = bearerRE.ReplaceAllString(s, "Bearer [REDACTED]")
	s = jwtRE.ReplaceAllString(s, "[REDACTED JWT]")
	s = awsRE.ReplaceAllString(s, "[REDACTED AWS KEY]")
	return Sanitize(s)
}
func safeValue(v any) any {
	switch x := v.(type) {
	case string:
		return SafeText(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, v := range x {
			m[SafeText(k)] = safeValue(v)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = safeValue(v)
		}
		return a
	}
	return v
}

//nolint:gocritic // Public value API guarantees redaction never mutates caller-owned entries.
func SafeEntry(e Entry) Entry {
	e = cloneEntry(e)
	if e.Sensitive {
		e.Raw = "[REDACTED PRIVATE KEY CONTENT]"
		e.Message = e.Raw
		e.Fields = nil
		e.OriginalLines = []string{e.Raw}
	}
	e.Raw = SafeText(e.Raw)
	e.Message = SafeText(e.Message)
	e.Level = SafeText(e.Level)
	if e.Fields != nil {
		e.Fields = safeValue(e.Fields).(map[string]any)
	}
	e.OriginalLines = strings.Split(SafeText(strings.Join(e.OriginalLines, "\n")), "\n")
	e.Source.Cluster = SafeText(e.Source.Cluster)
	e.Source.Context = SafeText(e.Source.Context)
	e.Source.Namespace = SafeText(e.Source.Namespace)
	e.Source.Pod = SafeText(e.Source.Pod)
	e.Source.UID = SafeText(e.Source.UID)
	e.Source.Container = SafeText(e.Source.Container)
	if e.Marker != nil {
		e.Marker.Kind = SafeText(e.Marker.Kind)
		e.Marker.Message = SafeText(e.Marker.Message)
		e.Marker.Origin = SafeText(e.Marker.Origin)
	}
	return e
}

// Redactor carries private-key state across source-local line/group/timeout boundaries.
// If active private-key sources exceed its cap it fails closed for the rest of its lifetime.
// Use one instance per stream pipeline; Engine and Recorder own independent instances.
type Redactor struct {
	active map[string]bool
	limit  int
	closed bool
}

func NewRedactor(limit int) *Redactor {
	if limit <= 0 {
		limit = 1024
	}
	return &Redactor{active: map[string]bool{}, limit: limit}
}

var pemDelimiter = regexp.MustCompile(`-{5}(?:BEGIN|END) (?:[A-Z0-9]+ )*PRIVATE KEY-{5}`)

//nolint:gocritic // Tracking returns an isolated value while retaining only bounded source state.
func (r *Redactor) Track(e Entry) Entry {
	key := e.Source.Key()
	e.Sensitive = e.Sensitive || r.closed || r.active[key]
	// Detection must see the same normalized text as SafeText. Process every
	// delimiter so a closed block followed by another BEGIN leaves state open.
	for _, delimiter := range pemDelimiter.FindAllString(Sanitize(e.Raw), -1) {
		if strings.HasPrefix(delimiter, "-----BEGIN ") {
			e.Sensitive = true
			if len(r.active) >= r.limit && !r.active[key] {
				r.closed = true
			} else {
				r.active[key] = true
			}
		} else {
			delete(r.active, key)
		}
	}
	return e
}

//nolint:gocritic // Public value API composes tracking and redaction without caller mutation.
func (r *Redactor) Transform(e Entry) Entry { return SafeEntry(r.Track(e)) }
