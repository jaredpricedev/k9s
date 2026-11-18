// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strconv"
	"strings"
	"time"
)

// Title is the first line of a page: what the view is, what it looks at,
// over which window, and how much of it was read.
type Title struct {
	// View is the name of the view, such as "Slow requests".
	View string
	// Object is what it looks at: "Service gitlab-webservice-default".
	Object string
	// Window is the time looked at: "last 1h".
	Window string
	// Read is when the page was read; the clock is written in UTC, like
	// the axis. The zero time is a page that was not read.
	Read time.Time
	// Done of Total queries were answered. A Total of zero writes no count.
	Done, Total int
	// Busy replaces the read status while a read runs: "reading…".
	Busy string
	// KeepEnd shortens an Object that does not fit in the middle instead of
	// at its end: the end of a generated name (a Pod's) is what tells it
	// from its siblings.
	KeepEnd bool
	// Verb and Unit are the words of the status of a page that does
	// something other than read queries: "sent" and "requests" write
	// "sent 14:02 · 15 of 15 requests". Empty is "read" and "queries".
	Verb, Unit string
}

// status is what is known about the read, in its forms from full to short,
// and whether it is the status of a complete read.
func (t Title) status(o Options) (forms []string, complete bool) {
	if t.Busy != "" {
		return []string{o.words(t.Busy)}, true
	}
	if t.Read.IsZero() {
		return []string{"not read"}, false
	}
	clock := t.Read.UTC().Format("15:04")
	verb, unit := t.Verb, t.Unit
	if verb == "" {
		verb = "read"
	}
	if unit == "" {
		unit = "queries"
	}
	if t.Total <= 0 {
		return []string{verb + " " + clock, clock}, true
	}
	done, total := strconv.Itoa(t.Done), strconv.Itoa(t.Total)
	return []string{
		verb + " " + clock + o.sep() + done + " of " + total + " " + unit,
		clock + o.sep() + done + "/" + total,
		done + "/" + total,
	}, t.Done >= t.Total
}

// TitleLine is the title in the width: the view name in the accent colour,
// the object and the window, and the read status at the right. The status
// is dim when everything was read and in the warning colour when not; its
// words say which. A narrow line shortens the status, then the object,
// then leaves the window out; a read that is not complete keeps its count
// to the last.
func TitleLine(t Title, width int, o Options) string {
	const gap = 2
	forms, complete := t.status(o)
	statusLook := o.dim()
	if !complete {
		statusLook = look{color: o.Palette.Warn}
	}
	view := look{color: o.Palette.Accent, bold: true}
	window := ""
	if t.Window != "" {
		window = o.sep() + o.words(t.Window)
	}
	t.View, t.Object = o.words(t.View), o.words(t.Object)
	draw := func(object, window, status string) string {
		l := newLine(o)
		l.put(view, o.clip(t.View, width))
		if object != "" {
			l.to(l.col + gap)
			l.put(o.text(), object+window)
		}
		if status != "" {
			l.to(width - margin(width) - cells(status))
			l.put(statusLook, status)
		}
		return l.String()
	}
	shorten := o.clip
	if t.KeepEnd {
		shorten = o.clipMiddle
	}
	head := cells(t.View) + gap
	const minObject = 8
	for index, status := range forms {
		room := width - margin(width) - head - gap - cells(status)
		switch {
		case cells(t.Object)+cells(window) <= room:
			return draw(t.Object, window, status)
		case index == 0 && len(forms) > 1:
			// A shorter status comes before a shorter name.
			continue
		case room-cells(window) >= minObject:
			return draw(shorten(t.Object, room-cells(window)), window, status)
		case room >= minObject:
			return draw(shorten(t.Object, room), "", status)
		}
	}
	last := forms[len(forms)-1]
	if cells(t.View)+gap+cells(last) <= width-margin(width) {
		return draw("", "", last)
	}
	if !complete && cells(last) < width {
		// The count of an incomplete read outlives the name of the view.
		l := newLine(o)
		l.put(view, o.clip(t.View, width-gap-cells(last)))
		l.to(width - cells(last))
		l.put(statusLook, last)
		return l.String()
	}
	return draw("", "", "")
}

// Action is one likely next step: the key and what it does, in words.
type Action struct {
	Key, Label string
}

// NextLine is the first of the two key lines: the word "next" and the
// likely actions, each a key and its words. A narrow line draws the
// actions closer, then drops them from the right; the first is always
// named.
func NextLine(actions []Action, width int, o Options) string {
	if len(actions) == 0 {
		return ""
	}
	texts, gap, count, room := nextFit(actions, width, &o)
	l := newLine(o)
	l.to(Indent)
	l.put(look{color: o.Palette.Warn, bold: true}, nextWord)
	l.to(l.col + 2)
	for i, text := range texts[:count] {
		if i > 0 {
			l.to(l.col + gap)
		}
		l.put(o.text(), o.clip(text, max(room, 0)))
	}
	return l.String()
}

// nextWord is the word the first key line starts with.
const nextWord = "next"

