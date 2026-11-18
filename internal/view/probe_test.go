// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/probe"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

const (
	probePodA = "gitlab-webservice-default-6f9c7d5b84-2bq7k"
	probePodB = "gitlab-webservice-default-6f9c7d5b84-8wzpd"
	probePodC = "gitlab-webservice-default-6f9c7d5b84-hx4mt"
)

func probeTestPod(name, node, zone string, declared bool) probe.Pod {
	container := map[string]any{"name": "webservice"}
	if declared {
		container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": "/-/readiness", "port": int64(8080)}, "periodSeconds": int64(10)}
		container["livenessProbe"] = map[string]any{"httpGet": map[string]any{"path": "/-/liveness", "port": int64(8080)}}
	}
	pod := probe.PodFromObject(map[string]any{
		"metadata": map[string]any{"namespace": "gitlab", "name": name, "uid": "uid-" + name},
		"spec":     map[string]any{"nodeName": node, "containers": []any{container}},
		"status":   map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	})
	pod.Zone = zone
	return pod
}

func probeTestPlan() *probe.Plan {
	plan := probe.NewPlan(probe.Scope{Kind: probe.KindService, Namespace: "gitlab", Name: "gitlab-webservice-default", UID: "svc"}, []probe.Pod{
		probeTestPod(probePodA, "worker-d", "study-zone-a", true),
		probeTestPod(probePodB, "worker-e", "study-zone-b", true),
		probeTestPod(probePodC, "worker-f", "study-zone-c", true),
	}, 3)
	plan.Access = probe.AccessAllowed
	return plan
}

// probeStory answers like the fixture: two Pods in milliseconds, one in
// seconds with a request that gets no answer.
type probeStory struct {
	calls atomic.Int32
}

func (s *probeStory) Get(_ context.Context, request *probe.Request) probe.Response {
	n := s.calls.Add(1)
	if request.Pod != probePodC {
		return probe.Response{Outcome: probe.Answered, Code: 200, Took: time.Duration(18+n%7) * time.Millisecond}
	}
	if n%5 == 0 {
		return probe.Response{Outcome: probe.TimedOut, Took: 5 * time.Second}
	}
	return probe.Response{Outcome: probe.Answered, Code: 200, Took: 2730 * time.Millisecond}
}

func probeViewFixture(t *testing.T) (*probeView, *probeStory) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	story := &probeStory{}
	v := newProbeView(app, app.Config.ActiveContextName(), probe.Scope{Kind: probe.KindService, Namespace: "gitlab", Name: "gitlab-webservice-default", UID: "svc"})
	v.transport, v.pause = story, 0
	require.NoError(t, v.Init(t.Context()))
	t.Cleanup(v.Stop)
	app.Content.Push(v)
	v.active = true
	return v, story
}

// probeFinish runs the plan to its end on this goroutine and hands the run
// to the view, as the background run does through the UI queue.
func probeFinish(t *testing.T, v *probeView, transport probe.Transport) {
	t.Helper()
	if v.run != nil && v.run.Sent() > 0 {
		v.previous, v.previousAt = v.run, v.ranAt
	}
	v.ranAt = time.Date(2026, 11, 17, 14, 2, 11, 0, time.UTC)
	v.finish(probe.Execute(t.Context(), transport, v.plan, probe.Options{Attempts: v.attempts, Pause: time.Nanosecond, Concurrency: 1}, nil))
}

func probePress(t *testing.T, v *probeView, key tcell.Key) {
	t.Helper()
	action, ok := v.actions.Get(key)
	require.True(t, ok)
	action.Action(nil)
}

// probeDetail presses a until the view shows the table and result pane it
// had before the calm layout (on), or the calm page again.
func probeDetail(t *testing.T, v *probeView, on bool) {
	t.Helper()
	if v.allDetail != on {
		probePress(t, v, calmDetailKey)
	}
	require.Equal(t, on, v.allDetail)
}

