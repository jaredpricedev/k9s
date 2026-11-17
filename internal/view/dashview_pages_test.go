// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dashboard"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

func TestDashboardNoIconsPageIsASCII(t *testing.T) {
	s := dashStorySnapshot(t)
	for _, zoom := range []bool{false, true} {
		page := dashPageOf(80, 20)
		page.ASCII, page.Zoom, page.Selected, page.Cursor = true, zoom, 5, 60
		text := dashText(t, s, page)
		for _, r := range text {
			require.True(t, r < 128 || r == '·', "%q is neither ASCII nor the shared separator", r)
		}
		require.Contains(t, text, "...hx4mt")
	}
	page := dashPageOf(132, 32)
	page.ASCII = true
	text := dashText(t, s, page)
	for _, want := range []string{"-- Requests -----", "========------------- of 100%", "a ...2bq7k 310ms", "c ...hx4mt 350ms peak 47.9s", "(Up/Down)",
		"Left/Right Up/Down panel · Enter zoom"} {
		require.Contains(t, text, want)
	}
}

func TestDashboardEmptyRejectedAndDeniedAnswersAreWordsNeverALine(t *testing.T) {
	denied := observability.Status{Kind: observability.KindPrometheus, State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: graphNow}
	fake := dashStory()
	fake.rules = append([]dashRule{
		{contains: "max(histogram_quantile(0.99", value: func(map[string]string, time.Time) (float64, bool) { return 0, false }},
		{contains: "histogram_quantile(0.99, sum by (pod, le)", rejected: `bad_data: 1:28: parse error: unexpected "by"` + "\x07"},
		{contains: "sum by (pod) (rate(http_requests_total", status: &denied},
		{contains: "puma_pool_capacity", value: func(map[string]string, time.Time) (float64, bool) { return 0, false }},
	}, fake.rules...)
	s := loadDash(t.Context(), fake, dashDefinition(t, "grafana12-gitlab-webservice.json"), dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	text := dashText(t, s, dashPageOf(132, 32))
	for _, want := range []string{
		"6/11 panels drawn · 9/11 answered, 1 rejected, 1 denied · 2 matched no series",
		// A panel whose query drew nothing takes one line with the reason;
		// an empty plot would only look like a reading.
		"  Slowest pod p99 · NOT DRAWN: no series matched in this window (not a zero)",
		`  Request duration p99 by pod · NOT DRAWN: expression failed: bad_data: 1:28: parse error: unexpected "by"`,
		"  Requests by pod · NOT DRAWN: denied (HTTP 403): nothing was read",
		"> Requests per second", "Puma threads in use", "c …hx4mt queued 0 peak 40",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "\x07")
	// One query of the Puma panel matched nothing: its other lines are drawn
	// and the zoomed panel names the query that is missing.
	page := dashPageOf(132, 32)
	page.Zoom, page.Selected = true, 8
	text = dashText(t, s, page)
	require.Contains(t, text, "query B: no series matched")
	require.Contains(t, text, "complete · 0 series · 0 samples · no series matched: missing data, not a zero")
	page.Selected = 5
	text = dashText(t, s, page)
	for _, want := range []string{
		`NOT DRAWN: expression failed: bad_data: 1:28: parse error: unexpected "by".`,
		"Prometheus rejected the expression as it was sent, in its own words above. Nothing was read.",
		`unavailable (HTTP 400 request rejected) · Prometheus said: bad_data: 1:28: parse error: unexpected "by"`,
	} {
		require.Contains(t, text, want)
	}
	page.Selected = 2
	text = dashText(t, s, page)
	require.Contains(t, text, "NOT DRAWN: no series matched in this window (not a zero).")
	require.Contains(t, text, "That is missing data, not a zero")

	report := dashReport(s)
	for _, want := range []string{
		"Dashboard GitLab / Webservice overview · READ ONLY",
		"Grafana dashboard uid gitlab-webservice, schema version 41 · found by search: variables are matched by the names namespace, pod, workload and node",
		"its queries go to the declared Prometheus source, not through Grafana",
		"Each query is one bounded GET (8s, 32 series, 4096 samples). One read draws at most 24 panels with 40 queries. Nothing is polled; r reads again.",
		"  $namespace=gitlab (selection)", "  $pod=~.* (All)", "  $datasource=prom-main (saved in dashboard)",
		"$__rate_interval is 2m: four steps and at least 2m, because the scrape interval is not read.",
		"5 of 11 · timeseries · Request duration p99 by pod\n  NOT DRAWN: expression failed: bad_data",
		`  Webservice log lines with timeout: panel type "logs" is not drawn here`,
		"  Requests by pod: denied (HTTP 403): nothing was read",
		"A threshold word (green, orange, red) is the name the dashboard author gave the step",
	} {
		require.Contains(t, report, want)
	}
}

func TestDashboardFailuresSayWhichSourceAndPointToSources(t *testing.T) {
	at := time.Date(2026, 11, 17, 5, 0, 9, 0, time.UTC)
	grafana := func(state observability.State, reason string) *dashSnapshot {
		s := newDashSnapshot(dashWebservice, dashLabel, time.Hour, graphNow)
		s.Grafana = &observability.Status{Kind: observability.KindGrafana, State: state, Reason: reason, ObservedAt: at, Source: "service monitoring/grafana:3000"}
		return s
	}
	for _, tc := range []struct {
		s    *dashSnapshot
		want []string
	}{
		{grafana(observability.StateDenied, "HTTP 403"), []string{"Dashboard · GitLab / Webservice overview · last 1h",
			"Grafana DENIED the read of the dashboard definition (HTTP 403) at 05:00:09Z.", "a read that failed is not a reading of zero",
			"Open :sources to see the declaration and probe it once (p).", "Source: grafana via service monitoring/grafana:3000"}},
		{grafana(observability.StateAbsent, "HTTP 404"), []string{"Grafana ABSENT: it has no dashboard with this UID, or it is not visible to these credentials (HTTP 404)",
			":dashboards lists what Grafana returns."}},
		{grafana(observability.StateTimeout, "no answer within the deadline"), []string{"Grafana TIMEOUT: the read of the dashboard definition was not answered in time", "Press r to ask again"}},
		{grafana(observability.StateIncomplete, "response exceeds the 1 MiB limit"), []string{"Grafana INCOMPLETE: the read of the dashboard definition was cut (response exceeds the 1 MiB limit)",
			"A dashboard definition is read whole or not at all"}},
		{grafana(observability.StateNotConfigured, ""), []string{"No Grafana source is declared for this context, so nothing was asked."}},
	} {
		text := dashText(t, tc.s, dashPageOf(132, 20))
		for _, want := range tc.want {
			require.Contains(t, text, want)
		}
		require.NotContains(t, text, "┤")
	}

	s := newDashSnapshot(dashWebservice, dashLabel, time.Hour, graphNow)
	s.Grafana = &observability.Status{Kind: observability.KindGrafana, State: observability.StateComplete, ObservedAt: at}
	s.ParseErr = "the answer is not a Grafana dashboard"
	require.Contains(t, dashText(t, s, dashPageOf(100, 20)), "Grafana answered, but the answer is not a Grafana dashboard.")

	// No Prometheus: the definition was read, and the page says what it
	// would have drawn instead of drawing empty panels.
	s = newDashSnapshot(dashWebservice, dashLabel, time.Hour, graphNow)
	s.Dash = dashDefinition(t, "grafana12-gitlab-webservice.json")
	s.bind(dashDeployment)
	s.Prom = &observability.Status{Kind: observability.KindPrometheus, State: observability.StateNotConfigured}
	text := dashText(t, s, dashPageOf(100, 20))
	require.Contains(t, text, "No Prometheus source is declared for this context, so nothing was asked.")
	require.Contains(t, text, "The definition has 11 panels; 9 have Prometheus expressions that would be drawn.")

	// Every query failed alike: one statement, not eleven empty tiles.
	denied := observability.Status{Kind: observability.KindPrometheus, State: observability.StateDenied, Reason: "HTTP 403", ObservedAt: at}
	all := loadDash(t.Context(), &dashFake{rules: []dashRule{{contains: "", status: &denied}}}, dashDefinition(t, "grafana12-gitlab-webservice.json"),
		dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	text = dashText(t, all, dashPageOf(100, 20))
	require.Contains(t, text, "Prometheus DENIED all 11 queries (HTTP 403) at 05:00:09Z.")
	require.Contains(t, text, "Source: prometheus via service monitoring/prometheus:9090")

	// A definition without panels says so.
	empty, err := dashboard.Parse([]byte(`{"dashboard": {"uid": "empty", "title": "Empty"}}`))
	require.NoError(t, err)
	none := loadDash(t.Context(), dashStory(), empty, dashWebservice, dashLabel, dashDeployment, time.Hour, graphNow)
	require.Contains(t, dashText(t, none, dashPageOf(100, 20)), "The dashboard definition has no panels.")
}

func TestDashboardVariablesThatCannotBeFilledNameThePanelAndTheReason(t *testing.T) {
	s := loadDash(t.Context(), dashStory(), dashDefinition(t, "grafana12-odd.json"), &dashSpec{UID: "odd-cases", Mapping: dashboard.ConventionalMapping},
		"namespace shop", dashboard.Selection{Namespace: "shop"}, time.Hour, graphNow)
	text := dashText(t, s, dashPageOf(132, 32))
	for _, want := range []string{
		"All pods without allValue · unit wibble (plain numbers) · NOT DRAWN: variable $pod is not resolved: it is set to All without an a…",
		"Mixed sources · NOT DRAWN: no series matched in this window (not a zero)",
		"Uses an empty variable · NOT DRAWN: variable $empty is not resolved: no value is saved in the dashboard",
		"Shared SLO panel · NOT DRAWN: library panel: its definition is not part of the dashboard JSON",
		`Share by team · NOT DRAWN: panel type "piechart" is not drawn here`,
		"Unknown variable · NOT DRAWN: variable $cluster is not defined",
		"SQL panel · NOT DRAWN: its datasource is postgres, not Prometheus",
		"Regex anchor · NOT DRAWN: it holds a literal $, which is not sent",
	} {
		require.Contains(t, text, want)
	}
	// Variables no expression uses are left to the report.
	require.Contains(t, text, "namespace=shop (selection) · pod not resolved · status=4..|5.. (saved in dashboard) · team=a|b (All) · job=api.v2 (saved")
	require.NotContains(t, text, "logs=", "a datasource variable chooses no data here")
	page := dashPageOf(132, 32)
	page.Zoom, page.Selected = true, 2
	zoom := dashText(t, s, page)
	require.Contains(t, zoom, "Note: query B is not sent: its datasource is loki, not Prometheus")
	require.Contains(t, zoom, "Panel 3 of 10 · stat · Mixed sources")
	report := dashReport(s)
	require.Contains(t, report, "$pod NOT RESOLVED: it is set to All without an allValue, and its values come from a query that is not run here")
	require.Contains(t, report, "Note: the panel repeats for $pod in Grafana; here it is drawn once")
}

func TestQueryBoardDrawsEveryNamedQueryForTheSelection(t *testing.T) {
	board := dashboard.QueryBoard(dashboard.NamedQueries(nil))
	spec := &dashSpec{Board: true}
	fake := dashStory()
	s := loadDash(t.Context(), fake, board, spec, dashLabel, dashDeployment, time.Hour, graphNow)
	require.Len(t, fake.asked, 5)
	text := dashText(t, s, dashPageOf(132, 32))
	for _, want := range []string{
		"Queries · Named queries · last 1h to 05:00Z · step 30s",
		"read 05:00:07Z · 5 of 5 panels drawn · 5 of 5 queries answered",
		"namespace=gitlab (selection) · pod=~gitlab-webservice-default-.+ (name prefix of the workload)",
		"> throttled · CPU throttled share by pod  a …2bq7k 1.2%", "b …8wzpd 1.2%  c …hx4mt 4% peak 86%",
		"ingress-499 · 499 share at ingress", "gitlab-webservice-default 0.02% peak 6.3%",
		"puma-queue · Puma queued connections by pod", "a …2bq7k 0  b …8wzpd 0  c …hx4mt 0 peak 40",
		"dns-p99 · DNS request duration p99  p99 6ms",
		// A line that stopped before the window did says when.
		"hubble-drops · Hubble drops by reason", "a POLICY_DENIED – last 04:20 peak 0.8/s", "b STALE_OR_UNROUTABLE_IP 0.01/s",
	} {
		require.Contains(t, text, want)
	}
	// 80 columns: one chart per line.
	text = dashText(t, s, dashPageOf(80, 20))
	require.Contains(t, text, "> throttled · CPU throttled share by pod  a …2bq7k 1.2%  b …8wzpd 1.2%")
	require.Contains(t, text, "1-2/5 ↑↓")

	// Nothing with a namespace is selected: the namespaced queries say so,
	// the cluster-wide ones are drawn.
	s = loadDash(t.Context(), dashStory(), board, spec, "no selection", dashboard.Selection{}, time.Hour, graphNow)
	text = dashText(t, s, dashPageOf(132, 32))
	require.Contains(t, text, "> throttled · CPU throttled share by pod · NOT DRAWN: variable $namespace is not resolved: nothing with a namespace is selected")
	require.Contains(t, text, "2 of 5 panels drawn, 3 not drawn (s lists why) · 2 of 2 queries answered")
	require.Contains(t, text, "hubble-drops · Hubble drops by reason")
	require.Contains(t, text, "a POLICY_DENIED – last 04:20 peak 0.8/s")
}

func TestQueryPageChartsATypedExpressionAndSaysWhatWentWrong(t *testing.T) {
	typed := func(text string) (*dashSpec, *dashboard.Dashboard) {
		spec := querySpec(nil, text)
		return &spec, dashboard.QueryPage(spec.Query)
	}
	page := dashPageOf(100, 20)
	page.Zoom, page.Cursor = true, 120

	spec, d := typed(`sum by (pod) (puma_queued_connections{namespace="$namespace", pod=~"$pod"})`)
	s := loadDash(t.Context(), dashStory(), d, spec, dashLabel, dashDeployment, time.Hour, graphNow)
	text := dashText(t, s, page)
	for _, want := range []string{
		"Query · typed expression · last 1h to 05:00Z · step 30s",
		`sum by (pod) (puma_queued_connections{namespace="$namespace", pod=~"$pod"})`,
		"a …2bq7k 0  b …8wzpd 0  c …hx4mt 0",
		`A sum by (pod) (puma_queued_connections{namespace="gitlab", pod=~"gitlab-webservice-default-.+"})`,
		"Variables: namespace=gitlab (selection) · pod=~gitlab-webservice-default-.+ (name prefix of the",
		"e edit · Esc back",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "Panel 1 of 1")

	// A known name is that query, with its unit and legend.
	named := querySpec(nil, "dns-p99")
	require.Equal(t, "dns-p99", named.Query.Name)
	s = loadDash(t.Context(), dashStory(), dashboard.QueryPage(named.Query), &named, dashLabel, dashDeployment, time.Hour, graphNow)
	text = dashText(t, s, page)
	require.Contains(t, text, "Query · dns-p99 · last 1h to 05:00Z · step 30s")
	require.Contains(t, text, "DNS request duration p99 · seconds")
	require.Contains(t, text, "p99 6ms")
	require.Contains(t, dashReport(s), "SAVE THIS QUERY (nothing writes it; add it to the context configuration)\n  k9s:\n    observability:\n      queries:\n        dns-p99:")
	require.True(t, querySpec(nil, "  ").Board)

	// Prometheus' own words for a rejected expression, redacted of control
	// characters and bounded, and a way on.
	fake := &dashFake{rules: []dashRule{{contains: "> 3", rejected: "bad_data: 1:8: parse error: unexpected character: '>'\x1b[31m"}}}
	spec, d = typed("up > 3")
	s = loadDash(t.Context(), fake, d, spec, dashLabel, dashDeployment, time.Hour, graphNow)
	text = strings.Join(strings.Fields(dashText(t, s, page)), " ")
	for _, want := range []string{
		"NOT DRAWN: expression failed: bad_data: 1:8: parse error: unexpected character: '>' [31m.",
		"Prometheus rejected the expression as it was sent, in its own words above. Nothing was read. Press e to edit the expression.",
		"1 15m 2 1h 3 6h 4 24h · r re-read · s detail · e edit · Esc back",
	} {
		require.Contains(t, strings.ReplaceAll(text, "[]", "]"), want)
	}
	require.NotContains(t, text, "\x1b")
	require.NotContains(t, text, "┤", "a failed query draws no axis and no flat line")
	require.NotContains(t, text, "cursor", "and offers no cursor")

	// An empty answer is "no series matched"; a bare word that is not a
	// name is offered the names.
	spec, d = typed("throtled")
	s = loadDash(t.Context(), &dashFake{}, d, spec, dashLabel, dashDeployment, time.Hour, graphNow)
	text = strings.Join(strings.Fields(dashText(t, s, page)), " ")
	for _, want := range []string{"NOT DRAWN: no series matched in this window (not a zero).", "That is missing data, not a zero",
		"If a named query was meant: throttled, ingress-499, puma-queue, dns-p99, hubble-drops.", "complete · 0 series · 0 samples"} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "┤")

	// A variable without a value is refused before anything is sent.
	fake = &dashFake{}
	spec, d = typed(`up{node="$node"}`)
	s = loadDash(t.Context(), fake, d, spec, dashLabel, dashDeployment, time.Hour, graphNow)
	require.Empty(t, fake.asked)
	text = strings.Join(strings.Fields(dashText(t, s, page)), " ")
	require.Contains(t, text, "NOT DRAWN: variable $node is not resolved: no node is selected or known for the selection.")
	require.Contains(t, text, "Nothing was sent for this panel. Press e to edit the expression.")

	// A selection's name is data in a typed expression too.
	spec, d = typed(`up{pod="$pod"}`)
	fake = &dashFake{}
	loadDash(t.Context(), fake, d, spec, "Pod x", dashboard.Selection{Namespace: "apps", Pod: `x"} or vector(1) #`}, time.Hour, graphNow)
	require.Equal(t, []string{`up{pod="x\"} or vector(1) #"}`}, fake.asked)
}

func dashViewFixture(t *testing.T, spec dashSpec) *dashView { //nolint:gocritic // A test fixture takes the spec by value like the view does.
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	declareStudySources(t, app)
	ct, err := app.Config.CurrentContext()
	require.NoError(t, err)
	ct.Observability.Grafana = studyGrafana()
	origin := selection(client.DpGVR, "gitlab", "gitlab-webservice-default")
	origin.Context = app.Config.ActiveContextName()
	target, err := grafanaTargetFor(origin, "", grafanaNow)
	require.NoError(t, err)
	v := newDashView(app, spec, target, origin, 0)
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	return v
}

func TestDashboardViewReadsTheDefinitionOnceAndMovesOnRequest(t *testing.T) {
	v := dashViewFixture(t, *dashWebservice)
	require.Equal(t, dashboard.Selection{Namespace: "gitlab", Workload: "gitlab-webservice-default"}, v.selection)
	definitions, reads := 0, 0
	v.load = func(ctx context.Context, spec *dashSpec, window time.Duration, kept *dashboard.Dashboard) *dashSnapshot {
		reads++
		if kept == nil {
			definitions++
			kept = dashDefinition(t, "grafana12-gitlab-webservice.json")
		}
		return loadDash(ctx, dashStory(), kept, spec, v.target.Label, v.selection, window, graphNow)
	}
	// The app is not running in a test, so apply what a read returns directly.
	v.active = true
	v.accept(v.load(t.Context(), &v.spec, v.window, nil))
	frame := drawnText(t, v, 80, 20)
	require.Contains(t, frame, "Dashboard · GitLab / Webservice overview · last 1h to 05:00Z · step 30s")
	require.Contains(t, frame, "> Requests per second")
	require.Contains(t, frame, "Panel 1 of 11 · stat")

	key := func(k tcell.Key) {
		action, ok := v.actions.Get(k)
		require.True(t, ok)
		require.Nil(t, action.Action(tcell.NewEventKey(k, 0, tcell.ModNone)))
	}
	key(tcell.KeyRight)
	require.Contains(t, drawnText(t, v, 80, 20), "> Slowest pod p99")
	key(tcell.KeyDown)
	require.Contains(t, drawnText(t, v, 80, 20), "> CPU throttled, worst pod")
	key(tcell.KeyDown)
	require.Contains(t, drawnText(t, v, 80, 20), "> Request duration p99 by pod")
	key(tcell.KeyUp)
	key(tcell.KeyHome)
	require.Contains(t, drawnText(t, v, 80, 20), "> Requests per second")
	key(tcell.KeyTab)
	key(tcell.KeyTab)
	key(tcell.KeyTab)
	key(tcell.KeyTab)
	require.Equal(t, 5, v.selected)

	// Enter zooms the selected panel; there the arrows move the cursor.
	key(tcell.KeyEnter)
	require.True(t, v.zoom)
	frame = drawnText(t, v, 80, 20)
	require.Contains(t, frame, "Panel 5 of 11 · timeseries · Request duration p99 by pod · seconds")
	require.Contains(t, frame, "Cursor 05:00:00Z (window end)")
	for range 62 {
		key(tcell.KeyLeft)
	}
	frame = drawnText(t, v, 80, 20)
	require.Contains(t, frame, "c …hx4mt 47.9s")
	require.Contains(t, frame, "Cursor 04:29:00Z, 31m before window end")
	left, _ := v.actions.Get(tcell.KeyLeft)
	require.Nil(t, left.Action(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModShift)))
	require.Contains(t, drawnText(t, v, 80, 20), "Cursor 04:24:00Z")
	key(tcell.KeyEnd)
	require.True(t, v.atEnd)
	key(tcell.KeyDown)
	require.Contains(t, drawnText(t, v, 80, 20), "Panel 6 of 11 · timeseries · Requests by pod")
	key(tcell.KeyEscape)
	require.False(t, v.zoom, "Esc leaves the zoom, not the view")
	require.Contains(t, drawnText(t, v, 80, 20), "> Requests by pod")
	require.Equal(t, 1, reads, "moving reads nothing")

	// The report is wrapped on plain text, so brackets are never markup.
	key(ui.KeyS)
	report := v.text.GetText(true)
	require.Contains(t, report, "Dashboard GitLab / Webservice overview · READ ONLY")
	require.Contains(t, report, `rate(http_request_duration_seconds_bucket{namespace="gitlab",`)
	for _, line := range strings.Split(report, "\n") {
		require.LessOrEqual(t, runewidth.StringWidth(strings.ReplaceAll(line, "[]", "]")), 80, line)
	}
	key(tcell.KeyEscape)
	require.False(t, v.report)

	// A window key reads the panels again and keeps the definition; the
	// new window's name is shown only with its own data.
	v.window = 6 * time.Hour
	v.render()
	text := v.text.GetText(true)
	require.Contains(t, text, "Dashboard · GitLab / Webservice overview · last 6h")
	require.Contains(t, text, "Reading the panels' last 6h once")
	require.NotContains(t, text, "Requests by pod")
	v.accept(v.load(t.Context(), &v.spec, 6*time.Hour, v.snapshot.Dash))
	require.Contains(t, v.text.GetText(true), "last 6h to 05:00Z · step 5m")
	require.Equal(t, 1, definitions, "the definition is read once")
	require.Equal(t, 2, reads)
	require.Contains(t, dashReport(v.snapshot), "$__interval is the step, 5m. $__rate_interval is 20m")

	window, ok := v.GrafanaWindow()
	require.True(t, ok)
	require.Equal(t, "the window drawn here, last 6h", window.Reason)
	require.Equal(t, "gitlab-webservice-default", v.SelectedResource().Name)
}

