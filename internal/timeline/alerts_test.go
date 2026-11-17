// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package timeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/alerts"
	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
)

func alertItem(name, severity string, began time.Duration, labels map[string]string) alerts.Item {
	all := map[string]string{"alertname": name, "severity": severity}
	for key, value := range labels {
		all[key] = value
	}
	return alerts.NewItem(&observability.Alert{Labels: all, State: observability.AlertActive, StartsAt: now.Add(-began),
		Annotations: map[string]string{"summary": name + " summary", "runbook_url": "https://runbooks.example.test/" + name}})
}

func TestAlertStartsArePlacedForScopedResourcesOnly(t *testing.T) {
	r, _ := readers(t, incident()...)
	s, err := Collect(context.Background(), r, deploymentScope(), now)
	require.NoError(t, err)
	before := len(s.Entries)

	silenced := alertItem("DiskAlmostFull", "warning", 25*time.Minute, map[string]string{"node": "node-a"})
	silenced.Alert.SilencedBy = []string{"s1"}
	ownedElsewhere := alertItem("NotReady", "warning", 5*time.Minute, map[string]string{"namespace": "apps", "pod": "replaced-pod"})
	ownedElsewhere.Owner = &alerts.Target{Kind: "Deployment", Namespace: "apps", Name: "checkout"}
	untimed := alertItem("NoStart", "warning", 0, map[string]string{"namespace": "apps", "deployment": "checkout"})
	untimed.Alert.StartsAt = time.Time{}
	s.AddAlerts(observability.Status{State: observability.StateComplete}, []alerts.Item{
		alertItem("HighErrorRate", "critical", 9*time.Minute, map[string]string{"namespace": "apps", "deployment": "checkout"}),
		alertItem("CrashLooping", "warning", 8*time.Minute, map[string]string{"namespace": "apps", "pod": "checkout-2-abcde"}),
		alertItem("OtherPod", "warning", 8*time.Minute, map[string]string{"namespace": "apps", "pod": "other-pod"}),
		alertItem("OtherNamespace", "critical", 8*time.Minute, map[string]string{"namespace": "ops", "deployment": "checkout"}),
		alertItem("OtherNode", "critical", 8*time.Minute, map[string]string{"node": "node-z"}),
		alertItem("Watchdog", "none", 100*time.Hour, nil),
		silenced, ownedElsewhere, untimed,
	})
	require.Len(t, s.Entries, before+4)
	var got []string
	for index := range s.Entries {
		if entry := &s.Entries[index]; entry.Source == SourceAlert {
			got = append(got, entry.Source.Tag()+" "+entry.Ref.Short()+" "+entry.Summary)
			require.Equal(t, !strings.Contains(entry.Summary, "silenced"), entry.Failing, "only a firing alert is a failing signal")
			require.Contains(t, entry.Origin, "Alertmanager startsAt")
		}
	}
	require.Equal(t, []string{
		"ALERT pod/replaced-pod NotReady (warning) began firing: NotReady summary",
		"ALERT pod/checkout-2-abcde CrashLooping (warning) began firing: CrashLooping summary",
		"ALERT deploy/checkout HighErrorRate (critical) began firing: HighErrorRate summary",
		"ALERT node/node-a DiskAlmostFull (warning, silenced) began: DiskAlmostFull summary",
	}, got)
	// The first failing signal is still the earliest one read: the BackOff event.
	require.Equal(t, SourceEvent, s.OnsetEntry().Source)
	coverage := s.Coverage[len(s.Coverage)-1]
	require.Equal(t, Coverage{Source: "Alerts", State: Complete, Count: 4,
		Detail: "9 held by Alertmanager, 4 about this scope; 1 without a start time, not placed"}, coverage)
	require.Contains(t, s.Report(0), "A resolved alert is not listed")
}

func TestAlertReadThatDidNotAnswerIsAGapNotAnEmptyHistory(t *testing.T) {
	r, _ := readers(t, incident()...)
	s, err := Collect(context.Background(), r, deploymentScope(), now)
	require.NoError(t, err)
	before := len(s.Entries)
	s.AddAlerts(observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}, nil)
	require.Len(t, s.Entries, before)
	require.Contains(t, s.Gaps(), "alerts denied")
	require.Contains(t, s.Report(0), "Alertmanager denied (HTTP 403); alert history is unknown, not empty")

	s.AddAlerts(observability.Status{State: observability.StateTimeout}, nil)
	require.Contains(t, s.Gaps(), "alerts unknown")
}

func TestNamespaceTimelineTakesEveryAlertOfItsNamespace(t *testing.T) {
	r, _ := readers(t, incident()...)
	s, err := Collect(context.Background(), r, &Scope{Context: "ctx", Namespace: "apps"}, now)
	require.NoError(t, err)
	require.True(t, s.InScope("Service", "apps", "anything"))
	require.False(t, s.InScope("Service", "ops", "anything"))
	require.True(t, s.InScope("Node", "", "node-a"), "the nodes its pods run on are in scope")
	require.False(t, s.InScope("Node", "", "node-z"))
}
