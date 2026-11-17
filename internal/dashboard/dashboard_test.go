// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dashboard

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
)

func load(t *testing.T, name string) *Dashboard {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	d, err := Parse(body)
	require.NoError(t, err)
	return d
}

func panelByID(t *testing.T, d *Dashboard, id int) (panel *Panel, index int) {
	t.Helper()
	for i := range d.Panels {
		if d.Panels[i].ID == id && d.Panels[i].Kind != KindRow {
			return &d.Panels[i], i
		}
	}
	t.Fatalf("no panel %d", id)
	return nil, 0
}

func TestParseReadsAGrafana12DashboardInItsOwnOrder(t *testing.T) {
	d := load(t, "grafana12-gitlab-webservice.json")
	require.Equal(t, "gitlab-webservice", d.UID)
	require.Equal(t, "GitLab / Webservice overview", d.Title)
	require.Equal(t, "GitLab", d.Folder)
	require.Equal(t, 41, d.SchemaVersion)

	var got []string
	for i := range d.Panels {
		p := &d.Panels[i]
		got = append(got, fmt.Sprintf("%d %s %s band %d", p.ID, p.Kind, p.Title, p.Band))
	}
	require.Equal(t, []string{
		"1 row Requests band 0",
		"2 stat Requests per second band 1", "3 stat Slowest pod p99 band 1", "4 gauge Puma threads in use band 1", "5 gauge CPU throttled, worst pod band 1",
		"6 timeseries Request duration p99 by pod band 2", "7 timeseries Requests by pod band 2",
		"8 row Puma and CPU band 2",
		"9 timeseries Puma queued connections band 3", "10 timeseries CPU usage against limit band 3", "11 timeseries CPU throttled periods band 3",
		"12 row Detail band 3",
		"13 unsupported Pods and nodes band 4", "14 unsupported Webservice log lines with timeout band 4",
	}, got)

	// Grafana 12 details: a datasource object that names a variable, the
	// unit and the thresholds under fieldConfig.defaults, the reducer.
	p99, _ := panelByID(t, d, 3)
	require.Equal(t, Datasource{Type: "prometheus", UID: "${datasource}"}, p99.Datasource)
	require.Equal(t, Unit{ID: "s", Chart: chart.Seconds, Scale: 1, Known: true}, p99.Unit)
	require.Equal(t, []Threshold{{Base: true, Color: "green"}, {Value: 1, Color: "orange"}, {Value: 10, Color: "red"}}, p99.Thresholds)
	require.Equal(t, "lastNotNull", p99.Calc)
	require.Equal(t, "Requests", p99.Section)
	gauge, _ := panelByID(t, d, 4)
	require.True(t, gauge.HasMin && gauge.HasMax)
	require.InDelta(t, 1.0, gauge.Max, 1e-9)
	require.Equal(t, chart.Ratio, gauge.Unit.Chart)
	require.True(t, gauge.Targets[0].Instant)
	queue, _ := panelByID(t, d, 9)
	require.Len(t, queue.Targets, 2)
	require.Equal(t, "{{pod}} free threads", queue.Targets[1].Legend)
	require.Equal(t, "Rails request duration, 99th percentile, one line per pod.", d.Panels[5].Description)

	// What the definition alone rules out is said per panel.
	table, _ := panelByID(t, d, 13)
	require.Equal(t, `panel type "table" is not drawn here`, table.NotDrawn)
	logs, _ := panelByID(t, d, 14)
	require.Equal(t, `panel type "logs" is not drawn here`, logs.NotDrawn)

	pod := d.Variable("pod")
	require.NotNil(t, pod)
	require.Equal(t, Variable{Name: "pod", Type: "query", Current: []string{AllToken}, AllValue: ".*", Multi: true, IncludeAll: true,
		Query: `label_values(kube_pod_info{namespace="$namespace", pod=~"gitlab-webservice-default-.*"}, pod)`}, *pod)
	require.Equal(t, "prometheus", d.Variable("datasource").Query)
}

