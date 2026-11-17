// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package problems

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

var testNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func stamp(ago time.Duration) string { return testNow.Add(-ago).Format(time.RFC3339) }

func object(kind, namespace, name string, spec, status map[string]any) unstructured.Unstructured {
	version := "apps/v1"
	switch kind {
	case "Pod", "Node", "PersistentVolumeClaim", "Event":
		version = "v1"
	case "Job":
		version = "batch/v1"
	}
	u := unstructured.Unstructured{Object: map[string]any{"apiVersion": version, "kind": kind}}
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetUID(types.UID("uid-" + name))
	if spec != nil {
		u.Object["spec"] = spec
	}
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func owned(u unstructured.Unstructured, kind, name string) unstructured.Unstructured {
	controller := true
	u.SetOwnerReferences([]metav1.OwnerReference{{Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &controller}})
	return u
}

func waitingPod(namespace, name, reason, message string) unstructured.Unstructured {
	return object("Pod", namespace, name,
		map[string]any{"containers": []any{map[string]any{"name": "api"}}},
		map[string]any{"phase": "Running",
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": stamp(14 * time.Minute)}},
			"containerStatuses": []any{map[string]any{"name": "api", "ready": false,
				"state": map[string]any{"waiting": map[string]any{"reason": reason, "message": message}}}}})
}

func warning(namespace, kind, name, reason, message string, count int64, ago time.Duration) unstructured.Unstructured {
	e := object("Event", namespace, name+"."+reason, nil, nil)
	e.Object["type"] = "Warning"
	e.Object["reason"] = reason
	e.Object["message"] = message
	e.Object["count"] = count
	e.Object["lastTimestamp"] = stamp(ago)
	e.Object["involvedObject"] = map[string]any{"kind": kind, "namespace": namespace, "name": name, "uid": "uid-" + name}
	return e
}

func conditions(kind, status, reason, message string) []any {
	return []any{map[string]any{"type": kind, "status": status, "reason": reason, "message": message,
		"lastTransitionTime": stamp(2 * time.Hour)}}
}

func TestClassifyReadsOnlyWhatTheStatusReports(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		object   unstructured.Unstructured
		severity Severity
		cause    string // empty: no problem reported
		since    time.Duration
	}{
		{"crash loop quotes the last exit", "Pod", object("Pod", "a", "p", map[string]any{"containers": []any{map[string]any{"name": "api"}}},
			map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "api", "ready": false,
				"state":     map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
				"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": int64(137)}}}}}),
			Failing, "CrashLoopBackOff · last OOMKilled exit 137", 0},
		{"image pull", "Pod", waitingPod("a", "p", "ImagePullBackOff", "Back-off pulling image"), Failing, "ImagePullBackOff · Back-off pulling image", 14 * time.Minute},
		{"unschedulable is blocked, not failed", "Pod", object("Pod", "a", "p", nil, map[string]any{"phase": "Pending",
			"conditions": []any{map[string]any{"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
				"message": "0/1 nodes available", "lastTransitionTime": stamp(3 * time.Minute)}}}),
			Degraded, "Pending · 0/1 nodes available", 3 * time.Minute},
		{"creating is unfinished work", "Pod", waitingPod("a", "p", "ContainerCreating", ""), Progressing, "ContainerCreating", 14 * time.Minute},
		{"running but not ready", "Pod", object("Pod", "a", "p", nil, map[string]any{"phase": "Running",
			"containerStatuses": []any{map[string]any{"name": "api", "ready": false, "state": map[string]any{"running": map[string]any{}}}}}),
			Degraded, "Running · running, not ready", 0},
		{"ready pod", "Pod", object("Pod", "a", "p", nil, map[string]any{"phase": "Running",
			"conditions":        []any{map[string]any{"type": "Ready", "status": "True"}},
			"containerStatuses": []any{map[string]any{"name": "api", "ready": true, "state": map[string]any{"running": map[string]any{}}}}}), 0, "", 0},
		{"completed pod", "Pod", object("Pod", "a", "p", nil, map[string]any{"phase": "Succeeded"}), 0, "", 0},

		{"stalled rollout", "Deployment", object("Deployment", "a", "d", map[string]any{"replicas": int64(2)},
			map[string]any{"availableReplicas": int64(1), "conditions": conditions("Progressing", "False", "ProgressDeadlineExceeded", "timed out")}),
			Failing, "ProgressDeadlineExceeded: timed out · 1 of 2 available", 2 * time.Hour},
		{"partly available", "Deployment", object("Deployment", "a", "d", map[string]any{"replicas": int64(3)},
			map[string]any{"availableReplicas": int64(2), "updatedReplicas": int64(3)}), Degraded, "2 of 3 available", 0},
		{"nothing available", "Deployment", object("Deployment", "a", "d", map[string]any{"replicas": int64(3)},
			map[string]any{"conditions": conditions("Available", "False", "MinimumReplicasUnavailable", "")}), Failing, "0 of 3 available", 2 * time.Hour},
		{"rolling out", "Deployment", object("Deployment", "a", "d", map[string]any{"replicas": int64(3)},
			map[string]any{"availableReplicas": int64(3), "updatedReplicas": int64(1)}), Progressing, "rollout: 1 of 3 updated", 0},
		{"progressing true is normal", "Deployment", object("Deployment", "a", "d", map[string]any{"replicas": int64(1)},
			map[string]any{"availableReplicas": int64(1), "updatedReplicas": int64(1), "conditions": conditions("Progressing", "True", "NewReplicaSetAvailable", "")}), 0, "", 0},
		{"scaled to zero", "Deployment", object("Deployment", "a", "d", map[string]any{"replicas": int64(0)}, map[string]any{}), 0, "", 0},

		{"statefulset partly ready", "StatefulSet", object("StatefulSet", "a", "s", map[string]any{"replicas": int64(3)},
			map[string]any{"readyReplicas": int64(2)}), Degraded, "2 of 3 ready", 0},
		{"daemonset none ready", "DaemonSet", object("DaemonSet", "a", "s", nil,
			map[string]any{"desiredNumberScheduled": int64(4), "numberReady": int64(0)}), Failing, "0 of 4 ready", 0},
		{"daemonset healthy", "DaemonSet", object("DaemonSet", "a", "s", nil,
			map[string]any{"desiredNumberScheduled": int64(4), "numberReady": int64(4), "updatedNumberScheduled": int64(4)}), 0, "", 0},

		{"failed job", "Job", object("Job", "a", "j", nil, map[string]any{"failed": int64(3),
			"conditions": conditions("Failed", "True", "BackoffLimitExceeded", "reached the backoff limit")}),
			Failing, "BackoffLimitExceeded: reached the backoff limit", 2 * time.Hour},
		{"retrying job", "Job", object("Job", "a", "j", nil, map[string]any{"failed": int64(1), "active": int64(1)}),
			Degraded, "1 failed pod; not marked Failed", 0},
		{"complete job with earlier failures", "Job", object("Job", "a", "j", nil, map[string]any{"failed": int64(1),
			"conditions": conditions("Complete", "True", "", "")}), 0, "", 0},

		{"pending claim names no cause", "PersistentVolumeClaim", object("PersistentVolumeClaim", "a", "c", nil, map[string]any{"phase": "Pending"}), Degraded, "Pending", 0},
		{"bound claim", "PersistentVolumeClaim", object("PersistentVolumeClaim", "a", "c", nil, map[string]any{"phase": "Bound"}), 0, "", 0},

		{"node not ready", "Node", object("Node", "", "n", nil, map[string]any{"conditions": conditions("Ready", "False", "KubeletNotReady", "runtime down")}),
			Failing, "NotReady · KubeletNotReady: runtime down", 2 * time.Hour},
		{"node lost contact", "Node", object("Node", "", "n", nil, map[string]any{"conditions": conditions("Ready", "Unknown", "NodeStatusUnknown", "")}),
			Failing, "Ready unknown · NodeStatusUnknown", 2 * time.Hour},
		{"node under pressure", "Node", object("Node", "", "n", nil, map[string]any{"conditions": append(conditions("Ready", "True", "", ""),
			conditions("DiskPressure", "True", "KubeletHasDiskPressure", "")...)}), Degraded, "DiskPressure · KubeletHasDiskPressure", 2 * time.Hour},
		{"ready node", "Node", object("Node", "", "n", nil, map[string]any{"conditions": conditions("Ready", "True", "KubeletReady", "")}), 0, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := classify(c.kind, &c.object)
			if c.cause == "" {
				if row != nil {
					t.Fatalf("reported a problem the status does not: %+v", row)
				}
				return
			}
			if row == nil {
				t.Fatal("problem not reported")
			}
			if row.Severity != c.severity || row.Cause != c.cause {
				t.Fatalf("got %s %q, want %s %q", row.Severity, row.Cause, c.severity, c.cause)
			}
			if c.since == 0 && !row.Since.IsZero() {
				t.Fatalf("invented a start time: %s", row.Since)
			}
			if c.since != 0 && !row.Since.Equal(testNow.Add(-c.since)) {
				t.Fatalf("since %s, want %s ago", row.Since, c.since)
			}
		})
	}
}