// nextFit is the actions of the first key line as a width draws them:
// their texts, the blanks between them, how many of them are drawn, and
// the room they have.
func nextFit(actions []Action, width int, o *Options) (texts []string, gap, count, room int) {
	room = width - Indent - cells(nextWord) - 2
	texts = make([]string, len(actions))
	for i, a := range actions {
		texts[i] = o.words(a.Key + " " + a.Label)
	}
	gap, count = 4, len(texts)
	fits := func() bool {
		need := (count - 1) * gap
		for _, text := range texts[:count] {
			need += cells(text)
		}
		return need <= room
	}
	if !fits() {
		gap = 2
	}
	for count > 1 && !fits() {
		count--
	}
	return texts, gap, count, room
}

// NextShown is how many of the actions NextLine names at a width: the
// first of them, as many as fit. A view whose page must keep a key named
// asks before it draws, and names on its second key line what the first
// has no room for.
func NextShown(actions []Action, width int, o Options) int { //nolint:gocritic // Options are passed by value, as NextLine takes them.
	if len(actions) == 0 {
		return 0
	}
	_, _, count, _ := nextFit(actions, width, &o)
	return count
}

// RestLine is the second key line: every other key, dim, in one line. The
// parts are written in the order given, which is the same in every view:
// what the arrows move, the window, all detail, the queries, the view's
// own keys, reading again, and the way out. A narrow line drops parts
// from the right, but three are the last to go: the way out, which by
// convention is the last part ("Esc back"), the key for all detail, and
// before it the key for the queries. Whatever a page leaves out, the key
// that shows it stays named.
func RestLine(parts []string, width int, o Options) string {
	type part struct {
		text string
		// rank is how long a part is kept: 0 goes first, from the right.
		rank int
	}
	kept := make([]part, 0, len(parts))
	for _, text := range parts {
		if text == "" {
			continue
		}
		rank := 0
		switch {
		case strings.HasPrefix(text, QueriesKey+" "):
			rank = 1
		case strings.HasPrefix(text, DetailKey+" "):
			rank = 2
		}
		kept = append(kept, part{text: o.words(text), rank: rank})
	}
	if len(kept) == 0 {
		return ""
	}
	kept[len(kept)-1].rank = 3
	room := width - Indent
	join := func() string {
		text := ""
		for i, p := range kept {
			if i > 0 {
				text += o.sep()
			}
			text += p.text
		}
		return text
	}
	for len(kept) > 1 && cells(join()) > room {
		// The rightmost part of the lowest rank goes.
		at := len(kept) - 1
		for i := len(kept) - 1; i >= 0; i-- {
			if kept[i].rank < kept[at].rank {
				at = i
			}
		}
		kept = append(kept[:at], kept[at+1:]...)
	}
	l := newLine(o)
	l.to(Indent)
	l.put(o.dim(), o.clip(join(), room))
	return l.String()
}

// headingLook is how a group label is drawn: bold, in its colour or muted.
func headingLook(color string, o Options) look {
	if color == "" {
		color = o.Palette.Muted
	}
	return look{color: color, bold: true}
}

// HeadingText is a group label as it is written: bold, in the given colour
// or muted. The label is written as given; callers write it in capitals.
// It is for a line a view writes itself, such as the line over its chart.
func HeadingText(label, color string, o Options) string {
	l := newLine(o)
	l.put(headingLook(color, o), o.words(label))
	return l.String()
}

// FoldedLine is a group folded to one line: its label and its items. Items
// that do not fit are counted, never silently dropped.
func FoldedLine(label, color string, items []string, quiet bool, width int, o Options) string {
	l := newLine(o)
	l.to(Indent)
	label = o.clip(o.words(label), width-Indent)
	l.put(headingLook(color, o), label)
	room := width - margin(width) - l.col - 2
	text, shown := "", 0
	for i, item := range items {
		item = o.words(item)
		next := text
		if i > 0 {
			next += o.sep()
		}
		next += item
		more := ""
		if left := len(items) - i - 1; left > 0 {
			more = " +" + strconv.Itoa(left) + " more"
		}
		if cells(next)+cells(more) > room {
			break
		}
		text, shown = next, i+1
	}
	if left := len(items) - shown; left > 0 {
		more := "+" + strconv.Itoa(left) + " more"
		if shown == 0 {
			// Not even one item fits whole: the first is cut and the rest
			// are counted; when there is no room for a cut one either,
			// the items are counted.
			text = o.clip(o.words(items[0]), room)
			if left > 1 {
				more = "+" + strconv.Itoa(left-1) + " more"
				text = o.clip(o.words(items[0]), room-cells(more)-1) + " " + more
				if room-cells(more)-1 < 2 {
					text = "(" + strconv.Itoa(left) + ")"
				}
			}
		} else {
			text += " " + more
		}
	}
	if text == "" || room < 1 {
		return l.String()
	}
	k := o.text()
	if quiet {
		k = o.dim()
	}
	l.to(l.col + 2)
	l.put(k, o.clip(text, room))
	return l.String()
}
