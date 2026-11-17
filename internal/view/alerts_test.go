// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/problems"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var alertsTestReadAt = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func alertsTestItem(name, severity string, ago time.Duration, labels map[string]string) alerts.Item {
	all := map[string]string{"alertname": name, "severity": severity}
	for key, value := range labels {
		all[key] = value
	}
	return alerts.NewItem(&observability.Alert{
		Fingerprint: "fp-" + name + labels["pod"], Labels: all, State: observability.AlertActive, StartsAt: alertsTestReadAt.Add(-ago),
		Annotations:  map[string]string{"summary": name + " summary [x]", "description": name + " description", "runbook_url": "https://runbooks.example.test/" + name},
		GeneratorURL: "http://prometheus.example.test/graph", Receivers: []string{"oncall"}})
}

func alertsTestSnapshot() *alerts.Snapshot {
	owner := &alerts.Target{Kind: "Deployment", Namespace: "apps", Name: "checkout-api", Resource: "apps/v1/deployments"}
	podA := alertsTestItem("KubePodNotReady", "warning", 7*time.Minute, map[string]string{"namespace": "apps", "pod": "checkout-api-7d9f8c6b5d-p8wm"})
	podB := alertsTestItem("KubePodNotReady", "warning", 6*time.Minute, map[string]string{"namespace": "apps", "pod": "checkout-api-7d9f8c6b5d-x2x9q"})
	podA.Owner, podA.OwnerFrom, podB.Owner, podB.OwnerFrom = owner, "owner references", owner, "owner references"
	inhibited := alertsTestItem("KubeNodeUnreachable", "warning", 16*time.Minute, map[string]string{"node": "worker-b"})
	inhibited.Alert.State, inhibited.Alert.InhibitedBy = observability.AlertSuppressed, []string{"fp-KubeNodeNotReady"}
	silenced := alertsTestItem("NodeFilesystemAlmostOutOfSpace", "warning", 25*time.Minute, map[string]string{"node": "worker-c"})
	silenced.Alert.State, silenced.Alert.SilencedBy = observability.AlertSuppressed, []string{"silence-1"}
	watchdog := alertsTestItem("Watchdog", "none", 6*24*time.Hour, nil)
	return &alerts.Snapshot{
		Status: observability.Status{Kind: observability.KindAlertmanager, State: observability.StateComplete}, ReadAt: alertsTestReadAt,
		Owners: alerts.OwnerRead{State: alerts.OwnersComplete, Asked: 2, Resolved: 2},
		Items: []alerts.Item{
			alertsTestItem("CheckoutHighErrorRate", "critical", 9*time.Minute, map[string]string{"namespace": "apps", "deployment": "checkout-api", "service": "checkout-api"}),
			alertsTestItem("KubeNodeNotReady", "critical", 18*time.Minute, map[string]string{"node": "worker-b"}),
			podA, podB, inhibited, silenced, watchdog,
		},
	}
}

func alertsTestLines(showSuppressed bool) ([]alerts.Group, []alertsLine) {
	groups := alerts.Groups(alertsTestSnapshot().Items)
	return groups, alertsLines(groups, map[string]bool{}, "", showSuppressed)
}

