// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

const MaxObjects = 100
const ReadTimeout = 3 * time.Second

type sourceSpec struct {
	name    string
	gvr     schema.GroupVersionResource
	cluster bool
}

var sources = []sourceSpec{
	{SourcePods, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, false},
	{SourceNodes, schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, true},
	{SourceQuota, schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}, false},
	{SourceLimitRange, schema.GroupVersionResource{Version: "v1", Resource: "limitranges"}, false},
	{SourceHPA, schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, false},
	{SourceVPA, schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}, false},
	{SourceMetrics, schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}, false},
}

type sourceResult struct {
	source   sourceSpec
	list     *unstructured.UnstructuredList
	coverage Coverage
}

// Collect reads a fixed seven-source set, independently bounded and canceled.
// It never discovers arbitrary APIs or reads Secrets, and never paginates an
// unbounded inventory. Continue/oversized results explicitly limit coverage.
func Collect(ctx context.Context, reader dynamic.Interface, scope *Scope, now time.Time) *Snapshot {
	snapshot := &Snapshot{Scope: *scope, CapturedAt: now}
	results := make(chan sourceResult, len(sources))
	for _, source := range sources {
		go func(source sourceSpec) { results <- readSource(ctx, reader, scope, source, now) }(source)
	}
	reads := make(map[string]*sourceResult, len(sources))
	for range sources {
		select {
		case result := <-results:
			reads[result.source.name] = &result
		case <-ctx.Done():
			for _, source := range sources {
				if _, found := reads[source.name]; !found {
					reads[source.name] = &sourceResult{source: source, coverage: Coverage{
						Source: source.name, State: Unavailable, ReadAt: now, Bound: MaxObjects, Detail: ctx.Err().Error(),
					}}
				}
			}
			goto project
		}
	}
project:
	for _, source := range sources {
		snapshot.Coverage = append(snapshot.Coverage, reads[source.name].coverage)
	}
	snapshot.Coverage = append(snapshot.Coverage, Coverage{Source: SourceHistory, State: NotConfigured, ReadAt: now,
		Detail: "No configured historical provider or coverage window; current metrics are samples only"})
	snapshot.addPods(scope, reads[SourcePods], reads[SourceMetrics], now)
	snapshot.addNodes(scope, reads[SourceNodes])
	snapshot.checkAssignedNodeCoverage()
	snapshot.addQuotas(scope, reads[SourceQuota])
	snapshot.addLimitRanges(scope, reads[SourceLimitRange])
	snapshot.addAutoscalers(scope, reads[SourceHPA])
	snapshot.addRecommendations(scope, reads[SourceVPA])
	sort.SliceStable(snapshot.Pods, func(i, j int) bool {
		if len(snapshot.Pods[i].Pending) != len(snapshot.Pods[j].Pending) {
			return len(snapshot.Pods[i].Pending) > len(snapshot.Pods[j].Pending)
		}
		return snapshot.Pods[i].Identity.Namespace+"/"+snapshot.Pods[i].Identity.Name < snapshot.Pods[j].Identity.Namespace+"/"+snapshot.Pods[j].Identity.Name
	})
	return snapshot
}

func (s *Snapshot) addPods(scope *Scope, read, metrics *sourceResult, now time.Time) {
	selector, err := labels.Parse(scope.Selector)
	if err != nil {
		s.decodeFailure(SourcePods, err)
		return
	}
	for _, obj := range items(read) {
		var raw corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
			s.decodeFailure(SourcePods, err)
			continue
		}
		if scope.PodName != "" && raw.Name != scope.PodName {
			continue
		}
		if !selector.Matches(labels.Set(raw.Labels)) {
			continue
		}
		if scope.Node != "" && raw.Spec.NodeName != scope.Node {
			continue
		}
		if scope.PodName != "" && scope.Identity.UID != "" && string(raw.UID) != scope.Identity.UID {
			s.decodeFailure(SourcePods, fmt.Errorf("selected Pod identity changed during collection"))
			continue
		}
		pod := Pod{Identity: identity(scope, "v1/pods", &raw), Phase: string(raw.Status.Phase),
			Node: raw.Spec.NodeName, Placement: Placement(&raw), LimitGaps: LimitGaps(&raw),
			Terminal: raw.Status.Phase == corev1.PodSucceeded || raw.Status.Phase == corev1.PodFailed}
		pod.Requests, pod.Limits, pod.SchedulerRequests = Requirements(&raw)
		for _, condition := range raw.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status != corev1.ConditionTrue {
				pod.Pending = append(pod.Pending, condition)
			}
		}
		pod.Usage = metricForPod(&raw, metrics, now)
		pod.Containers = inspect.NewResourceBudgets(&raw, &pod.Usage.Sample, pod.Usage.Containers, pod.Usage.Window)
		s.Pods = append(s.Pods, pod)
	}
}