func TestProbeSendsNothingUntilTheRequestWasReadAndConfirmed(t *testing.T) {
	v, story := probeViewFixture(t)
	require.Contains(t, v.runUnavailable(), "still being read")
	v.acceptPlan(probeTestPlan(), nil)
	// The calm page is one block before a run: nothing has left, what r
	// would send, and the Pods it would go to.
	frame := drawnText(t, v, 132, 33)
	require.Equal(t, "  Nothing has been sent.", strings.Split(frame, "\n")[2], "the answer comes first")
	flat := strings.Join(strings.Fields(frame), " ")
	// Two sentences and one dim line; how the requests are paced and what
	// opening the view read are in the question r asks and under a.
	require.Equal(t, "  r sends GET :8080/-/readiness on each pod, 5 times, through the Kubernetes API proxy: 15 requests in all. You are asked first.",
		strings.Split(frame, "\n")[3])
	require.Equal(t, "  It is each Pod's own readinessProbe; the application will see it in its logs.", strings.Split(frame, "\n")[4])
	require.Equal(t, "", strings.Split(frame, "\n")[5], "the answer is three lines at this width")
	require.NotContains(t, frame, "at most 4 Pods at a time")
	require.Len(t, strings.Split(strings.SplitN(drawnText(t, v, 80, 20), "\n\n", 3)[1], "\n"), 4, "and four at 80 columns")
	require.Regexp(t, `(?m)^Probe  Service gitlab/gitlab-webservice-default · GET :8080/-/readiness ×5 per pod +nothing sent$`, frame)
	require.Contains(t, frame, "  WOULD BE ASKED\n> 2bq7k  worker-d study-zone-a\n  8wzpd  worker-e study-zone-b\n  hx4mt  worker-f study-zone-c\n")
	require.Contains(t, frame, "  next  r send 15 requests (asks first)    + 10 per pod    u liveness probe instead    Enter open 2bq7k\n")
	require.NotContains(t, frame, probeMeasuredLabel, "nothing was measured, so nothing is said about a measurement")
	require.NotRegexp(t, `[▁▂▃▄▅▆▇█]`, frame, "there is no strip before a request")
	// a shows the table the view had before; it states the same.
	probeDetail(t, v, true)
	frame = drawnText(t, v, 132, 32)
	require.Contains(t, frame, "Nothing has been sent. Opening this view read Kubernetes objects only; r sends 15 read-only GET requests after you confirm.")
	require.Contains(t, frame, "GET :8080/-/readiness on each pod, 5 times, through the Kubernetes API proxy")
	require.Contains(t, frame, "not sent")
	detail := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, detail, "15 requests in all, one after another per Pod, at most 4 Pods at a time. You are asked first.")
	require.Contains(t, detail, "Path and port are those of each Pod's own readinessProbe, the request the kubelet already makes. "+
		"The application handles it and will see it in its logs.", "what the calm page leaves out is under a")
	probeDetail(t, v, false)
	require.Zero(t, story.calls.Load(), "opening the view, reading the plan and changing the layout contact no Pod")

	probePress(t, v, ui.KeyR)
	require.NotNil(t, v.confirm, "r asks before the first request")
	require.False(t, v.running)
	require.Zero(t, story.calls.Load())
	text := v.confirm.message
	for _, stated := range []string{
		"Send GET :8080/-/readiness to 3 pods of Service gitlab/gitlab-webservice-default, 5 times each: 15 requests in all.",
		"This is an active request to the application.",
		"no body and no added header",
		"the application handles it and will see it in its logs",
		"each pod's own readinessProbe: nothing here is typed",
		"Kubernetes API server pod proxy",
		"Each waits at most 5s; the response body is discarded. Esc cancels",
	} {
		require.Contains(t, text, stated)
	}
	require.Zero(t, v.confirm.focus, "Cancel has the focus")
	dialog := drawnText(t, v.confirm, 80, 24)
	for _, line := range strings.Split(dialog, "\n") {
		require.LessOrEqual(t, runewidth.StringWidth(line), 80, line)
	}
	flat = strings.Join(strings.Fields(strings.NewReplacer("│", " ").Replace(dialog)), " ")
	require.Contains(t, flat, "will see it in its logs", "the whole statement is on screen at 80x24 without scrolling")
	require.Contains(t, flat, "> Cancel < Send 15 requests")
	require.True(t, v.confirm.IsDialog())

	// Enter on the focused Cancel sends nothing.
	v.confirm.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	require.Nil(t, v.confirm)
	require.False(t, v.running)
	require.Empty(t, v.confirmed)
	require.Zero(t, story.calls.Load())
}

