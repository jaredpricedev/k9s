// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"regexp"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// impactRowAt finds the numbers of a drawn row: before, after, the change
// and its ratio, between the name of the signal and its strip.
var impactRowAt = regexp.MustCompile(`^[> ] \S.*? (\S+) (?:→|->) (\S+) +([+-]\S+|0) +(\S+) +\S+$`)

// One kind of number is read down one column: on every row of the page
// the arrow stands in one column, under the arrow of the word over it,
// and the change and its ratio each start in one.
func TestImpactCalmSetsTheNumbersOfEveryRowInSharedColumns(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		v := impactViewFixture(t)
		v.app.Config.K9s.UI.NoIcons = ascii
		v.accept(impactStoryRead(t, impactStory()))
		arrow := "→"
		if ascii {
			arrow = "->"
		}
		for size, rows := range map[[2]int]int{{132, 33}: 9, {80, 21}: 7} {
			frame := drawnText(t, v, size[0], size[1])
			column := func(line string, index int) int { return runewidth.StringWidth(line[:index]) }
			var arrows, deltas, ratios, ends []int
			head := -1
			for _, line := range strings.Split(frame, "\n") {
				if strings.HasPrefix(line, "  "+impactWorseLabel) {
					head = column(line, strings.Index(line, arrow))
				}
				at := impactRowAt.FindStringSubmatchIndex(line)
				if at == nil {
					continue
				}
				arrows = append(arrows, column(line, at[3])+1)
				deltas, ratios = append(deltas, column(line, at[6])), append(ratios, column(line, at[8]))
				// The strip starts in one column too: the text beside it is
				// as wide on every row.
				ends = append(ends, column(line, strings.LastIndex(line, " ")))
			}
			require.Len(t, arrows, rows, "%dx%d ascii=%v\n%s", size[0], size[1], ascii, frame)
			for _, columns := range [][]int{arrows, deltas, ratios, ends} {
				for _, at := range columns {
					require.Equal(t, columns[0], at, "%dx%d ascii=%v\n%s", size[0], size[1], ascii, frame)
				}
			}
			require.Equal(t, arrows[0], head, "the arrow of the heading stands over the arrows of the rows")
			require.Less(t, arrows[0], deltas[0])
			require.Less(t, deltas[0], ratios[0])
		}
	}

	// The two columns of before and after are set where the kit writes
	// them whole, and the word over them moves with the arrow.
	head, number, compare := impactColumnsOf([]impactFigures{
		{before: "0.2%", after: "14.2%", delta: "+14pp", ratio: "x71"},
		{before: "66.7ms", after: "593ms", delta: "+526ms", ratio: "+789%"},
		{before: "0", after: "6", delta: "+6", ratio: "new"},
	})
	require.Equal(t, "before -> after", head)
	require.Equal(t, []string{"  0.2% -> 14.2%", "66.7ms -> 593ms", "     0 -> 6    "}, number)
	require.Equal(t, []string{"+14pp   x71  ", "+526ms  +789%", "+6      new  "}, compare)
	head, number, _ = impactColumnsOf([]impactFigures{{before: "104.5Mi", after: "2Gi"}, {before: "60m", after: "340m"}})
	require.Equal(t, " before -> after", head)
	require.Equal(t, []string{"104.5Mi -> 2Gi ", "    60m -> 340m"}, number)
	// Wider than the kit writes a number: nothing is padded, so nothing
	// is cut for the sake of a column.
	head, number, _ = impactColumnsOf([]impactFigures{{before: "1023.5Mi", after: "1047.9Mi"}, {before: "0", after: "6"}})
	require.Equal(t, "before -> after", head)
	require.Equal(t, []string{"1023.5Mi -> 1047.9Mi", "0 -> 6"}, number)
}

