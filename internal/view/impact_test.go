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
	"github.com/derailed/k9s/internal/impact"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var impactRolloutAt = time.Date(2026, 11, 17, 4, 20, 0, 0, time.UTC)

// impactSource answers range reads from generators keyed by a fragment of
// the query text. A fragment with a code matcher beats the plain selector.
type impactSource struct {
	answers map[string][2]float64
	status  observability.Status
}

func (s *impactSource) QueryRange(_ context.Context, e observability.Expr, start, end time.Time) observability.PromResult {
	query := e.String()
	result := observability.PromResult{Status: observability.Status{State: observability.StateComplete}, Query: query,
		Start: start, End: end, Step: observability.StepFor(end.Sub(start))}
	if s.status.State != "" {
		result.Status = s.status
		return result
	}
	sample := func(values [2]float64) []observability.Sample {
		var out []observability.Sample
		for at := start; !at.After(end); at = at.Add(result.Step) {
			value := values[0]
			if at.After(impactRolloutAt) {
				value = values[1]
			}
			out = append(out, observability.Sample{Timestamp: at, Value: value})
		}
		return out
	}
	if strings.Contains(query, "_bucket") {
		for le, values := range map[string][2]float64{"0.1": {30, 2}, "0.25": {39.7, 10}, "0.5": {40, 20}, "2.5": {40, 41.6}, "+Inf": {40, 42}} {
			result.Series = append(result.Series, observability.PromSeries{Labels: map[string]string{"le": le}, Samples: sample(values)})
		}
		return result
	}
	best, score := "", 0
	for fragment := range s.answers {
		weight := len(fragment)
		if strings.Contains(fragment, "code=~") {
			weight += 1000
		}
		if strings.Contains(query, fragment) && weight > score {
			best, score = fragment, weight
		}
	}
	if best != "" {
		result.Series = []observability.PromSeries{{Labels: map[string]string{}, Samples: sample(s.answers[best])}}
	}
	return result
}

func impactStory() *impactSource {
	return &impactSource{answers: map[string][2]float64{
		"http_requests_total": {40, 42},
		`code=~"5.."`:         {0.08, 5.964},
		"kube_pod_container_status_restarts_total":  {0, 6},
		"max(container_memory_working_set_bytes":    {188 << 20, 254 << 20},
		"kube_pod_container_resource_limits":        {0.367, 0.992},
		"container_cpu_usage_seconds_total":         {0.06, 0.34},
		"kube_deployment_status_replicas_available": {2, 1},
	}}
}

func impactTemplate(tag, memory string) *corev1.PodTemplateSpec {
	env := []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}
	if tag == "v2" {
		env = append(env, corev1.EnvVar{Name: "FEATURE_FLAGS", Value: "synthetic-flag-value-must-not-be-shown"})
	}
	return &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api", Image: "example.test/checkout:" + tag, Env: env,
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)}},
	}}}}
}

func impactStoryRead(t *testing.T, source impact.Ranger) *impactRead {
	t.Helper()
	windows, err := impact.PlanWindows(impactRolloutAt, time.Time{}, time.Time{}, impactRolloutAt.Add(12*time.Minute), impact.DefaultWidth)
	require.NoError(t, err)
	previous := impact.Revision{Number: 1, Name: "checkout-api-6c8d7f9b5", Created: impactRolloutAt.Add(-72 * time.Hour), Template: impactTemplate("v1", "512Mi")}
	rollout := impact.Rollout{Revision: impact.Revision{Number: 2, Name: "checkout-api-7d9f8c6b5d", Created: impactRolloutAt, Template: impactTemplate("v2", "256Mi")},
		Previous: &previous, Total: 2}
	workload := impact.Workload{Kind: impact.KindDeployment, Namespace: "apps", Name: "checkout-api"}
	report := impact.Collect(t.Context(), source, &impact.Request{Workload: workload, Rollout: rollout, Windows: windows,
		Source: "service monitoring/prometheus:9090"})
	report.ReadAt = impactRolloutAt.Add(12*time.Minute + 20*time.Second)
	return &impactRead{workload: workload, rollout: &rollout, report: report}
}

