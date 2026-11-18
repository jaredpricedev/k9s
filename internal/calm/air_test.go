// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm_test

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/calm/calmtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A secondary group that asks to fold first is one line before the page
// gives any blank row up; without that it folds after the blank rows have
// gone. Nothing changes where the area has room for the rows.
func TestASecondaryGroupThatFoldsFirstKeepsThePageItsBlankRows(t *testing.T) {
	const label = "DIFFERS, BUT NOT DURING A STALL"
	lay := func(first bool, width, height int) (calm.Plan, []string) {
		page := calmtest.Slow(plainOpts)
		secondary := -1
		for i := range page.Groups {
			if page.Groups[i].Label == label {
				require.Equal(t, calm.Secondary, page.Groups[i].Role)
				page.Groups[i].FoldFirst, secondary = first, i
			}
		}
		require.GreaterOrEqual(t, secondary, 0)
		plan := calm.Lay(&page, width, height, plainOpts)
		lines := calm.Plain(calm.Render(&page, &plan, plainOpts).Lines())
		return plan, lines
	}
	blanks := func(lines []string) int {
		n := 0
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				n++
			}
		}
		return n
	}

	// Room for everything: the rows stay rows.
	plan, _ := lay(true, 132, 30)
	assert.Empty(t, plan.Dropped)

	// One row short. The page gives its blank rows up first; a group that
	// folds first gives its rows up for them.
	plan, usual := lay(false, 132, 27)
	assert.Equal(t, []string{"blank rows around the verdict and the keys"}, plan.Dropped)
	plan, first := lay(true, 132, 27)
	assert.Equal(t, []string{label + " folded to one line"}, plan.Dropped)
	assert.Greater(t, blanks(first), blanks(usual), "the page keeps the blank rows the fold paid for")
	require.LessOrEqual(t, len(first), 27)
	folded := false
	for _, line := range first {
		folded = folded || strings.HasPrefix(line, "  "+label+"  ")
	}
	assert.True(t, folded, "the group is one line that still names what its rows did")

	// Shorter still: the rest of the order is the usual one, and the page
	// still has the blank rows before its groups, which it gives up for
	// the rows of a group that does not fold first.
	plan, _ = lay(false, 132, 21)
	assert.Equal(t, []string{"blank rows around the verdict and the keys", "QUIET folded to one line", "blank rows between subjects",
		"blank rows before groups"}, plan.Dropped)
	plan, _ = lay(true, 132, 21)
	assert.Equal(t, []string{label + " folded to one line", "blank rows around the verdict and the keys", "QUIET folded to one line"}, plan.Dropped)
	assert.False(t, plan.TooSmall)

	// A page that is only too narrow does not fold for it: a fold that
	// does not widen the strips is taken back, as every other is.
	plan, _ = lay(true, 36, 40)
	assert.NotContains(t, plan.Dropped, label+" folded to one line")
}

// A page that sets its answer apart gives the blank row over its keys up
// before the one under the verdict: with one row to spare the answer and
// the evidence are still apart. Without it the two rows go together.
func TestAPageThatSetsItsAnswerApartSpendsItsLastRowUnderTheVerdict(t *testing.T) {
	lay := func(apart bool, height int) (calm.Plan, []string) {
		page := calmtest.Slow(plainOpts)
		page.AnswerApart = apart
		plan := calm.Lay(&page, 80, height, plainOpts)
		lines := calm.Plain(calm.Render(&page, &plan, plainOpts).Lines())
		return plan, lines
	}
	// under is whether a blank row follows the answer; over, whether one
	// stands before the next line.
	rows := func(lines []string) (under, over bool) {
		for i, line := range lines {
			if strings.HasPrefix(line, "  next ") {
				over = i > 0 && lines[i-1] == ""
			}
			if strings.Contains(line, "not proof of cause") {
				under = i+1 < len(lines) && lines[i+1] == ""
			}
		}
		return under, over
	}
	const both, keys, verdict = "the blank rows under the verdict and over the keys", "the blank row over the keys", "the blank row under the verdict"

	// Room for both rows: nothing changes.
	for _, apart := range []bool{false, true} {
		plan, lines := lay(apart, 19)
		under, over := rows(lines)
		assert.True(t, under && over, "apart=%v", apart)
		for _, words := range []string{both, keys, verdict} {
			assert.NotContains(t, plan.Dropped, words, "apart=%v", apart)
		}
	}

	// One row short. The two rows go together and leave a row unused; a
	// page that sets its answer apart gives only the one over its keys.
	plan, lines := lay(false, 18)
	under, over := rows(lines)
	assert.False(t, under || over)
	assert.Contains(t, plan.Dropped, both)
	assert.Len(t, lines, 17, "the row to spare is not used")
	plan, lines = lay(true, 18)
	under, over = rows(lines)
	assert.True(t, under, "the answer is still apart from the evidence")
	assert.False(t, over)
	assert.Equal(t, keys, plan.Dropped[len(plan.Dropped)-1])
	assert.NotContains(t, plan.Dropped, both)
	assert.Len(t, lines, 18)

	// With no row to spare it gives that one up too, and says so in the
	// order it gave them up.
	plan, lines = lay(true, 17)
	under, over = rows(lines)
	assert.False(t, under || over)
	assert.Equal(t, []string{keys, verdict}, plan.Dropped[len(plan.Dropped)-2:])
	assert.False(t, plan.TooSmall)
	usual, same := lay(false, 17)
	assert.Equal(t, same, lines, "from there on the two pages are the same")
	assert.Len(t, usual.Dropped, len(plan.Dropped)-1)
}