func (s *Snapshot) addNodes(scope *Scope, read *sourceResult) {
	for _, obj := range items(read) {
		var raw corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
			s.decodeFailure(SourceNodes, err)
			continue
		}
		if scope.Node != "" && raw.Name != scope.Node {
			continue
		}
		if scope.Node != "" && scope.Kind == "Node" && string(raw.UID) != scope.Identity.UID {
			s.decodeFailure(SourceNodes, fmt.Errorf("selected Node identity changed during collection"))
			continue
		}
		s.Nodes = append(s.Nodes, Node{Identity: identity(scope, "v1/nodes", &raw), Allocatable: raw.Status.Allocatable,
			Unschedulable: raw.Spec.Unschedulable, Conditions: raw.Status.Conditions, Taints: raw.Spec.Taints})
	}
}

func (s *Snapshot) addQuotas(scope *Scope, read *sourceResult) {
	for _, obj := range items(read) {
		var raw corev1.ResourceQuota
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
			s.decodeFailure(SourceQuota, err)
			continue
		}
		s.Quotas = append(s.Quotas, Quota{Identity: identity(scope, "v1/resourcequotas", &raw), Desired: raw.Spec.Hard,
			Hard: raw.Status.Hard, Used: raw.Status.Used, Scopes: raw.Spec.Scopes, ScopeSelector: raw.Spec.ScopeSelector})
	}
}

func (s *Snapshot) addLimitRanges(scope *Scope, read *sourceResult) {
	for _, obj := range items(read) {
		var raw corev1.LimitRange
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
			s.decodeFailure(SourceLimitRange, err)
			continue
		}
		s.LimitRanges = append(s.LimitRanges, LimitRange{Identity: identity(scope, "v1/limitranges", &raw), Limits: raw.Spec.Limits})
	}
}

func (s *Snapshot) addAutoscalers(scope *Scope, read *sourceResult) {
	for _, obj := range items(read) {
		var raw autoscalingv2.HorizontalPodAutoscaler
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &raw); err != nil {
			s.decodeFailure(SourceHPA, err)
			continue
		}
		if !targetMatches(scope, raw.Spec.ScaleTargetRef.APIVersion, raw.Spec.ScaleTargetRef.Kind, raw.Spec.ScaleTargetRef.Name) {
			continue
		}
		_, currentReported, _ := unstructured.NestedInt64(obj.Object, "status", "currentReplicas")
		_, desiredReported, _ := unstructured.NestedInt64(obj.Object, "status", "desiredReplicas")
		s.Autoscalers = append(s.Autoscalers, Autoscaler{Identity: identity(scope, "autoscaling/v2/horizontalpodautoscalers", &raw),
			Target: raw.Spec.ScaleTargetRef, Min: raw.Spec.MinReplicas, Max: raw.Spec.MaxReplicas,
			Current: raw.Status.CurrentReplicas, Desired: raw.Status.DesiredReplicas, CurrentReported: currentReported,
			DesiredReported: desiredReported, Generation: raw.Generation, ObservedGeneration: raw.Status.ObservedGeneration,
			Metrics: raw.Spec.Metrics, CurrentMetrics: raw.Status.CurrentMetrics, Conditions: raw.Status.Conditions})
	}
}

func (s *Snapshot) addRecommendations(scope *Scope, read *sourceResult) {
	for _, obj := range items(read) {
		target, _, _ := unstructured.NestedString(obj.Object, "spec", "targetRef", "name")
		kind, _, _ := unstructured.NestedString(obj.Object, "spec", "targetRef", "kind")
		version, _, _ := unstructured.NestedString(obj.Object, "spec", "targetRef", "apiVersion")
		if !targetMatches(scope, version, kind, target) {
			continue
		}
		mode, _, _ := unstructured.NestedString(obj.Object, "spec", "updatePolicy", "updateMode")
		r := Recommendation{Identity: identity(scope, "autoscaling.k8s.io/v1/verticalpodautoscalers", &obj), Target: kind + "/" + target, APIVersion: version, Mode: mode}
		containers, _, _ := unstructured.NestedSlice(obj.Object, "status", "recommendation", "containerRecommendations")
		if len(containers) > MaxObjects {
			s.decodeFailure(SourceVPA, fmt.Errorf("container recommendations exceed the %d-item bound", MaxObjects))
		}
		for _, value := range containers[:min(len(containers), 100)] {
			m, ok := value.(map[string]any)
			if !ok {
				s.decodeFailure(SourceVPA, fmt.Errorf("invalid recommendation"))
				continue
			}
			c := ContainerRecommendation{}
			c.Name, _ = m["containerName"].(string)
			for key, into := range map[string]*corev1.ResourceList{"target": &c.Target, "lowerBound": &c.Lower, "upperBound": &c.Upper} {
				if raw, ok := m[key].(map[string]any); ok {
					if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, into); err != nil {
						s.decodeFailure(SourceVPA, err)
					}
				}
			}
			r.Containers = append(r.Containers, c)
		}
		s.Recommendations = append(s.Recommendations, r)
	}
}