func impactViewFixture(t *testing.T) *impactView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	v := newImpactView(app, app.Config.ActiveContextName(), impact.KindDeployment, "apps", "checkout-api")
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	return v
}

func TestImpactFramesStateBeforeAfterAndVerdictAtEverySize(t *testing.T) {
	v := impactViewFixture(t)
	v.accept(impactStoryRead(t, impactStory()))
	// 80x20 and 60x20 are what the app leaves the view on an 80x24 or
	// 60x24 terminal.
	for _, size := range [][2]int{{120, 34}, {80, 24}, {80, 20}, {60, 20}, {40, 8}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		flat := strings.Join(strings.Fields(frame), " ")
		require.Contains(t, frame, "rev 2 (latest")
		require.Contains(t, frame, "04:20:00Z")
		require.Contains(t, flat, "After rev 2 (04:20): error ratio 0.2% -> 14.2%, p99 248ms -> 2.5s")
		require.Contains(t, frame, "rev 2 changed: image v1 -> v2; limits.memory 512Mi -> 256Mi")
		require.Contains(t, frame, "12m")
		require.Contains(t, frame, "step 15s")
		for _, row := range []string{"SIGNAL", "BEFORE", "AFTER", "VERDICT"} {
			require.Contains(t, frame, row)
		}
		require.Contains(t, flat, "! Error ratio 0.2% 14.2%")
		require.Contains(t, flat, "Request rate 40/s 42/s")
		require.Contains(t, flat, "Memory peak 188Mi 254Mi")
		require.Regexp(t, `! Error ratio .* worse`, flat)
		require.Regexp(t, `CPU throttled - - (not available)`, flat, "a missing metric is a dash and the words, never a zero")
		lower := strings.ToLower(frame)
		for _, banned := range []string{"because", "caused", "due to", "synthetic-flag"} {
			require.NotContains(t, lower, banned)
		}
		switch size[0] {
		case 120:
			require.Contains(t, frame, "prometheus service monitoring/prometheus:9090 · read 04:32:20Z · step 15s · rates over 2m")
			require.Contains(t, frame, "before 04:08:00-04:20:00Z │ after 04:20:00-04:32:00Z · 12m each (asked 30m; the rollout was 12m before this read)")
			require.Contains(t, frame, "Gaps, not zeros - not available: CPU throttled")
			require.Contains(t, frame, "CHANGE")
			require.Contains(t, flat, "+14pp x71 worse")
			require.Contains(t, frame, "rule: counts as changed from 0.1pp and 10% of before; needs 4 samples on each side")
			require.Contains(t, frame, `query: sum(rate(http_requests_total{namespace="apps",pod=~"checkout-api-[a-z0-9]+-[a-z0-9]+",code=~"5.."}[120s]))`)
			require.Contains(t, frame, "before│after")
		case 80:
			require.Contains(t, frame, "service monitoring/prometheus:9090 · read 04:32:20Z · step 15s")
			require.Contains(t, frame, "before 04:08:00-04:20:00Z │ after 04:20:00-04:32:00Z · 12m each, asked 30m")
			require.Contains(t, frame, "before│after")
			require.Contains(t, frame, "x71")
			require.NotContains(t, frame, "CHANGE")
			if size[1] == 20 {
				require.Contains(t, frame, "1 not compared", "the folded state line leaves its count on the source line")
				require.Contains(t, flat, "! Ready (min) 2 1", "every signal stays on screen at 80x24")
				require.Contains(t, frame, "Error ratio: 0.2% -> 14.2% · +14pp · x71 · worse")
			}
		case 60:
			require.Contains(t, frame, "before 04:08-04:20Z │ after 04:20-04:32Z · 12m (asked 30m)")
			require.NotContains(t, frame, "before│after", "the trend gives way before signal, before, after or verdict")
			require.Contains(t, frame, "p/n rollout")
		}
	}
}

