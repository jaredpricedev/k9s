// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/observability"
	"github.com/mattn/go-runewidth"
)

// alertsLine is one visible table row: a section heading, a ranked alert row
// or, under an expanded row, one of the alerts folded into it.
type alertsLine struct {
	heading  string // non-empty for a section heading, which is not selectable
	group    *alerts.Group
	item     *alerts.Item // the single alert of the row; nil for a folded row
	child    bool
	expanded bool
}

// alertsColumns is the column budget for one viewport width, in terminal
// cells. Severity, alert name and object are kept at every width; the state
// word, the age and the summary give way in that order of need.
type alertsColumns struct {
	severity, state, name, object, age, summary int
}

const (
	alertsSectionUnmapped   = "not mapped to an object"
	alertsSectionSuppressed = "silenced or inhibited: not notifying"
)

func alertsShortKind(kind string, short bool) string {
	return problemsKindLabel(kind, short)
}

// alertsObject is the object cell before elision.
func alertsObject(target *alerts.Target, short bool) string {
	if !target.Mapped() {
		return "not mapped"
	}
	kind := alertsShortKind(target.Kind, short)
	if target.Namespace == "" {
		return kind + " " + target.Name
	}
	return kind + " " + target.Namespace + "/" + target.Name
}

func alertsLineObject(line *alertsLine, short bool) string {
	if line.item != nil {
		text := alertsObject(&line.item.Target, short)
		if line.item.Missing {
			text += " (not found)"
		}
		return text
	}
	// The count leads so that it survives elision of a long name.
	return fmt.Sprintf("%s of %s", problemsPlural(len(line.group.Items), "pod"), alertsObject(&line.group.Target, short))
}

// alertsLineName is the alert cell. A folded row carries an expand marker and
// its alerts are indented under it; the markers have an ASCII form.
func alertsLineName(line *alertsLine, ascii bool) string {
	name := line.group.Name
	switch {
	case line.child && ascii:
		return "  | " + name
	case line.child:
		return "  └ " + name
	case !line.group.Folded():
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

func alertsLineSince(line *alertsLine) time.Time {
	if line.item != nil {
		return line.item.Alert.StartsAt
	}
	return line.group.Since
}

func alertsLineSummary(line *alertsLine) string {
	if line.item != nil {
		return line.item.Summary()
	}
	return line.group.Items[0].Summary()
}

// alertsLayout divides width between the columns for the lines on show.
func alertsLayout(width int, lines []alertsLine, ascii bool, readAt time.Time) alertsColumns {
	short := width < 100
	c := alertsColumns{severity: len("SEVERITY")}
	name, object, age := len("ALERT"), len("OBJECT"), len("FOR")
	for i := range lines {
		line := &lines[i]
		if line.heading != "" {
			continue
		}
		c.severity = max(c.severity, runewidth.StringWidth(line.group.Severity))
		name = max(name, runewidth.StringWidth(alertsLineName(line, ascii)))
		object = max(object, runewidth.StringWidth(alertsLineObject(line, short)))
		age = max(age, len(problemsFor(alertsLineSince(line), readAt)))
	}
	c.severity = min(c.severity, 10)
	rest := width - c.severity - 1
	if width >= 72 {
		c.state, c.age = len(alerts.Inhibited), age
		rest -= c.state + 1 + c.age + 1
	}
	c.name = max(5, min(name, 32, rest/2))
	rest -= c.name + 1
	// The summary appears only when the object keeps a useful width beside it.
	if room := rest - min(object, 44) - 1; width >= 100 && room >= 14 {
		c.object = min(object, 44)
		c.summary = room
	} else {
		c.object = max(rest, 1)
	}
	return c
}

func alertsHeading(c alertsColumns) string {
	text := timelineCell("SEVERITY", c.severity) + " "
	if c.state > 0 {
		text += timelineCell("STATE", c.state) + " "
	}
	text += timelineCell("ALERT", c.name) + " " + timelineCell("OBJECT", c.object)
	if c.age > 0 {
		text += " " + timelineCell("FOR", c.age)
	}
	if c.summary > 0 {
		text += " " + timelineCell("SUMMARY", c.summary)
	}
	return text
}

// alertsCells renders one line to fit the budget: severity, then the rest.
// Text is cut by terminal cell width with a visible ellipsis; an object name
// keeps both ends, because the generated suffix tells pods apart.
func alertsCells(line *alertsLine, c alertsColumns, ascii, short bool, readAt time.Time) (severity, rest string) {
	severity = timelineCell(line.group.Severity, c.severity) + " "
	if c.state > 0 {
		rest += timelineCell(line.group.State, c.state) + " "
	}
	object := timelineName(alertsLineObject(line, short), c.object)
	if line.item == nil {
		object = timelineCell(alertsLineObject(line, short), c.object)
	}
	rest += timelineCell(alertsLineName(line, ascii), c.name) + " " + object
	if c.age > 0 {
		rest += " " + timelineCell(problemsFor(alertsLineSince(line), readAt), c.age)
	}
	if c.summary > 0 {
		rest += " " + timelineCell(alertsLineSummary(line), c.summary)
	}
	return severity, rest
}

// alertsMatch is a literal, case-insensitive filter: every token must occur
// in the alert's name, severity, state, object, owner, summary or a label.
func alertsMatch(item *alerts.Item, query string) bool {
	parts := []string{item.Name(), item.Severity(), item.State(), item.Target.Label(), alertsObject(&item.Target, true), item.Summary()}
	if item.Owner != nil {
		parts = append(parts, item.Owner.Label(), alertsObject(item.Owner, true))
	}
	for key, value := range item.Alert.Labels {
		parts = append(parts, key+"="+value)
	}
	text := strings.ToLower(strings.Join(parts, " "))
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, token) {
			return false
		}
	}
	return true
}

