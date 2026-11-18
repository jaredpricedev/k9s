// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/k9s/internal/ui"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// timelineHangView is the timeline of a Pod of a Service that stalls,
// opened from :slow: nothing retained from the last hour, and the stalls
// found there.
func timelineHangView(t *testing.T) *timelineView {
	t.Helper()
	v := timelineCalmFixture(t, nil)
	v.episodes = timelineStalls()
	v.acceptSnapshot(timelineHang(&v.scope), nil)
	return v
}

// timelineWithoutFailing is the incident without its failing signals: the
// changes, and what else is in the window.
func timelineWithoutFailing(t *testing.T) *timelineView {
	t.Helper()
	return timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		s := timelineIncident(scope, nil)
		var kept []timeline.Entry
		for i := range s.Entries {
			if !s.Entries[i].Failing {
				kept = append(kept, s.Entries[i])
			}
		}
		return timeline.NewSnapshot(scope, timelineAt, kept, s.Coverage)
	})
}

// An abnormal status, a failing node condition and a firing alert are
// states read at the capture. Their sources keep what is the case now and
// nothing of what ended: a Pod that :slow found not Ready twice in the hour
// leaves no entry once it is Ready again. With none of them read the page
// says "none now" and draws the window before the capture as not read. It
// never lists them with the kinds that keep a history, under a strip of
// quiet columns.
func TestTimelineCalmNeverDrawsAStateOfTheCaptureAsAQuietHour(t *testing.T) {
	v := timelineHangView(t)
	for _, size := range [][2]int{{132, 33}, {100, 30}, {80, 21}, {60, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		lines := strings.Split(frame, "\n")
		row := ""
		for _, line := range lines {
			if strings.HasPrefix(line, "  status · nodes · alerts ") {
				row = line
			}
		}
		if row != "" {
			_, axis := timelineLineWith(t, frame, "  UTC")
			width := len(timelineCols(lines[axis-1], "─┬━┯"))
			runes := []rune(row)
			require.Equal(t, strings.Repeat("×", width-1)+"·", string(runes[len(runes)-width:]),
				"%dx%d: only the column of the capture is read", size[0], size[1])
			require.Contains(t, row, "none now", "%dx%d", size[0], size[1])
		} else {
			require.Contains(t, frame, "status, nodes, alerts: none now", "%dx%d: folded, the line says the same\n%s", size[0], size[1], frame)
		}
		// No line names one of them over a strip of quiet columns, and no
		// line of names lists one beside the kinds that keep a history.
		require.NotRegexp(t, `(?m)^.*\b(status|nodes|alerts)\b[^×\n]*  ·+$`, frame, "%dx%d", size[0], size[1])
		require.NotRegexp(t, `(rollouts|config|scaling|warnings|exits|annotations) · (status|nodes|alerts)\b`, frame, "%dx%d", size[0], size[1])
	}
	// The kinds that keep a history for the window are still quiet.
	frame := drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^  rollouts · config · scaling +none retained +·+$`, frame)
	require.Regexp(t, `(?m)^  warnings · exits +none retained +·+$`, frame)

	// A state that was read is a span from when it began: the kind is then
	// a row of its own, not a quiet line.
	incident := timelineIncidentView(t)
	frame = drawnText(t, incident, 132, 33)
	require.Regexp(t, `(?m)^  status +Progressing=False… +11:58  for 2m +▁+┊▁+█+$`, frame)
	require.Contains(t, frame, "  QUIET  scaling · nodes: none now\n", "no failing node condition now; scaling keeps the time it last scaled")
}

// A container status retains its last exit and counts the restarts before
// it. Those happened, at moments nothing states: the row counts them and
// draws the stretch before the retained exit as not read. A container that
// exited once has nothing before its mark that is not known.
func TestTimelineCalmDrawsRestartsWithoutATimeAsNotRead(t *testing.T) {
	v := timelineIncidentView(t)
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		line, _ := timelineLineWith(t, strings.ReplaceAll(frame, "\n> ", "\n  "), "  exits ")
		require.Regexp(t, `  ×+▲·+$`, line, "%dx%d", size[0], size[1])
		require.NotContains(t, line, "┊", "%dx%d: the guide does not stand over what was not read", size[0], size[1])
	}
	frame := drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^  exits +api Error \(exit 1\) · pod x2x9q  11:58  6 times  `, frame)
	// The list still states the count as the model words it.
	v.moveRow(3)
	require.Equal(t, "exit", v.calmKey)
	frame = drawnText(t, v, 132, 37)
	require.Contains(t, frame, "container api Error (exit 1), restart count 6")
	require.Contains(t, frame, "Only the most recent exit is retained; 5 earlier restarts have no time.")

	once := timelineCalmFixture(t, func(scope *timeline.Scope) *timeline.Snapshot {
		s := timelineIncident(scope, nil)
		for i := range s.Entries {
			if e := &s.Entries[i]; e.Source == timeline.SourceRestart {
				e.Earlier, e.Summary, e.Detail = 0, "container api Error (exit 1), restart count 1", nil
			}
		}
		return s
	})
	frame = drawnText(t, once, 132, 33)
	require.Regexp(t, `(?m)^  exits +api Error \(exit 1\), restart… +11:58 +·+┊·+▲·+$`, frame, "one exit, one mark, and nothing unknown before it")
}