func TestDashboardViewDiscardsRepliesAfterItClosedOrTheDestinationChanged(t *testing.T) {
	v := dashViewFixture(t, *dashWebservice)
	release, done := make(chan struct{}), make(chan struct{})
	v.load = func(ctx context.Context, spec *dashSpec, window time.Duration, _ *dashboard.Dashboard) *dashSnapshot {
		defer close(done)
		<-release
		return loadDash(ctx, dashStory(), dashDefinition(t, "grafana12-gitlab-webservice.json"), spec, "x", dashDeployment, window, graphNow)
	}
	v.Start()
	require.Equal(t, "Reading the dashboard definition, then its panels' last 1h, once…", v.busy)
	require.Contains(t, v.text.GetText(true), "Reading the dashboard definition, then its panels' last 1h, once…\nEsc cancels and goes back.")
	v.Stop()
	close(release)
	<-done
	require.Nil(t, v.snapshot)

	// A changed destination refuses a new read instead of mixing contexts.
	v.active = true
	v.revision++
	v.load = func(context.Context, *dashSpec, time.Duration, *dashboard.Dashboard) *dashSnapshot {
		t.Fatal("must not read")
		return nil
	}
	v.refresh()
	require.Empty(t, v.busy)
}

func TestDashboardViewOpensTheSameDashboardInGrafana(t *testing.T) {
	t.Setenv(clipboardModeEnv, "")
	fake := newFakeDelivery("linux", map[string]string{})
	useDelivery(t, fake)
	v := dashViewFixture(t, *dashWebservice)
	v.active = true
	v.accept(loadDash(t.Context(), dashStory(), dashDefinition(t, "grafana12-gitlab-webservice.json"), &v.spec, v.target.Label, v.selection, time.Hour, graphNow))
	v.openInGrafana()
	h := v.app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	text := handoffText(h, 100)
	require.Contains(t, text, "Link    Dashboard · gitlab-webservice · the dashboard drawn here")
	require.Contains(t, text, "Fills   namespace=gitlab")
	require.Contains(t, text, "the window drawn here, last 1h")
	require.Len(t, fake.copied, 1)
	require.True(t, strings.HasPrefix(fake.copied[0], "https://grafana.example.test/d/gitlab-webservice?from="), fake.copied[0])
	require.Contains(t, fake.copied[0], "&to=now&var-namespace=gitlab")
	require.NotContains(t, fake.copied[0], "var-pod", "a saved value is the dashboard's own and is not carried")
	h.close()

	// A query page opens Explore with the expression as it was sent.
	spec := querySpec(nil, "puma-queue")
	q := dashViewFixture(t, spec)
	q.active = true
	q.accept(loadDash(t.Context(), dashStory(), dashboard.QueryPage(spec.Query), &q.spec, q.target.Label, q.selection, time.Hour, graphNow))
	q.openInGrafana()
	h = q.app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	text = handoffText(h, 100)
	require.Contains(t, text, "Link    Explore metrics · sum by (pod) (puma_queued_connections{namespace=\"gitlab\"")
	require.Contains(t, text, "datasource prom-main · the expression as it was sent here")
	require.Contains(t, fake.copied[1], "https://grafana.example.test/explore?")
	h.close()
}