func TestAlertsListSectionsFoldingAndSuppressedToggle(t *testing.T) {
	groups, lines := alertsTestLines(true)
	require.Len(t, groups, 6)
	var got []string
	for i := range lines {
		if lines[i].heading != "" {
			got = append(got, "# "+lines[i].heading)
			continue
		}
		got = append(got, lines[i].group.State+" "+alertsLineName(&lines[i], false)+" | "+alertsLineObject(&lines[i], true))
	}
	assert.Equal(t, []string{
		"firing CheckoutHighErrorRate | deploy apps/checkout-api",
		"firing KubeNodeNotReady | node worker-b",
		"firing ▸ KubePodNotReady | 2 pods of deploy apps/checkout-api",
		"# not mapped to an object",
		"firing Watchdog | not mapped",
		"# silenced or inhibited: not notifying",
		"inhibited KubeNodeUnreachable | node worker-b",
		"silenced NodeFilesystemAlmostOutOfSpace | node worker-c",
	}, got, "silenced and inhibited alerts are listed as such, and an unmapped alert is not dropped")

	_, hidden := alertsTestLines(false)
	assert.Len(t, hidden, 5, "z hides the alerts that are not notifying and their heading")

	expanded := alertsLines(groups, map[string]bool{groups[2].Key(): true}, "", true)
	require.Len(t, expanded, 10)
	assert.True(t, expanded[2].expanded)
	assert.Equal(t, "  └ KubePodNotReady", alertsLineName(&expanded[3], false))
	assert.Equal(t, "  | KubePodNotReady", alertsLineName(&expanded[3], true), "markers have an ASCII form")
	assert.Equal(t, "- KubePodNotReady", alertsLineName(&expanded[2], true))
	assert.Equal(t, "+ KubePodNotReady", alertsLineName(&lines[2], true))
	assert.Equal(t, "pod apps/checkout-api-7d9f8c6b5d-p8wm", alertsLineObject(&expanded[3], true))

	// A filter that matches one folded alert shows that alert under its row.
	filtered := alertsLines(groups, map[string]bool{}, "x2x9q", true)
	require.Len(t, filtered, 2)
	assert.True(t, filtered[1].child)
	assert.Equal(t, "checkout-api-7d9f8c6b5d-x2x9q", filtered[1].item.Target.Name)
	assert.Len(t, alertsLines(groups, map[string]bool{}, "service=checkout-api critical", true), 1, "labels are searchable")
	assert.Len(t, alertsLines(groups, map[string]bool{}, "silenced", true), 2, "the state word is searchable")
}

func TestAlertsLayoutKeepsSeverityNameAndObjectAtEveryWidth(t *testing.T) {
	groups, _ := alertsTestLines(true)
	lines := alertsLines(groups, map[string]bool{groups[2].Key(): true}, "", true)
	for _, width := range []int{58, 78, 118, 158} {
		columns := alertsLayout(width, lines, false, alertsTestReadAt)
		heading := alertsHeading(columns)
		assert.LessOrEqual(t, runewidth.StringWidth(heading), width, "width %d heading", width)
		for _, label := range []string{"SEVERITY", "ALERT", "OBJECT"} {
			assert.Contains(t, heading, label, "width %d", width)
		}
		assert.Equal(t, width >= 72, strings.Contains(heading, "STATE"), "width %d", width)
		assert.Equal(t, width >= 72, strings.Contains(heading, "FOR"), "width %d", width)
		assert.Equal(t, width >= 100, strings.Contains(heading, "SUMMARY"), "width %d", width)
		for i := range lines {
			if lines[i].heading != "" {
				continue
			}
			severity, rest := alertsCells(&lines[i], columns, false, width < 100, alertsTestReadAt)
			assert.Equal(t, runewidth.StringWidth(heading), runewidth.StringWidth(severity+rest), "width %d row %d is aligned with the heading", width, i)
			assert.True(t, strings.HasPrefix(severity, lines[i].group.Severity), "severity is never elided")
			assert.GreaterOrEqual(t, columns.name, 15, "width %d keeps a readable alert name", width)
			assert.GreaterOrEqual(t, columns.object, 20, "width %d keeps a readable object", width)
		}
	}
	// A pod name keeps both ends, and a folded row keeps its count.
	columns := alertsLayout(58, lines, false, alertsTestReadAt)
	_, pod := alertsCells(&lines[3], columns, false, true, alertsTestReadAt)
	assert.Contains(t, pod, "p8wm")
	assert.Contains(t, pod, "pod apps/")
	_, folded := alertsCells(&lines[2], columns, false, true, alertsTestReadAt)
	assert.Contains(t, folded, "2 pods of deploy")
}

