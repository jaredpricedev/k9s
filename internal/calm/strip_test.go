// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"math"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/chart"
	"github.com/stretchr/testify/assert"
)

// tenMinutes is ten columns, one a minute, one sample a column.
var tenMinutes = chart.Frame{Start: minute(0), End: minute(10), Step: time.Minute, Width: 10}

func over(limit float64) func(float64) bool {
	return func(v float64) bool { return v > limit }
}

func TestStripForms(t *testing.T) {
	gapped := append(series(1, 1, 9, 5, 1), chart.Point{T: minute(8), V: 1}, chart.Point{T: minute(9), V: 6}, chart.Point{T: minute(10), V: 1})
	for name, c := range map[string]struct {
		strip       Strip
		want, ascii string
	}{
		"over its rule a value is a level, under it the baseline, unsampled the gap glyph": {
			Levels(gapped, over(4)), "▁▁█▅▁××▁▆▁", "__#=_xx_+_"},
		"over the rule is at least one step above the baseline, however small; a series that ends early ends in gaps": {
			Levels(series(0, 0.02, 1, 0.011, 0.01), over(0.01)), "▁▂█▂▁×××××", "_.#._xxxxx"},
		"a shared top makes rows comparable": {
			Levels(series(0, 5, 0), over(1)).Top(10), "▁▅▁×××××××", "_=_xxxxxxx"},
		"without a rule every sampled column is its level": {
			Trend(series(0, 5, 10, 10, 2)), "▁▅██▂×××××", "_=##.xxxxx"},
		"a trend with a top is drawn against it": {
			Trend(series(3, 3, 3)).Top(6), "▅▅▅×××××××", "===xxxxxxx"},
		"a trend with a rule keeps the height of its value on both sides of the rule": {
			Trend(series(8, 8, 8, 4, 1, 0)).Rule(func(v float64) bool { return v < 6 }), "███▅▂▁××××", "###=._xxxx"},
		"a strip without a sample is gaps, not a baseline": {
			Levels(nil, over(1)), "××××××××××", "xxxxxxxxxx"},
		"a missing sample is NaN to the fold, never zero": {
			Trend([]chart.Point{{T: minute(2), V: math.NaN()}, {T: minute(3), V: 4}}), "××█×××××××", "xx#xxxxxxx"},
		"events are marks on a quiet row": {
			Events(minute(2), minute(2).Add(20*time.Second), minute(7)), "··▲····▲··", "..^....^.."},
		"an event strip is not read where it was not read": {
			Events(minute(5)).Unknown(span(0, 3)), "×××··▲····", "xxx..^...."},
		"an event outside the window is not drawn": {
			Events(minute(-3), minute(30)), "··········", ".........."},
		"a condition is a full block where it held": {
			Held(span(2, 4), span(8, 9)), "▁▁██▁▁▁▁█▁", "__##____#_"},
		"not read wins over held": {
			Held(span(2, 6)).Unknown(span(4, 10)), "▁▁██××××××", "__##xxxxxx"},
		"quiet":           {Quiet(), "··········", ".........."},
		"not read":        {UnreadStrip(), "××××××××××", "xxxxxxxxxx"},
		"no strip at all": {Strip{}, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, plain(StripLine(c.strip, "", Bands{}, tenMinutes, plainOpts)))
			assert.Equal(t, c.ascii, plain(StripLine(c.strip, "", Bands{}, tenMinutes, asciiOpts)))
		})
	}
	assert.Empty(t, StripLine(Quiet(), "", Bands{}, chart.Frame{}, plainOpts), "no frame, no strip")
}

