// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var timelineAt = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// timelineReads is the coverage of a collection in which every read was
// complete, with an Alertmanager and a Grafana declared.
func timelineReads(short map[string]timeline.Coverage) []timeline.Coverage {
	var out []timeline.Coverage
	for _, source := range []string{"Selected resource", "Pods", "Events", "ReplicaSets", "Deployments", "Autoscalers",
		"ConfigMap metadata", "Secret metadata", "Nodes", "Alerts", timelineAnnotationsName} {
		coverage := timeline.Coverage{Source: source, State: timeline.Complete, Detail: "read"}
		if other, ok := short[source]; ok {
			coverage.State, coverage.Detail = other.State, other.Detail
		}
		out = append(out, coverage)
	}
	return out
}

// timelineIncident is a ConfigMap edit 14m before the capture, a rollout
// 12m before it, and what the cluster reported after that: the crash loop
// the walk of an incident starts from.
func timelineIncident(scope *timeline.Scope, short map[string]timeline.Coverage) *timeline.Snapshot {
	at := timelineAt
	ago := func(d time.Duration) time.Time { return at.Add(-d) }
	deployment := timeline.Ref{Resource: "apps/v1/deployments", Kind: "Deployment", Namespace: "apps", Name: "checkout-api"}
	pod := timeline.Ref{Resource: "v1/pods", Kind: "Pod", Namespace: "apps", Name: "checkout-api-7d9f8c6b5d-x2x9q"}
	other := timeline.Ref{Resource: "v1/pods", Kind: "Pod", Namespace: "apps", Name: "checkout-api-7d9f8c6b5d-p8wm"}
	hpa := timeline.Ref{Resource: "autoscaling/v2/horizontalpodautoscalers", Kind: "HorizontalPodAutoscaler", Namespace: "apps", Name: "checkout-api"}
	config := timeline.Ref{Resource: "v1/configmaps", Kind: "ConfigMap", Namespace: "apps", Name: "checkout-config"}
	entries := []timeline.Entry{
		{At: ago(144 * time.Hour), Source: timeline.SourceNode, Summary: "Ready=True KubeletReady", Ref: timeline.Ref{Resource: "v1/nodes", Kind: "Node", Name: "node-a"}},
		{At: ago(72 * time.Hour), Source: timeline.SourceRollout, Change: true, Summary: "rev 1 (earliest retained): image example.test/checkout:v1",
			Ref: timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "apps", Name: "checkout-api-6c8d7f9b5"}},
		{At: ago(26 * time.Hour), Source: timeline.SourceAutoscaler, Change: true, Summary: "last scaled Deployment/checkout-api: current 2, desired 2", Ref: hpa},
		{At: ago(125 * time.Minute), Source: timeline.SourceConfig, Change: true, Summary: "Secret applied by external-secrets",
			Ref: timeline.Ref{Resource: "v1/secrets", Kind: "Secret", Namespace: "apps", Name: "checkout-secrets"}},
		{At: ago(14 * time.Minute), Source: timeline.SourceConfig, Change: true, Summary: "ConfigMap updated by kubectl-edit",
			Origin: "metadata.managedFields[].time and manager", Ref: config,
			Detail: []string{"Last Update by this manager; earlier writes by the same manager are not retained."}},
		{At: ago(14 * time.Minute), Source: timeline.SourceAnnotation, Summary: "ConfigMap apps/checkout-config edited", Ref: timeline.Ref{Kind: "Grafana"},
			Detail: []string{"tags deploy, namespace:apps", "in scope by tag namespace:apps", "not attached to a dashboard"}},
		{At: ago(12 * time.Minute), Source: timeline.SourceRollout, Change: true,
			Summary: "rev 2: image v1 -> v2; env ~PAYMENT_TOKEN +FEATURE_FLAGS; limits.memory 512Mi -> 256Mi",
			Origin:  "ReplicaSet metadata.creationTimestamp and deployment.kubernetes.io/revision",
			Ref:     timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "apps", Name: "checkout-api-7d9f8c6b5d"},
			Detail:  []string{"Deployment checkout-api, compared with revision 1 (checkout-api-6c8d7f9b5).", "Now: example.test/checkout:v2"}},
		{At: ago(12 * time.Minute), Source: timeline.SourceEvent, Summary: "Normal ScalingReplicaSet: Scaled up replica set checkout-api-7d9f8c6b5d to 1", Ref: deployment, Count: 1},
		{At: ago(11 * time.Minute), Last: ago(time.Minute), Count: 7, Source: timeline.SourceEvent, Summary: "Normal Started x7: Started container api", Ref: pod},
		{At: ago(10 * time.Minute), Last: ago(time.Minute), Count: 14, Source: timeline.SourceEvent, Failing: true,
			Summary: "Warning BackOff x14: Back-off restarting failed container api in pod checkout-api-7d9f8c6b5d-x2x9q", Ref: pod,
			Origin: "Event firstTimestamp/lastTimestamp, falling back to eventTime then creationTimestamp",
			Detail: []string{"Reported by: kubelet", "First 2026-11-17 11:50:00Z, last 2026-11-17 11:59:00Z, 14 occurrences"}},
		{At: ago(9 * time.Minute), Source: timeline.SourceAlert, Failing: true, Ref: deployment,
			Summary: "CheckoutHighErrorRate (critical) began firing: checkout-api is answering more than 5% of requests with 5xx"},
		{At: ago(8 * time.Minute), Source: timeline.SourceAlert, Failing: true, Ref: pod, Summary: "KubePodCrashLooping (warning) began firing: Pod is crash looping."},
		{At: ago(8 * time.Minute), Source: timeline.SourceAutoscaler, Failing: true, Ref: hpa, Summary: "ScalingActive=False FailedGetResourceMetric"},
		{At: ago(7 * time.Minute), Source: timeline.SourceAlert, Failing: true, Ref: other, Summary: "KubePodNotReady (warning) began firing: Pod has been in a non-ready state for more than 15 minutes."},
		{At: ago(2 * time.Minute), Source: timeline.SourceRestart, Failing: true, Ref: pod, Summary: "container api Error (exit 1), restart count 6", Earlier: 5,
			Origin: "Pod status containerStatuses[].lastState.terminated.finishedAt",
			Detail: []string{"Ran 41s (started 2026-11-17 11:57:19Z)", "Only the most recent exit is retained; 5 earlier restarts have no time."}},
		{At: ago(2 * time.Minute), Source: timeline.SourceCondition, Failing: true, Ref: deployment, Summary: "Progressing=False ProgressDeadlineExceeded"},
	}
	return timeline.NewSnapshot(scope, at, entries, timelineReads(short))
}

func timelineCalmFixture(t *testing.T, snapshot func(*timeline.Scope) *timeline.Snapshot) *timelineView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	scope := &timeline.Scope{Context: app.Config.ActiveContextName(), Namespace: "apps", Resource: "apps/v1/deployments", Kind: "Deployment", Name: "checkout-api"}
	v := newTimelineView(app, scope, "")
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	if snapshot != nil {
		v.acceptSnapshot(snapshot(scope), nil)
	}
	return v
}

func timelineIncidentView(t *testing.T) *timelineView {
	t.Helper()
	return timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot { return timelineIncident(scope, nil) })
}

// timelineStalls are the stalls :slow found in the hour before the
// capture, as it hands them to a view opened from it.
func timelineStalls() *graphEpisodes {
	at := timelineAt
	return &graphEpisodes{Label: "stalls", Start: at.Add(-time.Hour), End: at, Intervals: []chart.Interval{
		{Start: at.Add(-52 * time.Minute), End: at.Add(-49 * time.Minute)},
		{Start: at.Add(-33 * time.Minute), End: at.Add(-29 * time.Minute)},
		{Start: at.Add(-7 * time.Minute), End: at.Add(-5 * time.Minute)},
	}}
}

