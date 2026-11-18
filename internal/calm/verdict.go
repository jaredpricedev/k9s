// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// VerdictKind is what a read came to.
type VerdictKind int

const (
	// Found is a finding: the first sentence states it.
	Found VerdictKind = iota
	// NothingFound is a read that found nothing. It is not health: the
	// sentences say how much was read.
	NothingFound
	// NotRead is a read that did not happen or did not answer. It is drawn
	// in the warning colour and its words say so.
	NotRead
)

// Verdict is the answer at the top of a page: one or two plain sentences
// and the caveat that keeps them honest. The view writes the sentences; it
// states only what the data supports.
type Verdict struct {
	Kind VerdictKind
	// Sentences are written in order; the first is bold.
	Sentences []string
	// Caveat is the evidential limit, such as "Overlap in time, not proof
	// of cause.". It is dim, at the right of the last line when it fits.
	Caveat string
	// Apart starts every sentence on a line of its own, for a verdict whose
	// second sentence is a statement of its own kind (what a revision
	// changed) and not the continuation of the first.
	Apart bool
}

// Nothing is the verdict of a read that found nothing. The sentence says
// what was looked for ("No stall in the last 1h."); the verdict adds how
// much was read, so a partial read never passes for a clean one.
func Nothing(sentence string, done, total int) Verdict {
	v := Verdict{Kind: NothingFound, Sentences: []string{sentence}, Caveat: "Nothing found is not proof of health."}
	switch {
	case total <= 0:
	case done >= total:
		v.Sentences = append(v.Sentences, "All "+strconv.Itoa(total)+" queries answered.")
	default:
		v.Sentences = append(v.Sentences, strconv.Itoa(total-done)+" of "+strconv.Itoa(total)+" queries were not answered; nothing is known about those.")
	}
	return v
}

// Unread is the verdict of a page whose sources were not read. what names
// them ("Prometheus was not read."), why repeats the reason given.
func Unread(what, why string) Verdict {
	v := Verdict{Kind: NotRead, Sentences: []string{what}, Caveat: "Not read is not healthy."}
	if why != "" {
		v.Sentences = append(v.Sentences, why)
	}
	return v
}

// piece is a run of one look on a verdict line.
type piece struct {
	k    look
	text string
}

// VerdictLines lays the verdict out in the width: the first sentence bold,
// the others plain, wrapped between words by cell width. A following
// sentence starts a new line unless all of it fits on the current one. The
// caveat is dim and right-aligned on the last line when it fits there, on
// its own line otherwise.
func VerdictLines(v Verdict, width int, o Options) []string {
	room := width - Indent - margin(width)
	if room < 1 {
		return nil
	}
	first, rest := o.strong(), o.text()
	if v.Kind == NotRead {
		first = look{color: o.Palette.Warn, bold: true}
	}
	var lines [][]piece
	used := 0
	open := func() {
		lines = append(lines, nil)
		used = 0
	}
	add := func(k look, word string) {
		if len(lines) == 0 {
			open()
		}
		at := len(lines) - 1
		if used > 0 {
			word = " " + word
		}
		if n := len(lines[at]); n > 0 && lines[at][n-1].k == k {
			lines[at][n-1].text += word
		} else {
			lines[at] = append(lines[at], piece{k: k, text: word})
		}
		used += cells(word)
	}
	for index, sentence := range v.Sentences {
		sentence = strings.Join(strings.Fields(o.words(sentence)), " ")
		if sentence == "" {
			continue
		}
		k := rest
		if index == 0 {
			k = first
		}
		if used > 0 && (v.Apart || used+1+cells(sentence) > room) {
			open()
		}
		for _, word := range strings.Fields(sentence) {
			// A word wider than the line is cut by cells: nothing is lost.
			for cells(word) > room {
				if used > 0 {
					open()
				}
				head := runewidth.Truncate(word, room, "")
				if head == "" {
					break
				}
				add(k, head)
				word = strings.TrimPrefix(word, head)
			}
			if word == "" {
				continue
			}
			if used > 0 && used+1+cells(word) > room {
				open()
			}
			add(k, word)
		}
	}
	caveat := o.words(strings.Join(strings.Fields(v.Caveat), " "))
	out := make([]string, 0, len(lines)+1)
	for index, pieces := range lines {
		l := newLine(o)
		l.to(Indent)
		for _, p := range pieces {
			l.put(p.k, p.text)
		}
		if index == len(lines)-1 && caveat != "" && l.col+3+cells(caveat) <= Indent+room {
			l.to(Indent + room - cells(caveat))
			l.put(o.dim(), caveat)
			caveat = ""
		}
		out = append(out, l.String())
	}
	for caveat != "" {
		// On its own line the caveat wraps like a sentence.
		head := caveat
		if cells(head) > room {
			head = runewidth.Truncate(caveat, room, "")
			if cut := strings.LastIndex(head, " "); cut > 0 {
				head = head[:cut]
			}
		}
		if head == "" {
			break
		}
		l := newLine(o)
		l.to(Indent)
		l.put(o.dim(), head)
		out = append(out, l.String())
		caveat = strings.TrimSpace(strings.TrimPrefix(caveat, head))
	}
	return out
}