func TestAssembleFoldsPodsIntoTheirControllerByUID(t *testing.T) {
	deployment := object("Deployment", "apps", "checkout", map[string]any{"replicas": int64(2)},
		map[string]any{"availableReplicas": int64(0), "conditions": conditions("Progressing", "False", "ProgressDeadlineExceeded", "")})
	replicaSet := owned(object("ReplicaSet", "apps", "checkout-6c9", nil, nil), "Deployment", "checkout")
	// Same name prefix, but controlled by a ReplicaSet that was not read.
	stray := owned(waitingPod("apps", "checkout-6c9-zzz", "CrashLoopBackOff", ""), "ReplicaSet", "other")
	rows := assemble(&objects{byKind: map[string][]unstructured.Unstructured{
		"Deployment": {deployment},
		"ReplicaSet": {replicaSet},
		"Pod": {
			owned(waitingPod("apps", "checkout-6c9-a", "CrashLoopBackOff", "back-off"), "ReplicaSet", "checkout-6c9"),
			owned(waitingPod("apps", "checkout-6c9-b", "ImagePullBackOff", ""), "ReplicaSet", "checkout-6c9"),
			stray,
			waitingPod("apps", "lonely", "ContainerCreating", ""),
		},
	}, events: []unstructured.Unstructured{
		warning("apps", "Pod", "checkout-6c9-a", "BackOff", "restarting", 41, time.Minute),
		warning("apps", "ReplicaSet", "checkout-6c9", "FailedCreate", "quota", 3, time.Minute),
	}}, testNow, EventWindow)

	if len(rows) != 3 {
		t.Fatalf("want deployment, stray pod and lonely pod, got %d rows: %+v", len(rows), rows)
	}
	top := rows[0]
	if top.Kind != "Deployment" || top.Affected() != 2 || top.Severity != Failing {
		t.Fatalf("deployment row: %+v", top)
	}
	// 2 status findings + 2 pods + the ReplicaSet warning + the pod's warning.
	if top.Signals != 6 {
		t.Fatalf("signals = %d, want 6", top.Signals)
	}
	if got := top.Summary(); !strings.HasPrefix(got, "2 pods affected · ProgressDeadlineExceeded") {
		t.Fatalf("summary %q", got)
	}
	if len(top.Events) != 1 || top.Events[0].Reason != "FailedCreate" {
		t.Fatalf("ReplicaSet warning not attributed to its Deployment: %+v", top.Events)
	}
	if first := top.Children[0]; first.Name != "checkout-6c9-a" || len(first.Events) != 1 || first.Signals != 2 {
		t.Fatalf("folded pod lost its event or rank: %+v", first)
	}
	if rows[1].Name != "checkout-6c9-zzz" || rows[1].Kind != "Pod" {
		t.Fatalf("a pod was folded by name without a verified owner: %+v", rows[1])
	}
	if rows[2].Name != "lonely" || rows[2].Severity != Progressing {
		t.Fatalf("order: %+v", rows[2])
	}
}

