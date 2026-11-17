// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

func slowViewFixture(t *testing.T) *slowView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	declareStudySources(t, app)
	selection := slowtest.Path().Selection
	selection.Context = app.Config.ActiveContextName()
	v := newSlowView(app, selection)
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	// The app is not running in a test, so a read is applied directly.
	v.active = true
	return v
}

func slowStoryRead(story *slowtest.Story, window time.Duration) *slowRead {
	path := slowtest.Path()
	return &slowRead{path: path, report: slowtest.Report(story, slowtest.Sources(), path, window)}
}

func slowPress(t *testing.T, v *slowView, key tcell.Key) {
	t.Helper()
	action, ok := v.actions.Get(key)
	require.True(t, ok, "no action on %s", tcell.KeyNames[key])
	action.Action(tcell.NewEventKey(key, 0, tcell.ModNone))
}

// slowAllDetail turns a view to the page a shows: the hop table and the
// lanes strip the view had before the calm page. The tests below hold that
// page to what it showed then; slow_calm_test.go holds the calm page.
func slowAllDetail(t *testing.T, v *slowView) {
	t.Helper()
	if !v.all {
		slowPress(t, v, calmDetailKey)
	}
	require.True(t, v.all)
}

func TestSlowAllDetailStatesEveryHopWhenItHappenedAndWhatOverlapsAtEverySize(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	slowAllDetail(t, v)
	// 132x32, 80x20 and 60x20 are what the app leaves the view on a 132x36,
	// 80x24 or 60x24 terminal.
	for _, size := range [][2]int{{132, 32}, {80, 20}, {60, 20}, {40, 8}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		flat := strings.Join(strings.Fields(frame), " ")
		// Hop, key number and status word at every width.
		for _, want := range [][2]string{{"client → ingress", "499 2.2/s elevated"}, {"ingress → service", "2 of 3 ready elevated"},
			{"service → pods", "p99 48s OUTLIER"}, {"dns", "p99 6ms quiet"}, {"network", "drops 0.37/s elevated"}} {
			require.Regexp(t, `(?m)^[> ] `+regexp.QuoteMeta(want[0])+` .*`+regexp.QuoteMeta(want[1]), slowFlatLines(frame), "%dx%d", size[0], size[1])
		}
		require.Contains(t, flat, "3 episodes of client give-ups (HTTP 499) at the ingress")
		require.Contains(t, flat, "All 3 overlap slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.")
		require.Contains(t, flat, "Overlap is order in time, not cause.")
		require.Contains(t, frame, "██", "the lanes mark when a rule was crossed")
		require.NotContains(t, flat, "overlap packet drops")
		lower := strings.ToLower(frame)
		for _, banned := range []string{"because", "caused", "due to", "root cause", "healthy"} {
			require.NotContains(t, lower, banned)
		}
		switch size[0] {
		case 132:
			require.Contains(t, frame, "Slow requests / Service gitlab/gitlab-webservice-default · last 1h 07:20–08:20 UTC · read 08:20:12 · step 30s · 35/35 queries")
			require.Contains(t, frame, "3 episodes of client give-ups (HTTP 499) at the ingress: 07:28, 07:47, 08:13 UTC, 2m-4m each.")
			require.Contains(t, frame, "  HOP                 WHAT                      LAST 1h        WORST          STATE    EVIDENCE")
			require.Contains(t, flat, "client → ingress gitlab.study.example.test")
			require.Contains(t, flat, "6.29% of requests · p99 10s")
			require.Contains(t, flat, "> service → pods 3 pods")
			require.Contains(t, flat, "OUTLIER 1 of 3 pods: …-hx4mt p99 48s vs peers 350ms", "the row says whether one Pod is the outlier")
			require.Contains(t, flat, "…-hx4mt …default-6f9c7d5b84-hx4mt")
			require.Contains(t, flat, "p99 48s OUTLIER queued 30 · free-threads 0 · throttled 86%")
			require.Contains(t, flat, "pods → postgresql gitlab-postgresql")
			require.Contains(t, flat, "connections 41 quiet up 1 · active 6 · max-connections 100")
			require.Contains(t, frame, "WHEN IT HAPPENED · 1 column = 1m · █ over its rule · × no data · UTC")
			for _, label := range []string{"ingress: 499, p99, +1", "endpoints: not ready …-hx4mt", "pods: p99 …-hx4mt", "pods: queued …-hx4mt",
				"pods: free-threads …-hx4mt", "pods: throttled …-hx4mt", "network: drops …-2bq7k"} {
				require.Contains(t, frame, label)
			}
			// One column is one minute: the three stalls and, between the
			// first two, the unrelated burst of drops on another lane. The
			// give-ups are read from a rate over two minutes and the queue
			// from a gauge; both fill the same cells.
			require.Contains(t, frame, "ingress: 499, p99, +1          │········███················████······················██·····  9m of 1h")
			require.Contains(t, frame, "pods: queued …-hx4mt           │········███················████······················██·····  9m of 1h")
			require.Contains(t, frame, "network: drops …-2bq7k         │···················█········································  1m of 1h")
			require.Contains(t, frame, "no marks: dependencies, dns, restarts, rollouts")
			require.Contains(t, frame, "WHAT LINES UP")
			require.Contains(t, flat, "2 of 3 (07:28, 07:47) overlap not Ready on pod …-hx4mt.")
			require.Contains(t, flat, "Packet drops (POLICY_DENIED) on pod …-2bq7k: 07:39 for 1m, outside every episode.")
			require.Contains(t, flat, "5xx stayed at 0%: clients gave up waiting, they were not refused.")
			// The next steps name the sibling views by the keys that open
			// them here: the probe, the Pods side by side on the signal that
			// set one apart, and that Pod against its limits. The line ends
			// on a whole step.
			require.Contains(t, frame, "next: Shift-B probe each pod now · m p99 per pod · Enter on …-hx4mt graphs it · h Hubble · Shift-D Grafana · t timeline\n")
			require.NotContains(t, frame, "p probe", "p is not the probe: Shift-B is, here as on a Service row")
			require.Contains(t, frame, "↑↓ hop · Enter open · a less detail · ←→ cursor · n/N episode · 1-4 window · s queries · r re-read · b dots · Esc back",
				"the key back to the calm page is named with the keys of this one")
		case 80:
			require.Contains(t, frame, "Slow requests / gitlab-webservice-default · last 1h · read 08:20:12")
			require.Contains(t, frame, "3 episodes of client give-ups (HTTP 499) at the ingress, 2m-4m each.")
			require.Contains(t, frame, "  HOP                LAST 1h    WORST         STATE    EVIDENCE")
			require.Contains(t, flat, "pods → 3 deps 3 read quiet no rule crossed: postgre", "quiet dependencies share a row when the screen is short")
			require.Contains(t, flat, "OUTLIER 1 of 3: …-hx4mt vs 350ms")
			require.Contains(t, frame, "pods …-hx4mt: p99 +3", "the Pod name survives a narrow label")
			require.Contains(t, frame, "not ready …-hx4mt", "and so does what its marks mean")
			require.Contains(t, frame, "network: drops …-2bq7k")
			require.Contains(t, frame, "when, UTC")
			require.Contains(t, frame, "1 column = 2m")
			require.Contains(t, flat, "Packet drops (POLICY_DENIED) on pod …-2bq7k: 07:39 for 1m, outside every episode.")
			require.Contains(t, frame, "↑↓ hop · Enter open · a less detail · ←→ cursor · 1-4 window · s queries · Esc")
		case 60:
			require.Contains(t, frame, "Enter open · a less detail · 1-4 window · s queries · Esc")
			require.Contains(t, frame, "  HOP                WORST         STATE    EVIDENCE")
			require.NotContains(t, frame, "LAST 1h", "the trend gives way before hop, key number or state")
			for _, label := range []string{"not ready …-hx4mt", "p99 +3 …-hx4mt", "drops …-2bq7k"} {
				require.Contains(t, frame, label, "a narrow label keeps what the mark means and the Pod")
			}
		}
	}
}

