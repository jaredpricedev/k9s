// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package timeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
)

const secretValue = "hunter2-must-not-appear"

var now = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

func object(apiVersion, kind, name string, created time.Duration, extra map[string]any) *unstructured.Unstructured {
	content := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{
		"name": name, "namespace": "apps", "uid": "uid-" + strings.ToLower(kind) + "-" + name, "creationTimestamp": ago(created)}}
	for key, value := range extra {
		if key == "metadata" {
			for k, v := range value.(map[string]any) {
				content["metadata"].(map[string]any)[k] = v
			}
			continue
		}
		content[key] = value
	}
	return &unstructured.Unstructured{Object: content}
}

func template(image, limit string, env ...any) map[string]any {
	return map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "checkout"}}, "spec": map[string]any{
		"containers": []any{map[string]any{"name": "api", "image": image, "env": env,
			"envFrom":   []any{map[string]any{"configMapRef": map[string]any{"name": "checkout-config"}}},
			"resources": map[string]any{"limits": map[string]any{"memory": limit}}}}}}
}

func envValue(name, value string) map[string]any { return map[string]any{"name": name, "value": value} }
func envSecret(name, key string) map[string]any {
	return map[string]any{"name": name, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "checkout-secrets", "key": key}}}
}

var deploymentOwner = []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "checkout", "uid": "uid-deployment-checkout", "controller": true}}

// incident is a ConfigMap edit 14m ago, a rollout 12m ago and a crash 10m ago.
func incident() []runtime.Object {
	oldTemplate := template("example.test/checkout:v1", "512Mi", envValue("LOG_LEVEL", "info"), envSecret("TOKEN", "v1"))
	newTemplate := template("example.test/checkout:v2", "256Mi", envValue("LOG_LEVEL", "info"), envSecret("TOKEN", "v2"), envValue("FLAGS", secretValue))
	selector := map[string]any{"matchLabels": map[string]any{"app": "checkout"}}
	podSpec := newTemplate["spec"].(map[string]any)
	podSpec["nodeName"] = "node-a"
	return []runtime.Object{
		object("apps/v1", "Deployment", "checkout", 72*time.Hour, map[string]any{
			"spec": map[string]any{"selector": selector, "template": newTemplate},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "lastTransitionTime": ago(2 * time.Minute)},
				map[string]any{"type": "Available", "status": "False", "reason": "MinimumReplicasUnavailable"}}}}),
		object("apps/v1", "Deployment", "unrelated", 72*time.Hour, map[string]any{
			"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "other"}}}}),
		object("apps/v1", "ReplicaSet", "checkout-1", 72*time.Hour, map[string]any{"spec": map[string]any{"template": oldTemplate},
			"metadata": map[string]any{"ownerReferences": deploymentOwner, "annotations": map[string]any{revisionAnnotation: "1", desiredAnnotation: "2"}}}),
		object("apps/v1", "ReplicaSet", "checkout-2", 12*time.Minute, map[string]any{"spec": map[string]any{"template": newTemplate},
			"metadata": map[string]any{"ownerReferences": deploymentOwner, "annotations": map[string]any{revisionAnnotation: "2", desiredAnnotation: "3"}}}),
		object("v1", "Pod", "checkout-2-abcde", 12*time.Minute, map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": "checkout"}}, "spec": podSpec,
			"status": map[string]any{"containerStatuses": []any{map[string]any{"name": "api", "restartCount": int64(6),
				"state":     map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
				"lastState": map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": int64(1), "finishedAt": ago(2 * time.Minute)}}}}}}),
		object("v1", "Pod", "other-pod", time.Hour, map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "other"}}}),
		event("backoff-1", "Pod", "checkout-2-abcde", "BackOff", "Warning", 10*time.Minute, 4*time.Minute, 9),
		event("backoff-2", "Pod", "checkout-2-abcde", "BackOff", "Warning", 3*time.Minute, time.Minute, 5),
		event("scaled", "Deployment", "checkout", "ScalingReplicaSet", "Normal", 12*time.Minute, 12*time.Minute, 1),
		event("noise", "Pod", "other-pod", "Unhealthy", "Warning", 50*time.Minute, 50*time.Minute, 1),
		object("autoscaling/v2", "HorizontalPodAutoscaler", "checkout", 72*time.Hour, map[string]any{
			"spec": map[string]any{"scaleTargetRef": map[string]any{"kind": "Deployment", "name": "checkout"}, "maxReplicas": int64(4)},
			"status": map[string]any{"lastScaleTime": ago(26 * time.Hour), "conditions": []any{
				map[string]any{"type": "ScalingActive", "status": "False", "reason": "FailedGetResourceMetric", "lastTransitionTime": ago(8 * time.Minute)}}}}),
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "apps"}}},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": "node-a", "uid": "node-a"},
			"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "KubeletReady", "lastTransitionTime": ago(144 * time.Hour)}}}}},
	}
}

