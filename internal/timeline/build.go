// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package timeline

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	resPods        = "v1/pods"
	resEvents      = "v1/events"
	resNodes       = "v1/nodes"
	resConfigMaps  = "v1/configmaps"
	resSecrets     = "v1/secrets"
	resDeployments = "apps/v1/deployments"
	resReplicaSets = "apps/v1/replicasets"
	resHPAs        = "autoscaling/v2/horizontalpodautoscalers"

	revisionAnnotation = "deployment.kubernetes.io/revision"
	desiredAnnotation  = "deployment.kubernetes.io/desired-replicas"
	maxManagerEntries  = 6
)

var kindResources = map[string]string{
	"Pod": resPods, "Node": resNodes, "ConfigMap": resConfigMaps, "Secret": resSecrets,
	"Deployment": resDeployments, "ReplicaSet": resReplicaSets, "HorizontalPodAutoscaler": resHPAs,
	"StatefulSet": "apps/v1/statefulsets", "DaemonSet": "apps/v1/daemonsets", "Job": "batch/v1/jobs",
	"CronJob": "batch/v1/cronjobs", "Service": "v1/services", "PersistentVolumeClaim": "v1/persistentvolumeclaims",
	"Namespace": "v1/namespaces", "Ingress": "networking.k8s.io/v1/ingresses",
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// eventEntries merges events by type, reason and involved kind. inScope is nil
// for a whole namespace; otherwise it holds "Kind/name" keys.
func eventEntries(events []corev1.Event, inScope map[string]bool) (entries []Entry, oldest time.Time, read int) {
	type group struct {
		entry   Entry
		objects map[string]bool
		latest  time.Time
		message string
	}
	groups := map[string]*group{}
	var order []string
	for index := range events {
		event := &events[index]
		object := event.InvolvedObject
		if inScope != nil && !inScope[object.Kind+"/"+object.Name] {
			continue
		}
		first, last, count := eventTimes(event)
		if first.IsZero() {
			continue
		}
		read++
		if oldest.IsZero() || first.Before(oldest) {
			oldest = first
		}
		key := event.Type + "\x00" + event.Reason + "\x00" + object.Kind
		g, found := groups[key]
		if !found {
			g = &group{objects: map[string]bool{}, entry: Entry{At: first, Source: SourceEvent, Failing: event.Type == corev1.EventTypeWarning,
				Origin: "Event firstTimestamp/lastTimestamp, falling back to eventTime then creationTimestamp"}}
			groups[key] = g
			order = append(order, key)
		}
		g.objects[object.Name] = true
		g.entry.Count += count
		if first.Before(g.entry.At) {
			g.entry.At = first
		}
		if last.After(g.entry.Last) {
			g.entry.Last = last
		}
		if !last.Before(g.latest) {
			g.latest = last
			g.entry.Ref = Ref{Resource: kindResources[object.Kind], Kind: object.Kind, Namespace: object.Namespace, Name: object.Name}
			g.entry.Summary = event.Type + " " + event.Reason
			g.entry.Detail = nil
			g.message = oneLine(event.Message)
			if component := event.Source.Component; component != "" {
				g.entry.Detail = append(g.entry.Detail, "Reported by: "+component)
			} else if event.ReportingController != "" {
				g.entry.Detail = append(g.entry.Detail, "Reported by: "+event.ReportingController)
			}
		}
	}
	for _, key := range order {
		g := groups[key]
		entry := g.entry
		if entry.Count > 1 {
			entry.Summary += fmt.Sprintf(" x%d", entry.Count)
		}
		if len(g.objects) > 1 {
			names := make([]string, 0, len(g.objects))
			for name := range g.objects {
				names = append(names, name)
			}
			sort.Strings(names)
			entry.Summary += fmt.Sprintf(" on %d %ss", len(names), strings.ToLower(entry.Ref.Kind))
			if len(names) > 5 {
				names = append(names[:5], fmt.Sprintf("and %d more", len(names)-5))
			}
			entry.Detail = append(entry.Detail, "Objects: "+strings.Join(names, ", ")+" (Enter opens the latest)")
		}
		if entry.Last.After(entry.At) {
			entry.Detail = append(entry.Detail, fmt.Sprintf("First %s, last %s, %d occurrences", Stamp(entry.At), Stamp(entry.Last), entry.Count))
		} else {
			entry.Last = time.Time{}
		}
		if g.message != "" {
			entry.Summary += ": " + g.message
		}
		entries = append(entries, entry)
	}
	return entries, oldest, read
}

func eventTimes(event *corev1.Event) (first, last time.Time, count int) {
	first, last, count = event.FirstTimestamp.Time, event.LastTimestamp.Time, int(event.Count)
	if event.Series != nil {
		if event.Series.LastObservedTime.After(last) {
			last = event.Series.LastObservedTime.Time
		}
		count = max(count, int(event.Series.Count))
	}
	for _, fallback := range []time.Time{event.EventTime.Time, event.CreationTimestamp.Time} {
		if first.IsZero() {
			first = fallback
		}
	}
	if last.Before(first) {
		last = first
	}
	return first, last, max(count, 1)
}

// rolloutEntries reports each retained revision of one Deployment and what its
// pod template changed against the revision before it.
func rolloutEntries(deployment string, sets []appsv1.ReplicaSet) []Entry {
	sort.SliceStable(sets, func(i, j int) bool {
		a, b := revision(&sets[i]), revision(&sets[j])
		if a != b {
			return a < b
		}
		return sets[i].CreationTimestamp.Before(&sets[j].CreationTimestamp)
	})
	var entries []Entry
	for index := range sets {
		set := &sets[index]
		if set.CreationTimestamp.IsZero() {
			continue
		}
		label := "rev " + set.Annotations[revisionAnnotation]
		if set.Annotations[revisionAnnotation] == "" {
			label = "rev unknown"
		}
		entry := Entry{At: set.CreationTimestamp.Time, Source: SourceRollout, Change: true,
			Origin: "ReplicaSet metadata.creationTimestamp and " + revisionAnnotation,
			Ref:    Ref{Resource: resReplicaSets, Kind: "ReplicaSet", Namespace: set.Namespace, Name: set.Name}}
		if index == 0 {
			entry.Summary = label + " (earliest retained): " + strings.Join(images(&set.Spec.Template), ", ")
			entry.Detail = []string{"Deployment " + deployment + ". Earliest retained revision; there is no earlier revision to compare with."}
		} else {
			previous := &sets[index-1]
			changes := TemplateChanges(&previous.Spec.Template, &set.Spec.Template)
			if a, b := previous.Annotations[desiredAnnotation], set.Annotations[desiredAnnotation]; a != "" && b != "" && a != b {
				changes = append(changes, "desired replicas "+a+" -> "+b+" (ReplicaSet annotation)")
			}
			if len(changes) == 0 {
				changes = []string{"pod template differs only in fields not compared here (volumes, probes, labels, annotations)"}
			}
			entry.Summary = label + ": " + strings.Join(changes, "; ")
			entry.Detail = []string{"Deployment " + deployment + ", compared with revision " + previous.Annotations[revisionAnnotation] + " (" + previous.Name + ").",
				"Now: " + strings.Join(images(&set.Spec.Template), ", "),
				"env: + added, - removed, ~ changed. Values and command lines are never shown."}
		}
		entries = append(entries, entry)
	}
	return entries
}

func revision(set *appsv1.ReplicaSet) int64 {
	value, _ := strconv.ParseInt(set.Annotations[revisionAnnotation], 10, 64)
	return value
}

func images(template *corev1.PodTemplateSpec) []string {
	var result []string
	for index := range template.Spec.Containers {
		result = append(result, containerLabel(template, template.Spec.Containers[index].Name)+"image "+template.Spec.Containers[index].Image)
	}
	return result
}

// containerLabel names a container only when the template has several.
func containerLabel(template *corev1.PodTemplateSpec, name string) string {
	if len(template.Spec.Containers)+len(template.Spec.InitContainers) <= 1 {
		return ""
	}
	return name + " "
}

// TemplateChanges describes what differs between two pod templates: images,
// environment keys, resources, command and containers. Environment values and
// command lines are never printed; they may hold secrets.
func TemplateChanges(before, after *corev1.PodTemplateSpec) []string {
	var changes []string
	old := map[string]*corev1.Container{}
	for _, list := range [][]corev1.Container{before.Spec.InitContainers, before.Spec.Containers} {
		for index := range list {
			old[list[index].Name] = &list[index]
		}
	}
	seen := map[string]bool{}
	for _, list := range [][]corev1.Container{after.Spec.InitContainers, after.Spec.Containers} {
		for index := range list {
			current := &list[index]
			seen[current.Name] = true
			previous, found := old[current.Name]
			if !found {
				changes = append(changes, "container "+current.Name+" added (image "+current.Image+")")
				continue
			}
			changes = append(changes, containerChanges(containerLabel(after, current.Name), previous, current)...)
		}
	}
	var removed []string
	for name := range old {
		if !seen[name] {
			removed = append(removed, "container "+name+" removed")
		}
	}
	sort.Strings(removed)
	return append(changes, removed...)
}

func containerChanges(label string, before, after *corev1.Container) []string {
	var changes []string
	if before.Image != after.Image {
		changes = append(changes, label+"image "+imageChange(before.Image, after.Image))
	}
	if env := envChanges(before, after); env != "" {
		changes = append(changes, label+"env "+env)
	}
	for _, part := range []struct {
		name          string
		before, after corev1.ResourceList
	}{{"requests", before.Resources.Requests, after.Resources.Requests}, {"limits", before.Resources.Limits, after.Resources.Limits}} {
		names := map[string]bool{}
		for name := range part.before {
			names[string(name)] = true
		}
		for name := range part.after {
			names[string(name)] = true
		}
		sorted := make([]string, 0, len(names))
		for name := range names {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			a, hadA := part.before[corev1.ResourceName(name)]
			b, hadB := part.after[corev1.ResourceName(name)]
			if hadA && hadB && a.Cmp(b) == 0 {
				continue
			}
			from, to := "unset", "unset"
			if hadA {
				from = a.String()
			}
			if hadB {
				to = b.String()
			}
			changes = append(changes, fmt.Sprintf("%s%s.%s %s -> %s", label, part.name, name, from, to))
		}
	}
	if !reflect.DeepEqual(before.Command, after.Command) || !reflect.DeepEqual(before.Args, after.Args) {
		changes = append(changes, label+"command/args changed (not shown)")
	}
	return changes
}

func imageChange(before, after string) string {
	split := func(image string) (string, string) {
		if at := strings.LastIndex(image, "@"); at >= 0 {
			return image[:at], image[at:]
		}
		if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
			return image[:colon], image[colon:]
		}
		return image, ""
	}
	repoA, tagA := split(before)
	repoB, tagB := split(after)
	if repoA == repoB && tagA != "" && tagB != "" {
		// The repository is unchanged; the entry detail names the full image.
		return strings.TrimPrefix(tagA, ":") + " -> " + strings.TrimPrefix(tagB, ":")
	}
	return before + " -> " + after
}

