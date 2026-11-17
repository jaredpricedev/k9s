// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package problems

import (
	"strconv"
	"strings"
	"time"
)

// AlertsSource is the name of the optional Alertmanager source. It is listed
// only when the context declares one.
const AlertsSource = "alerts"

// AlertNote is one Alertmanager alert about a listed resource. It is what
// Alertmanager reported at the read, not a status of the resource.
type AlertNote struct {
	Name, Severity string
	// State is "firing", "silenced" or "inhibited". Only a firing alert
	// counts as a signal or lists a resource.
	State   string
	Summary string
	Since   time.Time
	// Paging is true when the severity word is one that usually pages.
	Paging bool
	// Pod names the pod the alert is about when it is shown on the workload.
	Pod string
}

// AlertRef addresses an AlertNote to the object the alert's labels name.
type AlertRef struct {
	Kind, Namespace, Name string
	Note                  AlertNote
}

func (n *AlertNote) firing() bool { return n.State == "firing" }

// AddAlerts records the outcome of the alert read as one more source and
// attaches each alert to the row of the object it names. A firing alert on a
// listed resource adds a signal to that row. A firing alert on an object no
// row lists becomes its own row, marked FromAlert: its status, where it was
// read, reports no problem, which is the case status alone cannot show. An
// alert that is silenced or inhibited is noted on a listed row but never
// lists a resource. A row that only a Warning event listed becomes alerting
// when an alert is firing on it. It performs no read.
func (s *Snapshot) AddAlerts(source Source, refs []AlertRef) {
	source.Name = AlertsSource
	// Listed before the Warning events, whose time window closes the list.
	at := max(len(s.Sources)-1, 0)
	s.Sources = append(s.Sources[:at], append([]Source{source}, s.Sources[at:]...)...)
	top := make(map[string]*Row, len(s.Rows))
	child := make(map[string][2]*Row)
	for i := range s.Rows {
		row := &s.Rows[i]
		if !row.former {
			top[row.Key()] = row
		}
		for j := range row.Children {
			child[row.Children[j].Key()] = [2]*Row{row, &row.Children[j]}
		}
	}
	var added []*Row
	for i := range refs {
		ref := &refs[i]
		key := ident(ref.Kind, ref.Namespace, ref.Name)
		note := ref.Note
		switch {
		case top[key] != nil:
			top[key].attach(&note)
		case child[key][0] != nil:
			child[key][1].attach(&note)
			note.Pod = ref.Name
			child[key][0].attach(&note)
		case ref.Kind == "Pod" && top[s.owners[key]] != nil:
			note.Pod = ref.Name
			top[s.owners[key]].attach(&note)
		case note.firing():
			row := &Row{Severity: Alerting, Kind: ref.Kind, Resource: resources[ref.Kind], Namespace: ref.Namespace, Name: ref.Name,
				Since: note.Since, FromAlert: true}
			row.attach(&note)
			top[key] = row
			added = append(added, row)
		}
	}
	// A row that only a Warning event listed, and that an alert is firing on,
	// is listed as alerting: its status still reports no problem, but the
	// alert is the stronger of the two signals and leads the cause.
	for i := range s.Rows {
		row := &s.Rows[i]
		if firing := row.firingAlerts(); row.Severity == Warned && len(firing) > 0 {
			row.Severity = Alerting
			for n := range firing {
				if at := firing[n].Since; !at.IsZero() && (row.Since.IsZero() || at.Before(row.Since)) {
					row.Since = at
				}
			}
			row.Cause = bounded(strings.TrimSuffix(alertCause(firing)+" · "+row.Cause, " · "), 300)
		}
	}
	for _, row := range added {
		row.Cause = bounded(alertCause(row.Alerts), 300)
		s.Rows = append(s.Rows, *row)
	}
	sortRows(s.Rows)
}

func (r *Row) attach(note *AlertNote) {
	r.Alerts = append(r.Alerts, *note)
	if note.firing() {
		r.Signals++
		if r.FromAlert && (r.Since.IsZero() || (!note.Since.IsZero() && note.Since.Before(r.Since))) {
			r.Since = note.Since
		}
	}
}

func (r *Row) firingAlerts() []AlertNote {
	var firing []AlertNote
	for i := range r.Alerts {
		if r.Alerts[i].firing() {
			firing = append(firing, r.Alerts[i])
		}
	}
	return firing
}

// alertCause is the list phrase of a row that only an alert lists.
func alertCause(notes []AlertNote) string {
	if len(notes) == 1 {
		text := "alert " + notes[0].Name + " (" + notes[0].Severity + ")"
		if notes[0].Summary != "" {
			text += ": " + notes[0].Summary
		}
		return text
	}
	names := make([]string, 0, len(notes))
	for i := range notes {
		names = append(names, notes[i].Name+" ("+notes[i].Severity+")")
	}
	return strconv.Itoa(len(notes)) + " alerts: " + strings.Join(names, ", ")
}

// rank orders rows. An alert-only row whose alert usually pages follows the
// failing rows; any other alert-only row follows the degraded ones.
func (r *Row) rank() int {
	switch r.Severity {
	case Failing:
		return 0
	case Degraded:
		return 2
	case Progressing:
		return 4
	case Alerting:
		for i := range r.Alerts {
			if r.Alerts[i].Paging && r.Alerts[i].firing() {
				return 1
			}
		}
		return 3
	}
	return 5
}
