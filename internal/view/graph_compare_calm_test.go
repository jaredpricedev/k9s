// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

// calmColumns are the cell columns of a line that hold one of the runes.
func calmColumns(line, runes string) []int {
	var out []int
	for col, r := range []rune(line) {
		if strings.ContainsRune(runes, r) {
			out = append(out, col)
		}
	}
	return out
}

const calmLevels = "▂▃▄▅▆▇█"

// calmMarker finds the marker row of the hang story: the label of its
// bands, which the verdict does not end on.
const calmMarker = "hx4mt differs  "

func TestGraphCompareCalmPageNamesThePodThatDiffersAndShowsWhatLinesUp(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	pg := calmGraphPage(132, 33)
	tagged := renderGraphCompareCalm(base, c, pg)
	page := graphPlainLines(t, tagged, 132)
	lines := calmLines(page)
	require.LessOrEqual(t, len(lines), 33)
	require.Empty(t, pg.Dropped, "132x36 gives nothing up")

	// The title and the answer: which Pod, in how many of the signals that
	// were read, and that the rest had no reading.
	require.Regexp(t, `^Compare pods  Deployment gitlab/web · 4 pods · last 1h +read 05:00 · 13 of 13 queries$`, lines[0])
	require.Equal(t, "  Pod hx4mt differs from the other 3 in 4 of 7 signals. No other Pod differs. 3 of 10 signals had no reading.", lines[2])
	require.Equal(t, "  A difference, not proof of cause.", lines[3])

	// One group per signal in which a Pod differs, the one in which it is
	// furthest from its peers first; a signal that has no room is named in
	// one line.
	queued := strings.Index(page, "\n  QUEUED · connections waiting for a free Puma thread")
	p99 := strings.Index(page, "\n  P99 · Rails request duration, 99th percentile")
	require.True(t, queued > 0 && queued < p99, "queued (14 against 0) before p99 (48s against 350ms)")
	require.Equal(t, 8, strings.Count(page, "worker-"), "two groups of four Pods have room with their blank rows; a third has not")
	require.Contains(t, page, "\n  ALSO DIFFERS  throttled (hx4mt 86%, peers 1.4%) · cpu (hx4mt 1.47, peers 370m)\n")
	require.Contains(t, page, "\n  ALIKE ON ALL 4  memory · restarts · network\n")
	require.Contains(t, page, "\n  NOT READ  5xx (no data) · free-threads (no data) · busy-threads (no data)\n")
	require.Regexp(t, `QUEUED · connections waiting for a free Puma thread +peak$`, calmLine(t, page, "  QUEUED"))

	// Each group: the Pod that differs first, with the word; its peers by
	// name, each the quiet baseline. One number per row: what ranks it.
	group := lines[indexOf(lines, "  QUEUED"):]
	require.Regexp(t, `^> hx4mt  worker-f  [▁-█]+  14 +differs$`, group[1])
	require.Regexp(t, `^  2bq7k  worker-d  ▁+  0$`, group[2])
	require.Regexp(t, `^  8wzpd  worker-e  ▁+  0$`, group[3])
	require.Regexp(t, `^  r5n9j  worker-d  ▁+  0$`, group[4])
	require.Regexp(t, `^  hx4mt  worker-f  [▁-█]+  48s +differs$`, lines[indexOf(lines, "  P99")+1])

	// The bands are the columns in which the marked row is apart from the
	// others. They are on a marker row and heavy on the axis, so colour is
	// not what carries them; and the rows of the other signals stand in
	// the same columns: that is what "lines up" looks like.
	marker := calmLine(t, page, calmMarker)
	band := calmColumns(marker, "▼")
	require.Len(t, band, 7, "eight samples of thirty seconds on a strip of 92 columns for an hour")
	require.Equal(t, band, calmColumns(group[1], calmLevels))
	require.Equal(t, band, calmColumns(lines[indexOf(lines, "  P99")+1], calmLevels))
	axis := calmLine(t, page, "┬")
	require.Equal(t, band, calmColumns(axis, "━┯"))
	require.Regexp(t, regexp.QuoteMeta("["+pg.Palette.Fail+":"+pg.Palette.Band+":-]")+`█+`, strings.Join(tagged, "\n"), "the Pod's colour on the band's background")
	require.Contains(t, strings.Join(tagged, "\n"), "["+pg.Palette.Fail+":-:b]hx4mt", "one colour for the Pod that differs")

	// The keys: Enter opens the marked Pod, and the other steps in words.
	require.Equal(t, hangPods[2], pg.Open)
	require.Equal(t, "gitlab-webservice/queued", pg.Signal)
	require.Equal(t, "  next  Enter open hx4mt    g group by container / revision / node / zone    B probe the pods now", lines[len(lines)-3])
	require.Equal(t, "  ↑↓ signal · ←→ time · 1-4 window · a show every signal · p overlaid lines · r re-read · s queries · Esc back", lines[len(lines)-1])

	// Colours come from the semantic palette only.
	allowed := map[string]bool{}
	for _, color := range []string{pg.Palette.Text, pg.Palette.Muted, pg.Palette.Accent, pg.Palette.Warn, pg.Palette.Fail, pg.Palette.Info, pg.Palette.Mark, pg.Palette.Band} {
		allowed[color] = true
	}
	for _, color := range regexp.MustCompile(`#[0-9a-fA-F]{6}`).FindAllString(strings.Join(tagged, "\n"), -1) {
		require.True(t, allowed[color], "%s is not a colour of the semantic palette", color)
	}

	// Without colour and without Unicode the page says the same.
	plain := calmGraphPage(132, 33)
	plain.ASCII, plain.Palette = true, calm.Palette{}
	ascii := graphPlainLines(t, renderGraphCompareCalm(base, c, plain), 132)
	for _, r := range ascii {
		require.Less(t, r, rune(128), "%q is not ASCII", r)
	}
	require.Equal(t, 7, strings.Count(calmLine(t, ascii, calmMarker), "v"))
	require.Regexp(t, `^> hx4mt  worker-f  _+#+_+  14 +differs$`, calmLine(t, ascii, "> hx4mt"))
	require.Contains(t, calmLine(t, ascii, "+-"), "=")
}

