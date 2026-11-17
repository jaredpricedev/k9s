// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var grafanaNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func studyGrafana() *observability.GrafanaSource {
	return &observability.GrafanaSource{Source: observability.Source{Service: "monitoring/grafana:3000"},
		PublicURL: "https://grafana.example.test", Datasource: "prom-main", LokiDatasource: "loki-main",
		Dashboards: map[string]observability.Dashboard{
			"pod":       {UID: "k8s-pod", NamespaceVar: "namespace", PodVar: "pod"},
			"workload":  {UID: "k8s-workload", NamespaceVar: "namespace", WorkloadVar: "workload"},
			"node":      {UID: "k8s-node", NodeVar: "instance"},
			"namespace": {UID: "k8s-namespace", NamespaceVar: "namespace"},
		}}
}

// grafanaApp returns an app whose context declares Grafana as given; nil
// leaves it undeclared.
func grafanaApp(t *testing.T, g *observability.GrafanaSource) *App {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	ct, err := app.Config.CurrentContext()
	require.NoError(t, err)
	ct.Observability = observability.Sources{Grafana: g}
	return app
}

// fakeDelivery records what a hand-off would do without starting a program
// or touching a clipboard.
type fakeDelivery struct {
	*linkDelivery
	env                map[string]string
	started, copied    []string
	missing            map[string]bool
	startErr           error
	nativeErr, oscErr  error
	nativeN, oscN      int
	lookedUp, startArg []string
}

