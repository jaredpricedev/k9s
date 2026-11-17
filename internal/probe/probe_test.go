// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func httpProbe(path string, port any) map[string]any {
	return map[string]any{"httpGet": map[string]any{"path": path, "port": port}, "periodSeconds": int64(10)}
}

func webContainer() map[string]any {
	return map[string]any{
		"name":           "webservice",
		"ports":          []any{map[string]any{"name": "http-web", "containerPort": int64(8080)}},
		"readinessProbe": httpProbe("/-/readiness", "http-web"),
		"livenessProbe":  httpProbe("/-/liveness", int64(8080)),
		"env":            []any{map[string]any{"name": "TOKEN", "value": "PRIVATE-ENV"}},
	}
}

func testPod(name, node string, containers ...map[string]any) *unstructured.Unstructured {
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod"}}
	pod.SetNamespace("gitlab")
	pod.SetName(name)
	pod.SetUID(types.UID("uid-" + name))
	pod.SetLabels(map[string]string{"app": "web"})
	list := make([]any, 0, len(containers))
	for _, container := range containers {
		list = append(list, container)
	}
	pod.Object["spec"] = map[string]any{"nodeName": node, "containers": list}
	pod.Object["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	return pod
}

func plannedPods(names ...string) []Pod {
	pods := make([]Pod, 0, len(names))
	for _, name := range names {
		pods = append(pods, PodFromObject(testPod(name, "worker-a", webContainer()).Object))
	}
	return pods
}

func TestOnlyADeclaredHTTPProbeBecomesARequest(t *testing.T) {
	pod := PodFromObject(testPod("web-1", "worker-a", webContainer()).Object)
	require.Equal(t, "worker-a", pod.Node)
	require.True(t, pod.Ready)
	require.Len(t, pod.Probes, 2)
	require.Equal(t, HTTPGet{Kind: Readiness, Container: "webservice", Scheme: "http", Port: "8080", PortName: "http-web",
		Path: "/-/readiness", Period: 10 * time.Second}, pod.Probes[0], "a named port is resolved against the container's own ports")

	plan := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, []Pod{pod}, 1)
	require.Equal(t, Readiness, plan.Kind)
	require.Equal(t, []Kind{Readiness, Liveness}, plan.Available)
	request := plan.Targets[0].Request
	require.NotNil(t, request)
	require.Equal(t, "/api/v1/namespaces/gitlab/pods/web-1:8080/proxy/-/readiness", request.APIPath())
	require.Equal(t, "GET :8080/-/readiness", request.Display())
	plan.Select(plan.NextKind())
	require.Equal(t, "GET :8080/-/liveness", plan.Targets[0].Request.Display())

	exec := map[string]any{"name": "db", "readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"pg_isready"}}}}
	host := map[string]any{"name": "edge", "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/", "port": int64(80), "host": "example.test"}}}
	unnamed := map[string]any{"name": "app", "readinessProbe": httpProbe("/ok", "nope")}
	spaced := map[string]any{"name": "app", "readinessProbe": httpProbe("/a b", int64(80))}
	// A path that a server or proxy could resolve to another API path is
	// never sent: it would leave with this user's credentials.
	for _, path := range []string{"/../../../../api/v1/namespaces/kube-system/secrets", "/a/./b", "/ok/..", "/a/%2e%2e/b", "/a%2f..%2fb",
		"/%2E%2E", "/a\\..\\b", "//other.example/x", "http://other.example/x", "no-slash", "/a#b"} {
		require.False(t, usablePath(path), path)
		climbing := map[string]any{"name": "app", "readinessProbe": httpProbe(path, int64(80))}
		refused := NewPlan(Scope{Kind: KindPod, Namespace: "gitlab", Name: "p"}, []Pod{PodFromObject(testPod("p", "n", climbing).Object)}, 1)
		require.Nil(t, refused.Targets[0].Request, path)
		require.Contains(t, refused.Targets[0].Skip, "path cannot be sent as written", path)
	}
	for _, path := range []string{"/", "/-/readiness", "/healthz?full=1&x=..", "/a.b/c..d/.well-known/x", "/api/v1/health%20check"} {
		require.True(t, usablePath(path), path)
	}
	pending := testPod("pending", "", webContainer())
	pending.Object["status"] = map[string]any{"phase": "Pending"}
	leaving := testPod("leaving", "worker-a", webContainer())
	leaving.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
	for name, want := range map[string]string{
		"exec":    "no declared HTTP probe (readiness exec only)",
		"plain":   "no declared HTTP probe",
		"host":    "its readinessProbe names its own host",
		"unnamed": `port "nope" is not a port the container declares`,
		"spaced":  "path cannot be sent as written",
		"pending": "phase Pending; only a running Pod is contacted",
		"leaving": "terminating; not contacted",
	} {
		object := map[string]*unstructured.Unstructured{
			"exec": testPod("p", "n", exec), "plain": testPod("p", "n", map[string]any{"name": "app"}), "host": testPod("p", "n", host),
			"unnamed": testPod("p", "n", unnamed), "spaced": testPod("p", "n", spaced), "pending": pending, "leaving": leaving,
		}[name]
		skipped := NewPlan(Scope{Kind: KindPod, Namespace: "gitlab", Name: "p"}, []Pod{PodFromObject(object.Object)}, 1)
		require.Nil(t, skipped.Targets[0].Request, name)
		require.Contains(t, skipped.Targets[0].Skip, want, name)
		require.Zero(t, skipped.Sendable(), name)
	}

	https := webContainer()
	https["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": "/health?token=abc&full=1", "port": float64(8443), "scheme": "HTTPS",
		"httpHeaders": []any{map[string]any{"name": "Authorization", "value": "Bearer PRIVATE-HEADER"}}}, "periodSeconds": float64(3)}
	secure := NewPlan(Scope{}, []Pod{PodFromObject(testPod("tls", "n", https).Object)}, 1).Targets[0]
	require.Equal(t, "https:tls:8443", secure.Request.ProxyTarget())
	require.Equal(t, "GET https :8443/health?token=…&full=…", secure.Request.Display(), "query values can be credentials and are masked")
	require.Equal(t, 1, secure.HeadersOmitted, "a declared header is counted, never read or sent")
	require.Equal(t, 3*time.Second, secure.Period)
	require.NotContains(t, fmt.Sprintf("%+v %+v", secure, *secure.Request), "PRIVATE")
}

