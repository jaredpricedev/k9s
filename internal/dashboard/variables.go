// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dashboard

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
)

// Selection is the Kubernetes object the page is opened for.
type Selection struct {
	Namespace, Pod, Workload, Node string
}

// Mapping names the dashboard variables that stand for the selection, as
// declared per dashboard kind under k9s.observability.grafana.dashboards.
type Mapping struct {
	Namespace, Pod, Workload, Node string
}

// ConventionalMapping is used for a dashboard that is not declared: the
// variable names most Kubernetes dashboards use, which are also what a pin
// snippet proposes.
var ConventionalMapping = Mapping{Namespace: "namespace", Pod: "pod", Workload: "workload", Node: "node"}

// Where a value comes from, in the words the page shows.
const (
	OriginSelection = "selection"
	OriginSaved     = "saved in dashboard"
	OriginAll       = "All"
	OriginConstant  = "constant"
	OriginWindow    = "window"
	OriginPrefix    = "name prefix of the workload"
	OriginAny       = "no pod selected"
)

// Binding is what one variable stands for in this read.
type Binding struct {
	Name string
	// Values are literal values. They are escaped where they are put: as a
	// string in a = matcher, as a quoted regular expression in a =~ one.
	Values []string
	// Pattern is a regular expression that stands for the variable: the
	// dashboard's allValue, or a name prefix. It is only valid in a =~ or
	// !~ matcher.
	Pattern string
	// Authored says the values are regular expressions the dashboard
	// author wrote (a single-value variable), so they are not quoted.
	Authored bool
	Origin   string
	// Unresolved is why the variable has no value. A query that uses it
	// is not sent.
	Unresolved string
}

// Display writes the binding for a status line: "namespace=gitlab
// (selection)".
func (b *Binding) Display() string {
	switch {
	case b.Unresolved != "":
		return b.Name + " not resolved"
	case b.Pattern != "":
		return b.Name + "=~" + b.Pattern + " (" + b.Origin + ")"
	}
	return b.Name + "=" + strings.Join(b.Values, "|") + " (" + b.Origin + ")"
}

// Bindings are the values of every variable for one read.
type Bindings struct {
	List []Binding
	// Step is the spacing of samples, Window the range drawn and Rate the
	// range $__rate_interval stands for.
	Step, Window, Rate time.Duration
	// Missing names mapped variables that the selection has a value for
	// and the dashboard does not define.
	Missing []string
}

// RateInterval is what $__rate_interval stands for: the range :slow,
// :graph and :impact take a rate over for the same step (four steps and at
// least two minutes), so that a query here and the same reading there show
// the same numbers. Grafana derives it from the scrape interval of the
// datasource, which is not read here; this value is at least as wide for
// scrape intervals up to 30 seconds, so a rate always has samples.
func RateInterval(step time.Duration) time.Duration {
	return impact.RateWindowFor(step)
}

// Find returns the binding of a variable, or nil.
func (b *Bindings) Find(name string) *Binding {
	for i := range b.List {
		if b.List[i].Name == name {
			return &b.List[i]
		}
	}
	return nil
}

func (b *Bindings) set(binding *Binding) {
	if existing := b.Find(binding.Name); existing != nil {
		*existing = *binding
		return
	}
	b.List = append(b.List, *binding)
}

func escapeString(text string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
}

// Bind decides what every variable of a dashboard stands for. A variable
// the mapping names takes the selection's value. Any other keeps the value
// saved in the dashboard: a constant, a custom or text value, an interval,
// or the saved choice of a query-backed variable. All becomes the
// variable's allValue; All without one cannot be resolved, because its
// values come from a query that is not run.
func Bind(d *Dashboard, m Mapping, sel Selection, window, step time.Duration) Bindings {
	b := Bindings{Step: step, Window: window, Rate: RateInterval(step)}
	chosen := map[string]string{}
	for _, pair := range [][2]string{{m.Namespace, sel.Namespace}, {m.Pod, sel.Pod}, {m.Workload, sel.Workload}, {m.Node, sel.Node}} {
		if pair[0] != "" && pair[1] != "" {
			chosen[pair[0]] = pair[1]
		}
	}
	for i := range d.Variables {
		v := &d.Variables[i]
		if v.Name == "" {
			continue
		}
		if value, ok := chosen[v.Name]; ok {
			b.set(&Binding{Name: v.Name, Values: []string{value}, Origin: OriginSelection})
			delete(chosen, v.Name)
			continue
		}
		saved := savedBinding(v, step)
		b.set(&saved)
	}
	// A mapped variable the definition does not list fills nothing; it is
	// named so that a wrong declaration can be seen.
	for _, name := range []string{m.Namespace, m.Pod, m.Workload, m.Node} {
		if _, ok := chosen[name]; ok {
			b.Missing = append(b.Missing, name)
			delete(chosen, name)
		}
	}
	return b
}

