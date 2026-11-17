// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package timeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
)

// Read bounds. Every read has its own timeout inside one total deadline, and
// no list is paginated: a truncated list is reported as incomplete.
const (
	ReadTimeout   = 3 * time.Second
	TotalDeadline = 12 * time.Second
	MaxEvents     = 500
	MaxObjects    = 200
	MaxConfigRefs = 20
	MaxNodes      = 10

	sourceTarget      = "Selected resource"
	sourcePods        = "Pods"
	sourceEvents      = "Events"
	sourceReplicaSets = "ReplicaSets"
	sourceDeployments = "Deployments"
	sourceAutoscalers = "Autoscalers"
	sourceConfigMaps  = "ConfigMap metadata"
	sourceSecrets     = "Secret metadata"
	sourceNodes       = "Nodes"
)

// Readers are the two clients a collection uses. Metadata returns object
// metadata only, so ConfigMap and Secret values are never requested.
type Readers struct {
	Objects  dynamic.Interface
	Metadata metadata.Interface
}

func gvr(resource string) schema.GroupVersionResource {
	parts := strings.Split(resource, "/")
	if len(parts) == 2 {
		return schema.GroupVersionResource{Version: parts[0], Resource: parts[1]}
	}
	return schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}
}

// SupportedKind reports whether a selected kind can scope a timeline.
func SupportedKind(kind string) bool {
	switch kind {
	case "Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job":
		return true
	}
	return false
}

type listRead struct {
	items    []unstructured.Unstructured
	coverage Coverage
}

func readState(err error) (state, detail string) {
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return Denied, "access denied; this history is unknown, not empty"
	case apierrors.IsNotFound(err):
		return Unknown, "not found or API not served"
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err):
		return Unknown, "read timed out"
	case errors.Is(err, context.Canceled):
		return Unknown, "read canceled"
	default:
		return Unknown, "read failed: " + oneLine(err.Error())
	}
}

func (r *Readers) list(ctx context.Context, source, resource, namespace string, limit int64, options metav1.ListOptions) listRead {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	options.Limit = limit
	list, err := r.Objects.Resource(gvr(resource)).Namespace(namespace).List(readCtx, options)
	if err != nil {
		state, detail := readState(err)
		return listRead{coverage: Coverage{Source: source, State: state, Detail: detail}}
	}
	read := listRead{items: list.Items, coverage: Coverage{Source: source, State: Complete, Count: len(list.Items),
		Detail: fmt.Sprintf("%d read (limit %d)", len(list.Items), limit)}}
	if list.GetContinue() != "" {
		read.coverage.State = Incomplete
		read.coverage.Detail = fmt.Sprintf("only the first %d were read; more exist", len(list.Items))
	}
	return read
}

func decode[T any](items []unstructured.Unstructured, coverage *Coverage) []T {
	result := make([]T, 0, len(items))
	for index := range items {
		var value T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(items[index].Object, &value); err != nil {
			coverage.State, coverage.Detail = Incomplete, "an object could not be decoded: "+oneLine(err.Error())
			continue
		}
		result = append(result, value)
	}
	return result
}