func TestPlanIsBoundedAndASignatureCoversExactlyOneRequestShape(t *testing.T) {
	names := make([]string, 0, 20)
	for i := range 20 {
		names = append(names, fmt.Sprintf("web-%02d", i))
	}
	plan := NewPlan(Scope{Kind: KindDeployment, Namespace: "gitlab", Name: "web"}, plannedPods(names...), 23)
	require.Len(t, plan.Targets, MaxPods)
	require.Equal(t, 23, plan.Found)
	require.Equal(t, 7, plan.LeftOut, "Pods beyond the limit are counted, not contacted")
	require.Equal(t, "web-00", plan.Targets[0].Pod.Name)
	require.Equal(t, MaxPods, plan.Sendable())
	requests, pods := plan.Requests()
	require.Equal(t, []string{"GET :8080/-/readiness"}, requests)
	require.Equal(t, []int{MaxPods}, pods)

	five := plan.Signature(5)
	require.NotEqual(t, five, plan.Signature(10), "another count is another request shape")
	plan.Select(Liveness)
	require.NotEqual(t, five, plan.Signature(5), "another path is another request shape")
	plan.Select(Readiness)
	require.Equal(t, five, plan.Signature(5))

	require.Equal(t, DefaultAttempts, ClampAttempts(0))
	require.Equal(t, MaxAttempts, ClampAttempts(500))
	require.Equal(t, MaxAttempts, AttemptChoices[len(AttemptChoices)-1])
}

// fakeTransport answers from a table and records what it was asked.
type fakeTransport struct {
	mu       sync.Mutex
	answer   func(pod string, n int) Response
	calls    map[string]int
	inFlight map[string]int
	together atomic.Int32
	peak     atomic.Int32
	overlap  atomic.Bool
	wait     time.Duration
}

func (f *fakeTransport) Get(ctx context.Context, request *Request) Response {
	f.mu.Lock()
	if f.calls == nil {
		f.calls, f.inFlight = map[string]int{}, map[string]int{}
	}
	n := f.calls[request.Pod]
	f.calls[request.Pod]++
	f.inFlight[request.Pod]++
	if f.inFlight[request.Pod] > 1 {
		f.overlap.Store(true)
	}
	f.mu.Unlock()
	if now := f.together.Add(1); now > f.peak.Load() {
		f.peak.Store(now)
	}
	defer func() {
		f.together.Add(-1)
		f.mu.Lock()
		f.inFlight[request.Pod]--
		f.mu.Unlock()
	}()
	if f.wait > 0 {
		select {
		case <-time.After(f.wait):
		case <-ctx.Done():
			return Response{}
		}
	}
	return f.answer(request.Pod, n)
}

func ok(took time.Duration) Response { return Response{Outcome: Answered, Code: 200, Took: took} }

