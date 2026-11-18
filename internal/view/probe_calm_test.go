// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/probe"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// The calm page after a run: the sentence first, one row per Pod with the
// Pod that stands out first, one line for what the time covers and two
// lines of keys. 132x33, 80x21 and 60x21 are what the application leaves
// the view in terminals of 132x36, 80x24 and 60x24.
func TestProbeCalmPageStatesTheAnswerFirstAtEverySize(t *testing.T) {
	v, story := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)
	probeFinish(t, v, story)
	require.Equal(t, probePodC, v.selected, "the cursor starts on the Pod that differs")
	require.NotContains(t, drawnText(t, v, 132, 33), "m graph its CPU", "a check whose key would be refused is not proposed")
	declareStudySources(t, v.app)
	v.render()
	for _, size := range [][2]int{{132, 33}, {120, 30}, {80, 21}, {60, 21}, {40, 8}} {
		frame := drawnText(t, v, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			continue
		}
		lines := strings.Split(frame, "\n")
		for _, line := range lines {
			require.LessOrEqual(t, runewidth.StringWidth(line), size[0], line)
		}
		flat := strings.Join(strings.Fields(frame), " ")
		// The answer comes first, and the caveat that keeps it honest.
		require.True(t, strings.HasPrefix(lines[0], "Probe  Service gitlab/gitlab-"), lines[0])
		require.True(t, strings.HasPrefix(lines[2], "  Only hx4mt answered slowly: median 2.73s against "), "%d columns: %q", size[0], lines[2])
		require.Contains(t, flat, "1 of 5 had no answer within 5s.")
		require.Contains(t, flat, "The other 2 Pods answered all 10 requests in")
		require.Contains(t, flat, probeDifferenceCaveat)
		// A Pod, where it runs, its requests, its median and its word are
		// one row, and a Pod has one row.
		require.Regexp(t, `(?m)^> hx4mt +worker-f .*\S +2\.73s .*OUTLIER$`, frame, "%d columns", size[0])
		require.Regexp(t, `(?m)^  2bq7k +worker-d .*\S +\d+ms$`, frame, "%d columns", size[0])
		require.Equal(t, 1, strings.Count(frame, "hx4mt  worker-f"))
		// The Pod that stands out comes first, under its own label.
		slow, outlier := strings.Index(frame, probeSlowLabel), strings.Index(frame, "hx4mt  worker-f")
		alike, peer := strings.Index(frame, probeAlikeLabel), strings.Index(frame, "2bq7k  worker-d")
		require.True(t, 0 < slow && slow < outlier && outlier < alike && alike < peer, "%d columns: %d %d %d %d", size[0], slow, outlier, alike, peer)
		// What the time covers is one line, and it is never left out.
		require.Equal(t, 1, strings.Count(frame, probeMeasuredLabel))
		require.Regexp(t, `(?m)^  MEASURED  API server → Pod, not ingress or Service`, frame)
		// Two lines of keys: the likely ones in words, then the rest.
		require.Regexp(t, `(?m)^  next  Enter open hx4mt`, frame)
		require.Regexp(t, `(?m)^  ↑↓ pod · a every time and status code .*Esc back$`, frame)
		lower := strings.ToLower(flat)
		require.NotContains(t, lower, "healthy")
		require.NotContains(t, lower, "because")
		// The table and its pane are under a, not on this page.
		for _, gone := range []string{"FASTEST", "SLOWEST", "CODES", "VS PEERS", "Result", "nothing is sent unless you press r", "which is the floor"} {
			require.NotContains(t, frame, gone)
		}
	}
	wide := drawnText(t, v, 132, 33)
	require.Empty(t, v.dropped, "132x36 gives nothing up")
	for _, shown := range []string{"sent 14:02 · 15 of 15 requests", "study-zone-c", ": a slow Pod, not a slow path · full bar █ = ",
		"  next  Enter open hx4mt    m graph its CPU    r run again    W slow requests by hop\n",
		"  ↑↓ pod · a every time and status code · +/_ ×5 per pod · u liveness probe · t timeline · m graph · D Grafana · Esc back\n"} {
		require.Contains(t, wide, shown)
	}
	require.Regexp(t, `(?m)^  SLOWER THAN ITS PEERS +median$`, wide)
	require.Regexp(t, `hx4mt  worker-f study-zone-c  (\S ){4}\S +2\.73s +peers [\d.]+ms +OUTLIER`, wide, "one cell per request, with a blank between two")
	require.Contains(t, wide, "×", "a request without an answer is a cross, not a bar")
	require.Regexp(t, `(?m)^  MEASURED  .* · full bar █ = \d+s · floor [\d.]+ms \(fastest Pod\)$`, wide, "the floor says whose time it is")

	// 80x24 keeps every row, every strip and what the median is held
	// against. At 60 columns that comparison goes first.
	require.Regexp(t, `hx4mt  worker-f study-zone-c  (\S ){4}\S +2\.73s +peers [\d.]+ms +OUTLIER`, drawnText(t, v, 80, 21))
	require.Empty(t, v.dropped)
	narrow := drawnText(t, v, 60, 21)
	require.Equal(t, []string{"what each number is compared with"}, v.dropped)
	require.NotContains(t, narrow, "peers ")
	require.Regexp(t, `hx4mt  worker-f \S+…  (\S ){4}\S +2\.73s +OUTLIER`, narrow, "where it runs shortens before the strip does")

	// A second run keeps the first as a dimmer strip on the same row, named
	// once over the rows.
	probeFinish(t, v, story)
	frame := drawnText(t, v, 132, 33)
	require.Equal(t, 1, strings.Count(frame, "previous 14:02:11Z"))
	require.Regexp(t, `(?m)^ +this run +previous 14:02:11Z$`, frame)
	require.Regexp(t, `(?m)^> hx4mt  worker-f study-zone-c  (\S ){4}\S +(\S ){4}\S +2\.73s`, frame)
	require.Equal(t, 1, strings.Count(frame, "hx4mt  worker-f"), "the run before is not a second row")
	require.Empty(t, v.dropped)
	// At 80 columns the two strips stand side by side without blanks, and
	// the text gives way for them.
	frame = drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^ +this run +previous 14:02:11Z$`, frame)
	require.Regexp(t, `(?m)^> hx4mt  worker-f study-zone-c  \S{5} +\S{5} +2\.73s +OUTLIER$`, frame)
	require.Equal(t, []string{"what each number is compared with"}, v.dropped)
	// At 60 the run before is the first evidence to go, and the page says so.
	frame = drawnText(t, v, 60, 21)
	require.NotContains(t, frame, "this run")
	require.Contains(t, frame, "  not shown at this size (a shows all): previous 14:02:11Z\n")
	require.Equal(t, []string{"previous 14:02:11Z", "what each number is compared with"}, v.dropped)

	// The cursor follows the order on screen, and the keys name the Pod
	// they act on.
	probePress(t, v, tcell.KeyDown)
	require.Equal(t, probePodA, v.selected)
	frame = drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^> 2bq7k `, frame)
	require.Contains(t, frame, "  next  Enter open 2bq7k")
	probePress(t, v, tcell.KeyDown)
	probePress(t, v, tcell.KeyDown)
	require.Equal(t, probePodB, v.selected, "the cursor stops at the last row")
	probePress(t, v, tcell.KeyHome)
	require.Equal(t, probePodC, v.selected, "the first row is the Pod that stands out")
	target := v.SelectedResource()
	require.Equal(t, client.PodGVR, target.GVR)
	require.Equal(t, "gitlab/"+probePodC, target.Path())
	require.Equal(t, "gitlab/gitlab-webservice-default", v.scopeTarget().Path())
}

