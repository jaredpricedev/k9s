// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/problems"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// problemsLine is one visible table row: a ranked resource or, under an
// expanded resource, one of the pods folded into it.
type problemsLine struct {
	row      *problems.Row
	child    bool
	expanded bool
}

// problemsColumns is the column budget for one viewport width, in terminal
// cells. Severity is never elided; names shorten before the cause is dropped.
type problemsColumns struct {
	kind, name, cause, age, signals int
	kindInName                      bool
}

var problemsShortKinds = map[string]string{
	"Pod": "pod", "Deployment": "deploy", "StatefulSet": "sts", "DaemonSet": "ds", "Job": "job",
	"PersistentVolumeClaim": "pvc", "Node": "node", "ReplicaSet": "rs", "CronJob": "cj", "Service": "svc",
	"HorizontalPodAutoscaler": "hpa", "Ingress": "ing", "ConfigMap": "cm", "Secret": "secret", "PersistentVolume": "pv",
}

func problemsKindLabel(kind string, short bool) string {
	if kind == "PersistentVolumeClaim" {
		return "PVC"
	}
	if label, ok := problemsShortKinds[kind]; ok && short {
		return label
	}
	return kind
}

// problemsLayout divides width between the columns for the lines on show.
// Fixed columns take only what their widest value needs, so the cause gets
// every remaining cell and short names never reserve width it could use.
func problemsLayout(width int, lines []problemsLine, allNamespaces, ascii bool, readAt time.Time) problemsColumns {
	c := problemsColumns{kindInName: width < 72}
	severity, kind, name, age := len("SEVERITY"), len("KIND"), len("NAME"), len("FOR")
	for i := range lines {
		line := &lines[i]
		severity = max(severity, len(line.row.Severity.String()))
		kind = max(kind, runewidth.StringWidth(problemsKindLabel(line.row.Kind, width < 100)))
		name = max(name, runewidth.StringWidth(problemsName(line, allNamespaces, c.kindInName, ascii)))
		age = max(age, len(problemsFor(line.row.Since, readAt)))
	}
	rest := width - severity - 1
	share := rest * 9 / 20
	switch {
	case width >= 100:
		c.kind, c.age, c.signals = kind, age, 3
		rest -= c.kind + c.age + c.signals + 3
		share = min(44, rest*2/5)
	case width >= 72:
		c.kind, c.age = kind, age
		rest -= c.kind + c.age + 2
		share = rest * 2 / 5
	}
	c.name = max(4, min(name, share))
	c.cause = max(0, rest-c.name-1)
	return c
}

func problemsHeaders(c problemsColumns) []string {
	headers := []string{"SEVERITY"}
	if c.kind > 0 {
		headers = append(headers, "KIND")
	}
	headers = append(headers, "NAME", "CAUSE")
	if c.age > 0 {
		headers = append(headers, "FOR")
	}
	if c.signals > 0 {
		headers = append(headers, "SIG")
	}
	return headers
}

// problemsName is the identity cell before elision. Folded pods are indented
// under their owner; an owner with folded pods carries an expand marker. The
// markers have an ASCII form and never stand alone as the only meaning.
func problemsName(line *problemsLine, allNamespaces, kindInName, ascii bool) string {
	name := line.row.Name
	// Below 72 columns the namespace gives way to the name; the detail pane
	// under the list always states the full identity of the selected row.
	if allNamespaces && line.row.Namespace != "" && !line.child && !kindInName {
		name = line.row.Namespace + "/" + name
	}
	if kindInName && !line.child { // folded rows are always pods
		name = problemsKindLabel(line.row.Kind, true) + " " + name
	}
	switch {
	case line.child && ascii:
		return "  | " + name
	case line.child:
		return "  └ " + name
	case line.row.Affected() == 0:
		return name
	case line.expanded && ascii:
		return "- " + name
	case line.expanded:
		return "▾ " + name
	case ascii:
		return "+ " + name
	}
	return "▸ " + name
}

func problemsCause(line *problemsLine) string {
	if line.child || line.expanded {
		if line.row.Cause == "" && !line.child {
			return problemsPlural(line.row.Affected(), "pod") + " affected"
		}
		return line.row.Cause
	}
	return line.row.Summary()
}

func problemsPlural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// problemsCells renders one line to fit the budget. Text is cut by terminal
// cell width with a visible ellipsis; the full text stays in the detail pane.
func problemsCells(line *problemsLine, c problemsColumns, allNamespaces, ascii, short bool, readAt time.Time) []string {
	cells := []string{line.row.Severity.String()}
	if c.kind > 0 {
		cells = append(cells, problemsCut(problemsKindLabel(line.row.Kind, short), c.kind))
	}
	cells = append(cells, problemsCut(problemsName(line, allNamespaces, c.kindInName, ascii), c.name), problemsCut(problemsCause(line), c.cause))
	if c.age > 0 {
		cells = append(cells, problemsFor(line.row.Since, readAt))
	}
	if c.signals > 0 {
		cells = append(cells, fmt.Sprintf("%d", line.row.Signals))
	}
	return cells
}