// timelineHang is what a Pod of a Service that stalls shows: nothing
// retained from the last hour, and its one rollout days ago.
func timelineHang(scope *timeline.Scope) *timeline.Snapshot {
	at := timelineAt
	entries := []timeline.Entry{
		{At: at.Add(-149 * time.Hour), Source: timeline.SourceNode, Summary: "Ready=True KubeletReady", Ref: timeline.Ref{Resource: "v1/nodes", Kind: "Node", Name: "worker-f"}},
		{At: at.Add(-149 * time.Hour), Source: timeline.SourceRollout, Change: true, Summary: "rev 1 (earliest retained): webservice image registry.example.test/gitlab-webservice-ee:v18.5.1",
			Ref: timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "gitlab", Name: "gitlab-webservice-default-6f9c7d5b84"}},
	}
	return timeline.NewSnapshot(scope, at, entries, timelineReads(nil))
}

// calmPinned compares a frame of a view with its pinned text under
// testdata/<view>. The pinned pages are the acceptance text of a calm
// page: CALM_UPDATE=1 writes them again, which is done on purpose and read
// before it is committed.
func calmPinned(t *testing.T, view, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", view, name+".txt")
	got = strings.TrimRight(got, "\n") + "\n"
	if os.Getenv("CALM_UPDATE") != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got, name)
}

// timelinePinned compares a frame with its pinned text: the acceptance
// text of the calm :timeline.
func timelinePinned(t *testing.T, name, got string) {
	t.Helper()
	calmPinned(t, "timeline", name, got)
}

// The pages a view is given in terminals of 132x36, 80x24 and 60x24: the
// application keeps two rows at the top and one at the bottom.
func TestTimelineCalmPagesArePinnedAtTheSizesAViewIsGiven(t *testing.T) {
	denied := map[string]timeline.Coverage{
		"Nodes":  {State: timeline.Denied, Detail: "0 of 1 running the pods in scope; node-a: access denied; this history is unknown, not empty"},
		"Events": {State: timeline.Incomplete, Detail: "only the first 500 were read; more exist"},
	}
	for _, c := range []struct {
		name          string
		view          func(t *testing.T) *timelineView
		width, height int
		ascii         bool
	}{
		{"incident", timelineIncidentView, 132, 33, false},
		{"incident", timelineIncidentView, 80, 21, false},
		{"incident", timelineIncidentView, 60, 21, true},
		{"denied", func(t *testing.T) *timelineView {
			return timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
				s := timelineIncident(scope, denied)
				s.Untimed = []string{"deploy/checkout-api Available=False MinimumReplicasUnavailable: condition has no lastTransitionTime"}
				return s
			})
		}, 132, 33, false},
		{"denied", func(t *testing.T) *timelineView {
			return timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot { return timelineIncident(scope, denied) })
		}, 80, 21, false},
		{"hang", func(t *testing.T) *timelineView {
			v := timelineCalmFixture(t, nil)
			v.episodes = timelineStalls()
			v.acceptSnapshot(timelineHang(&v.scope), nil)
			return v
		}, 132, 33, false},
		{"hang", func(t *testing.T) *timelineView {
			v := timelineCalmFixture(t, nil)
			v.episodes = timelineStalls()
			v.acceptSnapshot(timelineHang(&v.scope), nil)
			return v
		}, 60, 21, true},
	} {
		name := fmt.Sprintf("%s-%dx%d", c.name, c.width, c.height)
		if c.ascii {
			name += "-ascii"
		}
		t.Run(name, func(t *testing.T) {
			v := c.view(t)
			v.app.Config.K9s.UI.NoIcons = c.ascii
			frame := drawnText(t, v, c.width, c.height)
			timelinePinned(t, name, frame)
			for _, line := range strings.Split(frame, "\n") {
				require.LessOrEqual(t, runewidth.StringWidth(line), c.width, line)
			}
			if c.ascii {
				for _, r := range frame {
					require.Less(t, r, rune(128), "%q is not ASCII", r)
				}
			}
			lower := strings.ToLower(frame)
			for _, banned := range []string{"because", "caused", "due to", "root cause", "blame", "healthy "} {
				require.NotContains(t, lower, banned)
			}
		})
	}
}

// timelineLineWith is the first line of a frame that starts with a text,
// and its index.
func timelineLineWith(t *testing.T, frame, prefix string) (line string, index int) {
	t.Helper()
	for i, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line, i
		}
	}
	require.Failf(t, "no line", "that starts with %q in\n%s", prefix, frame)
	return "", -1
}

// timelineCols are the columns of a line that hold one of the runes. No
// rune of a calm page is wider than one cell, so a column is a rune index.
func timelineCols(line, runes string) []int {
	var out []int
	for col, r := range []rune(line) {
		if strings.ContainsRune(runes, r) {
			out = append(out, col)
		}
	}
	return out
}

