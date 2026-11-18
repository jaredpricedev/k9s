// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"regexp"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/impact"
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
// itself is under s, whole at every size: it is cut only for a page that
// would otherwise lose its strips or its next line.
func TestImpactCalmShowsTheWholeDiffUnderS(t *testing.T) {
	v := impactViewFixture(t)
	read := impactStoryRead(t, impactStory())
	keys := []string{"~PAYMENT_TOKEN", "+FEATURE_FLAGS", "+CHECKOUT_TIMEOUT_MS", "+LEDGER_ENDPOINT", "-LEGACY_GATEWAY_URL"}
	read.report.Changes = []string{"image v1 -> v2", "limits.memory 512Mi -> 256Mi", "env " + strings.Join(keys, " ")}
	v.accept(read)
	frame := drawnText(t, v, 80, 21)
	require.Contains(t, frame, "  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 5 env vars.\n")
	for _, key := range keys {
		require.NotContains(t, frame, key[1:], "the names are not in the answer")
	}

	impactPress(t, v, impactQueriesKey)
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		frame = drawnText(t, v, size[0], size[1])
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		at := strings.Index(frame, "\n  changed  image v1 → v2; ")
		require.Positive(t, at, "%dx%d\n%s", size[0], size[1], frame)
		block, _, found := strings.Cut(frame[at:], "\n  next ")
		require.True(t, found)
		if size[0] == 132 {
			// A tall page states the source and the windows under it.
			block, _, found = strings.Cut(block, "\n  source ")
			require.True(t, found)
		}
		flat := strings.Join(strings.Fields(block), " ")
		require.Contains(t, flat, "limits.memory 512Mi → 256Mi; env "+strings.Join(keys, " "), "%dx%d: whole, on as many lines as it needs", size[0], size[1])
		require.NotContains(t, block, "…", "%dx%d", size[0], size[1])
		require.Regexp(t, `(?m)^> Error ratio .*▁+┊█+$`, frame, "%dx%d: the row it belongs to keeps its strip", size[0], size[1])
	}
	// A screen with no room for it under a row with its strip cuts it,
	// and marks the cut.
	frame = drawnText(t, v, 60, 16)
	require.Regexp(t, `(?m)^  changed  image v1 → v2; limits\.memory 512Mi → 256Mi; env .*…$`, frame)

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
