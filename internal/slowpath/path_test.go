// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

var pathNow = time.Date(2026, 11, 17, 8, 20, 0, 0, time.UTC)

func object(apiVersion, kind, name string, content map[string]any) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
	o.SetNamespace("gitlab")
	o.SetName(name)
	o.SetUID(types.UID("uid-" + name))
	for key, value := range content {
		o.Object[key] = value
	}
	return o
}

func owned(o *unstructured.Unstructured, apiVersion, kind, name string) *unstructured.Unstructured {
	controller := true
	o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &controller}})
	return o
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// pathCluster is a Service with two Pods behind it (one not ready and
// restarted), an Ingress with two backends, a second Service that selects
// the same Pods, a Deployment with one retained revision, a workload no
// Service selects, and the EndpointSlice of a declared dependency.
func pathCluster(t *testing.T) *fake.FakeDynamicClient {
	t.Helper()
	labels := map[string]string{"app": "web"}
	pod := func(name, node string, ready bool, restarts int64) *unstructured.Unstructured {
		status := "True"
		if !ready {
			status = "False"
		}
		container := map[string]any{"name": "web", "restartCount": restarts}
		if restarts > 0 {
			container["lastState"] = map[string]any{"terminated": map[string]any{"finishedAt": stamp(pathNow.Add(-10 * time.Minute))}}
		}
		o := owned(object("v1", "Pod", name, map[string]any{
			"spec": map[string]any{"nodeName": node, "dnsPolicy": "ClusterFirst"},
			"status": map[string]any{"phase": "Running", "podIP": "10.42.0.9",
				"conditions":        []any{map[string]any{"type": "Ready", "status": status, "lastTransitionTime": stamp(pathNow.Add(-9 * time.Minute))}},
				"containerStatuses": []any{container}},
		}), "apps/v1", "ReplicaSet", "web-6f9c")
		o.SetLabels(labels)
		o.SetCreationTimestamp(metav1.NewTime(pathNow.Add(-48 * time.Hour)))
		return o
	}
	service := func(name string, selector map[string]any) *unstructured.Unstructured {
		return object("v1", "Service", name, map[string]any{"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.0.0.1", "selector": selector,
			"ports": []any{map[string]any{"name": "http", "port": int64(80), "targetPort": "http", "protocol": "TCP"}}}})
	}
	slice := func(name, service string, endpoints ...any) *unstructured.Unstructured {
		o := owned(object("discovery.k8s.io/v1", "EndpointSlice", name, map[string]any{"addressType": "IPv4", "endpoints": endpoints}), "v1", "Service", service)
		o.SetLabels(map[string]string{"kubernetes.io/service-name": service})
		return o
	}
	endpoint := func(pod, node string, ready bool) map[string]any {
		return map[string]any{"addresses": []any{"10.42.0.9"}, "conditions": map[string]any{"ready": ready}, "nodeName": node,
			"targetRef": map[string]any{"apiVersion": "v1", "kind": "Pod", "namespace": "gitlab", "name": pod, "uid": "uid-" + pod}}
	}
	backend := func(name string) map[string]any {
		return map[string]any{"path": "/", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": name, "port": map[string]any{"number": int64(80)}}}}
	}
	template := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "web"}}}}
	replicaSet := owned(object("apps/v1", "ReplicaSet", "web-6f9c", map[string]any{"spec": map[string]any{"template": template}}), "apps/v1", "Deployment", "web")
	replicaSet.SetLabels(labels)
	replicaSet.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "3"})
	replicaSet.SetCreationTimestamp(metav1.NewTime(pathNow.Add(-48 * time.Hour)))
	kinds := map[schema.GroupVersionResource]string{}
	for resource, kind := range map[string][3]string{
		"services": {"", "v1", "ServiceList"}, "pods": {"", "v1", "PodList"}, "namespaces": {"", "v1", "NamespaceList"},
		"endpointslices": {"discovery.k8s.io", "v1", "EndpointSliceList"}, "ingresses": {"networking.k8s.io", "v1", "IngressList"},
		"networkpolicies": {"networking.k8s.io", "v1", "NetworkPolicyList"}, "httproutes": {"gateway.networking.k8s.io", "v1", "HTTPRouteList"},
		"gateways": {"gateway.networking.k8s.io", "v1", "GatewayList"}, "referencegrants": {"gateway.networking.k8s.io", "v1beta1", "ReferenceGrantList"},
		"deployments": {"apps", "v1", "DeploymentList"}, "statefulsets": {"apps", "v1", "StatefulSetList"}, "replicasets": {"apps", "v1", "ReplicaSetList"},
		"controllerrevisions": {"apps", "v1", "ControllerRevisionList"},
	} {
		kinds[schema.GroupVersionResource{Group: kind[0], Version: kind[1], Resource: resource}] = kind[2]
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds,
		service("web", map[string]any{"app": "web"}), service("web-metrics", map[string]any{"app": "web"}), service("other", map[string]any{"app": "other"}),
		pod("web-6f9c-aaaaa", "worker-a", true, 0), pod("web-6f9c-bbbbb", "worker-b", false, 2),
		slice("web-x1", "web", endpoint("web-6f9c-aaaaa", "worker-a", true), endpoint("web-6f9c-bbbbb", "worker-b", false)),
		slice("db-x1", "db", endpoint("db-0", "worker-a", true)),
		object("networking.k8s.io/v1", "Ingress", "web-ing", map[string]any{"spec": map[string]any{"ingressClassName": "nginx",
			"rules": []any{map[string]any{"host": "web.example.test", "http": map[string]any{"paths": []any{backend("other"), backend("web")}}}}}}),
		object("networking.k8s.io/v1", "Ingress", "empty-ing", map[string]any{"spec": map[string]any{}}),
		object("apps/v1", "Deployment", "web", map[string]any{"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "template": template}}),
		replicaSet,
		object("apps/v1", "StatefulSet", "batch", map[string]any{"spec": map[string]any{
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "batch"}}}}}),
	)
}

