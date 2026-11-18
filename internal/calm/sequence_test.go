// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nan() float64 { return math.NaN() }

func itoa(n int) string { return strconv.Itoa(n) }

// run is five requests to one Pod: two slow answers, one that got no
// answer, one still awaited and one never sent.
func run() []Tick {
	return []Tick{{V: 2.7, Over: true}, {V: 3.6, Over: true}, {Glyph: '×', ASCII: 'x', Loud: true}, {Glyph: '~'}, {Glyph: '·', ASCII: ' '}}
}

func fast(count int) []Tick {
	out := make([]Tick, count)
	for i := range out {
		out[i] = Tick{V: 0.02}
	}
	return out
}

func TestSequenceForms(t *testing.T) {
	one := Sequence("this run", run()).Top(4)
	two := one.Then("previous 14:02:11Z", fast(5))
	for name, c := range map[string]struct {
		strip       Strip
		width       int
		want, ascii string
	}{
		"one cell per thing, in order, with a blank between two when there is room": {one, 12, "▆ ▇ × ~ ·", "+ * x ~"},
		"side by side when there is not":                                            {one, 5, "▆▇×~·", "+*x~"},
		"a strip narrower than its cells ends in the cut mark":                      {one, 4, "▆▇×…", "+*x>"},
		"the earlier sequence stands after it, each part as wide as its caption":    {two, 30, "▆ ▇ × ~ ·   ▁ ▁ ▁ ▁ ▁", "+ * x ~     _ _ _ _ _"},
		"both side by side before the earlier one is given up":                      {two, 29, "▆▇×~·      ▁▁▁▁▁", "+*x~       _____"},
		"the earlier one is the first thing a narrow strip leaves out":              {two, 28, "▆ ▇ × ~ ·", "+ * x ~"},
		"a value that is not over its rule is drawn at its own height":              {Sequence("", []Tick{{V: 2}, {V: 0}}).Top(4), 12, "▅ ▁", "= _"},
		"without a top the largest value is the full height":                        {Sequence("", []Tick{{V: 1}, {V: 2, Over: true}}), 12, "▅ █", "= #"},
		"a value without a number is the gap glyph":                                 {Sequence("", []Tick{{V: nan()}, {V: 1}}), 12, "× █", "x #"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, plain(StripLine(c.strip, "", Bands{}, hourFrame(c.width), plainOpts)))
			assert.Equal(t, c.ascii, plain(StripLine(c.strip, "", Bands{}, hourFrame(c.width), asciiOpts)))
		})
	}
}

// Over its rule a cell is the row's colour, and so is a thing whose
// missing value is what stands out. Everything else is dim, and the
// earlier sequence is dim throughout: it is the quieter of the two.
func TestSequenceColours(t *testing.T) {
	strip := Sequence("this run", run()).Top(4).Then("previous", []Tick{{V: 3, Over: true}, {Glyph: '×', Loud: true}})
	cells := draw(t, []string{StripLine(strip, testPalette.Fail, Bands{}, hourFrame(30), colorOpts)}, 30)[0]
	for col, want := range map[int]shown{
		0:  {r: '▆', fg: testPalette.Fail},
		4:  {r: '×', fg: testPalette.Fail},
		6:  {r: '~', fg: testPalette.Muted, dim: true},
		8:  {r: '·', fg: testPalette.Muted, dim: true},
		12: {r: '▆', fg: testPalette.Muted, dim: true},
		14: {r: '×', fg: testPalette.Muted, dim: true},
	} {
		assert.Equal(t, want, cells[col], "column %d", col)
	}
	// A row without a colour is a quiet row: over its rule it is muted.
	quiet := draw(t, []string{StripLine(strip, "", Bands{}, hourFrame(30), colorOpts)}, 30)[0]
	assert.Equal(t, shown{r: '▆', fg: testPalette.Muted}, quiet[0])
}

