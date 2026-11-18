// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests draw the view in 132x32, 80x20 and 60x20: the areas it is given
// in terminals of 132x36, 80x24 and 60x24, where the application keeps two
// rows at the top and two at the bottom.

var slowDenied = observability.Status{State: observability.StateDenied, Reason: "HTTP 403", HTTPStatus: 403}

// slowCell is one cell of a drawn view.
type slowCell struct {
	r         rune
	fg, bg    string
	bold, dim bool
}

// slowCells draws a view on a simulated screen and returns its cells, with
// the colours as the tags of the page name them.
func slowCells(t *testing.T, view tview.Primitive, width, height int) [][]slowCell {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(width, height)
	view.SetRect(0, 0, width, height)
	view.Draw(screen)
	hex := func(c tcell.Color) string {
		if c == tcell.ColorDefault || c.Hex() < 0 {
			return ""
		}
		return fmt.Sprintf("#%06x", c.Hex())
	}
	out := make([][]slowCell, height)
	for y := range out {
		out[y] = make([]slowCell, width)
		for x := range out[y] {
			r, _, style, _ := screen.GetContent(x, y)
			fg, bg, attrs := style.Decompose()
			out[y][x] = slowCell{r: r, fg: hex(fg), bg: hex(bg), bold: attrs&tcell.AttrBold != 0, dim: attrs&tcell.AttrDim != 0}
		}
	}
	return out
}

// slowLineWith is the first line of a frame that holds the text, and its
// index.
func slowLineWith(t *testing.T, frame, text string) (line string, index int) {
	t.Helper()
	for i, candidate := range strings.Split(frame, "\n") {
		if strings.Contains(candidate, text) {
			return candidate, i
		}
	}
	require.Failf(t, "no such line", "%q in\n%s", text, frame)
	return "", -1
}

var slowGroupLabel = regexp.MustCompile(`^ {2}[A-Z][A-Z ,]{3,}`)

// slowRowWith is the first row of a calm page that holds the text, and its
// index in the frame. Rows stand under the first group label; the answer
// above it names the same Pods and signals in words.
func slowRowWith(t *testing.T, frame, text string) (row string, index int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		if slowGroupLabel.MatchString(line) {
			row, at := slowLineWith(t, strings.Join(lines[i:], "\n"), text)
			return row, i + at
		}
	}
	require.Failf(t, "no group", "in\n%s", frame)
	return "", -1
}

// slowColsOf are the columns of a line that hold one of the runes. No
// rune of a calm page is wider than one cell, so a column is a rune index.
func slowColsOf(line, runes string) []int {
	var out []int
	for col, r := range []rune(line) {
		if strings.ContainsRune(runes, r) {
			out = append(out, col)
		}
	}
	return out
}

// slowPinned compares a frame with its pinned text. The pinned pages are
// the acceptance text of the calm :slow: CALM_UPDATE=1 writes them again,
// which is done on purpose and read before it is committed.
func slowPinned(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "slow", name+".txt")
	got = strings.TrimRight(got, "\n") + "\n"
	if os.Getenv("CALM_UPDATE") != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got, name)
}

// slowQuietRead is a window in which nothing crossed its rule and the
// network profile matched no series.
func slowQuietRead() *slowRead {
	quiet := slowtest.Path()
	for i := range quiet.Pods {
		quiet.Pods[i].ReadySince = slowtest.Now.Add(-72 * time.Hour)
	}
	return &slowRead{path: quiet, report: slowtest.Report(&slowtest.Story{Quiet: true, Absent: []string{"hubble_"}}, slowtest.Sources(), quiet, time.Hour)}
}

// slowRanger answers every range read of a batch with one steady level per
// Pod, for a story that has no stall in it.
type slowRanger func(query string) map[string]float64

func (r slowRanger) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	res := observability.PromResult{Query: e.String(), Start: start, End: end, Step: observability.StepFor(end.Sub(start)),
		Status: observability.Status{Kind: observability.KindPrometheus, State: observability.StateComplete}}
	levels := r(res.Query)
	pods := make([]string, 0, len(levels))
	for pod := range levels {
		pods = append(pods, pod)
	}
	slices.Sort(pods)
	for _, pod := range pods {
		series := observability.PromSeries{Labels: map[string]string{"pod": pod}}
		for at := start; !at.After(end); at = at.Add(res.Step) {
			series.Samples = append(series.Samples, observability.Sample{Timestamp: at, Value: levels[pod]})
		}
		res.Series = append(res.Series, series)
	}
	return res
}