// slowFlatLines removes braille trend cells and runs of spaces inside each
// line, so that a row can be matched by its words at every width.
func slowFlatLines(frame string) string {
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		var b strings.Builder
		for _, r := range line {
			if r < 0x2800 || r > 0x28ff {
				b.WriteRune(r)
			}
		}
		marker := "  "
		if strings.HasPrefix(line, ">") {
			marker = "> "
		}
		lines[i] = marker + strings.Join(strings.Fields(strings.TrimPrefix(b.String(), ">")), " ")
	}
	return strings.Join(lines, "\n")
}

func TestSlowLaneCellsAddUpToTheTimeBesideThem(t *testing.T) {
	start := time.Date(2026, 11, 17, 7, 20, 0, 0, time.UTC)
	frame := chart.Frame{Start: start, End: start.Add(time.Hour), Step: time.Minute, Width: 60}
	at := func(minutes float64) time.Time { return start.Add(time.Duration(minutes * float64(time.Minute))) }
	marks := []chart.Interval{
		{Start: at(7.5), End: at(10.5)},  // three raised samples half a minute apart from the column edges
		{Start: at(20.4), End: at(20.6)}, // shorter than half a column
		{Start: at(30.2), End: at(30.2)}, // a moment
		{Start: at(59.5), End: at(60)},   // the newest sample of the window
	}
	snapped := slowSnap(marks, frame)
	require.Equal(t, []chart.Interval{
		{Start: at(8), End: at(11)}, {Start: at(20), End: at(21)}, {Start: at(30.2), End: at(30.2)}, {Start: at(59), End: at(60)},
	}, snapped)
	require.Equal(t, 3+1+1+1, frame.MarkedCount(snapped), "three minutes fill three cells, not four")
	require.Equal(t, 4+1+1+1, frame.MarkedCount(marks), "unsnapped, a stretch that straddles two columns fills one more")
	require.Equal(t, marks, slowSnap(marks, chart.Frame{}), "without a frame nothing is moved")
}