func newFakeDelivery(goos string, env map[string]string) *fakeDelivery {
	f := &fakeDelivery{env: env, missing: map[string]bool{}}
	f.linkDelivery = &linkDelivery{
		goos:   goos,
		getenv: func(key string) string { return f.env[key] },
		lookPath: func(name string) (string, error) {
			f.lookedUp = append(f.lookedUp, name)
			if f.missing[name] {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		start: func(name string, args ...string) error {
			f.started = append(f.started, name)
			f.startArg = append([]string(nil), args...)
			return f.startErr
		},
		copyNative: func(text string) error {
			f.nativeN++
			if f.nativeErr == nil {
				f.copied = append(f.copied, text)
			}
			return f.nativeErr
		},
		copyOSC52: func(text string) error {
			f.oscN++
			if f.oscErr == nil {
				f.copied = append(f.copied, text)
			}
			return f.oscErr
		},
	}
	return f
}

func useDelivery(t *testing.T, f *fakeDelivery) {
	t.Helper()
	previous := grafanaDelivery
	grafanaDelivery = f.linkDelivery
	t.Cleanup(func() { grafanaDelivery = previous })
}

func TestDeliveryOpensABrowserOnlyWhenItIsYoursOtherwiseCopies(t *testing.T) {
	t.Setenv(clipboardModeEnv, "")
	const link = "https://grafana.example.test/d/k8s-pod?var-pod=a%20b"
	display := map[string]string{"DISPLAY": ":0"}
	denied := errors.New("no clipboard")
	for _, tc := range []struct {
		name       string
		goos       string
		env        map[string]string
		restricted string
		setup      func(*fakeDelivery)
		opened     bool
		copied     bool
		started    string
		args       []string
		native     int
		osc        int
		says       []string
	}{
		{name: "desktop linux", goos: "linux", env: display, opened: true, started: "xdg-open", args: []string{link},
			says: []string{"Asked the system to open it in your browser (xdg-open)."}},
		{name: "wayland", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, opened: true, started: "xdg-open", args: []string{link}},
		{name: "macOS", goos: "darwin", env: map[string]string{}, opened: true, started: "open", args: []string{link}},
		{name: "windows", goos: "windows", env: map[string]string{}, opened: true, started: "rundll32", args: []string{"url.dll,FileProtocolHandler", link}},
		{name: "read-only mode", goos: "linux", env: display, restricted: "read-only mode starts no local program", copied: true, native: 1,
			says: []string{"Copied to the clipboard.", "Browser not launched: read-only mode starts no local program."}},
		{name: "ssh session", goos: "linux", env: map[string]string{"DISPLAY": "localhost:10.0", "SSH_CONNECTION": "10.0.0.1 1 10.0.0.2 22"}, copied: true, native: 1,
			says: []string{"Copied to the clipboard.", "Browser not launched: this is an SSH session, so a browser here would not be on your screen."}},
		{name: "ssh tty on macOS", goos: "darwin", env: map[string]string{"SSH_TTY": "/dev/ttys001"}, copied: true, native: 1},
		{name: "no display", goos: "linux", env: map[string]string{}, copied: true, native: 1,
			says: []string{"Browser not launched: no display (DISPLAY and WAYLAND_DISPLAY are unset)."}},
		{name: "no opener installed", goos: "linux", env: display, setup: func(f *fakeDelivery) { f.missing["xdg-open"] = true }, copied: true, native: 1,
			says: []string{"Browser not launched: xdg-open is not installed."}},
		{name: "turned off", goos: "darwin", env: map[string]string{browserEnv: "off"}, copied: true, native: 1,
			says: []string{"Browser not launched: K9PLUS_BROWSER turns the browser off."}},
		{name: "opener fails to start", goos: "linux", env: display, setup: func(f *fakeDelivery) { f.startErr = errors.New("exec failed") },
			started: "xdg-open", args: []string{link}, copied: true, native: 1,
			says: []string{"Copied to the clipboard.", "Browser not launched: xdg-open could not be started."}},
		{name: "no native clipboard falls to OSC 52", goos: "linux", env: map[string]string{}, setup: func(f *fakeDelivery) { f.nativeErr = denied },
			copied: true, native: 1, osc: 1,
			says: []string{"Sent to your terminal's clipboard (OSC 52); the terminal decides whether to accept it."}},
		{name: "nothing reachable still reports", goos: "linux", env: map[string]string{}, setup: func(f *fakeDelivery) { f.nativeErr, f.oscErr = denied, denied },
			native: 1, osc: 1, says: []string{"Not copied: no clipboard could be reached. The link is below."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDelivery(tc.goos, tc.env)
			if tc.setup != nil {
				tc.setup(f)
			}
			out := f.deliver(link, tc.restricted)
			require.Equal(t, tc.opened, out.Opened)
			require.Equal(t, tc.copied, out.Copied)
			if tc.started == "" {
				require.Empty(t, f.started, "no program may be started")
			} else {
				require.Equal(t, []string{tc.started}, f.started)
				require.Equal(t, tc.args, f.startArg, "the link is one argument, never a shell string")
			}
			require.Equal(t, tc.native, f.nativeN)
			require.Equal(t, tc.osc, f.oscN)
			if tc.opened {
				require.Empty(t, f.copied, "an opened link leaves the clipboard alone")
				require.Len(t, out.Lines, 1)
			} else {
				require.Len(t, out.Lines, 2, "the copy result first, then why no browser")
				require.True(t, strings.HasPrefix(out.Lines[1], "Browser not launched: "), out.Lines[1])
			}
			if tc.copied {
				require.Equal(t, []string{link}, f.copied)
			}
			for _, want := range tc.says {
				require.Contains(t, out.Lines, want)
			}
		})
	}

	// The clipboard mode narrows the copy path.
	t.Setenv(clipboardModeEnv, clipboardModeOSC52)
	f := newFakeDelivery("linux", map[string]string{})
	require.True(t, f.deliver(link, "").Copied)
	require.Equal(t, [2]int{0, 1}, [2]int{f.nativeN, f.oscN})
	t.Setenv(clipboardModeEnv, clipboardModeNative)
	f = newFakeDelivery("linux", map[string]string{})
	f.nativeErr = denied
	require.False(t, f.deliver(link, "").Copied)
	require.Equal(t, [2]int{1, 0}, [2]int{f.nativeN, f.oscN})
	t.Setenv(clipboardModeEnv, "")

	// Anything but a plain http(s) link is never handed to a program.
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:SECRET@grafana.example.test/d/x", "https://grafana.example.test/d/x y",
		"-oProxyCommand=x", "", "https://"} {
		f := newFakeDelivery("linux", display)
		opened, line := f.open(bad, "")
		require.False(t, opened, bad)
		require.Empty(t, f.started, bad)
		require.NotContains(t, line, "SECRET")
	}
}

func TestIncidentWindowComesFromTheObjectsOwnStatus(t *testing.T) {
	now := grafanaNow
	at := func(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }

	// A pod that restarted repeatedly: the range starts before the pod did.
	started := at(12 * time.Minute)
	looping := &corev1.Pod{Status: corev1.PodStatus{StartTime: &started, Phase: corev1.PodRunning,
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: at(2 * time.Minute)}},
		ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 6, LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: at(3 * time.Minute)}}}}}}
	w := podWindow(looping, now)
	require.Equal(t, now.Add(-22*time.Minute), w.From)
	require.True(t, w.Live)
	require.Equal(t, "11:38:00 to now UTC · 10m before pod start (6 restarts since) 11:48:00", w.String())

	// One restart: the last exit is all the status retains.
	once := looping.DeepCopy()
	once.Status.ContainerStatuses[0].RestartCount = 1
	w = podWindow(once, now)
	require.Equal(t, now.Add(-13*time.Minute), w.From)
	require.Contains(t, w.Reason, "the last container exit")

	// An old pod that restarted many times: its start is not the incident.
	old := looping.DeepCopy()
	oldStart := at(72 * time.Hour)
	old.Status.StartTime = &oldStart
	require.Equal(t, now.Add(-13*time.Minute), podWindow(old, now).From)

	// Never scheduled: the earliest False condition.
	pending := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, LastTransitionTime: at(40 * time.Minute)}}}}
	w = podWindow(pending, now)
	require.Equal(t, now.Add(-50*time.Minute), w.From)
	require.Contains(t, w.Reason, "a pod condition turning False")

	// Healthy or completed: the last hour, and it says why.
	for _, pod := range []*corev1.Pod{
		{Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: at(time.Hour)}}}},
		{Status: corev1.PodStatus{Phase: corev1.PodSucceeded, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: at(5 * time.Minute)}}}},
	} {
		w = podWindow(pod, now)
		require.Equal(t, now.Add(-time.Hour), w.From)
		require.Equal(t, "last hour: pod status reports no fault", w.Reason)
	}

	// Deployment: the rollout that came shortly before the fault.
	deployment := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, LastTransitionTime: at(10 * time.Minute)},
		{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, LastUpdateTime: at(12 * time.Minute), LastTransitionTime: at(72 * time.Hour)}}}}
	w = deploymentWindow(deployment, now)
	require.Equal(t, now.Add(-22*time.Minute), w.From)
	require.Contains(t, w.Reason, "the last rollout progress")
	// A rollout days before the fault is not the start of it.
	deployment.Status.Conditions[1].LastUpdateTime = at(72 * time.Hour)
	w = deploymentWindow(deployment, now)
	require.Equal(t, now.Add(-20*time.Minute), w.From)
	require.Contains(t, w.Reason, "a failing Deployment condition")
	// Stalled rollout: Progressing=False is the fault.
	stalled := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", LastTransitionTime: at(2 * time.Minute)}}}}
	require.Equal(t, now.Add(-12*time.Minute), deploymentWindow(stalled, now).From)
	// No fault: a recent rollout, else the last hour.
	healthy := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastTransitionTime: at(90 * time.Hour)},
		{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, LastUpdateTime: at(3 * time.Hour)}}}}
	require.Equal(t, now.Add(-3*time.Hour-10*time.Minute), deploymentWindow(healthy, now).From)
	healthy.Status.Conditions[1].LastUpdateTime = at(90 * time.Hour)
	require.Equal(t, "last hour: Deployment status reports no fault or recent rollout", deploymentWindow(healthy, now).Reason)

	// Node: not Ready, or under pressure; Ready=True is not a fault.
	node := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, LastTransitionTime: at(19 * time.Minute)},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse, LastTransitionTime: at(5 * time.Minute)}}}}
	require.Equal(t, now.Add(-29*time.Minute), nodeWindow(node, now).From)
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: at(time.Minute)},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, LastTransitionTime: at(45 * time.Minute)}}
	require.Equal(t, now.Add(-55*time.Minute), nodeWindow(node, now).From)
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	require.Equal(t, "last hour: the failing Node condition carries no time", nodeWindow(node, now).Reason)
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	require.Equal(t, "last hour: Node conditions report no fault", nodeWindow(node, now).Reason)

	// A time in the future is not used; a fault older than the limit is capped and dated.
	require.Equal(t, now.Add(-time.Hour), anchoredWindow(now, now.Add(time.Minute), "x").From)
	capped := anchoredWindow(now, now.Add(-30*24*time.Hour), "a failing Node condition")
	require.Equal(t, now.Add(-observability.MaxWindow), capped.From)
	require.Equal(t, "Nov 10 12:00 to now UTC · last 7d: a failing Node condition is older than that", capped.String())
	multiDay := anchoredWindow(now, now.Add(-26*time.Hour), "the last container exit")
	require.Equal(t, "Nov 16 09:50 to now UTC · 10m before the last container exit Nov 16 10:00", multiDay.String())
	// A fixed window names both ends.
	fixed := grafanaWindow{From: now.Add(-time.Hour), To: now, Reason: "the timeline's window"}
	require.Equal(t, "11:00:00 to 12:00:00 UTC · the timeline's window", fixed.String())
	require.Equal(t, observability.LinkRange{From: fixed.From, To: fixed.To}, fixed.linkRange())
}

func selection(gvr *client.GVR, namespace, name string) SelectedResourceTarget {
	return SelectedResourceTarget{Context: "ct-1-1", GVR: gvr, Namespace: namespace, Name: name}
}

