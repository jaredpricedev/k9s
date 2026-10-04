// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/derailed/k9s/internal/hubble"
)

type FlowSample struct {
	Context               string
	StartedAt, CapturedAt time.Time
	Events                []hubble.Event
	Status                hubble.Status
	Evicted               uint64
}

// ComposeFlows uses an explicitly captured native Hubble window. Peer names/IPs
// do not become Service identities, dependencies, authorization or current Pod UIDs.
func ComposeFlows(sample *FlowSample) *FlowWindow {
	w := &FlowWindow{StartedAt: sample.StartedAt, CapturedAt: sample.CapturedAt, Status: sample.Status, Evicted: sample.Evicted, Count: len(sample.Events)}
	w.Status.Error = safe(w.Status.Error)
	w.Status.CoverageError = safe(w.Status.CoverageError)
	w.Status.LossDetail = safe(w.Status.LossDetail)
	w.Status.NodeEvent = safe(w.Status.NodeEvent)
	w.Status.Nodes = append([]hubble.Node(nil), sample.Status.Nodes...)
	groups := make(map[string]int)
	groupPeers := make(map[string][2]string)
	groupConversations := make(map[string]string)
	for i := range sample.Events {
		e := &sample.Events[i]
		if !e.Time.IsZero() && (w.FirstReportedAt.IsZero() || e.Time.Before(w.FirstReportedAt)) {
			w.FirstReportedAt = e.Time
		}
		if e.Time.After(w.LastReportedAt) {
			w.LastReportedAt = e.Time
		}
		if e.Verdict == "DROPPED" {
			peers, conversation := flowKeys(e)
			key := conversation + " " + safe(e.DropReason)
			groups[key]++
			groupPeers[key], groupConversations[key] = peers, conversation
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys[:min(32, len(keys))] {
		item := Item{Group: GroupFlows, State: "reported drop", Summary: fmt.Sprintf("%d retained reports · %s", groups[key], key)}
		item.EndpointKeys, item.ConversationKey = groupPeers[key], groupConversations[key]
		item.Source.Kind = "Hubble drop group"
		item.Source.CapturedAt = sample.CapturedAt
		item.Source.Identity.Context = sample.Context
		item.fact("Drop grouping key", key)
		item.fact("Boundary", "Drop counts are retained flow reports in this window; identity, loss and policy attribution may be incomplete")
		w.Items = append(w.Items, item)
	}
	w.DropGroupsOmitted = max(0, len(keys)-32)
	groupRows := len(w.Items)
	for i := len(sample.Events) - 1; i >= 0 && len(w.Items) < MaxRelated; i-- {
		item := projectFlow(&sample.Events[i], sample.Context, sample.CapturedAt)
		w.Items = append(w.Items, item)
	}
	w.ProjectedOmitted = max(0, len(sample.Events)-(len(w.Items)-groupRows))
	return w
}

func projectFlow(e *hubble.Event, contextName string, capturedAt time.Time) Item {
	item := Item{Group: GroupFlows, State: safe(e.Verdict), Summary: safe(e.Source.String() + " → " + e.Destination.String() + " " + e.Protocol)}
	item.EndpointKeys, item.ConversationKey = flowKeys(e)
	item.Source.Kind = "Hubble Relay flow"
	item.Source.CapturedAt = capturedAt
	item.Source.Identity.Context = contextName
	item.fact("Retained flow record ID", fmt.Sprint(e.ID))
	reportedAt := "unknown / no API timestamp reported"
	if !e.Time.IsZero() {
		reportedAt = e.Time.UTC().Format(time.RFC3339Nano)
	}
	item.fact("Reported flow time", reportedAt)
	item.fact("Stream origin", e.Origin)
	item.fact("Source peer", e.Source.String())
	item.fact("Destination peer", e.Destination.String())
	item.fact("Reported ports", fmt.Sprintf("%d → %d", e.SourcePort, e.DestinationPort))
	item.fact("Drop reason", e.DropReason)
	item.fact("Reported policy attribution", e.Policy)
	item.fact("Identity boundary", "Reported cluster/name/IP is not a proven Service binding or Pod UID; no cross-cluster identity mapping applied")
	if e.DNS.Reported {
		item.fact("Reported DNS query", e.DNS.Query)
		item.fact("Reported DNS answers", e.DNS.Answers)
		item.fact("Reported DNS CNAMEs", e.DNS.CNames)
		item.fact("DNS record source", e.DNS.RecordType+" · "+e.DNS.ObservationSource)
		latency := "unknown / not reported"
		if e.DNS.LatencyNs > 0 {
			latency = fmt.Sprintf("%d ns reported", e.DNS.LatencyNs)
		}
		item.fact("Reported DNS latency", latency)
		item.fact("DNS boundary", "An empty answer/query record is not a failed lookup; no L7 collection was enabled by this review")
	}
	return item
}

func flowKeys(e *hubble.Event) (keys [2]string, conversation string) {
	source, _ := json.Marshal(e.Source)
	destination, _ := json.Marshal(e.Destination)
	keys = [2]string{safe(string(source)), safe(string(destination))}
	if e.Source.Pod == "" && e.Source.IP == "" && e.Source.Names == "" {
		keys[0] = ""
	}
	if e.Destination.Pod == "" && e.Destination.IP == "" && e.Destination.Names == "" {
		keys[1] = ""
	}
	if keys[0] == "" || keys[1] == "" {
		return keys, ""
	}
	return keys, safe(keys[0] + " → " + keys[1] + " " + e.Protocol + " " + fmt.Sprint(e.SourcePort) + "/" + fmt.Sprint(e.DestinationPort))
}