// The calm page: what happened in order first, then one row per kind of
// entry on one axis, grouped by how it stands to the change the answer
// starts from. 132x33, 80x21 and 60x21 are what the application leaves the
// view in terminals of 132x36, 80x24 and 60x24.
func TestTimelineCalmPageStatesWhatHappenedInOrderAtEverySize(t *testing.T) {
	v := timelineIncidentView(t)
	require.False(t, v.allDetail, "the calm page is the default")
	require.Equal(t, 15*time.Minute, v.window, "it opens on the narrowest window that holds the fault and the change before it")
	for _, size := range [][2]int{{132, 33}, {120, 34}, {100, 30}, {80, 21}, {60, 21}, {40, 12}, {40, 8}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		lines := strings.Split(frame, "\n")
		for _, line := range lines {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		flat := strings.Join(strings.Fields(frame), " ")
		require.True(t, strings.HasPrefix(lines[0], "Timeline  Deployment app"), lines[0])
		// The answer comes first: the change, then what followed it and
		// how soon, and that this is an order and nothing more.
		require.Contains(t, flat, "Rev 2 rolled out at 11:48, after 1 other change.", "%d columns", size[0])
		// The second sentence names the first failing signal. It is one
		// line where a form of it fits one: at 80 columns the whole of it
		// is one cell too long, and the Pod is written by its name alone;
		// where no form fits one line it wraps and keeps every word.
		followed := "7 failing signals followed within 10m, the first after 2m: BackOff on pod x2x9q."
		if size[0] == 80 {
			followed = "7 failing signals followed within 10m, the first after 2m: BackOff on x2x9q."
			require.Contains(t, frame, "\n  "+followed+"\n  "+timelineOrderCaveat+"\n", "each sentence and the caveat are a line of their own")
			require.NotContains(t, flat, "on pod x2x9q.")
		}
		require.Contains(t, flat, followed, "%d columns", size[0])
		require.Contains(t, flat, timelineOrderCaveat)
		require.True(t, strings.HasPrefix(lines[1], "  Rev 2 rolled out at 11:48"), "%d columns: %q", size[0], lines[1])
		answer, changed := strings.Index(frame, "Rev 2 rolled out"), strings.Index(frame, "\n  "+timelineChangedLabel)
		require.True(t, answer >= 0 && answer < changed, "%d columns: the evidence is under the answer\n%s", size[0], frame)
		// The key that shows every entry is named at every size.
		require.Regexp(t, `(?m)^  next  .*\ba full list\b|a shows all`, frame, "%d columns", size[0])
		lower := strings.ToLower(frame)
		for _, banned := range []string{"because", "caused", "due to", "root cause"} {
			require.NotContains(t, lower, banned)
		}
		// The list this view had before is under a, not on this page.
		for _, gone := range []string{"UTC TIME", "VS FAULT", "FAULT BEGAN", "below: preceded", "captured 12:00:00Z"} {
			require.NotContains(t, frame, gone, "%d columns", size[0])
		}
		if size[0] < 60 {
			// No room for a strip beside the text: the rows are text, and
			// the page says what it left out.
			require.Contains(t, frame, "not shown (a shows all)")
			require.Contains(t, v.dropped, "the time strips")
			continue
		}
		// What changed, then what went wrong after it, each kind one row
		// with its marks on the shared axis.
		wrong := strings.Index(frame, "\n  "+timelineThenLabel)
		require.Less(t, changed, wrong, "%d columns", size[0])
		require.Empty(t, timelineGaveUp(v, "rows of"), "%d columns: every row is on the page", size[0])
		for _, row := range []string{"config", "rollouts", "warnings", "alerts", "autoscaler", "status"} {
			require.Regexp(t, `(?m)^[> ] `+row+` .*[·▲▁█┊]{20,}$`, frame, "%d columns: %s", size[0], row)
		}
		// The container restarted six times and retains the time of its
		// last exit only: the stretch before that mark is not read, never
		// quiet, and after it nothing more happened.
		require.Regexp(t, `(?m)^[> ] exits .*  ×{10,}▲·+$`, frame, "%d columns", size[0])
		require.Regexp(t, `(?m)^> warnings +BackOff`, frame, "the row cursor opens on the first failing signal")
		// The change is marked over the strips by a sign, not only a colour.
		require.Regexp(t, `(?m)^ +rev 2 .*▼$`, frame, "%d columns", size[0])
		switch size[0] {
		case 132:
			require.Equal(t, []string{"blank rows around the verdict and the keys", "QUIET folded to one line"}, v.dropped,
				"132x36 gives up blank rows and folds the quiet line, for the list under the axis")
			require.Contains(t, lines[0], "last 15m · 12 of 16 entries")
			require.Contains(t, lines[0], "read 12:00 · 11 of 11 sources")
			require.Contains(t, frame, "rev 2 rolled out 11:48")
			require.Regexp(t, `(?m)^  warnings +BackOff · pod x2x9q +11:50  14 times +▁+┊▁+█+▁+$`, strings.Replace(frame, "> warnings", "  warnings", 1))
			// What a row is about ends between two words, and the count of
			// its other entries stays after the cut.
			require.Regexp(t, `(?m)^  alerts +CheckoutHighErrorRate… \+2 +11:51  for 9m +▁+┊▁+█+$`, frame)
			require.Regexp(t, `(?m)^  exits +api Error \(exit 1\) · pod x2x9q  11:58  6 times +×+▲·+$`, frame,
				"the exits are counted beside the text, which does not write the count again")
			require.Regexp(t, `(?m)^  rollouts +rev 2: image v1 → v2; env… +11:48 +·+▲·+$`, frame, "the mark of the rollout stands where its guide is")
			require.Regexp(t, `(?m)^  ALSO IN THE WINDOW +┊\n  annotations +ConfigMap apps/checkout-confi…  11:46 +·+▲·+┊·+\n  events +ScalingReplicaSet… \+1 +11:48  8 times `, frame)
			require.Contains(t, frame, "  QUIET  scaling · nodes: none now\n")
			// The keys do not stand directly on the list.
			require.Contains(t, frame, "\n\n  next  Enter open x2x9q    a full list\n")
			require.Contains(t, frame, "  ↑↓ row · ←→ time · 1-4 window, 0 all · s sources · n next entry · x mark · / filter · r re-read · Esc back\n")
		case 80:
			// Every row keeps its strip; what is neither a change nor a
			// failing signal folds to one line and the second key line goes.
			// The count of entries gives way in the title before the object
			// or the window does.
			require.Equal(t, "Timeline  Deployment apps/checkout-api · last 15m  read 12:00 · 11 of 11 sources", lines[0])
			require.Contains(t, frame, "  ALSO IN THE WINDOW  annotations 11:46 · events 11:48 8 times\n")
			require.Contains(t, frame, "  QUIET  scaling · nodes: none now\n")
			require.Contains(t, v.dropped, "the second key line")
			require.Contains(t, v.dropped, "what each number is compared with")
			require.NotContains(t, frame, "not shown", "nothing is left out at 80x24")
		case 60:
			// What a row is about is cut; its time and its strip stay.
			require.Contains(t, lines[0], "last 15m", "the window outlasts the count of entries in the title")
			require.Regexp(t, `(?m)^> warnings +BackOff… +11:50  ▁+┊▁+█+▁+$`, frame, "a part of the Pod's name says less than none of it")
			require.Regexp(t, `(?m)^  alerts +CheckoutHighE… \+2  11:51  `, frame, "the count of the other alerts outlasts the name of the first")
			require.NotContains(t, frame, "not shown", "nothing is left out at 60x24")
		}
	}
}

// timelineGaveUp are the things the page gave up for its size that name a
// word.
func timelineGaveUp(v *timelineView, word string) []string {
	var out []string
	for _, what := range v.dropped {
		if strings.Contains(what, word) {
			out = append(out, what)
		}
	}
	return out
}

// The moment the answer starts from is one column on every row: the marker
// over the strips, the guide through them and the heavy tick on the axis.
// A state that was read at the capture is a span to the end of the axis, a
// repeated event a span from its first to its last occurrence, anything
// else a mark where it happened.
func TestTimelineCalmRowsShareOneAxisAndTheChangeIsAGuide(t *testing.T) {
	v := timelineIncidentView(t)
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		lines := strings.Split(frame, "\n")
		marker, axisAt := "", -1
		for i, line := range lines {
			switch {
			case strings.ContainsRune(line, '▼'):
				marker = line
			case strings.ContainsAny(line, "┬┯"):
				axisAt = i
			}
		}
		require.Contains(t, marker, "rev 2")
		at := timelineCols(marker, "▼")
		require.Len(t, at, 1, "a rollout is a moment: one marker")
		guide := at[0]
		require.Positive(t, axisAt)
		require.Equal(t, []int{guide}, timelineCols(lines[axisAt], "━┯"), "%d columns: heavy on the axis under the marker", size[0])
		first := len([]rune(lines[axisAt])) - len([]rune(strings.TrimLeft(lines[axisAt], " ")))
		end := len([]rune(lines[axisAt]))
		// The guide never stands over a column that was not read: the row of
		// the exits is not read there.
		for _, row := range []struct{ name, glyph string }{{"config", "┊"}, {"rollouts", "▲"}, {"warnings", "┊"}, {"alerts", "┊"}, {"autoscaler", "┊"}, {"exits", "×"}, {"status", "┊"}} {
			line, _ := timelineLineWith(t, strings.ReplaceAll(frame, "\n> ", "\n  "), "  "+row.name+" ")
			runes := []rune(line)
			require.Len(t, runes, end, "%d columns: the strip of %s ends where the axis does", size[0], row.name)
			require.Equal(t, row.glyph, string(runes[guide]), "%d columns: %s", size[0], row.name)
			require.NotContains(t, string(runes[first:]), " ", "a strip has a glyph in every column")
		}
		strip := func(name string) string {
			line, _ := timelineLineWith(t, strings.ReplaceAll(frame, "\n> ", "\n  "), "  "+name+" ")
			return string([]rune(line)[first:])
		}
		// Still the case at the capture: a span to the last column.
		for _, held := range []string{"alerts", "autoscaler", "status"} {
			require.True(t, strings.HasSuffix(strip(held), "█"), "%d columns: %s holds until the capture", size[0], held)
		}
		// A repeated event: from its first to its last occurrence, a minute
		// before the capture.
		require.Regexp(t, `█▁+$`, strip("warnings"), "%d columns", size[0])
		// Moments: one mark each.
		require.Len(t, timelineCols(strip("exits"), "▲"), 1)
		require.Len(t, timelineCols(strip("config"), "▲"), 1)
		// In the order they began: every row starts at or after the one
		// above it in its group.
		firstMark := func(name string) int { return timelineCols(strip(name), "▲█")[0] }
		require.Less(t, firstMark("config"), firstMark("rollouts"))
		for _, pair := range [][2]string{{"warnings", "alerts"}, {"alerts", "autoscaler"}, {"autoscaler", "exits"}, {"exits", "status"}} {
			require.LessOrEqual(t, firstMark(pair[0]), firstMark(pair[1]), "%d columns: %v", size[0], pair)
		}
		require.Greater(t, firstMark("warnings"), guide-first, "what went wrong began after the change")
	}

	// The guide and its column are drawn in the warning colour and on the
	// band surface: the page is read without them, and faster with them.
	runes, back := drawnCells(t, v, 132, 33)
	col, row := -1, -1
	for y := range runes {
		for x, r := range runes[y] {
			if r == '▼' {
				col, row = x, y
			}
		}
	}
	require.GreaterOrEqual(t, col, 0)
	band := back[row][col]
	require.NotEqual(t, back[row][col-1], band, "the column of the change is shaded")
	for y := row; y < len(runes) && !strings.ContainsRune(string(runes[y]), '┬'); y++ {
		require.Equal(t, band, back[y][col], "row %d: the band runs down to the axis", y)
	}
}