func event(name, kind, involved, reason, kindType string, first, last time.Duration, count int64) *unstructured.Unstructured {
	return object("v1", "Event", name, first, map[string]any{
		"involvedObject": map[string]any{"kind": kind, "name": involved, "namespace": "apps"},
		"reason":         reason, "type": kindType, "count": count, "message": reason + " message for " + involved,
		"firstTimestamp": ago(first), "lastTimestamp": ago(last), "source": map[string]any{"component": "kubelet"}})
}

func readers(t *testing.T, objects ...runtime.Object) (*Readers, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	kinds := map[schema.GroupVersionResource]string{}
	for resource, kind := range map[string]string{resPods: "PodList", resEvents: "EventList", resNodes: "NodeList",
		resDeployments: "DeploymentList", resReplicaSets: "ReplicaSetList", resHPAs: "HorizontalPodAutoscalerList"} {
		kinds[gvr(resource)] = kind
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...)
	scheme := metadatafake.NewTestScheme()
	require.NoError(t, metav1.AddMetaToScheme(scheme))
	stamp := func(d time.Duration) *metav1.Time { value := metav1.NewTime(now.Add(-d)); return &value }
	meta := metadatafake.NewSimpleMetadataClient(scheme,
		&metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-config", Namespace: "apps", CreationTimestamp: *stamp(72 * time.Hour), ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "kubectl-create", Operation: metav1.ManagedFieldsOperationUpdate, Time: stamp(72 * time.Hour)},
				{Manager: "kubectl-edit", Operation: metav1.ManagedFieldsOperationUpdate, Time: stamp(14 * time.Minute)}}}},
		&metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-secrets", Namespace: "apps", CreationTimestamp: *stamp(720 * time.Hour),
			Annotations: map[string]string{"note": secretValue}, ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "external-secrets", Operation: metav1.ManagedFieldsOperationApply, Time: stamp(2 * time.Hour)}}}})
	return &Readers{Objects: dyn, Metadata: meta}, dyn
}

func deploymentScope() *Scope {
	return &Scope{Context: "ctx", Namespace: "apps", Resource: resDeployments, Kind: "Deployment", Name: "checkout", UID: "uid-deployment-checkout"}
}

func summaries(s *Snapshot) []string {
	result := make([]string, 0, len(s.Entries))
	for index := range s.Entries {
		entry := &s.Entries[index]
		result = append(result, entry.Source.Tag()+" "+entry.Ref.Short()+" "+entry.Summary)
	}
	return result
}