func TestProbeCalmNoIconsUsesASCIIOnly(t *testing.T) {
	v, story := probeViewFixture(t)
	v.app.Config.K9s.UI.NoIcons = true
	v.acceptPlan(probeTestPlan(), nil)
	for _, size := range [][2]int{{132, 33}, {80, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, glyph := range []string{"…", "·", "×", "↑", "↓"} {
			require.NotContains(t, frame, glyph)
		}
	}
	probeFinish(t, v, story)
	probeFinish(t, v, story)
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, glyph := range []string{"▁", "▂", "▇", "█", "×", "≥", "…", "·", "→", "↑", "↓", "▼"} {
			require.NotContains(t, frame, glyph, "%d columns", size[0])
		}
		require.Regexp(t, `(?m)^> hx4mt +worker-f .* 2\.73s .*OUTLIER$`, frame)
		require.Contains(t, frame, "Only hx4mt answered slowly")
		require.Contains(t, frame, "MEASURED  API server -> Pod, not ingress or Service")
		require.Regexp(t, `(?m)^  Up/Down pod \| a every time and status code`, frame)
	}
	frame := drawnText(t, v, 132, 33)
	require.Regexp(t, `(?m)^> hx4mt  worker-f study-zone-c  ([_.:=+*#x-] ){4}[_.:=+*#x-] +([_.:=+*#x-] ){4}[_.:=+*#x-] +2\.73s`, frame)
	require.Contains(t, frame, "full bar # = ")
}

