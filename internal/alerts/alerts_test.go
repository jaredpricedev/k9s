// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metadatafake "k8s.io/client-go/metadata/fake"
	ktesting "k8s.io/client-go/testing"
)

var testNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func alert(name, severity string, ago time.Duration, labels map[string]string) observability.Alert {
	all := map[string]string{"alertname": name}
	if severity != "" {
		all["severity"] = severity
	}
	for key, value := range labels {
		all[key] = value
	}
	return observability.Alert{Fingerprint: name + "-" + labels["pod"] + labels["node"] + labels["team"], Labels: all, State: observability.AlertActive, GeneratorURL: "http://prometheus.example.test/graph",
		StartsAt: testNow.Add(-ago), Annotations: map[string]string{"summary": name + " summary"}}
}

func TestMapFollowsLabelsMostSpecificFirstAndNeverGuesses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   string
		from   []string
	}{
		{"pod wins over its workload and node", map[string]string{"namespace": "apps", "pod": "api-1", "deployment": "api", "node": "n1"},
			"Pod apps/api-1", []string{"namespace", "pod"}},
		{"workload wins over service", map[string]string{"namespace": "apps", "service": "api", "deployment": "api"},
			"Deployment apps/api", []string{"namespace", "deployment"}},
		{"statefulset", map[string]string{"namespace": "ops", "statefulset": "db"}, "StatefulSet ops/db", nil},
		{"daemonset", map[string]string{"namespace": "ops", "daemonset": "agent"}, "DaemonSet ops/agent", nil},
		{"the scrape job label is not a Kubernetes Job", map[string]string{"namespace": "ops", "job": "kube-state-metrics"},
			"Namespace ops", []string{"namespace"}},
		{"job_name is a Kubernetes Job", map[string]string{"namespace": "ops", "job_name": "export", "job": "kube-state-metrics"},
			"Job ops/export", []string{"namespace", "job_name"}},
		{"claim", map[string]string{"namespace": "ops", "persistentvolumeclaim": "data"}, "PersistentVolumeClaim ops/data", nil},
		{"service", map[string]string{"namespace": "apps", "service": "api"}, "Service apps/api", nil},
		{"exported labels name the subject", map[string]string{"namespace": "monitoring", "pod": "ksm-0", "exported_namespace": "apps", "exported_pod": "api-1"},
			"Pod apps/api-1", []string{"exported_namespace", "exported_pod"}},
		{"node needs no namespace", map[string]string{"node": "worker-b", "job": "kube-state-metrics"}, "Node worker-b", []string{"node"}},
		{"a pod without a namespace is not an object", map[string]string{"pod": "api-1"}, "not mapped to an object", nil},
		{"an instance is not a node", map[string]string{"instance": "10.0.0.4:9100"}, "not mapped to an object", nil},
		{"no Kubernetes labels", map[string]string{"severity": "none"}, "not mapped to an object", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := Map(tc.labels)
			assert.Equal(t, tc.want, target.Label())
			if tc.from != nil {
				assert.Equal(t, tc.from, target.From)
			}
			assert.Equal(t, tc.want != "not mapped to an object", target.Mapped())
			if target.Mapped() {
				assert.NotEmpty(t, target.Resource, "a mapped object names the list it opens in")
			}
		})
	}
}

func TestItemWordsSeverityStateAndOwnerFromLabels(t *testing.T) {
	a := alert("PodDown", "CRITICAL", time.Minute, map[string]string{"namespace": "apps", "pod": "api-1", "statefulset": "api"})
	item := NewItem(&a)
	assert.Equal(t, "critical", item.Severity())
	assert.Equal(t, Firing, item.State())
	require.NotNil(t, item.Owner)
	assert.Equal(t, "StatefulSet apps/api", item.Owner.Label())
	assert.Equal(t, "label statefulset", item.OwnerFrom)
	assert.Equal(t, "PodDown summary", item.Summary())

	a = alert("", "", time.Minute, nil)
	a.State, a.SilencedBy = observability.AlertSuppressed, []string{"s1"}
	item = NewItem(&a)
	assert.Equal(t, "(no alertname)", item.Name())
	assert.Equal(t, "unset", item.Severity(), "a missing severity is said, not invented")
	assert.Equal(t, Silenced, item.State())
	assert.False(t, item.IsFiring())

	a.SilencedBy, a.InhibitedBy = nil, []string{"f1"}
	item = NewItem(&a)
	assert.Equal(t, Inhibited, item.State())
	a.InhibitedBy, a.State = nil, observability.AlertUnprocessed
	item = NewItem(&a)
	assert.Equal(t, Unprocessed, item.State())
}