func problemsCut(text string, width int) string {
	if runewidth.StringWidth(text) <= width {
		return text
	}
	return runewidth.Truncate(text, width, "…")
}

// problemsFor is how long the state has lasted at the time of the read, or
// "-" when the status does not say.
func problemsFor(since, readAt time.Time) string {
	if since.IsZero() || since.After(readAt) {
		return "-"
	}
	return dailyWorkspaceAge(since, readAt)
}

// problemsMatch is a literal, case-insensitive filter: every token must occur
// in the row's severity, kind, namespace, name or cause.
func problemsMatch(row *problems.Row, query string) bool {
	text := strings.ToLower(strings.Join([]string{row.Severity.String(), row.Kind, problemsKindLabel(row.Kind, true),
		row.Namespace, row.Name, row.Cause}, " "))
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, token) {
			return false
		}
	}
	return true
}

// problemsLines flattens the ranked rows for display. A resource stays listed
// when one of its folded pods matches the filter, and that pod is shown.
func problemsLines(rows []problems.Row, expanded map[string]bool, query string) []problemsLine {
	lines := make([]problemsLine, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		own := problemsMatch(row, query)
		var children []problemsLine
		for j := range row.Children {
			child := &row.Children[j]
			if matches := problemsMatch(child, query); (expanded[row.Key()] && (own || matches)) || (!own && matches) {
				children = append(children, problemsLine{row: child, child: true})
			}
		}
		if !own && len(children) == 0 {
			continue
		}
		lines = append(lines, problemsLine{row: row, expanded: len(children) > 0})
		lines = append(lines, children...)
	}
	return lines
}

func problemsScope(namespace string) string {
	if namespace == "" {
		return "all namespaces"
	}
	return "namespace " + namespace
}

func problemsCounts(rows []problems.Row) string {
	counts := make(map[problems.Severity]int)
	for i := range rows {
		counts[rows[i].Severity]++
	}
	var parts []string
	for _, severity := range []problems.Severity{problems.Failing, problems.Degraded, problems.Progressing, problems.Warned} {
		if counts[severity] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[severity], severity))
		}
	}
	if len(parts) == 0 {
		return "no problems reported"
	}
	return strings.Join(parts, " · ")
}