func TestParseRefusesWhatIsNotADashboardAndBoundsWhatItKeeps(t *testing.T) {
	for _, body := range []string{"", "not json", `[]`, `{"message":"Dashboard not found"}`, `{"dashboard": 3}`} {
		_, err := Parse([]byte(body))
		require.Error(t, err, body)
	}
	var panels []string
	for i := range MaxPanels + 7 {
		panels = append(panels, fmt.Sprintf(`{"id": %d, "type": "stat", "title": "p\u0007%d", "targets": [{"expr": "up"}]}`, i, i))
	}
	d, err := Parse([]byte(`{"title": "Big", "panels": [` + strings.Join(panels, ",") + `]}`))
	require.NoError(t, err)
	require.Len(t, d.Panels, MaxPanels)
	require.Equal(t, 7, d.PanelsCut)
	require.Equal(t, "p 3", d.Panels[3].Title, "control characters never reach the page")
	require.Equal(t, "A", d.Panels[0].Targets[0].RefID, "a target without a refId is still named")

	var targets []string
	for range MaxTargets + 2 {
		targets = append(targets, `{"expr": "up"}`)
	}
	d, err = Parse([]byte(`{"title": "Wide", "panels": [{"id": 1, "type": "timeseries", "targets": [` + strings.Join(targets, ",") + `]}]}`))
	require.NoError(t, err)
	require.Len(t, d.Panels[0].Targets, MaxTargets)
	require.Equal(t, 2, d.Panels[0].TargetsCut)
	require.Equal(t, "panel 1", d.Panels[0].Title)
}

func TestBindFillsTheSelectionAndKeepsSavedValues(t *testing.T) {
	d := load(t, "grafana12-gitlab-webservice.json")
	// A workload is selected: the namespace comes from it, the pod variable
	// keeps the dashboard's saved All, which stands for its allValue.
	b := Bind(d, ConventionalMapping, Selection{Namespace: "gitlab", Workload: "gitlab-webservice-default"}, time.Hour, 30*time.Second)
	var shown []string
	for i := range b.List {
		shown = append(shown, b.List[i].Display())
	}
	require.Equal(t, []string{"datasource=prom-main (saved in dashboard)", "namespace=gitlab (selection)", "pod=~.* (All)"}, shown)
	require.Equal(t, 2*time.Minute, b.Rate)

	p99, _ := panelByID(t, d, 6)
	text, err := b.Expand(p99.Targets[0].Expr)
	require.NoError(t, err)
	require.Equal(t, `histogram_quantile(0.99, sum by (pod, le) (rate(http_request_duration_seconds_bucket{namespace="gitlab", pod=~".*"}[2m])))`, text)

	// A Pod is selected: its name is data. It is quoted as a regular
	// expression where the panel uses =~, and it cannot close the string.
	b = Bind(d, ConventionalMapping, Selection{Namespace: "gitlab", Pod: `web.1"} or vector(1) #`}, 6*time.Hour, 5*time.Minute)
	text, err = b.Expand(p99.Targets[0].Expr)
	require.NoError(t, err)
	require.Equal(t, `histogram_quantile(0.99, sum by (pod, le) (rate(http_request_duration_seconds_bucket{namespace="gitlab", pod=~"web\\.1\"\\} or vector\\(1\\) #"}[20m])))`, text)
	require.NoError(t, observability.AuthoredExpr(text).Err())

	// A declared mapping names other variables; one the definition does
	// not list fills nothing and is named.
	b = Bind(d, Mapping{Namespace: "ns", Pod: "pod"}, Selection{Namespace: "apps", Pod: "api-0"}, time.Hour, 30*time.Second)
	require.Nil(t, b.Find("ns"))
	require.Equal(t, []string{"ns"}, b.Missing)
	require.Equal(t, "pod=api-0 (selection)", b.Find("pod").Display())
	require.Equal(t, "namespace=gitlab (saved in dashboard)", b.Find("namespace").Display())

	// Nothing selected: the dashboard's own saved values stand.
	b = Bind(d, ConventionalMapping, Selection{}, time.Hour, 30*time.Second)
	require.Equal(t, "namespace=gitlab (saved in dashboard)", b.Find("namespace").Display())
}