func envChanges(before, after *corev1.Container) string {
	old := map[string]corev1.EnvVar{}
	for _, item := range before.Env {
		old[item.Name] = item
	}
	var parts []string
	seen := map[string]bool{}
	for _, item := range after.Env {
		seen[item.Name] = true
		previous, found := old[item.Name]
		switch {
		case !found:
			parts = append(parts, "+"+item.Name)
		case !reflect.DeepEqual(previous, item):
			parts = append(parts, "~"+item.Name)
		}
	}
	var removed []string
	for name := range old {
		if !seen[name] {
			removed = append(removed, "-"+name)
		}
	}
	sort.Strings(removed)
	parts = append(parts, removed...)
	if !reflect.DeepEqual(before.EnvFrom, after.EnvFrom) {
		parts = append(parts, "envFrom sources changed")
	}
	return strings.Join(parts, " ")
}

// restartEntries reports the retained exit of each container. Pod status keeps
// only the most recent termination.
func restartEntries(pod *corev1.Pod) (entries []Entry, untimed []string) {
	ref := Ref{Resource: resPods, Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for index := range statuses {
			status := &statuses[index]
			for _, item := range []struct {
				terminated *corev1.ContainerStateTerminated
				field      string
			}{{status.LastTerminationState.Terminated, "lastState"}, {status.State.Terminated, "state"}} {
				terminated := item.terminated
				if terminated == nil {
					continue
				}
				failing := terminated.ExitCode != 0 || (terminated.Reason != "" && terminated.Reason != "Completed")
				reason := terminated.Reason
				if reason == "" {
					reason = "exited"
				}
				summary := fmt.Sprintf("container %s %s (exit %d)", status.Name, reason, terminated.ExitCode)
				if terminated.Signal != 0 {
					summary += fmt.Sprintf(" signal %d", terminated.Signal)
				}
				if status.RestartCount > 0 {
					summary += fmt.Sprintf(", restart count %d", status.RestartCount)
				}
				if terminated.FinishedAt.IsZero() {
					if failing {
						untimed = append(untimed, ref.Short()+" "+summary+": no finishedAt")
					}
					continue
				}
				entry := Entry{At: terminated.FinishedAt.Time, Source: SourceRestart, Ref: ref, Summary: summary, Failing: failing,
					Origin: "Pod status containerStatuses[]." + item.field + ".terminated.finishedAt"}
				if !terminated.StartedAt.IsZero() {
					entry.Detail = append(entry.Detail, fmt.Sprintf("Ran %s (started %s)", Span(terminated.FinishedAt.Sub(terminated.StartedAt.Time)), Stamp(terminated.StartedAt.Time)))
				}
				if status.RestartCount > 1 && item.field == "lastState" {
					entry.Detail = append(entry.Detail, fmt.Sprintf("Only the most recent exit is retained; %d earlier restarts have no time.", status.RestartCount-1))
				}
				if waiting := status.State.Waiting; waiting != nil && waiting.Reason != "" {
					entry.Detail = append(entry.Detail, "Now waiting: "+waiting.Reason)
				}
				entries = append(entries, entry)
			}
		}
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type != corev1.PodScheduled || condition.Status != corev1.ConditionFalse {
			continue
		}
		summary := "not scheduled: " + condition.Reason
		if condition.LastTransitionTime.IsZero() {
			untimed = append(untimed, ref.Short()+" "+summary+": condition has no lastTransitionTime")
			continue
		}
		entries = append(entries, Entry{At: condition.LastTransitionTime.Time, Source: SourceCondition, Ref: ref, Summary: summary, Failing: true,
			Origin: "Pod status.conditions[PodScheduled].lastTransitionTime", Detail: []string{oneLine(condition.Message)}})
	}
	return entries, untimed
}