func TestProbeRunsOnlyWhatWasConfirmedAndStopsOnEsc(t *testing.T) {
	v, story := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)

	// Tab to Send, Enter: the requests leave, one Pod after another.
	probePress(t, v, ui.KeyR)
	v.confirm.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), nil)
	v.confirm.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	require.Nil(t, v.confirm)
	require.True(t, v.running)
	require.NotEmpty(t, v.confirmed)
	require.Eventually(t, func() bool { return story.calls.Load() == 15 }, 5*time.Second, 5*time.Millisecond)
	require.Contains(t, v.runUnavailable(), "A probe is running")

	// Esc stops a running probe instead of leaving the view.
	probePress(t, v, tcell.KeyEscape)
	require.False(t, v.running)
	require.Equal(t, "canceled", v.run.Stopped)
	require.Equal(t, v, v.app.Content.Top())

	// The same request shape runs again without a second question; another
	// count is another request and asks again.
	sent := story.calls.Load()
	probePress(t, v, ui.KeyR)
	require.Nil(t, v.confirm)
	require.True(t, v.running)
	require.Eventually(t, func() bool { return story.calls.Load() == sent+15 }, 5*time.Second, 5*time.Millisecond)
	v.cancelRun()
	probePress(t, v, probeMoreKey)
	require.Equal(t, 10, v.attempts)
	probePress(t, v, ui.KeyR)
	require.NotNil(t, v.confirm)
	require.Contains(t, v.confirm.message, "10 times each: 30 requests in all")
	require.False(t, v.running)
	v.confirm.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), nil)
	require.Nil(t, v.confirm)

	// The other declared probe is the only other request there is. The
	// answers of one path are put aside while the other is shown, never
	// mixed with it, and come back with their path.
	readiness := v.run
	require.NotNil(t, readiness)
	probePress(t, v, ui.KeyU)
	require.Equal(t, probe.Liveness, v.plan.Kind)
	require.Nil(t, v.run, "answers to another path are not shown as if comparable")
	require.Contains(t, drawnText(t, v, 132, 32), "GET :8080/-/liveness on each pod, 10 times")
	probePress(t, v, ui.KeyU)
	require.Equal(t, probe.Readiness, v.plan.Kind)
	require.Same(t, readiness, v.run, "one key press does not throw a run away")
	probePress(t, v, ui.KeyU)
	require.Nil(t, v.run)

	// Leaving the view cancels and invalidates whatever is in flight.
	generation := v.generation
	v.Stop()
	require.Greater(t, v.generation, generation)
	require.False(t, v.running)
}

