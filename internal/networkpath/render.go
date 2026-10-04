// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

var Tabs = []string{"Path", "DNS", "Backends", "Routes", "Policies", "Flows", "Evidence"}

func (s *Snapshot) TabItems(tab int) []*Item {
	groups := map[int]string{1: GroupDNS, 2: GroupBackends, 3: GroupRoutes, 4: GroupPolicies, 5: GroupFlows}
	rows := make([]*Item, 0)
	if tab == 5 && s.Flows != nil {
		for i := range s.Flows.Items {
			rows = append(rows, &s.Flows.Items[i])
		}
		return rows
	}
	for i := range s.Items {
		if s.Items[i].Group == groups[tab] {
			rows = append(rows, &s.Items[i])
		}
	}
	return rows
}

func ItemKey(item *Item) string {
	key := item.Source.Identity.GVR + "/" + item.Source.Identity.Namespace + "/" + item.Source.Identity.Name + "/" + item.Source.Identity.UID + "/" + item.Group
	for _, fact := range item.Facts {
		if fact.Name == "Addresses" || fact.Name == "Configured entrypoint" || fact.Name == "Backend port" ||
			fact.Name == "Retained flow record ID" || fact.Name == "Drop grouping key" || fact.Name == "Configured HTTP match" {
			key += "/" + fact.Value
		}
	}
	return key
}

func (s *Snapshot) GapCount() int {
	gaps := 0
	for _, c := range s.Coverage {
		if c.State != "complete" {
			gaps++
		}
	}
	if s.Omitted > 0 {
		gaps++
	}
	for i := range s.Items {
		if len(s.Items[i].Gaps) > 0 {
			gaps++
		}
	}
	if s.Flows == nil || !s.Flows.Status.CoverageKnown || s.Flows.Status.Lost > 0 || s.Flows.Evicted > 0 || s.Flows.ProjectedOmitted > 0 || s.Flows.DropGroupsOmitted > 0 {
		gaps++
	}
	return gaps
}

func (s *Snapshot) Render(tab, selected, width, height int) string {
	if tab == 0 {
		return s.pathSummary()
	}
	if tab == 6 {
		return s.Evidence(nil)
	}
	items := s.TabItems(tab)
	if len(items) == 0 {
		if tab == 5 && s.Flows != nil {
			return "No retained flow reports in captured window.\nRelay: " + safe(s.Flows.Status.Error) + "\n" +
				"Coverage/filter/node/loss details remain in Evidence. Empty or unavailable observation does not prove no traffic.\nh reopens native peer/conversation view."
		}
		if tab == 5 {
			return "Flow observation not requested or no retained reports.\nh opens native Hubble peers/conversations.\n" +
				"Relay absence, filters or an empty window do not prove no traffic.\nL7 collection and endpoint probes are never automatically enabled."
		}
		return "No matching current evidence retained for this section.\nInspect Evidence for denied/absent/truncated queries.\n" +
			"An empty section does not establish connectivity or health."
	}
	selected = max(0, min(selected, len(items)-1))
	var b strings.Builder
	shown := max(1, min(len(items), height-5))
	start := max(0, min(selected-shown/2, len(items)-shown))
	for i := start; i < start+shown; i++ {
		marker := "  "
		if i == selected {
			marker = "> "
		}
		item := items[i]
		if item.Pinned {
			marker = "* "
		}
		if item.Pinned && i == selected {
			marker = "*>"
		}
		name := item.Source.Identity.Name
		if name == "" {
			name = item.Source.Kind
		}
		fmt.Fprintf(&b, "%s%s\n", marker, clip(item.State+" "+name+" · "+item.Summary, width-2))
	}
	item := items[selected]
	fmt.Fprintf(&b, "\n%s %s/%s\n", item.Source.Kind, item.Source.Identity.Namespace, item.Source.Identity.Name)
	for _, fact := range item.Facts[:min(2, len(item.Facts))] {
		fmt.Fprintf(&b, "%s: %s\n", fact.Name, fact.Value)
	}
	fmt.Fprintf(&b, "%d/%d · j/k select · v exact evidence", selected+1, len(items))
	return b.String()
}

func (s *Snapshot) pathSummary() string {
	if s.Service.Source.Identity.UID == "" {
		return "Captured Service source unavailable.\nNo configured path obtained; inspect Evidence query coverage.\n" +
			"A replacement UID never substitutes for the selected source."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Configured path · connectivity untested\n%s.%s.svc → Service → reported EndpointSlices / Pods\n",
		s.Service.Source.Identity.Name, s.Service.Source.Identity.Namespace)
	for _, port := range s.Service.Ports {
		fmt.Fprintf(&b, "Port %s %d/%s → targetPort %s\n", port.Name, port.Number, port.Protocol, port.Target)
	}
	fmt.Fprintf(&b, "\n%d obtained Pods · %d backend records · %d configured entrypoints · %d policy candidates\n",
		len(s.Pods), len(s.TabItems(2)), len(s.TabItems(3)), len(s.TabItems(4)))
	fmt.Fprintln(&b, "DNS configuration, endpoint readiness and controller conditions are separate evidence.")
	if s.Flows == nil {
		fmt.Fprintln(&b, "Observed flows: not requested · h opens native Hubble peers/conversations")
	} else {
		fmt.Fprintf(&b, "Observed flows: %d retained reports · %s\nWindow %s to %s · lost %d · evicted %d\n", s.Flows.Count, s.Flows.Status.Phase,
			s.Flows.StartedAt.UTC().Format(time.RFC3339), s.Flows.CapturedAt.UTC().Format(time.RFC3339), s.Flows.Status.Lost, s.Flows.Evicted)
		fmt.Fprintf(&b, "Projected flow reports omitted %d · drop groups omitted %d · retained pins %d\n",
			s.Flows.ProjectedOmitted, s.Flows.DropGroupsOmitted, len(s.Flows.Pins))
	}
	fmt.Fprintf(&b, "\nCollection gaps %d · omitted %d\n"+
		"Current configuration and a filtered flow window are not a connectivity or authorization proof.\n", s.GapCount(), s.Omitted)
	return b.String()
}

func (s *Snapshot) Evidence(item *Item) string {
	value := any(s)
	if item != nil {
		value = item
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "Retained evidence unavailable: " + err.Error()
	}
	return fmt.Sprintf("Context: %s\nCaptured Service: %s/%s UID=%s\nRoute namespaces: %s\nCollection window: %s to %s\n"+
		"Configured paths and policy candidates are not proven connectivity. Observed flow peers do not establish Service/Pod UIDs or continuous history.\n\n%s",
		s.Scope.Service.Context, s.Scope.Service.Namespace, s.Scope.Service.Name, s.Scope.Service.UID, strings.Join(s.Scope.RouteNamespaces, ", "),
		s.StartedAt.UTC().Format(time.RFC3339Nano), s.CapturedAt.UTC().Format(time.RFC3339Nano), string(data))
}

func clip(value string, width int) string {
	runes := []rune(safe(value))
	if len(runes) > max(1, width) {
		return string(runes[:max(0, width-1)]) + "…"
	}
	return string(runes)
}
