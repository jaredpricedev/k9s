// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/problems"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var problemsTestReadAt = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func problemsTestSnapshot() *problems.Snapshot {
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	return &problems.Snapshot{
		ReadAt: problemsTestReadAt, Window: time.Hour,
		Sources: []problems.Source{
			{Name: "pods", State: problems.StateComplete, Count: 8},
			{Name: "deployments", State: problems.StateComplete, Count: 3},
			{Name: "replicasets", State: problems.StateComplete, Count: 1, Support: true},
			{Name: "nodes", State: problems.StateDenied},
			{Name: "jobs", State: problems.StateUnknown, Detail: "timed out"},
			{Name: "pvcs", State: problems.StateIncomplete, Count: 500, Detail: "first 500 only"},
		},
		Rows: []problems.Row{
			{Severity: problems.Failing, Kind: "Deployment", Resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
				Namespace: "apps", Name: "checkout-api", UID: "uid-d", Cause: "ProgressDeadlineExceeded: rollout has not completed", Signals: 5,
				Since: problemsTestReadAt.Add(-14 * time.Minute),
				Children: []problems.Row{
					{Severity: problems.Failing, Kind: "Pod", Resource: pods, Namespace: "apps", Name: "checkout-api-6c9f7d-x2kq", UID: "uid-p1",
						Cause: "CrashLoopBackOff · last OOMKilled exit 137", Signals: 2,
						Events: []problems.EventNote{{Reason: "BackOff", Count: 41, Message: "restarting", Last: problemsTestReadAt.Add(-time.Minute)}}},
					{Severity: problems.Failing, Kind: "Pod", Resource: pods, Namespace: "apps", Name: "checkout-api-6c9f7d-p8wm", UID: "uid-p2",
						Cause: "ImagePullBackOff · Back-off pulling image", Signals: 1},
				}},
			{Severity: problems.Degraded, Kind: "Pod", Resource: pods, Namespace: "ops", Name: "worker", UID: "uid-w",
				Cause: "Pending · 0/1 nodes available: insufficient memory", Signals: 1},
			{Severity: problems.Warned, Kind: "Widget", Namespace: "apps", Name: "gadget", Cause: "event Broken: it broke", Signals: 1},
		},
	}
}

func TestProblemsLayoutKeepsSeverityNameAndCauseAtEveryWidth(t *testing.T) {
	snapshot := problemsTestSnapshot()
	lines := problemsLines(snapshot.Rows, map[string]bool{"Deployment/apps/checkout-api": true}, "")
	require.Len(t, lines, 5)
	for _, width := range []int{118, 78, 58, 40} {
		columns := problemsLayout(width, lines, true, false, snapshot.ReadAt)
		headers := problemsHeaders(columns)
		assert.Contains(t, headers, "SEVERITY")
		assert.Contains(t, headers, "NAME")
		assert.Contains(t, headers, "CAUSE")
		assert.Equal(t, width >= 72, columns.kind > 0, "width %d", width)
		assert.Equal(t, width >= 72, columns.age > 0, "width %d", width)
		assert.Equal(t, width >= 100, columns.signals > 0, "width %d", width)
		assert.Positive(t, columns.cause, "width %d", width)
		for i := range lines {
			cells := problemsCells(&lines[i], columns, true, false, width < 100, snapshot.ReadAt)
			require.Len(t, cells, len(headers))
			assert.Equal(t, lines[i].row.Severity.String(), cells[0], "the severity word is never elided")
			total := len(cells) - 1
			for _, cell := range cells {
				total += runewidth.StringWidth(cell)
			}
			assert.LessOrEqual(t, total, width, "width %d line %d: %q", width, i, cells)
		}
	}
	// Below 72 columns the kind moves into the name and the namespace yields.
	narrow := problemsCells(&lines[0], problemsLayout(58, lines, true, false, snapshot.ReadAt), true, false, true, snapshot.ReadAt)
	assert.True(t, strings.HasPrefix(narrow[1], "▾ deploy checkout"), narrow[1])
	assert.Contains(t, strings.Join(problemsHeader(snapshot, "", "", 58), "\n"), "(may hide problems)")
	wide := problemsCells(&lines[0], problemsLayout(118, lines, true, false, snapshot.ReadAt), true, false, false, snapshot.ReadAt)
	assert.Equal(t, []string{"failing", "Deployment", "▾ apps/checkout-api", "ProgressDeadlineExceeded: rollout has not completed", "14m", "5"}, wide)
}