func readSource(ctx context.Context, reader dynamic.Interface, scope *Scope, source sourceSpec, now time.Time) sourceResult {
	r := sourceResult{source: source, coverage: Coverage{Source: source.name, State: Unavailable, ReadAt: now, Bound: MaxObjects}}
	if err := ctx.Err(); err != nil {
		r.coverage.Detail = err.Error()
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	options := metav1.ListOptions{Limit: MaxObjects + 1}
	if source.name == SourcePods || source.name == SourceMetrics {
		options.LabelSelector = scope.Selector
	}
	if source.name == SourcePods {
		if scope.Node != "" {
			options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", scope.Node).String()
		}
		if scope.PodName != "" {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", scope.PodName).String()
		}
	}
	if source.name == SourceNodes && scope.Node != "" {
		options.FieldSelector = fields.OneTermEqualSelector("metadata.name", scope.Node).String()
	}
	resource := reader.Resource(source.gvr)
	var err error
	if source.name == SourceMetrics && scope.PodName != "" {
		r.coverage.Bound = 1
		var obj *unstructured.Unstructured
		obj, err = resource.Namespace(scope.Namespace).Get(ctx, scope.PodName, metav1.GetOptions{})
		if obj != nil {
			r.list = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*obj}}
		}
	} else if source.cluster {
		r.list, err = resource.List(ctx, options)
	} else {
		r.list, err = resource.Namespace(scope.Namespace).List(ctx, options)
	}
	if err != nil {
		r.coverage.State = errorState(err)
		// A missing named PodMetrics sample does not establish that the
		// cluster's optional metrics API is absent.
		if source.name == SourceMetrics && scope.PodName != "" && apierrors.IsNotFound(err) {
			r.coverage.State = Unavailable
		}
		r.coverage.Detail = err.Error()
		return r
	}
	if r.list == nil {
		r.coverage.Detail = "API returned no collection"
		return r
	}
	r.coverage.ReadAt = time.Now()
	r.coverage.State = Complete
	if len(r.list.Items) == 0 {
		r.coverage.State = Empty
	}
	if len(r.list.Items) > MaxObjects || r.list.GetContinue() != "" {
		r.coverage.State = Partial
		r.coverage.Detail = "Bounded page; more objects may exist. Narrow the scope."
	}
	r.list.Items = r.list.Items[:min(len(r.list.Items), MaxObjects)]
	r.coverage.Visible = len(r.list.Items)
	return r
}

func errorState(err error) State {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return Denied
	case apierrors.IsNotFound(err), apiMeta.IsNoMatchError(err):
		return Absent
	default:
		return Unavailable
	}
}
func items(read *sourceResult) []unstructured.Unstructured {
	if read.list == nil {
		return nil
	}
	return read.list.Items
}
func identity(scope *Scope, gvr string, obj metav1.Object) inspect.ResourceIdentity {
	return inspect.ResourceIdentity{Context: scope.Identity.Context, GVR: gvr, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID())}
}
func (s *Snapshot) decodeFailure(source string, err error) {
	for i := range s.Coverage {
		if s.Coverage[i].Source == source {
			s.Coverage[i].State = Partial
			s.Coverage[i].Detail = "Some evidence could not be decoded: " + err.Error()
		}
	}
}