func TestAlertsHeaderStatesSourceTimeCountsAndWhatWasNotRead(t *testing.T) {
	snapshot := alertsTestSnapshot()
	source := "Alertmanager service monitoring/alertmanager:9093"
	wide := strings.Join(alertsHeader(snapshot, source, "", 0, 118), "\n")
	assert.Contains(t, wide, "5 firing (2 critical, 2 warning, 1 none) · 1 silenced · 1 inhibited · 1 not mapped")
	assert.Contains(t, wide, "Read at "+alertsTestReadAt.Local().Format("15:04:05")+" from "+source+" · owner read for 2 pods")
	assert.Contains(t, wide, "One read, no polling")
	assert.NotContains(t, wide, "NOT READ")

	narrow := alertsHeader(snapshot, source, "", 0, 58)
	assert.Len(t, narrow, 2, "below 100 columns the standing reminder gives its row to the list")
	assert.Contains(t, narrow[0], "5 firing (2 critical, 2 warning, 1 none) · 2 not notifying · 1 unmapped")
	assert.Contains(t, narrow[1], "service monitoring/alertmanager:9093")
	assert.Contains(t, alertsHeader(snapshot, source, "", 2, 118)[0], "2 not notifying, hidden (z shows)")

	snapshot.NotRetained = 12
	snapshot.Owners = alerts.OwnerRead{State: alerts.OwnersDenied, Asked: 2, Detail: "pod metadata read is not permitted"}
	gaps := alertsHeader(snapshot, source, "", 0, 58)
	require.Len(t, gaps, 3, "a NOT READ line is never dropped for width")
	assert.Equal(t, "NOT READ: 12 alerts beyond the limit of 500 · pod owners denied (pod metadata read is not permitted), those alerts are listed by pod", gaps[2])

	denied := &alerts.Snapshot{ReadAt: alertsTestReadAt, Status: observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}}
	text := strings.Join(alertsHeader(denied, source, "", 0, 78), "\n")
	assert.Contains(t, text, "NOT READ: Alertmanager denied (HTTP 403)")
	assert.Contains(t, text, "what is firing is unknown, not empty")
	assert.NotContains(t, text, "0 firing", "an unread source is not an empty one")
	assert.Contains(t, alertsEmpty(denied, "", 0), `This is not "no alerts"`)

	undeclared := &alerts.Snapshot{Status: observability.Status{State: observability.StateNotConfigured}}
	assert.Contains(t, alertsEmpty(undeclared, "", 0), "nothing was contacted")
	quiet := &alerts.Snapshot{ReadAt: alertsTestReadAt, Status: observability.Status{State: observability.StateComplete}}
	assert.Contains(t, alertsHeader(quiet, source, "", 0, 118)[0], "Alertmanager holds no alert")
	assert.Contains(t, alertsEmpty(quiet, "", 0), "not a health check")
	assert.Contains(t, alertsEmpty(snapshot, "zzz", 0), `No alert matches filter "zzz"`)
	assert.Contains(t, alertsEmpty(snapshot, "", 2), "2 alerts silenced or inhibited are hidden; z shows them")
}

func TestAlertsDetailAndReportNameEveryFieldAndNeverClaimACause(t *testing.T) {
	groups, lines := alertsTestLines(true)
	names := map[string]string{"fp-KubeNodeNotReady": "KubeNodeNotReady"}
	head, body := alertsDetail(&lines[0], alertsTestReadAt, names, 0)
	assert.Equal(t, "CheckoutHighErrorRate · critical · firing for 9m", head)
	for _, want := range []string{
		"Deployment apps/checkout-api · mapped from labels namespace + deployment",
		"CheckoutHighErrorRate summary [x]", "CheckoutHighErrorRate description",
		"Began 2026-11-17 11:51:00Z · runbook https://runbooks.example.test/CheckoutHighErrorRate",
		"Labels: deployment=checkout-api · namespace=apps · service=checkout-api · severity=critical",
		"Generator: http://prometheus.example.test/graph",
	} {
		assert.Contains(t, body, want)
	}
	// In a pane too narrow for both, the link starts its own row instead of
	// being broken in the middle.
	_, narrow := alertsDetail(&lines[0], alertsTestReadAt, names, 77)
	assert.Contains(t, narrow, "Began 2026-11-17 11:51:00Z\nRunbook: https://runbooks.example.test/CheckoutHighErrorRate\n")

	head, body = alertsDetail(&lines[2], alertsTestReadAt, names, 0)
	assert.Equal(t, "KubePodNotReady · warning · firing for 7m · 2 alerts", head)
	assert.Contains(t, body, "Deployment apps/checkout-api: the same alert on 2 pods (checkout-api-7d9f8c6b5d-p8wm, checkout-api-7d9f8c6b5d-x2x9q)")
	assert.Contains(t, body, "Folded by owner references")

	_, body = alertsDetail(&lines[4], alertsTestReadAt, names, 0)
	assert.Contains(t, body, "Not mapped to an object: its labels name no namespace, workload, pod or node.")
	head, body = alertsDetail(&lines[6], alertsTestReadAt, names, 0)
	assert.Contains(t, head, "inhibited for 16m")
	assert.Contains(t, body, "Inhibited by KubeNodeNotReady (fp-KubeNodeNotReady): it is held but does not notify.")
	_, body = alertsDetail(&lines[7], alertsTestReadAt, names, 0)
	assert.Contains(t, body, "Silenced by silence silence-1")

	item := &groups[2].Items[0]
	item.Missing = true
	report := alertsReport(item, alertsTestSnapshot(), "Alertmanager url https://alerts.example.test", names)
	for _, want := range []string{
		"ALERT KubePodNotReady", "State: firing for 7m", "Pod apps/checkout-api-7d9f8c6b5d-p8wm", "Mapped from labels: namespace + pod",
		"Workload: Deployment checkout-api (from owner references)", "NOT FOUND at the read",
		"Started: 2026-11-17 11:53:00Z", "LINKS (text only; never contacted)", "Runbook:   https://runbooks.example.test/KubePodNotReady",
		"Generator: http://prometheus.example.test/graph", "  pod=checkout-api-7d9f8c6b5d-p8wm", "  summary=KubePodNotReady summary [x]",
		"RECEIVERS\n  oncall", "It is not checked against what the rule measures.",
	} {
		assert.Contains(t, report, want)
	}
	for _, text := range []string{report, body} {
		assert.NotContains(t, strings.ToLower(text), "caused")
		assert.NotContains(t, strings.ToLower(text), "root cause")
	}
}

