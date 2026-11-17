// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

const secretValue = "synthetic-value-must-not-be-shown"

func template(tag, memory, key string, flags bool) *corev1.PodTemplateSpec {
	env := []corev1.EnvVar{
		{Name: "LOG_LEVEL", Value: "info"},
		{Name: "PAYMENT_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "checkout-secrets"}, Key: key}}},
	}
	if flags {
		env = append(env, corev1.EnvVar{Name: "FEATURE_FLAGS", Value: secretValue})
	}
	return &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api", Image: "example.test/checkout:" + tag, Env: env,
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)}},
	}}}}
}

func templateMap(t *testing.T, tag, memory, key string, flags bool) map[string]any {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(template(tag, memory, key, flags))
	require.NoError(t, err)
	return raw
}

func object(apiVersion, kind, name string, created time.Time, owner map[string]any, extra map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": "apps", "uid": "uid-" + strings.ToLower(kind) + "-" + name,
		"creationTimestamp": created.UTC().Format(time.RFC3339), "labels": map[string]any{"app": "checkout"}}
	if owner != nil {
		meta["ownerReferences"] = []any{owner}
	}
	content := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	for key, value := range extra {
		if key == "annotations" {
			meta[key] = value
			continue
		}
		content[key] = value
	}
	return &unstructured.Unstructured{Object: content}
}

func ownerRef(kind, name string) map[string]any {
	return map[string]any{"apiVersion": "apps/v1", "kind": kind, "name": name, "uid": "uid-" + strings.ToLower(kind) + "-" + name, "controller": true}
}

func replicaSet(t *testing.T, name, revision string, created time.Time, owner, tag, memory string) *unstructured.Unstructured {
	t.Helper()
	return object("apps/v1", "ReplicaSet", name, created, ownerRef("Deployment", owner), map[string]any{
		"annotations": map[string]any{revisionAnnotation: revision},
		"spec":        map[string]any{"template": templateMap(t, tag, memory, "token-"+tag, tag != "v1")},
	})
}

func cluster(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		podsResource: "PodList", replicaSetsResource: "ReplicaSetList", revisionsResource: "ControllerRevisionList",
		workloadResources[KindDeployment]: "DeploymentList", workloadResources[KindStatefulSet]: "StatefulSetList",
		workloadResources[KindDaemonSet]: "DaemonSetList",
	}, objects...)
}

func selector() map[string]any {
	return map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "checkout"}}}
}

func TestHistoryOrdersRevisionsAndDiffsNamesNotValues(t *testing.T) {
	objects := cluster(
		object("apps/v1", "Deployment", "checkout-api", rolloutAt.Add(-96*time.Hour), nil, map[string]any{"spec": selector()}),
		replicaSet(t, "checkout-api-7d9f8c6b5d", "2", rolloutAt, "checkout-api", "v2", "256Mi"),
		replicaSet(t, "checkout-api-6c8d7f9b5", "1", rolloutAt.Add(-72*time.Hour), "checkout-api", "v1", "512Mi"),
		replicaSet(t, "other-5d5d5d5d5", "9", rolloutAt.Add(time.Hour), "other", "v9", "1Gi"),
	)
	workload := Workload{Kind: KindDeployment, Namespace: "apps", Name: "checkout-api"}
	history, err := ReadHistory(t.Context(), objects, workload)
	require.NoError(t, err)
	require.Len(t, history.Revisions, 2, "a ReplicaSet of another Deployment is not a revision of this one")
	require.Equal(t, int64(1), history.Revisions[0].Number)
	require.Contains(t, history.Notes[0], "creation time of the revision's ReplicaSet")

	latest, err := history.Rollout(0)
	require.NoError(t, err)
	require.Equal(t, "rev 2", latest.Label())
	require.Equal(t, rolloutAt, latest.Revision.Created.UTC())
	require.Equal(t, rolloutAt.Add(-72*time.Hour), latest.PreviousAt().UTC())
	require.True(t, latest.NextAt.IsZero())
	require.Equal(t, 2, latest.Total)
	changes, limit := latest.Changes()
	require.Equal(t, []string{"image v1 -> v2", "limits.memory 512Mi -> 256Mi", "env ~PAYMENT_TOKEN +FEATURE_FLAGS"}, changes)
	require.Equal(t, "512Mi -> 256Mi", limit)
	require.NotContains(t, strings.Join(changes, " "), secretValue)
	require.NotContains(t, strings.Join(changes, " "), "token-v")

	earliest, err := history.Rollout(1)
	require.NoError(t, err)
	require.Equal(t, 1, earliest.Back)
	require.Nil(t, earliest.Previous)
	require.Equal(t, rolloutAt, earliest.NextAt.UTC())
	changes, limit = earliest.Changes()
	require.Empty(t, changes)
	require.Empty(t, limit)
	clamped, err := history.Rollout(7)
	require.NoError(t, err)
	require.Equal(t, 1, clamped.Back)

	require.Equal(t, "apps/v1/deployments", workload.Resource())
	require.Equal(t, "Deployment apps/checkout-api", workload.Label())
}