// deploymentConditionEntries reports abnormal Deployment conditions only.
func deploymentConditionEntries(deployment *appsv1.Deployment) (entries []Entry, untimed []string) {
	ref := Ref{Resource: resDeployments, Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name}
	for _, condition := range deployment.Status.Conditions {
		abnormal := condition.Status == corev1.ConditionFalse
		if condition.Type == appsv1.DeploymentReplicaFailure {
			abnormal = condition.Status == corev1.ConditionTrue
		}
		if !abnormal {
			continue
		}
		summary := fmt.Sprintf("%s=%s %s", condition.Type, condition.Status, condition.Reason)
		if condition.LastTransitionTime.IsZero() {
			untimed = append(untimed, ref.Short()+" "+summary+": condition has no lastTransitionTime")
			continue
		}
		entries = append(entries, Entry{At: condition.LastTransitionTime.Time, Source: SourceCondition, Ref: ref, Summary: summary, Failing: true,
			Origin: "Deployment status.conditions[].lastTransitionTime", Detail: []string{oneLine(condition.Message)}})
	}
	return entries, untimed
}

// configEntries reports when each field manager last wrote a ConfigMap or
// Secret. It receives object metadata only and never reports values.
func configEntries(kind string, meta *metav1.ObjectMeta) []Entry {
	ref := Ref{Resource: kindResources[kind], Kind: kind, Namespace: meta.Namespace, Name: meta.Name}
	type write struct {
		at                 time.Time
		manager, operation string
	}
	var writes []write
	seen := map[string]bool{}
	for _, field := range meta.ManagedFields {
		if field.Time == nil || field.Time.IsZero() {
			continue
		}
		manager := field.Manager
		if manager == "" {
			manager = "unnamed manager"
		}
		key := manager + "\x00" + string(field.Operation) + "\x00" + field.Time.UTC().String()
		if seen[key] {
			continue
		}
		seen[key] = true
		writes = append(writes, write{field.Time.Time, manager, string(field.Operation)})
	}
	sort.SliceStable(writes, func(i, j int) bool { return writes[i].at.After(writes[j].at) })
	if len(writes) > maxManagerEntries {
		writes = writes[:maxManagerEntries]
	}
	var entries []Entry
	for _, item := range writes {
		verb := "written"
		if !meta.CreationTimestamp.IsZero() && !item.at.After(meta.CreationTimestamp.Time) {
			verb = "created"
		} else if item.operation == string(metav1.ManagedFieldsOperationApply) {
			verb = "applied"
		} else if item.operation == string(metav1.ManagedFieldsOperationUpdate) {
			verb = "updated"
		}
		entries = append(entries, Entry{At: item.at, Source: SourceConfig, Ref: ref, Change: true,
			Summary: fmt.Sprintf("%s %s by %s", kind, verb, item.manager),
			Origin:  "metadata.managedFields[].time and manager",
			Detail: []string{"Last " + item.operation + " by this manager; earlier writes by the same manager are not retained.",
				"Values are not read. Running pods see a mounted change after a delay; environment references need a restart."}})
	}
	if len(entries) == 0 && !meta.CreationTimestamp.IsZero() {
		entries = append(entries, Entry{At: meta.CreationTimestamp.Time, Source: SourceConfig, Ref: ref, Change: true,
			Summary: kind + " created", Origin: "metadata.creationTimestamp",
			Detail: []string{"No managedFields times were returned, so later updates cannot be timed."}})
	}
	return entries
}