func TestGrafanaTargetMapsTheSelectionToADashboardKind(t *testing.T) {
	now := grafanaNow
	for _, tc := range []struct {
		name   string
		target SelectedResourceTarget
		active string
		kind   string
		label  string
		link   observability.LinkTarget
		note   string
		err    string
	}{
		{name: "pod", target: selection(client.PodGVR, "apps", "api-0"), kind: "pod", label: "Pod apps/api-0", link: observability.LinkTarget{Namespace: "apps", Pod: "api-0"}},
		{name: "deployment", target: selection(client.DpGVR, "apps", "checkout-api"), kind: "workload", label: "Deployment apps/checkout-api",
			link: observability.LinkTarget{Namespace: "apps", Workload: "checkout-api"}},
		{name: "statefulset", target: selection(client.StsGVR, "apps", "db"), kind: "workload", label: "StatefulSet apps/db", link: observability.LinkTarget{Namespace: "apps", Workload: "db"}},
		{name: "daemonset", target: selection(client.DsGVR, "kube-system", "cni"), kind: "workload", label: "DaemonSet kube-system/cni",
			link: observability.LinkTarget{Namespace: "kube-system", Workload: "cni"}},
		{name: "cronjob", target: selection(client.CjGVR, "ops", "nightly"), kind: "workload", label: "CronJob ops/nightly", link: observability.LinkTarget{Namespace: "ops", Workload: "nightly"}},
		{name: "node", target: selection(client.NodeGVR, "", "worker-b"), kind: "node", label: "Node worker-b", link: observability.LinkTarget{Node: "worker-b"}},
		{name: "namespace", target: selection(client.NsGVR, "", "apps"), kind: "namespace", label: "namespace apps", link: observability.LinkTarget{Namespace: "apps"}},
		{name: "all-namespaces row", target: selection(client.NsGVR, "", client.NamespaceAll), err: "not a namespace"},
		{name: "service falls back to its namespace", target: selection(client.SvcGVR, "apps", "checkout-api"), kind: "namespace", label: "namespace apps",
			link: observability.LinkTarget{Namespace: "apps"}, note: "services have no dashboard kind; using the namespace of checkout-api"},
		{name: "custom resource named pods is not a Pod", target: selection(client.NewGVR("example.test/v1/pods"), "apps", "x"), kind: "namespace", label: "namespace apps",
			link: observability.LinkTarget{Namespace: "apps"}, note: "pods have no dashboard kind; using the namespace of x"},
		{name: "cluster-scoped other", target: selection(client.NewGVR("v1/persistentvolumes"), "", "pv-1"), err: "Select a Pod, workload, Node or namespace first"},
		{name: "no selection, one namespace", target: SelectedResourceTarget{UnavailableReason: "no selection"}, active: "apps", kind: "namespace", label: "namespace apps",
			link: observability.LinkTarget{Namespace: "apps"}},
		{name: "no selection, all namespaces", target: SelectedResourceTarget{UnavailableReason: "no selection"}, active: client.NamespaceAll, err: "Select a Pod, workload, Node or namespace first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := grafanaTargetFor(tc.target, tc.active, now)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.kind, got.Kind)
			require.Equal(t, tc.label, got.Label)
			require.Equal(t, tc.link, got.Link)
			require.Equal(t, tc.note, got.Note)
			require.True(t, now.Add(-time.Hour).Equal(got.Window.From), "without the object the range is the last hour")
			require.True(t, strings.HasPrefix(got.Window.Reason, "last hour: "))
		})
	}
	for _, gvr := range []*client.GVR{client.PodGVR, client.DpGVR, client.StsGVR, client.DsGVR, client.RsGVR, client.JobGVR, client.CjGVR, client.NodeGVR, client.NsGVR} {
		require.True(t, grafanaKeyResource(gvr), gvr.String())
	}
	for _, gvr := range []*client.GVR{client.SvcGVR, client.CmGVR, client.NewGVR("example.test/v1/pods"), nil} {
		require.False(t, grafanaKeyResource(gvr))
	}

	// The cached object refines the range, a Pod's node and a ReplicaSet's owner.
	pod, _ := grafanaTargetFor(selection(client.PodGVR, "apps", "api-0"), "", now)
	applyGrafanaObject(pod, map[string]any{"spec": map[string]any{"nodeName": "worker-b"}, "status": map[string]any{"phase": "Running",
		"conditions": []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": now.Add(-5 * time.Minute).Format(time.RFC3339)}}}}, now)
	require.Equal(t, "worker-b", pod.Link.Node)
	require.True(t, now.Add(-15*time.Minute).Equal(pod.Window.From))
	rs, _ := grafanaTargetFor(selection(client.RsGVR, "apps", "checkout-api-7d9f8c6b5d"), "", now)
	controller := true
	applyGrafanaObject(rs, map[string]any{"metadata": map[string]any{"ownerReferences": []any{
		map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "checkout-api", "uid": "u", "controller": controller}}}}, now)
	require.Equal(t, "checkout-api", rs.Link.Workload)
	require.Equal(t, "workload variable is the owning Deployment checkout-api", rs.Note)
	bare, _ := grafanaTargetFor(selection(client.RsGVR, "apps", "standalone"), "", now)
	applyGrafanaObject(bare, map[string]any{"metadata": map[string]any{"name": "standalone"}}, now)
	require.Equal(t, "standalone", bare.Link.Workload)
}

func TestGrafanaLinksForEachKindAndWhatIsMissing(t *testing.T) {
	now := grafanaNow
	g := studyGrafana()
	pod, _ := grafanaTargetFor(selection(client.PodGVR, "apps", "checkout-api-7d9f8c6b5d-x2x9q"), "", now)
	pod.Window = grafanaWindow{From: now.Add(-22 * time.Minute), To: now, Live: true, Reason: "10m before pod start"}
	links := grafanaLinks(g, pod)
	require.Len(t, links, 3)
	require.Equal(t, "Dashboard", links[0].Name)
	require.Equal(t, "k8s-pod · declared for pod", links[0].Detail)
	require.Equal(t, "namespace=apps, pod=checkout-api-7d9f8c6b5d-x2x9q", links[0].Fills)
	require.Equal(t, "https://grafana.example.test/d/k8s-pod?from=1794915480000&to=now&var-namespace=apps&var-pod=checkout-api-7d9f8c6b5d-x2x9q", links[0].URL)
	require.Equal(t, `container_memory_working_set_bytes{namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q"}`, links[1].Detail)
	require.Contains(t, links[1].URL, "https://grafana.example.test/explore?")
	require.Equal(t, `{namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q"}`, links[2].Detail)
	require.Contains(t, links[2].URL, "loki-main")
	for _, link := range links {
		require.NoError(t, safeLink(link.URL))
		require.Empty(t, link.Unavailable)
	}

	for resource, want := range map[*client.GVR]string{
		client.DpGVR:  `kube_deployment_status_replicas_available{namespace="apps",deployment="w"}`,
		client.StsGVR: `kube_statefulset_status_replicas_ready{namespace="apps",statefulset="w"}`,
		client.DsGVR:  `kube_daemonset_status_number_ready{namespace="apps",daemonset="w"}`,
		client.JobGVR: `sum by (pod) (kube_pod_container_status_restarts_total{namespace="apps",pod=~"w-.+"})`,
	} {
		target, _ := grafanaTargetFor(selection(resource, "apps", "w"), "", now)
		require.Equal(t, want, exploreMetric(target).String())
		selector, why := exploreLogSelector(target)
		require.Empty(t, why)
		text, err := observability.LogSelector(selector...)
		require.NoError(t, err)
		require.Equal(t, `{namespace="apps",pod=~"w-.+"}`, text)
	}
	node, _ := grafanaTargetFor(selection(client.NodeGVR, "", "worker-b"), "", now)
	require.Equal(t, `kube_node_status_condition{node="worker-b",status="true"}`, exploreMetric(node).String())
	links = grafanaLinks(g, node)
	require.Equal(t, "instance=worker-b", links[0].Fills)
	require.Empty(t, links[2].URL)
	require.Contains(t, links[2].Unavailable, "node log labels differ")
	ns, _ := grafanaTargetFor(selection(client.NsGVR, "", "apps"), "", now)
	require.Equal(t, `sum by (pod) (kube_pod_container_status_restarts_total{namespace="apps"})`, exploreMetric(ns).String())

	// A name with regex characters cannot widen the workload selector.
	odd, _ := grafanaTargetFor(selection(client.DpGVR, "apps", "a.b"), "", now)
	selector, _ := exploreLogSelector(odd)
	text, _ := observability.LogSelector(selector...)
	require.Equal(t, `{namespace="apps",pod=~"a\\.b-.+"}`, text)

	// Missing pieces are named with the setting that supplies them.
	bare := studyGrafana()
	bare.Datasource, bare.LokiDatasource = "", ""
	delete(bare.Dashboards, "pod")
	links = grafanaLinks(bare, pod)
	require.True(t, links[0].Missing)
	require.Empty(t, links[0].URL)
	require.Contains(t, links[0].Unavailable, "No dashboard is declared for pod")
	require.Contains(t, links[1].Unavailable, "Add datasource under k9s.observability.grafana")
	require.Contains(t, links[2].Unavailable, "Add lokiDatasource under k9s.observability.grafana")
	// A declared dashboard whose variable cannot be filled says which.
	workloadForPod := dashboardLink(g, "workload", pod)
	require.Equal(t, "namespace=apps · not filled: workload", workloadForPod.Fills)

	require.Contains(t, grafanaUnavailable(nil), "Grafana is not declared for this context. :sources")
	require.Contains(t, grafanaUnavailable(&observability.Sources{}), ":sources")
	private := studyGrafana()
	private.PublicURL = ""
	require.Contains(t, grafanaUnavailable(&observability.Sources{Grafana: private}), "declared without publicURL")
	require.Contains(t, grafanaUnavailable(&observability.Sources{Grafana: private}), ":sources")
	broken := studyGrafana()
	broken.PublicURL = "https://user:SECRET@grafana.example.test"
	reason := grafanaUnavailable(&observability.Sources{Grafana: broken})
	require.Contains(t, reason, "cannot be used")
	require.NotContains(t, reason, "SECRET")
	require.Empty(t, grafanaUnavailable(&observability.Sources{Grafana: g}))
}

func handoffText(h *grafanaHandoff, width int) string {
	rows, _ := h.text(width)
	return stripTags(strings.Join(rows, "\n"))
}

// stripTags removes tview color tags so text can be compared.
func stripTags(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			if end := strings.IndexByte(s[i:], ']'); end > 0 && strings.Count(s[i:i+end], ":") == 2 {
				i += end
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func TestHandoffDialogDeliversShowsTheWholeLinkAndSwitchesLinks(t *testing.T) {
	t.Setenv(clipboardModeEnv, "")
	app := grafanaApp(t, studyGrafana())
	fake := newFakeDelivery("linux", map[string]string{"SSH_TTY": "/dev/pts/3"})
	useDelivery(t, fake)
	before := app.GetInputCapture()

	app.openGrafanaFor(selection(client.PodGVR, "apps", "checkout-api-7d9f8c6b5d-x2x9q"), nil)
	require.True(t, app.Content.HasPage(grafanaDialogPage))
	h := app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	require.Empty(t, fake.started)
	require.Len(t, fake.copied, 1, "one key press delivers once")
	link := fake.copied[0]
	require.True(t, strings.HasPrefix(link, "https://grafana.example.test/d/k8s-pod?from="), link)
	require.Contains(t, link, "&to=now&var-namespace=apps&var-pod=checkout-api-7d9f8c6b5d-x2x9q")

	for _, width := range []int{96, 76, 54} {
		rows, keys := h.text(width)
		for _, row := range rows {
			require.LessOrEqual(t, runewidth.StringWidth(stripTags(row)), width, "width %d: %q", width, row)
		}
		text := stripTags(strings.Join(rows, "\n"))
		for _, want := range []string{"Pod apps/checkout-api-7d9f8c6b5d-x2x9q", "Link    Dashboard · k8s-pod", "Fills   namespace=apps", "Window  ", "Result  Copied to the", "SSH session"} {
			require.Contains(t, text, want, "width %d", width)
		}
		// The rows that carry the link join back into exactly the link.
		var joined strings.Builder
		for _, row := range strings.Split(text, "\n") {
			if strings.HasPrefix(row, "https://") || (joined.Len() > 0 && row != "" && !strings.Contains(row, " ")) {
				joined.WriteString(row)
			} else if joined.Len() > 0 {
				break
			}
		}
		require.Equal(t, link, joined.String(), "width %d", width)
		require.LessOrEqual(t, runewidth.StringWidth(keys[1]), 54)
	}
	_, keys := h.text(96)
	require.Equal(t, "o open · c copy · e Explore metrics · l Explore logs · a all dashboards · Esc close", keys[0])
	require.Equal(t, "o open · c copy · e metrics · l logs · a all · Esc", keys[1])
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}} {
		frame := drawnText(t, h, size[0], size[1])
		require.Contains(t, frame, "Open in Grafana")
		require.Contains(t, frame, "https://grafana.example.test/d/k8s-pod?from=")
		require.Contains(t, frame, "o open · c copy")
		require.Contains(t, frame, "Esc")
	}

	// e and l switch to the Explore links and deliver them; d returns.
	press := func(r rune) { require.Nil(t, h.keyboard(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))) }
	press('e')
	require.Len(t, fake.copied, 2)
	require.Contains(t, fake.copied[1], "/explore?")
	require.Contains(t, handoffText(h, 140), `Link    Explore metrics · container_memory_working_set_bytes{namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q"}`)
	press('l')
	require.Len(t, fake.copied, 3)
	require.Contains(t, handoffText(h, 96), `Link    Explore logs · {namespace="apps",pod="checkout-api-7d9f8c6b5d-x2x9q"}`)
	press('d')
	require.Equal(t, link, fake.copied[3])
	// c copies again; o tries the browser and says why not.
	press('c')
	require.Len(t, fake.copied, 5)
	press('o')
	require.Empty(t, fake.started)
	require.Contains(t, handoffText(h, 96), "Browser not launched: this is an SSH session")
	// Keys that belong to the view underneath do not leak through.
	require.Nil(t, h.keyboard(tcell.NewEventKey(tcell.KeyRune, ':', tcell.ModNone)))
	require.Nil(t, h.keyboard(tcell.NewEventKey(tcell.KeyCtrlD, 0, tcell.ModCtrl)))

	require.Nil(t, h.keyboard(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)))
	require.False(t, app.Content.HasPage(grafanaDialogPage))
	require.Equal(t, before == nil, app.GetInputCapture() == nil, "the app's own key handling is restored")

	// Read-only mode starts no program even where a browser exists.
	desktop := newFakeDelivery("linux", map[string]string{"DISPLAY": ":0"})
	useDelivery(t, desktop)
	app.openGrafanaFor(selection(client.DpGVR, "apps", "checkout-api"), nil)
	h = app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	if app.Config.IsReadOnly() {
		require.Empty(t, desktop.started)
		require.Contains(t, handoffText(h, 96), "read-only mode starts no local program")
	} else {
		require.Equal(t, []string{"xdg-open"}, desktop.started)
		require.Empty(t, desktop.copied)
		require.Contains(t, handoffText(h, 96), "Result  Asked the system to open it in your browser (xdg-open).")
	}
	require.Contains(t, handoffText(h, 96), "Fills   namespace=apps, workload=checkout-api")
	h.close()
}