func TestAlertsFooterKeepsFilterAndRereadAtEveryWidth(t *testing.T) {
	assert.Equal(t, "Enter open · t timeline · d all fields · m graph · Shift-D Grafana · c copy runbook · Space expand · z silenced · / filter · r re-read · Esc back",
		alertsFooter(158, true, true))
	assert.Equal(t, "Enter open  t timeline  d fields  m graph  D Grafana  c copy  Space fold  z silenced  / filter  r reread  Esc",
		alertsFooter(118, true, true), "a 120 column terminal names the graph and the Grafana hand-off")
	assert.Equal(t, "Enter open  t timeline  d fields  m graph  z silenced  / filter  r reread  Esc", alertsFooter(78, true, true))
	for _, width := range []int{58, 78} {
		footer := alertsFooter(width, true, true)
		assert.LessOrEqual(t, runewidth.StringWidth(footer), width)
		for _, hint := range []string{"Enter open", "z silenced", "/ filter", "r reread", "Esc"} {
			assert.Contains(t, footer, hint, "width %d", width)
		}
		assert.NotContains(t, footer, "…")
	}
	assert.Equal(t, "r re-read · Esc back", alertsFooter(58, false, false))
}

func alertsTestView(t *testing.T, declare bool) *alertsView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	if declare {
		declareStudySources(t, app)
	}
	v := newAlertsView(app)
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	return v
}