// alertsLines flattens the ranked rows for display under their section
// headings. A folded row stays listed when one of its alerts matches the
// filter, and then only the matching alerts are shown under it.
func alertsLines(groups []alerts.Group, expanded map[string]bool, query string, showSuppressed bool) []alertsLine {
	lines := make([]alertsLine, 0, len(groups)+2)
	section := 0
	for i := range groups {
		group := &groups[i]
		if group.Section() == 2 && !showSuppressed {
			continue
		}
		matched := 0
		for j := range group.Items {
			if alertsMatch(&group.Items[j], query) {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		if s := group.Section(); s != section {
			section = s
			heading := alertsSectionUnmapped
			if s == 2 {
				heading = alertsSectionSuppressed
			}
			lines = append(lines, alertsLine{heading: heading})
		}
		if !group.Folded() {
			lines = append(lines, alertsLine{group: group, item: &group.Items[0]})
			continue
		}
		open := expanded[group.Key()] || matched < len(group.Items)
		lines = append(lines, alertsLine{group: group, expanded: open})
		if !open {
			continue
		}
		for j := range group.Items {
			if alertsMatch(&group.Items[j], query) {
				lines = append(lines, alertsLine{group: group, item: &group.Items[j], child: true})
			}
		}
	}
	return lines
}

// alertsCounts is the header tally: what is firing by severity, and what is
// held but not notifying.
func alertsCounts(items []alerts.Item, compact, hidden bool) string {
	counts := alerts.Count(items)
	if len(items) == 0 {
		return "Alertmanager holds no alert"
	}
	severities := make([]string, 0, len(counts.BySeverity))
	for severity := range counts.BySeverity {
		severities = append(severities, severity)
	}
	sort.Slice(severities, func(i, j int) bool {
		if a, b := alerts.SeverityRank(severities[i]), alerts.SeverityRank(severities[j]); a != b {
			return a < b
		}
		return severities[i] < severities[j]
	})
	firing := fmt.Sprintf("%d firing", counts.Firing)
	if len(severities) > 0 {
		words := make([]string, 0, len(severities))
		for _, severity := range severities {
			words = append(words, fmt.Sprintf("%d %s", counts.BySeverity[severity], severity))
		}
		firing += " (" + strings.Join(words, ", ") + ")"
	}
	parts := []string{firing}
	tally := []struct {
		count int
		word  string
	}{{counts.Silenced, alerts.Silenced}, {counts.Inhibited, alerts.Inhibited}, {counts.Unprocessed, alerts.Unprocessed}, {counts.Unmapped, "not mapped"}}
	if compact || hidden {
		// The section heading in the list says which are silenced or inhibited.
		tally = tally[2:]
		tally[0] = struct {
			count int
			word  string
		}{counts.Silenced + counts.Inhibited + counts.Unprocessed, "not notifying"}
		if hidden {
			tally[0].word += ", hidden (s shows)"
		}
	}
	if compact {
		tally[len(tally)-1].word = "unmapped"
	}
	for _, item := range tally {
		if item.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.count, item.word))
		}
	}
	return strings.Join(parts, " · ")
}