func TestExpandKnowsGrafanaSyntaxAndEscapesByPlace(t *testing.T) {
	d := load(t, "grafana12-odd.json")
	b := Bind(d, ConventionalMapping, Selection{Namespace: "shop"}, time.Hour, 30*time.Second)
	for _, tc := range []struct{ expr, want, fails string }{
		{expr: `sum by (status) (rate(http_requests_total{namespace="$namespace", status=~"${status:regex}", job="$job"}[$__rate_interval]))`,
			want: `sum by (status) (rate(http_requests_total{namespace="shop", status=~"(4\\.\\.|5\\.\\.)", job="api.v2"}[2m]))`},
		// All of a custom variable is its listed values; a text box value is
		// the author's own regular expression and is not quoted.
		{expr: `sum(rate(http_requests_total{team=~"$team", job=~"$job"}[$__interval])) / $__range_s`,
			want: `sum(rate(http_requests_total{team=~"(a|b)", job=~"api.v2"}[30s])) / 3600`},
		{expr: `up{namespace='[[namespace]]', job=~'${job}'} * $__interval_ms + $__range_ms`, want: `up{namespace='shop', job=~'api.v2'} * 30000 + 3600000`},
		{expr: `up{pod=~"$pod"}`, fails: "variable $pod is not resolved: it is set to All without an allValue, and its values come from a query that is not run here"},
		{expr: `up{job="$empty"}`, fails: "variable $empty is not resolved: no value is saved in the dashboard, and its values come from a query that is not run here"},
		{expr: `up{cluster="$cluster"}`, fails: "variable $cluster is not defined"},
		{expr: `up{job="$filters"}`, fails: `variable $filters is not resolved: variable type "adhoc" is not supported`},
		{expr: `up{team="$team"}`, fails: "$team has 2 values, which only a =~ matcher can take"},
		{expr: `up{job=~"api|^$"}`, fails: "it holds a literal $, which is not sent (a matcher is anchored without it)"},
		{expr: `up{job="${job:json}"}`, fails: "the format :json of $job is not supported"},
		{expr: `up{job="${job"}`, fails: "a ${ is not closed"},
		{expr: `$namespace{job="x"}`, want: `shop{job="x"}`},
		{expr: `up{job="x"}[$team]`, fails: "the value of $team cannot stand outside a quoted string"},
	} {
		got, err := b.Expand(tc.expr)
		if tc.fails != "" {
			require.EqualError(t, err, tc.fails, tc.expr)
			continue
		}
		require.NoError(t, err, tc.expr)
		require.Equal(t, tc.want, got)
	}

	// The bar's own variables: a workload stands for a name prefix, which
	// only a =~ matcher can take, and says that it is one.
	q := SelectionBindings(Selection{Namespace: "gitlab", Workload: "web.api"}, time.Hour, 30*time.Second)
	text, err := q.Expand(`sum by (pod) (up{namespace="$namespace", pod=~"$pod", app="$workload"})`)
	require.NoError(t, err)
	require.Equal(t, `sum by (pod) (up{namespace="gitlab", pod=~"web\\.api-.+", app="web.api"})`, text)
	require.Equal(t, `pod=~web\\.api-.+ (name prefix of the workload)`, q.Find("pod").Display())
	_, err = q.Expand(`up{pod="$pod"}`)
	require.EqualError(t, err, "$pod stands for a pattern (name prefix of the workload), which only a =~ matcher can take")
	_, err = q.Expand(`up{node="$node"}`)
	require.EqualError(t, err, "variable $node is not resolved: no node is selected or known for the selection")
	q = SelectionBindings(Selection{Namespace: "gitlab", Pod: "web-0", Node: "worker-f"}, time.Hour, 30*time.Second)
	text, err = q.Expand(`up{pod="$pod", node="$node"}`)
	require.NoError(t, err)
	require.Equal(t, `up{pod="web-0", node="worker-f"}`, text)
	q = SelectionBindings(Selection{}, time.Hour, 30*time.Second)
	require.Equal(t, "pod=~.+ (no pod selected)", q.Find("pod").Display())
	_, err = q.Expand(`up{namespace="$namespace"}`)
	require.EqualError(t, err, "variable $namespace is not resolved: nothing with a namespace is selected")
}