// Collect performs one bounded observation. It returns an error only when the
// selected resource itself cannot be read or is no longer the same object;
// every other gap is reported in the snapshot's coverage.
func Collect(ctx context.Context, readers *Readers, scope *Scope, now time.Time) (*Snapshot, error) {
	if readers == nil || readers.Objects == nil {
		return nil, errors.New("Kubernetes resource client is unavailable")
	}
	if scope.Namespace == "" {
		return nil, errors.New("a timeline needs one namespace")
	}
	ctx, cancel := context.WithTimeout(ctx, TotalDeadline)
	defer cancel()
	snapshot := &Snapshot{Scope: *scope, CapturedAt: now, Onset: -1}

	var (
		target   *unstructured.Unstructured
		selector labels.Selector
	)
	podOptions := metav1.ListOptions{}
	if scope.Kind != "" {
		if !SupportedKind(scope.Kind) {
			return nil, fmt.Errorf("%s is not a workload a timeline can follow", scope.Kind)
		}
		readCtx, stop := context.WithTimeout(ctx, ReadTimeout)
		object, err := readers.Objects.Resource(gvr(scope.Resource)).Namespace(scope.Namespace).Get(readCtx, scope.Name, metav1.GetOptions{})
		stop()
		if err != nil {
			_, detail := readState(err)
			return nil, fmt.Errorf("%s: %s", scope.Label(), detail)
		}
		if scope.UID != "" && string(object.GetUID()) != scope.UID {
			return nil, fmt.Errorf("identity changed for %s: this resource was replaced, reopen it", scope.Label())
		}
		target = object
		snapshot.Scope.UID = string(object.GetUID())
		snapshot.Coverage = append(snapshot.Coverage, Coverage{Source: sourceTarget, State: Complete, Count: 1, Detail: scope.Label()})
		if scope.Kind == "Pod" {
			podOptions.FieldSelector = "metadata.name=" + scope.Name
		} else {
			raw, _, _ := unstructured.NestedMap(object.Object, "spec", "selector")
			var labelSelector metav1.LabelSelector
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &labelSelector); err != nil {
				return nil, fmt.Errorf("%s has an unreadable pod selector", scope.Label())
			}
			parsed, err := metav1.LabelSelectorAsSelector(&labelSelector)
			if err != nil || parsed.Empty() {
				return nil, fmt.Errorf("%s reports no pod selector; refusing an unrelated namespace-wide read", scope.Label())
			}
			selector = parsed
			podOptions.LabelSelector = parsed.String()
		}
	}

	// First pass: five independent namespace reads. Without a selected
	// resource nothing else proves the namespace exists, and lists in a
	// namespace that does not exist succeed with no items.
	var pass sync.WaitGroup
	var namespaceErr error
	if scope.Kind == "" {
		pass.Add(1)
		go func() {
			defer pass.Done()
			readCtx, stop := context.WithTimeout(ctx, ReadTimeout)
			defer stop()
			_, namespaceErr = readers.Objects.Resource(gvr(resNamespaces)).Get(readCtx, scope.Namespace, metav1.GetOptions{})
		}()
	}
	reads := make([]listRead, 5)
	for index, item := range []struct {
		source, resource string
		limit            int64
		options          metav1.ListOptions
	}{
		{sourcePods, resPods, MaxObjects, podOptions},
		{sourceEvents, resEvents, MaxEvents, metav1.ListOptions{}},
		{sourceReplicaSets, resReplicaSets, MaxObjects, metav1.ListOptions{}},
		{sourceDeployments, resDeployments, MaxObjects, metav1.ListOptions{}},
		{sourceAutoscalers, resHPAs, MaxObjects, metav1.ListOptions{}},
	} {
		pass.Add(1)
		go func() {
			defer pass.Done()
			reads[index] = readers.list(ctx, item.source, item.resource, scope.Namespace, item.limit, item.options)
		}()
	}
	pass.Wait()
	if namespaceErr != nil {
		if apierrors.IsNotFound(namespaceErr) {
			return nil, fmt.Errorf("namespace %s was not found in this context", scope.Namespace)
		}
		_, detail := readState(namespaceErr)
		snapshot.Notes = append(snapshot.Notes, "The namespace object itself could not be read ("+detail+
			"). If the namespace does not exist, every list is empty; an empty timeline does not show that it exists.")
	}
	podRead, eventRead, setRead, deploymentRead, hpaRead := &reads[0], &reads[1], &reads[2], &reads[3], &reads[4]

	pods := decode[corev1.Pod](podRead.items, &podRead.coverage)
	sets := decode[appsv1.ReplicaSet](setRead.items, &setRead.coverage)
	deployments := decode[appsv1.Deployment](deploymentRead.items, &deploymentRead.coverage)
	hpas := decode[autoscalingv2.HorizontalPodAutoscaler](hpaRead.items, &hpaRead.coverage)
	events := decode[corev1.Event](eventRead.items, &eventRead.coverage)

	// Narrow to the selected workload and what it depends on.
	wholeNamespace := scope.Kind == ""
	if !wholeNamespace {
		kept := pods[:0]
		for index := range pods {
			pod := &pods[index]
			if scope.Kind == "Pod" && (pod.Name != scope.Name || string(pod.UID) != snapshot.Scope.UID) {
				continue
			}
			if selector != nil && !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			kept = append(kept, *pod)
		}
		pods = kept
	}
	deploymentUIDs := map[string]bool{}
	if !wholeNamespace {
		ownerSet := ""
		switch scope.Kind {
		case "Deployment":
			deploymentUIDs[snapshot.Scope.UID] = true
		case "ReplicaSet":
			ownerSet = snapshot.Scope.UID
		case "Pod":
			for _, owner := range target.GetOwnerReferences() {
				if owner.Kind == "ReplicaSet" {
					ownerSet = string(owner.UID)
				}
			}
		}
		for index := range sets {
			if ownerSet == "" || string(sets[index].UID) != ownerSet {
				continue
			}
			for _, owner := range sets[index].OwnerReferences {
				if owner.Kind == "Deployment" {
					deploymentUIDs[string(owner.UID)] = true
				}
			}
		}
	}
	inScope := map[string]bool{}
	if !wholeNamespace {
		inScope[scope.Kind+"/"+scope.Name] = true
	}
	for index := range pods {
		inScope["Pod/"+pods[index].Name] = true
	}
	configMaps, secrets := map[string]bool{}, map[string]bool{}
	workloads := map[string]bool{}
	for index := range deployments {
		deployment := &deployments[index]
		if !wholeNamespace && !deploymentUIDs[string(deployment.UID)] {
			continue
		}
		deploymentUIDs[string(deployment.UID)] = true
		inScope["Deployment/"+deployment.Name] = true
		workloads["Deployment/"+deployment.Name] = true
		configRefs(&deployment.Spec.Template.Spec, configMaps, secrets)
		entries, untimed := deploymentConditionEntries(deployment)
		snapshot.Entries = append(snapshot.Entries, entries...)
		snapshot.Untimed = append(snapshot.Untimed, untimed...)
	}
	if !wholeNamespace {
		workloads[scope.Kind+"/"+scope.Name] = true
	}
	history := map[string][]appsv1.ReplicaSet{}
	names := map[string]string{}
	for index := range sets {
		for _, owner := range sets[index].OwnerReferences {
			if owner.Kind == "Deployment" && deploymentUIDs[string(owner.UID)] {
				history[string(owner.UID)] = append(history[string(owner.UID)], sets[index])
				names[string(owner.UID)] = owner.Name
				inScope["ReplicaSet/"+sets[index].Name] = true
			}
		}
	}
	uids := make([]string, 0, len(history))
	for uid := range history {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	for _, uid := range uids {
		snapshot.Entries = append(snapshot.Entries, rolloutEntries(names[uid], history[uid])...)
	}
	for index := range hpas {
		hpa := &hpas[index]
		if !wholeNamespace && !workloads[hpa.Spec.ScaleTargetRef.Kind+"/"+hpa.Spec.ScaleTargetRef.Name] {
			continue
		}
		inScope["HorizontalPodAutoscaler/"+hpa.Name] = true
		entries, untimed := autoscalerEntries(hpa)
		snapshot.Entries = append(snapshot.Entries, entries...)
		snapshot.Untimed = append(snapshot.Untimed, untimed...)
	}
	nodes := map[string]bool{}
	for index := range pods {
		pod := &pods[index]
		entries, untimed := restartEntries(pod)
		snapshot.Entries = append(snapshot.Entries, entries...)
		snapshot.Untimed = append(snapshot.Untimed, untimed...)
		configRefs(&pod.Spec, configMaps, secrets)
		if pod.Spec.NodeName != "" {
			nodes[pod.Spec.NodeName] = true
		}
	}
	for name := range configMaps {
		inScope["ConfigMap/"+name] = true
	}
	for name := range secrets {
		inScope["Secret/"+name] = true
	}
	snapshot.scoped = make(map[string]bool, len(inScope)+len(nodes))
	for key := range inScope {
		snapshot.scoped[key] = true
	}
	for name := range nodes {
		snapshot.scoped["Node/"+name] = true
	}
	if wholeNamespace {
		inScope = nil
	}
	eventEntryList, oldest, eventsRead := eventEntries(events, inScope)
	snapshot.Entries = append(snapshot.Entries, eventEntryList...)
	snapshot.OldestEvent, snapshot.EventsRead = oldest, eventsRead
	if eventRead.coverage.State == Complete {
		eventRead.coverage.Detail = fmt.Sprintf("%d in the namespace, %d in scope (limit %d)", len(events), eventsRead, MaxEvents)
	}
	snapshot.Coverage = append(snapshot.Coverage, podRead.coverage, eventRead.coverage, setRead.coverage, deploymentRead.coverage, hpaRead.coverage)

	// Second pass: metadata of referenced configuration, and the pods' nodes.
	snapshot.readSecondPass(ctx, readers, configMaps, secrets, nodes)
	snapshot.finish()
	return snapshot, nil
}