func TestSlowCalmPagesArePinnedAtTheSizesAViewIsGiven(t *testing.T) {
	for _, c := range []struct {
		name          string
		read          *slowRead
		width, height int
		ascii         bool
	}{
		{"hang", slowStoryRead(&slowtest.Story{}, time.Hour), 132, 32, false},
		{"hang", slowStoryRead(&slowtest.Story{}, time.Hour), 80, 20, false},
		{"hang", slowStoryRead(&slowtest.Story{}, time.Hour), 60, 20, true},
		{"quiet", slowQuietRead(), 132, 32, false},
		{"quiet", slowQuietRead(), 80, 20, false},
		{"denied", slowStoryRead(&slowtest.Story{Deny: slowDenied}, time.Hour), 132, 32, false},
		{"denied", slowStoryRead(&slowtest.Story{Deny: slowDenied}, time.Hour), 60, 20, true},
	} {
		name := fmt.Sprintf("%s-%dx%d", c.name, c.width, c.height)
		if c.ascii {
			name += "-ascii"
		}
		t.Run(name, func(t *testing.T) {
			v := slowViewFixture(t)
			v.app.Config.K9s.UI.NoIcons = c.ascii
			v.accept(c.read)
			frame := drawnText(t, v, c.width, c.height)
			slowPinned(t, name, frame)
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

// The answer is the first thing on the page, and everything under it is
// one row per lane on one time axis, grouped by how the lane stands to the
// stalls. What stands in a group is the overlap slowpath computed.
func TestSlowCalmPageStatesTheAnswerThenEveryLaneOnOneRowOfASharedAxis(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	report := v.report()

	frame := drawnText(t, v, 132, 32)
	lines := strings.Split(frame, "\n")
	require.Equal(t, "Slow requests  Service gitlab/gitlab-webservice-default · last 1h                                    read 08:20 · 35 of 35 queries", lines[0])
	require.Empty(t, lines[1])
	require.Equal(t, "  3 stalls: clients gave up waiting at 07:28, 07:47 and 08:13 UTC, for 2 to 4 minutes each.", lines[2], "the answer comes first")
	require.Equal(t, "  All 3 line up with slow p99, queueing, no free threads and CPU throttling on pod hx4mt.", lines[3])
	require.Regexp(t, `^  2 of 3 also line up with not Ready on pod hx4mt\. +Overlap in time, not proof of cause\.$`, lines[4],
		"the caveat is at the right of the last line of the answer")

	// The groups, in this order, each a label in capitals. Nothing was left
	// unread, so there is no such group.
	order := []string{"LINES UP WITH THE STALLS", "DIFFERS, BUT NOT DURING A STALL", "QUIET"}
	at := -1
	for _, label := range order {
		_, index := slowLineWith(t, frame, "  "+label)
		require.Greater(t, index, at, label)
		require.Empty(t, strings.TrimSpace(lines[index-1]), "a blank row before %s", label)
		at = index
	}
	require.NotContains(t, frame, "NOT READ")
	require.NotContains(t, frame, "not shown at this size", "everything is on a 132x36 terminal")

	// One row per lane: subject, what, the number, what it is held
	// against. A subject is written once for its rows.
	for _, row := range []string{
		`^  ingress    client give-ups \(HTTP 499\) \+2  6\.29%   of requests  [▁█]+$`,
		`^> pod hx4mt  slow p99                       48s     peers 350ms  [▁█]+$`,
		`^             queueing                       30      peers 0      [▁█]+$`,
		`^             no free threads                0       peers 5      [▁█]+$`,
		`^             CPU throttling                 86%     peers 1\.2%   [▁█]+$`,
		`^             not Ready                      twice   up to 3m     [▁█]+$`,
		`^  network    packet drops \(POLICY_DENIED\)   0\.37/s  to 2bq7k     [▁█]+$`,
		`^  pods 2bq7k, 8wzpd    p99 350ms +·+$`,
		`^  3 dependencies       no rule crossed +·+$`,
		`^  dns                  p99 6ms +·+$`,
		`^  restarts · rollouts  none in the last 1h +·+$`,
	} {
		require.Regexp(t, "(?m)"+row, frame)
	}
	require.Equal(t, 1, strings.Count(strings.Join(lines[5:], "\n"), "pod hx4mt"), "the subject is written once, its signals beneath")
	_, lined := slowLineWith(t, frame, "LINES UP WITH THE STALLS")
	_, differs := slowLineWith(t, frame, "DIFFERS, BUT NOT DURING A STALL")
	_, quiet := slowLineWith(t, frame, "  QUIET")
	place := func(text string) int {
		_, index := slowLineWith(t, strings.Join(lines[lined:], "\n"), text)
		return lined + index
	}
	for _, finding := range report.Findings() {
		// Which group a lane is in is the overlap, not a choice of the view.
		lane := finding.Lane
		switch finding.Relation {
		case slowpath.LaneSymptom, slowpath.LaneEvery, slowpath.LaneSome:
			index := place(lane.Phrase)
			require.True(t, index > lined && index < differs, "%s lines up", lane.ID)
		case slowpath.LaneOutside:
			index := place(lane.Phrase)
			require.True(t, index > differs && index < quiet, "%s is raised outside every stall", lane.ID)
		case slowpath.LaneQuiet:
			require.NotContains(t, strings.Join(lines[lined:quiet], "\n"), "  "+lane.Short+" ", "%s is quiet", lane.ID)
		}
	}

	// The stalls are marked on the marker row and drawn heavy on the axis;
	// every strip starts in one column, and a row that lines up is over
	// its rule inside those columns and nowhere else.
	marker, markerAt := slowLineWith(t, frame, "stalls  ")
	bands := slowColsOf(marker, "▼")
	require.Len(t, bands, 14, "three stalls of 4, 6 and 3 minutes on 63 columns")
	axis, axisAt := slowLineWith(t, frame, "┬──")
	require.Equal(t, bands, slowColsOf(axis, "━┯"), "the axis carries the bands without colour")
	require.Regexp(t, `^  UTC +07:20 +07:30 +07:40 +07:50 +08:00 +08:10 +08:20$`, lines[axisAt+1])
	start := slices.Index([]rune(axis), '┬')
	// rowWith is a row under the marker row: the answer above it names the
	// same signals in words.
	rowWith := func(text string) string {
		row, _ := slowLineWith(t, strings.Join(lines[markerAt:], "\n"), text)
		return row
	}
	for _, text := range []string{"client give-ups", "slow p99", "queueing", "no free threads", "CPU throttling", "not Ready", "packet drops", "3 dependencies", "dns  "} {
		row := rowWith(text)
		strip := slowColsOf(row, "▁█·")
		require.Equal(t, start, strip[0], "%s: every strip starts in the column of the axis", text)
		require.Equal(t, len([]rune(axis))-1, strip[len(strip)-1], "%s: and ends with it", text)
	}
	require.Equal(t, bands, slowColsOf(rowWith("client give-ups"), "█"), "the stalls are the stretches of the signal the row names")
	for _, text := range []string{"slow p99", "queueing", "no free threads", "CPU throttling"} {
		require.Equal(t, bands, slowColsOf(rowWith("  "+text+"  "), "█"), "%s is over its rule in the columns of the stalls", text)
	}
	marked := slowColsOf(rowWith("not Ready"), "█")
	require.NotEmpty(t, marked)
	for _, col := range marked {
		require.Contains(t, bands, col, "not Ready is inside a stall")
	}
	require.Less(t, marked[len(marked)-1], bands[len(bands)-3], "and in two of the three stalls only")
	for _, col := range slowColsOf(rowWith("packet drops"), "█") {
		require.NotContains(t, bands, col, "the burst of drops is between two stalls")
	}
	require.Less(t, markerAt, lined)
	require.Greater(t, axisAt, quiet)

	// Two lines of keys: the likely next steps in words, then the rest.
	require.Equal(t, "  next  B probe the pods now    m compare the pods    Enter open hx4mt    D open in Grafana", lines[axisAt+3])
	require.Equal(t, "  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · n next stall · t timeline · h Hubble · r re-read · Esc back",
		lines[axisAt+5])
	for _, line := range lines[axisAt+6:] {
		require.Empty(t, line)
	}

	// 80x24: the answer leaves the times to the marker row and counts the
	// signals its rows name, the comparisons go, the groups under the
	// stalls fold to one line each, and both key lines stay.
	frame = drawnText(t, v, 80, 20)
	lines = strings.Split(frame, "\n")
	require.Equal(t, "Slow requests  Service gitlab/gitlab-webservice-default · last 1h  08:20 · 35/35", lines[0])
	require.Equal(t, []string{
		"  3 stalls: clients gave up waiting, for 2 to 4 minutes each.",
		"  All 3 line up with 4 signals on pod hx4mt.",
		"  2 of 3 also line up with not Ready on pod hx4mt.",
		"  Overlap in time, not proof of cause.",
	}, lines[1:5])
	require.Regexp(t, `(?m)^ +stalls +▼▼▼ +▼▼▼ +▼▼$`, frame)
	for _, row := range []string{
		`^  ingress    client give-ups \(HTTP 499\) \+2  6\.29%  [▁█]+$`,
		`^> pod hx4mt  slow p99                       48s    [▁█]+$`,
		`^             not Ready                      twice  [▁█]+$`,
		`^  DIFFERS, BUT NOT DURING A STALL  network packet drops \(POLICY_DENIED\) 0\.37/s …$`,
		`^  QUIET  pods 2bq7k, 8wzpd · 3 dependencies · dns · restarts · rollouts$`,
		`^  next  B probe the pods now  m compare the pods  Enter open hx4mt$`,
		`^  ↑↓ row · ←→ time · 1-4 window · a show every signal · s queries · Esc back$`,
	} {
		require.Regexp(t, "(?m)"+row, frame)
	}
	require.NotContains(t, frame, "peers 350ms")
	require.NotContains(t, frame, "not shown at this size", "nothing is off the screen: folded is still named")

	// 60 columns: what is measured is shortened; number and strip stay,
	// and the key for all detail is still named.
	frame = drawnText(t, v, 60, 20)
	for _, row := range []string{
		`^Slow requests  Service gitlab/gitl… · last 1h  08:20 · 35/35$`,
		`^  ingress    client give-ups \(…  6\.29%  [▁█]{20}$`,
		`^> pod hx4mt  slow p99            48s    [▁█]{20}$`,
		`^             not Ready           twice  [▁█]{20}$`,
		`^  QUIET  pods 2bq7k, 8wzpd · 3 dependencies · dns \+1 more$`,
		`^  next  B probe the pods now  m compare the pods$`,
		`^  ↑↓ row · a show every signal · s queries · Esc back$`,
	} {
		require.Regexp(t, "(?m)"+row, frame)
	}

	// Under the size a task view needs, the view says so.
	require.Contains(t, drawnText(t, v, 40, 8), "View too small")

	// At the smallest size it is given, the strips go and the times stay
	// in the answer; the page says what it has no room for and which key
	// shows it.
	frame = drawnText(t, v, 40, 12)
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "3 stalls: clients gave up waiting at 07:28, 07:47 and 08:13 UTC, for 2 to 4 minutes each.")
	require.NotContains(t, frame, "▼")
	// 40 columns have no room for the long form of that line: the short
	// one names the rows left out before it is cut.
	require.Contains(t, frame, "  not shown (a shows all): 5 of 6 rows …\n")
}

// What a size gives up, in the order it gives it up. The strips shorten
// first, then the quiet groups fold, then the group that does not line up.
func TestSlowCalmPageSaysWhatEachSizeGivesUp(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	report, o := v.report(), calmOptions(v.app)
	answer := report.Answer()
	plan := func(width, height int) (strip int, dropped []string, under bool) {
		page, _ := v.calmPage(report, &answer, true, width, o)
		p, under := slowCalmPlan(&page, width, height, o)
		return p.Layout.Strip, p.Dropped, under
	}
	strip, dropped, under := plan(132, 32)
	assert.Equal(t, 65, strip)
	assert.Empty(t, dropped, "132x36 gives nothing up")
	assert.False(t, under)

	strip, dropped, under = plan(80, 20)
	assert.Equal(t, 29, strip, "the strips shorten first")
	assert.Equal(t, []string{
		"blank rows around the verdict and the keys", "QUIET folded to one line", "blank rows between subjects", "blank rows before groups",
		"DIFFERS, BUT NOT DURING A STALL folded to one line", "the blank rows under the verdict and over the keys", "what each number is compared with",
	}, dropped)
	assert.True(t, under, "the second key line is kept in place of the last blank rows")

	strip, dropped, _ = plan(60, 20)
	assert.Equal(t, 20, strip)
	assert.Contains(t, dropped, "what each number is compared with")
	assert.NotContains(t, strings.Join(dropped, "; "), "rows of", "every row that lines up is still on a 60-column screen")
}

// One colour per subject on the whole page, quiet things dim, and the
// bands of the stalls behind every row between the marker row and the
// axis. The colours are the semantic palette of the skin.
func TestSlowCalmPageHasOneColourPerSubjectAndBandsThroughEveryRow(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	p := calmPalette(v.app.Styles.Semantic())
	canvas := graphHex(v.app.Styles.Semantic().Canvas)
	frame := drawnText(t, v, 132, 32)
	cells := slowCells(t, v, 132, 32)
	marker, markerAt := slowLineWith(t, frame, "stalls  ")
	_, axisAt := slowLineWith(t, frame, "┬──")
	bands := slowColsOf(marker, "▼")

	// The band is behind the columns of a stall on every row from the
	// marker row down to the row above the axis: rows, blank rows and
	// group labels alike. It is nowhere else.
	for y := markerAt; y < axisAt; y++ {
		for x := 60; x < 132; x++ {
			want := canvas
			if slices.Contains(bands, x) {
				want = p.Band
			}
			require.Equal(t, want, cells[y][x].bg, "row %d, column %d", y, x)
		}
	}
	for _, y := range []int{0, 2, 3, 4, axisAt, axisAt + 1, axisAt + 3, axisAt + 5} {
		for x := range 132 {
			require.Equal(t, canvas, cells[y][x].bg, "row %d has no band", y)
		}
	}
	for _, col := range bands {
		require.Equal(t, p.Warn, cells[markerAt][col].fg, "the markers and the axis under them are the warning colour")
		require.Equal(t, p.Warn, cells[axisAt][col].fg)
	}

	// subject is the colour of a row's subject, number and of what is over
	// its rule in its strip.
	subject := func(text, value string) (name, number, over, under slowCell) {
		row, y := slowRowWith(t, frame, text)
		raised, quiet := slowColsOf(row, "█"), slowColsOf(row, "▁")
		before, _, found := strings.Cut(row, "  "+value+"  ")
		require.True(t, found, "%s in %q", value, row)
		number = cells[y][utf8.RuneCountInString(before)+2]
		return cells[y][2], number, cells[y][raised[0]], cells[y][quiet[0]]
	}
	name, number, over, under := subject("pod hx4mt", "48s")
	require.Equal(t, []string{p.Fail, p.Fail, p.Fail}, []string{name.fg, number.fg, over.fg}, "the Pod that differs from its peers is one colour")
	require.True(t, name.bold)
	require.True(t, under.dim, "what is under its rule is the dim baseline")
	require.Equal(t, p.Muted, under.fg)
	for text, value := range map[string]string{"queueing": "30", "no free threads": "0", "CPU throttling": "86%", "not Ready": "twice"} {
		_, number, over, _ = subject("  "+text+"  ", value)
		require.Equal(t, []string{p.Fail, p.Fail}, []string{number.fg, over.fg}, "%s is the same subject, so the same colour", text)
	}
	name, number, over, _ = subject("ingress  ", "6.29%")
	require.Equal(t, []string{p.Warn, p.Warn, p.Warn}, []string{name.fg, number.fg, over.fg}, "the ingress lines up, in its own colour")
	name, number, over, _ = subject("network  ", "0.37/s")
	require.Equal(t, []string{p.Info, p.Info, p.Info}, []string{name.fg, number.fg, over.fg}, "what is raised without lining up has a third")

	_, y := slowRowWith(t, frame, "3 dependencies")
	require.Equal(t, p.Muted, cells[y][2].fg, "a quiet thing is muted")
	require.True(t, cells[y][27].dim, "and what it measured is dim")
	require.True(t, cells[y][100].dim)
	_, y = slowLineWith(t, frame, "LINES UP WITH THE STALLS")
	require.Equal(t, p.Warn, cells[y][2].fg)
	require.True(t, cells[y][2].bold)
	require.Equal(t, p.Accent, cells[0][0].fg, "the view name is the accent")
	_, y = slowLineWith(t, frame, "> pod")
	require.Equal(t, p.Accent, cells[y][0].fg, "and so is the row cursor")
	require.Equal(t, canvas, cells[y][1].bg, "the selected row has no background of its own: a band is the only shading")

	// Without colour the page reads the same: the marker row and the axis
	// carry the stalls.
	v.app.Config.K9s.UI.NoIcons = true
	v.render()
	plain := drawnText(t, v, 132, 32)
	marker, _ = slowLineWith(t, plain, "stalls  ")
	axis, _ := slowLineWith(t, plain, "+---")
	require.Equal(t, slowColsOf(marker, "v"), slowColsOf(axis, "=#"))
	row, _ := slowRowWith(t, plain, "client give-ups")
	require.Equal(t, slowColsOf(marker, "v"), slowColsOf(row, "#"))
}

// The row cursor and the time cursor move over a page that stays where it
// is: no row and no column moves, and nothing is read again.
func TestSlowCalmCursorsMoveWithoutTheLayoutShifting(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	still := drawnText(t, v, 132, 32)
	require.Equal(t, slowRow{hop: slowpath.HopApp, pod: slowtest.Outlier}, v.focus, "the page opens on the strongest finding: the Pod that differs")
	require.Equal(t, "app:p99", v.calmKey)

	// The rows, top to bottom. Only the marker and what Enter opens change.
	unmarked := regexp.MustCompile(`(?m)^>`)
	enter := regexp.MustCompile(`Enter (open hx4mt|its queries)  +`)
	same := func(frame string) string {
		return enter.ReplaceAllString(unmarked.ReplaceAllString(frame, " "), "Enter ")
	}
	var keys []string
	for range 12 {
		keys = append(keys, v.calmKey)
		slowPress(t, v, tcell.KeyDown)
		require.Equal(t, same(still), same(drawnText(t, v, 132, 32)), "after %s", v.calmKey)
	}
	require.Equal(t, []string{"app:p99", "app:queued", "app:free-threads", "app:throttled", "service", "network", "app:peers", "dependencies", "dns", "facts",
		"facts", "facts"}, keys, "down the page and no further")
	require.Contains(t, drawnText(t, v, 132, 32), "> restarts · rollouts")
	require.Contains(t, drawnText(t, v, 132, 32), "Enter its queries")
	for range 12 {
		slowPress(t, v, tcell.KeyUp)
	}
	require.Equal(t, "ingress", v.calmKey)
	require.Equal(t, slowRow{hop: slowpath.HopIngress}, v.focus)
	slowPress(t, v, tcell.KeyDown)
	require.Equal(t, still, drawnText(t, v, 132, 32))

	// What the shared actions act on follows the row.
	require.Equal(t, client.PodGVR, v.SelectedResource().GVR)
	require.Equal(t, slowtest.Outlier, v.SelectedResource().Name)
	slowPress(t, v, tcell.KeyUp)
	require.Equal(t, "apps/v1/deployments", v.SelectedResource().GVR.String(), "a row of a hop hands the workload to graph, timeline and Grafana")
	require.Equal(t, client.SvcGVR, v.serviceTarget().GVR, "a probe is asked for the Service")
	slowPress(t, v, tcell.KeyDown)

	// The time cursor: n lands on the first sample of a stall. The title
	// states its time and the value of the selected row there, each row
	// states its own where the number was held against something, and Esc
	// puts it away.
	slowPress(t, v, ui.KeyN)
	frame := drawnText(t, v, 132, 32)
	lines, before := strings.Split(frame, "\n"), strings.Split(still, "\n")
	require.Regexp(t, `^Slow requests  Service gitlab/gitlab-webservice-default · last 1h +cursor 07:27:23–07:28:18 UTC · slow p99 48s$`, lines[0])
	require.Len(t, lines, len(before))
	axis, axisAt := slowLineWith(t, frame, "┬──")
	cursor := slowColsOf(axis, "^")
	require.Len(t, cursor, 1)
	marker, _ := slowLineWith(t, frame, "stalls  ")
	require.Equal(t, slowColsOf(marker, "▼")[0], cursor[0], "the column in which the first raised sample ends, the first of the band")
	left, _, found := strings.Cut(before[9], "of requests")
	require.True(t, found)
	held := utf8.RuneCountInString(left)
	for y := 1; y < len(lines); y++ {
		got, want := []rune(lines[y]), []rune(before[y])
		if y == axisAt+5 {
			require.Equal(t, strings.Replace(before[y], "r re-read · Esc back", "Esc hides the cursor", 1), lines[y])
			continue
		}
		for len(got) < len(want) {
			got = append(got, ' ')
		}
		for len(want) < len(got) {
			want = append(want, ' ')
		}
		for x := range got {
			switch {
			case x >= held && x < held+len("peers 350ms") && y > 8 && y < 19:
				// The column of what a number is held against reads the cursor.
			case x == cursor[0] && (got[x] == '┆' || got[x] == '^'):
				// The cursor runs through blank cells and stands on the axis.
			default:
				require.Equal(t, string(want[x]), string(got[x]), "row %d, column %d: nothing else moves\n%s", y, x, frame)
			}
		}
	}
	for _, row := range []string{
		`^  ingress    client give-ups \(HTTP 499\) \+2  6\.29%   ┆ 0\.68/s     ▁`,
		`^> pod hx4mt  slow p99                       48s     ┆ 48s        ▁`,
		`^             no free threads                0       ┆ 0          ▁`,
		`^             not Ready                      twice   ┆ 0          ▁`,
		`^  network    packet drops \(POLICY_DENIED\)   0\.37/s  ┆ 0/s        ▁`,
	} {
		require.Regexp(t, "(?m)"+row, frame)
	}
	slowPress(t, v, ui.KeyN)
	require.Contains(t, drawnText(t, v, 132, 32), "cursor 07:46:46–07:47:41 UTC")
	slowPress(t, v, ui.KeyShiftN)
	slowPress(t, v, tcell.KeyLeft)
	require.Contains(t, drawnText(t, v, 132, 32), "cursor 07:26:27–07:27:23 UTC")
	require.Contains(t, drawnText(t, v, 80, 20), "cursor 07:26:12–07:28:16 UTC · slow p99 48s", "narrower strips have wider columns; the cursor keeps its time")
	slowPress(t, v, tcell.KeyEscape)
	require.Equal(t, still, drawnText(t, v, 132, 32), "Esc first puts the cursor away")
	require.Same(t, v, v.app.Content.Top())
	require.NotNil(t, v.report())
}

// s shows the queries, the samples and the rules behind the selected row,
// with the row's own signal first; Enter on a row of a Pod opens that
// Pod's graph, and on any other row its queries.
func TestSlowCalmRowsShowTheirQueriesAndOpenThePod(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	drawnText(t, v, 132, 32)
	for range 3 {
		slowPress(t, v, tcell.KeyDown)
	}
	require.Equal(t, "app:throttled", v.calmKey)
	slowPress(t, v, ui.KeyS)
	frame := drawnText(t, v, 132, 32)
	lines := strings.Split(frame, "\n")
	require.Equal(t, "Slow requests  Service gitlab/gitlab-webservice-default · last 1h                                    read 08:20 · 35 of 35 queries", lines[0])
	require.Regexp(t, `^  QUERIES, SAMPLES AND RULES · s or Enter returns · lines 1-27 of \d+ · PgDn PgUp$`, lines[2])
	require.Equal(t, "  service → pods · 3 pods · profiles gitlab-webservice, container-cpu · outlier", lines[3])
	require.Equal(t, "  throttled [container-cpu] share of CPU periods in which a container was throttled at its limit", lines[4], "the signal of the row is stated first")
	require.Equal(t, "    worst 86% at 07:30 (hx4mt) · usual 4% · raised 9m of 1h in 3 stretches · 363 samples in 3 series, step 30s", lines[5])
	require.Equal(t, "    rule: raised at 3x its window median and at least 25 points above it; always raised at 50%", lines[6])
	require.Contains(t, frame, `    query: (sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{namespace="gitlab",pod=~"gitlab-webservice-default-6f9c7d`)
	require.Contains(t, frame, "  p99 [gitlab-webservice] Rails request duration, 99th percentile", "then the other signals of the hop")
	require.Equal(t, "  ↑↓ row · PgDn PgUp page · s or Enter back to the page · Esc back", lines[31])
	require.NotContains(t, frame, "LINES UP")
	slowPress(t, v, tcell.KeyPgDn)
	require.NotContains(t, drawnText(t, v, 132, 32), "lines 1-")

	// The queries follow the row cursor, and wrap at every width.
	slowPress(t, v, tcell.KeyDown)
	frame = drawnText(t, v, 132, 32)
	require.Contains(t, frame, "  ingress → service · 3 endpoints · elevated")
	require.Contains(t, frame, `    query: min by (pod) (kube_pod_status_ready{namespace="gitlab",pod=~"`)
	narrow := drawnText(t, v, 60, 20)
	require.Regexp(t, `(?m)^  QUERIES( AND RULES)? · s returns · (lines )?1-\d+ of \d+ · PgDn PgUp$`, narrow, "the paging keys are not cut off a narrow heading")
	for _, line := range strings.Split(narrow, "\n")[1:] {
		require.LessOrEqual(t, runewidth.StringWidth(line), 60, line)
		require.False(t, strings.HasSuffix(line, "…"), "nothing on the page of queries is cut: %q", line)
	}

	// Esc closes the queries before it leaves; Enter returns too.
	slowPress(t, v, tcell.KeyEscape)
	require.Same(t, v, v.app.Content.Top())
	require.Contains(t, drawnText(t, v, 132, 32), ">            not Ready")
	slowPress(t, v, ui.KeyS)
	slowPress(t, v, tcell.KeyEnter)
	require.False(t, v.detail)

	// The dated facts have a page too, and a quiet one says so.
	for range 6 {
		slowPress(t, v, tcell.KeyDown)
	}
	require.Equal(t, "facts", v.calmKey)
	slowPress(t, v, tcell.KeyEnter)
	frame = drawnText(t, v, 132, 32)
	require.Contains(t, frame, "  restarts · the restart counter when it is read, and the last restart the Kubernetes API reports per Pod")
	require.Contains(t, frame, "    none in the last 1h")
	require.Contains(t, frame, "  rollouts, new pods · retained revisions of the workload and Pods created in the window, from the Kubernetes API")
	slowPress(t, v, ui.KeyS)

	// Enter on a row of a Pod opens that Pod's own charts for the window
	// that was read, whichever of its signals the row is.
	for range 5 {
		slowPress(t, v, tcell.KeyUp)
	}
	require.Equal(t, slowtest.Outlier, v.focus.pod)
	require.Equal(t, slowpath.HopService, v.focus.hop, "the row of its Ready condition")
	slowPress(t, v, tcell.KeyEnter)
	graph, ok := v.app.Content.Top().(*graphView)
	require.True(t, ok)
	t.Cleanup(graph.Stop)
	require.False(t, graph.comparing)
	require.Equal(t, "Pod", graph.target.Kind)
	require.Equal(t, slowtest.Outlier, graph.target.Name)
	require.Equal(t, time.Hour, graph.window)
	v.app.Content.Pop()
}

// a shows all the detail the view had before the calm page, and a again
// returns to it. The row the cursor stood on is found on either page.
func TestSlowAllDetailKeyTogglesTheHopTableAndTheLanes(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	calmPage := drawnText(t, v, 132, 32)
	action, ok := v.actions.Get(calmDetailKey)
	require.True(t, ok)
	require.Equal(t, calmDetailLabel, action.Description)
	require.Contains(t, calmPage, "a show every signal")

	slowPress(t, v, calmDetailKey)
	frame := drawnText(t, v, 132, 32)
	require.Contains(t, frame, "  HOP                 WHAT                      LAST 1h        WORST          STATE    EVIDENCE")
	require.Contains(t, frame, "WHEN IT HAPPENED · 1 column = 1m · █ over its rule · × no data · UTC")
	require.Contains(t, frame, "WHAT LINES UP")
	require.Contains(t, frame, ">   hx4mt", "the Pod the cursor stood on keeps it where its row is listed")
	require.Contains(t, frame, "↑↓ hop · Enter open · a less detail · ←→ cursor · n/N episode · 1-4 window · s queries · r re-read · b dots · Esc back")
	require.NotContains(t, frame, "LINES UP WITH THE STALLS")

	// Back: the calm page as it was. From a hop of the table, the row of
	// that hop.
	slowPress(t, v, calmDetailKey)
	require.Equal(t, calmPage, drawnText(t, v, 132, 32))
	slowPress(t, v, calmDetailKey)
	drawnText(t, v, 132, 32)
	for range 8 {
		slowPress(t, v, tcell.KeyDown)
	}
	require.Equal(t, slowRow{hop: slowpath.HopNetwork}, v.focus)
	slowPress(t, v, calmDetailKey)
	require.Contains(t, drawnText(t, v, 132, 32), "> network    packet drops (POLICY_DENIED)")
	require.Equal(t, "network", v.calmKey)

	// The key reads nothing.
	var asked bool
	v.loader = func(context.Context, *slowpath.Path, time.Duration) slowRead { asked = true; return slowRead{} }
	slowPress(t, v, calmDetailKey)
	slowPress(t, v, calmDetailKey)
	require.False(t, asked, "a layout change adds no request")
}

// What was not read is never quiet: it is listed with the reason, and the
// answer says exactly what the read came to.
func TestSlowCalmPageSaysWhatWasNotReadAndWhy(t *testing.T) {
	// Before the first read arrives.
	v := slowViewFixture(t)
	frame := drawnText(t, v, 100, 30)
	lines := strings.Split(frame, "\n")
	require.Regexp(t, `^Slow requests  Service gitlab/gitlab-webservice-default +not read$`, lines[0])
	require.Equal(t, "  Reading the configured path, then one bounded batch from the declared Prometheus.", lines[2])
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "Every hop is then read once from the declared Prometheus. Bounded, read-only, and only because this view was opened.")
	v.loading = true
	v.render()
	require.Regexp(t, `(?m)^Slow requests  Service gitlab/gitlab-webservice-default +reading…$`, drawnText(t, v, 100, 30))
	v.loading = false

	// Prometheus refused: the answer says so, every hop is listed as not
	// read with the reason, and what the Kubernetes API still knows is a
	// dated fact in the window. Nothing is called quiet.
	v.accept(slowStoryRead(&slowtest.Story{Deny: slowDenied}, time.Hour))
	frame = drawnText(t, v, 132, 32)
	lines = strings.Split(frame, "\n")
	require.Regexp(t, ` 0 of 35 queries$`, lines[0])
	require.Regexp(t, `^  Prometheus was not read \(denied: HTTP 403\), so no hop has latency evidence\. +Not read is not healthy\.$`, lines[2])
	_, unread := slowLineWith(t, frame, "  NOT READ")
	for offset, hop := range []string{"ingress", "pods", "3 dependencies", "dns", "network"} {
		require.Regexp(t, `^  `+hop+` +prometheus: denied \(HTTP 403\) +×+$`, lines[unread+1+offset], "every hop that was not read says so, with the reason")
	}
	require.Regexp(t, `^  endpoints  1 of 1 signals: prometheus: denied \(HTTP 40…  ×+$`, lines[unread+6], "the history of the endpoints was not read either")
	require.Regexp(t, `(?m)^  IN THE WINDOW\n> endpoints  hx4mt Ready=True  once  at 07:51 +×+▲·+$`, frame,
		"the one Ready change the API still knows is drawn where it happened; the time before it is not known")
	require.NotContains(t, frame, "▼")
	for _, hop := range []string{"ingress", "pods", "3 dependencies", "dns", "network"} {
		require.NotRegexp(t, `(?m)^[> ] `+hop+` .*·{3}`, frame, "%s was not read: its strip is never the quiet one", hop)
	}

	// A quiet window says so for what was read, how much was read, and
	// lists what was not. The row cursor stays on the lane it stood on.
	v.accept(slowQuietRead())
	frame = drawnText(t, v, 132, 32)
	lines = strings.Split(frame, "\n")
	require.Regexp(t, `^  No signal that was read crossed its rule in the last 1h\. All 35 queries answered\. +Nothing found is not proof of health\.$`, lines[2])
	for _, row := range []string{
		`^  QUIET$`, `^  ingress +p99 440ms +·+$`, `^> endpoints +3 of 3 ready +·+$`, `^  pods +p99 350ms +·+$`, `^  dns +p99 6ms +·+$`,
		`^  NOT READ$`, `^  network  no series matched hubble_drop_total with dest…  ×+$`,
		`^  dns  1 of 5 signals: no series matched hubble_dns_resp…  ×+$`,
	} {
		require.Regexp(t, "(?m)"+row, frame)
	}
	require.NotContains(t, frame, "LINES UP")
	require.NotContains(t, frame, "█")
	require.NotContains(t, frame, "n next", "there is no episode to step to")
	folded := drawnText(t, v, 80, 12)
	require.Contains(t, folded, "  NOT READ  network · dns (1 of 5 signals)", "folded, a hop that was read in part is not listed as not read")

	// s on a row that was not read states the reason in full.
	drawnText(t, v, 132, 32)
	for range 5 {
		slowPress(t, v, tcell.KeyDown)
	}
	require.Equal(t, "network", v.calmKey)
	slowPress(t, v, ui.KeyS)
	require.Contains(t, strings.Join(strings.Fields(drawnText(t, v, 132, 32)), " "),
		"not read: no series matched hubble_drop_total with destination_namespace=\"gitlab\"")
	slowPress(t, v, ui.KeyS)

	// A selection without a request path explains itself.
	fresh := slowViewFixture(t)
	fresh.accept(&slowRead{err: errors.New("no Service in namespace gitlab selects the Pods of StatefulSet gitlab/batch, so there is no request path to read")})
	frame = drawnText(t, fresh, 100, 30)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, "No request path: no Service in namespace gitlab selects the Pods of StatefulSet gitlab/batch, so there is no request path to read.")
	require.Contains(t, flat, "Nothing has been read; r reads again, and :sources shows what is declared for this context.")
	require.Contains(t, flat, "Not read is not healthy.")

	// A re-read that fails keeps the earlier read on the screen and says
	// so under the title, where it is read before the answer.
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	whole := drawnText(t, v, 132, 32)
	v.accept(&slowRead{err: errors.New("Service gitlab/gitlab-webservice-default: the read timed out")})
	frame = drawnText(t, v, 132, 32)
	lines = strings.Split(frame, "\n")
	require.Equal(t, "  Re-read failed, the earlier read is shown: Service gitlab/gitlab-webservice-default: the read timed out", lines[1])
	require.Equal(t, strings.Split(whole, "\n")[2], lines[3], "the answer of the earlier read")
	require.Contains(t, frame, "  pod hx4mt  slow p99", "the evidence of the earlier read")
}

// Without an episode a Pod that differs from its peers all window is still
// the finding: it is the answer, and a group of its own.
func TestSlowCalmPageShowsAPodThatDiffersWithoutAnEpisode(t *testing.T) {
	path := slowtest.Path()
	path.Ingresses = nil
	plan := slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	steady := slowRanger(func(query string) map[string]float64 {
		if strings.Contains(query, "http_request_duration_seconds_bucket") && strings.Contains(query, "0.99") {
			return map[string]float64{slowtest.Calm: 0.3, slowtest.Third: 0.31, slowtest.Outlier: 2}
		}
		return nil
	})
	v := slowViewFixture(t)
	v.accept(&slowRead{path: path, report: slowpath.Evaluate(plan, slowpath.Collect(t.Context(), steady, plan), nil, slowtest.Now)})
	frame := drawnText(t, v, 132, 32)
	lines := strings.Split(frame, "\n")
	require.Equal(t, "  Pod hx4mt usually differs from its peers: p99 2s (peers 305ms). No signal that was read crossed its rule in the last 1h.", lines[2])
	require.Equal(t, "  A difference, not proof of cause.", lines[3])
	require.Regexp(t, `(?m)^  DIFFERS FROM ITS PEERS\n> pod hx4mt  slow p99, usually +2s +peers 305ms +▁+$`, frame, "it never crossed its rule: the strip is the quiet baseline")
	require.Regexp(t, `(?m)^  pods 2bq7k, 8wzpd +p99 up to 310ms +·+$`, frame)
	require.Regexp(t, `(?m)^  ingress  no Ingress in namespace gitlab routes to Serv…  ×+$`, frame)
	require.NotContains(t, frame, "▼")
	cells := slowCells(t, v, 132, 32)
	_, y := slowLineWith(t, frame, "> pod hx4mt")
	require.Equal(t, calmPalette(v.app.Styles.Semantic()).Fail, cells[y][2].fg)
}

// Dated facts are rows like any other: a rollout that falls into a stall
// lines up with it, a restart between two stalls does not.
func TestSlowCalmPagePlacesDatedFactsByOverlapToo(t *testing.T) {
	path := slowtest.Path()
	path.Rollouts = append(path.Rollouts, slowpath.Event{At: slowtest.Now.Add(-34 * time.Minute), Label: "rev 2"},
		slowpath.Event{At: slowtest.Now.Add(-390 * time.Second), Label: "rev 3"})
	path.Pods[0].Restarts, path.Pods[0].LastRestart = 3, slowtest.Now.Add(-20*time.Minute)
	plan := slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	alerts := &slowpath.Alerts{Read: true, Firing: []slowpath.AlertSpan{{Name: "GitLabSlow", Start: slowtest.Now.Add(-6 * time.Minute), End: slowtest.Now.Add(time.Hour)}}}
	v := slowViewFixture(t)
	v.accept(&slowRead{path: path, report: slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), alerts, slowtest.Now)})
	frame := drawnText(t, v, 132, 36)
	_, lined := slowLineWith(t, frame, "LINES UP WITH THE STALLS")
	_, differs := slowLineWith(t, frame, "DIFFERS, BUT NOT DURING A STALL")
	rollouts, at := slowLineWith(t, frame, "  rollouts ")
	require.True(t, at > lined && at < differs)
	require.Regexp(t, `^  rollouts +a rollout or new Pod +twice +from 07:46 +[·▲]+$`, rollouts)
	require.Len(t, slowColsOf(rollouts, "▲"), 2, "a moment is an event mark")
	alert, at := slowLineWith(t, frame, "  alerts ")
	require.True(t, at > lined && at < differs)
	require.Regexp(t, `^  alerts +GitLabSlow +6m +at 08:14 +[▁█]+$`, alert, "a firing alert is a stretch")
	restart, at := slowLineWith(t, frame, "  pod 2bq7k ")
	require.Greater(t, at, differs)
	require.Regexp(t, `^  pod 2bq7k +restarts +once +at 08:00 +[×·▲]+$`, restart)
	marker, _ := slowLineWith(t, frame, "stalls  ")
	for _, col := range slowColsOf(restart, "▲") {
		require.NotContains(t, slowColsOf(marker, "▼"), col)
	}
	require.Contains(t, frame, "1 of 3 also lines up with a rollout or new Pod and a firing alert.")
}