// What is only higher or lower folds to one line before the page gives a
// blank row up, so a page that is a few rows short still sets its answer,
// its groups and its keys apart. The group the cursor is in stays rows.
func TestImpactCalmFoldsTheNeutralRowsBeforeItGivesABlankRowUp(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	frame := drawnText(t, v, 100, 26)
	require.Equal(t, []string{"HIGHER OR LOWER AFTER folded to one line"}, v.dropped)
	lines := strings.Split(frame, "\n")
	require.Empty(t, lines[1], "the blank row under the title")
	require.Regexp(t, `(?m)^  UTC .*\n\n  HIGHER OR LOWER AFTER  Memory peak 188Mi → 254Mi · CPU usage 60m → 340m\n  UNCHANGED  Request rate\n`, frame)
	require.Regexp(t, `(?m)^  NOT AVAILABLE  CPU throttled\n\n  next  .*\n\n  ↑↓ signal `, frame)
	require.Regexp(t, `(?m)^ +rev 2 rolled out .*▼\n +┊\n  WORSE AFTER `, frame, "and the one between the marker row and the rows")

	// In a terminal of 80x24 the view is given 21 rows, and 20 where the
	// application also draws its line of crumbs. In 21 the same fold leaves
	// the page one blank row under the answer and one over the keys; in 20
	// the one row to spare is the one under the answer. Either way every
	// signal that is worse keeps its row.
	worse := []string{"Error ratio", "Latency p50", "Latency p95", "Latency p99", "Restarts", "Memory/limit", "Ready (min)"}
	for _, width := range []int{80, 60} {
		frame = drawnText(t, v, width, 21)
		require.Len(t, strings.Split(strings.TrimRight(frame, "\n"), "\n"), 21)
		require.Regexp(t, `(?m)^  `+regexp.QuoteMeta(impactOrderCaveat)+`\n\n +rev 2 .*▼\n  WORSE AFTER `, frame)
		require.Regexp(t, `(?m)^  NOT AVAILABLE  CPU throttled\n\n  next  `, frame)
		require.NotContains(t, v.dropped, "the blank row over the keys")
		for _, signal := range worse {
			require.Contains(t, frame, " "+signal+" ", "%d columns", width)
		}

		frame = drawnText(t, v, width, 20)
		require.Len(t, strings.Split(strings.TrimRight(frame, "\n"), "\n"), 20, "no row is left unused")
		require.Regexp(t, `(?m)^  `+regexp.QuoteMeta(impactOrderCaveat)+`\n\n +rev 2 .*▼\n  WORSE AFTER `, frame, "%d columns: the answer is still apart", width)
		require.Regexp(t, `(?m)^  NOT AVAILABLE  CPU throttled\n  next  `, frame)
		require.Contains(t, v.dropped, "the blank row over the keys")
		require.NotContains(t, v.dropped, "the blank row under the verdict")
		require.NotContains(t, frame, "not shown", "%d columns: nothing is left out", width)
		for _, signal := range worse {
			require.Contains(t, frame, " "+signal+" ", "%d columns", width)
		}
		require.Len(t, impactAnswerLines(t, frame), 3)
	}

	// With the cursor on one of its rows the group is what the keys act
	// on: it stays rows, and the page gives blank rows up for it.
	for v.focused != impact.SigMemory {
		before := v.focused
		impactPress(t, v, tcell.KeyDown)
		require.NotEqual(t, before, v.focused)
	}
	frame = drawnText(t, v, 100, 26)
	require.Regexp(t, `(?m)^> Memory peak +188Mi → 254Mi .*▆+┊█+$`, frame)
	require.NotContains(t, v.dropped, "HIGHER OR LOWER AFTER folded to one line")
	require.Contains(t, v.dropped, "blank rows around the verdict and the keys")
}