func TestCollectOrdersWorkloadEvidenceAndMarksWhatPrecededTheFault(t *testing.T) {
	r, _ := readers(t, incident()...)
	s, err := Collect(context.Background(), r, deploymentScope(), now)
	require.NoError(t, err)

	// One untimed abnormal condition is reported, never silently placed.
	require.Len(t, s.Untimed, 1)
	require.Contains(t, s.Untimed[0], "Available=False")
	s.Untimed = nil
	require.False(t, s.Partial())

	got := summaries(s)
	require.Equal(t, []string{
		"STATUS deploy/checkout Progressing=False ProgressDeadlineExceeded",
		"RESTART pod/checkout-2-abcde container api Error (exit 1), restart count 6",
		"HPA hpa/checkout ScalingActive=False FailedGetResourceMetric",
		"EVENT pod/checkout-2-abcde Warning BackOff x14: BackOff message for checkout-2-abcde",
		"EVENT deploy/checkout Normal ScalingReplicaSet: ScalingReplicaSet message for checkout",
		"ROLLOUT rs/checkout-2 rev 2: image v1 -> v2; env ~TOKEN +FLAGS; limits.memory 512Mi -> 256Mi; desired replicas 2 -> 3 (ReplicaSet annotation)",
		"CONFIG cm/checkout-config ConfigMap updated by kubectl-edit",
		"CONFIG secret/checkout-secrets Secret applied by external-secrets",
		"HPA hpa/checkout last scaled Deployment/checkout: current 0, desired 0",
		"ROLLOUT rs/checkout-1 rev 1 (earliest retained): image example.test/checkout:v1",
		"CONFIG cm/checkout-config ConfigMap created by kubectl-create",
		"NODE node/node-a Ready=True KubeletReady",
	}, got)

	// The merged BackOff event is placed at its first retained occurrence.
	onset := s.OnsetEntry()
	require.NotNil(t, onset)
	require.Equal(t, "Warning BackOff x14: BackOff message for checkout-2-abcde", onset.Summary)
	require.True(t, now.Add(-10*time.Minute).Equal(onset.At))
	require.True(t, now.Add(-time.Minute).Equal(onset.Last))
	require.Equal(t, "FAULT BEGAN", s.RelationLabel(s.Onset))

	require.Equal(t, "2m after", s.RelationLabel(2))
	require.Equal(t, "2m before", s.RelationLabel(5))
	require.Equal(t, "4m before", s.RelationLabel(6))
	require.Equal(t, "This change preceded the first failing signal by 4m. Order is not cause.", s.RelationSentence(6))

	all := strings.Join(got, "\n") + s.Report(0)
	for index := range s.Entries {
		all += strings.Join(s.Entries[index].Detail, "\n") + s.RelationSentence(index)
	}
	require.NotContains(t, all, secretValue, "environment and Secret values are never rendered")
	require.NotContains(t, strings.ToLower(all), "caused")
	require.NotContains(t, all, "other-pod", "events outside the workload are out of scope")
	require.Contains(t, s.Report(0), "older event history is unavailable, not empty")
}

func TestCollectReportsDeniedAndTruncatedSourcesInsteadOfImplyingNothingHappened(t *testing.T) {
	r, dyn := readers(t, incident()...)
	dyn.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)
	})
	dyn.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSetList"}}
		list.SetContinue("more")
		return true, list, nil
	})
	dyn.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})
	s, err := Collect(context.Background(), r, deploymentScope(), now)
	require.NoError(t, err)
	require.True(t, s.Partial())
	require.Equal(t, "events denied, replicasets incomplete, nodes unknown", s.Gaps())
	require.Contains(t, s.EventHorizon(), "event history is unknown, not empty")
	for index := range s.Entries {
		require.NotEqual(t, SourceEvent, s.Entries[index].Source)
		require.NotEqual(t, SourceRollout, s.Entries[index].Source)
	}
	// With events denied, the earliest failing signal left is the autoscaler.
	require.Equal(t, SourceAutoscaler, s.OnsetEntry().Source)
	report := s.Report(0)
	require.Contains(t, report, "DENIED     access denied; this history is unknown, not empty")
	require.Contains(t, report, "INCOMPLETE only the first 0 were read; more exist")
	require.Contains(t, report, "node-a: read timed out")
}

