// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MatchOp is a PromQL label-matching operator.
type MatchOp string

const (
	MatchEqual    MatchOp = "="
	MatchNotEqual MatchOp = "!="
	MatchRegex    MatchOp = "=~"
	MatchNotRegex MatchOp = "!~"
)

// Matcher constrains one label. Value is always written as an escaped
// string literal, so text from the cluster cannot change the query shape.
// For the regex operators Value is a regular expression: pass untrusted
// text through RegexQuote first.
type Matcher struct {
	Label string
	Op    MatchOp // empty means MatchEqual
	Value string
}

// Eq is the exact matcher label="value".
func Eq(label, value string) Matcher { return Matcher{Label: label, Op: MatchEqual, Value: value} }

// Re is the regex matcher label=~"pattern".
func Re(label, pattern string) Matcher { return Matcher{Label: label, Op: MatchRegex, Value: pattern} }

// RegexQuote escapes text for use inside a Re pattern.
func RegexQuote(s string) string { return regexp.QuoteMeta(s) }

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]{0,199}$`)
	labelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)
)

// Expr is a PromQL expression that can only be built with the functions in
// this file. There is deliberately no way to wrap a raw string. A building
// mistake is carried in Err and refused by Query and QueryRange.
type Expr struct {
	text  string
	err   error
	rng   bool // a range vector, e.g. metric{...}[300s]
	plain bool // a bare selector, the only thing Range accepts
}

// MaxAuthoredQuery bounds the text AuthoredExpr accepts.
const MaxAuthoredQuery = 4096

// AuthoredExpr wraps PromQL that a person wrote as a whole: the query of a
// Grafana dashboard panel written by the owner of a declared source, or the
// expression the operator typed into :query or saved under a name in their
// own context configuration, after its variables were substituted. It is
// the one exception to building queries from typed parts, and it exists
// only for those two readers; never pass text assembled from cluster object
// names, labels or anything a source returned. Values put in place of a
// variable must be escaped by the caller (RegexQuote for a regex
// matcher). Text with a control character, an unsubstituted
// "$" variable or more than MaxAuthoredQuery bytes is refused. The source
// only ever receives it in a GET.
func AuthoredExpr(text string) Expr {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return invalid("empty expression")
	case len(text) > MaxAuthoredQuery:
		return invalid("authored query exceeds %d bytes", MaxAuthoredQuery)
	case strings.Contains(text, "$"):
		return invalid("authored query still holds a $ variable")
	}
	for _, r := range text {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return invalid("authored query holds a control character")
		}
	}
	return Expr{text: text}
}

// String returns the PromQL text, or "" for an invalid expression.
func (e Expr) String() string {
	if e.err != nil {
		return ""
	}
	return e.text
}

// Err reports why the expression is invalid, or nil.
func (e Expr) Err() error {
	if e.err == nil && e.text == "" {
		return errors.New("empty expression")
	}
	return e.err
}

func invalid(format string, args ...any) Expr { return Expr{err: fmt.Errorf(format, args...)} }

func instant(e Expr) error {
	if err := e.Err(); err != nil {
		return err
	}
	if e.rng {
		return errors.New("a range vector needs Rate, Increase or OverTime")
	}
	return nil
}

func matchers(ms []Matcher) (string, error) {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		op := m.Op
		if op == "" {
			op = MatchEqual
		}
		if op != MatchEqual && op != MatchNotEqual && op != MatchRegex && op != MatchNotRegex {
			return "", fmt.Errorf("unsupported matcher operator %q", string(op))
		}
		if !labelName.MatchString(m.Label) {
			return "", fmt.Errorf("invalid label name %q", m.Label)
		}
		if len(m.Value) > 1024 || strings.ContainsAny(m.Value, "\x00") {
			return "", errors.New("matcher value exceeds limit")
		}
		parts = append(parts, m.Label+string(op)+strconv.Quote(m.Value))
	}
	return strings.Join(parts, ","), nil
}

// Select is the instant selector metric{matchers}.
func Select(metric string, ms ...Matcher) Expr {
	if !metricName.MatchString(metric) {
		return invalid("invalid metric name %q", metric)
	}
	body, err := matchers(ms)
	if err != nil {
		return Expr{err: err}
	}
	return Expr{text: metric + "{" + body + "}", plain: true}
}

func duration(d time.Duration) (string, error) {
	if d < time.Second || d > MaxWindow {
		return "", errors.New("range must be between one second and seven days")
	}
	return strconv.FormatInt(int64(math.Ceil(d.Seconds())), 10) + "s", nil
}

func ranged(fn string, e Expr, window time.Duration) Expr {
	if err := e.Err(); err != nil {
		return Expr{err: err}
	}
	if !e.plain {
		return invalid("%s needs a Select expression", fn)
	}
	d, err := duration(window)
	if err != nil {
		return Expr{err: err}
	}
	return Expr{text: fn + "(" + e.text + "[" + d + "])"}
}

// Rate is rate(selector[window]): per-second increase of a counter.
func Rate(sel Expr, window time.Duration) Expr { return ranged("rate", sel, window) }

// Increase is increase(selector[window]).
func Increase(sel Expr, window time.Duration) Expr { return ranged("increase", sel, window) }

// OverTime is fn_over_time(selector[window]) for fn in max, min, avg.
func OverTime(fn string, sel Expr, window time.Duration) Expr {
	if fn != "max" && fn != "min" && fn != "avg" {
		return invalid("unsupported over-time function %q", fn)
	}
	return ranged(fn+"_over_time", sel, window)
}

// Aggregate is op by (labels) (e) for op in sum, avg, max, min, count.
// With no labels the whole vector is aggregated into one series.
func Aggregate(op string, e Expr, by ...string) Expr {
	switch op {
	case "sum", "avg", "max", "min", "count":
	default:
		return invalid("unsupported aggregation %q", op)
	}
	if err := instant(e); err != nil {
		return Expr{err: err}
	}
	for _, l := range by {
		if !labelName.MatchString(l) {
			return invalid("invalid label name %q", l)
		}
	}
	if len(by) == 0 {
		return Expr{text: op + "(" + e.text + ")"}
	}
	return Expr{text: op + " by (" + strings.Join(by, ",") + ") (" + e.text + ")"}
}

// Sum is Aggregate("sum", e, by...).
func Sum(e Expr, by ...string) Expr { return Aggregate("sum", e, by...) }

// TopK is topk(k, e) with k between 1 and MaxSeries.
func TopK(k int, e Expr) Expr {
	if k < 1 || k > MaxSeries {
		return invalid("topk needs k between 1 and %d", MaxSeries)
	}
	if err := instant(e); err != nil {
		return Expr{err: err}
	}
	return Expr{text: "topk(" + strconv.Itoa(k) + ", " + e.text + ")"}
}

// HistogramQuantile is histogram_quantile(q, e); e must keep the le label,
// for example Sum(Rate(Select("..._bucket", ...), w), "le").
func HistogramQuantile(q float64, e Expr) Expr {
	if math.IsNaN(q) || q < 0 || q > 1 {
		return invalid("quantile must be between 0 and 1")
	}
	if err := instant(e); err != nil {
		return Expr{err: err}
	}
	return Expr{text: "histogram_quantile(" + strconv.FormatFloat(q, 'g', -1, 64) + ", " + e.text + ")"}
}

// Scalar is a finite number literal.
func Scalar(v float64) Expr {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return invalid("scalar must be finite")
	}
	return Expr{text: strconv.FormatFloat(v, 'g', -1, 64)}
}

// Binary is (a op b) for op in + - * /. Vector operands are matched by
// Prometheus one-to-one on identical label sets, so aggregate both sides by
// the same labels first.
func Binary(op string, a, b Expr) Expr {
	switch op {
	case "+", "-", "*", "/":
	default:
		return invalid("unsupported operator %q", op)
	}
	if err := instant(a); err != nil {
		return Expr{err: err}
	}
	if err := instant(b); err != nil {
		return Expr{err: err}
	}
	return Expr{text: "(" + a.text + " " + op + " " + b.text + ")"}
}

// Div is Binary("/", a, b).
func Div(a, b Expr) Expr { return Binary("/", a, b) }

// StepFor chooses the query_range step for a window: the smallest step from
// a fixed ladder (15s … 6h) that asks for at most MaxPoints sample times.
func StepFor(window time.Duration) time.Duration {
	ladder := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 6 * time.Hour}
	for _, step := range ladder {
		if window/step <= MaxPoints {
			return step
		}
	}
	return ladder[len(ladder)-1]
}
