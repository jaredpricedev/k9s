// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/observability"
	"github.com/stretchr/testify/require"
)

// answered is the answer of a read of the story, stamped as the tests of
// the summary stamp theirs.
func answered(t *testing.T, source *fakeSource) (*Report, Answer) {
	t.Helper()
	report := Collect(t.Context(), source, checkoutRequest(t))
	report.ReadAt = rolloutAt.Add(12 * time.Minute)
	answer := report.Answer()
	return report, answer
}

// The answer says what a person would: how many signals got worse after
// the rollout and of which kinds, then what the revision changed. The
// values are on the rows of the page; the sentences repeat none of them.
func TestAnswerNamesWhatGotWorseAndRepeatsNoValueOfTheRows(t *testing.T) {
	report, answer := answered(t, checkoutStory())
	require.Equal(t, "7 signals got worse after rev 2 rolled out at 04:20: errors, latency, restarts, memory and ready replicas.", answer.Lead,
		"the three latency percentiles are one kind")
	require.Equal(t, "7 signals got worse after rev 2 rolled out at 04:20.", answer.Brief)
	require.Equal(t, "Rev 2 changed the image, the memory limit (512Mi -> 256Mi) and 2 env vars.", answer.Changed)
	require.Equal(t, "Rev 2 changed the image, the memory limit and 2 env vars.", answer.ChangedBrief)

	// No before or after value of a signal is in the lead: the only
	// numbers are the count, the revision and the time.
	value := regexp.MustCompile(`\d(%|ms|s|Mi|m|/s)\b`)
	for _, sentence := range []string{answer.Lead, answer.Brief} {
		require.NotRegexp(t, value, sentence)
		for index := range report.Outcomes {
			outcome := &report.Outcomes[index]
			if outcome.State == Compared && outcome.Signal.Unit != UnitCount {
				require.NotContains(t, sentence, outcome.Signal.Format(outcome.After.Value), outcome.Signal.Label)
			}
		}
	}
	// It states an order in time and names no cause, and no value of an
	// environment variable.
	for _, sentence := range []string{answer.Lead, answer.Brief, answer.Changed, answer.ChangedBrief} {
		lower := strings.ToLower(sentence)
		for _, banned := range []string{"because", "caused", "due to", "led to", "synthetic", "token"} {
			require.NotContains(t, lower, banned, sentence)
		}
	}

	// Read on another day, the rollout carries its date.
	later := *report
	later.ReadAt = rolloutAt.Add(26 * time.Hour)
	require.Equal(t, "7 signals got worse after rev 2 rolled out on Nov17 at 04:20.", later.Answer().Brief)

	// The sentence adds no rule of its own: it counts the verdicts.
	worse, _, _, _ := report.Counts()
	require.Equal(t, 7, worse)
}

// One kind is said by its name, with its signals where there are several.
func TestAnswerSaysOneKindByItsName(t *testing.T) {
	slow := checkoutStory()
	slow.answers = map[string]func(time.Duration) float64{
		"http_requests_total": slow.answers["http_requests_total"],
		`code=~"5.."`:         stepped(0.08, 0.08),
	}
	_, answer := answered(t, slow)
	require.Equal(t, "Latency got worse after rev 2 rolled out at 04:20: p50, p95 and p99.", answer.Lead)

	report := &Report{Rollout: checkoutRequest(t).Rollout, Windows: checkoutRequest(t).Windows, ReadAt: rolloutAt.Add(time.Hour)}
	signal := func(id, verdict string) Outcome {
		for index := range Signals {
			if Signals[index].ID == id {
				return Outcome{Signal: Signals[index], State: Compared, Verdict: verdict}
			}
		}
		t.Fatalf("no signal %s", id)
		return Outcome{}
	}
	report.Outcomes = []Outcome{signal(SigRequests, Unchanged), signal(SigP50, Worse), signal(SigP95, Worse), signal(SigP99, Worse), signal(SigCPU, Higher)}
	answer = report.Answer()
	require.Equal(t, "Latency got worse after rev 2 rolled out at 04:20: p50, p95 and p99.", answer.Lead)
	require.Equal(t, "Latency got worse after rev 2 rolled out at 04:20.", answer.Brief)

	report.Outcomes = []Outcome{signal(SigThrottled, Worse), signal(SigErrors, Better)}
	answer = report.Answer()
	require.Equal(t, "CPU throttling got worse after rev 2 rolled out at 04:20.", answer.Lead)
	require.Equal(t, answer.Lead, answer.Brief)

	report.Outcomes = []Outcome{signal(SigThrottled, Worse), signal(SigReady, Worse)}
	require.Equal(t, "2 signals got worse after rev 2 rolled out at 04:20: CPU throttling and ready replicas.", report.Answer().Lead)
}