func TestHandoffStatesWhenGrafanaIsNotUsableAndPointsToSources(t *testing.T) {
	fake := newFakeDelivery("linux", map[string]string{"DISPLAY": ":0"})
	useDelivery(t, fake)
	private := studyGrafana()
	private.PublicURL = ""
	for name, tc := range map[string]struct {
		grafana *observability.GrafanaSource
		want    string
	}{
		"not declared": {nil, "Grafana is not declared for this context."},
		"no publicURL": {private, "Grafana is declared without publicURL"},
	} {
		app := grafanaApp(t, tc.grafana)
		for _, open := range []func(){
			func() { app.openGrafanaFor(selection(client.PodGVR, "apps", "api-0"), nil) },
			func() { app.openDashboardsFor(selection(client.PodGVR, "apps", "api-0"), nil, "") },
		} {
			open()
			require.True(t, app.Content.HasPage(grafanaDialogPage), name)
			h := app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
			text := handoffText(h, 54)
			require.Contains(t, strings.Join(strings.Fields(text), " "), tc.want, name)
			require.Contains(t, text, ":sources", name)
			require.Contains(t, text, "Nothing was contacted.", name)
			_, keys := h.text(54)
			require.Equal(t, "s open :sources · Esc close", keys[0])
			frame := drawnText(t, h, 60, 24)
			require.Contains(t, frame, "Grafana unavailable")
			require.Contains(t, frame, "s open :sources · Esc close")
			h.close()
		}
		require.Empty(t, fake.started, name)
		require.Empty(t, fake.copied, name)
	}

	// The palette and help give the same reason.
	app := grafanaApp(t, nil)
	owner := timelineOwner(t, app)
	reasons := map[string]string{}
	for _, item := range actionCatalog(owner, app) {
		if item.Label == grafanaKeyLabel || item.ID == "command.dashboards" {
			reasons[item.Label] = item.UnavailableReason
		}
	}
	require.Equal(t, map[string]string{
		grafanaKeyLabel:                        "Grafana is not declared for this context; see :sources",
		"Grafana dashboards for the selection": "Grafana is not declared for this context; see :sources",
	}, reasons)
}