func TestAssembleFoldsPodsUnderAnOwnerThatReportsNothingItself(t *testing.T) {
	job := object("Job", "ops", "export", nil, map[string]any{"active": int64(1)})
	rows := assemble(&objects{byKind: map[string][]unstructured.Unstructured{
		"Job": {job},
		"Pod": {owned(waitingPod("ops", "export-x", "ImagePullBackOff", "no such image"), "Job", "export")},
	}}, testNow, EventWindow)
	if len(rows) != 1 || rows[0].Kind != "Job" || rows[0].Severity != Failing {
		t.Fatalf("rows: %+v", rows)
	}
	if got := rows[0].Summary(); got != "1 pod affected: ImagePullBackOff · no such image" {
		t.Fatalf("summary %q", got)
	}
}

func TestWarningEventsAreEvidenceNotStatus(t *testing.T) {
	claim := object("PersistentVolumeClaim", "apps", "data", nil, map[string]any{"phase": "Pending"})
	rows := assemble(&objects{
		byKind: map[string][]unstructured.Unstructured{"PersistentVolumeClaim": {claim}},
		events: []unstructured.Unstructured{
			warning("apps", "PersistentVolumeClaim", "data", "ProvisioningFailed", "storageclass not found", 9, time.Minute),
			warning("apps", "PersistentVolumeClaim", "data", "ProvisioningFailed", "storageclass not found", 2, 2*time.Minute),
			warning("apps", "Service", "web", "FailedToUpdateEndpointSlices", "conflict", 1, 5*time.Minute),
			warning("apps", "Service", "old", "Stale", "long ago", 1, 2*time.Hour),
			func() unstructured.Unstructured {
				e := warning("apps", "Service", "web", "Synced", "fine", 1, time.Minute)
				e.Object["type"] = "Normal"
				return e
			}(),
		},
	}, testNow, EventWindow)
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].Cause != "Pending · event ProvisioningFailed x11: storageclass not found" || rows[0].Signals != 2 {
		t.Fatalf("a status without a reason should quote its event, labelled as one: %+v", rows[0])
	}
	service := rows[1]
	if service.Severity != Warned || service.Severity.String() != "warning" || service.Cause != "event FailedToUpdateEndpointSlices: conflict" {
		t.Fatalf("event-only row must not claim a status severity: %+v", service)
	}
	if service.Resource.Resource != "services" {
		t.Fatalf("known kinds stay navigable: %+v", service.Resource)
	}
}

