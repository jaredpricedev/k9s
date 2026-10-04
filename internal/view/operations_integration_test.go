// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/watch"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// This optional test only accepts the separately named, disposable local kind
// cluster. Ordinary tests never load a user's default kubeconfig or contact it.
func TestDisposableKubernetesOperationsAndPressure(t *testing.T) {
	path := os.Getenv("K9PLUS_AUDIT_KUBECONFIG")
	if path == "" {
		t.Skip("set K9PLUS_AUDIT_KUBECONFIG to the isolated kind-k9plus-audit kubeconfig")
	}
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw.CurrentContext != "kind-k9plus-audit" {
		t.Fatal("integration test requires the named disposable kind-k9plus-audit context")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(cfg.Host)
	if err != nil || (endpoint.Hostname() != "127.0.0.1" && endpoint.Hostname() != "localhost") {
		t.Fatal("integration test requires a local loopback API endpoint")
	}
	cfg.Timeout = 10 * time.Second
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	installAuditResourceMetadata(t, typed)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ns := fmt.Sprintf("k9plus-audit-%d", time.Now().UnixNano())
	if _, createErr := typed.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); createErr != nil {
		t.Fatal(createErr)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if deleteErr := typed.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			t.Error(deleteErr)
		}
	})
	zero := int32(0)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: ns}, Spec: appsv1.DeploymentSpec{
		Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "audit"}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "audit"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10"}}}},
	}}
	created, err := typed.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	connection := inspectionConnection{dynamic: dyn, typed: typed}
	factory := watch.NewFactory(connection)
	factory.Start(ns)
	defer factory.Terminate()
	informer, err := factory.ForResource(ns, client.DpGVR)
	if err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForCacheSync(ctx.Done(), informer.Informer().HasSynced) {
		t.Fatal("real list/watch cache did not synchronize")
	}
	app := NewApp(mock.NewMockConfig(t))
	app.factory = factory
	browser := NewBrowser(client.DpGVR).(*Browser)
	browser.app = app
	browser.GetModel().SetNamespace(ns)
	target := selectedResourceForPath(browser, "kind-k9plus-audit", client.FQN(ns, created.Name))
	if target.Err() != nil || target.UID != created.UID {
		t.Fatal("real selection cache did not capture identity", target.Err())
	}
	verifyAuditOperations(t, ctx, typed, dyn, &target, deployment)
	verifyAuditPressure(t, ctx, typed, dyn, ns, target.Context)
}

func installAuditResourceMetadata(t *testing.T, typed kubernetes.Interface) {
	t.Helper()
	// Older package fixtures register built-in resources as synthetic. The live
	// selection must use actual discovery metadata, independently of that state.
	previous := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = previous })
	resources, err := typed.Discovery().ServerResourcesForGroupVersion("apps/v1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range resources.APIResources {
		if resources.APIResources[i].Name == client.DpGVR.R() {
			dao.MetaAccess.RegisterMeta(client.DpGVR.String(), &resources.APIResources[i])
			return
		}
	}
	t.Fatal("real deployment metadata was not discovered")
}

func verifyAuditOperations(t *testing.T, ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface,
	target *SelectedResourceTarget, deployment *appsv1.Deployment,
) {
	t.Helper()
	ns := target.Namespace
	session := &operationSession{dynamic: dyn, typed: typed}
	if restartErr := session.restart(ctx, *target, metav1.PatchOptions{FieldManager: "k9plus-audit"}); restartErr != nil {
		t.Fatal("real restart", restartErr)
	}
	observed, err := typed.AppsV1().Deployments(ns).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil || observed.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Fatal("restart not accepted", err)
	}
	if scaleErr := session.scale(ctx, *target, 1); scaleErr != nil {
		t.Fatal("real scale", scaleErr)
	}
	observed, err = typed.AppsV1().Deployments(ns).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil || *observed.Spec.Replicas != 1 {
		t.Fatal("scale not accepted", err)
	}
	staleVersion := observed.ResourceVersion
	_, err = typed.AppsV1().Deployments(ns).Patch(ctx, target.Name, types.MergePatchType, []byte(`{"metadata":{"annotations":{"k9plus-audit":"advance-version"}}}`), metav1.PatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": string(target.UID), "resourceVersion": staleVersion}, "spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]string{"kubectl.kubernetes.io/restartedAt": "must-not-apply"}}}}})
	if _, patchErr := dyn.Resource(target.GVR.GVR()).Namespace(ns).Patch(ctx, target.Name, types.MergePatchType, patch, metav1.PatchOptions{}); !apierrors.IsConflict(patchErr) {
		t.Fatal("API accepted stale resource-version patch", patchErr)
	}
	if deleteErr := session.delete(ctx, *target, nil, dao.DefaultGrace); deleteErr != nil {
		t.Fatal("real delete", deleteErr)
	}
	for {
		_, getErr := typed.AppsV1().Deployments(ns).Get(ctx, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			break
		}
		if getErr != nil {
			t.Fatal(getErr)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	deployment.ResourceVersion, deployment.UID = "", ""
	replacement, err := typed.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil || replacement.UID == target.UID {
		t.Fatal("recreation failed", err)
	}
	if restartErr := session.restart(ctx, *target, metav1.PatchOptions{}); restartErr == nil {
		t.Fatal("restart followed same-name recreation")
	}
	if scaleErr := session.scale(ctx, *target, 3); scaleErr == nil {
		t.Fatal("scale followed same-name recreation")
	}
	if deleteErr := session.delete(ctx, *target, nil, dao.DefaultGrace); deleteErr == nil {
		t.Fatal("delete followed same-name recreation")
	}
	oldUID := target.UID
	if deleteErr := dyn.Resource(target.GVR.GVR()).Namespace(ns).Delete(ctx, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &oldUID}}); deleteErr == nil {
		t.Fatal("API accepted stale UID delete precondition")
	}
	current, err := typed.AppsV1().Deployments(ns).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil || current.UID != replacement.UID || *current.Spec.Replicas != 0 {
		t.Fatal("replacement was changed", err)
	}
}

func verifyAuditPressure(t *testing.T, ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, ns, contextName string) {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending-evidence", Namespace: ns}, Spec: corev1.PodSpec{
		NodeSelector: map[string]string{"k9plus-audit/no-matching-node": "true"},
		Containers:   []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}}}},
	}}
	pod, err := typed.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	podTarget := SelectedResourceTarget{Context: contextName, GVR: client.PodGVR, Namespace: ns, Name: pod.Name, UID: pod.UID}
	conn := inspectionConnection{dynamic: dyn, typed: typed}
	var text string
	for {
		text, err = loadResourcePressure(ctx, conn, podTarget, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "FailedScheduling") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("scheduler evidence did not arrive", text)
		case <-time.After(200 * time.Millisecond):
		}
	}
	for _, expected := range []string{"request=100m", "64.00MiB (64Mi)", "usage=N/A", "CPU throttling: unknown", "FailedScheduling", "Kubernetes Events API"} {
		if !strings.Contains(text, expected) {
			t.Fatal("real pressure evidence missing", expected, text)
		}
	}
	if strings.Contains(text, "usage=0m") {
		t.Fatal("missing real metrics became measured zero", text)
	}
	if snapshot, snapshotErr := loadTargetInspection(ctx, conn, podTarget, troubleshootCommand); snapshotErr != nil || !strings.Contains(snapshot, string(pod.UID)) {
		t.Fatal("real snapshot unavailable", snapshotErr)
	}
	t.Log("real local Kubernetes accepted restart/scale/delete, enforced UID/RV preconditions, rejected same-name recreation, retained pressure/scheduling evidence without metrics")
}
