// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package configreview

import (
	"context"
	"testing"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func configurationFixtures(t *testing.T) (*Scope, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	optional := true
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "api", UID: "pod-uid"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{
			{Name: "LITERAL", Value: "PRIVATE-LITERAL"},
			{Name: "URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: "missing"}}},
			{Name: "AUTH", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token"}}},
			{Name: "OPTIONAL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "absent"}, Key: "key", Optional: &optional}}},
		}}}}}
	cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: ConfigMapKind},
		ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "apps", UID: "cm-uid", ResourceVersion: "1",
			Annotations: map[string]string{"configuration": "PRIVATE-ANNOTATION"}},
		Data: map[string]string{"url": "PRIVATE-CONFIG-VALUE"}, BinaryData: map[string][]byte{"ca.crt": []byte("PRIVATE-BINARY")}}
	listKinds := make(map[schema.GroupVersionResource]string)
	for _, r := range consumerResources {
		listKinds[r.gvr] = r.kind + "List"
	}
	listKinds[schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}] = "ConfigMapList"
	objects := make([]runtime.Object, 0, 2)
	for _, object := range []runtime.Object{pod, cm} {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
		require.NoError(t, err)
		objects = append(objects, &unstructured.Unstructured{Object: raw})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	scope := &Scope{Identity: Identity{ResourceIdentity: inspect.ResourceIdentity{
		Context: "demo", GVR: "v1/pods", Namespace: "apps", Name: "api", UID: "pod-uid"}}}
	return scope, dyn
}

func TestCollectDistinguishesMissingOptionalAndSecretUnknownWithoutValueRetention(t *testing.T) {
	scope, dyn := configurationFixtures(t)
	secretReads := 0
	readers := &Readers{Objects: dyn, Secrets: func(_ context.Context, namespace, name string) (*metav1.PartialObjectMetadata, error) {
		secretReads++
		require.Equal(t, "apps", namespace)
		require.Equal(t, "auth", name)
		return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, UID: "secret-uid", ResourceVersion: "2"}}, nil
	}}
	snapshot, err := Collect(t.Context(), readers, scope)
	require.NoError(t, err)
	require.Len(t, snapshot.References, 3)
	require.Equal(t, "required key missing", snapshot.State(&snapshot.References[0]))
	require.Equal(t, "object present; key visibility unknown", snapshot.State(&snapshot.References[1]))
	require.Equal(t, "optional reference missing", snapshot.State(&snapshot.References[2]))
	require.Equal(t, "URL", snapshot.References[0].Variable)
	require.Equal(t, 1, secretReads)
	text := snapshot.ReferenceText() + snapshot.Evidence() + snapshot.ConsumerText()
	for _, private := range []string{"PRIVATE-LITERAL", "PRIVATE-CONFIG-VALUE", "PRIVATE-BINARY", "PRIVATE-ANNOTATION"} {
		require.NotContains(t, text, private)
	}
	require.Contains(t, text, "ca.crt")
	require.Contains(t, text, "Rollout hypothesis")
	listCount := 0
	for _, action := range dyn.Actions() {
		require.Equal(t, "apps", action.GetNamespace())
		require.NotEqual(t, "secrets", action.GetResource().Resource, "dynamic Secret reads are forbidden")
		require.Contains(t, []string{"get", "list"}, action.GetVerb())
		if options, ok := action.(interface{ GetListOptions() metav1.ListOptions }); ok {
			listCount++
			require.EqualValues(t, MaxConsumersPerKind+1, options.GetListOptions().Limit)
		}
	}
	require.Equal(t, len(consumerResources), listCount)
}

func TestCollectDeniedConsumersAndSecretsDoNotBecomeAbsence(t *testing.T) {
	scope, dyn := configurationFixtures(t)
	dyn.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", nil)
	})
	readers := &Readers{Objects: dyn, Secrets: func(context.Context, string, string) (*metav1.PartialObjectMetadata, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "auth", nil)
	}}
	snapshot, err := Collect(t.Context(), readers, scope)
	require.NoError(t, err)
	require.False(t, snapshot.ConsumersComplete)
	require.Equal(t, Denied, snapshot.State(&snapshot.References[1]))
	require.Contains(t, snapshot.Evidence(), "consumer LIST: denied")
	require.NotContains(t, snapshot.State(&snapshot.References[1]), "missing")
}

func TestCollectRejectsCapturedUIDReplacementAndInvalidScopeBeforeFurtherReads(t *testing.T) {
	scope, dyn := configurationFixtures(t)
	scope.Identity.UID = "old-pod-uid"
	_, err := Collect(t.Context(), &Readers{Objects: dyn}, scope)
	require.ErrorIs(t, err, ErrReplaced)
	require.Len(t, dyn.Actions(), 1)
	dyn.ClearActions()
	scope.Identity.Namespace = ""
	_, err = Collect(t.Context(), &Readers{Objects: dyn}, scope)
	require.Error(t, err)
	require.Empty(t, dyn.Actions())
}

func TestReferencesProjectMountedItemsAndBoundLargeDeclarations(t *testing.T) {
	identity := &Identity{Kind: "Pod", ResourceIdentity: inspect.ResourceIdentity{Namespace: "apps", Name: "api", UID: "uid"}}
	spec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "api", VolumeMounts: []corev1.VolumeMount{{
		Name: "config", MountPath: "/etc/api", SubPath: "settings.yaml"}}}}, Volumes: []corev1.Volume{{Name: "config",
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
			ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"},
				Items: []corev1.KeyToPath{{Key: "url", Path: "settings.yaml"}}}}}}}}}}
	refs := References(identity, spec)
	require.Len(t, refs, 1)
	require.Equal(t, "/etc/api", refs[0].Mount)
	require.Equal(t, "settings.yaml", refs[0].ItemPath)
	require.True(t, refs[0].SubPath)
	snapshot := &Snapshot{References: refs}
	require.Contains(t, snapshot.ReferenceText(), "subPath mounts do not receive")
	for range 1000 {
		spec.Containers[0].Env = append(spec.Containers[0].Env, corev1.EnvVar{Name: "KEY", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token"}}})
	}
	require.Len(t, References(identity, spec), MaxReferences+1, "sentinel reports truncation without retaining unbounded references")
}

func TestChangesDoNotInferValuesRuntimeOrDeletionFromResourceVersionsAndGaps(t *testing.T) {
	identity := Identity{Kind: ConfigMapKind, ResourceIdentity: inspect.ResourceIdentity{Namespace: "apps", Name: "settings", UID: "cm"}}
	prior := &Snapshot{Objects: []Object{{Identity: identity, State: Present, ResourceVersion: "1", KeysKnown: true, KeyNames: []string{"url"}}}}
	current := &Snapshot{Objects: []Object{{Identity: identity, State: Present, ResourceVersion: "2", KeysKnown: true, KeyNames: []string{"url"}}}}
	text := current.Changes(prior)
	require.Contains(t, text, "metadata version only")
	require.Contains(t, text, "runtime adoption remain hypotheses")
	current.Objects[0].State = Denied
	require.Contains(t, current.Changes(prior), "no deletion/value-change inference")
	require.NotContains(t, current.Changes(prior), "Key names: url -> url")
}