func TestPlanGivesEveryPanelQueriesOrAReason(t *testing.T) {
	d := load(t, "grafana12-odd.json")
	plan := BuildPlan(d, Bind(d, ConventionalMapping, Selection{Namespace: "shop"}, time.Hour, 30*time.Second))
	got := map[int]string{}
	for i := range d.Panels {
		p, pp := &d.Panels[i], &plan.Panels[i]
		if p.Kind == KindRow {
			require.Empty(t, pp.NotDrawn)
			continue
		}
		state := "drawn"
		if pp.NotDrawn != "" {
			state = pp.NotDrawn
			for j := range pp.Queries {
				require.False(t, pp.Queries[j].Sent() && pp.Queries[j].Expr.Err() != nil)
			}
		}
		got[p.ID] = state
	}
	require.Equal(t, map[int]string{
		1:  "drawn",
		2:  "variable $pod is not resolved: it is set to All without an allValue, and its values come from a query that is not run here",
		3:  "drawn",
		4:  "variable $empty is not resolved: no value is saved in the dashboard, and its values come from a query that is not run here",
		5:  "library panel: its definition is not part of the dashboard JSON",
		6:  `panel type "piechart" is not drawn here`,
		7:  "variable $cluster is not defined",
		8:  "its datasource is postgres, not Prometheus",
		9:  "it holds a literal $, which is not sent (a matcher is anchored without it)",
		11: "drawn",
	}, got)
	require.Equal(t, Counts{Panels: 10, Planned: 3, NotDrawn: 7}, plan.Count(d))
	require.Equal(t, 4, plan.Queries)

	// A panel on mixed datasources draws its Prometheus query and names
	// the one it leaves out.
	_, mixed := panelByID(t, d, 3)
	require.True(t, plan.Panels[mixed].Queries[0].Sent())
	require.Equal(t, `sum(up{namespace="shop"})`, plan.Panels[mixed].Queries[0].Expr.String())
	require.Equal(t, []string{"query B is not sent: its datasource is loki, not Prometheus"}, plan.Panels[mixed].Notes)
	// A panel inside a collapsed row is planned like any other.
	inside, at := panelByID(t, d, 11)
	require.Equal(t, "Collapsed", inside.Section)
	require.Equal(t, `sum by (pod) (container_memory_working_set_bytes{namespace="shop"})`, plan.Panels[at].Queries[0].Expr.String())
	require.Equal(t, "Shared SLO panel", d.Panels[4].Title)

	// The limits of one read are applied in order and counted.
	var panels []string
	for i := range MaxDrawn + 3 {
		panels = append(panels, fmt.Sprintf(`{"id": %d, "type": "timeseries", "title": "t", "targets": [{"expr": "up"}, {"expr": "up"}]}`, i+1))
	}
	big, err := Parse([]byte(`{"title": "Big", "panels": [` + strings.Join(panels, ",") + `]}`))
	require.NoError(t, err)
	plan = BuildPlan(big, Bind(big, ConventionalMapping, Selection{}, time.Hour, 30*time.Second))
	require.Equal(t, MaxQueries, plan.Queries)
	require.Equal(t, MaxQueries/2, plan.Planned)
	require.Equal(t, MaxDrawn+3-MaxQueries/2, plan.Count(big).NotDrawn)
	require.Equal(t, fmt.Sprintf("not read: one read sends at most %d queries", MaxQueries), plan.Panels[MaxQueries/2].NotDrawn)
}