func TestCollectScopesNamespaceAndPodAndRejectsReplacedIdentity(t *testing.T) {
	r, _ := readers(t, incident()...)
	s, err := Collect(context.Background(), r, &Scope{Context: "ctx", Namespace: "apps"}, now)
	require.NoError(t, err)
	require.Contains(t, strings.Join(summaries(s), "\n"), "EVENT pod/other-pod Warning Unhealthy")
	require.Equal(t, "Warning Unhealthy: Unhealthy message for other-pod", s.OnsetEntry().Summary, "the earliest warning in the namespace")
	s.SetReference(3)
	require.Equal(t, "REFERENCE", s.RelationLabel(3))
	require.Contains(t, s.RelationSentence(len(s.Entries)-1), "before the reference moment you chose")
	s.ResetReference()
	require.False(t, s.Chosen)

	r, _ = readers(t, incident()...)
	podScope := &Scope{Context: "ctx", Namespace: "apps", Resource: resPods, Kind: "Pod", Name: "checkout-2-abcde"}
	objects := incident()
	objects[4].(*unstructured.Unstructured).SetOwnerReferences([]metav1.OwnerReference{{Kind: "ReplicaSet", Name: "checkout-2", UID: "uid-replicaset-checkout-2"}})
	r, _ = readers(t, objects...)
	s, err = Collect(context.Background(), r, podScope, now)
	require.NoError(t, err)
	joined := strings.Join(summaries(s), "\n")
	require.Contains(t, joined, "ROLLOUT rs/checkout-2 rev 2", "a pod follows its ReplicaSet to the owning Deployment")
	require.Contains(t, joined, "CONFIG cm/checkout-config ConfigMap updated by kubectl-edit")
	require.NotContains(t, joined, "other-pod")

	replaced := deploymentScope()
	replaced.UID = "earlier-uid"
	_, err = Collect(context.Background(), r, replaced, now)
	require.ErrorContains(t, err, "identity changed")
	_, err = Collect(context.Background(), r, &Scope{Namespace: "apps", Kind: "Service", Name: "x", Resource: "v1/services"}, now)
	require.ErrorContains(t, err, "not a workload")
	_, err = Collect(context.Background(), r, &Scope{}, now)
	require.ErrorContains(t, err, "one namespace")
}

func TestTemplateChangesNameKeysAndResourcesButNeverValues(t *testing.T) {
	container := func(image string, env []corev1.EnvVar, limit string, args ...string) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: image, Env: env, Args: args,
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}}}}}}
	}
	before := container("registry.test:5000/team/api:1.4", []corev1.EnvVar{{Name: "KEEP", Value: "same"}, {Name: "GONE", Value: secretValue}, {Name: "EDIT", Value: "a"}}, "1Gi")
	after := container("registry.test:5000/team/api:1.5", []corev1.EnvVar{{Name: "KEEP", Value: "same"}, {Name: "EDIT", Value: secretValue}, {Name: "NEW", Value: secretValue}}, "1024Mi", "--token="+secretValue)
	changes := TemplateChanges(&before, &after)
	require.Equal(t, []string{"image 1.4 -> 1.5", "env ~EDIT +NEW -GONE", "command/args changed (not shown)"}, changes,
		"1Gi and 1024Mi are the same quantity")
	require.NotContains(t, strings.Join(changes, " "), secretValue)

	after.Spec.Containers = append(after.Spec.Containers, corev1.Container{Name: "proxy", Image: "other.test/proxy@sha256:abc"})
	after.Spec.Containers[0].Image = "other.test/api:1.4"
	changes = TemplateChanges(&before, &after)
	require.Contains(t, changes, "api image registry.test:5000/team/api:1.4 -> other.test/api:1.4")
	require.Contains(t, changes, "container proxy added (image other.test/proxy@sha256:abc)")
	require.Equal(t, []string{"container proxy removed"}, TemplateChanges(&after, &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: after.Spec.Containers[:1]}})[0:1])
}