// What a row is about is cut by the view to the column the page gives it.
// The count of the other entries of the row is never what gives way, with
// noIcons either, where the mark of a cut is three cells; and a cut stands
// where the object begins or between two words when that keeps enough.
func TestTimelineWhatKeepsTheCountOfTheOtherEntries(t *testing.T) {
	plain, ascii := calm.Options{}, calm.Options{ASCII: true}
	for _, c := range []struct {
		text  string
		more  int
		width int
		o     calm.Options
		want  string
	}{
		{"BackOff · pod x2x9q", 0, 30, plain, "BackOff · pod x2x9q"},
		{"BackOff · pod x2x9q", 3, 30, plain, "BackOff · pod x2x9q +3"},
		{"CheckoutHighErrorRate (critical)", 4, 30, plain, "CheckoutHighErrorRate… +4"},
		{"CheckoutHighErrorRate (critical)", 4, 30, ascii, "CheckoutHighErrorRate... +4"},
		{"CheckoutHighErrorRate (critical)", 4, 16, plain, "CheckoutHigh… +4"},
		{"CheckoutHighErrorRate (critical)", 4, 16, ascii, "CheckoutHi... +4"},
		{"ScalingReplicaSet · deploy/checkout-api", 1, 30, plain, "ScalingReplicaSet… +1"},
		{"ScalingReplicaSet · deploy/checkout-api", 1, 30, ascii, "ScalingReplicaSet... +1"},
		{"BackOff · pod x2x9q", 0, 16, plain, "BackOff…"},
		{"rev 2: image v1 → v2; env ~PAYMENT_TOKEN +FEATURE_FLAGS", 0, 30, plain, "rev 2: image v1 → v2; env…"},
		{"rev 2: image v1 -> v2; env ~PAYMENT_TOKEN +FEATURE_FLAGS", 0, 30, ascii, "rev 2: image v1 -> v2; env..."},
		{"checkout-config updated by kubectl-edit", 0, 30, plain, "checkout-config updated by…"},
		{"rev 1 (earliest retained): webservice image registry.example.test", 0, 30, plain, "rev 1 (earliest retained)…"},
		{"rev 1 (earliest retained): webservice image registry.example.test", 0, 26, plain, "rev 1 (earliest retained)…"},
		{"Progressing=False ProgressDeadlineExceeded", 0, 30, plain, "Progressing=False…"},
		{"ProgressDeadlineExceededWithoutASpaceAnywhere", 0, 20, plain, "ProgressDeadlineExc…"},
		{"a b", 12, 4, plain, "+12"},
	} {
		got := timelineWhat(c.text, c.more, c.width, c.o)
		require.Equal(t, c.want, got, "%q in %d cells", c.text, c.width)
		require.LessOrEqual(t, runewidth.StringWidth(got), c.width, got)
	}

	// The page cuts a text for the widest column a row may have, and again
	// for the column the rows then share, which is as wide as the longest
	// of the texts so cut. The second cut ends where the first did: a row
	// does not lose a word for standing beside a shorter one.
	for _, text := range []string{
		"rev 1 (earliest retained): webservice image registry.example.test/gitlab-webservice-ee:v18.5.1",
		"rev 2: image v1 → v2; env ~PAYMENT_TOKEN +FEATURE_FLAGS; limits.memory 512Mi → 256Mi",
		"checkout-config updated by kubectl-edit", "CheckoutHighErrorRate (critical)", "api Error (exit 1), restart count 1 · pod x2x9q",
		"ScalingReplicaSet · deploy/checkout-api", "DiskPressure=False KubeletHasNoDiskPressure · node/fixture-node",
		"Progressing=False ProgressDeadlineExceeded", "ConfigMap apps/checkout-config edited",
	} {
		for _, o := range []calm.Options{plain, ascii} {
			for more := range 3 {
				first := timelineWhat(text, more, timelineWhatWidth, o)
				require.Equal(t, first, timelineWhat(text, more, runewidth.StringWidth(first), o), "%q +%d", text, more)
			}
		}
	}

	// On the page: a row of several entries says so at every width, with
	// icons and without, also where the rows are written as text because
	// the screen has no room for strips.
	for _, noIcons := range []bool{false, true} {
		v := timelineIncidentView(t)
		v.app.Config.K9s.UI.NoIcons = noIcons
		for width := ui.MinTaskWidth; width <= 160; width += 4 {
			frame := drawnText(t, v, width, 33)
			line, _ := timelineLineWith(t, strings.ReplaceAll(frame, "\n> ", "\n  "), "  alerts ")
			require.Regexp(t, `^  alerts +Che\S* \+2 +11:51( |$)`, line, "%d columns, noIcons %v\n%s", width, noIcons, frame)
			require.NotContains(t, line, "....", "%d columns: the mark of a cut is written once", width)
		}
	}
}