// lineIs is the index of the first line that is exactly the words.
func lineIs(lines []string, words string) int {
	for i, line := range lines {
		if line == words {
			return i
		}
	}
	return -1
}

// indexOf is the index of the first line that starts with the words.
func indexOf(lines []string, prefix string) int {
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	return -1
}

func TestGraphCompareCalmBandsAreTheEpisodesOfTheViewItWasOpenedFrom(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	end := base.End
	render := func(episodes *graphEpisodes) string {
		pg := calmGraphPage(132, 33)
		pg.Episodes = episodes
		return graphPlainLines(t, renderGraphCompareCalm(base, c, pg), 132)
	}
	own := calmColumns(calmLine(t, render(nil), calmMarker), "▼")

	// :slow read the same hour and found two episodes: they are the bands,
	// under their own name, and the rows are held against them. One of
	// them is the stall; in the other nothing of hx4mt stands.
	// An episode is stated as :slow states it: the first sample of the
	// stall stands for the half minute that ends at it.
	found := &graphEpisodes{Label: "episodes", Start: base.Start, End: end, Intervals: []chart.Interval{
		{Start: end.Add(-30*time.Minute - 30*time.Second), End: end.Add(-26*time.Minute - 30*time.Second)},
		{Start: end.Add(-10 * time.Minute), End: end.Add(-8 * time.Minute)},
	}}
	page := render(found)
	require.NotContains(t, page, calmMarker)
	marker := calmLine(t, page, "  episodes  ")
	band := calmColumns(marker, "▼")
	require.Greater(t, len(band), len(own))
	hot := calmColumns(calmLine(t, page, "> hx4mt"), calmLevels)
	require.Equal(t, own, hot, "the row is drawn as it is without the episodes")
	require.Subset(t, band, hot)
	require.NotSubset(t, hot, band, "an episode in which the Pod does not differ stays empty in its row")

	// Episodes looked for in another window say nothing about this one:
	// the page falls back on the marked row, and says whose columns they
	// are. So do episodes that were not found.
	for _, other := range []*graphEpisodes{
		{Label: "episodes", Start: end.Add(-15 * time.Minute), End: end, Intervals: found.Intervals[1:]},
		{Label: "episodes", Start: base.Start, End: end},
	} {
		page = render(other)
		require.Equal(t, own, calmColumns(calmLine(t, page, calmMarker), "▼"))
	}
	require.True(t, found.cover(base))
	require.True(t, (&graphEpisodes{Start: base.Start.Add(-time.Minute), End: end.Add(-time.Minute), Intervals: found.Intervals}).cover(base), "a read a minute earlier is the same window")
	require.False(t, (*graphEpisodes)(nil).cover(base))
}