func TestLegacyDashboardsWithRowsNamesAndOldVariableSyntax(t *testing.T) {
	d := load(t, "legacy-rows.json")
	require.Equal(t, "Legacy / API", d.Title)
	plan := BuildPlan(d, Bind(d, ConventionalMapping, Selection{}, time.Hour, 30*time.Second))
	requests, at := panelByID(t, d, 1)
	require.Equal(t, KindTimeseries, requests.Kind)
	require.Equal(t, "Traffic", requests.Section)
	require.Equal(t, chart.PerSecond, requests.Unit.Chart, "a graph panel's unit is its left axis format")
	require.Equal(t, `sum(rate(http_requests_total{cluster="prod-eu", namespace="shop"}[5m])) by (code)`, plan.Panels[at].Queries[0].Expr.String())
	require.Equal(t, "it is hidden in the dashboard", plan.Panels[at].Queries[1].Skip)
	require.Equal(t, []string{"query B is not sent: it is hidden in the dashboard"}, plan.Panels[at].Notes)

	latency, at := panelByID(t, d, 2)
	require.Equal(t, KindStat, latency.Kind)
	require.Equal(t, Unit{ID: "ms", Chart: chart.Seconds, Scale: 1e-3, Known: true}, latency.Unit)
	require.True(t, plan.Panels[at].Queries[0].Assumed, "no datasource is stated")
	require.Equal(t, []string{"query A: the dashboard does not state the datasource type; it is sent to Prometheus"}, plan.Panels[at].Notes)
	require.Equal(t, requests.Band, latency.Band, "panels of one legacy row stay side by side")

	// The auto interval is the step of this read.
	b := Bind(d, ConventionalMapping, Selection{}, 6*time.Hour, 5*time.Minute)
	text, err := b.Expand(`rate(x[$resolution])`)
	require.NoError(t, err)
	require.Equal(t, "rate(x[5m])", text)
}

func TestUnitsThresholdsAndReducers(t *testing.T) {
	for id, want := range map[string]Unit{
		"percentunit": {ID: "percentunit", Chart: chart.Ratio, Scale: 1, Known: true},
		"percent":     {ID: "percent", Chart: chart.Ratio, Scale: 0.01, Known: true},
		"reqps":       {ID: "reqps", Chart: chart.PerSecond, Scale: 1, Known: true},
		"bytes":       {ID: "bytes", Chart: chart.Bytes, Scale: 1, Known: true},
		"":            {Chart: chart.Count, Scale: 1, Known: true},
		"wibble":      {ID: "wibble", Chart: chart.Count, Scale: 1},
	} {
		require.Equal(t, want, UnitFor(id), id)
	}
	require.Equal(t, "unit wibble (plain numbers)", UnitFor("wibble").Word())
	require.Equal(t, "percent", UnitFor("percentunit").Word())
	require.Empty(t, UnitFor("short").Word())
	require.Equal(t, chart.Seconds, UnitNamed(observability.UnitSeconds).Chart)

	d := load(t, "grafana12-gitlab-webservice.json")
	p99, _ := panelByID(t, d, 3)
	step, ok := p99.StepFor(0.31)
	require.True(t, ok)
	require.Equal(t, Step{Word: "green", Level: LevelGood}, step)
	step, _ = p99.StepFor(48)
	require.Equal(t, Step{Word: "red", Level: LevelBad, From: 10, HasFrom: true}, step)
	step, _ = p99.StepFor(1)
	require.Equal(t, Step{Word: "orange", Level: LevelWarn, From: 1, HasFrom: true}, step)
	rate, _ := panelByID(t, d, 2)
	_, ok = rate.StepFor(12)
	require.False(t, ok, "a single base step says nothing")

	// Percentage thresholds are a share of min to max; a color without a
	// name is a numbered step.
	odd := load(t, "grafana12-odd.json")
	mixed, _ := panelByID(t, odd, 3)
	step, ok = mixed.StepFor(170)
	require.True(t, ok)
	require.Equal(t, Step{Word: "red", Level: LevelBad, From: 160, HasFrom: true}, step)
	step, _ = mixed.StepFor(120)
	require.Equal(t, Step{Word: "step 2 of 3", From: 100, HasFrom: true}, step)
	mixed.HasMax = false
	_, ok = mixed.StepFor(120)
	require.False(t, ok)
	require.Equal(t, "max", mixed.Calc)

	samples := []observability.Sample{{Value: 2}, {Value: 9}, {Value: 4}}
	for calc, want := range map[string]float64{"": 4, "lastNotNull": 4, "last": 4, "first": 2, "mean": 5, "max": 9, "min": 2, "sum": 15} {
		got, _, reduced := Reduce(calc, samples)
		require.True(t, reduced)
		require.InDelta(t, want, got, 1e-9, calc)
	}
	_, word, _ := Reduce("stdDev", samples)
	require.Equal(t, "last (reducer stdDev is not computed here)", word)
	_, _, ok = Reduce("max", nil)
	require.False(t, ok)
}

