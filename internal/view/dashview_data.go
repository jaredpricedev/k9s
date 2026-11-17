// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/observability"
)

const (
	// dashDeadline bounds one whole read of a page: the dashboard
	// definition and every query of its panels.
	dashDeadline = 45 * time.Second
	// dashNameWidth is the longest a line's name is in a legend.
	dashNameWidth = 28
	// dashNotRead is the state of a query that was never sent or answered.
	dashNotRead = "not read"
)

// dashSpec says what a page draws: a Grafana dashboard by UID, one query,
// or the board of named queries.
type dashSpec struct {
	// UID is the Grafana dashboard; empty for a query page.
	UID string
	// Hint is the title known before the definition is read.
	Hint string
	// For is the kind the dashboard is declared for; Mapping names its
	// variables. Declared is false for one found by search, whose
	// variables are matched by their conventional names.
	For      string
	Mapping  dashboard.Mapping
	Declared bool
	// Query is the one query of a query page; Board draws every named one.
	Query *dashboard.Named
	Board bool
}

func (s *dashSpec) isQuery() bool { return s.UID == "" }

// noun is what the page calls itself in its first line.
func (s *dashSpec) noun() string {
	switch {
	case !s.isQuery():
		return "Dashboard"
	case s.Board:
		return "Queries"
	}
	return "Query"
}

// dashSnapshot is one retained read: the definition, what was planned for
// it, and each answer.
type dashSnapshot struct {
	Spec dashSpec
	// Label names the selection the variables were filled from.
	Label string
	Dash  *dashboard.Dashboard
	// Grafana is the read of the definition; nil for a query page.
	Grafana *observability.Status
	// ParseErr is why Grafana's answer could not be used.
	ParseErr string
	Plan     dashboard.Plan
	Results  map[string]observability.PromResult
	// Prom is set when no Prometheus source could be asked at all.
	Prom               *observability.Status
	Source             string
	Window, Step       time.Duration
	Start, End, ReadAt time.Time
}

func dashKey(panel int, refID string) string { return strconv.Itoa(panel) + "/" + refID }

func newDashSnapshot(spec *dashSpec, label string, window time.Duration, now time.Time) *dashSnapshot {
	start, end, step := graphFrameTimes(now, window)
	return &dashSnapshot{Spec: *spec, Label: label, Window: window, Step: step, Start: start, End: end, ReadAt: now.UTC(),
		Results: make(map[string]observability.PromResult)}
}

// bind decides the variables and the queries of the read.
func (s *dashSnapshot) bind(sel dashboard.Selection) {
	var b dashboard.Bindings
	if s.Spec.isQuery() {
		b = dashboard.SelectionBindings(sel, s.Window, s.Step)
	} else {
		b = dashboard.Bind(s.Dash, s.Spec.Mapping, sel, s.Window, s.Step)
	}
	s.Plan = dashboard.BuildPlan(s.Dash, b)
}

// run sends every planned query, a few at a time, and stops when ctx ends.
// It contacts nothing but the given source.
func (s *dashSnapshot) run(ctx context.Context, q graphQuerier) {
	s.Source = q.Display()
	type job struct {
		key  string
		expr observability.Expr
	}
	var jobs []job
	for i := range s.Plan.Panels {
		for j := range s.Plan.Panels[i].Queries {
			if query := &s.Plan.Panels[i].Queries[j]; s.Plan.Panels[i].NotDrawn == "" && query.Sent() {
				jobs = append(jobs, job{dashKey(i, query.RefID), query.Expr})
			}
		}
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		gate = make(chan struct{}, graphParallel)
	)
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			var res observability.PromResult
			if ctx.Err() == nil {
				res = q.QueryRange(ctx, j.expr, s.Start, s.End)
			}
			mu.Lock()
			s.Results[j.key] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
}

// loadDash plans and reads one page from a definition that is already
// parsed.
func loadDash(ctx context.Context, q graphQuerier, d *dashboard.Dashboard, spec *dashSpec, label string, sel dashboard.Selection,
	window time.Duration, now time.Time) *dashSnapshot {
	s := newDashSnapshot(spec, label, window, now)
	s.Dash = d
	s.bind(sel)
	s.run(ctx, q)
	return s
}