func TestEventsAboutAnEarlierUIDAreKeptApart(t *testing.T) {
	pod := waitingPod("apps", "api", "CrashLoopBackOff", "")
	stale := warning("apps", "Pod", "api", "Killing", "old pod", 1, time.Minute)
	stale.Object["involvedObject"].(map[string]any)["uid"] = "uid-previous"
	rows := assemble(&objects{byKind: map[string][]unstructured.Unstructured{"Pod": {pod}}, events: []unstructured.Unstructured{stale}}, testNow, EventWindow)
	if len(rows) != 2 || len(rows[0].Events) != 0 || rows[1].Severity != Warned || rows[0].Key() == rows[1].Key() {
		t.Fatalf("rows: %+v", rows)
	}
	if !strings.Contains(rows[1].Cause, "earlier identity") {
		t.Fatalf("cause %q", rows[1].Cause)
	}
}

func TestRowsRankBySeverityThenSignals(t *testing.T) {
	rows := []Row{
		{Severity: Degraded, Kind: "Pod", Name: "b", Signals: 9},
		{Severity: Failing, Kind: "Pod", Name: "z", Signals: 1},
		{Severity: Failing, Kind: "Pod", Name: "a", Signals: 3},
		{Severity: Failing, Kind: "Node", Name: "n", Signals: 1},
		{Severity: Progressing, Kind: "Deployment", Name: "d", Signals: 20},
	}
	sortRows(rows)
	got := ""
	for _, row := range rows {
		got += row.Name
	}
	if got != "anzbd" {
		t.Fatalf("order %q", got)
	}
}

func testReader(items ...unstructured.Unstructured) *fake.FakeDynamicClient {
	kinds := make(map[schema.GroupVersionResource]string)
	for kind, resource := range resources {
		kinds[resource] = kind + "List"
	}
	initial := make([]runtime.Object, 0, len(items))
	for i := range items {
		initial = append(initial, &items[i])
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, initial...)
}

func sourceNamed(t *testing.T, snapshot *Snapshot, name string) Source {
	t.Helper()
	for _, source := range snapshot.Sources {
		if source.Name == name {
			return source
		}
	}
	t.Fatalf("source %q not reported", name)
	return Source{}
}