// The table of all detail (a): every column the width has room for, the
// run before as a dimmer row, and the result pane under it.
func TestProbeFramesKeepPodMedianAndVerdictAtEverySize(t *testing.T) {
	v, story := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)
	probeDetail(t, v, true)
	probeFinish(t, v, story)
	require.Equal(t, probePodC, v.selected, "the cursor starts on the Pod that differs")
	require.NotContains(t, drawnText(t, v, 132, 32), "m graphs", "a check whose key would be refused is not proposed")
	declareStudySources(t, v.app)
	v.render()
	for _, size := range [][2]int{{132, 32}, {120, 30}, {80, 20}, {60, 20}, {40, 8}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		flat := strings.Join(strings.Fields(frame), " ")
		require.Contains(t, frame, "Probe / ")
		require.Contains(t, frame, "POD")
		require.Contains(t, frame, "MEDIAN")
		require.Contains(t, frame, "VS PEERS")
		require.Regexp(t, `>! \S*-hx4mt .* 2\.73s .*OUTLIER`, frame, "pod, median and the outlier word at %d columns", size[0])
		require.Regexp(t, `\S*-2bq7k .* \d+ms .*like peers`, frame)
		require.Contains(t, flat, "Only hx4mt answered slowly: median 2.73s against")
		require.Contains(t, flat, "1 of 5 had no answer within 5s")
		require.Regexp(t, `not (through the )?ingress or (the )?Service`, flat, "the measured path is always stated")
		require.Regexp(t, `a slow Pod, not a slow (network )?path`, flat)
		require.Contains(t, flat, "15 sent in")
		require.Contains(t, flat, "full bar █ = ")
		require.NotContains(t, strings.ToLower(flat), "healthy")
		require.NotContains(t, strings.ToLower(flat), "because")
	}
	wide := drawnText(t, v, 132, 32)
	for _, shown := range []string{"FASTEST", "SLOWEST", "OVER 5s", "CODES", "NODE", "ZONE", "worker-f", "study-zone-c", "1 of 5", "≥5s", "200",
		"EACH REQUEST", "nothing is sent unless you press r", "the fastest Pod here,", "which is the floor",
		"next: Enter opens hx4mt in the Pod list", "m graphs its CPU against its limit"} {
		// A sentence wraps where the width ends: the words are looked for
		// across the line breaks.
		require.Contains(t, strings.Join(strings.Fields(wide), " "), shown)
	}
	require.Regexp(t, `-hx4mt (\S ){4}\S +2\.73s`, wide, "one cell per request, with a blank between them")
	require.Contains(t, wide, "×", "a request without an answer is a cross, not a bar")
	narrow := drawnText(t, v, 60, 20)
	require.NotContains(t, narrow, "ZONE")
	require.Contains(t, narrow, "SLOWEST")
	require.Contains(t, narrow, "…-6f9c7d5b84-hx4mt", "a shortened name keeps the end that tells the Pods apart")
	require.NotContains(t, narrow, "…lt-", "and is cut at a dash, not inside a word")

	// A second run keeps the first as a dimmer row under each Pod.
	probeFinish(t, v, story)
	frame := drawnText(t, v, 132, 32)
	require.Equal(t, 3, strings.Count(frame, "previous 14:02:11Z"))
	require.Regexp(t, `previous 14:02:11Z +(\S ){4}\S +\S+ +2\.73s`, frame)
	require.Contains(t, drawnText(t, v, 60, 20), "previous 14:02:11Z")

	// Moving the cursor names the Pod under it.
	probePress(t, v, tcell.KeyUp)
	require.Equal(t, probePodB, v.selected)
	require.Contains(t, drawnText(t, v, 132, 32), "8wzpd on worker-e (study-zone-b)")
	target := v.SelectedResource()
	require.Equal(t, client.PodGVR, target.GVR)
	require.Equal(t, "gitlab/"+probePodB, target.Path())
	require.Equal(t, client.SvcGVR, v.scopeTarget().GVR)
	require.Equal(t, "gitlab/gitlab-webservice-default", v.scopeTarget().Path(), ":slow is handed the selection the probe was opened for, not the Pod under the cursor")
	_, err := slowTarget(v.scopeTarget())
	require.NoError(t, err, "a Service has a request path")
}