func TestImpactNoIconsUsesASCIIOnly(t *testing.T) {
	v := impactViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	v.accept(impactStoryRead(t, impactStory()))
	frame := drawnText(t, v, 100, 30)
	require.Contains(t, frame, "before|after")
	require.Contains(t, frame, "Z | after 04:20:00")
	require.Regexp(t, `_{4,}\|#{4,}`, frame, "the error ratio trend: low before the mark, high after it")
	for _, glyph := range []string{"│", "▁", "█", "▶"} {
		require.NotContains(t, frame, glyph)
	}
	require.Equal(t, "_.:-=+*#", impactSpark([]int{0, 1, 2, 3, 4, 5, 6, 7}, true))
	require.Equal(t, "▁ █", impactSpark([]int{0, -1, 7}, false), "a cell with no sample is blank")
}

func TestImpactStatesWhatCouldNotBeReadInsteadOfShowingZeros(t *testing.T) {
	v := impactViewFixture(t)
	frame := drawnText(t, v, 80, 24)
	require.Contains(t, frame, "Impact / Deployment apps/checkout-api")
	require.Contains(t, frame, "Bounded, read-only, and only because this view was opened.")

	v.accept(impactStoryRead(t, &impactSource{status: observability.Status{State: observability.StateDenied, HTTPStatus: 403}}))
	frame = drawnText(t, v, 100, 30)
	require.Contains(t, frame, "After rev 2 (04:20): nothing was read from the source: denied (HTTP 403). No comparison was made.")
	require.Contains(t, frame, "Not read is not zero and not healthy.")
	require.Contains(t, frame, "rev 2 changed: image v1 -> v2", "what the revision changed needs no metrics source")
	require.Equal(t, len(impact.Signals), strings.Count(frame, "not read  "))
	require.Contains(t, frame, "Request rate: not read - denied (HTTP 403)")
	require.NotRegexp(t, `Request rate +0`, frame)

	rollback := impact.Rollout{Revision: impact.Revision{Number: 3, Name: "checkout-api-6c8d7f9b5"}, Total: 3}
	v.accept(&impactRead{workload: v.workload(), rollout: &rollback,
		err: errors.New("rev 3 reuses checkout-api-6c8d7f9b5: a rollback. Kubernetes does not record when a rollback happened")})
	frame = drawnText(t, v, 80, 24)
	require.Contains(t, frame, "rev 3 (latest; 3 retained)")
	require.Contains(t, frame, "No comparison: rev 3 reuses checkout-api-6c8d7f9b5: a rollback.")
	require.Contains(t, frame, "p and n step to another retained rollout.")
	require.Contains(t, frame, "Nothing was compared.")

	v.accept(&impactRead{err: errors.New("Pod apps/checkout-api-0 has no controller, so it has no rollout to compare around")})
	require.Contains(t, drawnText(t, v, 80, 24), "No comparison: Pod apps/checkout-api-0 has no controller")
}

func impactCluster(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	object := func(kind, name string, created time.Time, owner string, extra map[string]any) *unstructured.Unstructured {
		meta := map[string]any{"name": name, "namespace": "apps", "uid": "uid-" + name, "creationTimestamp": created.Format(time.RFC3339),
			"labels": map[string]any{"app": "checkout"}}
		if owner != "" {
			meta["ownerReferences"] = []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": owner, "uid": "uid-" + owner, "controller": true}}
		}
		if annotations, ok := extra["annotations"]; ok {
			meta["annotations"] = annotations
			delete(extra, "annotations")
		}
		content := map[string]any{"apiVersion": "apps/v1", "kind": kind, "metadata": meta}
		for key, value := range extra {
			content[key] = value
		}
		return &unstructured.Unstructured{Object: content}
	}
	set := func(name, revision string, created time.Time, tag, memory string) *unstructured.Unstructured {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(impactTemplate(tag, memory))
		require.NoError(t, err)
		return object("ReplicaSet", name, created, "checkout-api", map[string]any{
			"annotations": map[string]any{"deployment.kubernetes.io/revision": revision}, "spec": map[string]any{"template": raw}})
	}
	now := time.Now().UTC().Truncate(time.Second)
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "apps", Version: "v1", Resource: "replicasets"}: "ReplicaSetList", {Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
	},
		object("Deployment", "checkout-api", now.Add(-96*time.Hour), "", map[string]any{
			"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "checkout"}}}}),
		set("checkout-api-6c8d7f9b5", "1", now.Add(-72*time.Hour), "v1", "512Mi"),
		set("checkout-api-7d9f8c6b5d", "2", now.Add(-12*time.Minute), "v2", "256Mi"))
}