// Neither cursor moves a row or a column of the page: the row cursor
// changes its marker and the list under the axis, the time cursor adds its
// guide, its caret and its readout in the title.
func TestTimelineCalmCursorsMoveNothing(t *testing.T) {
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		v := timelineIncidentView(t)
		base := strings.Split(drawnText(t, v, size[0], size[1]), "\n")
		_, list := timelineLineWith(t, strings.Join(base, "\n"), "  WARNINGS")
		// evidence is the page down to its axis labels.
		_, evidence := timelineLineWith(t, strings.Join(base, "\n"), "  UTC")
		same := func(got []string, what string, allowed func(line int, a, b rune) bool) {
			t.Helper()
			require.Len(t, got, len(base), what)
			for i := 0; i <= evidence; i++ {
				a, b := []rune(base[i]), []rune(got[i])
				for len(a) < len(b) {
					a = append(a, ' ')
				}
				for len(b) < len(a) {
					b = append(b, ' ')
				}
				for col := range a {
					if a[col] != b[col] {
						require.True(t, allowed(i, a[col], b[col]), "%s, %dx%d: line %d column %d: %q became %q\n%s\n%s", what, size[0], size[1], i, col, a[col], b[col], base[i], got[i])
					}
				}
			}
		}
		// Down a row, and down again: only the marker moves.
		for range 3 {
			v.moveRow(1)
			moved := strings.Split(drawnText(t, v, size[0], size[1]), "\n")
			same(moved, "row cursor", func(_ int, a, b rune) bool { return a == '>' && b == ' ' || a == ' ' && b == '>' })
			require.NotEqual(t, base[list], moved[list], "the list under the axis follows the row")
			require.Regexp(t, `^  [A-Z]+  \d`, moved[list], "and its heading stands where it stood")
		}
		require.Equal(t, "exit", v.calmKey, "the row of the container exits")
		// Along the axis: the cursor is drawn where nothing else is, and
		// the title says where it stands.
		for _, step := range []int{1, 1, 10, -3, 40, -200} {
			v.moveCursor(step)
			moved := strings.Split(drawnText(t, v, size[0], size[1]), "\n")
			same(moved, "time cursor", func(line int, a, b rune) bool {
				switch {
				case line == 0:
					return true // the readout takes the place of the read status
				case a == '>' || b == '>':
					return true
				}
				return (b == '┆' && strings.ContainsRune(" ·┊", a)) || (b == '^' && strings.ContainsRune("─┬━┯", a))
			})
			require.Contains(t, moved[0], "cursor 11:", "%dx%d", size[0], size[1])
			require.Contains(t, moved[0], " UTC")
			require.True(t, strings.HasPrefix(moved[list], "  AT 11:"), moved[list])
			caret := timelineCols(moved[evidence-1], "^")
			require.Len(t, caret, 1)
			require.Equal(t, v.cursor, caret[0]-(len([]rune(moved[evidence-1]))-v.frame.Width), "the caret stands in the column of the cursor")
		}
		require.Zero(t, v.cursor, "the cursor stops at the first column")
		// Esc puts the cursor away before it leaves, and the page is the
		// one it was.
		require.Nil(t, v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)))
		require.Negative(t, v.cursor)
		v.moveRow(-3)
		require.Equal(t, base, strings.Split(drawnText(t, v, size[0], size[1]), "\n"))
		require.Equal(t, v.snapshot.Onset, v.focused)
	}
}

