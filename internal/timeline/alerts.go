// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package timeline

import (
	"fmt"

	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/observability"
)

// SourceAlert marks an entry placed at the time Alertmanager says an alert
// began. It is listed only when the context declares an Alertmanager source.
const (
	SourceAlert  Source = "Alert"
	sourceAlerts        = "Alerts"
)

// InScope reports whether a resource belongs to what this timeline follows:
// the selected workload, its pods, what they reference and the nodes they run
// on, or for a namespace timeline everything in the namespace.
func (s *Snapshot) InScope(kind, namespace, name string) bool {
	if kind == "Node" {
		return s.scoped["Node/"+name]
	}
	if namespace != s.Scope.Namespace {
		return false
	}
	return s.Scope.Kind == "" || s.scoped[kind+"/"+name]
}

// AddAlerts places the start of each alert about a scoped resource in the
// order, and records the alert read as one more source. A firing alert is a
// failing signal; a silenced or inhibited one is listed and worded as such.
// A read that did not answer is reported as a gap, never as "no alerts".
func (s *Snapshot) AddAlerts(status observability.Status, items []alerts.Item) {
	coverage := Coverage{Source: sourceAlerts, State: Complete}
	switch status.State {
	case observability.StateComplete:
	case observability.StateIncomplete:
		coverage.State = Incomplete
	case observability.StateDenied:
		coverage.State = Denied
	default:
		coverage.State = Unknown
	}
	if !status.OK() {
		coverage.Detail = "Alertmanager " + string(status.State)
		if status.Reason != "" {
			coverage.Detail += " (" + status.Reason + ")"
		}
		coverage.Detail += "; alert history is unknown, not empty"
		s.Coverage = append(s.Coverage, coverage)
		return
	}
	untimed := 0
	for i := range items {
		item := &items[i]
		target := &item.Target
		inScope := target.Mapped() && s.InScope(target.Kind, target.Namespace, target.Name)
		if owner := item.Owner; !inScope && owner != nil {
			inScope = s.InScope(owner.Kind, owner.Namespace, owner.Name)
		}
		if !inScope {
			continue
		}
		if item.Alert.StartsAt.IsZero() {
			untimed++
			continue
		}
		coverage.Count++
		state := ""
		if !item.IsFiring() {
			state = ", " + item.State()
		}
		summary := fmt.Sprintf("%s (%s%s) began", item.Name(), item.Severity(), state)
		if item.IsFiring() {
			summary += " firing"
		}
		if text := item.Summary(); text != "" {
			summary += ": " + text
		}
		entry := Entry{At: item.Alert.StartsAt, Source: SourceAlert, Origin: "Alertmanager startsAt, read at capture", Summary: summary,
			Ref:     Ref{Resource: target.Resource, Kind: target.Kind, Namespace: target.Namespace, Name: target.Name},
			Failing: item.IsFiring()}
		if text := item.Alert.Annotations["description"]; text != "" && text != item.Summary() {
			entry.Detail = append(entry.Detail, text)
		}
		if text := item.Runbook(); text != "" {
			entry.Detail = append(entry.Detail, "runbook "+text)
		}
		s.Entries = append(s.Entries, entry)
	}
	coverage.Detail = fmt.Sprintf("%d held by Alertmanager, %d about this scope", len(items), coverage.Count)
	if status.Reason != "" {
		coverage.Detail += "; " + status.Reason
	}
	if untimed > 0 {
		coverage.Detail += fmt.Sprintf("; %d without a start time, not placed", untimed)
	}
	s.Coverage = append(s.Coverage, coverage)
	s.Notes = append(s.Notes, "Alert entries are placed at the start time Alertmanager reports for alerts it still holds. "+
		"A resolved alert is not listed, and a rule's \"for\" delay means the condition began before the alert did.")
	s.finish()
}