func TestCollectReadsEachSourceOnceBoundedAndInScope(t *testing.T) {
	reader := testReader(
		waitingPod("apps", "api", "CrashLoopBackOff", ""),
		waitingPod("other", "elsewhere", "CrashLoopBackOff", ""),
		object("Node", "", "n1", nil, map[string]any{"conditions": conditions("Ready", "False", "KubeletNotReady", "")}),
		warning("apps", "Pod", "api", "BackOff", "restarting", 4, time.Minute),
	)
	var mu sync.Mutex
	seen := make(map[string]int)
	reader.PrependReactor("*", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if action.GetVerb() != "list" {
			t.Errorf("unexpected verb %s on %s", action.GetVerb(), action.GetResource().Resource)
		}
		resource := action.GetResource().Resource
		seen[resource]++
		if want := map[bool]string{true: "", false: "apps"}[resource == "nodes"]; action.GetNamespace() != want {
			t.Errorf("%s read in namespace %q, want %q", resource, action.GetNamespace(), want)
		}
		return false, nil, nil
	})
	snapshot := Collect(context.Background(), reader, "apps", testNow)
	if len(seen) != 9 {
		t.Fatalf("want 9 sources, read %v", seen)
	}
	for resource, count := range seen {
		if count != 1 {
			t.Fatalf("%s read %d times", resource, count)
		}
	}
	if len(snapshot.Gaps()) != 0 {
		t.Fatalf("gaps: %+v", snapshot.Gaps())
	}
	if len(snapshot.Rows) != 2 || snapshot.Rows[0].Name != "api" || snapshot.Rows[0].Signals != 2 || snapshot.Rows[1].Kind != "Node" {
		t.Fatalf("rows: %+v", snapshot.Rows)
	}
	if got := sourceNamed(t, &snapshot, "warning events"); got.Count != 1 || got.State != StateComplete {
		t.Fatalf("events source: %+v", got)
	}
	if got := sourceNamed(t, &snapshot, "replicasets"); !got.Support {
		t.Fatalf("replicasets are read for ownership only: %+v", got)
	}
}

func TestCollectNamesEveryMissingSource(t *testing.T) {
	reader := testReader(waitingPod("apps", "api", "CrashLoopBackOff", ""))
	reader.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		resource := action.GetResource()
		switch resource.Resource {
		case "nodes":
			return true, nil, apierrors.NewForbidden(resource.GroupResource(), "", nil)
		case "jobs":
			return true, nil, context.DeadlineExceeded
		case "statefulsets":
			return true, nil, apierrors.NewNotFound(resource.GroupResource(), "")
		case "pods":
			list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{waitingPod("apps", "api", "CrashLoopBackOff", "")}}
			list.SetContinue("more")
			return true, list, nil
		}
		return false, nil, nil
	})
	snapshot := Collect(context.Background(), reader, "", testNow)
	want := map[string][2]string{
		"nodes":        {StateDenied, ""},
		"jobs":         {StateUnknown, "timed out"},
		"statefulsets": {StateUnknown, "not served by this API"},
		"pods":         {StateIncomplete, "first 1 only"},
	}
	if len(snapshot.Gaps()) != len(want) {
		t.Fatalf("gaps: %+v", snapshot.Gaps())
	}
	for name, expected := range want {
		if got := sourceNamed(t, &snapshot, name); got.State != expected[0] || got.Detail != expected[1] {
			t.Fatalf("%s: %+v, want %v", name, got, expected)
		}
	}
	// A truncated read still contributes the rows it did return.
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Name != "api" {
		t.Fatalf("rows: %+v", snapshot.Rows)
	}
}

func TestCollectDoesNotWaitForAReaderThatNeverAnswers(t *testing.T) {
	reader := testReader(waitingPod("apps", "api", "CrashLoopBackOff", ""))
	release := make(chan struct{})
	defer close(release)
	reader.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan Snapshot, 1)
	go func() { done <- Collect(ctx, reader, "", testNow) }()
	select {
	case snapshot := <-done:
		if got := sourceNamed(t, &snapshot, "nodes"); got.State != StateUnknown || got.Detail != "timed out" {
			t.Fatalf("nodes: %+v", got)
		}
		if len(snapshot.Rows) != 1 {
			t.Fatalf("sources that answered must still be listed: %+v", snapshot.Rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Collect waited past its deadline")
	}
}