// The cursor marks a row and moves nothing: every line of the page but
// the next line, which names the graph of the row, is the same wherever
// the cursor is.
func TestImpactCalmCursorMovesNoColumn(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	still := func(frame string) []string {
		var out []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "  next ") {
				continue
			}
			if strings.HasPrefix(line, "> ") {
				line = "  " + line[2:]
			}
			out = append(out, line)
		}
		return out
	}
	for size, rows := range map[[2]int]int{{132, 33}: 9, {80, 21}: 7} {
		for v.focused != impact.SigErrors {
			impactPress(t, v, tcell.KeyUp)
		}
		first := drawnText(t, v, size[0], size[1])
		require.Equal(t, 1, strings.Count(first, "\n> "))
		for row := 1; row < rows; row++ {
			impactPress(t, v, tcell.KeyDown)
			frame := drawnText(t, v, size[0], size[1])
			require.Equal(t, 1, strings.Count(frame, "\n> "), "%dx%d row %d", size[0], size[1], row)
			require.Equal(t, still(first), still(frame), "%dx%d row %d (%s)", size[0], size[1], row, v.focused)
		}
	}
}

// In a terminal of 80x24 or 60x24 the view has 20 rows, and the group of
// what is only higher or lower is one line. The cursor opens it, and the
// page leaves rows of the first group out for it. No column moves for
// that: the names are one column as wide as the widest of all rows, so the
// arrows, the strips, the rollout guide and the axis stand where they
// stood, whichever rows are drawn.
func TestImpactCalmCursorOpensAGroupAndMovesNoColumn(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	column := func(line, mark string) int {
		at := strings.Index(line, mark)
		if at < 0 {
			return -1
		}
		return runewidth.StringWidth(line[:at])
	}
	for _, width := range []int{80, 60} {
		for v.focused != impact.SigErrors {
			impactPress(t, v, tcell.KeyUp)
		}
		type columns struct{ arrow, strip, guide, marker, axis int }
		var first columns
		opened := false
		for row := range 9 {
			if row > 0 {
				impactPress(t, v, tcell.KeyDown)
			}
			frame := drawnText(t, v, width, 20)
			require.Equal(t, 1, strings.Count(frame, "\n> "), "%d columns, row %d: one row is marked", width, row)
			var seen []columns
			at := columns{marker: -1, axis: -1}
			for _, line := range strings.Split(frame, "\n") {
				switch {
				case strings.HasSuffix(line, "▼"):
					at.marker = column(line, "▼")
				case strings.HasPrefix(strings.TrimLeft(line, " "), "─"):
					at.axis = column(line, "─")
				}
				if impactRowLine.MatchString(line) {
					seen = append(seen, columns{arrow: column(line, " → "), strip: runewidth.StringWidth(line[:strings.LastIndex(line, " ")+1]),
						guide: max(column(line, "┊"), column(line, "▲"))})
				}
			}
			require.NotEmpty(t, seen, "%d columns, row %d\n%s", width, row, frame)
			for _, got := range seen[1:] {
				require.Equal(t, seen[0].arrow, got.arrow, "%d columns, row %d\n%s", width, row, frame)
				require.Equal(t, seen[0].strip, got.strip, "%d columns, row %d\n%s", width, row, frame)
			}
			at.arrow, at.strip = seen[0].arrow, seen[0].strip
			for _, got := range seen {
				if got.guide >= 0 {
					at.guide = got.guide
				}
			}
			require.Equal(t, at.strip, at.axis, "the axis starts where the strips do")
			require.Equal(t, at.guide, at.marker, "the marker stands over the guide")
			if row == 0 {
				first = at
				require.NotContains(t, frame, "not shown", "%d columns: every row that is worse is drawn", width)
			}
			require.Equal(t, first, at, "%d columns, row %d (%s): no column moved\n%s", width, row, v.focused, frame)
			if strings.Contains(frame, "\n  HIGHER OR LOWER AFTER  Memory peak ") {
				continue
			}
			// The group is open: its rows are drawn, and what the page
			// left out for them is counted on a line that names the key.
			opened = true
			require.Regexp(t, `(?m)^[> ] Memory peak +188Mi → 254Mi .*[▆█┊]+$`, frame)
			require.Regexp(t, `(?m)^  not shown( at this size)? \(a shows all\): 2 of 7 rows of WORSE AFTER$`, frame)
		}
		require.True(t, opened, "%d columns: the cursor reaches the rows that are only higher or lower", width)
		require.Equal(t, impact.SigCPU, v.focused)
	}
}

