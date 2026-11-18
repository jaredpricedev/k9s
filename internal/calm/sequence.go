// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"math"
	"strconv"
	"time"

	"github.com/derailed/k9s/internal/chart"
)

// Tick is one cell of a Sequence: one counted thing, such as one request.
type Tick struct {
	// V is the value: the height of the cell on the scale of Top.
	V float64
	// Over says the value is over its rule. The cell is then drawn in the
	// row's colour and at least one step above the baseline, as in Levels;
	// any other value is drawn dim at its own height.
	Over bool
	// Glyph replaces the level for a thing that has no value: a request
	// without an answer, one still awaited, one never sent. ASCII is what
	// it is with plain characters; zero keeps Glyph.
	Glyph, ASCII rune
	// Loud draws the glyph in the row's colour instead of dim: the thing
	// has no value and that is itself what stands out.
	Loud bool
}

// Sequence is the strip of things that are counted, not timed: one cell
// per thing, in the order given, from the left. It is the strip of a page
// with Page.Counted, where the shared axis is the position in the order
// (the first request to every Pod is the first cell of every row) and not
// the clock. caption names the sequence on the caption row when a second
// one stands beside it (Then): "this run".
func Sequence(caption string, ticks []Tick) Strip {
	return Strip{kind: stripSequence, seq: ticks, caption: caption}
}

// Then puts an earlier sequence on the same row, after this one and
// dimmer: the previous run of the same thing. caption names it on the
// caption row: "previous 14:02:11Z". It is the first thing a narrow strip
// leaves out, and the page then says so.
func (s Strip) Then(caption string, ticks []Tick) Strip {
	s.then, s.thenCaption = ticks, caption
	return s
}

// sequenceGap is the blank cells between a sequence and the earlier one.
const sequenceGap = 3

// sequenceForm is how the sequences of a page are drawn. It is the same
// for every row, so the cells of two rows stand in the same columns.
type sequenceForm struct {
	// spaced puts a blank between two cells, so each stands alone.
	spaced bool
	// then draws the earlier sequences.
	then bool
	// first and second are the widths of the two parts, each at least as
	// wide as its caption; width is all of it.
	first, second, width int
	caption, thenCaption string
	// cells is the most cells a sequence of the page has, earlier is
	// whether a row has an earlier sequence at all.
	cells   int
	earlier bool
	// clipped is a strip narrower than its cells: the last are cut.
	clipped bool
}

func sequenceWidth(cells int, spaced bool) int {
	if spaced && cells > 0 {
		return 2*cells - 1
	}
	return cells
}

// sequenceForms is the forms the sequences of the groups can take, the
// best first: a blank between cells and the earlier sequence beside it,
// then without the blanks, then without the earlier sequence.
func sequenceForms(groups []Group) []sequenceForm {
	base := sequenceForm{}
	later := 0
	for gi := range groups {
		for ri := range groups[gi].Rows {
			s := &groups[gi].Rows[ri].Strip
			if s.kind != stripSequence {
				continue
			}
			base.cells, later = max(base.cells, len(s.seq)), max(later, len(s.then))
			if base.caption == "" {
				base.caption = s.caption
			}
			if base.thenCaption == "" && len(s.then) > 0 {
				base.thenCaption = s.thenCaption
			}
		}
	}
	if base.cells == 0 {
		return nil
	}
	base.earlier = later > 0
	var out []sequenceForm
	for _, then := range []bool{true, false} {
		if then && !base.earlier {
			continue
		}
		for _, spaced := range []bool{true, false} {
			form := base
			form.spaced, form.then = spaced, then
			form.first = sequenceWidth(base.cells, spaced)
			if then {
				form.first = max(form.first, cells(base.caption))
				form.second = max(sequenceWidth(later, spaced), cells(base.thenCaption))
				form.width = form.first + sequenceGap + form.second
			} else {
				form.width = form.first
			}
			out = append(out, form)
		}
	}
	return out
}