func pathSources() *observability.Sources {
	return &observability.Sources{RequestPath: &observability.RequestPath{Services: map[string]observability.ServicePath{
		"gitlab/web": {Dependencies: []observability.Dependency{{Service: "gitlab/db", Profile: "postgres-exporter"}}},
	}}}
}

func selection(kind, name string) slowpath.Selection {
	return slowpath.Selection{Context: "ct-1-1", Kind: kind, Namespace: "gitlab", Name: name}
}

func TestThePathOfAServiceIsReadOnceFromTheKubernetesAPI(t *testing.T) {
	objects := pathCluster(t)
	path, err := slowpath.Resolve(t.Context(), objects, pathSources(), selection(slowpath.KindService, "web"))
	require.NoError(t, err)
	assert.Equal(t, "gitlab/web", path.Key())
	assert.Equal(t, "uid-web", path.ServiceUID, "a selection without a UID is pinned to the Service that was read")
	assert.Equal(t, []slowpath.Ingress{{Name: "web-ing", Entry: "web.example.test / (Prefix)"}}, path.Ingresses)
	assert.Equal(t, 2, path.Endpoints)
	assert.Equal(t, 1, path.EndpointsReady)
	assert.True(t, path.EndpointsRead)
	require.Len(t, path.Pods, 2)
	assert.Equal(t, slowpath.Pod{Name: "web-6f9c-aaaaa", UID: "uid-web-6f9c-aaaaa", Node: "worker-a", Ready: "True", ReadySince: pathNow.Add(-9 * time.Minute),
		Created: pathNow.Add(-48 * time.Hour), Endpoint: "ready"}, path.Pods[0])
	assert.Equal(t, slowpath.Pod{Name: "web-6f9c-bbbbb", UID: "uid-web-6f9c-bbbbb", Node: "worker-b", Ready: "False", ReadySince: pathNow.Add(-9 * time.Minute),
		Created: pathNow.Add(-48 * time.Hour), Restarts: 2, LastRestart: pathNow.Add(-10 * time.Minute), Endpoint: "not ready"}, path.Pods[1])
	assert.Equal(t, map[string][]string{"gitlab/db": {"db-0"}}, path.Dependencies, "the Pods behind a declared dependency come from its EndpointSlices")
	assert.Equal(t, impact.Workload{Kind: impact.KindDeployment, Namespace: "gitlab", Name: "web"}, path.Workload)
	assert.Equal(t, []slowpath.Event{{At: pathNow.Add(-48 * time.Hour), Label: "rev 3"}}, path.Rollouts)
	assert.Empty(t, path.RolloutsNote)
	assert.Empty(t, path.Focus)
	assert.Empty(t, path.Notes)

	// Every read is a GET or a bounded LIST in the Service's namespace, and
	// nothing is written.
	for _, action := range objects.Actions() {
		assert.Contains(t, []string{"get", "list"}, action.GetVerb())
		assert.Equal(t, "gitlab", action.GetNamespace())
		assert.NotEqual(t, "secrets", action.GetResource().Resource)
		if list, ok := action.(ktesting.ListAction); ok && action.GetResource().Resource == "endpointslices" {
			assert.Contains(t, list.GetListRestrictions().Labels.String(), "kubernetes.io/service-name=")
		}
	}

	// The endpoints and the dated facts reach the report without any metric.
	plan := slowpath.BuildPlan(pathSources(), path, time.Hour, pathNow)
	status := observability.Status{Kind: observability.KindPrometheus, State: observability.StateNotConfigured, Reason: "no Prometheus source is declared"}
	report := slowpath.Evaluate(plan, slowpath.Unread(plan, status), nil, pathNow)
	service := report.Hop(slowpath.HopService)
	assert.Equal(t, slowpath.Elevated, service.State)
	assert.Equal(t, "1 of 2 ready", service.Key)
	assert.Equal(t, "not ready now: bbbbb · aaaaa Ready=True since 08:11 · bbbbb Ready=False since 08:11 · API only: kube_pod_status_ready not read", service.Evidence)
	assert.Contains(t, sentences(report), "In the window: a Ready change on the endpoints at 08:11; restarts on pod bbbbb at 08:10.")
}

