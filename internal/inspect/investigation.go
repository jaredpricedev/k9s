// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	investigationPodKind    = "Pod"
	containerRoleApp        = "app"
	containerRoleSidecar    = "sidecar"
	containerRoleInit       = "init"
	containerRoleEphemeral  = "ephemeral"
	containerWaiting        = "waiting"
	containerRunning        = "running"
	containerTerminated     = "terminated"
	observationNotCollected = "not collected"
	readinessConditionNames = " Ready "
)

// Investigation is a bounded projection of source records. It is separate
// from rendered evidence so presentation never turns prose into health data.
type Investigation struct {
	Identity    ResourceIdentity
	Kind        string
	CapturedAt  time.Time
	Phase, Node string
	Conditions  []InvestigationCondition
	Containers  []InvestigationContainer
	Events      []InvestigationEvent
	Coverage    []InvestigationCoverage
	Resources   []ResourceBudget
}

type InvestigationCondition struct {
	Type, Status, Reason, Message string
	Adverse                       bool
	TransitionAt                  time.Time
}

type ContainerTermination struct {
	Reason, Message       string
	ExitCode              *int64
	StartedAt, FinishedAt time.Time
}

type InvestigationContainer struct {
	Pod, UID, Name, Role                string
	Ready                               *bool
	Restarts                            *int64
	State, Reason, Message              string
	CurrentTermination, LastTermination *ContainerTermination
}

type InvestigationEvent struct {
	UID, Type, Reason, Message, Source string
	LastObserved                       time.Time
	Count                              int32
}

// State describes source visibility, never an inferred health verdict.
type InvestigationCoverage struct{ Source, State, Detail string }

type ResourceBudget struct {
	Pod, UID, Container, Role                                                     string
	CPURequest, CPULimit, CPUUsage, CPURequestRatio, CPULimitRatio                string
	MemoryRequest, MemoryLimit, MemoryUsage, MemoryRequestRatio, MemoryLimitRatio string
	MetricsState, MetricsReason, CurrentState                                     string
	ObservedAt                                                                    time.Time
	Window                                                                        time.Duration
}

// NewInvestigation retains status and configured budget facts, never raw
// Secret values or arbitrary object spec. Unknown fields remain unknown.
func NewInvestigation(o *unstructured.Unstructured, contextName, gvr string, at time.Time) *Investigation {
	i := &Investigation{
		Identity: ResourceIdentity{
			Context: clean(contextName), GVR: clean(gvr), Namespace: clean(o.GetNamespace()),
			Name: clean(o.GetName()), UID: clean(string(o.GetUID())),
		},
		Kind: clean(o.GetKind()), CapturedAt: at,
	}
	i.Phase, _, _ = unstructured.NestedString(o.Object, "status", "phase")
	i.Node, _, _ = unstructured.NestedString(o.Object, "spec", "nodeName")
	i.Phase, i.Node = clean(i.Phase), clean(i.Node)
	conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, raw := range conditions[:min(len(conditions), 100)] {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		condition := InvestigationCondition{
			Type: text(c, "type"), Status: text(c, "status"), Reason: text(c, "reason"),
			Message: text(c, "message"), TransitionAt: timestamp(c, "lastTransitionTime"),
		}
		condition.Adverse = adverseCondition(o.GetKind(), o.GroupVersionKind().Group, condition.Type, condition.Status)
		// Successful terminal Pods normally report Ready=False because their
		// containers have exited. Readiness is not an active fault in that phase.
		if o.GetKind() == investigationPodKind && o.GroupVersionKind().Group == "" && i.Phase == string(corev1.PodSucceeded) &&
			(condition.Type == "Ready" || condition.Type == "ContainersReady") {
			condition.Adverse = false
		}
		i.Conditions = append(i.Conditions, condition)
	}
	sort.SliceStable(i.Conditions, func(a, b int) bool { return i.Conditions[a].Adverse && !i.Conditions[b].Adverse })
	if o.GetKind() == investigationPodKind && o.GroupVersionKind().Group == "" {
		i.AddPod(o)
	}
	i.Coverage = append(i.Coverage,
		InvestigationCoverage{Source: "object", State: ObservationComplete, Detail: "Current API object; status is not a continuous history"},
		InvestigationCoverage{Source: "logs", State: observationNotCollected, Detail: "Current and previous logs were not collected"},
		InvestigationCoverage{Source: "metrics", State: observationNotCollected, Detail: "Use pressure for metrics.k8s.io usage and sample visibility"},
	)
	return i
}