func savedBinding(v *Variable, step time.Duration) Binding {
	out := Binding{Name: v.Name, Origin: OriginSaved}
	all := false
	for _, value := range v.Current {
		all = all || value == AllToken
	}
	switch v.Type {
	case "constant":
		out.Origin, out.Authored = OriginConstant, true
		out.Values = v.Current
		if len(out.Values) == 0 && v.Query != "" {
			out.Values = []string{v.Query}
		}
	case "custom", "textbox", typeQuery, typeDatasource:
		switch {
		case all && v.AllValue != "":
			out.Pattern, out.Origin = v.AllValue, OriginAll
		case all && v.Type == "custom" && len(v.Options) > 0:
			out.Values, out.Origin = v.Options, OriginAll
		case all:
			out.Unresolved = "it is set to All without an allValue, and its values come from a query that is not run here"
		case len(v.Current) > 0:
			out.Values = v.Current
			out.Authored = !v.Multi && !v.IncludeAll && v.Type != typeQuery
		case v.Type == "textbox" && v.Query != "":
			out.Values = []string{v.Query}
		default:
			out.Unresolved = "no value is saved in the dashboard, and its values come from a query that is not run here"
			if v.Type != typeQuery {
				out.Unresolved = "no value is saved in the dashboard"
			}
		}
	case "interval":
		out.Authored = true
		switch {
		case len(v.Current) == 1 && !strings.HasPrefix(v.Current[0], "$__auto"):
			out.Values = v.Current
		default:
			out.Values, out.Origin = []string{durationText(step)}, OriginWindow
		}
	default:
		out.Unresolved = "variable type " + strconv.Quote(v.Type) + " is not supported"
	}
	if out.Unresolved == "" && out.Pattern == "" && len(out.Values) == 0 {
		out.Unresolved = "no value is saved in the dashboard"
	}
	return out
}

// SelectionBindings are the variables of the :query bar: $namespace, $pod,
// $workload and $node from the selection. Without a selected Pod, $pod is a
// pattern: the Pods whose name starts with the selected workload's name, or
// any Pod. That is a name match, not a read of who owns which Pod, and the
// page says so.
func SelectionBindings(sel Selection, window, step time.Duration) Bindings {
	b := Bindings{Step: step, Window: window, Rate: RateInterval(step)}
	literal := func(name, value, missing string) {
		if value == "" {
			b.set(&Binding{Name: name, Unresolved: missing})
			return
		}
		b.set(&Binding{Name: name, Values: []string{value}, Origin: OriginSelection})
	}
	literal("namespace", sel.Namespace, "nothing with a namespace is selected")
	switch {
	case sel.Pod != "":
		literal("pod", sel.Pod, "")
	case sel.Workload != "":
		b.set(&Binding{Name: "pod", Pattern: escapeString(observability.RegexQuote(sel.Workload)) + "-.+", Origin: OriginPrefix})
	default:
		b.set(&Binding{Name: "pod", Pattern: ".+", Origin: OriginAny})
	}
	literal("workload", sel.Workload, "no workload is selected")
	literal("node", sel.Node, "no node is selected or known for the selection")
	return b
}