func TestGraphCompareCalmStepsThroughSignalsInTheOrderItShowsThem(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	order, unread, perPod := graphCalmSignals(c, graphByPod)
	var ids []string
	for i := range order {
		ids = append(ids, order[i].sig.ID)
	}
	require.Equal(t, []string{"gitlab-webservice/queued", "gitlab-webservice/p99", "throttled", "cpu", "memory", "restarts", "network"}, ids,
		"the signals in which a Pod differs first, the furthest from its peers before the others; then the rest as they are read")
	require.Equal(t, []string{"5xx (no data)", "free-threads (no data)", "busy-threads (no data)"}, unread)
	require.Empty(t, perPod)

	render := func(signal string, width, height int) (string, *graphPage) {
		pg := calmGraphPage(width, height)
		pg.Signal = signal
		return graphPlainLines(t, renderGraphCompareCalm(base, c, pg), width), pg
	}
	// A caller arrives about one signal, by the name it knows it by: that
	// signal is marked, whether it is on the page already or not.
	for _, name := range []string{"p99", "gitlab-webservice/p99", "another-profile/p99"} {
		page, pg := render(name, 132, 33)
		lines := calmLines(page)
		require.Equal(t, "gitlab-webservice/p99", pg.Signal, name)
		require.True(t, strings.HasPrefix(lines[indexOf(lines, "  P99")+1], "> hx4mt"), name)
		require.Equal(t, 1, strings.Count(page, "\n> "), "one row is marked")
	}
	// The fourth signal has no room beside the first three: marking it
	// scrolls the groups, and the first is named in a line instead.
	page, pg := render("cpu", 132, 33)
	require.Equal(t, "cpu", pg.Signal)
	lines := calmLines(page)
	require.True(t, strings.HasPrefix(lines[lineIs(lines, "  CPU")+1], "> hx4mt  worker-f"))
	require.NotContains(t, page, "\n  QUEUED")
	require.Contains(t, page, "ALSO DIFFERS  queued (hx4mt 14, peers 0)")
	// A signal in which no Pod differs can be looked at too: its rows are
	// quiet, no band is drawn, and Enter opens the row that is marked.
	page, pg = render("network", 132, 33)
	require.Equal(t, "network", pg.Signal)
	require.Contains(t, page, "\n  NETWORK")
	require.Regexp(t, `^> 2bq7k  worker-d  ▁+  350kB/s$`, calmLine(t, page, "> "))
	require.NotContains(t, page, "▼")
	require.Equal(t, hangPods[0], pg.Open)
	require.NotContains(t, calmLine(t, page, "ALIKE ON ALL 4"), "network", "it is on the page, not in the line of the others")
	// An unknown name changes nothing.
	_, pg = render("no-such-signal", 132, 33)
	require.Equal(t, "gitlab-webservice/queued", pg.Signal)

	// A moved cursor reads every row at its sample, and says which.
	moved := calmGraphPage(132, 33)
	moved.Moved, moved.Cursor = true, int(base.Window/base.Step)-56
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, moved), 132)
	require.Regexp(t, `QUEUED · connections waiting for a free Puma thread .* 04:32:00$`, calmLine(t, page, "  QUEUED"))
	require.Regexp(t, `^> hx4mt  worker-f  [▁-█]+  14 +differs$`, calmLine(t, page, "> hx4mt"))
	require.Contains(t, calmLine(t, page, "┬"), "^")
	moved.Cursor = 10
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, moved), 132)
	require.Regexp(t, `^> hx4mt  worker-f  [▁-█]+  0 +differs$`, calmLine(t, page, "> hx4mt"), "outside the stall its queue is empty like the others'")
}

func TestGraphCompareCalmPageSaysWhatASmallerScreenGivesUp(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	render := func(width, height int, by graphGrouping) (string, *graphPage) {
		pg := calmGraphPage(width, height)
		pg.Grouping = by
		page := graphPlainLines(t, renderGraphCompareCalm(base, c, pg), width)
		require.LessOrEqual(t, len(calmLines(page)), height, "%dx%d", width, height)
		return page, pg
	}
	// 80x24 leaves 20 rows: one group of four Pods, every row of it, and
	// the other signals in which the Pod differs counted in one line.
	page, pg := render(80, 20, graphByPod)
	require.Empty(t, pg.Missing)
	for _, what := range pg.Dropped {
		require.Contains(t, what, "blank rows", "only blank rows are given up")
	}
	require.Contains(t, page, "Compare pods  Deployment gitlab/web · 4 pods · last 1h")
	require.Contains(t, page, "\n  QUEUED · connections waiting for a free Puma thread")
	require.Equal(t, 4, strings.Count(page, "worker-"), "one group, four rows")
	require.Contains(t, page, "\n  ALSO DIFFERS  p99 (hx4mt 48s, peers 350ms) +2 more\n")
	require.Contains(t, page, "\n  ALIKE ON ALL 4  memory · restarts · network\n")
	require.Contains(t, page, "NOT READ  5xx (no data)")
	require.Contains(t, page, "  next  Enter open hx4mt  g group by container / revision / node / zone")
	require.Contains(t, page, "  ↑↓ signal · ←→ time · 1-4 window · a show every signal · Esc back")
	require.Len(t, calmColumns(calmLine(t, page, calmMarker), "▼"), 4)

	// Regrouped, a signal has three rows. A second signal would cost the
	// second key line, so it stays in the line of the others.
	page, pg = render(80, 20, graphByNode)
	require.Empty(t, pg.Missing)
	require.Contains(t, page, "\n  QUEUED")
	require.NotContains(t, page, "\n  P99")
	require.Contains(t, page, "ALSO DIFFERS  p99 (hx4mt 48s, peers 350ms) +2 more")
	require.Contains(t, page, "  ↑↓ signal · ←→ time · 1-4 window · a show every signal · Esc back")

	// 60 columns: the strips shorten; nothing else changes. The number
	// column keeps the room of a cursor time, so the strips are as wide
	// with a cursor as without one.
	page, pg = render(60, 20, graphByPod)
	require.Empty(t, pg.Missing)
	require.Regexp(t, `^> hx4mt  worker-f  [▁-█]{22}  14 +differs$`, calmLine(t, page, "> hx4mt"))
	require.Contains(t, page, "NOT READ  5xx (no data) · free-threads (no data) +1 more")
	require.Contains(t, page, "  ↑↓ signal · ←→ time · 1-4 window · Esc back")

	// A very small screen keeps the Pod that differs, says what is not
	// shown, and never drops what was not read.
	page, pg = render(40, 12, graphByPod)
	require.NotEmpty(t, pg.Missing)
	require.Contains(t, page, "> hx4mt")
	require.Contains(t, page, "NOT READ")
	require.Contains(t, page, "not shown at this size (a shows all)")

	for _, by := range []graphGrouping{graphByPod, graphByRevision, graphByNode, graphByZone} {
		for _, size := range [][2]int{{200, 60}, {132, 33}, {100, 24}, {80, 20}, {72, 20}, {60, 20}, {50, 16}, {40, 12}, {30, 8}} {
			render(size[0], size[1], by)
		}
	}
	// A tall screen shows every signal in which a Pod differs.
	page, _ = render(132, 60, graphByPod)
	require.NotContains(t, page, graphAlsoDiffers)
	require.Positive(t, lineIs(calmLines(page), "  CPU"))
	require.Equal(t, 16, strings.Count(page, "worker-"), "four signals, four Pods each")
}