// problemsSourceList names the sources that returned objects (read) or the
// ones that are missing or cut short (not read), each with its reason.
func problemsSourceList(snapshot *problems.Snapshot, read bool) string {
	var parts []string
	for _, source := range snapshot.Sources {
		text := ""
		switch {
		case source.State == problems.StateComplete && read:
			text = fmt.Sprintf("%s %d", source.Name, source.Count)
		case source.State == problems.StateIncomplete && read:
			text = fmt.Sprintf("%s %d+", source.Name, source.Count)
		case source.State == problems.StateIncomplete:
			text = fmt.Sprintf("%s incomplete (%s)", source.Name, source.Detail)
		case source.State != problems.StateComplete && !read:
			text = source.Name + " " + source.State
			if source.Detail != "" {
				text += " (" + source.Detail + ")"
			}
		default:
			continue
		}
		if source.Support && read {
			text += " (ownership only)"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " · ")
}

// problemsHeader states exactly what the list rests on: the scope, when it
// was read, what each source returned and which sources are missing. A source
// that was denied, timed out or cut short is always named.
func problemsHeader(snapshot *problems.Snapshot, namespace, status string, width int) []string {
	title := "Problems in " + problemsScope(namespace)
	if snapshot == nil {
		return []string{title, status}
	}
	lines := []string{title + " · " + problemsCounts(snapshot.Rows)}
	if status != "" {
		lines[0] += " · " + status
	}
	read := "Read at " + snapshot.ReadAt.Local().Format("15:04:05") + ": "
	gaps := snapshot.Gaps()
	if width >= 100 {
		read += problemsSourceList(snapshot, true) + " (last " + problemsWindow(snapshot.Window) + ")"
	} else {
		objects := 0
		for _, source := range snapshot.Sources {
			objects += source.Count
		}
		read += fmt.Sprintf("%d of %d sources complete, %s", len(snapshot.Sources)-len(gaps), len(snapshot.Sources), problemsPlural(objects, "object"))
	}
	lines = append(lines, read)
	if len(gaps) > 0 {
		caveat := " - problems there cannot be ruled out"
		if width < 100 {
			caveat = " (may hide problems)"
		}
		lines = append(lines, "NOT READ: "+problemsSourceList(snapshot, false)+caveat)
	} else {
		lines = append(lines, fmt.Sprintf("No source is missing; each read is one page of at most %d.", problems.ListLimit))
	}
	return lines
}

func problemsWindow(window time.Duration) string {
	if window == time.Hour {
		return "1h"
	}
	return window.String()
}

// problemsEmpty explains a quiet list without claiming health.
func problemsEmpty(snapshot *problems.Snapshot, namespace, query string) string {
	if query != "" && len(snapshot.Rows) > 0 {
		return fmt.Sprintf("No row matches filter %q. %d rows are hidden; Esc clears the filter.", query, len(snapshot.Rows))
	}
	checked := problemsSourceList(snapshot, true)
	if checked == "" {
		return "Nothing could be read in " + problemsScope(namespace) + ", so nothing is known. Not read: " +
			problemsSourceList(snapshot, false) + ". r reads again."
	}
	text := "No failing, degraded or in-progress resource is reported in " + problemsScope(namespace) +
		" by what was read: " + checked + " (last " + problemsWindow(snapshot.Window) + ")."
	if gaps := problemsSourceList(snapshot, false); gaps != "" {
		text += "\nNot read: " + gaps + ". Problems there cannot be ruled out."
	}
	return text + "\nThis is what each status reported at that moment, not a health check. r reads again."
}

// problemsDetail spells out the selected row in full: identity, complete cause
// and the Warning events counted as signals.
func problemsDetail(line *problemsLine, readAt time.Time) string {
	row := line.row
	identity := row.Kind + " " + row.Name
	if row.Namespace != "" {
		identity = row.Kind + " " + row.Namespace + "/" + row.Name
	}
	head := fmt.Sprintf("%s · %s · %s", identity, row.Severity, problemsPlural(row.Signals, "signal"))
	if age := problemsFor(row.Since, readAt); age != "-" {
		head += " · for " + age
	}
	cause := row.Cause
	if !line.child {
		cause = row.Summary()
	}
	if cause == "" {
		cause = "The status reports no reason."
	}
	text := head + "\n" + cause
	if len(row.Events) > 0 {
		var notes []string
		for _, note := range row.Events {
			item := fmt.Sprintf("%s x%d (%s ago)", note.Reason, note.Count, dailyWorkspaceAge(note.Last, readAt))
			if note.Message != "" {
				item += " " + note.Message
			}
			notes = append(notes, item)
		}
		text += "\nWarning events: " + strings.Join(notes, " · ")
	}
	return text
}

// problemsFooter keeps as many key hints as fit, most useful first. When the
// spelled-out hints do not fit, shorter labels are used so that filter and
// re-read stay on show at 80 and 60 columns.
func problemsFooter(width int, rows, pods bool) string {
	type hint struct{ long, short string }
	hints := []hint{{"r re-read", "r reread"}}
	if rows {
		hints = []hint{{"Enter open in list", "Enter open"}, {"d describe", "d desc"}, {"l logs", "l logs"}}
		if pods {
			hints = append(hints, hint{"Space pods", "Space pods"})
		}
		hints = append(hints, hint{"/ filter", "/ filter"}, hint{"r re-read", "r reread"})
	}
	hints = append(hints, hint{"Esc back", "Esc back"}, hint{"? help", "? help"})
	long := make([]string, 0, len(hints))
	for _, h := range hints {
		long = append(long, h.long)
	}
	if text := strings.Join(long, " · "); runewidth.StringWidth(text) <= width {
		return text
	}
	text := ""
	for _, h := range hints {
		next := strings.TrimPrefix(text+"  "+h.short, "  ")
		if runewidth.StringWidth(next) > width {
			break
		}
		text = next
	}
	return text
}

// problemsPack breaks text at its " · " separators so that an item such as
// "jobs 0" is never split between its name and its count by word wrap. A
// single item wider than the pane is left for the pane to wrap.
func problemsPack(text string, width int) string {
	if width < 1 {
		return text
	}
	const sep = " · "
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		line, used := "", 0
		for _, item := range strings.Split(paragraph, sep) {
			size := runewidth.StringWidth(item)
			switch {
			case line == "":
				line, used = item, size
			case used+runewidth.StringWidth(sep)+size+2 <= width: // room for a trailing " ·"
				line, used = line+sep+item, used+runewidth.StringWidth(sep)+size
			default:
				out = append(out, line+" ·")
				line, used = item, size
			}
			if used > width {
				// The pane wraps this item; continue from its last wrapped line.
				if wrapped := tview.WordWrap(line, width); len(wrapped) > 0 {
					used = runewidth.StringWidth(wrapped[len(wrapped)-1])
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// problemsWrappedRows is how many pane rows text occupies at width.
func problemsWrappedRows(text string, width int) int {
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		rows += max(1, len(tview.WordWrap(line, width)))
	}
	return rows
}