// durationText writes a duration the way PromQL ranges are written, in one
// unit.
func durationText(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d >= time.Second && d%time.Second == 0:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

func (b *Bindings) builtin(name string) (string, bool) {
	switch name {
	case "__interval":
		return durationText(b.Step), true
	case "__interval_ms":
		return strconv.FormatInt(b.Step.Milliseconds(), 10), true
	case "__rate_interval":
		return durationText(b.Rate), true
	case "__rate_interval_ms":
		return strconv.FormatInt(b.Rate.Milliseconds(), 10), true
	case "__range":
		return durationText(b.Window), true
	case "__range_s":
		return strconv.FormatInt(int64(b.Window/time.Second), 10), true
	case "__range_ms":
		return strconv.FormatInt(b.Window.Milliseconds(), 10), true
	}
	return "", false
}

var bareValue = regexp.MustCompile(`^[A-Za-z0-9_:.]+$`)

// where a variable stands in the expression.
type place struct {
	quote rune // 0 outside a string
	regex bool // the string is the operand of =~ or !~
}

func (b *Bindings) value(name, format string, at place) (string, error) {
	if text, ok := b.builtin(name); ok {
		return text, nil
	}
	binding := b.Find(name)
	if binding == nil {
		return "", fmt.Errorf("variable $%s is not defined", name)
	}
	if binding.Unresolved != "" {
		return "", fmt.Errorf("variable $%s is not resolved: %s", name, binding.Unresolved)
	}
	quote := func(text string) (string, error) {
		switch at.quote {
		case '"':
			return escapeString(text), nil
		case '\'':
			return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(text), nil
		}
		if strings.Contains(text, "`") {
			return "", fmt.Errorf("the value of $%s holds a backquote", name)
		}
		return text, nil
	}
	switch format {
	case "", "regex":
	case "pipe":
		if binding.Pattern != "" {
			return binding.Pattern, nil
		}
		return quote(strings.Join(binding.Values, "|"))
	case "raw", "text", "csv":
		if binding.Pattern != "" {
			return binding.Pattern, nil
		}
		return quote(strings.Join(binding.Values, ","))
	default:
		return "", fmt.Errorf("the format :%s of $%s is not supported", format, name)
	}
	regex := at.regex || format == "regex"
	switch {
	case at.quote == 0:
		if binding.Pattern != "" || len(binding.Values) != 1 || !bareValue.MatchString(binding.Values[0]) {
			return "", fmt.Errorf("the value of $%s cannot stand outside a quoted string", name)
		}
		return binding.Values[0], nil
	case binding.Pattern != "":
		if !regex {
			return "", fmt.Errorf("$%s stands for a pattern (%s), which only a =~ matcher can take", name, binding.Origin)
		}
		return binding.Pattern, nil
	case !regex:
		if len(binding.Values) != 1 {
			return "", fmt.Errorf("$%s has %d values, which only a =~ matcher can take", name, len(binding.Values))
		}
		return quote(binding.Values[0])
	}
	parts := make([]string, 0, len(binding.Values))
	for _, value := range binding.Values {
		if !binding.Authored {
			value = observability.RegexQuote(value)
		}
		quoted, err := quote(value)
		if err != nil {
			return "", err
		}
		parts = append(parts, quoted)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, "|") + ")", nil
}

func identStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func identPart(c byte) bool { return identStart(c) || (c >= '0' && c <= '9') }

func identifier(name string) bool {
	if name == "" || !identStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !identPart(name[i]) {
			return false
		}
	}
	return true
}

// Expand puts the values of the variables into an expression. It knows
// $name, ${name}, ${name:format} and [[name]], and Grafana's
// $__rate_interval, $__interval and $__range families. A value is escaped
// for the place it lands in: inside a quoted string, and as a regular
// expression when that string is the operand of =~ or !~. Any variable
// without a value is an error that names it; nothing is guessed.
func (b *Bindings) Expand(expr string) (string, error) {
	var out strings.Builder
	at := place{}
	for i := 0; i < len(expr); {
		c := expr[i]
		// Track quoted strings, and what stands in front of each.
		switch {
		case at.quote != 0 && c == '\\' && at.quote != '`' && i+1 < len(expr):
			out.WriteString(expr[i : i+2])
			i += 2
			continue
		case at.quote != 0 && rune(c) == at.quote:
			at = place{}
			out.WriteByte(c)
			i++
			continue
		case at.quote == 0 && (c == '"' || c == '\'' || c == '`'):
			before := strings.TrimRight(out.String(), " \t\n")
			at = place{quote: rune(c), regex: strings.HasSuffix(before, "=~") || strings.HasSuffix(before, "!~")}
			out.WriteByte(c)
			i++
			continue
		}
		name, format, length := "", "", 0
		switch {
		case c == '$' && i+1 < len(expr) && expr[i+1] == '{':
			end := strings.IndexByte(expr[i:], '}')
			if end < 0 {
				return "", errors.New("a ${ is not closed")
			}
			name, format, _ = strings.Cut(expr[i+2:i+end], ":")
			name, _, _ = strings.Cut(name, ".")
			if !identifier(name) {
				return "", errors.New("a ${ is not closed")
			}
			length = end + 1
		case c == '$' && i+1 < len(expr) && identStart(expr[i+1]):
			j := i + 1
			for j < len(expr) && identPart(expr[j]) {
				j++
			}
			name, length = expr[i+1:j], j-i
		case c == '[' && i+2 < len(expr) && expr[i+1] == '[' && identStart(expr[i+2]):
			end := strings.Index(expr[i:], "]]")
			if end < 0 {
				return "", errors.New("a [[ is not closed")
			}
			name, format, _ = strings.Cut(expr[i+2:i+end], ":")
			if !identifier(name) {
				return "", errors.New("a [[ is not closed")
			}
			length = end + 2
		}
		if length == 0 {
			if c == '$' {
				return "", errors.New("it holds a literal $, which is not sent (a matcher is anchored without it)")
			}
			out.WriteByte(c)
			i++
			continue
		}
		text, err := b.value(name, format, at)
		if err != nil {
			return "", err
		}
		out.WriteString(text)
		i += length
	}
	return out.String(), nil
}