func TestProblemsCellsMeasureTerminalCellsNotBytes(t *testing.T) {
	row := problems.Row{Severity: problems.Failing, Kind: "Pod", Name: "日本語のポッド名前-長い", Cause: "CrashLoopBackOff · 失敗しました、再試行しています"}
	lines := []problemsLine{{row: &row}}
	columns := problemsLayout(58, lines, false, false, problemsTestReadAt)
	cells := problemsCells(&lines[0], columns, false, false, true, problemsTestReadAt)
	assert.LessOrEqual(t, runewidth.StringWidth(cells[1]), columns.name)
	assert.LessOrEqual(t, runewidth.StringWidth(cells[2]), columns.cause)
	assert.True(t, strings.HasSuffix(cells[1], "…"))
}

func TestProblemsMarkersHaveAnASCIIForm(t *testing.T) {
	snapshot := problemsTestSnapshot()
	folded := problemsLines(snapshot.Rows, nil, "")
	require.Len(t, folded, 3)
	assert.Equal(t, "+ checkout-api", problemsName(&folded[0], false, false, true))
	assert.Equal(t, "▸ checkout-api", problemsName(&folded[0], false, false, false))
	assert.Equal(t, "2 pods affected · ProgressDeadlineExceeded: rollout has not completed", problemsCause(&folded[0]))
	open := problemsLines(snapshot.Rows, map[string]bool{snapshot.Rows[0].Key(): true}, "")
	assert.Equal(t, "- checkout-api", problemsName(&open[0], false, false, true))
	assert.Equal(t, "  | checkout-api-6c9f7d-x2kq", problemsName(&open[1], false, false, true))
	for i := range open {
		for _, r := range problemsName(&open[i], true, true, true) {
			assert.Less(t, r, rune(128), "noIcons must stay ASCII")
		}
	}
}

func TestProblemsFilterKeepsAnOwnerWhoseFoldedPodMatches(t *testing.T) {
	snapshot := problemsTestSnapshot()
	lines := problemsLines(snapshot.Rows, nil, "imagepull")
	require.Len(t, lines, 2)
	assert.Equal(t, "checkout-api", lines[0].row.Name)
	assert.True(t, lines[1].child)
	assert.Equal(t, "checkout-api-6c9f7d-p8wm", lines[1].row.Name)
	lines = problemsLines(snapshot.Rows, nil, "DEGRADED ops")
	require.Len(t, lines, 1)
	assert.Equal(t, "worker", lines[0].row.Name)
	assert.Empty(t, problemsLines(snapshot.Rows, nil, "no-such-thing"))
	assert.Contains(t, problemsEmpty(snapshot, "", "no-such-thing"), "3 rows are hidden")
}

func TestProblemsHeaderNamesWhatWasReadAndWhatWasNot(t *testing.T) {
	snapshot := problemsTestSnapshot()
	wide := strings.Join(problemsHeader(snapshot, "", "", 118), "\n")
	assert.Contains(t, wide, "Problems in all namespaces · 1 failing · 1 degraded · 1 warning")
	assert.Contains(t, wide, "Read at "+problemsTestReadAt.Local().Format("15:04:05"))
	assert.Contains(t, wide, "pods 8 · deployments 3 · replicasets 1 (ownership only) · pvcs 500+ (last 1h)")
	assert.Contains(t, wide, "NOT READ: nodes denied · jobs unknown (timed out) · pvcs incomplete (first 500 only)")
	assert.Contains(t, wide, "cannot be ruled out")
	narrow := strings.Join(problemsHeader(snapshot, "apps", "", 58), "\n")
	assert.Contains(t, narrow, "Problems in namespace apps")
	assert.Contains(t, narrow, "3 of 6 sources complete")
	assert.Contains(t, narrow, "NOT READ: nodes denied", "gaps are never dropped to save width")

	for i := range snapshot.Sources {
		snapshot.Sources[i].State, snapshot.Sources[i].Detail = problems.StateComplete, ""
	}
	assert.Contains(t, strings.Join(problemsHeader(snapshot, "", "", 118), "\n"), "No source is missing")
	assert.Equal(t, []string{"Problems in all namespaces", "reading..."}, problemsHeader(nil, "", "reading...", 118))
}

func TestProblemsQuietListStatesWhatWasCheckedWithoutClaimingHealth(t *testing.T) {
	snapshot := problemsTestSnapshot()
	snapshot.Rows = nil
	text := problemsEmpty(snapshot, "apps", "")
	assert.Contains(t, text, "No failing, degraded or in-progress resource is reported in namespace apps")
	assert.Contains(t, text, "pods 8 · deployments 3")
	assert.Contains(t, text, "Not read: nodes denied · jobs unknown (timed out) · pvcs incomplete (first 500 only). Problems there cannot be ruled out.")
	assert.Contains(t, text, "not a health check")
	assert.NotContains(t, strings.ToLower(text), "healthy")
	assert.NotContains(t, strings.ToLower(text), "all clear")

	for i := range snapshot.Sources {
		snapshot.Sources[i] = problems.Source{Name: snapshot.Sources[i].Name, State: problems.StateDenied}
	}
	text = problemsEmpty(snapshot, "apps", "")
	assert.Contains(t, text, "Nothing could be read")
	assert.Contains(t, text, "nothing is known")
	assert.Equal(t, "no problems reported", problemsCounts(nil))
}

