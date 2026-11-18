// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/calm/calmtest"
	"github.com/derailed/k9s/internal/chart"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	plainOpts = calm.Options{}
	asciiOpts = calm.Options{ASCII: true}
	colorOpts = calm.Options{Palette: calm.TestPalette}
)

var pages = map[string]func(calm.Options) calm.Page{
	"slow":        calmtest.Slow,
	"slow-unread": calmtest.SlowUnread,
	"graph":       calmtest.Graph,
	"compare":     calmtest.Compare,
}

func render(name string, width, height int, o calm.Options) (calm.Plan, calm.Rendered) {
	page := pages[name](o)
	plan := calm.Lay(&page, width, height, o)
	return plan, calm.Render(&page, &plan, o)
}

// The reference pages are pinned as text, one file a page and size, in
// testdata. The sizes are the areas a view is given in a terminal of
// 132x36, 80x24 and 60x24: the application keeps two rows at the top and
// one at the bottom. They are the acceptance text of the calm layout: a
// change to one is a change to what every incident view looks like, and
// is made on purpose with CALM_UPDATE=1.
func TestReferencePagesArePinned(t *testing.T) {
	for _, c := range []struct {
		page          string
		width, height int
		ascii         bool
	}{
		{"slow", 132, 33, false}, {"slow", 80, 21, false}, {"slow", 60, 21, true},
		{"graph", 132, 33, false}, {"graph", 80, 21, false}, {"graph", 60, 21, true},
		{"compare", 132, 33, false}, {"compare", 80, 21, false}, {"compare", 60, 21, true},
		{"slow-unread", 132, 33, false}, {"slow-unread", 80, 21, false}, {"slow-unread", 60, 21, true},
	} {
		name := fmt.Sprintf("%s-%dx%d", c.page, c.width, c.height)
		opts := plainOpts
		if c.ascii {
			name += "-ascii"
			opts = asciiOpts
		}
		t.Run(name, func(t *testing.T) {
			_, page := render(c.page, c.width, c.height, opts)
			got := strings.Join(calm.Plain(page.Lines()), "\n") + "\n"
			path := filepath.Join("testdata", name+".txt")
			if os.Getenv("CALM_UPDATE") != "" {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(want), got)

			// With the palette the page reads the same: colour adds
			// nothing the text does not say.
			colors := colorOpts
			colors.ASCII = c.ascii
			_, colored := render(c.page, c.width, c.height, colors)
			assert.Equal(t, calm.Plain(page.Lines()), calm.Plain(colored.Lines()))
			var drawn []string
			for _, row := range calm.Screen(t, colored.Lines(), c.width) {
				var b strings.Builder
				for _, cell := range row {
					if cell.R == 0 {
						cell.R = ' '
					}
					b.WriteRune(cell.R)
				}
				drawn = append(drawn, strings.TrimRight(b.String(), " "))
			}
			assert.Equal(t, calm.Plain(page.Lines()), drawn, "tview draws exactly the text: every tag is a tag")
		})
	}
}