func TestLegendNames(t *testing.T) {
	labels := map[string]string{"pod": "web-1", "status": "200"}
	name, ok := LegendName("{{pod}} / {{ status }} done", labels)
	require.True(t, ok)
	require.Equal(t, "web-1 / 200 done", name)
	_, ok = LegendName("__auto", labels)
	require.False(t, ok)
	_, ok = LegendName("", labels)
	require.False(t, ok)

	require.Equal(t, []string{"200", "499"}, AutoNames([]map[string]string{{"status": "200", "job": "a"}, {"status": "499", "job": "a"}}, "A"))
	require.Equal(t, []string{"pod=a status=200", "pod=b status=499"},
		AutoNames([]map[string]string{{"status": "200", "pod": "a"}, {"status": "499", "pod": "b"}}, "A"))
	require.Equal(t, []string{"A"}, AutoNames([]map[string]string{{}}, "A"))
	require.Equal(t, []string{"web-1"}, AutoNames([]map[string]string{{"pod": "web-1", "job": "x"}}, "A"))

	require.Equal(t, []string{"…2bq7k", "…8wzpd queued", "…hx4mt"}, ShortNames([]string{
		"gitlab-webservice-default-6f9c7d5b84-2bq7k", "gitlab-webservice-default-6f9c7d5b84-8wzpd queued", "gitlab-webservice-default-6f9c7d5b84-hx4mt"}))
	require.Equal(t, []string{"api-1", "api-2"}, ShortNames([]string{"api-1", "api-2"}), "a short common start is kept")
	require.Equal(t, []string{"gitlab-webservice-default-6f9c7d5b84-2bq7k"}, ShortNames([]string{"gitlab-webservice-default-6f9c7d5b84-2bq7k"}))
	require.Equal(t, []string{"200", "499"}, ShortNames([]string{"200", "499"}))
	// Two groups with equally long starts: the same one is shortened on
	// every draw, whatever order a map yields them in.
	for range 50 {
		require.Equal(t, []string{"payments-api-aaaaa", "payments-api-bbbbb", "…ccccc", "…ddddd"}, ShortNames([]string{
			"payments-api-aaaaa", "payments-api-bbbbb", "checkout-api-ccccc", "checkout-api-ddddd"}))
	}
}

func lineText(d *Dashboard, lines []Line) []string {
	var out []string
	for _, line := range lines {
		var parts []string
		for _, slot := range line.Slots {
			parts = append(parts, fmt.Sprintf("%d@%d+%d", d.Panels[slot.Panel].ID, slot.X, slot.Width))
		}
		out = append(out, []string{"section", "note", "value", "chart"}[line.Kind]+" "+strings.Join(parts, " "))
	}
	return out
}