// Every line of the block of s is read to its end: the rule, the count of
// samples, the source and the windows are written on as many lines as
// they need where the page has the rows, as the queries and the diff are.
// They are the first to be one line each again, cut and marked, where it
// has not: before a query or the diff is cut.
func TestImpactCalmWritesTheRuleAndTheSamplesWholeWhereThePageHasTheRows(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	impactPress(t, v, impactQueriesKey)
	block := func(frame string) string {
		at := strings.Index(frame, "\n  ERROR RATIO  ")
		require.Positive(t, at, frame)
		out, _, found := strings.Cut(frame[at:], "\n  next ")
		require.True(t, found)
		return out
	}
	flat := func(text string) string { return strings.Join(strings.Fields(text), " ") }
	const (
		rule    = "rule counts as changed from 0.1pp and 10% of before; needs 4 samples on each side"
		samples = "samples 49 before, 41 after; 7 just after the rollout left out (their 2m rate window spans it)"
		windows = "windows before 04:08:00-04:20:00Z | after 04:20:00-04:32:00Z · 12m each (asked 30m; the rollout was 12m before this read)"
	)
	for _, size := range [][2]int{{80, 40}, {60, 44}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		text := block(frame)
		require.NotContains(t, text, "…", "%dx%d: nothing in the block is cut\n%s", size[0], size[1], frame)
		for _, whole := range []string{rule, samples, windows, "changed image v1 → v2; limits.memory 512Mi → 256Mi; env +FEATURE_FLAGS"} {
			require.Contains(t, flat(text), whole, "%dx%d", size[0], size[1])
		}
		require.Regexp(t, `strip the value from zero; in \S+ where a sample is further from the before value than the rule allows rule `, flat(text), "%dx%d", size[0], size[1])
		// A line that continues stands under the text, not under its label.
		require.Regexp(t, `(?m)^  rule     counts as changed .*\n           \S`, text, "%dx%d", size[0], size[1])
		require.Regexp(t, `(?m)^> Error ratio .*▁+┊█+$`, frame, "%dx%d: the row it belongs to keeps its strip", size[0], size[1])
		require.Contains(t, frame, "\n  next  Enter graph", "%dx%d", size[0], size[1])
	}
	// The 21 rows of an 80x24 terminal have not the rows for that: the
	// rule and the samples are one line each, and the queries and the
	// diff are still whole.
	text := block(drawnText(t, v, 80, 21))
	require.Regexp(t, `(?m)^  rule     counts as changed from 0\.1pp and 10% of before; .*…$`, text)
	require.Regexp(t, `(?m)^  samples  49 before, 41 after; .*…$`, text)
	require.Contains(t, flat(text), `pod=~"checkout-api-[a-z0-9]+-[a-z0-9]+",code=~"5.."}[120s]))`)
	require.Contains(t, text, "\n  changed  image v1 → v2; limits.memory 512Mi → 256Mi; env +FEATURE_FLAGS")

	// Nothing was read: the sentence that says so is read to its end too.
	v.accept(impactStoryRead(t, &impactSource{status: observability.Status{State: observability.StateDenied, HTTPStatus: 403}}))
	v.focused = ""
	frame := drawnText(t, v, 60, 30)
	require.Contains(t, flat(frame), "Nothing was read, so nothing is shown as a value. That is not a zero and not a sign of health.")
}