func TestImpactReadNeedsADeclaredSourceButStillStatesTheRollout(t *testing.T) {
	objects := impactCluster(t)
	read := readImpact(t.Context(), objects, nil, nil, impact.KindDeployment, "apps", "checkout-api", 0, impact.DefaultWidth)
	require.NoError(t, read.err)
	require.Contains(t, read.report.Failure, "no Prometheus source is declared for this context; :sources shows how to declare one")
	require.Equal(t, "512Mi -> 256Mi", read.report.MemoryLimit)
	require.Equal(t, 12*time.Minute, read.report.Windows.Width)
	require.Empty(t, read.report.Outcomes)
	v := impactViewFixture(t)
	v.accept(&read)
	frame := drawnText(t, v, 100, 30)
	require.Contains(t, frame, "nothing was read from the source: no Prometheus source is declared")
	require.Contains(t, frame, "rev 2 changed: image v1 -> v2; limits.memory 512Mi -> 256Mi; env +FEATURE_FLAGS")
	require.Contains(t, frame, "Nothing was read, so nothing is shown as a value.")

	bad := &observability.Sources{RED: &observability.RED{Requests: "not a metric"}}
	read = readImpact(t.Context(), objects, bad, nil, impact.KindDeployment, "apps", "checkout-api", 0, impact.DefaultWidth)
	require.Contains(t, read.report.Failure, "the k9s.observability.red mapping is not usable")

	earliest := readImpact(t.Context(), objects, nil, nil, impact.KindDeployment, "apps", "checkout-api", 1, impact.DefaultWidth)
	require.Equal(t, 1, earliest.rollout.Back)
	require.Equal(t, 30*time.Minute, earliest.report.Windows.Width)

	missing := readImpact(t.Context(), objects, nil, nil, impact.KindDeployment, "apps", "gone", 0, impact.DefaultWidth)
	require.ErrorContains(t, missing.err, "Deployment apps/gone: not found")
	require.Nil(t, missing.report)
}

func TestImpactKeysReadOnlyOnRequestAndDiscardLateReplies(t *testing.T) {
	v := impactViewFixture(t)
	type call struct {
		back  int
		width time.Duration
	}
	calls := make(chan call, 8)
	v.loader = func(_ context.Context, back int, width time.Duration) impactRead {
		calls <- call{back, width}
		return impactRead{}
	}
	next := func() call {
		select {
		case got := <-calls:
			return got
		case <-time.After(5 * time.Second):
			t.Fatal("no read was started")
			return call{}
		}
	}
	press := func(key tcell.Key) {
		action, ok := v.actions.Get(key)
		require.True(t, ok)
		action.Action(nil)
	}
	quiet := func() {
		select {
		case got := <-calls:
			t.Fatalf("unexpected read %+v", got)
		case <-time.After(50 * time.Millisecond):
		}
	}
	v.accept(impactStoryRead(t, impactStory()))
	quiet()
	press(ui.Key1)
	require.Equal(t, call{0, 5 * time.Minute}, next())
	press(ui.KeyP)
	require.Equal(t, call{1, 5 * time.Minute}, next())
	press(ui.KeyP)
	quiet() // only two revisions are retained
	press(ui.KeyN)
	require.Equal(t, call{0, 5 * time.Minute}, next())
	press(ui.KeyN)
	quiet() // already the latest
	press(ui.KeyR)
	require.Equal(t, call{0, 5 * time.Minute}, next())
	require.True(t, v.loading)
	require.Contains(t, v.header.GetText(true), "Reading again; the comparison below is retained")

	generation := v.generation
	v.Stop()
	require.Greater(t, v.generation, generation, "leaving the view invalidates the read in flight")
	require.False(t, v.loading)

	_, bound := v.actions.Get(tcell.KeyEnter)
	require.False(t, bound, "Enter does nothing when the build has no graph command")
	require.NotContains(t, v.footer.GetText(true), "Enter")
	withGraph := impactViewFixture(t)
	withGraph.app.command = NewCommand(withGraph.app)
	aliases := []string{impactGraphCommand}
	withGraph.app.command.suggestionCatalog.Store(&aliases)
	withGraph.bindImpactKeys()
	withGraph.render()
	_, bound = withGraph.actions.Get(tcell.KeyEnter)
	require.True(t, bound)
	require.Contains(t, withGraph.footer.GetText(true), "Enter graph")
}

