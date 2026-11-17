// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

// NoticeLine is one line a view puts under the title for what is true of
// the page as a whole and is not evidence: a read that failed and left the
// earlier one on the screen, a destination that changed under the view. It
// is written in the given colour, muted when there is none, indented like
// the page and cut to the width. A page has at most one, and a view plans
// its page one row shorter for it.
func NoticeLine(text, color string, width int, o Options) string {
	k := o.muted()
	if color != "" {
		k = look{color: color}
	}
	l := newLine(o)
	l.to(Indent)
	l.put(k, o.clip(o.words(text), width-Indent))
	return l.String()
}
