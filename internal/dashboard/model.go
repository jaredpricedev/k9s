// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package dashboard reads the definition of a Grafana dashboard and decides
// what of it can be drawn in a terminal: which panels are charts of
// Prometheus expressions, what their variables stand for, which queries
// that makes, and how the panels flow into a grid of a given width.
//
// It contacts nothing. Every panel of the definition ends up either with
// queries to run or with the reason it is not drawn; none is dropped.
package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/observability"
)

// Limits of one dashboard definition and of one read of its panels.
const (
	// MaxPanels bounds the panels kept from one definition.
	MaxPanels = 80
	// MaxTargets bounds the queries kept per panel.
	MaxTargets = 6
	// MaxVariables bounds the template variables kept.
	MaxVariables = 40
	// MaxDrawn bounds the panels whose queries one read sends.
	MaxDrawn = 24
	// MaxQueries bounds the queries one read sends.
	MaxQueries = 40
	// MaxLines bounds the lines one chart draws.
	MaxLines = 12
)

// Kind is what a panel is to this package.
type Kind int

const (
	// KindUnsupported is a panel type that is not drawn.
	KindUnsupported Kind = iota
	// KindRow is a section title; it holds no data.
	KindRow
	// KindTimeseries is a line chart over the window (timeseries, graph).
	KindTimeseries
	// KindStat is one value with its trend (stat, singlestat).
	KindStat
	// KindGauge is one value against a minimum and a maximum.
	KindGauge
	// KindBarGauge is one bar per series.
	KindBarGauge
)

// String names the kind the way the page does.
func (k Kind) String() string {
	switch k {
	case KindRow:
		return typeRow
	case KindTimeseries:
		return "timeseries"
	case KindStat:
		return "stat"
	case KindGauge:
		return "gauge"
	case KindBarGauge:
		return "bar gauge"
	}
	return "unsupported"
}

// Drawable reports whether panels of this kind are drawn at all.
func (k Kind) Drawable() bool { return k >= KindTimeseries }

// Value reports whether the panel shows single values rather than lines.
func (k Kind) Value() bool { return k == KindStat || k == KindGauge || k == KindBarGauge }

// DrawnTypes names the Grafana panel types that are drawn.
const DrawnTypes = "timeseries, graph, stat, gauge, bargauge"

// Datasource is a panel's or a target's datasource as the definition
// states it: an object with type and uid (Grafana 8 and later), a bare name
// or variable (older), or nothing.
type Datasource struct {
	Type, UID, Name string
}

// Stated reports whether the definition says anything about the datasource.
func (d Datasource) Stated() bool { return d.Type != "" || d.UID != "" || d.Name != "" }

// Variable is one template variable of the definition.
type Variable struct {
	Name, Type string
	// Current holds the saved selection. AllValue marks "All".
	Current []string
	// Options are the values listed in the definition (custom variables).
	Options []string
	// AllValue is the custom text that stands for All; empty when none.
	AllValue string
	Multi    bool
	// IncludeAll says the variable offers All.
	IncludeAll bool
	// Query is the plugin type of a datasource variable, the value of a
	// constant and the definition of a query variable.
	Query string
}

// AllToken is Grafana's saved value for "All".
const AllToken = "$__all"

// Threshold is one step of a panel's thresholds. The base step has no value.
type Threshold struct {
	Value float64
	Base  bool
	Color string
}

// Target is one query of a panel.
type Target struct {
	RefID string
	// Expr is the query as the dashboard author wrote it.
	Expr string
	// Legend is the legendFormat; empty or "__auto" names lines by labels.
	Legend     string
	Hidden     bool
	Instant    bool
	Datasource Datasource
}