func TestExecuteAsksEachPodInTurnAndABoundedNumberAtOnce(t *testing.T) {
	names := make([]string, 0, 9)
	for i := range 9 {
		names = append(names, fmt.Sprintf("web-%d", i))
	}
	plan := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods(names...), 9)
	transport := &fakeTransport{wait: 2 * time.Millisecond, answer: func(string, int) Response { return ok(10 * time.Millisecond) }}
	// Every request is reported twice: when it leaves, so that a caller knows
	// what is in flight, and again with its outcome.
	var left, reported, disorder atomic.Int32
	var mu sync.Mutex
	state := map[[2]int]Outcome{}
	run := Execute(t.Context(), transport, plan, Options{Attempts: 3, Pause: time.Millisecond}, func(pod, n int, a Attempt) {
		mu.Lock()
		defer mu.Unlock()
		before, seen := state[[2]int{pod, n}]
		switch {
		case a.Outcome == InFlight && !seen:
			left.Add(1)
		case a.Outcome == Answered && seen && before == InFlight:
			reported.Add(1)
		default:
			disorder.Add(1)
		}
		state[[2]int{pod, n}] = a.Outcome
	})
	require.Zero(t, disorder.Load(), "in flight first, then the outcome, once each")
	require.Equal(t, 27, run.Planned())
	require.Equal(t, 27, run.Sent())
	require.Equal(t, 27, run.Finished())
	require.Zero(t, run.Abandoned())
	require.EqualValues(t, 27, left.Load())
	require.EqualValues(t, 27, reported.Load())
	require.Empty(t, run.Stopped)
	require.False(t, transport.overlap.Load(), "one Pod is never asked two requests at once")
	require.LessOrEqual(t, int(transport.peak.Load()), Concurrency)
	require.Greater(t, int(transport.peak.Load()), 1, "Pods are probed side by side")
	for _, name := range names {
		require.Equal(t, 3, transport.calls[name])
	}
}

func TestExecuteStopsOnCancelDeadlineAndRefusal(t *testing.T) {
	plan := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods("a", "b", "c"), 3)

	// Cancel: what was not sent stays unsent. The three requests in flight
	// did leave, so they are counted as sent and marked abandoned: the
	// application may still handle them, and saying "not sent" would be
	// false.
	ctx, cancel := context.WithCancel(t.Context())
	slow := &fakeTransport{wait: time.Hour, answer: func(string, int) Response { return ok(time.Millisecond) }}
	time.AfterFunc(30*time.Millisecond, cancel)
	run := Execute(ctx, slow, plan, Options{Attempts: 5, Pause: time.Millisecond}, nil)
	require.Equal(t, "canceled", run.Stopped)
	require.Equal(t, 15, run.Planned())
	require.Equal(t, 3, run.Sent(), "one request per Pod had left")
	require.Equal(t, 3, slow.calls["a"]+slow.calls["b"]+slow.calls["c"], "and the count agrees with what the transport saw")
	require.Zero(t, run.Finished())
	require.Equal(t, 3, run.Abandoned())
	canceled := Summarize(plan, run, nil)
	for i := range canceled.Pods {
		require.Equal(t, Canceled, canceled.Pods[i].Verdict)
		require.False(t, canceled.Pods[i].Verdict.Flagged(), "an abandoned request says nothing about the Pod")
	}
	require.Equal(t, []string{"Canceled: 3 of 15 requests were sent, 3 of them still in flight and abandoned without an answer " +
		"(the application may still handle them); the rest were not."}, Result(plan, run, &canceled, false))

	// A caller that stops waiting marks what it knows to be in flight.
	waiting := NewRun(plan, Options{Attempts: 2})
	waiting.Pods[0].Attempts[0] = Attempt{Outcome: Answered, Code: 200, Took: time.Millisecond}
	waiting.Pods[0].Attempts[1] = Attempt{Outcome: InFlight}
	require.Equal(t, 2, waiting.Sent())
	require.Equal(t, 1, waiting.Finished())
	require.Equal(t, 1, waiting.Abandon())
	require.Equal(t, Abandoned, waiting.Pods[0].Attempts[1].Outcome)
	require.Zero(t, waiting.Abandon())

	// A request that outlives its deadline is a timeout that took at least
	// the deadline; the next request is still sent.
	stalls := &fakeTransport{answer: func(string, int) Response { return ok(time.Millisecond) }}
	stalls.wait = 50 * time.Millisecond
	run = Execute(t.Context(), stalls, plan, Options{Attempts: 2, Deadline: 5 * time.Millisecond, Pause: time.Millisecond}, nil)
	require.Empty(t, run.Stopped)
	for _, pod := range run.Pods {
		for _, attempt := range pod.Attempts {
			require.Equal(t, Attempt{Outcome: TimedOut, Took: 5 * time.Millisecond, Detail: "no answer within the deadline"}, attempt)
		}
	}

	// A refusal by the API server ends the run for every Pod: the rule that
	// refused one request refuses the rest.
	denied := &fakeTransport{answer: func(string, int) Response { return Response{Outcome: Denied, Code: 403, Detail: "forbidden"} }}
	run = Execute(t.Context(), denied, plan, Options{Attempts: 5, Pause: time.Millisecond}, nil)
	require.Equal(t, "denied", run.Stopped)
	require.LessOrEqual(t, run.Sent(), 3, "at most the first request of each Pod left")
	summary := Summarize(plan, run, nil)
	require.True(t, summary.Denied)
	lines := Result(plan, run, &summary, false)
	require.Contains(t, lines[0], `RBAC verb "get" on resource "pods/proxy" (core API group) in namespace "gitlab"`)
	require.Contains(t, lines[1], "No request reached a Pod")

	// A rule limited to some Pods by name refuses the others: answers that
	// did arrive are not denied after the fact.
	partly := NewRun(plan, Options{Attempts: 2})
	partly.Pods[0].Attempts[0] = Attempt{Outcome: Answered, Code: 200, Took: 20 * time.Millisecond}
	partly.Pods[1].Attempts[0] = Attempt{Outcome: Denied, Code: 403, Detail: "forbidden"}
	partly.Stopped = "denied"
	summary = Summarize(plan, partly, nil)
	lines = Result(plan, partly, &summary, false)
	require.Contains(t, lines[0], "Not allowed")
	require.NotContains(t, strings.Join(lines, " "), "No request reached a Pod")
	require.Contains(t, lines[1], "1 request was answered before the refusal ended the run")

	// A Pod that is gone is not asked again; the others are.
	gone := &fakeTransport{answer: func(pod string, _ int) Response {
		if pod == "b" {
			return Response{Outcome: Gone, Code: 404, Detail: "the Pod is no longer there"}
		}
		return ok(time.Millisecond)
	}}
	run = Execute(t.Context(), gone, plan, Options{Attempts: 4, Pause: time.Millisecond}, nil)
	require.Equal(t, 1, gone.calls["b"])
	require.Equal(t, 4, gone.calls["a"])
	require.Equal(t, 9, run.Sent())
}

