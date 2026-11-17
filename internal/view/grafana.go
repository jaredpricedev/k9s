// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Grafana hand-off: the dashboard declared for the selected kind, with its
// variables filled from the selection and the time range set to the
// incident. Building a link contacts nothing; only :dashboards and the
// timeline read from Grafana, once, on request.

const (
	dashboardsCommandToken = "dashboards"
	grafanaKey             = ui.KeyShiftD
	grafanaKeyLabel        = "Open in Grafana"

	// grafanaLead is how far before a known fault or rollout a link begins.
	grafanaLead = 10 * time.Minute
	// grafanaDefaultSpan is the range when the object's status gives no time.
	grafanaDefaultSpan = time.Hour
	// grafanaNearRollout is how close before a fault a rollout must be for
	// the range to start at the rollout instead of the fault.
	grafanaNearRollout = time.Hour
)

// grafanaWindow is the time range of a link and the reason it was chosen.
type grafanaWindow struct {
	From, To time.Time
	// Live ends the range at Grafana's "now" instead of a fixed time.
	Live   bool
	Reason string
}

func (w grafanaWindow) linkRange() observability.LinkRange {
	return observability.LinkRange{From: w.From, To: w.To, Live: w.Live}
}

// String states the range in UTC and why it was chosen.
func (w grafanaWindow) String() string {
	if w.From.IsZero() {
		return "dashboard default"
	}
	end := "now"
	if !w.Live {
		end = grafanaClock(w.To, w.From)
	}
	text := grafanaClock(w.From, w.To) + " to " + end + " UTC"
	if w.Reason != "" {
		text += " · " + w.Reason
	}
	return text
}