func TestGroupsFoldPodsOfOneWorkloadOnlyWhenOwnershipIsKnown(t *testing.T) {
	owner := &Target{Kind: "Deployment", Namespace: "apps", Name: "api", Resource: "apps/v1/deployments"}
	make := func(name, severity string, ago time.Duration, labels map[string]string, owned bool) Item {
		a := alert(name, severity, ago, labels)
		item := NewItem(&a)
		if owned {
			item.Owner, item.OwnerFrom = owner, "owner references"
		}
		return item
	}
	silenced := make("DiskFull", "warning", time.Hour, map[string]string{"node": "n2"}, false)
	silenced.Alert.SilencedBy = []string{"s1"}
	items := []Item{
		make("NotReady", "warning", 6*time.Minute, map[string]string{"namespace": "apps", "pod": "api-b"}, true),
		make("Watchdog", "none", 24*time.Hour, nil, false),
		silenced,
		make("NotReady", "warning", 7*time.Minute, map[string]string{"namespace": "apps", "pod": "api-a"}, true),
		make("NotReady", "warning", 5*time.Minute, map[string]string{"namespace": "apps", "pod": "stray"}, false),
		make("NodeDown", "critical", 18*time.Minute, map[string]string{"node": "n1"}, false),
		make("Watchdog", "none", 24*time.Hour, map[string]string{"team": "b"}, false),
		make("ErrorRate", "critical", 9*time.Minute, map[string]string{"namespace": "apps", "deployment": "api"}, false),
	}
	groups := Groups(items)
	var got []string
	for i := range groups {
		got = append(got, groups[i].Name+" "+groups[i].Target.Label())
	}
	assert.Equal(t, []string{
		"ErrorRate Deployment apps/api", // critical, started most recently
		"NodeDown Node n1",
		"NotReady Pod apps/stray", // owner unknown: its own row, never guessed into the workload
		"NotReady Deployment apps/api",
		"Watchdog not mapped to an object", // unmapped alerts are never folded together
		"Watchdog not mapped to an object",
		"DiskFull Node n2",
	}, got)
	folded := &groups[3]
	require.True(t, folded.Folded())
	assert.Len(t, folded.Items, 2)
	assert.Equal(t, "api-a", folded.Items[0].Target.Name)
	assert.Equal(t, testNow.Add(-7*time.Minute), folded.Since, "a folded row is as old as its earliest alert")
	assert.Equal(t, []int{0, 0, 0, 0, 1, 1, 2}, []int{groups[0].Section(), groups[1].Section(), groups[2].Section(), groups[3].Section(),
		groups[4].Section(), groups[5].Section(), groups[6].Section()})
	assert.NotEqual(t, groups[4].Key(), groups[5].Key())

	counts := Count(items)
	assert.Equal(t, 7, counts.Firing)
	assert.Equal(t, 1, counts.Silenced)
	assert.Equal(t, 2, counts.Unmapped)
	assert.Equal(t, map[string]int{"critical": 2, "warning": 3, "none": 2}, counts.BySeverity)
}

type fakeSource struct {
	result observability.AlertsResult
	calls  int
}

func (f *fakeSource) Alerts(context.Context, observability.AlertQuery) observability.AlertsResult {
	f.calls++
	return f.result
}

func controlled(kind, name string) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: &yes}}
}

func meta(apiVersion, kind, namespace, name string, owners []metav1.OwnerReference) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: apiVersion, Kind: kind},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, OwnerReferences: owners}}
}

func metadataClient(t *testing.T, objects ...runtime.Object) *metadatafake.FakeMetadataClient {
	t.Helper()
	scheme := metadatafake.NewTestScheme()
	require.NoError(t, metav1.AddMetaToScheme(scheme))
	return metadatafake.NewSimpleMetadataClient(scheme, objects...)
}

