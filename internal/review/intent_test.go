// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const intentTestOneCPU = "1000m"

func intentFixture() Manifest {
	return Manifest{APIVersion: "apps/v1", Kind: "Deployment", Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "api"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "api", "image": "api:v2", "resources": map[string]any{"requests": map[string]any{"cpu": "1", "memory": "1Gi"}}},
		}}}},
	}}
}

//nolint:gocritic // Tests pass immutable fixture descriptors by value; the helper clones only their object.
func intentLive(manifest Manifest) map[string]any {
	data, _ := json.Marshal(manifest.Object)
	var live map[string]any
	_ = json.Unmarshal(data, &live)
	return live
}

func intentContainers(object map[string]any) []any {
	return object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
}

func TestIntentReviewsAuthoredFieldsWithoutDeletingDefaults(t *testing.T) {
	desired := intentFixture()
	live := intentLive(desired)
	live["status"] = map[string]any{"availableReplicas": 2}
	live["metadata"].(map[string]any)["resourceVersion"] = "91"
	live["spec"].(map[string]any)["replicas"] = 1
	container := intentContainers(live)[0].(map[string]any)
	container["imagePullPolicy"] = "IfNotPresent"
	requests := container["resources"].(map[string]any)["requests"].(map[string]any)
	requests["cpu"], requests["memory"] = intentTestOneCPU, "1024Mi"
	result := CompareIntent(desired, live)
	require.Empty(t, result.Changes)
	require.Positive(t, result.DeclaredFields)
	require.Equal(t, result.DeclaredFields, result.MatchedFields)
	require.Contains(t, strings.Join(result.Unreviewed, " "), "not proposed for removal")
	container["image"] = "api:v1"
	result = CompareIntent(desired, live)
	require.Len(t, result.Changes, 1)
	require.Equal(t, "/spec/template/spec/containers/0/image", result.Changes[0].Path)
	require.Equal(t, "\"api:v1\"", result.Changes[0].Before)
	require.Equal(t, "\"api:v2\"", result.Changes[0].After)
}

func TestIntentNamedListsFollowNamesAndExcludeInjectedEntries(t *testing.T) {
	desired := intentFixture()
	containers := intentContainers(desired.Object)
	containers = append(containers, map[string]any{"name": "worker", "image": "worker:v2"})
	desired.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"] = containers
	live := intentLive(desired)
	current := intentContainers(live)
	live["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"] = []any{current[1], map[string]any{"name": "injected", "image": "proxy:v1"}, current[0]}
	result := CompareIntent(desired, live)
	require.Empty(t, result.Changes)
	require.Contains(t, strings.Join(result.Unreviewed, " "), "live-only named entries")
}

func TestIntentMountsUsePathsAndNumericQuantitiesMatchLive(t *testing.T) {
	desired := intentFixture()
	container := intentContainers(desired.Object)[0].(map[string]any)
	container["volumeMounts"] = []any{map[string]any{"name": "shared", "mountPath": "/a"}, map[string]any{"name": "shared", "mountPath": "/b"}}
	container["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] = int64(1)
	live := intentLive(desired)
	current := intentContainers(live)[0].(map[string]any)
	mounts := current["volumeMounts"].([]any)
	current["volumeMounts"] = []any{mounts[1], mounts[0]}
	current["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] = intentTestOneCPU
	require.Empty(t, CompareIntent(desired, live).Changes)
	current["volumeMounts"] = []any{map[string]any{"name": "shared", "mountPath": "/different"}, mounts[1]}
	result := CompareIntent(desired, live)
	require.NotEmpty(t, result.Changes)
	for _, change := range result.Changes {
		require.Equal(t, "[absent]", change.Before)
	}
}

func TestIntentRedactedValuesNeverEstablishEqualityOrLeak(t *testing.T) {
	desired := intentFixture()
	container := intentContainers(desired.Object)[0].(map[string]any)
	container["env"] = []any{map[string]any{"name": "PASSWORD", "value": "desired-private-value"}}
	container["args"] = []any{"--password", "argument-private-value"}
	live := intentLive(desired)
	intentContainers(live)[0].(map[string]any)["env"].([]any)[0].(map[string]any)["value"] = "live-private-value"
	result := CompareIntent(desired, live)
	require.Empty(t, result.Changes)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	for _, forbidden := range []string{"desired-private-value", "argument-private-value", "live-private-value"} {
		require.NotContains(t, string(encoded), forbidden)
	}
	require.Contains(t, string(encoded), "redacted")
	desired.Object = map[string]any{"data": map[string]any{"PASSWORD": "credential"}}
	result = CompareIntent(desired, map[string]any{"data": map[string]any{"PASSWORD": "credential"}})
	require.Zero(t, result.DeclaredFields)
}

func TestIntentCRDArraysDoNotInferMergeOrQuantitySemantics(t *testing.T) {
	desired := Manifest{APIVersion: "example.io/v1", Kind: "Thing", Object: map[string]any{"spec": map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "1"}}, "items": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}}}}}
	live := intentLive(desired)
	live["spec"].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] = intentTestOneCPU
	live["spec"].(map[string]any)["items"] = []any{map[string]any{"name": "b"}, map[string]any{"name": "a"}}
	result := CompareIntent(desired, live)
	require.Len(t, result.Changes, 2)
	require.Contains(t, strings.Join(result.Unreviewed, " "), "list merge")
}