func countedPage() Page {
	p := testPalette
	slow := Sequence("this run", run()).Top(4).Then("previous 14:02:11Z", []Tick{{V: 2.9, Over: true}, {V: 3, Over: true}, {V: 2.7, Over: true}, {V: 2.8, Over: true}, {V: 3.1, Over: true}})
	peer := Sequence("this run", fast(5)).Top(4).Then("previous 14:02:11Z", fast(5))
	return Page{
		Title:   Title{View: "Probe", Object: "Service web", Window: "GET /ready ×5 per pod", Read: minute(2), Done: 14, Total: 15, Verb: "sent", Unit: "requests"},
		Verdict: Verdict{Sentences: []string{"Only hx4mt answered slowly: median 2.7s against 20ms for its 2 peers."}, Caveat: "A difference between Pods now, not proof of cause."},
		Counted: true, NumberAfter: true,
		Groups: []Group{
			{Label: "SLOWER THAN ITS PEERS", Color: p.Warn, Head: "median", Rows: []Row{
				{Subject: "hx4mt", What: "worker-f  zone-c", Number: "2.7s", Compare: "peers 20ms", Tag: "OUTLIER", Color: p.Fail, Strip: slow, Selected: true}}},
			{Label: "LIKE THEIR PEERS", Role: Secondary, Rows: []Row{
				{Subject: "2bq7k", What: "worker-d  zone-a", Number: "20ms", Strip: peer},
				{Subject: "8wzpd", What: "worker-e  zone-b", Number: "20ms", Strip: peer}}},
			{Label: "MEASURED", Role: NotReadRole, Summary: []string{"API server → Pod, not ingress or Service"}},
		},
		Next: []Action{{Key: "Enter", Label: "open hx4mt"}, {Key: "r", Label: "run again"}},
		Keys: []string{"↑↓ pod", "a all detail", "Esc back"},
	}
}

// A page of sequences has no clock: no axis, strips as wide as their
// cells, the numbers directly after them, and one row that names the two
// sequences of every row.
func TestCountedPage(t *testing.T) {
	page := countedPage()
	plan := Lay(&page, 100, 20, plainOpts)
	require.Empty(t, plan.Dropped)
	assert.Equal(t, 30, plan.Layout.Strip, "the strip is as wide as its two captioned parts, not as wide as the screen")
	assert.Equal(t, []string{
		right("Probe  Service web · GET /ready ×5 per pod", "sent 14:02 · 14 of 15 requests", 98),
		"",
		"  Only hx4mt answered slowly: median 2.7s against 20ms for its 2 peers.",
		"  A difference between Pods now, not proof of cause.",
		"",
		"                           this run    previous 14:02:11Z",
		"  SLOWER THAN ITS PEERS                                    median",
		"> hx4mt  worker-f  zone-c  ▆ ▇ × ~ ·   ▆ ▆ ▆ ▆ ▆           2.7s    peers 20ms  OUTLIER",
		"",
		"  LIKE THEIR PEERS",
		"  2bq7k  worker-d  zone-a  ▁ ▁ ▁ ▁ ▁   ▁ ▁ ▁ ▁ ▁           20ms",
		"  8wzpd  worker-e  zone-b  ▁ ▁ ▁ ▁ ▁   ▁ ▁ ▁ ▁ ▁           20ms",
		"",
		"  MEASURED  API server → Pod, not ingress or Service",
		"",
		"  next  Enter open hx4mt    r run again",
		"",
		"  ↑↓ pod · a all detail · Esc back",
	}, plainAll(Render(&page, &plan, plainOpts).Lines()))
	for _, line := range Render(&page, &plan, plainOpts).Lines() {
		assert.NotContains(t, line, "UTC", "a page of counted things has no time axis")
	}

	// The text gives way for the earlier sequence before the earlier
	// sequence does; then it goes, and the page says so by its caption.
	narrow := Lay(&page, 68, 20, plainOpts)
	assert.Equal(t, 29, narrow.Layout.Strip, "side by side, with what each number is compared with left out")
	assert.Equal(t, []string{"what each number is compared with"}, narrow.Dropped)
	lines := plainAll(Render(&page, &narrow, plainOpts).Lines())
	assert.Contains(t, lines, "                      this run   previous 14:02:11Z")
	assert.Contains(t, lines, "> hx4mt  worker-f  …  ▆▇×~·      ▆▆▆▆▆               2.7s    OUTLIER")

	tight := Lay(&page, 48, 20, plainOpts)
	assert.Equal(t, []string{"previous 14:02:11Z"}, tight.Missing)
	lines = plainAll(Render(&page, &tight, plainOpts).Lines())
	assert.Contains(t, lines, "  not shown at this size (a shows all): previou…")
	for _, line := range lines {
		assert.NotContains(t, line, "this run", "one sequence needs no caption")
		assert.LessOrEqual(t, width(line), 48, line)
	}
	assert.Contains(t, strings.Join(lines, "\n"), "▆ ▇ × ~ ·")

	// Below the narrowest strip the rows are text, as on every page.
	text := Lay(&page, 30, 20, plainOpts)
	assert.True(t, text.TextRows)
	assert.Contains(t, text.Missing, "the strips")
}