// dashStateWord says how one query went when it drew nothing, in words a
// tile can carry. An answer that matched nothing is never a zero.
func dashStateWord(res *observability.PromResult) string {
	switch {
	case res.State == "":
		// Planned and never sent: the deadline of the whole page ended while
		// earlier queries were still being answered.
		return dashNotRead + ": the page's " + graphSpan(dashDeadline) + " deadline ended first; r reads again"
	case res.Error != "":
		// The source's own words, cleaned and bounded by the client.
		return "expression failed: " + safeProviderText(res.Error)
	case !res.OK():
		word := string(res.State)
		if res.Reason != "" {
			word += " (" + res.Reason + ")"
		}
		return word + ": nothing was read"
	case graphSamples(res) == 0:
		return "no series matched"
	}
	return ""
}

// dashLine is one drawn series of a panel.
type dashLine struct {
	// Name is the short legend name, Full the whole one.
	Name, Full, RefID string
	Points            []chart.Point
	Raw               []observability.Sample
}

// dashData is what a panel's answers amount to.
type dashData struct {
	Lines []dashLine
	// Cut counts series beyond the lines one chart draws.
	Cut int
	// States holds, per sent query that drew nothing, why.
	States []string
	// Problem is what the tile says instead of a plot when no line was
	// drawn: the state all its queries share, or the first one.
	Problem string
	// Incomplete names answers a limit cut.
	Incomplete []string
}

// data turns the answers of one panel into lines, named as the dashboard's
// legend says and scaled into the panel's unit.
func (s *dashSnapshot) data(index int, ascii bool) dashData {
	var out dashData
	if s.Dash == nil || index >= len(s.Plan.Panels) {
		return out
	}
	p, pp := &s.Dash.Panels[index], &s.Plan.Panels[index]
	sent := 0
	for j := range pp.Queries {
		q := &pp.Queries[j]
		if !q.Sent() {
			continue
		}
		sent++
		res := s.Results[dashKey(index, q.RefID)]
		if word := dashStateWord(&res); word != "" {
			out.States = append(out.States, q.RefID+": "+word)
			continue
		}
		if res.State == observability.StateIncomplete {
			out.Incomplete = append(out.Incomplete, q.RefID+": "+res.Reason)
		}
		labels := make([]map[string]string, len(res.Series))
		for k := range res.Series {
			labels[k] = res.Series[k].Labels
		}
		fallback := q.RefID
		if len(pp.Queries) == 1 {
			fallback = "value"
		}
		auto := dashboard.AutoNames(labels, fallback)
		lines := make([]dashLine, 0, len(res.Series))
		for k := range res.Series {
			if len(res.Series[k].Samples) == 0 {
				continue
			}
			name, ok := dashboard.LegendName(q.Legend, res.Series[k].Labels)
			if !ok || name == "" {
				name = auto[k]
			}
			points := graphPoints(res.Series[k].Samples)
			for n := range points {
				points[n].V *= p.Unit.Scale
			}
			lines = append(lines, dashLine{Full: safeProviderText(name), RefID: q.RefID, Points: points, Raw: res.Series[k].Samples})
		}
		sort.SliceStable(lines, func(a, b int) bool { return lines[a].Full < lines[b].Full })
		out.Lines = append(out.Lines, lines...)
	}
	if len(out.Lines) > dashboard.MaxLines {
		out.Cut = len(out.Lines) - dashboard.MaxLines
		out.Lines = out.Lines[:dashboard.MaxLines]
	}
	full := make([]string, len(out.Lines))
	for i := range out.Lines {
		full[i] = out.Lines[i].Full
	}
	for i, name := range dashboard.ShortNames(full) {
		// A long name that shares no start with another keeps both ends.
		out.Lines[i].Name = graphWords(graphMiddle(name, dashNameWidth), ascii)
	}
	if len(out.Lines) == 0 && len(out.States) > 0 {
		word := func(state string) string { return state[strings.Index(state, ": ")+2:] }
		same := true
		for _, state := range out.States {
			same = same && word(state) == word(out.States[0])
		}
		out.Problem = out.States[0]
		if same {
			out.Problem = word(out.States[0])
		}
		if out.Problem == "no series matched" {
			out.Problem = "no series matched in this window (not a zero)"
		}
	}
	return out
}

// dashTally counts the answers of a snapshot by outcome.
type dashTally struct {
	Asked, Failed, Empty, Incomplete int
	// Rejected counts queries the source refused as written.
	Rejected int
	// Worst is the failed answer the page names when every read failed: the
	// first one, in panel order, of the state most reads failed with.
	Worst observability.Status
	// Mixed says the failed reads did not all fail the same way.
	Mixed bool
	// Failures words the failed reads by state: "2 denied, 1 timeout".
	Failures string
}

// dashStateName words the state of a read that drew nothing. A query that
// was never sent has no state: the page's deadline ended first.
func dashStateName(state observability.State) string {
	if state == "" {
		return dashNotRead
	}
	return string(state)
}

