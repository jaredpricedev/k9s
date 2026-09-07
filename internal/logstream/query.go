package logstream

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

type comparison struct {
	path, op string
	value    any
}
type Query struct {
	expression  string
	regex       *regexp.Regexp
	comparisons []comparison
	exclusions  []string
}

var clauseRE = regexp.MustCompile(`^([a-zA-Z_@][a-zA-Z0-9_.@-]*)\s*(>=|<=|!=|==|=|>|<)\s*`)
var typedRE = regexp.MustCompile(`^[a-zA-Z_@][a-zA-Z0-9_.@-]*\s*[=!<>]`)

func CompileQuery(expression string, exclusions []string) (*Query, error) {
	if len(expression) > 16384 {
		return nil, fmt.Errorf("query exceeds 16384 bytes")
	}
	if err := validateRules(exclusions); err != nil {
		return nil, err
	}
	q := &Query{expression: expression, exclusions: append([]string(nil), exclusions...)}
	s := strings.TrimSpace(expression)
	if s == "" {
		return q, nil
	}
	if !typedRE.MatchString(s) {
		r, err := regexp.Compile(s)
		if err != nil {
			return nil, err
		}
		q.regex = r
		return q, nil
	}
	for s != "" {
		m := clauseRE.FindStringSubmatch(s)
		if m == nil {
			return nil, fmt.Errorf("invalid field comparison near %q", s)
		}
		s = s[len(m[0]):]
		if s == "" {
			return nil, fmt.Errorf("missing value for %s", m[1])
		}
		var v any
		if s[0] == '"' {
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
				return nil, fmt.Errorf("unterminated quoted value")
			}
			value, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return nil, err
			}
			v = value
			s = s[i+1:]
		} else {
			end := strings.IndexAny(s, " \t\r\n")
			if end < 0 {
				end = len(s)
			}
			value := s[:end]
			if strings.ContainsAny(value, "<>=!\"") {
				return nil, fmt.Errorf("invalid comparison value %q", value)
			}
			v = value
			if value == "true" {
				v = true
			} else if value == "false" {
				v = false
			} else if _, ok := new(big.Rat).SetString(value); ok {
				v = json.Number(value)
			}
			s = s[end:]
		}
		q.comparisons = append(q.comparisons, comparison{m[1], m[2], v})
		if len(q.comparisons) > 128 {
			return nil, fmt.Errorf("too many comparisons")
		}
		if s != "" && s[0] != ' ' && s[0] != '\t' && s[0] != '\n' && s[0] != '\r' {
			return nil, fmt.Errorf("expected conjunction")
		}
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "AND ") || strings.HasPrefix(s, "and ") {
			s = strings.TrimSpace(s[4:])
			if s == "" {
				return nil, fmt.Errorf("missing comparison after AND")
			}
		}
	}
	return q, nil
}
func numeric(v any) (*big.Rat, bool) {
	switch n := v.(type) {
	case json.Number:
		r, ok := new(big.Rat).SetString(string(n))
		return r, ok
	case float64:
		r := new(big.Rat).SetFloat64(n)
		return r, r != nil
	case int:
		return new(big.Rat).SetInt64(int64(n)), true
	case int64:
		return new(big.Rat).SetInt64(n), true
	}
	return nil, false
}
func compare(a, b any, path string) (int, bool) {
	if path == "level" || path == "severity" || path == "log.level" {
		sa, sb := Severity(fmt.Sprint(a)), Severity(fmt.Sprint(b))
		if sa > 0 && sb > 0 {
			return sa - sb, true
		}
	}
	if an, ok := numeric(a); ok {
		bn, ok := numeric(b)
		if !ok {
			return 0, false
		}
		return an.Cmp(bn), true
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(av, bv), true
	case bool:
		bv, ok := b.(bool)
		if !ok {
			return 0, false
		}
		if av == bv {
			return 0, true
		}
		if av {
			return 1, true
		}
		return -1, true
	}
	return 0, false
}

//nolint:gocritic // Exported value API evaluates without retaining or mutating the caller's entry.
func (q *Query) Match(e Entry) bool {
	if q == nil {
		return true
	}
	for _, s := range q.exclusions {
		if strings.Contains(e.Raw, s) {
			return false
		}
	}
	if q.regex != nil && !q.regex.MatchString(e.Raw) {
		return false
	}
	for _, c := range q.comparisons {
		a, ok := e.Field(c.path)
		if !ok {
			return false
		}
		n, ok := compare(a, c.value, c.path)
		if !ok {
			return false
		}
		switch c.op {
		case "=", "==":
			if n != 0 {
				return false
			}
		case "!=":
			if n == 0 {
				return false
			}
		case ">":
			if n <= 0 {
				return false
			}
		case "<":
			if n >= 0 {
				return false
			}
		case ">=":
			if n < 0 {
				return false
			}
		case "<=":
			if n > 0 {
				return false
			}
		}
	}
	return true
}