func TestQueryIsRegisteredAsCommandPaletteEntryAndCompletesNames(t *testing.T) {
	v := dashViewFixture(t, dashSpec{Board: true})
	require.False(t, v.zoom)
	var found *ui.ActionDescriptor
	catalog := actionCatalog(v, v.app)
	for index := range catalog {
		if catalog[index].ID == "command.query" {
			found = &catalog[index]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, ":query", found.Shortcut)
	// The palette gives a label 40 cells; a longer one is cut mid-word.
	require.Equal(t, "Query: chart PromQL or a named query", found.Label)
	require.LessOrEqual(t, len(found.Label), 40)
	require.Equal(t, ui.ActionInspect, found.Category)
	require.Empty(t, found.UnavailableReason)
	command := NewCommand(v.app)
	command.updateSuggestionAliases()
	require.Contains(t, command.suggestionAliases(), queryCommandToken)

	ct, err := v.app.Config.CurrentContext()
	require.NoError(t, err)
	sources := ct.Observability
	sources.Queries = map[string]observability.NamedQuery{"puma-busy": {Expr: "sum(puma_running)"}}
	require.Equal(t, []string{"ma-queue", "ma-busy"}, querySuggestions(&sources, "query pu"))
	require.Equal(t, []string{"throttled", "ingress-499", "puma-queue", "dns-p99", "hubble-drops", "puma-busy"}, querySuggestions(&sources, "query "))
	require.Empty(t, querySuggestions(&sources, "query dns-p99"), "a complete name suggests nothing")
	require.Empty(t, querySuggestions(&sources, "query sum(up"))
	require.Nil(t, querySuggestions(&sources, "queryx"))
	saved := querySpec(&sources, "puma-busy")
	require.Equal(t, "sum(puma_running)", saved.Query.Expr)
	require.False(t, saved.Query.Builtin)

	// A query typed while a query page is open replaces what it draws and
	// keeps the selection the page was opened for.
	var asked []string
	v.load = func(_ context.Context, spec *dashSpec, _ time.Duration, _ *dashboard.Dashboard) *dashSnapshot {
		if spec.Query != nil {
			asked = append(asked, spec.Query.Expr)
		}
		return nil
	}
	v.active = true
	command.queryCommand(`query sum(rate(http_requests_total{namespace="$namespace"}[5m]))`)
	require.Same(t, v, v.app.Content.Top())
	require.True(t, v.zoom, "one query is drawn alone")
	require.Equal(t, `sum(rate(http_requests_total{namespace="$namespace"}[5m]))`, v.spec.Query.Expr)
	require.Contains(t, v.text.GetText(true), "Query · typed expression · last 1h")
	require.Equal(t, "gitlab-webservice-default", v.SelectedResource().Name)
	// The page of one query is its zoomed panel: Enter is not offered as a
	// key that would do nothing. The board offers it again.
	enterShown := func() bool {
		for _, hint := range v.Hints() {
			if hint.Description == "Zoom panel" {
				return hint.Visible
			}
		}
		return false
	}
	require.False(t, enterShown())
	command.queryCommand("QUERY")
	require.True(t, v.spec.Board)
	require.False(t, v.zoom)
	require.True(t, enterShown())

	// e puts the expression back into the prompt.
	command.queryCommand("query   up{job=\"x\"}")
	edit, ok := v.actions.Get(dashEditKey)
	require.True(t, ok)
	edit.Action(nil)
	require.True(t, v.app.CmdBuff().IsActive())
	require.Equal(t, `query up{job="x"}`, v.app.CmdBuff().GetText())
	v.app.CmdBuff().SetActive(false)

	// Honest reasons: no declared Prometheus, and saved queries that
	// cannot be used.
	ct.Observability.Queries = map[string]observability.NamedQuery{"Bad Name": {Expr: "up"}}
	before := v.spec
	command.queryCommand("query up")
	require.Equal(t, before, v.spec, "a broken declaration runs nothing")
	ct.Observability = observability.Sources{}
	require.Equal(t, "No Prometheus source is declared for this context; :sources shows what to add", dashAvailability(v.app))
	ct.Observability = observability.Sources{Grafana: &observability.GrafanaSource{Source: observability.Source{Service: "monitoring/grafana:3000"}, Datasource: "prom-main"}}
	require.Empty(t, dashAvailability(v.app), "a Grafana datasource can answer the queries")
}

func TestDashboardsListDrawsADashboardHereOnV(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 5, 0, time.UTC)
	result := observability.DashboardsResult{Status: observability.Status{Kind: observability.KindGrafana, State: observability.StateComplete, ObservedAt: at},
		Dashboards: studyHits()}
	list, _ := dashboardsFixture(t, studyGrafana(), result)
	ct, err := list.app.Config.CurrentContext()
	require.NoError(t, err)
	ct.Observability.Prometheus = &observability.Source{Service: "monitoring/prometheus:9090"}
	list.accept(&result)
	require.Contains(t, list.footer.GetText(true), "v view here · Enter open/copy · / filter")
	view, ok := list.actions.Get(dashViewKey)
	require.True(t, ok)
	require.Equal(t, "View dashboard here", view.Description)

	// A declared dashboard is drawn with its declared variables.
	list.table.Select(1, 0)
	require.Nil(t, view.Action(nil))
	top, isDash := list.app.Content.Top().(*dashView)
	require.True(t, isDash)
	require.Equal(t, dashSpec{UID: "k8s-pod", Hint: "Kubernetes / Compute Resources / Pod", For: "pod",
		Mapping: dashboard.Mapping{Namespace: "namespace", Pod: "pod"}, Declared: true}, top.spec)
	require.Equal(t, dashboard.Selection{Namespace: "apps", Pod: "checkout-api-7d9f8c6b5d-x2x9q"}, top.selection)
	top.Stop()
	list.app.Content.Pop()

	// One found by search is matched by the conventional names.
	list.table.Select(7, 0)
	require.Equal(t, "checkout-slo", list.selectedRow().UID)
	view.Action(nil)
	top = list.app.Content.Top().(*dashView)
	require.Equal(t, dashSpec{UID: "checkout-slo", Hint: "Checkout / Service SLO", Mapping: dashboard.ConventionalMapping}, top.spec)
	top.Stop()
	list.app.Content.Pop()

	// An Explore row is a link, not a dashboard.
	list.table.Select(5, 0)
	view.Action(nil)
	require.Same(t, list, list.app.Content.Top())

	// Without a Prometheus source nothing could be drawn, and it says so.
	ct.Observability.Prometheus, ct.Observability.Grafana.Datasource = nil, ""
	list.table.Select(1, 0)
	view.Action(nil)
	require.Same(t, list, list.app.Content.Top())
}