// Panel is one panel of the definition, in the order the dashboard shows
// them.
type Panel struct {
	ID int
	// Type is Grafana's panel type, as written.
	Type  string
	Kind  Kind
	Title string
	// Description is the author's note on the panel.
	Description string
	// Section is the title of the row the panel sits under.
	Section string
	// X, Y, W and H are the grid position; Band numbers the dashboard
	// rows of panels that share a Y.
	X, Y, W, H int
	Band       int
	Unit       Unit
	// Min and Max are the declared ends of the value axis, in the unit of
	// the query result.
	Min, Max       float64
	HasMin, HasMax bool
	Thresholds     []Threshold
	// Percentage says the thresholds are percentages of Min to Max.
	Percentage bool
	// Calc is the reducer of a stat or gauge (lastNotNull, mean, max ...).
	Calc       string
	Datasource Datasource
	Targets    []Target
	// TargetsCut counts targets beyond MaxTargets.
	TargetsCut int
	// Repeat names the variable the panel repeats for; it is drawn once.
	Repeat string
	// NotDrawn is why the definition alone rules the panel out; empty for
	// a panel that can be planned.
	NotDrawn string
}

// Dashboard is a parsed definition.
type Dashboard struct {
	UID, Title, Folder string
	SchemaVersion      int
	Variables          []Variable
	Panels             []Panel
	// PanelsCut and VariablesCut count what was beyond the limits.
	PanelsCut, VariablesCut int
}

// Variable finds a template variable by name.
func (d *Dashboard) Variable(name string) *Variable {
	for i := range d.Variables {
		if d.Variables[i].Name == name {
			return &d.Variables[i]
		}
	}
	return nil
}

// raw shapes. Grafana's JSON is loose: numbers arrive as strings in old
// dashboards, a datasource is an object, a name or null, and a variable's
// current value is a string or a list.

type rawGrid struct {
	H, W, X, Y json.RawMessage
}

type rawTarget struct {
	RefID        string          `json:"refId"`
	Expr         string          `json:"expr"`
	LegendFormat string          `json:"legendFormat"`
	Hide         bool            `json:"hide"`
	Instant      bool            `json:"instant"`
	Datasource   json.RawMessage `json:"datasource"`
}