type secondRead struct {
	source  string
	entries []Entry
	err     error
}

func (s *Snapshot) readSecondPass(ctx context.Context, readers *Readers, configMaps, secrets, nodes map[string]bool) {
	type job struct{ source, kind, name string }
	var jobs []job
	skipped := map[string]int{}
	total := map[string]int{sourceConfigMaps: len(configMaps), sourceSecrets: len(secrets), sourceNodes: len(nodes)}
	for _, group := range []struct {
		source, kind string
		names        map[string]bool
		limit        int
	}{{sourceConfigMaps, "ConfigMap", configMaps, MaxConfigRefs}, {sourceSecrets, "Secret", secrets, MaxConfigRefs}, {sourceNodes, "Node", nodes, MaxNodes}} {
		sorted := make([]string, 0, len(group.names))
		for name := range group.names {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		if len(sorted) > group.limit {
			skipped[group.source] = len(sorted) - group.limit
			sorted = sorted[:group.limit]
		}
		for _, name := range sorted {
			jobs = append(jobs, job{group.source, group.kind, name})
		}
	}
	results := make([]secondRead, len(jobs))
	var pass sync.WaitGroup
	for index, item := range jobs {
		pass.Add(1)
		go func() {
			defer pass.Done()
			readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
			defer cancel()
			result := secondRead{source: item.source}
			if item.kind == "Node" {
				object, err := readers.Objects.Resource(gvr(resNodes)).Get(readCtx, item.name, metav1.GetOptions{})
				if err == nil {
					var node corev1.Node
					if err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &node); err == nil {
						result.entries = nodeEntries(&node)
					}
				}
				result.err = err
			} else if readers.Metadata == nil {
				result.err = errors.New("metadata-only client unavailable; full objects are not requested")
			} else {
				meta, err := readers.Metadata.Resource(gvr(kindResources[item.kind])).Namespace(s.Scope.Namespace).Get(readCtx, item.name, metav1.GetOptions{})
				if err == nil {
					meta.Namespace = s.Scope.Namespace
					result.entries = configEntries(item.kind, &meta.ObjectMeta)
				}
				result.err = err
			}
			results[index] = result
		}()
	}
	pass.Wait()
	for _, source := range []string{sourceConfigMaps, sourceSecrets, sourceNodes} {
		coverage := Coverage{Source: source, State: Complete}
		var failures []string
		for index := range results {
			if results[index].source != source {
				continue
			}
			if err := results[index].err; err != nil {
				state, detail := readState(err)
				if coverage.State == Complete || state == Denied {
					coverage.State = state
				}
				failures = append(failures, jobs[index].name+": "+detail)
				continue
			}
			coverage.Count++
			s.Entries = append(s.Entries, results[index].entries...)
		}
		what := "referenced by the pods and templates in scope"
		if source == sourceNodes {
			what = "running the pods in scope"
		}
		coverage.Detail = fmt.Sprintf("%d of %d %s", coverage.Count, total[source], what)
		if skipped[source] > 0 {
			if coverage.State == Complete {
				coverage.State = Incomplete
			}
			coverage.Detail += fmt.Sprintf("; %d not read (limit reached)", skipped[source])
		}
		if len(failures) > 0 {
			if len(failures) > 3 {
				failures = append(failures[:3], fmt.Sprintf("and %d more", len(failures)-3))
			}
			coverage.Detail += "; " + strings.Join(failures, "; ")
		}
		s.Coverage = append(s.Coverage, coverage)
	}
}