// More cells than the strip has columns are cut and counted.
func TestCountedPageCountsCellsItCuts(t *testing.T) {
	page := countedPage()
	page.Groups = []Group{{Label: "ANSWERED", Rows: []Row{{Subject: "hx4mt", What: "worker-f", Number: "20ms", Strip: Sequence("", fast(30)).Top(4)}}}}
	plan := Lay(&page, 40, 20, plainOpts)
	require.Greater(t, plan.Layout.Strip, 0)
	require.Less(t, plan.Layout.Strip, 30)
	cut := 30 - plan.Layout.Strip + 1
	assert.Contains(t, plan.Missing, "the last "+itoa(cut)+" of 30 cells of each strip")
	assert.Contains(t, strings.Join(plainAll(Render(&page, &plan, plainOpts).Lines()), "\n"), strings.Repeat("▁", plan.Layout.Strip-1)+"…")
}

func TestTitleWordsOfAPageThatSendsInsteadOfReading(t *testing.T) {
	title := Title{View: "Probe", Object: "Service web", Read: minute(2), Done: 12, Total: 15, Verb: "sent", Unit: "requests"}
	assert.Equal(t, right("Probe  Service web", "sent 14:02 · 12 of 15 requests", 80), plain(TitleLine(title, 80, plainOpts)))
	assert.Equal(t, right("Probe  Service web", "14:02 · 12/15", 40), plain(TitleLine(title, 40, plainOpts)))
	// A run that did not send everything is partial, as a read is.
	cells := draw(t, []string{TitleLine(title, 80, colorOpts)}, 80)[0]
	assert.Equal(t, testPalette.Warn, cells[at(t, cells, "sent 14:02")].fg)
	title.Verb, title.Unit = "", ""
	assert.Equal(t, right("Probe  Service web", "read 14:02 · 12 of 15 queries", 80), plain(TitleLine(title, 80, plainOpts)))
}

func TestVerdictApartStartsEverySentenceOnItsOwnLine(t *testing.T) {
	v := Verdict{Sentences: []string{"After rev 2: p99 0.2s -> 2.5s.", "rev 2 changed: image v1 -> v2"}, Caveat: "Order in time, not cause."}
	assert.Equal(t, []string{right("  After rev 2: p99 0.2s -> 2.5s. rev 2 changed: image v1 -> v2", "Order in time, not cause.", 98)},
		plainAll(VerdictLines(v, 100, plainOpts)))
	v.Apart = true
	assert.Equal(t, []string{"  After rev 2: p99 0.2s -> 2.5s.", right("  rev 2 changed: image v1 -> v2", "Order in time, not cause.", 98)},
		plainAll(VerdictLines(v, 100, plainOpts)))
}