// Every page fits every area it is laid out for, in cells, and says what
// it left out.
func TestReferencePagesFitEveryArea(t *testing.T) {
	for name := range pages {
		for _, opts := range []calm.Options{plainOpts, asciiOpts} {
			for width := 20; width <= 170; width += 5 {
				for height := 4; height <= 44; height += 3 {
					plan, page := render(name, width, height, opts)
					lines := calm.Plain(page.Lines())
					where := fmt.Sprintf("%s %dx%d ascii=%v", name, width, height, opts.ASCII)
					require.LessOrEqual(t, len(lines), max(height, 3), where)
					for _, line := range lines {
						require.LessOrEqual(t, runewidth.StringWidth(line), width, "%s: %q", where, line)
						if opts.ASCII {
							require.Equal(t, -1, strings.IndexFunc(line, func(r rune) bool { return r > 127 }), "%s: %q", where, line)
						}
					}
					text := strings.Join(lines, "\n")
					if plan.TooSmall {
						require.Contains(t, text, "a shows all", where)
						continue
					}
					// What is not on the screen is named on it, with the
					// key that shows it.
					// A line too narrow for the full note says it in
					// fewer words before it cuts the list.
					note := "not shown at this size (a shows all)"
					if !strings.Contains(text, note) {
						note = "not shown (a shows all)"
					}
					if width < 50 {
						note = note[:9] // a narrow line cuts the note, never drops it
					}
					require.Equal(t, len(plan.Missing) > 0, strings.Contains(text, note), where)
					if full := "  " + plan.Note(); len(plan.Missing) > 0 && runewidth.StringWidth(full) <= width {
						require.Contains(t, text, full, "%s: the full note is written where it fits", where)
					}
					// Missing data is never left out: the not-read group
					// is on every page that is not too small for any.
					if name == "slow-unread" {
						require.Contains(t, text, "NOT READ", where)
						if width >= 40 {
							require.Contains(t, text, "postgresql", where)
						}
					}
					// The strips, when there are any, are wide enough to
					// say when.
					if !plan.TextRows {
						require.GreaterOrEqual(t, plan.Layout.Strip, calm.MinStrip, where)
						require.Equal(t, plan.Layout.Strip, plan.Frame.Width, where)
					}
				}
			}
		}
	}
}

func TestWhatASmallerAreaGivesUp(t *testing.T) {
	for _, c := range []struct {
		page          string
		width, height int
		dropped       []string
		missing       []string
	}{
		{page: "slow", width: 132, height: 30},
		{page: "slow", width: 132, height: 26, dropped: []string{
			"blank rows around the verdict and the keys",
		}},
		{page: "slow", width: 132, height: 24, dropped: []string{
			"blank rows around the verdict and the keys",
			"QUIET folded to one line",
		}},
		{page: "slow", width: 132, height: 20, dropped: []string{
			"blank rows around the verdict and the keys",
			"QUIET folded to one line",
			"blank rows between subjects",
			"blank rows before groups",
		}},
		{page: "slow", width: 80, height: 20, dropped: []string{
			"blank rows around the verdict and the keys",
			"QUIET folded to one line",
			"blank rows between subjects",
			"blank rows before groups",
			"DIFFERS, BUT NOT DURING A STALL folded to one line",
		}},
		{page: "slow", width: 60, height: 20, dropped: []string{
			"blank rows around the verdict and the keys",
			"QUIET folded to one line",
			"blank rows between subjects",
			"blank rows before groups",
			"DIFFERS, BUT NOT DURING A STALL folded to one line",
			"the second key line",
			"what each number is compared with",
		}},
		{page: "slow", width: 60, height: 14, dropped: []string{
			"blank rows around the verdict and the keys",
			"QUIET folded to one line",
			"blank rows between subjects",
			"blank rows before groups",
			"DIFFERS, BUT NOT DURING A STALL folded to one line",
			"the second key line",
			"the blank rows under the verdict and over the keys",
			"4 of 5 rows of LINES UP WITH THE STALLS",
			"QUIET",
			"what each number is compared with",
		}, missing: []string{"4 of 5 rows of LINES UP WITH THE STALLS", "QUIET"}},
		{page: "slow", width: 36, height: 30, dropped: []string{
			"the time strips",
			"blank rows around the verdict and the keys",
		}, missing: []string{"the time strips"}},
		{page: "graph", width: 132, height: 30},
		{page: "graph", width: 80, height: 20, dropped: []string{
			"blank rows around the verdict and the keys",
		}},
		{page: "graph", width: 80, height: 16, dropped: []string{
			"blank rows around the verdict and the keys",
			"blank rows before groups",
			"ALSO folded to one line",
		}},
		{page: "graph", width: 80, height: 13, dropped: []string{
			"blank rows around the verdict and the keys",
			"blank rows before groups",
			"ALSO folded to one line",
			"the second key line",
			"the blank rows under the verdict and over the keys",
		}},
		{page: "graph", width: 80, height: 12, dropped: []string{
			"blank rows around the verdict and the keys",
			"blank rows before groups",
			"ALSO folded to one line",
			"the second key line",
			"the blank rows under the verdict and over the keys",
			"ALSO",
			"the next line",
		}, missing: []string{"ALSO"}},
		{page: "compare", width: 80, height: 20, dropped: []string{
			"blank rows around the verdict and the keys",
			"blank rows before groups",
			"the second key line",
			"the blank rows under the verdict and over the keys",
			"1 of 3 rows of REQUESTS WAITING FOR A THREAD",
			"1 of 3 rows of CPU THROTTLED",
		}, missing: []string{"1 of 3 rows of REQUESTS WAITING FOR A THREAD", "1 of 3 rows of CPU THROTTLED"}},
	} {
		t.Run(fmt.Sprintf("%s %dx%d", c.page, c.width, c.height), func(t *testing.T) {
			plan, _ := render(c.page, c.width, c.height, plainOpts)
			assert.False(t, plan.TooSmall)
			assert.Equal(t, c.dropped, plan.Dropped)
			assert.Equal(t, c.missing, plan.Missing)
		})
	}
}