// Under the axis the page lists what the cursor is on: the entries of the
// selected row, or everything drawn in the column of the time cursor, with
// the time, the object and the text the list under a has. n and Shift-N
// reach every entry in the order they began, and Enter, x and m act on it.
func TestTimelineCalmListReachesEveryEntryOfTheWindow(t *testing.T) {
	v := timelineIncidentView(t)
	s := v.snapshot
	frame := drawnText(t, v, 132, 33)
	require.Equal(t, s.Onset, v.focused, "the keys act on the first failing signal when the page opens")
	// The entry the keys act on is written in full where there is room:
	// its text whole, how it stands to the first failing signal, what the
	// model says of it and where its time is from. The keys never stand
	// directly on a list of this length: in a terminal of 132x36 its last
	// line, where the time is from, gives its row to the blank row over
	// them, and a taller one has both.
	written := "  WARNINGS  1 entry in the last 15m\n" +
		"> 11:50:00  pod/checkout-api-7d9f8c6b5d-x2x9q  Warning BackOff x14: Back-off restarting failed container api in pod\n" +
		"            checkout-api-7d9f8c6b5d-x2x9q\n" +
		"            First failing signal that could be read. The fault may have begun earlier than the evidence shows.\n" +
		"            Reported by: kubelet | First 2026-11-17 11:50:00Z, last 2026-11-17 11:59:00Z, 14 occurrences\n"
	from := "            Time from: Event firstTimestamp/lastTimestamp, falling back to eventTime then creationTimestamp\n"
	require.Contains(t, frame, written+"\n  next  Enter open x2x9q")
	require.NotContains(t, frame, from)
	require.Contains(t, drawnText(t, v, 132, 37), written+from+"\n  next  Enter open x2x9q")
	frame = drawnText(t, v, 132, 33)
	target := v.SelectedResource()
	require.Equal(t, "v1/pods", target.GVR.String())
	require.Equal(t, "apps/checkout-api-7d9f8c6b5d-x2x9q", target.Path())

	// A row of several entries lists them, oldest first.
	v.moveRow(1)
	frame = drawnText(t, v, 132, 33)
	alerts := []string{"  ALERTS  3 entries in the last 15m\n" +
		"> 11:51:00  deploy/checkout-api                CheckoutHighErrorRate (critical) began firing: checkout-api is answering more than\n" +
		"            5% of requests with 5xx\n",
		"            Began 1m after the first failing signal.\n",
		"  11:52:00  pod/checkout-api-7d9f8c6b5d-x2x9q  KubePodCrashLooping (warning) began firing: Pod is crash looping.\n" +
			"  11:53:00  pod/checkout-api-7d9f8c6b5d-p8wm   KubePodNotReady (warning) began firing: Pod has been in a non-ready state for more t…\n"}
	// Every entry of the row outlasts what the list adds under the first.
	require.Contains(t, frame, alerts[0]+alerts[2]+"\n  next  Enter open deploy/checkout-api")
	require.Contains(t, drawnText(t, v, 132, 37), strings.Join(alerts, "")+"\n  next  Enter open deploy/checkout-api")
	drawnText(t, v, 132, 33)
	require.Equal(t, "apps/v1/deployments", v.SelectedResource().GVR.String())

	// n from the first entry on: every entry of the window, in the order
	// they began, each with its row selected and the cursor on its column.
	for v.focused != v.story.shown[0] {
		before := v.focused
		v.stepEntry(-1)
		drawnText(t, v, 132, 33)
		require.NotEqual(t, before, v.focused)
	}
	var walked []string
	var last time.Time
	for {
		frame = drawnText(t, v, 132, 33)
		e := v.focusedEntry()
		require.NotNil(t, e)
		require.False(t, e.At.Before(last), "in the order they began")
		last = e.At
		walked = append(walked, e.Summary)
		line, _ := timelineLineWith(t, frame, "> "+timeline.Clock(e.At, s.CapturedAt))
		require.Contains(t, line, strings.Fields(e.Summary)[0], "the list marks the entry the keys act on")
		require.Contains(t, frame, "  AT 11:", "the list is of the column the cursor stands in")
		require.Equal(t, v.story.rowOf(v.focused), v.calmKey, "its row is selected")
		col, ok := v.frame.Col(e.At)
		require.True(t, ok)
		require.Equal(t, col, v.cursor)
		before := v.focused
		v.stepEntry(1)
		if v.focused == before {
			break
		}
	}
	require.Len(t, walked, 12, "every entry of the window: %v", walked)
	require.Equal(t, "ConfigMap updated by kubectl-edit", walked[0])
	require.Equal(t, "container api Error (exit 1), restart count 6", walked[len(walked)-1])
	require.Subset(t, walked, []string{"Normal Started x7: Started container api", "ConfigMap apps/checkout-config edited",
		"KubePodNotReady (warning) began firing: Pod has been in a non-ready state for more than 15 minutes."})

	// A column in which nothing is drawn says so, and names the entries
	// on either side of it: where Shift-N and n go.
	v.cursorEdge(-1)
	frame = drawnText(t, v, 132, 33)
	require.Negative(t, v.focused)
	require.Regexp(t, `(?m)^  AT 11:45:00–11:45:1\d  nothing retained began or held here; the entries before and after it:\n  11:46:00  (config|annotations) `, frame)
	require.Contains(t, frame, "  next  a full list\n", "Enter has nothing to open")
	require.Equal(t, "apps/v1/deployments", v.SelectedResource().GVR.String(), "the graph and Grafana are of the workload then")

	// x marks the entry the keys act on: the answer starts from it, and
	// its guide stands where the rollout's did. x on it again returns to
	// the rollout.
	v.stepEntry(1)
	drawnText(t, v, 132, 33)
	require.Equal(t, "ConfigMap updated by kubectl-edit", v.focusedEntry().Summary)
	v.markReference()
	frame = drawnText(t, v, 132, 33)
	require.True(t, s.Chosen)
	require.Contains(t, frame, "  Your mark is at 11:46: ConfigMap updated by kubectl-edit on cm/checkout-config.\n")
	require.Contains(t, frame, "7 failing signals followed within 12m, the first after 4m: BackOff on pod x2x9q.")
	require.Regexp(t, `(?m)^ +your mark 11:46 .*▼$`, frame)
	require.NotContains(t, frame, "rev 2 rolled out 11:48")
	require.Contains(t, frame, "            Reference moment you chose; other entries are labelled before or after it.\n")
	// x on the mark takes it away, and the key line says so while the keys
	// act on it; on any other entry x moves the mark there.
	require.Contains(t, frame, " · n next entry · x clear mark · / filter · ")
	v.stepEntry(1)
	require.Contains(t, drawnText(t, v, 132, 33), " · n next entry · x mark · / filter · ")
	v.stepEntry(-1)
	require.Contains(t, drawnText(t, v, 132, 33), " · x clear mark · ")
	v.markReference()
	frame = drawnText(t, v, 132, 33)
	require.False(t, s.Chosen)
	require.Contains(t, frame, " · n next entry · x mark · / filter · ")
	require.Contains(t, frame, "  Rev 2 rolled out at 11:48, after 1 other change.\n")
	require.Regexp(t, `(?m)^ +rev 2 rolled out 11:48 .*▼$`, frame)

	// A mark in the middle of what went wrong: what began before it is a
	// group of its own, and the answer says how much that is.
	for v.focusedEntry().Summary != "ScalingActive=False FailedGetResourceMetric" {
		v.stepEntry(1)
		drawnText(t, v, 132, 33)
	}
	v.markReference()
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, "  Your mark is at 11:52: ScalingActive=False FailedGetResourceMetric on hpa/checkout-api.\n")
	require.Contains(t, frame, "4 failing signals followed within 6m, the first in the same second: alert KubePodCrashLooping (warning).")
	require.Contains(t, frame, "2 failing signals had begun before it.")
	require.Regexp(t, `(?m)^  `+timelineThenLabel+` +[┊┆]\n> autoscaler +ScalingActive=False… +11:52  for 8m .*\n  alerts +KubePodCrashLooping… \+1 +11:52  for 8m `, frame)
	require.Regexp(t, `(?m)^  `+timelineBeforeLabel+` +[┊┆]\n  warnings +BackOff · pod x2x9q .*\n  alerts +CheckoutHighErrorRate… +11:51  for 9m `, frame)
}

