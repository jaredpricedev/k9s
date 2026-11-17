// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package calm is the layout kit of the incident views (:slow, :graph and
// its per-Pod page, :probe, :impact, a drawn dashboard, :query). It lays a
// page out the same way in every one of them:
//
//  1. The answer comes first, in one or two sentences; everything below is
//     evidence.
//  2. A thing and its timeline are one row on a shared time axis.
//  3. Episodes are shaded columns through every row, and a marker row and
//     the axis carry them without colour.
//  4. One colour per subject; quiet things are dim.
//  5. Rows are grouped by meaning under a short caps label.
//  6. One main chart, everything else a one-row strip on the same axis.
//  7. Two lines of keys at most.
//  8. Nothing is lost: one key shows all detail.
//
// Every function is pure: it takes plain text and returns lines carrying
// tview colour tags. Text given to the package is plain; the package
// escapes it. Every tag it writes names the foreground, the background and
// the attributes, because tview reads a tag as a whole. docs/presentation.md has the rules in full and an annotated
// example; calmtest has the three reference pages.
package calm

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	// DetailKey shows all the detail a view had before the calm layout:
	// every signal, the detail table. It is the same key in every view.
	DetailKey = "a"
	// NextChartKey makes the next strip the main chart, in the views where
	// n steps through nothing else (:graph, a dashboard, :query).
	NextChartKey = "n"
	// NextChartAltKey is the next-chart key that is free in every incident
	// view, for the two where n steps through episodes (:slow) or rollouts
	// (:impact).
	NextChartAltKey = "Tab"

	// Indent is the left margin of every line under the title.
	Indent = 2
)

// Palette is the tview colours of a page, each from one semantic token.
// An empty colour adds no tag, so the zero Palette is a monochrome page.
type Palette struct {
	// Text is names and values (semantic text).
	Text string
	// Muted is labels and quiet rows; dim text is Muted with the dim
	// attribute (semantic muted).
	Muted string
	// Accent is the view name and a neutral group label (semantic focus).
	Accent string
	// Warn is the episode markers, the word "next" and the label of what
	// lines up with the episodes (semantic warning).
	Warn string
	// Fail is the subject that stands out: the outlier Pod (semantic
	// failure).
	Fail string
	// Info is a subject that differs without lining up (semantic progress).
	Info string
	// Mark is a labelled moment such as a rollout (semantic category).
	Mark string
	// Band is the background of episode columns (semantic band). Empty
	// draws no shading; the marker row and the axis still carry the bands.
	Band string
}

// Options are the presentation choices of a page.
type Options struct {
	// ASCII draws with plain characters, for noIcons.
	ASCII   bool
	Palette Palette
}

// look is how a run of text is drawn: a colour and the bold or dim
// attribute.
type look struct {
	color     string
	bold, dim bool
}

func (o Options) text() look   { return look{color: o.Palette.Text} }
func (o Options) strong() look { return look{color: o.Palette.Text, bold: true} }
func (o Options) muted() look  { return look{color: o.Palette.Muted} }
func (o Options) dim() look    { return look{color: o.Palette.Muted, dim: true} }

// style is everything a tag sets. tview reads a tag as a whole: a field it
// leaves empty is the default again, not what it was. So every tag the kit
// writes names all three fields, and a line is closed with a full reset.
type style struct {
	fg, bg, attrs string
}

func (k look) style() style {
	attrs := ""
	if k.bold {
		attrs += "b"
	}
	if k.dim {
		attrs += "d"
	}
	return style{fg: k.color, attrs: attrs}
}

func (s style) tag() string {
	field := func(v string) string {
		if v == "" {
			return "-"
		}
		return v
	}
	return "[" + field(s.fg) + ":" + field(s.bg) + ":" + field(s.attrs) + "]"
}

const resetTag = "[-:-:-]"

// glyph is one cell of a line, or two for a wide rune: the rune with the
// zero-width runes that follow it, and how it is drawn.
type glyph struct {
	text  string
	width int
	style
}

func (g glyph) blank() bool { return g.text == " " }

var (
	// tagAt is a tview colour tag, escapeAt a bracket that tview.Escape
	// kept from being read as one.
	tagAt    = regexp.MustCompile(`^\[([a-zA-Z]+|#[0-9a-zA-Z]{6}|-)?:([a-zA-Z]+|#[0-9a-zA-Z]{6}|-)?:([lbdru]+|-)?\]`)
	escapeAt = regexp.MustCompile(`^\[([a-zA-Z0-9_,;: \-."#]+)\[(\[*)\]`)
)