type rawPanel struct {
	ID          json.RawMessage `json:"id"`
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	GridPos     *rawGrid        `json:"gridPos"`
	Datasource  json.RawMessage `json:"datasource"`
	Targets     []rawTarget     `json:"targets"`
	Panels      []rawPanel      `json:"panels"`
	Repeat      string          `json:"repeat"`
	Format      string          `json:"format"` // singlestat
	Yaxes       []struct {
		Format string `json:"format"`
	} `json:"yaxes"` // graph
	LibraryPanel *struct {
		Name string `json:"name"`
	} `json:"libraryPanel"`
	FieldConfig struct {
		Defaults struct {
			Unit       string          `json:"unit"`
			Min        json.RawMessage `json:"min"`
			Max        json.RawMessage `json:"max"`
			Thresholds struct {
				Mode  string `json:"mode"`
				Steps []struct {
					Color string          `json:"color"`
					Value json.RawMessage `json:"value"`
				} `json:"steps"`
			} `json:"thresholds"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
	Options struct {
		ReduceOptions struct {
			Calcs []string `json:"calcs"`
		} `json:"reduceOptions"`
	} `json:"options"`
}

type rawVariable struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Query      json.RawMessage `json:"query"`
	Definition string          `json:"definition"`
	AllValue   string          `json:"allValue"`
	Multi      bool            `json:"multi"`
	IncludeAll bool            `json:"includeAll"`
	Current    struct {
		Value json.RawMessage `json:"value"`
	} `json:"current"`
	Options []struct {
		Value json.RawMessage `json:"value"`
	} `json:"options"`
}

type rawBoard struct {
	UID           string     `json:"uid"`
	Title         string     `json:"title"`
	SchemaVersion int        `json:"schemaVersion"`
	Panels        []rawPanel `json:"panels"`
	Rows          []struct {
		Title  string     `json:"title"`
		Panels []rawPanel `json:"panels"`
	} `json:"rows"` // before schema 16
	Templating struct {
		List []rawVariable `json:"list"`
	} `json:"templating"`
}

func number(raw json.RawMessage) (float64, bool) {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		return 0, false
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || v != v || v > 1e300 || v < -1e300 {
		return 0, false
	}
	return v, true
}

func whole(raw json.RawMessage) int {
	v, _ := number(raw)
	return int(v)
}

func stringOrList(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

func datasource(raw json.RawMessage) Datasource {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return Datasource{Name: clean(name, 80)}
	}
	var object struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return Datasource{Type: clean(object.Type, 80), UID: clean(object.UID, 80)}
	}
	return Datasource{}
}

func clean(text string, limit int) string {
	return strings.Join(strings.Fields(observability.Clean(text, limit)), " ")
}

// flatten lists the panels of a board in the order Grafana shows them, with
// the title of the row each one sits under. Panels of a collapsed row and
// of the rows of an old dashboard are taken out of their rows.
func flatten(board *rawBoard) (flat []rawPanel, sections []string) {
	section := ""
	add := func(p *rawPanel) {
		sections = append(sections, section)
		flat = append(flat, *p)
	}
	panels := board.Panels
	placed := true
	for i := range panels {
		placed = placed && panels[i].GridPos != nil
	}
	if placed {
		// The grid position decides the order Grafana shows, top to bottom
		// and left to right; without one the listed order stands.
		sort.SliceStable(panels, func(a, b int) bool {
			ga, gb := panels[a].GridPos, panels[b].GridPos
			if ya, yb := whole(ga.Y), whole(gb.Y); ya != yb {
				return ya < yb
			}
			return whole(ga.X) < whole(gb.X)
		})
	}
	for i := range panels {
		p := &panels[i]
		if p.Type != typeRow {
			add(p)
			continue
		}
		section = clean(p.Title, 80)
		add(p)
		// A collapsed row keeps its panels inside itself.
		for j := range p.Panels {
			add(&p.Panels[j])
		}
	}
	// Dashboards from before schema 16 list rows of panels without a grid.
	for i := range board.Rows {
		row := &board.Rows[i]
		section = clean(row.Title, 80)
		add(&rawPanel{Type: typeRow, Title: row.Title})
		for j := range row.Panels {
			p := row.Panels[j]
			if p.GridPos == nil {
				y := json.RawMessage(strconv.Itoa(1_000_000 + i))
				p.GridPos = &rawGrid{Y: y, X: json.RawMessage(strconv.Itoa(j))}
			}
			add(&p)
		}
	}
	return flat, sections
}

// Parse reads the body of GET /api/dashboards/uid/<uid>, or a bare dashboard
// model as exported from Grafana. Text is cleaned of control characters and
// bounded; panels and variables beyond the limits are counted.
func Parse(body []byte) (*Dashboard, error) {
	var envelope struct {
		Dashboard json.RawMessage `json:"dashboard"`
		Meta      struct {
			FolderTitle string `json:"folderTitle"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("the answer is not JSON")
	}
	model := body
	if len(envelope.Dashboard) > 0 && envelope.Dashboard[0] == '{' {
		model = envelope.Dashboard
	}
	var board rawBoard
	if err := json.Unmarshal(model, &board); err != nil {
		return nil, errors.New("the answer is not a Grafana dashboard")
	}
	if board.Title == "" && board.UID == "" && len(board.Panels) == 0 && len(board.Rows) == 0 {
		return nil, errors.New("the answer is not a Grafana dashboard")
	}
	d := &Dashboard{UID: clean(board.UID, 64), Title: clean(board.Title, 120), Folder: clean(envelope.Meta.FolderTitle, 80),
		SchemaVersion: board.SchemaVersion}
	for i := range board.Templating.List {
		if len(d.Variables) >= MaxVariables {
			d.VariablesCut++
			continue
		}
		d.Variables = append(d.Variables, variableOf(&board.Templating.List[i]))
	}
	flat, sections := flatten(&board)
	band, lastY, first := 0, 0, true
	for i := range flat {
		if len(d.Panels) >= MaxPanels {
			d.PanelsCut++
			continue
		}
		p := panelOf(&flat[i])
		p.Section = sections[i]
		if p.Kind == KindRow {
			first = true
		} else if first || p.Y != lastY {
			band++
			lastY, first = p.Y, false
		}
		p.Band = band
		d.Panels = append(d.Panels, p)
	}
	for i := range d.Panels {
		d.classify(&d.Panels[i])
	}
	return d, nil
}

func variableOf(raw *rawVariable) Variable {
	v := Variable{Name: clean(raw.Name, 64), Type: clean(raw.Type, 32), AllValue: observability.Clean(raw.AllValue, 256),
		Multi: raw.Multi, IncludeAll: raw.IncludeAll}
	for _, value := range stringOrList(raw.Current.Value) {
		if len(v.Current) < 32 {
			v.Current = append(v.Current, observability.Clean(value, 256))
		}
	}
	for i := range raw.Options {
		for _, value := range stringOrList(raw.Options[i].Value) {
			if len(v.Options) < 32 && value != AllToken {
				v.Options = append(v.Options, observability.Clean(value, 256))
			}
		}
	}
	var query string
	if json.Unmarshal(raw.Query, &query) == nil {
		v.Query = observability.Clean(query, 512)
	} else if raw.Definition != "" {
		v.Query = observability.Clean(raw.Definition, 512)
	} else {
		var object struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(raw.Query, &object) == nil {
			v.Query = observability.Clean(object.Query, 512)
		}
	}
	return v
}

func panelOf(raw *rawPanel) Panel {
	p := Panel{ID: whole(raw.ID), Type: clean(raw.Type, 40), Title: clean(raw.Title, 120), Description: clean(raw.Description, 400),
		Datasource: datasource(raw.Datasource), Repeat: clean(raw.Repeat, 64)}
	if raw.GridPos != nil {
		p.X, p.Y, p.W, p.H = whole(raw.GridPos.X), whole(raw.GridPos.Y), whole(raw.GridPos.W), whole(raw.GridPos.H)
	}
	switch p.Type {
	case typeRow:
		p.Kind = KindRow
	case "timeseries", "graph":
		p.Kind = KindTimeseries
	case "stat", "singlestat":
		p.Kind = KindStat
	case "gauge":
		p.Kind = KindGauge
	case "bargauge":
		p.Kind = KindBarGauge
	}
	if p.Title == "" && p.Kind != KindRow {
		p.Title = fmt.Sprintf("panel %d", p.ID)
	}
	unit := raw.FieldConfig.Defaults.Unit
	if unit == "" && len(raw.Yaxes) > 0 {
		unit = raw.Yaxes[0].Format
	}
	if unit == "" {
		unit = raw.Format
	}
	p.Unit = UnitFor(clean(unit, 40))
	p.Min, p.HasMin = number(raw.FieldConfig.Defaults.Min)
	p.Max, p.HasMax = number(raw.FieldConfig.Defaults.Max)
	p.Percentage = raw.FieldConfig.Defaults.Thresholds.Mode == "percentage"
	for _, step := range raw.FieldConfig.Defaults.Thresholds.Steps {
		if len(p.Thresholds) >= 8 {
			break
		}
		value, ok := number(step.Value)
		p.Thresholds = append(p.Thresholds, Threshold{Value: value, Base: !ok, Color: clean(step.Color, 40)})
	}
	sort.SliceStable(p.Thresholds, func(a, b int) bool {
		ta, tb := p.Thresholds[a], p.Thresholds[b]
		if ta.Base != tb.Base {
			return ta.Base
		}
		return ta.Value < tb.Value
	})
	if calcs := raw.Options.ReduceOptions.Calcs; len(calcs) > 0 {
		p.Calc = clean(calcs[0], 24)
	}
	for i := range raw.Targets {
		if len(p.Targets) >= MaxTargets {
			p.TargetsCut++
			continue
		}
		t := &raw.Targets[i]
		expr := t.Expr
		if len(expr) > 2*observability.MaxAuthoredQuery {
			expr = expr[:2*observability.MaxAuthoredQuery]
		}
		refID := clean(t.RefID, 8)
		if refID == "" {
			refID = string(rune('A' + i%26))
		}
		p.Targets = append(p.Targets, Target{RefID: refID, Expr: expr, Legend: clean(t.LegendFormat, 200), Hidden: t.Hide, Instant: t.Instant,
			Datasource: datasource(t.Datasource)})
	}
	if raw.LibraryPanel != nil && p.Type == "" {
		p.Type = "library panel"
		p.NotDrawn = "library panel: its definition is not part of the dashboard JSON"
		if p.Title == fmt.Sprintf("panel %d", p.ID) && raw.LibraryPanel.Name != "" {
			p.Title = clean(raw.LibraryPanel.Name, 120)
		}
	}
	return p
}

// Type names of the definition that are compared in more than one place.
const (
	typeRow        = "row"
	typeDatasource = "datasource"
	typeQuery      = "query"
)

// Datasource kinds a panel or target can resolve to.
const (
	sourcePrometheus = "prometheus"
	sourceMixed      = "mixed"
	sourceUnstated   = ""
)

// variableName returns the name inside "$name" or "${name}", or "".
func variableName(text string) string {
	switch {
	case strings.HasPrefix(text, "${") && strings.HasSuffix(text, "}"):
		return text[2 : len(text)-1]
	case strings.HasPrefix(text, "$"):
		return text[1:]
	}
	return ""
}

// sourceKind says what kind of datasource a reference names: "prometheus",
// "mixed", "" when the definition does not say, or the other plugin type.
func (d *Dashboard) sourceKind(ref Datasource) string {
	byVariable := func(text string) (string, bool) {
		name := variableName(text)
		if name == "" {
			return "", false
		}
		if v := d.Variable(name); v != nil && v.Type == typeDatasource && v.Query != "" {
			return v.Query, true
		}
		// An unresolved placeholder such as ${DS_PROMETHEUS} says nothing.
		return sourceUnstated, true
	}
	switch {
	case ref.Type == typeDatasource || strings.HasPrefix(ref.UID, "-- ") || strings.HasPrefix(ref.Name, "-- "):
		if strings.Contains(ref.UID+ref.Name, "Mixed") {
			return sourceMixed
		}
		return "grafana"
	case ref.Type != "":
		return ref.Type
	case ref.UID != "":
		if kind, ok := byVariable(ref.UID); ok {
			return kind
		}
		return sourceUnstated
	case ref.Name != "":
		if kind, ok := byVariable(ref.Name); ok {
			return kind
		}
		return sourceUnstated
	}
	return sourceUnstated
}

// targetSource is the datasource kind a target's query goes to: its own
// when it states one, the panel's otherwise.
func (d *Dashboard) targetSource(p *Panel, t *Target) string {
	if t.Datasource.Stated() {
		if kind := d.sourceKind(t.Datasource); kind != sourceUnstated {
			return kind
		}
	}
	return d.sourceKind(p.Datasource)
}

// classify decides whether the definition alone rules a panel out.
func (d *Dashboard) classify(p *Panel) {
	if p.NotDrawn != "" || p.Kind == KindRow {
		return
	}
	switch {
	case p.Type == "text":
		p.NotDrawn = "text panel: it has no query"
		return
	case p.Kind == KindUnsupported:
		p.NotDrawn = fmt.Sprintf("panel type %q is not drawn here", p.Type)
		return
	}
	usable, other := 0, ""
	for i := range p.Targets {
		t := &p.Targets[i]
		if strings.TrimSpace(t.Expr) == "" || t.Hidden {
			continue
		}
		switch kind := d.targetSource(p, t); kind {
		case sourcePrometheus, sourceUnstated:
			usable++
		default:
			other = kind
		}
	}
	switch {
	case usable > 0:
	case other != "":
		p.NotDrawn = fmt.Sprintf("its datasource is %s, not Prometheus", other)
	case d.sourceKind(p.Datasource) != sourcePrometheus && d.sourceKind(p.Datasource) != sourceUnstated && d.sourceKind(p.Datasource) != sourceMixed:
		p.NotDrawn = fmt.Sprintf("its datasource is %s, not Prometheus", d.sourceKind(p.Datasource))
	default:
		p.NotDrawn = "it has no Prometheus expression"
	}
}