// The words of the quiet lines start at one column, whatever their names
// are and however narrow the screen: a line of names is never wider than
// the column the kit keeps for them, and a line whose words do not fit the
// column after it says them shorter instead of starting them elsewhere.
func TestTimelineCalmQuietLinesShareTheirColumns(t *testing.T) {
	for name, build := range map[string]func(t *testing.T) *timelineView{
		"hang, 1h": timelineHangView,
		"hang, 6h": func(t *testing.T) *timelineView {
			v := timelineHangView(t)
			v.setWindow(6 * time.Hour)
			return v
		},
		"no failing signal": timelineWithoutFailing,
	} {
		for _, width := range []int{132, 120, 100, 80, 72} {
			frame := drawnText(t, build(t), width, 40)
			lines := strings.Split(frame, "\n")
			_, quiet := timelineLineWith(t, frame, "  "+timelineQuietLabel)
			at, said := -1, 0
			for _, line := range lines[quiet+1:] {
				if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
					break // the axis, or a blank row: the group ended
				}
				// The words of a line are what stands between its names and
				// its strip.
				names, rest, ok := strings.Cut(line[2:], "  ")
				require.True(t, ok, line)
				rest = strings.TrimLeft(rest, " ")
				if strings.ContainsAny(rest[:1], "·×") {
					continue // a line of names alone
				}
				said++
				col := runewidth.StringWidth(line) - runewidth.StringWidth(rest)
				if at < 0 {
					at = col
				}
				require.Equal(t, at, col, "%s, %d columns: the words of %q start where the others do\n%s", name, width, names, frame)
				require.NotContains(t, rest[:strings.IndexAny(rest, "·×")], "…", "%s, %d columns: %q says its words whole", name, width, names)
			}
			require.Positive(t, said, "%s, %d columns\n%s", name, width, frame)
			if width >= 100 {
				require.GreaterOrEqual(t, said, 3, "%s, %d columns: every quiet line says what it is quiet about\n%s", name, width, frame)
			}
		}
	}
	// 80 columns: the column has no room for the long words, and the
	// lines say the same in fewer.
	v := timelineHangView(t)
	v.setWindow(6 * time.Hour)
	frame := drawnText(t, v, 80, 40)
	require.Regexp(t, `(?m)^  status · nodes · alerts +none now +×+·$`, frame)
	require.Regexp(t, `(?m)^  warnings +since 11:00 +×+·+$`, frame)
	require.Regexp(t, `(?m)^  rollouts · config · scaling  none retained +·+$`, frame)
}