// autoscalerEntries reports HPA condition transitions and the last scale time.
func autoscalerEntries(hpa *autoscalingv2.HorizontalPodAutoscaler) (entries []Entry, untimed []string) {
	ref := Ref{Resource: resHPAs, Kind: "HorizontalPodAutoscaler", Namespace: hpa.Namespace, Name: hpa.Name}
	for _, condition := range hpa.Status.Conditions {
		failing := condition.Type != autoscalingv2.ScalingLimited && condition.Status == corev1.ConditionFalse
		summary := fmt.Sprintf("%s=%s %s", condition.Type, condition.Status, condition.Reason)
		if condition.LastTransitionTime.IsZero() {
			if failing {
				untimed = append(untimed, ref.Short()+" "+summary+": condition has no lastTransitionTime")
			}
			continue
		}
		entries = append(entries, Entry{At: condition.LastTransitionTime.Time, Source: SourceAutoscaler, Ref: ref, Summary: summary, Failing: failing,
			Origin: "HorizontalPodAutoscaler status.conditions[].lastTransitionTime", Detail: []string{oneLine(condition.Message)}})
	}
	if last := hpa.Status.LastScaleTime; last != nil && !last.IsZero() {
		entries = append(entries, Entry{At: last.Time, Source: SourceAutoscaler, Ref: ref, Change: true,
			Summary: fmt.Sprintf("last scaled %s/%s: current %d, desired %d", hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name, hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas),
			Origin:  "HorizontalPodAutoscaler status.lastScaleTime",
			Detail:  []string{"Only the most recent scale time is retained; the replica counts are current, not those at that time."}})
	}
	return entries, untimed
}