func timelineOwner(t *testing.T, app *App) *timelineView {
	t.Helper()
	scope := &timeline.Scope{Context: app.Config.ActiveContextName(), Namespace: "apps", Resource: "apps/v1/deployments", Kind: "Deployment", Name: "checkout-api"}
	v := newTimelineView(app, scope, "")
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(timelineSnapshot(scope), nil)
	return v
}

func TestGrafanaIsRegisteredWithTheKeyThePaletteAndTheCommand(t *testing.T) {
	app := grafanaApp(t, studyGrafana())
	owner := timelineOwner(t, app)
	var open, list *ui.ActionDescriptor
	catalog := actionCatalog(owner, app)
	count := 0
	for i := range catalog {
		switch {
		case catalog[i].Label == grafanaKeyLabel:
			open = &catalog[i]
			count++
		case catalog[i].ID == "command.dashboards":
			list = &catalog[i]
		}
	}
	require.Equal(t, 1, count, "the key's action is listed once, not twice")
	require.Equal(t, "Shift-D", open.Shortcut)
	require.Empty(t, open.UnavailableReason)
	require.True(t, open.Available())
	require.Equal(t, ":dashboards", list.Shortcut)
	require.Empty(t, list.UnavailableReason)
	command := NewCommand(app)
	command.updateSuggestionAliases()
	require.Contains(t, command.suggestionAliases(), dashboardsCommandToken)

	// A view without the key still gets the palette entry.
	sources := newSourcesView(app)
	require.NoError(t, sources.Init(t.Context()))
	t.Cleanup(sources.Stop)
	found := false
	for _, item := range actionCatalog(sources, app) {
		if item.ID == "resource.grafana" {
			found = true
			require.Equal(t, grafanaKeyLabel, item.Label)
			require.Empty(t, item.Shortcut, "this view does not bind the key")
		}
	}
	require.True(t, found)

	// From the timeline the hand-off is about the scope, with its window.
	fake := newFakeDelivery("linux", map[string]string{})
	useDelivery(t, fake)
	owner.setWindow(time.Hour)
	app.openGrafana(owner)
	h := app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	require.Equal(t, "https://grafana.example.test/d/k8s-workload?from=1794913200000&to=1794916800000&var-namespace=apps&var-workload=checkout-api", fake.copied[0])
	require.Contains(t, handoffText(h, 96), "Window  11:00:00 to 12:00:00 UTC · the timeline's window, last 1h before capture")
	h.close()
	owner.setWindow(0)
	window, ok := owner.GrafanaWindow()
	require.True(t, ok)
	require.Equal(t, grafanaNow.Add(-144*time.Hour-time.Minute), window.From)
	require.Equal(t, grafanaNow, window.To)
	require.False(t, window.Live)
	namespace := newTimelineView(app, &timeline.Scope{Context: app.Config.ActiveContextName(), Namespace: "apps"}, "")
	selected := namespace.GrafanaSelection()
	require.Equal(t, "namespaces", selected.GVR.R())
	require.Equal(t, "apps", selected.Name)
	_, ok = namespace.GrafanaWindow()
	require.False(t, ok, "no observation yet, so no window to hand over")
}