func TestCollectReadsOnceAndResolvesPodOwnersThroughMetadataOnly(t *testing.T) {
	source := &fakeSource{result: observability.AlertsResult{
		Status: observability.Status{Kind: observability.KindAlertmanager, State: observability.StateComplete},
		Alerts: []observability.Alert{
			alert("NotReady", "warning", 6*time.Minute, map[string]string{"namespace": "apps", "pod": "api-7d9-x"}),
			alert("Crash", "warning", 8*time.Minute, map[string]string{"namespace": "apps", "pod": "api-7d9-x"}),
			alert("NotReady", "warning", 9*time.Minute, map[string]string{"namespace": "apps", "pod": "gone"}),
			alert("NotReady", "warning", 9*time.Minute, map[string]string{"namespace": "ops", "pod": "db-0"}),
			alert("NotReady", "warning", 9*time.Minute, map[string]string{"namespace": "ops", "pod": "bare"}),
			alert("NodeDown", "critical", 9*time.Minute, map[string]string{"node": "n1"}),
		}}}
	client := metadataClient(t,
		meta("v1", "Pod", "apps", "api-7d9-x", controlled("ReplicaSet", "api-7d9")),
		meta("apps/v1", "ReplicaSet", "apps", "api-7d9", controlled("Deployment", "api")),
		meta("v1", "Pod", "ops", "db-0", controlled("StatefulSet", "db")),
		meta("v1", "Pod", "ops", "bare", nil))
	snapshot := Collect(t.Context(), source, client, testNow)
	assert.Equal(t, 1, source.calls, "one alert read, no retry")
	require.Len(t, snapshot.Items, 6)
	assert.Equal(t, OwnerRead{State: OwnersComplete, Asked: 4, Resolved: 4, Missing: 1}, snapshot.Owners)

	require.NotNil(t, snapshot.Items[0].Owner)
	assert.Equal(t, "Deployment apps/api", snapshot.Items[0].Owner.Label(), "a ReplicaSet is followed to its Deployment")
	assert.Equal(t, "owner references", snapshot.Items[0].OwnerFrom)
	assert.Equal(t, "Deployment apps/api", snapshot.Items[1].Owner.Label(), "both alerts on one pod share the one lookup")
	assert.True(t, snapshot.Items[2].Missing, "a pod that no longer exists is reported, not hidden")
	assert.Nil(t, snapshot.Items[2].Owner)
	assert.Equal(t, "StatefulSet ops/db", snapshot.Items[3].Owner.Label())
	assert.Nil(t, snapshot.Items[4].Owner, "a pod without a controller has no workload")
	assert.False(t, snapshot.Items[4].Missing)
	for _, action := range client.Actions() {
		assert.Equal(t, "get", action.GetVerb(), "owners are read by name, never listed")
	}
}

func TestCollectReportsDeniedOwnersAndAnUnreadSource(t *testing.T) {
	source := &fakeSource{result: observability.AlertsResult{
		Status: observability.Status{State: observability.StateComplete},
		Alerts: []observability.Alert{alert("NotReady", "warning", time.Minute, map[string]string{"namespace": "apps", "pod": "api-1"})}}}
	client := metadataClient(t)
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "api-1", nil)
	})
	snapshot := Collect(t.Context(), source, client, testNow)
	assert.Equal(t, OwnersDenied, snapshot.Owners.State)
	require.Len(t, snapshot.Items, 1)
	assert.Nil(t, snapshot.Items[0].Owner)
	assert.False(t, snapshot.Items[0].Missing, "a denied lookup says nothing about whether the pod exists")

	snapshot = Collect(t.Context(), source, nil, testNow)
	assert.Equal(t, OwnersUnknown, snapshot.Owners.State, "without a metadata client the owners are unknown, not absent")

	denied := &fakeSource{result: observability.AlertsResult{Status: observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}}}
	snapshot = Collect(t.Context(), denied, client, testNow)
	assert.Equal(t, observability.StateDenied, snapshot.Status.State)
	assert.Empty(t, snapshot.Items)
	assert.Equal(t, OwnersNotNeeded, snapshot.Owners.State)
}

func TestOwnerLookupsAreBounded(t *testing.T) {
	var all []observability.Alert
	for i := range MaxOwnerLookups + 5 {
		all = append(all, alert("NotReady", "warning", time.Minute, map[string]string{"namespace": "apps", "pod": "p-" + string(rune('a'+i/26)) + string(rune('a'+i%26))}))
	}
	source := &fakeSource{result: observability.AlertsResult{Status: observability.Status{State: observability.StateComplete}, Alerts: all}}
	client := metadataClient(t)
	snapshot := Collect(t.Context(), source, client, testNow)
	assert.Equal(t, MaxOwnerLookups, snapshot.Owners.Asked)
	assert.Equal(t, OwnersIncomplete, snapshot.Owners.State)
	assert.Contains(t, snapshot.Owners.Detail, "limit 30")
	assert.Len(t, client.Actions(), MaxOwnerLookups)
}
