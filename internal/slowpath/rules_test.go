// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
	"testing"

	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/stretchr/testify/assert"
)

func TestRulesCompareASampleWithItsOwnUsualLevel(t *testing.T) {
	signal := func(name, unit, worse string) *observability.Signal {
		return &observability.Signal{Name: name, Unit: unit, Worse: worse}
	}
	for _, c := range []struct {
		name          string
		signal        *observability.Signal
		value, median float64
		want          bool
	}{
		{"latency near its usual level", signal("p99", observability.UnitSeconds, observability.WorseHigh), 0.36, 0.35, false},
		{"latency at three times its usual level", signal("p99", observability.UnitSeconds, observability.WorseHigh), 1.2, 0.35, true},
		{"fast hop, ten times slower but under 50ms more", signal("p99", observability.UnitSeconds, observability.WorseHigh), 0.03, 0.003, false},
		{"fast hop, 54ms more", signal("p99", observability.UnitSeconds, observability.WorseHigh), 0.06, 0.006, true},
		{"latency that is always above five seconds", signal("p99", observability.UnitSeconds, observability.WorseHigh), 6, 6, true},
		{"error share under one point", signal("5xx", observability.UnitRatio, observability.WorseHigh), 0.008, 0, false},
		{"error share of two points from nothing", signal("5xx", observability.UnitRatio, observability.WorseHigh), 0.02, 0, true},
		{"error share that is always six percent", signal("5xx", observability.UnitRatio, observability.WorseHigh), 0.06, 0.06, true},
		{"throttling a fifth of periods", signal("throttled", observability.UnitRatio, observability.WorseHigh), 0.2, 0.01, false},
		{"throttling 30% from 4%", signal("throttled", observability.UnitRatio, observability.WorseHigh), 0.3, 0.04, true},
		{"throttling that is always over half", signal("throttled", observability.UnitRatio, observability.WorseHigh), 0.55, 0.55, true},
		{"memory at 80% of the limit", signal(slowpath.SignalMemory, observability.UnitRatio, observability.WorseHigh), 0.8, 0.2, false},
		{"memory at 95% of the limit", signal(slowpath.SignalMemory, observability.UnitRatio, observability.WorseHigh), 0.95, 0.95, true},
		{"a trickle of drops", signal("drops", observability.UnitPerSecond, observability.WorseHigh), 0.05, 0, false},
		{"a burst of drops", signal("drops", observability.UnitPerSecond, observability.WorseHigh), 0.8, 0.002, true},
		{"a busy rate that is steady", signal("499", observability.UnitPerSecond, observability.WorseHigh), 40, 39, false},
		{"half a queued connection", signal("queued", observability.UnitCount, observability.WorseHigh), 0.5, 0, false},
		{"two queued connections from none", signal("queued", observability.UnitCount, observability.WorseHigh), 2, 0, true},
		{"no free thread", signal("free-threads", observability.UnitCount, observability.WorseLow), 0, 5, true},
		{"one free thread of a usual five", signal("free-threads", observability.UnitCount, observability.WorseLow), 1, 5, true},
		{"two free threads of a usual five", signal("free-threads", observability.UnitCount, observability.WorseLow), 2, 5, false},
		{"an exporter that is down the whole window", signal("up", observability.UnitCount, observability.WorseLow), 0, 0, true},
		{"an exporter that is up", signal("up", observability.UnitCount, observability.WorseLow), 1, 1, false},
		{"a context signal, however large", signal("requests", observability.UnitPerSecond, observability.WorseNone), 9000, 1, false},
		{"a zero that is three times a zero", signal("p99", observability.UnitSeconds, observability.WorseHigh), 0, 0, false},
	} {
		assert.Equal(t, c.want, slowpath.RuleFor(c.signal).Crosses(c.value, c.median), c.name)
	}
	// A declared signal without a direction is bad when high, as profiles say.
	assert.True(t, slowpath.RuleFor(&observability.Signal{Name: "lag", Unit: observability.UnitSeconds}).Crosses(9, 0.1))
}

func TestEveryRuleIsStatedInWords(t *testing.T) {
	text := func(name, unit, worse string) string {
		return slowpath.RuleFor(&observability.Signal{Name: name, Unit: unit, Worse: worse}).Text()
	}
	assert.Equal(t, "raised at 3x its window median and at least 50ms above it; always raised at 5s", text("p99", observability.UnitSeconds, observability.WorseHigh))
	assert.Equal(t, "raised at 3x its window median and at least 1% above it; always raised at 5%", text("5xx", observability.UnitRatio, observability.WorseHigh))
	assert.Equal(t, "raised at 3x its window median and at least 25% above it; always raised at 50%", text("throttled", observability.UnitRatio, observability.WorseHigh))
	assert.Equal(t, "raised at 3x its window median and at least 0.1/s above it", text("drops", observability.UnitPerSecond, observability.WorseHigh))
	assert.Equal(t, "raised at 1/3 of its window median or less and at least 1 below it; always raised at 0", text("free-threads", observability.UnitCount, observability.WorseLow))
	assert.Equal(t, "always raised at 90%", text(slowpath.SignalMemory, observability.UnitRatio, observability.WorseHigh))
	assert.Equal(t, "raised where the counter grew between two samples", text(slowpath.SignalRestarts, observability.UnitCount, observability.WorseHigh))
	assert.Equal(t, "shown for context; no rule (neither direction is bad)", text("requests", observability.UnitPerSecond, observability.WorseNone))

	assert.Equal(t, "client give-ups (HTTP 499)", slowpath.Phrase("499"))
	assert.Equal(t, "CPU throttling", slowpath.Phrase("throttled"))
	assert.Equal(t, "lag raised", slowpath.Phrase("lag"), "a declared signal is named by its own name")
	for _, state := range []slowpath.State{slowpath.Quiet, slowpath.Elevated, slowpath.Outlier, slowpath.NotRead} {
		assert.NotContains(t, string(state), "health", "no state claims health")
	}
}