func TestTheServiceIsFoundFromAnIngressAWorkloadOrAPod(t *testing.T) {
	path, err := slowpath.Resolve(t.Context(), pathCluster(t), pathSources(), selection(slowpath.KindIngress, "web-ing"))
	require.NoError(t, err)
	assert.Equal(t, "web", path.Service, "of two backends the one with a request-path declaration is read")
	assert.Equal(t, []string{"Ingress gitlab/web-ing routes to 2 Services; this reads web (others: other)"}, path.Notes)
	assert.Equal(t, "web-ing", path.Ingresses[0].Name)

	path, err = slowpath.Resolve(t.Context(), pathCluster(t), nil, selection(slowpath.KindIngress, "web-ing"))
	require.NoError(t, err)
	assert.Equal(t, "other", path.Service, "without a declaration the first backend is read, and the rest is named")
	assert.Empty(t, path.Pods, "no Pod carries the labels that Service selects")

	path, err = slowpath.Resolve(t.Context(), pathCluster(t), pathSources(), selection(slowpath.KindDeployment, "web"))
	require.NoError(t, err)
	assert.Equal(t, "web", path.Service)
	assert.Equal(t, []string{"2 Services select these Pods; this reads web (others: web-metrics)"}, path.Notes)
	assert.Equal(t, impact.Workload{Kind: impact.KindDeployment, Namespace: "gitlab", Name: "web"}, path.Workload)
	assert.Len(t, path.Pods, 2)

	path, err = slowpath.Resolve(t.Context(), pathCluster(t), pathSources(), selection(slowpath.KindPod, "web-6f9c-bbbbb"))
	require.NoError(t, err)
	assert.Equal(t, "web", path.Service)
	assert.Equal(t, "web-6f9c-bbbbb", path.Focus, "the selected Pod is the row the view opens on")
	assert.Equal(t, impact.KindDeployment, path.Workload.Kind, "the workload is found through the Pod's controller")
}