// nodeEntries reports the last transition of each node condition.
func nodeEntries(node *corev1.Node) []Entry {
	ref := Ref{Resource: resNodes, Kind: "Node", Name: node.Name}
	var entries []Entry
	for _, condition := range node.Status.Conditions {
		if condition.LastTransitionTime.IsZero() {
			continue
		}
		failing := condition.Status == corev1.ConditionTrue
		if condition.Type == corev1.NodeReady {
			failing = condition.Status != corev1.ConditionTrue
		}
		entries = append(entries, Entry{At: condition.LastTransitionTime.Time, Source: SourceNode, Ref: ref, Failing: failing,
			Summary: strings.TrimSpace(fmt.Sprintf("%s=%s %s", condition.Type, condition.Status, condition.Reason)),
			Origin:  "Node status.conditions[].lastTransitionTime", Detail: []string{oneLine(condition.Message)}})
	}
	return entries
}

// configRefs lists the ConfigMaps and Secrets a pod spec references by name.
func configRefs(spec *corev1.PodSpec, configMaps, secrets map[string]bool) {
	for index := range spec.Volumes {
		volume := &spec.Volumes[index]
		if volume.ConfigMap != nil {
			configMaps[volume.ConfigMap.Name] = true
		}
		if volume.Secret != nil {
			secrets[volume.Secret.SecretName] = true
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil {
					configMaps[source.ConfigMap.Name] = true
				}
				if source.Secret != nil {
					secrets[source.Secret.Name] = true
				}
			}
		}
	}
	for _, list := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for index := range list {
			for _, from := range list[index].EnvFrom {
				if from.ConfigMapRef != nil {
					configMaps[from.ConfigMapRef.Name] = true
				}
				if from.SecretRef != nil {
					secrets[from.SecretRef.Name] = true
				}
			}
			for _, env := range list[index].Env {
				if env.ValueFrom == nil {
					continue
				}
				if env.ValueFrom.ConfigMapKeyRef != nil {
					configMaps[env.ValueFrom.ConfigMapKeyRef.Name] = true
				}
				if env.ValueFrom.SecretKeyRef != nil {
					secrets[env.ValueFrom.SecretKeyRef.Name] = true
				}
			}
		}
	}
	for _, pull := range spec.ImagePullSecrets {
		secrets[pull.Name] = true
	}
	delete(configMaps, "")
	delete(secrets, "")
}