func TestSlowAllDetailNoIconsUsesPlainCharactersOnly(t *testing.T) {
	v := slowViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	slowAllDetail(t, v)
	for _, size := range [][2]int{{132, 32}, {80, 20}, {60, 20}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, r := range frame {
			require.True(t, r < 128 || r == '·', "%q is neither ASCII nor the shared separator at %d columns", r, size[0])
		}
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		require.Contains(t, frame, "client -> ingress")
		require.Contains(t, frame, "OUTLIER")
		require.Contains(t, frame, "##", "a mark is a hash")
		require.Contains(t, frame, "...-hx4mt")
		lines := strings.Split(strings.TrimRight(frame, "\n "), "\n")
		require.Regexp(t, `Esc( back)?$`, strings.TrimRight(lines[len(lines)-1], " "),
			"the key hints are measured in the words that are drawn, and end whole at %d columns", size[0])
	}
	frame := drawnText(t, v, 132, 32)
	require.Contains(t, frame, "WHEN IT HAPPENED · 1 column = 1m · # over its rule · x no data · UTC")
	require.Contains(t, frame, "pods: queued ...-hx4mt         |........###................####......................##.....  9m of 1h")
	require.Contains(t, frame, "> service -> pods")
	require.Contains(t, frame, "Up/Down hop · Enter open · a less detail · Left/Right cursor")
	require.Contains(t, frame, "latency 120us")
}

func TestSlowAllDetailSaysNotReadWithTheReasonAndNeverDrawsItAsQuiet(t *testing.T) {
	v := slowViewFixture(t)
	slowAllDetail(t, v)
	frame := drawnText(t, v, 100, 30)
	require.Contains(t, frame, "Slow requests / Service gitlab/gitlab-webservice-default")
	require.Contains(t, frame, "Reading the configured path, then one bounded batch from the declared Prometheus.")
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "Bounded, read-only, and only because this view was opened.")

	denied := &slowtest.Story{Deny: observability.Status{State: observability.StateDenied, Reason: "HTTP 403", HTTPStatus: 403}}
	v.accept(slowStoryRead(denied, time.Hour))
	frame = drawnText(t, v, 132, 32)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, frame, "0/35 queries")
	require.Contains(t, frame, "Prometheus was not read (denied: HTTP 403), so no hop has latency evidence.")
	require.Equal(t, 7, strings.Count(frame, "not read prometheus: denied (HTTP 403)"), "every hop but the endpoints says not read, with the reason")
	require.Contains(t, flat, "client → ingress gitlab.study.example.test - not read prometheus: denied (HTTP 403)", "no number, a dash and the words")
	require.Contains(t, flat, "ingress → service 3 endpoints 3 of 3 ready elevated …-hx4mt Ready=True since 07:51", "the Kubernetes API still answers")
	require.Contains(t, frame, "ingress                        │××××××××××××××××××××××××××××××××××××××××××××××××××××××××××××  not read")
	require.Contains(t, frame, "endpoints: Ready changed       │×××××××××××××××××××××××××××××××█····························  1 in 1h",
		"what the API no longer knows is unknown; the change it does know is drawn")
	require.Contains(t, frame, "WHAT WAS READ")
	require.Contains(t, flat, "Not read: ingress, pods, dependencies, dns and network. Not read is not quiet and not healthy.")
	require.NotRegexp(t, `(?m)^[> ] (client|ingress|service|pods|dns|network).* quiet( |$)`, frame, "no row that was not read is called quiet")
	require.NotRegexp(t, `client → ingress .* 0/s`, frame)

	// A quiet window says so for what was read and lists what was not.
	quiet := slowtest.Path()
	for i := range quiet.Pods {
		quiet.Pods[i].ReadySince = slowtest.Now.Add(-72 * time.Hour)
	}
	v.accept(&slowRead{path: quiet, report: slowtest.Report(&slowtest.Story{Quiet: true, Absent: []string{"hubble_"}}, slowtest.Sources(), quiet, time.Hour)})
	frame = drawnText(t, v, 132, 32)
	flat = strings.Join(strings.Fields(frame), " ")
	require.Contains(t, frame, "No signal crossed its rule in the last 1h.")
	require.Contains(t, flat, "Quiet for the hops that were read: ingress, endpoints, pods, dependencies and dns.")
	require.Contains(t, flat, "Not read: network.")
	require.Contains(t, flat, "Quiet is not healthy: a rule compares each signal with its own usual level in this window.")
	require.Contains(t, flat, "network cilium-hubble, to pods - not read no series matched hubble_drop_total")
	require.NotContains(t, frame, "OUTLIER")
	require.NotRegexp(t, `│[·×]*█`, frame, "no lane has a mark")

	// A selection without a request path explains itself and keeps nothing.
	fresh := slowViewFixture(t)
	slowAllDetail(t, fresh)
	fresh.accept(&slowRead{err: errors.New("no Service in namespace gitlab selects the Pods of StatefulSet gitlab/batch, so there is no request path to read")})
	frame = drawnText(t, fresh, 100, 30)
	require.Contains(t, strings.Join(strings.Fields(frame), " "),
		"No request path: no Service in namespace gitlab selects the Pods of StatefulSet gitlab/batch, so there is no request path to read.")
	require.Contains(t, frame, "Nothing has been read yet.")

	// A re-read that fails keeps the earlier read on screen and says so.
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	v.accept(&slowRead{err: errors.New("Service gitlab/gitlab-webservice-default: the read timed out")})
	frame = drawnText(t, v, 132, 32)
	require.Contains(t, frame, "Re-read failed, the earlier read is shown: Service gitlab/gitlab-webservice-default: the read timed out")
	require.Contains(t, frame, "OUTLIER")
}