// AddPod projects selector-matching Pod evidence without asserting ownership.
func (i *Investigation) AddPod(o *unstructured.Unstructured) {
	for _, group := range []struct{ field, role string }{
		{"containerStatuses", containerRoleApp}, {"initContainerStatuses", containerRoleInit}, {"ephemeralContainerStatuses", containerRoleEphemeral},
	} {
		rows, _, _ := unstructured.NestedSlice(o.Object, "status", group.field)
		for _, raw := range rows[:min(len(rows), 100)] {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			c := InvestigationContainer{Pod: clean(o.GetName()), UID: clean(string(o.GetUID())), Name: text(m, "name"), Role: group.role, State: ObservationUnknown}
			if group.role == containerRoleInit {
				c.Role = podInitRole(o, c.Name)
			}
			if v, ok := m["ready"].(bool); ok {
				c.Ready = &v
			}
			if v, ok := integer(m["restartCount"]); ok {
				c.Restarts = &v
			}
			state, _, _ := unstructured.NestedMap(m, "state")
			for _, key := range []string{containerWaiting, containerRunning, containerTerminated} {
				s, ok := state[key].(map[string]any)
				if !ok {
					continue
				}
				c.State = key
				c.Reason = text(s, "reason")
				c.Message = text(s, "message")
				if key == containerTerminated {
					c.CurrentTermination = termination(s)
				}
				break
			}
			if s, found, _ := unstructured.NestedMap(m, "lastState", containerTerminated); found {
				c.LastTermination = termination(s)
			}
			i.Containers = append(i.Containers, c)
		}
	}
	for _, group := range []struct{ field, role string }{
		{"containers", containerRoleApp}, {"initContainers", containerRoleInit}, {"ephemeralContainers", containerRoleEphemeral},
	} {
		rows, _, _ := unstructured.NestedSlice(o.Object, "spec", group.field)
		for _, raw := range rows[:min(len(rows), 100)] {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			r := ResourceBudget{
				Pod: clean(o.GetName()), UID: clean(string(o.GetUID())), Container: text(m, "name"), Role: group.role,
				CPURequest: budget(m, "requests", "cpu"), CPULimit: budget(m, "limits", "cpu"),
				MemoryRequest: budget(m, "requests", "memory"), MemoryLimit: budget(m, "limits", "memory"),
				CPUUsage: "N/A", MemoryUsage: "N/A", MetricsState: observationNotCollected,
			}
			if group.role == containerRoleInit {
				r.Role = podInitRole(o, r.Container)
			}
			i.Resources = append(i.Resources, r)
		}
	}
}

// AddEvents verifies identity again even when the API applied a UID filter.
func (i *Investigation) AddEvents(events []corev1.Event) {
	for index := range events[:min(len(events), 100)] {
		e := &events[index]
		if string(e.InvolvedObject.UID) != i.Identity.UID {
			continue
		}
		at := e.CreationTimestamp.Time
		if !e.EventTime.IsZero() {
			at = e.EventTime.Time
		}
		if !e.LastTimestamp.IsZero() {
			at = e.LastTimestamp.Time
		}
		if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
			at = e.Series.LastObservedTime.Time
		}
		count := e.Count
		if e.Series != nil {
			count = e.Series.Count
		}
		i.Events = append(i.Events, InvestigationEvent{
			UID: clean(string(e.InvolvedObject.UID)), Type: clean(e.Type), Reason: clean(e.Reason), Message: clean(e.Message),
			Source: "core/v1 events · involvedObject.uid", LastObserved: at, Count: count,
		})
	}
	sort.SliceStable(i.Events, func(a, b int) bool { return i.Events[a].LastObserved.After(i.Events[b].LastObserved) })
}