// A read that fell short is never drawn as quiet: its kinds are not on the
// quiet line, their rows are not-read wherever they hold no mark, and the
// read is named with its reason on a line the page never gives up.
func TestTimelineCalmNeverDrawsAReadThatFellShortAsQuiet(t *testing.T) {
	short := map[string]timeline.Coverage{
		"Nodes":       {State: timeline.Denied, Detail: "0 of 1 running the pods in scope; node-a: access denied; this history is unknown, not empty"},
		"Alerts":      {State: timeline.Unknown, Detail: "Alertmanager timeout (no answer within 8s); alert history is unknown, not empty"},
		"Autoscalers": {State: timeline.Incomplete, Detail: "only the first 200 were read; more exist"},
	}
	v := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot { return timelineIncident(scope, short) })
	frame := drawnText(t, v, 132, 40)
	require.Contains(t, strings.Split(frame, "\n")[0], "read 12:00 · 8 of 11 sources")
	require.Contains(t, frame, "Not read: autoscalers incomplete, nodes denied, alerts unknown.")
	// Each read with its state and its reason, and the gap glyph all along.
	require.Regexp(t, `(?m)^  NOT READ +┊\n  autoscalers +incomplete: only the first 200 were read[^×\n]* ×+\n  nodes +denied: 0 of 1 running the pods in scope[^×\n]* ×+\n  alerts +unknown: Alertmanager timeout \(no answer[^×\n]* ×+$`, frame)
	// A kind whose read fell short keeps the marks that were read, and is
	// not-read everywhere else: an alert that began and ended in between is
	// not known not to have happened.
	require.Regexp(t, `(?m)^  alerts +CheckoutHighErrorRate… \+2 +11:51  for 9m +×+█+$`, frame)
	require.Regexp(t, `(?m)^  autoscaler +ScalingActive=False… +11:52  for 8m +×+█+$`, frame)
	// What was read in full is still drawn as read.
	require.Regexp(t, `(?m)^  status +Progressing=False… +11:58  for 2m +▁+┊▁+█+$`, frame)
	// Neither the nodes nor the autoscaler's scaling is quiet.
	require.NotContains(t, frame, "QUIET", "every kind that was read in full has an entry here")
	require.NotRegexp(t, `(?m)^  [a-z ·]*(nodes|scaling|alerts)[a-z ·]* +none retained`, frame)

	// The page gives the list under the axis up before the reasons, and at
	// every size names each read that fell short.
	for _, size := range [][2]int{{132, 33}, {100, 30}, {80, 21}, {60, 21}, {60, 14}, {40, 12}} {
		small := drawnText(t, v, size[0], size[1])
		flat := strings.Join(strings.Fields(small), " ")
		for _, read := range []string{"autoscalers", "nodes", "alerts"} {
			require.Regexp(t, `NOT READ.*`+read+`|Not read: .*`+read, flat, "%dx%d: %s", size[0], size[1], read)
		}
		require.NotRegexp(t, `QUIET[^\n]*(nodes|scaling|alerts)`, small, "%dx%d", size[0], size[1])
	}

	// An Alertmanager that is not declared was not asked: alerts are then
	// neither quiet nor a gap, and the page does not mention them.
	undeclared := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		s := timelineHang(scope)
		kept := s.Coverage[:0]
		for _, coverage := range s.Coverage {
			if coverage.Source != "Alerts" {
				kept = append(kept, coverage)
			}
		}
		s.Coverage = kept
		return s
	})
	frame = drawnText(t, undeclared, 132, 33)
	require.Regexp(t, `(?m)^  warnings · exits +none retained +·+$`, frame)
	require.Regexp(t, `(?m)^  status · nodes +none now; what ended is not kept +×+·$`, frame)
	require.NotContains(t, frame, "alerts")
	require.NotContains(t, frame, "NOT READ")

	// Kubernetes expires events. A window that reaches further back than
	// they are kept draws that stretch of an event row as not read, and a
	// quiet kind of event says how far back its quiet reaches.
	v = timelineIncidentView(t)
	v.setWindow(6 * time.Hour)
	frame = drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^> warnings +BackOff · pod x2x9q +11:50  14 times  ×{50,}▁+┊█+$`, frame)
	require.Regexp(t, `(?m)^  alerts .* for 9m +▁{50,}┊█+$`, frame, "alerts are held by Alertmanager, not expired with the events")
	hang := timelineCalmFixture(t, timelineHang)
	hang.setWindow(6 * time.Hour)
	frame = drawnText(t, hang, 132, 33)
	require.Regexp(t, `(?m)^  warnings +none since 11:00; older expired +×{40,}·+$`, frame)
	require.NotRegexp(t, `(?m)^  [a-z ·]*warnings[a-z ·]* +none retained`, frame)
	// The oldest event that was read moves that edge back.
	hang.snapshot.EventsRead, hang.snapshot.OldestEvent = 3, timelineAt.Add(-3*time.Hour)
	hang.render()
	frame = drawnText(t, hang, 132, 33)
	require.Regexp(t, `(?m)^  warnings +none since 09:00; older expired +×{20,}·{20,}$`, frame)
}

// When nothing happened in the window, or nothing was read, the page says
// exactly that, and never that all is well.
func TestTimelineCalmSaysWhenNothingHappenedOrNothingWasRead(t *testing.T) {
	// Nothing in the window: how much was read outside it, and the key
	// that shows it.
	v := timelineCalmFixture(t, timelineHang)
	require.Equal(t, time.Hour, v.window, "with nothing recent the page opens on an hour")
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, strings.Split(frame, "\n")[0], "last 1h · 0 of 2 entries")
	require.Contains(t, frame, "  Nothing that was read began in the last 1h. 0 shows all 2 entries read.")
	require.Contains(t, frame, timelineNothingCaveat)
	require.NotContains(t, frame, timelineOrderCaveat)
	// What keeps a history for the hour is quiet. An abnormal status, a
	// failing node condition and a firing alert are states of the capture:
	// there is none now, and what ended before it is not kept, so the hour
	// before the capture is not read, never quiet.
	require.Regexp(t, `(?m)^  QUIET\n  rollouts · config · scaling +none retained +·+\n  warnings · exits +none retained +·+\n  annotations +none retained +·+\n`+
		`  status · nodes · alerts +none now; what ended is not kept +×+·$`, frame)
	require.Contains(t, frame, "  next  a full list\n")
	require.NotContains(t, frame, "↑↓ row", "a page without rows names no cursor")
	// 0 shows it: the one rollout, days ago, on an axis of days.
	v.setWindow(0)
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, "  Rev 1 rolled out at Nov11 07:00. No failing signal was read after it.")
	require.Contains(t, frame, timelineNothingCaveat)
	require.Regexp(t, `(?m)^  UTC +Nov12 +Nov13 +Nov14 +Nov15 +Nov16 +Nov17$`, frame)

	// Nothing with a time at all.
	empty := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		return timeline.NewSnapshot(scope, timelineAt, nil, timelineReads(nil))
	})
	frame = drawnText(t, empty, 132, 33)
	require.Contains(t, frame, "  No timed entries were read.\n  No events were returned. Kubernetes expires events (commonly after 1h); this is not evidence that nothing happened.")
	require.Contains(t, frame, timelineNothingCaveat)

	// Changes, and no failing signal after them.
	calm := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		s := timelineIncident(scope, nil)
		kept := s.Entries[:0]
		for i := range s.Entries {
			if !s.Entries[i].Failing {
				kept = append(kept, s.Entries[i])
			}
		}
		return timeline.NewSnapshot(scope, timelineAt, kept, s.Coverage)
	})
	frame = drawnText(t, calm, 132, 33)
	require.Contains(t, frame, "  Rev 2 rolled out at 11:48, after 1 other change. No failing signal was read after it.")
	require.Contains(t, frame, timelineNothingCaveat)
	require.NotContains(t, frame, timelineThenLabel)
	require.Regexp(t, `(?m)^  warnings · exits +none retained +·+┊·+$`, frame)
	// No failing signal was read: of the states of the capture that says
	// that there is none now, and nothing of the minutes before it. The
	// guide does not stand over what was not read.
	require.Regexp(t, `(?m)^  status · nodes · alerts +none now; what ended is not kept +×+·$`, frame)

	// Failing signals, and no change before them in the window.
	wrong := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		s := timelineIncident(scope, nil)
		kept := s.Entries[:0]
		for i := range s.Entries {
			if !s.Entries[i].Change || s.Entries[i].At.Before(timelineAt.Add(-time.Hour)) {
				kept = append(kept, s.Entries[i])
			}
		}
		return timeline.NewSnapshot(scope, timelineAt, kept, s.Coverage)
	})
	frame = drawnText(t, wrong, 132, 33)
	require.Contains(t, frame, "  7 failing signals began in the last 15m, the first at 11:50: BackOff on pod x2x9q. No change was read in this window.\n")
	require.Regexp(t, `(?m)^  `+timelineWrongLabel+`\n> warnings `, frame)
	require.NotContains(t, frame, "▼", "without a change or a mark there is no guide")
	require.Regexp(t, `(?m)^  rollouts · config · scaling +none retained +·+$`, frame)

	// A state that began before the window and still held at the capture
	// is on its axis: it does not vanish because it began earlier.
	old := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		s := timelineIncident(scope, nil)
		entries := append([]timeline.Entry(nil), s.Entries...)
		entries = append(entries, timeline.Entry{At: timelineAt.Add(-3 * time.Hour), Source: timeline.SourceAlert, Failing: true,
			Summary: "KubeQuotaAlmostFull (info) began firing: Namespace quota is almost full.", Ref: timeline.Ref{Kind: "Namespace", Name: "apps"}})
		return timeline.NewSnapshot(scope, timelineAt, entries, s.Coverage)
	})
	old.setWindow(15 * time.Minute)
	frame = drawnText(t, old, 132, 36)
	require.Empty(t, old.snapshot.Visible(15*time.Minute, "KubeQuota"), "it did not begin in the window")
	require.Contains(t, strings.Split(frame, "\n")[0], "13 of 17 entries")
	require.Contains(t, frame, "  Rev 2 rolled out at 11:48, after 1 other change.\n")
	require.Contains(t, frame, "1 failing signal had begun before it.")
	require.Regexp(t, `(?m)^  `+timelineBeforeLabel+` +┊\n  alerts +KubeQuotaAlmostFull \(info\) +09:00  for 3h +█+┊█+$`, frame)

	// Before a read, and after one that failed, there is no observation.
	loading := timelineCalmFixture(t, nil)
	loading.loading = true
	loading.render()
	frame = drawnText(t, loading, 100, 30)
	require.Regexp(t, `^Timeline  Deployment apps/checkout-api +reading…\n`, frame)
	require.Contains(t, frame, "  Reading events, rollout history, restarts, configuration metadata, autoscalers and nodes.\n")
	require.NotContains(t, frame, "QUIET")
}

// A read opens on the window its answer is about; a window the user chose
// stays through a re-read.
func TestTimelineOpensOnTheWindowOfItsAnswer(t *testing.T) {
	scope := &timeline.Scope{Namespace: "apps", Kind: "Deployment", Name: "checkout-api"}
	at := timelineAt
	entry := func(ago time.Duration, change, failing bool) timeline.Entry {
		return timeline.Entry{At: at.Add(-ago), Source: timeline.SourceConfig, Change: change, Failing: failing, Summary: "entry"}
	}
	for name, c := range map[string]struct {
		entries []timeline.Entry
		stalls  *graphEpisodes
		want    time.Duration
	}{
		"the fault and the change before it, with a tenth to spare": {[]timeline.Entry{entry(13*time.Minute, true, false), entry(10*time.Minute, false, true)}, nil, 15 * time.Minute},
		"a change 14m ago needs more than 15m to be seen whole":     {[]timeline.Entry{entry(14*time.Minute, true, false), entry(10*time.Minute, false, true)}, nil, time.Hour},
		"a change long before the fault does not widen it":          {[]timeline.Entry{entry(5*time.Hour, true, false), entry(10*time.Minute, false, true)}, nil, 15 * time.Minute},
		"a fault hours ago":                                           {[]timeline.Entry{entry(3*time.Hour+10*time.Minute, true, false), entry(3*time.Hour, false, true)}, nil, 6 * time.Hour},
		"no fault: the latest change":                                 {[]timeline.Entry{entry(40*time.Hour, true, false), entry(3*time.Hour, true, false)}, nil, 6 * time.Hour},
		"nothing recent: an hour":                                     {[]timeline.Entry{entry(72*time.Hour, true, false)}, nil, time.Hour},
		"a fault days ago is not opened on: 0 shows it":               {[]timeline.Entry{entry(72*time.Hour, false, true)}, nil, time.Hour},
		"nothing at all":                                              {nil, nil, time.Hour},
		"opened from :slow: the window the stalls were looked for in": {[]timeline.Entry{entry(12*time.Minute, true, false), entry(10*time.Minute, false, true)}, timelineStalls(), time.Hour},
	} {
		require.Equal(t, c.want, timelineDefaultWindow(timeline.NewSnapshot(scope, at, c.entries, nil), c.stalls), name)
	}

	v := timelineIncidentView(t)
	require.Equal(t, 15*time.Minute, v.window)
	v.setWindow(6 * time.Hour)
	v.acceptSnapshot(timelineIncident(&v.scope, nil), nil)
	require.Equal(t, 6*time.Hour, v.window, "a re-read keeps the window the user chose")
	v.setWindow(0)
	v.acceptSnapshot(timelineIncident(&v.scope, nil), nil)
	require.Zero(t, v.window, "also when it is everything read")
}

// Opened from :slow, the timeline draws the stalls that were found there
// as bands through its rows, and the change as a labelled moment beside
// them. They were read there: the timeline reads nothing for them, and
// says where nobody looked.
func TestTimelineDrawsTheStallsOfTheViewItWasOpenedFrom(t *testing.T) {
	var source graphEpisodeSource = slowViewFixture(t)
	slow := source.(*slowView)
	slow.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	found := slow.graphEpisodes()
	require.NotNil(t, found)
	require.Equal(t, "stalls", found.Label)
	require.Len(t, found.Intervals, 3, "the stalls :slow found are what it hands over")

	v := timelineCalmFixture(t, nil)
	v.episodes = timelineStalls()
	v.acceptSnapshot(timelineIncident(&v.scope, nil), nil)
	require.Equal(t, time.Hour, v.window)
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, frame, "The 3 stalls :slow found are shaded.")
	marker, _ := timelineLineWith(t, frame, strings.Repeat(" ", 40))
	require.Regexp(t, `^ +stalls +▼{3,} +▼{3,} +┊ +▼{2,}$`, marker)
	lines := strings.Split(frame, "\n")
	axis := ""
	for _, line := range lines {
		if strings.ContainsAny(line, "┬┯") {
			axis = line
		}
	}
	require.Equal(t, timelineCols(marker, "▼"), timelineCols(axis, "━┯"), "heavy on the axis under every stall")
	// The rollout is a labelled moment: its key on the axis, one line
	// that says what it was, and its guide through the rows.
	require.Len(t, timelineCols(axis, "R"), 1)
	require.Equal(t, timelineCols(axis, "R"), timelineCols(marker, "┊"))
	require.Contains(t, frame, "  R rev 2 rolled out 11:48 · image v1 → v2; env ~PAYMENT_TOKEN +FEATURE_FLAGS; limits.memory 512Mi → 256Mi\n")
	require.Regexp(t, `(?m)^  rollouts +rev 2: image v1 → v2; env… +11:48 +·+▲·+$`, frame)
	// The bands are a background behind every row.
	runes, back := drawnCells(t, v, 132, 33)
	var cols []int
	row := -1
	for y := range runes {
		if strings.Contains(string(runes[y]), "stalls ") && strings.ContainsRune(string(runes[y]), '▼') {
			row, cols = y, timelineCols(string(runes[y]), "▼")
		}
	}
	require.GreaterOrEqual(t, row, 0)
	plain := back[row][cols[0]-1]
	for y := row; !strings.ContainsAny(string(runes[y]), "┬┯"); y++ {
		for _, col := range cols {
			require.NotEqual(t, plain, back[y][col], "row %d column %d is in a stall", y, col)
		}
		require.Equal(t, plain, back[y][cols[0]-1], "row %d: and the column before the first stall is not", y)
	}

	// A window that reaches past the hour the stalls were looked for in
	// says so: nobody looked for a stall in the rest of it.
	v.setWindow(6 * time.Hour)
	frame = drawnText(t, v, 132, 40)
	require.Regexp(t, `(?m)^  stalls  :slow looked for them 11:00–12:00 only +×+$`, frame)
	require.Regexp(t, `(?m)^  NOT READ\b`, frame)
	// A window with no stall in it draws none, and claims none.
	v.episodes = &graphEpisodes{Label: "stalls", Start: timelineAt.Add(-24 * time.Hour), End: timelineAt.Add(-23 * time.Hour),
		Intervals: []chart.Interval{{Start: timelineAt.Add(-24 * time.Hour), End: timelineAt.Add(-23*time.Hour - 50*time.Minute)}}}
	v.setWindow(15 * time.Minute)
	frame = drawnText(t, v, 132, 33)
	require.NotContains(t, frame, "stalls")
	require.Regexp(t, `(?m)^ +rev 2 rolled out 11:48 +▼$`, frame, "the change is the guide again")
}

// The calm layout adds no request: the page, both cursors, the windows,
// the mark, the list and the sources are drawn from the retained
// observation.
func TestTimelineCalmLayoutReadsNothing(t *testing.T) {
	v := timelineIncidentView(t)
	reads := 0
	v.loader = func(context.Context) (*timeline.Snapshot, error) { reads++; return nil, errors.New("unexpected read") }
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		drawnText(t, v, size[0], size[1])
		for _, window := range append([]time.Duration{0}, timeline.Windows...) {
			v.setWindow(window)
			drawnText(t, v, size[0], size[1])
			v.moveRow(1)
			v.moveCursor(3)
			v.cursorEdge(1)
			v.stepEntry(-1)
			v.stepEntry(1)
			v.markReference()
			v.markReference()
			v.toggleDetail()
			drawnText(t, v, size[0], size[1])
			v.toggleDetail()
			v.toggleSources()
			drawnText(t, v, size[0], size[1])
			v.toggleSources()
			v.applyQuery("config")
			v.applyQuery("")
			v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		}
	}
	require.Zero(t, reads, "a layout reads nothing")
	require.NotNil(t, v.snapshot)
}

// a shows the list and the pane this view had before the calm layout, and
// s the sources and limits, from either page. Nothing the list showed is
// lost, and the keys the list scrolls with are its own again.
func TestTimelineKeepsItsListUnderAAndItsSourcesUnderS(t *testing.T) {
	v := timelineIncidentView(t)
	require.Equal(t, calmDetailLabel, walkKey(v.actions, calmDetailKey))
	require.Equal(t, "Sources and limits", walkKey(v.actions, ui.KeyS))
	press := func(key tcell.Key, r rune) *tcell.EventKey {
		action, ok := v.actions.Get(key)
		require.True(t, ok)
		return action.Action(tcell.NewEventKey(key, r, tcell.ModNone))
	}
	calm := drawnText(t, v, 132, 33)

	// a: the list, with the entry the keys act on selected.
	require.Nil(t, press(calmDetailKey, 'a'))
	require.True(t, v.allDetail)
	list := drawnText(t, v, 132, 33)
	require.Contains(t, list, "Timeline / Deployment apps/checkout-api")
	require.Contains(t, list, "12 of 16 entries · window last 15m · captured 12:00:00Z")
	require.Contains(t, list, "Fault began 11:50:00Z (10m before capture): pod/checkout-api-7d9f8c6b5d-x2x9q Warning BackOff x14")
	require.Contains(t, list, "  UTC TIME KIND    VS FAULT    AGE    RESOURCE")
	require.Contains(t, list, "▶ 11:50:00 EVENT   FAULT BEGAN 10m    pod/checkout-api-7…b5d-x2x9q Warning BackOff x14")
	require.Contains(t, list, "below: preceded the first failing signal; order, not cause")
	require.Contains(t, list, "◆ 11:48:00 ROLLOUT 2m before   12m    rs/checkout-api-7d9f8c6b5d   rev 2: image v1 -> v2")
	require.Contains(t, list, "Time from: Event firstTimestamp/lastTimestamp")
	require.Contains(t, list, "r read · Shift-D Grafana · a less · Esc", "the key line names the way back")
	// 0 shows every entry that was read, as the list opened before.
	require.Nil(t, press(ui.Key0, '0'))
	require.Contains(t, drawnText(t, v, 132, 33), "16 of 16 entries · window all read")
	require.Nil(t, press(ui.Key1, '1'))
	// The arrows and n are the list's own there: the calm page's cursors
	// are not moved from under it.
	for _, key := range []tcell.Key{tcell.KeyUp, tcell.KeyDown, tcell.KeyLeft, tcell.KeyRight, tcell.KeyHome, tcell.KeyEnd} {
		require.NotNil(t, press(key, 0), "%s is handed on to the list", tcell.KeyNames[key])
	}
	require.NotNil(t, press(ui.KeyN, 'n'))
	require.NotNil(t, press(ui.KeyJ, 'j'))
	require.Negative(t, v.cursor)
	// s from the list, and back to it.
	require.Nil(t, press(ui.KeyS, 's'))
	sources := drawnText(t, v, 132, 33)
	require.Contains(t, sources, "Timeline / Deployment apps/checkout-api")
	require.Contains(t, sources, "TIMELINE OBSERVATION")
	require.Nil(t, v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)))
	require.True(t, v.allDetail)
	require.Equal(t, list, drawnText(t, v, 132, 33))
	// a again: the calm page as it was.
	require.Nil(t, press(calmDetailKey, 'a'))
	require.False(t, v.allDetail)
	require.Equal(t, calm, drawnText(t, v, 132, 33))

	// s from the calm page: the sources and limits under its title.
	require.Nil(t, press(ui.KeyS, 's'))
	sources = drawnText(t, v, 132, 33)
	lines := strings.Split(sources, "\n")
	require.Equal(t, strings.Split(calm, "\n")[0], lines[0], "the title of the page stays over them")
	require.Equal(t, "TIMELINE OBSERVATION", lines[1])
	for _, want := range []string{"Window shown: last 15m before capture (12 of 16 entries)", "FIRST FAILING SIGNAL", "SOURCES", "Events                 COMPLETE", "LIMITS",
		"Repeated events are merged by type, reason and kind"} {
		require.Contains(t, sources, want)
	}
	require.Contains(t, sources, "s or Esc returns to the timeline · r re-read")
	require.NotNil(t, press(tcell.KeyDown, 0), "the text scrolls with the arrows")
	require.Nil(t, press(ui.KeyS, 's'))
	require.Equal(t, calm, drawnText(t, v, 132, 33))

	// While the filter prompt is open a letter is text, not a key.
	v.cmdBuff.SetActive(true)
	for _, r := range "anjk" {
		require.NotNil(t, press(tcell.Key(r), r), "%c goes to the prompt", r)
	}
	require.False(t, v.allDetail)
	v.cmdBuff.SetActive(false)

	// Esc: the time cursor first, then the filter, then out.
	v.moveCursor(1)
	v.applyQuery("config")
	require.Nil(t, v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)))
	require.Negative(t, v.cursor)
	require.Equal(t, "config", v.query)
	require.Nil(t, v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)))
	require.Empty(t, v.query)
}

// A filter narrows the rows and the list to the entries that match. The
// answer is still about the window, and says how much the filter left.
func TestTimelineCalmFilterNarrowsTheRowsAndSaysSo(t *testing.T) {
	v := timelineIncidentView(t)
	v.applyQuery("config")
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, strings.Split(frame, "\n")[0], "last 15m · filter config · 2 of 16 entries")
	require.Contains(t, frame, "  Rev 2 rolled out at 11:48, after 1 other change.\n")
	require.Contains(t, frame, `Filter "config": 2 of 12 entries in this window drawn; Esc clears it.`)
	require.Regexp(t, `(?m)^> config +checkout-config updated by… +11:46 `, frame)
	require.Regexp(t, `(?m)^  annotations +ConfigMap apps/checkout-confi…  11:46 `, frame)
	require.NotRegexp(t, `(?m)^[> ] (warnings|alerts|rollouts) `, frame)
	require.NotContains(t, frame, "QUIET", "what the filter hides is not quiet")
	v.applyQuery("nothing-matches")
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, `Filter "nothing-matches": 0 of 12 entries in this window drawn; Esc clears it.`)
	require.NotContains(t, frame, "QUIET")
	v.applyQuery("")
	require.Contains(t, drawnText(t, v, 132, 33), "  QUIET  scaling · nodes: none now\n")
}

// Every size a terminal can give the view: the page fits it in cells,
// writes plain characters with noIcons, and keeps its title and its
// answer.
func TestTimelineCalmPageFitsEveryArea(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		for name, build := range map[string]func(t *testing.T) *timelineView{
			"incident": timelineIncidentView,
			"hang": func(t *testing.T) *timelineView {
				v := timelineCalmFixture(t, nil)
				v.episodes = timelineStalls()
				v.acceptSnapshot(timelineHang(&v.scope), nil)
				return v
			},
		} {
			v := build(t)
			v.app.Config.K9s.UI.NoIcons = ascii
			for width := ui.MinTaskWidth; width <= 170; width += 7 {
				for height := ui.MinTaskHeight; height <= 44; height += 4 {
					for _, cursor := range []bool{false, true} {
						if cursor {
							v.stepEntry(1)
						} else if v.cursor >= 0 {
							v.escape(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
						}
						frame := drawnText(t, v, width, height)
						where := fmt.Sprintf("%s %dx%d ascii=%v cursor=%v", name, width, height, ascii, cursor)
						lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
						require.LessOrEqual(t, len(lines), height, where)
						require.True(t, strings.HasPrefix(lines[0], "Timeline  "), "%s: %q", where, lines[0])
						for _, line := range lines {
							require.LessOrEqual(t, runewidth.StringWidth(line), width, "%s: %q", where, line)
						}
						if ascii {
							require.Equal(t, -1, strings.IndexFunc(frame, func(r rune) bool { return r > 127 }), "%s:\n%s", where, frame)
						}
						require.NotContains(t, strings.ToLower(frame), "caused", where)
					}
				}
			}
		}
	}
}