// While a probe sends, the page says that nothing on it is final and the
// rows stay in the order of the plan. A request in flight at Esc did
// leave: it is counted as sent and the sentence under the first says so.
func TestProbeCalmWhileSendingAndAfterCancel(t *testing.T) {
	v, _ := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)
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
	frame := drawnText(t, v, 132, 33)
	lines := strings.Split(frame, "\n")
	require.Regexp(t, `^Probe .* sending… 11 of 15 done$`, lines[0], "a request in flight is not done")
	require.Equal(t, "  Still sending: 11 of 15 requests have an outcome. What follows is not final.", lines[2])
	require.Less(t, strings.Index(frame, probeRunningLabel), strings.Index(frame, "2bq7k"))
	require.Less(t, strings.Index(frame, "2bq7k"), strings.Index(frame, "8wzpd"))
	require.Less(t, strings.Index(frame, "8wzpd"), strings.Index(frame, "hx4mt  worker-f"), "no row moves while answers arrive")
	require.Regexp(t, `(?m)^> hx4mt  worker-f study-zone-c  \S ~ · · · +2\.32s .*probing$`, frame, "in flight is drawn as such")
	require.Contains(t, frame, "  next  Esc cancel; what was not sent stays unsent\n")
	require.NotContains(t, frame, "run again")

	probePress(t, v, tcell.KeyEscape)
	require.False(t, v.running)
	require.Equal(t, 12, v.run.Sent())
	frame = drawnText(t, v, 132, 33)
	lines = strings.Split(frame, "\n")
	require.Regexp(t, `^Probe .* sent \d\d:\d\d · 12 of 15 requests$`, lines[0], "what left is counted as sent")
	require.True(t, strings.HasPrefix(lines[2], "  Only hx4mt answered slowly: median 2.32s against 20ms for its 2 peers."), lines[2])
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "Canceled: 12 of 15 requests were sent, 1 of them still in flight and abandoned "+
		"without an answer (the application may still handle it); the rest were not.")
	require.Regexp(t, `(?m)^> hx4mt  worker-f study-zone-c  \S \? · · · `, frame, "abandoned, not unsent")
	require.Contains(t, frame, "r run again")
}

// A request without an answer is a fact about its Pod whatever its peers
// did: with two Pods it still has its own group, its count and its word.
func TestProbeCalmFlagsTimeoutsWithoutPeers(t *testing.T) {
	v, _ := probeViewFixture(t)
	plan := probe.NewPlan(v.scope, []probe.Pod{
		probeTestPod(probePodA, "worker-d", "study-zone-a", true), probeTestPod(probePodB, "worker-e", "study-zone-b", true),
	}, 2)
	plan.Access = probe.AccessAllowed
	v.acceptPlan(plan, nil)
	v.attempts = 20
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
	require.Equal(t, probePodB, v.selected)
	frame := drawnText(t, v, 132, 33)
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, "8wzpd gave no answer within 5s to 2 of 20 requests; its 18 answers took 20ms.")
	require.Contains(t, flat, "The other Pod answered all 20 requests in 20ms or less.")
	require.Less(t, strings.Index(frame, probeFailedLabel), strings.Index(frame, probeAnsweredLabel))
	row := ""
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "> 8wzpd") {
			row = line
		}
	}
	require.Regexp(t, `20ms +2 of 20 unanswered +TIMEOUTS$`, row)
	require.Equal(t, 2, strings.Count(row, "×"), "each request without an answer is a cross in its place")
	require.Regexp(t, `study-zone-b  ([▂×] ){19}[▂×] +20ms`, row, "all twenty requests are on the row")
	require.Regexp(t, `(?m)^  2bq7k  worker-d study-zone-a  (▂ ){19}▂ +20ms$`, frame)
}