func runOf(plan *Plan, attempts int, took map[string][]time.Duration) *Run {
	run := NewRun(plan, Options{Attempts: attempts})
	for i := range plan.Targets {
		for n, d := range took[plan.Targets[i].Pod.Name] {
			switch {
			case d < 0:
				run.Pods[i].Attempts[n] = Attempt{Outcome: TimedOut, Took: run.Deadline}
			default:
				run.Pods[i].Attempts[n] = Attempt{Outcome: Answered, Code: 200, Took: d}
			}
		}
	}
	return run
}

func ms(values ...int) []time.Duration {
	out := make([]time.Duration, 0, len(values))
	for _, v := range values {
		out = append(out, time.Duration(v)*time.Millisecond)
	}
	return out
}

func TestSummaryNamesTheOutlierAgainstItsPeers(t *testing.T) {
	plan := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"},
		plannedPods("webservice-6f9c7d5b84-2bq7k", "webservice-6f9c7d5b84-8wzpd", "webservice-6f9c7d5b84-hx4mt"), 3)
	run := runOf(plan, 5, map[string][]time.Duration{
		"webservice-6f9c7d5b84-2bq7k": ms(20, 24, 22, 31, 18),
		"webservice-6f9c7d5b84-8wzpd": ms(19, 25, 21, 40, 23),
		"webservice-6f9c7d5b84-hx4mt": ms(1800, 3100, 3670, 2100, -1),
	})
	summary := Summarize(plan, run, nil)
	require.True(t, summary.Enough)
	require.Equal(t, 3, summary.Compared)
	require.Equal(t, []Verdict{LikePeers, LikePeers, Outlier}, []Verdict{summary.Pods[0].Verdict, summary.Pods[1].Verdict, summary.Pods[2].Verdict})
	slow := summary.Pods[2]
	require.Equal(t, "3.1s median vs peers 22.5ms", slow.Mark(false), "the mark states the Pod's median against the median of its peers")
	require.Equal(t, 1, slow.TimedOut)
	require.Equal(t, "≥5s", slow.WorstText(false))
	require.Equal(t, ">=5s", slow.WorstText(true))
	require.Equal(t, "200", slow.CodesText())
	require.Equal(t, "200 ×4", slow.CodesDetail(false))
	require.Equal(t, 0, summary.FloorPod)
	require.Equal(t, 22*time.Millisecond, summary.Floor, "the fastest Pod's median is the floor of the proxy path")
	require.InDelta(t, 4.0, summary.Top, 1e-9, "the slowest answer, rounded up, is a full bar")

	lines := Result(plan, run, &summary, false)
	require.Equal(t, "Only …-hx4mt answered slowly: median 3.1s against 22.5ms for its 2 peers; 1 of 5 had no answer within 5s.", lines[0])
	require.Equal(t, "The other 2 Pods answered all 10 requests in 40ms or less.", lines[1])
	require.Contains(t, lines[2], "did not pass the ingress or the Service")
	text := strings.ToLower(strings.Join(lines, " "))
	for _, claim := range []string{"because", "cause", "healthy", "root"} {
		require.NotContains(t, text, claim, "a difference between Pods is stated, not explained")
	}
	require.Equal(t, "Only ...-hx4mt answered slowly: median 3.1s against 22.5ms for its 2 peers; 1 of 5 had no answer within 5s.",
		Result(plan, run, &summary, true)[0])
}