func TestProblemsDetailAndFooter(t *testing.T) {
	snapshot := problemsTestSnapshot()
	lines := problemsLines(snapshot.Rows, map[string]bool{snapshot.Rows[0].Key(): true}, "")
	detail := problemsDetail(&lines[1], snapshot.ReadAt)
	assert.Contains(t, detail, "Pod apps/checkout-api-6c9f7d-x2kq · failing · 2 signals")
	assert.Contains(t, detail, "Warning events: BackOff x41 (1m ago) restarting")
	assert.Contains(t, problemsDetail(&lines[0], snapshot.ReadAt), "for 14m")
	assert.Equal(t, "-", problemsFor(time.Time{}, snapshot.ReadAt), "an unknown duration is not shown as zero")
	for _, width := range []int{118, 78, 58, 30} {
		footer := problemsFooter(width, true, true)
		assert.LessOrEqual(t, runewidth.StringWidth(footer), width)
		assert.True(t, strings.HasPrefix(footer, "Enter open"))
	}
	assert.Contains(t, problemsFooter(118, true, true), "Space pods")
	assert.NotContains(t, problemsFooter(118, true, false), "Space pods")
	assert.Equal(t, "r re-read · Esc back · ? help", problemsFooter(118, false, false), "an empty list offers only keys that do something")
	// The inner widths of an 80 and a 60 column terminal still name filter and re-read.
	// The timeline and graph keys outlast Space and help; describe, logs, filter, re-read and Esc outlast them.
	assert.Equal(t, "Enter open  d desc  l logs  t time  m graph  D Grafana  Space pods  / filter  r reread  Esc back  ? help", problemsFooter(118, true, true))
	assert.Equal(t, "Enter open  d desc  l logs  t time  m graph  / filter  r reread  Esc back", problemsFooter(78, true, true))
	assert.Equal(t, "Enter open  d desc  l logs  / filter  r reread  Esc back", problemsFooter(58, true, true))
}

func TestProblemsPackNeverSplitsAnItemFromItsCount(t *testing.T) {
	text := "Read at 12:00:00: pods 0 · deployments 0 · replicasets 0 (ownership only) · statefulsets 0 · daemonsets 0 · jobs 0 · pvcs 0 · nodes 1 · warning events 0 (last 1h)"
	for _, width := range []int{117, 77, 57, 40} {
		packed := problemsPack(text, width)
		assert.Equal(t, text, strings.Join(strings.Split(packed, " ·\n"), " · "), "packing only moves line breaks")
		for _, line := range strings.Split(packed, "\n") {
			// The pane wraps on encoded length, so that is what must fit.
			assert.LessOrEqual(t, len(line), width, "width %d: %q", width, line)
			assert.NotRegexp(t, `^\d`, line, "width %d: a count was separated from its source", width)
		}
		assert.Equal(t, strings.Count(packed, "\n")+1, problemsWrappedRows(packed, width), "width %d: the pane wraps a packed line again", width)
	}
	// 116 cells but 122 bytes: it fits a 117 cell pane by sight and still wraps there.
	seen := "Read at 04:26:07: pods 2 · deployments 0 · replicasets 0 (ownership only) · statefulsets 2 · daemonsets 1 · jobs 3 ·"
	assert.Equal(t, 116, runewidth.StringWidth(seen))
	assert.Equal(t, 2, problemsWrappedRows(seen, 117))
	// Prose longer than the pane is left to wrap, and the items after it still pack.
	packed := problemsPack("No failing resource is reported in namespace default by what was read: pods 0 · deployments 0 · jobs 0", 40)
	for _, line := range strings.Split(packed, "\n")[1:] {
		assert.LessOrEqual(t, runewidth.StringWidth(line), 40)
	}
	assert.Equal(t, 2, problemsWrappedRows("one\ntwo", 40))
}

func TestProblemsDetailGrowsToShowWarningEvents(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	v := newProblemsView(app)
	require.NoError(t, v.Init(t.Context()))
	app.Content.Push(v)
	v.width, v.height = 58, 18
	snapshot := problemsTestSnapshot()
	snapshot.Rows[1].Cause = "Pending · 0/3 nodes are available: 1 node(s) had untolerated taint, 2 Insufficient memory, preemption is not helpful for scheduling"
	snapshot.Rows[1].Events = []problems.EventNote{{Reason: "FailedScheduling", Count: 7, Last: problemsTestReadAt.Add(-time.Minute)}}
	v.acceptSnapshot(snapshot)
	v.table.Select(2, 0)
	assert.Contains(t, v.detail.GetText(true), "Warning events: FailedScheduling x7")
	assert.Greater(t, problemsWrappedRows(v.detail.GetText(true), v.width-1), 3, "this row needs more than the minimum three lines")
	assert.Equal(t, 4, v.detailRows, "the pane grows, bounded by a third of the height left under the header")
	v.table.Select(3, 0)
	assert.Equal(t, 3, v.detailRows, "a short row keeps the minimum")
}