func alertsSourceWord(status *observability.Status) string {
	text := "Alertmanager " + string(status.State)
	if status.Reason != "" {
		text += " (" + status.Reason + ")"
	}
	return text
}

// alertsOwnerGap names an owner read that fell short, or "" when none did.
func alertsOwnerGap(owners *alerts.OwnerRead) string {
	switch owners.State {
	case alerts.OwnersDenied:
		return "pod owners denied (" + owners.Detail + ")"
	case alerts.OwnersIncomplete:
		return "pod owners incomplete (" + owners.Detail + ")"
	case alerts.OwnersUnknown:
		return "pod owners unknown (" + owners.Detail + ")"
	}
	return ""
}

// alertsHeader states what the list rests on: the counts, the source, when
// it was read and anything that was not read. Below 100 columns the standing
// reminder gives its row to the list; a NOT READ line is never dropped.
func alertsHeader(snapshot *alerts.Snapshot, source, status string, hidden, width int) []string {
	if snapshot == nil {
		return []string{source, status}
	}
	at := snapshot.ReadAt.Local().Format("15:04:05")
	if !snapshot.Status.OK() {
		lines := []string{"Not read"}
		if status != "" {
			lines[0] += " · " + status
		}
		return append(lines, "NOT READ: "+alertsSourceWord(&snapshot.Status)+" at "+at+" · what is firing is unknown, not empty",
			"Source: "+source)
	}
	lines := []string{alertsCounts(snapshot.Items, width < 100, hidden > 0)}
	if status != "" {
		lines[0] += " · " + status
	}
	read := "Read at " + at + " from " + source
	if width < 72 {
		read = "Read " + at + ", " + strings.TrimPrefix(source, "Alertmanager ")
	}
	if owners := &snapshot.Owners; width >= 100 && owners.Asked > 0 && owners.State != alerts.OwnersDenied {
		read += fmt.Sprintf(" · owner read for %s", problemsPlural(owners.Resolved, "pod"))
	}
	lines = append(lines, read)
	var gaps []string
	if snapshot.NotRetained > 0 {
		gaps = append(gaps, fmt.Sprintf("%d alerts beyond the limit of %d", snapshot.NotRetained, observability.MaxAlerts))
	}
	if gap := alertsOwnerGap(&snapshot.Owners); gap != "" {
		gaps = append(gaps, gap+", those alerts are listed by pod")
	}
	switch {
	case len(gaps) > 0:
		lines = append(lines, "NOT READ: "+strings.Join(gaps, " · "))
	case width >= 100:
		lines = append(lines, "One read, no polling. Resolved alerts are not held, so they are not listed.")
	}
	return lines
}

// alertsEmpty explains a list with no rows without claiming health.
func alertsEmpty(snapshot *alerts.Snapshot, query string, hidden int) string {
	switch {
	case !snapshot.Status.OK() && snapshot.Status.State == observability.StateNotConfigured:
		return "No Alertmanager source is declared for this context, so nothing was contacted.\n" +
			":sources shows the declaration and suggests in-cluster services."
	case !snapshot.Status.OK():
		return "Nothing was read: " + alertsSourceWord(&snapshot.Status) + ".\n" +
			"What is firing is unknown. This is not \"no alerts\". r reads again; :sources probes the declaration."
	case query != "" && len(snapshot.Items) > 0:
		return fmt.Sprintf("No alert matches filter %q. %d are hidden; Esc clears the filter.", query, len(snapshot.Items))
	case hidden > 0:
		return fmt.Sprintf("No alert is firing. %s silenced or inhibited %s hidden; s shows them.", problemsPlural(hidden, "alert"),
			map[bool]string{true: "is", false: "are"}[hidden == 1])
	}
	return "Alertmanager answered and holds no alert at this moment.\n" +
		"That is what its rules reported, not a health check: a rule that does not exist cannot fire. r reads again."
}

func alertsStamp(at time.Time) string {
	if at.IsZero() {
		return "start time not reported"
	}
	return at.UTC().Format("2006-01-02 15:04:05Z")
}

func alertsOwnerPhrase(item *alerts.Item) string {
	if item.Owner == nil {
		return ""
	}
	return fmt.Sprintf(" of %s %s (from %s)", item.Owner.Kind, item.Owner.Name, item.OwnerFrom)
}