// Nothing worse is not health, missing data is not a zero, and a read
// that did not happen says why: each has its own sentence, and the short
// form never drops what was not compared.
func TestAnswerIsHonestAboutNothingWorseMissingDataAndUnreadSources(t *testing.T) {
	healthy := &fakeSource{answers: map[string]func(time.Duration) float64{"http_requests_total": stepped(40, 41), `code=~"5.."`: stepped(4, 0.041)}}
	_, answer := answered(t, healthy)
	require.Equal(t, "No compared signal got worse after rev 2 rolled out at 04:20: 1 better, 1 unchanged, 9 not compared.", answer.Lead)
	require.Equal(t, "No compared signal got worse after rev 2 rolled out at 04:20; 9 not compared.", answer.Brief)
	require.NotContains(t, strings.ToLower(answer.Lead+answer.Brief), "health")

	// Only higher or lower: neutral signals moved, none in a direction
	// that has a worse side.
	neutral := &fakeSource{answers: map[string]func(time.Duration) float64{
		"http_requests_total":               stepped(40, 41),
		`code=~"5.."`:                       stepped(0.08, 0.08),
		"container_cpu_usage_seconds_total": stepped(0.06, 0.34),
	}}
	_, answer = answered(t, neutral)
	require.Equal(t, "No compared signal got worse after rev 2 rolled out at 04:20: 1 higher or lower, 2 unchanged, 8 not compared.", answer.Lead)

	// Every signal compared: the short form has nothing to keep.
	whole := &Report{Rollout: checkoutRequest(t).Rollout, Windows: checkoutRequest(t).Windows, ReadAt: rolloutAt.Add(time.Hour)}
	for index := range Signals {
		whole.Outcomes = append(whole.Outcomes, Outcome{Signal: Signals[index], State: Compared, Verdict: Unchanged})
	}
	answer = whole.Answer()
	require.Equal(t, "No compared signal got worse after rev 2 rolled out at 04:20: 11 unchanged.", answer.Lead)
	require.Equal(t, "No compared signal got worse after rev 2 rolled out at 04:20.", answer.Brief)

	// Nothing matched: no signal has a value on either side.
	_, answer = answered(t, &fakeSource{})
	require.Equal(t, "No signal could be compared around rev 2, rolled out at 04:20: 11 not available.", answer.Lead)
	require.Equal(t, "No signal could be compared around rev 2, rolled out at 04:20.", answer.Brief)

	// Series that begin after the rollout: not enough data before it.
	fresh := checkoutStory()
	for fragment, fn := range fresh.answers {
		fresh.answers[fragment] = func(offset time.Duration) float64 {
			if offset < 0 {
				return math.NaN()
			}
			return fn(offset)
		}
	}
	for le, fn := range fresh.buckets {
		fresh.buckets[le] = func(offset time.Duration) float64 {
			if offset < 0 {
				return math.NaN()
			}
			return fn(offset)
		}
	}
	_, answer = answered(t, fresh)
	require.Regexp(t, `^No signal could be compared around rev 2, rolled out at 04:20: (\d+ not available, )?\d+ with too few samples before or after\.$`, answer.Lead)

	// The source refused: the reason is the answer in both forms.
	denied := &fakeSource{status: observability.Status{State: observability.StateDenied, HTTPStatus: 403}}
	_, answer = answered(t, denied)
	require.Equal(t, "Nothing was read around rev 2, rolled out at 04:20: denied (HTTP 403).", answer.Lead)
	require.Equal(t, answer.Lead, answer.Brief)
	require.Equal(t, "Rev 2 changed the image, the memory limit (512Mi -> 256Mi) and 2 env vars.", answer.Changed,
		"what the revision changed needs no metrics source")

	unread := Unread(checkoutRequest(t), "no Prometheus source is declared for this context; :sources shows how to declare one")
	unread.ReadAt = rolloutAt.Add(12 * time.Minute)
	require.Equal(t, "Nothing was read around rev 2, rolled out at 04:20: no Prometheus source is declared for this context; "+
		":sources shows how to declare one.", unread.Answer().Lead)
}