// The title names the Service that is read. The namespace is written when
// it is not the one the session is in, and the selection when the path was
// resolved from something other than the Service.
func TestSlowCalmTitleNamesTheServiceTheWindowAndWhatWasRead(t *testing.T) {
	v := slowViewFixture(t)
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	require.Regexp(t, `^Slow requests  Service gitlab/gitlab-webservice-default · last 1h +read 08:20 · 35 of 35 queries\n`, drawnText(t, v, 132, 32))
	require.NoError(t, v.app.Config.SetActiveNamespace(slowtest.Namespace))
	v.render()
	require.Regexp(t, `^Slow requests  Service gitlab-webservice-default · last 1h +read 08:20 · 35 of 35 queries\n`, drawnText(t, v, 132, 32))

	v.selection.Kind, v.selection.Name = slowpath.KindDeployment, "gitlab-webservice-default"
	v.render()
	require.Regexp(t, `^Slow requests  Service gitlab-webservice-default \(from Deployment gitlab-webservice-default\) · last 1h +08:20 · 35/35\n`,
		drawnText(t, v, 132, 32))
	require.Regexp(t, `^Slow requests  Service gitlab-webservice-default \(from… · last 1h  08:20 · 35/35\n`, drawnText(t, v, 80, 20),
		"a narrow title shortens the status, then the name")

	// A wide window says what it cannot show, in the answer, and a hop
	// that matched no series is listed as not read.
	path := slowtest.Path()
	v.accept(&slowRead{path: path, report: slowtest.Report(&slowtest.Story{Absent: []string{"hubble_"}}, slowtest.Sources(), path, 6*time.Hour)})
	frame := drawnText(t, v, 132, 32)
	require.Regexp(t, `· last 6h +08:20 · 35/35\n`, frame)
	require.Contains(t, strings.Join(strings.Fields(frame), " "),
		"A sample here stands for 5m and a rate for 20m: a stall of a few minutes merges with the next one or disappears; 1h or 15m shows it.")
	require.Regexp(t, `(?m)^  NOT READ\n  network +no series matched hubble_drop_total`, frame)
}