func TestGraphCompareCalmRegroupedHasOneRowPerGroup(t *testing.T) {
	fake := hangStory()
	base, c := hangCompare(t, fake)
	render := func(c *graphCompare, by graphGrouping) (string, *graphPage) {
		pg := calmGraphPage(132, 33)
		pg.Grouping = by
		return graphPlainLines(t, renderGraphCompareCalm(base, c, pg), 132), pg
	}

	// By node: the row of a group that holds the Pod that differs names
	// it, carries its number and stands in its columns.
	page, pg := render(c, graphByNode)
	require.Regexp(t, `^Compare pods  Deployment gitlab/web · 4 pods · by node · last 1h`, page)
	require.Contains(t, page, "Pod hx4mt differs from the other 3 in 4 of 7 signals.")
	require.Regexp(t, `^> worker-f  1 pod   [▁-█]+  14 +hx4mt  differs$`, calmLine(t, page, "> worker-f"))
	require.Regexp(t, `^  worker-d  2 pods  ▁+  0$`, calmLine(t, page, "  worker-d"))
	require.Regexp(t, `^  worker-e  1 pod   ▁+  0$`, calmLine(t, page, "  worker-e"))
	require.Equal(t, hangPods[2], pg.Open, "Enter opens the Pod, not the Node")
	require.Equal(t, calmColumns(calmLine(t, page, calmMarker), "▼"), calmColumns(calmLine(t, page, "> worker-f"), calmLevels))
	require.Contains(t, page, "  next  Enter open hx4mt    g group by zone / pod / container / revision")

	// By revision: the names of the revisions, and their sizes.
	page, _ = render(c, graphByRevision)
	require.Regexp(t, `^> rev 1 6f9c7d5b84  3 pods  [▁-█]+  14 +hx4mt  differs$`, calmLine(t, page, "> rev 1"))
	require.Regexp(t, `^  rev 2 7c5d8e9f01  1 pod   ▁+  0$`, calmLine(t, page, "  rev 2"))

	// By container the per-container series are read first, once; until
	// then the page says so and draws nothing under another name.
	page, _ = render(c, graphByContainer)
	require.Contains(t, page, "The per-container series have not been read. r reads again.")
	require.NotContains(t, page, "worker-")
	waiting := calmGraphPage(132, 33)
	waiting.Grouping, waiting.Busy = graphByContainer, "Reading the per-container series once…"
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, waiting), 132)
	require.Contains(t, page, "Reading the per-container series once…")
	require.Contains(t, page, "reading…")

	containers := loadGraphContainers(t.Context(), fake, c)
	page, pg = render(containers, graphByContainer)
	require.Regexp(t, `^Compare pods  Deployment gitlab/web · 4 pods · by container · last 1h`, page)
	require.Regexp(t, `^> hx4mt/webservice +worker-f +[▁-█]+  1\.44 +differs$`, calmLine(t, page, "> hx4mt/webservice"))
	require.Regexp(t, `^  2bq7k/gitlab-workhorse  worker-d +▁+  30m$`, calmLine(t, page, "  2bq7k/gitlab-workhorse"), "the proxy beside it is like the other proxies")
	require.Equal(t, hangPods[2], pg.Open)
	require.Contains(t, page, "\n  PER POD ONLY  p99 · 5xx · queued · free-threads · busy-threads · network\n", "a signal that is not measured per container says so")
	require.Regexp(t, `^  \+4 more +like their peers$`, calmLine(t, page, "  +4 more"), "eight containers: the one that differs, three of the others, and the count of the rest")
	require.Contains(t, page, "PgUp/PgDn pods (page 1 of 3)")
}