func TestVisibleWindowKeepsOngoingEventsAndFiltersText(t *testing.T) {
	s := &Snapshot{CapturedAt: now, Entries: []Entry{
		{At: now.Add(-2 * time.Hour), Last: now.Add(-time.Minute), Source: SourceEvent, Summary: "Warning BackOff", Failing: true},
		{At: now.Add(-30 * time.Minute), Source: SourceConfig, Summary: "ConfigMap updated", Change: true, Ref: Ref{Kind: "ConfigMap", Name: "settings"}},
		{At: now.Add(-3 * time.Hour), Source: SourceRollout, Summary: "rev 2", Change: true},
	}}
	s.finish()
	require.Equal(t, []int{0, 1, 2}, s.Visible(0, ""))
	require.Equal(t, []int{0, 1}, s.Visible(time.Hour, ""), "an event still recurring inside the window stays visible")
	require.Equal(t, []int{1}, s.Visible(15*time.Minute, ""))
	require.Equal(t, []int{0}, s.Visible(0, "cm/SETT"))
	require.Equal(t, []int{2}, s.Visible(0, "rollout"))
	require.Equal(t, 1, s.Onset)
	require.Equal(t, "1h before", s.RelationLabel(2))
	require.Equal(t, "1h30m after", Span(90*time.Minute)+" after")

	empty := &Snapshot{CapturedAt: now}
	empty.finish()
	require.Nil(t, empty.OnsetEntry())
	require.Contains(t, empty.Report(0), "None read. This is not evidence of health")
	require.Contains(t, empty.EventHorizon(), "not evidence that nothing happened")
}

func TestSpansAndClock(t *testing.T) {
	for d, want := range map[time.Duration]string{45 * time.Second: "45s", 150 * time.Second: "2m30s", 12 * time.Minute: "12m",
		190 * time.Minute: "3h10m", 26 * time.Hour: "26h", 52 * time.Hour: "2d4h", 30 * 24 * time.Hour: "30d"} {
		require.Equal(t, want, Span(d))
	}
	require.Equal(t, "2d", Coarse(71*time.Hour))
	require.Equal(t, "11:58:00", Clock(now.Add(-2*time.Minute), now))
	require.Equal(t, "Nov14 12h", Clock(now.Add(-72*time.Hour), now))
}

func TestCollectRefusesAMissingNamespaceAndSaysWhenItCannotVerifyOne(t *testing.T) {
	r, dyn := readers(t, incident()...)
	_, err := Collect(context.Background(), r, &Scope{Context: "ctx", Namespace: "nosuch"}, now)
	require.ErrorContains(t, err, "namespace nosuch was not found", "empty lists in a missing namespace must not read as a quiet namespace")

	dyn.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "apps", errors.New("no"))
	})
	s, err := Collect(context.Background(), r, &Scope{Context: "ctx", Namespace: "apps"}, now)
	require.NoError(t, err, "a namespace-restricted reader still gets a timeline")
	require.NotEmpty(t, s.Entries)
	require.Contains(t, s.Report(0), "The namespace object itself could not be read (access denied")
}

func TestAutoscalerDeliberatelyDisabledIsNotAFailingSignal(t *testing.T) {
	at := metav1.NewTime(now.Add(-time.Hour))
	hpa := &autoscalingv2.HorizontalPodAutoscaler{Status: autoscalingv2.HorizontalPodAutoscalerStatus{Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
		{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "ScalingDisabled", LastTransitionTime: at},
		{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", LastTransitionTime: at}}}}
	entries, _ := autoscalerEntries(hpa)
	require.Len(t, entries, 2)
	require.False(t, entries[0].Failing)
	require.True(t, entries[1].Failing)
}

// A view draws one row per kind of entry and may call a kind quiet only
// when the reads it comes from were complete. Every read a source names is
// one a collection reports, so a kind is never held against a read that
// does not exist.
func TestEverySourceNamesTheReadsACollectionReports(t *testing.T) {
	r, _ := readers(t, incident()...)
	s, err := Collect(context.Background(), r, deploymentScope(), now)
	require.NoError(t, err)
	reported := map[string]bool{sourceAlerts: true}
	for _, coverage := range s.Coverage {
		reported[coverage.Source] = true
	}
	for _, source := range []Source{SourceEvent, SourceRollout, SourceRestart, SourceConfig, SourceAutoscaler, SourceNode, SourceCondition, SourceAlert} {
		reads := source.Reads()
		require.NotEmpty(t, reads, source)
		for _, read := range reads {
			require.True(t, reported[read], "%s names the read %q, which no collection reports", source, read)
		}
	}
	for index := range s.Entries {
		require.NotEmpty(t, s.Entries[index].Source.Reads(), s.Entries[index].Source)
	}
	require.Empty(t, SourceAnnotation.Reads(), "the view reads annotations and names that read itself")
}