// The windows and the re-read are the keys of every incident view. A
// window key sends the batch again and keeps the resolved path. While the
// read runs the page stays as it is and the title says so.
func TestSlowCalmWindowKeysReadAgainAndKeepThePage(t *testing.T) {
	v := slowViewFixture(t)
	var reads, resolved atomic.Int32
	release := make(chan struct{})
	v.loader = func(_ context.Context, path *slowpath.Path, window time.Duration) slowRead {
		reads.Add(1)
		if path == nil {
			resolved.Add(1)
			path = slowtest.Path()
		}
		<-release
		return slowRead{path: path, report: slowtest.Report(&slowtest.Story{}, slowtest.Sources(), path, window)}
	}
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	still := drawnText(t, v, 132, 32)
	slowPress(t, v, ui.Key3)
	require.Equal(t, 6*time.Hour, v.window)
	require.True(t, v.loading)
	frame := drawnText(t, v, 132, 32)
	require.Regexp(t, `^Slow requests  Service gitlab/gitlab-webservice-default · last 1h +reading again…\n`, frame)
	require.Equal(t, strings.Split(still, "\n")[1:], strings.Split(frame, "\n")[1:], "what is shown is retained until the new read arrives")
	close(release)
	require.Eventually(t, func() bool { return reads.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Zero(t, resolved.Load(), "a window change does not read the path again")
	for _, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key4, ui.KeyR, ui.KeyH, ui.KeyT, ui.KeyM, probeKey, grafanaKey, chartStyleKey} {
		_, ok := v.actions.Get(key)
		require.True(t, ok, "%s is bound on the calm page as it was before", tcell.KeyNames[key])
	}
}