func TestGraphCompareCalmCountsThePodsItHasNoRowFor(t *testing.T) {
	// Twelve Pods, one of which uses five times the CPU of the others.
	fake := &compareFake{pods: map[string]graphPodMeta{}}
	var pods []string
	for i := range 12 {
		pod := fmt.Sprintf("api-5d4c8b7f6-p%02dzz", i)
		pods = append(pods, pod)
		fake.pods[pod] = graphPodMeta{Node: fmt.Sprintf("worker-%d", i%3), OwnerKind: graphReplicaSet, OwnerName: "api-5d4c8b7f6"}
		usage := 0.3
		if i == 7 {
			usage = 1.5
		}
		fake.series = append(fake.series, compareSeries{match: []string{"sum by (pod) (rate(container_cpu_usage_seconds_total"},
			labels: map[string]string{"pod": pod}, value: func(time.Time) (float64, bool) { return usage, true }})
	}
	target := &graphTarget{Context: "ct-1-1", Kind: "Deployment", Resource: client.DpGVR.String(), Namespace: "apps", Name: "api"}
	base := loadGraph(t.Context(), fake, target, time.Hour, graphNow)
	require.Nil(t, base.failure())
	c := loadGraphCompare(t.Context(), fake, base, hangSources(), graphServicePath{}, graphNow)
	render := func(page int) (string, *graphPage) {
		pg := calmGraphPage(132, 33)
		pg.Peers = page
		return graphPlainLines(t, renderGraphCompareCalm(base, c, pg), 132), pg
	}

	// The Pod that differs is on every page; its peers take turns, three
	// at a time, and one row counts the ones that are not shown.
	page, pg := render(0)
	require.Contains(t, page, "Pod p07zz differs from the other 11 in 1 of 1 signal. No other Pod differs.")
	lines := calmLines(page)
	group := lines[indexOf(lines, "  CPU"):]
	require.Regexp(t, `^> p07zz +worker-1 +█+  1\.5 +differs$`, group[1], "five times its peers in every sample")
	require.Regexp(t, `^  p00zz +worker-0 +▁+  300m$`, group[2])
	require.Regexp(t, `^  p01zz +worker-1 +▁+  300m$`, group[3])
	require.Regexp(t, `^  p02zz +worker-2 +▁+  300m$`, group[4])
	require.Equal(t, "  +8 more  like their peers", group[5])
	require.Equal(t, 4, pg.PeerPages)
	require.Contains(t, page, "PgUp/PgDn pods (page 1 of 4)")
	require.Equal(t, pods[7], pg.Open)

	page, pg = render(1)
	require.Contains(t, page, "> p07zz ")
	require.Contains(t, page, "\n  p03zz ")
	require.NotContains(t, page, "p00zz")
	require.Contains(t, page, "PgUp/PgDn pods (page 2 of 4)")
	require.Equal(t, 1, pg.Peers)

	// The last page holds what is left, and a page beyond it is the last.
	page, pg = render(99)
	require.Equal(t, 3, pg.Peers)
	require.Contains(t, page, "\n  p10zz ")
	require.Contains(t, page, "\n  p11zz ")
	require.Contains(t, page, "\n  +9 more  like their peers")

	// A row that differs and has no room is counted as one that differs.
	rows := make([]graphCalmRow, 9)
	for i := range rows {
		rows[i] = graphCalmRow{row: calm.Row{Subject: fmt.Sprintf("p%d", i)}, differs: i < 5}
	}
	shown, pages := graphCalmPaged(rows, 0, "like their peers")
	require.Equal(t, 6, pages)
	require.Len(t, shown, 5)
	require.Equal(t, calm.Row{Subject: "+5 more", What: "1 of them differ"}, shown[4].row)
	few, pages := graphCalmPaged(rows[:5], 3, "like their peers")
	require.Len(t, few, 5, "five rows are all shown")
	require.Equal(t, 1, pages)
}

func TestGraphCompareCalmNeverReadsMissingDataAsHealth(t *testing.T) {
	// Two Pods: there is no way to say which one is the odd one. The rows
	// are still drawn; none is called different.
	two := hangStory()
	delete(two.pods, hangPods[0])
	delete(two.pods, hangPods[3])
	base, c := hangCompare(t, two)
	pg := calmGraphPage(132, 33)
	page := graphPlainLines(t, renderGraphCompareCalm(base, c, pg), 132)
	require.Contains(t, page, "  2 Pods: too few to call one different. 3 are needed.")
	require.Contains(t, page, "Nothing found is not proof of health.")
	require.NotContains(t, page, "differs")
	require.NotContains(t, page, "▼")
	require.Regexp(t, `^> 8wzpd  worker-e  ▁+  1\.3%$`, calmLine(t, page, "> "))
	require.Contains(t, page, "\n  NOT COMPARED  cpu · memory · restarts · p99 · queued · network\n")
	require.NotContains(t, page, "ALIKE")

	// Nothing has samples: the page says that it compared nothing.
	none := &compareFake{pods: hangStory().pods}
	base, c = hangCompare(t, none)
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, calmGraphPage(132, 33)), 132)
	require.Contains(t, page, "No signal had samples for these Pods.")
	require.Contains(t, page, "Nothing can be compared: that is missing data, not health.")
	require.Contains(t, page, "Not read is not healthy.")
	require.Contains(t, page, "NOT READ  throttled (no data) · cpu (no data)")
	require.NotContains(t, page, "ALIKE")

	// No Pod differs: the page says how much it read, and shows the first
	// signal so that the claim can be looked at.
	level := hangStory()
	for i := range level.series {
		normal := level.series[i].value
		level.series[i].value = func(at time.Time) (float64, bool) {
			return normal(graphNow.Add(-50 * time.Minute))
		}
	}
	base, c = hangCompare(t, level)
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, calmGraphPage(132, 33)), 132)
	require.Contains(t, page, "  No Pod differs from its peers in the 7 signals read. 3 of 10 signals had no reading.")
	require.Contains(t, page, "Nothing found is not proof of health.")
	require.Contains(t, page, "\n  CPU THROTTLED")
	require.Contains(t, page, "ALIKE ON ALL 4  cpu · memory · restarts · p99 · queued · network")
	require.NotRegexp(t, `(?m)differs$`, page, "no row is called different")

	// The comparison is not read yet, or the read of the charts failed:
	// the page says which, under its own name.
	base, c = hangCompare(t, hangStory())
	waiting := calmGraphPage(100, 20)
	waiting.Busy = "Reading every signal once per Pod…"
	page = graphPlainLines(t, renderGraphCompareCalm(base, nil, waiting), 100)
	require.Regexp(t, `^Compare pods  Deployment gitlab/web · last 1h +reading…`, page)
	require.Contains(t, page, "Reading every signal once per Pod…")
	page = graphPlainLines(t, renderGraphCompareCalm(base, &graphCompare{Base: &graphSnapshot{}}, calmGraphPage(100, 20)), 100)
	require.Contains(t, page, "The per-Pod series of this window have not been read. r reads again.")
	denied := hangStory()
	denied.states = map[string]observability.State{"kube_pod_info": observability.StateDenied}
	failed := loadGraph(t.Context(), denied, hangTarget, time.Hour, graphNow)
	page = graphPlainLines(t, renderGraphCompareCalm(failed, c, calmGraphPage(100, 20)), 100)
	require.Regexp(t, `^Compare pods  Deployment gitlab/web · last 1h`, page)
	require.Contains(t, strings.Join(strings.Fields(page), " "), "Prometheus DENIED the lookup of this Deployment's Pods (HTTP 403)")
	require.Contains(t, page, "Not read is not healthy.")
}