// The keys are read apart from the entries: a list of four rows or more
// never stands directly on the next line, at any height. The one or two
// rows a short screen leaves the list are kept as they are.
func TestTimelineCalmKeysDoNotStandOnTheList(t *testing.T) {
	v := timelineIncidentView(t)
	spaced := 0
	for _, width := range []int{132, 100, 80} {
		for height := 21; height <= 46; height++ {
			lines := strings.Split(strings.TrimRight(drawnText(t, v, width, height), "\n"), "\n")
			require.LessOrEqual(t, len(lines), height, "%dx%d", width, height)
			heading, next := -1, -1
			for i, line := range lines {
				switch {
				case strings.HasPrefix(line, "  WARNINGS  "):
					heading = i
				case strings.HasPrefix(line, "  next  "):
					next = i
				}
			}
			require.Positive(t, next, "%dx%d", width, height)
			if heading < 0 {
				continue
			}
			rows := 0
			for _, line := range lines[heading:next] {
				if line != "" {
					rows++
				}
			}
			if lines[next-1] == "" {
				spaced++
			}
			if rows >= timelineListSpare {
				require.Empty(t, lines[next-1], "%dx%d: %d rows of list stand on the keys\n%s", width, height, rows, strings.Join(lines, "\n"))
			}
		}
	}
	require.Positive(t, spaced)
}

// Entries of one second have no order that is known. What began in the
// second of the moment the answer starts from began with it: the answer
// does not say that it followed, or that it did so "within 0s".
func TestTimelineCalmAnswerDoesNotOrderWhatBeganInOneSecond(t *testing.T) {
	v := timelineIncidentView(t)
	s := v.snapshot
	mark := func(summary string) string {
		t.Helper()
		for i := range s.Entries {
			if s.Entries[i].Summary == summary {
				s.SetReference(i)
				v.render()
				// The answer as one line, however the width breaks it.
				return strings.Join(strings.Fields(drawnText(t, v, 132, 33)), " ")
			}
		}
		t.Fatalf("no entry %q", summary)
		return ""
	}
	// The exit and the Deployment's status are of the same second.
	frame := mark("container api Error (exit 1), restart count 6")
	require.Contains(t, frame, "Your mark is at 11:58: api Error (exit 1), restart count 6 on pod x2x9q. ")
	require.Contains(t, frame, "1 failing signal began in the same second: Progressing=False ProgressDeadlineExceeded on deploy/checkout-api.")
	require.NotContains(t, frame, "followed")
	require.NotContains(t, frame, "within 0s")
	// One of two in a second, and one a minute later: those followed.
	frame = mark("KubePodCrashLooping (warning) began firing: Pod is crash looping.")
	require.Contains(t, frame, "4 failing signals followed within 6m, the first in the same second: ScalingActive=False FailedGetResourceMetric on hpa/checkout-api.")
	// A moment with nothing but its own second after it.
	for i := range s.Entries {
		if e := &s.Entries[i]; e.Source == timeline.SourceAlert && strings.HasPrefix(e.Summary, "KubePodNotReady") {
			e.At = timelineAt.Add(-2 * time.Minute)
		}
	}
	frame = mark("container api Error (exit 1), restart count 6")
	require.Contains(t, frame, "2 failing signals began in the same second, among them ")
	require.NotContains(t, frame, "within 0s")
}

// The page docs/resource-investigation.md shows is the pinned page of a
// 132x36 terminal, to the character: the document is read beside the
// screen, and a page that changed is changed there too.
func TestTimelineDocShowsThePinnedPage(t *testing.T) {
	pinned, err := os.ReadFile(filepath.Join("testdata", "timeline", "incident-132x33.txt"))
	require.NoError(t, err)
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "resource-investigation.md"))
	require.NoError(t, err)
	require.Contains(t, string(doc), "```text\n"+string(pinned)+"```\n")
}