// A page with a main chart ends as the chart alone; a page of rows as its
// first group; and a page that has no room for either says so.
func TestTheSmallestForms(t *testing.T) {
	plan, page := render("graph", 80, 12, plainOpts)
	assert.Equal(t, []bool{true}, plan.Hidden)
	assert.Equal(t, 4, plan.ChartHeight)
	assert.Equal(t, []string{
		"Graph  Pod checkout-api-7d9f8c6b5d-x2x9q · last 15m                15:00 · 10/10",
		"  Memory reached its 256Mi limit 6 times. The container restarted after each.",
		"  Order in time, not proof of cause.",
		"  MEMORY  254Mi now  at limit   - limit 256Mi   . request 128Mi",
	}, calm.Plain(page.Lines())[:4])
	assert.Contains(t, calm.Plain(page.Lines()), "  not shown at this size (a shows all): ALSO")

	plan, page = render("slow", 60, 12, plainOpts)
	assert.False(t, plan.TooSmall)
	assert.Equal(t, []int{1, 1, 3}, plan.Rows, "the first row of the first group stays")
	assert.Contains(t, strings.Join(calm.Plain(page.Lines()), "\n"), "ingress")

	plan, page = render("slow", 40, 8, plainOpts)
	assert.True(t, plan.TooSmall)
	lines := calm.Plain(page.Lines())
	assert.LessOrEqual(t, len(lines), 8)
	assert.True(t, strings.HasPrefix(lines[0], "Slow requests"), "the title stays")
	assert.True(t, strings.HasPrefix(lines[1], "  3 stalls:"), "and as much of the verdict as fits")
	assert.Equal(t, "a shows all detail: no room for the evi…", lines[len(lines)-1])
}