func TestProbeCalmStatesWhyNothingCanBeSent(t *testing.T) {
	// No declared HTTP probe: listed with the reason, never contacted.
	v, story := probeViewFixture(t)
	plan := probe.NewPlan(v.scope, []probe.Pod{probeTestPod("gitlab-gitaly-0", "worker-d", "study-zone-a", false)}, 1)
	v.acceptPlan(plan, nil)
	frame := drawnText(t, v, 100, 24)
	lines := strings.Split(frame, "\n")
	require.Equal(t, "  Nothing can be sent.", lines[2])
	require.Contains(t, strings.Join(strings.Fields(frame), " "), "The listed Pod does not declare an httpGet readinessProbe or livenessProbe, "+
		"and this command sends only a request a Pod declares for itself. The path is never typed.")
	require.Contains(t, frame, "  NOT CONTACTED\n> gitaly-0  no declared HTTP probe\n")
	require.NotContains(t, frame, "r send", "a key that would send nothing is not offered")
	require.NotContains(t, frame, "+/_")
	require.Contains(t, frame, "  next  Enter open gitaly-0\n")
	probePress(t, v, ui.KeyR)
	require.Nil(t, v.confirm)
	require.Zero(t, story.calls.Load())

	// The access review said no: the rule is named and r sends nothing.
	denied := probeTestPlan()
	denied.Access, denied.AccessReason = probe.AccessDenied, "missing "+probe.MissingAccess("gitlab")
	v.acceptPlan(denied, nil)
	frame = drawnText(t, v, 100, 30)
	require.Equal(t, "  Not allowed.", strings.Split(frame, "\n")[2])
	flat := strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, `The API server says this user is missing RBAC verb "get" on resource "pods/proxy" (core API group) in namespace "gitlab".`)
	require.Contains(t, flat, "Nothing was sent and r will send nothing.")
	require.NotContains(t, frame, "r send")
	require.Contains(t, frame, "  NOT ASKED\n> 2bq7k  worker-d study-zone-a\n", "the Pods are listed, and not as Pods that would be asked")
	require.NotContains(t, frame, probeListLabel)
	probePress(t, v, ui.KeyR)
	require.Nil(t, v.confirm)
	require.Zero(t, story.calls.Load())

	// The review allowed it but the request itself is refused.
	v.acceptPlan(probeTestPlan(), nil)
	probeFinish(t, v, probeTransportFunc(func(*probe.Request) probe.Response {
		return probe.Response{Outcome: probe.Denied, Code: 403, Detail: "forbidden"}
	}))
	frame = drawnText(t, v, 100, 30)
	require.True(t, strings.HasPrefix(strings.Split(frame, "\n")[2], "  Not allowed: the API server refused the request."))
	flat = strings.Join(strings.Fields(frame), " ")
	require.Contains(t, flat, `This user is missing RBAC verb "get" on resource "pods/proxy"`)
	require.Contains(t, flat, "No request reached a Pod, so nothing is known about how fast any of them answers.")

	// More Pods than the limit, and a gap of the read: one line that is
	// never left out, down to 60 columns.
	many := probeTestPlan()
	many.Found, many.LeftOut, many.Notes = 19, 16, []string{"zones not read: access to nodes denied"}
	v.acceptPlan(many, nil)
	require.Contains(t, drawnText(t, v, 132, 30), "  NOT READ  16 more Pods beyond the limit of 16 are not listed and not probed · zones not read: access to nodes denied\n")
	require.Contains(t, drawnText(t, v, 60, 21), "  NOT READ  16 more Pods beyond the limit of 16 are… +1 more\n")

	// The Pods could not be read at all.
	v.plan = nil
	v.acceptPlan(nil, context.DeadlineExceeded)
	frame = drawnText(t, v, 100, 24)
	require.Equal(t, "  Nothing was read: context deadline exceeded.", strings.Split(frame, "\n")[2])
	require.Contains(t, frame, "No request was sent to any Pod.")
	require.Regexp(t, `(?m)^Probe  Service gitlab/gitlab-webservice-default +not read$`, frame)
	require.Contains(t, v.runUnavailable(), "could not be read")
}