func TestSummaryKeepsJitterSpikesFewPeersAndErrorsApart(t *testing.T) {
	plan := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods("web-a", "web-b", "web-c"), 3)

	// Jitter between fast Pods is not a difference.
	alike := runOf(plan, 3, map[string][]time.Duration{"web-a": ms(6, 9, 40), "web-b": ms(12, 30, 21), "web-c": ms(35, 80, 90)})
	summary := Summarize(plan, alike, nil)
	for i := range summary.Pods {
		require.Equal(t, LikePeers, summary.Pods[i].Verdict, "a Pod %d ms slower than its peers is like them", i)
	}
	lines := Result(plan, alike, &summary, false)
	require.Contains(t, lines[0], "All 3 Pods answered alike: medians 9ms to 80ms")
	require.Contains(t, lines[1], "An intermittent stall can fall between 3 requests per Pod")
	require.InDelta(t, 0.15, summary.Top, 1e-9, "ordinary jitter is drawn low: a full bar is five times the usual answer")

	// One slow answer among fast ones: the median hides it, the slowest
	// answer does not.
	spiky := runOf(plan, 5, map[string][]time.Duration{"web-a": ms(20, 22, 21, 25, 19), "web-b": ms(18, 24, 20, 23, 22), "web-c": ms(21, -1, 20, 22, 19)})
	summary = Summarize(plan, spiky, nil)
	require.Equal(t, Spikes, summary.Pods[2].Verdict)
	require.Equal(t, "slowest ≥5s vs peers 24.5ms", summary.Pods[2].Mark(false))
	require.Contains(t, Result(plan, spiky, &summary, false)[0], "Only web-c was slow some of the time: slowest ≥5s against 24.5ms for its 2 peers, median 21ms; 1 of 5 had no answer within 5s.")

	// Two Pods cannot say which of them is the odd one.
	pair := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods("web-a", "web-b"), 2)
	two := runOf(pair, 3, map[string][]time.Duration{"web-a": ms(20, 22, 21), "web-b": ms(3000, 2900, 3100)})
	summary = Summarize(pair, two, nil)
	require.False(t, summary.Enough)
	require.Equal(t, []Verdict{NoPeers, NoPeers}, []Verdict{summary.Pods[0].Verdict, summary.Pods[1].Verdict})
	require.Contains(t, Result(pair, two, &summary, false)[0], "Fewer than 3 Pods is too few to call one of them different")

	// An answer that is not a success, and no answer at all.
	failing := runOf(plan, 2, map[string][]time.Duration{"web-a": ms(20, 22), "web-b": ms(21, 19), "web-c": ms(20, 20)})
	failing.Pods[1].Attempts[1] = Attempt{Outcome: Answered, Code: 503, Took: 20 * time.Millisecond}
	failing.Pods[2].Attempts = []Attempt{{Outcome: Unreachable, Code: 503, Detail: "connection refused"}, {Outcome: Unreachable, Code: 503, Detail: "connection refused"}}
	summary = Summarize(plan, failing, nil)
	require.Equal(t, Errors, summary.Pods[1].Verdict)
	require.Equal(t, NoAnswer, summary.Pods[2].Verdict)
	require.Equal(t, "200 503", summary.Pods[1].CodesText())
	text := strings.Join(Result(plan, failing, &summary, false), "\n")
	require.Contains(t, text, "web-b did not always answer with success: 200 ×1, 503 ×1.")
	require.Contains(t, text, "web-c gave no answer to 2 requests (connection refused).")

	// Nothing sent, nothing said.
	require.Nil(t, Result(plan, NewRun(plan, Options{}), &summary, false))
	unsent := Summarize(plan, nil, nil)
	require.Equal(t, Unprobed, unsent.Pods[0].Verdict)
	require.Equal(t, "not sent", Unprobed.Word())
}