func TestGraphApartSaysWhenAPodThatDiffersIsOutsideTheOthers(t *testing.T) {
	at := func(minutes int) time.Time { return graphNow.Add(time.Duration(minutes) * time.Minute) }
	series := func(values ...float64) []chart.Point {
		points := make([]chart.Point, 0, len(values))
		for i, v := range values {
			if !math.IsNaN(v) {
				points = append(points, chart.Point{T: at(i), V: v})
			}
		}
		return points
	}
	keys := func(apart map[int64]bool) []int {
		var out []int
		for minute := range 8 {
			if apart[at(minute).UnixNano()] {
				out = append(out, minute)
			}
		}
		return out
	}
	gap := math.NaN()
	above := &graphSignal{Direction: chart.Above, Floor: 0.1}
	set := &graphSeriesSet{Signal: above, Members: []graphMember{
		//                    0    1    2    3    4    5    6    7
		{Name: "odd", Outlier: true, Points: series(0.3, 5.0, 0.7, 0.65, 0.35, 5.0, 5.0, 0.1)},
		{Name: "b", Points: series(0.3, 0.3, 0.6, 0.30, 0.30, 0.3, gap, 0.3)},
		{Name: "c", Points: series(0.3, 0.4, 0.3, 0.30, 0.30, gap, gap, 0.3)},
	}}
	apart := graphApart(set)
	// 0: the same as the others. 1: far above both. 2: above both, but 0.7
	// is not twice their median of 0.45. 3: 0.65 is more than twice 0.3 and
	// above both. 4: closer to them than the noise floor. 5: one other Pod
	// is enough to say when. 6: no other Pod has a sample. 7: below, on a
	// side this signal is not worse on.
	require.Equal(t, []int{1, 3, 5}, keys(apart[0]))
	require.Empty(t, apart[1], "a Pod that does not differ over the window has no such samples")
	require.Empty(t, apart[2])

	// A signal that is worse when low is apart below the others only.
	low := &graphSeriesSet{Signal: &graphSignal{Direction: chart.Below, Floor: 1, Rank: graphRankLow}, Members: []graphMember{
		{Name: "odd", Outlier: true, Points: series(4, 0, 3, 9)},
		{Name: "b", Points: series(4, 4, 4, 4)},
		{Name: "c", Points: series(4, 3, 4, 4)},
	}}
	require.Equal(t, []int{1}, keys(graphApart(low)[0]))
	// A counter is compared by its rise at that sample.
	counter := &graphSeriesSet{Signal: &graphSignal{Direction: chart.Above, Floor: 1, Rank: graphRankRise}, Members: []graphMember{
		{Name: "odd", Outlier: true, Points: series(2, 2, 3, 3, 5)},
		{Name: "b", Points: series(7, 7, 7, 7, 7)},
	}}
	require.Equal(t, []int{2, 4}, keys(graphApart(counter)[0]))

	// The bands are stated as episodes are: a sample stands for the step
	// that ends at it, and neighbouring samples are one stretch.
	require.Equal(t, []chart.Interval{{Start: at(0), End: at(1)}, {Start: at(2), End: at(3)}, {Start: at(4), End: at(5)}}, graphApartBands(apart[0], time.Minute))
	require.Equal(t, []chart.Interval{{Start: at(0), End: at(2)}}, graphApartBands(map[int64]bool{at(1).UnixNano(): true, at(2).UnixNano(): true}, time.Minute))
}