func TestASelectionWithoutARequestPathSaysSo(t *testing.T) {
	for _, c := range []struct {
		selection slowpath.Selection
		want      string
	}{
		{selection(slowpath.KindStatefulSet, "batch"),
			"no Service in namespace gitlab selects the Pods of StatefulSet gitlab/batch, so there is no request path to read; :graph shows its resource use"},
		{selection(slowpath.KindIngress, "empty-ing"), "Ingress gitlab/empty-ing names no backend Service, so there is no request path to read"},
		{selection(slowpath.KindService, "gone"), "Service gitlab/gone was not found"},
		{selection(slowpath.KindPod, "gone"), "Pod gitlab/gone was not found"},
		{selection("Node", "worker-a"), "select a Service, an Ingress, a Deployment, a StatefulSet or a Pod: a request path belongs to a Service"},
		{slowpath.Selection{Kind: slowpath.KindService, Name: "web"}, "select a Service, an Ingress, a Deployment, a StatefulSet or a Pod first"},
	} {
		path, err := slowpath.Resolve(t.Context(), pathCluster(t), pathSources(), c.selection)
		require.Error(t, err, c.want)
		assert.Equal(t, c.want, err.Error())
		assert.Nil(t, path)
	}
	_, err := slowpath.Resolve(t.Context(), nil, nil, selection(slowpath.KindService, "web"))
	require.EqualError(t, err, "no Kubernetes connection")

	// A Service that was replaced since it was selected is not read in its place.
	replaced := selection(slowpath.KindService, "web")
	replaced.UID = "uid-of-an-earlier-web"
	_, err = slowpath.Resolve(t.Context(), pathCluster(t), pathSources(), replaced)
	require.ErrorContains(t, err, "Service gitlab/web: captured Service identity changed")
}

func TestWhatCouldNotBeReadIsNotedAndNeverGuessed(t *testing.T) {
	objects := pathCluster(t)
	denied := func(resource string) {
		objects.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", nil)
		})
	}
	denied("endpointslices")
	denied("replicasets")
	path, err := slowpath.Resolve(t.Context(), objects, pathSources(), selection(slowpath.KindService, "web"))
	require.NoError(t, err)
	assert.False(t, path.EndpointsRead)
	assert.Zero(t, path.Endpoints)
	assert.Len(t, path.Pods, 2, "the Pods still come from the Service selector")
	assert.Empty(t, path.Pods[0].Endpoint, "without a slice nothing says whether the Pod is an endpoint")
	notes := strings.Join(path.Notes, "\n")
	assert.Contains(t, notes, "EndpointSlices of web: denied")
	assert.Contains(t, notes, "EndpointSlices of dependency gitlab/db: not permitted to read it; its profile reads the whole namespace")
	assert.Empty(t, path.Dependencies["gitlab/db"])
	assert.Empty(t, path.Rollouts)
	assert.Contains(t, path.RolloutsNote, "ReplicaSets of Deployment gitlab/web")

	plan := slowpath.BuildPlan(pathSources(), path, time.Hour, pathNow)
	report := slowpath.Evaluate(plan, slowpath.Unread(plan, observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}), nil, pathNow)
	service := report.Hop(slowpath.HopService)
	assert.Equal(t, slowpath.NotRead, service.State, "neither the slices nor the readiness history were read")
	assert.Contains(t, service.Reason, "the EndpointSlices of the Service were not read")
	rollouts := lane(t, report, "rollouts")
	assert.Contains(t, rollouts.NotRead, "ReplicaSets of Deployment gitlab/web")
	assert.Contains(t, sentences(report), "Not read: ingress, endpoints, pods, db, dns, network and rollouts.")
}