func TestIntentNullAndLimitsDoNotInventPrunePlans(t *testing.T) {
	desired := Manifest{Kind: "ConfigMap", Object: map[string]any{"metadata": map[string]any{"name": "x"}, "immutable": nil}}
	result := CompareIntent(desired, map[string]any{"immutable": true})
	require.Len(t, result.Changes, 1)
	require.Equal(t, "null", result.Changes[0].After)
	require.NotEqual(t, "removed", result.Changes[0].Kind)
	require.Empty(t, CompareIntent(desired, map[string]any{}).Changes)
	desired.Object = map[string]any{"description": strings.Repeat("x", 65537)}
	result = CompareIntent(desired, map[string]any{})
	require.True(t, result.Truncated)
	require.Zero(t, result.DeclaredFields)
	require.Empty(t, result.Changes)
}

func TestIntentAbsentTargetReviewsSafeCreateFields(t *testing.T) {
	desired := intentFixture()
	result := CompareIntent(desired, nil)
	require.False(t, result.Truncated)
	require.NotEmpty(t, result.Changes)
	for _, change := range result.Changes {
		require.Equal(t, "set", change.Kind)
		require.Equal(t, "[absent]", change.Before)
	}
}

func TestIntentLiteralRedactionMarkersRemainUnreviewed(t *testing.T) {
	desired := Manifest{APIVersion: "example.io/v1", Kind: "Thing", Object: map[string]any{"spec": map[string]any{"password": "[REDACTED]"}}}
	result := CompareIntent(desired, intentLive(desired))
	require.Zero(t, result.DeclaredFields)
	require.Zero(t, result.MatchedFields)
	require.Empty(t, result.Changes)
	require.Contains(t, strings.Join(result.Unreviewed, " "), "redacted")
	desired.Object = map[string]any{"spec": map[string]any{"items": []any{map[string]any{"password": "[REDACTED]"}}}}
	result = CompareIntent(desired, intentLive(desired))
	require.Zero(t, result.DeclaredFields)
	require.Empty(t, result.Changes)
}

func TestIntentReportBoundsRepeatedLongPaths(t *testing.T) {
	leaves := make(map[string]any)
	for i := range 2000 {
		leaves[strings.Repeat("x", 160)+string(rune(i+1000))] = "authored"
	}
	object := map[string]any{"spec": leaves}
	desired := Manifest{APIVersion: "example.io/v1", Kind: "Thing", Object: object}
	result := CompareIntent(desired, map[string]any{})
	require.True(t, result.Truncated)
	bytes := 0
	for _, change := range result.Changes {
		bytes += len(change.Path) + len(change.Kind) + len(change.Before) + len(change.After)
	}
	for _, notice := range result.Unreviewed {
		bytes += len(notice)
	}
	require.LessOrEqual(t, bytes, 256*1024)
	desired.Object = map[string]any{strings.Repeat("x", 4096): map[string]any{"nested": "value"}}
	result = CompareIntent(desired, map[string]any{})
	require.True(t, result.Truncated)
	require.Empty(t, result.Changes)
}