// parse reads a tagged line into its cells.
func parse(text string) []glyph {
	var out []glyph
	cur := style{}
	add := func(r rune) {
		if w := runewidth.RuneWidth(r); w > 0 || len(out) == 0 {
			out = append(out, glyph{text: string(r), width: w, style: cur})
		} else {
			out[len(out)-1].text += string(r)
		}
	}
	field := func(v string) string {
		if v == "-" {
			return ""
		}
		return v
	}
	for i := 0; i < len(text); {
		if text[i] == '[' {
			if tag := tagAt.FindStringSubmatch(text[i:]); tag != nil {
				cur = style{fg: field(tag[1]), bg: field(tag[2]), attrs: field(tag[3])}
				i += len(tag[0])
				continue
			}
			if esc := escapeAt.FindStringSubmatch(text[i:]); esc != nil {
				for _, r := range "[" + esc[1] + esc[2] + "]" {
					add(r)
				}
				i += len(esc[0])
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		add(r)
		i += size
	}
	return out
}

// tagged writes cells as a tagged line. Runs of one style share a tag, and
// blank cells in the default style at the end are left out.
func tagged(row []glyph) string {
	end := len(row)
	for end > 0 && row[end-1].blank() && row[end-1].style == (style{}) {
		end--
	}
	var out, run strings.Builder
	cur, open := style{}, false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		switch {
		case cur != (style{}):
			out.WriteString(cur.tag())
			open = true
		case open:
			out.WriteString(resetTag)
			open = false
		}
		out.WriteString(tview.Escape(run.String()))
		run.Reset()
	}
	for _, g := range row[:end] {
		if g.style != cur {
			flush()
			cur = g.style
		}
		run.WriteString(g.text)
	}
	flush()
	if open {
		out.WriteString(resetTag)
	}
	return out.String()
}

func cells(text string) int { return runewidth.StringWidth(text) }

// plainWords replaces the signs the kit and its callers write with plain
// characters.
var plainWords = strings.NewReplacer(
	"←/→", "Left/Right", "↑/↓", "Up/Down", "←→", "Left/Right", "↑↓", "Up/Down",
	" · ", " | ", "·", "|", "…", "...", "→", "->", "–", "-", "—", "-",
	"▼", "v", "▲", "^", "×", "x", "─", "-", "┄", "-",
)

// words is text as it is written with these options: with ASCII, the few
// signs of a page become plain characters.
func (o Options) words(text string) string {
	if !o.ASCII {
		return text
	}
	return plainWords.Replace(text)
}

// sep is the separator between the parts of a line.
func (o Options) sep() string { return o.words(" · ") }

// clip shortens text to a width in cells and marks the cut.
func (o Options) clip(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if cells(text) <= width {
		return text
	}
	tail := o.words("…")
	if width <= cells(tail) {
		return runewidth.Truncate(text, width, "")
	}
	return runewidth.Truncate(text, width, tail)
}

// margin is the right margin: a wide screen keeps two cells free.
func margin(width int) int {
	if width >= 100 {
		return Indent
	}
	return 0
}

// line builds one line of a page and knows the column it has reached.
type line struct {
	row []glyph
	col int
	o   Options
}

func newLine(o Options) *line { return &line{o: o} }

// to pads with spaces up to a column.
func (l *line) to(col int) {
	for l.col < col {
		l.row = append(l.row, glyph{text: " ", width: 1})
		l.col++
	}
}

// put writes text as it is.
func (l *line) put(k look, text string) {
	st := k.style()
	for _, r := range text {
		w := runewidth.RuneWidth(r)
		if w == 0 && len(l.row) > 0 {
			l.row[len(l.row)-1].text += string(r)
			continue
		}
		l.row = append(l.row, glyph{text: string(r), width: w, style: st})
		l.col += w
	}
}

// field writes text clipped to a width and moves on by that width.
func (l *line) field(k look, text string, width int) {
	end := l.col + max(width, 0)
	l.put(k, l.o.clip(text, width))
	l.to(end)
}

// strip writes the cells of a strip.
func (l *line) strip(row []cell) {
	for _, c := range row {
		l.row = append(l.row, glyph{text: string(c.r), width: 1, style: c.k.style()})
	}
	l.col += len(row)
}

func (l *line) String() string { return tagged(l.row) }