// The hand-off to the timeline is named on the second key line where the
// first has no room for it and the second has: among the view's own keys,
// before Grafana, as on every calm page.
func TestImpactCalmNamesTheTimelineOnTheSecondLineWhereTheFirstHasNoRoom(t *testing.T) {
	v := impactViewFixture(t)
	read := impactStoryRead(t, impactStory())
	read.rollout.Total = 1
	v.accept(read)
	frame := drawnText(t, v, 60, 33)
	require.Contains(t, frame, "  next  Enter graph  a all 11 signals  s query and rule\n")
	require.Contains(t, frame, "  ↑↓ signal · 1-5 window · t timeline · D Grafana · Esc back\n")
	frame = drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^  next  .* t timeline$`, frame)
	require.Contains(t, frame, "  ↑↓ signal · 1-5 window · D Grafana · r re-read · Esc back\n", "and once where the first has it")
}

// impactRowLine is a drawn row: a name, before and after, and a strip.
var impactRowLine = regexp.MustCompile(`^[> ] \S.* (?:→|->) \S+ .*[▁▂▃▄▅▆▇█·▲┊]{8,}$`)

// A key that works stays named. The first key line names all detail, the
// queries and the timeline, because a short page gives the second line
// up; where it is too narrow for all of them it says "graph" without
// what, which the marked row says, and what it still has no room for is
// named on the second line, in the place every calm page has for it.
func TestImpactCalmKeepsItsKeysNamedOnANarrowLine(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	next := func(frame string) string {
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "  next  ") {
				return line
			}
		}
		t.Fatalf("no next line\n%s", frame)
		return ""
	}
	// What Enter graphs, for every row the cursor reaches.
	graphs := []string{"the workload", "the workload", "the workload", "the workload", "restarts", "memory/limit", "the workload", "memory peak", "CPU"}
	for _, size := range [][2]int{{132, 33}, {80, 20}, {60, 20}, {60, 33}} {
		for v.focused != impact.SigErrors {
			impactPress(t, v, tcell.KeyUp)
		}
		for row, graph := range graphs {
			if row > 0 {
				impactPress(t, v, tcell.KeyDown)
			}
			frame := drawnText(t, v, size[0], size[1])
			line := next(frame)
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0])
			require.Regexp(t, `^  next  Enter graph.* a all 11 signals +s query and rule`, line, "%dx%d row %d: a and s are named at every size", size[0], size[1], row)
			switch size[0] {
			case 132, 80:
				// Every row names all four, with what Enter graphs.
				require.Regexp(t, `^  next  Enter graph `+regexp.QuoteMeta(graph)+` +a all 11 signals +s query and rule +t timeline$`, line, "%dx%d row %d", size[0], size[1], row)
			default:
				// 60 columns: the timeline has no room on this line.
				require.NotContains(t, line, "timeline")
				if tviewCells("Enter graph "+graph) > 15 {
					require.Regexp(t, `^  next  Enter graph  a all 11 signals`, line, "%dx%d row %d", size[0], size[1], row)
				}
			}
			rest := ""
			if at := strings.Index(frame, "\n  ↑↓ signal "); at >= 0 {
				rest = strings.SplitN(frame[at+1:], "\n", 2)[0]
			}
			switch {
			case size == [2]int{132, 33}:
				require.Equal(t, "  ↑↓ signal · 1-5 window · n/N later/earlier rollout · D Grafana · r re-read · Esc back", rest, "what the next line names is not named twice")
			case size == [2]int{60, 33}:
				// The line drops from the right: the keys of the rollouts
				// are said short, so that they are named at all.
				require.Equal(t, "  ↑↓ signal · 1-5 window · n/N rollout · Esc back", rest, "%dx%d row %d", size[0], size[1], row)
			default:
				require.Empty(t, rest, "%dx%d: a full page has no second key line", size[0], size[1])
			}
		}
	}
}

// The answer is two lines in a terminal of 132 columns and three in one
// of 80 or 60: each sentence takes the fullest form that is one line, and
// the caveat is at the right of the last where it fits. A request rate
// that doubled is one sentence more, because it limits what the
// comparison says; it is shortened like the others and never left out.
func TestImpactCalmAnswerTakesFewLinesAndLeavesNothingOut(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	for size, lines := range map[[2]int]int{{132, 33}: 2, {120, 34}: 2, {110, 30}: 3, {80, 21}: 3, {60, 21}: 3} {
		require.Len(t, impactAnswerLines(t, drawnText(t, v, size[0], size[1])), lines, "%dx%d", size[0], size[1])
	}

	shifted := impactStory()
	shifted.answers["http_requests_total"] = [2]float64{40, 120}
	v.accept(impactStoryRead(t, shifted))
	const traffic = "Request rate 40/s → 120/s (+200%): error ratio and latency are not like-for-like."
	answer := impactAnswerLines(t, drawnText(t, v, 132, 33))
	require.Len(t, answer, 3, "never more than three lines at 132 columns")
	require.Regexp(t, `^  \d signals got worse after rev 2 rolled out at 04:20: `, answer[0])
	require.Equal(t, "  "+traffic, answer[1], "what limits the comparison follows the sentence it limits")
	require.Regexp(t, `^  Rev 2 changed the image, .* {3,}Order in time, not proof of cause\.$`, answer[2])
	// At 80 columns the sentence is one line with the size of the change,
	// and the folded line under the axis has the two rates; the page keeps
	// its blank rows. At 60 it takes two lines, and is still there.
	frame := drawnText(t, v, 80, 21)
	require.Equal(t, []string{"  7 signals got worse after rev 2 rolled out at 04:20.", "  Request rate +200%: errors and latency are not like-for-like.",
		"  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 1 env var.", "  " + impactOrderCaveat}, impactAnswerLines(t, frame))
	require.Contains(t, frame, "  HIGHER OR LOWER AFTER  Request rate 40/s → 120/s +2 more\n")
	require.Regexp(t, `(?m)`+regexp.QuoteMeta(impactOrderCaveat)+`\n\n +rev 2 .*▼$`, frame)
	answer = impactAnswerLines(t, drawnText(t, v, 60, 21))
	require.Len(t, answer, 5)
	require.Contains(t, strings.Join(strings.Fields(strings.Join(answer, " ")), " "), "Request rate +200%: errors and latency are not like-for-like.")
}

// The answer says what kinds of thing the revision changed. The list
// itself is under s, whole wherever the page has the rows for it beside
// the queries. Where it has not, the queries stay whole, because s is the
// key of the query and the rule, and the list is one line with its cut
// marked; the answer still says what kinds of thing changed.
func TestImpactCalmShowsTheWholeDiffUnderS(t *testing.T) {
	v := impactViewFixture(t)
	read := impactStoryRead(t, impactStory())
	keys := []string{"~PAYMENT_TOKEN", "+FEATURE_FLAGS", "+CHECKOUT_TIMEOUT_MS", "+LEDGER_ENDPOINT", "-LEGACY_GATEWAY_URL"}
	read.report.Changes = []string{"image v1 -> v2", "limits.memory 512Mi -> 256Mi", "env " + strings.Join(keys, " ")}
	v.accept(read)
	frame := drawnText(t, v, 80, 21)
	const changed = "  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 5 env vars.\n"
	require.Contains(t, frame, changed)
	for _, key := range keys {
		require.NotContains(t, frame, key[1:], "the names are not in the answer")
	}

	impactPress(t, v, impactQueriesKey)
	// flat is the block of s from its line of the diff to the keys, as
	// one line of words.
	diff := func(frame string) (block, flat string) {
		at := strings.Index(frame, "\n  changed  image v1 → v2; ")
		require.Positive(t, at, frame)
		block, _, found := strings.Cut(frame[at:], "\n  next ")
		require.True(t, found)
		if rest, _, tall := strings.Cut(block, "\n  source "); tall {
			// A tall page states the source and the windows under it.
			block = rest
		}
		return block, strings.Join(strings.Fields(block), " ")
	}
	const query = `sum(rate(http_requests_total{namespace="apps", pod=~"checkout-api-[a-z0-9]+-[a-z0-9]+", code=~"5.."}[120s]))`
	queries := func(frame string) string {
		block, _, found := strings.Cut(frame, "\n  changed ")
		require.True(t, found)
		// A query breaks after a comma: its lines are joined with a blank.
		return strings.ReplaceAll(strings.Join(strings.Fields(block), " "), `", `, `",`)
	}
	for _, size := range [][2]int{{132, 33}, {80, 22}, {60, 24}} {
		frame = drawnText(t, v, size[0], size[1])
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		block, flat := diff(frame)
		require.Contains(t, flat, "limits.memory 512Mi → 256Mi; env "+strings.Join(keys, " "), "%dx%d: whole, on as many lines as it needs", size[0], size[1])
		require.NotContains(t, block, "…", "%dx%d", size[0], size[1])
		require.Contains(t, queries(frame), strings.ReplaceAll(query, `", `, `",`), "%dx%d: and so are the queries", size[0], size[1])
		require.Regexp(t, `(?m)^> Error ratio .*▁+┊█+$`, frame, "%dx%d: the row it belongs to keeps its strip", size[0], size[1])
	}
	// One row fewer has no room for both. The queries are what s is asked
	// for: they stay whole, and the list is cut and marked. What the
	// revision changed is still said, by kind, in the answer.
	for _, size := range [][2]int{{80, 21}, {80, 20}, {60, 23}} {
		frame = drawnText(t, v, size[0], size[1])
		block, _ := diff(frame)
		require.Regexp(t, `^\n  changed  image v1 → v2; limits\.memory 512Mi → 256Mi; env .*…$`, block, "%dx%d: one line, and the cut is marked", size[0], size[1])
		require.Contains(t, queries(frame), strings.ReplaceAll(query, `", `, `",`), "%dx%d", size[0], size[1])
		require.NotContains(t, strings.SplitN(frame, "\n  changed ", 2)[0], "  query    sum(rate(http_requests_total{namespace=\"apps\",pod=~\"checkout-api…", "%dx%d", size[0], size[1])
		require.Contains(t, frame, strings.ReplaceAll(changed, " (512Mi → 256Mi)", map[bool]string{true: "", false: " (512Mi → 256Mi)"}[size[0] == 60]), "%dx%d", size[0], size[1])
		require.Regexp(t, `(?m)^> Error ratio .*▁+┊█+$`, frame, "%dx%d", size[0], size[1])
	}
	// A screen with no room for either under a row with its strip has
	// one line of each, and marks every cut.
	for _, height := range []int{20, 16} {
		frame = drawnText(t, v, 60, height)
		require.Regexp(t, `(?m)^  changed  image v1 → v2; limits\.memory 512Mi → 256Mi; env .*…$`, frame, "60x%d", height)
		require.Regexp(t, `(?m)^  query    sum\(rate\(.*…$`, frame, "60x%d", height)
		require.Regexp(t, `(?m)^> Error ratio +0\.2% → 14\.2% `, frame, "60x%d: the row it belongs to stays", height)
	}

	// A revision with nothing to diff says so in both places.
	read = impactStoryRead(t, impactStory())
	read.report.Changes, read.report.MemoryLimit = nil, ""
	v.accept(read)
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, "  Rev 2 changed nothing in images, env keys, resources or commands.")
	require.Contains(t, frame, "  changed  nothing in images, env keys, resources or commands\n")
	read.report.Rollout.Previous = nil
	v.accept(read)
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, "  Rev 2 is the earliest retained revision; there is no earlier one to diff.")
	require.Contains(t, frame, "  changed  not known: rev 2 is the earliest retained revision\n")
}
