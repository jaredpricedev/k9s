// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package alerts ties Alertmanager alerts to the Kubernetes objects their
// labels name. It is an observation: one bounded read on request, nothing
// stored, nothing polled. The mapping follows labels only; it states which
// labels it used and never guesses an object from a name pattern.
package alerts

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/observability"
)

// Notification states, as words. Firing means Alertmanager reports the alert
// active; it does not say that a notification was delivered.
const (
	Firing      = "firing"
	Silenced    = "silenced"
	Inhibited   = "inhibited"
	Unprocessed = "unprocessed"
)

// Target is the Kubernetes object an alert's labels name. The zero value
// means the labels name no object.
type Target struct {
	Kind, Namespace, Name string
	// Resource is the API resource in group/version/resource form, as typed
	// commands accept it.
	Resource string
	// From lists the label names the mapping used, in order.
	From []string
}

// Mapped reports whether the labels named an object.
func (t *Target) Mapped() bool { return t.Kind != "" }

// Key identifies the object.
func (t *Target) Key() string { return t.Kind + "/" + t.Namespace + "/" + t.Name }

// Label is "Kind namespace/name", or "Kind name" for a cluster-scoped object.
func (t *Target) Label() string {
	if !t.Mapped() {
		return "not mapped to an object"
	}
	if t.Namespace == "" {
		return t.Kind + " " + t.Name
	}
	return t.Kind + " " + t.Namespace + "/" + t.Name
}

type kind struct {
	name, resource string
	labels         []string
}

// Namespaced kinds in the order they win when an alert carries several: the
// most specific object first. The plain "job" label is the Prometheus scrape
// job, never a Kubernetes Job, so only job_name is read for Jobs.
var namespacedKinds = []kind{
	{"Pod", "v1/pods", []string{"exported_pod", "pod"}},
	{"Deployment", "apps/v1/deployments", []string{"deployment"}},
	{"StatefulSet", "apps/v1/statefulsets", []string{"statefulset"}},
	{"DaemonSet", "apps/v1/daemonsets", []string{"daemonset"}},
	{"Job", "batch/v1/jobs", []string{"job_name"}},
	{"CronJob", "batch/v1/cronjobs", []string{"cronjob"}},
	{"PersistentVolumeClaim", "v1/persistentvolumeclaims", []string{"persistentvolumeclaim"}},
	{"HorizontalPodAutoscaler", "autoscaling/v2/horizontalpodautoscalers", []string{"horizontalpodautoscaler"}},
	{"Service", "v1/services", []string{"exported_service", "service"}},
}

// ownerKinds are the labels that name the workload of a pod on the same alert.
var ownerKinds = namespacedKinds[1:5]

func first(labels map[string]string, names ...string) (value, name string) {
	for _, name := range names {
		if value := labels[name]; value != "" {
			return value, name
		}
	}
	return "", ""
}

// Map resolves the object an alert is about from its labels. A namespaced
// object needs a namespace label; without one only a node can be named.
func Map(labels map[string]string) Target {
	namespace, namespaceLabel := first(labels, "exported_namespace", "namespace")
	if namespace != "" {
		for _, k := range namespacedKinds {
			if name, label := first(labels, k.labels...); name != "" {
				return Target{Kind: k.name, Namespace: namespace, Name: name, Resource: k.resource, From: []string{namespaceLabel, label}}
			}
		}
	}
	if name, label := first(labels, "exported_node", "node"); name != "" {
		return Target{Kind: "Node", Name: name, Resource: "v1/nodes", From: []string{label}}
	}
	if namespace != "" {
		return Target{Kind: "Namespace", Name: namespace, Resource: "v1/namespaces", From: []string{namespaceLabel}}
	}
	return Target{}
}

// labelOwner reads the workload of a pod from the alert's own labels.
func labelOwner(labels map[string]string, namespace string) *Target {
	for _, k := range ownerKinds {
		if name, label := first(labels, k.labels...); name != "" {
			return &Target{Kind: k.name, Namespace: namespace, Name: name, Resource: k.resource, From: []string{label}}
		}
	}
	return nil
}

// Item is one alert with the object it maps to.
type Item struct {
	Alert  observability.Alert
	Target Target
	// Owner is the workload that controls a pod target, when it is known from
	// the alert's labels or from the pod's owner references. Nil is unknown.
	Owner *Target
	// OwnerFrom says how Owner is known: "label deployment" or "owner references".
	OwnerFrom string
	// Missing is true when the pod was looked up and not found at the read.
	Missing bool
}

// NewItem maps one alert. The owner comes from labels only.
func NewItem(alert *observability.Alert) Item {
	item := Item{Alert: *alert, Target: Map(alert.Labels)}
	if item.Target.Kind == "Pod" {
		if owner := labelOwner(alert.Labels, item.Target.Namespace); owner != nil {
			item.Owner, item.OwnerFrom = owner, "label "+owner.From[0]
		}
	}
	return item
}

// Name is the alert name, or a placeholder when the label is absent.
func (i *Item) Name() string {
	if name := i.Alert.Name(); name != "" {
		return name
	}
	return "(no alertname)"
}

// Severity is the severity label as a lower-case word; "unset" when absent.
func (i *Item) Severity() string {
	if severity := strings.ToLower(strings.TrimSpace(i.Alert.Labels["severity"])); severity != "" {
		return severity
	}
	return "unset"
}