func TestProbeNoIconsUsesASCIIOnly(t *testing.T) {
	v, story := probeViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	v.acceptPlan(probeTestPlan(), nil)
	probeDetail(t, v, true)
	probeFinish(t, v, story)
	frame := drawnText(t, v, 132, 32)
	for _, glyph := range []string{"▁", "▂", "▇", "█", "×", "≥", "…", "·", "→"} {
		require.NotContains(t, frame, glyph)
	}
	require.Regexp(t, `>! \S*-hx4mt .* 2\.73s .* >=5s .*OUTLIER`, frame)
	require.Regexp(t, `-hx4mt ([_.:=+*#x-] ){4}[_.:=+*#x-] `, frame)
	probePress(t, v, probeMoreKey)
	probePress(t, v, ui.KeyR)
	require.NotNil(t, v.confirm)
	dialog := drawnText(t, v.confirm, 80, 24)
	require.Contains(t, dialog, "Tab switches | Enter chooses | Esc cancels")
	for _, glyph := range []string{"·", "…", "×"} {
		require.NotContains(t, dialog, glyph)
	}
	v.confirm.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), nil)
	require.Contains(t, frame, "full bar # = ")
	require.Contains(t, frame, "Only hx4mt answered slowly")
	require.Contains(t, frame, " | ")
}

// A request that was in flight when Esc was pressed did reach the API
// server. It is counted as sent and drawn as abandoned, never as unsent.
func TestProbeCountsARequestInFlightAtCancelAsSent(t *testing.T) {
	v, _ := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)
	probeDetail(t, v, true)
	v.selected = probePodC
	v.run = probe.NewRun(v.plan, probe.Options{Attempts: 5})
	v.ranAt = time.Now().UTC().Add(-4 * time.Second)
	v.run.StartedAt = v.ranAt
	v.running = true
	fast := probe.Attempt{Outcome: probe.Answered, Code: 200, Took: 20 * time.Millisecond}
	for n := range 5 {
		v.run.Pods[0].Attempts[n], v.run.Pods[1].Attempts[n] = fast, fast
	}
	v.run.Pods[2].Attempts[0] = probe.Attempt{Outcome: probe.Answered, Code: 200, Took: 2320 * time.Millisecond}
	v.run.Pods[2].Attempts[1] = probe.Attempt{Outcome: probe.InFlight}
	v.render()
	frame := drawnText(t, v, 132, 32)
	require.Contains(t, frame, "Sending: 11 of 15 done", "a request in flight is not done")
	require.Regexp(t, `-hx4mt \S ~ · · · .* 0 of 1 .*probing`, frame, "in flight is drawn as such, and not counted as answered in time")
	require.Contains(t, frame, "2.32s waiting")

	probePress(t, v, tcell.KeyEscape)
	require.False(t, v.running)
	require.Equal(t, 12, v.run.Sent())
	require.Equal(t, 1, v.run.Abandoned())
	frame = drawnText(t, v, 132, 32)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, "12 sent in")
	require.Contains(t, flat, "Canceled: 12 of 15 requests were sent, 1 of them still in flight and abandoned without an answer "+
		"(the application may still handle it); the rest were not.")
	require.Regexp(t, `-hx4mt \S \? · · · .* 0 of 1 `, frame)
	require.Contains(t, frame, "2.32s abandoned")
	require.NotContains(t, frame, "...", "nothing is elided from five requests")

	// Leaving the view with a request in flight records it the same way.
	v.run.Pods[2].Attempts[2] = probe.Attempt{Outcome: probe.InFlight}
	v.running = true
	v.Stop()
	require.Equal(t, 2, v.run.Abandoned())
	require.Equal(t, "canceled", v.run.Stopped)
}