// A request without an answer is stated for what it is, and Pods that
// differ widely are not said to answer alike because no single one differs.
func TestSummaryFlagsTimeoutsOnTheirOwnAndDoesNotCallAWideSpreadAlike(t *testing.T) {
	plan := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods("web-a", "web-b", "web-c"), 3)

	// A request without an answer is a fact about that Pod, not a comparison:
	// it is flagged when every Pod has one, where nothing "stands out".
	stalled := runOf(plan, 3, map[string][]time.Duration{"web-a": ms(20, -1, 21), "web-b": ms(-1, 19, 22), "web-c": ms(20, 23, -1)})
	summary := Summarize(plan, stalled, nil)
	for i := range summary.Pods {
		require.Equal(t, Timeouts, summary.Pods[i].Verdict)
		require.True(t, summary.Pods[i].Verdict.Flagged())
	}
	require.Equal(t, "TIMEOUTS", Timeouts.Word())
	text := strings.Join(Result(plan, stalled, &summary, false), "\n")
	require.Contains(t, text, "web-a gave no answer within 5s to 1 of 3 requests; its 2 answers took 20ms to 21ms.")
	require.NotContains(t, text, "alike")
	require.NotContains(t, text, "stands out")

	// About half of the Pods slow: the peer rule cannot name one, and the
	// result does not call that "alike".
	four := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods("web-a", "web-b", "web-c", "web-d"), 4)
	split := runOf(four, 3, map[string][]time.Duration{"web-a": ms(20, 22, 21), "web-b": ms(19, 21, 20), "web-c": ms(3000, 2900, 3100), "web-d": ms(2800, 3000, 3200)})
	summary = Summarize(four, split, nil)
	for i := range summary.Pods {
		require.Equal(t, LikePeers, summary.Pods[i].Verdict)
	}
	lines := Result(four, split, &summary, false)
	require.Contains(t, lines[0], "The 4 Pods did not answer alike: medians 20ms to 3s")
	require.Contains(t, lines[0], "cannot name one when several are slow")
	require.NotContains(t, strings.Join(lines, " "), "answered alike")

	// Two Pods cannot be compared, but a request that got no answer is said
	// of two Pods, and of one.
	pair := NewPlan(Scope{Kind: KindService, Namespace: "gitlab", Name: "web"}, plannedPods("web-a", "web-b"), 2)
	hung := runOf(pair, 5, map[string][]time.Duration{"web-a": ms(20, 22, 21, 19, 23), "web-b": ms(21, -1, 20, -1, 22)})
	summary = Summarize(pair, hung, nil)
	require.Equal(t, []Verdict{NoPeers, Timeouts}, []Verdict{summary.Pods[0].Verdict, summary.Pods[1].Verdict})
	lines = Result(pair, hung, &summary, false)
	require.Equal(t, "web-b gave no answer within 5s to 2 of 5 requests; its 3 answers took 20ms to 22ms.", lines[0])
	require.Equal(t, "The other Pod answered all 5 requests in 23ms or less.", lines[1])
	single := NewPlan(Scope{Kind: KindPod, Namespace: "gitlab", Name: "web-a"}, plannedPods("web-a"), 1)
	alone := runOf(single, 3, map[string][]time.Duration{"web-a": ms(20, -1, 21)})
	summary = Summarize(single, alone, nil)
	require.Equal(t, Timeouts, summary.Pods[0].Verdict)
}

func proxyStatus(code int, reason, message string) string {
	return fmt.Sprintf(`{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":%q,"reason":%q,"code":%d}`, message, reason, code)
}

func TestAPITransportSendsAPlainGetAndTellsThePodFromTheAPIServer(t *testing.T) {
	type seen struct {
		method, path, query string
		body                int64
		headers             []string
	}
	var (
		mu       sync.Mutex
		requests []seen
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := seen{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: r.ContentLength}
		for name := range r.Header {
			entry.headers = append(entry.headers, name)
		}
		mu.Lock()
		requests = append(requests, entry)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/pods/denied:"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(proxyStatus(403, "Forbidden", `pods "denied" is forbidden: User "u" cannot get resource "pods/proxy"`)))
		case strings.Contains(r.URL.Path, "/pods/gone:"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(proxyStatus(404, "NotFound", `pods "gone" not found`)))
		case strings.Contains(r.URL.Path, "/pods/closed:"):
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(proxyStatus(503, "ServiceUnavailable", "error trying to reach service: dial tcp 10.0.0.1:80: connect: connection refused")))
		case strings.Contains(r.URL.Path, "/pods/unready:"):
			// The application's own 503, with a body that is not a Status.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"failed","token":"PRIVATE-BODY"}`))
		case strings.Contains(r.URL.Path, "/pods/moved:"):
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case strings.Contains(r.URL.Path, "/pods/slow:"):
			time.Sleep(60 * time.Millisecond)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case strings.Contains(r.URL.Path, "/pods/large:"):
			_, _ = w.Write([]byte(strings.Repeat("x", 64*MaxResponseBytes)))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer server.Close()
	transport, err := NewAPITransport(&rest.Config{Host: server.URL, BearerToken: "PRIVATE-TOKEN"})
	require.NoError(t, err)
	defer transport.Close()
	get := func(pod, path string) Response {
		return transport.Get(t.Context(), &Request{Namespace: "gitlab", Pod: pod, Scheme: "http", Port: "8080", Path: path})
	}

	answer := get("web", "/-/readiness?full=1")
	require.Equal(t, Answered, answer.Outcome)
	require.Equal(t, 200, answer.Code)
	require.Positive(t, answer.Took)
	require.Equal(t, "GET", requests[0].method)
	require.Equal(t, "/api/v1/namespaces/gitlab/pods/web:8080/proxy/-/readiness", requests[0].path)
	require.Equal(t, "full=1", requests[0].query)
	require.LessOrEqual(t, requests[0].body, int64(0), "no body is sent")
	for _, header := range requests[0].headers {
		require.Contains(t, []string{"Authorization", "User-Agent", "Accept-Encoding"}, header,
			"only what the Kubernetes connection itself adds; the API server removes the credential before it forwards")
	}

	require.Equal(t, Response{Outcome: Denied, Code: 403, Detail: "forbidden"}, withoutTime(get("denied", "/")))
	require.Equal(t, Response{Outcome: Gone, Code: 404, Detail: "the Pod is no longer there"}, withoutTime(get("gone", "/")))
	require.Equal(t, Response{Outcome: Unreachable, Code: 503, Detail: "connection refused"}, withoutTime(get("closed", "/")))
	require.Equal(t, Response{Outcome: Answered, Code: 503}, withoutTime(get("unready", "/")), "a 503 of the application is its answer")
	require.Equal(t, Response{Outcome: Answered, Code: 302}, withoutTime(get("moved", "/")), "a redirect is an answer and is not followed")
	require.Equal(t, Answered, get("large", "/").Outcome, "a large body is cut, not read")
	for _, entry := range requests {
		require.NotEqual(t, "/elsewhere", entry.path)
	}
	slow := get("slow", "/")
	require.GreaterOrEqual(t, slow.Took, 50*time.Millisecond, "the time is the wait for the first byte")

	attempt, sent := send(t.Context(), transport, &Request{Namespace: "gitlab", Pod: "slow", Scheme: "http", Port: "8080", Path: "/"}, 10*time.Millisecond)
	require.True(t, sent)
	require.Equal(t, Attempt{Outcome: TimedOut, Took: 10 * time.Millisecond, Detail: "no answer within the deadline"}, attempt)

	refused := transport.Get(t.Context(), &Request{Namespace: "gitlab", Pod: "web", Scheme: "http", Port: "8080", Path: "/a b"})
	require.Equal(t, Failed, refused.Outcome, "a path that cannot be sent as written is not sent")
	for _, response := range []Response{answer, slow, refused, get("unready", "/"), get("denied", "/")} {
		require.NotContains(t, fmt.Sprintf("%+v", response), "PRIVATE", "no body, token or server message is retained")
	}

	_, err = NewAPITransport(nil)
	require.ErrorContains(t, err, "no Kubernetes connection")
	secure := Request{Namespace: "gitlab", Pod: "web", Scheme: "https", Port: "8443", Path: "/h"}
	target, err := transport.url(&secure)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(target, "/api/v1/namespaces/gitlab/pods/https:web:8443/proxy/h"), target)
}