// The numbers of the rows are read under the cursor and differ in width
// from sample to sample and from what ranks a row (48s, 350ms, 14, 0), and
// the word over them becomes a time. The number column is held as wide as
// the widest of them: moving the cursor moves nothing but the cursor.
func TestGraphCompareCalmCursorMovesNothingButItself(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	steps := int(base.Window / base.Step)
	for _, by := range []graphGrouping{graphByPod, graphByNode} {
		for _, size := range [][2]int{{132, 33}, {80, 20}, {60, 20}} {
			still := calmGraphPage(size[0], size[1])
			still.Grouping = by
			page := graphPlainLines(t, renderGraphCompareCalm(base, c, still), size[0])
			start, width := calmAxisSpan(t, page)
			band := calmColumns(calmLine(t, page, "▼"), "▼")
			differs := strings.Index(calmLine(t, page, "> "), " differs")
			require.NotEmpty(t, band)
			require.Positive(t, differs)
			for _, cursor := range []int{0, 1, steps / 3, steps - 56, steps - 30, steps - 1, steps} {
				moved := calmGraphPage(size[0], size[1])
				moved.Grouping, moved.Moved, moved.Cursor = by, true, cursor
				page := graphPlainLines(t, renderGraphCompareCalm(base, c, moved), size[0])
				gotStart, gotWidth := calmAxisSpan(t, page)
				require.Equal(t, [2]int{start, width}, [2]int{gotStart, gotWidth}, "%s %dx%d cursor %d: the axis\n%s", by, size[0], size[1], cursor, page)
				require.Equal(t, band, calmColumns(calmLine(t, page, "▼"), "▼"), "%s %dx%d cursor %d: the bands", by, size[0], size[1], cursor)
				require.Equal(t, differs, strings.Index(calmLine(t, page, "> "), " differs"), "%s %dx%d cursor %d: the word after the strip", by, size[0], size[1], cursor)
			}
		}
	}
}

// The word over the numbers says what they are, and under a moved cursor
// it is the only place that says which time they are read at. On a narrow
// page the label of the group gives way to it.
func TestGraphCompareCalmKeepsTheWordOverTheNumbersOnANarrowPage(t *testing.T) {
	base, c := hangCompare(t, hangStory())
	still := calmGraphPage(60, 20)
	page := graphPlainLines(t, renderGraphCompareCalm(base, c, still), 60)
	require.Regexp(t, `^  QUEUED +peak$`, calmLine(t, page, "  QUEUED"), "60 columns have no room for what the signal measures and the word")
	moved := calmGraphPage(60, 20)
	moved.Moved, moved.Cursor = true, int(base.Window/base.Step)-56
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, moved), 60)
	require.Regexp(t, `^  QUEUED +┆? +04:32:00$`, calmLine(t, page, "  QUEUED"))
	require.Regexp(t, `^> hx4mt  worker-f  [▁-█┆]+  14 +differs$`, calmLine(t, page, "> hx4mt"))
	// Where both fit, both are written.
	for _, size := range [][2]int{{132, 33}, {80, 20}} {
		page = graphPlainLines(t, renderGraphCompareCalm(base, c, calmGraphPage(size[0], size[1])), size[0])
		require.Regexp(t, `^  QUEUED · connections waiting for a free Puma thread +peak$`, calmLine(t, page, "  QUEUED"))
	}
	// A group whose word is the one already written over the group above
	// has none to make room for, and keeps its label.
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, calmGraphPage(60, 33)), 60)
	require.Contains(t, page, "\n  P99 · Rails request duration, 99th percentile\n")
}

// A Pod without a sample in a signal was compared with nothing in it. The
// page does not call it alike, and does not count it among the Pods the
// one that differs was held against.
func TestGraphCompareCalmCountsOnlyThePodsThatWereSampled(t *testing.T) {
	story := hangStory()
	kept := story.series[:0]
	for _, series := range story.series {
		// The fourth Pod has no memory, no network and no queue metric.
		unsampled := series.labels["pod"] == hangPods[3] &&
			(strings.Contains(strings.Join(series.match, " "), "container_memory_working_set_bytes") ||
				strings.Contains(strings.Join(series.match, " "), "container_network_receive_bytes_total") ||
				strings.Contains(strings.Join(series.match, " "), "puma_queued_connections"))
		if !unsampled {
			kept = append(kept, series)
		}
	}
	story.series = kept
	base, c := hangCompare(t, story)
	page := graphPlainLines(t, renderGraphCompareCalm(base, c, calmGraphPage(132, 33)), 132)
	lines := calmLines(page)
	require.Equal(t, "  Pod hx4mt differs from its peers in 4 of 7 signals. No other Pod differs. 3 of 10 signals had no reading.", lines[2],
		"in the queue it was held against two Pods, not \"the other 3\"")
	require.Contains(t, page, "\n  ALIKE  memory (3 of 4 pods sampled) · restarts · network (3 of 4 pods sampled)\n")
	require.NotContains(t, page, "ALIKE ON ALL")
	require.Regexp(t, `^  r5n9j  worker-d  ×+  no samples$`, calmLine(t, page, "  r5n9j"), "its row says so, with the gap glyph all along")

	// Every Pod sampled in every signal: the count is of all of them.
	base, c = hangCompare(t, hangStory())
	page = graphPlainLines(t, renderGraphCompareCalm(base, c, calmGraphPage(132, 33)), 132)
	require.Contains(t, page, "Pod hx4mt differs from the other 3 in 4 of 7 signals.")
	require.Contains(t, page, "\n  ALIKE ON ALL 4  memory · restarts · network\n")
}