func TestBandsRunThroughTheBlockAndStopAtTheAxis(t *testing.T) {
	plan, page := render("slow", 132, 30, colorOpts)
	cells := calm.Screen(t, page.Lines(), 132)
	lines := calm.Plain(page.Lines())
	shaded := func(row []calm.Cell) []int {
		var out []int
		for col, cell := range row {
			if cell.Bg == calm.TestPalette.Band {
				out = append(out, col)
			}
		}
		return out
	}
	marker, axis := -1, -1
	for y, line := range lines {
		if strings.Contains(line, "stalls  ") && strings.Contains(line, "▼") {
			marker = y
		}
		if strings.Contains(line, "┬") {
			axis = y
		}
	}
	require.Positive(t, marker)
	require.Greater(t, axis, marker)
	want := shaded(cells[marker])
	require.Len(t, want, 14, "three stalls of 3, 4 and 2 minutes in a strip of 73 columns")
	for _, col := range want {
		assert.Equal(t, '▼', cells[marker][col].R, "a marker over every shaded column: the band reads without colour")
		assert.GreaterOrEqual(t, col, plan.Layout.StripCol)
	}
	for y := marker; y < axis; y++ {
		assert.Equal(t, want, shaded(cells[y]), "line %d: %q", y, lines[y])
	}
	for y := range cells {
		if y < marker || y >= axis {
			assert.Empty(t, shaded(cells[y]), "line %d: %q", y, lines[y])
		}
	}
	// On the axis a band is a heavy rule instead.
	for _, col := range want {
		assert.Contains(t, "━┯", string(cells[axis][col].R))
	}

	// What lines up is inside the band: every column of pod hx4mt that is
	// over its rule is a shaded column, and every shaded column of its
	// first row is over its rule.
	for y := marker; y < axis; y++ {
		for col, cell := range cells[y] {
			if cell.Fg == calm.TestPalette.Fail && strings.ContainsRune("▂▃▄▅▆▇█", cell.R) {
				assert.Contains(t, want, col, "line %d", y)
			}
		}
	}
	row := -1
	for y, line := range lines {
		if strings.Contains(line, "response time p99") && strings.Contains(line, "pod hx4mt") {
			row = y
		}
	}
	require.Greater(t, row, marker)
	for _, col := range want {
		assert.Equal(t, calm.Cell{R: '█', Fg: calm.TestPalette.Fail, Bg: calm.TestPalette.Band}, cells[row][col])
	}
	assert.Equal(t, calm.Cell{R: '>', Fg: calm.TestPalette.Accent, Bold: true}, cells[row][0], "the selected row")
}

// continued is a row that does not repeat its subject.
var continued = regexp.MustCompile(`^ {13}[A-Za-z]`)

// One colour per subject on the whole screen.
func TestOneColourPerSubject(t *testing.T) {
	_, page := render("slow", 132, 30, colorOpts)
	cells := calm.Screen(t, page.Lines(), 132)
	palette := calm.TestPalette
	for y, line := range calm.Plain(page.Lines()) {
		seen := map[string]bool{}
		for _, cell := range cells[y] {
			if cell.Fg == palette.Fail || cell.Fg == palette.Warn || cell.Fg == palette.Info {
				seen[cell.Fg] = true
			}
		}
		switch {
		case strings.HasPrefix(line, "> pod hx4mt") || continued.MatchString(line):
			assert.Equal(t, map[string]bool{palette.Fail: true}, seen, line)
		case strings.HasPrefix(line, "  ingress") || strings.HasPrefix(line, "  LINES UP") || strings.Contains(line, "▼") || strings.HasPrefix(line, "  next"):
			assert.Equal(t, map[string]bool{palette.Warn: true}, seen, line)
		case strings.HasPrefix(line, "  network"):
			assert.Equal(t, map[string]bool{palette.Info: true}, seen, line)
		case strings.Contains(line, "┬"):
			assert.Equal(t, map[string]bool{palette.Warn: true}, seen, "the axis marks the bands in their colour")
		default:
			assert.Empty(t, seen, line)
		}
	}
}