// With more Pods than rows the last peers are left out and counted, never
// the Pod that stands out, and the rows turn to keep the cursor on screen
// as it moves through them.
func TestProbeCalmKeepsTheCursorOnScreen(t *testing.T) {
	v, _ := probeViewFixture(t)
	var pods []probe.Pod
	for i := range probe.MaxPods {
		pods = append(pods, probeTestPod(fmt.Sprintf("gitlab-webservice-default-6f9c7d5b84-p%02d", i), "worker-d", "study-zone-a", true))
	}
	plan := probe.NewPlan(v.scope, pods, len(pods))
	plan.Access = probe.AccessAllowed
	v.acceptPlan(plan, nil)
	probeFinish(t, v, probeTransportFunc(func(request *probe.Request) probe.Response {
		took := 20 * time.Millisecond
		if strings.HasSuffix(request.Pod, "-p03") {
			took = 2 * time.Second
		}
		return probe.Response{Outcome: probe.Answered, Code: 200, Took: took}
	}))
	require.True(t, strings.HasSuffix(v.selected, "-p03"))
	frame := drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^> p03 .* 2s .*OUTLIER$`, frame)
	require.Regexp(t, `(?m)^  p00 +worker-d .* 20ms$`, frame)
	require.Regexp(t, `not shown at this size \(a shows all\): \d+ of 15 rows of LIKE THEIR PEERS`, frame)
	require.NotRegexp(t, `(?m)^  p15 `, frame)
	// At 132x36 every Pod has its row.
	frame = drawnText(t, v, 132, 33)
	require.Equal(t, probe.MaxPods, strings.Count(frame, "worker-d study-zone-a"))
	require.Empty(t, v.dropped)

	probePress(t, v, tcell.KeyDown)
	require.True(t, strings.HasSuffix(v.selected, "-p00"), "after the Pod that stands out come its peers, in order")
	frame = drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^> p00 `, frame)
	probePress(t, v, tcell.KeyEnd)
	require.True(t, strings.HasSuffix(v.selected, "-p15"))
	frame = drawnText(t, v, 80, 21)
	require.Regexp(t, `(?m)^> p15 `, frame, "the rows turn to keep the cursor on screen")
	require.Contains(t, frame, "  next  Enter open p15")
	require.Regexp(t, `(?m)^> p03 |^  p03 `, frame, "the Pod that stands out keeps its row")
}

// a changes what is drawn and nothing else: it sends nothing, and the
// question before a request is the same from either layout.
func TestProbeAllDetailChangesWhatIsDrawnNeverWhatIsSent(t *testing.T) {
	v, story := probeViewFixture(t)
	v.acceptPlan(probeTestPlan(), nil)
	for range 4 {
		probePress(t, v, calmDetailKey)
	}
	require.False(t, v.allDetail)
	require.Zero(t, story.calls.Load())
	require.Nil(t, v.confirm)
	require.False(t, v.running)

	probeFinish(t, v, story)
	sent := story.calls.Load()
	probeDetail(t, v, true)
	frame := drawnText(t, v, 132, 33)
	for _, shown := range []string{"VS PEERS", "FASTEST", "CODES", "Result", "HTTP 200 ×4", "which is the floor",
		"The difference is between Pods, now. The request did not pass the ingress or the Service, so neither slowed it."} {
		// A sentence wraps where the width ends: the words are looked for
		// across the line breaks.
		require.Contains(t, strings.Join(strings.Fields(frame), " "), shown, "everything the view showed before is under a")
	}
	probeDetail(t, v, false)
	frame = drawnText(t, v, 132, 33)
	require.Contains(t, frame, probeSlowLabel)
	require.NotContains(t, frame, "VS PEERS")
	require.Equal(t, sent, story.calls.Load())

	// Another count is another request: r asks, in the same words, from
	// the calm page and from the table.
	probePress(t, v, probeMoreKey)
	var asked []string
	for _, detail := range []bool{false, true} {
		probeDetail(t, v, detail)
		probePress(t, v, ui.KeyR)
		require.NotNil(t, v.confirm, "r asks before it sends")
		require.Zero(t, v.confirm.focus, "Cancel has the focus")
		asked = append(asked, v.confirm.message)
		// a under the question changes the page, not the answer.
		probePress(t, v, calmDetailKey)
		require.NotNil(t, v.confirm)
		probePress(t, v, calmDetailKey)
		v.confirm.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), nil)
		require.Nil(t, v.confirm)
		require.False(t, v.running)
	}
	require.Contains(t, asked[0], "Send GET :8080/-/readiness to 3 pods of Service gitlab/gitlab-webservice-default, 10 times each: 30 requests in all.")
	require.Equal(t, asked[0], asked[1])
	require.Equal(t, sent, story.calls.Load())
}
