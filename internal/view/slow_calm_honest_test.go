// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

// slowGapped answers as the story does, without the samples of one stretch:
// a source that was down, or a scrape that failed, for that long.
type slowGapped struct {
	story    *slowtest.Story
	from, to time.Time
}

func (g slowGapped) QueryRange(ctx context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	res := g.story.QueryRange(ctx, e, start, end)
	for i := range res.Series {
		kept := res.Series[i].Samples[:0:0]
		for _, sample := range res.Series[i].Samples {
			if sample.Timestamp.Before(g.from) || sample.Timestamp.After(g.to) {
				kept = append(kept, sample)
			}
		}
		res.Series[i].Samples = kept
	}
	return res
}

// A stretch without samples is not a quiet stretch. Every row that is read
// from samples draws it as not read: the rows that line up, the quiet hops,
// the Pods that stayed under their rules and the restarts, whose history is
// the restart counter.
func TestSlowCalmPageNeverDrawsAStretchWithoutSamplesAsQuiet(t *testing.T) {
	path := slowtest.Path()
	plan := slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	// Between the first two stalls of the story, clear of both.
	gap := slowGapped{story: &slowtest.Story{}, from: slowtest.Now.Add(-46 * time.Minute), to: slowtest.Now.Add(-36 * time.Minute)}
	v := slowViewFixture(t)
	v.accept(&slowRead{path: path, report: slowpath.Evaluate(plan, slowpath.Collect(t.Context(), gap, plan), nil, slowtest.Now)})
	frame := drawnText(t, v, 132, 32)
	marker, _ := slowLineWith(t, frame, "stalls ")
	require.Regexp(t, `^ +stalls +▼+ +▼+ +▼+$`, marker, "the three stalls of the story are still read")

	// The columns that lie wholly inside the stretch without samples.
	var inside []int
	for col := range v.frame.Width {
		from, to := v.frame.ColumnSpan(col)
		if from.After(gap.from) && to.Before(gap.to) {
			inside = append(inside, col)
		}
	}
	require.GreaterOrEqual(t, len(inside), 8)
	strip := func(line string) []rune {
		cells := []rune(line)
		return cells[len(cells)-v.frame.Width:]
	}
	for _, name := range []string{
		"  ingress ", "pod hx4mt  slow p99", "queueing", "no free threads", "CPU throttling", "not Ready",
		"  pods 2bq7k, 8wzpd", "  3 dependencies", "  dns  ", "  restarts",
	} {
		line, _ := slowRowWith(t, frame, name)
		cells := strip(line)
		for _, col := range inside {
			require.Equalf(t, "×", string(cells[col]), "%q draws column %d, which has no sample, as read:\n%s", strings.TrimSpace(name), col, line)
		}
		require.NotEqual(t, "×", string(cells[inside[0]-3]), "%q has samples before the stretch", strings.TrimSpace(name))
	}
	// What the API states with a time of its own is not read from samples.
	line, _ := slowRowWith(t, frame, "  rollouts")
	require.NotContains(t, line, "×")
	require.Regexp(t, `(?m)^  restarts +none where read, no data for \d+m\S* +·+×+·+$`, frame, "the row says how long nothing is known")
	require.Regexp(t, `(?m)^  rollouts +none in the last 1h +·+$`, frame, "what has no stretch without data keeps the line it had")

	// s on the row states the same in full.
	for v.calmKey != "restarts" {
		before := v.calmKey
		slowPress(t, v, tcell.KeyDown)
		drawnText(t, v, 132, 32)
		require.NotEqual(t, before, v.calmKey, "the restarts have a row of their own")
	}
	slowPress(t, v, ui.KeyS)
	require.Contains(t, drawnText(t, v, 132, 32), "nothing is known about that time")
}

// A stall the window cuts is not stated as if it had been seen whole: the
// answer says it was already raised when the window began, or still raised
// at its last sample, before it says what lines up.
func TestSlowCalmAnswerSaysWhenTheWindowCutsAStall(t *testing.T) {
	read := func(back, window time.Duration) *slowRead {
		path, now := slowtest.Path(), slowtest.Now.Add(-back)
		plan := slowpath.BuildPlan(slowtest.Sources(), path, window, now)
		return &slowRead{path: path, report: slowpath.Evaluate(plan, slowpath.Collect(t.Context(), &slowtest.Story{}, plan), nil, now)}
	}
	v := slowViewFixture(t)

	// The third stall of the story is 08:13 to 08:15; the window ends at 08:14.
	v.accept(read(6*time.Minute, 15*time.Minute))
	frame := drawnText(t, v, 132, 32)
	lines := strings.Split(frame, "\n")
	require.Equal(t, "  1 stall: clients gave up waiting at 08:13:15 UTC, for 1 minute. It was still raised at the last sample: it may not be over.", lines[2])
	require.Regexp(t, `^  It lines up with .+ on pod hx4mt\. Nothing else lines up\.`, lines[3])
	marker, _ := slowLineWith(t, frame, "stall ")
	require.Regexp(t, `▼$`, marker, "the band runs to the last column of the strips")
	axis, _ := slowLineWith(t, frame, "━")
	require.Regexp(t, `[━┯]$`, axis, "and the axis is heavy to its end")

	// The second is 07:47 to 07:51; the window begins at 07:49.
	v.accept(read(16*time.Minute, 15*time.Minute))
	frame = drawnText(t, v, 132, 32)
	lines = strings.Split(frame, "\n")
	require.Equal(t, "  1 stall: clients gave up waiting at 07:49:15 UTC, for at least 1m45s. It was already raised when the window began.", lines[2])
	marker, _ = slowLineWith(t, frame, "stall ")
	require.Regexp(t, `^ +stall ▼`, marker, "the band begins in the first column of the strips")
	row, _ := slowRowWith(t, frame, "  ingress ")
	require.Equal(t, slowColsOf(marker, "▼"), slowColsOf(row, "█"), "under the marks of the lane it is counted from")

	// Narrow, it is a line of its own, before what lines up.
	narrow := drawnText(t, v, 80, 20)
	require.Contains(t, narrow, "  1 stall: clients gave up waiting at 07:49:15 UTC, for at least 1m45s.\n  It was already raised when the window began.\n"+
		"  It lines up with 5 signals on pod hx4mt. Nothing else lines up.\n")

	// A window that holds every stall whole adds nothing.
	v.accept(slowStoryRead(&slowtest.Story{}, time.Hour))
	require.NotContains(t, drawnText(t, v, 132, 32), "raised when the window began")
	require.NotContains(t, drawnText(t, v, 132, 32), "may not be over")
}