func TestFlowKeepsDashboardRowsSideBySideAsFarAsTheWidthAllows(t *testing.T) {
	d := load(t, "grafana12-gitlab-webservice.json")
	plan := BuildPlan(d, Bind(d, ConventionalMapping, Selection{Namespace: "gitlab"}, time.Hour, 30*time.Second))
	require.Equal(t, []string{
		"section 1@0+132",
		"value 2@0+31 3@33+31 4@66+31 5@99+31",
		"chart 6@0+65 7@67+65",
		"section 8@0+132",
		"chart 9@0+65 10@67+65",
		"chart 11@0+65",
		"section 12@0+132",
		"note 13@0+132", "note 14@0+132",
	}, lineText(d, Flow(d, &plan, 132)))
	// 80 columns: the four values make two even lines, each chart a line.
	require.Equal(t, []string{
		"section 1@0+80",
		"value 2@0+39 3@41+39", "value 4@0+39 5@41+39",
		"chart 6@0+80", "chart 7@0+80",
		"section 8@0+80",
		"chart 9@0+80", "chart 10@0+80", "chart 11@0+80",
		"section 12@0+80",
		"note 13@0+80", "note 14@0+80",
	}, lineText(d, Flow(d, &plan, 80)))
	narrow := Flow(d, &plan, 60)
	require.Equal(t, "value 2@0+29 3@31+29", lineText(d, narrow)[1])
	require.Equal(t, "chart 6@0+60", lineText(d, narrow)[3])
	wide := Flow(d, &plan, 170)
	require.Equal(t, "chart 9@0+55 10@57+55 11@114+55", lineText(d, wide)[4])
	// Every panel has a place at every width.
	for _, width := range []int{20, 60, 80, 132, 200} {
		placed := 0
		for _, line := range Flow(d, &plan, width) {
			placed += len(line.Slots)
			for _, slot := range line.Slots {
				require.LessOrEqual(t, slot.X+slot.Width, max(width, slot.Width))
			}
		}
		require.Equal(t, len(d.Panels), placed, width)
	}

	// Moving the selection: reading order skips row titles and stops at the
	// ends; up and down keep the column.
	lines := Flow(d, &plan, 132)
	index := func(id int) int { _, i := panelByID(t, d, id); return i }
	require.Equal(t, index(3), Advance(lines, index(2), 1))
	require.Equal(t, index(6), Advance(lines, index(5), 1), "the row title is skipped")
	require.Equal(t, index(2), Advance(lines, index(2), -1))
	require.Equal(t, index(14), Advance(lines, index(14), 1))
	require.Equal(t, index(7), AdvanceLine(lines, index(5), 1), "down from the fourth value lands on the right chart")
	require.Equal(t, index(6), AdvanceLine(lines, index(2), 1))
	require.Equal(t, index(10), AdvanceLine(lines, index(7), 1))
	require.Equal(t, index(4), AdvanceLine(lines, index(7), -1))
	require.Equal(t, index(2), AdvanceLine(lines, index(2), -1))
}

func TestVisibleShowsWholeLinesAroundTheSelection(t *testing.T) {
	heights := []int{1, 5, 9, 1, 9, 9, 1, 1, 1}
	start, count := Visible(heights, 0, 1, 16)
	require.Equal(t, []int{0, 4}, []int{start, count}, "1+5+9+1 fit into 16")
	start, count = Visible(heights, 0, 4, 16)
	require.Equal(t, []int{3, 2}, []int{start, count}, "the selected line is brought into view")
	start, count = Visible(heights, 3, 5, 16)
	require.Equal(t, 5, start)
	require.Equal(t, 4, count)
	start, count = Visible(heights, 8, 8, 16)
	require.Equal(t, []int{5, 4}, []int{start, count}, "room above is not left unused at the end")
	start, count = Visible(heights, 0, 2, 4)
	require.Equal(t, []int{2, 1}, []int{start, count}, "a line taller than the page is still shown")
	start, count = Visible(nil, 0, 0, 10)
	require.Zero(t, start+count)
}

