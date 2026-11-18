// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// drawnCells draws a view and returns the runes and the background colour
// of every cell: a band is a background, not a character.
func drawnCells(t *testing.T, view tview.Primitive, width, height int) (runes [][]rune, back [][]tcell.Color) {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(width, height)
	view.SetRect(0, 0, width, height)
	view.Draw(screen)
	for y := range height {
		runes, back = append(runes, make([]rune, width)), append(back, make([]tcell.Color, width))
		for x := range width {
			r, _, style, _ := screen.GetContent(x, y)
			_, bg, _ := style.Decompose()
			runes[y][x], back[y][x] = r, bg
		}
	}
	return runes, back
}

// impactGroupAt finds the label row of a group or its folded line.
var impactGroupAt = regexp.MustCompile(`^ {2}[A-Z][A-Z ]+[A-Z]( |$)`)

// impactAnswerLines are the lines of the answer on a drawn calm page: the
// lines between the title and the marker row of the rollout, or the first
// group when there is no marker row, without the blank ones.
func impactAnswerLines(t *testing.T, frame string) []string {
	t.Helper()
	var out []string
	lines := strings.Split(frame, "\n")
	require.Greater(t, len(lines), 1)
	for _, line := range lines[1:] {
		if strings.HasSuffix(line, "▼") || strings.HasSuffix(line, " v") || impactGroupAt.MatchString(line) ||
			strings.HasPrefix(line, "  next ") {
			return out
		}
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// The calm page: the answer first, as a person would say it, with what
// the revision changed under it; then one row per signal that moved, and
// one line each for what did not move and what could not be compared.
// 132x33, 80x21 and 60x21 are what the application leaves the view in
// terminals of 132x36, 80x24 and 60x24; one row less where it draws its
// line of crumbs, which TestImpactCalmFoldsTheNeutralRowsBeforeItGivesABlankRowUp
// covers.
func TestImpactCalmPageStatesTheAnswerFirstAtEverySize(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	require.False(t, v.allDetail, "the calm page is the default")
	const (
		lead    = "7 signals got worse after rev 2 rolled out at 04:20"
		kinds   = ": errors, latency, restarts, memory and ready replicas."
		changed = "Rev 2 changed the image, the memory limit"
	)
	for _, size := range [][2]int{{132, 33}, {120, 34}, {80, 21}, {60, 21}, {40, 8}} {
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
		require.True(t, strings.HasPrefix(lines[0], "Impact  Deployment apps/"), lines[0])
		require.Contains(t, lines[0], "rev 2 (latest of 2)")
		// The answer comes first: what got worse, then what the revision
		// changed, each on one line, then the evidence. The rows under it
		// carry every before and after value, so the answer repeats none.
		answer := impactAnswerLines(t, frame)
		require.GreaterOrEqual(t, len(answer), 2, "%d columns", size[0])
		require.True(t, strings.HasPrefix(answer[0], "  "+lead), "%d columns: %q", size[0], answer[0])
		require.True(t, strings.HasPrefix(answer[1], "  "+changed), "%d columns: %q", size[0], answer[1])
		for _, line := range answer {
			require.NotRegexp(t, `\d(%|ms|s)( |,|\.|$)`, line, "%d columns: no value of a row is said twice", size[0])
			require.NotContains(t, line, "After rev 2 (", "the sentence that lists the values is under a")
		}
		require.Contains(t, flat, impactOrderCaveat)
		at, evidence := strings.Index(frame, lead), strings.Index(frame, impactWorseLabel)
		require.True(t, 0 < at && at < strings.Index(frame, changed) && strings.Index(frame, changed) < evidence, "%d columns", size[0])
		// One blank row sets the answer apart from the evidence at every
		// size, and one the keys.
		require.Regexp(t, `(?m)`+regexp.QuoteMeta(impactOrderCaveat)+`\n\n +rev 2 .*▼$`, frame, "%d columns", size[0])
		require.Regexp(t, `(?m)^  NOT AVAILABLE  CPU throttled\n\n  next  `, frame, "%d columns", size[0])
		switch size[0] {
		case 132, 120:
			// Two lines: the kinds of signal are named, the memory limit
			// is stated, and the caveat is at the right of the last line.
			require.Equal(t, []string{"  " + lead + kinds}, answer[:1], "%d columns", size[0])
			require.Len(t, answer, 2, "%d columns", size[0])
			require.Regexp(t, `^  Rev 2 changed the image, the memory limit \(512Mi → 256Mi\) and 1 env var\. {3,}Order in time, not proof of cause\.$`, answer[1])
		case 80:
			// Three lines: each sentence is one line, in the fullest form
			// that fits one, and the caveat is the third.
			require.Equal(t, []string{"  " + lead + ".", "  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 1 env var.",
				"  " + impactOrderCaveat}, answer)
		case 60:
			require.Equal(t, []string{"  " + lead + ".", "  Rev 2 changed the image, the memory limit and 1 env var.", "  " + impactOrderCaveat}, answer)
		}
		// A signal, before -> after and its strip are one row: the value
		// from zero, with the rollout as a guide where the windows meet.
		require.Regexp(t, `(?m)^> Error ratio +0\.2% → 14\.2% .*▁+┊█+$`, frame, "%d columns", size[0])
		require.Regexp(t, `(?m)^  Latency p99 +248ms → 2\.5s .*▂+┊█+$`, frame, "%d columns", size[0])
		require.Regexp(t, `(?m)^  WORSE AFTER +before → after +┊$`, frame)
		// The rollout is marked over the strips, by a sign and not only by
		// a colour.
		require.Regexp(t, `(?m)^ +rev 2 .*▼$`, frame)
		// What did not move and what could not be compared is one line
		// each; missing is never a zero and never left out.
		require.Contains(t, frame, "  UNCHANGED  Request rate\n")
		require.Contains(t, frame, "  NOT AVAILABLE  CPU throttled\n")
		require.NotRegexp(t, `CPU throttled +0`, frame)
		// a is on the first key line: the second is the first to go.
		require.Regexp(t, `(?m)^  next  Enter graph this workload +a all 11 signals`, frame)
		// The table, its pane and the source line are under a and s.
		for _, gone := range []string{"SIGNAL", "VERDICT", "prometheus", "Gaps, not zeros", "step 15s"} {
			require.NotContains(t, frame, gone, "%d columns", size[0])
		}
		require.NotRegexp(t, `(?m)^  (strip|rule|samples|query|source|windows) `, frame)
		lower := strings.ToLower(frame)
		for _, banned := range []string{"because", "caused", "due to", "synthetic-flag"} {
			require.NotContains(t, lower, banned)
		}
		switch size[0] {
		case 132:
			require.Empty(t, v.dropped, "132x36 gives nothing up")
			require.Contains(t, lines[0], "read 04:32 · 9 of 9 queries")
			require.Contains(t, frame, "rev 2 rolled out 04:20:00Z · 12m each side")
			require.Regexp(t, `(?m)^> Error ratio +0\.2% → 14\.2% +\+14pp +x71 +▁`, frame)
			// A signal that fell is high before the rollout and low after it.
			require.Regexp(t, `(?m)^  Ready \(min\) +2 → 1 +-1 +-50% +█+┊▅+$`, frame)
			require.Regexp(t, `(?m)^  Memory/limit +36\.7% → 99\.2% +\+62\.5pp  \+170% +▄+┊█+$`, frame)
			// A restart in the step of the rollout keeps its mark.
			require.Regexp(t, `(?m)^  Restarts +0 → 6 +\+6 +new +·+▲·+$`, frame)
			require.Regexp(t, `(?m)^  HIGHER OR LOWER AFTER +┊\n  Memory peak +188Mi → 254Mi +\+66Mi +\+35% +▆+┊█+$`, frame)
			require.Contains(t, frame, "  next  Enter graph this workload    a all 11 signals    s query and rule    t timeline\n")
			require.Contains(t, frame, "  ↑↓ signal · 1-5 window · n/N later/earlier rollout · D Grafana · r re-read · Esc back\n")
		case 80:
			// Every signal that is worse keeps its row and its strip. The
			// neutral ones fold to one line before any blank row goes, and
			// the second key line goes before the blank rows that set the
			// answer and the keys apart.
			require.Contains(t, lines[0], "04:32 · 9/9")
			for _, signal := range []string{"Error ratio", "Latency p50", "Latency p95", "Latency p99", "Restarts", "Memory/limit", "Ready (min)"} {
				require.Regexp(t, `(?m)^[> ] `+strings.NewReplacer("(", `\(`, ")", `\)`).Replace(signal)+` .*[▁▂▄▅█·▲┊]{20,}$`, frame)
			}
			require.Contains(t, frame, "  HIGHER OR LOWER AFTER  Memory peak 188Mi → 254Mi · CPU usage 60m → 340m\n")
			require.Equal(t, []string{"HIGHER OR LOWER AFTER folded to one line", "blank rows around the verdict and the keys", "blank rows before groups",
				"the second key line"}, v.dropped)
		case 60:
			// The change column goes; every worse signal keeps its row and
			// its strip.
			require.Contains(t, frame, "rev 2 at 04:20:00Z")
			require.NotContains(t, frame, "+14pp")
			require.NotContains(t, frame, "not shown", "nothing is left out at 60x24")
			require.Contains(t, v.dropped, "what each number is compared with")
			// Three rows fewer: the two blank rows go, then the last rows
			// of the group (one more for the line that says so), counted
			// on a line that names the key for all of it.
			short := drawnText(t, v, 60, size[1]-3)
			require.Contains(t, short, "  not shown (a shows all): 2 of 7 rows of WORSE AFTER\n", "the line names the group whole")
			require.Contains(t, v.dropped, "2 of 7 rows of WORSE AFTER")
			require.Len(t, impactAnswerLines(t, short), 3, "the answer is whole")
		}
	}
}

// The rollout is one column through every row: the marker, the guide and
// the background of every line down to the axis, and a heavy tick on the
// axis stand in one column, and a strip has one level before it and
// another after it.
func TestImpactCalmRolloutIsOneBandThroughEveryRow(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	for _, size := range [][2]int{{132, 33}, {80, 21}} {
		runes, back := drawnCells(t, v, size[0], size[1])
		marker, column, axis := -1, -1, -1
		for y, row := range runes {
			for x, r := range row {
				switch {
				case r == '▼':
					marker, column = y, x
				case column >= 0 && x == column && (r == '┯' || r == '━'):
					axis = y
				}
			}
		}
		require.Greater(t, marker, 0, "the marker row")
		require.Greater(t, axis, marker+7, "the axis is under the rows")
		band := back[marker][column]
		require.NotEqual(t, back[marker][column-1], band, "the band is a surface of its own")
		for y := marker; y < axis; y++ {
			require.Equal(t, band, back[y][column], "row %d at %d columns", y, size[0])
			require.NotEqual(t, band, back[y][column-1], "the rollout is one column wide")
			require.NotEqual(t, band, back[y][column+1])
		}
		// The guide is a sign of its own in every line the band runs
		// through, so the split is seen without colour; only a restart in
		// the step of the rollout keeps its mark.
		rows, guides := 0, 0
		for y := marker + 1; y < axis; y++ {
			switch runes[y][column] {
			case '┊':
				guides++
			case '▲':
			default:
				require.Failf(t, "no guide", "row %d at %d columns: %q", y, size[0], string(runes[y]))
			}
			if before, after := runes[y][column-2], runes[y][column+2]; strings.ContainsRune("▁▂▃▄▅▆▇█", before) {
				rows++
				require.NotEqual(t, before, after, "row %d: one level before the rollout, another after it", y)
			}
		}
		require.Equal(t, axis-marker-2, guides, "every line but the row of the restarts")
		require.GreaterOrEqual(t, rows, 6, "the levelled rows of the page")
	}
}

func TestImpactCalmNoIconsUsesASCIIOnly(t *testing.T) {
	v := impactViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	v.accept(impactStoryRead(t, impactStory()))
	impactPress(t, v, impactQueriesKey)
	for _, size := range [][2]int{{132, 33}, {100, 30}, {80, 21}, {60, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, glyph := range []string{"│", "▁", "█", "▼", "▲", "·", "→", "…", "↑", "━", "┯", "─"} {
			require.NotContains(t, frame, glyph, "%d columns", size[0])
		}
		require.Regexp(t, `(?m)^ +rev 2 .* v$`, frame, "the rollout marker")
		require.Regexp(t, `(?m)^> Error ratio +0\.2% -> 14\.2% .*_{4,}\|#{4,}$`, frame, "low before the rollout guide, high after it")
	}
	frame := drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^  Ready \(min\) +2 -> 1 .*#{4,}\|={4,}$`, frame, "a signal that fell is high before the guide and lower after it")
	require.Regexp(t, `(?m)^  Restarts +0 -> 6 .*\.{4,}\^\.{4,}$`, frame)
	require.Regexp(t, `(?m)^ +[-+]+#[-+]+$`, frame, "the rollout is the heavy tick of the axis")
	require.Contains(t, frame, "  windows  before 04:08:00-04:20:00Z | after 04:20:00-04:32:00Z | 12m each")
}

// s shows the rule, the sample counts and the exact queries of the row
// under the cursor, the diff of the revision, and the source and windows
// of the comparison; a shows the table. The cursor steps through the rows.
func TestImpactCalmKeysShowTheDetailOnRequest(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	label := func(key tcell.Key) string {
		action, ok := v.actions.Get(key)
		if !ok {
			return ""
		}
		return action.Description
	}
	require.Equal(t, calmDetailLabel, label(calmDetailKey))
	require.Contains(t, label(impactQueriesKey), "Queries", "s is the queries behind the page, as in every chart view")
	require.Equal(t, "Next (later) rollout", label(ui.KeyN), "n stays the rollout")

	impactPress(t, v, impactQueriesKey)
	frame := drawnText(t, v, 132, 33)
	for _, shown := range []string{
		"  ERROR RATIO  0.2% → 14.2% · +14pp · x71 · worse\n",
		"  strip    the value from zero; in colour where a sample is further from the before value than the rule allows\n",
		"  rule     counts as changed from 0.1pp and 10% of before; needs 4 samples on each side\n",
		"  samples  49 before, 41 after; 7 just after the rollout left out (their 2m rate window spans it)\n",
		`  query    sum(rate(http_requests_total{namespace="apps",pod=~"checkout-api-[a-z0-9]+-[a-z0-9]+",code=~"5.."}[120s]))` + "\n",
		"  changed  image v1 → v2; limits.memory 512Mi → 256Mi; env +FEATURE_FLAGS\n",
		"  source   prometheus service monitoring/prometheus:9090 · step 15s · rates over 2m · http: pod=~",
		"  windows  before 04:08:00-04:20:00Z | after 04:20:00-04:32:00Z · 12m each (asked 30m; the rollout was 12m before this read)\n",
	} {
		require.Contains(t, frame, shown)
	}
	require.Less(t, strings.Index(frame, impactMissingLabel), strings.Index(frame, "ERROR RATIO"), "the detail stands under the evidence")
	require.Less(t, strings.Index(frame, "windows  before"), strings.Index(frame, "  next  "), "and over the keys")
	require.Regexp(t, `(?m)^> Error ratio .*█$`, frame, "the page it belongs to stays")
	// A short screen keeps the rule, the samples, the queries and the
	// diff, and the row they belong to.
	frame = drawnText(t, v, 80, 21)
	for _, shown := range []string{"  ERROR RATIO  0.2% → 14.2% · +14pp · x71 · worse\n", "  rule     counts as changed from 0.1pp", "  samples  49 before, 41 after", "  query    sum(rate(",
		"  changed  image v1 → v2; limits.memory 512Mi → 256Mi; env +FEATURE_FLAGS\n"} {
		require.Contains(t, frame, shown)
	}
	require.NotContains(t, frame, "  source ")
	require.Regexp(t, `(?m)^> Error ratio +0\.2% → 14\.2% `, frame)
	for _, line := range strings.Split(frame, "\n") {
		require.LessOrEqual(t, runewidth.StringWidth(line), 80, line)
	}

	// A query is read to be run again: where the page under it keeps its
	// strips and its next line, a query wider than the line is written on
	// as many lines as it needs and is not cut.
	whole := []string{
		`  query    sum(rate(http_requests_total{namespace="apps",` + "\n" + `           pod=~"checkout-api-[a-z0-9]+-[a-z0-9]+"}[120s]))` + "\n",
		`  query    sum(rate(http_requests_total{namespace="apps",` + "\n" + `           pod=~"checkout-api-[a-z0-9]+-[a-z0-9]+",code=~"5.."}[120s]))` + "\n",
	}
	for _, size := range [][2]int{{80, 21}, {80, 20}, {80, 30}} {
		frame = drawnText(t, v, size[0], size[1])
		for _, query := range whole {
			require.Contains(t, frame, query, "%dx%d", size[0], size[1])
		}
		require.Regexp(t, `(?m)^> Error ratio +0\.2% → 14\.2% .*▁+┊█+$`, frame, "%dx%d: the row keeps its strip", size[0], size[1])
		require.Contains(t, frame, "\n  next  Enter graph this workload", "%dx%d", size[0], size[1])
	}
	// Where whole queries would cost the row its strip they are one line
	// each, cut where the line ends and marked as cut.
	frame = drawnText(t, v, 60, 21)
	require.Contains(t, frame, `  query    sum(rate(http_requests_total{namespace="apps",po…`+"\n")
	require.Regexp(t, `(?m)^> Error ratio +0\.2% → 14\.2% .*▁+┊█+$`, frame)
	require.Equal(t, []string{"sum(rate(x{a=\"b\",", "c=\"d\"}[120s]))"}, impactQueryLines(`sum(rate(x{a="b",c="d"}[120s]))`, 20), "a query breaks after a comma where it can")
	require.Equal(t, []string{"aaaaaaaaaa", "aaaaa"}, impactQueryLines("aaaaaaaaaaaaaaa", 10), "and at the width where it cannot")

	// The cursor steps through the rows, top to bottom, and the detail
	// follows it.
	var seen []string
	for range 10 {
		impactPress(t, v, tcell.KeyDown)
		seen = append(seen, v.focused)
	}
	require.Equal(t, []string{impact.SigP50, impact.SigP95, impact.SigP99, impact.SigRestarts, impact.SigMemLimit, impact.SigReady,
		impact.SigMemory, impact.SigCPU, impact.SigCPU, impact.SigCPU}, seen, "worse first, then higher or lower; it stops at the last row")
	frame = drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^> CPU usage +60m → 340m `, frame)
	require.Contains(t, frame, "  CPU USAGE  60m → 340m · +280m · +467% · higher\n")
	require.Contains(t, frame, "  next  Enter graph CPU ")
	// On a short screen the group the cursor is in opens, and the page
	// counts what it left out for it.
	frame = drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^> CPU usage +60m → 340m `, frame)
	impactPress(t, v, tcell.KeyUp)
	require.Equal(t, impact.SigMemory, v.focused)
	impactPress(t, v, impactQueriesKey)
	require.NotContains(t, drawnText(t, v, 132, 33), "  rule ")

	// a: the table of every signal, with the cursor where it was; and back.
	impactDetail(t, v, true)
	frame = drawnText(t, v, 132, 33)
	for _, shown := range []string{"SIGNAL", "BEFORE", "VERDICT", "before│after", "Request rate", "unchanged", "Memory peak: 188Mi -> 254Mi", "rule: counts as changed from"} {
		require.Contains(t, frame, shown, "everything the view showed before is under a")
	}
	impactPress(t, v, tcell.KeyUp)
	impactPress(t, v, tcell.KeyUp)
	impactPress(t, v, tcell.KeyUp)
	impactPress(t, v, tcell.KeyUp)
	impactPress(t, v, tcell.KeyUp)
	impactPress(t, v, tcell.KeyUp)
	require.Equal(t, impact.SigRequests, v.focused, "in the table the cursor reaches every signal")
	require.Contains(t, drawnText(t, v, 132, 33), "Request rate: 40/s -> 42/s")
	impactDetail(t, v, false)
	require.Equal(t, impact.SigErrors, v.focused, "back on the calm page the cursor is on a row")
	require.Regexp(t, `(?m)^> Error ratio `, drawnText(t, v, 132, 33))
}

func TestImpactCalmStatesWhatCouldNotBeRead(t *testing.T) {
	v := impactViewFixture(t)
	frame := drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^Impact  Deployment apps/checkout-api +reading…$`, frame)
	require.Contains(t, frame, "  Reading the rollout history, then the declared Prometheus source.\n  Bounded, read-only, and only because this view was opened.\n")

	// The source refused every query: the sentence says so, every signal
	// is named as not read, and nothing is drawn as a value.
	v.accept(impactStoryRead(t, &impactSource{status: observability.Status{State: observability.StateDenied, HTTPStatus: 403}}))
	frame = drawnText(t, v, 100, 30)
	lines := strings.Split(frame, "\n")
	require.Contains(t, lines[0], "read 04:32 · 0 of 9 queries")
	require.Equal(t, "  Nothing was read around rev 2, rolled out at 04:20: denied (HTTP 403).", lines[2])
	require.Equal(t, []string{"  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 1 env var.", "  " + impactUnreadCaveat}, lines[3:5],
		"what the revision changed needs no metrics source")
	require.Contains(t, frame, "  NOT READ  denied (HTTP 403): Request rate · Error ratio · Latency p50 · Latency p95 +7 more\n")
	require.Contains(t, frame, "  next  r read again    :sources show and probe the declaration    t timeline    a all 11 signals\n")
	require.Contains(t, frame, "  1-5 window · n/N later/earlier rollout · D Grafana · Esc back\n")
	require.Equal(t, 1, strings.Count(frame, "r read again"), "a key on the next line is not named again under it")
	require.NotContains(t, frame, "r re-read", "nor in the short form of the second key line")
	require.NotRegexp(t, `[▁█]`, frame)
	require.NotRegexp(t, `Request rate +0`, frame)
	require.NotContains(t, frame, "↑↓ signal", "there is no row to step through")
	require.Contains(t, drawnText(t, v, 60, 21), "  NOT READ  denied (HTTP 403): Request rate +10 more\n", "the line of what was not read is never left out")

	// Some queries failed: their signals are one line, with the reason.
	partial := impactStory()
	delete(partial.answers, "container_cpu_usage_seconds_total")
	v.accept(impactStoryRead(t, partial))
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, "  NOT AVAILABLE  CPU usage · CPU throttled\n")
	require.Regexp(t, `(?m)^> Error ratio `, frame)

	// A rollback has no time to compare around.
	rollback := impact.Rollout{Revision: impact.Revision{Number: 3, Name: "checkout-api-6c8d7f9b5"}, Total: 3}
	v.accept(&impactRead{workload: v.workload(), rollout: &rollback,
		err: errors.New("rev 3 reuses checkout-api-6c8d7f9b5: a rollback. Kubernetes does not record when a rollback happened")})
	frame = drawnText(t, v, 80, 21)
	require.Contains(t, frame, "rev 3 (latest of 3)")
	require.Regexp(t, `(?m)^Impact .* not read$`, frame)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, "No comparison: rev 3 reuses checkout-api-6c8d7f9b5: a rollback. Kubernetes does not record when a rollback happened.")
	require.Contains(t, frame, "  n and N step to another retained rollout.")
	require.Contains(t, frame, "No comparison is not a sign that nothing changed.")

	v.accept(&impactRead{err: errors.New("Pod apps/checkout-api-0 has no controller, so it has no rollout to compare around")})
	frame = drawnText(t, v, 100, 24)
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "No comparison: Pod apps/checkout-api-0 has no controller, so it has no rollout to compare around.")
	require.Contains(t, frame, "  r re-read · Esc back\n")
	require.NotContains(t, frame, "n/N", "a key with nothing to step through is not named")
}

// Without a source nothing was asked: the title says "not read", the
// rollout is still stated, and s says why there is no value to show.
func TestImpactCalmWithoutASourceStillStatesTheRollout(t *testing.T) {
	read := readImpact(t.Context(), impactCluster(t), nil, nil, impact.KindDeployment, "apps", "checkout-api", 0, impact.DefaultWidth)
	require.NoError(t, read.err)
	v := impactViewFixture(t)
	v.accept(&read)
	frame := drawnText(t, v, 100, 30)
	require.Regexp(t, `(?m)^Impact  Deployment apps/checkout-api · rev 2 \(latest of 2\) +not read$`, frame)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Regexp(t, `Nothing was read around rev 2, rolled out at \d\d:\d\d: no Prometheus source is declared for this context; :sources shows how to declare one\.`, flat)
	require.Contains(t, frame, "  Rev 2 changed the image, the memory limit (512Mi → 256Mi) and 1 env var.\n")
	require.Contains(t, frame, impactUnreadCaveat)
	impactPress(t, v, impactQueriesKey)
	frame = drawnText(t, v, 100, 30)
	require.Contains(t, frame, "  Nothing was read, so nothing is shown as a value. That is not a zero and not a sign of health.\n")
	require.Contains(t, frame, "  changed  image v1 → v2; limits.memory 512Mi → 256Mi; env +FEATURE_FLAGS\n", "the diff itself is under s")
	require.Contains(t, frame, "  source   prometheus: not read\n")
}

// A page with every kind of signal: what is worse first and in its
// colour's group, what is better and what only moved under it, what did
// not move and what is missing as one line each. A strip is the value from
// zero, so a signal that fell is high before the rollout and low after it.
func TestImpactCalmSeparatesWorseBetterAndNeutral(t *testing.T) {
	v := impactViewFixture(t)
	story := &impactSource{answers: map[string][2]float64{
		"http_requests_total": {40, 42},
		`code=~"5.."`:         {5.964, 0.08},
		"kube_pod_container_status_restarts_total":  {3, 3},
		"max(container_memory_working_set_bytes":    {254 << 20, 188 << 20},
		"kube_pod_container_resource_limits":        {0.992, 0.367},
		"container_cpu_usage_seconds_total":         {0.34, 0.06},
		"kube_deployment_status_replicas_available": {1, 2},
	}}
	v.accept(impactStoryRead(t, story))
	frame := drawnText(t, v, 132, 33)
	require.Equal(t, "  Latency got worse after rev 2 rolled out at 04:20: p50, p95 and p99.", strings.Split(frame, "\n")[2], "one kind is said by its name")
	worse, better, moved := strings.Index(frame, impactWorseLabel), strings.Index(frame, impactBetterLabel), strings.Index(frame, impactMovedLabel)
	require.True(t, 0 < worse && worse < better && better < moved)
	require.Regexp(t, `(?m)^> Latency p50 +66\.7ms → 593ms .*▂+┊█+$`, frame, "the cursor starts on the first signal that is worse")
	require.Regexp(t, `(?m)^  Error ratio +14\.9% → 0\.19% +-14\.7pp  -99% +█+┊▁+$`, frame, "a fall is drawn as a fall")
	require.Regexp(t, `(?m)^  Ready \(min\) +1 → 2 +\+1 +\+100% +▅+┊█+$`, frame)
	require.Regexp(t, `(?m)^  Memory peak +254Mi → 188Mi +-66Mi +-26% +█+┊▆+$`, frame)
	require.Contains(t, frame, "  UNCHANGED  Request rate · Restarts\n")
	require.Contains(t, frame, "  NOT AVAILABLE  CPU throttled\n")
	// At 80x24 what the sentence rests on keeps its rows; the rest is one
	// line per group, by name and numbers.
	frame = drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^  Latency p99 +248ms → 2\.5s .*█$`, frame)
	require.Contains(t, frame, "  BETTER AFTER  Error ratio 14.9% → 0.19% · Memory/limit 99.2% → 36.7% +1 more\n")
	require.Contains(t, frame, "  HIGHER OR LOWER AFTER  Memory peak 254Mi → 188Mi · CPU usage 340m → 60m\n")

	// Nothing worse is not health: the sentence says how much was compared
	// and how much was not, and what is better is the rows of the page.
	story.steady = true
	v.accept(impactStoryRead(t, story))
	frame = drawnText(t, v, 132, 33)
	require.Equal(t, "  No compared signal got worse after rev 2 rolled out at 04:20: 3 better, 2 higher or lower, 5 unchanged, 1 not compared.",
		strings.Split(frame, "\n")[2], "every signal is counted, the one that was not compared too")
	// The short form drops the counts the groups state, never the signal
	// that was not compared.
	require.Equal(t, "  No compared signal got worse after rev 2 rolled out at 04:20; 1 not compared.", strings.Split(drawnText(t, v, 80, 21), "\n")[1])
	require.NotContains(t, frame, impactWorseLabel)
	require.Regexp(t, `(?m)^  BETTER AFTER +before → after +┊$`, frame)
	require.Regexp(t, `(?m)^> Error ratio +14\.9% → 0\.19% `, frame)
	require.Contains(t, frame, "  UNCHANGED  Request rate · Latency p50 · Latency p95 · Latency p99 · Restarts\n")
	require.Contains(t, frame, "  NOT AVAILABLE  CPU throttled\n")
	require.Contains(t, frame, impactOrderCaveat)
	require.NotContains(t, strings.ToLower(frame), "healthy")
	require.Regexp(t, `(?m)^> Error ratio +14\.9% → 0\.19% .*█+┊▁+$`, drawnText(t, v, 80, 21), "what is better keeps its rows at 80x24: it is what the sentence names")
}
