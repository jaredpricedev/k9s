// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package problems

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func note(name, severity, state string, ago time.Duration) AlertNote {
	return AlertNote{Name: name, Severity: severity, State: state, Summary: name + " summary", Since: testNow.Add(-ago),
		Paging: severity == "critical"}
}

func alertSnapshot() Snapshot {
	return Snapshot{
		Namespace: "apps", ReadAt: testNow,
		Sources: []Source{{Name: "pods", State: StateComplete, Count: 4}, {Name: "warning events", State: StateComplete, Count: 1}},
		Rows: []Row{
			{Severity: Failing, Kind: "Deployment", Namespace: "apps", Name: "api", Signals: 3,
				Children: []Row{{Severity: Failing, Kind: "Pod", Namespace: "apps", Name: "api-1", Signals: 1}}},
			{Severity: Degraded, Kind: "Node", Name: "worker-c", Cause: "DiskPressure", Signals: 1},
			{Severity: Warned, Kind: "Service", Namespace: "apps", Name: "api", Cause: "event FailedToUpdateEndpointSlices: conflict", Signals: 1, weak: true},
		},
		owners: map[string]string{ident("Pod", "apps", "api-2"): ident("Deployment", "apps", "api")},
	}
}

func rowNamed(t *testing.T, s *Snapshot, kind, name string) *Row {
	t.Helper()
	for i := range s.Rows {
		if s.Rows[i].Kind == kind && s.Rows[i].Name == name {
			return &s.Rows[i]
		}
	}
	t.Fatalf("no %s %s row in %+v", kind, name, s.Rows)
	return nil
}

func TestAlertsAddSignalsToListedRowsAndListWhatStatusCannotSee(t *testing.T) {
	s := alertSnapshot()
	s.AddAlerts(Source{State: StateComplete, Count: 7}, []AlertRef{
		{Kind: "Deployment", Namespace: "apps", Name: "api", Note: note("ErrorRate", "critical", "firing", 9*time.Minute)},
		{Kind: "Pod", Namespace: "apps", Name: "api-1", Note: note("CrashLooping", "warning", "firing", 8*time.Minute)},
		{Kind: "Pod", Namespace: "apps", Name: "api-2", Note: note("NotReady", "warning", "firing", 6*time.Minute)},
		{Kind: "Node", Name: "worker-c", Note: note("DiskAlmostFull", "warning", "silenced", time.Hour)},
		{Kind: "Node", Name: "worker-d", Note: note("NodeUnreachable", "warning", "inhibited", time.Hour)},
		{Kind: "Service", Namespace: "apps", Name: "api", Note: note("HighLatency", "warning", "firing", 7*time.Minute)},
		{Kind: "StatefulSet", Namespace: "apps", Name: "ledger", Note: note("ReplicationLag", "critical", "firing", 30*time.Minute)},
		{Kind: "StatefulSet", Namespace: "apps", Name: "ledger", Note: note("BackupStale", "warning", "firing", 3*time.Hour)},
	})

	// The alert read is one more source, listed before the events whose
	// window closes the list.
	if got := s.Sources[1]; got.Name != AlertsSource || got.Count != 7 || s.Sources[2].Name != "warning events" {
		t.Fatalf("sources: %+v", s.Sources)
	}
	deployment := rowNamed(t, &s, "Deployment", "api")
	if deployment.Severity != Failing || deployment.Signals != 6 || len(deployment.Alerts) != 3 || deployment.FromAlert {
		t.Fatalf("a listed row keeps its status severity and gains one signal per firing alert: %+v", deployment)
	}
	if deployment.Alerts[1].Pod != "api-1" || deployment.Alerts[2].Pod != "api-2" {
		t.Fatalf("alerts on the workload's pods name the pod: %+v", deployment.Alerts)
	}
	if child := deployment.Children[0]; len(child.Alerts) != 1 || child.Signals != 2 {
		t.Fatalf("the folded pod carries its own alert: %+v", child)
	}
	node := rowNamed(t, &s, "Node", "worker-c")
	if node.Signals != 1 || len(node.Alerts) != 1 || node.Severity != Degraded {
		t.Fatalf("a silenced alert is noted but is not a signal: %+v", node)
	}
	for i := range s.Rows {
		if s.Rows[i].Name == "worker-d" {
			t.Fatalf("an inhibited alert must not list a resource: %+v", s.Rows[i])
		}
	}
	service := rowNamed(t, &s, "Service", "api")
	if service.Severity != Alerting || service.FromAlert || service.Signals != 2 || !service.Since.Equal(testNow.Add(-7*time.Minute)) ||
		service.Cause != "alert HighLatency (warning): HighLatency summary · event FailedToUpdateEndpointSlices: conflict" {
		t.Fatalf("a row only an event listed becomes alerting and leads with the alert: %+v", service)
	}
	ledger := rowNamed(t, &s, "StatefulSet", "ledger")
	if !ledger.FromAlert || ledger.Severity != Alerting || ledger.Severity.String() != "alerting" || ledger.Signals != 2 ||
		ledger.Cause != "2 alerts: ReplicationLag (critical), BackupStale (warning)" || !ledger.Since.Equal(testNow.Add(-3*time.Hour)) {
		t.Fatalf("an object status cannot see becomes its own row, marked as from an alert: %+v", ledger)
	}
	if ledger.Resource.Resource != "statefulsets" {
		t.Fatalf("an alert-only row still opens in its list: %+v", ledger.Resource)
	}

	// Failing first, then the alert-only row whose alert pages, then degraded,
	// then the alerting row with only a warning.
	var order []string
	for i := range s.Rows {
		order = append(order, s.Rows[i].Kind)
	}
	if want := []string{"Deployment", "StatefulSet", "Node", "Service"}; len(order) != 4 || order[0] != want[0] || order[1] != want[1] ||
		order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("order: %v, want %v", order, want)
	}
}

func TestAlertSourceThatWasNotReadIsAGapAndChangesNoRow(t *testing.T) {
	s := alertSnapshot()
	s.AddAlerts(Source{State: StateDenied, Detail: "HTTP 403"}, nil)
	gaps := s.Gaps()
	if len(gaps) != 1 || gaps[0].Name != AlertsSource || gaps[0].State != StateDenied {
		t.Fatalf("gaps: %+v", gaps)
	}
	if len(s.Rows) != 3 || s.Rows[2].Severity != Warned {
		t.Fatalf("rows changed without an alert read: %+v", s.Rows)
	}
}

func TestAssembleRecordsTheWorkloadOfHealthyPods(t *testing.T) {
	deployment := object("Deployment", "apps", "api", nil, map[string]any{"availableReplicas": int64(1)})
	set := owned(object("ReplicaSet", "apps", "api-7d9", nil, nil), "Deployment", "api")
	pod := owned(object("Pod", "apps", "api-7d9-x", nil, map[string]any{"phase": "Running"}), "ReplicaSet", "api-7d9")
	collected := &objects{byKind: map[string][]unstructured.Unstructured{"Deployment": {deployment}, "ReplicaSet": {set}, "Pod": {pod}}}
	rows := assemble(collected, testNow, EventWindow)
	for i := range rows {
		if rows[i].Kind == "Pod" || len(rows[i].Children) > 0 {
			t.Fatalf("a healthy pod is not a row: %+v", rows)
		}
	}
	if got := collected.owners[ident("Pod", "apps", "api-7d9-x")]; got != ident("Deployment", "apps", "api") {
		t.Fatalf("owner of a healthy pod: %q", got)
	}
}