func TestARollbackOntoAnOlderReplicaSetHasNoKnownTime(t *testing.T) {
	objects := cluster(
		object("apps/v1", "Deployment", "checkout-api", rolloutAt.Add(-96*time.Hour), nil, map[string]any{"spec": selector()}),
		replicaSet(t, "checkout-api-6c8d7f9b5", "3", rolloutAt.Add(-72*time.Hour), "checkout-api", "v1", "512Mi"),
		replicaSet(t, "checkout-api-7d9f8c6b5d", "2", rolloutAt, "checkout-api", "v2", "256Mi"),
	)
	history, err := ReadHistory(t.Context(), objects, Workload{Kind: KindDeployment, Namespace: "apps", Name: "checkout-api"})
	require.NoError(t, err)
	rollout, err := history.Rollout(0)
	require.ErrorContains(t, err, "rev 3 reuses checkout-api-6c8d7f9b5")
	require.ErrorContains(t, err, "Kubernetes does not record when a rollback happened")
	require.Equal(t, 2, rollout.Total, "stepping to another rollout stays possible")
	earlier, err := history.Rollout(1)
	require.NoError(t, err)
	require.Equal(t, "rev 2", earlier.Label())

	_, err = (&History{Workload: Workload{Kind: KindDeployment, Namespace: "apps", Name: "new"}}).Rollout(0)
	require.ErrorContains(t, err, "no revision of Deployment apps/new is retained")
}

func TestResolveFollowsAPodToItsWorkloadAndRefusesWhatHasNoRollout(t *testing.T) {
	objects := cluster(
		object("apps/v1", "Deployment", "checkout-api", rolloutAt, nil, map[string]any{"spec": selector()}),
		replicaSet(t, "checkout-api-7d9f8c6b5d", "2", rolloutAt, "checkout-api", "v2", "256Mi"),
		object("v1", "Pod", "checkout-api-7d9f8c6b5d-x2x9q", rolloutAt, ownerRef("ReplicaSet", "checkout-api-7d9f8c6b5d"), nil),
		object("v1", "Pod", "ledger-0", rolloutAt, ownerRef("StatefulSet", "ledger"), nil),
		object("v1", "Pod", "checkout-api-0", rolloutAt, nil, nil),
		object("v1", "Pod", "nightly-abcde", rolloutAt, ownerRef("Job", "nightly"), nil),
	)
	resolve := func(kind, name string) (Workload, error) { return Resolve(t.Context(), objects, kind, "apps", name) }
	w, err := resolve(KindPod, "checkout-api-7d9f8c6b5d-x2x9q")
	require.NoError(t, err)
	require.Equal(t, Workload{Kind: KindDeployment, Namespace: "apps", Name: "checkout-api"}, w)
	w, err = resolve(KindPod, "ledger-0")
	require.NoError(t, err)
	require.Equal(t, KindStatefulSet, w.Kind)
	w, err = resolve(KindDaemonSet, "node-agent")
	require.NoError(t, err)
	require.Equal(t, "node-agent-[a-z0-9]+", w.PodPattern())

	_, err = resolve(KindPod, "checkout-api-0")
	require.ErrorContains(t, err, "has no controller")
	_, err = resolve(KindPod, "nightly-abcde")
	require.ErrorContains(t, err, "controlled by a Job, which has no rollout history")
	_, err = resolve(KindPod, "missing")
	require.ErrorContains(t, err, "not found")
	_, err = resolve("Service", "checkout-api")
	require.ErrorContains(t, err, "select a Deployment, StatefulSet, DaemonSet or one of their Pods")
	_, err = ReadHistory(t.Context(), objects, Workload{Kind: KindDeployment, Namespace: "apps", Name: "missing"})
	require.ErrorContains(t, err, "Deployment apps/missing: not found")
}