func TestSlowAllDetailKeysOpenRowsMoveTheCursorAndShowQueries(t *testing.T) {
	v := slowViewFixture(t)
	var reads, resolved atomic.Int32
	v.loader = func(_ context.Context, path *slowpath.Path, window time.Duration) slowRead {
		reads.Add(1)
		if path == nil {
			resolved.Add(1)
			path = slowtest.Path()
		}
		return slowRead{path: path, report: slowtest.Report(&slowtest.Story{}, slowtest.Sources(), path, window)}
	}
	read := v.loader(t.Context(), nil, v.window)
	v.accept(&read)
	slowAllDetail(t, v)
	frame := drawnText(t, v, 132, 32)
	require.Equal(t, slowRow{hop: slowpath.HopApp}, v.focus, "the table opens on the hop of the strongest finding")
	require.Contains(t, frame, "> service → pods")
	require.Contains(t, frame, "    …-hx4mt", "the Pods are listed when one of them is the outlier and there is room")

	// Down walks hops and Pods; the focused Pod is what a graph is about.
	slowPress(t, v, tcell.KeyDown)
	slowPress(t, v, tcell.KeyDown)
	slowPress(t, v, tcell.KeyDown)
	require.Equal(t, slowRow{hop: slowpath.HopApp, pod: slowtest.Outlier}, v.focus)
	require.Contains(t, drawnText(t, v, 132, 32), ">   …-hx4mt")
	target := v.SelectedResource()
	require.Equal(t, client.PodGVR, target.GVR)
	require.Equal(t, slowtest.Outlier, target.Name)
	require.Equal(t, types.UID("uid-"+slowtest.Outlier), target.UID)
	require.Contains(t, drawnText(t, v, 132, 32), "· m p99 of each pod side by side · Enter graphs …-hx4mt ·")
	slowPress(t, v, tcell.KeyUp)
	slowPress(t, v, tcell.KeyUp)
	slowPress(t, v, tcell.KeyUp)
	require.Equal(t, "apps/v1/deployments", v.SelectedResource().GVR.String(), "a hop row hands the workload to graph, timeline and Grafana")
	require.Equal(t, client.SvcGVR, v.serviceTarget().GVR, "a probe is asked for the Service")
	window, ok := v.GrafanaWindow()
	require.True(t, ok)
	require.Equal(t, slowtest.Now.Add(-time.Hour), window.From)
	require.Equal(t, slowtest.Now, window.To)
	require.Equal(t, "the window :slow read, 1h", window.Reason)

	// Enter on the app hop folds and unfolds its Pods.
	slowPress(t, v, tcell.KeyEnter)
	require.NotContains(t, drawnText(t, v, 132, 32), "…default-6f9c7d5b84-hx4mt")
	slowPress(t, v, tcell.KeyEnter)
	require.Contains(t, drawnText(t, v, 132, 32), "…default-6f9c7d5b84-hx4mt")

	// The cursor reads the time and each lane's value; n jumps to an episode.
	slowPress(t, v, ui.KeyN)
	frame = drawnText(t, v, 132, 32)
	require.Contains(t, frame, "WHEN IT HAPPENED · cursor 07:28–07:29 UTC · the value of each lane at the cursor is on the right · Esc hides it")
	require.Contains(t, frame, "pods: p99 …-hx4mt              │········███················████······················██·····  48s")
	require.Contains(t, frame, "pods: free-threads …-hx4mt     │········███················████······················██·····  0")
	require.Contains(t, frame, "network: drops …-2bq7k         │········┆··········█········································  0/s")
	slowPress(t, v, ui.KeyN)
	require.Contains(t, drawnText(t, v, 132, 32), "cursor 07:47–07:48 UTC")
	slowPress(t, v, ui.KeyShiftN)
	slowPress(t, v, tcell.KeyLeft)
	require.Contains(t, drawnText(t, v, 132, 32), "cursor 07:27–07:28 UTC")
	require.Contains(t, drawnText(t, v, 80, 20), "at 07:26–07:28", "a narrow strip has wider columns and reads the cursor in the axis gutter")
	slowPress(t, v, tcell.KeyEscape)
	require.NotContains(t, drawnText(t, v, 132, 32), "cursor 07:", "Esc first puts the cursor away")
	require.EqualValues(t, 1, reads.Load(), "moving the focus and the cursor reads nothing")

	// s shows every query, the samples behind it and its rule.
	slowPress(t, v, ui.KeyS)
	frame = drawnText(t, v, 132, 32)
	require.Contains(t, frame, "QUERIES, SAMPLES AND RULES · s or Enter returns to the lanes · lines 1-")
	require.Contains(t, frame, "p99 [gitlab-webservice] Rails request duration, 99th percentile")
	require.Contains(t, frame, "worst 48s at 07:28 (…-hx4mt) · usual 350ms · raised 9m of 1h in 3 stretches · 363 samples in 3 series, step 30s")
	require.Contains(t, frame, "rule: raised at 3x its window median and at least 50ms above it; always raised at 5s")
	require.Contains(t, frame, `  query: histogram_quantile(0.99, sum by (le,pod) (rate(http_request_duration_seconds_bucket{namespace="gitlab",pod=~"gitlab-web`)
	require.Contains(t, frame, `6f9c7d5b84-hx4mt"}[120s])))`, "a query is wrapped, never cut")
	slowPress(t, v, tcell.KeyPgDn)
	require.NotContains(t, drawnText(t, v, 132, 32), "lines 1-")
	slowPress(t, v, tcell.KeyUp)
	require.Contains(t, drawnText(t, v, 132, 32), `query: min by (pod) (kube_pod_status_ready{namespace="gitlab",pod=~"`, "the detail follows the focused hop")
	slowPress(t, v, ui.KeyS)
	require.Contains(t, drawnText(t, v, 132, 32), "WHEN IT HAPPENED")

	// A window that crosses midnight names both days.
	v.accept(slowStoryRead(&slowtest.Story{}, 24*time.Hour))
	require.Contains(t, drawnText(t, v, 132, 32), "last 24h Nov16 08:15–Nov17 08:15 UTC · read 08:20:12")
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))

	// A window key sends the batch again and keeps the resolved path; r
	// reads the path again as well.
	slowPress(t, v, ui.Key3)
	require.Equal(t, 6*time.Hour, v.window)
	require.True(t, v.loading)
	require.Contains(t, drawnText(t, v, 132, 32), "Reading again; what is shown is retained until the new read arrives.")
	require.Eventually(t, func() bool { return reads.Load() == 2 }, 2*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 1, resolved.Load(), "a window change does not read the path again")
	slowPress(t, v, ui.KeyR)
	require.Eventually(t, func() bool { return resolved.Load() == 2 }, 2*time.Second, 5*time.Millisecond)
}