func studyHits() []observability.DashboardHit {
	return []observability.DashboardHit{
		{UID: "k8s-pod", Title: "Kubernetes / Compute Resources / Pod", FolderTitle: "Kubernetes", Tags: []string{"kubernetes-mixin"}},
		{UID: "etcd", Title: "etcd", FolderTitle: "Control plane"},
		{UID: "k8s-node", Title: "Node Exporter / Nodes", FolderTitle: "Kubernetes"},
		{UID: "checkout-slo", Title: "Checkout / Service SLO", FolderTitle: "Payments", Tags: []string{"checkout", "slo"}},
	}
}

func dashboardsFixture(t *testing.T, g *observability.GrafanaSource, result observability.DashboardsResult) (*dashboardsView, *int) {
	t.Helper()
	app := grafanaApp(t, g)
	target, err := grafanaTargetFor(selection(client.PodGVR, "apps", "checkout-api-7d9f8c6b5d-x2x9q"), "", grafanaNow)
	require.NoError(t, err)
	target.Window = grafanaWindow{From: grafanaNow.Add(-22 * time.Minute), To: grafanaNow, Live: true, Reason: "10m before pod start (6 restarts since) 11:48:00"}
	v := newDashboardsView(app, target, "", "")
	searches := 0
	v.search = func(context.Context) observability.DashboardsResult {
		searches++
		return result
	}
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	return v, &searches
}

func TestDashboardsListsDeclaredThenExploreThenSearchAndOpensOnEnter(t *testing.T) {
	t.Setenv(clipboardModeEnv, "")
	fake := newFakeDelivery("linux", map[string]string{})
	useDelivery(t, fake)
	at := time.Date(2026, 11, 17, 12, 0, 5, 0, time.UTC)
	result := observability.DashboardsResult{Status: observability.Status{Kind: observability.KindGrafana, State: observability.StateComplete, ObservedAt: at},
		Dashboards: studyHits()}
	v, searches := dashboardsFixture(t, studyGrafana(), result)
	require.Zero(t, *searches, "building the view reads nothing")
	require.Contains(t, v.header.GetText(true), "Grafana search NOT READ yet")
	v.accept(&result)

	var order []string
	for i := range v.rows {
		order = append(order, v.rows[i].Origin+" "+v.rows[i].For+" "+v.rows[i].UID)
	}
	require.Equal(t, []string{
		"declared pod k8s-pod", "declared workload k8s-workload", "declared node k8s-node", "declared namespace k8s-namespace",
		"explore metrics ", "explore logs ",
		"search - checkout-slo", "search - etcd",
	}, order, "the selection's kind first; a search hit sharing a word with the selection before one that does not; declared ones not repeated")
	require.Equal(t, "Kubernetes / Compute Resources / Pod", v.rows[0].Title, "a declared dashboard takes its title from the search")
	require.Equal(t, "k8s-workload", v.rows[1].Title, "and shows its UID when the search did not return it")

	header := v.header.GetText(true)
	require.Contains(t, header, "Dashboards / Pod apps/checkout-api-7d9f8c6b5d-x2x9q")
	require.Contains(t, header, "Window 11:38:00 to now UTC · 10m before pod start (6 restarts since) 11:48:00")
	line, _ := v.searchLine()
	require.Equal(t, "Grafana search: 4 dashboards read at 12:00:05Z via service monitoring/grafana:3000", line)
	require.Contains(t, header, "Links open at https://grafana.example.test · nothing is opened until Enter")

	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0])
		}
		for _, want := range []string{"ORIGIN   FOR       DASHBOARD OR QUERY", "> declared pod       Kubernetes / Compute Resources / Pod", "  explore  metrics   container_memory_working_set_bytes",
			"  search   -         Checkout / Service SLO", "Fills namespace=apps, pod=checkout-api-7d9f8c6b5d-x2x9q", "https://grafana.example.test/d/k8s-pod?from=", "Enter open"} {
			require.Contains(t, frame, want, "%dx%d", size[0], size[1])
		}
		require.Equal(t, size[0] >= 100, strings.Contains(frame, " UID"), "the UID column appears only when there is room")
	}
	require.Contains(t, drawnText(t, v, 120, 34), "Payments       checkout-slo")
	require.Contains(t, drawnText(t, v, 80, 24), "Checkout / Service SLO (folder Payments)")

	// Enter on the declared row delivers that link and shows it.
	v.openRow()
	require.Equal(t, []string{"https://grafana.example.test/d/k8s-pod?from=1794915480000&to=now&var-namespace=apps&var-pod=checkout-api-7d9f8c6b5d-x2x9q"}, fake.copied)
	h := v.app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	_, keys := h.text(96)
	require.Equal(t, "o open · c copy · Esc close", keys[0])
	h.close()

	// Enter on a search row: no variable is guessed, and the YAML that pins it is shown.
	v.table.Select(7, 0)
	require.Equal(t, "checkout-slo", v.selectedRow().UID)
	require.Contains(t, strings.Join(strings.Fields(v.detail.GetText(true)), " "), "Not declared: no variable is filled. Enter shows the YAML that pins it as the pod dashboard.")
	v.openRow()
	require.Equal(t, "https://grafana.example.test/d/checkout-slo?from=1794915480000&to=now", fake.copied[1])
	h = v.app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	text := handoffText(h, 96)
	for _, want := range []string{"Link    Dashboard · checkout-slo · Checkout / Service SLO", "Not declared, so no variable is filled.", "To pin it, add this to the context config:",
		"cl-1/ct-1-1/config.yaml", "  k9s:\n    observability:\n      grafana:\n        dashboards:\n          pod: {uid: checkout-slo, namespaceVar: namespace, podVar: pod}",
		"Nothing is written for you."} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "Fills ")
	_, keys = h.text(96)
	require.Equal(t, "o open · c copy · y copy YAML · Esc close", keys[0])
	require.Nil(t, h.keyboard(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone)))
	require.Equal(t, observability.PinSnippet("pod", "checkout-slo")+"\n", fake.copied[2])
	require.Contains(t, handoffText(h, 96), "Result  YAML copied to the clipboard.")
	// A long dialog on a short screen keeps its keys visible and says it scrolls.
	frame := drawnText(t, h, 60, 16)
	require.Contains(t, frame, "Up/Down scroll · o open · c copy · y YAML · Esc")
	h.close()

	// An unavailable Explore row explains itself instead of opening.
	bare := studyGrafana()
	bare.LokiDatasource = ""
	delete(bare.Dashboards, "pod")
	w, _ := dashboardsFixture(t, bare, result)
	w.accept(&result)
	require.Equal(t, "declared workload k8s-workload", w.rows[0].Origin+" "+w.rows[0].For+" "+w.rows[0].UID)
	require.Equal(t, "search - k8s-pod", w.rows[5].Origin+" "+w.rows[5].For+" "+w.rows[5].UID, "an undeclared dashboard is a search result again")
	w.table.Select(5, 0)
	require.Equal(t, "logs", w.selectedRow().For)
	require.Contains(t, strings.Join(strings.Fields(w.detail.GetText(true)), " "), "Explore logs No Loki datasource UID is declared. Add lokiDatasource under")
	copies := len(fake.copied)
	w.openRow()
	require.Len(t, fake.copied, copies, "nothing to deliver")
	h = w.app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	require.Contains(t, handoffText(h, 96), "No Loki datasource UID is declared.")
	h.close()

	// The filter narrows locally, without another read.
	v.applyFilter("slo")
	require.Len(t, v.visible, 1)
	require.Contains(t, v.header.GetText(true), `Filter "slo": 1 of 8 rows · Esc clears`)
	v.applyFilter("nothing-matches")
	require.Contains(t, drawnText(t, v, 80, 24), "No row matches the filter. Esc clears it.")
	v.applyFilter("")
	require.Zero(t, *searches)
}