func TestAlertsViewSelectionExpansionAndAvailability(t *testing.T) {
	v := alertsTestView(t, true)
	assert.Equal(t, "Alertmanager url https://alerts.example.test", v.source, "the header names the source, never a token")
	v.acceptSnapshot(alertsTestSnapshot())
	require.Len(t, v.lines, 8)
	row, _ := v.table.GetSelection()
	assert.Equal(t, 1, row)

	target := v.SelectedResource()
	require.NoError(t, target.Err())
	assert.Equal(t, "apps/v1/deployments", target.GVR.String())
	assert.Equal(t, "apps/checkout-api", target.Path())
	assert.Empty(t, v.availability(tcell.KeyEnter))
	assert.Empty(t, v.availability(ui.KeyT))
	assert.Empty(t, v.availability(ui.KeyD))
	assert.Empty(t, v.availability(ui.KeyC))
	assert.Contains(t, v.availability(ui.KeySpace), "folds several alerts")
	assert.Contains(t, v.detail.GetText(false), "CheckoutHighErrorRate summary [x[]", "source text is escaped, not interpreted as a color tag")

	v.table.Select(2, 0) // node
	assert.Equal(t, "v1/nodes", v.SelectedResource().GVR.String())
	assert.Contains(t, v.availability(ui.KeyT), "A timeline follows a namespaced workload")
	assert.Empty(t, v.availability(tcell.KeyEnter))

	v.table.Select(3, 0) // folded row: the workload
	assert.Equal(t, "apps/checkout-api", v.SelectedResource().Path())
	assert.Contains(t, v.availability(ui.KeyD), "Space lists each alert")
	v.key(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	require.Len(t, v.lines, 10)
	v.table.Select(4, 0)
	assert.Equal(t, "apps/checkout-api-7d9f8c6b5d-p8wm", v.SelectedResource().Path())
	assert.Equal(t, "v1/pods", v.SelectedResource().GVR.String())
	// Folding from an alert returns the selection to its row.
	v.key(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	require.Len(t, v.lines, 8)
	row, _ = v.table.GetSelection()
	assert.Equal(t, 3, row)

	v.table.Select(5, 0) // Watchdog
	assert.ErrorContains(t, v.SelectedResource().Err(), "labels name no Kubernetes object")
	assert.Contains(t, v.availability(tcell.KeyEnter), "labels name no Kubernetes object")
	assert.Empty(t, v.availability(ui.KeyD), "every field of an unmapped alert can still be read")

	v.key(tcell.NewEventKey(tcell.KeyRune, 'z', tcell.ModNone))
	require.Len(t, v.lines, 5)
	assert.Contains(t, v.header.GetText(true), "2 not notifying, hidden (z shows)")
	v.key(tcell.NewEventKey(tcell.KeyRune, 'z', tcell.ModNone))
	require.Len(t, v.lines, 8)

	v.SetFilter("worker-b", false)
	require.Len(t, v.lines, 3)
	assert.Contains(t, v.header.GetText(true), `filter "worker-b" shows 2 of 6 rows`)
	assert.NotNil(t, v.selectedLine(), "the selection never rests on a section heading")
	v.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	assert.Len(t, v.lines, 8, "Esc clears the filter before it leaves the view")
	assert.Same(t, v, v.app.Content.Top())

	// A re-read keeps the selection on the same alert.
	v.table.Select(2, 0)
	next := alertsTestSnapshot()
	next.Items = append([]alerts.Item{alertsTestItem("NewPage", "critical", time.Minute, map[string]string{"namespace": "ops", "statefulset": "db"})}, next.Items...)
	v.acceptSnapshot(next)
	assert.Equal(t, "KubeNodeNotReady", v.selectedLine().group.Name)

	// The plain ASCII form carries the same structure.
	v.app.Config.K9s.UI.NoIcons = true
	v.render()
	assert.Contains(t, v.table.GetCell(4, 0).Text, "+ KubePodNotReady")
	assert.Contains(t, v.table.GetCell(5, 0).Text, "-- not mapped to an object -")
}

func TestAlertsViewReadsOnceOnRequestAndDiscardsLateReplies(t *testing.T) {
	v := alertsTestView(t, true)
	calls := 0
	release := make(chan struct{})
	v.read = func(ctx context.Context) alerts.Snapshot {
		calls++
		select {
		case <-release:
		case <-ctx.Done():
		}
		return *alertsTestSnapshot()
	}
	v.refresh()
	assert.True(t, v.loading)
	assert.Contains(t, v.header.GetText(true), "reading Alertmanager once")
	assert.Equal(t, 0, len(v.lines), "nothing is listed before the read answers")
	// Leaving cancels the read; its reply must not land later.
	v.Stop()
	close(release)
	assert.False(t, v.loading)
	assert.Nil(t, v.snapshot)
	assert.Contains(t, v.status, "read canceled while away")

	v.destinationRevision++
	v.refresh()
	assert.Contains(t, v.header.GetText(true), "destination changed; reopen :alerts")
	assert.Contains(t, v.availability(tcell.KeyEnter), "Destination changed")
}

func TestAlertsViewWithoutADeclarationContactsNothingAndSaysSo(t *testing.T) {
	v := alertsTestView(t, false)
	read, declared := alertsReader(v.app, true)
	assert.False(t, declared)
	snapshot := read(t.Context())
	assert.Equal(t, observability.StateNotConfigured, snapshot.Status.State)
	v.acceptSnapshot(&snapshot)
	assert.Contains(t, v.header.GetText(true), "NOT READ: Alertmanager not-configured")
	assert.Contains(t, v.detail.GetText(true), "No Alertmanager source is declared for this context, so nothing was contacted.")
	assert.Nil(t, v.selectedLine())
}

func TestAlertsCommandIsRegisteredWithAnHonestReason(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	find := func() *ui.ActionDescriptor {
		owner := &discoveryOwner{Details: NewDetails(app, "test", "", contentTXT, false)}
		for _, descriptor := range actionCatalog(owner, app) {
			if descriptor.ID == "command.alerts" {
				return &descriptor
			}
		}
		return nil
	}
	found := find()
	require.NotNil(t, found, "the palette and Help list :alerts")
	assert.Equal(t, ":alerts", found.Shortcut)
	assert.True(t, found.Discoverable)
	assert.Equal(t, alertsNotDeclared, found.UnavailableReason, "without a declared Alertmanager the action says why it cannot run")

	command := NewCommand(app)
	command.updateSuggestionAliases()
	assert.Contains(t, command.suggestionAliases(), alertsCommandToken)
	assert.True(t, command.toolkitCmd(cmd.NewInterpreter(alertsCommandToken)), "typed :alerts is dispatched, not resolved as a resource alias")
	assert.Nil(t, app.Content.Top(), "nothing opens, and nothing is contacted, without a declaration")

	declareStudySources(t, app)
	assert.Empty(t, find().UnavailableReason)
}

func TestProblemsTakesAlertsOnlyForItsScopeAndNamesAnUnreadSource(t *testing.T) {
	observed := alertsTestSnapshot()
	observed.Items = append(observed.Items,
		alertsTestItem("OtherNamespace", "critical", time.Minute, map[string]string{"namespace": "ops", "deployment": "ledger"}),
		alertsTestItem("NamespaceQuota", "warning", time.Minute, map[string]string{"namespace": "apps"}))
	snapshot := problemsTestSnapshot()
	snapshot.Namespace = "apps"
	problemsAddAlerts(snapshot, observed)

	var source problems.Source
	for _, s := range snapshot.Sources {
		if s.Name == problems.AlertsSource {
			source = s
		}
	}
	assert.Equal(t, problems.StateComplete, source.State)
	assert.Equal(t, 7, source.Count, "node alerts and the namespace's own; not another namespace's, not the unmapped one")
	deployment := &snapshot.Rows[0]
	require.Equal(t, "checkout-api", deployment.Name)
	require.Len(t, deployment.Alerts, 1)
	assert.Equal(t, 6, deployment.Signals)
	line := &problemsLine{row: deployment}
	assert.Contains(t, problemsDetail(line, problemsTestReadAt),
		"Alerts: CheckoutHighErrorRate (critical, firing 9m) CheckoutHighErrorRate summary [x]")
	assert.True(t, problemsMatch(deployment, "alert checkouthigherrorrate"))

	var alerting []string
	for i := range snapshot.Rows {
		if row := &snapshot.Rows[i]; row.Severity == problems.Alerting {
			alerting = append(alerting, row.Kind+" "+row.Name)
			assert.True(t, row.FromAlert)
			assert.Contains(t, problemsDetail(&problemsLine{row: row}, problemsTestReadAt),
				"Listed as alerting: its own status, where it was read, reports no problem.")
		}
	}
	assert.ElementsMatch(t, []string{"Node worker-b", "Pod checkout-api-7d9f8c6b5d-p8wm", "Pod checkout-api-7d9f8c6b5d-x2x9q", "Namespace apps"}, alerting)
	assert.Contains(t, problemsCounts(snapshot.Rows), "4 alerting")
	narrow := strings.Join(problemsHeader(snapshot, "apps", "", 78), "\n")
	assert.Contains(t, narrow, "objects · 7 alerts", "alerts are counted apart from Kubernetes objects")

	// Declared but denied: the NOT READ line says so and no row changes.
	denied := problemsTestSnapshot()
	problemsAddAlerts(denied, &alerts.Snapshot{Status: observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}})
	header := strings.Join(problemsHeader(denied, "", "", 118), "\n")
	assert.Contains(t, header, "alerts denied (HTTP 403)")
	assert.Len(t, denied.Rows, 3)
	timedOut := problemsTestSnapshot()
	problemsAddAlerts(timedOut, &alerts.Snapshot{Status: observability.Status{State: observability.StateTimeout}})
	assert.Contains(t, strings.Join(problemsHeader(timedOut, "", "", 118), "\n"), "alerts unknown (timed out)")

	// Not declared: the header does not mention alerts at all.
	assert.NotContains(t, strings.Join(problemsHeader(problemsTestSnapshot(), "", "", 118), "\n"), "alert")
}