// A request without an answer is flagged on its own, with few Pods and when
// every Pod has one; and more than twelve times are counted, not cut.
func TestProbeFlagsTimeoutsWithoutPeersAndCountsElidedTimes(t *testing.T) {
	v, _ := probeViewFixture(t)
	plan := probe.NewPlan(v.scope, []probe.Pod{
		probeTestPod(probePodA, "worker-d", "study-zone-a", true), probeTestPod(probePodB, "worker-e", "study-zone-b", true),
	}, 2)
	plan.Access = probe.AccessAllowed
	v.acceptPlan(plan, nil)
	probeDetail(t, v, true)
	v.attempts = 20
	// Counted per Pod, so the answers do not depend on which Pod is asked
	// first.
	var mu sync.Mutex
	calls := map[string]int{}
	probeFinish(t, v, probeTransportFunc(func(request *probe.Request) probe.Response {
		mu.Lock()
		calls[request.Pod]++
		n := calls[request.Pod]
		mu.Unlock()
		if request.Pod == probePodB && n%8 == 0 {
			return probe.Response{Outcome: probe.TimedOut, Took: 5 * time.Second}
		}
		return probe.Response{Outcome: probe.Answered, Code: 200, Took: 20 * time.Millisecond}
	}))
	require.Equal(t, probePodB, v.selected, "the cursor starts on the Pod with requests that got no answer")
	frame := drawnText(t, v, 132, 32)
	require.Regexp(t, `>! \S*-8wzpd .* 2 of 20 .*TIMEOUTS`, frame)
	require.Regexp(t, `   \S*-2bq7k .* 0 of 20 .*answered`, frame)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, "8wzpd gave no answer within 5s to 2 of 20 requests; its 18 answers took 20ms.")
	require.Contains(t, flat, "The other Pod answered all 20 requests in 20ms or less.")
	require.Contains(t, flat, "... (8 more)")
}

func TestProbeStatesWhyNothingCanBeSent(t *testing.T) {
	// No declared HTTP probe: listed, never contacted.
	v, story := probeViewFixture(t)
	plan := probe.NewPlan(v.scope, []probe.Pod{probeTestPod("gitlab-gitaly-0", "worker-d", "study-zone-a", false)}, 1)
	v.acceptPlan(plan, nil)
	probeDetail(t, v, true)
	frame := drawnText(t, v, 100, 24)
	require.Contains(t, frame, "not contacted: no declared HTTP probe")
	require.Contains(t, frame, "The listed Pod does not declare an httpGet readinessProbe or livenessProbe")
	require.Contains(t, frame, "The path is never typed.")
	require.Contains(t, v.runUnavailable(), "nothing this command may send")
	require.NotContains(t, v.footer.GetText(true), "r run", "a key that would send nothing is not offered")
	require.Contains(t, v.footer.GetText(true), "Enter open pod")
	probePress(t, v, ui.KeyR)
	require.Nil(t, v.confirm)
	require.Zero(t, story.calls.Load())

	// The access review said no: the rule is named and r sends nothing.
	denied := probeTestPlan()
	denied.Access, denied.AccessReason = probe.AccessDenied, "missing "+probe.MissingAccess("gitlab")
	v.acceptPlan(denied, nil)
	frame = strings.Join(strings.Fields(drawnText(t, v, 100, 30)), " ")
	require.Contains(t, frame, `Not allowed. The API server says this user is missing RBAC verb "get" on resource "pods/proxy" (core API group) in namespace "gitlab".`)
	require.Contains(t, frame, "Nothing was sent and r will send nothing.")
	probePress(t, v, ui.KeyR)
	require.Nil(t, v.confirm)
	require.Zero(t, story.calls.Load())

	// The review allowed it but the request itself is refused.
	allowed := probeTestPlan()
	v.acceptPlan(allowed, nil)
	probeFinish(t, v, probeTransportFunc(func(*probe.Request) probe.Response {
		return probe.Response{Outcome: probe.Denied, Code: 403, Detail: "forbidden"}
	}))
	frame = strings.Join(strings.Fields(drawnText(t, v, 100, 30)), " ")
	require.Contains(t, frame, `Not allowed: the API server refused the request. This user is missing RBAC verb "get" on resource "pods/proxy"`)
	require.Contains(t, frame, "No request reached a Pod")

	// More Pods than the limit, and a gap of the read.
	many := probeTestPlan()
	many.Found, many.LeftOut, many.Notes = 19, 16, []string{"zones not read: access to nodes denied"}
	v.acceptPlan(many, nil)
	frame = drawnText(t, v, 132, 30)
	require.Contains(t, frame, "16 more Pods beyond the limit of 16 are not listed and not probed · zones not read: access to nodes denied")

	// The Pods could not be read at all.
	v.plan = nil
	v.acceptPlan(nil, context.DeadlineExceeded)
	frame = drawnText(t, v, 100, 24)
	require.Contains(t, frame, "Nothing was read: context deadline exceeded.")
	require.Contains(t, frame, "No request was sent to any Pod.")
	require.Contains(t, v.runUnavailable(), "could not be read")
}