// grafanaClock renders a UTC time of day, with the date when other is on a
// different day so a multi-day range cannot be misread.
func grafanaClock(t, other time.Time) string {
	t = t.UTC()
	if other.IsZero() || (t.Year() == other.UTC().Year() && t.YearDay() == other.UTC().YearDay()) {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 2 15:04")
}

// defaultGrafanaWindow is the last hour, stated with the reason no better
// range is known.
func defaultGrafanaWindow(now time.Time, why string) grafanaWindow {
	return grafanaWindow{From: now.Add(-grafanaDefaultSpan), To: now, Live: true, Reason: "last hour: " + why}
}

// anchoredWindow starts shortly before a moment the object's own status
// reports. A moment in the future or more than seven days old is not used.
func anchoredWindow(now, anchor time.Time, what string) grafanaWindow {
	if anchor.IsZero() || anchor.After(now) {
		return defaultGrafanaWindow(now, "no usable time in the object's status")
	}
	if now.Sub(anchor)+grafanaLead > observability.MaxWindow {
		return grafanaWindow{From: now.Add(-observability.MaxWindow), To: now, Live: true,
			Reason: "last 7d: " + what + " is older than that"}
	}
	return grafanaWindow{From: anchor.Add(-grafanaLead), To: now, Live: true,
		Reason: fmt.Sprintf("%s before %s %s", grafanaSpan(grafanaLead), what, grafanaClock(anchor, now))}
}

func grafanaSpan(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func earliest(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if !t.IsZero() && (out.IsZero() || t.Before(out)) {
			out = t
		}
	}
	return out
}

// podWindow reads the fault start from a Pod's own status. Pod status keeps
// only the latest exit of each container, so for a pod that restarted more
// than once the range starts at the pod's start: every restart is after it.
func podWindow(pod *corev1.Pod, now time.Time) grafanaWindow {
	var restarts int32
	var lastExit, notReady time.Time
	statuses := append(append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
	for i := range statuses {
		restarts += statuses[i].RestartCount
		if t := statuses[i].LastTerminationState.Terminated; t != nil {
			lastExit = earliest(lastExit, t.FinishedAt.Time)
		}
		if t := statuses[i].State.Terminated; t != nil && t.ExitCode != 0 {
			lastExit = earliest(lastExit, t.FinishedAt.Time)
		}
	}
	if pod.Status.Phase != corev1.PodSucceeded {
		for i := range pod.Status.Conditions {
			c := &pod.Status.Conditions[i]
			if c.Status == corev1.ConditionFalse {
				notReady = earliest(notReady, c.LastTransitionTime.Time)
			}
		}
	}
	if restarts > 1 && pod.Status.StartTime != nil && now.Sub(pod.Status.StartTime.Time) <= 24*time.Hour {
		return anchoredWindow(now, pod.Status.StartTime.Time, fmt.Sprintf("pod start (%d restarts since)", restarts))
	}
	switch {
	case !lastExit.IsZero() && notReady.IsZero() && now.Sub(lastExit) > 24*time.Hour:
		// No condition is False and the only exit the status retains is
		// old: that is history, not an open fault, so it sets no range.
		return defaultGrafanaWindow(now, "no pod condition is False; the last container exit was "+grafanaSpan(now.Sub(lastExit))+" ago")
	case !lastExit.IsZero() && (notReady.IsZero() || !notReady.Before(lastExit)):
		return anchoredWindow(now, lastExit, "the last container exit")
	case !notReady.IsZero():
		return anchoredWindow(now, notReady, "a pod condition turning False")
	}
	return defaultGrafanaWindow(now, "pod status reports no fault")
}

// deploymentWindow starts at the fault the Deployment reports, or at its
// last rollout progress when that came shortly before the fault or when no
// fault is reported.
func deploymentWindow(d *appsv1.Deployment, now time.Time) grafanaWindow {
	var fault, rollout time.Time
	for i := range d.Status.Conditions {
		c := &d.Status.Conditions[i]
		switch {
		case c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionFalse,
			c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue,
			c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse:
			fault = earliest(fault, c.LastTransitionTime.Time)
		case c.Type == appsv1.DeploymentProgressing:
			rollout = c.LastUpdateTime.Time
		}
	}
	switch {
	case !fault.IsZero() && !rollout.IsZero() && rollout.Before(fault) && fault.Sub(rollout) <= grafanaNearRollout:
		return anchoredWindow(now, rollout, "the last rollout progress")
	case !fault.IsZero():
		return anchoredWindow(now, fault, "a failing Deployment condition")
	case !rollout.IsZero() && now.Sub(rollout) <= 24*time.Hour:
		return anchoredWindow(now, rollout, "the last rollout progress")
	}
	return defaultGrafanaWindow(now, "Deployment status reports no fault or recent rollout")
}

// nodeWindow starts when the Node stopped being Ready or came under pressure.
func nodeWindow(n *corev1.Node, now time.Time) grafanaWindow {
	var fault time.Time
	failing := false
	for i := range n.Status.Conditions {
		c := &n.Status.Conditions[i]
		bad := c.Status == corev1.ConditionTrue
		if c.Type == corev1.NodeReady {
			bad = c.Status != corev1.ConditionTrue
		}
		if bad {
			failing = true
			fault = earliest(fault, c.LastTransitionTime.Time)
		}
	}
	switch {
	case fault.IsZero() && failing:
		return defaultGrafanaWindow(now, "the failing Node condition carries no time")
	case fault.IsZero():
		return defaultGrafanaWindow(now, "Node conditions report no fault")
	}
	return anchoredWindow(now, fault, "a failing Node condition")
}

// jobWindow starts at the Job's own start when that run is failing, because
// the whole run is the incident; otherwise at the moment its Failed
// condition reports.
func jobWindow(j *batchv1.Job, now time.Time) grafanaWindow {
	var failed time.Time
	failing := j.Status.Failed > 0
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		if (c.Type == batchv1.JobFailed || c.Type == batchv1.JobFailureTarget) && c.Status == corev1.ConditionTrue {
			failing = true
			failed = earliest(failed, c.LastTransitionTime.Time)
		}
	}
	if !failing {
		return defaultGrafanaWindow(now, "Job status reports no failure")
	}
	if start := j.Status.StartTime; start != nil && !start.IsZero() && !start.After(now) && now.Sub(start.Time) <= 24*time.Hour &&
		(failed.IsZero() || !failed.Before(start.Time)) {
		return anchoredWindow(now, start.Time, "the failing Job's start")
	}
	if failed.IsZero() {
		return defaultGrafanaWindow(now, "the failing Job's status carries no usable time")
	}
	return anchoredWindow(now, failed, "the Job's Failed condition")
}

// grafanaTarget is what a hand-off is about: a dashboard kind, the names
// that fill its variables, and the time range.
type grafanaTarget struct {
	Kind     string // observability.DashboardPod and friends; "" for no selection
	Label    string
	Resource string // lower-case API resource, e.g. deployments
	Link     observability.LinkTarget
	Window   grafanaWindow
	Note     string
}

var grafanaWorkloads = map[string]string{
	"deployments": "Deployment", "statefulsets": "StatefulSet", "daemonsets": "DaemonSet",
	"replicasets": "ReplicaSet", "jobs": "Job", "cronjobs": "CronJob",
}

// grafanaKeyResource reports whether a resource list offers the Grafana key.
func grafanaKeyResource(gvr *client.GVR) bool {
	if gvr == nil {
		return false
	}
	resource, group := gvr.RG()
	switch group {
	case "":
		return resource == "pods" || resource == "nodes" || resource == "namespaces"
	case "apps", "batch":
		return grafanaWorkloads[resource] != ""
	}
	return false
}

// grafanaTargetFor maps a selection to a dashboard kind without any read.
// The time range is refined separately from the list cache.
//
//nolint:gocritic // The immutable selection is captured by value.
func grafanaTargetFor(target SelectedResourceTarget, activeNamespace string, now time.Time) (*grafanaTarget, error) {
	fallback := func() (*grafanaTarget, error) {
		if activeNamespace == "" || client.IsAllNamespaces(activeNamespace) || client.IsClusterScoped(activeNamespace) {
			return nil, errors.New("Select a Pod, workload, Node or namespace first")
		}
		return &grafanaTarget{Kind: observability.DashboardNamespace, Label: "namespace " + activeNamespace, Resource: "namespaces",
			Link:   observability.LinkTarget{Namespace: activeNamespace},
			Window: defaultGrafanaWindow(now, "a namespace carries no fault time")}, nil
	}
	if target.Err() != nil {
		return fallback()
	}
	resource, group := target.GVR.RG()
	core := group == ""
	out := &grafanaTarget{Resource: resource, Window: defaultGrafanaWindow(now, "the object is not in the list cache")}
	switch {
	case core && resource == "pods":
		out.Kind, out.Label = observability.DashboardPod, "Pod "+target.Path()
		out.Link = observability.LinkTarget{Namespace: target.Namespace, Pod: target.Name}
	case core && resource == "nodes":
		out.Kind, out.Label = observability.DashboardNode, "Node "+target.Name
		out.Link = observability.LinkTarget{Node: target.Name}
	case core && resource == "namespaces":
		if client.IsAllNamespaces(target.Name) {
			return nil, errors.New("The all-namespaces row is not a namespace; select one namespace")
		}
		out.Kind, out.Label = observability.DashboardNamespace, "namespace "+target.Name
		out.Link = observability.LinkTarget{Namespace: target.Name}
		out.Window = defaultGrafanaWindow(now, "a namespace carries no fault time")
	case grafanaWorkloads[resource] != "" && (group == "apps" || group == "batch") && target.Namespace != "":
		out.Kind, out.Label = observability.DashboardWorkload, grafanaWorkloads[resource]+" "+target.Path()
		out.Link = observability.LinkTarget{Namespace: target.Namespace, Workload: target.Name}
	case target.Namespace != "":
		out.Kind, out.Label = observability.DashboardNamespace, "namespace "+target.Namespace
		out.Resource = "namespaces"
		out.Link = observability.LinkTarget{Namespace: target.Namespace}
		out.Note = resource + " have no dashboard kind; using the namespace of " + target.Name
		out.Window = defaultGrafanaWindow(now, "a namespace carries no fault time")
	default:
		return fallback()
	}
	return out, nil
}

// refineGrafanaTarget sets the time range, and for a Pod its node, from the
// object the list already holds. It is cache-only: nothing is requested.
//
//nolint:gocritic // The immutable selection is captured by value.
func refineGrafanaTarget(app *App, selected SelectedResourceTarget, out *grafanaTarget, now time.Time) {
	if app == nil || app.factory == nil || selected.Err() != nil || out.Resource == "namespaces" {
		return
	}
	var raw map[string]any
	for _, listNamespace := range []string{selected.Namespace, client.BlankNamespace, app.Config.ActiveNamespace()} {
		o, err := app.factory.CachedGet(selected.GVR, listNamespace, selected.Path())
		if err != nil || o == nil {
			continue
		}
		if u, ok := o.(*unstructured.Unstructured); ok {
			raw = u.Object
			break
		}
	}
	if raw == nil {
		return
	}
	applyGrafanaObject(out, raw, now)
}

// applyGrafanaObject derives the range from one object's own status.
func applyGrafanaObject(out *grafanaTarget, raw map[string]any, now time.Time) {
	convert := runtime.DefaultUnstructuredConverter.FromUnstructured
	switch out.Resource {
	case "pods":
		var pod corev1.Pod
		if convert(raw, &pod) == nil {
			out.Window = podWindow(&pod, now)
			out.Link.Node = pod.Spec.NodeName
		}
	case "deployments":
		var d appsv1.Deployment
		if convert(raw, &d) == nil {
			out.Window = deploymentWindow(&d, now)
		}
	case "nodes":
		var n corev1.Node
		if convert(raw, &n) == nil {
			out.Window = nodeWindow(&n, now)
		}
	case "replicasets", "jobs":
		// A dashboard's workload variable names the controller, so a
		// ReplicaSet or Job owned by one links to its owner.
		var meta metav1.PartialObjectMetadata
		if convert(raw, &meta) == nil {
			if owner := metav1.GetControllerOf(&meta); owner != nil && (owner.Kind == "Deployment" || owner.Kind == "CronJob") {
				out.Note = fmt.Sprintf("workload variable is the owning %s %s", owner.Kind, owner.Name)
				out.Link.Workload = owner.Name
			}
		}
		out.Window = defaultGrafanaWindow(now, "this kind's status carries no fault time")
		var job batchv1.Job
		if out.Resource == "jobs" && convert(raw, &job) == nil {
			out.Window = jobWindow(&job, now)
		}
	default:
		out.Window = defaultGrafanaWindow(now, "this kind's status carries no fault time")
	}
}

// grafanaLink is one link offered for a target, or the reason it cannot be
// built. URL never carries a credential: none is an input to building it.
type grafanaLink struct {
	Name        string // Dashboard, Explore metrics, Explore logs
	Detail      string // which dashboard or query
	Fills       string // variables carried, or why none
	URL         string
	Unavailable string
	// Missing is set when no dashboard is declared for the target's kind.
	Missing bool
	// Snippet is the YAML that would declare this dashboard for the kind.
	Snippet string
}

// grafanaUnavailable states why no Grafana link can be built at all, or "".
// Every reason points at :sources, where the declaration is shown.
func grafanaUnavailable(s *observability.Sources) string {
	switch {
	case s == nil || s.Grafana == nil:
		return "Grafana is not declared for this context. :sources shows what is declared and how to add it."
	case s.Grafana.PublicURL == "":
		return "Grafana is declared without publicURL, the address your browser uses, so no link can be built. " +
			"Add publicURL under k9s.observability.grafana; :sources shows the declaration."
	}
	if err := s.Grafana.Validate(); err != nil {
		return "The Grafana declaration cannot be used: " + observability.Clean(err.Error(), 200) + ". :sources shows it."
	}
	return ""
}

func dashboardLink(g *observability.GrafanaSource, kind string, t *grafanaTarget) grafanaLink {
	link := grafanaLink{Name: "Dashboard"}
	d, declared := g.Dashboards[kind]
	if !declared {
		link.Missing = true
		link.Detail = "none declared for " + kind
		link.Unavailable = "No dashboard is declared for " + kind + ". :dashboards lists what Grafana has and shows the YAML that pins one."
		return link
	}
	link.Detail = d.UID + " · declared for " + kind
	filled, empty := d.Filled(t.Link)
	link.Fills = strings.Join(filled, ", ")
	if link.Fills == "" {
		link.Fills = "no variable"
	}
	if len(empty) > 0 {
		link.Fills += " · not filled: " + strings.Join(empty, ", ")
	}
	url, err := g.DashboardURL(kind, t.Link, t.Window.linkRange())
	if err != nil {
		link.Unavailable = observability.Clean(err.Error(), 200)
		return link
	}
	link.URL = url
	return link
}

// exploreMetric is the query an Explore link opens with. Metric names are
// the kube-state-metrics and cAdvisor defaults; nothing checks they exist.
func exploreMetric(t *grafanaTarget) observability.Expr {
	eq, sel := observability.Eq, observability.Select
	switch t.Kind {
	case observability.DashboardPod:
		return sel("container_memory_working_set_bytes", eq("namespace", t.Link.Namespace), eq("pod", t.Link.Pod))
	case observability.DashboardNode:
		return sel("kube_node_status_condition", eq("node", t.Link.Node), eq("status", "true"))
	case observability.DashboardWorkload:
		ns := eq("namespace", t.Link.Namespace)
		switch t.Resource {
		case "deployments":
			return sel("kube_deployment_status_replicas_available", ns, eq("deployment", t.Link.Workload))
		case "statefulsets":
			return sel("kube_statefulset_status_replicas_ready", ns, eq("statefulset", t.Link.Workload))
		case "daemonsets":
			return sel("kube_daemonset_status_number_ready", ns, eq("daemonset", t.Link.Workload))
		}
		return observability.Sum(sel("kube_pod_container_status_restarts_total", ns,
			observability.Re("pod", observability.RegexQuote(t.Link.Workload)+"-.+")), "pod")
	}
	return observability.Sum(sel("kube_pod_container_status_restarts_total", eq("namespace", t.Link.Namespace)), "pod")
}

// exploreLogSelector is the LogQL stream selector for a target. The label
// names namespace and pod are the common Loki defaults.
func exploreLogSelector(t *grafanaTarget) ([]observability.Matcher, string) {
	ns := observability.Eq("namespace", t.Link.Namespace)
	switch t.Kind {
	case observability.DashboardPod:
		return []observability.Matcher{ns, observability.Eq("pod", t.Link.Pod)}, ""
	case observability.DashboardWorkload:
		return []observability.Matcher{ns, observability.Re("pod", observability.RegexQuote(t.Link.Workload)+"-.+")}, ""
	case observability.DashboardNamespace:
		return []observability.Matcher{ns}, ""
	}
	return nil, "A log link is offered for Pods, workloads and namespaces; node log labels differ between installations."
}

func exploreLinks(g *observability.GrafanaSource, t *grafanaTarget) []grafanaLink {
	metrics := grafanaLink{Name: "Explore metrics"}
	expr := exploreMetric(t)
	metrics.Detail = expr.String()
	if url, err := g.ExploreMetricsURL(expr, t.Window.linkRange()); err != nil {
		metrics.Unavailable = "No Prometheus datasource UID is declared. Add datasource under k9s.observability.grafana."
		if !errors.Is(err, observability.ErrNoDatasource) {
			metrics.Unavailable = observability.Clean(err.Error(), 200)
		}
	} else {
		metrics.URL = url
		metrics.Fills = "datasource " + g.Datasource + " · default metric names, edit in Explore"
	}
	logs := grafanaLink{Name: "Explore logs"}
	selector, why := exploreLogSelector(t)
	switch {
	case why != "":
		logs.Unavailable = why
	case g.LokiDatasource == "":
		logs.Unavailable = "No Loki datasource UID is declared. Add lokiDatasource under k9s.observability.grafana."
	default:
		logs.Detail, _ = observability.LogSelector(selector...)
		url, err := g.ExploreLogsURL(selector, t.Window.linkRange())
		if err != nil {
			logs.Unavailable = observability.Clean(err.Error(), 200)
		} else {
			logs.URL = url
			logs.Fills = "datasource " + g.LokiDatasource + " · default label names, edit in Explore"
		}
	}
	return []grafanaLink{metrics, logs}
}

// grafanaLinks are the links of a quick hand-off: the declared dashboard of
// the target's kind, then the two Explore links.
func grafanaLinks(g *observability.GrafanaSource, t *grafanaTarget) []grafanaLink {
	return append([]grafanaLink{dashboardLink(g, t.Kind, t)}, exploreLinks(g, t)...)
}

// grafanaWindowProvider is implemented by a view that has its own time
// window, such as the timeline. A hand-off from it uses that window.
type grafanaWindowProvider interface {
	GrafanaWindow() (grafanaWindow, bool)
}

// grafanaSelector is implemented by a view whose hand-off is about its own
// scope rather than its focused row, such as the timeline.
type grafanaSelector interface {
	GrafanaSelection() SelectedResourceTarget
}

func grafanaSelection(owner actionOwner, contextName string) SelectedResourceTarget {
	if selector, ok := owner.(grafanaSelector); ok {
		target := selector.GrafanaSelection()
		if target.Context != "" && target.Context != contextName {
			target.UnavailableReason = "Context changed; reopen the view"
		}
		return target
	}
	return actionTarget(owner, contextName)
}

// openGrafana is the one-key hand-off for the owner's selection.
func (a *App) openGrafana(owner actionOwner) {
	a.openGrafanaFor(grafanaSelection(owner, a.Config.ActiveContextName()), owner)
}

//nolint:gocritic // The immutable selection is captured by value.
func (a *App) openGrafanaFor(selected SelectedResourceTarget, owner any) {
	sources := declaredSources(a)
	if reason := grafanaUnavailable(sources); reason != "" {
		showGrafanaHandoff(a, &grafanaHandoff{problem: reason})
		return
	}
	now := time.Now()
	target, err := grafanaTargetFor(selected, a.Config.ActiveNamespace(), now)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	refineGrafanaTarget(a, selected, target, now)
	if provider, ok := owner.(grafanaWindowProvider); ok {
		if window, ok := provider.GrafanaWindow(); ok {
			target.Window = window
		}
	}
	links := grafanaLinks(sources.Grafana, target)
	if links[0].Missing {
		// No dashboard for this kind: the chooser makes one bounded search.
		a.openDashboards(target, selected, "", "No "+target.Kind+" dashboard is declared · Enter on one shows the pin YAML")
		return
	}
	showGrafanaHandoff(a, &grafanaHandoff{target: target, origin: selected, links: links, deliverNow: true, offerList: true})
}

func (c *Command) dashboardsCommand(line string) {
	selected := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	var owner any
	if o, ok := c.app.Content.Top().(actionOwner); ok {
		selected, owner = grafanaSelection(o, c.app.Config.ActiveContextName()), o
	}
	query := ""
	if fields := strings.Fields(line); len(fields) > 1 {
		query = strings.Join(fields[1:], " ")
	}
	c.app.openDashboardsFor(selected, owner, query)
}

//nolint:gocritic // The immutable selection is captured by value.
func (a *App) openDashboardsFor(selected SelectedResourceTarget, owner any, query string) {
	if reason := grafanaUnavailable(declaredSources(a)); reason != "" {
		showGrafanaHandoff(a, &grafanaHandoff{problem: reason})
		return
	}
	now := time.Now()
	target, err := grafanaTargetFor(selected, a.Config.ActiveNamespace(), now)
	if err != nil {
		// No selection and no single namespace: list without variables.
		target = &grafanaTarget{Label: "no selection", Window: defaultGrafanaWindow(now, "nothing is selected")}
	} else {
		refineGrafanaTarget(a, selected, target, now)
	}
	if provider, ok := owner.(grafanaWindowProvider); ok {
		if window, ok := provider.GrafanaWindow(); ok {
			target.Window = window
		}
	}
	a.openDashboards(target, selected, query, "")
}

// openDashboards lists dashboards for a target. origin is the selection the
// target was made from; a dashboard drawn from the list keeps it.
//
//nolint:gocritic // The immutable selection is captured by value.
func (a *App) openDashboards(target *grafanaTarget, origin SelectedResourceTarget, query, notice string) {
	v := newDashboardsView(a, target, query, notice)
	v.origin = origin
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

// grafanaActions registers the hand-off and the dashboard list with the
// action palette and help, with the reason either is unavailable.
func grafanaActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	selected := grafanaSelection(owner, app.Config.ActiveContextName())
	open := grafanaUnavailableShort(app, owner)
	declared := open
	if grafanaUnavailable(declaredSources(app)) == "" {
		declared = ""
	}
	result := []ui.ActionDescriptor{{ID: "command.dashboards", Label: "Grafana dashboards for the selection", Category: ui.ActionNavigate,
		Shortcut: ":" + dashboardsCommandToken, Discoverable: true, UnavailableReason: declared,
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openDashboardsFor(selected, owner, ""); return nil }}}
	// A view that binds the key already lists the action with its own
	// availability; elsewhere the palette offers it for the selection.
	if bound, ok := owner.Actions().Get(grafanaKey); !ok || bound.Description != grafanaKeyLabel {
		result = append(result, ui.ActionDescriptor{ID: "resource.grafana", Label: grafanaKeyLabel, Category: ui.ActionNavigate,
			Discoverable: true, UnavailableReason: open,
			Handler: func(*tcell.EventKey) *tcell.EventKey { app.openGrafanaFor(selected, owner); return nil }})
	}
	return result
}