func TestDashboardsStatesDeniedUnreachableAndIncompleteSearch(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 5, 0, time.UTC)
	for name, tc := range map[string]struct {
		status observability.Status
		want   string
	}{
		"denied":      {observability.Status{State: observability.StateDenied, Reason: "HTTP 403", HTTPStatus: 403, ObservedAt: at}, "NOT READ: search denied (HTTP 403) 12:00:05Z · see :sources"},
		"timeout":     {observability.Status{State: observability.StateTimeout, Reason: "no answer within 8s", ObservedAt: at}, "NOT READ: search timeout (no answer within 8s) 12:00:05Z · see :sources"},
		"unreachable": {observability.Status{State: observability.StateUnavailable, Reason: "connection refused", ObservedAt: at}, "NOT READ: search unavailable (connection refused) 12:00:05Z · see :sources"},
	} {
		result := observability.DashboardsResult{Status: tc.status}
		v, _ := dashboardsFixture(t, studyGrafana(), result)
		v.accept(&result)
		for _, size := range [][2]int{{80, 24}, {60, 24}} {
			frame := drawnText(t, v, size[0], size[1])
			require.Contains(t, frame, ui.Truncate(tc.want, size[0]-1)[:len(ui.Truncate(tc.want, size[0]-1))-3], "%s %dx%d", name, size[0], size[1])
			require.Contains(t, frame, "> declared pod       k8s-pod", "declared dashboards still link when the search cannot be read")
			require.NotContains(t, frame, "search   -")
		}
		line, _ := v.searchLine()
		require.Equal(t, tc.want+" · declared links still work", line, name)
	}

	incomplete := observability.DashboardsResult{Status: observability.Status{State: observability.StateIncomplete, Reason: "dashboard limit 100 reached; more may exist", ObservedAt: at},
		Dashboards: studyHits()}
	v, _ := dashboardsFixture(t, studyGrafana(), incomplete)
	v.query = "kube"
	v.accept(&incomplete)
	line, _ := v.searchLine()
	require.Equal(t, `INCOMPLETE: first 4 dashboards read with title containing "kube" at 12:00:05Z; more exist · :dashboards <title words> narrows`, line)
	require.Len(t, v.rows, 8, "what was read is still listed")

	// A reply that arrives after the view closed, or after the destination
	// changed, is discarded; a re-read keeps the selection.
	v.Start()
	v.Stop()
	generation := v.generation
	v.refresh()
	require.NotEqual(t, generation, v.generation)
	v.revision++
	before := v.result
	v.refresh()
	require.Same(t, before, v.result)
	require.Contains(t, v.header.GetText(true), "Destination changed; this list is retained.")
}