// measureCounted is Measure for a page of sequences. A strip is as wide as
// the best form the width has room for and no wider. The text beside it
// gives way as Measure says for the cells of a sequence, so that the
// earlier sequence stays on the row, but never for the blanks between the
// cells. When not even the cells of one sequence fit, the strip takes what
// the text can leave it and the last cells are cut.
func measureCounted(groups []Group, width, gutter int, numberAfter bool) Layout {
	forms := sequenceForms(groups)
	// free is the columns when the text gives way only for the narrowest
	// strip; widest is the widest strip the text can make room for.
	free := measure(groups, width, gutter, numberAfter, MinStrip)
	if len(forms) == 0 {
		// Nothing but rows that were not read: their strips need no room.
		if free.Strip > 0 {
			free.Strip = MinStrip
		}
		return free
	}
	widest := free
	for _, form := range forms {
		want := max(form.width, MinStrip)
		if free.Strip >= want {
			free.Strip, free.seq = want, form
			return free
		}
		if form.spaced {
			// The text gives way for the cells of a sequence, never for
			// the blanks between them.
			continue
		}
		l := measure(groups, width, gutter, numberAfter, want)
		if l.Strip >= want {
			l.Strip, l.seq = want, form
			return l
		}
		if l.Strip > widest.Strip {
			widest = l
		}
	}
	widest.seq = forms[len(forms)-1]
	widest.seq.clipped = true
	return widest
}

// missing is what the form leaves out of the strips, in words; empty when
// every cell of every sequence is drawn.
func (form sequenceForm) missing(strip int) []string {
	var out []string
	if form.cells == 0 || strip == 0 {
		return nil
	}
	if form.earlier && !form.then {
		words := form.thenCaption
		if words == "" {
			words = "the earlier strip of each row"
		}
		out = append(out, words)
	}
	if form.clipped && form.cells > strip {
		out = append(out, "the last "+strconv.Itoa(form.cells-strip+1)+" of "+strconv.Itoa(form.cells)+" cells of each strip")
	}
	return out
}

// captioned reports whether the form has a caption row: two sequences
// stand side by side and at least one of them is named.
func (form sequenceForm) captioned() bool {
	return form.then && (form.caption != "" || form.thenCaption != "")
}

// captionRow names the two sequences of every row, each over its part.
func captionRow(l Layout, o Options) string {
	out := newLine(o)
	out.to(l.StripCol)
	out.put(o.muted(), o.words(l.seq.caption))
	out.to(l.StripCol + l.seq.first + sequenceGap)
	out.put(o.muted(), o.words(l.seq.thenCaption))
	return out.String()
}

// sequence draws the cells of a Sequence in a form, width cells wide.
func (s Strip) sequence(form sequenceForm, width int, color string, o Options) []cell {
	out := make([]cell, max(width, 0))
	for i := range out {
		out[i] = cell{r: ' '}
	}
	if color == "" {
		color = o.Palette.Muted
	}
	hot, dim := look{color: color}, o.dim()
	glyphs := levelGlyphs
	if o.ASCII {
		glyphs = levelASCII
	}
	top := s.top
	if !(top > 0) {
		for _, ticks := range [][]Tick{s.seq, s.then} {
			for _, tick := range ticks {
				if tick.Glyph == 0 && !math.IsNaN(tick.V) && tick.V > top {
					top = tick.V
				}
			}
		}
	}
	step := 1
	if form.spaced {
		step = 2
	}
	part := func(at, room int, ticks []Tick, earlier bool) {
		for i, tick := range ticks {
			col := at + i*step
			if col >= at+room || col >= len(out) {
				break
			}
			loud := hot
			if earlier {
				loud = dim
			}
			switch {
			case tick.Glyph != 0:
				r, k := tick.Glyph, dim
				if o.ASCII && tick.ASCII != 0 {
					r = tick.ASCII
				}
				if tick.Loud {
					k = loud
				}
				out[col] = cell{r: r, k: k}
			case math.IsNaN(tick.V):
				gap := chart.GapGlyph
				if o.ASCII {
					gap = chart.GapASCII
				}
				out[col] = cell{r: gap, k: dim}
			case tick.Over:
				out[col] = cell{r: glyphs[max(level(tick.V, top, len(glyphs)), 1)], k: loud}
			default:
				out[col] = cell{r: glyphs[level(tick.V, top, len(glyphs))], k: dim}
			}
		}
	}
	switch {
	case form.clipped && len(s.seq) > len(out) && len(out) > 0:
		// The strip has no room for every cell: the last is the cut mark.
		part(0, len(out)-1, s.seq, false)
		mark := '…'
		if o.ASCII {
			mark = '>'
		}
		out[len(out)-1] = cell{r: mark, k: dim}
	case form.then:
		part(0, form.first, s.seq, false)
		part(form.first+sequenceGap, form.second, s.then, true)
	default:
		part(0, len(out), s.seq, false)
	}
	return out
}

// countedFrame is the frame of a page of sequences. Its strips are not
// timelines, so it carries no clock; it only has to be a frame.
func countedFrame(width int) chart.Frame {
	start := time.Unix(0, 0).UTC()
	return chart.Frame{Start: start, End: start.Add(time.Hour), Width: width}
}