type probeTransportFunc func(*probe.Request) probe.Response

func (f probeTransportFunc) Get(_ context.Context, request *probe.Request) probe.Response {
	return f(request)
}

func TestProbeIsRegisteredAndBoundWhereItApplies(t *testing.T) {
	for gvr, kind := range map[*client.GVR]string{
		client.SvcGVR: probe.KindService, client.DpGVR: probe.KindDeployment, client.StsGVR: probe.KindStatefulSet,
		client.DsGVR: probe.KindDaemonSet, client.RsGVR: probe.KindReplicaSet, client.PodGVR: probe.KindPod,
	} {
		scope, err := probeScope(SelectedResourceTarget{Context: "ctx", GVR: gvr, Namespace: "gitlab", Name: "web", UID: "u"})
		require.NoError(t, err)
		require.Equal(t, probe.Scope{Kind: kind, Namespace: "gitlab", Name: "web", UID: "u"}, scope)
	}
	_, err := probeScope(SelectedResourceTarget{Context: "ctx", GVR: client.CmGVR, Namespace: "gitlab", Name: "web"})
	require.ErrorContains(t, err, "Probe times the Pods behind a Service, Deployment, StatefulSet, DaemonSet, ReplicaSet or one Pod")
	_, err = probeScope(SelectedResourceTarget{Context: "ctx", UnavailableReason: "nothing selected"})
	require.ErrorContains(t, err, "Select a Service, a workload or a Pod")

	v, _ := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)
	var found *ui.ActionDescriptor
	catalog := actionCatalog(v, v.app)
	for i := range catalog {
		if catalog[i].ID == probeActionID {
			found = &catalog[i]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, ":probe", found.Shortcut)
	require.Equal(t, ui.ActionInspect, found.Category)
	require.Contains(t, found.Label, "asks first")
	require.Empty(t, found.UnavailableReason, "a Pod under the cursor can be probed")
	command := NewCommand(v.app)
	command.updateSuggestionAliases()
	require.Contains(t, command.suggestionAliases(), probeCommandToken)

	label := func(key tcell.Key) string {
		action, ok := v.actions.Get(key)
		if !ok {
			return ""
		}
		return action.Description
	}
	require.Contains(t, label(ui.KeyM), "Graph", "m is the graph key, as in the other incident views")
	require.Contains(t, label(ui.KeyT), "imeline")
	require.Equal(t, "Run probe (asks first)", label(ui.KeyR))
	require.Equal(t, "Open pod", label(tcell.KeyEnter))
	require.Empty(t, label(ui.KeyDash), "the dash is the application's last-view key")

	// The digits are window keys in every chart view and a probe has no
	// window, so the count steps with + and _ through fixed choices.
	for index := range probe.AttemptChoices {
		require.Empty(t, label(ui.Key1+tcell.Key(index)), "a digit is not a count here")
	}
	require.Equal(t, "More requests per pod", label(probeMoreKey))
	require.Equal(t, "Fewer requests per pod", label(probeFewerKey))
	require.Equal(t, probe.DefaultAttempts, v.attempts)
	// The end of the choices says so; nothing consumes the flash line in a
	// test, so it is emptied here.
	said := func() string {
		text := ""
		for len(v.app.Flash().Channel()) > 0 {
			if message := <-v.app.Flash().Channel(); message.Text != "" {
				text = message.Text
			}
		}
		return text
	}
	said()
	var seen []int
	for range 4 {
		probePress(t, v, probeMoreKey)
		seen = append(seen, v.attempts)
	}
	require.Equal(t, []int{10, 20, 30, 30}, seen, "+ stops at the largest choice")
	require.Equal(t, "30 requests per pod is the most this view sends", said())
	seen = seen[:0]
	for range 6 {
		probePress(t, v, probeFewerKey)
		seen = append(seen, v.attempts)
	}
	require.Equal(t, []int{20, 10, 5, 3, 1, 1}, seen, "_ stops at one")
	require.Equal(t, "1 request per pod is the fewest this view sends", said())
	v.setAttempts(probe.DefaultAttempts)
	// The key line of all detail names a as the way back to the calm page.
	require.Equal(t, calmDetailLabel, label(calmDetailKey))
	probeDetail(t, v, true)
	require.Contains(t, drawnText(t, v, 140, 30), "r run · + more  _ fewer per pod (×5) · u liveness probe · Enter open pod · m graph · t timeline · Shift-W slow · a less detail · Esc back")
	frame := drawnText(t, v, 132, 30)
	require.Contains(t, frame, "r run · + more  _ fewer per pod (×5) · u liveness probe · Enter open pod · m graph · t timeline · Shift-W slow · a less · Esc back")
	require.Contains(t, drawnText(t, v, 80, 24), "r run · +/_ ×5 · u probe · Enter pod · m graph · Shift-W slow · a less · Esc\n")
	require.Contains(t, drawnText(t, v, 60, 24), "r run · +/_ ×5 · Enter pod · m graph · a less · Esc\n")

	// Shift-W is :slow here as on a Service row, for the selection the
	// probe was opened for.
	require.Equal(t, slowKeyLabel, label(slowKey))
	require.Empty(t, label(ui.KeyW), "w alone is not a second key for it")
	require.Contains(t, frame, "Shift-W slow")

	// The strip keeps a blank between two requests before a number column
	// is added: each request stands alone, as far as the width allows.
	require.Equal(t, probeColumns{pod: 42, strip: 12, fastest: 7, slowest: 7, over: 7, codes: 7, node: 8, zone: 12, verdict: 10, spaced: true},
		probeLayout(132, 5, 42, 8, 12))
	require.Equal(t, probeColumns{pod: 25, strip: 59, slowest: 7, over: 7, node: 8, verdict: 10, spaced: true}, probeLayout(132, 30, 42, 8, 12),
		"thirty requests keep their blanks at 132 columns; fastest and codes give way")
	require.Equal(t, probeColumns{pod: 23, strip: 9, slowest: 7, over: 7, node: 8, verdict: 10, spaced: true}, probeLayout(80, 5, 42, 8, 12))
	require.Equal(t, probeColumns{pod: 19, strip: 30, slowest: 7, verdict: 10}, probeLayout(80, 30, 42, 8, 12), "at 80 columns thirty requests stand side by side")
	require.Equal(t, probeColumns{pod: 20, strip: 9, slowest: 7, verdict: 10, spaced: true}, probeLayout(60, 5, 42, 8, 12))
	require.Equal(t, probeColumns{pod: 18, verdict: 10}, probeLayout(40, 30, 42, 8, 12), "pod, median and the verdict word are never given up")
	require.Equal(t, "…-6f9c7d5b84-hx4mt", strings.TrimSpace(probeTail(probePodC, 18, false)))
	require.Equal(t, "…-6f9c7d5b84-hx4mt", strings.TrimSpace(probeTail(probePodC, 20, false)), "cut at a dash, not inside \"default\"")
	require.Equal(t, "…-webservice-default-6f9c7d5b84-hx4mt", strings.TrimSpace(probeTail(probePodC, 38, false)))
	require.Equal(t, "…bcdefghijklmnopqrstuvwxyz", probeTail("0abcdefghijklmnopqrstuvwxyz", 26, false),
		"a name without a dash keeps as much of its end as fits")
	require.Equal(t, "~-6f9c7d5b84-hx4mt", strings.TrimSpace(probeTail(probePodC, 18, true)))
}
