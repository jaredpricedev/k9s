// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// testPalette is the stock semantic palette, as a view hands it over.
var testPalette = Palette{Text: "#e1e7e3", Muted: "#a5b0aa", Accent: "#79c7d4", Warn: "#e7bd73",
	Fail: "#ef8278", Info: "#91b8ec", Mark: "#b6a3df", Band: "#16202a"}

var (
	plainOpts = Options{}
	colorOpts = Options{Palette: testPalette}
	asciiOpts = Options{ASCII: true}
)

var anyTag = regexp.MustCompile(`\[[a-zA-Z0-9#-]*:[a-zA-Z0-9#-]*:[a-z-]*\]`)

// plain is a line as it reads on the screen: without its tags.
func plain(line string) string {
	return strings.TrimRight(anyTag.ReplaceAllString(line, ""), " ")
}

func plainAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = plain(line)
	}
	return out
}

// shown is one cell of a drawn screen.
type shown struct {
	r         rune
	fg, bg    string
	bold, dim bool
}

// draw puts lines through tview the way a view does, on a simulated
// screen, and returns its cells. It is the proof that the tags are tags.
func draw(t *testing.T, lines []string, width int) [][]shown {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	height := max(len(lines), 1)
	screen.SetSize(width, height)
	view := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	view.SetTextColor(tcell.ColorDefault)
	view.SetBackgroundColor(tcell.ColorDefault)
	view.SetText(strings.Join(lines, "\n"))
	view.SetRect(0, 0, width, height)
	view.Draw(screen)
	hex := func(c tcell.Color) string {
		if c == tcell.ColorDefault || c.Hex() < 0 {
			return ""
		}
		return fmt.Sprintf("#%06x", c.Hex())
	}
	out := make([][]shown, height)
	for y := range out {
		out[y] = make([]shown, width)
		for x := range out[y] {
			r, _, style, _ := screen.GetContent(x, y)
			fg, bg, attrs := style.Decompose()
			out[y][x] = shown{r: r, fg: hex(fg), bg: hex(bg), bold: attrs&tcell.AttrBold != 0, dim: attrs&tcell.AttrDim != 0}
		}
	}
	return out
}

// text is a drawn row as text.
func text(row []shown) string {
	var b strings.Builder
	for _, c := range row {
		if c.r == 0 {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(c.r)
	}
	return strings.TrimRight(b.String(), " ")
}

func width(line string) int { return runewidth.StringWidth(plain(line)) }

var day = time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)

func minute(m int) time.Time { return day.Add(time.Duration(m) * time.Minute) }

// hourFrame is 14:00 to 15:00 in one-minute steps, one column a minute
// when it is 60 wide.
func hourFrame(width int) chart.Frame {
	return chart.Frame{Start: minute(0), End: minute(60), Step: time.Minute, Width: width}
}

func span(from, to int) chart.Interval {
	return chart.Interval{Start: minute(from), End: minute(to)}
}

// series is one sample a minute from the values given. A sample stands
// for the step that ends at it, so the first is taken at 14:01 and is
// drawn in the first column.
func series(values ...float64) []chart.Point {
	out := make([]chart.Point, len(values))
	for i, v := range values {
		out[i] = chart.Point{T: minute(i + 1), V: v}
	}
	return out
}

func fixedZone() *time.Location { return time.FixedZone("UTC+2", 2*3600) }

// right is a line with its end at the right edge of a width.
func right(left, end string, width int) string {
	return left + strings.Repeat(" ", width-runewidth.StringWidth(left)-runewidth.StringWidth(end)) + end
}

// at is the column of a text on a drawn row.
func at(t *testing.T, row []shown, what string) int {
	t.Helper()
	index := strings.Index(text(row), what)
	require.GreaterOrEqual(t, index, 0, "%q is not in %q", what, text(row))
	return runewidth.StringWidth(text(row)[:index])
}