func TestProblemsViewSelectionExpansionAndAvailability(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	v := newProblemsView(app)
	require.NoError(t, v.Init(t.Context()))
	app.Content.Push(v)
	v.acceptSnapshot(problemsTestSnapshot())
	require.Len(t, v.lines, 3)

	target := v.SelectedResource()
	require.NoError(t, target.Err())
	assert.Equal(t, "apps/v1/deployments", target.GVR.String())
	assert.Equal(t, "apps/checkout-api", target.Path())
	assert.EqualValues(t, "uid-d", target.UID, "the jump carries the identity that was read")
	assert.Contains(t, v.availability(ui.KeyL), "Space expands the affected pods")
	assert.Empty(t, v.availability(tcell.KeyEnter))
	assert.Empty(t, v.availability(ui.KeySpace))

	v.key(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	require.Len(t, v.lines, 5)
	v.table.Select(2, 0)
	target = v.SelectedResource()
	assert.Equal(t, client.PodGVR, target.GVR)
	assert.Equal(t, "apps/checkout-api-6c9f7d-x2kq", target.Path())
	assert.Empty(t, v.availability(ui.KeyL))
	// Folding from a pod returns the selection to its owner.
	v.key(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	require.Len(t, v.lines, 3)
	row, _ := v.table.GetSelection()
	assert.Equal(t, 1, row)

	v.table.Select(3, 0)
	assert.ErrorContains(t, v.SelectedResource().Err(), "named only by an event")
	assert.Contains(t, v.availability(tcell.KeyEnter), "named only by an event")
	assert.Contains(t, v.availability(ui.KeySpace), "affected pods")

	v.SetFilter("worker", false)
	require.Len(t, v.lines, 1)
	assert.Contains(t, v.header.GetText(true), `filter "worker" shows 1 of 3`)
	v.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	assert.Len(t, v.lines, 3, "Esc clears the filter before it leaves the view")
	assert.Same(t, v, app.Content.Top())

	// A re-read keeps the selection on the same resource.
	v.table.Select(2, 0)
	next := problemsTestSnapshot()
	next.Rows = append([]problems.Row{{Severity: problems.Failing, Kind: "Node", Name: "n1", Cause: "NotReady", Signals: 9}}, next.Rows...)
	v.acceptSnapshot(next)
	assert.Equal(t, "worker", v.selectedLine().row.Name)

	empty := problemsTestSnapshot()
	empty.Rows = nil
	v.acceptSnapshot(empty)
	assert.Nil(t, v.selectedLine())
	assert.Contains(t, v.detail.GetText(true), "Problems there cannot be ruled out")
	assert.Contains(t, v.availability(tcell.KeyEnter), "Select a listed resource first")
}

func TestProblemsViewRefusesToReadWithoutAConnectionOrAfterDestinationChange(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	v := newProblemsView(app)
	require.NoError(t, v.Init(t.Context()))
	app.Content.Push(v)
	v.refresh()
	assert.False(t, v.loading)
	assert.Contains(t, v.header.GetText(true), "not read: "+problemsNoConnection)
	assert.NotContains(t, v.header.GetText(true), "no problems reported", "an unread list is not an empty list")

	v.destinationRevision++
	v.refresh()
	assert.Contains(t, v.header.GetText(true), "destination changed; reopen :problems")
	assert.Contains(t, v.availability(tcell.KeyEnter), "Destination changed")
}

func TestProblemsCommandIsRegisteredWithAnHonestReason(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	owner := &discoveryOwner{Details: NewDetails(app, "test", "", contentTXT, false)}
	var found *ui.ActionDescriptor
	for _, descriptor := range actionCatalog(owner, app) {
		if descriptor.ID == "command.problems" {
			found = &descriptor
		}
	}
	require.NotNil(t, found, "the palette and Help list :problems")
	assert.Equal(t, ":problems", found.Shortcut)
	assert.True(t, found.Discoverable)
	assert.Equal(t, problemsNoConnection, found.UnavailableReason, "without a connection the action says why it cannot run")

	command := NewCommand(app)
	command.updateSuggestionAliases()
	assert.Contains(t, command.suggestionAliases(), problemsCommandToken)
	assert.Nil(t, app.Content.Top())
	assert.True(t, command.toolkitCmd(cmd.NewInterpreter(problemsCommandToken)), "typed :problems is dispatched, not resolved as a resource alias")
	assert.Nil(t, app.Content.Top(), "nothing opens without a connection")
}