// A request rate that doubled or halved limits what the comparison says.
// The sentence is the one the summary has; its short form keeps the size
// of the change.
func TestAnswerSaysWhenTrafficMakesTheComparisonUnlike(t *testing.T) {
	_, answer := answered(t, checkoutStory())
	require.Empty(t, answer.Traffic)
	require.Empty(t, answer.TrafficBrief)
	halved := checkoutStory()
	halved.answers["http_requests_total"] = stepped(40, 12)
	report, answer := answered(t, halved)
	require.Equal(t, "Request rate 40/s -> 12/s (-70%): error ratio and latency are not like-for-like.", answer.Traffic)
	require.Equal(t, report.Traffic, answer.Traffic)
	require.Equal(t, "Request rate -70%: errors and latency are not like-for-like.", answer.TrafficBrief)
}

// What the revision changed is counted by kind from the list of changes,
// and an entry of a kind the sentence does not know is still counted.
func TestAnswerCountsWhatTheRevisionChangedByKind(t *testing.T) {
	report := &Report{Rollout: checkoutRequest(t).Rollout}
	for _, c := range []struct {
		changes       []string
		limit         string
		changed, some string
	}{
		{changes: []string{"image v1 -> v2"}, changed: "Rev 2 changed the image."},
		{changes: []string{"env +A ~B -C"}, changed: "Rev 2 changed 3 env vars."},
		{changes: []string{"env +A envFrom sources changed"}, changed: "Rev 2 changed 1 env var and the envFrom sources."},
		{changes: []string{"limits.memory 512Mi -> 256Mi"}, limit: "512Mi -> 256Mi",
			changed: "Rev 2 changed the memory limit (512Mi -> 256Mi).", some: "Rev 2 changed the memory limit."},
		{changes: []string{"limits.cpu 1 -> 500m", "command/args changed (not shown)"}, changed: "Rev 2 changed the CPU limit and the command."},
		{changes: []string{"requests.cpu 100m -> 200m", "requests.memory 64Mi -> 128Mi", "limits.memory unset -> 256Mi"}, limit: "unset -> 256Mi",
			changed: "Rev 2 changed the memory limit (unset -> 256Mi) and 2 other resource settings.",
			some:    "Rev 2 changed the memory limit and 2 other resource settings."},
		{changes: []string{"requests.ephemeral-storage 1Gi -> 2Gi"}, changed: "Rev 2 changed 1 resource setting."},
		// Several containers: every entry is led by the name of its own.
		{changes: []string{"app image v1 -> v2", "proxy image 1.2 -> 1.3", "app limits.memory 512Mi -> 256Mi", "proxy limits.memory 64Mi -> 32Mi",
			"app env +A", "container init added (image busybox:1)", "container old removed"}, limit: "app 512Mi -> 256Mi, proxy 64Mi -> 32Mi",
			changed: "Rev 2 changed 2 images, 2 memory limits, 1 env var and the containers (1 added, 1 removed)."},
		// A container named like a kind of change does not hide the change.
		{changes: []string{"env image v1 -> v2", "image env +A"}, changed: "Rev 2 changed the image and 1 env var."},
		{changes: []string{"image env -> v2"}, changed: "Rev 2 changed the image."},
		{changes: []string{"image v1 -> v2", "priorityClassName low -> high"}, changed: "Rev 2 changed the image and 1 other setting."},
	} {
		report.Changes, report.MemoryLimit = c.changes, c.limit
		answer := report.Answer()
		require.Equal(t, c.changed, answer.Changed, "%v", c.changes)
		if c.some == "" {
			c.some = c.changed
		}
		require.Equal(t, c.some, answer.ChangedBrief, "%v", c.changes)
	}
	report.Changes, report.MemoryLimit = nil, ""
	require.Equal(t, "Rev 2 changed nothing in images, env keys, resources or commands.", report.Answer().Changed)
	report.Rollout.Previous = nil
	require.Equal(t, "Rev 2 is the earliest retained revision; there is no earlier one to diff.", report.Answer().Changed)
}