func metricForPod(pod *corev1.Pod, read *sourceResult, now time.Time) Usage {
	u := Usage{Sample: client.MetricSample{State: client.MetricsUnavailable, Source: client.PodMetricsSource,
		Reason: "No matching metrics sample in the visible page"},
		Values: make(corev1.ResourceList), Containers: make(map[string]corev1.ResourceList)}
	if read.coverage.State == Denied {
		u.Sample.State = client.MetricsDenied
		u.Sample.Reason = read.coverage.Detail
		return u
	}
	if read.coverage.State == Absent {
		u.Sample.State = client.MetricsNotConfigured
		u.Sample.Reason = read.coverage.Detail
		return u
	}
	if read.list == nil {
		u.Sample.Reason = read.coverage.Detail
		return u
	}
	for _, obj := range read.list.Items {
		if obj.GetName() != pod.Name || obj.GetNamespace() != pod.Namespace {
			continue
		}
		var m metricsv1beta1.PodMetrics
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &m); err != nil {
			u.Sample.Reason = "Invalid metric quantities: " + err.Error()
			return u
		}
		u.Sample = client.NewMetricSample(client.ClusterMetrics{}, m.Timestamp.Time, client.PodMetricsSource, now)
		u.Window = m.Window.Duration
		if m.UID == "" {
			u.Sample.Reason = "Metrics has no Pod UID; association uses namespace/name and observation time"
		}
		if m.UID != "" && m.UID != pod.UID {
			u.Sample.State, u.Sample.Reason = client.MetricsUnavailable, "Metrics identity differs from the observed Pod"
			return u
		}
		if !pod.CreationTimestamp.IsZero() && m.Timestamp.Before(&pod.CreationTimestamp) {
			u.Sample.State, u.Sample.Reason = client.MetricsStale, "Metric observation predates this Pod"
		}
		if !pod.CreationTimestamp.IsZero() && m.Window.Duration > 0 && m.Timestamp.Add(-m.Window.Duration).Before(pod.CreationTimestamp.Time) {
			u.Sample.State, u.Sample.Reason = client.MetricsStale, "Metric window includes time before this Pod existed"
		}
		if m.Timestamp.Time.After(now.Add(time.Minute)) {
			u.Sample.State, u.Sample.Reason = client.MetricsUnavailable, "Metric observation time is in the future"
		}
		if m.Window.Duration < 0 {
			u.Sample.State, u.Sample.Reason = client.MetricsUnavailable, "Metric window is negative"
		}
		for _, container := range m.Containers {
			if _, duplicate := u.Containers[container.Name]; duplicate {
				u.Sample.State, u.Sample.Reason = client.MetricsUnavailable, "Duplicate container metrics"
				return u
			}
			u.Containers[container.Name] = container.Usage
		}
		u.Complete = len(m.Containers) > 0
		for index := range pod.Spec.Containers {
			container := &pod.Spec.Containers[index]
			if _, ok := u.Containers[container.Name]; !ok {
				u.Complete = false
			}
		}
		for index := range pod.Spec.InitContainers {
			container := &pod.Spec.InitContainers[index]
			if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
				if _, ok := u.Containers[container.Name]; !ok {
					u.Complete = false
				}
			}
		}
		for _, usage := range u.Containers {
			for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				if q, ok := usage[name]; !ok || q.Sign() < 0 {
					u.Complete = false
				}
			}
			Add(u.Values, usage)
		}
		if !u.Complete {
			u.Sample.Reason = "Incomplete container usage; observed values are a subset"
		}
		return u
	}
	return u
}

// A scale target is a configured reference, not a verified ownership claim.
// Omitted API versions remain visible as unreported; reported groups must match.
func targetMatches(scope *Scope, version, kind, name string) bool {
	if scope.Selector == "" {
		return true
	}
	if name != scope.Identity.Name || kind != scope.Kind {
		return false
	}
	if version == "" {
		return true
	}
	gv, err := schema.ParseGroupVersion(version)
	if err != nil {
		return false
	}
	parts := strings.Split(scope.Identity.GVR, "/")
	group := ""
	if len(parts) == 3 {
		group = parts[0]
	}
	return gv.Group == group
}

func (s *Snapshot) checkAssignedNodeCoverage() {
	if state := s.Source(SourceNodes).State; state != Complete && state != Empty && state != Partial {
		return
	}
	visible := make(map[string]bool, len(s.Nodes))
	for i := range s.Nodes {
		visible[s.Nodes[i].Identity.Name] = true
	}
	missing := make(map[string]bool)
	for i := range s.Pods {
		if name := s.Pods[i].Node; name != "" && !visible[name] {
			missing[name] = true
		}
	}
	if len(missing) == 0 {
		return
	}
	for i := range s.Coverage {
		if s.Coverage[i].Source == SourceNodes {
			s.Coverage[i].State = Partial
			s.Coverage[i].Detail += fmt.Sprintf(" %d assigned Pod node(s) are not in the visible node page; allocatable evidence is incomplete.", len(missing))
		}
	}
}