func TestNamedQueriesShipBuiltinsAndTakeSavedOnes(t *testing.T) {
	var names []string
	for _, q := range NamedQueries(nil) {
		names = append(names, q.Name)
		require.True(t, observability.QueryName(q.Name))
		require.NotEmpty(t, q.About)
	}
	require.Equal(t, []string{"throttled", "ingress-499", "puma-queue", "dns-p99", "hubble-drops"}, names)

	// Each shipped query expands for a selected workload and is accepted
	// as one bounded expression.
	b := SelectionBindings(Selection{Namespace: "gitlab", Workload: "gitlab-webservice-default"}, time.Hour, 30*time.Second)
	board := QueryBoard(NamedQueries(nil))
	plan := BuildPlan(board, b)
	require.Equal(t, Counts{Panels: 5, Planned: 5}, plan.Count(board))
	require.Equal(t, `sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{namespace="gitlab", pod=~"gitlab-webservice-default-.+"}[2m])) / `+
		`sum by (pod) (rate(container_cpu_cfs_periods_total{namespace="gitlab", pod=~"gitlab-webservice-default-.+"}[2m]))`, plan.Panels[0].Queries[0].Expr.String())
	require.Equal(t, "throttled · CPU throttled share by pod", board.Panels[0].Title)
	require.Equal(t, chart.Ratio, board.Panels[0].Unit.Chart)
	require.Equal(t, `sum by (reason) (rate(hubble_drop_total[2m]))`, plan.Panels[4].Queries[0].Expr.String())

	// Without a namespace the namespaced ones say why they are not drawn;
	// the cluster-wide ones still are.
	plan = BuildPlan(board, SelectionBindings(Selection{}, time.Hour, 30*time.Second))
	require.Equal(t, "variable $namespace is not resolved: nothing with a namespace is selected", plan.Panels[0].NotDrawn)
	require.Empty(t, plan.Panels[3].NotDrawn)

	saved := &observability.Sources{Queries: map[string]observability.NamedQuery{
		"throttled": {Expr: "up", Title: "Mine"}, "slow-sql": {Expr: `pg_stat_activity_max_tx_duration{datname="$namespace"}`, Unit: "seconds"}}}
	require.NoError(t, saved.ValidateQueries())
	names = names[:0]
	for _, q := range NamedQueries(saved) {
		names = append(names, fmt.Sprintf("%s builtin=%v", q.Name, q.Builtin))
	}
	require.Equal(t, []string{"ingress-499 builtin=true", "puma-queue builtin=true", "dns-p99 builtin=true", "hubble-drops builtin=true",
		"slow-sql builtin=false", "throttled builtin=false"}, names)
	q, ok := FindQuery(saved, "throttled")
	require.True(t, ok)
	require.Equal(t, "up", q.Expr)
	_, ok = FindQuery(saved, "nope")
	require.False(t, ok)
	page := QueryPage(&q)
	require.Equal(t, "throttled", page.Title)
	require.Equal(t, "Mine", page.Panels[0].Title)
	typed := QueryPage(&Named{NamedQuery: observability.NamedQuery{Expr: "sum(up)\n  by (job)"}})
	require.Equal(t, "typed expression", typed.Title)
	require.Equal(t, "sum(up) by (job)", typed.Panels[0].Title, "a typed expression is its own title")
	require.Equal(t, "k9s:\n  observability:\n    queries:\n      my-query:\n        expr: 'sum(up{job=''x''})'", SaveSnippet("", "sum(up{job='x'})\n"))

	for name, bad := range map[string]observability.NamedQuery{"Bad Name": {Expr: "up"}, "empty": {}, "unit": {Expr: "up", Unit: "furlongs"}} {
		s := &observability.Sources{Queries: map[string]observability.NamedQuery{name: bad}}
		require.Error(t, s.ValidateQueries(), name)
	}
}