func TestGraphCompareVerdictCountsPodsAndSignals(t *testing.T) {
	_, c := hangCompare(t, hangStory())
	st := c.standing()
	v := graphCompareVerdict(c, &st)
	require.Equal(t, []string{"Pod hx4mt differs from the other 3 in 4 of 7 signals.", "No other Pod differs. 3 of 10 signals had no reading."}, v.Sentences)
	require.Equal(t, "A difference, not proof of cause.", v.Caveat)

	several := graphStanding{Pods: []string{"hx4mt", "2bq7k", "8wzpd", "r5n9j", "extra"}, Read: 7, Total: 7,
		Signals: map[string][]string{"hx4mt": {"a", "b", "c"}, "2bq7k": {"a"}, "8wzpd": {"b"}, "r5n9j": {"c"}, "extra": {"d"}}}
	v = graphCompareVerdict(c, &several)
	require.Equal(t, []string{"5 of 4 Pods differ from their peers: hx4mt in 3, 2bq7k in 1, 8wzpd in 1, 2 more of 7 signals."}, v.Sentences)
	require.Equal(t, "the other one", graphOthers(1))
	require.Equal(t, "the other two", graphOthers(2))
	require.Equal(t, "the other 11", graphOthers(11))
}

func TestGraphViewCalmComparisonKeysOpenThePodAndTakeTheEpisodesOfSlow(t *testing.T) {
	v, _, reads, deliver := compareViewFixture(t, hangTarget)
	press := func(k tcell.Key) {
		t.Helper()
		action, ok := v.actions.Get(k)
		require.True(t, ok, tcell.KeyNames[k])
		action.Action(tcell.NewEventKey(k, 0, tcell.ModNone))
	}
	press(ui.KeyP)
	deliver()
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, frame, "Compare pods  Deployment gitlab/web · 4 pods · last 1h")
	require.Contains(t, frame, "> hx4mt  worker-f")
	require.Equal(t, "gitlab-webservice/queued", v.signal)

	// Down, n and Tab mark the next signal in the order of the page; Up,
	// Shift-N and Shift-Tab the previous. They wrap, and read nothing.
	press(tcell.KeyDown)
	require.Equal(t, "gitlab-webservice/p99", v.signal)
	press(ui.KeyN)
	press(calmNextChartAltKey)
	require.Equal(t, "cpu", v.signal)
	require.Contains(t, drawnText(t, v, 132, 33), "\n  CPU\n> hx4mt")
	press(tcell.KeyUp)
	press(ui.KeyShiftN)
	press(tcell.KeyBacktab)
	press(tcell.KeyUp)
	require.Equal(t, "network", v.signal, "before the first is the last")
	press(tcell.KeyDown)
	require.Equal(t, "gitlab-webservice/queued", v.signal)

	// a shows the page with a chart per Pod on the same signal; there the
	// arrows turn its pages as its key line says. a again returns.
	press(calmDetailKey)
	detail := drawnText(t, v, 132, 33)
	require.Contains(t, detail, "QUEUED by pod")
	require.Contains(t, detail, "EVERY SIGNAL")
	require.Contains(t, detail, "a short page")
	press(calmDetailKey)
	require.Contains(t, drawnText(t, v, 132, 33), "> hx4mt  worker-f")

	// Four Pods have one page: the page keys change nothing.
	press(tcell.KeyPgDn)
	require.Zero(t, v.peers)
	require.EqualValues(t, 1, reads.Load())

	// Enter opens the charts of the Pod the page marks, for the same window.
	require.Equal(t, hangPods[2], v.open)
	press(tcell.KeyEnter)
	opened, ok := v.app.Content.Top().(*graphView)
	require.True(t, ok)
	require.NotSame(t, v, opened)
	require.Equal(t, "Pod", opened.target.Kind)
	require.Equal(t, hangPods[2], opened.target.Name)
	require.Equal(t, v.window, opened.window)
	require.Nil(t, opened.episodes, "a graph view knows of no episodes")

	// Opened from :slow, the comparison takes the episodes that view found
	// and the window they were looked for in.
	slow := slowViewFixture(t)
	slow.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	report := slow.report()
	require.NotEmpty(t, report.Episodes)
	slow.app.Content.Push(slow)
	open, ok := slow.actions.Get(ui.KeyM)
	require.True(t, ok)
	open.Action(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
	fromSlow, ok := slow.app.Content.Top().(*graphView)
	require.True(t, ok)
	require.True(t, fromSlow.comparing)
	require.NotNil(t, fromSlow.episodes)
	require.Equal(t, graphEpisodes{Label: "episodes", Start: report.Start, End: report.End, Intervals: report.Episodes}, *fromSlow.episodes)
}