func withoutTime(r Response) Response {
	r.Took = 0
	return r
}

func controlled(object *unstructured.Unstructured, kind, name, uid string) *unstructured.Unstructured {
	yes := true
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID(uid), Controller: &yes}})
	return object
}

func cluster(t *testing.T) *fake.FakeDynamicClient {
	t.Helper()
	object := func(apiVersion, kind, namespace, name, uid string) *unstructured.Unstructured {
		o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
		o.SetNamespace(namespace)
		o.SetName(name)
		o.SetUID(types.UID(uid))
		return o
	}
	selector := map[string]any{"matchLabels": map[string]any{"app": "web"}}
	service := object("v1", "Service", "gitlab", "web", "svc-uid")
	service.Object["spec"] = map[string]any{"type": "ClusterIP", "selector": map[string]any{"app": "web"},
		"ports": []any{map[string]any{"name": "http", "port": int64(8181), "protocol": "TCP"}}}
	deployment := object("apps/v1", "Deployment", "gitlab", "web", "dp-uid")
	deployment.Object["spec"] = map[string]any{"selector": selector}
	set := controlled(object("apps/v1", "ReplicaSet", "gitlab", "web-6f9c", "rs-uid"), "Deployment", "web", "dp-uid")
	set.SetLabels(map[string]string{"app": "web"})
	set.Object["spec"] = map[string]any{"selector": selector}
	foreign := controlled(object("apps/v1", "ReplicaSet", "gitlab", "other-1", "rs-other"), "Deployment", "other", "dp-other")
	foreign.SetLabels(map[string]string{"app": "web"})
	objects := []runtime.Object{service, deployment, set, foreign}
	for name, node := range map[string]string{"web-6f9c-a": "worker-d", "web-6f9c-b": "worker-e", "web-6f9c-c": "worker-f"} {
		objects = append(objects, controlled(testPod(name, node, webContainer()), "ReplicaSet", "web-6f9c", "rs-uid"))
	}
	// Same labels, another controller: behind the Service, not of the Deployment.
	objects = append(objects, controlled(testPod("other-1-x", "worker-d", map[string]any{"name": "sidecar"}), "ReplicaSet", "other-1", "rs-other"))
	for name, zone := range map[string]string{"worker-d": "zone-a", "worker-e": "zone-b"} {
		node := object("v1", "Node", "", name, "node-"+name)
		node.SetLabels(map[string]string{"topology.kubernetes.io/zone": zone})
		objects = append(objects, node)
	}
	kinds := map[schema.GroupVersionResource]string{}
	for resource, kind := range map[schema.GroupVersionResource]string{
		svcResource: "ServiceList", podsResource: "PodList", nodesResource: "NodeList", rsResource: "ReplicaSetList",
		workloads[KindDeployment]:  "DeploymentList",
		workloads[KindStatefulSet]: "StatefulSetList", workloads[KindDaemonSet]: "DaemonSetList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}:      "EndpointSliceList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}:          "IngressList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}:    "NetworkPolicyList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
	} {
		kinds[resource] = kind
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...)
}