func TestStatefulSetRevisionsComeFromControllerRevisions(t *testing.T) {
	revision := func(name string, number int64, created time.Time, tag, memory string) *unstructured.Unstructured {
		patch := templateMap(t, tag, memory, "token-"+tag, false)
		patch["$patch"] = "replace"
		return object("apps/v1", "ControllerRevision", name, created, ownerRef("StatefulSet", "ledger"), map[string]any{
			"revision": number, "data": map[string]any{"spec": map[string]any{"template": patch}},
		})
	}
	objects := cluster(
		object("apps/v1", "StatefulSet", "ledger", rolloutAt.Add(-96*time.Hour), nil, map[string]any{"spec": selector()}),
		revision("ledger-5f7d", 4, rolloutAt, "v4", "1Gi"),
		revision("ledger-77c9", 3, rolloutAt.Add(-24*time.Hour), "v3", "1Gi"),
	)
	history, err := ReadHistory(t.Context(), objects, Workload{Kind: KindStatefulSet, Namespace: "apps", Name: "ledger"})
	require.NoError(t, err)
	require.Len(t, history.Revisions, 2)
	rollout, err := history.Rollout(0)
	require.NoError(t, err)
	require.Equal(t, "rev 4", rollout.Label())
	changes, limit := rollout.Changes()
	require.Equal(t, []string{"image v3 -> v4", "env ~PAYMENT_TOKEN"}, changes)
	require.Empty(t, limit, "an unchanged limit is not reported as a change")
	require.Contains(t, history.Notes[0], "ControllerRevision")
}

func TestHistorySaysWhenTheRevisionListWasCut(t *testing.T) {
	objects := cluster(
		object("apps/v1", "Deployment", "checkout-api", rolloutAt.Add(-96*time.Hour), nil, map[string]any{"spec": selector()}),
		replicaSet(t, "checkout-api-6c8d7f9b5", "1", rolloutAt.Add(-72*time.Hour), "checkout-api", "v1", "512Mi"),
	)
	workload := Workload{Kind: KindDeployment, Namespace: "apps", Name: "checkout-api"}
	history, err := ReadHistory(t.Context(), objects, workload)
	require.NoError(t, err)
	require.Empty(t, history.Incomplete, "a complete list makes no claim about missing rollouts")

	// The server holds more ReplicaSets than one bounded read returns.
	objects.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSetList"}}
		list.SetContinue("more")
		list.Items = append(list.Items, *replicaSet(t, "checkout-api-6c8d7f9b5", "1", rolloutAt.Add(-72*time.Hour), "checkout-api", "v1", "512Mi"))
		return true, list, nil
	})
	history, err = ReadHistory(t.Context(), objects, workload)
	require.NoError(t, err)
	require.Equal(t, "Only the first 1 ReplicaSets were read (limit 200): a later rollout may be missing.", history.Incomplete)
}