// The view hands on to its siblings by the keys that mean the same in every
// incident view: Shift-B the probe, m the graph, t the timeline, Shift-D
// Grafana. m arrives in :graph on one chart per Pod, on the signal that set
// a Pod apart here and for the window that was read.
func TestSlowHandsOnToProbeGraphAndTimelineByTheSharedKeys(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	for _, key := range []tcell.Key{probeKey, ui.KeyH, ui.KeyM, ui.KeyT, ui.KeyS, ui.KeyR, tcell.KeyEnter, grafanaKey, ui.Key1, ui.Key4,
		tcell.KeyHome, tcell.KeyEnd} {
		_, ok := v.actions.Get(key)
		require.True(t, ok, tcell.KeyNames[key])
	}
	_, ok := v.actions.Get(ui.KeyP)
	require.False(t, ok, "p is the per-Pod page of :graph; it is not a second key for the probe here")
	action, _ := v.actions.Get(grafanaKey)
	require.Equal(t, grafanaKeyLabel, action.Description, "Shift-D is the Grafana hand-off here as in the other incident views")
	action, _ = v.actions.Get(ui.KeyM)
	require.Equal(t, graphPodsShort, action.Description, "m is the graph key; here it opens the Pods side by side")
	require.True(t, action.Opts.Visible && action.Opts.Priority > 0, "named in the key line above the view, where a short screen has no next: line")
	action, _ = v.actions.Get(ui.KeyT)
	require.Contains(t, action.Description, "imeline")
	action, _ = v.actions.Get(probeKey)
	require.Equal(t, "Probe pods", action.Description, "the words of the same key on a Service row")
	require.True(t, action.Opts.Visible && action.Opts.Priority > 0)
	require.Equal(t, tcell.KeyNames[probeKey], probeKeyName)
	require.Equal(t, tcell.KeyNames[slowKey], slowKeyName)

	// The probe is asked for the Service, which it accepts.
	require.Equal(t, client.SvcGVR, v.serviceTarget().GVR)
	require.Empty(t, probeAvailability(v.serviceTarget()))

	// The signal that set a Pod apart is the one the comparison opens on.
	id, name := slowGraphSignal(v.report())
	require.Equal(t, [2]string{"gitlab-webservice/p99", "p99"}, [2]string{id, name})
	none, _ := slowGraphSignal(nil)
	require.Empty(t, none)
	slowPress(t, v, ui.Key3)
	v.loading = false
	slowPress(t, v, ui.KeyM)
	graph, ok := v.app.Content.Top().(*graphView)
	require.True(t, ok, "m opens :graph")
	t.Cleanup(graph.Stop)
	require.True(t, graph.comparing, "one chart per Pod")
	require.Equal(t, "gitlab-webservice/p99", graph.signal)
	require.Equal(t, 6*time.Hour, graph.window, "the window that was read here")
	require.Equal(t, graphKindService, graph.target.Kind, "the Pods compared are the Pods behind the Service")
	require.Equal(t, "gitlab-webservice-default", graph.target.Name)
	v.app.Content.Pop()

	// Enter on a Pod row opens that Pod's own charts, in the same window.
	drawnText(t, v, 132, 32)
	slowPress(t, v, tcell.KeyDown)
	slowPress(t, v, tcell.KeyDown)
	slowPress(t, v, tcell.KeyDown)
	require.Equal(t, slowtest.Outlier, v.focus.pod)
	slowPress(t, v, tcell.KeyEnter)
	graph, ok = v.app.Content.Top().(*graphView)
	require.True(t, ok)
	t.Cleanup(graph.Stop)
	require.False(t, graph.comparing)
	require.Equal(t, "Pod", graph.target.Kind)
	require.Equal(t, slowtest.Outlier, graph.target.Name)
	require.Equal(t, 6*time.Hour, graph.window)
	v.app.Content.Pop()

	// A view whose destination changed opens nothing.
	v.revision++
	slowPress(t, v, ui.KeyM)
	slowPress(t, v, probeKey)
	require.Same(t, v, v.app.Content.Top())
	v.revision--

	// A line that has no room for every step drops whole steps from its
	// end; it never ends inside one.
	steps := []slowStep{{"Shift-B probe each pod now", "Shift-B probe pods"}, {"m p99 of each pod side by side", "m p99 per pod"}, {"t timeline", "t timeline"}}
	require.Equal(t, "next: Shift-B probe each pod now · m p99 of each pod side by side · t timeline", v.fitSteps(steps, 90))
	require.Equal(t, "next: Shift-B probe each pod now · m p99 per pod · t timeline", v.fitSteps(steps, 70))
	require.Equal(t, "next: Shift-B probe pods · m p99 per pod · t timeline", v.fitSteps(steps, 58))
	require.Equal(t, "next: Shift-B probe pods · m p99 per pod", v.fitSteps(steps, 50))
	require.Equal(t, "next: Shift-B probe pods", v.fitSteps(steps, 30))
	require.Empty(t, v.fitSteps(nil, 80))
}