func TestImpactTargetsWorkloadsAndIsRegistered(t *testing.T) {
	for gvr, kind := range map[*client.GVR]string{
		client.DpGVR: impact.KindDeployment, client.StsGVR: impact.KindStatefulSet, client.DsGVR: impact.KindDaemonSet, client.PodGVR: impact.KindPod,
	} {
		got, err := impactTarget(SelectedResourceTarget{Context: "ctx", GVR: gvr, Namespace: "apps", Name: "checkout-api"})
		require.NoError(t, err)
		require.Equal(t, kind, got)
	}
	_, err := impactTarget(SelectedResourceTarget{Context: "ctx", GVR: client.SvcGVR, Namespace: "apps", Name: "checkout-api"})
	require.ErrorContains(t, err, "select a Deployment, StatefulSet, DaemonSet or one of their Pods")
	_, err = impactTarget(SelectedResourceTarget{Context: "ctx", UnavailableReason: "nothing selected"})
	require.ErrorContains(t, err, "Select a Deployment")

	v := impactViewFixture(t)
	target := v.SelectedResource()
	require.Equal(t, "apps/v1/deployments", target.GVR.String())
	require.Equal(t, "apps/checkout-api", target.Path())
	find := func() *ui.ActionDescriptor {
		catalog := actionCatalog(v, v.app)
		for index := range catalog {
			if catalog[index].ID == "resource.impact" {
				return &catalog[index]
			}
		}
		return nil
	}
	action := find()
	require.NotNil(t, action)
	require.Equal(t, ":impact", action.Shortcut)
	require.Equal(t, ui.ActionInspect, action.Category)
	require.Contains(t, action.UnavailableReason, "No Prometheus source is declared", "the palette says why before anything is opened")
	declareStudySources(t, v.app)
	require.Empty(t, find().UnavailableReason)
	command := NewCommand(v.app)
	command.updateSuggestionAliases()
	require.Contains(t, command.suggestionAliases(), impactCommandToken)

	require.Equal(t, impactColumns{change: 9, percent: 6, cells: 12}, impactLayout(120))
	require.Equal(t, impactColumns{percent: 6, cells: 9}, impactLayout(80))
	require.Equal(t, impactColumns{percent: 6}, impactLayout(60))
	require.Equal(t, []string{"After rev 2 (04:20): error ratio", "0.2% -> 14.1%."}, impactWrap("After rev 2 (04:20): error ratio 0.2% -> 14.1%.", 33))
}

func TestImpactWarnsWhenTheRevisionListWasCut(t *testing.T) {
	v := impactViewFixture(t)
	read := impactStoryRead(t, impactStory())
	read.incomplete = "Only the first 200 ReplicaSets were read (limit 200): a later rollout may be missing."
	v.accept(read)
	for _, size := range [][2]int{{100, 30}, {80, 20}, {60, 20}} {
		frame := drawnText(t, v, size[0], size[1])
		require.Contains(t, frame, "Only the first 200 ReplicaSets were read (limit 200)", "%dx%d", size[0], size[1])
		require.Contains(t, frame, "Error ratio", "%dx%d", size[0], size[1])
	}
}