// State is the notification state as a word.
func (i *Item) State() string {
	switch {
	case i.Alert.Silenced():
		return Silenced
	case i.Alert.Inhibited():
		return Inhibited
	case i.Alert.State == observability.AlertUnprocessed:
		return Unprocessed
	case i.Alert.State == observability.AlertSuppressed:
		return Silenced
	}
	return Firing
}

// IsFiring reports whether the alert is active and not suppressed.
func (i *Item) IsFiring() bool { return i.State() == Firing }

// Summary is the summary annotation, or the nearest text the alert carries.
func (i *Item) Summary() string {
	text, _ := first(i.Alert.Annotations, "summary", "message", "description")
	return text
}

// Runbook is the runbook link annotation, as text.
func (i *Item) Runbook() string {
	text, _ := first(i.Alert.Annotations, "runbook_url", "runbook")
	return text
}

// SeverityRank orders severities: paging words first, unknown words last
// before the ones that say nothing.
func SeverityRank(severity string) int {
	switch severity {
	case "critical", "page", "emergency", "fatal":
		return 0
	case "error", "high", "major":
		return 1
	case "warning", "warn", "medium":
		return 2
	case "info", "informational", "low", "minor":
		return 4
	case "none", "unset":
		return 5
	}
	return 3
}

// Group is one list row: identical alerts (same name, severity and state)
// about one object, or about several pods of one workload.
type Group struct {
	Name, Severity, State string
	// Target is the object the row is about: the workload when the row folds
	// several of its pods, otherwise the alert's own object.
	Target Target
	Items  []Item
	Since  time.Time // earliest start among the items
}

// Key identifies a row for selection and expansion across re-reads.
func (g *Group) Key() string {
	return g.Name + "|" + g.Severity + "|" + g.State + "|" + g.Target.Key() + "|" + g.unmapped()
}

func (g *Group) unmapped() string {
	if g.Target.Mapped() || len(g.Items) == 0 {
		return ""
	}
	return g.Items[0].Alert.Fingerprint
}

// Folded reports whether the row stands for more than one alert.
func (g *Group) Folded() bool { return len(g.Items) > 1 }

// Section orders the list: firing alerts on an object, firing alerts that
// name no object, then alerts that are not notifying.
func (g *Group) Section() int {
	switch {
	case g.State != Firing:
		return 2
	case !g.Target.Mapped():
		return 1
	}
	return 0
}

// Groups folds items into rows and ranks them. Pods are folded under their
// workload only when that ownership is known; an unmapped alert is never
// folded with another.
func Groups(items []Item) []Group {
	index := make(map[string]int)
	var groups []Group
	for i := range items {
		item := &items[i]
		object := item.Target
		if item.Owner != nil {
			object = *item.Owner
		}
		key := item.Name() + "|" + item.Severity() + "|" + item.State() + "|" + object.Key()
		if !object.Mapped() {
			key += "|#" + strconv.Itoa(i)
		}
		at, ok := index[key]
		if !ok {
			at = len(groups)
			index[key] = at
			groups = append(groups, Group{Name: item.Name(), Severity: item.Severity(), State: item.State(), Since: item.Alert.StartsAt})
		}
		group := &groups[at]
		group.Items = append(group.Items, *item)
		if !item.Alert.StartsAt.IsZero() && (group.Since.IsZero() || item.Alert.StartsAt.Before(group.Since)) {
			group.Since = item.Alert.StartsAt
		}
	}
	for i := range groups {
		group := &groups[i]
		sort.SliceStable(group.Items, func(a, b int) bool { return group.Items[a].Target.Name < group.Items[b].Target.Name })
		// One alert keeps its own object; several pods are named by their workload.
		group.Target = group.Items[0].Target
		if group.Folded() && group.Items[0].Owner != nil {
			group.Target = *group.Items[0].Owner
		}
	}
	sort.SliceStable(groups, func(a, b int) bool {
		x, y := &groups[a], &groups[b]
		switch {
		case x.Section() != y.Section():
			return x.Section() < y.Section()
		case SeverityRank(x.Severity) != SeverityRank(y.Severity):
			return SeverityRank(x.Severity) < SeverityRank(y.Severity)
		case !x.Since.Equal(y.Since):
			return x.Since.After(y.Since) // most recently started first
		case x.Name != y.Name:
			return x.Name < y.Name
		}
		return x.Target.Key() < y.Target.Key()
	})
	return groups
}

// Counts summarizes a read in the words the header uses.
type Counts struct {
	Firing, Silenced, Inhibited, Unprocessed, Unmapped int
	BySeverity                                         map[string]int // firing only
}

// Count tallies items by state. Unmapped counts every state.
func Count(items []Item) Counts {
	counts := Counts{BySeverity: make(map[string]int)}
	for i := range items {
		item := &items[i]
		switch item.State() {
		case Firing:
			counts.Firing++
			counts.BySeverity[item.Severity()]++
		case Silenced:
			counts.Silenced++
		case Inhibited:
			counts.Inhibited++
		default:
			counts.Unprocessed++
		}
		if !item.Target.Mapped() {
			counts.Unmapped++
		}
	}
	return counts
}