// The cursor moves as it does in :graph and in a zoomed dashboard panel:
// one column, ten with Shift, and Home and End to the ends of the window.
func TestSlowCursorKeysAreThoseOfEveryChartView(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	drawnText(t, v, 132, 32)
	press := func(key tcell.Key, mod tcell.ModMask) {
		action, ok := v.actions.Get(key)
		require.True(t, ok)
		action.Action(tcell.NewEventKey(key, 0, mod))
	}
	press(tcell.KeyHome, tcell.ModNone)
	require.Zero(t, v.cursor)
	press(tcell.KeyRight, tcell.ModShift)
	require.Equal(t, 10, v.cursor)
	press(tcell.KeyRight, tcell.ModNone)
	require.Equal(t, 11, v.cursor, "a Shift press does not make the next press larger")
	press(tcell.KeyLeft, tcell.ModShift)
	require.Equal(t, 1, v.cursor)
	press(tcell.KeyLeft, tcell.ModShift)
	require.Zero(t, v.cursor, "the cursor stops at the start of the window")
	press(tcell.KeyEnd, tcell.ModNone)
	require.Equal(t, v.frame.Width-1, v.cursor)
	require.Contains(t, drawnText(t, v, 132, 32), "cursor 08:19:02–08:20:00 UTC", "the last column of the strips")
	// The cursor keeps its time when the page changes under it: the lanes
	// of the all-detail page have columns of a whole minute.
	slowAllDetail(t, v)
	require.Contains(t, drawnText(t, v, 132, 32), "cursor 08:19–08:20 UTC")
	require.Equal(t, v.frame.Width-1, v.cursor)
	press(tcell.KeyHome, tcell.ModNone)
	press(tcell.KeyRight, tcell.ModShift)
	require.Equal(t, 10, v.cursor, "the keys are the same on both pages")
	for index, window := range graphWindows {
		action, ok := v.actions.Get(ui.Key1 + tcell.Key(index))
		require.True(t, ok)
		require.Equal(t, "Window "+graphSpan(window), action.Description, "the window keys are those of :graph and the dashboards")
	}
	require.Equal(t, graphDefaultWindow, slowpath.DefaultWindow)
}