// tally counts the answers. It walks them in a fixed order, so that the
// same read is always described in the same words.
func (s *dashSnapshot) tally() dashTally {
	var t dashTally
	keys := make([]string, 0, len(s.Results))
	for key := range s.Results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	counts := map[observability.State]int{}
	first := map[observability.State]observability.Status{}
	var states []observability.State
	for _, key := range keys {
		res := s.Results[key]
		t.Asked++
		switch {
		case res.Error != "":
			t.Rejected++
		case !res.OK():
			t.Failed++
			if counts[res.State] == 0 {
				states = append(states, res.State)
				first[res.State] = res.Status
			}
			counts[res.State]++
		case graphSamples(&res) == 0:
			t.Empty++
		}
		if res.State == observability.StateIncomplete {
			t.Incomplete++
		}
	}
	sort.SliceStable(states, func(a, b int) bool { return counts[states[a]] > counts[states[b]] })
	parts := make([]string, 0, len(states))
	for _, state := range states {
		parts = append(parts, fmt.Sprintf("%d %s", counts[state], dashStateName(state)))
	}
	t.Failures, t.Mixed = strings.Join(parts, ", "), len(states) > 1
	if len(states) > 0 {
		t.Worst = first[states[0]]
	}
	return t
}

// drawn counts the panels that have at least one line.
func (s *dashSnapshot) drawn() int {
	n := 0
	for i := range s.Plan.Panels {
		if s.Plan.Panels[i].NotDrawn == "" && s.Dash.Panels[i].Kind != dashboard.KindRow && len(s.data(i, false).Lines) > 0 {
			n++
		}
	}
	return n
}

// uses reports whether any expression of the page names a variable.
func (s *dashSnapshot) uses(name string) bool {
	if s.Dash == nil {
		return false
	}
	for i := range s.Dash.Panels {
		for j := range s.Dash.Panels[i].Targets {
			expr := s.Dash.Panels[i].Targets[j].Expr
			for _, form := range []string{"$" + name, "${" + name, "[[" + name} {
				// Every occurrence is looked at: $pod is not a use of $po, and
				// a $pod_name earlier in the text does not hide a later $pod.
				for from := 0; ; {
					at := strings.Index(expr[from:], form)
					if at < 0 {
						break
					}
					end := from + at + len(form)
					if end == len(expr) || !(expr[end] == '_' || expr[end] >= '0' && expr[end] <= '9' || expr[end] >= 'a' && expr[end] <= 'z' || expr[end] >= 'A' && expr[end] <= 'Z') {
						return true
					}
					from = end
				}
			}
		}
	}
	return false
}

// variableParts are the variables the page's expressions use, as the page
// states them. The report lists every variable.
func (s *dashSnapshot) variableParts() []string {
	var parts []string
	for i := range s.Plan.Bindings.List {
		b := &s.Plan.Bindings.List[i]
		if !s.uses(b.Name) {
			continue
		}
		parts = append(parts, safeProviderText(b.Display()))
	}
	return parts
}

// dashValue reduces one line to the value a stat or gauge shows, with the
// threshold step it falls into.
type dashValue struct {
	Value     float64 // in the chart unit
	Word      string  // reducer word when it is not the last sample
	Step      dashboard.Step
	HasStep   bool
	Min, Max  float64 // of the window, in the chart unit
	MaxStep   dashboard.Step
	HasMaxLvl bool
}

func dashValueOf(p *dashboard.Panel, line *dashLine) (dashValue, bool) {
	raw, word, ok := dashboard.Reduce(p.Calc, line.Raw)
	if !ok {
		return dashValue{}, false
	}
	out := dashValue{Value: raw * p.Unit.Scale}
	if word != "last" {
		out.Word = word
	}
	out.Step, out.HasStep = p.StepFor(raw)
	low, _, _ := dashboard.Reduce("min", line.Raw)
	high, _, _ := dashboard.Reduce("max", line.Raw)
	out.Min, out.Max = low*p.Unit.Scale, high*p.Unit.Scale
	if step, ok := p.StepFor(high); ok && out.HasStep && step.Word != out.Step.Word {
		out.MaxStep, out.HasMaxLvl = step, true
	}
	return out, true
}

// stepText words a threshold step: "red, from 10s".
func dashStepText(p *dashboard.Panel, step dashboard.Step) string {
	if !step.HasFrom {
		return step.Word
	}
	return fmt.Sprintf("%s, from %s", step.Word, p.Unit.Chart.Format(step.From*p.Unit.Scale))
}