func TestResolveReadsObjectsOnlyAndVerifiesWhoIsBehindTheSelection(t *testing.T) {
	objects := cluster(t)

	// A Deployment: its Pods through its ReplicaSets, not every Pod that
	// happens to carry the labels.
	plan, err := Resolve(t.Context(), objects, "study", Scope{Kind: KindDeployment, Namespace: "gitlab", Name: "web", UID: "dp-uid"})
	require.NoError(t, err)
	require.Len(t, plan.Targets, 3)
	require.Equal(t, 3, plan.Sendable())
	require.Equal(t, []string{"zone-a", "zone-b", ""}, []string{plan.Targets[0].Pod.Zone, plan.Targets[1].Pod.Zone, plan.Targets[2].Pod.Zone})
	require.Equal(t, []string{"zones not read: read failed"}, plan.Notes, "a zone that was not read is reported, not left looking absent")
	require.False(t, plan.ReadAt.IsZero())

	// A Service: the backends the network path collector resolves.
	plan, err = Resolve(t.Context(), objects, "study", Scope{Kind: KindService, Namespace: "gitlab", Name: "web"})
	require.NoError(t, err)
	require.Len(t, plan.Targets, 4)
	require.Equal(t, 3, plan.Sendable())
	require.Equal(t, "other-1-x", plan.Targets[0].Pod.Name)
	require.Equal(t, "no declared HTTP probe", plan.Targets[0].Skip)

	// One Pod.
	plan, err = Resolve(t.Context(), objects, "study", Scope{Kind: KindPod, Namespace: "gitlab", Name: "web-6f9c-a", UID: "uid-web-6f9c-a"})
	require.NoError(t, err)
	require.Len(t, plan.Targets, 1)
	require.Equal(t, "worker-d", plan.Targets[0].Pod.Node)

	// Every request so far was a read of a Kubernetes object.
	for _, action := range objects.Actions() {
		require.Contains(t, []string{"get", "list"}, action.GetVerb())
		require.Empty(t, action.GetSubresource(), "building a plan never touches pods/proxy")
	}

	_, err = Resolve(t.Context(), objects, "study", Scope{Kind: KindPod, Namespace: "gitlab", Name: "web-6f9c-a", UID: "another"})
	require.ErrorContains(t, err, "was replaced since it was selected")
	_, err = Resolve(t.Context(), objects, "study", Scope{Kind: KindDeployment, Namespace: "gitlab", Name: "missing"})
	require.ErrorContains(t, err, "Deployment gitlab/missing: not found")
	_, err = Resolve(t.Context(), objects, "study", Scope{Kind: "ConfigMap", Namespace: "gitlab", Name: "x"})
	require.ErrorContains(t, err, "select a Service, Deployment")
	_, err = Resolve(t.Context(), nil, "study", Scope{Kind: KindPod, Namespace: "gitlab", Name: "x"})
	require.Error(t, err)
	require.True(t, Accepts(KindStatefulSet))
	require.False(t, Accepts("Node"))
}

type reviewer struct {
	allowed bool
	err     error
	asked   *authorizationv1.ResourceAttributes
}

//nolint:gocritic // The signature is the access review client's.
func (r *reviewer) Create(_ context.Context, review *authorizationv1.SelfSubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SelfSubjectAccessReview, error) {
	r.asked = review.Spec.ResourceAttributes
	review.Status.Allowed = r.allowed
	return review, r.err
}

func TestAccessReviewNamesTheMissingRule(t *testing.T) {
	yes := &reviewer{allowed: true}
	access, reason := CheckAccess(t.Context(), yes, "gitlab")
	require.Equal(t, AccessAllowed, access)
	require.Empty(t, reason)
	require.Equal(t, &authorizationv1.ResourceAttributes{Namespace: "gitlab", Verb: "get", Resource: "pods", Subresource: "proxy"}, yes.asked)

	access, reason = CheckAccess(t.Context(), &reviewer{}, "gitlab")
	require.Equal(t, AccessDenied, access)
	require.Equal(t, `missing RBAC verb "get" on resource "pods/proxy" (core API group) in namespace "gitlab"`, reason)

	access, reason = CheckAccess(t.Context(), &reviewer{err: context.DeadlineExceeded}, "gitlab")
	require.Equal(t, AccessUnknown, access, "no answer is not a refusal")
	require.NotEmpty(t, reason)
	access, _ = CheckAccess(t.Context(), nil, "gitlab")
	require.Equal(t, AccessUnknown, access)
}