func TestSlowIsRegisteredAsACommandAPaletteEntryAndARowKey(t *testing.T) {
	alerts := alertsTestView(t, true)
	alerts.acceptSnapshot(alertsTestSnapshot())
	count := 0
	for _, item := range actionCatalog(alerts, alerts.app) {
		if item.ID != "resource.slow" {
			continue
		}
		count++
		assert.Equal(t, "Slow requests: which hop", item.Label)
		assert.Equal(t, ":slow", item.Shortcut)
		assert.Empty(t, item.UnavailableReason, "the alerted Deployment has a request path to resolve")
		assert.NotNil(t, item.Handler)
	}
	assert.Equal(t, 1, count)

	undeclared := alertsTestView(t, false)
	undeclared.acceptSnapshot(alertsTestSnapshot())
	assert.Equal(t, "No Prometheus source is declared for this context; :sources shows what to add",
		slowAvailability(undeclared.app, actionTarget(undeclared, undeclared.app.Config.ActiveContextName())))
	node := SelectedResourceTarget{Context: "ct-1-1", GVR: client.NodeGVR, Name: "worker-a"}
	assert.Contains(t, slowAvailability(alerts.app, node), "Select a Service, an Ingress, a Deployment, a StatefulSet or a Pod first")

	for gvr, kind := range map[*client.GVR]string{client.SvcGVR: slowpath.KindService, client.IngGVR: slowpath.KindIngress, client.DpGVR: slowpath.KindDeployment,
		client.StsGVR: slowpath.KindStatefulSet, client.PodGVR: slowpath.KindPod} {
		selection, err := slowTarget(SelectedResourceTarget{Context: "ct-1-1", GVR: gvr, Namespace: "gitlab", Name: "web", UID: "uid-web"})
		require.NoError(t, err, kind)
		assert.Equal(t, slowpath.Selection{Context: "ct-1-1", Kind: kind, Namespace: "gitlab", Name: "web", UID: "uid-web"}, selection)
		assert.Contains(t, slowKinds, gvr.String(), "the list of this kind binds the row key")
	}
	key := slowKeyAction(alerts.app, alerts)
	assert.Equal(t, "Slow requests", key.Description)
	assert.Equal(t, "Shift-W", tcell.KeyNames[slowKey])

	// Both spellings are typed commands and are offered by the prompt.
	command := NewCommand(alerts.app)
	command.updateSuggestionAliases()
	assert.Contains(t, command.suggestionAliases(), "slow")
	assert.Contains(t, command.suggestionAliases(), "requests")
	for _, line := range []string{"slow", "requests"} {
		before := len(alerts.app.Content.Peek())
		assert.True(t, command.toolkitCmd(cmd.NewInterpreter(line)), line)
		assert.Len(t, alerts.app.Content.Peek(), before, "without a connection the command explains itself and opens nothing")
	}
}