func TestTheMainChartStandsOnTheSharedAxis(t *testing.T) {
	page := calmtest.Graph(colorOpts)
	var gotFrame chart.Frame
	var gotGutter, gotHeight, calls int
	draw := page.Chart.Draw
	page.Chart.Draw = func(f chart.Frame, gutter, height int) []string {
		gotFrame, gotGutter, gotHeight = f, gutter, height
		calls++
		return draw(f, gutter, height)
	}
	plan := calm.Lay(&page, 132, 30, colorOpts)
	assert.Zero(t, calls, "planning a page draws nothing")
	rendered := calm.Render(&page, &plan, colorOpts)
	assert.Equal(t, 1, calls, "the chart is drawn once")
	assert.Equal(t, plan.ChartHeight, gotHeight)
	assert.Equal(t, 10, gotHeight)
	assert.Equal(t, plan.Layout.StripCol-2, gotGutter, "the plot starts where the strips do")
	assert.Equal(t, plan.Layout.Strip, gotFrame.Width)
	assert.Equal(t, calm.SampleFrame(plan.Frame), gotFrame)
	assert.Equal(t, plan.Frame.Start.Add(plan.Frame.Step/2), gotFrame.Start, "half a step on: a sample stands over the step that ends at it")

	// The first filled column of the plot is the first column of the CPU
	// strip that has a sample.
	lines := calm.Plain(rendered.Lines())
	first := func(line, glyphs string) int {
		at := strings.IndexAny(line, glyphs)
		require.GreaterOrEqual(t, at, 0, line)
		return runewidth.StringWidth(line[:at])
	}
	var plot, strip string
	for _, line := range lines {
		if strings.HasPrefix(line, "         0 ┤") {
			plot = line
		}
		if strings.HasPrefix(line, "  CPU") {
			strip = line
		}
	}
	assert.Equal(t, first(strip, "▁▂▃▄▅▆▇█"), first(plot, "█"))
	assert.Equal(t, plan.Layout.StripCol, first(strip, "×"))

	// The guide of the rollout runs from the top of the chart to the axis,
	// where its key is.
	guide := -1
	for _, line := range lines {
		if at := strings.IndexRune(line, '┊'); at >= 0 {
			col := runewidth.StringWidth(line[:at])
			if guide < 0 {
				guide = col
			}
			assert.Equal(t, guide, col, line)
		}
	}
	require.Positive(t, guide)
	for _, line := range lines {
		if strings.Contains(line, "┬") {
			assert.Equal(t, guide, first(line, "R"))
		}
	}
	assert.Contains(t, lines, "  R rollout to rev 2 at 14:48 · memory limit changed 512Mi → 256Mi")
}

func TestRenderedPartsAndAPageWithoutAFrame(t *testing.T) {
	plan, page := render("slow", 132, 30, plainOpts)
	assert.True(t, strings.HasPrefix(calm.Plain([]string{page.Title})[0], "Slow requests  Service"))
	assert.Len(t, page.Verdict, 2)
	assert.Len(t, page.Keys, 2)
	assert.Len(t, page.Lines(), 28)
	assert.Equal(t, 73, plan.Layout.Strip)
	assert.Equal(t, []bool{false, false, false}, plan.Folded)

	// Without a window there is nothing to draw strips on: the rows are
	// text, and the page says the strips are not there.
	slow := calmtest.Slow(plainOpts)
	slow.Frame = chart.Frame{}
	bare := calm.Lay(&slow, 132, 40, plainOpts)
	assert.True(t, bare.TextRows)
	lines := calm.Plain(calm.Render(&slow, &bare, plainOpts).Lines())
	assert.Contains(t, lines, "> pod hx4mt  response time p99      47.9s  peers 0.35s")
	assert.NotContains(t, strings.Join(lines, "\n"), "▼")

	// A page of nothing but a verdict.
	empty := calm.Page{Title: calm.Title{View: "Probe"}, Verdict: calm.Unread("Nothing was sent.", "")}
	plan = calm.Lay(&empty, 60, 10, plainOpts)
	assert.False(t, plan.TooSmall)
	assert.Equal(t, []string{
		"Probe                                               not read",
		"",
		"  Nothing was sent.                 Not read is not healthy.",
	}, calm.Plain(calm.Render(&empty, &plan, plainOpts).Lines()))
}

// An area a view does not have yet (tview draws a view before it has a
// size) or that is a single cell is laid out without a panic, and never
// with more than the title and the line that names the key.
func TestNoAreaIsTooSmallToAsk(t *testing.T) {
	for name := range pages {
		for _, width := range []int{-1, 0, 1, 2, 7} {
			for _, height := range []int{-1, 0, 1, 2, 3} {
				plan, page := render(name, width, height, colorOpts)
				assert.True(t, plan.TooSmall, "%s %dx%d", name, width, height)
				assert.LessOrEqual(t, len(page.Lines()), max(height, 2), "%s %dx%d", name, width, height)
				for _, line := range calm.Plain(page.Lines()) {
					assert.LessOrEqual(t, runewidth.StringWidth(line), max(width, 0), "%s %dx%d: %q", name, width, height, line)
				}
			}
		}
	}
	assert.Equal(t, chart.Frame{}, calm.SampleFrame(chart.Frame{}), "no frame, nothing to move")
}