func TestTimelineShowsGrafanaAnnotationsForItsScopeAndStatesGaps(t *testing.T) {
	at := grafanaNow
	notes := []observability.Annotation{
		{ID: 101, Time: at.Add(-26 * time.Hour), TimeEnd: at.Add(-25 * time.Hour), Text: "Load test: checkout-api baseline", Tags: []string{"loadtest", "checkout-api", "namespace:apps"}, DashboardUID: "checkout-slo"},
		{ID: 102, Time: at.Add(-14 * time.Minute), Text: "ConfigMap apps/checkout-config edited", Tags: []string{"config", "namespace:apps"}},
		{ID: 103, Time: at.Add(-12 * time.Minute), Text: "Deploy checkout-api revision 2:\n example.test/checkout:v1 -> v2", Tags: []string{"deploy"}},
		{ID: 104, Time: at.Add(-9 * time.Minute), AlertName: "CheckoutHighErrorRate", NewState: "alerting", PrevState: "ok", Text: "CheckoutHighErrorRate {namespace=apps} - alerting",
			Tags: []string{"alert", "service:checkout-api"}, DashboardUID: "checkout-slo"},
		{ID: 105, Time: at.Add(-18 * time.Minute), AlertName: "KubeNodeNotReady", NewState: "alerting", Text: "KubeNodeNotReady {node=worker-b} - alerting", Tags: []string{"alert", "node:worker-b"}, DashboardUID: "k8s-node"},
		{ID: 106, Time: at.Add(-5 * time.Minute), Text: "Deploy checkout-api-worker revision 9", Tags: []string{"deploy", "namespace:apps"}},
		{ID: 107, Time: at.Add(-4 * time.Minute), Text: "Deploy checkout-api revision 4", Tags: []string{"deploy", "namespace:staging"}},
		{ID: 108, Time: at.Add(-3 * time.Minute), Text: "Quarterly failover drill"},
	}
	workload := &timeline.Scope{Namespace: "apps", Kind: "Deployment", Name: "checkout-api"}
	entries, notShown := annotationEntries(workload, []string{"checkout-api"}, nil, notes)
	var got []string
	for _, e := range entries {
		got = append(got, e.Summary)
		require.Equal(t, timeline.SourceAnnotation, e.Source)
		require.False(t, e.Failing, "an annotation is a statement by its author, never the fault's start")
		require.False(t, e.Change)
		require.Equal(t, "Grafana", e.Ref.Kind)
		require.Empty(t, e.Ref.Resource)
	}
	require.Equal(t, []string{"Load test: checkout-api baseline", "Deploy checkout-api revision 2: example.test/checkout:v1 -> v2", "Alert CheckoutHighErrorRate: ok to alerting"}, got,
		"only what names the workload; not a sibling workload, another namespace's deploy, or an untagged note")
	require.Equal(t, 5, notShown)
	require.Equal(t, at.Add(-25*time.Hour), entries[0].Last)
	require.Contains(t, entries[0].Detail, "region until 2026-11-16 11:00:00Z (1h)")
	require.Contains(t, entries[0].Detail, "in scope by tag checkout-api")
	require.Contains(t, entries[1].Detail, "in scope by text names checkout-api")
	require.Contains(t, entries[1].Detail, "not attached to a dashboard")
	require.Contains(t, entries[2].Detail, "in scope by tag service:checkout-api")
	require.Equal(t, "checkout-slo", entries[2].Ref.Name)
	require.Equal(t, "Grafana annotation 104, the time its author gave it", entries[2].Origin)

	// A namespace scope matches by namespace, and a node the timeline holds.
	namespace := &timeline.Scope{Namespace: "apps"}
	entries, notShown = annotationEntries(namespace, nil, map[string]bool{"worker-b": true}, notes)
	got = got[:0]
	for _, e := range entries {
		got = append(got, e.Summary)
	}
	require.Equal(t, []string{"Load test: checkout-api baseline", "ConfigMap apps/checkout-config edited", "Alert CheckoutHighErrorRate: ok to alerting",
		"Alert KubeNodeNotReady: unknown to alerting", "Deploy checkout-api-worker revision 9"}, got)
	require.Equal(t, 3, notShown)
	entries, _ = annotationEntries(namespace, nil, nil, notes[4:5])
	require.Empty(t, entries, "a node the timeline did not read is another scope")

	for text, want := range map[string]bool{"Deploy checkout-api revision 2": true, "checkout-api": true, "pod checkout-api.": true, "apps/checkout-api:v2": true,
		"checkout-api-worker": false, "my-checkout-api": false, "checkout-apis": false, "checkout-api.internal": false, "": false} {
		require.Equal(t, want, mentions(text, "checkout-api"), text)
	}

	// Merged into a snapshot: entries are placed in order, the read is stated.
	scope := &timeline.Scope{Context: "ct-1-1", Namespace: "apps", Resource: "apps/v1/deployments", Kind: "Deployment", Name: "checkout-api"}
	merged := timelineSnapshot(scope)
	before := merged.Onset
	read := &timelineAnnotations{declared: true, result: observability.AnnotationsResult{Status: observability.Status{State: observability.StateComplete}, Annotations: notes[1:]}}
	read.mergeInto(merged)
	require.Len(t, merged.Entries, 8)
	require.Equal(t, "Warning BackOff x14: Back-off restarting failed container", merged.OnsetEntry().Summary, "the fault's start is unchanged by annotations")
	require.NotEqual(t, before, -1)
	require.Empty(t, strings.ReplaceAll(merged.Gaps(), "nodes denied", ""), "a complete annotation read is not a gap")
	report := merged.Report(0)
	require.Contains(t, report, "Grafana annotations    COMPLETE   7 in the last 24h, 2 name this scope by tag or text, 5 not shown (limit 200)")
	require.Contains(t, report, "matched to this scope by tag or text only")

	app := grafanaApp(t, studyGrafana())
	v := newTimelineView(app, scope, "")
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	v.acceptSnapshot(merged, nil)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "ANNOT", "%dx%d", size[0], size[1])
		require.Contains(t, frame, "Alert CheckoutHighErr", "%dx%d", size[0], size[1])
	}
	require.Contains(t, drawnText(t, v, 120, 34), "grafana/checkout-slo")
	require.Contains(t, drawnText(t, v, 140, 34), "Shift-D Grafana")

	// Enter on an annotation opens the dashboard it is attached to, around its time.
	fake := newFakeDelivery("linux", map[string]string{})
	useDelivery(t, fake)
	for i := range merged.Entries {
		if merged.Entries[i].Summary == "Alert CheckoutHighErrorRate: ok to alerting" {
			v.focused = i
		}
	}
	v.openEntry()
	require.Equal(t, []string{"https://grafana.example.test/d/checkout-slo?from=1794915360000&to=1794917160000"}, fake.copied)
	h := app.Content.GetPrimitive(grafanaDialogPage).(*grafanaHandoff)
	require.Contains(t, handoffText(h, 96), "Window  11:36:00 to 12:06:00 UTC · 15m either side of the annotation")
	h.close()

	// Not readable: the header's sources line says so, and nothing is implied.
	for name, tc := range map[string]struct {
		status observability.Status
		gap    string
		detail string
	}{
		"denied":  {observability.Status{State: observability.StateDenied, Reason: "HTTP 403"}, "grafana annotations denied", "DENIED     denied (HTTP 403); annotations are unknown, not absent. :sources"},
		"timeout": {observability.Status{State: observability.StateTimeout, Reason: "no answer within 8s"}, "grafana annotations unknown", "UNKNOWN    timeout (no answer within 8s); annotations are unknown, not absent."},
	} {
		s := timelineSnapshot(scope)
		(&timelineAnnotations{declared: true, result: observability.AnnotationsResult{Status: tc.status}}).mergeInto(s)
		require.Len(t, s.Entries, 6, name)
		require.Contains(t, s.Gaps(), tc.gap, name)
		require.Contains(t, s.Report(0), "Grafana annotations    "+tc.detail, name)
		v.acceptSnapshot(s, nil)
		require.Contains(t, drawnText(t, v, 80, 24), "NOT READ: nodes denied, "+tc.gap, name)
	}
	truncated := timelineSnapshot(scope)
	(&timelineAnnotations{declared: true, result: observability.AnnotationsResult{Status: observability.Status{State: observability.StateIncomplete, Reason: "annotation limit 200 reached; older or newer entries may exist"},
		Annotations: notes[1:]}}).mergeInto(truncated)
	require.Contains(t, truncated.Gaps(), "grafana annotations incomplete")
	require.Contains(t, truncated.Report(0), "INCOMPLETE 7 in the last 24h, 2 name this scope by tag or text, 5 not shown (limit 200); annotation limit 200 reached")

	// Not declared: nothing was asked, so it is a stated limit, not a gap.
	undeclared := timelineSnapshot(scope)
	readTimelineAnnotations(t.Context(), &observability.Sources{}, nil, at).mergeInto(undeclared)
	require.Len(t, undeclared.Entries, 6)
	require.Equal(t, "nodes denied", undeclared.Gaps())
	require.Contains(t, undeclared.Report(0), "- Grafana is not declared for this context, so no annotations were read. :sources shows how to declare it.")
	require.NotContains(t, undeclared.Report(0), "Grafana annotations    ")

	// A Pod's timeline also shows what was annotated for its controller.
	podScope := &timeline.Scope{Namespace: "apps", Kind: "Pod", Name: "checkout-api-7d9f8c6b5d-x2x9q"}
	podSnapshot := timeline.NewSnapshot(podScope, at, []timeline.Entry{
		{At: at.Add(-12 * time.Minute), Source: timeline.SourceRollout, Ref: timeline.Ref{Kind: "Deployment", Name: "checkout-api"}},
		{At: at.Add(-12 * time.Minute), Source: timeline.SourceRollout, Ref: timeline.Ref{Kind: "ReplicaSet", Name: "checkout-api-7d9f8c6b5d"}}}, nil)
	require.Equal(t, []string{"checkout-api-7d9f8c6b5d-x2x9q", "checkout-api"}, snapshotNames(podSnapshot))
	require.Nil(t, snapshotNames(timeline.NewSnapshot(namespace, at, nil, nil)))
}