// grafanaUnavailableShort is the one-line reason the hand-off key cannot
// act for the owner's current selection, or "".
func grafanaUnavailableShort(app *App, owner actionOwner) string {
	sources := declaredSources(app)
	switch {
	case sources == nil || sources.Grafana == nil:
		return "Grafana is not declared for this context; see :sources"
	case sources.Grafana.PublicURL == "":
		return "Grafana publicURL is not declared; see :sources"
	case sources.Grafana.Validate() != nil:
		return "The Grafana declaration is invalid; see :sources"
	}
	if _, err := grafanaTargetFor(grafanaSelection(owner, app.Config.ActiveContextName()), app.Config.ActiveNamespace(), time.Now()); err != nil {
		return err.Error()
	}
	return ""
}

// grafanaKeyAction is the key binding a view adds to offer the hand-off.
// The key always answers: when Grafana is not usable it says why.
func grafanaKeyAction(app *App, owner func() actionOwner, idle func() bool) ui.KeyAction {
	action := ui.NewKeyActionWithOpts(grafanaKeyLabel, func(event *tcell.EventKey) *tcell.EventKey {
		if idle != nil && !idle() {
			return event
		}
		app.openGrafana(owner())
		return nil
	}, ui.ActionOpts{Visible: true})
	action.Category = ui.ActionNavigate
	action.Availability = func() string { return grafanaUnavailableShort(app, owner()) }
	return action
}