func TestSlowDiscardsALateReplyAfterTheDestinationChanged(t *testing.T) {
	v := slowViewFixture(t)
	release := make(chan struct{})
	done := make(chan struct{})
	v.loader = func(ctx context.Context, _ *slowpath.Path, window time.Duration) slowRead {
		defer close(done)
		select {
		case <-release:
		case <-ctx.Done():
			return slowRead{err: ctx.Err()}
		}
		return *slowStoryRead(&slowtest.Story{}, window)
	}
	v.refresh(true)
	require.True(t, v.loading)
	// Leaving the view cancels the read; its reply is never applied.
	v.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the read was not canceled when the view stopped")
	}
	close(release)
	require.Nil(t, v.read)
	require.False(t, v.loading)

	// A view whose destination changed does not read again.
	other := slowViewFixture(t)
	other.revision++
	asked := false
	other.loader = func(context.Context, *slowpath.Path, time.Duration) slowRead {
		asked = true
		return slowRead{}
	}
	other.refresh(true)
	require.False(t, asked)
	require.Contains(t, drawnText(t, other, 100, 30), "Destination changed; this read is retained. Reopen :slow for the current context.")
}

func TestSlowAllDetailKeepsWholeSentencesAndTheFindingWhenRowsAreFew(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	slowAllDetail(t, v)
	// 60x24 with the Pods of the app hop listed leaves seven rows under the
	// table: two lanes, the axis, one line for the rest and the summary.
	slowPress(t, v, tcell.KeyEnter)
	frame := drawnText(t, v, 60, 20)
	lines := strings.Split(strings.TrimRight(frame, "\n "), "\n")
	require.Contains(t, frame, "    …-hx4mt")
	require.Contains(t, frame, "ingress: 499, p9… │", "the lane the episodes are counted from stays")
	require.Contains(t, frame, "p99 +3 …-hx4mt    │", "then the lane that overlaps every episode: it is what the summary speaks about")
	require.Contains(t, frame, "2 more lanes with marks do not fit")
	require.NotContains(t, frame, "not ready …-hx4mt │")
	summary := strings.TrimRight(lines[len(lines)-2], " ")
	require.True(t, strings.HasSuffix(summary, "."), "the summary ends with a whole sentence, not in the middle of one: %q", summary)
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "All 3 overlap slow p99, queueing, no free threads and CPU throttling on pod …-hx4mt.")

	// The line that names the rest gives way when exactly the marked lanes
	// fit without it; one lane is never left out for the sake of saying so.
	lanes, rest := slowPickLanes(v.report(), 4)
	require.Len(t, lanes, 4)
	require.Empty(t, rest)
	lanes, rest = slowPickLanes(v.report(), 2)
	require.Len(t, lanes, 1, "the lanes and the line that names the rest never take more rows than they are given")
	require.Equal(t, slowpath.HopIngress, lanes[0].ID)
	require.Contains(t, rest, "3 more lanes with marks do not fit")

	// The page of queries and rules wraps: no number and no rule is cut.
	slowPress(t, v, ui.KeyS)
	var detail []string
	for range 12 {
		drawn := strings.Split(drawnText(t, v, 60, 20), "\n")
		for i, line := range drawn {
			if strings.HasPrefix(line, "QUERIES") {
				require.Regexp(t, `^QUERIES( AND RULES)? · s returns · (lines )?\d+-\d+ of \d+ · PgDn PgUp *$`, line, "the paging keys are not cut off a narrow heading")
				detail = append(detail, drawn[i+1:len(drawn)-1]...)
			}
		}
		slowPress(t, v, tcell.KeyPgDn)
	}
	text := strings.Join(strings.Fields(strings.Join(detail, " ")), " ")
	require.Contains(t, text, "worst 48s at 07:28 (…-hx4mt) · usual 350ms · raised 9m of 1h in 3 stretches · 363 samples in 3 series, step 30s")
	require.Contains(t, text, "rule: raised at 3x its window median and at least 50ms above it; always raised at 5s")
	require.Contains(t, text, "rule: raised at 3x its window median and at least 25 points above it; always raised at 50%")
	require.Contains(t, text, "stretches: read from a rate over 2m; the time a burst takes to leave that window is not counted")
	for _, line := range detail {
		require.False(t, strings.HasSuffix(strings.TrimRight(line, " "), "…"), "cut: %q", line)
	}
}
