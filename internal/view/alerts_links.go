// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/problems"
)

// problemsAddAlerts hands one alert observation to a problems snapshot as one
// more source. Only alerts whose labels name an object inside the snapshot's
// scope are passed on: a node, or an object in the namespace that was read.
// Alerts that name no object are left to :alerts.
func problemsAddAlerts(snapshot *problems.Snapshot, observed *alerts.Snapshot) {
	source := problems.Source{State: problems.StateComplete}
	switch status := &observed.Status; status.State {
	case observability.StateComplete:
	case observability.StateIncomplete:
		source.State, source.Detail = problems.StateIncomplete, status.Reason
	case observability.StateDenied:
		source.State, source.Detail = problems.StateDenied, status.Reason
	case observability.StateTimeout:
		source.State, source.Detail = problems.StateUnknown, "timed out"
	default:
		source.State, source.Detail = problems.StateUnknown, string(status.State)
		if status.Reason != "" {
			source.Detail += ": " + status.Reason
		}
	}
	var refs []problems.AlertRef
	for i := range observed.Items {
		item := &observed.Items[i]
		target := &item.Target
		switch {
		case !target.Mapped():
			continue
		case target.Kind == "Node":
		case target.Kind == "Namespace":
			if snapshot.Namespace != "" && target.Name != snapshot.Namespace {
				continue
			}
		case snapshot.Namespace != "" && target.Namespace != snapshot.Namespace:
			continue
		}
		source.Count++
		refs = append(refs, problems.AlertRef{Kind: target.Kind, Namespace: target.Namespace, Name: target.Name,
			Note: problems.AlertNote{Name: item.Name(), Severity: item.Severity(), State: item.State(), Summary: item.Summary(),
				Since: item.Alert.StartsAt, Paging: alerts.SeverityRank(item.Severity()) <= 1}})
	}
	snapshot.AddAlerts(source, refs)
}