func TestStripColours(t *testing.T) {
	levels := Levels(append(series(1, 9, 1), chart.Point{T: minute(6), V: 1}), over(4))
	row := draw(t, []string{StripLine(levels, testPalette.Fail, Bands{}, tenMinutes, colorOpts)}, 10)[0]
	assert.Equal(t, "▁█▁××▁××××", text(row))
	assert.Equal(t, shown{r: '█', fg: testPalette.Fail}, row[1], "over its rule: the row's colour, not dim")
	assert.Equal(t, shown{r: '▁', fg: testPalette.Muted, dim: true}, row[0], "the baseline is quiet")
	assert.Equal(t, shown{r: '×', fg: testPalette.Muted, dim: true}, row[3], "a gap is quiet too, and a glyph of its own")

	// A row without a colour is muted: what is over its rule is still a
	// level, so it reads without colour.
	row = draw(t, []string{StripLine(levels, "", Bands{}, tenMinutes, colorOpts)}, 10)[0]
	assert.Equal(t, shown{r: '█', fg: testPalette.Muted}, row[1])

	// A trend with a rule: the height is the value, the rule is the colour.
	fell := Trend(series(8, 8, 1, 0)).Rule(func(v float64) bool { return v < 6 })
	row = draw(t, []string{StripLine(fell, testPalette.Info, Bands{}, tenMinutes, colorOpts)}, 10)[0]
	assert.Equal(t, "██▂▁××××××", text(row))
	assert.Equal(t, shown{r: '█', fg: testPalette.Muted, dim: true}, row[0], "within its rule a value is dim at its own height")
	assert.Equal(t, shown{r: '▂', fg: testPalette.Info}, row[2], "over its rule it is in the row's colour, low as it is")
	assert.Equal(t, shown{r: '▁', fg: testPalette.Info}, row[3])
	assert.Equal(t, "▁█▁××▁××××", plain(StripLine(levels.Rule(nil), "", Bands{}, tenMinutes, plainOpts)), "no rule changes nothing")

	row = draw(t, []string{StripLine(Events(minute(4)), testPalette.Warn, Bands{}, tenMinutes, colorOpts)}, 10)[0]
	assert.Equal(t, shown{r: '▲', fg: testPalette.Warn, bold: true}, row[4])
	assert.Equal(t, shown{r: '·', fg: testPalette.Muted, dim: true}, row[3])

	// Without a palette the glyphs carry everything.
	row = draw(t, []string{StripLine(levels, "", Bands{}, tenMinutes, plainOpts)}, 10)[0]
	assert.Equal(t, "▁█▁××▁××××", text(row))
	assert.Empty(t, row[1].fg)
}

func TestMomentsAndCursorRunThroughWhatIsEmpty(t *testing.T) {
	// The cursor is on the sample taken at 14:07: the step from 14:06.
	bands := Bands{Marks: []chart.Mark{{Key: 'R', T: minute(3), Label: "rollout"}}, Cursor: minute(7)}
	for name, c := range map[string]struct {
		strip       Strip
		want, ascii string
	}{
		"through a quiet row":                {Quiet(), "···┊··┆···", "...|..:..."},
		"through a row without a strip":      {Strip{}, "   ┊  ┆", "   |  :"},
		"never over a level or the baseline": {Trend(series(1, 1, 1, 1, 1, 1, 1, 1, 1, 1)), "██████████", "##########"},
		"never over an event":                {Events(minute(3), minute(6)), "···▲··▲···", "...^..^..."},
		"never over what was not read":       {UnreadStrip(), "××××××××××", "xxxxxxxxxx"},
		"beside an event":                    {Events(minute(4)), "···┊▲·┆···", "...|^.:..."},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, plain(StripLine(c.strip, "", bands, tenMinutes, plainOpts)))
			assert.Equal(t, c.ascii, plain(StripLine(c.strip, "", bands, tenMinutes, asciiOpts)))
		})
	}
	row := draw(t, []string{StripLine(Quiet(), "", bands, tenMinutes, colorOpts)}, 10)[0]
	assert.Equal(t, shown{r: '┊', fg: testPalette.Mark}, row[3])
	assert.Equal(t, shown{r: '┆', fg: testPalette.Text}, row[6])
}