func adverseCondition(kind, group, name, status string) bool {
	// Condition polarity depends on the API. For example Complete=False on a
	// running Job and MemoryPressure=False on a Node are ordinary states.
	positive, negative := "", ""
	switch {
	case kind == investigationPodKind && group == "":
		positive = " Ready ContainersReady PodScheduled "
	case kind == "Node" && group == "":
		positive = readinessConditionNames
		negative = " MemoryPressure DiskPressure PIDPressure NetworkUnavailable "
	case (kind == "Deployment" || kind == "DaemonSet" || kind == "StatefulSet" || kind == "ReplicaSet") && group == "apps":
		positive = " Available "
		negative = " ReplicaFailure "
	case kind == "Job" && group == "batch":
		negative = " Failed FailureTarget "
	case (kind == "Kustomization" && group == "kustomize.toolkit.fluxcd.io") ||
		(kind == "HelmRelease" && group == "helm.toolkit.fluxcd.io") ||
		((kind == "GitRepository" || kind == "OCIRepository" || kind == "Bucket" || kind == "HelmRepository" || kind == "HelmChart") &&
			group == "source.toolkit.fluxcd.io"):
		positive = readinessConditionNames
		negative = " Stalled "
	case kind == "Certificate" && group == "cert-manager.io":
		positive = readinessConditionNames
		negative = " Failed "
	}
	return status == "False" && strings.Contains(positive, " "+name+" ") || status == "True" && strings.Contains(negative, " "+name+" ")
}

func termination(m map[string]any) *ContainerTermination {
	t := &ContainerTermination{Reason: text(m, "reason"), Message: text(m, "message"), StartedAt: timestamp(m, "startedAt"), FinishedAt: timestamp(m, "finishedAt")}
	if n, ok := integer(m["exitCode"]); ok {
		t.ExitCode = &n
	}
	return t
}
func clean(s string) string                    { return logstream.SafeText(s) }
func text(m map[string]any, key string) string { s, _ := m[key].(string); return clean(s) }
func timestamp(m map[string]any, key string) time.Time {
	at, _ := time.Parse(time.RFC3339, text(m, key))
	return at
}
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}
func budget(m map[string]any, allocation, resource string) string {
	s, found, _ := unstructured.NestedString(m, "resources", allocation, resource)
	if !found {
		return "unset"
	}
	return clean(s)
}

//nolint:gocritic // Value methods evaluate independent retained records without mutation.
func (c InvestigationContainer) CurrentIssue() bool {
	if c.State == containerWaiting {
		return c.Reason != "ContainerCreating" && c.Reason != "PodInitializing"
	}
	if c.State == containerTerminated {
		return c.CurrentTermination == nil || c.CurrentTermination.ExitCode == nil || *c.CurrentTermination.ExitCode != 0
	}
	return (c.Role == containerRoleApp || c.Role == containerRoleSidecar) && c.Ready != nil && !*c.Ready
}

//nolint:gocritic // Value methods evaluate independent retained records without mutation.
func (c InvestigationContainer) CurrentLabel() string {
	if c.Reason != "" {
		return c.Reason
	}
	if c.State == containerRunning && (c.Role == containerRoleApp || c.Role == containerRoleSidecar) && c.Ready != nil && !*c.Ready {
		return "Running / not ready"
	}
	if c.State == ObservationUnknown && (c.Role == containerRoleApp || c.Role == containerRoleSidecar) && c.Ready != nil && !*c.Ready {
		return "Not ready / state unknown"
	}
	return c.State
}

func podInitRole(o *unstructured.Unstructured, name string) string {
	rows, _, _ := unstructured.NestedSlice(o.Object, "spec", "initContainers")
	for _, raw := range rows {
		if m, ok := raw.(map[string]any); ok && text(m, "name") == name && text(m, "restartPolicy") == "Always" {
			return containerRoleSidecar
		}
	}
	return containerRoleInit
}

//nolint:gocritic // Formatting accepts an independent retained termination value.
func (t ContainerTermination) Label() string {
	reason := t.Reason
	if reason == "" {
		reason = containerTerminated
	}
	exit := ObservationUnknown
	if t.ExitCode != nil {
		exit = fmt.Sprint(*t.ExitCode)
	}
	return reason + " · exit " + exit
}