func alertsSuppression(item *alerts.Item, names map[string]string) string {
	alert := &item.Alert
	switch {
	case alert.Silenced():
		return "Silenced by silence " + strings.Join(alert.SilencedBy, ", ") + ": it is held but does not notify."
	case alert.Inhibited():
		by := make([]string, 0, len(alert.InhibitedBy))
		for _, fingerprint := range alert.InhibitedBy {
			if name := names[fingerprint]; name != "" {
				by = append(by, name+" ("+fingerprint+")")
			} else {
				by = append(by, fingerprint)
			}
		}
		return "Inhibited by " + strings.Join(by, ", ") + ": it is held but does not notify."
	case !item.IsFiring():
		return "Alertmanager reports it as " + item.State() + "."
	}
	return ""
}

func alertsSortedPairs(values map[string]string, skip ...string) []string {
	keys := make([]string, 0, len(values))
outer:
	for key := range values {
		for _, name := range skip {
			if key == name {
				continue outer
			}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		keys[i] = key + "=" + values[key]
	}
	return keys
}

// alertsDetail is the pane under the list. The head line is returned apart so
// the caller can embolden it; the rest is plain text. width is the pane width
// in cells, or 0 when the text is not laid out for a pane.
func alertsDetail(line *alertsLine, readAt time.Time, names map[string]string, width int) (head, body string) {
	group := line.group
	age := problemsFor(alertsLineSince(line), readAt)
	head = fmt.Sprintf("%s · %s · %s", group.Name, group.Severity, group.State)
	if age != "-" {
		head += " for " + age
	}
	if line.item == nil {
		pods := make([]string, 0, len(group.Items))
		for i := range group.Items {
			pods = append(pods, group.Items[i].Target.Name)
		}
		head += fmt.Sprintf(" · %d alerts", len(group.Items))
		lines := []string{fmt.Sprintf("%s: the same alert on %s (%s)", group.Target.Label(), problemsPlural(len(pods), "pod"), strings.Join(pods, ", ")),
			"Folded by " + group.Items[0].OwnerFrom + ". Earliest began " + alertsStamp(group.Since) + ". Space lists each alert."}
		if summary := group.Items[0].Summary(); summary != "" {
			lines = append(lines, summary)
		}
		return head, strings.Join(lines, "\n")
	}
	item := line.item
	var lines []string
	if item.Target.Mapped() {
		object := item.Target.Label() + alertsOwnerPhrase(item) + " · mapped from labels " + strings.Join(item.Target.From, " + ")
		if item.Missing {
			object += " · NOT FOUND at the read: the pod may have been replaced"
		}
		lines = append(lines, object)
	} else {
		lines = append(lines, "Not mapped to an object: its labels name no namespace, workload, pod or node.")
	}
	if text := alertsSuppression(item, names); text != "" {
		lines = append(lines, text)
	}
	summary, description := item.Summary(), item.Alert.Annotations["description"]
	if summary != "" {
		lines = append(lines, summary)
	}
	if description != "" && description != summary {
		lines = append(lines, description)
	}
	when := "Began " + alertsStamp(item.Alert.StartsAt)
	if runbook := item.Runbook(); runbook != "" {
		// A link that would be broken across two rows starts its own row,
		// where it has the whole width before it has to wrap.
		if joined := when + " · runbook " + runbook; width <= 0 || runewidth.StringWidth(joined) <= width {
			when = joined
		} else {
			when += "\nRunbook: " + runbook
		}
	}
	lines = append(lines, when, "Labels: "+strings.Join(alertsSortedPairs(item.Alert.Labels, "alertname"), " · "))
	if item.Alert.GeneratorURL != "" {
		lines = append(lines, "Generator: "+item.Alert.GeneratorURL)
	}
	return head, strings.Join(lines, "\n")
}

// alertsReport is the complete plain-text statement of one alert, for the
// scrollable full view. URLs are text; nothing here is contacted.
func alertsReport(item *alerts.Item, snapshot *alerts.Snapshot, source string, names map[string]string) string {
	var b strings.Builder
	alert := &item.Alert
	fmt.Fprintf(&b, "ALERT %s\nSeverity: %s\nState: %s", item.Name(), item.Severity(), item.State())
	if age := problemsFor(alert.StartsAt, snapshot.ReadAt); age != "-" {
		fmt.Fprintf(&b, " for %s", age)
	}
	fmt.Fprintf(&b, "\nRead: %s from %s\n", alertsStamp(snapshot.ReadAt), source)
	if text := alertsSuppression(item, names); text != "" {
		b.WriteString(text + "\n")
	}
	b.WriteString("\nOBJECT\n")
	if item.Target.Mapped() {
		fmt.Fprintf(&b, "%s\nMapped from labels: %s\n", item.Target.Label(), strings.Join(item.Target.From, " + "))
		switch {
		case item.Owner != nil:
			fmt.Fprintf(&b, "Workload: %s %s (from %s)\n", item.Owner.Kind, item.Owner.Name, item.OwnerFrom)
		case item.Target.Kind == "Pod":
			b.WriteString("Workload: not known (no workload label; owner references not read or none)\n")
		}
		if item.Missing {
			b.WriteString("NOT FOUND at the read: the pod may have been replaced since the alert began.\n")
		}
		b.WriteString("The mapping follows the alert's labels. It is not checked against what the rule measures.\n")
	} else {
		b.WriteString("Not mapped to an object: its labels name no namespace, workload, pod or node.\n")
	}
	b.WriteString("\nTIMES\n")
	fmt.Fprintf(&b, "Started: %s\n", alertsStamp(alert.StartsAt))
	if !alert.UpdatedAt.IsZero() {
		fmt.Fprintf(&b, "Updated: %s\n", alertsStamp(alert.UpdatedAt))
	}
	if !alert.EndsAt.IsZero() {
		fmt.Fprintf(&b, "Ends:    %s (when Alertmanager drops it unless it is sent again)\n", alertsStamp(alert.EndsAt))
	}
	b.WriteString("\nLINKS (text only; never contacted)\n")
	runbook, generator := item.Runbook(), alert.GeneratorURL
	if runbook == "" {
		runbook = "none in the annotations"
	}
	if generator == "" {
		generator = "not reported"
	}
	fmt.Fprintf(&b, "Runbook:   %s\nGenerator: %s\n", runbook, generator)
	b.WriteString("\nLABELS\n")
	for _, pair := range alertsSortedPairs(alert.Labels) {
		b.WriteString("  " + pair + "\n")
	}
	b.WriteString("\nANNOTATIONS\n")
	pairs := alertsSortedPairs(alert.Annotations)
	if len(pairs) == 0 {
		b.WriteString("  none\n")
	}
	for _, pair := range pairs {
		b.WriteString("  " + pair + "\n")
	}
	if len(alert.Receivers) > 0 {
		fmt.Fprintf(&b, "\nRECEIVERS\n  %s\n", strings.Join(alert.Receivers, ", "))
	}
	if alert.Fingerprint != "" {
		fmt.Fprintf(&b, "\nFingerprint: %s\n", alert.Fingerprint)
	}
	return b.String()
}

// alertsFooter keeps as many key hints as fit. When the spelled-out hints do
// not fit, shorter labels are used and the row actions give way first, so
// that the silenced toggle, filter and re-read stay on show at 60 columns.
func alertsFooter(width int, rows, folded bool) string {
	type hint struct {
		long, short string
		keep        int // higher outlasts lower
	}
	hints := []hint{{"r re-read", "r reread", 9}}
	if rows {
		hints = []hint{{"Enter open", "Enter open", 5}, {"t timeline", "t timeline", 4}, {"d all fields", "d fields", 3},
			{"c copy runbook", "c copy", 1}}
		if folded {
			hints = append(hints, hint{"Space expand", "Space fold", 2})
		}
		hints = append(hints, hint{"s silenced", "s silenced", 6}, hint{"/ filter", "/ filter", 7}, hint{"r re-read", "r reread", 8})
	}
	hints = append(hints, hint{"Esc back", "Esc", 9})
	long := make([]string, 0, len(hints))
	for _, h := range hints {
		long = append(long, h.long)
	}
	if text := strings.Join(long, " · "); runewidth.StringWidth(text) <= width {
		return text
	}
	for floor := 0; ; floor++ {
		parts := make([]string, 0, len(hints))
		for _, h := range hints {
			if h.keep > floor {
				parts = append(parts, h.short)
			}
		}
		if text := strings.Join(parts, "  "); runewidth.StringWidth(text) <= width || len(parts) <= 1 {
			return runewidth.Truncate(text, max(width, 0), "…")
		}
	}
}
